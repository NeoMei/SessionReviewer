package sync

import (
	"bytes"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
	"gopkg.in/yaml.v3"
)

type Candidate struct {
	Present      bool
	RelativePath string
	Document     syncdoc.Document
	Hash         string
	// Source and SourceHash retain the exact on-disk preimage when Document
	// rendering canonicalizes representation such as compact-v2 CRLF input.
	Source     []byte
	SourceHash string
}

type MergeInput struct {
	EntityID string
	Base     *syncdoc.Document
	Project  Candidate
	Vault    Candidate

	// These fields carry immutable mapping context which cannot be derived from
	// syncdoc.Document because its repository-relative path is intentionally
	// private. They are required when the corresponding merge rule uses them.
	ProjectID        string
	BasePath         string
	GOOS             string
	CaseMode         platform.CaseMode
	OccupiedPathKeys map[string]string
}

type MergeKind string

const (
	MergeNoop         MergeKind = "noop"
	MergeWriteProject MergeKind = "write_project"
	MergeWriteVault   MergeKind = "write_vault"
	MergeWriteBoth    MergeKind = "write_both"
	MergeConflict     MergeKind = "conflict"
)

type UnitConflict struct {
	Key                  syncdoc.UnitKey
	Base, Project, Vault syncdoc.Unit
}

type MergeResult struct {
	Kind      MergeKind
	Accepted  *syncdoc.Document
	Conflicts []UnitConflict
	Reason    string
}

var relativePathUnitKey = syncdoc.UnitKey{Kind: syncdoc.UnitKind("path"), Name: "relative_path"}

var errCandidateHash = errors.New("candidate content hash mismatch")

func Merge(input MergeInput) MergeResult {
	if input.Base == nil {
		return mergeFirstSync(input)
	}
	return mergeFromBase(input)
}

func mergeFromBase(input MergeInput) MergeResult {
	baseIdentity, err := input.Base.Identity()
	if err != nil || input.BasePath == "" || input.ProjectID == "" || baseIdentity.ID != input.EntityID || baseIdentity.ProjectID != input.ProjectID {
		return conflictResult("invalid_input", nil)
	}
	for _, candidate := range []Candidate{input.Project, input.Vault} {
		if !candidate.Present {
			continue
		}
		identity, identityErr := candidate.Document.Identity()
		if identityErr != nil || identity != baseIdentity {
			return conflictResult("reserved_field", nil)
		}
		if validationErr := candidate.Document.ValidateHumanChanges(*input.Base); validationErr != nil {
			return conflictResult(reasonForHumanValidation(validationErr), nil)
		}
		if !validDocumentShape(candidate.Document) {
			return conflictResult("invalid_document", nil)
		}
	}
	targetPath, pathFailure := mergePathFromBase(input)
	if pathFailure != nil {
		return *pathFailure
	}
	projectArchives := candidateArchives(input.Project, *input.Base)
	vaultArchives := candidateArchives(input.Vault, *input.Base)
	if projectArchives != vaultArchives {
		other := input.Project
		if projectArchives {
			other = input.Vault
		}
		if candidateChangesOtherUnit(other, *input.Base) || candidatePathChanged(input, other) {
			return conflictResult("archive_vs_modify", nil)
		}
	}

	baseUnits := input.Base.SemanticUnits()
	projectUnits := baseUnits
	if input.Project.Present {
		projectUnits = input.Project.Document.SemanticUnits()
	}
	vaultUnits := baseUnits
	if input.Vault.Present {
		vaultUnits = input.Vault.Document.SemanticUnits()
	}
	if conflicts := compactV2MarkerPresenceConflicts(*input.Base, baseUnits, projectUnits, vaultUnits); len(conflicts) != 0 {
		return conflictResult("unit_conflict", conflicts)
	}
	merged, conflicts := mergeUnitSets(baseUnits, projectUnits, vaultUnits)
	if len(conflicts) != 0 {
		return conflictResult("unit_conflict", conflicts)
	}
	candidate, err := input.Base.WithSemanticUnits(merged)
	if err != nil {
		return conflictResult("invalid_document", nil)
	}
	rendered, err := candidate.Render()
	if err != nil {
		return conflictResult("invalid_document", nil)
	}
	candidate, err = syncdoc.Parse(targetPath, rendered)
	if err != nil {
		return conflictResult("invalid_path", nil)
	}
	changed := !unitSetsEqual(baseUnits, merged) || targetPath != input.BasePath
	accepted, err := finalizeAcceptedDocument(candidate, input.Base, changed)
	if err != nil {
		return conflictResult(reasonForHumanValidation(err), nil)
	}
	if !validDocumentShape(accepted) {
		return conflictResult("invalid_document", nil)
	}
	return acceptedResult(input, targetPath, accepted)
}

