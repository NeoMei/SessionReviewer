package apply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/proposal"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

const maxInputBytes = 4 << 20

var safeApplyID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Options struct {
	ProposalPath, EvidencePath, ProjectRoot, DataDir string
	Now                                              func() time.Time
	ExpectedProjectRoot                              os.FileInfo
	hooks                                            applyHooks
}

type Result struct {
	ProjectID, SessionID           string
	FromCursor, ToCursor           int
	ChangedFiles                   []string
	CursorAdvanced, AlreadyApplied bool
}

type applyHooks struct {
	afterRender             func() error
	afterPreparedReceipt    func() error
	afterFile               func(index int, relativePath string) error
	afterAppliedReceipt     func() error
	beforeCAS               func() error
	afterInputRead          func(kind string) error
	duringInputRead         func(kind string) error
	applyPlan               func(ledger.WritePlan, os.FileInfo) ([]string, error)
	writeReceipt            func(*os.Root, string, []byte, fs.FileMode) error
	syncPublication         func(*os.Root, string) error
	removeReceipt           func(*os.Root, string) error
	syncReceiptDirectory    func(*os.Root) error
	afterReceiptEnumeration func() error
	receiptScanByteLimit    uint64
	ensureRootDir           func(*os.Root, string, fs.FileMode) error
}

type inputContext struct {
	Packet               evidence.Packet
	Proposal             proposal.Proposal
	ProposalDigest       string
	EvidenceFileDigest   string
	EvidencePacketDigest string
}

type applyRoots struct {
	project *pathguard.Directory
	data    *pathguard.Directory
}

func (roots *applyRoots) Close() error {
	if roots == nil {
		return nil
	}
	var err error
	if roots.project != nil {
		err = errors.Join(err, roots.project.Close())
	}
	if roots.data != nil {
		err = errors.Join(err, roots.data.Close())
	}
	return err
}

const applyLockTimeout = 2 * time.Second

type projectApplyLock struct {
	platform        *applyPlatformLock
	projectDataPath string
	projects        *os.Root
	projectData     *os.Root
	projectsInfo    os.FileInfo
	projectDataInfo os.FileInfo
}

func acquireProjectApplyLock(projectRoot, dataDir, projectID string) (*projectApplyLock, error) {
	project, err := pathguard.Open(projectRoot)
	if err != nil {
		return nil, err
	}
	defer project.Close()
	data, err := pathguard.Open(dataDir)
	if err != nil {
		return nil, err
	}
	defer data.Close()
	return acquireProjectApplyLockRoot(project, data, projectID)
}

func acquireProjectApplyLockRoot(project, data *pathguard.Directory, projectID string) (*projectApplyLock, error) {
	if !safeIdentifier(projectID) {
		return nil, errors.New("invalid project ID for apply lock")
	}
	if project == nil || project.Root == nil || data == nil || data.Root == nil {
		return nil, errors.New("initialized project and data roots are required")
	}
	// DataDir is the trusted application boundary. Unix first locks the pinned
	// project root and, after rooted initialization, also locks the pinned
	// project-data directory so replacing the project-root pathname cannot
	// create a second transaction owner. Windows uses one DataDir-identity and
	// project-ID named mutex. Replacing DataDir itself remains outside this
	// cooperative trust boundary; retained identities and path rechecks fail
	// the active operation closed when such replacement is observed.
	platform, err := acquireApplyPlatformLock(project.Root, data.Root, data.Path, projectID, applyLockTimeout)
	if err != nil {
		return nil, err
	}
	return &projectApplyLock{platform: platform}, nil
}

func (lock *projectApplyLock) initializeProjectData(data *pathguard.Directory, projectID string, ensureRootDir func(*os.Root, string, fs.FileMode) error) error {
	if lock == nil || lock.platform == nil || data == nil || data.Root == nil {
		return errors.New("initialized apply lock and data root are required")
	}
	if err := ensureApplySubdirectory(data.Root, "projects", ensureRootDir); err != nil {
		return err
	}
	projectsPath := filepath.Join(data.Path, "projects")
	projectsInfo, err := data.Root.Lstat("projects")
	if err != nil {
		return err
	}
	projects, err := data.Root.OpenRoot("projects")
	if err != nil {
		return err
	}
	openedProjects, err := projects.Stat(".")
	if err != nil || !os.SameFile(projectsInfo, openedProjects) {
		_ = projects.Close()
		return errors.New("projects directory identity changed while opening")
	}
	if err := ensureApplySubdirectory(projects, projectID, ensureRootDir); err != nil {
		_ = projects.Close()
		return err
	}
	projectDataPath := filepath.Join(projectsPath, projectID)
	projectDataInfo, err := projects.Lstat(projectID)
	if err != nil {
		_ = projects.Close()
		return err
	}
	projectData, err := projects.OpenRoot(projectID)
	if err != nil {
		_ = projects.Close()
		return err
	}
	openedProjectData, err := projectData.Stat(".")
	if err != nil || !os.SameFile(projectDataInfo, openedProjectData) {
		_ = projectData.Close()
		_ = projects.Close()
		return errors.New("project data directory identity changed while opening")
	}
	if err := attachApplyProjectDataLock(lock.platform, projectData, applyLockTimeout); err != nil {
		_ = projectData.Close()
		_ = projects.Close()
		return err
	}
	lock.projectDataPath = projectDataPath
	lock.projects = projects
	lock.projectData = projectData
	lock.projectsInfo = projectsInfo
	lock.projectDataInfo = projectDataInfo
	return nil
}

