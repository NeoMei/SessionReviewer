package atomicfile

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const backupSuffix = ".session-reviewer-backup"

func BackupPath(path string) string {
	return path + backupSuffix
}

func Write(path string, data []byte, perm fs.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return WriteRoot(root, filepath.Base(path), data, perm)
}

func WriteRoot(root *os.Root, path string, data []byte, perm fs.FileMode) (retErr error) {
	if root == nil {
		return fmt.Errorf("atomic file root is required")
	}
	return writeRootWithParentOpener(root, path, data, perm, root.OpenRoot)
}

// WriteRootFile atomically writes one leaf below an already pinned immediate
// parent. Callers that must hold a directory identity across validation and
// publication use this instead of re-opening the parent by path.
func WriteRootFile(parent *os.Root, leaf string, data []byte, perm fs.FileMode) error {
	return WriteRootFileChecked(parent, leaf, data, perm, nil)
}

// WriteRootFileChecked applies checkpoint before temporary creation, after
// temporary durability but before publication, and after durable publication.
// A failed post-publication checkpoint removes only a still-identical file
// whose content matches this write.
func WriteRootFileChecked(parent *os.Root, leaf string, data []byte, perm fs.FileMode, checkpoint func() error) error {
	if parent == nil {
		return fmt.Errorf("atomic file root is required")
	}
	if !strictRootLeaf(leaf) {
		return fmt.Errorf("atomic file leaf is invalid")
	}
	return writeRootAtParentCheckedWithOps(parent, leaf, data, perm, checkpoint, defaultDurabilityOps())
}

func strictRootLeaf(leaf string) bool {
	return leaf != "" && leaf != "." && leaf != ".." &&
		!strings.ContainsAny(leaf, `/\:`) && !filepath.IsAbs(leaf) &&
		filepath.VolumeName(leaf) == "" && filepath.Base(leaf) == leaf
}

func writeRootWithParentOpener(root *os.Root, path string, data []byte, perm fs.FileMode, openParent func(string) (*os.Root, error)) error {
	if root == nil {
		return fmt.Errorf("atomic file root is required")
	}
	parent, err := openParent(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open destination parent: %w", err)
	}
	defer parent.Close()
	return writeRootAtParent(parent, filepath.Base(path), data, perm)
}

func writeRootAtParent(parent *os.Root, name string, data []byte, perm fs.FileMode) (retErr error) {
	return writeRootAtParentWithOps(parent, name, data, perm, defaultDurabilityOps())
}

type durabilityOps struct {
	syncTemporary     func(*os.File) error
	sanitizeTemporary func(*os.File) error
	publish           func(*os.Root, string, string) error
	syncPublication   func(*os.Root, string) error
	linkRollback      func(*os.Root, string, string) error
}

func defaultDurabilityOps() durabilityOps {
	return durabilityOps{
		syncTemporary:     (*os.File).Sync,
		sanitizeTemporary: sanitizeRootTemporary,
		publish:           replaceRootFile,
		syncPublication:   syncRootPublication,
		linkRollback:      (*os.Root).Link,
	}
}

func writeRootAtParentWithOps(parent *os.Root, name string, data []byte, perm fs.FileMode, ops durabilityOps) (retErr error) {
	return writeRootAtParentCheckedWithOps(parent, name, data, perm, nil, ops)
}

