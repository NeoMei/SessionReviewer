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
	"github.com/neomei/SessionReviewer/internal/source"
)

const (
	privateDirectoryMode fs.FileMode = 0o700
	privateFileMode      fs.FileMode = 0o600
	maxCatalogRecord                 = 4 << 20
	maxBatchJournal                  = 64 << 20
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
	batchCheckpoint    func()
	beforeJournalLink  func() error
	afterJournalLink   func() error
	beforeOrphanUnlink func()
}

const (
	batchJournalLeaf        = "batch-mutation-v1.json"
	batchJournalStagingLeaf = ".batch-mutation-v1.staging"
)

type BatchMutation struct {
	Relation       source.BoundaryRelation `json:"relation"`
	ExpectedDigest string                  `json:"expected_digest,omitempty"`
	Desired        memory.SourceRecord     `json:"desired"`
}

type BatchResult struct {
	Record memory.SourceRecord
	Digest string
}

type SnapshotKey struct{ Provider, SessionID string }
type SourceSnapshot struct {
	Record memory.SourceRecord
	Found  bool
	Digest string
}

// SnapshotSources reads all requested catalog baselines under one advisory
// lock. Returned records are defensive content-free copies.
func (c *Catalog) SnapshotSources(keys []SnapshotKey) (map[SnapshotKey]SourceSnapshot, error) {
	result := make(map[SnapshotKey]SourceSnapshot, len(keys))
	err := c.withReadLock(func() error {
		for _, key := range keys {
			if key.Provider == "" || key.SessionID == "" {
				return errors.New("invalid source snapshot key")
			}
			if _, duplicate := result[key]; duplicate {
				return errors.New("duplicate source snapshot key")
			}
			record, found, _, err := c.readLeaf(sourceLeaf(key.Provider, key.SessionID))
			if err != nil {
				return err
			}
			snapshot := SourceSnapshot{Record: cloneRecord(record), Found: found}
			if found {
				snapshot.Digest, err = memory.Digest(record)
				if err != nil {
					return err
				}
			}
			result[key] = snapshot
		}
		return nil
	})
	return result, err
}

type batchJournal struct {
	Version int                 `json:"version"`
	Entries []batchJournalEntry `json:"entries"`
}

type batchJournalEntry struct {
	Leaf      string                  `json:"leaf"`
	Relation  source.BoundaryRelation `json:"relation"`
	Old       *memory.SourceRecord    `json:"old,omitempty"`
	OldDigest string                  `json:"old_digest,omitempty"`
	Desired   memory.SourceRecord     `json:"desired"`
	Digest    string                  `json:"digest"`
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
	catalog := &Catalog{data: data, root: root}
	if err := catalog.withWriteLock(func() error { return nil }); err != nil {
		_ = catalog.Close()
		return nil, err
	}
	return catalog, nil
}

// ApplyBatch validates and durably publishes one content-free catalog
// transaction. A recovered journal always converges to the complete desired
// batch, so readers observe the old set or the full new set, never a partial
// transaction after restart.
func (c *Catalog) ApplyBatch(mutations []BatchMutation) ([]BatchResult, error) {
	if len(mutations) == 0 {
		return nil, errors.New("source catalog batch is empty")
	}
	var results []BatchResult
	err := c.withWriteLock(func() error {
		entries, planned, err := c.planBatch(mutations)
		if err != nil {
			return err
		}
		results = planned
		if len(entries) == 0 {
			return nil
		}
		journalBody, err := marshalCanonical(batchJournal{Version: 1, Entries: entries})
		if err != nil {
			return err
		}
		if len(journalBody) > maxBatchJournal {
			return errors.New("source catalog batch journal exceeds bounded limit")
		}
		if err := c.writeBatchJournalUnlocked(journalBody); err != nil {
			return fmt.Errorf("write source catalog batch journal: %w", err)
		}
		c.runBatchCheckpoint()
		if err := c.publishBatch(entries); err != nil {
			return err
		}
		if err := atomicfile.RemoveRoot(c.root.Root, batchJournalLeaf); err != nil {
			return err
		}
		c.runBatchCheckpoint()
		return nil
	})
	return results, err
}