func (lock *projectApplyLock) verifyIdentity(roots *applyRoots, projectPath, dataPath, projectID string) error {
	if lock == nil || roots == nil || roots.project == nil || roots.data == nil {
		return errors.New("apply transaction identity is unavailable")
	}
	for label, check := range map[string]struct {
		path string
		info os.FileInfo
	}{
		"project root": {projectPath, roots.project.Info()},
		"data root":    {dataPath, roots.data.Info()},
	} {
		current, err := pathguard.Open(check.path)
		if err != nil {
			return fmt.Errorf("%s identity changed: %w", label, err)
		}
		same := os.SameFile(check.info, current.Info())
		closeErr := current.Close()
		if closeErr != nil {
			return closeErr
		}
		if !same {
			return fmt.Errorf("%s identity changed", label)
		}
	}
	if lock.projects != nil || lock.projectData != nil {
		projectsInfo, err := roots.data.Root.Lstat("projects")
		if err != nil || !os.SameFile(lock.projectsInfo, projectsInfo) {
			return errors.New("projects directory identity changed")
		}
		projectDataInfo, err := lock.projects.Lstat(projectID)
		if err != nil || !os.SameFile(lock.projectDataInfo, projectDataInfo) {
			return errors.New("project data directory identity changed")
		}
	}
	return nil
}

