package apply

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

const (
	receiptSchemaVersion       = 2
	receiptPrepared            = "prepared"
	receiptApplied             = "applied"
	maxReceiptBytes            = 64 << 20
	maxReceiptFiles            = 4096
	maxReceiptScanFiles        = 4096
	maxReceiptScanBytes        = 4 * maxReceiptBytes
	maxReceiptPreflightEntries = 4096
)

// ErrPendingReceiptConflict means another proposal digest still owns the
// project transaction and must be recovered or resolved first.
var ErrPendingReceiptConflict = errors.New("pending apply receipt conflicts with current proposal")

type applyReceipt struct {
	SchemaVersion        int                     `json:"schema_version"`
	State                string                  `json:"state"`
	ReceiptSHA256        string                  `json:"receipt_sha256"`
	ProjectID            string                  `json:"project_id"`
	SessionID            string                  `json:"session_id"`
	FromCursor           int                     `json:"from_cursor"`
	ToCursor             int                     `json:"to_cursor"`
	ProposalSHA256       string                  `json:"proposal_sha256"`
	EvidenceFileSHA256   string                  `json:"evidence_file_sha256"`
	EvidencePacketSHA256 string                  `json:"evidence_packet_sha256"`
	LedgerSnapshotSHA256 string                  `json:"ledger_snapshot_sha256"`
	ExpectedCursor       evidence.CursorBoundary `json:"expected_cursor"`
	NextCursor           evidence.CursorBoundary `json:"next_cursor"`
	Files                []receiptFile           `json:"files"`
	ChangedFiles         []string                `json:"changed_files"`
}

type receiptFile struct {
	RelativePath   string `json:"relative_path"`
	PreimageExists bool   `json:"preimage_exists"`
	PreimageMode   uint32 `json:"preimage_mode"`
	PreimageSHA256 string `json:"preimage_sha256,omitempty"`
	TargetMode     uint32 `json:"target_mode"`
	TargetSHA256   string `json:"target_sha256"`
	TargetData     []byte `json:"target_data"`
}

type scannedReceipt struct {
	name string
	info os.FileInfo
}

func newPreparedReceipt(ctx inputContext, plan ledger.WritePlan, ledgerSnapshotSHA256 string) (applyReceipt, error) {
	if err := validatePreparedReceiptBudget(plan); err != nil {
		return applyReceipt{}, err
	}
	receipt := applyReceipt{
		SchemaVersion: receiptSchemaVersion, State: receiptPrepared,
		ProjectID: ctx.Packet.ProjectID, SessionID: ctx.Packet.SessionID,
		FromCursor: ctx.Packet.FromCursor, ToCursor: ctx.Packet.ToCursor,
		ProposalSHA256: ctx.ProposalDigest, EvidenceFileSHA256: ctx.EvidenceFileDigest,
		EvidencePacketSHA256: ctx.EvidencePacketDigest,
		LedgerSnapshotSHA256: ledgerSnapshotSHA256,
		ExpectedCursor:       ctx.Packet.ExpectedCursor, NextCursor: ctx.Packet.NextCursor,
		Files: make([]receiptFile, 0, len(plan.Files)), ChangedFiles: []string{},
	}
	for _, file := range plan.Files {
		entry := receiptFile{
			RelativePath: file.RelativePath, PreimageExists: file.ExpectedExists,
			TargetMode:   normalizeApplyMode(file.Perm),
			TargetSHA256: digestBytes(file.Data), TargetData: append([]byte(nil), file.Data...),
		}
		if file.ExpectedExists {
			entry.PreimageMode = normalizeApplyMode(file.ExpectedPerm)
			entry.PreimageSHA256 = digestBytes(file.ExpectedData)
		}
		receipt.Files = append(receipt.Files, entry)
	}
	sort.Slice(receipt.Files, func(i, j int) bool { return receipt.Files[i].RelativePath < receipt.Files[j].RelativePath })
	if err := receipt.validate(); err != nil {
		return applyReceipt{}, err
	}
	return receipt, nil
}

