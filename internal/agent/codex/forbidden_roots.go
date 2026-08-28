package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/neomei/SessionReviewer/internal/agent"
)

var errForbiddenRootIdentityChanged = errors.New("forbidden root identity changed")

type pinnedForbiddenRoot struct {
	kind   agent.ForbiddenRootKind
	path   string
	anchor *os.File
	info   os.FileInfo
}

type forbiddenRootSet struct {
	roots []pinnedForbiddenRoot
}

func openForbiddenRoots(specifications []agent.ForbiddenRoot) (*forbiddenRootSet, error) {
	if len(specifications) != 2 {
		return nil, errors.New("one canonical Project root and one canonical Vault root are required")
	}
	seen := make(map[agent.ForbiddenRootKind]struct{}, 2)
	set := &forbiddenRootSet{roots: make([]pinnedForbiddenRoot, 0, len(specifications))}
	defer func() {
		if set != nil && len(set.roots) != len(specifications) {
			_ = set.close()
		}
	}()
	for _, specification := range specifications {
		if specification.Kind != agent.ForbiddenRootProject && specification.Kind != agent.ForbiddenRootVault {
			return nil, errors.New("unreviewed forbidden root kind")
		}
		if _, duplicate := seen[specification.Kind]; duplicate {
			return nil, errors.New("duplicate forbidden root kind")
		}
		seen[specification.Kind] = struct{}{}
		path := specification.CanonicalPath
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errors.New("forbidden root is not a canonical absolute path")
		}
		leaf, err := os.Lstat(path)
		if err != nil || !leaf.IsDir() || leaf.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("forbidden root is not a physical directory")
		}
		physical, err := filepath.EvalSymlinks(path)
		if err != nil || !canonicalPathEqual(filepath.Clean(physical), path) {
			return nil, errors.New("forbidden root path is not canonical")
		}
		anchor, err := os.Open(path)
		if err != nil {
			return nil, errors.New("forbidden root cannot be pinned")
		}
		info, infoErr := anchor.Stat()
		visible, visibleErr := os.Stat(path)
		platformErr := validateForbiddenRootPlatform(path, anchor)
		if infoErr != nil || visibleErr != nil || platformErr != nil || !info.IsDir() ||
			!os.SameFile(leaf, info) || !os.SameFile(info, visible) {
			_ = anchor.Close()
			return nil, errors.New("forbidden root identity cannot be pinned")
		}
		set.roots = append(set.roots, pinnedForbiddenRoot{
			kind: specification.Kind, path: path, anchor: anchor, info: info,
		})
	}
	if _, ok := seen[agent.ForbiddenRootProject]; !ok {
		return nil, errors.New("canonical Project root is required")
	}
	if _, ok := seen[agent.ForbiddenRootVault]; !ok {
		return nil, errors.New("canonical Vault root is required")
	}
	return set, nil
}

func (set *forbiddenRootSet) recheckDisjoint(private *privateRoot) error {
	if set == nil || private == nil || private.anchor == nil {
		return errForbiddenRootIdentityChanged
	}
	privateInfo, err := private.anchor.Stat()
	visiblePrivate, visibleErr := os.Stat(private.path)
	if err != nil || visibleErr != nil || !os.SameFile(private.info, privateInfo) ||
		!os.SameFile(privateInfo, visiblePrivate) {
		return errPrivateRootIdentityChanged
	}
	for _, root := range set.roots {
		current, currentErr := root.anchor.Stat()
		visible, visibleErr := os.Stat(root.path)
		if currentErr != nil || visibleErr != nil || !os.SameFile(root.info, current) ||
			!os.SameFile(current, visible) {
			return errForbiddenRootIdentityChanged
		}
		if pathsOverlap(private.path, root.path) {
			return errors.New("private working root overlaps a forbidden root")
		}
		overlap, overlapErr := physicalDirectoriesOverlap(private.anchor, root.anchor)
		if overlapErr != nil {
			return overlapErr
		}
		if overlap {
			return errors.New("private working root physically overlaps a forbidden root")
		}
	}
	return nil
}

func (set *forbiddenRootSet) close() error {
	if set == nil {
		return nil
	}
	var result error
	for index := range set.roots {
		if set.roots[index].anchor != nil {
			result = errors.Join(result, set.roots[index].anchor.Close())
			set.roots[index].anchor = nil
		}
	}
	return result
}

func pathsOverlap(first, second string) bool {
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !filepath.IsAbs(relative) &&
		len(relative) > 0 && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}
