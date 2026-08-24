package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

const (
	maxQueueRecordBytes     = 64 << 10
	maxDurableQueueAttempts = 64
)

type QueueState string

var ErrQueueRecoveryRequired = errors.New("queue rollback recovery is required")

type QueueRecoveryReceipt struct {
	ItemID          string
	ExpectedOldHash string
}

type QueueRecoveryError struct {
	ItemID          string
	ExpectedOldHash string
}

func (*QueueRecoveryError) Error() string { return "queue rollback recovery is required" }

func (*QueueRecoveryError) Unwrap() error { return ErrQueueRecoveryRequired }

func (recovery *QueueRecoveryError) Receipt() QueueRecoveryReceipt {
	if recovery == nil {
		return QueueRecoveryReceipt{}
	}
	return QueueRecoveryReceipt{ItemID: recovery.ItemID, ExpectedOldHash: recovery.ExpectedOldHash}
}

const (
	QueuePending QueueState = "pending"
	QueueBlocked QueueState = "blocked"
)

type QueueErrorClass string

const (
	QueueErrorSharingViolation QueueErrorClass = "sharing_violation"
	QueueErrorLockViolation    QueueErrorClass = "lock_violation"
	QueueErrorTransientWrite   QueueErrorClass = "transient_write"
	QueueErrorVaultUnavailable QueueErrorClass = "vault_unavailable"
)

