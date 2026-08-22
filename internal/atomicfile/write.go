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
	tmp, tmpName, err := createRootTemp(root, filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = root.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return replaceRootFile(root, tmpName, path)
}

func createRootTemp(root *os.Root, dir string) (*os.File, string, error) {
	for range 100 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", fmt.Errorf("generate temporary name: %w", err)
		}
		name := filepath.Join(dir, ".session-reviewer-"+hex.EncodeToString(random[:]))
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
