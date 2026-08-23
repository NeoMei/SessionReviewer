package sync

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

var conflictTime = time.Date(2026, time.August, 23, 1, 2, 3, 456, time.FixedZone("test", 8*60*60))

func TestBuildConflictUsesDeterministicUTCIDAndExactHashes(t *testing.T) {
	t.Parallel()

	base := []byte("BASE")
	project := []byte("PROJECT")
	vault := []byte("VAULT")
	wantDigest := sha256.Sum256([]byte(syncdoc.ContentHash(base) + "|" + syncdoc.ContentHash(project) + "|" + syncdoc.ContentHash(vault)))
	wantID := fmt.Sprintf("conflict-20260822T170203Z-decision-1-%x", wantDigest[:6])

	first, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Base: base, Project: project, Vault: vault,
		Suggested: []byte("SUGGESTED"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Base: base, Project: project, Vault: vault,
		Suggested: []byte("SUGGESTED"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Record == nil || first.Repair != nil || second.Record == nil {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if first.Record.ID != wantID || first.Record.CreatedAt.Location() != time.UTC {
		t.Fatalf("id=%q created=%s want=%q UTC", first.Record.ID, first.Record.CreatedAt, wantID)
	}
	if first.Record.BaseHash != syncdoc.ContentHash(base) || first.Record.ProjectHash != syncdoc.ContentHash(project) || first.Record.VaultHash != syncdoc.ContentHash(vault) {
		t.Fatalf("record hashes=%+v", first.Record)
	}
	if !bytes.Equal(first.Notes.Project.Content, second.Notes.Project.Content) || first.Notes.Project.RelativePath != second.Notes.Project.RelativePath {
		t.Fatal("same input did not produce deterministic note bytes and path")
	}
}

func TestRenderConflictUsesDynamicFenceAndMirrorsExactBytes(t *testing.T) {
	t.Parallel()

	project := []byte("---\ntitle: candidate\n---\n\n`````\n# not a heading to the note\n`````\n")
	vault := []byte("<script>not markup</script>\n")
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictArchiveEdit,
		RelativePath: "decisions/decision-1.md", Base: nil, Project: project, Vault: vault,
		Suggested: []byte("suggested without final newline"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Record == nil {
		t.Fatalf("artifact=%+v", artifact)
	}
	note, err := RenderConflict(*artifact.Record)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(note, artifact.Notes.Project.Content) || !bytes.Equal(note, artifact.Notes.Vault.Content) || artifact.Notes.Project.RelativePath != artifact.Notes.Vault.RelativePath {
		t.Fatal("conflict notes are not byte-identical mirrors")
	}
	if artifact.Notes.Project.RelativePath != "sync-conflicts/"+artifact.Record.ID+".md" {
		t.Fatalf("path=%q", artifact.Notes.Project.RelativePath)
	}
	for _, candidate := range [][]byte{project, vault, artifact.Record.Suggested} {
		if !bytes.Contains(note, candidate) {
			t.Fatalf("candidate bytes not preserved: %q", candidate)
		}
	}
	if !bytes.Contains(note, []byte("`````` markdown\n")) {
		t.Fatalf("dynamic fence was not longer than candidate run:\n%s", note)
	}
	frontmatterEnd := bytes.Index(note[len("---\n"):], []byte("---\n"))
	if frontmatterEnd < 0 {
		t.Fatal("missing frontmatter close")
	}
	frontmatter := note[:len("---\n")+frontmatterEnd+len("---\n")]
	for _, forbidden := range [][]byte{project, vault, []byte("BasePath"), []byte("ProjectPath"), []byte("VaultPath")} {
		if bytes.Contains(frontmatter, forbidden) {
			t.Fatalf("unsafe frontmatter contains %q", forbidden)
		}
	}
	for _, want := range []string{"accept_project", "accept_obsidian", "manual_merge", artifact.Record.BaseHash, artifact.Record.ProjectHash, artifact.Record.VaultHash} {
		if !strings.Contains(string(frontmatter), want) {
			t.Fatalf("frontmatter missing %q:\n%s", want, frontmatter)
		}
	}
}

func TestBuildConflictReturnsDefensiveCopies(t *testing.T) {
	t.Parallel()

	project := []byte("PROJECT")
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: project, CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantNote := bytes.Clone(artifact.Notes.Vault.Content)
	project[0] = 'X'
	artifact.Record.Project[0] = 'Y'
	artifact.Notes.Project.Content[0] = 'Z'
	if !bytes.Equal(artifact.Notes.Vault.Content, wantNote) {
		t.Fatal("artifact aliases caller, record, or sibling note bytes")
	}
}

func TestBuildConflictConvertsEverySecretBearingCandidateToContentFreeRepair(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"base", "project", "vault", "suggested"} {
		field := field
		t.Run(field, func(t *testing.T) {
			canary := "api_key=sk-abcdefghijklmnopqrstuvwxyz012345"
			record := ConflictRecord{
				Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
				RelativePath: "decisions/decision-1.md", Base: []byte("safe-base"), Project: []byte("safe-project"), Vault: []byte("safe-vault"), Suggested: []byte("safe-suggested"), CreatedAt: conflictTime,
			}
			switch field {
			case "base":
				record.Base = []byte(canary)
			case "project":
				record.Project = []byte(canary)
			case "vault":
				record.Vault = []byte(canary)
			case "suggested":
				record.Suggested = []byte(canary)
			}
			artifact, err := BuildConflict(record)
			if err != nil {
				t.Fatalf("safe error expected, got %v", err)
			}
			if artifact.Record != nil || artifact.Repair == nil || artifact.Repair.IssueCode != syncdoc.IssueSensitive {
				t.Fatalf("artifact=%+v", artifact)
			}
			wantSide := SideProject
			if field == "vault" {
				wantSide = SideVault
			}
			if artifact.Repair.Side != wantSide {
				t.Fatalf("repair side=%q field=%q", artifact.Repair.Side, field)
			}
			visible := string(artifact.Notes.Project.Content) + string(artifact.Notes.Vault.Content) + artifact.Notes.Project.RelativePath + fmt.Sprintf("%+v", *artifact.Repair)
			if strings.Contains(visible, canary) || strings.Contains(visible, "REDACTED") {
				t.Fatalf("repair persisted candidate-derived text: %s", visible)
			}
			if !bytes.Equal(artifact.Notes.Project.Content, artifact.Notes.Vault.Content) || artifact.Notes.Project.RelativePath != artifact.Notes.Vault.RelativePath {
				t.Fatal("repair note was not mirrored identically")
			}
		})
	}
}

func TestBuildRepairNoteNeverPersistsRawPathOrSourceBytes(t *testing.T) {
	t.Parallel()

	canary := "MALFORMED-CONTENT-CANARY"
	absolute := "/Users/private/secret-project/decisions/bad.md"
	artifact, err := BuildRepair(RepairInput{
		CreatedAt: conflictTime, ProjectID: "project-1", EntityID: "", Side: SideVault,
		IssueCode: syncdoc.IssueMalformed, SourcePath: absolute, Source: []byte(canary),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := sha256.Sum256([]byte(absolute))
	wantID := fmt.Sprintf("repair-20260822T170203Z-%x", wantSuffix[:6])
	if artifact.Repair == nil || artifact.Record != nil || artifact.Repair.ID != wantID || artifact.Repair.SourceHash != syncdoc.ContentHash([]byte(canary)) {
		t.Fatalf("artifact=%+v", artifact)
	}
	visible := artifact.Notes.Project.RelativePath + string(artifact.Notes.Project.Content) + fmt.Sprintf("%+v", *artifact.Repair)
	if strings.Contains(visible, canary) || strings.Contains(visible, absolute) || strings.Contains(visible, "/Users/") {
		t.Fatalf("repair leaked source material: %s", visible)
	}
	for _, want := range []string{"malformed", "vault", syncdoc.ContentHash([]byte(canary)), "sync status", "manual_merge"} {
		if !strings.Contains(string(artifact.Notes.Project.Content), want) {
			t.Fatalf("repair note missing %q:\n%s", want, artifact.Notes.Project.Content)
		}
	}
}

func TestRenderConflictRejectsSensitiveRecordWithoutEcho(t *testing.T) {
	t.Parallel()

	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: []byte("safe"), CreatedAt: conflictTime,
	})
	if err != nil || artifact.Record == nil {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
	record := *artifact.Record
	canary := "password=secret-value-canary"
	record.Project = []byte(canary)
	record.ProjectHash = syncdoc.ContentHash(record.Project)
	digest := sha256.Sum256([]byte(record.BaseHash + "|" + record.ProjectHash + "|" + record.VaultHash))
	record.ID = fmt.Sprintf("conflict-%s-%s-%x", record.CreatedAt.Format("20060102T150405Z"), record.EntityID, digest[:6])
	_, err = RenderConflict(record)
	if !errors.Is(err, ErrSensitiveContent) || strings.Contains(err.Error(), canary) {
		t.Fatalf("err=%v", err)
	}
}

func TestSelectResolutionRendersLiveCandidateAndDoesNotTrustClaimedHash(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	projectDocument := editFrontmatterUnit(t, base, "title", "Project title\n")
	vaultDocument := editFrontmatterUnit(t, base, "tags", "[vault]\n")
	record := conflictRecordForDocuments(t, relative, &base, &projectDocument, &vaultDocument)

	liveProject := candidate(relative, projectDocument)
	liveProject.Hash = "not-the-live-hash"
	selected, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptProject}, liveProject, candidate(relative, vaultDocument), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := selected.Render()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := projectDocument.Render()
	if !bytes.Equal(got, want) {
		t.Fatalf("selected candidate changed:\ngot=%s\nwant=%s", got, want)
	}

	drifted := editFrontmatterUnit(t, projectDocument, "title", "Live drift\n")
	liveProject = candidate(relative, drifted)
	liveProject.Hash = record.ProjectHash
	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptProject}, liveProject, candidate(relative, vaultDocument), nil); !errors.Is(err, ErrStaleConflict) {
		t.Fatalf("stale err=%v", err)
	}
}

func TestSelectResolutionValidatesEmbeddedIdentityAndReservedChanges(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	for _, tc := range []struct {
		name, field, value string
		want               error
	}{
		{name: "entity-id", field: "id", value: "decision-other\n", want: syncdoc.ErrReservedField},
		{name: "project-id", field: "project_id", value: "project-other\n", want: syncdoc.ErrReservedField},
		{name: "entity-type", field: "entity_type", value: "open_loop\n", want: syncdoc.ErrReservedField},
		{name: "sync-status", field: "sync_status", value: "conflicted\n", want: syncdoc.ErrReservedField},
		{name: "revision", field: "revision", value: "99\n", want: syncdoc.ErrProtectedProvenance},
		{name: "source-sessions", field: "source_sessions", value: "[session-other]\n", want: syncdoc.ErrProtectedProvenance},
		{name: "evidence-source-hash", field: "evidence", value: "- evidence_id: evidence-1\n  session_id: session-1\n  jsonl_line: 7\n  source_hash: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n  summary: changed\n", want: syncdoc.ErrProtectedProvenance},
		{name: "supersedes", field: "supersedes", value: "[decision-other]\n", want: syncdoc.ErrProtectedProvenance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edited := editFrontmatterUnit(t, base, tc.field, tc.value)
			record := conflictRecordForDocuments(t, relative, &base, &edited, &base)
			_, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptProject}, candidate(relative, edited), candidate(relative, base), nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestSelectResolutionManualFailsClosedForNilPathIdentityDomainAndSecrets(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	projectDocument := editFrontmatterUnit(t, base, "title", "Project title\n")
	vaultDocument := editFrontmatterUnit(t, base, "tags", "[vault]\n")
	record := conflictRecordForDocuments(t, relative, &base, &projectDocument, &vaultDocument)
	liveProject, liveVault := candidate(relative, projectDocument), candidate(relative, vaultDocument)

	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "manual.md"}, liveProject, liveVault, nil); !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("nil manual err=%v", err)
	}
	manual := projectDocument
	for _, invalid := range []string{"", "  ", "bad\x00path"} {
		if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: invalid}, liveProject, liveVault, &manual); !errors.Is(err, ErrInvalidResolution) {
			t.Fatalf("manual path %q err=%v", invalid, err)
		}
	}

	wrongIdentity := editFrontmatterUnit(t, projectDocument, "project_id", "project-other\n")
	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "manual.md"}, liveProject, liveVault, &wrongIdentity); !errors.Is(err, syncdoc.ErrReservedField) {
		t.Fatalf("identity err=%v", err)
	}
	invalidDomain := editFrontmatterUnit(t, projectDocument, "status", "impossible\n")
	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "manual.md"}, liveProject, liveVault, &invalidDomain); !errors.Is(err, syncdoc.ErrInvalidDocument) {
		t.Fatalf("domain err=%v", err)
	}
	secret := editSectionUnit(t, projectDocument, sectionKeyBySuffix(t, projectDocument, " / Alternatives#1").Name, "api_key=sk-abcdefghijklmnopqrstuvwxyz012345\n")
	_, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "manual.md"}, liveProject, liveVault, &secret)
	if !errors.Is(err, ErrSensitiveContent) || strings.Contains(err.Error(), "abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("secret err=%v", err)
	}
}

