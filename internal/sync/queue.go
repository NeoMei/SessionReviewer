package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

const maxQueueRecordBytes = 64 << 10

type QueueState string

const (
	QueuePending QueueState = "pending"
	QueueBlocked QueueState = "blocked"
)

type QueueItem struct {
	Version          int        `json:"version"`
	ID               string     `json:"id"`
	EntityID         string     `json:"entity_id"`
	Target           Side       `json:"target"`
	ExpectedBaseHash string     `json:"expected_base_hash"`
	Attempts         int        `json:"attempts"`
	NotBefore        time.Time  `json:"not_before"`
	State            QueueState `json:"state"`
	LastErrorClass   string     `json:"last_error_class"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type Queue struct {
	Root  *os.Root
	Retry RetryPolicy
	Now   func() time.Time
}

type loadedQueueItem struct {
	item    QueueItem
	info    os.FileInfo
	encoded []byte
}

// Queue mutations are CAS-checked immediately before publication. Callers
// serialize multi-operation reconciliation by holding the project sync lock.
func (q Queue) Enqueue(item QueueItem) (QueueItem, error) {
	now, err := q.validate()
	if err != nil {
		return QueueItem{}, err
	}
	if item.ID == "" {
		item.ID = queueID(item.EntityID, item.Target, item.ExpectedBaseHash)
	}
	if item.State == "" {
		item.State = QueuePending
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = now
	}
	if item.NotBefore.IsZero() {
		item.NotBefore = now
	}
	item.NotBefore = item.NotBefore.UTC()
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	if err := validateQueueItem(item); err != nil {
		return QueueItem{}, err
	}
	records, err := loadQueueItems(q.Root)
	if err != nil {
		return QueueItem{}, err
	}
	if current, found := records[item.ID]; found {
		return current.item, nil
	}
	if _, err := q.Root.Lstat(queueRecordPath(item.ID)); !errors.Is(err, os.ErrNotExist) {
		return QueueItem{}, errors.New("queue state changed before enqueue")
	}
	if err := writeQueueItem(q.Root, item); err != nil {
		return QueueItem{}, err
	}
	return item, nil
}

func (q Queue) Ready(now time.Time, limit int) ([]QueueItem, error) {
	if _, err := q.validate(); err != nil {
		return nil, err
	}
	if now.IsZero() || now.Location() != time.UTC || limit < 0 {
		return nil, errors.New("invalid queue readiness request")
	}
	records, err := loadQueueItems(q.Root)
	if err != nil {
		return nil, err
	}
	ready := make([]QueueItem, 0, len(records))
	for _, record := range records {
		if record.item.State == QueueBlocked || (record.item.State == QueuePending && !record.item.NotBefore.After(now)) {
			ready = append(ready, record.item)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		if !ready[i].NotBefore.Equal(ready[j].NotBefore) {
			return ready[i].NotBefore.Before(ready[j].NotBefore)
		}
		if !ready[i].CreatedAt.Equal(ready[j].CreatedAt) {
			return ready[i].CreatedAt.Before(ready[j].CreatedAt)
		}
		return ready[i].ID < ready[j].ID
	})
	if limit > 0 && len(ready) > limit {
		ready = ready[:limit]
	}
	return ready, nil
}

func (q Queue) Ack(id string) error {
	if _, err := q.validate(); err != nil {
		return err
	}
	if !queueIDPattern(id) {
		return errors.New("invalid queue item ID")
	}
	records, err := loadQueueItems(q.Root)
	if err != nil {
		return err
	}
	current, found := records[id]
	if !found {
		return nil
	}
	if err := verifyQueuePreimage(q.Root, id, current); err != nil {
		return err
	}
	if err := atomicfile.RemoveRoot(q.Root, queueRecordPath(id)); err != nil {
		return errors.New("cannot remove queue state")
	}
	if _, err := q.Root.Lstat(queueRecordPath(id)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("queue state remains after acknowledgement")
	}
	return nil
}

func (q Queue) Reschedule(id, errorClass string) (QueueItem, error) {
	now, err := q.validate()
	if err != nil {
		return QueueItem{}, err
	}
	if !queueIDPattern(id) || !queueErrorClass(errorClass) {
		return QueueItem{}, errors.New("invalid queue reschedule request")
	}
	records, err := loadQueueItems(q.Root)
	if err != nil {
		return QueueItem{}, err
	}
	current, found := records[id]
	if !found {
		return QueueItem{}, errors.New("queue item does not exist")
	}
	if current.item.State == QueueBlocked {
		return QueueItem{}, errors.New("queue item is blocked")
	}
	if err := verifyQueuePreimage(q.Root, id, current); err != nil {
		return QueueItem{}, err
	}
	delay := q.Retry.Initial
	for attempt := 0; attempt < current.item.Attempts; attempt++ {
		delay = nextRetryDelay(delay, q.Retry.Max)
	}
	next := current.item
	next.Attempts++
	next.NotBefore = now.Add(delay)
	next.UpdatedAt = now
	next.LastErrorClass = errorClass
	if next.Attempts >= q.Retry.QueueAttempts {
		next.State = QueueBlocked
	}
	if err := validateQueueItem(next); err != nil {
		return QueueItem{}, err
	}
	if err := writeQueueItem(q.Root, next); err != nil {
		return QueueItem{}, err
	}
	return next, nil
}

func (q Queue) validate() (time.Time, error) {
	if q.Root == nil || q.Now == nil {
		return time.Time{}, errors.New("queue root and clock are required")
	}
	if err := validateRetryPolicy(q.Retry); err != nil {
		return time.Time{}, errors.New("invalid queue retry policy")
	}
	info, err := q.Root.Stat(".")
	if err != nil || info == nil || !info.IsDir() || isStateRedirect(info) {
		return time.Time{}, errors.New("queue root is redirected, invalid, or not private")
	}
	if !privateStateMode(info, 0o700) {
		file, openErr := q.Root.Open(".")
		if openErr != nil {
			return time.Time{}, errors.New("cannot protect queue root")
		}
		protectErr := file.Chmod(0o700)
		closeErr := file.Close()
		protected, statErr := q.Root.Stat(".")
		if protectErr != nil || closeErr != nil || statErr != nil || !os.SameFile(info, protected) || !privateStateMode(protected, 0o700) {
			return time.Time{}, errors.New("cannot protect queue root")
		}
	}
	now := q.Now()
	if now.IsZero() {
		return time.Time{}, errors.New("queue clock returned zero time")
	}
	return now.UTC(), nil
}

func queueID(entityID string, target Side, expectedBaseHash string) string {
	digest := sha256.Sum256([]byte(entityID + "|" + string(target) + "|" + expectedBaseHash))
	return hex.EncodeToString(digest[:16])
}

func queueRecordPath(id string) string { return id + ".json" }

func queueIDPattern(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func queueErrorClass(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func validateQueueItem(item QueueItem) error {
	if item.Version != 1 || !stableBaseID.MatchString(item.EntityID) {
		return errors.New("invalid queue item identity")
	}
	if item.Target != SideProject && item.Target != SideVault {
		return errors.New("invalid queue item target")
	}
	if item.ExpectedBaseHash != "" && !lowerSHA256.MatchString(item.ExpectedBaseHash) {
		return errors.New("invalid queue item hash")
	}
	if !queueIDPattern(item.ID) || item.ID != queueID(item.EntityID, item.Target, item.ExpectedBaseHash) {
		return errors.New("queue item ID mismatch")
	}
	if item.Attempts < 0 || (item.State != QueuePending && item.State != QueueBlocked) {
		return errors.New("invalid queue item state")
	}
	if item.LastErrorClass != "" && !queueErrorClass(item.LastErrorClass) {
		return errors.New("invalid queue error class")
	}
	for _, value := range []time.Time{item.NotBefore, item.CreatedAt, item.UpdatedAt} {
		if value.IsZero() || value.Location() != time.UTC {
			return errors.New("queue times must be UTC")
		}
	}
	return nil
}

func loadQueueItems(root *os.Root) (map[string]loadedQueueItem, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, errors.New("cannot inspect queue state")
	}
	records := make(map[string]loadedQueueItem, len(entries))
	seenNames := make(map[string]string, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		folded := strings.ToLower(name)
		if previous, found := seenNames[folded]; found && previous != name {
			return nil, errors.New("queue state names collide")
		}
		seenNames[folded] = name
		if !strings.HasSuffix(name, ".json") || !queueIDPattern(strings.TrimSuffix(name, ".json")) {
			return nil, errors.New("unexpected queue state entry")
		}
		id := strings.TrimSuffix(name, ".json")
		info, err := root.Lstat(name)
		if err != nil || info == nil || !info.Mode().IsRegular() || isStateRedirect(info) || !privateStateMode(info, 0o600) {
			return nil, errors.New("queue state is redirected, invalid, or not private")
		}
		item, encoded, err := readQueueItem(root, name, info)
		if err != nil {
			return nil, err
		}
		if item.ID != id {
			return nil, errors.New("queue filename does not certify its item ID")
		}
		if _, duplicate := records[item.ID]; duplicate {
			return nil, errors.New("duplicate queue item ID")
		}
		records[item.ID] = loadedQueueItem{item: item, info: info, encoded: encoded}
	}
	return records, nil
}

func readQueueItem(root *os.Root, name string, before os.FileInfo) (QueueItem, []byte, error) {
	if before.Size() > maxQueueRecordBytes {
		return QueueItem{}, nil, errors.New("queue state exceeds size limit")
	}
	file, err := root.Open(name)
	if err != nil {
		return QueueItem{}, nil, errors.New("cannot open queue state")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameBaseFileMetadata(before, opened) {
		return QueueItem{}, nil, errors.New("queue state changed while opening")
	}
	encoded, err := readQueueSnapshot(file)
	if err != nil {
		return QueueItem{}, nil, err
	}
	middle, err := file.Stat()
	if err != nil || !sameBaseFileMetadata(opened, middle) {
		return QueueItem{}, nil, errors.New("queue state changed while reading")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return QueueItem{}, nil, errors.New("queue state cannot be reread")
	}
	second, err := readQueueSnapshot(file)
	if err != nil {
		return QueueItem{}, nil, err
	}
	afterOpen, statErr := file.Stat()
	afterName, nameErr := root.Lstat(name)
	if statErr != nil || nameErr != nil || !sameBaseFileMetadata(opened, afterOpen) || !sameBaseFileMetadata(opened, afterName) || !bytes.Equal(encoded, second) {
		return QueueItem{}, nil, errors.New("queue state changed while reading")
	}
	if err := validateFlatQueueJSON(encoded); err != nil {
		return QueueItem{}, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var item QueueItem
	if err := decoder.Decode(&item); err != nil {
		return QueueItem{}, nil, errors.New("queue state is corrupt")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return QueueItem{}, nil, errors.New("queue state is corrupt")
	}
	if err := validateQueueItem(item); err != nil {
		return QueueItem{}, nil, err
	}
	return item, encoded, nil
}

func readQueueSnapshot(file *os.File) ([]byte, error) {
	encoded, err := io.ReadAll(io.LimitReader(file, maxQueueRecordBytes+1))
	if err != nil || len(encoded) > maxQueueRecordBytes || !utf8.Valid(encoded) {
		return nil, errors.New("queue state is corrupt")
	}
	return encoded, nil
}

func validateFlatQueueJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("queue state is corrupt")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return errors.New("queue state is corrupt")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("queue state contains duplicate fields")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("queue state is corrupt")
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return errors.New("queue state is corrupt")
	}
	return nil
}

func verifyQueuePreimage(root *os.Root, id string, current loadedQueueItem) error {
	info, err := root.Lstat(queueRecordPath(id))
	if err != nil || !sameBaseFileMetadata(current.info, info) {
		return errors.New("queue state changed before mutation")
	}
	item, encoded, err := readQueueItem(root, queueRecordPath(id), info)
	if err != nil || item.ID != current.item.ID || !bytes.Equal(encoded, current.encoded) {
		return errors.New("queue state changed before mutation")
	}
	return nil
}

func writeQueueItem(root *os.Root, item QueueItem) error {
	encoded, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return errors.New("cannot encode queue state")
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxQueueRecordBytes {
		return errors.New("queue state exceeds size limit")
	}
	if err := atomicfile.WriteRoot(root, queueRecordPath(item.ID), encoded, 0o600); err != nil {
		return errors.New("cannot persist queue state")
	}
	info, err := root.Lstat(queueRecordPath(item.ID))
	if err != nil || info == nil || !info.Mode().IsRegular() || isStateRedirect(info) || !privateStateMode(info, 0o600) {
		return errors.New("queue state failed post-write verification")
	}
	written, _, err := readQueueItem(root, queueRecordPath(item.ID), info)
	if err != nil || written != item {
		return errors.New("queue state failed post-write verification")
	}
	return nil
}
