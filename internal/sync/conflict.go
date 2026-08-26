package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

const MaxConflictRecordBytes = 16 << 20

type ConflictKind string

const (
	ConflictUnits       ConflictKind = "units"
	ConflictArchiveEdit ConflictKind = "archive_vs_modify"
	ConflictReserved    ConflictKind = "reserved_field"
	ConflictMalformed   ConflictKind = "malformed"
	ConflictCollision   ConflictKind = "path_collision"
)

type ConflictRecord struct {
	Version                                        int
	ID, EntityID, ProjectID                        string
	Kind                                           ConflictKind
	RelativePath, BasePath, ProjectPath, VaultPath string
	BaseHash, ProjectHash, VaultHash               string
	Base, Project, Vault, Suggested                []byte
	CreatedAt                                      time.Time
	ResolutionStatus                               ResolutionStatus
	ResolutionAction                               ResolutionAction
	ResolvedHash                                   string
	ResolvedAt                                     time.Time
}

type ConflictNote struct {
	RelativePath string
	Content      []byte
}

type MirroredNotes struct {
	Project ConflictNote
	Vault   ConflictNote
}

type RepairRecord struct {
	Version    int
	ID         string
	ProjectID  string
	EntityID   string
	Side       Side
	IssueCode  syncdoc.IssueKind
	SourceHash string
	CreatedAt  time.Time
}

type RepairInput struct {
	CreatedAt  time.Time
	ProjectID  string
	EntityID   string
	Side       Side
	IssueCode  syncdoc.IssueKind
	SourcePath string
	Source     []byte
}

type ConflictArtifact struct {
	Record *ConflictRecord
	Repair *RepairRecord
	Notes  MirroredNotes
}

type ResolutionAction string

const (
	AcceptProject  ResolutionAction = "accept_project"
	AcceptObsidian ResolutionAction = "accept_obsidian"
	ManualMerge    ResolutionAction = "manual_merge"
)

type Resolution struct {
	ConflictID string
	Action     ResolutionAction
	ManualFile string
}

type ResolutionStatus string

const (
	ResolutionOpen     ResolutionStatus = "open"
	ResolutionResolved ResolutionStatus = "resolved"
)

var (
	ErrInvalidConflict   = errors.New("invalid conflict record")
	ErrInvalidRepair     = errors.New("invalid repair issue")
	ErrInvalidResolution = errors.New("invalid conflict resolution")
	ErrStaleConflict     = errors.New("stale conflict")
	ErrConflictResolved  = errors.New("conflict already resolved")
	ErrSensitiveContent  = errors.New("candidate contains suspected sensitive content")
)

func BuildConflict(input ConflictRecord) (ConflictArtifact, error) {
	record := cloneConflictRecord(input)
	if record.Version != 1 || !stableBaseID.MatchString(record.EntityID) || !stableBaseID.MatchString(record.ProjectID) || !validConflictKind(record.Kind) || record.CreatedAt.IsZero() {
		return ConflictArtifact{}, ErrInvalidConflict
	}
	if err := validateMergePath(record.RelativePath); err != nil {
		return ConflictArtifact{}, ErrInvalidConflict
	}
	for _, relative := range []string{record.BasePath, record.ProjectPath, record.VaultPath} {
		if relative != "" {
			if err := validateMergePath(relative); err != nil {
				return ConflictArtifact{}, ErrInvalidConflict
			}
		}
	}
	record.CreatedAt = record.CreatedAt.UTC()
	if (record.ResolutionStatus != "" && record.ResolutionStatus != ResolutionOpen) || record.ResolutionAction != "" || record.ResolvedHash != "" || !record.ResolvedAt.IsZero() {
		return ConflictArtifact{}, ErrInvalidConflict
	}
	record.ResolutionStatus = ResolutionOpen
	record.ResolutionAction = ""
	record.ResolvedHash = ""
	record.ResolvedAt = time.Time{}
	if side, source, sensitive := sensitiveConflictCandidate(record); sensitive {
		return BuildRepair(RepairInput{
			CreatedAt: record.CreatedAt, ProjectID: record.ProjectID, EntityID: record.EntityID,
			Side: side, IssueCode: syncdoc.IssueSensitive, SourcePath: record.RelativePath, Source: source,
		})
	}
	if issueCode, side, source, relative, isolated := isolatedConflictSource(record); isolated {
		return BuildRepair(RepairInput{
			CreatedAt: record.CreatedAt, ProjectID: record.ProjectID, EntityID: record.EntityID,
			Side: side, IssueCode: issueCode, SourcePath: relative, Source: source,
		})
	}
	baseHash := syncdoc.ContentHash(record.Base)
	projectHash := syncdoc.ContentHash(record.Project)
	vaultHash := syncdoc.ContentHash(record.Vault)
	if (record.BaseHash != "" && record.BaseHash != baseHash) || (record.ProjectHash != "" && record.ProjectHash != projectHash) || (record.VaultHash != "" && record.VaultHash != vaultHash) {
		return ConflictArtifact{}, ErrInvalidConflict
	}
	record.BaseHash = baseHash
	record.ProjectHash = projectHash
	record.VaultHash = vaultHash
	digest := sha256.Sum256([]byte(record.BaseHash + "|" + record.ProjectHash + "|" + record.VaultHash))
	wantID := fmt.Sprintf("conflict-%s-%x", record.EntityID, digest[:6])
	if record.ID != "" && record.ID != wantID {
		return ConflictArtifact{}, ErrInvalidConflict
	}
	record.ID = wantID

	note, err := RenderConflict(record)
	if err != nil {
		return ConflictArtifact{}, err
	}
	relative := ".session-reviewer/conflicts/" + record.ID + ".json"
	return ConflictArtifact{
		Record: &record,
		Notes: MirroredNotes{
			Project: ConflictNote{RelativePath: relative, Content: bytes.Clone(note)},
			Vault:   ConflictNote{RelativePath: relative, Content: bytes.Clone(note)},
		},
	}, nil
}

