// Package sourcecatalog stores one content-free record for each logical
// provider/session source below the global private SessionReviewer data root.
package sourcecatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
)

const (
	privateDirectoryMode fs.FileMode = 0o700
	privateFileMode      fs.FileMode = 0o600
	maxCatalogRecord                 = 4 << 20
	catalogLockTimeout               = 5 * time.Second
)

var ErrCASConflict = errors.New("source catalog compare-and-swap conflict")

type Catalog struct {
	mu                 sync.RWMutex
	data               *pathguard.Directory
	root               *pathguard.Directory
	closed             bool
	beforeAdvisoryLock func()
	beforePublish      func() error
}

func Open(dataRoot string) (*Catalog, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("SessionReviewer data root must be an absolute clean path")
	}
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("open SessionReviewer data root: %w", err)
	}
	closeData := true
	defer func() {
		if closeData {
			_ = data.Close()
		}
	}()
	if err := protectDirectory(data); err != nil {
		return nil, err
	}
	if err := data.EnsureDirectory("source-catalog", privateDirectoryMode); err != nil {
		return nil, fmt.Errorf("create private source catalog: %w", err)
	}
	root, err := pathguard.Open(filepath.Join(data.Path, "source-catalog"))
	if err != nil {
		return nil, err
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = root.Close()
		}
	}()
	if len(root.Ancestors) < 2 || !os.SameFile(data.Info(), root.Ancestors[len(root.Ancestors)-2]) {
		return nil, errors.New("source catalog escaped SessionReviewer data root")
	}
	if err := protectDirectory(root); err != nil {
		return nil, err
	}
	closeData = false
	closeRoot = false
	return &Catalog{data: data, root: root}, nil
}

func (c *Catalog) UpsertSource(record memory.SourceRecord) (string, error) {
	record = cloneRecord(record)
	sort.Strings(record.ProjectIDs)
	record.ProjectIDs = distinct(record.ProjectIDs)
	if err := memory.ValidateSourceRecord(record); err != nil {
		return "", fmt.Errorf("invalid source record: %w", err)
	}
	var digest string
	err := c.withWriteLock(func() error {
		leaf := sourceLeaf(record.Provider, record.SessionID)
		existing, found, body, err := c.readLeaf(leaf)
		if err != nil {
			return err
		}
		merged := record
		if found {
			merged, err = mergeSource(existing, record)
			if err != nil {
				return err
			}
		}
		newBody, err := marshalCanonical(merged)
		if err != nil {
			return err
		}
		digest, err = memory.Digest(merged)
		if err != nil {
			return err
		}
		if found && bytes.Equal(body, newBody) {
			return nil
		}
		if found {
			checkpoint := func() error { return c.verifyLiveRoot() }
			rollbackCheckpoint := func() error {
				if err := c.verifyLiveRoot(); err != nil {
					return err
				}
				_, present, current, err := c.readLeaf(leaf)
				if err != nil || !present || !bytes.Equal(current, body) {
					return errors.Join(errors.New("source catalog compare-and-swap conflict"), err)
				}
				return nil
			}
			if err := atomicfile.WriteRootFileCheckedWithRollbackCheckpoint(c.root.Root, leaf, newBody, privateFileMode, checkpoint, rollbackCheckpoint); err != nil {
				return err
			}
		} else if err := atomicfile.WriteRootFileCreateIfAbsent(c.root.Root, leaf, newBody, privateFileMode, c.verifyBeforePublish); err != nil {
			return err
		}
		stored, present, storedBody, err := c.readLeaf(leaf)
		if err != nil || !present || !bytes.Equal(storedBody, newBody) || !reflect.DeepEqual(stored, merged) {
			return errors.Join(errors.New("source catalog canonical re-read failed"), err)
		}
		return nil
	})
	return digest, err
}

