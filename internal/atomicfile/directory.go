package atomicfile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/neomei/SessionReviewer/internal/platform"
)

var (
	ErrRootDirectoryIdentityChanged      = errors.New("root directory identity changed during creation")
	ErrRootDirectoryPublicationCollision = errors.New("root directory publication collided")
)

const rootDirectoryTemporaryPrefix = ".session-reviewer-directory-"
const rootDirectoryQuarantinePrefix = ".session-reviewer-directory-quarantine-"
const rootDirectoryLockName = ".session-reviewer-directory.lock"

// IsRootDirectoryTemporaryName reports the exact bounded name shape reserved
// for an unpublished rooted directory. Inventory scanners can reject a
// process-crash residue instead of treating it as user or migration content.
func IsRootDirectoryTemporaryName(name string) bool {
	return isRootDirectoryMachineName(name, rootDirectoryTemporaryPrefix)
}

// IsRootDirectoryQuarantineName reports the reserved recoverable name used
// when an unexpected final-window directory occupant has already been moved.
func IsRootDirectoryQuarantineName(name string) bool {
	return isRootDirectoryMachineName(name, rootDirectoryQuarantinePrefix)
}

// IsRootDirectoryLockName reports the one persistent advisory-lock leaf
// reserved inside each pinned parent used for directory publication.
func IsRootDirectoryLockName(name string) bool {
	return name == rootDirectoryLockName
}

// IsRootDirectoryLockLikeName reserves the lock leaf namespace so aliases or
// extra lock-looking content cannot be mistaken for user inventory.
func IsRootDirectoryLockLikeName(name string) bool {
	key, ok := rootDirectoryPortableComponentKey(name)
	return ok && strings.HasPrefix(key, rootDirectoryLockName)
}

func rootDirectoryPortableComponentKey(name string) (string, bool) {
	key, err := platform.PathKey("windows", platform.CaseInsensitive, name)
	return key, err == nil
}