func BuildRepair(input RepairInput) (ConflictArtifact, error) {
	if input.CreatedAt.IsZero() || !stableBaseID.MatchString(input.ProjectID) || !validRepairSide(input.Side) || !validRepairIssue(input.IssueCode) {
		return ConflictArtifact{}, ErrInvalidRepair
	}
	entityID := ""
	suffix := ""
	if input.EntityID != "" {
		if !stableBaseID.MatchString(input.EntityID) {
			return ConflictArtifact{}, ErrInvalidRepair
		}
		entityID = input.EntityID
		suffix = entityID
	} else {
		if input.SourcePath == "" {
			return ConflictArtifact{}, ErrInvalidRepair
		}
		digest := sha256.Sum256([]byte(input.SourcePath))
		suffix = fmt.Sprintf("%x", digest[:6])
	}
	createdAt := input.CreatedAt.UTC()
	record := RepairRecord{
		Version: 1, ID: "repair-" + createdAt.Format("20060102t150405z") + "-" + suffix,
		ProjectID: input.ProjectID, EntityID: entityID, Side: input.Side, IssueCode: input.IssueCode,
		SourceHash: syncdoc.ContentHash(input.Source), CreatedAt: createdAt,
	}
	note := renderRepair(record)
	relative := ".session-reviewer/conflicts/" + record.ID + ".json"
	return ConflictArtifact{
		Repair: &record,
		Notes: MirroredNotes{
			Project: ConflictNote{RelativePath: relative, Content: bytes.Clone(note)},
			Vault:   ConflictNote{RelativePath: relative, Content: bytes.Clone(note)},
		},
	}, nil
}

func RenderConflict(input ConflictRecord) ([]byte, error) {
	record := cloneConflictRecord(input)
	if _, _, sensitive := sensitiveConflictCandidate(record); sensitive {
		return nil, ErrSensitiveContent
	}
	if err := validateConflictRecord(record); err != nil {
		return nil, err
	}
	if len(record.Base)+len(record.Project)+len(record.Vault)+len(record.Suggested) > MaxConflictRecordBytes {
		return nil, ErrInvalidConflict
	}
	wire := conflictRecordWire{
		Version: record.Version, ID: record.ID, EntityID: record.EntityID, ProjectID: record.ProjectID,
		Kind: record.Kind, RelativePath: record.RelativePath, BasePath: record.BasePath,
		ProjectPath: record.ProjectPath, VaultPath: record.VaultPath,
		BaseHash: record.BaseHash, ProjectHash: record.ProjectHash, VaultHash: record.VaultHash,
		Base: string(record.Base), Project: string(record.Project), Vault: string(record.Vault), Suggested: string(record.Suggested),
		CreatedAt: record.CreatedAt.UTC().Format(time.RFC3339Nano), ResolutionStatus: record.ResolutionStatus,
		ResolutionAction: record.ResolutionAction, ResolvedHash: record.ResolvedHash,
	}
	if !record.ResolvedAt.IsZero() {
		wire.ResolvedAt = record.ResolvedAt.UTC().Format(time.RFC3339Nano)
	}
	body, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return nil, ErrInvalidConflict
	}
	body = append(body, '\n')
	if len(body) > MaxConflictRecordBytes {
		return nil, ErrInvalidConflict
	}
	return body, nil
}

type conflictRecordWire struct {
	Version          int              `json:"version"`
	ID               string           `json:"id"`
	EntityID         string           `json:"entity_id"`
	ProjectID        string           `json:"project_id"`
	Kind             ConflictKind     `json:"kind"`
	RelativePath     string           `json:"relative_path"`
	BasePath         string           `json:"base_path,omitempty"`
	ProjectPath      string           `json:"project_path,omitempty"`
	VaultPath        string           `json:"vault_path,omitempty"`
	BaseHash         string           `json:"base_hash"`
	ProjectHash      string           `json:"project_hash"`
	VaultHash        string           `json:"vault_hash"`
	Base             string           `json:"base"`
	Project          string           `json:"project"`
	Vault            string           `json:"vault"`
	Suggested        string           `json:"suggested"`
	CreatedAt        string           `json:"created_at"`
	ResolutionStatus ResolutionStatus `json:"resolution_status"`
	ResolutionAction ResolutionAction `json:"resolution_action,omitempty"`
	ResolvedHash     string           `json:"resolved_hash,omitempty"`
	ResolvedAt       string           `json:"resolved_at,omitempty"`
}

