package pathguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Directory is an opened directory reached without following redirects in any
// existing path component. Ancestors contains stable identities from the
// filesystem root through the opened directory.
type Directory struct {
	Root      *os.Root
	Path      string
	Ancestors []os.FileInfo
}

func Open(path string) (*Directory, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("directory path is required")
	}
	directory, remaining, err := OpenDeepest(path)
	if err != nil {
		return nil, err
	}
	if len(remaining) != 0 {
		_ = directory.Close()
		return nil, fmt.Errorf("directory does not exist: %w", os.ErrNotExist)
	}
	return directory, nil
}

// OpenDeepest opens the deepest existing directory in path and returns the
// remaining components. Every existing component is checked before and after
// opening so a redirect or replacement race cannot change the bound identity.
func OpenDeepest(path string) (*Directory, []string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, fmt.Errorf("make path absolute: %w", err)
	}
	absolute = filepath.Clean(absolute)
	absolute = canonicalDarwinSystemAlias(absolute)
	rootPath, components, err := splitAbsolute(absolute)
	if err != nil {
		return nil, nil, err
	}
	current, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open filesystem root: %w", err)
	}
	rootInfo, err := current.Stat(".")
	if err != nil {
		_ = current.Close()
		return nil, nil, fmt.Errorf("inspect filesystem root: %w", err)
	}
	identities := []os.FileInfo{rootInfo}
	openedPath := rootPath
	for index, component := range components {
		before, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			return &Directory{Root: current, Path: openedPath, Ancestors: identities}, components[index:], nil
		}
		if err != nil {
			_ = current.Close()
			return nil, nil, fmt.Errorf("inspect path component: %w", err)
		}
		if isRedirect(before) || !before.IsDir() {
			_ = current.Close()
			return nil, nil, fmt.Errorf("path component is a symlink or reparse point, or not a directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, nil, fmt.Errorf("open path component: %w", err)
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(before, opened) {
			_ = next.Close()
			_ = current.Close()
			return nil, nil, fmt.Errorf("path component changed while opening")
		}
		_ = current.Close()
		current = next
		openedPath = filepath.Join(openedPath, component)
		identities = append(identities, opened)
	}
	return &Directory{Root: current, Path: openedPath, Ancestors: identities}, nil, nil
}

func canonicalDarwinSystemAlias(path string) string {
	if runtime.GOOS != "darwin" {
		return path
	}
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	if len(components) == 0 || (components[0] != "var" && components[0] != "tmp" && components[0] != "etc") {
		return path
	}
	alias := filepath.Join(string(filepath.Separator), components[0])
	resolved, err := filepath.EvalSymlinks(alias)
	if err != nil || resolved == alias {
		return path
	}
	return filepath.Join(append([]string{resolved}, components[1:]...)...)
}

func (directory *Directory) Close() error {
	if directory == nil || directory.Root == nil {
		return nil
	}
	return directory.Root.Close()
}

func (directory *Directory) Info() os.FileInfo {
	if directory == nil || len(directory.Ancestors) == 0 {
		return nil
	}
	return directory.Ancestors[len(directory.Ancestors)-1]
}

func (directory *Directory) ContainsIdentity(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	for _, ancestor := range directory.Ancestors {
		if os.SameFile(ancestor, info) {
			return true
		}
	}
	return false
}

// OpenRegular opens one regular file below directory without following a
// redirect in any component and returns the identity pinned by the file handle.
func (directory *Directory) OpenRegular(path string) (*os.File, os.FileInfo, error) {
	if directory == nil || directory.Root == nil {
		return nil, nil, fmt.Errorf("directory root is required")
	}
	path = filepath.Clean(filepath.FromSlash(path))
	if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return nil, nil, fmt.Errorf("invalid relative file path")
	}
	components := strings.Split(path, string(filepath.Separator))
	current := directory.Root
	owned := false
	for _, component := range components[:len(components)-1] {
		before, err := current.Lstat(component)
		if err != nil || isRedirect(before) || !before.IsDir() {
			if owned {
				_ = current.Close()
			}
			return nil, nil, fmt.Errorf("file parent is redirected or not a directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			if owned {
				_ = current.Close()
			}
			return nil, nil, fmt.Errorf("open file parent: %w", err)
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(before, opened) {
			_ = next.Close()
			if owned {
				_ = current.Close()
			}
			return nil, nil, fmt.Errorf("file parent changed while opening")
		}
		if owned {
			_ = current.Close()
		}
		current = next
		owned = true
	}
	if owned {
		defer current.Close()
	}
	name := components[len(components)-1]
	before, err := current.Lstat(name)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect file: %w", err)
	}
	if isRedirect(before) || !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("file is redirected or not regular")
	}
	file, err := current.Open(name)
	if err != nil {
		return nil, nil, fmt.Errorf("open file: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("file changed while opening")
	}
	return file, opened, nil
}

// OpenDirectory opens an existing descendant directory relative to an already
// pinned root. Every component is rejected if redirected and checked against
// the handle that was actually opened.
func (directory *Directory) OpenDirectory(path string) (*os.Root, os.FileInfo, error) {
	if directory == nil || directory.Root == nil {
		return nil, nil, fmt.Errorf("directory root is required")
	}
	path = filepath.Clean(filepath.FromSlash(path))
	if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return nil, nil, fmt.Errorf("invalid relative directory path")
	}
	components := strings.Split(path, string(filepath.Separator))
	current := directory.Root
	owned := false
	var openedInfo os.FileInfo
	for _, component := range components {
		before, err := current.Lstat(component)
		if err != nil {
			if owned {
				_ = current.Close()
			}
			return nil, nil, err
		}
		if isRedirect(before) || !before.IsDir() {
			if owned {
				_ = current.Close()
			}
			return nil, nil, fmt.Errorf("directory is redirected or not a directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			if owned {
				_ = current.Close()
			}
			return nil, nil, fmt.Errorf("open directory: %w", err)
		}
		openedInfo, err = next.Stat(".")
		if err != nil || !os.SameFile(before, openedInfo) {
			_ = next.Close()
			if owned {
				_ = current.Close()
			}
			return nil, nil, fmt.Errorf("directory changed while opening")
		}
		if owned {
			_ = current.Close()
		}
		current = next
		owned = true
	}
	return current, openedInfo, nil
}

func SameDirectory(first, second string) (bool, error) {
	firstDirectory, err := Open(first)
	if err != nil {
		return false, err
	}
	defer firstDirectory.Close()
	secondDirectory, err := Open(second)
	if err != nil {
		return false, err
	}
	defer secondDirectory.Close()
	return os.SameFile(firstDirectory.Info(), secondDirectory.Info()), nil
}

func splitAbsolute(path string) (string, []string, error) {
	volume := filepath.VolumeName(path)
	root := volume + string(filepath.Separator)
	if volume == "" {
		root = string(filepath.Separator)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", nil, fmt.Errorf("path is not absolute")
	}
	if relative == "." {
		return root, nil, nil
	}
	components := strings.Split(relative, string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return "", nil, fmt.Errorf("invalid path component")
		}
	}
	return root, components, nil
}
