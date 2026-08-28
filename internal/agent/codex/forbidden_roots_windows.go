//go:build windows

package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func canonicalPathEqual(first, second string) bool {
	return strings.EqualFold(filepath.Clean(first), filepath.Clean(second))
}

func validateForbiddenRootPlatform(path string, _ *os.File) error {
	return validateNoReparseComponents(path)
}

func physicalDirectoriesOverlap(first, second *os.File) (bool, error) {
	firstPath, err := finalDirectoryPath(first)
	if err != nil {
		return false, err
	}
	secondPath, err := finalDirectoryPath(second)
	if err != nil {
		return false, err
	}
	return pathsOverlap(firstPath, secondPath), nil
}

func finalDirectoryPath(file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("directory identity is unavailable")
	}
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil || length == 0 || length >= uint32(len(buffer)) {
		return "", errors.New("final directory path is unavailable")
	}
	path := windows.UTF16ToString(buffer[:length])
	if strings.HasPrefix(path, `\\?\UNC\`) {
		path = `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
	} else {
		path = strings.TrimPrefix(path, `\\?\`)
	}
	return filepath.Clean(path), nil
}