func ParseConflictRecord(body []byte) (ConflictRecord, error) {
	if len(body) == 0 || len(body) > MaxConflictRecordBytes {
		return ConflictRecord{}, ErrInvalidConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var wire conflictRecordWire
	if err := decoder.Decode(&wire); err != nil {
		return ConflictRecord{}, ErrInvalidConflict
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ConflictRecord{}, ErrInvalidConflict
	}
	createdAt, err := time.Parse(time.RFC3339Nano, wire.CreatedAt)
	if err != nil {
		return ConflictRecord{}, ErrInvalidConflict
	}
	resolvedAt := time.Time{}
	if wire.ResolvedAt != "" {
		resolvedAt, err = time.Parse(time.RFC3339Nano, wire.ResolvedAt)
		if err != nil {
			return ConflictRecord{}, ErrInvalidConflict
		}
	}
	record := ConflictRecord{
		Version: wire.Version, ID: wire.ID, EntityID: wire.EntityID, ProjectID: wire.ProjectID,
		Kind: wire.Kind, RelativePath: wire.RelativePath, BasePath: wire.BasePath, ProjectPath: wire.ProjectPath, VaultPath: wire.VaultPath,
		BaseHash: wire.BaseHash, ProjectHash: wire.ProjectHash, VaultHash: wire.VaultHash,
		Base: []byte(wire.Base), Project: []byte(wire.Project), Vault: []byte(wire.Vault), Suggested: []byte(wire.Suggested),
		CreatedAt: createdAt.UTC(), ResolutionStatus: wire.ResolutionStatus, ResolutionAction: wire.ResolutionAction,
		ResolvedHash: wire.ResolvedHash, ResolvedAt: resolvedAt.UTC(),
	}
	if err := validateConflictRecord(record); err != nil {
		return ConflictRecord{}, err
	}
	if _, err := RenderConflict(record); err != nil {
		return ConflictRecord{}, err
	}
	return record, nil
}

func (engine *Engine) persistConflictRecord(ctx context.Context, artifact ConflictArtifact) error {
	if artifact.Record == nil || artifact.Repair != nil ||
		!bytes.Equal(artifact.Notes.Project.Content, artifact.Notes.Vault.Content) ||
		artifact.Notes.Project.RelativePath != artifact.Notes.Vault.RelativePath {
		return ErrInvalidConflict
	}
	body := artifact.Notes.Project.Content
	relative := artifact.Notes.Project.RelativePath
	projectRelative := path.Join("docs/session-review", relative)
	vaultRelative := path.Join(engine.options.VaultReviewPath, relative)
	projectBefore, projectFound, err := engine.project.ReadRegularOptional(projectRelative, MaxConflictRecordBytes)
	if err != nil {
		return err
	}
	vaultBefore, vaultFound, err := engine.vault.ReadRegularOptional(vaultRelative, MaxConflictRecordBytes)
	if err != nil {
		return err
	}
	if projectFound {
		stored, parseErr := ParseConflictRecord(projectBefore)
		if parseErr != nil || !sameOpenConflictIdentity(stored, *artifact.Record) {
			return errors.New("hidden conflict identity collision")
		}
		body = projectBefore
	}
	if vaultFound {
		stored, parseErr := ParseConflictRecord(vaultBefore)
		if parseErr != nil || !sameOpenConflictIdentity(stored, *artifact.Record) || (projectFound && !bytes.Equal(projectBefore, vaultBefore)) {
			return errors.New("hidden conflict identity collision")
		}
		if !projectFound {
			body = vaultBefore
		}
	}
	if projectFound && vaultFound {
		return nil
	}
	desiredHash := syncdoc.ContentHash(body)
	txn := Transaction{
		Version: 1, Kind: TxnConflictRecord, EntityID: artifact.Record.ID,
		DesiredHash: desiredHash, ExpectedBaseHash: desiredHash,
		ExpectedProjectHash: optionalConflictHash(projectBefore, projectFound),
		ExpectedVaultHash:   optionalConflictHash(vaultBefore, vaultFound),
		Stage:               TxnPlanned, UpdatedAt: engine.options.Now().UTC(),
	}
	if err := engine.transactions.Save(txn); err != nil {
		return err
	}
	return engine.resumeConflictRecord(ctx, artifact.Record.ID, body, projectBefore, projectFound, vaultBefore, vaultFound, txn)
}

func (engine *Engine) planResolvedConflict(record ConflictRecord, action ResolutionAction, resolvedHash string) ([]byte, []byte, Transaction, error) {
	resolvedAt := engine.options.Now().UTC()
	resolved, err := MarkConflictResolved(record, action, resolvedHash, resolvedAt)
	if err != nil {
		return nil, nil, Transaction{}, err
	}
	desired, err := RenderConflict(resolved)
	if err != nil {
		return nil, nil, Transaction{}, err
	}
	expected, err := RenderConflict(record)
	if err != nil {
		return nil, nil, Transaction{}, err
	}
	if err := engine.verifyHiddenConflict(record.ID, expected); err != nil {
		return nil, nil, Transaction{}, ErrStaleConflict
	}
	expectedHash := syncdoc.ContentHash(expected)
	txn := Transaction{
		Version: 1, Kind: TxnConflictResolve, EntityID: record.ID,
		DesiredHash: syncdoc.ContentHash(desired), ExpectedBaseHash: expectedHash,
		ExpectedProjectHash: expectedHash, ExpectedVaultHash: expectedHash,
		Stage: TxnPlanned, UpdatedAt: resolvedAt,
	}
	if err := engine.transactions.Save(txn); err != nil {
		return nil, nil, Transaction{}, err
	}
	return desired, expected, txn, nil
}

func (engine *Engine) resumeConflictResolution(ctx context.Context, conflictID string, desired, expected []byte, txn Transaction) error {
	projectRelative := path.Join("docs/session-review/.session-reviewer/conflicts", conflictID+".json")
	vaultRelative := path.Join(engine.options.VaultReviewPath, ".session-reviewer/conflicts", conflictID+".json")
	if txn.Stage == TxnPlanned {
		if err := engine.writeHiddenConflictSide(ctx, SideProject, projectRelative, desired, expected, true); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnProjectWritten); err != nil {
			return err
		}
	}
	if txn.Stage == TxnProjectWritten {
		if err := engine.writeHiddenConflictSide(ctx, SideVault, vaultRelative, desired, expected, true); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnVaultWritten); err != nil {
			return err
		}
	}
	if txn.Stage == TxnVaultWritten {
		if err := engine.verifyHiddenConflict(conflictID, desired); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnBaseCommitted); err != nil {
			return err
		}
	}
	if txn.Stage == TxnBaseCommitted {
		if err := engine.verifyHiddenConflict(conflictID, desired); err != nil {
			return err
		}
	}
	return engine.transactions.Remove(conflictID)
}