func validatePreparedReceiptBudget(plan ledger.WritePlan) error {
	if len(plan.Files) > maxReceiptFiles {
		return fmt.Errorf("apply receipt file count exceeds %d", maxReceiptFiles)
	}
	// Conservatively budget the indented JSON, two escaped path occurrences
	// (files and changed_files), hashes, modes, field names, commas/newlines,
	// and base64 expansion before copying any target bytes.
	budget := uint64(4096)
	limit := uint64(maxReceiptBytes)
	for _, file := range plan.Files {
		if len(file.Data) > ledger.MaxDocumentBytes {
			return fmt.Errorf("invalid target metadata for %s", file.RelativePath)
		}
		encoded := uint64(base64.StdEncoding.EncodedLen(len(file.Data)))
		pathBudget := uint64(len(file.RelativePath))*12 + 4
		if encoded > limit || pathBudget > limit || budget > limit-encoded || budget+encoded > limit-pathBudget || budget+encoded+pathBudget > limit-1024 {
			return fmt.Errorf("apply receipt encoded size exceeds %d bytes", maxReceiptBytes)
		}
		budget += encoded + pathBudget + 1024
	}
	return nil
}

func (receipt applyReceipt) matches(ctx inputContext) error {
	if err := receipt.validate(); err != nil {
		return err
	}
	if receipt.ProjectID != ctx.Packet.ProjectID || receipt.SessionID != ctx.Packet.SessionID ||
		receipt.FromCursor != ctx.Packet.FromCursor || receipt.ToCursor != ctx.Packet.ToCursor ||
		receipt.ProposalSHA256 != ctx.ProposalDigest || receipt.EvidenceFileSHA256 != ctx.EvidenceFileDigest ||
		receipt.EvidencePacketSHA256 != ctx.EvidencePacketDigest || receipt.ExpectedCursor != ctx.Packet.ExpectedCursor ||
		receipt.NextCursor != ctx.Packet.NextCursor {
		return errors.New("receipt does not match exact apply inputs")
	}
	return nil
}

func (receipt applyReceipt) validate() error {
	if receipt.SchemaVersion != receiptSchemaVersion || (receipt.State != receiptPrepared && receipt.State != receiptApplied) {
		return errors.New("invalid apply receipt schema or state")
	}
	if !safeIdentifier(receipt.ProjectID) || !safeIdentifier(receipt.SessionID) || !validReceiptDigest(receipt.ProposalSHA256) || !validReceiptDigest(receipt.EvidenceFileSHA256) || !validReceiptDigest(receipt.EvidencePacketSHA256) || !validReceiptDigest(receipt.LedgerSnapshotSHA256) {
		return errors.New("invalid apply receipt identity or input digest")
	}
	if receipt.FromCursor < 1 || receipt.ToCursor < receipt.FromCursor || receipt.ExpectedCursor.Line != receipt.FromCursor-1 || receipt.NextCursor.Line != receipt.ToCursor {
		return errors.New("invalid apply receipt boundaries")
	}
	if !validReceiptBoundary(receipt.ExpectedCursor) || !validReceiptBoundary(receipt.NextCursor) {
		return errors.New("invalid apply receipt boundary hash")
	}
	if len(receipt.Files) > maxReceiptFiles {
		return fmt.Errorf("apply receipt file count exceeds %d", maxReceiptFiles)
	}
	seen := make(map[string]struct{}, len(receipt.Files))
	caseSeen := make(map[string]string, len(receipt.Files))
	for _, file := range receipt.Files {
		fromSlash := filepath.FromSlash(file.RelativePath)
		if file.RelativePath == "" || file.RelativePath == ".." || strings.HasPrefix(file.RelativePath, "../") || filepath.IsAbs(file.RelativePath) || strings.Contains(file.RelativePath, "\\") || filepath.Clean(fromSlash) != fromSlash {
			return errors.New("invalid receipt file path")
		}
		if _, duplicate := seen[file.RelativePath]; duplicate {
			return fmt.Errorf("duplicate receipt file path %q", file.RelativePath)
		}
		seen[file.RelativePath] = struct{}{}
		folded := strings.ToLower(file.RelativePath)
		if prior, collision := caseSeen[folded]; collision && prior != file.RelativePath {
			return fmt.Errorf("case-colliding receipt paths %q and %q", prior, file.RelativePath)
		}
		caseSeen[folded] = file.RelativePath
		if !validApplyMode(file.TargetMode) || file.TargetSHA256 != digestBytes(file.TargetData) || len(file.TargetData) > ledger.MaxDocumentBytes {
			return fmt.Errorf("invalid target metadata for %s", file.RelativePath)
		}
		if file.PreimageExists {
			if !validApplyMode(file.PreimageMode) || !validReceiptDigest(file.PreimageSHA256) {
				return fmt.Errorf("invalid preimage metadata for %s", file.RelativePath)
			}
		} else if file.PreimageMode != 0 || file.PreimageSHA256 != "" {
			return fmt.Errorf("unexpected missing-preimage metadata for %s", file.RelativePath)
		}
	}
	changed := make(map[string]struct{}, len(receipt.ChangedFiles))
	for _, path := range receipt.ChangedFiles {
		if _, ok := seen[path]; !ok {
			return fmt.Errorf("changed file %q is absent from receipt plan", path)
		}
		if _, duplicate := changed[path]; duplicate {
			return fmt.Errorf("duplicate changed file %q", path)
		}
		changed[path] = struct{}{}
	}
	if receipt.State == receiptPrepared && len(receipt.ChangedFiles) != 0 {
		return errors.New("prepared apply receipt records changed files")
	}
	if receipt.State == receiptApplied && !equalReceiptPaths(receipt.ChangedFiles, receiptPlannedChanges(receipt)) {
		return errors.New("applied receipt changed files do not match its plan")
	}
	return nil
}

