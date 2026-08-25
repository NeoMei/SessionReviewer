package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// EnsureRootDir creates exactly one directory below an existing, pinned
// parent and does not return success until the parent's directory entry has
// passed the platform durability operation. Existing directories are resynced
// so a retry can resolve an earlier create-then-sync failure.
func EnsureRootDir(root *os.Root, path string, perm fs.FileMode) error {
	return ensureRootDirWithOps(root, path, perm, directoryDurabilityOps{
		syncParent: syncRootDirectoryEntry,
	})
}

type directoryDurabilityOps struct {
	syncParent func(*os.Root, string) error
}

func ensureRootDirWithOps(root *os.Root, path string, perm fs.FileMode, ops directoryDurabilityOps) error {
	if root == nil {
		return fmt.Errorf("atomic file root is required")
	}
	parent, name, err := openPinnedParent(root, path)
	if err != nil {
		return err
	}
	defer parent.Close()

	info, err := parent.Lstat(name)
	if err == nil {
		if err := validateRootDirectory(parent, name, info); err != nil {
			return err
		}
		if err := ops.syncParent(parent, name); err != nil {
			return fmt.Errorf("sync existing directory entry: %w", err)
		}
		return validateRootDirectoryIdentity(parent, name, info)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect directory: %w", err)
	}
	if err := parent.Mkdir(name, perm); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create directory: %w", err)
		}
		info, err = parent.Lstat(name)
		if err != nil {
			return fmt.Errorf("inspect concurrently created directory: %w", err)
		}
		if err := validateRootDirectory(parent, name, info); err != nil {
			return err
		}
		if err := ops.syncParent(parent, name); err != nil {
			return fmt.Errorf("sync concurrently created directory entry: %w", err)
		}
		return validateRootDirectoryIdentity(parent, name, info)
	}
	if err := ops.syncParent(parent, name); err != nil {
		return fmt.Errorf("sync created directory entry: %w", err)
	}
	info, err = parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect created directory: %w", err)
	}
	return validateRootDirectory(parent, name, info)
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
	return removeRootWithSync(root, path, syncRootDirectoryEntry)
}

func removeRootWithSync(root *os.Root, path string, syncParent func(*os.Root, string) error) error {
	parent, name, err := openPinnedParent(root, path)
	if err != nil {
		return err
	}
	defer parent.Close()
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
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean != path {
		return "", fmt.Errorf("invalid rooted path")
	}
	return clean, nil
}