func TestSelectResolutionManualReturnsValidatedDocument(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	vault := editFrontmatterUnit(t, base, "tags", "[vault]\n")
	manual := editFrontmatterUnit(t, base, "title", "Manual title\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &vault)
	selected, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "/already/safely/opened/manual.md"}, candidate(relative, project), candidate(relative, vault), &manual)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := selected.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rendered, []byte("title: Manual title")) || !bytes.Contains(rendered, []byte("sync_status: synced")) {
		t.Fatalf("selected=%s", rendered)
	}
}

func TestSelectResolutionRejectsMalformedDuplicateAndMissingCandidates(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	baseBytes, _ := base.Render()
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	projectBytes, _ := project.Render()
	vaultBytes := bytes.Clone(baseBytes)

	for _, tc := range []struct {
		name    string
		project []byte
	}{
		{name: "malformed", project: []byte("MALFORMED-CANARY")},
		{name: "duplicate-yaml", project: bytes.Replace(projectBytes, []byte("title: Project title\n"), []byte("title: Project title\ntitle: duplicate\n"), 1)},
		{name: "duplicate-section", project: append(bytes.Clone(projectBytes), []byte("\n## Alternatives\n\nduplicate\n")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact, err := BuildConflict(ConflictRecord{Version: 1, EntityID: "decision-sync", ProjectID: "project-1111111111111111", Kind: ConflictUnits, RelativePath: relative, BasePath: relative, ProjectPath: relative, VaultPath: relative, Base: baseBytes, Project: tc.project, Vault: vaultBytes, CreatedAt: conflictTime})
			if err != nil {
				t.Fatal(err)
			}
			liveDocument, parseErr := syncdoc.Parse(relative, tc.project)
			live := Candidate{}
			if parseErr == nil {
				live = candidate(relative, liveDocument)
			}
			_, selectErr := SelectResolution(*artifact.Record, Resolution{ConflictID: artifact.Record.ID, Action: AcceptProject}, live, candidate(relative, base), nil)
			if !errors.Is(selectErr, ErrInvalidConflict) || strings.Contains(selectErr.Error(), "CANARY") {
				t.Fatalf("err=%v", selectErr)
			}
		})
	}

	record := conflictRecordForDocuments(t, relative, &base, &project, &base)
	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptProject}, Candidate{}, candidate(relative, base), nil); !errors.Is(err, ErrStaleConflict) {
		t.Fatalf("missing candidate err=%v", err)
	}
	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ResolutionAction("unknown")}, candidate(relative, project), candidate(relative, base), nil); !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("invalid action err=%v", err)
	}
	if _, err := SelectResolution(record, Resolution{ConflictID: "wrong", Action: AcceptProject}, candidate(relative, project), candidate(relative, base), nil); !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("wrong conflict id err=%v", err)
	}
}