// PlanBatch performs the same complete validation and normalization as
// ApplyBatch without writing a journal or source record.
func (c *Catalog) PlanBatch(mutations []BatchMutation) ([]BatchResult, error) {
	if len(mutations) == 0 {
		return nil, errors.New("source catalog batch is empty")
	}
	var results []BatchResult
	err := c.withReadLock(func() error {
		_, planned, err := c.planBatch(mutations)
		results = planned
		return err
	})
	return results, err
}

// WithBatchSnapshot verifies that every previously applied batch result is
// still current and holds the catalog transaction lock while run commits its
// dependent pointer. It prevents a stale scan from preparing against a newer
// catalog boundary.
func (c *Catalog) WithBatchSnapshot(expected []BatchResult, run func() error) error {
	if len(expected) == 0 || run == nil {
		return errors.New("catalog batch snapshot and callback are required")
	}
	return c.withWriteLock(func() error {
		seen := make(map[string]struct{}, len(expected))
		for _, item := range expected {
			if err := memory.ValidateSourceRecord(item.Record); err != nil {
				return err
			}
			leaf := sourceLeaf(item.Record.Provider, item.Record.SessionID)
			if _, duplicate := seen[leaf]; duplicate {
				return errors.New("duplicate source in catalog snapshot")
			}
			seen[leaf] = struct{}{}
			record, found, _, err := c.readLeaf(leaf)
			if err != nil || !found {
				return errors.Join(ErrCASConflict, err)
			}
			digest, err := memory.Digest(record)
			if err != nil || digest != item.Digest || !reflect.DeepEqual(record, item.Record) {
				return errors.Join(ErrCASConflict, err)
			}
		}
		return run()
	})
}

func (c *Catalog) planBatch(mutations []BatchMutation) ([]batchJournalEntry, []BatchResult, error) {
	seen := make(map[string]struct{}, len(mutations))
	entries := make([]batchJournalEntry, 0, len(mutations))
	results := make([]BatchResult, 0, len(mutations))
	for _, mutation := range mutations {
		leaf := sourceLeaf(mutation.Desired.Provider, mutation.Desired.SessionID)
		if _, duplicate := seen[leaf]; duplicate {
			return nil, nil, errors.New("duplicate source in catalog batch")
		}
		seen[leaf] = struct{}{}
		existing, found, _, err := c.readLeaf(leaf)
		if err != nil {
			return nil, nil, err
		}
		desired, desiredDigest, currentDigest, idempotent, err := validateMutation(existing, found, mutation)
		if err != nil {
			return nil, nil, err
		}
		results = append(results, BatchResult{Record: cloneRecord(desired), Digest: desiredDigest})
		if idempotent {
			continue
		}
		entry := batchJournalEntry{Leaf: leaf, Relation: mutation.Relation, OldDigest: currentDigest, Desired: desired, Digest: desiredDigest}
		if found {
			old := cloneRecord(existing)
			entry.Old = &old
		}
		entries = append(entries, entry)
	}
	return entries, results, nil
}

