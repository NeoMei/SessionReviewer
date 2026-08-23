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
	syncTemporary   func(*os.File) error
	publish         func(*os.Root, string, string) error
	syncPublication func(*os.Root, string) error
}

func defaultDurabilityOps() durabilityOps {
	return durabilityOps{
		syncTemporary:   (*os.File).Sync,
		publish:         replaceRootFile,
		syncPublication: syncRootPublication,
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
	published := false
	defer func() {
		_ = tmp.Close()
		if retErr != nil && !published {
			retErr = errors.Join(retErr, removeRootEntryIfOwned(parent, tmpName, createdInfo))
		}
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
	if checkpoint != nil {
		if err = checkpoint(); err != nil {
			return err
		}
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = ops.publish(parent, tmpName, name); err != nil {
		return err
	}
	published = true
	publishedInfo, err := parent.Lstat(name)
	if err != nil || !os.SameFile(temporaryInfo, publishedInfo) || !publishedInfo.Mode().IsRegular() || isAtomicRedirect(publishedInfo) {
		return errors.New("atomic publication identity changed")
	}
	if err = ops.syncPublication(parent, name); err != nil {
		return fmt.Errorf("sync published file metadata: %w", err)
	}
	if checkpoint != nil {
		if err = checkpoint(); err != nil {
			cleanupErr := removePublishedRootFileIfOwned(parent, name, publishedInfo, data)
			return errors.Join(err, cleanupErr)
		}
	}
	return nil
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