func TestSelectResolutionSupportsEmptyBaseNewEntity(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	newEntity := documentAtRevision(t, base, relative, 1)
	record := conflictRecordForDocuments(t, relative, nil, &newEntity, &newEntity)
	selected, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptObsidian}, candidate(relative, newEntity), candidate(relative, newEntity), nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := selected.Identity()
	if err != nil || identity.ID != "decision-sync" {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
}

func TestMarkConflictResolvedIsPureAndIdempotent(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &base)
	projectBytes, _ := project.Render()
	resolvedAt := conflictTime.Add(time.Hour)

	resolved, err := MarkConflictResolved(record, AcceptProject, syncdoc.ContentHash(projectBytes), resolvedAt)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ResolutionStatus != ResolutionResolved || resolved.ResolutionAction != AcceptProject || resolved.ResolvedHash != syncdoc.ContentHash(projectBytes) || !resolved.ResolvedAt.Equal(resolvedAt) || resolved.ResolvedAt.Location() != time.UTC {
		t.Fatalf("resolved=%+v", resolved)
	}
	if record.ResolutionStatus != ResolutionOpen || record.ResolutionAction != "" || !record.ResolvedAt.IsZero() {
		t.Fatalf("input record was mutated: %+v", record)
	}
	note, err := RenderConflict(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"resolution_status: \"resolved\"", "resolution_action: \"accept_project\"", resolved.ResolvedHash, resolvedAt.UTC().Format(time.RFC3339Nano)} {
		if !strings.Contains(string(note), want) {
			t.Fatalf("resolved note missing %q:\n%s", want, note)
		}
	}

	idempotent, err := MarkConflictResolved(resolved, AcceptProject, resolved.ResolvedHash, resolvedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !idempotent.ResolvedAt.Equal(resolved.ResolvedAt) {
		t.Fatalf("idempotent resolution rewrote timestamp: %s -> %s", resolved.ResolvedAt, idempotent.ResolvedAt)
	}
	if _, err := MarkConflictResolved(resolved, AcceptObsidian, resolved.ResolvedHash, resolvedAt); !errors.Is(err, ErrConflictResolved) {
		t.Fatalf("different action err=%v", err)
	}
	if _, err := MarkConflictResolved(resolved, AcceptProject, strings.Repeat("f", 64), resolvedAt); !errors.Is(err, ErrConflictResolved) {
		t.Fatalf("different hash err=%v", err)
	}
}