func mergePathFromBase(input MergeInput) (string, *MergeResult) {
	if err := validateMergePath(input.BasePath); err != nil {
		failure := conflictResult("invalid_path", nil)
		return "", &failure
	}
	if expected, compact := compactV2Filename(*input.Base); compact {
		if input.BasePath != expected {
			failure := conflictResult("invalid_path", nil)
			return "", &failure
		}
		for _, candidate := range []Candidate{input.Project, input.Vault} {
			if candidate.Present && candidate.RelativePath != expected {
				failure := conflictResult("invalid_path", nil)
				return "", &failure
			}
		}
		return expected, nil
	}
	rename := false
	for _, candidate := range []Candidate{input.Project, input.Vault} {
		if !candidate.Present {
			continue
		}
		if err := validateCandidateClaim(candidate); err != nil {
			failure := conflictResult(candidateClaimReason(err), nil)
			return "", &failure
		}
		rename = rename || candidate.RelativePath != input.BasePath
	}
	if !rename {
		return input.BasePath, nil
	}
	if !validPathContext(input) {
		failure := conflictResult("invalid_input", nil)
		return "", &failure
	}
	baseKey, _ := platform.PathKey(input.GOOS, input.CaseMode, input.BasePath)
	baseUnit := pathUnit(input.BasePath)
	projectOriginal, projectEffective := baseUnit, baseUnit
	if input.Project.Present {
		projectOriginal = pathUnit(input.Project.RelativePath)
		projectEffective = projectOriginal
		if key, _ := platform.PathKey(input.GOOS, input.CaseMode, input.Project.RelativePath); key == baseKey {
			projectEffective = baseUnit
		}
	}
	vaultOriginal, vaultEffective := baseUnit, baseUnit
	if input.Vault.Present {
		vaultOriginal = pathUnit(input.Vault.RelativePath)
		vaultEffective = vaultOriginal
		if key, _ := platform.PathKey(input.GOOS, input.CaseMode, input.Vault.RelativePath); key == baseKey {
			vaultEffective = baseUnit
		}
	}
	merged, conflict := mergeUnit(baseUnit, projectEffective, vaultEffective)
	if conflict {
		failure := conflictResult("path_conflict", []UnitConflict{{Key: relativePathUnitKey, Base: baseUnit, Project: projectOriginal, Vault: vaultOriginal}})
		return "", &failure
	}
	target := string(merged.Value)
	if err := validateMergePath(target); err != nil {
		failure := conflictResult("invalid_path", nil)
		return "", &failure
	}
	targetKey, _ := platform.PathKey(input.GOOS, input.CaseMode, target)
	if owner, occupied := input.OccupiedPathKeys[targetKey]; occupied && owner != input.EntityID {
		failure := conflictResult("path_collision", nil)
		return "", &failure
	}
	return target, nil
}

func candidatePathChanged(input MergeInput, candidate Candidate) bool {
	if !candidate.Present || candidate.RelativePath == input.BasePath {
		return false
	}
	if !validPathContext(input) {
		return true
	}
	baseKey, baseErr := platform.PathKey(input.GOOS, input.CaseMode, input.BasePath)
	candidateKey, candidateErr := platform.PathKey(input.GOOS, input.CaseMode, candidate.RelativePath)
	return baseErr != nil || candidateErr != nil || baseKey != candidateKey
}

func candidateArchives(candidate Candidate, base syncdoc.Document) bool {
	if !candidate.Present {
		return false
	}
	baseStatus, baseOK := decodeStringUnit(base.Units(), "status")
	candidateStatus, candidateOK := decodeStringUnit(candidate.Document.Units(), "status")
	return baseOK && candidateOK && baseStatus != "archived" && candidateStatus == "archived"
}

func candidateChangesOtherUnit(candidate Candidate, base syncdoc.Document) bool {
	if !candidate.Present {
		return false
	}
	baseUnits, candidateUnits := base.SemanticUnits(), candidate.Document.SemanticUnits()
	for _, key := range sortedUnitKeys(baseUnits, candidateUnits) {
		if !unitsEqual(baseUnits[key], candidateUnits[key]) {
			return true
		}
	}
	return false
}