func validReceiptBoundary(boundary evidence.CursorBoundary) bool {
	if boundary.Line == 0 {
		return boundary.SourceHash == ""
	}
	return boundary.Line > 0 && validReceiptDigest("sha256:"+boundary.SourceHash)
}

func equalReceiptPaths(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func saveReceipt(projectData *os.Root, receipt applyReceipt, hookOptions ...applyHooks) error {
	if err := receipt.validate(); err != nil {
		return err
	}
	receipt.ReceiptSHA256 = ""
	unsigned, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receipt.ReceiptSHA256 = digestBytes(unsigned)
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > maxReceiptBytes {
		return fmt.Errorf("apply receipt exceeds %d bytes", maxReceiptBytes)
	}
	hooks := applyHooks{}
	if len(hookOptions) != 0 {
		hooks = hookOptions[0]
	}
	directory, err := openReceiptDirectory(projectData, true, hooks.ensureRootDir)
	if err != nil {
		return err
	}
	defer directory.Close()
	name, err := receiptFileName(receipt.ProposalSHA256)
	if err != nil {
		return err
	}
	if err := rejectEntryCaseCollisionsBounded(directory, maxReceiptPreflightEntries, name, atomicfile.BackupPath(name)); err != nil {
		return err
	}
	writeReceipt := hooks.writeReceipt
	if writeReceipt == nil {
		writeReceipt = atomicfile.WriteRoot
	}
	if err := writeReceipt(directory, name, body, 0o600); err != nil {
		return fmt.Errorf("persist apply receipt: %w", err)
	}
	if _, err := protectReceiptFile(directory, name); err != nil {
		return err
	}
	if err := hooks.publicationSync()(directory, name); err != nil {
		return fmt.Errorf("sync apply receipt publication: %w", err)
	}
	return nil
}

func loadReceiptRoot(directory *os.Root, name, proposalDigest string, remaining *uint64, syncPublication func(*os.Root, string) error) (applyReceipt, error) {
	return loadReceiptRootWithMode(directory, name, proposalDigest, remaining, syncPublication, true)
}

func loadReceiptRootWithMode(directory *os.Root, name, proposalDigest string, remaining *uint64, syncPublication func(*os.Root, string) error, protect bool) (applyReceipt, error) {
	file, info, err := openReceiptRegularWithMode(directory, name, protect)
	if err != nil {
		return applyReceipt{}, fmt.Errorf("open apply receipt: %w", err)
	}
	defer file.Close()
	if info.Size() > maxReceiptBytes {
		return applyReceipt{}, errors.New("apply receipt exceeds size limit")
	}
	if info.Size() < 0 || uint64(info.Size()) > *remaining {
		return applyReceipt{}, fmt.Errorf("apply receipt aggregate size exceeds %d bytes", maxReceiptScanBytes)
	}
	if syncPublication != nil {
		if err := syncPublication(directory, name); err != nil {
			return applyReceipt{}, fmt.Errorf("sync apply receipt publication: %w", err)
		}
	}
	body, err := io.ReadAll(io.LimitReader(file, int64(*remaining)))
	if err != nil || uint64(len(body)) > *remaining {
		return applyReceipt{}, errors.New("read bounded apply receipt")
	}
	*remaining -= uint64(len(body))
	after, err := directory.Lstat(name)
	openedAfter, openedErr := file.Stat()
	if err != nil || openedErr != nil || !os.SameFile(info, after) || !os.SameFile(info, openedAfter) ||
		after.Size() != info.Size() || openedAfter.Size() != info.Size() || int64(len(body)) != info.Size() ||
		!after.ModTime().Equal(info.ModTime()) || !openedAfter.ModTime().Equal(info.ModTime()) {
		return applyReceipt{}, errors.New("apply receipt changed while reading")
	}
	if err := inspectJSONObject(body); err != nil {
		return applyReceipt{}, fmt.Errorf("invalid apply receipt JSON: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var receipt applyReceipt
	if err := dec.Decode(&receipt); err != nil {
		return applyReceipt{}, fmt.Errorf("decode apply receipt: %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return applyReceipt{}, err
	}
	wantDigest := receipt.ReceiptSHA256
	receipt.ReceiptSHA256 = ""
	unsigned, err := json.Marshal(receipt)
	if err != nil || wantDigest != digestBytes(unsigned) {
		return applyReceipt{}, errors.New("apply receipt integrity check failed")
	}
	receipt.ReceiptSHA256 = wantDigest
	if err := receipt.validate(); err != nil {
		return applyReceipt{}, err
	}
	if receipt.ProposalSHA256 != proposalDigest {
		return applyReceipt{}, errors.New("apply receipt filename digest mismatch")
	}
	return receipt, nil
}

func scanReceipts(projectData *os.Root, ctx inputContext, hooks applyHooks) (applyReceipt, bool, []applyReceipt, error) {
	directory, err := openReceiptDirectory(projectData, false, hooks.ensureRootDir)
	if errors.Is(err, os.ErrNotExist) {
		return applyReceipt{}, false, nil, nil
	}
	if err != nil {
		return applyReceipt{}, false, nil, err
	}
	defer directory.Close()
	directoryInfo, err := directory.Stat(".")
	if err != nil {
		return applyReceipt{}, false, nil, fmt.Errorf("inspect apply receipt directory: %w", err)
	}
	file, err := directory.Open(".")
	if err != nil {
		return applyReceipt{}, false, nil, err
	}
	entries, readErr := file.ReadDir(maxReceiptScanFiles + 1)
	closeErr := file.Close()
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return applyReceipt{}, false, nil, fmt.Errorf("enumerate apply receipts: %w", err)
	}
	if len(entries) > maxReceiptScanFiles {
		return applyReceipt{}, false, nil, fmt.Errorf("apply receipt count exceeds %d", maxReceiptScanFiles)
	}

	receipts := make([]scannedReceipt, 0, len(entries))
	folded := make(map[string]string, len(entries))
	currentName, err := receiptFileName(ctx.ProposalDigest)
	if err != nil {
		return applyReceipt{}, false, nil, err
	}
	limit := hooks.receiptScanByteLimit
	if limit == 0 {
		limit = maxReceiptScanBytes
	}
	var aggregate uint64
	removeReceipt := hooks.removeReceipt
	if removeReceipt == nil {
		removeReceipt = atomicfile.RemoveRoot
	}
	for _, entry := range entries {
		name := entry.Name()
		if exactReceiptTemporaryName(name) {
			if err := removeReceipt(directory, name); err != nil {
				return applyReceipt{}, false, nil, fmt.Errorf("remove orphan apply receipt temporary file: %w", err)
			}
			continue
		}
		if strings.EqualFold(name, currentName) && name != currentName {
			return applyReceipt{}, false, nil, fmt.Errorf("case-colliding apply receipt name %q", name)
		}
		fold := strings.ToLower(name)
		if prior, exists := folded[fold]; exists && prior != name {
			return applyReceipt{}, false, nil, fmt.Errorf("case-colliding apply receipt names %q and %q", prior, name)
		}
		folded[fold] = name
		if _, err := receiptDigestFromName(name); err != nil {
			return applyReceipt{}, false, nil, err
		}
		info, err := directory.Lstat(name)
		if err != nil {
			return applyReceipt{}, false, nil, fmt.Errorf("inspect apply receipt: %w", err)
		}
		if !info.Mode().IsRegular() || isApplyRedirect(info) {
			return applyReceipt{}, false, nil, fmt.Errorf("apply receipt %q is redirected or not regular", name)
		}
		if info.Size() < 0 || info.Size() > maxReceiptBytes {
			return applyReceipt{}, false, nil, fmt.Errorf("apply receipt %q exceeds size limit", name)
		}
		size := uint64(info.Size())
		if aggregate > ^uint64(0)-size {
			return applyReceipt{}, false, nil, errors.New("apply receipt aggregate size overflow")
		}
		aggregate += size
		receipts = append(receipts, scannedReceipt{name: name, info: info})
	}
	if aggregate > limit {
		return applyReceipt{}, false, nil, fmt.Errorf("apply receipt aggregate size exceeds %d bytes", limit)
	}
	syncDirectory := hooks.syncReceiptDirectory
	if syncDirectory == nil {
		syncDirectory = atomicfile.SyncRootDirectory
	}
	if err := syncDirectory(directory); err != nil {
		return applyReceipt{}, false, nil, fmt.Errorf("sync apply receipt directory: %w", err)
	}
	if hooks.afterReceiptEnumeration != nil {
		if err := hooks.afterReceiptEnumeration(); err != nil {
			return applyReceipt{}, false, nil, err
		}
	}

	sort.Slice(receipts, func(i, j int) bool { return receipts[i].name < receipts[j].name })
	var current applyReceipt
	var foundCurrent bool
	others := make([]applyReceipt, 0, len(receipts))
	remaining := limit
	for _, scanned := range receipts {
		name := scanned.name
		digest, _ := receiptDigestFromName(name)
		receipt, err := loadReceiptRoot(directory, name, digest, &remaining, hooks.publicationSync())
		if err != nil {
			return applyReceipt{}, false, nil, fmt.Errorf("scan apply receipt %q: %w", name, err)
		}
		if currentInfo, err := directory.Lstat(name); err != nil || !os.SameFile(scanned.info, currentInfo) {
			return applyReceipt{}, false, nil, fmt.Errorf("apply receipt %q changed during scan", name)
		}
		if receipt.ProjectID != ctx.Packet.ProjectID {
			return applyReceipt{}, false, nil, fmt.Errorf("apply receipt %q belongs to a different project", name)
		}
		if digest == ctx.ProposalDigest {
			current = receipt
			foundCurrent = true
			continue
		}
		others = append(others, receipt)
	}
	if err := verifyReceiptDirectorySnapshot(directory, receipts); err != nil {
		return applyReceipt{}, false, nil, err
	}
	if err := validateReceiptDirectoryIdentity(projectData, directory, directoryInfo); err != nil {
		return applyReceipt{}, false, nil, err
	}
	return current, foundCurrent, others, nil
}

func verifyReceiptDirectorySnapshot(directory *os.Root, expected []scannedReceipt) error {
	file, err := directory.Open(".")
	if err != nil {
		return fmt.Errorf("re-enumerate apply receipts: %w", err)
	}
	entries, readErr := file.ReadDir(maxReceiptScanFiles + 1)
	closeErr := file.Close()
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return fmt.Errorf("re-enumerate apply receipts: %w", err)
	}
	if len(entries) > maxReceiptScanFiles || len(entries) != len(expected) {
		return errors.New("apply receipt directory entries changed during scan")
	}
	want := make(map[string]os.FileInfo, len(expected))
	for _, receipt := range expected {
		want[receipt.name] = receipt.info
	}
	for _, entry := range entries {
		before, found := want[entry.Name()]
		if !found {
			return errors.New("apply receipt directory entries changed during scan")
		}
		after, err := directory.Lstat(entry.Name())
		if err != nil || !os.SameFile(before, after) {
			return errors.New("apply receipt directory entries changed during scan")
		}
	}
	return nil
}

func validateReceiptDirectoryIdentity(projectData, directory *os.Root, expected os.FileInfo) error {
	pinned, pinnedErr := directory.Stat(".")
	named, namedErr := projectData.Lstat("applied-proposals")
	if pinnedErr != nil || namedErr != nil || !os.SameFile(expected, pinned) || !os.SameFile(expected, named) {
		return errors.New("apply receipt directory identity changed during scan")
	}
	return nil
}

func exactReceiptTemporaryName(name string) bool {
	const prefix = ".session-reviewer-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
		return false
	}
	for _, r := range name[len(prefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// loadExactReceiptReadOnly checks only the exact proposal receipt below the
// already pinned DataDir. It never creates, syncs, chmods, cleans temporary
// files, or repairs state, so it is safe before the apply kernel lock.
func loadExactReceiptReadOnly(dataRoot *os.Root, ctx inputContext) (applyReceipt, bool, error) {
	projects, found, err := openReceiptChildReadOnly(dataRoot, "projects", "projects directory")
	if err != nil || !found {
		return applyReceipt{}, false, err
	}
	defer projects.Close()
	projectData, found, err := openReceiptChildReadOnly(projects, ctx.Packet.ProjectID, "project data directory")
	if err != nil || !found {
		return applyReceipt{}, false, err
	}
	defer projectData.Close()
	directory, found, err := openReceiptChildReadOnly(projectData, "applied-proposals", "apply receipt directory")
	if err != nil || !found {
		return applyReceipt{}, false, err
	}
	defer directory.Close()
	name, err := receiptFileName(ctx.ProposalDigest)
	if err != nil {
		return applyReceipt{}, false, err
	}
	if err := rejectEntryCaseCollisionsBounded(directory, maxReceiptPreflightEntries, name, atomicfile.BackupPath(name)); err != nil {
		return applyReceipt{}, false, err
	}
	if _, err := directory.Lstat(name); errors.Is(err, os.ErrNotExist) {
		return applyReceipt{}, false, nil
	} else if err != nil {
		return applyReceipt{}, false, err
	}
	remaining := uint64(maxReceiptBytes)
	receipt, err := loadReceiptRootWithMode(directory, name, ctx.ProposalDigest, &remaining, nil, false)
	if err != nil {
		return applyReceipt{}, true, err
	}
	if err := receipt.matches(ctx); err != nil {
		return applyReceipt{}, true, err
	}
	return receipt, true, nil
}

func openReceiptChildReadOnly(parent *os.Root, name, label string) (*os.Root, bool, error) {
	if parent == nil {
		return nil, false, errors.New("receipt parent root is required")
	}
	if err := rejectEntryCaseCollisionsBounded(parent, maxReceiptPreflightEntries, name); err != nil {
		return nil, false, err
	}
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() || isApplyRedirect(info) {
		return nil, true, fmt.Errorf("%s is redirected or not a directory", label)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, true, fmt.Errorf("open %s: %w", label, err)
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = child.Close()
		return nil, true, fmt.Errorf("%s changed while opening", label)
	}
	return child, true, nil
}

func openReceiptDirectory(projectData *os.Root, create bool, ensure func(*os.Root, string, fs.FileMode) error) (*os.Root, error) {
	if projectData == nil {
		return nil, errors.New("project data root is required")
	}
	if err := rejectDirectoryCaseCollision(projectData, "applied-proposals"); err != nil {
		return nil, err
	}
	info, err := projectData.Lstat("applied-proposals")
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil, os.ErrNotExist
	}
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return nil, errors.New("apply receipt directory is redirected or not a directory")
	}
	if !missing && (info == nil || !info.IsDir() || isApplyRedirect(info)) {
		return nil, errors.New("apply receipt directory is redirected or not a directory")
	}
	before := info
	if ensure == nil {
		ensure = atomicfile.EnsureRootDir
	}
	if err := ensure(projectData, "applied-proposals", 0o700); err != nil {
		return nil, err
	}
	info, err = projectData.Lstat("applied-proposals")
	if err != nil || info == nil || !info.IsDir() || isApplyRedirect(info) {
		return nil, errors.New("apply receipt directory is redirected or not a directory")
	}
	if before != nil && !os.SameFile(before, info) {
		return nil, errors.New("apply receipt directory changed while ensuring durability")
	}
	directory, err := projectData.OpenRoot("applied-proposals")
	if err != nil {
		return nil, err
	}
	opened, err := directory.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = directory.Close()
		return nil, errors.New("apply receipt directory identity changed while opening")
	}
	directoryFile, err := directory.Open(".")
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	var chmodErr error
	if !receiptPrivacyModeEqual(info.Mode(), 0o700) {
		chmodErr = directoryFile.Chmod(0o700)
	}
	protected, statErr := directoryFile.Stat()
	closeErr := directoryFile.Close()
	after, pathErr := projectData.Lstat("applied-proposals")
	if err := errors.Join(chmodErr, statErr, closeErr, pathErr); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("protect apply receipt directory: %w", err)
	}
	if !os.SameFile(info, protected) || !os.SameFile(protected, after) || !receiptPrivacyModeEqual(protected.Mode(), 0o700) {
		_ = directory.Close()
		return nil, errors.New("apply receipt directory identity or privacy mode changed")
	}
	return directory, nil
}