func TestSelectResolutionAlreadyResolvedAllowsOnlySameActionAndHash(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	vault := editFrontmatterUnit(t, base, "tags", "[vault]\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &vault)
	projectBytes, _ := project.Render()
	resolved, err := MarkConflictResolved(record, AcceptProject, syncdoc.ContentHash(projectBytes), conflictTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SelectResolution(resolved, Resolution{ConflictID: resolved.ID, Action: AcceptProject}, candidate(relative, project), candidate(relative, vault), nil); err != nil {
		t.Fatalf("same resolution was not idempotent: %v", err)
	}
	if _, err := SelectResolution(resolved, Resolution{ConflictID: resolved.ID, Action: AcceptObsidian}, candidate(relative, project), candidate(relative, vault), nil); !errors.Is(err, ErrConflictResolved) {
		t.Fatalf("different resolution err=%v", err)
	}
}

func TestBuildConflictConvertsIsolationKindsToContentFreeRepair(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind ConflictKind
		want syncdoc.IssueKind
	}{
		{kind: ConflictMalformed, want: syncdoc.IssueMalformed},
		{kind: ConflictCollision, want: syncdoc.IssuePathCollision},
		{kind: ConflictReserved, want: syncdoc.IssueReservedEdit},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			canary := "ISOLATED-BYTES-CANARY"
			artifact, err := BuildConflict(ConflictRecord{
				Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: tc.kind,
				RelativePath: "decisions/decision-1.md", Project: []byte(canary), CreatedAt: conflictTime,
			})
			if err != nil {
				t.Fatal(err)
			}
			if artifact.Record != nil || artifact.Repair == nil || artifact.Repair.IssueCode != tc.want {
				t.Fatalf("artifact=%+v", artifact)
			}
			if strings.Contains(string(artifact.Notes.Project.Content), canary) || strings.Contains(fmt.Sprintf("%+v", *artifact.Repair), canary) {
				t.Fatal("repair leaked isolated bytes")
			}
		})
	}
}