func mergeFirstSync(input MergeInput) MergeResult {
	if !input.Project.Present && !input.Vault.Present {
		return conflictResult("invalid_input", nil)
	}
	if !validPathContext(input) {
		return conflictResult("invalid_input", nil)
	}
	for index, candidate := range []Candidate{input.Project, input.Vault} {
		if !candidate.Present {
			continue
		}
		if err := validateCandidateClaim(candidate); err != nil {
			return conflictResult(candidateClaimReason(err), nil)
		}
		valid := validNewCandidate(candidate.Document, input.EntityID, input.ProjectID)
		if index == 0 {
			valid = validImportedProjectCandidate(candidate.Document, input.EntityID, input.ProjectID)
		}
		if !valid {
			return conflictResult("invalid_new_entity", nil)
		}
	}
	var skeleton syncdoc.Document
	var target string
	switch {
	case input.Project.Present:
		skeleton, target = input.Project.Document, input.Project.RelativePath
	default:
		skeleton, target = input.Vault.Document, input.Vault.RelativePath
	}
	projectPath, vaultPath := syncdoc.Unit{}, syncdoc.Unit{}
	if input.Project.Present {
		projectPath = pathUnit(input.Project.RelativePath)
	}
	if input.Vault.Present {
		vaultPath = pathUnit(input.Vault.RelativePath)
	}
	projectEffective, vaultEffective := projectPath, vaultPath
	if input.Project.Present && input.Vault.Present {
		projectKey, _ := platform.PathKey(input.GOOS, input.CaseMode, input.Project.RelativePath)
		vaultKey, _ := platform.PathKey(input.GOOS, input.CaseMode, input.Vault.RelativePath)
		if projectKey == vaultKey {
			// With no Base spelling to preserve, Project is the deterministic
			// authority and Vault converges to that display spelling.
			vaultEffective = projectPath
		}
	}
	mergedPath, pathConflict := mergeUnit(syncdoc.Unit{}, projectEffective, vaultEffective)
	if pathConflict {
		return conflictResult("path_conflict", []UnitConflict{{Key: relativePathUnitKey, Project: cloneUnit(projectPath), Vault: cloneUnit(vaultPath)}})
	}
	target = string(mergedPath.Value)
	if err := validateMergePath(target); err != nil {
		return conflictResult("invalid_path", nil)
	}
	if expected, compact := compactV2Filename(skeleton); compact && target != expected {
		return conflictResult("invalid_path", nil)
	}
	targetKey, _ := platform.PathKey(input.GOOS, input.CaseMode, target)
	if owner, occupied := input.OccupiedPathKeys[targetKey]; occupied && owner != input.EntityID {
		return conflictResult("path_collision", nil)
	}
	projectUnits, vaultUnits := syncdoc.UnitSet{}, syncdoc.UnitSet{}
	if input.Project.Present {
		projectUnits = input.Project.Document.SemanticUnits()
	}
	if input.Vault.Present {
		vaultUnits = input.Vault.Document.SemanticUnits()
	}
	merged, conflicts := mergeUnitSets(syncdoc.UnitSet{}, projectUnits, vaultUnits)
	if len(conflicts) != 0 {
		return conflictResult("unit_conflict", conflicts)
	}
	accepted, err := skeleton.WithSemanticUnits(merged)
	if err != nil {
		return conflictResult("invalid_document", nil)
	}
	accepted, err = finalizeAcceptedDocument(accepted, nil, false)
	if err != nil {
		return conflictResult("invalid_document", nil)
	}
	rendered, err := accepted.Render()
	if err != nil {
		return conflictResult("invalid_document", nil)
	}
	accepted, err = syncdoc.Parse(target, rendered)
	if err != nil {
		return conflictResult("invalid_path", nil)
	}
	return acceptedResult(input, target, accepted)
}

func finalizeAcceptedDocument(selected syncdoc.Document, base *syncdoc.Document, changed bool) (syncdoc.Document, error) {
	var err error
	if base != nil {
		selected, err = selected.FinalizeHumanMerge(*base, changed)
		if err != nil {
			return syncdoc.Document{}, err
		}
	}
	if isCompactV2Document(selected) {
		return selected, nil
	}
	return selected.WithSyncStatus("synced")
}

func pathUnit(relative string) syncdoc.Unit {
	return syncdoc.Unit{Present: true, Value: []byte(relative)}
}

func validPathContext(input MergeInput) bool {
	if input.GOOS != "darwin" && input.GOOS != "linux" && input.GOOS != "windows" {
		return false
	}
	if input.CaseMode != platform.CaseSensitive && input.CaseMode != platform.CaseInsensitive {
		return false
	}
	return input.OccupiedPathKeys != nil
}

