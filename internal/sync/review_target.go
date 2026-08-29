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
	// owner is pointer-scoped deliberately. A shallow copy shares this exact
	// close token and therefore cannot double-decrement the shared state.
	owner *reviewTargetOwner
	state *reviewTargetState
}

type reviewTargetOwner struct {
	mu       sync.Mutex
	closed   bool
	closeErr error
	state    *reviewTargetState
}

type reviewTargetState struct {
	mu   sync.Mutex
	refs int

	full        string
	reviewPath  string
	caseMode    platform.CaseMode
	project     os.FileInfo
	projectPath string
	vault       os.FileInfo
	vaultPath   string
	data        os.FileInfo
	dataPath    string
	current     *pathguard.Directory
	remaining   []string
	closed      bool

	// These are test-only adversarial namespace seams around creation. Their
	// production values are nil; the rechecks and identity continuity below
	// remain mandatory.
	beforeTargetMutation func(*pathguard.Directory, string) error
	afterCreateIdentity  func(*os.Root, string) error
	openSecurityHandle   func(*os.Root, string) (*os.File, error)
	afterCreatePinned    func(*pathguard.Directory) error
}

// PinReviewTarget requires Project and the editable Vault review subtree to be
// disjoint in both containment directions. Existing components are also
// authenticated by physical ancestry, catching aliases beyond path spelling.
func PinReviewTarget(reviewPath string, caseMode platform.CaseMode, project, vault *pathguard.Directory, data ...*pathguard.Directory) (*ReviewTargetPin, error) {
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
	state := &reviewTargetState{
		refs: 1,
		full: full, reviewPath: reviewPath, caseMode: caseMode,
		project: project.Info(), projectPath: project.Path,
		vault: vault.Info(), vaultPath: vault.Path, current: opened,
		remaining: append([]string(nil), remaining...),
	}
	if len(data) > 1 || (len(data) == 1 && data[0] == nil) {
		_ = opened.Close()
		return nil, errors.New("vault review target data binding is invalid")
	}
	if len(data) == 1 {
		if targetOverlapsDirectory(opened, len(remaining) == 0, data[0]) {
			_ = opened.Close()
			return nil, errors.New("vault review target must be disjoint from sync Data")
		}
		state.data, state.dataPath = data[0].Info(), data[0].Path
	}
	owner := &reviewTargetOwner{state: state}
	return &ReviewTargetPin{owner: owner, state: state}, nil
}

// Close releases the retained target or parent handle. It is idempotent; a
// closed capability cannot be cloned, rechecked, or used for an operation.
func (binding *ReviewTargetPin) Close() error {
	if binding == nil || binding.owner == nil {
		return nil
	}
	owner := binding.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return owner.closeErr
	}
	owner.closed = true
	state := owner.state
	if state == nil || state != binding.state {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.refs <= 0 {
		return nil
	}
	state.refs--
	if state.refs != 0 {
		return nil
	}
	state.closed = true
	current := state.current
	state.current = nil
	state.remaining = nil
	if current != nil {
		owner.closeErr = current.Close()
	}
	return owner.closeErr
}

// cloneFor retains the exact authority for a separately owned Engine lifetime.
// It deliberately clones through the shared retained handle, never through the
// replaceable configured pathname.
func (binding *ReviewTargetPin) cloneFor(reviewPath string, caseMode platform.CaseMode, project, vault *pathguard.Directory, data ...*pathguard.Directory) (*ReviewTargetPin, error) {
	_, state, unlock, err := binding.lockOwnedState()
	if err != nil {
		return nil, errors.New("vault review target binding is unavailable")
	}
	defer unlock()
	if err := state.validateHandleLocked(reviewPath, caseMode, project, vault); err != nil {
		return nil, err
	}
	if len(data) > 1 || (len(data) == 1 && data[0] == nil) {
		return nil, errors.New("vault review target data binding is invalid")
	}
	if len(data) == 1 {
		if err := state.bindDataLocked(data[0]); err != nil {
			return nil, err
		}
	}
	if err := state.recheckNamespaceLocked(project, vault, firstDirectory(data)); err != nil {
		return nil, err
	}
	state.refs++
	cloneOwner := &reviewTargetOwner{state: state}
	return &ReviewTargetPin{owner: cloneOwner, state: state}, nil
}