func sameOpenConflictIdentity(stored, current ConflictRecord) bool {
	return stored.ResolutionStatus == ResolutionOpen && current.ResolutionStatus == ResolutionOpen &&
		stored.Version == current.Version && stored.ID == current.ID && stored.EntityID == current.EntityID && stored.ProjectID == current.ProjectID && stored.Kind == current.Kind &&
		stored.RelativePath == current.RelativePath && stored.BasePath == current.BasePath && stored.ProjectPath == current.ProjectPath && stored.VaultPath == current.VaultPath &&
		stored.BaseHash == current.BaseHash && stored.ProjectHash == current.ProjectHash && stored.VaultHash == current.VaultHash &&
		bytes.Equal(stored.Base, current.Base) && bytes.Equal(stored.Project, current.Project) && bytes.Equal(stored.Vault, current.Vault) && bytes.Equal(stored.Suggested, current.Suggested)
}

func optionalConflictHash(body []byte, found bool) string {
	if !found {
		return ""
	}
	return syncdoc.ContentHash(body)
}

func (engine *Engine) resumeConflictRecord(ctx context.Context, conflictID string, body, projectBefore []byte, projectFound bool, vaultBefore []byte, vaultFound bool, txn Transaction) error {
	projectRelative := path.Join("docs/session-review/.session-reviewer/conflicts", conflictID+".json")
	vaultRelative := path.Join(engine.options.VaultReviewPath, ".session-reviewer/conflicts", conflictID+".json")
	if txn.Stage == TxnPlanned {
		if err := engine.writeHiddenConflictSide(ctx, SideProject, projectRelative, body, projectBefore, projectFound); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnProjectWritten); err != nil {
			return err
		}
	}
	if txn.Stage == TxnProjectWritten {
		if err := engine.writeHiddenConflictSide(ctx, SideVault, vaultRelative, body, vaultBefore, vaultFound); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnVaultWritten); err != nil {
			return err
		}
	}
	if txn.Stage == TxnVaultWritten {
		if err := engine.verifyHiddenConflict(conflictID, body); err != nil {
			return err
		}
		if err := engine.advanceDerived(&txn, TxnBaseCommitted); err != nil {
			return err
		}
	}
	if txn.Stage == TxnBaseCommitted {
		if err := engine.verifyHiddenConflict(conflictID, body); err != nil {
			return err
		}
	}
	return engine.transactions.Remove(conflictID)
}

func (engine *Engine) writeHiddenConflictSide(ctx context.Context, side Side, relative string, body, expected []byte, expectedFound bool) error {
	directory := engine.project
	if side == SideVault {
		directory = engine.vault
	}
	current, found, err := directory.ReadRegularOptional(relative, MaxConflictRecordBytes)
	if err != nil {
		return err
	}
	if found && bytes.Equal(current, body) {
		return nil
	}
	if found != expectedFound || (found && !bytes.Equal(current, expected)) {
		return errors.New("hidden conflict changed after planning")
	}
	if err := directory.EnsureDirectory(path.Dir(relative), 0o700); err != nil {
		return err
	}
	return engine.writer.WriteIfUnchanged(ctx, side, relative, body, 0o600, expected, expectedFound)
}

func (engine *Engine) verifyHiddenConflict(conflictID string, body []byte) error {
	for _, target := range []struct {
		directory interface {
			ReadRegular(string, int64) ([]byte, bool, error)
		}
		relative string
	}{
		{engine.project, path.Join("docs/session-review/.session-reviewer/conflicts", conflictID+".json")},
		{engine.vault, path.Join(engine.options.VaultReviewPath, ".session-reviewer/conflicts", conflictID+".json")},
	} {
		current, found, err := target.directory.ReadRegular(target.relative, MaxConflictRecordBytes)
		if err != nil || !found || !bytes.Equal(current, body) {
			return errors.New("hidden conflict publication did not converge")
		}
	}
	return nil
}