func writeRootAtParentCheckedWithOps(parent *os.Root, name string, data []byte, perm fs.FileMode, checkpoint func() error, ops durabilityOps) (retErr error) {
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return err
		}
	}
	tmp, tmpName, err := createRootTemp(parent)
	if err != nil {
		return err
	}
	createdInfo, err := tmp.Stat()
	if err != nil || !createdInfo.Mode().IsRegular() || isAtomicRedirect(createdInfo) {
		_ = tmp.Close()
		return errors.New("cannot verify created atomic temporary file")
	}
	prepublication := true
	sanitizeOnReturn := true
	defer func() {
		if retErr != nil && sanitizeOnReturn {
			retErr = errors.Join(retErr, sanitizeAndRemoveRootTemporary(parent, tmp, tmpName, createdInfo, ops.sanitizeTemporary))
		}
		retErr = errors.Join(retErr, tmp.Close())
	}()
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = ops.syncTemporary(tmp); err != nil {
		return err
	}
	temporaryInfo, err := tmp.Stat()
	if err != nil || !os.SameFile(createdInfo, temporaryInfo) || !temporaryInfo.Mode().IsRegular() || isAtomicRedirect(temporaryInfo) {
		return errors.New("cannot verify atomic temporary file")
	}
	temporaryNameInfo, err := parent.Lstat(tmpName)
	if err != nil || !os.SameFile(temporaryInfo, temporaryNameInfo) || !temporaryNameInfo.Mode().IsRegular() || isAtomicRedirect(temporaryNameInfo) {
		return errors.New("atomic temporary file changed before publication")
	}
	rollback := rootRollback{}
	if checkpoint != nil {
		rollback, err = prepareRootRollback(parent, name, ops.linkRollback)
		if err != nil {
			return err
		}
	}
	rollbackFinalized := !rollback.active
	defer func() {
		if retErr != nil && prepublication && !rollbackFinalized {
			retErr = errors.Join(retErr, removeRootRollback(parent, rollback))
		}
	}()
	if checkpoint != nil {
		if err = checkpoint(); err != nil {
			return err
		}
	}
	if rollback.active {
		if err := validateRootRollback(parent, name, rollback, true); err != nil {
			return err
		}
	}
	if err = ops.publish(parent, tmpName, name); err != nil {
		partial, inspectErr := parent.Lstat(name)
		var partialRollbackErr error
		if inspectErr == nil && os.SameFile(temporaryInfo, partial) && partial.Mode().IsRegular() && !isAtomicRedirect(partial) {
			prepublication = false
			partialRollbackErr = rollbackRootPublication(parent, name, partial, data, rollback)
			rollbackFinalized = partialRollbackErr == nil
		} else if inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
			partialRollbackErr = errors.New("cannot inspect destination after failed atomic publication")
		}
		return errors.Join(err, partialRollbackErr)
	}
	prepublication = false
	sanitizeOnReturn = false
	publishedInfo, err := parent.Lstat(name)
	if err != nil || !os.SameFile(temporaryInfo, publishedInfo) || !publishedInfo.Mode().IsRegular() || isAtomicRedirect(publishedInfo) {
		sanitizeOnReturn = true
		return errors.New("atomic publication identity changed")
	}
	if err = ops.syncPublication(parent, name); err != nil {
		return fmt.Errorf("sync published file metadata: %w", err)
	}
	if checkpoint != nil {
		if err = checkpoint(); err != nil {
			rollbackErr := rollbackRootPublication(parent, name, publishedInfo, data, rollback)
			rollbackFinalized = rollbackErr == nil
			sanitizeOnReturn = rollbackErr != nil
			return errors.Join(err, rollbackErr)
		}
	}
	if rollback.active {
		if err := removeRootRollback(parent, rollback); err != nil {
			return err
		}
		rollbackFinalized = true
	}
	return nil
}

type rootRollback struct {
	active   bool
	name     string
	original os.FileInfo
}

func prepareRootRollback(parent *os.Root, destination string, link func(*os.Root, string, string) error) (rootRollback, error) {
	backup := BackupPath(destination)
	if _, err := parent.Lstat(backup); err == nil {
		return rootRollback{}, errors.New("atomic rollback backup already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return rootRollback{}, errors.New("cannot inspect atomic rollback backup")
	}
	original, err := parent.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return rootRollback{}, nil
	}
	if err != nil || !original.Mode().IsRegular() || isAtomicRedirect(original) {
		return rootRollback{}, errors.New("atomic destination is redirected or not regular")
	}
	file, err := parent.Open(destination)
	if err != nil {
		return rootRollback{}, errors.New("cannot open atomic destination for rollback")
	}
	opened, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(original, opened) || !opened.Mode().IsRegular() || isAtomicRedirect(opened) {
		return rootRollback{}, errors.New("atomic destination changed before rollback")
	}
	if link == nil {
		link = (*os.Root).Link
	}
	if err := link(parent, destination, backup); err != nil {
		return rootRollback{}, fmt.Errorf("cannot establish atomic rollback hardlink: %w", err)
	}
	rollback := rootRollback{active: true, name: backup, original: original}
	if err := validateRootRollback(parent, destination, rollback, true); err != nil {
		return rootRollback{}, errors.Join(err, removeRootRollback(parent, rollback))
	}
	if err := syncRootDirectoryEntry(parent, backup); err != nil {
		return rootRollback{}, errors.Join(errors.New("cannot sync atomic rollback hardlink"), removeRootRollback(parent, rollback))
	}
	if err := validateRootRollback(parent, destination, rollback, true); err != nil {
		return rootRollback{}, errors.Join(err, removeRootRollback(parent, rollback))
	}
	return rollback, nil
}

func validateRootRollback(parent *os.Root, destination string, rollback rootRollback, requireDestination bool) error {
	if !rollback.active {
		return nil
	}
	backup, err := parent.Lstat(rollback.name)
	if err != nil || !os.SameFile(rollback.original, backup) || !backup.Mode().IsRegular() || isAtomicRedirect(backup) {
		return errors.New("atomic rollback backup identity changed")
	}
	if requireDestination {
		current, err := parent.Lstat(destination)
		if err != nil || !os.SameFile(rollback.original, current) || !current.Mode().IsRegular() || isAtomicRedirect(current) {
			return errors.New("atomic destination changed after rollback backup")
		}
	}
	return nil
}