// ReplaceSource replaces one authenticated logical source only when the live
// record still has expectedDigest. It is reserved for truncation or interior
// mutation; monotonic appends must continue through UpsertSource.
func (c *Catalog) ReplaceSource(expectedDigest string, record memory.SourceRecord) (string, error) {
	record = cloneRecord(record)
	sort.Strings(record.ProjectIDs)
	record.ProjectIDs = distinct(record.ProjectIDs)
	if err := memory.ValidateSourceRecord(record); err != nil {
		return "", fmt.Errorf("invalid source replacement: %w", err)
	}
	var digest string
	err := c.withWriteLock(func() error {
		leaf := sourceLeaf(record.Provider, record.SessionID)
		existing, found, body, err := c.readLeaf(leaf)
		if err != nil {
			return err
		}
		if !found {
			return ErrCASConflict
		}
		currentDigest, err := memory.Digest(existing)
		if err != nil {
			return err
		}
		if currentDigest != expectedDigest {
			return ErrCASConflict
		}
		if existing.Provider != record.Provider || existing.SessionID != record.SessionID ||
			existing.SourceIdentity != record.SourceIdentity || existing.StartedAt != record.StartedAt {
			return projectidentity.ErrAssociationRequired
		}
		existingLocation, replacementLocation := existing.FrozenBoundary.Location.JSONL, record.FrozenBoundary.Location.JSONL
		if existingLocation == nil || replacementLocation == nil || replacementLocation.Line > existingLocation.Line || replacementLocation.ByteOffset > existingLocation.ByteOffset {
			return projectidentity.ErrAssociationRequired
		}
		newBody, err := marshalCanonical(record)
		if err != nil {
			return err
		}
		digest, err = memory.Digest(record)
		if err != nil {
			return err
		}
		if bytes.Equal(body, newBody) {
			return nil
		}
		checkpoint := func() error { return c.verifyLiveRoot() }
		rollbackCheckpoint := func() error {
			if err := c.verifyLiveRoot(); err != nil {
				return err
			}
			_, present, current, err := c.readLeaf(leaf)
			if err != nil || !present || !bytes.Equal(current, body) {
				return errors.Join(ErrCASConflict, err)
			}
			return nil
		}
		if err := atomicfile.WriteRootFileCheckedWithRollbackCheckpoint(c.root.Root, leaf, newBody, privateFileMode, checkpoint, rollbackCheckpoint); err != nil {
			return err
		}
		stored, present, storedBody, err := c.readLeaf(leaf)
		if err != nil || !present || !bytes.Equal(storedBody, newBody) || !reflect.DeepEqual(stored, record) {
			return errors.Join(errors.New("source catalog replacement canonical re-read failed"), err)
		}
		return nil
	})
	return digest, err
}

func (c *Catalog) GetSource(provider, sessionID string) (memory.SourceRecord, bool, error) {
	var record memory.SourceRecord
	var found bool
	err := c.withReadLock(func() error {
		var err error
		record, found, _, err = c.readLeaf(sourceLeaf(provider, sessionID))
		return err
	})
	return cloneRecord(record), found, err
}