func validateCandidateClaim(candidate Candidate) error {
	if !candidate.Present || candidate.RelativePath == "" {
		return syncdoc.ErrInvalidPath
	}
	if err := validateMergePath(candidate.RelativePath); err != nil {
		return err
	}
	rendered, err := candidate.Document.Render()
	if err != nil {
		return err
	}
	computed := syncdoc.ContentHash(rendered)
	if !lowerSHA256.MatchString(candidate.Hash) || candidate.Hash != computed {
		return errCandidateHash
	}
	if candidate.Source != nil || candidate.SourceHash != "" {
		if candidate.Source == nil || !lowerSHA256.MatchString(candidate.SourceHash) || syncdoc.ContentHash(candidate.Source) != candidate.SourceHash {
			return errCandidateHash
		}
		sourceDocument, sourceErr := syncdoc.Parse(candidate.RelativePath, candidate.Source)
		if sourceErr != nil || documentHash(sourceDocument) != candidate.Hash {
			return errCandidateHash
		}
	}
	_, err = syncdoc.Parse(candidate.RelativePath, rendered)
	return err
}

func candidateClaimReason(err error) string {
	if errors.Is(err, errCandidateHash) {
		return "invalid_hash"
	}
	return "invalid_path"
}

func validateMergePath(relative string) error {
	if strings.Contains(relative, "\\") || !strings.HasSuffix(relative, ".md") {
		return syncdoc.ErrInvalidPath
	}
	for _, component := range strings.Split(relative, "/") {
		if strings.EqualFold(component, "sync-conflicts") {
			return syncdoc.ErrInvalidPath
		}
	}
	if _, err := platform.PathKey("windows", platform.CaseSensitive, relative); err != nil {
		return syncdoc.ErrInvalidPath
	}
	return nil
}

func validNewCandidate(document syncdoc.Document, entityID, projectID string) bool {
	identity, err := document.Identity()
	if err != nil || identity.ID != entityID || identity.ProjectID != projectID || !stableBaseID.MatchString(identity.ID) {
		return false
	}
	units := document.Units()
	if isCompactV2Identity(identity) {
		return validCompactV2Candidate(identity, units)
	}
	revision := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]
	status := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "sync_status"}]
	if !revision.Present || string(revision.Value) != "1\n" || !status.Present || string(status.Value) != "synced\n" || hasReservedHashUnit(units) {
		return false
	}
	if identity.EntityType == "project_overview" {
		return identity.ID == "project-overview" && validOverviewCandidate(units)
	}
	return validDocumentShape(document)
}

func validImportedProjectCandidate(document syncdoc.Document, entityID, projectID string) bool {
	identity, err := document.Identity()
	if err != nil || identity.ID != entityID || identity.ProjectID != projectID || !stableBaseID.MatchString(identity.ID) {
		return false
	}
	units := document.Units()
	if isCompactV2Identity(identity) {
		return validCompactV2Candidate(identity, units)
	}
	statusUnit := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "sync_status"}]
	status, statusValid := decodeStringUnit(units, "sync_status")
	if (statusUnit.Present && (!statusValid || status != "synced")) || hasReservedHashUnit(units) {
		return false
	}
	if identity.EntityType == "project_overview" {
		var revision int
		revisionUnit := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]
		return identity.ID == "project-overview" && revisionUnit.Present && yaml.Unmarshal(revisionUnit.Value, &revision) == nil && revision >= 1 && revision <= 1<<53-1 && validOverviewCandidate(units)
	}
	return validDocumentShapeWithStatus(document, false)
}

func validDocumentShape(document syncdoc.Document) bool {
	return validDocumentShapeWithStatus(document, true)
}

func validDocumentShapeWithStatus(document syncdoc.Document, requireStatus bool) bool {
	identity, err := document.Identity()
	if err != nil || !stableBaseID.MatchString(identity.ID) {
		return false
	}
	units := document.Units()
	if isCompactV2Identity(identity) {
		return validCompactV2Candidate(identity, units)
	}
	var revision int
	revisionUnit := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]
	status, statusOK := decodeStringUnit(units, "sync_status")
	if !revisionUnit.Present || yaml.Unmarshal(revisionUnit.Value, &revision) != nil || revision < 1 || revision > 1<<53-1 || (requireStatus && !statusOK) || (statusOK && status != "synced") || hasReservedHashUnit(units) {
		return false
	}
	if identity.EntityType == "project_overview" {
		return identity.ID == "project-overview" && validOverviewCandidate(units)
	}
	rendered, err := document.Render()
	if err != nil {
		return false
	}
	ledgerDocument, err := ledger.ParseDocument(rendered)
	if err != nil {
		return false
	}
	if _, err := ledgerDocument.Render(); err != nil {
		return false
	}
	return validLedgerCandidateDocument(identity, ledgerDocument, units)
}

func isCompactV2Identity(identity syncdoc.Identity) bool {
	return (identity.ID == "project-overview" && identity.EntityType == "project_review") ||
		(identity.ID == "project-history" && identity.EntityType == "project_history")
}