func validateMutation(existing memory.SourceRecord, found bool, mutation BatchMutation) (memory.SourceRecord, string, string, bool, error) {
	desired := cloneRecord(mutation.Desired)
	sort.Strings(desired.ProjectIDs)
	desired.ProjectIDs = distinct(desired.ProjectIDs)
	if err := memory.ValidateSourceRecord(desired); err != nil {
		return memory.SourceRecord{}, "", "", false, fmt.Errorf("invalid batch source record: %w", err)
	}
	if _, err := marshalCanonical(desired); err != nil {
		return memory.SourceRecord{}, "", "", false, err
	}
	currentDigest := ""
	if found {
		var err error
		currentDigest, err = memory.Digest(existing)
		if err != nil {
			return memory.SourceRecord{}, "", "", false, err
		}
		if existing.Provider != desired.Provider || existing.SessionID != desired.SessionID || existing.SourceIdentity != desired.SourceIdentity || existing.StartedAt != desired.StartedAt {
			return memory.SourceRecord{}, "", "", false, projectidentity.ErrAssociationRequired
		}
		desired.ProjectIDs = append(desired.ProjectIDs, existing.ProjectIDs...)
		sort.Strings(desired.ProjectIDs)
		desired.ProjectIDs = distinct(desired.ProjectIDs)
	}
	desiredDigest, err := memory.Digest(desired)
	if err != nil {
		return memory.SourceRecord{}, "", "", false, err
	}
	if found && currentDigest == desiredDigest {
		return desired, desiredDigest, currentDigest, true, nil
	}
	switch mutation.Relation {
	case source.BoundaryInitial:
		if found {
			return memory.SourceRecord{}, "", "", false, ErrCASConflict
		}
	case source.BoundaryUnchanged:
		if !found || mutation.ExpectedDigest == "" || mutation.ExpectedDigest != currentDigest {
			return memory.SourceRecord{}, "", "", false, ErrCASConflict
		}
		comparison, compareErr := compareBoundary(existing.FrozenBoundary, desired.FrozenBoundary)
		if compareErr != nil || comparison != 0 {
			return memory.SourceRecord{}, "", "", false, ErrCASConflict
		}
		if existing.EndedAt != desired.EndedAt || !reflect.DeepEqual(existing.Usage, desired.Usage) {
			return memory.SourceRecord{}, "", "", false, projectidentity.ErrAssociationRequired
		}
	case source.BoundaryAppend:
		if !found || mutation.ExpectedDigest == "" || mutation.ExpectedDigest != currentDigest {
			return memory.SourceRecord{}, "", "", false, ErrCASConflict
		}
		comparison, compareErr := compareBoundary(existing.FrozenBoundary, desired.FrozenBoundary)
		if compareErr != nil || comparison >= 0 {
			return memory.SourceRecord{}, "", "", false, ErrCASConflict
		}
		existingEnd, oldErr := time.Parse(time.RFC3339Nano, existing.EndedAt)
		desiredEnd, newErr := time.Parse(time.RFC3339Nano, desired.EndedAt)
		if oldErr != nil || newErr != nil || desiredEnd.Before(existingEnd) {
			return memory.SourceRecord{}, "", "", false, projectidentity.ErrAssociationRequired
		}
	case source.BoundaryReplacement:
		if !found || mutation.ExpectedDigest == "" || mutation.ExpectedDigest != currentDigest {
			return memory.SourceRecord{}, "", "", false, ErrCASConflict
		}
		oldLoc, newLoc := existing.FrozenBoundary.Location.JSONL, desired.FrozenBoundary.Location.JSONL
		if oldLoc == nil || newLoc == nil || newLoc.Line > oldLoc.Line || newLoc.ByteOffset > oldLoc.ByteOffset {
			return memory.SourceRecord{}, "", "", false, projectidentity.ErrAssociationRequired
		}
	default:
		return memory.SourceRecord{}, "", "", false, errors.New("invalid source catalog batch relation")
	}
	if err := memory.ValidateSourceRecord(desired); err != nil {
		return memory.SourceRecord{}, "", "", false, err
	}
	return desired, desiredDigest, currentDigest, false, nil
}

func (c *Catalog) publishBatch(entries []batchJournalEntry) error {
	for _, entry := range entries {
		body, err := marshalCanonical(entry.Desired)
		if err != nil {
			return err
		}
		if err := c.verifyBeforePublish(); err != nil {
			return err
		}
		if err := atomicfile.WriteRootFile(c.root.Root, entry.Leaf, body, privateFileMode); err != nil {
			return err
		}
		c.runBatchCheckpoint()
		stored, found, _, err := c.readLeaf(entry.Leaf)
		if err != nil || !found {
			return errors.Join(errors.New("batch catalog re-read failed"), err)
		}
		digest, err := memory.Digest(stored)
		if err != nil || digest != entry.Digest {
			return errors.Join(errors.New("batch catalog digest mismatch"), err)
		}
	}
	return nil
}

func (c *Catalog) runBatchCheckpoint() {
	if c.batchCheckpoint != nil {
		c.batchCheckpoint()
	}
}

