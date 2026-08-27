package sync

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
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/platform"
)

var ErrStaleBase = errors.New("stale merge base")

const (
	baseDirectoryName = "merge-bases"
	baseLockName      = ".base-store.lock"
	maxBaseBytes      = 8 << 20
	maxBaseContent    = 4 << 20
)

var (
	stableBaseID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	lowerSHA256  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	baseJSONName = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)
	baseTempName = regexp.MustCompile(`^\.session-reviewer-[0-9a-f]{32}$`)
)

type BaseRecord struct {
	Version      int       `json:"version"`
	EntityID     string    `json:"entity_id"`
	RelativePath string    `json:"relative_path"`
	ContentHash  string    `json:"content_hash"`
	ProjectHash  string    `json:"project_hash"`
	VaultHash    string    `json:"vault_hash"`
	Content      []byte    `json:"content"`
	SyncedAt     time.Time `json:"synced_at"`
}

type BaseStore struct {
	Root *os.Root
}

func (s BaseStore) Load(entityID string) (BaseRecord, bool, error) {
	return s.loadWithFinalVerifyHook(entityID, nil)
}

func (s BaseStore) loadWithFinalVerifyHook(entityID string, beforeFinalVerify func() error) (BaseRecord, bool, error) {
	if !stableBaseID.MatchString(entityID) {
		return BaseRecord{}, false, errors.New("invalid merge-base entity ID")
	}
	bases, found, err := s.openBaseDirectory(false)
	if err != nil || !found {
		return BaseRecord{}, false, err
	}
	defer bases.Close()
	records, err := loadAllBaseRecords(bases)
	var record BaseRecord
	recordFound := false
	if err == nil {
		for _, candidate := range records {
			if candidate.EntityID == entityID {
				record = candidate
				recordFound = true
				break
			}
		}
	}
	if verifyErr := verifyBaseDirectoryIdentityWithHook(s.Root, bases, beforeFinalVerify); verifyErr != nil {
		return BaseRecord{}, false, errors.Join(err, verifyErr)
	}
	return record, recordFound, err
}

func (s BaseStore) List() ([]BaseRecord, error) {
	return s.listWithFinalVerifyHook(nil)
}

// ResetForMigration retires every authenticated v1 merge Base before compact
// v2 entities reuse stable IDs such as project-overview. The human documents
// remain authoritative; an interrupted reset is idempotent and a subsequent
// compact sync establishes fresh Bases from the v2 documents.
func (s BaseStore) ResetForMigration() (retErr error) {
	bases, found, err := s.openBaseDirectory(false)
	if err != nil || !found {
		return err
	}
	defer func() { retErr = errors.Join(retErr, bases.Close()) }()
	lock, err := acquireBaseStoreLock(bases)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.release()) }()
	if _, err := loadAllBaseRecords(bases); err != nil {
		return err
	}
	pairs, err := inspectBaseStateNames(bases)
	if err != nil {
		return err
	}
	for _, pair := range pairs {
		for _, name := range []string{pair.primary, pair.backup} {
			if name == "" {
				continue
			}
			info, found, err := regularBaseEntry(bases, name)
			if err != nil || !found {
				return errors.New("merge-base state changed during migration reset")
			}
			file, err := bases.Open(name)
			if err != nil {
				return errors.New("cannot open merge-base state during migration reset")
			}
			body, readErr := readBoundedBaseSnapshot(file)
			opened, statErr := file.Stat()
			closeErr := file.Close()
			if readErr != nil || statErr != nil || closeErr != nil || !sameBaseFileMetadata(info, opened) {
				return errors.New("merge-base state changed during migration reset")
			}
			digest := sha256.Sum256(body)
			if err := atomicfile.RemoveRootFileIfHashMatches(bases, name, hex.EncodeToString(digest[:])); err != nil {
				return err
			}
		}
	}
	return verifyBaseDirectoryIdentity(s.Root, bases)
}

func (s BaseStore) listWithFinalVerifyHook(beforeFinalVerify func() error) ([]BaseRecord, error) {
	bases, found, err := s.openBaseDirectory(false)
	if err != nil || !found {
		return nil, err
	}
	defer bases.Close()
	records, err := loadAllBaseRecords(bases)
	if err != nil {
		return nil, err
	}
	if err := verifyBaseDirectoryIdentityWithHook(s.Root, bases, beforeFinalVerify); err != nil {
		return nil, err
	}
	return records, nil
}

