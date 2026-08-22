package prepare

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

type outputTarget struct {
	anchor      *pathguard.Directory
	parentPath  string
	relativeDir string
	name        string
}

func prepareOutputTarget(path, sessionsRoot string, dataDir *pathguard.Directory) (*outputTarget, error) {
	if dataDir == nil || dataDir.Info() == nil {
		return nil, fmt.Errorf("inspect protected root: data directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("invalid output path")
	}
	name := filepath.Base(absolute)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return nil, fmt.Errorf("invalid output path")
	}
	parent := filepath.Dir(absolute)
	anchor, remaining, err := pathguard.OpenDeepest(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect output parent: %w", err)
	}
	keepAnchor := false
	defer func() {
		if !keepAnchor {
			_ = anchor.Close()
		}
	}()
	protectedSessions, err := pathguard.Open(sessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect protected root: %w", err)
	}
	insideSessions := anchor.ContainsIdentity(protectedSessions.Info())
	_ = protectedSessions.Close()
	if insideSessions || anchor.ContainsIdentity(dataDir.Info()) {
		return nil, fmt.Errorf("output path is inside a protected data root")
	}
	relativeDir := filepath.Join(remaining...)
	if relativeDir == "" {
		relativeDir = "."
	}
	target := &outputTarget{anchor: anchor, parentPath: parent, relativeDir: relativeDir, name: name}
	if relativeDir == "." {
		if err := validateOutputEntry(anchor.Root, name); err != nil {
			return nil, err
		}
	}
	keepAnchor = true
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
		return target.anchor.Root, false, nil
	}
	current := target.anchor.Root
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