func isRootDirectoryMachineName(name, prefix string) bool {
	encoded := strings.TrimPrefix(name, prefix)
	if encoded == name || len(encoded) != 32 {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func randomRootDirectoryMachineName(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

// EnsureRootDir creates exactly one directory below an existing, pinned
// parent and does not return success until the parent's directory entry has
// passed the platform durability operation. Existing directories are resynced
// so a retry can resolve an earlier create-then-sync failure.
func EnsureRootDir(root *os.Root, path string, perm fs.FileMode) error {
	return EnsureRootDirChecked(root, path, perm, nil)
}

// EnsureRootDirChecked revalidates caller authority immediately before every
// creation/publication/protection mutation used to establish a missing rooted
// directory. Existing entries invoke no callback unless their handle mode must
// be protected by the higher-level caller.
func EnsureRootDirChecked(root *os.Root, path string, perm fs.FileMode, checkpoint func() error) error {
	var beforePublish func(*os.Root, string, string) error
	if checkpoint != nil {
		beforePublish = func(*os.Root, string, string) error { return checkpoint() }
	}
	_, err := ensureRootDirPreparedWithOps(root, path, perm, checkpoint, nil, directoryDurabilityOps{
		beforeParentLock:       checkpoint,
		beforeCreate:           checkpoint,
		beforeDirectoryPublish: beforePublish,
		syncParent:             syncRootDirectoryEntry,
	})
	return err
}

// EnsureRootDirCreated is EnsureRootDir plus an ownership result. created is
// true only when this call's identity-bound creation was published at the
// requested leaf. A pre-existing entry or no-replace publication winner
// reports false and must not be silently repaired.
func EnsureRootDirCreated(root *os.Root, path string, perm fs.FileMode) (created bool, err error) {
	return ensureRootDirCreatedWithOps(root, path, perm, directoryDurabilityOps{syncParent: syncRootDirectoryEntry})
}

// EnsureRootDirPrepared keeps an identity-bound handle to a directory created
// by this invocation. Cooperative SessionReviewer processes serialize the
// complete operation with an advisory exclusive lock file opened relative to
// the pinned parent, so pathname replacement cannot redirect the lock outside
// that namespace.
// POSIX restores the requested mode on the retained handle before no-replace
// publication, so the process umask cannot weaken the exact-mode contract.
// Callers that deliberately ignore the advisory lock are outside the ownership
// guarantee; detected substitutions are preserved and reported, never removed
// or overwritten. Existing entries never change and never invoke callbacks.
func EnsureRootDirPrepared(root *os.Root, path string, perm fs.FileMode, beforePrepare func() error, prepare func(*os.File) error) (created bool, err error) {
	return ensureRootDirPreparedWithOps(root, path, perm, beforePrepare, prepare, directoryDurabilityOps{syncParent: syncRootDirectoryEntry})
}

type directoryDurabilityOps struct {
	createDirectory              func(*os.Root, string, fs.FileMode) (*os.File, error)
	beforeParentLock             func() error
	beforeCreate                 func() error
	afterStagingIdentity         func(*os.Root, string, string) error
	beforeDirectoryPublish       func(*os.Root, string, string) error
	afterStagingPublicationCheck func(*os.Root, string, string) error
	syncParent                   func(*os.Root, string) error
}

type rootDirectoryCreation struct {
	file         *os.File
	info         os.FileInfo
	published    bool
	recoveryName string
	publish      func() error
}

type rootDirectoryCreationHooks struct {
	afterStagingIdentity         func(*os.Root, string, string) error
	beforeDirectoryPublish       func(*os.Root, string, string) error
	afterStagingPublicationCheck func(*os.Root, string, string) error
}

func ensureRootDirWithOps(root *os.Root, path string, perm fs.FileMode, ops directoryDurabilityOps) error {
	_, err := ensureRootDirCreatedWithOps(root, path, perm, ops)
	return err
}

func ensureRootDirCreatedWithOps(root *os.Root, path string, perm fs.FileMode, ops directoryDurabilityOps) (bool, error) {
	return ensureRootDirPreparedWithOps(root, path, perm, nil, nil, ops)
}

func ensureRootDirPreparedWithOps(root *os.Root, path string, perm fs.FileMode, beforePrepare func() error, prepare func(*os.File) error, ops directoryDurabilityOps) (created bool, retErr error) {
	if root == nil {
		return false, fmt.Errorf("atomic file root is required")
	}
	parent, name, err := openPinnedParent(root, path)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	if ops.beforeParentLock != nil {
		if err := ops.beforeParentLock(); err != nil {
			return false, err
		}
	}
	releaseParentLock, err := lockRootDirectoryParent(parent)
	if err != nil {
		return false, fmt.Errorf("lock directory creation parent: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, releaseParentLock()) }()

	info, err := parent.Lstat(name)
	if err == nil {
		if err := validateRootDirectory(parent, name, info); err != nil {
			return false, err
		}
		if err := ops.syncParent(parent, name); err != nil {
			return false, fmt.Errorf("sync existing directory entry: %w", err)
		}
		return false, validateRootDirectoryIdentity(parent, name, info)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect directory: %w", err)
	}
	if ops.beforeCreate != nil {
		if err := ops.beforeCreate(); err != nil {
			return false, err
		}
	}
	var creation *rootDirectoryCreation
	if ops.createDirectory != nil {
		createdFile, createErr := ops.createDirectory(parent, name, perm)
		if createErr == nil {
			creation = &rootDirectoryCreation{file: createdFile, published: true}
		}
		err = createErr
	} else {
		creation, err = createRootDirectoryFile(parent, name, perm, rootDirectoryCreationHooks{
			afterStagingIdentity:         ops.afterStagingIdentity,
			beforeDirectoryPublish:       ops.beforeDirectoryPublish,
			afterStagingPublicationCheck: ops.afterStagingPublicationCheck,
		})
	}
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return false, fmt.Errorf("create directory: %w", err)
		}
		info, err = parent.Lstat(name)
		if err != nil {
			return false, fmt.Errorf("inspect concurrently created directory: %w", err)
		}
		if err := validateRootDirectory(parent, name, info); err != nil {
			return false, err
		}
		if err := ops.syncParent(parent, name); err != nil {
			return false, fmt.Errorf("sync concurrently created directory entry: %w", err)
		}
		return false, validateRootDirectoryIdentity(parent, name, info)
	}
	if creation == nil || creation.file == nil {
		return false, ErrRootDirectoryIdentityChanged
	}
	defer func() {
		retErr = errors.Join(retErr, creation.file.Close())
	}()
	createdInfo := creation.info
	openedInfo, err := creation.file.Stat()
	if createdInfo == nil {
		createdInfo = openedInfo
	}
	if err != nil || createdInfo == nil || !createdInfo.IsDir() || isAtomicRedirect(createdInfo) || !os.SameFile(createdInfo, openedInfo) {
		return false, ErrRootDirectoryIdentityChanged
	}
	modePrepared := false
	if !creation.published {
		if err := setExactCreatedRootDirectoryMode(creation.file, perm); err != nil {
			return false, fmt.Errorf("set exact staged directory mode: %w", err)
		}
		if err := syncCreatedRootDirectory(creation.file); err != nil {
			return false, fmt.Errorf("sync staged directory handle: %w", err)
		}
		modePrepared = true
		if creation.publish == nil {
			return false, ErrRootDirectoryIdentityChanged
		}
		if err := creation.publish(); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return false, fmt.Errorf("publish directory: %w", err)
			}
			info, err = parent.Lstat(name)
			if err != nil {
				return false, fmt.Errorf("inspect concurrently published directory: %w", err)
			}
			if err := validateRootDirectory(parent, name, info); err != nil {
				return false, err
			}
			return false, fmt.Errorf("%w: owned staging retained at %s", ErrRootDirectoryPublicationCollision, creation.recoveryName)
		}
		creation.published = true
	}
	current, err := parent.Lstat(name)
	if err != nil || !os.SameFile(createdInfo, current) || !current.IsDir() || isAtomicRedirect(current) {
		quarantine, quarantineErr := quarantineUnexpectedRootDirectory(parent, name, current)
		if quarantine != "" {
			return false, errors.Join(fmt.Errorf("%w: unexpected publication preserved at %s", ErrRootDirectoryIdentityChanged, quarantine), quarantineErr)
		}
		return false, errors.Join(ErrRootDirectoryIdentityChanged, quarantineErr)
	}
	if beforePrepare != nil {
		if err := beforePrepare(); err != nil {
			return true, err
		}
	}
	if !modePrepared {
		if err := setExactCreatedRootDirectoryMode(creation.file, perm); err != nil {
			return true, fmt.Errorf("set exact created directory mode: %w", err)
		}
	}
	if prepare != nil {
		if err := prepare(creation.file); err != nil {
			return true, err
		}
	}
	if err := syncCreatedRootDirectory(creation.file); err != nil {
		return true, fmt.Errorf("sync created directory handle: %w", err)
	}
	afterPrepare, err := creation.file.Stat()
	current, currentErr := parent.Lstat(name)
	if err != nil || currentErr != nil || !os.SameFile(createdInfo, afterPrepare) || !os.SameFile(createdInfo, current) {
		return true, preserveUnexpectedRootDirectory(parent, name, createdInfo, current, currentErr)
	}
	if err := ops.syncParent(parent, name); err != nil {
		return true, fmt.Errorf("sync created directory entry: %w", err)
	}
	info, err = parent.Lstat(name)
	if err != nil || !os.SameFile(createdInfo, info) {
		return true, preserveUnexpectedRootDirectory(parent, name, createdInfo, info, err)
	}
	return true, validateRootDirectoryIdentity(parent, name, createdInfo)
}

func preserveUnexpectedRootDirectory(parent *os.Root, name string, owned, current os.FileInfo, inspectErr error) error {
	if inspectErr != nil || current == nil || (owned != nil && os.SameFile(owned, current)) {
		return errors.Join(ErrRootDirectoryIdentityChanged, inspectErr)
	}
	quarantine, quarantineErr := quarantineUnexpectedRootDirectory(parent, name, current)
	if quarantine != "" {
		return errors.Join(fmt.Errorf("%w: unexpected publication preserved at %s", ErrRootDirectoryIdentityChanged, quarantine), quarantineErr)
	}
	return errors.Join(ErrRootDirectoryIdentityChanged, quarantineErr)
}

func quarantineUnexpectedRootDirectory(parent *os.Root, name string, expected os.FileInfo) (string, error) {
	for range 100 {
		quarantine, err := randomRootDirectoryMachineName(rootDirectoryQuarantinePrefix)
		if err != nil {
			return "", fmt.Errorf("generate directory quarantine name: %w", err)
		}
		if err := renameRootNoReplace(parent, name, parent, quarantine); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", fmt.Errorf("quarantine unexpected directory publication: %w", err)
		}
		moved, inspectErr := parent.Lstat(quarantine)
		if syncErr := syncRootDirectoryEntry(parent, quarantine); syncErr != nil {
			return quarantine, fmt.Errorf("sync unexpected directory quarantine: %w", syncErr)
		}
		if inspectErr != nil || expected == nil || !os.SameFile(expected, moved) {
			return quarantine, ErrRootDirectoryIdentityChanged
		}
		return quarantine, nil
	}
	return "", errors.New("create unique directory quarantine name")
}

