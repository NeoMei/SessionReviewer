//go:build !windows

package atomicfile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

func createRootDirectoryFile(parent *os.Root, name string, _ fs.FileMode, hooks rootDirectoryCreationHooks) (*rootDirectoryCreation, error) {
	for range 100 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("generate directory staging name: %w", err)
		}
		temporary := rootDirectoryTemporaryPrefix + hex.EncodeToString(random[:])
		if err := parent.Mkdir(temporary, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return nil, err
		}
		createdInfo, err := parent.Lstat(temporary)
		if err != nil || createdInfo == nil || !createdInfo.IsDir() || isAtomicRedirect(createdInfo) {
			return nil, errors.Join(ErrRootDirectoryIdentityChanged, cleanupRootDirectoryTemporary(parent, temporary, createdInfo))
		}
		cleanup := func() error {
			return cleanupRootDirectoryTemporary(parent, temporary, createdInfo)
		}
		if hooks.afterStagingIdentity != nil {
			if err := hooks.afterStagingIdentity(parent, temporary, name); err != nil {
				return nil, errors.Join(err, cleanup())
			}
		}
		file, err := parent.Open(temporary)
		if err != nil {
			return nil, errors.Join(ErrRootDirectoryIdentityChanged, cleanup())
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !os.SameFile(createdInfo, openedInfo) || !openedInfo.IsDir() || isAtomicRedirect(openedInfo) {
			return nil, errors.Join(ErrRootDirectoryIdentityChanged, file.Close(), cleanup())
		}
		return &rootDirectoryCreation{
			file:      file,
			info:      openedInfo,
			published: false,
			publish: func() error {
				if hooks.beforeDirectoryPublish != nil {
					if err := hooks.beforeDirectoryPublish(parent, temporary, name); err != nil {
						return err
					}
				}
				current, err := parent.Lstat(temporary)
				if err != nil || !os.SameFile(openedInfo, current) || !current.IsDir() || isAtomicRedirect(current) {
					return ErrRootDirectoryIdentityChanged
				}
				return renameRootNoReplace(parent, temporary, parent, name)
			},
			cleanup: cleanup,
		}, nil
	}
	return nil, errors.New("create unique directory staging name")
}

func cleanupRootDirectoryTemporary(parent *os.Root, name string, expected os.FileInfo) error {
	if expected == nil || !expected.IsDir() || isAtomicRedirect(expected) || !IsRootDirectoryTemporaryName(name) {
		return ErrRootDirectoryIdentityChanged
	}
	current, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) || !current.IsDir() || isAtomicRedirect(current) {
		return ErrRootDirectoryIdentityChanged
	}
	if err := parent.Remove(name); err != nil {
		return err
	}
	return syncRootDirectoryEntry(parent, name)
}