// Recheck proves that the live namespace still names this capability. Engine
// operations do not use the reopened handle; this is an entry/exit alarm only.
func (binding *ReviewTargetPin) Recheck(project, vault *pathguard.Directory, data ...*pathguard.Directory) error {
	_, state, unlock, err := binding.lockOwnedState()
	if err != nil {
		return err
	}
	defer unlock()
	if err := state.validateHandleLocked(state.reviewPath, state.caseMode, project, vault); err != nil {
		return err
	}
	if len(data) > 1 || (len(data) == 1 && data[0] == nil) {
		return errors.New("vault review target data binding is invalid")
	}
	if len(data) == 1 {
		if err := state.bindDataLocked(data[0]); err != nil {
			return err
		}
	}
	return state.recheckNamespaceLocked(project, vault, firstDirectory(data))
}

// directory returns the exact retained target. If creation is requested for a
// missing target, every component is created exclusively below the retained
// parent; any racer, redirect, or case alias fails closed.
func (binding *ReviewTargetPin) directory(create bool) (*pathguard.Directory, bool, error) {
	_, state, unlock, err := binding.lockOwnedState()
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	if state.closed || state.current == nil || state.current.Root == nil || state.current.Info() == nil {
		return nil, false, errors.New("vault review target binding is unavailable")
	}
	current, err := state.current.Root.Stat(".")
	if err != nil || !current.IsDir() || !os.SameFile(state.current.Info(), current) {
		return nil, false, errors.New("vault review target identity changed")
	}
	project, vault, data, err := state.openBoundRootsLocked()
	if err != nil {
		return nil, false, err
	}
	defer closeReviewTargetRoots(project, vault, data)
	if err := state.recheckNamespaceLocked(project, vault, data); err != nil {
		return nil, false, err
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
		beforeMutation := func() error {
			if state.beforeTargetMutation != nil {
				if err := state.beforeTargetMutation(state.current, component); err != nil {
					return err
				}
			}
			return state.recheckNamespaceLocked(project, vault, data)
		}
		openSecurityHandle := state.openSecurityHandle
		if openSecurityHandle == nil {
			openSecurityHandle = openReviewTargetSecurityHandle
		}
		next, err := createPinnedReviewTargetChild(state.current, component, state.caseMode, beforeMutation, func(created *pathguard.Directory) error {
			if state.beforeTargetMutation != nil {
				if err := state.beforeTargetMutation(state.current, component); err != nil {
					return err
				}
			}
			return state.recheckNamespaceAtLocked(project, vault, data, created, state.remaining[1:])
		}, state.afterCreateIdentity, openSecurityHandle)
		if err != nil {
			return nil, false, err
		}
		old := state.current
		state.current = next
		state.remaining = state.remaining[1:]
		if err := old.Close(); err != nil {
			return nil, false, errors.New("vault review target parent could not be released")
		}
		if state.afterCreatePinned != nil {
			if err := state.afterCreatePinned(state.current); err != nil {
				return nil, false, err
			}
		}
		// Creating a component changes the retained authority. Re-prove that
		// its configured namespace is still attached below Vault and remains
		// disjoint from Project/Data before another component can be created or
		// the new target can be returned to a mutating caller.
		if err := state.recheckNamespaceLocked(project, vault, data); err != nil {
			return nil, false, err
		}
	}
	return state.current, true, nil
}