func removeRootRollback(parent *os.Root, rollback rootRollback) error {
	if !rollback.active {
		return nil
	}
	if err := validateRootRollback(parent, "", rollback, false); err != nil {
		return err
	}
	if err := parent.Remove(rollback.name); err != nil {
		return errors.New("cannot remove atomic rollback backup")
	}
	if err := syncRootDirectoryEntry(parent, rollback.name); err != nil {
		return errors.New("cannot sync atomic rollback backup removal")
	}
	return nil
}

func rollbackRootPublication(parent *os.Root, destination string, publishedInfo os.FileInfo, desired []byte, rollback rootRollback) error {
	if rollback.active {
		if err := validateRootRollback(parent, destination, rollback, false); err != nil {
			return err
		}
	}
	if err := removePublishedRootFileIfOwned(parent, destination, publishedInfo, desired); err != nil {
		return err
	}
	if !rollback.active {
		return nil
	}
	if err := parent.Link(rollback.name, destination); err != nil {
		return errors.New("cannot restore atomic rollback destination")
	}
	restored, err := parent.Lstat(destination)
	if err != nil || !os.SameFile(rollback.original, restored) || !restored.Mode().IsRegular() || isAtomicRedirect(restored) {
		return errors.New("restored atomic rollback identity changed")
	}
	if err := syncRootDirectoryEntry(parent, destination); err != nil {
		return errors.New("cannot sync restored atomic rollback destination")
	}
	return removeRootRollback(parent, rollback)
}

func sanitizeRootTemporary(file *os.File) error {
	truncateErr := file.Truncate(0)
	syncErr := file.Sync()
	return errors.Join(truncateErr, syncErr)
}

func sanitizeAndRemoveRootTemporary(parent *os.Root, file *os.File, name string, createdInfo os.FileInfo, sanitize func(*os.File) error) error {
	if sanitize == nil {
		sanitize = sanitizeRootTemporary
	}
	var sanitizeErr error
	if err := sanitize(file); err != nil {
		sanitizeErr = fmt.Errorf("cannot sanitize failed atomic temporary: %w", err)
	}
	return errors.Join(sanitizeErr, removeRootEntryIfOwned(parent, name, createdInfo))
}

func removeRootEntryIfOwned(parent *os.Root, name string, ownedInfo os.FileInfo) error {
	current, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(ownedInfo, current) || !current.Mode().IsRegular() || isAtomicRedirect(current) {
		return errors.New("atomic temporary ownership changed; not removed")
	}
	if err := parent.Remove(name); err != nil {
		return errors.New("cannot remove failed atomic temporary")
	}
	return nil
}

func removePublishedRootFileIfOwned(parent *os.Root, name string, publishedInfo os.FileInfo, desired []byte) error {
	before, err := parent.Lstat(name)
	if err != nil || !os.SameFile(publishedInfo, before) || !before.Mode().IsRegular() || isAtomicRedirect(before) {
		return errors.New("published file ownership changed; not removed")
	}
	file, err := parent.Open(name)
	if err != nil {
		return errors.New("cannot verify published file for cleanup")
	}
	opened, statErr := file.Stat()
	content, readErr := io.ReadAll(file)
	afterOpen, afterStatErr := file.Stat()
	closeErr := file.Close()
	afterName, inspectErr := parent.Lstat(name)
	wantHash := sha256.Sum256(desired)
	gotHash := sha256.Sum256(content)
	if statErr != nil || readErr != nil || afterStatErr != nil || closeErr != nil || inspectErr != nil ||
		!os.SameFile(publishedInfo, opened) || !os.SameFile(opened, afterOpen) || !os.SameFile(opened, afterName) ||
		!opened.Mode().IsRegular() || isAtomicRedirect(opened) || gotHash != wantHash {
		return errors.New("published file ownership or content changed; not removed")
	}
	final, err := parent.Lstat(name)
	if err != nil || !os.SameFile(publishedInfo, final) || !final.Mode().IsRegular() || isAtomicRedirect(final) {
		return errors.New("published file ownership changed before cleanup; not removed")
	}
	if err := parent.Remove(name); err != nil {
		return errors.New("cannot remove failed atomic publication")
	}
	if err := syncRootDirectoryEntry(parent, name); err != nil {
		return errors.New("cannot sync failed atomic publication cleanup")
	}
	return nil
}

func createRootTemp(root *os.Root) (*os.File, string, error) {
	for range 100 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", fmt.Errorf("generate temporary name: %w", err)
		}
		name := ".session-reviewer-" + hex.EncodeToString(random[:])
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("create unique temporary file")
}