func isCompactV2Document(document syncdoc.Document) bool {
	identity, err := document.Identity()
	return err == nil && isCompactV2Identity(identity)
}

func compactV2Filename(document syncdoc.Document) (string, bool) {
	identity, err := document.Identity()
	if err != nil {
		return "", false
	}
	switch {
	case identity.ID == "project-overview" && identity.EntityType == "project_review":
		return "项目回顾.md", true
	case identity.ID == "project-history" && identity.EntityType == "project_history":
		return "项目历史.md", true
	default:
		return "", false
	}
}

func validCompactV2Candidate(identity syncdoc.Identity, units syncdoc.UnitSet) bool {
	if !isCompactV2Identity(identity) || hasReservedHashUnit(units) ||
		units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "sync_status"}].Present {
		return false
	}
	var schema, revision int
	if unit := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "schema_version"}]; !unit.Present || yaml.Unmarshal(unit.Value, &schema) != nil || schema != 2 {
		return false
	}
	if unit := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]; !unit.Present || yaml.Unmarshal(unit.Value, &revision) != nil || revision < 1 || revision > 1<<53-1 {
		return false
	}
	return true
}

func compactV2MarkerPresenceConflicts(document syncdoc.Document, base, project, vault syncdoc.UnitSet) []UnitConflict {
	if !isCompactV2Document(document) {
		return nil
	}
	conflicts := make([]UnitConflict, 0)
	for _, key := range sortedUnitKeys(base, project, vault) {
		if key.Kind != syncdoc.UnitSection || !strings.HasPrefix(key.Name, "session-reviewer/") {
			continue
		}
		baseUnit, projectUnit, vaultUnit := base[key], project[key], vault[key]
		if baseUnit.Present == projectUnit.Present && baseUnit.Present == vaultUnit.Present {
			continue
		}
		conflicts = append(conflicts, UnitConflict{
			Key: key, Base: cloneUnit(baseUnit), Project: cloneUnit(projectUnit), Vault: cloneUnit(vaultUnit),
		})
	}
	return conflicts
}

func hasReservedHashUnit(units syncdoc.UnitSet) bool {
	for _, name := range []string{"sync_hash", "base_hash", "project_hash", "vault_hash", "source_hash"} {
		if units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name}].Present {
			return true
		}
	}
	return false
}

func validOverviewCandidate(units syncdoc.UnitSet) bool {
	for _, name := range []string{"source_sessions", "evidence", "supersedes"} {
		if units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name}].Present {
			return false
		}
	}
	return true
}

func validLedgerCandidateDocument(identity syncdoc.Identity, document ledger.Document, units syncdoc.UnitSet) bool {
	switch identity.EntityType {
	case "decision":
		title, titleOK := decodeStringUnit(units, "title")
		status, statusOK := decodeStringUnit(units, "status")
		tags, tagsOK := decodeStringSequenceUnit(units, "tags")
		supersedes, supersedesOK := decodeStringSequenceUnit(units, "supersedes")
		sessions, sessionsOK := decodeStringSequenceUnit(units, "source_sessions")
		evidence, evidenceOK := decodeEvidenceUnit(units, "evidence")
		_, alternativesOK := ledgerListSection(document, "Alternatives")
		_, rejectedOK := ledgerListSection(document, "Rejected paths")
		return titleOK && strings.TrimSpace(title) != "" && statusOK && validDecisionStatus(status) &&
			tagsOK && uniqueStrings(tags, true, false) && supersedesOK && uniqueStrings(supersedes, true, true) &&
			sessionsOK && uniqueStrings(sessions, true, false) && evidenceOK && validEvidenceRefs(evidence) && alternativesOK && rejectedOK
	case "open_loop":
		title, titleOK := decodeStringUnit(units, "title")
		status, statusOK := decodeStringUnit(units, "status")
		tags, tagsOK := decodeStringSequenceUnit(units, "tags")
		sessions, sessionsOK := decodeStringSequenceUnit(units, "source_sessions")
		evidence, evidenceOK := decodeEvidenceUnit(units, "evidence")
		_, attemptsOK := ledgerListSection(document, "Attempted paths")
		return titleOK && strings.TrimSpace(title) != "" && statusOK && validOpenLoopStatus(status) &&
			tagsOK && uniqueStrings(tags, true, false) && sessionsOK && uniqueStrings(sessions, true, false) &&
			evidenceOK && validEvidenceRefs(evidence) && attemptsOK
	case "session":
		return validSessionCandidate(document, units)
	case "current_state":
		return identity.ID == "current-state" && validCurrentStateCandidate(document, units)
	case "timeline":
		return identity.ID == "evolution-timeline" && validTimelineCandidate(document)
	default:
		return false
	}
}

