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

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/proposal"
)

const maxInputBytes = 4 << 20

var safeApplyID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Options struct {
	ProposalPath, EvidencePath, ProjectRoot, DataDir string
	Now                                              func() time.Time
	hooks                                            applyHooks
}

type Result struct {
	ProjectID, SessionID           string
	FromCursor, ToCursor           int
	ChangedFiles                   []string
	CursorAdvanced, AlreadyApplied bool
}

type applyHooks struct {
	afterRender          func() error
	afterPreparedReceipt func() error
	afterFile            func(index int, relativePath string) error
	afterAppliedReceipt  func() error
	beforeCAS            func() error
	afterInputRead       func(kind string) error
	duringInputRead      func(kind string) error
}

type inputContext struct {
	Packet               evidence.Packet
	Proposal             proposal.Proposal
	ProposalDigest       string
	EvidenceFileDigest   string
	EvidencePacketDigest string
}

const applyLockTimeout = 2 * time.Second

type projectApplyLock struct {
	file            *os.File
	projectDataPath string
}

func acquireProjectApplyLock(dataDir, projectID string) (*projectApplyLock, error) {
	if !safeIdentifier(projectID) {
		return nil, errors.New("invalid project ID for apply lock")
	}
	data, err := pathguard.Open(dataDir)
	if err != nil {
		return nil, err
	}
	defer data.Close()
	if err := ensureApplySubdirectory(data.Root, "projects"); err != nil {
		return nil, err
	}
	projectsPath := filepath.Join(data.Path, "projects")
	projects, err := pathguard.Open(projectsPath)
	if err != nil {
		return nil, err
	}
	defer projects.Close()
	if err := ensureApplySubdirectory(projects.Root, projectID); err != nil {
		return nil, err
	}
	projectDataPath := filepath.Join(projectsPath, projectID)
	projectData, err := pathguard.Open(projectDataPath)
	if err != nil {
		return nil, err
	}
	defer projectData.Close()
	const lockName = ".apply.lock"
	if err := rejectEntryCaseCollisions(projectData.Root, lockName); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(applyLockTimeout)
	for {
		file, err := openStableApplyLockFile(projectData.Root, lockName)
		if err != nil {
			return nil, err
		}
		locked, err := tryApplyPlatformLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire project apply lock: %w", err)
		}
		if locked {
			return &projectApplyLock{file: file, projectDataPath: projectDataPath}, nil
		}
		_ = file.Close()
		if !time.Now().Before(deadline) {
			return nil, errors.New("project apply transaction remains locked by a live owner")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ensureApplySubdirectory(root *os.Root, name string) error {
	if err := rejectDirectoryCaseCollision(root, name); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err := root.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = root.Lstat(name)
	}
	if err != nil || info == nil || !info.IsDir() || isApplyRedirect(info) {
		return fmt.Errorf("apply state directory %q is redirected or not a directory", name)
	}
	return nil
}

func openStableApplyLockFile(root *os.Root, name string) (*os.File, error) {
	for {
		before, err := root.Lstat(name)
		found := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if found && (!before.Mode().IsRegular() || isApplyRedirect(before)) {
			return nil, errors.New("apply lock is redirected or not regular")
		}
		flags := os.O_RDWR
		if !found {
			flags |= os.O_CREATE | os.O_EXCL
		}
		file, err := root.OpenFile(name, flags, 0o600)
		if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		opened, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		after, err := root.Lstat(name)
		if err != nil || !os.SameFile(opened, after) || (found && !os.SameFile(before, opened)) {
			_ = file.Close()
			continue
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, err
		}
		return file, nil
	}
}

func (lock *projectApplyLock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	return errors.Join(unlockApplyPlatformLock(lock.file), lock.file.Close())
}

func Run(opts Options) (result Result, retErr error) {
	ctx, err := openInputs(opts)
	if err != nil {
		return Result{}, err
	}
	result = baseResult(ctx)
	lock, err := acquireProjectApplyLock(opts.DataDir, ctx.Packet.ProjectID)
	if err != nil {
		return Result{}, err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()

	store := cursor.Store{Root: lock.projectDataPath}
	current, err := store.Load(ctx.Packet.SessionID)
	if err != nil {
		return Result{}, err
	}
	receipt, found, err := loadReceipt(lock.projectDataPath, ctx.ProposalDigest)
	if err != nil {
		return Result{}, err
	}
	if found {
		return recoverReceipt(store, current, receipt, ctx, opts, lock.projectDataPath)
	}
	if !cursorAtBoundary(current, ctx.Packet.ExpectedCursor) {
		return Result{}, cursor.ErrStale
	}
	state, err := ledger.Load(opts.ProjectRoot)
	if err != nil {
		return Result{}, err
	}
	if state.ProjectID != ctx.Packet.ProjectID {
		return Result{}, fmt.Errorf("ledger project ID does not match evidence")
	}
	changes, err := proposal.Validate(ctx.Proposal, ctx.Packet, state)
	if err != nil {
		return Result{}, err
	}
	plan, err := ledger.Render(state, changes)
	if err != nil {
		return Result{}, err
	}
	if opts.hooks.afterRender != nil {
		if err := opts.hooks.afterRender(); err != nil {
			return Result{}, err
		}
	}
	receipt, err = newPreparedReceipt(ctx, plan)
	if err != nil {
		return Result{}, err
	}
	if err := saveReceipt(lock.projectDataPath, receipt); err != nil {
		return Result{}, err
	}
	if opts.hooks.afterPreparedReceipt != nil {
		if err := opts.hooks.afterPreparedReceipt(); err != nil {
			return Result{}, err
		}
	}
	written, err := applyReceiptFiles(opts.ProjectRoot, receipt, opts.hooks)
	if err != nil {
		return Result{}, err
	}
	receipt.State = receiptApplied
	receipt.ChangedFiles = receiptPlannedChanges(receipt)
	if err := saveReceipt(lock.projectDataPath, receipt); err != nil {
		return Result{}, err
	}
	if opts.hooks.afterAppliedReceipt != nil {
		if err := opts.hooks.afterAppliedReceipt(); err != nil {
			return Result{}, err
		}
	}
	return finishReceipt(store, current, receipt, opts, written)
}

func openInputs(opts Options) (inputContext, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	for label, value := range map[string]string{
		"proposal path": opts.ProposalPath, "evidence path": opts.EvidencePath,
		"project root": opts.ProjectRoot, "data directory": opts.DataDir,
	} {
		if strings.TrimSpace(value) == "" {
			return inputContext{}, fmt.Errorf("%s is required", label)
		}
	}
	projectRoot, err := pathguard.Open(opts.ProjectRoot)
	if err != nil {
		return inputContext{}, fmt.Errorf("invalid project root: %w", err)
	}
	defer projectRoot.Close()
	dataRoot, err := pathguard.Open(opts.DataDir)
	if err != nil {
		return inputContext{}, fmt.Errorf("invalid data directory: %w", err)
	}
	defer dataRoot.Close()

	proposalBody, proposalDigest, err := readBoundedRegular(opts.ProposalPath, maxInputBytes, "proposal", opts.hooks.duringInputRead)
	if err != nil {
		return inputContext{}, err
	}
	if opts.hooks.afterInputRead != nil {
		if err := opts.hooks.afterInputRead("proposal"); err != nil {
			return inputContext{}, err
		}
	}
	p, err := proposal.Decode(bytes.NewReader(proposalBody))
	if err != nil {
		return inputContext{}, err
	}
	evidenceBody, evidenceFileDigest, err := readBoundedRegular(opts.EvidencePath, maxInputBytes, "evidence", opts.hooks.duringInputRead)
	if err != nil {
		return inputContext{}, err
	}
	if opts.hooks.afterInputRead != nil {
		if err := opts.hooks.afterInputRead("evidence"); err != nil {
			return inputContext{}, err
		}
	}
	if err := inspectJSONObject(evidenceBody); err != nil {
		return inputContext{}, fmt.Errorf("invalid evidence JSON: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(evidenceBody))
	dec.DisallowUnknownFields()
	var packet evidence.Packet
	if err := dec.Decode(&packet); err != nil {
		return inputContext{}, fmt.Errorf("decode evidence packet: %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return inputContext{}, err
	}
	packetDigest, err := evidence.Digest(packet)
	if err != nil {
		return inputContext{}, err
	}
	if p.EvidencePacketSHA256 != packetDigest {
		return inputContext{}, fmt.Errorf("proposal evidence digest does not match input packet")
	}
	if !safeIdentifier(packet.ProjectID) || !safeIdentifier(packet.SessionID) {
		return inputContext{}, fmt.Errorf("invalid packet identity")
	}
	if packet.ProjectID != p.ProjectID || packet.SessionID != p.SessionID {
		return inputContext{}, fmt.Errorf("proposal and evidence identities differ")
	}
	cfg, err := config.LoadRoot(dataRoot.Root, "config.toml")
	if err != nil {
		return inputContext{}, fmt.Errorf("load initialized project mapping: %w", err)
	}
	matches := 0
	for _, mapping := range cfg.Projects {
		if mapping.ID != packet.ProjectID {
			continue
		}
		mapped, mapErr := pathguard.SameDirectory(projectRoot.Path, mapping.Root)
		if mapErr != nil || !mapped {
			return inputContext{}, fmt.Errorf("initialized project mapping does not match requested root")
		}
		matches++
	}
	if matches != 1 {
		return inputContext{}, fmt.Errorf("project is not uniquely initialized")
	}
	return inputContext{
		Packet: packet, Proposal: p, ProposalDigest: proposalDigest,
		EvidenceFileDigest: evidenceFileDigest, EvidencePacketDigest: packetDigest,
	}, nil
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

func applyReceiptFiles(projectRoot string, receipt applyReceipt, hooks applyHooks) ([]string, error) {
	// ledger.Apply performs the final rooted preimage check immediately before
	// atomic replacement. As documented there, an uncooperative external writer
	// can still win the residual check-to-rename nanorace.
	files := append([]receiptFile(nil), receipt.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	var written []string
	for index, file := range files {
		current, exists, mode, err := readProjectTarget(projectRoot, file.RelativePath)
		if err != nil {
			return written, err
		}
		if exists && digestBytes(current) == file.TargetSHA256 && uint32(mode.Perm()) == file.TargetMode {
			continue
		}
		if exists != file.PreimageExists || (exists && (digestBytes(current) != file.PreimageSHA256 || uint32(mode.Perm()) != file.PreimageMode)) {
			return written, fmt.Errorf("ledger file %s has an intervening user edit", file.RelativePath)
		}
		planned := ledger.PlannedFile{
			RelativePath: file.RelativePath, Data: append([]byte(nil), file.TargetData...), Perm: fs.FileMode(file.TargetMode),
			ExpectedData: append([]byte(nil), current...), ExpectedExists: exists, ExpectedPerm: mode.Perm(),
		}
		changed, err := ledger.Apply(ledger.WritePlan{ProjectRoot: projectRoot, Files: []ledger.PlannedFile{planned}})
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
	return written, nil
}

func readProjectTarget(projectRoot, relative string) ([]byte, bool, fs.FileMode, error) {
	directory, err := pathguard.Open(projectRoot)
	if err != nil {
		return nil, false, 0, err
	}
	defer directory.Close()
	file, before, err := directory.OpenRegular(relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || targetParentMissing(directory.Path, relative) {
			return nil, false, 0, nil
		}
		return nil, false, 0, err
	}
	defer file.Close()
	if before.Size() > ledger.MaxDocumentBytes {
		return nil, false, 0, fmt.Errorf("ledger file %s exceeds size limit", relative)
	}
	body, err := io.ReadAll(io.LimitReader(file, ledger.MaxDocumentBytes+1))
	if err != nil || len(body) > ledger.MaxDocumentBytes {
		return nil, false, 0, fmt.Errorf("read ledger file %s", relative)
	}
	after, err := directory.Root.Lstat(filepath.FromSlash(relative))
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, false, 0, fmt.Errorf("ledger file %s changed while reading", relative)
	}
	return body, true, before.Mode().Perm(), nil
}

func targetParentMissing(root, relative string) bool {
	directory, remaining, err := pathguard.OpenDeepest(filepath.Join(root, filepath.Dir(filepath.FromSlash(relative))))
	if directory != nil {
		_ = directory.Close()
	}
	return err == nil && len(remaining) != 0
}

func recoverReceipt(store cursor.Store, current cursor.Cursor, receipt applyReceipt, ctx inputContext, opts Options, projectDataPath string) (Result, error) {
	if err := receipt.matches(ctx); err != nil {
		return Result{}, err
	}
	if cursorAtOrBeyond(current, receipt.NextCursor) {
		if err := verifyReceiptTargets(opts.ProjectRoot, receipt); err != nil {
			return Result{}, err
		}
		result := baseResult(ctx)
		result.AlreadyApplied = true
		return result, nil
	}
	if !cursorAtBoundary(current, receipt.ExpectedCursor) {
		return Result{}, cursor.ErrStale
	}
	written, err := applyReceiptFiles(opts.ProjectRoot, receipt, opts.hooks)
	if err != nil {
		return Result{}, err
	}
	if receipt.State == receiptPrepared {
		receipt.State = receiptApplied
		receipt.ChangedFiles = receiptPlannedChanges(receipt)
		if err := saveReceipt(projectDataPath, receipt); err != nil {
			return Result{}, err
		}
		if opts.hooks.afterAppliedReceipt != nil {
			if err := opts.hooks.afterAppliedReceipt(); err != nil {
				return Result{}, err
			}
		}
	}
	return finishReceipt(store, current, receipt, opts, written)
}

func finishReceipt(store cursor.Store, current cursor.Cursor, receipt applyReceipt, opts Options, written []string) (Result, error) {
	if err := verifyReceiptTargets(opts.ProjectRoot, receipt); err != nil {
		return Result{}, err
	}
	if opts.hooks.beforeCAS != nil {
		if err := opts.hooks.beforeCAS(); err != nil {
			return Result{}, err
		}
		if err := verifyReceiptTargets(opts.ProjectRoot, receipt); err != nil {
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
	next := cursor.Cursor{SessionID: receipt.SessionID, LastLine: receipt.NextCursor.Line, LastHash: receipt.NextCursor.SourceHash, UpdatedAt: now}
	if err := store.Commit(receipt.SessionID, current, next); err != nil {
		if !errors.Is(err, cursor.ErrStale) {
			return Result{}, err
		}
		latest, loadErr := store.Load(receipt.SessionID)
		if loadErr != nil || !cursorAtOrBeyond(latest, receipt.NextCursor) {
			return Result{}, cursor.ErrStale
		}
		if verifyErr := verifyReceiptTargets(opts.ProjectRoot, receipt); verifyErr != nil {
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

func verifyReceiptTargets(projectRoot string, receipt applyReceipt) error {
	for _, file := range receipt.Files {
		body, exists, mode, err := readProjectTarget(projectRoot, file.RelativePath)
		if err != nil {
			return err
		}
		if !exists || digestBytes(body) != file.TargetSHA256 || uint32(mode.Perm()) != file.TargetMode {
			return fmt.Errorf("ledger file %s does not match applied receipt", file.RelativePath)
		}
	}
	return nil
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
	sort.Strings(result)
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