func (c *Catalog) recoverBatchUnlocked() error {
	body, found, err := c.root.ReadRegular(batchJournalLeaf, maxBatchJournal)
	if err != nil || !found {
		return err
	}
	if err := requirePrivateFile(c.root.Root, batchJournalLeaf); err != nil {
		return err
	}
	var journal batchJournal
	if err := decodeCanonical(body, &journal); err != nil {
		return err
	}
	if err := c.validateJournalUnlocked(journal); err != nil {
		return err
	}
	if err := c.publishBatch(journal.Entries); err != nil {
		return err
	}
	return atomicfile.RemoveRoot(c.root.Root, batchJournalLeaf)
}

func (c *Catalog) validateJournalUnlocked(journal batchJournal) error {
	if journal.Version != 1 || len(journal.Entries) == 0 {
		return errors.New("invalid source catalog batch journal")
	}
	seen := make(map[string]struct{}, len(journal.Entries))
	for _, entry := range journal.Entries {
		if sourceLeaf(entry.Desired.Provider, entry.Desired.SessionID) != entry.Leaf {
			return errors.New("batch journal source identity mismatch")
		}
		if _, duplicate := seen[entry.Leaf]; duplicate {
			return errors.New("duplicate batch journal source")
		}
		seen[entry.Leaf] = struct{}{}
		oldFound := entry.Old != nil
		if oldFound != (entry.OldDigest != "") {
			return errors.New("batch journal predecessor presence mismatch")
		}
		var old memory.SourceRecord
		if oldFound {
			old = cloneRecord(*entry.Old)
			if err := memory.ValidateSourceRecord(old); err != nil || sourceLeaf(old.Provider, old.SessionID) != entry.Leaf {
				return errors.Join(errors.New("invalid batch journal predecessor"), err)
			}
			oldDigest, err := memory.Digest(old)
			if err != nil || oldDigest != entry.OldDigest {
				return errors.Join(errors.New("batch journal predecessor digest mismatch"), err)
			}
		}
		validated, digest, _, idempotent, err := validateMutation(old, oldFound, BatchMutation{Relation: entry.Relation, ExpectedDigest: entry.OldDigest, Desired: entry.Desired})
		if err != nil || idempotent || digest != entry.Digest || !reflect.DeepEqual(validated, entry.Desired) {
			return errors.Join(errors.New("batch journal transition is invalid"), err)
		}
		current, currentFound, _, err := c.readLeaf(entry.Leaf)
		if err != nil {
			return err
		}
		if oldFound && !currentFound {
			return errors.New("batch journal required predecessor is missing")
		}
		if currentFound && !reflect.DeepEqual(current, entry.Desired) && (!oldFound || !reflect.DeepEqual(current, old)) {
			return ErrCASConflict
		}
	}
	return nil
}