func (s BaseStore) Commit(expectedContentHash string, next BaseRecord) (retErr error) {
	return s.commitWithFinalVerifyHook(expectedContentHash, next, nil)
}

func (s BaseStore) commitWithFinalVerifyHook(expectedContentHash string, next BaseRecord, beforeFinalVerify func() error) (retErr error) {
	if err := validateBaseRecord(next, next.EntityID); err != nil {
		return err
	}
	bases, _, err := s.openBaseDirectory(true)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, bases.Close()) }()
	lock, err := acquireBaseStoreLock(bases)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.release()) }()
	defer func() {
		retErr = errors.Join(retErr, verifyBaseDirectoryIdentityWithHook(s.Root, bases, beforeFinalVerify))
	}()

	primaryName := baseRecordName(next.EntityID)
	backupName := atomicfile.BackupPath(primaryName)
	if err := rejectExpectedStateCaseCollision(bases, primaryName, backupName, baseLockName); err != nil {
		return err
	}
	records, err := loadAllBaseRecords(bases)
	if err != nil {
		return err
	}
	var current BaseRecord
	found := false
	for _, candidate := range records {
		if candidate.EntityID == next.EntityID {
			current = candidate
			found = true
			break
		}
	}
	currentHash := ""
	if found {
		currentHash = current.ContentHash
	}
	if currentHash != expectedContentHash {
		return ErrStaleBase
	}
	encoded, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return errors.New("cannot encode merge-base state")
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxBaseBytes {
		return errors.New("merge-base state exceeds size limit")
	}
	if err := atomicfile.WriteRoot(bases, primaryName, encoded, 0o600); err != nil {
		return fmt.Errorf("persist merge-base state: %w", err)
	}
	info, found, err := regularBaseEntry(bases, primaryName)
	if err != nil || !found {
		return errors.New("merge-base state is missing after commit")
	}
	written, err := readBaseRecord(bases, primaryName, next.EntityID, info)
	if err != nil || !reflect.DeepEqual(written, next) {
		return errors.New("merge-base state failed post-write verification")
	}
	return nil
}

func baseRecordPath(entityID string) string {
	return filepath.ToSlash(filepath.Join(baseDirectoryName, baseRecordName(entityID)))
}

func baseRecordName(entityID string) string {
	digest := sha256.Sum256([]byte(entityID))
	return hex.EncodeToString(digest[:]) + ".json"
}

func (s BaseStore) openBaseDirectory(create bool) (*os.Root, bool, error) {
	if s.Root == nil {
		return nil, false, errors.New("merge-base root is required")
	}
	rootBefore, err := s.Root.Stat(".")
	if err != nil || rootBefore == nil || !rootBefore.IsDir() || isStateRedirect(rootBefore) {
		return nil, false, errors.New("merge-base root is redirected or invalid")
	}
	before, err := s.Root.Lstat(baseDirectoryName)
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil, false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, errors.New("cannot inspect merge-base directory")
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := atomicfile.EnsureRootDir(s.Root, baseDirectoryName, 0o700); err != nil {
			return nil, false, fmt.Errorf("create merge-base directory: %w", err)
		}
		before, err = s.Root.Lstat(baseDirectoryName)
	}
	if err != nil || before == nil || !before.IsDir() || isStateRedirect(before) {
		return nil, false, errors.New("merge-base directory is redirected or invalid")
	}
	bases, err := s.Root.OpenRoot(baseDirectoryName)
	if err != nil {
		return nil, false, errors.New("cannot open merge-base directory")
	}
	opened, err := bases.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		_ = bases.Close()
		return nil, false, errors.New("merge-base directory changed while opening")
	}
	if create {
		file, err := bases.Open(".")
		if err != nil {
			_ = bases.Close()
			return nil, false, errors.New("cannot protect merge-base directory")
		}
		protectErr := file.Chmod(0o700)
		closeErr := file.Close()
		if err := errors.Join(protectErr, closeErr); err != nil {
			_ = bases.Close()
			return nil, false, errors.New("cannot protect merge-base directory")
		}
		opened, err = bases.Stat(".")
		if err != nil || !os.SameFile(before, opened) || !privateStateMode(opened, 0o700) {
			_ = bases.Close()
			return nil, false, errors.New("cannot verify merge-base directory permissions")
		}
	} else if !privateStateMode(opened, 0o700) {
		_ = bases.Close()
		return nil, false, errors.New("merge-base directory permissions are not private")
	}
	after, err := s.Root.Lstat(baseDirectoryName)
	rootAfter, rootErr := s.Root.Stat(".")
	if err != nil || rootErr != nil || !os.SameFile(before, after) || !os.SameFile(rootBefore, rootAfter) || isStateRedirect(after) || !after.IsDir() || !privateStateMode(opened, 0o700) || !privateStateMode(after, 0o700) {
		_ = bases.Close()
		return nil, false, errors.New("merge-base directory changed while opening")
	}
	return bases, true, nil
}