func validateRootDirectoryIdentity(parent *os.Root, name string, before os.FileInfo) error {
	after, err := parent.Lstat(name)
	if err != nil || !os.SameFile(before, after) {
		return fmt.Errorf("directory changed while syncing")
	}
	return validateRootDirectory(parent, name, after)
}

// SyncRootPublication re-establishes the platform publication durability of
// one existing regular file through a pinned immediate parent.
func SyncRootPublication(root *os.Root, path string) error {
	return syncRootPublicationWithSync(root, path, syncRootPublication)
}

func syncRootPublicationWithSync(root *os.Root, path string, sync func(*os.Root, string) error) error {
	if root == nil {
		return fmt.Errorf("atomic file root is required")
	}
	parent, name, err := openPinnedParent(root, path)
	if err != nil {
		return err
	}
	defer parent.Close()
	before, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect published file: %w", err)
	}
	if !before.Mode().IsRegular() || isAtomicRedirect(before) {
		return fmt.Errorf("published file is redirected or not regular")
	}
	if err := sync(parent, name); err != nil {
		return fmt.Errorf("sync existing published file metadata: %w", err)
	}
	after, err := parent.Lstat(name)
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || isAtomicRedirect(after) {
		return fmt.Errorf("published file changed while syncing")
	}
	return nil
}