func TestConflictRecordTamperingAndUnsafePathsFailClosed(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &base)

	mutations := []struct {
		name   string
		mutate func(*ConflictRecord)
	}{
		{name: "id", mutate: func(value *ConflictRecord) { value.ID = "conflict-tampered" }},
		{name: "hash", mutate: func(value *ConflictRecord) { value.ProjectHash = strings.Repeat("f", 64) }},
		{name: "bytes", mutate: func(value *ConflictRecord) { value.Project = append(value.Project, 'x') }},
		{name: "non-utc", mutate: func(value *ConflictRecord) { value.CreatedAt = value.CreatedAt.In(time.FixedZone("local", 3600)) }},
		{name: "absolute-project-path", mutate: func(value *ConflictRecord) { value.ProjectPath = "/private/project.md" }},
		{name: "reserved-path", mutate: func(value *ConflictRecord) { value.VaultPath = "sync-conflicts/entity.md" }},
		{name: "invalid-resolution-state", mutate: func(value *ConflictRecord) { value.ResolutionAction = AcceptProject }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			tampered := cloneConflictRecord(record)
			tc.mutate(&tampered)
			if _, err := RenderConflict(tampered); !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("render err=%v", err)
			}
			_, err := SelectResolution(tampered, Resolution{ConflictID: tampered.ID, Action: AcceptProject}, candidate(relative, project), candidate(relative, base), nil)
			if !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("select err=%v", err)
			}
		})
	}
}

