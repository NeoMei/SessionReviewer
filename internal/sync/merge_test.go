package sync

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

func TestMergeGeneratedOnlyDifferencesAreNoopWithoutRevisionIncrement(t *testing.T) {
	const (
		relative  = "decisions/decision-sync.md"
		projectID = "project-1111111111111111"
	)
	base := fixtureDocument(t, "base-decision.md", relative)
	units := base.Units()
	units[syncdoc.UnitKey{Kind: syncdoc.UnitSection, Name: "Base decision@1#1 / 快速理解#1"}] = syncdoc.Unit{
		Present: true, Value: []byte(ledger.GeneratedMarkerPrefix + "快速理解 -->\n\ncanonical\n"), HeadingPresentation: []byte("## 快速理解\n"),
	}
	var err error
	base, err = base.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	project := editSectionUnit(t, base, "Base decision@1#1 / 快速理解#1", ledger.GeneratedMarkerPrefix+"快速理解 -->\n\nproject edit\n")
	vault := editSectionUnit(t, base, "Base decision@1#1 / 快速理解#1", ledger.GeneratedMarkerPrefix+"快速理解 -->\n\nvault edit\n")
	got := Merge(MergeInput{
		EntityID: "decision-sync", ProjectID: projectID, BasePath: relative,
		Base: &base, Project: candidate(relative, project), Vault: candidate(relative, vault),
		GOOS: "windows", CaseMode: platform.CaseInsensitive,
	})
	if got.Kind != MergeNoop || got.Accepted == nil {
		t.Fatalf("merge=%+v", got)
	}
	revision := got.Accepted.Units()[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]
	if string(revision.Value) != "3\n" {
		t.Fatalf("revision=%q", revision.Value)
	}
}

func TestMergeUnitMatrix(t *testing.T) {
	t.Parallel()

	base := presentedUnit("base", "base-key", "base-heading")
	project := presentedUnit("project", "project-key", "project-heading")
	vault := presentedUnit("vault", "vault-key", "vault-heading")
	same := presentedUnit("same", "same-key", "same-heading")
	cases := []struct {
		name                 string
		base, project, vault syncdoc.Unit
		conflict             bool
		want                 syncdoc.Unit
	}{
		{"unchanged", base, base, base, false, base},
		{"project-only", base, project, base, false, project},
		{"vault-only", base, base, vault, false, vault},
		{"same-change", base, same, same, false, same},
		{"different-change", base, project, vault, true, syncdoc.Unit{}},
		{"project-delete", base, absentUnit(), base, false, absentUnit()},
		{"vault-delete", base, base, absentUnit(), false, absentUnit()},
		{"delete-vs-edit", base, absentUnit(), vault, true, syncdoc.Unit{}},
		{"both-delete", base, absentUnit(), absentUnit(), false, absentUnit()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, conflict := mergeUnit(tc.base, tc.project, tc.vault)
			if conflict != tc.conflict || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%+v conflict=%v want=%+v conflict=%v", got, conflict, tc.want, tc.conflict)
			}
			if conflict || !got.Present {
				return
			}
			got.Value[0] ^= 0xff
			got.KeyPresentation[0] ^= 0xff
			got.HeadingPresentation[0] ^= 0xff
			if bytes.Equal(got.Value, tc.want.Value) || bytes.Equal(got.KeyPresentation, tc.want.KeyPresentation) || bytes.Equal(got.HeadingPresentation, tc.want.HeadingPresentation) {
				t.Fatal("mergeUnit returned aliased presentation bytes")
			}
		})
	}
}

func TestMergeUnitComparesAllPresentationBytes(t *testing.T) {
	t.Parallel()

	base := presentedUnit("value", "key", "heading")
	for _, tc := range []struct {
		name           string
		project, vault syncdoc.Unit
	}{
		{"key-presentation", presentedUnit("value", "project-key", "heading"), presentedUnit("value", "vault-key", "heading")},
		{"heading-presentation", presentedUnit("value", "key", "project-heading"), presentedUnit("value", "key", "vault-heading")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, conflict := mergeUnit(base, tc.project, tc.vault); !conflict {
				t.Fatal("mergeUnit ignored presentation-only conflicting edits")
			}
		})
	}
}

func TestMergeDocumentPresenceMatrix(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	projectChanged := editFrontmatterUnit(t, base, "title", "Project title\n")
	vaultChanged := editFrontmatterUnit(t, base, "tags", "[sync, vault]\n")
	newBase := documentAtRevision(t, base, relative, 1)
	newProjectUnique := editFrontmatterUnit(t, newBase, "project_only", "keep-project\n")
	newVaultUnique := editFrontmatterUnit(t, newBase, "vault_only", "keep-vault\n")

	cases := []struct {
		name              string
		base              *syncdoc.Document
		project, vault    Candidate
		wantKind          MergeKind
		wantRevision      string
		wantProjectUnique bool
		wantVaultUnique   bool
	}{
		{"first-project-only", nil, candidate(relative, newBase), Candidate{}, MergeWriteVault, "1\n", false, false},
		{"first-vault-only", nil, Candidate{}, candidate(relative, newBase), MergeWriteProject, "1\n", false, false},
		{"first-byte-equivalent", nil, candidate(relative, newBase), candidate(relative, newBase), MergeNoop, "1\n", false, false},
		{"first-different-disjoint", nil, candidate(relative, newProjectUnique), candidate(relative, newVaultUnique), MergeWriteBoth, "1\n", true, true},
		{"missing-project-unchanged-vault", &base, Candidate{}, candidate(relative, base), MergeWriteProject, "3\n", false, false},
		{"unchanged-project-missing-vault", &base, candidate(relative, base), Candidate{}, MergeWriteVault, "3\n", false, false},
		{"missing-project-modified-vault", &base, Candidate{}, candidate(relative, vaultChanged), MergeWriteBoth, "4\n", false, false},
		{"modified-project-missing-vault", &base, candidate(relative, projectChanged), Candidate{}, MergeWriteBoth, "4\n", false, false},
		{"both-missing", &base, Candidate{}, Candidate{}, MergeWriteBoth, "3\n", false, false},
		{"project-changed-vault-unchanged", &base, candidate(relative, projectChanged), candidate(relative, base), MergeWriteBoth, "4\n", false, false},
		{"project-unchanged-vault-changed", &base, candidate(relative, base), candidate(relative, vaultChanged), MergeWriteBoth, "4\n", false, false},
		{"both-changed-disjoint", &base, candidate(relative, projectChanged), candidate(relative, vaultChanged), MergeWriteBoth, "4\n", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := MergeInput{
				EntityID:  "decision-sync",
				ProjectID: "project-1111111111111111",
				BasePath:  relative,
				Base:      tc.base,
				Project:   tc.project,
				Vault:     tc.vault,
			}
			if tc.base == nil {
				input.GOOS = "darwin"
				input.CaseMode = platform.CaseSensitive
				input.OccupiedPathKeys = map[string]string{}
			}
			got := Merge(input)
			if got.Kind != tc.wantKind || got.Reason != "" || len(got.Conflicts) != 0 || got.Accepted == nil {
				t.Fatalf("got kind=%q reason=%q conflicts=%+v accepted=%v", got.Kind, got.Reason, got.Conflicts, got.Accepted != nil)
			}
			units := got.Accepted.Units()
			if revision := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]; string(revision.Value) != tc.wantRevision {
				t.Fatalf("revision=%q want=%q", revision.Value, tc.wantRevision)
			}
			status := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "sync_status"}]
			if string(status.Value) != "synced\n" {
				t.Fatalf("sync_status=%q", status.Value)
			}
			for name, want := range map[string]bool{"project_only": tc.wantProjectUnique, "vault_only": tc.wantVaultUnique} {
				unit := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name}]
				if unit.Present != want {
					t.Fatalf("unit %s present=%v want=%v", name, unit.Present, want)
				}
			}
			rendered, err := got.Accepted.Render()
			if err != nil {
				t.Fatal(err)
			}
			if syncdoc.ContentHash(rendered) != strings.ToLower(syncdoc.ContentHash(rendered)) {
				t.Fatal("accepted hash is not lowercase SHA-256")
			}
			if _, err := syncdoc.Parse(relative, rendered); err != nil {
				t.Fatalf("accepted document does not reparse: %v", err)
			}
		})
	}
}