func (engine *Engine) recoverConflictRecord(ctx context.Context, txn Transaction) error {
	projectRelative := path.Join("docs/session-review/.session-reviewer/conflicts", txn.EntityID+".json")
	vaultRelative := path.Join(engine.options.VaultReviewPath, ".session-reviewer/conflicts", txn.EntityID+".json")
	projectBody, projectFound, err := engine.project.ReadRegularOptional(projectRelative, MaxConflictRecordBytes)
	if err != nil {
		return err
	}
	vaultBody, vaultFound, err := engine.vault.ReadRegularOptional(vaultRelative, MaxConflictRecordBytes)
	if err != nil {
		return err
	}
	var body []byte
	if projectFound && syncdoc.ContentHash(projectBody) == txn.DesiredHash {
		body = projectBody
	} else if vaultFound && syncdoc.ContentHash(vaultBody) == txn.DesiredHash {
		body = vaultBody
	}
	if body == nil {
		if txn.Stage == TxnPlanned && optionalConflictHash(projectBody, projectFound) == txn.ExpectedProjectHash && optionalConflictHash(vaultBody, vaultFound) == txn.ExpectedVaultHash {
			return engine.transactions.Remove(txn.EntityID)
		}
		return errors.New("interrupted hidden conflict content is unavailable")
	}
	if projectFound && syncdoc.ContentHash(projectBody) != txn.DesiredHash && syncdoc.ContentHash(projectBody) != txn.ExpectedProjectHash {
		return errors.New("interrupted project conflict changed")
	}
	if vaultFound && syncdoc.ContentHash(vaultBody) != txn.DesiredHash && syncdoc.ContentHash(vaultBody) != txn.ExpectedVaultHash {
		return errors.New("interrupted vault conflict changed")
	}
	return engine.resumeConflictRecord(ctx, txn.EntityID, body, projectBody, projectFound, vaultBody, vaultFound, txn)
}

func (engine *Engine) recoverConflictResolution(ctx context.Context, txn Transaction) error {
	projectRelative := path.Join("docs/session-review/.session-reviewer/conflicts", txn.EntityID+".json")
	vaultRelative := path.Join(engine.options.VaultReviewPath, ".session-reviewer/conflicts", txn.EntityID+".json")
	projectBody, projectFound, err := engine.project.ReadRegularOptional(projectRelative, MaxConflictRecordBytes)
	if err != nil {
		return err
	}
	vaultBody, vaultFound, err := engine.vault.ReadRegularOptional(vaultRelative, MaxConflictRecordBytes)
	if err != nil {
		return err
	}
	if !projectFound || !vaultFound {
		return errors.New("interrupted hidden conflict resolution disappeared")
	}
	projectHash := syncdoc.ContentHash(projectBody)
	vaultHash := syncdoc.ContentHash(vaultBody)
	for _, currentHash := range []string{projectHash, vaultHash} {
		if currentHash != txn.ExpectedBaseHash && currentHash != txn.DesiredHash {
			return errors.New("interrupted hidden conflict resolution changed")
		}
	}
	var desired []byte
	if projectHash == txn.DesiredHash {
		desired = projectBody
	} else if vaultHash == txn.DesiredHash {
		desired = vaultBody
	}
	var expected []byte
	if projectHash == txn.ExpectedBaseHash {
		expected = projectBody
	} else if vaultHash == txn.ExpectedBaseHash {
		expected = vaultBody
	}
	if expected == nil {
		return errors.New("interrupted open conflict record is unavailable")
	}
	if desired == nil {
		openRecord, parseErr := ParseConflictRecord(expected)
		if parseErr != nil || openRecord.ResolutionStatus != ResolutionOpen || openRecord.ID != txn.EntityID {
			return errors.New("interrupted open conflict record is invalid")
		}
		accepted, found, readErr := engine.project.ReadRegularOptional(path.Join("docs/session-review", openRecord.RelativePath), int64(syncdoc.MaxDocumentBytes))
		if readErr != nil {
			return readErr
		}
		if found {
			resolvedHash := syncdoc.ContentHash(accepted)
			for _, action := range []ResolutionAction{AcceptProject, AcceptObsidian, ManualMerge} {
				resolved, markErr := MarkConflictResolved(openRecord, action, resolvedHash, txn.UpdatedAt)
				if markErr != nil {
					continue
				}
				body, renderErr := RenderConflict(resolved)
				if renderErr == nil && syncdoc.ContentHash(body) == txn.DesiredHash {
					desired = body
					break
				}
			}
		}
	}
	if desired == nil {
		if txn.Stage == TxnPlanned && projectHash == txn.ExpectedBaseHash && vaultHash == txn.ExpectedBaseHash {
			// The durable intent preceded the human-document transaction, which
			// never committed. Leave the open record intact for a later retry.
			return engine.transactions.Remove(txn.EntityID)
		}
		return errors.New("interrupted resolved conflict record cannot be reconstructed")
	}
	resolved, err := ParseConflictRecord(desired)
	if err != nil || resolved.ResolutionStatus != ResolutionResolved {
		return errors.New("interrupted resolved conflict record is invalid")
	}
	return engine.resumeConflictResolution(ctx, txn.EntityID, desired, expected, txn)
}

func (engine *Engine) loadMirroredConflictRecord(conflictID string) (ConflictRecord, error) {
	if !stableBaseID.MatchString(conflictID) || !strings.HasPrefix(conflictID, "conflict-") {
		return ConflictRecord{}, ErrInvalidConflict
	}
	var bodies [][]byte
	for _, target := range []struct {
		directory interface {
			ReadRegular(string, int64) ([]byte, bool, error)
		}
		relative string
	}{
		{engine.project, path.Join("docs/session-review/.session-reviewer/conflicts", conflictID+".json")},
		{engine.vault, path.Join(engine.options.VaultReviewPath, ".session-reviewer/conflicts", conflictID+".json")},
	} {
		body, found, err := target.directory.ReadRegular(target.relative, MaxConflictRecordBytes)
		if err != nil || !found {
			return ConflictRecord{}, ErrStaleConflict
		}
		bodies = append(bodies, body)
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		return ConflictRecord{}, ErrStaleConflict
	}
	record, err := ParseConflictRecord(bodies[0])
	if err != nil || record.ID != conflictID {
		return ConflictRecord{}, ErrStaleConflict
	}
	return record, nil
}