func validSessionCandidate(document ledger.Document, units syncdoc.UnitSet) bool {
	var sessionID, initialGoal, previousID, nextID string
	var goalChanges, files, commits, verification, decisionsAdded, decisionsRevised, loopsCreated, loopsClosed []string
	var phases []ledger.SessionPhase
	sessions, sessionsOK := decodeStringSequenceUnit(units, "source_sessions")
	evidence, evidenceOK := decodeEvidenceUnit(units, "evidence")
	fieldsOK := decodeLedgerField(document, "session_id", &sessionID) && decodeLedgerField(document, "initial_goal", &initialGoal) &&
		decodeLedgerField(document, "goal_changes", &goalChanges) && decodeLedgerField(document, "phases", &phases) &&
		decodeLedgerField(document, "files", &files) && decodeLedgerField(document, "commits", &commits) &&
		decodeLedgerField(document, "verification", &verification) && decodeLedgerField(document, "decisions_added", &decisionsAdded) &&
		decodeLedgerField(document, "decisions_revised", &decisionsRevised) && decodeLedgerField(document, "open_loops_created", &loopsCreated) &&
		decodeLedgerField(document, "open_loops_closed", &loopsClosed) && decodeLedgerField(document, "previous_session_id", &previousID) &&
		decodeLedgerField(document, "next_session_id", &nextID)
	if !fieldsOK || strings.TrimSpace(sessionID) == "" || !sessionsOK || len(sessions) != 1 || sessions[0] != sessionID || !evidenceOK || !validEvidenceRefs(evidence) {
		return false
	}
	for _, values := range [][]string{goalChanges, files, commits, verification} {
		if !uniqueStrings(values, false, false) {
			return false
		}
	}
	for _, values := range [][]string{decisionsAdded, decisionsRevised, loopsCreated, loopsClosed} {
		if !uniqueStrings(values, true, true) {
			return false
		}
	}
	for _, phase := range phases {
		if strings.TrimSpace(phase.Title) == "" || phase.Evidence == nil || !validEvidenceRefs(phase.Evidence) {
			return false
		}
	}
	return true
}

func validCurrentStateCandidate(document ledger.Document, units syncdoc.UnitSet) bool {
	sessions, sessionsOK := decodeStringSequenceUnit(units, "source_sessions")
	evidence, evidenceOK := decodeEvidenceUnit(units, "evidence")
	if !sessionsOK || !uniqueStrings(sessions, true, false) || !evidenceOK || !validEvidenceRefs(evidence) {
		return false
	}
	for _, section := range []string{"Uncommitted changes", "Blockers", "Open risks"} {
		if _, ok := ledgerListSection(document, section); !ok {
			return false
		}
	}
	return true
}

func validTimelineCandidate(document ledger.Document) bool {
	var events []timelineCandidateEvent
	if !decodeLedgerField(document, "events", &events) || events == nil {
		return false
	}
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		if (event.OccurredAt != "" && event.LegacyOccurredAt != "") || (event.DecisionIDs != nil && event.LegacyDecisionIDs != nil) || (event.OpenLoopIDs != nil && event.LegacyOpenLoopIDs != nil) {
			return false
		}
		if event.OccurredAt == "" {
			event.OccurredAt = event.LegacyOccurredAt
		}
		if event.DecisionIDs == nil {
			event.DecisionIDs = event.LegacyDecisionIDs
		}
		if event.OpenLoopIDs == nil {
			event.OpenLoopIDs = event.LegacyOpenLoopIDs
		}
		_, timeErr := time.Parse(time.RFC3339Nano, event.OccurredAt)
		if !stableBaseID.MatchString(event.ID) || event.Revision < 1 || event.Revision > 1<<53-1 || timeErr != nil ||
			!validFactClass(event.Class) || strings.TrimSpace(event.Title) == "" || event.Evidence == nil || !validEvidenceRefs(event.Evidence) ||
			event.DecisionIDs == nil || !uniqueStrings(event.DecisionIDs, true, true) || event.OpenLoopIDs == nil || !uniqueStrings(event.OpenLoopIDs, true, true) {
			return false
		}
		if _, duplicate := seen[event.ID]; duplicate {
			return false
		}
		seen[event.ID] = struct{}{}
	}
	return true
}

