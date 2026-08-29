package sync

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const maxReviewTargetParentEntries = 10_000

// ReviewTargetPin is an owned operation capability for exactly one configured
// Vault review subtree. When the target exists, current retains its directory
// handle. When it does not, current is the deepest authenticated parent and
// remaining is an exclusive, no-follow creation claim; later path occupants
// are rejected rather than adopted.
type ReviewTargetPin struct {
	closeOnce sync.Once
	closeErr  error
	state     *reviewTargetState
}

type reviewTargetState struct {
	mu   sync.Mutex
	refs int

	full       string
	reviewPath string
	caseMode   platform.CaseMode
	project    os.FileInfo
	vault      os.FileInfo
	current    *pathguard.Directory
	remaining  []string
	closed     bool

	// afterCreateIdentity is a test-only adversarial namespace seam. The
	// production value is nil; identity continuity below remains mandatory.
	afterCreateIdentity func(*os.Root, string) error
}

// PinReviewTarget requires Project and the editable Vault review subtree to be
// disjoint in both containment directions. Existing components are also
// authenticated by physical ancestry, catching aliases beyond path spelling.
func PinReviewTarget(reviewPath string, caseMode platform.CaseMode, project, vault *pathguard.Directory) (*ReviewTargetPin, error) {
	if project == nil || vault == nil || reviewPath == "" || strings.Contains(reviewPath, `\`) ||
		path.Clean(reviewPath) != reviewPath || path.IsAbs(reviewPath) || reviewPath == "." || reviewPath == ".." || strings.HasPrefix(reviewPath, "../") ||
		(caseMode != platform.CaseSensitive && caseMode != platform.CaseInsensitive) {
		return nil, errors.New("vault review target is invalid")
	}
	full := filepath.Join(vault.Path, filepath.FromSlash(reviewPath))
	withinVault, err := filepath.Rel(vault.Path, full)
	if err != nil || withinVault == "." || withinVault == ".." || strings.HasPrefix(withinVault, ".."+string(filepath.Separator)) {
		return nil, errors.New("vault review target escapes its Vault root")
	}
	opened, remaining, err := pathguard.OpenDeepest(full)
	if err != nil {
		return nil, errors.New("vault review target is unsafe")
	}
	if !opened.ContainsIdentity(vault.Info()) || vaultTargetOverlapsProject(full, project, opened, len(remaining) == 0, caseMode) {
		_ = opened.Close()
		return nil, errors.New("vault review target must be disjoint from the authoritative Project")
	}
	if len(remaining) != 0 {
		if err := reviewTargetLeafStillUnclaimed(opened, remaining[0], caseMode); err != nil {
			_ = opened.Close()
			return nil, err
		}
	}
	return &ReviewTargetPin{state: &reviewTargetState{
		refs: 1,
		full: full, reviewPath: reviewPath, caseMode: caseMode,
		project: project.Info(), vault: vault.Info(), current: opened,
		remaining: append([]string(nil), remaining...),
	}}, nil
}

// Close releases the retained target or parent handle. It is idempotent; a
// closed capability cannot be cloned, rechecked, or used for an operation.
func (binding *ReviewTargetPin) Close() error {
	if binding == nil {
		return nil
	}
	binding.closeOnce.Do(func() {
		state := binding.state
		if state == nil {
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.refs <= 0 {
			return
		}
		state.refs--
		if state.refs != 0 {
			return
		}
		state.closed = true
		current := state.current
		state.current = nil
		state.remaining = nil
		if current != nil {
			binding.closeErr = current.Close()
		}
	})
	return binding.closeErr
}

// cloneFor retains the exact authority for a separately owned Engine lifetime.
// It deliberately clones through the shared retained handle, never through the
// replaceable configured pathname.
func (binding *ReviewTargetPin) cloneFor(reviewPath string, caseMode platform.CaseMode, project, vault *pathguard.Directory) (*ReviewTargetPin, error) {
	if binding == nil || binding.state == nil {
		return nil, errors.New("vault review target binding is unavailable")
	}
	state := binding.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.validateHandleLocked(reviewPath, caseMode, project, vault); err != nil {
		return nil, err
	}
	state.refs++
	return &ReviewTargetPin{state: state}, nil
}

// Recheck proves that the live namespace still names this capability. Engine
// operations do not use the reopened handle; this is an entry/exit alarm only.
func (binding *ReviewTargetPin) Recheck(project, vault *pathguard.Directory) error {
	if binding == nil || binding.state == nil {
		return errors.New("vault review target binding is unavailable")
	}
	state := binding.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.validateHandleLocked(state.reviewPath, state.caseMode, project, vault); err != nil {
		return err
	}
	opened, remaining, err := pathguard.OpenDeepest(state.full)
	if err != nil {
		return errors.New("vault review target identity changed")
	}
	defer opened.Close()
	if !os.SameFile(state.current.Info(), opened.Info()) || !sameReviewTargetComponents(state.remaining, remaining) ||
		!opened.ContainsIdentity(vault.Info()) ||
		vaultTargetOverlapsProject(state.full, project, opened, len(remaining) == 0, state.caseMode) {
		return errors.New("vault review target identity changed")
	}
	if len(remaining) != 0 {
		if err := reviewTargetLeafStillUnclaimed(opened, remaining[0], state.caseMode); err != nil {
			return err
		}
	}
	return nil
}

// directory returns the exact retained target. If creation is requested for a
// missing target, every component is created exclusively below the retained
// parent; any racer, redirect, or case alias fails closed.
func (binding *ReviewTargetPin) directory(create bool) (*pathguard.Directory, bool, error) {
	if binding == nil || binding.state == nil {
		return nil, false, errors.New("vault review target binding is unavailable")
	}
	state := binding.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.current == nil || state.current.Root == nil || state.current.Info() == nil {
		return nil, false, errors.New("vault review target binding is unavailable")
	}
	current, err := state.current.Root.Stat(".")
	if err != nil || !current.IsDir() || !os.SameFile(state.current.Info(), current) {
		return nil, false, errors.New("vault review target identity changed")
	}
	if len(state.remaining) == 0 {
		return state.current, true, nil
	}
	if err := reviewTargetLeafStillUnclaimed(state.current, state.remaining[0], state.caseMode); err != nil {
		return nil, false, err
	}
	if !create {
		return nil, false, nil
	}
	for len(state.remaining) != 0 {
		component := state.remaining[0]
		if err := reviewTargetLeafStillUnclaimed(state.current, component, state.caseMode); err != nil {
			return nil, false, err
		}
		next, err := createPinnedReviewTargetChild(state.current, component, state.afterCreateIdentity)
		if err != nil {
			return nil, false, err
		}
		old := state.current
		state.current = next
		state.remaining = state.remaining[1:]
		if err := old.Close(); err != nil {
			return nil, false, errors.New("vault review target parent could not be released")
		}
	}
	return state.current, true, nil
}

func (state *reviewTargetState) validateHandleLocked(reviewPath string, caseMode platform.CaseMode, project, vault *pathguard.Directory) error {
	if state.closed || state.refs <= 0 || state.current == nil || state.current.Root == nil || state.current.Info() == nil ||
		state.full == "" || state.reviewPath != reviewPath || state.caseMode != caseMode ||
		project == nil || vault == nil || project.Info() == nil || vault.Info() == nil ||
		state.project == nil || state.vault == nil || !os.SameFile(state.project, project.Info()) || !os.SameFile(state.vault, vault.Info()) {
		return errors.New("vault review target binding is unavailable")
	}
	current, err := state.current.Root.Stat(".")
	if err != nil || !current.IsDir() || !os.SameFile(state.current.Info(), current) {
		return errors.New("vault review target identity changed")
	}
	return nil
}

func createPinnedReviewTargetChild(parent *pathguard.Directory, component string, afterCreateIdentity func(*os.Root, string) error) (*pathguard.Directory, error) {
	if parent == nil || parent.Root == nil || component == "" || component == "." || component == ".." || strings.ContainsAny(component, `/\`) {
		return nil, errors.New("vault review target creation capability is invalid")
	}
	if err := parent.Root.Mkdir(component, 0o700); err != nil {
		return nil, errors.New("vault review target was claimed by another namespace entry")
	}
	created, err := parent.Root.Lstat(component)
	if err != nil || !created.IsDir() {
		return nil, errors.New("created vault review target is redirected or changed")
	}
	if afterCreateIdentity != nil {
		if err := afterCreateIdentity(parent.Root, component); err != nil {
			return nil, err
		}
	}
	child, opened, err := parent.OpenDirectory(component)
	if err != nil || !os.SameFile(created, opened) {
		if child != nil {
			_ = child.Close()
		}
		return nil, errors.New("created vault review target is redirected or changed")
	}
	modeHandle, err := child.Open(".")
	if err != nil {
		_ = child.Close()
		return nil, errors.New("created vault review target cannot be protected")
	}
	chmodErr := modeHandle.Chmod(0o700)
	closeErr := modeHandle.Close()
	afterOpen, statErr := child.Stat(".")
	afterName, nameErr := parent.Root.Lstat(component)
	if chmodErr != nil || closeErr != nil || statErr != nil || nameErr != nil ||
		!afterOpen.IsDir() || !afterName.IsDir() || afterOpen.Mode().Perm() != 0o700 ||
		!os.SameFile(created, afterOpen) || !os.SameFile(opened, afterOpen) || !os.SameFile(afterOpen, afterName) {
		_ = child.Close()
		return nil, errors.New("created vault review target changed while pinning")
	}
	return &pathguard.Directory{
		Root: child, Path: filepath.Join(parent.Path, component),
		Ancestors: append(append([]os.FileInfo(nil), parent.Ancestors...), afterOpen),
	}, nil
}

func reviewTargetLeafStillUnclaimed(parent *pathguard.Directory, component string, caseMode platform.CaseMode) error {
	if parent == nil || parent.Root == nil {
		return errors.New("vault review target binding is unavailable")
	}
	if _, err := parent.Root.Lstat(component); err == nil {
		return errors.New("vault review target identity changed")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("vault review target identity changed")
	}
	if runtime.GOOS != "windows" && caseMode != platform.CaseInsensitive {
		return nil
	}
	directory, err := parent.Root.Open(".")
	if err != nil {
		return errors.New("vault review target parent cannot be inspected")
	}
	entries, readErr := directory.ReadDir(maxReviewTargetParentEntries + 1)
	closeErr := directory.Close()
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if readErr != nil || closeErr != nil || len(entries) > maxReviewTargetParentEntries {
		return errors.New("vault review target parent cannot be inspected")
	}
	folder := cases.Fold()
	wanted := folder.String(norm.NFC.String(component))
	for _, entry := range entries {
		if folder.String(norm.NFC.String(entry.Name())) == wanted {
			return errors.New("vault review target case alias already exists")
		}
	}
	return nil
}

func vaultTargetOverlapsProject(full string, project, deepest *pathguard.Directory, targetExists bool, caseMode platform.CaseMode) bool {
	if project == nil || deepest == nil {
		return true
	}
	if lexicalPathContains(project.Path, full, caseMode) || lexicalPathContains(full, project.Path, caseMode) {
		return true
	}
	if deepest.ContainsIdentity(project.Info()) {
		return true
	}
	return targetExists && project.ContainsIdentity(deepest.Info())
}

func lexicalPathContains(parent, child string, caseMode platform.CaseMode) bool {
	parent = norm.NFC.String(filepath.ToSlash(filepath.Clean(parent)))
	child = norm.NFC.String(filepath.ToSlash(filepath.Clean(child)))
	if runtime.GOOS == "windows" || caseMode == platform.CaseInsensitive {
		folder := cases.Fold()
		parent = folder.String(parent)
		child = folder.String(child)
	}
	if parent == "" || child == "" {
		return false
	}
	if parent == child {
		return true
	}
	if strings.HasSuffix(parent, "/") {
		return strings.HasPrefix(child, parent)
	}
	return strings.HasPrefix(child, parent+"/")
}

func sameReviewTargetComponents(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
