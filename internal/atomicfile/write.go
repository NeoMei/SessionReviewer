package atomicfile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	tmp, tmpName, err := createRootTemp(parent)
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = parent.Remove(tmpName)
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
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = ops.publish(parent, tmpName, name); err != nil {
		return err
	}
	if err = ops.syncPublication(parent, name); err != nil {
		return fmt.Errorf("sync published file metadata: %w", err)
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