func openReceiptRegular(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	return openReceiptRegularWithMode(root, name, true)
}

func openReceiptRegularWithMode(root *os.Root, name string, protect bool) (*os.File, os.FileInfo, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || isApplyRedirect(before) {
		return nil, nil, errors.New("apply receipt is redirected or not regular")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, nil, errors.New("apply receipt identity changed while opening")
	}
	if protect && !receiptPrivacyModeEqual(opened.Mode(), 0o600) {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, nil, fmt.Errorf("protect apply receipt: %w", err)
		}
	}
	protected, err := file.Stat()
	after, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil || !os.SameFile(opened, protected) || !os.SameFile(protected, after) || (protect && !receiptPrivacyModeEqual(protected.Mode(), 0o600)) {
		_ = file.Close()
		return nil, nil, errors.New("apply receipt identity or privacy mode changed")
	}
	return file, protected, nil
}

func protectReceiptFile(root *os.Root, name string) (os.FileInfo, error) {
	file, info, err := openReceiptRegular(root, name)
	if file != nil {
		err = errors.Join(err, file.Close())
	}
	return info, err
}

func receiptFileName(digest string) (string, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) || len(digest) != len(prefix)+64 {
		return "", errors.New("invalid proposal digest")
	}
	hex := digest[len(prefix):]
	for _, r := range hex {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", errors.New("invalid proposal digest")
		}
	}
	return hex + ".json", nil
}

