package sync

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

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
	wantID := fmt.Sprintf("conflict-%s-%s-%x", record.CreatedAt.Format("20060102T150405Z"), record.EntityID, digest[:6])
	if record.ID != "" && record.ID != wantID {
		return ConflictArtifact{}, ErrInvalidConflict
	}
	record.ID = wantID

	note, err := RenderConflict(record)
	if err != nil {
		return ConflictArtifact{}, err
	}
	relative := "sync-conflicts/" + record.ID + ".md"
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
		Version: 1, ID: "repair-" + createdAt.Format("20060102T150405Z") + "-" + suffix,
		ProjectID: input.ProjectID, EntityID: entityID, Side: input.Side, IssueCode: input.IssueCode,
		SourceHash: syncdoc.ContentHash(input.Source), CreatedAt: createdAt,
	}
	note := renderRepair(record)
	relative := "sync-conflicts/" + record.ID + ".md"
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
	fence := strings.Repeat("`", longestBacktickRun(record.Base, record.Project, record.Vault, record.Suggested)+1)
	if len(fence) < 3 {
		fence = "```"
	}

	var out bytes.Buffer
	out.WriteString("---\n")
	writeConflictScalar(&out, "version", strconv.Itoa(record.Version))
	writeConflictScalar(&out, "conflict_id", record.ID)
	writeConflictScalar(&out, "entity_id", record.EntityID)
	writeConflictScalar(&out, "project_id", record.ProjectID)
	writeConflictScalar(&out, "kind", string(record.Kind))
	writeConflictScalar(&out, "base_hash", record.BaseHash)
	writeConflictScalar(&out, "project_hash", record.ProjectHash)
	writeConflictScalar(&out, "vault_hash", record.VaultHash)
	writeConflictScalar(&out, "resolution_status", string(record.ResolutionStatus))
	writeConflictScalar(&out, "created_at", record.CreatedAt.UTC().Format(time.RFC3339Nano))
	if record.ResolutionStatus == ResolutionResolved {
		writeConflictScalar(&out, "resolution_action", string(record.ResolutionAction))
		writeConflictScalar(&out, "resolved_hash", record.ResolvedHash)
		writeConflictScalar(&out, "resolved_at", record.ResolvedAt.Format(time.RFC3339Nano))
	}
	writeConflictScalar(&out, "accept_project", "session-reviewer sync resolve --conflict "+record.ID+" --action accept_project")
	writeConflictScalar(&out, "accept_obsidian", "session-reviewer sync resolve --conflict "+record.ID+" --action accept_obsidian")
	writeConflictScalar(&out, "manual_merge", "session-reviewer sync resolve --conflict "+record.ID+" --action manual_merge --file <path>")
	out.WriteString("---\n\n# Synchronization conflict\n")
	writeConflictCandidate(&out, "Base", fence, record.Base)
	writeConflictCandidate(&out, "Project", fence, record.Project)
	writeConflictCandidate(&out, "Obsidian", fence, record.Vault)
	writeConflictCandidate(&out, "Suggested Merge", fence, record.Suggested)
	return out.Bytes(), nil
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
		if err != nil || hasDuplicateConflictSection(document) {
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
	rendered, err := live.Document.Render()
	if err != nil || syncdoc.ContentHash(rendered) != embeddedHash || !bytes.Equal(rendered, embedded) {
		return ErrStaleConflict
	}
	if _, err := syncdoc.Parse(relative, rendered); err != nil {
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
	var out bytes.Buffer
	out.WriteString("---\n")
	writeConflictScalar(&out, "version", strconv.Itoa(record.Version))
	writeConflictScalar(&out, "repair_id", record.ID)
	writeConflictScalar(&out, "project_id", record.ProjectID)
	if record.EntityID != "" {
		writeConflictScalar(&out, "entity_id", record.EntityID)
	}
	writeConflictScalar(&out, "source_side", string(record.Side))
	writeConflictScalar(&out, "source_hash", record.SourceHash)
	writeConflictScalar(&out, "issue_code", string(record.IssueCode))
	writeConflictScalar(&out, "created_at", record.CreatedAt.Format(time.RFC3339Nano))
	writeConflictScalar(&out, "status_command", "session-reviewer sync status")
	writeConflictScalar(&out, "manual_merge", "session-reviewer sync resolve --repair "+record.ID+" --action manual_merge --file <path>")
	out.WriteString("---\n\n# Synchronization repair required\n\nThe source was isolated without copying or embedding its content.\n")
	return out.Bytes()
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
	wantID := fmt.Sprintf("conflict-%s-%s-%x", record.CreatedAt.Format("20060102T150405Z"), record.EntityID, digest[:6])
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