type baseStatePair struct {
	primary string
	backup  string
}

func inspectBaseStateNames(root *os.Root) ([]baseStatePair, error) {
	entries, err := readBoundedSyncStateEntries(root, maxSyncStateDirectoryEntries, "merge-base directory")
	if err != nil {
		return nil, errors.New("cannot inspect merge-base directory")
	}
	seenFolded := make(map[string]string, len(entries))
	pairs := make(map[string]baseStatePair)
	for _, entry := range entries {
		name := entry.Name()
		if name == baseLockName {
			if err := validateIgnoredBaseEntry(root, name, "merge-base lock"); err != nil {
				return nil, err
			}
			continue
		}
		if baseTempName.MatchString(name) {
			if err := validateIgnoredBaseEntry(root, name, "merge-base temporary"); err != nil {
				return nil, err
			}
			continue
		}
		folded := strings.ToLower(name)
		if previous, found := seenFolded[folded]; found && previous != name {
			return nil, errors.New("merge-base state names collide case-insensitively")
		}
		seenFolded[folded] = name
		backupSuffix := strings.TrimPrefix(atomicfile.BackupPath("x"), "x")
		baseName := name
		isBackup := false
		if strings.HasSuffix(baseName, backupSuffix) {
			baseName = strings.TrimSuffix(baseName, backupSuffix)
			isBackup = true
		}
		if !baseJSONName.MatchString(baseName) || baseName != strings.ToLower(baseName) {
			return nil, errors.New("invalid merge-base state filename")
		}
		pair := pairs[baseName]
		pair.primary = baseName
		if isBackup {
			pair.backup = name
		}
		pairs[baseName] = pair
	}
	result := make([]baseStatePair, 0, len(pairs))
	for _, pair := range pairs {
		result = append(result, pair)
	}
	sort.Slice(result, func(first, second int) bool { return result[first].primary < result[second].primary })
	return result, nil
}

func validateIgnoredBaseEntry(root *os.Root, name, label string) error {
	info, err := root.Lstat(name)
	if err != nil || info == nil || isStateRedirect(info) || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is redirected or not regular", label)
	}
	if !privateStateMode(info, 0o600) {
		return fmt.Errorf("%s permissions are not private", label)
	}
	return nil
}

func loadAllBaseRecords(root *os.Root) ([]BaseRecord, error) {
	pairs, err := inspectBaseStateNames(root)
	if err != nil {
		return nil, err
	}
	records := make([]BaseRecord, 0, len(pairs))
	seenIDs := make(map[string]struct{}, len(pairs))
	for _, pair := range pairs {
		record, found, err := loadBasePairByNames(root, pair.primary, pair.backup, "")
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if baseRecordName(record.EntityID) != pair.primary {
			return nil, errors.New("merge-base record filename does not certify its entity ID")
		}
		if _, duplicate := seenIDs[record.EntityID]; duplicate {
			return nil, errors.New("duplicate merge-base entity ID")
		}
		seenIDs[record.EntityID] = struct{}{}
		records = append(records, record)
	}
	sort.Slice(records, func(first, second int) bool {
		return records[first].EntityID < records[second].EntityID
	})
	return records, nil
}