func SelectResolution(record ConflictRecord, resolution Resolution, liveProject, liveVault Candidate, manual *syncdoc.Document) (syncdoc.Document, error) {
	if resolution.ConflictID == "" || resolution.ConflictID != record.ID {
		return syncdoc.Document{}, ErrInvalidResolution
	}
	switch resolution.Action {
	case AcceptProject, AcceptObsidian:
		if resolution.ManualFile != "" || manual != nil {
			return syncdoc.Document{}, ErrInvalidResolution
		}
	case ManualMerge:
		if manual == nil || strings.TrimSpace(resolution.ManualFile) == "" || strings.ContainsRune(resolution.ManualFile, 0) {
			return syncdoc.Document{}, ErrInvalidResolution
		}
	default:
		return syncdoc.Document{}, ErrInvalidResolution
	}

	documents, err := parseConflictDocuments(record)
	if err != nil {
		return syncdoc.Document{}, err
	}
	if err := validateLiveConflictCandidate(record.Project, record.ProjectHash, conflictPath(record.ProjectPath, record.RelativePath), liveProject); err != nil {
		return syncdoc.Document{}, err
	}
	if err := validateLiveConflictCandidate(record.Vault, record.VaultHash, conflictPath(record.VaultPath, record.RelativePath), liveVault); err != nil {
		return syncdoc.Document{}, err
	}

	var selected syncdoc.Document
	switch resolution.Action {
	case AcceptProject:
		if documents.project == nil {
			return syncdoc.Document{}, ErrInvalidResolution
		}
		selected = *documents.project
	case AcceptObsidian:
		if documents.vault == nil {
			return syncdoc.Document{}, ErrInvalidResolution
		}
		selected = *documents.vault
	case ManualMerge:
		selected = *manual
	}
	selected, err = validateSelectedDocument(record, documents.base, documents.identity, selected)
	if err != nil {
		return syncdoc.Document{}, err
	}
	if record.ResolutionStatus == ResolutionResolved {
		rendered, renderErr := selected.Render()
		if renderErr != nil {
			return syncdoc.Document{}, syncdoc.ErrInvalidDocument
		}
		if resolution.Action != record.ResolutionAction || syncdoc.ContentHash(rendered) != record.ResolvedHash {
			return syncdoc.Document{}, ErrConflictResolved
		}
	}
	return selected, nil
}

func MarkConflictResolved(record ConflictRecord, action ResolutionAction, resolvedHash string, resolvedAt time.Time) (ConflictRecord, error) {
	if _, _, sensitive := sensitiveConflictCandidate(record); sensitive {
		return ConflictRecord{}, ErrSensitiveContent
	}
	if err := validateConflictRecord(record); err != nil {
		return ConflictRecord{}, err
	}
	if !validResolutionAction(action) || !lowerSHA256.MatchString(resolvedHash) || resolvedAt.IsZero() || resolvedAt.Before(record.CreatedAt) {
		return ConflictRecord{}, ErrInvalidResolution
	}
	if record.ResolutionStatus == ResolutionResolved {
		if record.ResolutionAction != action || record.ResolvedHash != resolvedHash {
			return ConflictRecord{}, ErrConflictResolved
		}
		return cloneConflictRecord(record), nil
	}
	result := cloneConflictRecord(record)
	result.ResolutionStatus = ResolutionResolved
	result.ResolutionAction = action
	result.ResolvedHash = resolvedHash
	result.ResolvedAt = resolvedAt.UTC()
	return result, nil
}

type conflictDocuments struct {
	base, project, vault *syncdoc.Document
	identity             syncdoc.Identity
}