// ListCandidates lists all records, or records associated with one project
// when exactly one project ID is supplied.
func (c *Catalog) ListCandidates(projectIDs ...string) ([]memory.SourceRecord, error) {
	if len(projectIDs) > 1 {
		return nil, errors.New("at most one project filter is allowed")
	}
	var records []memory.SourceRecord
	err := c.withReadLock(func() error {
		var err error
		records, err = c.listRecords()
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(projectIDs) == 1 {
		filtered := records[:0]
		for _, record := range records {
			if contains(record.ProjectIDs, projectIDs[0]) {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}
	return records, nil
}

func (c *Catalog) AssociatedUsage(projectID string) ([]memory.AssociatedUsage, error) {
	records, err := c.ListCandidates(projectID)
	if err != nil {
		return nil, err
	}
	result := make([]memory.AssociatedUsage, 0, len(records))
	for _, record := range records {
		digest, err := memory.Digest(record)
		if err != nil {
			return nil, err
		}
		result = append(result, memory.AssociatedUsage{
			Provider: record.Provider, SessionID: record.SessionID,
			UsageRecordDigest: digest, Shared: len(record.ProjectIDs) > 1,
		})
	}
	return result, nil
}

func (c *Catalog) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return errors.Join(c.root.Close(), c.data.Close())
}

func mergeSource(existing, incoming memory.SourceRecord) (memory.SourceRecord, error) {
	if existing.Provider != incoming.Provider || existing.SessionID != incoming.SessionID ||
		existing.SourceIdentity != incoming.SourceIdentity || existing.StartedAt != incoming.StartedAt {
		return memory.SourceRecord{}, projectidentity.ErrAssociationRequired
	}
	comparison, err := compareBoundary(existing.FrozenBoundary, incoming.FrozenBoundary)
	if err != nil {
		return memory.SourceRecord{}, projectidentity.ErrAssociationRequired
	}
	merged := existing
	if comparison < 0 {
		merged.EndedAt = incoming.EndedAt
		merged.FrozenBoundary = incoming.FrozenBoundary
		merged.Availability = incoming.Availability
		merged.Usage = incoming.Usage
	} else if comparison == 0 {
		if existing.EndedAt != incoming.EndedAt || !reflect.DeepEqual(existing.Usage, incoming.Usage) {
			return memory.SourceRecord{}, projectidentity.ErrAssociationRequired
		}
		merged.Availability = incoming.Availability
	}
	merged.ProjectIDs = append(append([]string(nil), existing.ProjectIDs...), incoming.ProjectIDs...)
	sort.Strings(merged.ProjectIDs)
	merged.ProjectIDs = distinct(merged.ProjectIDs)
	if err := memory.ValidateSourceRecord(merged); err != nil {
		return memory.SourceRecord{}, err
	}
	return merged, nil
}

func compareBoundary(first, second memory.FrozenBoundary) (int, error) {
	if first.Location.Kind != memory.SourceLocationJSONL || second.Location.Kind != memory.SourceLocationJSONL || first.Location.JSONL == nil || second.Location.JSONL == nil {
		return 0, errors.New("unsupported source boundary")
	}
	a, b := first.Location.JSONL, second.Location.JSONL
	comparison := 0
	if a.Line < b.Line || (a.Line == b.Line && a.ByteOffset < b.ByteOffset) {
		comparison = -1
	} else if a.Line > b.Line || (a.Line == b.Line && a.ByteOffset > b.ByteOffset) {
		comparison = 1
	}
	if comparison == 0 && first.SourceHash != second.SourceHash {
		return 0, errors.New("same boundary has different source hash")
	}
	return comparison, nil
}

func (c *Catalog) withWriteLock(run func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.withAdvisoryLock(run)
}

func (c *Catalog) withReadLock(run func() error) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.withAdvisoryLock(run)
}

func (c *Catalog) withAdvisoryLock(run func() error) error {
	if err := c.requireOpen(); err != nil {
		return err
	}
	if err := c.verifyLiveRoot(); err != nil {
		return err
	}
	if c.beforeAdvisoryLock != nil {
		c.beforeAdvisoryLock()
	}
	lock, err := project.AcquireProjectLock(c.root.Root, "catalog.lock", catalogLockTimeout)
	if err != nil {
		return err
	}
	return errors.Join(run(), lock.Release())
}

func (c *Catalog) verifyBeforePublish() error {
	if err := c.verifyLiveRoot(); err != nil {
		return err
	}
	if c.beforePublish != nil {
		return c.beforePublish()
	}
	return nil
}

func (c *Catalog) requireOpen() error {
	if c == nil || c.closed || c.data == nil || c.root == nil {
		return errors.New("source catalog is closed")
	}
	return nil
}

func (c *Catalog) verifyLiveRoot() error {
	current, err := pathguard.Open(c.root.Path)
	if err != nil {
		return errors.New("source catalog root changed")
	}
	defer current.Close()
	if !os.SameFile(c.root.Info(), current.Info()) {
		return errors.New("source catalog root identity changed")
	}
	return protectDirectory(current)
}

func (c *Catalog) readLeaf(leaf string) (memory.SourceRecord, bool, []byte, error) {
	body, found, err := c.root.ReadRegular(leaf, maxCatalogRecord)
	if err != nil || !found {
		return memory.SourceRecord{}, found, nil, err
	}
	if err := requirePrivateFile(c.root.Root, leaf); err != nil {
		return memory.SourceRecord{}, false, nil, err
	}
	var record memory.SourceRecord
	if err := decodeCanonical(body, &record); err != nil {
		return memory.SourceRecord{}, false, nil, err
	}
	if err := memory.ValidateSourceRecord(record); err != nil {
		return memory.SourceRecord{}, false, nil, err
	}
	if sourceLeaf(record.Provider, record.SessionID) != leaf {
		return memory.SourceRecord{}, false, nil, errors.New("source catalog filename identity mismatch")
	}
	return record, true, body, nil
}

func (c *Catalog) listRecords() ([]memory.SourceRecord, error) {
	directory, err := c.root.Root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var records []memory.SourceRecord
	for _, entry := range entries {
		if entry.Name() == "catalog.lock" {
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || len(entry.Name()) != 64+len(".json") {
			return nil, fmt.Errorf("unexpected source catalog entry %q", entry.Name())
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(entry.Name(), ".json")); err != nil {
			return nil, fmt.Errorf("invalid source catalog entry %q", entry.Name())
		}
		record, found, _, err := c.readLeaf(entry.Name())
		if err != nil || !found {
			return nil, errors.Join(errors.New("invalid source catalog record"), err)
		}
		records = append(records, cloneRecord(record))
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Provider != records[j].Provider {
			return records[i].Provider < records[j].Provider
		}
		return records[i].SessionID < records[j].SessionID
	})
	return records, nil
}

func sourceLeaf(provider, sessionID string) string {
	digest := sha256.Sum256([]byte(provider + "\x00" + sessionID))
	return hex.EncodeToString(digest[:]) + ".json"
}

func cloneRecord(value memory.SourceRecord) memory.SourceRecord {
	value.ProjectIDs = append([]string(nil), value.ProjectIDs...)
	value.Usage.Models = append([]accounting.ModelUsage(nil), value.Usage.Models...)
	return value
}

func distinct(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func contains(values []string, wanted string) bool {
	index := sort.SearchStrings(values, wanted)
	return index < len(values) && values[index] == wanted
}

func marshalCanonical(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func decodeCanonical(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("source catalog JSON has trailing data")
	}
	canonical, err := marshalCanonical(destination)
	if err != nil || !bytes.Equal(canonical, body) {
		return errors.Join(errors.New("source catalog JSON is not canonical"), err)
	}
	return nil
}

func protectDirectory(directory *pathguard.Directory) error {
	if directory == nil || directory.Root == nil || directory.Info() == nil || !directory.Info().IsDir() {
		return errors.New("private directory is unavailable")
	}
	if runtime.GOOS != "windows" {
		if err := directory.Root.Chmod(".", privateDirectoryMode); err != nil {
			return err
		}
		info, err := directory.Root.Stat(".")
		if err != nil || !os.SameFile(directory.Info(), info) || info.Mode().Perm() != privateDirectoryMode {
			return errors.New("private directory permissions or identity changed")
		}
	}
	return nil
}

func requirePrivateFile(root *os.Root, leaf string) error {
	info, err := root.Lstat(leaf)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("source catalog record is not a regular file"), err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != privateFileMode {
		return errors.New("source catalog record is not private")
	}
	return nil
}