type QueueItem struct {
	Version          int             `json:"version"`
	ID               string          `json:"id"`
	EntityID         string          `json:"entity_id"`
	Target           Side            `json:"target"`
	ExpectedBaseHash string          `json:"expected_base_hash"`
	Attempts         int             `json:"attempts"`
	NotBefore        time.Time       `json:"not_before"`
	State            QueueState      `json:"state"`
	LastErrorClass   QueueErrorClass `json:"last_error_class"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type Queue struct {
	Root  *os.Root
	Retry RetryPolicy
	Now   func() time.Time

	// beforeMutation is a test-only race probe. Returning nil cannot bypass
	// the authenticated checkpoint that always follows it.
	beforeMutation func(operation string, checkpoint int, root *os.Root, leaf string) error
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
	if item.Attempts != 0 || item.State != QueuePending || item.LastErrorClass != "" {
		return QueueItem{}, errors.New("new queue item must be pending without attempts")
	}
	records, err := loadQueueItems(q.Root, q.Retry)
	if err != nil {
		return QueueItem{}, err
	}
	if current, found := records[item.ID]; found {
		return current.item, nil
	}
	if _, err := q.Root.Lstat(queueRecordPath(item.ID)); !errors.Is(err, os.ErrNotExist) {
		return QueueItem{}, errors.New("queue state changed before enqueue")
	}
	if err := writeQueueItemCAS(q.Root, item, q.Retry, nil, q.beforeMutation, "enqueue"); err != nil {
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
	records, err := loadQueueItems(q.Root, q.Retry)
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
	records, err := loadQueueItems(q.Root, q.Retry)
	if err != nil {
		return err
	}
	current, found := records[id]
	if !found {
		return nil
	}
	if err := writeQueueItemCAS(q.Root, current.item, q.Retry, &current, q.beforeMutation, "ack"); err != nil {
		return err
	}
	published, err := loadQueueItemByID(q.Root, id, q.Retry)
	if err != nil {
		return err
	}
	if q.beforeMutation != nil {
		if err := q.beforeMutation("ack", 4, q.Root, queueRecordPath(id)); err != nil {
			return errors.New("queue acknowledgement checkpoint failed")
		}
	}
	if err := verifyQueuePreimage(q.Root, id, published); err != nil {
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
	records, err := loadQueueItems(q.Root, q.Retry)
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
	delay := queueRetryDelay(q.Retry, current.item.Attempts)
	next := current.item
	next.Attempts++
	next.UpdatedAt = now
	if current.item.UpdatedAt.After(next.UpdatedAt) {
		next.UpdatedAt = current.item.UpdatedAt
	}
	next.NotBefore = next.UpdatedAt.Add(delay)
	if current.item.NotBefore.After(next.NotBefore) {
		next.NotBefore = current.item.NotBefore
	}
	next.LastErrorClass = QueueErrorClass(errorClass)
	if next.Attempts >= q.Retry.QueueAttempts {
		next.State = QueueBlocked
	}
	if err := validateQueueItem(next); err != nil {
		return QueueItem{}, err
	}
	if err := validateQueuePolicy(next, q.Retry); err != nil {
		return QueueItem{}, err
	}
	if err := writeQueueItemCAS(q.Root, next, q.Retry, &current, q.beforeMutation, "reschedule"); err != nil {
		return QueueItem{}, err
	}
	return next, nil
}

func (q Queue) Recover(receipt QueueRecoveryReceipt) (QueueItem, error) {
	if !queueIDPattern(receipt.ItemID) || !lowerSHA256.MatchString(receipt.ExpectedOldHash) {
		return QueueItem{}, errors.New("invalid queue recovery receipt")
	}
	if _, err := q.validate(); err != nil {
		return QueueItem{}, err
	}
	leaf := queueRecordPath(receipt.ItemID)
	backup := atomicfile.BackupPath(leaf)
	backupInfo, backupErr := q.Root.Lstat(backup)
	if backupErr == nil {
		if _, err := authenticateQueueRecoveryRecord(q.Root, backup, backupInfo, receipt, q.Retry); err != nil {
			return QueueItem{}, errors.New("queue recovery preflight failed")
		}
	} else if errors.Is(backupErr, os.ErrNotExist) {
		primaryInfo, err := q.Root.Lstat(leaf)
		if err != nil {
			return QueueItem{}, errors.New("queue recovery preflight failed")
		}
		recovered, err := authenticateQueueRecoveryRecord(q.Root, leaf, primaryInfo, receipt, q.Retry)
		if err != nil {
			return QueueItem{}, errors.New("queue recovery preflight failed")
		}
		if _, err := q.Root.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
			return QueueItem{}, errors.New("queue recovery preflight failed")
		}
		return recovered.item, nil
	} else {
		return QueueItem{}, errors.New("queue recovery preflight failed")
	}
	if err := atomicfile.RecoverRootFileRollback(q.Root, leaf, receipt.ExpectedOldHash); err != nil {
		return QueueItem{}, errors.New("queue recovery failed")
	}
	records, err := loadQueueItems(q.Root, q.Retry)
	if err != nil {
		return QueueItem{}, errors.New("queue recovery verification failed")
	}
	recovered, found := records[receipt.ItemID]
	if !found {
		return QueueItem{}, errors.New("queue recovery verification failed")
	}
	digest := sha256.Sum256(recovered.encoded)
	if hex.EncodeToString(digest[:]) != receipt.ExpectedOldHash {
		return QueueItem{}, errors.New("queue recovery verification failed")
	}
	if _, err := q.Root.Lstat(atomicfile.BackupPath(leaf)); !errors.Is(err, os.ErrNotExist) {
		return QueueItem{}, errors.New("queue recovery cleanup verification failed")
	}
	return recovered.item, nil
}

func authenticateQueueRecoveryRecord(root *os.Root, name string, before os.FileInfo, receipt QueueRecoveryReceipt, policy RetryPolicy) (loadedQueueItem, error) {
	if before == nil || !before.Mode().IsRegular() || isStateRedirect(before) || !privateStateMode(before, 0o600) {
		return loadedQueueItem{}, errors.New("invalid queue recovery state")
	}
	item, encoded, err := readQueueItem(root, name, before)
	if err != nil {
		return loadedQueueItem{}, errors.New("invalid queue recovery state")
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != receipt.ExpectedOldHash || item.ID != receipt.ItemID {
		return loadedQueueItem{}, errors.New("queue recovery state does not match receipt")
	}
	item = queuePolicyView(item, policy)
	if err := validateQueuePolicy(item, policy); err != nil {
		return loadedQueueItem{}, errors.New("invalid queue recovery policy state")
	}
	after, err := root.Lstat(name)
	if err != nil || !sameBaseFileMetadata(before, after) || !after.Mode().IsRegular() || isStateRedirect(after) || !privateStateMode(after, 0o600) {
		return loadedQueueItem{}, errors.New("queue recovery state changed during preflight")
	}
	return loadedQueueItem{item: item, info: after, encoded: encoded}, nil
}

func (q Queue) validate() (time.Time, error) {
	if q.Root == nil || q.Now == nil {
		return time.Time{}, errors.New("queue root and clock are required")
	}
	if err := validateQueueRetryPolicy(q.Retry); err != nil {
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
	switch QueueErrorClass(value) {
	case QueueErrorSharingViolation, QueueErrorLockViolation, QueueErrorTransientWrite, QueueErrorVaultUnavailable:
		return true
	default:
		return false
	}
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
	if item.Attempts < 0 || item.Attempts > maxDurableQueueAttempts || (item.State != QueuePending && item.State != QueueBlocked) {
		return errors.New("invalid queue item state")
	}
	if item.LastErrorClass != "" && !queueErrorClass(string(item.LastErrorClass)) {
		return errors.New("invalid queue error class")
	}
	for _, value := range []time.Time{item.NotBefore, item.CreatedAt, item.UpdatedAt} {
		if value.IsZero() || value.Location() != time.UTC {
			return errors.New("queue times must be UTC")
		}
	}
	if item.UpdatedAt.Before(item.CreatedAt) || item.NotBefore.Before(item.CreatedAt) {
		return errors.New("invalid queue time ordering")
	}
	return nil
}

func validateQueuePolicy(item QueueItem, policy RetryPolicy) error {
	if item.State == QueuePending && item.Attempts >= policy.QueueAttempts {
		return errors.New("pending queue item reached retry limit")
	}
	if item.State == QueueBlocked && item.Attempts == 0 {
		return errors.New("blocked queue item has no attempts")
	}
	if (item.Attempts == 0) != (item.LastErrorClass == "") {
		return errors.New("queue error class does not match attempts")
	}
	return nil
}

func queuePolicyView(item QueueItem, policy RetryPolicy) QueueItem {
	if item.State == QueuePending && item.Attempts >= policy.QueueAttempts {
		item.State = QueueBlocked
	}
	return item
}

func validateQueueRetryPolicy(policy RetryPolicy) error {
	if err := validateRetryPolicy(policy); err != nil || policy.QueueAttempts > maxDurableQueueAttempts {
		return errors.New("invalid bounded queue retry policy")
	}
	return nil
}

func queueRetryDelay(policy RetryPolicy, previousAttempts int) time.Duration {
	delay := policy.Initial
	for previousAttempts > 0 && delay < policy.Max {
		delay = nextRetryDelay(delay, policy.Max)
		previousAttempts--
	}
	return delay
}

func loadQueueItems(root *os.Root, policy RetryPolicy) (map[string]loadedQueueItem, error) {
	entries, err := readBoundedSyncStateEntries(root, maxSyncStateDirectoryEntries, "queue state")
	if err != nil {
		return nil, errors.New("cannot inspect queue state")
	}
	records := make(map[string]loadedQueueItem, len(entries))
	seenNames := make(map[string]string, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if baseTempName.MatchString(name) {
			if err := validateIgnoredBaseEntry(root, name, "queue temporary"); err != nil {
				return nil, err
			}
			continue
		}
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
		item = queuePolicyView(item, policy)
		if err := validateQueuePolicy(item, policy); err != nil {
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

func writeQueueItemCAS(root *os.Root, item QueueItem, policy RetryPolicy, expected *loadedQueueItem, hook func(string, int, *os.Root, string) error, operation string) error {
	encoded, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return errors.New("cannot encode queue state")
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxQueueRecordBytes {
		return errors.New("queue state exceeds size limit")
	}
	leaf := queueRecordPath(item.ID)
	checkpoint := 0
	if err := atomicfile.WriteRootFileChecked(root, leaf, encoded, 0o600, func() error {
		checkpoint++
		if hook != nil {
			if err := hook(operation, checkpoint, root, leaf); err != nil {
				return errors.New("queue mutation probe failed")
			}
		}
		if checkpoint < 3 {
			return verifyQueueExpected(root, item.ID, expected)
		}
		return verifyQueuePublication(root, item, encoded, policy)
	}); err != nil {
		if cleanupErr := cleanupQueueRollbackBackup(root, item.ID, expected, encoded); cleanupErr != nil {
			if errors.Is(cleanupErr, ErrQueueRecoveryRequired) {
				if expected != nil {
					return newQueueRecoveryError(*expected)
				}
				return ErrQueueRecoveryRequired
			}
			return errors.New("cannot persist or clean queue state")
		}
		return errors.New("cannot persist queue state")
	}
	if err := verifyQueuePublication(root, item, encoded, policy); err != nil {
		return errors.New("queue state failed post-write verification")
	}
	return nil
}

func newQueueRecoveryError(expected loadedQueueItem) *QueueRecoveryError {
	digest := sha256.Sum256(expected.encoded)
	return &QueueRecoveryError{ItemID: expected.item.ID, ExpectedOldHash: hex.EncodeToString(digest[:])}
}

func cleanupQueueRollbackBackup(root *os.Root, id string, expected *loadedQueueItem, desired []byte) error {
	backup := atomicfile.BackupPath(queueRecordPath(id))
	info, err := root.Lstat(backup)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || expected == nil || info == nil || !info.Mode().IsRegular() || isStateRedirect(info) || !privateStateMode(info, 0o600) || !sameBaseFileMetadata(expected.info, info) {
		return ErrQueueRecoveryRequired
	}
	_, encoded, err := readQueueItem(root, backup, info)
	if err != nil || !bytes.Equal(encoded, expected.encoded) {
		return ErrQueueRecoveryRequired
	}
	primary, err := root.Lstat(queueRecordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return ErrQueueRecoveryRequired
	}
	if err != nil || primary == nil || !primary.Mode().IsRegular() || isStateRedirect(primary) || !privateStateMode(primary, 0o600) || os.SameFile(info, primary) {
		return ErrQueueRecoveryRequired
	}
	primaryBytes, err := readStableQueueBytes(root, queueRecordPath(id), primary)
	if err != nil || len(primaryBytes) == 0 || bytes.Equal(primaryBytes, desired) {
		return ErrQueueRecoveryRequired
	}
	final, err := root.Lstat(backup)
	if err != nil || !sameBaseFileMetadata(info, final) {
		return errors.New("queue rollback backup changed before cleanup")
	}
	if err := atomicfile.RemoveRoot(root, backup); err != nil {
		return errors.New("cannot remove authenticated queue rollback backup")
	}
	if _, err := root.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return errors.New("queue rollback backup remains after cleanup")
	}
	return nil
}

func readStableQueueBytes(root *os.Root, name string, before os.FileInfo) ([]byte, error) {
	if before == nil || before.Size() > maxQueueRecordBytes {
		return nil, errors.New("queue state exceeds size limit")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, errors.New("cannot open queue state")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameBaseFileMetadata(before, opened) {
		return nil, errors.New("queue state changed while opening")
	}
	first, err := readQueueSnapshot(file)
	if err != nil {
		return nil, err
	}
	middle, err := file.Stat()
	if err != nil || !sameBaseFileMetadata(opened, middle) {
		return nil, errors.New("queue state changed while reading")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, errors.New("queue state cannot be reread")
	}
	second, err := readQueueSnapshot(file)
	if err != nil {
		return nil, err
	}
	afterOpen, statErr := file.Stat()
	afterName, nameErr := root.Lstat(name)
	if statErr != nil || nameErr != nil || !sameBaseFileMetadata(opened, afterOpen) || !sameBaseFileMetadata(opened, afterName) || !bytes.Equal(first, second) {
		return nil, errors.New("queue state changed while reading")
	}
	return first, nil
}

func verifyQueueExpected(root *os.Root, id string, expected *loadedQueueItem) error {
	if expected == nil {
		if _, err := root.Lstat(queueRecordPath(id)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("queue state changed before publication")
	}
	return verifyQueuePreimage(root, id, *expected)
}

func verifyQueuePublication(root *os.Root, item QueueItem, encoded []byte, policy RetryPolicy) error {
	current, err := loadQueueItemByID(root, item.ID, policy)
	if err != nil || current.item != item || !bytes.Equal(current.encoded, encoded) {
		return errors.New("queue publication failed authentication")
	}
	return nil
}

func loadQueueItemByID(root *os.Root, id string, policy RetryPolicy) (loadedQueueItem, error) {
	info, err := root.Lstat(queueRecordPath(id))
	if err != nil || info == nil || !info.Mode().IsRegular() || isStateRedirect(info) || !privateStateMode(info, 0o600) {
		return loadedQueueItem{}, errors.New("queue state is redirected, invalid, or not private")
	}
	item, encoded, err := readQueueItem(root, queueRecordPath(id), info)
	if err != nil {
		return loadedQueueItem{}, err
	}
	item = queuePolicyView(item, policy)
	if err := validateQueuePolicy(item, policy); err != nil {
		return loadedQueueItem{}, err
	}
	return loadedQueueItem{item: item, info: info, encoded: encoded}, nil
}