func parseConflictDocuments(record ConflictRecord) (conflictDocuments, error) {
	if _, _, sensitive := sensitiveConflictCandidate(record); sensitive {
		return conflictDocuments{}, ErrSensitiveContent
	}
	if err := validateConflictRecord(record); err != nil {
		if errors.Is(err, ErrSensitiveContent) {
			return conflictDocuments{}, err
		}
		return conflictDocuments{}, ErrInvalidConflict
	}
	parse := func(content []byte, relative string) (*syncdoc.Document, error) {
		if len(content) == 0 {
			return nil, nil
		}
		if err := validateMergePath(relative); err != nil {
			return nil, ErrInvalidConflict
		}
		document, err := syncdoc.Parse(relative, content)
		if err != nil || (!isCompactV2Document(document) && hasDuplicateConflictSection(document)) {
			return nil, ErrInvalidConflict
		}
		return &document, nil
	}
	base, err := parse(record.Base, conflictPath(record.BasePath, record.RelativePath))
	if err != nil {
		return conflictDocuments{}, err
	}
	project, err := parse(record.Project, conflictPath(record.ProjectPath, record.RelativePath))
	if err != nil {
		return conflictDocuments{}, err
	}
	vault, err := parse(record.Vault, conflictPath(record.VaultPath, record.RelativePath))
	if err != nil {
		return conflictDocuments{}, err
	}
	if project == nil && vault == nil {
		return conflictDocuments{}, ErrInvalidConflict
	}

	documents := []*syncdoc.Document{base, project, vault}
	var expected *syncdoc.Identity
	for _, document := range documents {
		if document == nil {
			continue
		}
		identity, identityErr := document.Identity()
		if identityErr != nil || identity.ID != record.EntityID || identity.ProjectID != record.ProjectID {
			return conflictDocuments{}, syncdoc.ErrReservedField
		}
		if expected == nil {
			copyIdentity := identity
			expected = &copyIdentity
		} else if identity != *expected {
			return conflictDocuments{}, syncdoc.ErrReservedField
		}
	}
	if expected == nil {
		return conflictDocuments{}, ErrInvalidConflict
	}
	if base != nil {
		if !validDocumentShape(*base) {
			return conflictDocuments{}, syncdoc.ErrInvalidDocument
		}
		for _, document := range []*syncdoc.Document{project, vault} {
			if document == nil {
				continue
			}
			if validationErr := document.ValidateHumanChanges(*base); validationErr != nil {
				return conflictDocuments{}, validationErr
			}
			if !validDocumentShape(*document) {
				return conflictDocuments{}, syncdoc.ErrInvalidDocument
			}
		}
	} else {
		for _, document := range []*syncdoc.Document{project, vault} {
			if document != nil && !validNewCandidate(*document, record.EntityID, record.ProjectID) {
				return conflictDocuments{}, syncdoc.ErrInvalidDocument
			}
		}
	}
	return conflictDocuments{base: base, project: project, vault: vault, identity: *expected}, nil
}

func validateLiveConflictCandidate(embedded []byte, embeddedHash, relative string, live Candidate) error {
	if len(embedded) == 0 {
		if live.Present {
			return ErrStaleConflict
		}
		return nil
	}
	if !live.Present || live.RelativePath != relative {
		return ErrStaleConflict
	}
	liveBody := live.Source
	if liveBody == nil {
		var err error
		liveBody, err = live.Document.Render()
		if err != nil {
			return ErrStaleConflict
		}
	} else if live.SourceHash == "" || syncdoc.ContentHash(liveBody) != live.SourceHash {
		return ErrStaleConflict
	}
	if syncdoc.ContentHash(liveBody) != embeddedHash || !bytes.Equal(liveBody, embedded) {
		return ErrStaleConflict
	}
	parsed, err := syncdoc.Parse(relative, liveBody)
	if err != nil || !parsed.SemanticEqual(live.Document) {
		return ErrStaleConflict
	}
	return nil
}

func validateSelectedDocument(record ConflictRecord, base *syncdoc.Document, expectedIdentity syncdoc.Identity, selected syncdoc.Document) (syncdoc.Document, error) {
	identity, err := selected.Identity()
	if err != nil || identity != expectedIdentity || identity.ID != record.EntityID || identity.ProjectID != record.ProjectID {
		return syncdoc.Document{}, syncdoc.ErrReservedField
	}
	if base != nil {
		baseIdentity, baseErr := base.Identity()
		if baseErr != nil || identity != baseIdentity {
			return syncdoc.Document{}, syncdoc.ErrReservedField
		}
	}
	rendered, err := selected.Render()
	if err != nil {
		return syncdoc.Document{}, syncdoc.ErrInvalidDocument
	}
	if result := redact.Default().Text(string(rendered)); len(result.Findings) != 0 {
		return syncdoc.Document{}, ErrSensitiveContent
	}
	selected, err = finalizeAcceptedDocument(selected, base, true)
	if err != nil {
		return syncdoc.Document{}, err
	}
	if base == nil {
		if !validNewCandidate(selected, record.EntityID, record.ProjectID) {
			return syncdoc.Document{}, syncdoc.ErrInvalidDocument
		}
	} else if !validDocumentShape(selected) {
		return syncdoc.Document{}, syncdoc.ErrInvalidDocument
	}
	return selected, nil
}

func conflictPath(specific, fallback string) string {
	if specific != "" {
		return specific
	}
	return fallback
}

func hasDuplicateConflictSection(document syncdoc.Document) bool {
	for key := range document.Units() {
		if key.Kind != syncdoc.UnitSection {
			continue
		}
		hash := strings.LastIndexByte(key.Name, '#')
		if hash < 0 {
			return true
		}
		occurrence, err := strconv.Atoi(key.Name[hash+1:])
		if err != nil || occurrence != 1 {
			return true
		}
	}
	return false
}

func renderRepair(record RepairRecord) []byte {
	wire := struct {
		Version    int               `json:"version"`
		ID         string            `json:"id"`
		ProjectID  string            `json:"project_id"`
		EntityID   string            `json:"entity_id,omitempty"`
		SourceSide Side              `json:"source_side"`
		SourceHash string            `json:"source_hash"`
		IssueCode  syncdoc.IssueKind `json:"issue_code"`
		CreatedAt  string            `json:"created_at"`
	}{
		Version: record.Version, ID: record.ID, ProjectID: record.ProjectID, EntityID: record.EntityID,
		SourceSide: record.Side, SourceHash: record.SourceHash, IssueCode: record.IssueCode,
		CreatedAt: record.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	body, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return nil
	}
	return append(body, '\n')
}