func rejectExpectedStateCaseCollision(root *os.Root, allowed ...string) error {
	entries, err := readBoundedSyncStateEntries(root, maxSyncStateDirectoryEntries, "merge-base directory")
	if err != nil {
		return errors.New("cannot inspect merge-base directory")
	}
	for _, entry := range entries {
		for _, name := range allowed {
			if strings.EqualFold(entry.Name(), name) && entry.Name() != name {
				return errors.New("merge-base state names collide case-insensitively")
			}
		}
	}
	return nil
}

func loadBasePair(root *os.Root, entityID string) (BaseRecord, bool, error) {
	primary := baseRecordName(entityID)
	return loadBasePairByNames(root, primary, atomicfile.BackupPath(primary), entityID)
}

func loadBasePairByNames(root *os.Root, primary, backup, entityID string) (BaseRecord, bool, error) {
	primaryInfo, primaryFound, primaryInspectErr := regularBaseEntry(root, primary)
	if primaryInspectErr != nil {
		return BaseRecord{}, false, primaryInspectErr
	}
	backupInfo, backupFound, backupInspectErr := regularBaseEntry(root, backup)
	if backupInspectErr != nil {
		return BaseRecord{}, false, backupInspectErr
	}
	if !primaryFound && !backupFound {
		return BaseRecord{}, false, nil
	}
	if primaryFound {
		if record, err := readBaseRecord(root, primary, entityID, primaryInfo); err == nil {
			return record, true, nil
		}
	}
	if backupFound {
		if record, err := readBaseRecord(root, backup, entityID, backupInfo); err == nil {
			return record, true, nil
		}
	}
	return BaseRecord{}, false, errors.New("merge-base state and recovery backup are corrupt")
}

func regularBaseEntry(root *os.Root, name string) (os.FileInfo, bool, error) {
	if name == "" {
		return nil, false, nil
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.New("cannot inspect merge-base state")
	}
	if isStateRedirect(info) || !info.Mode().IsRegular() {
		return nil, true, errors.New("merge-base state is redirected or not regular")
	}
	if !privateStateMode(info, 0o600) {
		return nil, true, errors.New("merge-base state permissions are not private")
	}
	return info, true, nil
}

func readBaseRecord(root *os.Root, name, entityID string, before os.FileInfo) (BaseRecord, error) {
	return readBaseRecordWithHook(root, name, entityID, before, nil)
}