// SyncRootDirectory flushes the contents of an already pinned directory.
func SyncRootDirectory(root *os.Root) error {
	if root == nil {
		return fmt.Errorf("atomic file root is required")
	}
	before, err := root.Stat(".")
	if err != nil || before == nil || !before.IsDir() || isAtomicRedirect(before) {
		return fmt.Errorf("inspect pinned directory")
	}
	if err := syncPinnedDirectory(root); err != nil {
		return fmt.Errorf("sync pinned directory: %w", err)
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return fmt.Errorf("pinned directory changed while syncing")
	}
	return nil
}

func validateRootDirectory(parent *os.Root, name string, before os.FileInfo) error {
	if before == nil || !before.IsDir() || isAtomicRedirect(before) {
		return fmt.Errorf("directory is redirected or not a directory")
	}
	opened, err := parent.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}
	defer opened.Close()
	after, err := opened.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return fmt.Errorf("directory changed while opening")
	}
	return nil
}

// RenameRoot durably renames two entries in the same pinned immediate parent.
func RenameRoot(root *os.Root, oldPath, newPath string) error {
	return renameRootWithSync(root, oldPath, newPath, syncRootDirectoryEntry)
}

// RenameRootNoReplace atomically moves the entry currently named by oldPath
// to an absent newPath. It never replaces the destination. Both parents are
// pinned below root and synced after the move.
func RenameRootNoReplace(root *os.Root, oldPath, newPath string) error {
	oldParent, oldName, err := openPinnedParent(root, oldPath)
	if err != nil {
		return err
	}
	defer oldParent.Close()
	newParent, newName, err := openPinnedParent(root, newPath)
	if err != nil {
		return err
	}
	defer newParent.Close()
	if err := renameRootNoReplace(oldParent, oldName, newParent, newName); err != nil {
		return err
	}
	if err := syncRootDirectoryEntry(oldParent, oldName); err != nil {
		return fmt.Errorf("sync no-replace rename source: %w", err)
	}
	if err := syncRootDirectoryEntry(newParent, newName); err != nil {
		return fmt.Errorf("sync no-replace rename destination: %w", err)
	}
	return nil
}

