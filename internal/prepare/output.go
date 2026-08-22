package prepare

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/platform"
)

type outputTarget struct {
	anchor      *os.Root
	parentPath  string
	relativeDir string
	name        string
}

func prepareOutputTarget(path, sessionsRoot, dataDir string) (*outputTarget, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("invalid output path")
	}
	name := filepath.Base(absolute)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return nil, fmt.Errorf("invalid output path")
	}
	for _, protected := range []string{sessionsRoot, dataDir} {
		protected, err = filepath.Abs(protected)
		if err != nil {
			return nil, fmt.Errorf("invalid protected root")
		}
		if pathInside(runtime.GOOS, protected, absolute) {
			return nil, fmt.Errorf("output path is inside a protected data root")
		}
	}

	parent := filepath.Dir(absolute)
	anchorPath := parent
	for {
		info, statErr := os.Lstat(anchorPath)
		if statErr == nil {
			if isSymlinkOrReparse(info) || !info.IsDir() {
				return nil, fmt.Errorf("output parent is redirected or not a directory")
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect output parent: %w", statErr)
		}
		next := filepath.Dir(anchorPath)
		if next == anchorPath {
			return nil, fmt.Errorf("output parent does not exist")
		}
		anchorPath = next
	}

	physicalAnchor, err := filepath.EvalSymlinks(anchorPath)
	if err != nil {
		return nil, fmt.Errorf("inspect output parent: %w", err)
	}
	relativeOutput, err := filepath.Rel(anchorPath, absolute)
	if err != nil || relativeOutput == ".." || strings.HasPrefix(relativeOutput, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("invalid output path")
	}
	physicalOutput := filepath.Join(physicalAnchor, relativeOutput)
	for _, protected := range []string{sessionsRoot, dataDir} {
		physicalProtected, err := filepath.EvalSymlinks(protected)
		if err != nil {
			return nil, fmt.Errorf("inspect protected root: %w", err)
		}
		if pathInside(runtime.GOOS, physicalProtected, physicalOutput) {
			return nil, fmt.Errorf("output path is inside a protected data root")
		}
	}

	root, err := os.OpenRoot(anchorPath)
	if err != nil {
		return nil, fmt.Errorf("open output parent: %w", err)
	}
	opened, openErr := root.Stat(".")
	anchored, anchorErr := os.Lstat(anchorPath)
	if openErr != nil || anchorErr != nil || isSymlinkOrReparse(anchored) || !os.SameFile(opened, anchored) {
		_ = root.Close()
		return nil, fmt.Errorf("output parent changed while opening")
	}
	relativeDir, err := filepath.Rel(anchorPath, parent)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("invalid output parent")
	}
	target := &outputTarget{anchor: root, parentPath: parent, relativeDir: relativeDir, name: name}
	if relativeDir == "." {
		if err := validateOutputEntry(root, name); err != nil {
			_ = root.Close()
			return nil, err
		}
	}
	return target, nil
}

func (target *outputTarget) close() error {
	if target == nil || target.anchor == nil {
		return nil
	}
	return target.anchor.Close()
}

func (target *outputTarget) write(data []byte) error {
	parent, closeParent, err := target.openParent()
	if err != nil {
		return err
	}
	if closeParent {
		defer parent.Close()
	}
	if err := validateOutputEntry(parent, target.name); err != nil {
		return err
	}
	visible, err := os.Lstat(target.parentPath)
	if err != nil || isSymlinkOrReparse(visible) || !visible.IsDir() {
		return fmt.Errorf("output parent changed before write")
	}
	opened, err := parent.Stat(".")
	if err != nil || !os.SameFile(visible, opened) {
		return fmt.Errorf("output parent changed before write")
	}
	if err := atomicfile.WriteRoot(parent, target.name, data, 0o600); err != nil {
		return err
	}
	info, err := parent.Lstat(target.name)
	if err != nil || isSymlinkOrReparse(info) || !info.Mode().IsRegular() {
		return fmt.Errorf("output file is unsafe after write")
	}
	return nil
}

func (target *outputTarget) openParent() (*os.Root, bool, error) {
	if target.relativeDir == "." {
		return target.anchor, false, nil
	}
	current := target.anchor
	owned := false
	components := strings.Split(target.relativeDir, string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			if owned {
				_ = current.Close()
			}
			return nil, false, fmt.Errorf("invalid output parent")
		}
		info, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			if err := current.Mkdir(component, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				if owned {
					_ = current.Close()
				}
				return nil, false, err
			}
			info, err = current.Lstat(component)
		}
		if err != nil || isSymlinkOrReparse(info) || !info.IsDir() {
			if owned {
				_ = current.Close()
			}
			return nil, false, fmt.Errorf("output parent component is redirected or not a directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			if owned {
				_ = current.Close()
			}
			return nil, false, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			if owned {
				_ = current.Close()
			}
			return nil, false, fmt.Errorf("output parent component changed while opening")
		}
		if owned {
			_ = current.Close()
		}
		current = next
		owned = true
	}
	return current, owned, nil
}

func validateOutputEntry(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect output file: %w", err)
	}
	if isSymlinkOrReparse(info) || !info.Mode().IsRegular() {
		return fmt.Errorf("output file is a symlink, reparse point, or non-regular file")
	}
	return nil
}

func pathInside(goos, root, candidate string) bool {
	root = platform.NormalizePath(goos, root)
	candidate = platform.NormalizePath(goos, candidate)
	if root == candidate {
		return true
	}
	separator := string(filepath.Separator)
	if goos == "windows" {
		separator = "/"
	}
	return strings.HasPrefix(candidate, strings.TrimSuffix(root, separator)+separator)
}