func validReceiptDigest(digest string) bool {
	_, err := receiptFileName(digest)
	return err == nil
}

func receiptDigestFromName(name string) (string, error) {
	if len(name) != 64+len(".json") || !strings.HasSuffix(name, ".json") {
		return "", fmt.Errorf("invalid apply receipt name %q", name)
	}
	hexDigest := strings.TrimSuffix(name, ".json")
	for _, r := range hexDigest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", fmt.Errorf("invalid apply receipt name %q", name)
		}
	}
	return "sha256:" + hexDigest, nil
}

func rejectReceiptCaseCollisions(root *os.Root, name string) error {
	return rejectEntryCaseCollisionsBounded(root, maxReceiptPreflightEntries, name, atomicfile.BackupPath(name))
}

func rejectDirectoryCaseCollision(root *os.Root, name string) error {
	return rejectEntryCaseCollisionsBounded(root, maxReceiptPreflightEntries, name)
}

func rejectEntryCaseCollisionsBounded(root *os.Root, limit int, names ...string) error {
	if root == nil || limit <= 0 {
		return errors.New("bounded apply state root and positive entry limit are required")
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := file.ReadDir(limit + 1)
	closeErr := file.Close()
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if len(entries) > limit {
		return fmt.Errorf("apply state entry count exceeds %d", limit)
	}
	for _, entry := range entries {
		for _, name := range names {
			if strings.EqualFold(entry.Name(), name) && entry.Name() != name {
				return fmt.Errorf("case-colliding apply state entry %q", entry.Name())
			}
		}
	}
	return nil
}

func inspectJSONObject(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := inspectJSONValue(dec); err != nil {
		return err
	}
	return requireJSONEOF(dec)
}

func inspectJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("explicit null is not permitted")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			nameToken, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("object member name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate object member %q", name)
			}
			seen[name] = struct{}{}
			if err := inspectJSONValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := inspectJSONValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}