func ensureApplySubdirectory(root *os.Root, name string, ensure func(*os.Root, string, fs.FileMode) error) error {
	if err := rejectDirectoryCaseCollision(root, name); err != nil {
		return err
	}
	before, err := root.Lstat(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (before == nil || !before.IsDir() || isApplyRedirect(before)) {
		return fmt.Errorf("apply state directory %q is redirected or not a directory", name)
	}
	if ensure == nil {
		ensure = atomicfile.EnsureRootDir
	}
	if err := ensure(root, name, 0o700); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil || info == nil || !info.IsDir() || isApplyRedirect(info) {
		return fmt.Errorf("apply state directory %q is redirected or not a directory", name)
	}
	if before != nil && !os.SameFile(before, info) {
		return fmt.Errorf("apply state directory %q changed while ensuring durability", name)
	}
	return nil
}

func (lock *projectApplyLock) Release() error {
	if lock == nil {
		return nil
	}
	var projectDataErr, projectsErr error
	if lock.projectData != nil {
		projectDataErr = lock.projectData.Close()
	}
	if lock.projects != nil {
		projectsErr = lock.projects.Close()
	}
	return errors.Join(projectDataErr, projectsErr, releaseApplyPlatformLock(lock.platform))
}

func Run(opts Options) (result Result, retErr error) {
	ctx, roots, err := openInputs(opts)
	if err != nil {
		return Result{}, err
	}
	defer func() { retErr = errors.Join(retErr, roots.Close()) }()
	result = baseResult(ctx)
	version, err := reviewv2.DetectVersionExpected(opts.ProjectRoot, roots.project.Info())
	if err != nil {
		return Result{}, err
	}
	if version == reviewv2.VersionLegacy {
		if _, err := reviewv2.LoadExpected(opts.ProjectRoot, roots.project.Info()); err != nil {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("legacy review ledger unexpectedly loaded without a migration requirement")
	}
	current, err := cursor.LoadReadOnlyRoot(roots.data.Root, ctx.Packet.ProjectID, ctx.Packet.SessionID)
	if err != nil {
		return Result{}, err
	}
	if err := preflightCursorAndExactReceipt(roots.data.Root, current, ctx); err != nil {
		return Result{}, err
	}
	lock, err := acquireProjectApplyLockRoot(roots.project, roots.data, ctx.Packet.ProjectID)
	if err != nil {
		return Result{}, err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	verifyIdentity := func() error {
		return lock.verifyIdentity(roots, opts.ProjectRoot, opts.DataDir, ctx.Packet.ProjectID)
	}
	if err := verifyIdentity(); err != nil {
		return Result{}, err
	}
	current, err = cursor.LoadReadOnlyRoot(roots.data.Root, ctx.Packet.ProjectID, ctx.Packet.SessionID)
	if err != nil {
		return Result{}, err
	}
	if err := preflightCursorAndExactReceipt(roots.data.Root, current, ctx); err != nil {
		return Result{}, err
	}
	if err := verifyIdentity(); err != nil {
		return Result{}, err
	}
	if err := lock.initializeProjectData(roots.data, ctx.Packet.ProjectID, opts.hooks.ensureRootDir); err != nil {
		return Result{}, err
	}
	if err := verifyIdentity(); err != nil {
		return Result{}, err
	}
	store := cursor.Store{Root: lock.projectDataPath, ExpectedRoot: lock.projectDataInfo}
	current, err = store.Load(ctx.Packet.SessionID)
	if err != nil {
		return Result{}, err
	}
	if !cursorCompatibleWithInput(current, ctx.Packet) {
		return Result{}, cursor.ErrStale
	}
	receipt, found, otherReceipts, err := scanReceipts(lock.projectData, ctx, opts.hooks)
	if err != nil {
		return Result{}, err
	}
	if err := rejectOutstandingProjectReceipts(store, otherReceipts); err != nil {
		return Result{}, err
	}
	if found {
		return recoverReceipt(store, current, receipt, ctx, opts, lock, roots.project.Info(), verifyIdentity)
	}
	if !cursorAtBoundary(current, ctx.Packet.ExpectedCursor) {
		return Result{}, cursor.ErrStale
	}
	if err := verifyIdentity(); err != nil {
		return Result{}, err
	}
	accepted, err := reviewv2.LoadExpected(opts.ProjectRoot, roots.project.Info())
	if err != nil {
		return Result{}, err
	}
	if accepted.Legacy.ProjectID != ctx.Packet.ProjectID {
		return Result{}, fmt.Errorf("ledger project ID does not match evidence")
	}
	baselineUsage, err := reviewv2.SnapshotUsageExpected(opts.ProjectRoot, roots.project.Info())
	if err != nil {
		return Result{}, err
	}
	if digestLedgerSnapshot(baselineUsage.Files) != digestLedgerSnapshot(accepted.Snapshot.Files) {
		return Result{}, errors.New("ledger namespace changed after loading")
	}
	changes, err := proposal.Validate(ctx.Proposal, ctx.Packet, accepted.Legacy)
	if err != nil {
		return Result{}, err
	}
	plan, err := reviewv2.ApplyChangeSet(accepted, changes)
	if err != nil {
		return Result{}, err
	}
	if err := validateLedgerTargetUsage(baselineUsage, plan); err != nil {
		return Result{}, err
	}
	receipt, err = newPreparedReceipt(ctx, plan, digestLedgerSnapshot(baselineUsage.Files))
	if err != nil {
		return Result{}, err
	}
	if opts.hooks.afterRender != nil {
		if err := opts.hooks.afterRender(); err != nil {
			return Result{}, err
		}
	}
	if err := verifyIdentity(); err != nil {
		return Result{}, err
	}
	if err := verifyLedgerNamespace(opts.ProjectRoot, roots.project.Info(), receipt); err != nil {
		return Result{}, err
	}
	if err := verifyIdentity(); err != nil {
		return Result{}, err
	}
	if err := saveReceipt(lock.projectData, receipt, opts.hooks); err != nil {
		return Result{}, err
	}
	if opts.hooks.afterPreparedReceipt != nil {
		if err := opts.hooks.afterPreparedReceipt(); err != nil {
			return Result{}, err
		}
	}
	if err := verifyIdentity(); err != nil {
		return Result{}, err
	}
	written, err := applyReceiptFiles(opts.ProjectRoot, roots.project.Info(), receipt, opts.hooks, verifyIdentity)
	if err != nil {
		return Result{}, err
	}
	receipt.State = receiptApplied
	receipt.ChangedFiles = receiptPlannedChanges(receipt)
	if err := verifyIdentity(); err != nil {
		return Result{}, err
	}
	if err := saveReceipt(lock.projectData, receipt, opts.hooks); err != nil {
		return Result{}, err
	}
	if opts.hooks.afterAppliedReceipt != nil {
		if err := opts.hooks.afterAppliedReceipt(); err != nil {
			return Result{}, err
		}
	}
	return finishReceipt(store, current, receipt, opts, written, roots.project.Info(), verifyIdentity)
}

func preflightCursorAndExactReceipt(dataRoot *os.Root, current cursor.Cursor, ctx inputContext) error {
	if cursorAtBoundary(current, ctx.Packet.ExpectedCursor) {
		return nil
	}
	if !cursorAtOrBeyond(current, ctx.Packet.NextCursor) {
		return cursor.ErrStale
	}
	_, found, err := loadExactReceiptReadOnly(dataRoot, ctx)
	if err != nil {
		return err
	}
	if !found {
		return cursor.ErrStale
	}
	return nil
}

func rejectOutstandingProjectReceipts(store cursor.Store, receipts []applyReceipt) error {
	for _, receipt := range receipts {
		if receipt.State != receiptApplied {
			return ErrPendingReceiptConflict
		}
		current, err := store.Load(receipt.SessionID)
		if err != nil {
			return err
		}
		if !cursorAtOrBeyond(current, receipt.NextCursor) {
			return ErrPendingReceiptConflict
		}
	}
	return nil
}

func openInputs(opts Options) (_ inputContext, roots *applyRoots, retErr error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	for label, value := range map[string]string{
		"proposal path": opts.ProposalPath, "evidence path": opts.EvidencePath,
		"project root": opts.ProjectRoot, "data directory": opts.DataDir,
	} {
		if strings.TrimSpace(value) == "" {
			return inputContext{}, nil, fmt.Errorf("%s is required", label)
		}
	}
	projectRoot, err := pathguard.Open(opts.ProjectRoot)
	if err != nil {
		return inputContext{}, nil, fmt.Errorf("invalid project root: %w", err)
	}
	if opts.ExpectedProjectRoot != nil && !os.SameFile(opts.ExpectedProjectRoot, projectRoot.Info()) {
		_ = projectRoot.Close()
		return inputContext{}, nil, errors.New("project root does not match expected identity")
	}
	dataRoot, err := pathguard.Open(opts.DataDir)
	if err != nil {
		_ = projectRoot.Close()
		return inputContext{}, nil, fmt.Errorf("invalid data directory: %w", err)
	}
	roots = &applyRoots{project: projectRoot, data: dataRoot}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, roots.Close())
			roots = nil
		}
	}()

	proposalBody, proposalDigest, err := readBoundedRegular(opts.ProposalPath, maxInputBytes, "proposal", opts.hooks.duringInputRead)
	if err != nil {
		return inputContext{}, nil, err
	}
	if opts.hooks.afterInputRead != nil {
		if err := opts.hooks.afterInputRead("proposal"); err != nil {
			return inputContext{}, nil, err
		}
	}
	p, err := proposal.Decode(bytes.NewReader(proposalBody))
	if err != nil {
		return inputContext{}, nil, err
	}
	evidenceBody, evidenceFileDigest, err := readBoundedRegular(opts.EvidencePath, maxInputBytes, "evidence", opts.hooks.duringInputRead)
	if err != nil {
		return inputContext{}, nil, err
	}
	if opts.hooks.afterInputRead != nil {
		if err := opts.hooks.afterInputRead("evidence"); err != nil {
			return inputContext{}, nil, err
		}
	}
	if err := inspectJSONObject(evidenceBody); err != nil {
		return inputContext{}, nil, fmt.Errorf("invalid evidence JSON: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(evidenceBody))
	dec.DisallowUnknownFields()
	var packet evidence.Packet
	if err := dec.Decode(&packet); err != nil {
		return inputContext{}, nil, fmt.Errorf("decode evidence packet: %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return inputContext{}, nil, err
	}
	packetDigest, err := evidence.Digest(packet)
	if err != nil {
		return inputContext{}, nil, err
	}
	if p.EvidencePacketSHA256 != packetDigest {
		return inputContext{}, nil, fmt.Errorf("proposal evidence digest does not match input packet")
	}
	if !safeIdentifier(packet.ProjectID) || !safeIdentifier(packet.SessionID) {
		return inputContext{}, nil, fmt.Errorf("invalid packet identity")
	}
	if packet.ProjectID != p.ProjectID || packet.SessionID != p.SessionID {
		return inputContext{}, nil, fmt.Errorf("proposal and evidence identities differ")
	}
	cfg, err := config.LoadRoot(dataRoot.Root, "config.toml")
	if err != nil {
		return inputContext{}, nil, fmt.Errorf("load initialized project mapping: %w", err)
	}
	matches := 0
	for _, mapping := range cfg.Projects {
		if mapping.ID != packet.ProjectID {
			continue
		}
		mappedRoot, mapErr := pathguard.Open(mapping.Root)
		if mapErr != nil {
			return inputContext{}, nil, fmt.Errorf("initialized project mapping does not match requested root")
		}
		mapped := os.SameFile(projectRoot.Info(), mappedRoot.Info())
		closeErr := mappedRoot.Close()
		if closeErr != nil || !mapped {
			return inputContext{}, nil, fmt.Errorf("initialized project mapping does not match requested root")
		}
		matches++
	}
	if matches != 1 {
		return inputContext{}, nil, fmt.Errorf("project is not uniquely initialized")
	}
	return inputContext{
		Packet: packet, Proposal: p, ProposalDigest: proposalDigest,
		EvidenceFileDigest: evidenceFileDigest, EvidencePacketDigest: packetDigest,
	}, roots, nil
}

func readBoundedRegular(path string, limit int64, label string, duringRead func(string) error) ([]byte, string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", fmt.Errorf("invalid %s path", label)
	}
	parent, err := pathguard.Open(filepath.Dir(absolute))
	if err != nil {
		return nil, "", fmt.Errorf("open %s parent: %w", label, err)
	}
	defer parent.Close()
	file, before, err := parent.OpenRegular(filepath.Base(absolute))
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", label, err)
	}
	defer file.Close()
	if before.Size() > limit {
		return nil, "", fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(body)) > limit {
		return nil, "", fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	if duringRead != nil {
		if err := duringRead(label); err != nil {
			return nil, "", err
		}
	}
	afterHandle, err := file.Stat()
	if err != nil || afterHandle.Size() != before.Size() || !afterHandle.ModTime().Equal(before.ModTime()) || afterHandle.Mode() != before.Mode() {
		return nil, "", fmt.Errorf("%s changed while reading", label)
	}
	afterPath, err := parent.Root.Lstat(filepath.Base(absolute))
	if err != nil || !os.SameFile(before, afterPath) || afterPath.Size() != before.Size() || !afterPath.ModTime().Equal(before.ModTime()) {
		return nil, "", fmt.Errorf("%s changed while reading", label)
	}
	return body, digestBytes(body), nil
}

func applyReceiptFiles(projectRoot string, expectedRoot os.FileInfo, receipt applyReceipt, hooks applyHooks, verifyIdentity func() error) ([]string, error) {
	// ledger.Apply performs the final rooted preimage check immediately before
	// atomic replacement. As documented there, an uncooperative external writer
	// can still win the residual check-to-rename nanorace.
	files := append([]receiptFile(nil), receipt.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	var written []string
	for index, file := range files {
		if verifyIdentity != nil {
			if err := verifyIdentity(); err != nil {
				return written, err
			}
		}
		if err := verifyLedgerNamespace(projectRoot, expectedRoot, receipt); err != nil {
			return written, err
		}
		current, exists, mode, err := readProjectTarget(projectRoot, expectedRoot, file.RelativePath)
		if err != nil {
			return written, err
		}
		if exists && digestBytes(current) == file.TargetSHA256 && applyModeEqual(mode, file.TargetMode) {
			if err := syncProjectTargetPublication(projectRoot, expectedRoot, file.RelativePath, hooks.publicationSync()); err != nil {
				return written, fmt.Errorf("sync ledger file %s publication: %w", file.RelativePath, err)
			}
			continue
		}
		if exists != file.PreimageExists || (exists && (digestBytes(current) != file.PreimageSHA256 || !applyModeEqual(mode, file.PreimageMode))) {
			return written, fmt.Errorf("ledger file %s has an intervening user edit", file.RelativePath)
		}
		planned := ledger.PlannedFile{
			RelativePath: file.RelativePath, Data: append([]byte(nil), file.TargetData...), Perm: fs.FileMode(file.TargetMode),
			ExpectedData: append([]byte(nil), current...), ExpectedExists: exists, ExpectedPerm: mode.Perm(),
		}
		applyPlan := hooks.applyPlan
		if applyPlan == nil {
			applyPlan = ledger.ApplyExpected
		}
		changed, err := applyPlan(ledger.WritePlan{ProjectRoot: projectRoot, Files: []ledger.PlannedFile{planned}}, expectedRoot)
		if err != nil {
			return written, err
		}
		if len(changed) != 0 {
			written = append(written, file.RelativePath)
		}
		if hooks.afterFile != nil {
			if err := hooks.afterFile(index, file.RelativePath); err != nil {
				return written, err
			}
		}
	}
	if err := verifyLedgerNamespace(projectRoot, expectedRoot, receipt); err != nil {
		return written, err
	}
	return written, nil
}

func readProjectTarget(projectRoot string, expectedRoot os.FileInfo, relative string) ([]byte, bool, fs.FileMode, error) {
	directory, err := pathguard.Open(projectRoot)
	if err != nil {
		return nil, false, 0, err
	}
	defer directory.Close()
	if expectedRoot != nil && !os.SameFile(expectedRoot, directory.Info()) {
		return nil, false, 0, errors.New("opened project root does not match expected project root identity")
	}
	file, before, err := directory.OpenRegular(relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || targetParentMissing(directory, relative) {
			return nil, false, 0, nil
		}
		return nil, false, 0, err
	}
	defer file.Close()
	maximum := maxApplyTargetBytes(relative)
	if before.Size() > maximum {
		return nil, false, 0, fmt.Errorf("ledger file %s exceeds size limit", relative)
	}
	body, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(body)) > maximum {
		return nil, false, 0, fmt.Errorf("read ledger file %s", relative)
	}
	after, err := directory.Root.Lstat(filepath.FromSlash(relative))
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, false, 0, fmt.Errorf("ledger file %s changed while reading", relative)
	}
	return body, true, before.Mode().Perm(), nil
}

func targetParentMissing(root *pathguard.Directory, relative string) bool {
	parent, _, err := root.OpenDirectory(filepath.Dir(filepath.FromSlash(relative)))
	if parent != nil {
		_ = parent.Close()
	}
	return errors.Is(err, os.ErrNotExist)
}

func recoverReceipt(store cursor.Store, current cursor.Cursor, receipt applyReceipt, ctx inputContext, opts Options, lock *projectApplyLock, expectedRoot os.FileInfo, verifyIdentity func() error) (Result, error) {
	if err := receipt.matches(ctx); err != nil {
		return Result{}, err
	}
	if cursorAtOrBeyond(current, receipt.NextCursor) {
		if err := verifyIdentity(); err != nil {
			return Result{}, err
		}
		if err := verifyReceiptTargets(opts.ProjectRoot, expectedRoot, receipt, opts.hooks.publicationSync()); err != nil {
			return Result{}, err
		}
		if err := verifyLedgerNamespace(opts.ProjectRoot, expectedRoot, receipt); err != nil {
			return Result{}, err
		}
		result := baseResult(ctx)
		result.AlreadyApplied = true
		return result, nil
	}
	if !cursorAtBoundary(current, receipt.ExpectedCursor) {
		return Result{}, cursor.ErrStale
	}
	written, err := applyReceiptFiles(opts.ProjectRoot, expectedRoot, receipt, opts.hooks, verifyIdentity)
	if err != nil {
		return Result{}, err
	}
	if receipt.State == receiptPrepared {
		receipt.State = receiptApplied
		receipt.ChangedFiles = receiptPlannedChanges(receipt)
		if err := verifyIdentity(); err != nil {
			return Result{}, err
		}
		if err := saveReceipt(lock.projectData, receipt, opts.hooks); err != nil {
			return Result{}, err
		}
		if opts.hooks.afterAppliedReceipt != nil {
			if err := opts.hooks.afterAppliedReceipt(); err != nil {
				return Result{}, err
			}
		}
	}
	return finishReceipt(store, current, receipt, opts, written, expectedRoot, verifyIdentity)
}

func finishReceipt(store cursor.Store, current cursor.Cursor, receipt applyReceipt, opts Options, written []string, expectedRoot os.FileInfo, verifyIdentity func() error) (Result, error) {
	if verifyIdentity != nil {
		if err := verifyIdentity(); err != nil {
			return Result{}, err
		}
	}
	if err := verifyReceiptTargets(opts.ProjectRoot, expectedRoot, receipt, opts.hooks.publicationSync()); err != nil {
		return Result{}, err
	}
	if err := verifyLedgerNamespace(opts.ProjectRoot, expectedRoot, receipt); err != nil {
		return Result{}, err
	}
	if opts.hooks.beforeCAS != nil {
		if err := opts.hooks.beforeCAS(); err != nil {
			return Result{}, err
		}
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	if !current.UpdatedAt.IsZero() && now.Before(current.UpdatedAt) {
		now = current.UpdatedAt
	}
	if verifyIdentity != nil {
		if err := verifyIdentity(); err != nil {
			return Result{}, err
		}
	}
	if err := verifyReceiptTargets(opts.ProjectRoot, expectedRoot, receipt, opts.hooks.publicationSync()); err != nil {
		return Result{}, err
	}
	if err := verifyLedgerNamespace(opts.ProjectRoot, expectedRoot, receipt); err != nil {
		return Result{}, err
	}
	next := cursor.Cursor{SessionID: receipt.SessionID, LastLine: receipt.NextCursor.Line, LastHash: receipt.NextCursor.SourceHash, UpdatedAt: now}
	if err := store.Commit(receipt.SessionID, current, next); err != nil {
		if !errors.Is(err, cursor.ErrStale) {
			return Result{}, err
		}
		if verifyIdentity != nil {
			if err := verifyIdentity(); err != nil {
				return Result{}, err
			}
		}
		latest, loadErr := store.Load(receipt.SessionID)
		if loadErr != nil || !cursorAtOrBeyond(latest, receipt.NextCursor) {
			return Result{}, cursor.ErrStale
		}
		if verifyErr := verifyReceiptTargets(opts.ProjectRoot, expectedRoot, receipt, opts.hooks.publicationSync()); verifyErr != nil {
			return Result{}, verifyErr
		}
		if verifyErr := verifyLedgerNamespace(opts.ProjectRoot, expectedRoot, receipt); verifyErr != nil {
			return Result{}, verifyErr
		}
		result := resultFromReceipt(receipt)
		result.AlreadyApplied = true
		return result, nil
	}
	result := resultFromReceipt(receipt)
	result.ChangedFiles = append([]string(nil), receipt.ChangedFiles...)
	result.CursorAdvanced = true
	return result, nil
}

func verifyReceiptTargets(projectRoot string, expectedRoot os.FileInfo, receipt applyReceipt, syncPublication func(*os.Root, string) error) error {
	for _, file := range receipt.Files {
		body, exists, mode, err := readProjectTarget(projectRoot, expectedRoot, file.RelativePath)
		if err != nil {
			return err
		}
		if !exists || digestBytes(body) != file.TargetSHA256 || !applyModeEqual(mode, file.TargetMode) {
			return fmt.Errorf("ledger file %s does not match applied receipt", file.RelativePath)
		}
		if err := syncProjectTargetPublication(projectRoot, expectedRoot, file.RelativePath, syncPublication); err != nil {
			return fmt.Errorf("sync ledger file %s publication: %w", file.RelativePath, err)
		}
	}
	return nil
}

type ledgerSnapshotEntry struct {
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	Mode         uint32 `json:"mode"`
}

func digestLedgerSnapshot(files []ledger.SnapshotFile) string {
	entries := make([]ledgerSnapshotEntry, 0, len(files))
	for _, file := range files {
		entries = append(entries, ledgerSnapshotEntry{
			RelativePath: file.RelativePath,
			SHA256:       file.SHA256,
			Mode:         normalizeApplyMode(file.Perm),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelativePath < entries[j].RelativePath })
	body, err := json.Marshal(entries)
	if err != nil {
		panic("marshal ledger snapshot: " + err.Error())
	}
	return digestBytes(body)
}

func validateLedgerTargetUsage(baseline ledger.SnapshotUsage, plan ledger.WritePlan) error {
	targets := make(map[string]ledger.SnapshotFile, len(baseline.Files)+len(plan.Files))
	for _, file := range baseline.Files {
		targets[file.RelativePath] = file
	}
	entries := baseline.DirectoryEntries
	for _, planned := range plan.Files {
		if !isApplySnapshotPath(planned.RelativePath) {
			continue
		}
		if _, exists := targets[planned.RelativePath]; !exists && ledger.IsCollectionSnapshotPath(planned.RelativePath) {
			entries++
		}
		targets[planned.RelativePath] = ledger.SnapshotFile{
			RelativePath: planned.RelativePath,
			SHA256:       digestBytes(planned.Data),
			Perm:         planned.Perm,
			Size:         int64(len(planned.Data)),
		}
	}
	files := make([]ledger.SnapshotFile, 0, len(targets))
	for _, file := range targets {
		maximum := maxApplyTargetBytes(file.RelativePath)
		if file.Size < 0 || file.Size > maximum {
			return fmt.Errorf("apply target cannot be recovered within ledger limits: %s exceeds its document byte budget", file.RelativePath)
		}
		remaining := file.Size
		for {
			chunk := min(remaining, int64(ledger.MaxDocumentBytes))
			files = append(files, ledger.SnapshotFile{RelativePath: file.RelativePath, Size: chunk})
			remaining -= chunk
			if remaining == 0 {
				break
			}
		}
	}
	if err := ledger.ValidateSnapshotUsage(ledger.SnapshotUsage{Files: files, DirectoryEntries: entries}); err != nil {
		return fmt.Errorf("apply target cannot be recovered within ledger limits: %w", err)
	}
	return nil
}

func verifyLedgerNamespace(projectRoot string, expectedRoot os.FileInfo, receipt applyReceipt) error {
	files, err := snapshotApplyNamespace(projectRoot, expectedRoot)
	if err != nil {
		return fmt.Errorf("read ledger namespace snapshot: %w", err)
	}
	current := make(map[string]ledger.SnapshotFile, len(files))
	for _, file := range files {
		if _, duplicate := current[file.RelativePath]; duplicate {
			return fmt.Errorf("duplicate ledger snapshot path %q", file.RelativePath)
		}
		current[file.RelativePath] = file
	}
	for _, planned := range receipt.Files {
		if !isApplySnapshotPath(planned.RelativePath) {
			continue
		}
		file, exists := current[planned.RelativePath]
		if exists && file.SHA256 == planned.TargetSHA256 && applyModeEqual(file.Perm, planned.TargetMode) {
			if planned.PreimageExists {
				current[planned.RelativePath] = ledger.SnapshotFile{
					RelativePath: planned.RelativePath,
					SHA256:       planned.PreimageSHA256,
					Perm:         fs.FileMode(planned.PreimageMode),
					Size:         file.Size,
				}
			} else {
				delete(current, planned.RelativePath)
			}
			continue
		}
		if exists && planned.PreimageExists && file.SHA256 == planned.PreimageSHA256 && applyModeEqual(file.Perm, planned.PreimageMode) {
			continue
		}
		if !exists && !planned.PreimageExists {
			continue
		}
		return fmt.Errorf("ledger namespace path %s has an intervening edit", planned.RelativePath)
	}
	normalized := make([]ledger.SnapshotFile, 0, len(current))
	for _, file := range current {
		normalized = append(normalized, file)
	}
	if digestLedgerSnapshot(normalized) != receipt.LedgerSnapshotSHA256 {
		return errors.New("ledger namespace has an intervening addition, removal, or edit")
	}
	return nil
}

func snapshotApplyNamespace(projectRoot string, expectedRoot os.FileInfo) ([]ledger.SnapshotFile, error) {
	files, err := ledger.SnapshotExpected(projectRoot, expectedRoot)
	if err != nil {
		return nil, err
	}
	for _, relative := range []string{
		reviewv2.HistoryRelativePath,
		reviewv2.MachineLedgerRelativePath,
		reviewv2.ReviewRelativePath,
	} {
		body, exists, mode, readErr := readProjectTarget(projectRoot, expectedRoot, relative)
		if readErr != nil {
			return nil, readErr
		}
		if !exists {
			continue
		}
		files = append(files, ledger.SnapshotFile{
			RelativePath: relative,
			SHA256:       digestBytes(body),
			Perm:         mode.Perm(),
			Size:         int64(len(body)),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	return files, nil
}

func isApplySnapshotPath(relative string) bool {
	return ledger.IsSnapshotPath(relative) || relative == reviewv2.HistoryRelativePath ||
		relative == reviewv2.MachineLedgerRelativePath || relative == reviewv2.ReviewRelativePath
}

func maxApplyTargetBytes(relative string) int64 {
	if relative == reviewv2.MachineLedgerRelativePath {
		return reviewv2.MaxMachineLedgerBytes
	}
	return ledger.MaxDocumentBytes
}

func syncProjectTargetPublication(projectRoot string, expectedRoot os.FileInfo, relative string, syncPublication func(*os.Root, string) error) error {
	directory, err := pathguard.Open(projectRoot)
	if err != nil {
		return err
	}
	defer directory.Close()
	if expectedRoot != nil && !os.SameFile(expectedRoot, directory.Info()) {
		return errors.New("opened project root does not match expected project root identity")
	}
	return syncPublication(directory.Root, filepath.FromSlash(relative))
}

func (hooks applyHooks) publicationSync() func(*os.Root, string) error {
	if hooks.syncPublication != nil {
		return hooks.syncPublication
	}
	return atomicfile.SyncRootPublication
}

func baseResult(ctx inputContext) Result {
	return Result{ProjectID: ctx.Packet.ProjectID, SessionID: ctx.Packet.SessionID, FromCursor: ctx.Packet.FromCursor, ToCursor: ctx.Packet.ToCursor}
}

func resultFromReceipt(receipt applyReceipt) Result {
	return Result{ProjectID: receipt.ProjectID, SessionID: receipt.SessionID, FromCursor: receipt.FromCursor, ToCursor: receipt.ToCursor}
}

func cursorAtBoundary(value cursor.Cursor, boundary evidence.CursorBoundary) bool {
	if boundary.Line == 0 {
		return value == (cursor.Cursor{})
	}
	return value.SessionID != "" && value.LastLine == boundary.Line && strings.EqualFold(value.LastHash, boundary.SourceHash)
}

func cursorCompatibleWithInput(value cursor.Cursor, packet evidence.Packet) bool {
	return cursorAtBoundary(value, packet.ExpectedCursor) || cursorAtOrBeyond(value, packet.NextCursor)
}

func cursorAtOrBeyond(value cursor.Cursor, boundary evidence.CursorBoundary) bool {
	if value.LastLine < boundary.Line || value.SessionID == "" {
		return false
	}
	if value.LastLine == boundary.Line {
		return strings.EqualFold(value.LastHash, boundary.SourceHash)
	}
	return true
}

func safeIdentifier(value string) bool {
	return value != "" && value != "." && value != ".." && safeApplyID.MatchString(value)
}

func digestBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func receiptPlannedChanges(receipt applyReceipt) []string {
	result := make([]string, 0, len(receipt.Files))
	for _, file := range receipt.Files {
		if !file.PreimageExists || file.PreimageSHA256 != file.TargetSHA256 || file.PreimageMode != file.TargetMode {
			result = append(result, file.RelativePath)
		}
	}
	v2Order := map[string]int{
		reviewv2.HistoryRelativePath:       0,
		reviewv2.MachineLedgerRelativePath: 1,
		reviewv2.ReviewRelativePath:        2,
	}
	sort.Slice(result, func(i, j int) bool {
		left, leftV2 := v2Order[result[i]]
		right, rightV2 := v2Order[result[j]]
		if leftV2 && rightV2 {
			return left < right
		}
		return result[i] < result[j]
	})
	return result
}

func requireJSONEOF(dec *json.Decoder) error {
	var trailing any
	if err := dec.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("trailing JSON data: %w", err)
	}
	return errors.New("trailing JSON value")
}
