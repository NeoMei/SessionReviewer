package apply

import (
	"bytes"
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
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const (
	receiptSchemaVersion = 1
	receiptPrepared      = "prepared"
	receiptApplied       = "applied"
	maxReceiptBytes      = 64 << 20
)

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

func newPreparedReceipt(ctx inputContext, plan ledger.WritePlan) (applyReceipt, error) {
	receipt := applyReceipt{
		SchemaVersion: receiptSchemaVersion, State: receiptPrepared,
		ProjectID: ctx.Packet.ProjectID, SessionID: ctx.Packet.SessionID,
		FromCursor: ctx.Packet.FromCursor, ToCursor: ctx.Packet.ToCursor,
		ProposalSHA256: ctx.ProposalDigest, EvidenceFileSHA256: ctx.EvidenceFileDigest,
		EvidencePacketSHA256: ctx.EvidencePacketDigest,
		ExpectedCursor:       ctx.Packet.ExpectedCursor, NextCursor: ctx.Packet.NextCursor,
		Files: make([]receiptFile, 0, len(plan.Files)), ChangedFiles: []string{},
	}
	for _, file := range plan.Files {
		entry := receiptFile{
			RelativePath: file.RelativePath, PreimageExists: file.ExpectedExists,
			PreimageMode: uint32(file.ExpectedPerm.Perm()), TargetMode: uint32(file.Perm.Perm()),
			TargetSHA256: digestBytes(file.Data), TargetData: append([]byte(nil), file.Data...),
		}
		if file.ExpectedExists {
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
	if !safeIdentifier(receipt.ProjectID) || !safeIdentifier(receipt.SessionID) || receipt.ProposalSHA256 == "" || receipt.EvidenceFileSHA256 == "" || receipt.EvidencePacketSHA256 == "" {
		return errors.New("invalid apply receipt identity")
	}
	if receipt.FromCursor < 1 || receipt.ToCursor < receipt.FromCursor || receipt.ExpectedCursor.Line != receipt.FromCursor-1 || receipt.NextCursor.Line != receipt.ToCursor {
		return errors.New("invalid apply receipt boundaries")
	}
	seen := make(map[string]struct{}, len(receipt.Files))
	caseSeen := make(map[string]string, len(receipt.Files))
	for _, file := range receipt.Files {
		if file.RelativePath == "" || filepath.IsAbs(file.RelativePath) || strings.Contains(file.RelativePath, "\\") || filepath.Clean(filepath.FromSlash(file.RelativePath)) != filepath.FromSlash(file.RelativePath) {
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
		if file.TargetMode == 0 || fs.FileMode(file.TargetMode).Perm() != fs.FileMode(file.TargetMode) || file.TargetSHA256 != digestBytes(file.TargetData) || len(file.TargetData) > ledger.MaxDocumentBytes {
			return fmt.Errorf("invalid target metadata for %s", file.RelativePath)
		}
		if file.PreimageExists {
			if file.PreimageMode == 0 || fs.FileMode(file.PreimageMode).Perm() != fs.FileMode(file.PreimageMode) || file.PreimageSHA256 == "" {
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
	return nil
}

func saveReceipt(projectDataPath string, receipt applyReceipt) error {
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
	directory, err := openReceiptDirectory(projectDataPath, true)
	if err != nil {
		return err
	}
	defer directory.Close()
	name, err := receiptFileName(receipt.ProposalSHA256)
	if err != nil {
		return err
	}
	if err := rejectReceiptCaseCollisions(directory.Root, name); err != nil {
		return err
	}
	if err := atomicfile.WriteRoot(directory.Root, name, body, 0o600); err != nil {
		return fmt.Errorf("persist apply receipt: %w", err)
	}
	return nil
}

func loadReceipt(projectDataPath, proposalDigest string) (applyReceipt, bool, error) {
	directory, err := openReceiptDirectory(projectDataPath, false)
	if errors.Is(err, os.ErrNotExist) {
		return applyReceipt{}, false, nil
	}
	if err != nil {
		return applyReceipt{}, false, err
	}
	defer directory.Close()
	name, err := receiptFileName(proposalDigest)
	if err != nil {
		return applyReceipt{}, false, err
	}
	if err := rejectReceiptCaseCollisions(directory.Root, name); err != nil {
		return applyReceipt{}, false, err
	}
	file, info, err := directory.OpenRegular(name)
	if errors.Is(err, os.ErrNotExist) {
		return applyReceipt{}, false, nil
	}
	if err != nil {
		return applyReceipt{}, false, fmt.Errorf("open apply receipt: %w", err)
	}
	defer file.Close()
	if info.Size() > maxReceiptBytes {
		return applyReceipt{}, true, errors.New("apply receipt exceeds size limit")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxReceiptBytes+1))
	if err != nil || len(body) > maxReceiptBytes {
		return applyReceipt{}, true, errors.New("read bounded apply receipt")
	}
	after, err := directory.Root.Lstat(name)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return applyReceipt{}, true, errors.New("apply receipt changed while reading")
	}
	if err := inspectJSONObject(body); err != nil {
		return applyReceipt{}, true, fmt.Errorf("invalid apply receipt JSON: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var receipt applyReceipt
	if err := dec.Decode(&receipt); err != nil {
		return applyReceipt{}, true, fmt.Errorf("decode apply receipt: %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return applyReceipt{}, true, err
	}
	wantDigest := receipt.ReceiptSHA256
	receipt.ReceiptSHA256 = ""
	unsigned, err := json.Marshal(receipt)
	if err != nil || wantDigest != digestBytes(unsigned) {
		return applyReceipt{}, true, errors.New("apply receipt integrity check failed")
	}
	receipt.ReceiptSHA256 = wantDigest
	if err := receipt.validate(); err != nil {
		return applyReceipt{}, true, err
	}
	if receipt.ProposalSHA256 != proposalDigest {
		return applyReceipt{}, true, errors.New("apply receipt filename digest mismatch")
	}
	return receipt, true, nil
}

func openReceiptDirectory(projectDataPath string, create bool) (*pathguard.Directory, error) {
	project, err := pathguard.Open(projectDataPath)
	if err != nil {
		return nil, err
	}
	defer project.Close()
	if err := rejectDirectoryCaseCollision(project.Root, "applied-proposals"); err != nil {
		return nil, err
	}
	info, err := project.Root.Lstat("applied-proposals")
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil, os.ErrNotExist
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := project.Root.Mkdir("applied-proposals", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err = project.Root.Lstat("applied-proposals")
	}
	if err != nil || info == nil || !info.IsDir() || isApplyRedirect(info) {
		return nil, errors.New("apply receipt directory is redirected or not a directory")
	}
	return pathguard.Open(filepath.Join(projectDataPath, "applied-proposals"))
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

func rejectReceiptCaseCollisions(root *os.Root, name string) error {
	return rejectEntryCaseCollisions(root, name, atomicfile.BackupPath(name))
}

func rejectDirectoryCaseCollision(root *os.Root, name string) error {
	return rejectEntryCaseCollisions(root, name)
}

func rejectEntryCaseCollisions(root *os.Root, names ...string) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return err
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