func TestMergeRejectsReservedAndProtectedHumanChanges(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	tests := []struct {
		name, field, value, reason string
	}{
		{"reserved-sync-status", "sync_status", "canary-reserved\n", "reserved_field"},
		{"protected-revision", "revision", "99\n", "protected_provenance"},
		{"protected-source-sessions", "source_sessions", "[canary-session]\n", "protected_provenance"},
		{"protected-evidence-nested-source-hash", "evidence", "- evidence_id: evidence-1\n  session_id: session-1\n  jsonl_line: 7\n  source_hash: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n  summary: canary-evidence\n", "protected_provenance"},
		{"protected-supersedes", "supersedes", "[canary-decision]\n", "protected_provenance"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			edited := editFrontmatterUnit(t, base, tc.field, tc.value)
			got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", BasePath: relative, Base: &base, Project: candidate(relative, edited), Vault: candidate(relative, base)})
			if got.Kind != MergeConflict || got.Reason != tc.reason || got.Accepted != nil {
				t.Fatalf("got=%+v", got)
			}
			if strings.Contains(got.Reason, "canary") {
				t.Fatalf("reason leaked edited value: %q", got.Reason)
			}
		})
	}
}

func TestMergeRejectsExistingDecisionWithInvalidDomainShape(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	tests := []struct {
		name    string
		project syncdoc.Document
	}{
		{"invalid-status", editFrontmatterUnit(t, base, "status", "not-a-status\n")},
		{"missing-alternatives", removeSectionUnit(t, base, " / Alternatives#1")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", BasePath: relative, Base: &base, Project: candidate(relative, tc.project), Vault: candidate(relative, base)})
			if got.Kind != MergeConflict || got.Reason != "invalid_document" || got.Accepted != nil {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestMergeValidatesExistingAndFinalShapesWithoutRequiringRevisionOne(t *testing.T) {
	t.Parallel()

	documents := existingShapeDocuments(t)
	for name, item := range documents {
		t.Run(name, func(t *testing.T) {
			base := item.document
			got := Merge(MergeInput{EntityID: item.id, ProjectID: "project-1111111111111111", BasePath: item.path, Base: &base, Project: candidate(item.path, base), Vault: candidate(item.path, base)})
			if got.Kind != MergeNoop || got.Reason != "" || got.Accepted == nil {
				t.Fatalf("got=%+v", got)
			}
			revision := got.Accepted.Units()[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]
			if string(revision.Value) != "7\n" {
				t.Fatalf("revision=%q", revision.Value)
			}
		})
	}
}

func TestMergeRejectsInvalidExistingShapeForEveryLedgerClass(t *testing.T) {
	t.Parallel()

	documents := existingShapeDocuments(t)
	tests := []struct {
		name string
		item existingShapeDocument
		edit func(*testing.T, syncdoc.Document) syncdoc.Document
	}{
		{"decision", documents["decision"], func(t *testing.T, document syncdoc.Document) syncdoc.Document {
			return editFrontmatterUnit(t, document, "status", "invalid\n")
		}},
		{"open-loop", documents["open-loop"], func(t *testing.T, document syncdoc.Document) syncdoc.Document {
			return removeSectionUnit(t, document, " / Attempted paths#1")
		}},
		{"session", documents["session"], func(t *testing.T, document syncdoc.Document) syncdoc.Document {
			return removeFrontmatterUnit(t, document, "goal_changes")
		}},
		{"current-state", documents["current-state"], func(t *testing.T, document syncdoc.Document) syncdoc.Document {
			return removeSectionUnit(t, document, " / Blockers#1")
		}},
		{"timeline", documents["timeline"], func(t *testing.T, document syncdoc.Document) syncdoc.Document {
			return editFrontmatterUnit(t, document, "events", "- id: event-1\n  occurred_at: invalid-time\n  revision: 1\n  class: verified\n  title: Event\n  summary: Summary\n  evidence: []\n  decision_ids: []\n  open_loop_ids: []\n")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := tc.item.document
			edited := tc.edit(t, base)
			got := Merge(MergeInput{EntityID: tc.item.id, ProjectID: "project-1111111111111111", BasePath: tc.item.path, Base: &base, Project: candidate(tc.item.path, edited), Vault: candidate(tc.item.path, base)})
			if got.Kind != MergeConflict || got.Reason != "invalid_document" || got.Accepted != nil {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestMergeRejectsMalformedExistingReservedEnvelope(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	original := fixtureDocument(t, "base-decision.md", relative)
	tests := []struct {
		name string
		base syncdoc.Document
	}{
		{"missing-revision", removeFrontmatterUnit(t, original, "revision")},
		{"missing-sync-status", removeFrontmatterUnit(t, original, "sync_status")},
		{"entity-hash-field", editFrontmatterUnit(t, original, "sync_hash", strings.Repeat("a", 64)+"\n")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := tc.base
			got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", BasePath: relative, Base: &base, Project: candidate(relative, base), Vault: candidate(relative, base)})
			if got.Kind != MergeConflict || got.Reason != "invalid_document" || got.Accepted != nil {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestMergeReportsEveryExactConflictDeterministically(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	contextKey := sectionKeyBySuffix(t, base, " / Context#1")
	project := editFrontmatterUnit(t, base, "title", "canary-project-title\n")
	project = editSectionUnit(t, project, contextKey.Name, "\ncanary-project-context.\n")
	vault := editFrontmatterUnit(t, base, "title", "canary-vault-title\n")
	vault = editSectionUnit(t, vault, contextKey.Name, "\ncanary-vault-context.\n")
	wantKeys := []syncdoc.UnitKey{{Kind: syncdoc.UnitFrontmatter, Name: "title"}, contextKey}

	var first []UnitConflict
	for run := 0; run < 100; run++ {
		got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", BasePath: relative, Base: &base, Project: candidate(relative, project), Vault: candidate(relative, vault)})
		if got.Kind != MergeConflict || got.Reason != "unit_conflict" || got.Accepted != nil || len(got.Conflicts) != len(wantKeys) {
			t.Fatalf("run=%d got=%+v", run, got)
		}
		if strings.Contains(got.Reason, "canary") {
			t.Fatalf("reason leaked a conflicting value: %q", got.Reason)
		}
		for index, wantKey := range wantKeys {
			conflict := got.Conflicts[index]
			if conflict.Key != wantKey || !reflect.DeepEqual(conflict.Base, base.Units()[wantKey]) || !reflect.DeepEqual(conflict.Project, project.Units()[wantKey]) || !reflect.DeepEqual(conflict.Vault, vault.Units()[wantKey]) {
				t.Fatalf("run=%d conflict[%d]=%+v", run, index, conflict)
			}
		}
		if run == 0 {
			first = got.Conflicts
		} else if !reflect.DeepEqual(got.Conflicts, first) {
			t.Fatalf("run=%d conflicts are nondeterministic", run)
		}
	}
	first[0].Project.Value[0] ^= 0xff
	if reflect.DeepEqual(first[0].Project, project.Units()[first[0].Key]) {
		t.Fatal("test mutation did not change conflict")
	}
	if !bytes.Contains(project.Units()[first[0].Key].Value, []byte("canary-project")) {
		t.Fatal("conflict result aliased the candidate document")
	}
}

func TestMergeValidatesNewEntitiesAndProposalProvenance(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	valid := documentAtRevision(t, base, relative, 1)
	tests := []struct {
		name      string
		entityID  string
		projectID string
		document  syncdoc.Document
	}{
		{"unstable-entity-id", "../decision", "project-1111111111111111", valid},
		{"entity-id-mismatch", "other-decision", "project-1111111111111111", valid},
		{"project-id-mismatch", "decision-sync", "project-2222222222222222", valid},
		{"unknown-entity-type", "decision-sync", "project-1111111111111111", editFrontmatterUnit(t, valid, "entity_type", "unknown\n")},
		{"invalid-status", "decision-sync", "project-1111111111111111", editFrontmatterUnit(t, valid, "status", "not-a-status\n")},
		{"duplicate-source-sessions", "decision-sync", "project-1111111111111111", editFrontmatterUnit(t, valid, "source_sessions", "[session-1, session-1]\n")},
		{"invalid-nested-source-hash", "decision-sync", "project-1111111111111111", editFrontmatterUnit(t, valid, "evidence", "- evidence_id: evidence-1\n  session_id: session-1\n  jsonl_line: 7\n  source_hash: CANARY\n  summary: invalid\n")},
		{"invalid-supersedes", "decision-sync", "project-1111111111111111", editFrontmatterUnit(t, valid, "supersedes", "[../decision]\n")},
		{"reserved-hash-in-frontmatter", "decision-sync", "project-1111111111111111", editFrontmatterUnit(t, valid, "sync_hash", strings.Repeat("a", 64)+"\n")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(MergeInput{EntityID: tc.entityID, ProjectID: tc.projectID, Project: candidate(relative, tc.document), GOOS: "darwin", CaseMode: platform.CaseSensitive, OccupiedPathKeys: map[string]string{}})
			if got.Kind != MergeConflict || got.Reason != "invalid_new_entity" || got.Accepted != nil {
				t.Fatalf("got=%+v", got)
			}
			if strings.Contains(got.Reason, "CANARY") {
				t.Fatalf("reason leaked invalid provenance: %q", got.Reason)
			}
		})
	}
}

func TestMergeFirstSyncImportsMatureProjectLedgerWithoutSyncMetadata(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	mature := documentAtRevision(t, base, relative, 6)
	mature = removeFrontmatterUnit(t, mature, "sync_status")
	got := Merge(MergeInput{
		EntityID: "decision-sync", ProjectID: "project-1111111111111111",
		Project: candidate(relative, mature), GOOS: "darwin", CaseMode: platform.CaseSensitive,
		OccupiedPathKeys: map[string]string{},
	})
	if got.Kind != MergeWriteBoth || got.Reason != "" || got.Accepted == nil {
		t.Fatalf("got=%+v", got)
	}
	units := got.Accepted.Units()
	if revision := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]; !revision.Present || string(revision.Value) != "6\n" {
		t.Fatalf("accepted revision=%+v", revision)
	}
	if status := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "sync_status"}]; !status.Present || string(status.Value) != "synced\n" {
		t.Fatalf("accepted sync_status=%+v", status)
	}
}

func TestMergeFirstSyncRejectsMalformedOptionalProjectSyncStatus(t *testing.T) {
	t.Parallel()
	const relative = "decisions/decision-sync.md"
	document := documentAtRevision(t, fixtureDocument(t, "base-decision.md", relative), relative, 6)
	document = editFrontmatterUnit(t, document, "sync_status", "[synced]\n")
	got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", Project: candidate(relative, document), GOOS: "darwin", CaseMode: platform.CaseSensitive, OccupiedPathKeys: map[string]string{}})
	if got.Kind != MergeConflict || got.Reason != "invalid_new_entity" {
		t.Fatalf("got=%+v", got)
	}
}

func TestMergeAcceptsNewProjectOverviewWithLocalProvenanceRule(t *testing.T) {
	t.Parallel()

	const content = "---\nid: project-overview\nentity_type: project_overview\nproject_id: project-1111111111111111\nrevision: 1\nsync_status: synced\n---\n\n# Project\n"
	document, err := syncdoc.Parse("project-overview.md", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	got := Merge(MergeInput{EntityID: "project-overview", ProjectID: "project-1111111111111111", Project: candidate("project-overview.md", document), GOOS: "darwin", CaseMode: platform.CaseSensitive, OccupiedPathKeys: map[string]string{}})
	if got.Kind != MergeWriteVault || got.Reason != "" || got.Accepted == nil {
		t.Fatalf("got=%+v", got)
	}
}

func TestMergeArchivePolicyMatrix(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	archive := editFrontmatterUnit(t, base, "status", "archived\n")
	superseded := editFrontmatterUnit(t, base, "status", "superseded\n")
	projectArchiveAndTitle := editFrontmatterUnit(t, archive, "title", "Project archived title\n")
	vaultArchiveAndTags := editFrontmatterUnit(t, archive, "tags", "[sync, vault]\n")
	modified := editFrontmatterUnit(t, base, "title", "Other live edit\n")
	tests := []struct {
		name            string
		project, vault  Candidate
		wantKind        MergeKind
		wantReason      string
		wantArchived    bool
		wantTitle, tags string
	}{
		{"project-archive-vault-unchanged", candidate(relative, archive), candidate(relative, base), MergeWriteBoth, "", true, "", ""},
		{"project-unchanged-vault-archive", candidate(relative, base), candidate(relative, archive), MergeWriteBoth, "", true, "", ""},
		{"project-archive-vault-modify", candidate(relative, archive), candidate(relative, modified), MergeConflict, "archive_vs_modify", false, "", ""},
		{"project-modify-vault-archive", candidate(relative, modified), candidate(relative, archive), MergeConflict, "archive_vs_modify", false, "", ""},
		{"project-archive-vault-other-status", candidate(relative, archive), candidate(relative, superseded), MergeConflict, "archive_vs_modify", false, "", ""},
		{"both-archive-merge-other-units", candidate(relative, projectArchiveAndTitle), candidate(relative, vaultArchiveAndTags), MergeWriteBoth, "", true, "Project archived title", "sync, vault"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", BasePath: relative, Base: &base, Project: tc.project, Vault: tc.vault})
			if got.Kind != tc.wantKind || got.Reason != tc.wantReason {
				t.Fatalf("kind=%q reason=%q conflicts=%+v", got.Kind, got.Reason, got.Conflicts)
			}
			if got.Kind == MergeConflict {
				if got.Accepted != nil || strings.Contains(got.Reason, "Other live edit") {
					t.Fatalf("conflict leaked value or accepted a document: %+v", got)
				}
				return
			}
			units := got.Accepted.Units()
			status, _ := decodeTestStringUnit(units, "status")
			if (status == "archived") != tc.wantArchived {
				t.Fatalf("status=%q", status)
			}
			if tc.wantTitle != "" {
				title, _ := decodeTestStringUnit(units, "title")
				if title != tc.wantTitle {
					t.Fatalf("title=%q", title)
				}
			}
			if tc.tags != "" {
				if !bytes.Contains(units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "tags"}].Value, []byte(tc.tags)) {
					t.Fatalf("tags=%q", units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "tags"}].Value)
				}
			}
		})
	}
}

func TestMergeRelativePathUnitMatrix(t *testing.T) {
	t.Parallel()

	const basePath = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", basePath)
	contentEdit := editFrontmatterUnit(t, base, "title", "Vault content edit\n")
	casePath := "Decisions/DECISION-SYNC.md"
	nfcPath := "decisions/Cafe\u0301.md"
	nfcBasePath := "decisions/Café.md"
	nfcBase := reparseDocumentAtPath(t, base, nfcBasePath)
	tests := []struct {
		name           string
		base           syncdoc.Document
		basePath       string
		project, vault Candidate
		goos           string
		caseMode       platform.CaseMode
		wantKind       MergeKind
		wantReason     string
		wantPath       string
		wantRevision   string
	}{
		{"project-one-sided-rename", base, basePath, candidateAtPath(t, "decisions/renamed.md", base), candidate(basePath, base), "darwin", platform.CaseSensitive, MergeWriteBoth, "", "decisions/renamed.md", "4\n"},
		{"vault-one-sided-rename", base, basePath, candidate(basePath, base), candidateAtPath(t, "decisions/renamed.md", base), "darwin", platform.CaseSensitive, MergeWriteBoth, "", "decisions/renamed.md", "4\n"},
		{"rename-plus-other-side-content", base, basePath, candidateAtPath(t, "decisions/renamed.md", base), candidate(basePath, contentEdit), "darwin", platform.CaseSensitive, MergeWriteBoth, "", "decisions/renamed.md", "4\n"},
		{"identical-two-sided-rename", base, basePath, candidateAtPath(t, "decisions/renamed.md", base), candidateAtPath(t, "decisions/renamed.md", base), "darwin", platform.CaseSensitive, MergeWriteBoth, "", "decisions/renamed.md", "4\n"},
		{"different-two-sided-renames", base, basePath, candidateAtPath(t, "decisions/project.md", base), candidateAtPath(t, "decisions/vault.md", base), "darwin", platform.CaseSensitive, MergeConflict, "path_conflict", "", ""},
		{"insensitive-case-only-keeps-base", base, basePath, candidateAtPath(t, casePath, base), candidate(basePath, base), "darwin", platform.CaseInsensitive, MergeNoop, "", basePath, "3\n"},
		{"sensitive-case-change-is-rename", base, basePath, candidateAtPath(t, casePath, base), candidate(basePath, base), "darwin", platform.CaseSensitive, MergeWriteBoth, "", casePath, "4\n"},
		{"windows-case-fold-keeps-base", base, basePath, candidateAtPath(t, casePath, base), candidate(basePath, base), "windows", platform.CaseSensitive, MergeNoop, "", basePath, "3\n"},
		{"insensitive-nfc-only-keeps-base", nfcBase, nfcBasePath, candidateAtPath(t, nfcPath, nfcBase), candidate(nfcBasePath, nfcBase), "darwin", platform.CaseInsensitive, MergeNoop, "", nfcBasePath, "3\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(MergeInput{
				EntityID:         "decision-sync",
				ProjectID:        "project-1111111111111111",
				BasePath:         tc.basePath,
				Base:             &tc.base,
				Project:          tc.project,
				Vault:            tc.vault,
				GOOS:             tc.goos,
				CaseMode:         tc.caseMode,
				OccupiedPathKeys: map[string]string{},
			})
			if got.Kind != tc.wantKind || got.Reason != tc.wantReason {
				t.Fatalf("kind=%q reason=%q conflicts=%+v", got.Kind, got.Reason, got.Conflicts)
			}
			if got.Kind == MergeConflict {
				if len(got.Conflicts) != 1 || got.Conflicts[0].Key != (syncdoc.UnitKey{Kind: syncdoc.UnitKind("path"), Name: "relative_path"}) || strings.Contains(got.Reason, "project.md") || strings.Contains(got.Reason, "vault.md") {
					t.Fatalf("path conflicts=%+v reason=%q", got.Conflicts, got.Reason)
				}
				return
			}
			rendered, err := got.Accepted.Render()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := syncdoc.Parse(tc.wantPath, rendered); err != nil {
				t.Fatalf("accepted target path invalid: %v", err)
			}
			revision := got.Accepted.Units()[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]
			if string(revision.Value) != tc.wantRevision {
				t.Fatalf("revision=%q want=%q", revision.Value, tc.wantRevision)
			}
			if tc.wantKind == MergeNoop {
				baseBytes, _ := tc.base.Render()
				if !bytes.Equal(rendered, baseBytes) {
					t.Fatal("identity-equivalent spelling manufactured a byte diff")
				}
			}
		})
	}
}

func TestMergeRejectsInvalidOrCollidingRenameTargetsAndMissingContext(t *testing.T) {
	t.Parallel()

	const basePath = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", basePath)
	tests := []struct {
		name, target, reason string
		context              bool
		occupied             map[string]string
	}{
		{"missing-mapping-context", "decisions/renamed.md", "invalid_input", false, nil},
		{"absolute", "/decisions/renamed.md", "invalid_path", true, map[string]string{}},
		{"backslash", `decisions\renamed.md`, "invalid_path", true, map[string]string{}},
		{"dirty", "decisions/../renamed.md", "invalid_path", true, map[string]string{}},
		{"not-markdown", "decisions/renamed.txt", "invalid_path", true, map[string]string{}},
		{"conflict-area", "sync-conflicts/decision-sync.md", "invalid_path", true, map[string]string{}},
		{"case-folded-conflict-area", "Sync-Conflicts/decision-sync.md", "invalid_path", true, map[string]string{}},
		{"nested-conflict-area", "decisions/sync-conflicts/decision-sync.md", "invalid_path", true, map[string]string{}},
		{"windows-device", "decisions/CON.md", "invalid_path", true, map[string]string{}},
		{"normalized-collision", "decisions/renamed.md", "path_collision", true, occupiedPath(t, "darwin", platform.CaseInsensitive, "Decisions/RENAMED.md", "other-entity")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", BasePath: basePath, Base: &base, Project: candidateWithClaimedPath(tc.target, base), Vault: candidate(basePath, base)}
			if tc.context {
				input.GOOS, input.CaseMode, input.OccupiedPathKeys = "darwin", platform.CaseInsensitive, tc.occupied
			}
			got := Merge(input)
			if got.Kind != MergeConflict || got.Reason != tc.reason || got.Accepted != nil || strings.Contains(got.Reason, "renamed") {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestMergeFirstSyncRequiresExactPathCollisionContext(t *testing.T) {
	t.Parallel()

	document := documentAtRevision(t, fixtureDocument(t, "base-decision.md", "decisions/decision-sync.md"), "decisions/decision-sync.md", 1)
	got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", Project: candidate("decisions/decision-sync.md", document)})
	if got.Kind != MergeConflict || got.Reason != "invalid_input" {
		t.Fatalf("first sync guessed missing mapping collision context: %+v", got)
	}
}

func TestMergeRequiresUnforgeableBaseIdentityContext(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	for _, tc := range []struct {
		name  string
		input MergeInput
	}{
		{"missing-project-id", MergeInput{EntityID: "decision-sync", BasePath: relative, Base: &base, Project: candidate(relative, base), Vault: candidate(relative, base)}},
		{"wrong-project-id", MergeInput{EntityID: "decision-sync", ProjectID: "project-2222222222222222", BasePath: relative, Base: &base, Project: candidate(relative, base), Vault: candidate(relative, base)}},
		{"missing-base-path", MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", Base: &base, Project: candidate(relative, base), Vault: candidate(relative, base)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(tc.input)
			if got.Kind != MergeConflict || got.Reason != "invalid_input" || got.Accepted != nil {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestMergeArchiveConflictsWithOtherSideRename(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	archive := editFrontmatterUnit(t, base, "status", "archived\n")
	got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", BasePath: relative, Base: &base, Project: candidate(relative, archive), Vault: candidateAtPath(t, "decisions/renamed.md", base), GOOS: "darwin", CaseMode: platform.CaseSensitive, OccupiedPathKeys: map[string]string{}})
	if got.Kind != MergeConflict || got.Reason != "archive_vs_modify" || got.Accepted != nil {
		t.Fatalf("got=%+v", got)
	}
}

func TestMergeDoesNotMutateOccupiedPathSnapshot(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	occupied := occupiedPath(t, "darwin", platform.CaseInsensitive, "decisions/existing.md", "other")
	want := maps.Clone(occupied)
	got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", BasePath: relative, Base: &base, Project: candidateAtPath(t, "decisions/renamed.md", base), Vault: candidate(relative, base), GOOS: "darwin", CaseMode: platform.CaseInsensitive, OccupiedPathKeys: occupied})
	if got.Kind != MergeWriteBoth || !reflect.DeepEqual(occupied, want) {
		t.Fatalf("kind=%q occupied=%v want=%v", got.Kind, occupied, want)
	}
}

func TestMergeFirstSyncValidatesEveryLedgerEntityShape(t *testing.T) {
	t.Parallel()

	const projectID = "project-1111111111111111"
	documents := map[string]struct {
		id, path, content string
	}{
		"decision":      {"decision-new", "decisions/decision-new.md", "---\nid: decision-new\nentity_type: decision\nproject_id: " + projectID + "\nrevision: 1\nsync_status: synced\ntitle: Decision\nstatus: proposed\ntags: []\nsupersedes: []\nsource_sessions: []\nevidence: []\n---\n\n# Decision\n\n## Alternatives\n\n## Rejected paths\n"},
		"open-loop":     {"loop-new", "open-loops/loop-new.md", "---\nid: loop-new\nentity_type: open_loop\nproject_id: " + projectID + "\nrevision: 1\nsync_status: synced\ntitle: Loop\nstatus: open\ntags: []\nsource_sessions: []\nevidence: []\n---\n\n# Loop\n\n## Attempted paths\n"},
		"session":       {"session-new", "sessions/session-new.md", "---\nid: session-new\nentity_type: session\nproject_id: " + projectID + "\nrevision: 1\nsync_status: synced\nsession_id: source-session\nsource_sessions: [source-session]\ninitial_goal: Goal\ngoal_changes: []\nphases:\n  - title: Build\n    summary: Done\n    evidence: []\nfiles: []\ncommits: []\nverification: []\ndecisions_added: []\ndecisions_revised: []\nopen_loops_created: []\nopen_loops_closed: []\nprevious_session_id: ''\nnext_session_id: ''\nevidence: []\n---\n\n# Session\n"},
		"current-state": {"current-state", "current-state.md", "---\nid: current-state\nentity_type: current_state\nproject_id: " + projectID + "\nrevision: 1\nsync_status: synced\nsource_sessions: []\nevidence: []\n---\n\n# Current state\n\n## Uncommitted changes\n\n## Blockers\n\n## Open risks\n"},
		"timeline":      {"evolution-timeline", "evolution-timeline.md", "---\nid: evolution-timeline\nentity_type: timeline\nproject_id: " + projectID + "\nrevision: 1\nsync_status: synced\nevents:\n  - id: event-1\n    occurred_at: '2026-08-23T00:00:00Z'\n    revision: 1\n    class: verified\n    title: Event\n    summary: Summary\n    evidence: []\n    decision_ids: []\n    open_loop_ids: []\n---\n\n# Timeline\n"},
	}
	for name, tc := range documents {
		t.Run(name, func(t *testing.T) {
			document := parseInlineDocument(t, tc.path, tc.content)
			got := Merge(MergeInput{EntityID: tc.id, ProjectID: projectID, Project: candidate(tc.path, document), GOOS: "darwin", CaseMode: platform.CaseSensitive, OccupiedPathKeys: map[string]string{}})
			if got.Kind != MergeWriteVault || got.Reason != "" || got.Accepted == nil {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestMergeFirstSyncRejectsIncompleteLedgerEntityShapes(t *testing.T) {
	t.Parallel()

	const projectID = "project-1111111111111111"
	decision := parseInlineDocument(t, "decisions/decision-new.md", "---\nid: decision-new\nentity_type: decision\nproject_id: "+projectID+"\nrevision: 1\nsync_status: synced\ntitle: Decision\nstatus: proposed\ntags: []\nsupersedes: []\nsource_sessions: []\nevidence: []\n---\n\n# Decision\n\n## Alternatives\n\n## Rejected paths\n")
	openLoop := parseInlineDocument(t, "open-loops/loop-new.md", "---\nid: loop-new\nentity_type: open_loop\nproject_id: "+projectID+"\nrevision: 1\nsync_status: synced\ntitle: Loop\nstatus: open\ntags: []\nsource_sessions: []\nevidence: []\n---\n\n# Loop\n\n## Attempted paths\n")
	session := parseInlineDocument(t, "sessions/session-new.md", "---\nid: session-new\nentity_type: session\nproject_id: "+projectID+"\nrevision: 1\nsync_status: synced\nsession_id: source-session\nsource_sessions: [source-session]\ninitial_goal: Goal\ngoal_changes: []\nphases: []\nfiles: []\ncommits: []\nverification: []\ndecisions_added: []\ndecisions_revised: []\nopen_loops_created: []\nopen_loops_closed: []\nprevious_session_id: ''\nnext_session_id: ''\nevidence: []\n---\n\n# Session\n")
	current := parseInlineDocument(t, "current-state.md", "---\nid: current-state\nentity_type: current_state\nproject_id: "+projectID+"\nrevision: 1\nsync_status: synced\nsource_sessions: []\nevidence: []\n---\n\n# Current state\n\n## Uncommitted changes\n\n## Blockers\n\n## Open risks\n")
	timeline := parseInlineDocument(t, "evolution-timeline.md", "---\nid: evolution-timeline\nentity_type: timeline\nproject_id: "+projectID+"\nrevision: 1\nsync_status: synced\nevents:\n  - id: event-1\n    occurred_at: invalid-time\n    revision: 1\n    class: verified\n    title: Event\n    summary: Summary\n    evidence: []\n    decision_ids: []\n    open_loop_ids: []\n---\n\n# Timeline\n")
	tests := []struct {
		name, id, path string
		document       syncdoc.Document
	}{
		{"decision-missing-alternatives", "decision-new", "decisions/decision-new.md", removeSectionUnit(t, decision, " / Alternatives#1")},
		{"open-loop-missing-attempts", "loop-new", "open-loops/loop-new.md", removeSectionUnit(t, openLoop, " / Attempted paths#1")},
		{"session-missing-required-array", "session-new", "sessions/session-new.md", removeFrontmatterUnit(t, session, "goal_changes")},
		{"session-missing-provenance", "session-new", "sessions/session-new.md", removeFrontmatterUnit(t, session, "source_sessions")},
		{"current-state-missing-required-array-section", "current-state", "current-state.md", removeSectionUnit(t, current, " / Blockers#1")},
		{"timeline-invalid-time", "evolution-timeline", "evolution-timeline.md", timeline},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(MergeInput{EntityID: tc.id, ProjectID: projectID, Project: candidate(tc.path, tc.document), GOOS: "darwin", CaseMode: platform.CaseSensitive, OccupiedPathKeys: map[string]string{}})
			if got.Kind != MergeConflict || got.Reason != "invalid_new_entity" || got.Accepted != nil {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestMergeFixturesPreserveDisjointUnknownUnitsAndPresentation(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := fixtureDocument(t, "project-decision.md", relative)
	vault := fixtureDocument(t, "vault-decision.md", relative)
	got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", BasePath: relative, Base: &base, Project: candidate(relative, project), Vault: candidate(relative, vault)})
	if got.Kind != MergeWriteBoth || got.Reason != "" || got.Accepted == nil {
		t.Fatalf("got=%+v", got)
	}
	rendered, err := got.Accepted.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"title: Project title", "tags: [sync, vault]", "plugin_key: project", "project_only: keep-project", "vault_only: keep-vault", "Project plugin section.", "Keep this project-only section.", "Keep this vault-only section.", "Vault context."} {
		if !bytes.Contains(rendered, []byte(want)) {
			t.Fatalf("accepted render lost %q\n%s", want, rendered)
		}
	}
}

func TestMergePreservesSetextDuplicateHeadingAndYAMLKeyPresentation(t *testing.T) {
	t.Parallel()

	const relative = "decisions/presentation.md"
	const baseSource = "---\nid: presentation\nentity_type: decision\nproject_id: project-1111111111111111\nrevision: 2\nsync_status: synced\ntitle: Presentation\nstatus: accepted\ntags: []\nsupersedes: []\nsource_sessions: []\nevidence: []\n# plugin key comment\nplugin: base\n---\n\nPresentation\n============\n\nExtensions\n----------\n\n### Plugin\n\nFirst.\n\n### Plugin\n\nSecond.\n\nAlternatives\n------------\n\nRejected paths\n--------------\n"
	base := parseInlineDocument(t, relative, baseSource)
	pluginKeys := sectionKeysByLeaf(t, base, "Plugin")
	if len(pluginKeys) != 2 {
		t.Fatalf("plugin keys=%+v", pluginKeys)
	}
	project := editSectionUnit(t, base, pluginKeys[0].Name, "\nProject first.\n")
	vault := editSectionUnit(t, base, pluginKeys[1].Name, "\nVault second.\n")
	got := Merge(MergeInput{EntityID: "presentation", ProjectID: "project-1111111111111111", BasePath: relative, Base: &base, Project: candidate(relative, project), Vault: candidate(relative, vault)})
	if got.Kind != MergeWriteBoth || got.Reason != "" || got.Accepted == nil {
		t.Fatalf("got=%+v", got)
	}
	rendered, err := got.Accepted.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# plugin key comment", "Presentation\n============", "Extensions\n----------", "### Plugin", "Project first.", "Vault second."} {
		if !bytes.Contains(rendered, []byte(want)) {
			t.Fatalf("lost presentation %q\n%s", want, rendered)
		}
	}
	if bytes.Count(rendered, []byte("### Plugin")) != 2 {
		t.Fatalf("duplicate Setext headings changed\n%s", rendered)
	}
}

func TestMergeFirstSyncConflictsEveryCommonDifferentUnit(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := documentAtRevision(t, fixtureDocument(t, "base-decision.md", relative), relative, 1)
	project := editFrontmatterUnit(t, base, "title", "canary-project\n")
	project = editFrontmatterUnit(t, project, "project_only", "keep-project\n")
	vault := editFrontmatterUnit(t, base, "title", "canary-vault\n")
	vault = editFrontmatterUnit(t, vault, "vault_only", "keep-vault\n")
	got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", Project: candidate(relative, project), Vault: candidate(relative, vault), GOOS: "darwin", CaseMode: platform.CaseSensitive, OccupiedPathKeys: map[string]string{}})
	if got.Kind != MergeConflict || got.Reason != "unit_conflict" || got.Accepted != nil || len(got.Conflicts) != 1 {
		t.Fatalf("got=%+v", got)
	}
	conflict := got.Conflicts[0]
	if conflict.Key != (syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "title"}) || conflict.Base.Present || !bytes.Contains(conflict.Project.Value, []byte("canary-project")) || !bytes.Contains(conflict.Vault.Value, []byte("canary-vault")) || strings.Contains(got.Reason, "canary") {
		t.Fatalf("conflict=%+v reason=%q", conflict, got.Reason)
	}
}

func TestMergeFirstSyncConvergesEquivalentPathKeysToProjectSpelling(t *testing.T) {
	t.Parallel()

	const basePath = "decisions/decision-sync.md"
	base := documentAtRevision(t, fixtureDocument(t, "base-decision.md", basePath), basePath, 1)
	tests := []struct {
		name, projectPath, vaultPath string
	}{
		{"case-fold", "Decisions/DECISION-SYNC.md", basePath},
		{"nfc-nfd", "decisions/Café.md", "decisions/Cafe\u0301.md"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			project := candidateAtPath(t, tc.projectPath, base)
			vault := candidateAtPath(t, tc.vaultPath, base)
			got := Merge(MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", Project: project, Vault: vault, GOOS: "darwin", CaseMode: platform.CaseInsensitive, OccupiedPathKeys: map[string]string{}})
			if got.Kind != MergeWriteVault || got.Reason != "" || got.Accepted == nil || len(got.Conflicts) != 0 {
				t.Fatalf("got=%+v", got)
			}
			accepted, err := got.Accepted.Render()
			if err != nil {
				t.Fatal(err)
			}
			projectBytes, _ := project.Document.Render()
			if !bytes.Equal(accepted, projectBytes) {
				t.Fatal("equivalent first-sync paths changed document bytes")
			}
		})
	}
}

func TestMergeRecomputesAndRejectsUntrustedCandidateHashes(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	bad := candidate(relative, base)
	bad.Hash = strings.Repeat("c", 64)
	for _, tc := range []struct {
		name  string
		input MergeInput
	}{
		{"from-base", MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", BasePath: relative, Base: &base, Project: bad, Vault: candidate(relative, base)}},
		{"first-sync", MergeInput{EntityID: "decision-sync", ProjectID: "project-1111111111111111", Project: Candidate{Present: true, RelativePath: relative, Document: documentAtRevision(t, base, relative, 1), Hash: strings.Repeat("C", 64)}, GOOS: "darwin", CaseMode: platform.CaseSensitive, OccupiedPathKeys: map[string]string{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(tc.input)
			if got.Kind != MergeConflict || got.Reason != "invalid_hash" || got.Accepted != nil || strings.Contains(got.Reason, strings.Repeat("c", 8)) {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func presentedUnit(value, key, heading string) syncdoc.Unit {
	return syncdoc.Unit{
		Present:             true,
		Value:               []byte(value),
		KeyPresentation:     []byte(key),
		HeadingPresentation: []byte(heading),
	}
}

func absentUnit() syncdoc.Unit { return syncdoc.Unit{} }

func TestV2HistoryUnitsSurviveTitleEditAndMergeDifferentEvents(t *testing.T) {
	base := v2HistoryWithTwoEvents(t)
	project := replaceV2Source(t, "项目历史.md", base, "信任链与 dry-run 边界修复", "新的标题")
	vault := replaceV2Source(t, "项目历史.md", base, "继续验证发布链。", "新的下一步")
	result := Merge(v2MergeInput("project-history", "项目历史.md", base, project, vault))
	if result.Kind != MergeWriteBoth || result.Accepted == nil {
		t.Fatalf("result=%+v", result)
	}
	rendered := renderDocument(t, *result.Accepted)
	for _, want := range []string{"新的标题", "新的下一步"} {
		if !bytes.Contains(rendered, []byte(want)) {
			t.Fatalf("accepted history missing %q:\n%s", want, rendered)
		}
	}
	assertV2VisibleFrontmatter(t, rendered, 2)
}

func TestV2ReviewUnitsMergeDifferentDecisions(t *testing.T) {
	base := v2ReviewWithTwoDecisions(t)
	project := replaceV2Source(t, "项目回顾.md", base, "Skill + 本地 CLI", "新的决策标题")
	vault := replaceV2Source(t, "项目回顾.md", base, "兼容实现边界。", "新的决策原因")
	result := Merge(v2MergeInput("project-overview", "项目回顾.md", base, project, vault))
	if result.Kind != MergeWriteBoth || result.Accepted == nil {
		t.Fatalf("result=%+v", result)
	}
	rendered := renderDocument(t, *result.Accepted)
	for _, want := range []string{"新的决策标题", "新的决策原因"} {
		if !bytes.Contains(rendered, []byte(want)) {
			t.Fatalf("accepted review missing %q:\n%s", want, rendered)
		}
	}
	assertV2VisibleFrontmatter(t, rendered, 2)
}

func TestV2MergeConflictAndConvergenceRules(t *testing.T) {
	history := v2HistoryWithTwoEvents(t)
	review := v2ReviewWithTwoDecisions(t)
	t.Run("same-event-concurrent-edit", func(t *testing.T) {
		project := replaceV2Source(t, "项目历史.md", history, "信任链与 dry-run 边界修复", "project 标题")
		vault := replaceV2Source(t, "项目历史.md", history, "信任链与 dry-run 边界修复", "vault 标题")
		got := Merge(v2MergeInput("project-history", "项目历史.md", history, project, vault))
		if got.Kind != MergeConflict || got.Reason != "unit_conflict" {
			t.Fatalf("merge=%+v", got)
		}
	})
	t.Run("reserved-frontmatter-edit", func(t *testing.T) {
		project := replaceV2Source(t, "项目回顾.md", review, "project-0123456789abcdef", "project-fedcba9876543210")
		got := Merge(v2MergeInput("project-overview", "项目回顾.md", review, project, review))
		if got.Kind != MergeConflict || got.Reason != "reserved_field" {
			t.Fatalf("merge=%+v", got)
		}
	})
	t.Run("marker-deletion", func(t *testing.T) {
		project := removeV2MarkerBlock(t, "项目历史.md", history, "timeline-release")
		got := Merge(v2MergeInput("project-history", "项目历史.md", history, project, history))
		if got.Kind != MergeConflict || got.Reason != "unit_conflict" {
			t.Fatalf("merge=%+v", got)
		}
	})
	t.Run("same-title-edit-converges", func(t *testing.T) {
		project := replaceV2Source(t, "项目历史.md", history, "信任链与 dry-run 边界修复", "两端相同标题")
		got := Merge(v2MergeInput("project-history", "项目历史.md", history, project, project))
		if got.Kind != MergeWriteBoth || got.Reason != "" || got.Accepted == nil {
			t.Fatalf("merge=%+v", got)
		}
		assertV2VisibleFrontmatter(t, renderDocument(t, *got.Accepted), 2)
	})
}

func TestV2CRLFMergeUsesNormalizedSemanticUnits(t *testing.T) {
	toCRLF := func(t *testing.T, document syncdoc.Document) syncdoc.Document {
		t.Helper()
		source := bytes.ReplaceAll(renderDocument(t, document), []byte("\n"), []byte("\r\n"))
		parsed, err := syncdoc.Parse("项目历史.md", source)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	base := toCRLF(t, v2HistoryWithTwoEvents(t))
	t.Run("different-events-merge", func(t *testing.T) {
		project := replaceV2Source(t, "项目历史.md", base, "信任链与 dry-run 边界修复", "CRLF project 标题")
		vault := replaceV2Source(t, "项目历史.md", base, "继续验证发布链。", "CRLF vault 下一步")
		got := Merge(v2MergeInput("project-history", "项目历史.md", base, project, vault))
		if got.Kind != MergeWriteBoth || got.Accepted == nil {
			t.Fatalf("merge=%+v", got)
		}
		rendered := renderDocument(t, *got.Accepted)
		if !bytes.Contains(rendered, []byte("CRLF project 标题")) || !bytes.Contains(rendered, []byte("CRLF vault 下一步")) || !bytes.Contains(rendered, []byte("timeline-in-fence")) {
			t.Fatalf("merged content lost:\n%s", rendered)
		}
		if _, err := syncdoc.Parse("项目历史.md", rendered); err != nil {
			t.Fatalf("accepted document did not reparse: %v", err)
		}
	})
	t.Run("same-event-conflicts", func(t *testing.T) {
		project := replaceV2Source(t, "项目历史.md", base, "信任链与 dry-run 边界修复", "CRLF project")
		vault := replaceV2Source(t, "项目历史.md", base, "信任链与 dry-run 边界修复", "CRLF vault")
		got := Merge(v2MergeInput("project-history", "项目历史.md", base, project, vault))
		if got.Kind != MergeConflict || got.Reason != "unit_conflict" {
			t.Fatalf("merge=%+v", got)
		}
	})
}

func TestV2MergeRejectsRenameAwayFromStableFilename(t *testing.T) {
	base := v2HistoryWithTwoEvents(t)
	input := v2MergeInput("project-history", "项目历史.md", base, base, base)
	input.Project = candidateAtPath(t, "改名后历史.md", base)
	result := Merge(input)
	if result.Kind != MergeConflict || result.Reason != "invalid_path" || result.Accepted != nil {
		t.Fatalf("result=%+v", result)
	}
}

func v2MergeInput(entityID, relative string, base, project, vault syncdoc.Document) MergeInput {
	return MergeInput{
		EntityID: entityID, ProjectID: "project-0123456789abcdef", BasePath: relative,
		Base: &base, Project: candidate(relative, project), Vault: candidate(relative, vault),
		GOOS: "darwin", CaseMode: platform.CaseSensitive, OccupiedPathKeys: map[string]string{},
	}
}

func v2HistoryWithTwoEvents(t *testing.T) syncdoc.Document {
	t.Helper()
	source := readV2Fixture(t, "项目历史.valid.md")
	second := `
<!-- session-reviewer:event id="timeline-release" -->
## 2026-08-24 · 发布验证
### 事件类别
里程碑
### 节点意义
完成发布前收敛。
### 摘要
验证发布链。
### 为什么会走到这里
需要证明真实运行。
### 发生了什么
- 执行全量测试
### 结果与验证
- 所有门禁通过
### 留下的问题或下一步
继续验证发布链。
<!-- /session-reviewer:event -->
`
	source = append(bytes.TrimSuffix(source, []byte("\n")), []byte(second)...)
	return parseInlineDocument(t, "项目历史.md", string(source))
}

func v2ReviewWithTwoDecisions(t *testing.T) syncdoc.Document {
	t.Helper()
	source := readV2Fixture(t, "项目回顾.valid.md")
	second := `<!-- session-reviewer:decision id="decision-compatibility" -->
### 兼容性决策
#### 日期
2026-08-24
#### 原因
兼容实现边界。
#### 影响
保留双向合并。
#### 状态
已采用
<!-- /session-reviewer:decision -->

`
	source = bytes.Replace(source, []byte("## 最近验证\n"), append([]byte(second), []byte("## 最近验证\n")...), 1)
	return parseInlineDocument(t, "项目回顾.md", string(source))
}

func readV2Fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "review-v2", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func replaceV2Source(t *testing.T, relative string, document syncdoc.Document, old, replacement string) syncdoc.Document {
	t.Helper()
	source := renderDocument(t, document)
	patched := bytes.Replace(source, []byte(old), []byte(replacement), 1)
	if bytes.Equal(source, patched) {
		t.Fatalf("source does not contain %q", old)
	}
	return parseInlineDocument(t, relative, string(patched))
}

func removeV2MarkerBlock(t *testing.T, relative string, document syncdoc.Document, id string) syncdoc.Document {
	t.Helper()
	source := renderDocument(t, document)
	start := bytes.Index(source, []byte("<!-- session-reviewer:event id=\""+id+"\" -->"))
	if start < 0 {
		t.Fatalf("missing marker %q", id)
	}
	endRelative := bytes.Index(source[start:], []byte("<!-- /session-reviewer:event -->"))
	if endRelative < 0 {
		t.Fatalf("missing closing marker %q", id)
	}
	end := start + endRelative + len("<!-- /session-reviewer:event -->")
	for end < len(source) && source[end] == '\n' {
		end++
	}
	patched := append(bytes.Clone(source[:start]), source[end:]...)
	return parseInlineDocument(t, relative, string(patched))
}

func renderDocument(t *testing.T, document syncdoc.Document) []byte {
	t.Helper()
	rendered, err := document.Render()
	if err != nil {
		t.Fatal(err)
	}
	return rendered
}

func assertV2VisibleFrontmatter(t *testing.T, rendered []byte, revision int) {
	t.Helper()
	frontmatterEnd := bytes.Index(rendered[len("---\n"):], []byte("---\n"))
	if frontmatterEnd < 0 {
		t.Fatal("missing frontmatter close")
	}
	frontmatter := rendered[:len("---\n")+frontmatterEnd]
	if !bytes.Contains(frontmatter, []byte("revision: "+strconv.Itoa(revision))) {
		t.Fatalf("frontmatter revision mismatch: %s", frontmatter)
	}
	for _, forbidden := range []string{"sync_status:", "sync_hash:", "base_hash:", "project_hash:", "vault_hash:"} {
		if bytes.Contains(frontmatter, []byte(forbidden)) {
			t.Fatalf("visible v2 frontmatter contains %q: %s", forbidden, frontmatter)
		}
	}
}

func fixtureDocument(t *testing.T, name, relative string) syncdoc.Document {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sync", name))
	if err != nil {
		t.Fatal(err)
	}
	document, err := syncdoc.Parse(relative, content)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func parseInlineDocument(t *testing.T, relative, content string) syncdoc.Document {
	t.Helper()
	document, err := syncdoc.Parse(relative, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

type existingShapeDocument struct {
	id, path string
	document syncdoc.Document
}

func existingShapeDocuments(t *testing.T) map[string]existingShapeDocument {
	t.Helper()
	const projectID = "project-1111111111111111"
	sources := map[string]struct{ id, path, content string }{
		"decision":      {"decision-existing", "decisions/decision-existing.md", "---\nid: decision-existing\nentity_type: decision\nproject_id: " + projectID + "\nrevision: 7\nsync_status: synced\ntitle: Decision\nstatus: accepted\ntags: []\nsupersedes: []\nsource_sessions: []\nevidence: []\n---\n\n# Decision\n\n## Alternatives\n\n## Rejected paths\n"},
		"open-loop":     {"loop-existing", "open-loops/loop-existing.md", "---\nid: loop-existing\nentity_type: open_loop\nproject_id: " + projectID + "\nrevision: 7\nsync_status: synced\ntitle: Loop\nstatus: open\ntags: []\nsource_sessions: []\nevidence: []\n---\n\n# Loop\n\n## Attempted paths\n"},
		"session":       {"session-existing", "sessions/session-existing.md", "---\nid: session-existing\nentity_type: session\nproject_id: " + projectID + "\nrevision: 7\nsync_status: synced\nsession_id: source-session\nsource_sessions: [source-session]\ninitial_goal: Goal\ngoal_changes: []\nphases: []\nfiles: []\ncommits: []\nverification: []\ndecisions_added: []\ndecisions_revised: []\nopen_loops_created: []\nopen_loops_closed: []\nprevious_session_id: ''\nnext_session_id: ''\nevidence: []\n---\n\n# Session\n"},
		"current-state": {"current-state", "current-state.md", "---\nid: current-state\nentity_type: current_state\nproject_id: " + projectID + "\nrevision: 7\nsync_status: synced\nsource_sessions: []\nevidence: []\n---\n\n# Current state\n\n## Uncommitted changes\n\n## Blockers\n\n## Open risks\n"},
		"timeline":      {"evolution-timeline", "evolution-timeline.md", "---\nid: evolution-timeline\nentity_type: timeline\nproject_id: " + projectID + "\nrevision: 7\nsync_status: synced\nevents:\n  - id: event-1\n    occurred_at: '2026-08-23T00:00:00Z'\n    revision: 1\n    class: verified\n    title: Event\n    summary: Summary\n    evidence: []\n    decision_ids: []\n    open_loop_ids: []\n---\n\n# Timeline\n"},
		"overview":      {"project-overview", "project-overview.md", "---\nid: project-overview\nentity_type: project_overview\nproject_id: " + projectID + "\nrevision: 7\nsync_status: synced\n---\n\n# Project\n"},
	}
	result := make(map[string]existingShapeDocument, len(sources))
	for name, source := range sources {
		result[name] = existingShapeDocument{id: source.id, path: source.path, document: parseInlineDocument(t, source.path, source.content)}
	}
	return result
}

func candidate(relative string, document syncdoc.Document) Candidate {
	rendered, err := document.Render()
	if err != nil {
		panic(err)
	}
	return Candidate{Present: true, RelativePath: relative, Document: document, Hash: syncdoc.ContentHash(rendered)}
}

func candidateAtPath(t *testing.T, relative string, document syncdoc.Document) Candidate {
	t.Helper()
	return candidate(relative, reparseDocumentAtPath(t, document, relative))
}

func candidateWithClaimedPath(relative string, document syncdoc.Document) Candidate {
	return candidate(relative, document)
}

func reparseDocumentAtPath(t *testing.T, document syncdoc.Document, relative string) syncdoc.Document {
	t.Helper()
	rendered, err := document.Render()
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := syncdoc.Parse(relative, rendered)
	if err != nil {
		// Invalid targets are intentionally represented as a claimed path with a
		// valid document so Merge, rather than test setup, validates the claim.
		return document
	}
	return reparsed
}

func occupiedPath(t *testing.T, goos string, caseMode platform.CaseMode, relative, entityID string) map[string]string {
	t.Helper()
	key, err := platform.PathKey(goos, caseMode, relative)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{key: entityID}
}

func editFrontmatterUnit(t *testing.T, document syncdoc.Document, name, value string) syncdoc.Document {
	t.Helper()
	units := document.Units()
	key := syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name}
	unit := units[key]
	unit.Present = true
	unit.Value = []byte(value)
	if !units[key].Present {
		unit.KeyPresentation = nil
	}
	units[key] = unit
	edited, err := document.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	return edited
}

func editSectionUnit(t *testing.T, document syncdoc.Document, name, value string) syncdoc.Document {
	t.Helper()
	units := document.Units()
	key := syncdoc.UnitKey{Kind: syncdoc.UnitSection, Name: name}
	unit := units[key]
	if !unit.Present {
		t.Fatalf("missing section unit %q", name)
	}
	unit.Value = []byte(value)
	units[key] = unit
	edited, err := document.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	return edited
}

func removeFrontmatterUnit(t *testing.T, document syncdoc.Document, name string) syncdoc.Document {
	t.Helper()
	units := document.Units()
	delete(units, syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name})
	result, err := document.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func removeSectionUnit(t *testing.T, document syncdoc.Document, suffix string) syncdoc.Document {
	t.Helper()
	key := sectionKeyBySuffix(t, document, suffix)
	units := document.Units()
	delete(units, key)
	result, err := document.WithUnits(units)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sectionKeyBySuffix(t *testing.T, document syncdoc.Document, suffix string) syncdoc.UnitKey {
	t.Helper()
	for key := range document.Units() {
		if key.Kind == syncdoc.UnitSection && strings.HasSuffix(key.Name, suffix) {
			return key
		}
	}
	t.Fatalf("missing section unit with suffix %q", suffix)
	return syncdoc.UnitKey{}
}

func sectionKeysByLeaf(t *testing.T, document syncdoc.Document, leaf string) []syncdoc.UnitKey {
	t.Helper()
	var keys []syncdoc.UnitKey
	for key := range document.Units() {
		if key.Kind != syncdoc.UnitSection {
			continue
		}
		component := key.Name
		if separator := strings.LastIndex(component, " / "); separator >= 0 {
			component = component[separator+3:]
		}
		if occurrence := strings.LastIndexByte(component, '#'); occurrence >= 0 {
			component = component[:occurrence]
		}
		if level := strings.LastIndexByte(component, '@'); level >= 0 {
			component = component[:level]
		}
		if component == leaf {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return keys
}

func documentAtRevision(t *testing.T, document syncdoc.Document, relative string, revision int) syncdoc.Document {
	t.Helper()
	rendered, err := document.Render()
	if err != nil {
		t.Fatal(err)
	}
	rendered = bytes.Replace(rendered, []byte("revision: 3"), []byte("revision: "+string(rune('0'+revision))), 1)
	parsed, err := syncdoc.Parse(relative, rendered)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func decodeTestStringUnit(units syncdoc.UnitSet, name string) (string, bool) {
	unit := units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name}]
	if !unit.Present {
		return "", false
	}
	return strings.TrimSpace(string(unit.Value)), true
}