func renameRootWithSync(root *os.Root, oldPath, newPath string, syncParent func(*os.Root, string) error) error {
	oldClean, err := cleanRootRelative(oldPath)
	if err != nil {
		return err
	}
	newClean, err := cleanRootRelative(newPath)
	if err != nil {
		return err
	}
	if filepath.Dir(oldClean) != filepath.Dir(newClean) {
		return fmt.Errorf("rooted durable rename requires one immediate parent")
	}
	oldParent, oldName, err := openPinnedParent(root, oldPath)
	if err != nil {
		return err
	}
	defer oldParent.Close()
	_, newName, err := splitRootRelative(newPath)
	if err != nil {
		return err
	}
	if err := oldParent.Rename(oldName, newName); err != nil {
		return err
	}
	if err := syncParent(oldParent, newName); err != nil {
		return fmt.Errorf("sync renamed directory entry: %w", err)
	}
	return nil
}

// RemoveRoot durably removes an entry from its pinned immediate parent.
func RemoveRoot(root *os.Root, path string) error {
	return RemoveRootChecked(root, path, nil)
}

// RemoveRootChecked calls checkpoint through the pinned immediate parent
// immediately before removing the authenticated entry.
func RemoveRootChecked(root *os.Root, path string, checkpoint func() error) error {
	return removeRootWithSyncChecked(root, path, checkpoint, syncRootDirectoryEntry)
}

func removeRootWithSync(root *os.Root, path string, syncParent func(*os.Root, string) error) error {
	return removeRootWithSyncChecked(root, path, nil, syncParent)
}

func removeRootWithSyncChecked(root *os.Root, path string, checkpoint func() error, syncParent func(*os.Root, string) error) error {
	parent, name, err := openPinnedParent(root, path)
	if err != nil {
		return err
	}
	defer parent.Close()
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return err
		}
	}
	if err := parent.Remove(name); err != nil {
		return err
	}
	if err := syncParent(parent, name); err != nil {
		return fmt.Errorf("sync removed directory entry: %w", err)
	}
	return nil
}

func openPinnedParent(root *os.Root, path string) (*os.Root, string, error) {
	parentPath, name, err := splitRootRelative(path)
	if err != nil {
		return nil, "", err
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, "", fmt.Errorf("pin root directory: %w", err)
	}
	if parentPath == "." {
		return current, name, nil
	}
	for _, component := range strings.Split(parentPath, string(filepath.Separator)) {
		before, err := current.Lstat(component)
		if err != nil || before == nil || !before.IsDir() || isAtomicRedirect(before) {
			_ = current.Close()
			return nil, "", fmt.Errorf("destination parent is redirected or not a directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, "", fmt.Errorf("open destination parent: %w", err)
		}
		after, err := next.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			_ = next.Close()
			_ = current.Close()
			return nil, "", fmt.Errorf("destination parent changed while opening")
		}
		_ = current.Close()
		current = next
	}
	return current, name, nil
}

func splitRootRelative(path string) (string, string, error) {
	clean, err := cleanRootRelative(path)
	if err != nil {
		return "", "", err
	}
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) {
		return "", "", fmt.Errorf("invalid rooted path")
	}
	return filepath.Dir(clean), name, nil
}

func cleanRootRelative(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", fmt.Errorf("invalid rooted path")
	}
	native := filepath.FromSlash(path)
	clean := filepath.Clean(native)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != filepath.ToSlash(native) {
		return "", fmt.Errorf("invalid rooted path")
	}
	return clean, nil
}