func (binding *ReviewTargetPin) lockOwnedState() (*reviewTargetOwner, *reviewTargetState, func(), error) {
	if binding == nil || binding.owner == nil || binding.state == nil {
		return nil, nil, nil, errors.New("vault review target binding is unavailable")
	}
	owner := binding.owner
	owner.mu.Lock()
	if owner.closed || owner.state == nil || owner.state != binding.state {
		owner.mu.Unlock()
		return nil, nil, nil, errors.New("vault review target binding is unavailable")
	}
	state := owner.state
	state.mu.Lock()
	return owner, state, func() {
		state.mu.Unlock()
		owner.mu.Unlock()
	}, nil
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

func (state *reviewTargetState) bindDataLocked(data *pathguard.Directory) error {
	if data == nil || data.Info() == nil || data.Path == "" {
		return errors.New("vault review target data binding is invalid")
	}
	if state.data == nil {
		state.data, state.dataPath = data.Info(), data.Path
		return nil
	}
	if !os.SameFile(state.data, data.Info()) || state.dataPath != data.Path {
		return errors.New("vault review target data identity changed")
	}
	return nil
}

func (state *reviewTargetState) openBoundRootsLocked() (project, vault, data *pathguard.Directory, retErr error) {
	project, err := pathguard.Open(state.projectPath)
	if err != nil || !os.SameFile(state.project, project.Info()) {
		if project != nil {
			_ = project.Close()
		}
		return nil, nil, nil, errors.New("vault review target Project identity changed")
	}
	vault, err = pathguard.Open(state.vaultPath)
	if err != nil || !os.SameFile(state.vault, vault.Info()) {
		_ = project.Close()
		if vault != nil {
			_ = vault.Close()
		}
		return nil, nil, nil, errors.New("vault review target Vault identity changed")
	}
	if state.data != nil {
		data, err = pathguard.Open(state.dataPath)
		if err != nil || !os.SameFile(state.data, data.Info()) {
			_ = project.Close()
			_ = vault.Close()
			if data != nil {
				_ = data.Close()
			}
			return nil, nil, nil, errors.New("vault review target Data identity changed")
		}
	}
	return project, vault, data, nil
}

func closeReviewTargetRoots(project, vault, data *pathguard.Directory) {
	if data != nil {
		_ = data.Close()
	}
	if vault != nil {
		_ = vault.Close()
	}
	if project != nil {
		_ = project.Close()
	}
}

func (state *reviewTargetState) recheckNamespaceLocked(project, vault, data *pathguard.Directory) error {
	return state.recheckNamespaceAtLocked(project, vault, data, state.current, state.remaining)
}

func (state *reviewTargetState) recheckNamespaceAtLocked(project, vault, data, current *pathguard.Directory, expectedRemaining []string) error {
	opened, actualRemaining, err := pathguard.OpenDeepest(state.full)
	if err != nil {
		return errors.New("vault review target identity changed")
	}
	defer opened.Close()
	if current == nil || current.Info() == nil || !os.SameFile(current.Info(), opened.Info()) || !sameReviewTargetComponents(expectedRemaining, actualRemaining) ||
		!opened.ContainsIdentity(vault.Info()) ||
		vaultTargetOverlapsProject(state.full, project, opened, len(actualRemaining) == 0, state.caseMode) ||
		targetOverlapsDirectory(opened, len(actualRemaining) == 0, data) {
		return errors.New("vault review target identity changed")
	}
	if err := verifyEquivalentReviewTargetPath(state.reviewPath, state.caseMode, vault, current, expectedRemaining); err != nil {
		return err
	}
	return nil
}

func firstDirectory(values []*pathguard.Directory) *pathguard.Directory {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

func createPinnedReviewTargetChild(parent *pathguard.Directory, component string, caseMode platform.CaseMode, beforeCreate func() error, beforeProtect func(*pathguard.Directory) error, afterCreateIdentity func(*os.Root, string) error, openSecurityHandle func(*os.Root, string) (*os.File, error)) (*pathguard.Directory, error) {
	if parent == nil || parent.Root == nil || component == "" || component == "." || component == ".." || strings.ContainsAny(component, `/\`) || openSecurityHandle == nil {
		return nil, errors.New("vault review target creation capability is invalid")
	}
	if beforeCreate != nil {
		if err := beforeCreate(); err != nil {
			return nil, err
		}
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
	modeHandle, err := openSecurityHandle(parent.Root, component)
	if err != nil {
		_ = child.Close()
		return nil, errors.New("created vault review target cannot be protected")
	}
	modeOpened, err := reviewTargetSecurityHandleIdentity(modeHandle)
	if err != nil || !os.SameFile(opened, modeOpened) {
		_ = modeHandle.Close()
		_ = child.Close()
		return nil, errors.New("created vault review target security handle identity changed")
	}
	if beforeProtect != nil {
		pending := &pathguard.Directory{
			Root: child, Path: filepath.Join(parent.Path, component),
			Ancestors: append(append([]os.FileInfo(nil), parent.Ancestors...), opened),
		}
		if err := beforeProtect(pending); err != nil {
			_ = modeHandle.Close()
			_ = child.Close()
			return nil, err
		}
	}
	modeBeforeProtect, err := reviewTargetSecurityHandleIdentity(modeHandle)
	if err != nil || !os.SameFile(opened, modeBeforeProtect) || !os.SameFile(modeOpened, modeBeforeProtect) {
		_ = modeHandle.Close()
		_ = child.Close()
		return nil, errors.New("created vault review target security handle identity changed")
	}
	protectErr := protectReviewTargetDirectory(modeHandle)
	modeAfterProtect, modeStatErr := reviewTargetSecurityHandleIdentity(modeHandle)
	closeErr := modeHandle.Close()
	afterOpen, statErr := child.Stat(".")
	afterName, nameErr := parent.Root.Lstat(component)
	if protectErr != nil || modeStatErr != nil || closeErr != nil || statErr != nil || nameErr != nil ||
		!afterOpen.IsDir() || !afterName.IsDir() || !reviewTargetDirectoryProtected(filepath.Join(parent.Path, component), afterOpen) ||
		!os.SameFile(created, afterOpen) || !os.SameFile(opened, afterOpen) || !os.SameFile(modeOpened, modeAfterProtect) ||
		!os.SameFile(afterOpen, modeAfterProtect) || !os.SameFile(afterOpen, afterName) {
		_ = child.Close()
		return nil, errors.New("created vault review target changed while pinning")
	}
	if err := reviewTargetEquivalentEntry(parent.Root, component, caseMode, afterOpen); err != nil {
		_ = child.Close()
		return nil, err
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
	return reviewTargetEquivalentEntry(parent.Root, component, caseMode, nil)
}

func reviewTargetEquivalentEntry(parent *os.Root, component string, caseMode platform.CaseMode, expected os.FileInfo) error {
	if runtime.GOOS != "windows" && caseMode != platform.CaseInsensitive {
		return nil
	}
	directory, err := parent.Open(".")
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
	candidates := make([]reviewTargetEquivalentCandidate, 0, len(entries))
	for _, entry := range entries {
		candidate := reviewTargetEquivalentCandidate{name: entry.Name()}
		if expected != nil && folder.String(norm.NFC.String(entry.Name())) == folder.String(norm.NFC.String(component)) {
			info, statErr := parent.Lstat(entry.Name())
			if statErr != nil {
				return errors.New("vault review target equivalent alias changed identity")
			}
			candidate.info = info
		}
		candidates = append(candidates, candidate)
	}
	return validateReviewTargetEquivalentCandidates(component, expected, candidates)
}

type reviewTargetEquivalentCandidate struct {
	name string
	info os.FileInfo
}

func validateReviewTargetEquivalentCandidates(component string, expected os.FileInfo, candidates []reviewTargetEquivalentCandidate) error {
	folder := cases.Fold()
	configured := norm.NFC.String(component)
	wanted := folder.String(configured)
	matches := 0
	for _, candidate := range candidates {
		candidateName := norm.NFC.String(candidate.name)
		if folder.String(candidateName) != wanted {
			continue
		}
		matches++
		if candidateName != configured {
			return errors.New("vault review target equivalent alias has the wrong spelling")
		}
		if expected != nil && (candidate.info == nil || !os.SameFile(expected, candidate.info)) {
			return errors.New("vault review target equivalent alias changed identity")
		}
	}
	if expected == nil && matches != 0 {
		return errors.New("vault review target case alias already exists")
	}
	if expected != nil && matches != 1 {
		return errors.New("vault review target equivalent alias is ambiguous")
	}
	return nil
}

func verifyEquivalentReviewTargetPath(reviewPath string, caseMode platform.CaseMode, vault, expected *pathguard.Directory, expectedRemaining []string) error {
	components := strings.Split(filepath.FromSlash(reviewPath), string(filepath.Separator))
	current := vault
	owned := false
	defer func() {
		if owned && current != nil {
			_ = current.Close()
		}
	}()
	for index, component := range components {
		before, err := current.Root.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			if !os.SameFile(current.Info(), expected.Info()) || !sameReviewTargetComponents(components[index:], expectedRemaining) {
				return errors.New("vault review target identity changed")
			}
			return reviewTargetEquivalentEntry(current.Root, component, caseMode, nil)
		}
		if err != nil || before == nil || !before.IsDir() {
			return errors.New("vault review target identity changed")
		}
		childRoot, opened, err := current.OpenDirectory(component)
		if err != nil || !os.SameFile(before, opened) {
			if childRoot != nil {
				_ = childRoot.Close()
			}
			return errors.New("vault review target identity changed")
		}
		if err := reviewTargetEquivalentEntry(current.Root, component, caseMode, opened); err != nil {
			_ = childRoot.Close()
			return err
		}
		ancestors := append(append([]os.FileInfo(nil), current.Ancestors...), opened)
		path := filepath.Join(current.Path, component)
		if owned {
			_ = current.Close()
		}
		current = &pathguard.Directory{Root: childRoot, Path: path, Ancestors: ancestors}
		owned = true
	}
	if len(expectedRemaining) != 0 || !os.SameFile(current.Info(), expected.Info()) {
		return errors.New("vault review target identity changed")
	}
	return nil
}

func targetOverlapsDirectory(target *pathguard.Directory, targetExists bool, directory *pathguard.Directory) bool {
	if target == nil || directory == nil {
		return false
	}
	if target.ContainsIdentity(directory.Info()) {
		return true
	}
	return targetExists && directory.ContainsIdentity(target.Info())
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