func readBaseRecordWithHook(root *os.Root, name, entityID string, before os.FileInfo, afterRead func() error) (BaseRecord, error) {
	if before.Size() > maxBaseBytes {
		return BaseRecord{}, errors.New("merge-base state exceeds size limit")
	}
	file, err := root.Open(name)
	if err != nil {
		return BaseRecord{}, errors.New("cannot open merge-base state")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameBaseFileMetadata(before, opened) || !opened.Mode().IsRegular() || isStateRedirect(opened) {
		return BaseRecord{}, errors.New("merge-base state changed while opening")
	}
	encoded, err := readBoundedBaseSnapshot(file)
	if err != nil {
		return BaseRecord{}, errors.New("merge-base state is corrupt")
	}
	if afterRead != nil {
		if err := afterRead(); err != nil {
			return BaseRecord{}, err
		}
	}
	middle, err := file.Stat()
	if err != nil || !sameBaseFileMetadata(opened, middle) {
		return BaseRecord{}, errors.New("merge-base state changed while reading")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return BaseRecord{}, errors.New("merge-base state cannot be reread")
	}
	second, err := readBoundedBaseSnapshot(file)
	if err != nil {
		return BaseRecord{}, errors.New("merge-base state is corrupt")
	}
	afterOpen, err := file.Stat()
	afterName, nameErr := root.Lstat(name)
	if err != nil || nameErr != nil || !sameBaseFileMetadata(opened, afterOpen) || !sameBaseFileMetadata(opened, afterName) || isStateRedirect(afterName) || !afterName.Mode().IsRegular() || !bytes.Equal(encoded, second) {
		return BaseRecord{}, errors.New("merge-base state changed while reading")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var record BaseRecord
	if err := decoder.Decode(&record); err != nil {
		return BaseRecord{}, errors.New("merge-base state is corrupt")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return BaseRecord{}, errors.New("merge-base state is corrupt")
	}
	if err := validateBaseRecord(record, entityID); err != nil {
		return BaseRecord{}, err
	}
	return record, nil
}

func readBoundedBaseSnapshot(file *os.File) ([]byte, error) {
	encoded, err := io.ReadAll(io.LimitReader(file, maxBaseBytes+1))
	if err != nil || len(encoded) > maxBaseBytes || !utf8.Valid(encoded) {
		return nil, errors.New("merge-base state is corrupt")
	}
	return encoded, nil
}

func sameBaseFileMetadata(first, second os.FileInfo) bool {
	return first != nil && second != nil &&
		os.SameFile(first, second) &&
		first.Size() == second.Size() &&
		first.Mode() == second.Mode() &&
		first.ModTime().Equal(second.ModTime())
}

func verifyBaseDirectoryIdentity(parent, bases *os.Root) error {
	if parent == nil || bases == nil {
		return errors.New("merge-base parent and pinned directory are required")
	}
	pinned, err := bases.Stat(".")
	if err != nil || pinned == nil || !pinned.IsDir() || isStateRedirect(pinned) || !privateStateMode(pinned, 0o700) {
		return errors.New("pinned merge-base directory is invalid")
	}
	namespace, err := parent.Lstat(baseDirectoryName)
	if err != nil || namespace == nil || !namespace.IsDir() || isStateRedirect(namespace) || !os.SameFile(pinned, namespace) || !privateStateMode(namespace, 0o700) {
		return errors.New("merge-base directory namespace identity changed")
	}
	reopened, err := parent.OpenRoot(baseDirectoryName)
	if err != nil {
		return errors.New("cannot re-open merge-base directory")
	}
	defer reopened.Close()
	opened, err := reopened.Stat(".")
	if err != nil || !os.SameFile(namespace, opened) || !os.SameFile(pinned, opened) || !opened.IsDir() || isStateRedirect(opened) || !privateStateMode(opened, 0o700) {
		return errors.New("merge-base directory changed while re-opening")
	}
	return nil
}

func verifyBaseDirectoryIdentityWithHook(parent, bases *os.Root, beforeFinalVerify func() error) error {
	if beforeFinalVerify != nil {
		if err := beforeFinalVerify(); err != nil {
			return err
		}
	}
	return verifyBaseDirectoryIdentity(parent, bases)
}

func validateBaseRecord(record BaseRecord, expectedEntityID string) error {
	if record.Version != 1 {
		return errors.New("invalid merge-base record version")
	}
	if !stableBaseID.MatchString(record.EntityID) || (expectedEntityID != "" && record.EntityID != expectedEntityID) {
		return errors.New("merge-base record entity ID mismatch")
	}
	if record.RelativePath == "" || strings.Contains(record.RelativePath, "\\") || !strings.EqualFold(filepath.Ext(record.RelativePath), ".md") {
		return errors.New("invalid merge-base relative path")
	}
	if _, err := platform.PathKey("darwin", platform.CaseSensitive, record.RelativePath); err != nil {
		return errors.New("invalid merge-base relative path")
	}
	if len(record.Content) > maxBaseContent || !utf8.Valid(record.Content) || bytes.IndexByte(record.Content, 0) >= 0 {
		return errors.New("invalid merge-base content")
	}
	digest := sha256.Sum256(record.Content)
	wantHash := hex.EncodeToString(digest[:])
	if !lowerSHA256.MatchString(record.ContentHash) || record.ContentHash != wantHash {
		return errors.New("merge-base content hash mismatch")
	}
	if record.ProjectHash != record.ContentHash || !lowerSHA256.MatchString(record.ProjectHash) {
		return errors.New("merge-base project hash is not the verified content hash")
	}
	if record.VaultHash != record.ContentHash || !lowerSHA256.MatchString(record.VaultHash) {
		return errors.New("merge-base vault hash is not the verified content hash")
	}
	if record.SyncedAt.IsZero() {
		return errors.New("merge-base synchronization time is required")
	}
	return nil
}

func privateStateMode(info os.FileInfo, want fs.FileMode) bool {
	return runtime.GOOS == "windows" || info.Mode().Perm() == want
}

func isStateRedirect(info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return false
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	attributes := value.FieldByName("FileAttributes")
	if !attributes.IsValid() || !attributes.CanUint() {
		return false
	}
	const fileAttributeReparsePoint = 0x400
	return attributes.Uint()&fileAttributeReparsePoint != 0
}