func (c *Catalog) writeBatchJournalUnlocked(body []byte) (retErr error) {
	if len(body) == 0 || len(body) > maxBatchJournal {
		return errors.New("source catalog batch journal body is invalid")
	}
	for _, leaf := range []string{batchJournalLeaf, batchJournalStagingLeaf} {
		if _, err := c.root.Root.Lstat(leaf); err == nil {
			return fmt.Errorf("source catalog journal state already exists: %w", fs.ErrExist)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	file, err := c.root.Root.OpenFile(batchJournalStagingLeaf, os.O_RDWR|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return err
	}
	created, err := file.Stat()
	if err != nil || !created.Mode().IsRegular() {
		return errors.Join(errors.New("cannot authenticate catalog journal staging"), err, file.Close())
	}
	keepForRecovery := false
	defer func() {
		retErr = errors.Join(retErr, file.Close())
		if retErr != nil && !keepForRecovery {
			retErr = errors.Join(retErr, c.removeCreatedJournalStagingUnlocked(created))
		}
	}()
	if err := file.Chmod(privateFileMode); err != nil {
		return err
	}
	written, err := io.Copy(file, bytes.NewReader(body))
	if err != nil || written != int64(len(body)) {
		return errors.Join(errors.New("write complete catalog journal staging"), err)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	staged, err := file.Stat()
	if err != nil || !os.SameFile(created, staged) || staged.Size() != int64(len(body)) {
		return errors.Join(errors.New("catalog journal staging changed while writing"), err)
	}
	if err := atomicfile.SyncRootPublication(c.root.Root, batchJournalStagingLeaf); err != nil {
		return err
	}
	validatedBody, _, validatedInfo, err := c.readValidatedJournalLeafUnlocked(batchJournalStagingLeaf)
	if err != nil || !os.SameFile(created, validatedInfo) || !bytes.Equal(body, validatedBody) {
		return errors.Join(errors.New("catalog journal staging validation failed"), err)
	}
	if err := c.verifyBeforePublish(); err != nil {
		return err
	}
	if c.beforeJournalLink != nil {
		if err := c.beforeJournalLink(); err != nil {
			return err
		}
	}
	if err := c.verifyLiveRoot(); err != nil {
		return err
	}
	beforeLinkBody, _, beforeLinkInfo, err := c.readValidatedJournalLeafUnlocked(batchJournalStagingLeaf)
	if err != nil || !os.SameFile(created, beforeLinkInfo) || !bytes.Equal(body, beforeLinkBody) {
		return errors.Join(errors.New("catalog journal staging changed before link"), err)
	}
	if _, err := c.root.Root.Lstat(batchJournalLeaf); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(errors.New("catalog journal target appeared before link"), err)
	}
	if err := c.root.Root.Link(batchJournalStagingLeaf, batchJournalLeaf); err != nil {
		return err
	}
	keepForRecovery = true
	if err := atomicfile.SyncRootPublication(c.root.Root, batchJournalLeaf); err != nil {
		return err
	}
	if c.afterJournalLink != nil {
		if err := c.afterJournalLink(); err != nil {
			return err
		}
	}
	if err := c.reconcileJournalAliasUnlocked(batchJournalStagingLeaf, false); err != nil {
		return err
	}
	keepForRecovery = false
	return nil
}

func (c *Catalog) removeCreatedJournalStagingUnlocked(created os.FileInfo) error {
	if err := c.verifyLiveRoot(); err != nil {
		return err
	}
	current, err := c.root.Root.Lstat(batchJournalStagingLeaf)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(created, current) {
		return errors.Join(errors.New("catalog journal staging ownership changed; not removed"), err)
	}
	if _, err := c.root.Root.Lstat(batchJournalLeaf); err == nil {
		return errors.New("catalog journal staging was linked; not removed as pre-link state")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := c.root.Root.Remove(batchJournalStagingLeaf); err != nil {
		return err
	}
	return atomicfile.SyncRootDirectory(c.root.Root)
}

func (c *Catalog) reconcileAtomicTempsUnlocked() error {
	directory, err := c.root.Root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	stagingFound := false
	legacy := make([]string, 0, 1)
	for _, entry := range entries {
		name := entry.Name()
		if name == batchJournalStagingLeaf {
			stagingFound = true
			continue
		}
		if !isAtomicTempName(name) {
			continue
		}
		legacy = append(legacy, name)
	}
	if len(legacy) > 1 || (stagingFound && len(legacy) != 0) {
		return errors.New("ambiguous catalog journal temporary ownership")
	}
	if stagingFound {
		return c.reconcileJournalAliasUnlocked(batchJournalStagingLeaf, true)
	}
	if len(legacy) == 1 {
		return c.reconcileJournalAliasUnlocked(legacy[0], false)
	}
	return nil
}

func (c *Catalog) reconcileJournalAliasUnlocked(name string, allowPreLink bool) error {
	temporaryBody, _, temporaryInfo, err := c.readValidatedJournalLeafUnlocked(name)
	if err != nil {
		return fmt.Errorf("invalid catalog journal temporary: %w", err)
	}
	targetBody, _, targetInfo, targetErr := c.readValidatedJournalLeafUnlocked(batchJournalLeaf)
	if errors.Is(targetErr, os.ErrNotExist) {
		if !allowPreLink {
			return errors.New("catalog atomic temporary has no active journal target")
		}
		if err := c.verifyLiveRoot(); err != nil {
			return err
		}
		recheckedBody, _, recheckedInfo, err := c.readValidatedJournalLeafUnlocked(name)
		if err != nil || !os.SameFile(temporaryInfo, recheckedInfo) || !bytes.Equal(temporaryBody, recheckedBody) {
			return errors.Join(errors.New("catalog journal staging changed before recovery link"), err)
		}
		if _, err := c.root.Root.Lstat(batchJournalLeaf); !errors.Is(err, os.ErrNotExist) {
			return errors.Join(errors.New("catalog journal target appeared during recovery"), err)
		}
		if err := c.root.Root.Link(name, batchJournalLeaf); err != nil {
			return err
		}
		if err := atomicfile.SyncRootPublication(c.root.Root, batchJournalLeaf); err != nil {
			return err
		}
		targetBody, _, targetInfo, targetErr = c.readValidatedJournalLeafUnlocked(batchJournalLeaf)
	}
	if targetErr != nil {
		return targetErr
	}
	if !os.SameFile(temporaryInfo, targetInfo) || !bytes.Equal(temporaryBody, targetBody) {
		return errors.New("catalog journal temporary is not the active journal inode")
	}
	if c.beforeOrphanUnlink != nil {
		c.beforeOrphanUnlink()
	}
	if err := c.verifyLiveRoot(); err != nil {
		return err
	}
	finalTemporaryBody, _, finalTemporaryInfo, err := c.readValidatedJournalLeafUnlocked(name)
	if err != nil {
		return err
	}
	finalTargetBody, _, finalTargetInfo, err := c.readValidatedJournalLeafUnlocked(batchJournalLeaf)
	if err != nil || !os.SameFile(temporaryInfo, finalTemporaryInfo) || !os.SameFile(targetInfo, finalTargetInfo) ||
		!os.SameFile(finalTemporaryInfo, finalTargetInfo) || !bytes.Equal(temporaryBody, finalTemporaryBody) || !bytes.Equal(targetBody, finalTargetBody) {
		return errors.Join(errors.New("catalog journal alias changed before unlink"), err)
	}
	if err := c.root.Root.Remove(name); err != nil {
		return err
	}
	if err := atomicfile.SyncRootDirectory(c.root.Root); err != nil {
		return err
	}
	afterBody, _, afterInfo, err := c.readValidatedJournalLeafUnlocked(batchJournalLeaf)
	if err != nil || !os.SameFile(finalTargetInfo, afterInfo) || !bytes.Equal(finalTargetBody, afterBody) {
		return errors.Join(errors.New("active catalog journal changed across temporary unlink"), err)
	}
	return nil
}

func (c *Catalog) readValidatedJournalLeafUnlocked(leaf string) ([]byte, batchJournal, os.FileInfo, error) {
	before, err := c.root.Root.Lstat(leaf)
	if err != nil {
		return nil, batchJournal{}, nil, err
	}
	if err := requirePrivateFile(c.root.Root, leaf); err != nil {
		return nil, batchJournal{}, nil, err
	}
	body, found, err := c.root.ReadRegular(leaf, maxBatchJournal)
	if err != nil || !found {
		return nil, batchJournal{}, nil, errors.Join(errors.New("read catalog journal file"), err)
	}
	after, err := c.root.Root.Lstat(leaf)
	if err != nil || !os.SameFile(before, after) {
		return nil, batchJournal{}, nil, errors.Join(errors.New("catalog journal file identity changed"), err)
	}
	if err := requirePrivateFile(c.root.Root, leaf); err != nil {
		return nil, batchJournal{}, nil, err
	}
	var journal batchJournal
	if err := decodeCanonical(body, &journal); err != nil {
		return nil, batchJournal{}, nil, err
	}
	if err := c.validateJournalUnlocked(journal); err != nil {
		return nil, batchJournal{}, nil, err
	}
	return bytes.Clone(body), journal, after, nil
}

func isAtomicTempName(name string) bool {
	const prefix = ".session-reviewer-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
		return false
	}
	suffix := strings.TrimPrefix(name, prefix)
	if suffix != strings.ToLower(suffix) {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
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
	if err := c.reconcileAtomicTempsUnlocked(); err != nil {
		return errors.Join(err, lock.Release())
	}
	if err := c.recoverBatchUnlocked(); err != nil {
		return errors.Join(err, lock.Release())
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
		if entry.Name() == "catalog.lock" || entry.Name() == batchJournalLeaf {
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