type timelineCandidateEvent struct {
	ID                string               `yaml:"id"`
	OccurredAt        string               `yaml:"occurred_at"`
	Revision          int                  `yaml:"revision"`
	Class             ledger.FactClass     `yaml:"class"`
	Title             string               `yaml:"title"`
	Summary           string               `yaml:"summary"`
	Evidence          []ledger.EvidenceRef `yaml:"evidence"`
	DecisionIDs       []string             `yaml:"decision_ids"`
	OpenLoopIDs       []string             `yaml:"open_loop_ids"`
	LegacyOccurredAt  string               `yaml:"occurredat"`
	LegacyDecisionIDs []string             `yaml:"decisionids"`
	LegacyOpenLoopIDs []string             `yaml:"openloopids"`
}

func decodeLedgerField(document ledger.Document, name string, target any) bool {
	for index := 0; index+1 < len(document.Frontmatter.Content); index += 2 {
		if document.Frontmatter.Content[index].Value != name {
			continue
		}
		node := document.Frontmatter.Content[index+1]
		return node != nil && node.Kind != yaml.AliasNode && node.Tag != "!!null" && node.Decode(target) == nil
	}
	return false
}

func ledgerListSection(document ledger.Document, name string) ([]string, bool) {
	for _, section := range document.Sections {
		if section.Name != name {
			continue
		}
		text := strings.Trim(section.Body, "\n")
		if text == "" {
			return []string{}, true
		}
		lines := strings.Split(text, "\n")
		marker := -1
		for index, line := range lines {
			if strings.HasPrefix(strings.TrimLeft(line, " \t"), "<!-- session-reviewer:list-codec") {
				if line != "<!-- session-reviewer:list-codec=v1 -->" || marker != -1 {
					return nil, false
				}
				marker = index
			}
		}
		if marker >= 0 {
			if marker != 0 {
				return nil, false
			}
			values := make([]string, 0, len(lines)-1)
			for _, line := range lines[1:] {
				if !strings.HasPrefix(line, "- sr-string: ") {
					return nil, false
				}
				value, err := strconv.Unquote(strings.TrimPrefix(line, "- sr-string: "))
				if err != nil {
					return nil, false
				}
				values = append(values, value)
			}
			return values, true
		}
		values := make([]string, 0)
		for _, line := range lines {
			line = strings.TrimLeft(line, " \t")
			if strings.HasPrefix(line, "- ") {
				values = append(values, strings.TrimPrefix(line, "- "))
			}
		}
		return values, true
	}
	return nil, false
}

func uniqueStrings(values []string, requireNonempty, requireStable bool) bool {
	if values == nil {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if (requireNonempty && strings.TrimSpace(value) == "") || (requireStable && !stableBaseID.MatchString(value)) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validFactClass(class ledger.FactClass) bool {
	switch class {
	case ledger.Verified, ledger.DecisionFact, ledger.Inference, ledger.Superseded, ledger.PendingConfirmation:
		return true
	default:
		return false
	}
}

func decodeStringUnit(units syncdoc.UnitSet, name string) (string, bool) {
	unit := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name}]
	if !unit.Present {
		return "", false
	}
	var value string
	if err := yaml.Unmarshal(unit.Value, &value); err != nil {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func decodeStringSequenceUnit(units syncdoc.UnitSet, name string) ([]string, bool) {
	unit := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name}]
	if !unit.Present {
		return nil, false
	}
	var values []string
	if err := yaml.Unmarshal(unit.Value, &values); err != nil || values == nil {
		return nil, false
	}
	return values, true
}

func decodeEvidenceUnit(units syncdoc.UnitSet, name string) ([]ledger.EvidenceRef, bool) {
	unit := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name}]
	if !unit.Present {
		return nil, false
	}
	var values []ledger.EvidenceRef
	if err := yaml.Unmarshal(unit.Value, &values); err != nil || values == nil {
		return nil, false
	}
	return values, true
}

func validEvidenceRefs(values []ledger.EvidenceRef) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !stableBaseID.MatchString(value.EvidenceID) || strings.TrimSpace(value.SessionID) == "" || value.JSONLLine < 1 || value.JSONLLine > 1<<53-1 || !lowerSHA256.MatchString(value.SourceHash) {
			return false
		}
		if _, duplicate := seen[value.EvidenceID]; duplicate {
			return false
		}
		seen[value.EvidenceID] = struct{}{}
	}
	return true
}

func validDecisionStatus(status string) bool {
	switch status {
	case "proposed", "accepted", "superseded", "archived":
		return true
	default:
		return false
	}
}

func validOpenLoopStatus(status string) bool {
	switch status {
	case "open", "blocked", "resolved", "abandoned", "archived":
		return true
	default:
		return false
	}
}

func reasonForHumanValidation(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, syncdoc.ErrReservedField):
		return "reserved_field"
	case errors.Is(err, syncdoc.ErrProtectedProvenance):
		return "protected_provenance"
	default:
		return "invalid_document"
	}
}