func validateConflictRecord(record ConflictRecord) error {
	if record.Version != 1 || !stableBaseID.MatchString(record.EntityID) || !stableBaseID.MatchString(record.ProjectID) || !validPersistedConflictKind(record.Kind) || record.CreatedAt.IsZero() {
		return ErrInvalidConflict
	}
	if err := validateMergePath(record.RelativePath); err != nil {
		return ErrInvalidConflict
	}
	for _, relative := range []string{record.BasePath, record.ProjectPath, record.VaultPath} {
		if relative != "" {
			if err := validateMergePath(relative); err != nil {
				return ErrInvalidConflict
			}
		}
	}
	if record.CreatedAt.Location() != time.UTC || record.BaseHash != syncdoc.ContentHash(record.Base) || record.ProjectHash != syncdoc.ContentHash(record.Project) || record.VaultHash != syncdoc.ContentHash(record.Vault) {
		return ErrInvalidConflict
	}
	digest := sha256.Sum256([]byte(record.BaseHash + "|" + record.ProjectHash + "|" + record.VaultHash))
	wantID := fmt.Sprintf("conflict-%s-%x", record.EntityID, digest[:6])
	if record.ID != wantID {
		return ErrInvalidConflict
	}
	switch record.ResolutionStatus {
	case ResolutionOpen:
		if record.ResolutionAction != "" || record.ResolvedHash != "" || !record.ResolvedAt.IsZero() {
			return ErrInvalidConflict
		}
	case ResolutionResolved:
		if !validResolutionAction(record.ResolutionAction) || !lowerSHA256.MatchString(record.ResolvedHash) || record.ResolvedAt.IsZero() || record.ResolvedAt.Location() != time.UTC || record.ResolvedAt.Before(record.CreatedAt) {
			return ErrInvalidConflict
		}
	default:
		return ErrInvalidConflict
	}
	return nil
}

func validConflictKind(kind ConflictKind) bool {
	switch kind {
	case ConflictUnits, ConflictArchiveEdit, ConflictReserved, ConflictMalformed, ConflictCollision:
		return true
	default:
		return false
	}
}

func validPersistedConflictKind(kind ConflictKind) bool {
	return kind == ConflictUnits || kind == ConflictArchiveEdit
}

func validResolutionAction(action ResolutionAction) bool {
	return action == AcceptProject || action == AcceptObsidian || action == ManualMerge
}

func validRepairSide(side Side) bool {
	return side == SideProject || side == SideVault
}

func validRepairIssue(kind syncdoc.IssueKind) bool {
	switch kind {
	case syncdoc.IssueMalformed, syncdoc.IssueDuplicateID, syncdoc.IssuePathCollision, syncdoc.IssueReservedEdit, syncdoc.IssueSensitive:
		return true
	default:
		return false
	}
}

func sensitiveConflictCandidate(record ConflictRecord) (Side, []byte, bool) {
	candidates := []struct {
		side  Side
		value []byte
	}{
		{side: SideProject, value: record.Base},
		{side: SideProject, value: record.Project},
		{side: SideVault, value: record.Vault},
		{side: SideProject, value: record.Suggested},
	}
	redactor := redact.Default()
	for _, candidate := range candidates {
		if result := redactor.Text(string(candidate.value)); len(result.Findings) != 0 {
			return candidate.side, bytes.Clone(candidate.value), true
		}
	}
	return "", nil, false
}

func isolatedConflictSource(record ConflictRecord) (syncdoc.IssueKind, Side, []byte, string, bool) {
	var issue syncdoc.IssueKind
	switch record.Kind {
	case ConflictMalformed:
		issue = syncdoc.IssueMalformed
	case ConflictCollision:
		issue = syncdoc.IssuePathCollision
	case ConflictReserved:
		issue = syncdoc.IssueReservedEdit
	default:
		return "", "", nil, "", false
	}
	if len(record.Project) != 0 {
		return issue, SideProject, bytes.Clone(record.Project), conflictPath(record.ProjectPath, record.RelativePath), true
	}
	if len(record.Vault) != 0 {
		return issue, SideVault, bytes.Clone(record.Vault), conflictPath(record.VaultPath, record.RelativePath), true
	}
	return issue, SideProject, bytes.Clone(record.Base), conflictPath(record.BasePath, record.RelativePath), true
}

func cloneConflictRecord(record ConflictRecord) ConflictRecord {
	record.Base = bytes.Clone(record.Base)
	record.Project = bytes.Clone(record.Project)
	record.Vault = bytes.Clone(record.Vault)
	record.Suggested = bytes.Clone(record.Suggested)
	return record
}

func longestBacktickRun(values ...[]byte) int {
	longest := 0
	for _, value := range values {
		current := 0
		for _, character := range value {
			if character == '`' {
				current++
				if current > longest {
					longest = current
				}
				continue
			}
			current = 0
		}
	}
	return longest
}

func writeConflictScalar(out *bytes.Buffer, key, value string) {
	fmt.Fprintf(out, "%s: %s\n", key, strconv.Quote(value))
}

func writeConflictCandidate(out *bytes.Buffer, title, fence string, content []byte) {
	fmt.Fprintf(out, "\n## %s\n\nbytes: %d\n\n%s markdown\n", title, len(content), fence)
	out.Write(content)
	if len(content) == 0 || content[len(content)-1] != '\n' {
		out.WriteByte('\n')
	}
	out.WriteString(fence)
	out.WriteByte('\n')
}