func TestBuildConflictRejectsPrepopulatedTamperFields(t *testing.T) {
	t.Parallel()

	for _, mutate := range []func(*ConflictRecord){
		func(record *ConflictRecord) { record.ID = "caller-id" },
		func(record *ConflictRecord) { record.BaseHash = strings.Repeat("f", 64) },
		func(record *ConflictRecord) { record.ProjectPath = "/absolute/project.md" },
		func(record *ConflictRecord) { record.ResolutionAction = AcceptProject },
		func(record *ConflictRecord) { record.ResolvedHash = strings.Repeat("f", 64) },
		func(record *ConflictRecord) { record.ResolvedAt = conflictTime },
	} {
		record := ConflictRecord{Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits, RelativePath: "decisions/decision-1.md", Project: []byte("PROJECT"), CreatedAt: conflictTime}
		mutate(&record)
		if _, err := BuildConflict(record); !errors.Is(err, ErrInvalidConflict) {
			t.Fatalf("err=%v record=%+v", err, record)
		}
	}
}

func TestSelectResolutionRejectsSecretEmbeddedRecordWithoutEcho(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &base)
	canary := "api_key=sk-abcdefghijklmnopqrstuvwxyz012345"
	record.Suggested = []byte(canary)
	_, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptProject}, candidate(relative, project), candidate(relative, base), nil)
	if !errors.Is(err, ErrSensitiveContent) || strings.Contains(err.Error(), canary) {
		t.Fatalf("err=%v", err)
	}
}

func conflictRecordForDocuments(t *testing.T, relative string, base, project, vault *syncdoc.Document) ConflictRecord {
	t.Helper()
	render := func(document *syncdoc.Document) []byte {
		if document == nil {
			return nil
		}
		content, err := document.Render()
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-sync", ProjectID: "project-1111111111111111", Kind: ConflictUnits,
		RelativePath: relative, BasePath: relative, ProjectPath: relative, VaultPath: relative,
		Base: render(base), Project: render(project), Vault: render(vault), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Record == nil {
		t.Fatalf("unexpected repair: %+v", artifact.Repair)
	}
	return *artifact.Record
}