func mergeUnitSets(base, project, vault syncdoc.UnitSet) (syncdoc.UnitSet, []UnitConflict) {
	keys := sortedUnitKeys(base, project, vault)
	merged := make(syncdoc.UnitSet, len(keys))
	conflicts := make([]UnitConflict, 0)
	for _, key := range keys {
		baseUnit, projectUnit, vaultUnit := base[key], project[key], vault[key]
		unit, conflict := mergeUnit(baseUnit, projectUnit, vaultUnit)
		if conflict {
			conflicts = append(conflicts, UnitConflict{Key: key, Base: cloneUnit(baseUnit), Project: cloneUnit(projectUnit), Vault: cloneUnit(vaultUnit)})
			continue
		}
		if unit.Present {
			merged[key] = unit
		}
	}
	return merged, conflicts
}

func sortedUnitKeys(sets ...syncdoc.UnitSet) []syncdoc.UnitKey {
	seen := make(map[syncdoc.UnitKey]struct{})
	for _, set := range sets {
		for key := range set {
			seen[key] = struct{}{}
		}
	}
	keys := make([]syncdoc.UnitKey, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Kind == keys[j].Kind {
			return keys[i].Name < keys[j].Name
		}
		return keys[i].Kind < keys[j].Kind
	})
	return keys
}

func unitSetsEqual(first, second syncdoc.UnitSet) bool {
	for _, key := range sortedUnitKeys(first, second) {
		if !unitsEqual(first[key], second[key]) {
			return false
		}
	}
	return true
}

func acceptedResult(input MergeInput, target string, accepted syncdoc.Document) MergeResult {
	projectEqual := candidateEquals(input, input.Project, target, accepted)
	vaultEqual := candidateEquals(input, input.Vault, target, accepted)
	kind := MergeWriteBoth
	switch {
	case projectEqual && vaultEqual:
		kind = MergeNoop
	case projectEqual:
		kind = MergeWriteVault
	case vaultEqual:
		kind = MergeWriteProject
	}
	copy := accepted
	return MergeResult{Kind: kind, Accepted: &copy}
}

func candidateEquals(input MergeInput, candidate Candidate, target string, accepted syncdoc.Document) bool {
	if !candidate.Present || !candidatePathEquals(input, candidate.RelativePath, target) {
		return false
	}
	if !candidate.Document.SemanticEqual(accepted) {
		return false
	}
	if candidate.SourceHash == "" || !isCompactV2Document(accepted) {
		return true
	}
	return candidate.SourceHash == documentHash(accepted)
}

func candidatePathEquals(input MergeInput, candidatePath, target string) bool {
	if candidatePath == target {
		return true
	}
	if input.Base == nil || target != input.BasePath || !validPathContext(input) {
		return false
	}
	baseKey, baseErr := platform.PathKey(input.GOOS, input.CaseMode, input.BasePath)
	candidateKey, candidateErr := platform.PathKey(input.GOOS, input.CaseMode, candidatePath)
	return baseErr == nil && candidateErr == nil && baseKey == candidateKey
}

func conflictResult(reason string, conflicts []UnitConflict) MergeResult {
	return MergeResult{Kind: MergeConflict, Conflicts: conflicts, Reason: reason}
}

// mergeUnit performs a three-way merge of one complete document unit. The
// presentation bytes are part of the value and are never shared with callers.
func mergeUnit(base, project, vault syncdoc.Unit) (syncdoc.Unit, bool) {
	projectChanged := !unitsEqual(project, base)
	vaultChanged := !unitsEqual(vault, base)
	switch {
	case projectChanged && vaultChanged:
		if !unitsEqual(project, vault) {
			return syncdoc.Unit{}, true
		}
		return cloneUnit(project), false
	case projectChanged:
		return cloneUnit(project), false
	case vaultChanged:
		return cloneUnit(vault), false
	default:
		return cloneUnit(base), false
	}
}

func unitsEqual(first, second syncdoc.Unit) bool {
	return first.Present == second.Present &&
		bytes.Equal(first.Value, second.Value) &&
		bytes.Equal(first.KeyPresentation, second.KeyPresentation) &&
		bytes.Equal(first.HeadingPresentation, second.HeadingPresentation)
}

func cloneUnit(unit syncdoc.Unit) syncdoc.Unit {
	return syncdoc.Unit{
		Present:             unit.Present,
		Value:               bytes.Clone(unit.Value),
		KeyPresentation:     bytes.Clone(unit.KeyPresentation),
		HeadingPresentation: bytes.Clone(unit.HeadingPresentation),
	}
}
