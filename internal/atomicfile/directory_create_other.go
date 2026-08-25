//go:build !windows

package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

func createRootDirectoryFile(parent *os.Root, name string, _ fs.FileMode, hooks rootDirectoryCreationHooks) (*rootDirectoryCreation, error) {
	for range 100 {
		temporary, err := randomRootDirectoryMachineName(rootDirectoryTemporaryPrefix)
		if err != nil {
			return nil, fmt.Errorf("generate directory staging name: %w", err)
		}
		if err := parent.Mkdir(temporary, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return nil, err
		}
		createdInfo, err := parent.Lstat(temporary)
		if err != nil || createdInfo == nil || !createdInfo.IsDir() || isAtomicRedirect(createdInfo) {
			return nil, fmt.Errorf("%w: directory staging retained at %s", ErrRootDirectoryIdentityChanged, temporary)
		}
		if hooks.afterStagingIdentity != nil {
			if err := hooks.afterStagingIdentity(parent, temporary, name); err != nil {
				return nil, fmt.Errorf("%w: directory staging retained at %s", err, temporary)
			}
		}
		file, err := parent.Open(temporary)
		if err != nil {
			return nil, fmt.Errorf("%w: directory staging retained at %s", ErrRootDirectoryIdentityChanged, temporary)
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !os.SameFile(createdInfo, openedInfo) || !openedInfo.IsDir() || isAtomicRedirect(openedInfo) {
			return nil, errors.Join(fmt.Errorf("%w: directory staging retained at %s", ErrRootDirectoryIdentityChanged, temporary), file.Close())
		}
		return &rootDirectoryCreation{
			file:         file,
			info:         openedInfo,
			published:    false,
			recoveryName: temporary,
			publish: func() error {
				if hooks.beforeDirectoryPublish != nil {
					if err := hooks.beforeDirectoryPublish(parent, temporary, name); err != nil {
						return fmt.Errorf("%w: directory staging retained at %s", err, temporary)
					}
				}
				current, err := parent.Lstat(temporary)
				if err != nil || !os.SameFile(openedInfo, current) || !current.IsDir() || isAtomicRedirect(current) {
					return ErrRootDirectoryIdentityChanged
				}
				if hooks.afterStagingPublicationCheck != nil {
					if err := hooks.afterStagingPublicationCheck(parent, temporary, name); err != nil {
						return fmt.Errorf("%w: directory staging retained at %s", err, temporary)
					}
				}
				if err := renameRootNoReplace(parent, temporary, parent, name); err != nil {
					return fmt.Errorf("%w: directory staging retained at %s", err, temporary)
				}
				return nil
			},
		}, nil
	}
	return nil, errors.New("create unique directory staging name")
}
