package pathguard

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/platform"
)

const (
	maxWalkMarkdownBytes int64 = 4 << 20
	maxReadRegularBytes  int64 = 64 << 20
)

type treeParentIdentity struct {
	parent *os.Root
	name   string
	info   os.FileInfo
}

// EnsureDirectory creates a relative directory tree below an already pinned
// root. Every component is opened and identity-checked before it becomes the
// parent for the next component.
func (directory *Directory) EnsureDirectory(relative string, perm fs.FileMode) error {
	components, err := cleanTreeRelative(relative, false)
	if err != nil {
		return err
	}
	if err := directory.validatePinnedRoot(); err != nil {
		return err
	}
	current, err := directory.Root.OpenRoot(".")
	if err != nil {
		return fmt.Errorf("pin directory root: %w", err)
	}
	defer func() { _ = current.Close() }()
	for _, component := range components {
		if err := atomicfile.EnsureRootDir(current, component, perm); err != nil {
			return fmt.Errorf("ensure directory component: %w", err)
		}
		before, err := current.Lstat(component)
		if err != nil || before == nil || !before.IsDir() || isRedirect(before) {
			return errors.New("created directory is redirected or not a directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return fmt.Errorf("open ensured directory: %w", err)
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(before, opened) {
			_ = next.Close()
			return errors.New("ensured directory changed while opening")
		}
		file, err := next.Open(".")
		if err != nil {
			_ = next.Close()
			return fmt.Errorf("open ensured directory mode handle: %w", err)
		}
		chmodErr := file.Chmod(perm)
		closeErr := file.Close()
		if err := errors.Join(chmodErr, closeErr); err != nil {
			_ = next.Close()
			return fmt.Errorf("protect ensured directory: %w", err)
		}
		after, err := current.Lstat(component)
		if err != nil || !os.SameFile(before, after) || isRedirect(after) || !after.IsDir() {
			_ = next.Close()
			return errors.New("ensured directory changed while protecting")
		}
		_ = current.Close()
		current = next
	}
	return directory.validatePinnedRoot()
}

// ReadRegular reads a bounded regular file without following redirects and
// verifies that its namespace entry still names the opened file after reading.
func (directory *Directory) ReadRegular(relative string, max int64) ([]byte, bool, error) {
	return directory.readRegularWithHook(relative, max, nil)
}

func (directory *Directory) readRegularWithHook(relative string, max int64, afterRead func() error) ([]byte, bool, error) {
	components, err := cleanTreeRelative(relative, false)
	if err != nil {
		return nil, false, err
	}
	if max < 0 || max > maxReadRegularBytes {
		return nil, false, errors.New("read limit is outside the supported range")
	}
	if err := directory.validatePinnedRoot(); err != nil {
		return nil, false, err
	}
	current, err := directory.Root.OpenRoot(".")
	if err != nil {
		return nil, false, fmt.Errorf("pin directory root: %w", err)
	}
	openedRoots := []*os.Root{current}
	defer func() {
		for index := len(openedRoots) - 1; index >= 0; index-- {
			_ = openedRoots[index].Close()
		}
	}()
	parents := make([]treeParentIdentity, 0, len(components)-1)
	for _, component := range components[:len(components)-1] {
		before, err := current.Lstat(component)
		if err != nil || before == nil || !before.IsDir() || isRedirect(before) {
			return nil, false, errors.New("file parent is redirected or not a directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return nil, false, fmt.Errorf("open file parent: %w", err)
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(before, opened) {
			_ = next.Close()
			return nil, false, errors.New("file parent changed while opening")
		}
		parents = append(parents, treeParentIdentity{parent: current, name: component, info: before})
		openedRoots = append(openedRoots, next)
		current = next
	}
	name := components[len(components)-1]
	before, err := current.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect regular file: %w", err)
	}
	if isRedirect(before) || !before.Mode().IsRegular() {
		return nil, true, errors.New("file is redirected or not regular")
	}
	if before.Size() > max {
		return nil, true, errors.New("file exceeds read limit")
	}
	file, err := current.Open(name)
	if err != nil {
		return nil, true, fmt.Errorf("open regular file: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, true, errors.New("file changed while opening")
	}
	content, err := readStableRegularSnapshot(file, before, opened, max, afterRead)
	if err != nil {
		return nil, true, err
	}
	afterOpen, err := file.Stat()
	afterName, nameErr := current.Lstat(name)
	if err != nil || nameErr != nil || !sameStableFileMetadata(opened, afterOpen) || !sameStableFileMetadata(opened, afterName) || isRedirect(afterName) || !afterName.Mode().IsRegular() {
		return nil, true, errors.New("file changed while reading")
	}
	for _, parent := range parents {
		after, err := parent.parent.Lstat(parent.name)
		if err != nil || !os.SameFile(parent.info, after) || isRedirect(after) || !after.IsDir() {
			return nil, true, errors.New("file parent changed while reading")
		}
	}
	if err := directory.validatePinnedRoot(); err != nil {
		return nil, true, err
	}
	return bytes.Clone(content), true, nil
}

// WalkMarkdown visits regular Markdown files in slash-path order. It prunes
// hidden, sync-conflict, and atomic temporary/backup subtrees before inspecting
// their contents. Redirects elsewhere are rejected, and each directory/file
// identity is checked again after it has been consumed.
func (directory *Directory) WalkMarkdown(relative string, visit func(relative string, content []byte) error) error {
	components, err := cleanTreeRelative(relative, true)
	if err != nil {
		return err
	}
	if visit == nil {
		return errors.New("Markdown visitor is required")
	}
	if err := directory.validatePinnedRoot(); err != nil {
		return err
	}
	current, err := directory.Root.OpenRoot(".")
	if err != nil {
		return fmt.Errorf("pin walk root: %w", err)
	}
	openedRoots := []*os.Root{current}
	defer func() {
		for index := len(openedRoots) - 1; index >= 0; index-- {
			_ = openedRoots[index].Close()
		}
	}()
	parents := make([]treeParentIdentity, 0, len(components))
	prefix := ""
	for _, component := range components {
		before, err := current.Lstat(component)
		if err != nil || before == nil || !before.IsDir() || isRedirect(before) {
			return errors.New("walk root is redirected or not a directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return fmt.Errorf("open walk root: %w", err)
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(before, opened) {
			_ = next.Close()
			return errors.New("walk root changed while opening")
		}
		parents = append(parents, treeParentIdentity{parent: current, name: component, info: before})
		openedRoots = append(openedRoots, next)
		current = next
		prefix = path.Join(prefix, component)
	}
	if err := walkMarkdownRoot(current, prefix, visit); err != nil {
		return err
	}
	for _, parent := range parents {
		after, err := parent.parent.Lstat(parent.name)
		if err != nil || !sameStableFileMetadata(parent.info, after) || isRedirect(after) || !after.IsDir() {
			return errors.New("walk root namespace changed while walking")
		}
	}
	return directory.validatePinnedRoot()
}

func walkMarkdownRoot(root *os.Root, prefix string, visit func(string, []byte) error) error {
	beforeDirectory, err := root.Stat(".")
	if err != nil || beforeDirectory == nil || !beforeDirectory.IsDir() || isRedirect(beforeDirectory) {
		return errors.New("walk directory is redirected or invalid")
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("enumerate Markdown directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if skipMarkdownTreeEntry(name) {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil {
			return fmt.Errorf("inspect Markdown tree entry: %w", err)
		}
		if isRedirect(info) {
			return errors.New("Markdown tree entry is redirected")
		}
		relative := path.Join(prefix, name)
		if info.IsDir() {
			child, err := root.OpenRoot(name)
			if err != nil {
				return fmt.Errorf("open Markdown directory: %w", err)
			}
			opened, err := child.Stat(".")
			if err != nil || !os.SameFile(info, opened) {
				_ = child.Close()
				return errors.New("Markdown directory changed while opening")
			}
			err = walkMarkdownRoot(child, relative, visit)
			closeErr := child.Close()
			if err := errors.Join(err, closeErr); err != nil {
				return err
			}
			after, err := root.Lstat(name)
			if err != nil || !os.SameFile(info, after) || isRedirect(after) || !after.IsDir() {
				return errors.New("Markdown directory changed while walking")
			}
			continue
		}
		if !info.Mode().IsRegular() || !strings.EqualFold(path.Ext(name), ".md") {
			continue
		}
		content, err := readMarkdownRootFile(root, name, info)
		if err != nil {
			return err
		}
		if err := visit(relative, content); err != nil {
			return err
		}
	}
	afterDirectory, err := root.Stat(".")
	if err != nil || !os.SameFile(beforeDirectory, afterDirectory) {
		return errors.New("Markdown directory changed while walking")
	}
	return nil
}

func skipMarkdownTreeEntry(name string) bool {
	return strings.HasPrefix(name, ".") ||
		strings.EqualFold(name, "sync-conflicts") ||
		strings.HasSuffix(name, strings.TrimPrefix(atomicfile.BackupPath("x"), "x"))
}

func readMarkdownRootFile(root *os.Root, name string, before os.FileInfo) ([]byte, error) {
	if before.Size() > maxWalkMarkdownBytes {
		return nil, errors.New("Markdown file exceeds read limit")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open Markdown file: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errors.New("Markdown file changed while opening")
	}
	content, err := readStableRegularSnapshot(file, before, opened, maxWalkMarkdownBytes, nil)
	if err != nil {
		return nil, err
	}
	after, err := root.Lstat(name)
	if err != nil || !sameStableFileMetadata(opened, after) || isRedirect(after) || !after.Mode().IsRegular() {
		return nil, errors.New("Markdown file changed while reading")
	}
	return bytes.Clone(content), nil
}

func readStableRegularSnapshot(file *os.File, before, opened os.FileInfo, max int64, afterFirstRead func() error) ([]byte, error) {
	if max < 0 || max > maxReadRegularBytes || !sameStableFileMetadata(before, opened) {
		return nil, errors.New("regular file changed before reading")
	}
	first, err := readBoundedRegular(file, max)
	if err != nil {
		return nil, err
	}
	if afterFirstRead != nil {
		if err := afterFirstRead(); err != nil {
			return nil, err
		}
	}
	middle, err := file.Stat()
	if err != nil || !sameStableFileMetadata(opened, middle) {
		return nil, errors.New("regular file changed during reading")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, errors.New("cannot rewind regular file snapshot")
	}
	second, err := readBoundedRegular(file, max)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !sameStableFileMetadata(opened, after) || !bytes.Equal(first, second) {
		return nil, errors.New("regular file content changed during reading")
	}
	return first, nil
}

func readBoundedRegular(file *os.File, max int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, errors.New("cannot read regular file snapshot")
	}
	if int64(len(content)) > max {
		return nil, errors.New("file exceeds read limit")
	}
	return content, nil
}

func sameStableFileMetadata(first, second os.FileInfo) bool {
	return first != nil && second != nil &&
		os.SameFile(first, second) &&
		first.Size() == second.Size() &&
		first.Mode() == second.Mode() &&
		first.ModTime().Equal(second.ModTime())
}

func (directory *Directory) validatePinnedRoot() error {
	if directory == nil || directory.Root == nil || directory.Info() == nil {
		return errors.New("directory root is required")
	}
	current, err := directory.Root.Stat(".")
	if err != nil || !os.SameFile(directory.Info(), current) || !current.IsDir() || isRedirect(current) {
		return errors.New("directory root identity changed")
	}
	return nil
}

func cleanTreeRelative(relative string, allowDot bool) ([]string, error) {
	if relative == "." && allowDot {
		return nil, nil
	}
	if relative == "" || relative == "." || strings.Contains(relative, "\\") || path.Clean(relative) != relative {
		return nil, errors.New("invalid relative tree path")
	}
	if _, err := platform.PathKey("windows", platform.CaseSensitive, relative); err != nil {
		return nil, errors.New("invalid relative tree path")
	}
	components := strings.Split(relative, "/")
	if len(components) == 0 {
		return nil, errors.New("invalid relative tree path")
	}
	return components, nil
}
