package projectview

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestReduceReconcilesExactInputsAndSeparatesLiveFromWitnessedState(t *testing.T) {
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-08-01T10:00:00Z", []summarySpec{
		{sequence: 2, kind: "branch", subject: "branch-observation", occurredAt: "2026-08-01T10:05:00Z", operation: "git_observation", fields: map[string]string{"branch": "release/old"}},
		{sequence: 3, kind: "verification", subject: "test-call", occurredAt: "2026-08-01T10:06:00Z", operation: "verification", object: "package", outcome: "passed", fields: map[string]string{"component": "package", "status": "test"}},
	})
	s2 := viewFixture(t, "s2", memory.Missing, "2026-08-02T10:00:00Z", nil)
	probe := probeFixture(t, "project-a", "main", strings.Repeat("b", 40), 3)
	usage := []memory.AssociatedUsage{
		{Provider: "codex", SessionID: "s2", UsageRecordDigest: s2.UsageRecordDigest, Shared: true},
		{Provider: "codex", SessionID: "s1", UsageRecordDigest: s1.UsageRecordDigest},
	}

	got, changed, err := Reduce(Input{
		ProjectID: "project-a", SessionViews: []memory.SessionView{s2, s1}, ProbeState: probe,
		AssociatedUsage: usage, ReducerVersion: ReducerVersion, ReferenceTime: "2026-09-01T00:00:00Z",
	})
	if err != nil || !changed {
		t.Fatalf("Reduce changed=%v err=%v", changed, err)
	}
	if err := memory.ValidateProjectView(got); err != nil {
		t.Fatalf("invalid ProjectView: %v", err)
	}
	if got.SourceSessions != 2 || got.TerminalCounts.Indexed != 1 || got.TerminalCounts.Missing != 1 || terminalTotal(got.TerminalCounts) != 2 {
		t.Fatalf("coverage mismatch: sessions=%d counts=%+v", got.SourceSessions, got.TerminalCounts)
	}
	wantDeps := []memory.SessionViewDependency{{Provider: "codex", SessionID: "s1", Digest: s1.Digest}, {Provider: "codex", SessionID: "s2", Digest: s2.Digest}}
	if !reflect.DeepEqual(got.SessionViewDependencies, wantDeps) {
		t.Fatalf("dependencies=%+v want %+v", got.SessionViewDependencies, wantDeps)
	}
	if got.LiveState.Branch != "main" || got.LiveState.Head != strings.Repeat("b", 40) || got.LiveState.DirtyPathCount != 3 || got.ProbeStateDigest != probe.Digest {
		t.Fatalf("live state did not come exactly from probe: %+v", got.LiveState)
	}
	branch := findRecord(t, got.WitnessedState, "witnessed_state", "branch")
	if branch.Fields["value"] != "release/old" || branch.OccurredAt != "2026-08-01T10:05:00Z" {
		t.Fatalf("witnessed branch=%+v", branch)
	}
	verification := findRecord(t, got.WitnessedState, "witnessed_state", "verification:package")
	if verification.Fields["outcome"] != "passed" || verification.OccurredAt != "2026-08-01T10:06:00Z" {
		t.Fatalf("witnessed verification=%+v", verification)
	}
	for _, record := range append(append([]memory.DerivedRecord(nil), got.WitnessedState...), got.DerivedRecords...) {
		if record.Kind == "release" || record.Fields["historical_success"] != "" {
			t.Fatalf("live probe invented historical success: %+v", record)
		}
	}
	wantUsage := []memory.AssociatedUsage{
		{Provider: "codex", SessionID: "s1", UsageRecordDigest: s1.UsageRecordDigest},
		{Provider: "codex", SessionID: "s2", UsageRecordDigest: s2.UsageRecordDigest, Shared: true},
	}
	if !reflect.DeepEqual(got.AssociatedUsage, wantUsage) {
		t.Fatalf("usage=%+v want %+v", got.AssociatedUsage, wantUsage)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"total_tokens", "input_tokens", "raw_tool_output", "transcript", "assistant_message"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("ProjectView copied forbidden content %q: %s", forbidden, body)
		}
	}
}

func TestReduceRejectsCoverageUsageAndIdentityMismatches(t *testing.T) {
	base := viewFixture(t, "s1", memory.Indexed, "2026-08-01T10:00:00Z", nil)
	probe := probeFixture(t, "project-a", "main", strings.Repeat("a", 40), 0)
	valid := func() Input {
		return Input{ProjectID: "project-a", SessionViews: []memory.SessionView{base}, ProbeState: probe,
			AssociatedUsage: []memory.AssociatedUsage{{Provider: "codex", SessionID: "s1", UsageRecordDigest: base.UsageRecordDigest}},
			ReducerVersion:  ReducerVersion, ReferenceTime: "2026-09-01T00:00:00Z"}
	}
	tests := []struct {
		name string
		edit func(*Input)
	}{
		{name: "no sessions", edit: func(in *Input) { in.SessionViews = nil; in.AssociatedUsage = nil }},
		{name: "duplicate session", edit: func(in *Input) { in.SessionViews = append(in.SessionViews, base) }},
		{name: "cross project session", edit: func(in *Input) { in.SessionViews[0].ProjectID = "project-b" }},
		{name: "cross project probe", edit: func(in *Input) { in.ProbeState = probeFixture(t, "project-b", "main", strings.Repeat("a", 40), 0) }},
		{name: "missing usage", edit: func(in *Input) { in.AssociatedUsage = nil }},
		{name: "duplicate usage", edit: func(in *Input) { in.AssociatedUsage = append(in.AssociatedUsage, in.AssociatedUsage[0]) }},
		{name: "wrong usage digest", edit: func(in *Input) { in.AssociatedUsage[0].UsageRecordDigest = digestFor("wrong-usage") }},
		{name: "unknown usage session", edit: func(in *Input) { in.AssociatedUsage[0].SessionID = "s2" }},
		{name: "unsupported reducer", edit: func(in *Input) { in.ReducerVersion = "project-view-v2" }},
		{name: "hidden wall clock", edit: func(in *Input) { in.ReferenceTime = "" }},
		{name: "noncanonical reference time", edit: func(in *Input) { in.ReferenceTime = "2026-08-31T20:00:00-04:00" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid()
			input.SessionViews = append([]memory.SessionView(nil), input.SessionViews...)
			input.AssociatedUsage = append([]memory.AssociatedUsage(nil), input.AssociatedUsage...)
			test.edit(&input)
			if got, changed, err := Reduce(input); err == nil || changed || got.Digest != "" {
				t.Fatalf("invalid input accepted: changed=%v err=%v view=%+v", changed, err, got)
			}
		})
	}
}

func TestReduceOrdersEventsAndDeduplicatesOnlyExactTypedProvenance(t *testing.T) {
	timestamp := "2026-08-01T10:00:00Z"
	s1 := viewFixture(t, "s1", memory.Indexed, timestamp, []summarySpec{
		{sequence: 3, kind: "file", subject: "same", occurredAt: timestamp, operation: "file_change", object: "a.go", outcome: "success", fields: map[string]string{"path": "a.go"}},
		{sequence: 2, kind: "artifact", subject: "same", occurredAt: timestamp, operation: "artifact_created", object: "artifact", outcome: "success", fields: map[string]string{"artifact_id": "artifact-a"}},
	})
	s2 := viewFixture(t, "s2", memory.Indexed, timestamp, []summarySpec{
		{sequence: 1, kind: "file", subject: "same", occurredAt: timestamp, operation: "file_change", object: "b.go", outcome: "success", fields: map[string]string{"path": "b.go"}},
	})
	got := reduceFixture(t, []memory.SessionView{s2, s1}, "2026-09-01T00:00:00Z", nil)
	events := recordsOfKind(got.DerivedRecords, "event_ref")
	if len(events) != 3 {
		t.Fatalf("typed facts sharing text were collapsed: %+v", events)
	}
	want := []string{"s1:2:" + s1.ObservationSummaries[0].RevisionID, "s1:3:" + s1.ObservationSummaries[1].RevisionID, "s2:1:" + s2.ObservationSummaries[0].RevisionID}
	gotOrder := make([]string, 0, len(events))
	for _, event := range events {
		gotOrder = append(gotOrder, event.Fields["session_id"]+":"+event.Fields["sequence"]+":"+event.DependencyRevisionIDs[0])
	}
	if !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("event order=%v want %v", gotOrder, want)
	}
}

func TestReduceDerivesOnlyCompatibleLaterCrossSessionRecovery(t *testing.T) {
	failure := summarySpec{sequence: 1, kind: "verification", subject: "failure", occurredAt: "2026-08-01T10:00:00Z", operation: " verification ", object: "package", outcome: "failed", fields: map[string]string{"component": " Package ", "status": "test"}}
	s1 := viewFixture(t, "s1", memory.Indexed, failure.occurredAt, []summarySpec{failure})
	s2 := viewFixture(t, "s2", memory.Indexed, "2026-08-02T10:00:00Z", []summarySpec{
		{sequence: 1, kind: "verification", subject: "wrong-component", occurredAt: "2026-08-02T10:00:00Z", operation: "verification", object: "other", outcome: "passed", fields: map[string]string{"component": "other", "status": "test"}},
		{sequence: 2, kind: "verification", subject: "build-is-incompatible", occurredAt: "2026-08-02T10:01:00Z", operation: "verification", object: "package", outcome: "passed", fields: map[string]string{"component": "package", "status": "build"}},
		{sequence: 3, kind: "verification", subject: "lint-is-incompatible", occurredAt: "2026-08-02T10:02:00Z", operation: "verification", object: "package", outcome: "passed", fields: map[string]string{"component": "package", "status": "lint"}},
		{sequence: 4, kind: "verification", subject: "match", occurredAt: "2026-08-02T10:03:00Z", operation: "VERIFICATION", object: "package", outcome: "passed", fields: map[string]string{"component": "package", "status": "test"}},
	})
	got := reduceFixture(t, []memory.SessionView{s2, s1}, "2026-09-01T00:00:00Z", nil)
	recoveries := recordsOfKind(got.DerivedRecords, "recovery_link")
	if len(recoveries) != 1 {
		t.Fatalf("recovery links=%+v want exactly compatible pair", recoveries)
	}
	wantDeps := []string{s1.ObservationSummaries[0].RevisionID, s2.ObservationSummaries[3].RevisionID}
	if !reflect.DeepEqual(recoveries[0].DependencyRevisionIDs, wantDeps) || recoveries[0].Fields["operation"] != "test" || recoveries[0].Fields["component"] != "package" || recoveries[0].Fields["fact_kind"] != "verification" {
		t.Fatalf("recovery=%+v want dependencies %v", recoveries[0], wantDeps)
	}

	within := viewFixture(t, "s3", memory.Indexed, "2026-08-03T10:00:00Z", []summarySpec{
		{sequence: 1, kind: "verification", subject: "f", occurredAt: "2026-08-03T10:00:00Z", operation: "verification", outcome: "failed", fields: map[string]string{"component": "package"}},
		{sequence: 2, kind: "verification", subject: "p", occurredAt: "2026-08-03T10:01:00Z", operation: "verification", outcome: "passed", fields: map[string]string{"component": "package"}},
	})
	within.DerivedRecords = []memory.DerivedRecord{{
		ID: "session-recovery", Kind: "recovery_link", Subject: "verification:package", OccurredAt: within.ObservationSummaries[1].OccurredAt,
		DependencyRevisionIDs: []string{within.ObservationSummaries[0].RevisionID, within.ObservationSummaries[1].RevisionID},
		RuleID:                "matching-operation-component", RuleVersion: "session-view-v1", Fields: map[string]string{"operation": "verification", "component": "package", "outcome": "recovered"},
	}}
	within.Digest = mustSessionDigest(t, within)
	got = reduceFixture(t, []memory.SessionView{within}, "2026-09-01T00:00:00Z", nil)
	links := recordsOfKind(got.DerivedRecords, "recovery_link")
	if len(links) != 1 || !reflect.DeepEqual(links[0], within.DerivedRecords[0]) {
		t.Fatalf("SessionView recovery was lost or rewritten: got=%+v want=%+v", links, within.DerivedRecords)
	}
}

func TestReduceRejectsDuplicatePhysicalSourceIdentityAcrossSessions(t *testing.T) {
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-08-01T00:00:00Z", nil)
	s2 := viewFixture(t, "s2", memory.Indexed, "2026-08-02T00:00:00Z", nil)
	s2.SourceIdentity = s1.SourceIdentity
	s2.Digest = mustSessionDigest(t, s2)
	if err := memory.ValidateSessionView(s2); err != nil {
		t.Fatalf("fixture should expose cross-view physical duplication while remaining valid: %v", err)
	}
	input := inputFixture(t, []memory.SessionView{s1, s2}, "2026-09-01T00:00:00Z", nil)
	if view, changed, err := Reduce(input); err == nil || changed || view.Digest != "" {
		t.Fatalf("duplicate physical source accepted: changed=%v err=%v view=%+v", changed, err, view)
	}
}

func TestReduceRanksModulesByDocumentedFormulaAndNormalizedPathTieBreak(t *testing.T) {
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-06-01T10:00:00Z", []summarySpec{
		{sequence: 1, kind: "file", subject: "a1", occurredAt: "2026-08-28T10:00:00Z", operation: "file_change", object: "./internal/a.go", outcome: "success", fields: map[string]string{"path": "./internal/a.go"}},
		{sequence: 2, kind: "verification", subject: "av", occurredAt: "2026-08-28T10:01:00Z", operation: "verification", outcome: "passed", fields: map[string]string{"component": "internal/a.go", "status": "test"}},
		{sequence: 3, kind: "file", subject: "b1", occurredAt: "2026-06-01T10:00:00Z", operation: "file_change", object: "b.go", outcome: "success", fields: map[string]string{"path": "b.go"}},
	})
	s2 := viewFixture(t, "s2", memory.Indexed, "2026-08-29T10:00:00Z", []summarySpec{
		{sequence: 1, kind: "file", subject: "a2", occurredAt: "2026-08-29T10:00:00Z", operation: "file_change", object: "internal\\a.go", outcome: "success", fields: map[string]string{"path": "internal\\a.go"}},
		{sequence: 2, kind: "verification", subject: "av2", occurredAt: "2026-08-29T10:01:00Z", operation: "verification", outcome: "passed", fields: map[string]string{"component": "./internal/a.go", "status": "test"}},
		{sequence: 3, kind: "file", subject: "c1", occurredAt: "2026-08-29T10:02:00Z", operation: "file_change", object: "c.go", outcome: "success", fields: map[string]string{"path": "c.go"}},
	})
	got := reduceFixture(t, []memory.SessionView{s1, s2}, "2026-09-01T00:00:00Z", nil)
	ranks := recordsOfKind(got.DerivedRecords, "module_rank")
	if len(ranks) != 3 {
		t.Fatalf("module ranks=%+v", ranks)
	}
	if ranks[0].Subject != "internal/a.go" || ranks[0].Fields["score"] != "17" || ranks[0].Fields["session_coverage"] != "2" || ranks[0].Fields["verification_count"] != "2" || ranks[0].Fields["change_count"] != "2" || ranks[0].Fields["recency_bucket"] != "3" {
		t.Fatalf("a rank=%+v want score 17", ranks[0])
	}
	if ranks[1].Subject != "c.go" || ranks[2].Subject != "b.go" {
		t.Fatalf("rank order=%v want a, c, b", []string{ranks[0].Subject, ranks[1].Subject, ranks[2].Subject})
	}

	tie1 := viewFixture(t, "s3", memory.Indexed, "2026-08-30T00:00:00Z", []summarySpec{{sequence: 1, kind: "file", subject: "z", occurredAt: "2026-08-30T00:00:00Z", operation: "file_change", fields: map[string]string{"path": "z.go"}}})
	tie2 := viewFixture(t, "s4", memory.Indexed, "2026-08-30T00:00:00Z", []summarySpec{{sequence: 1, kind: "file", subject: "a", occurredAt: "2026-08-30T00:00:00Z", operation: "file_change", fields: map[string]string{"path": "a.go"}}})
	tied := reduceFixture(t, []memory.SessionView{tie1, tie2}, "2026-09-01T00:00:00Z", nil)
	tiedRanks := recordsOfKind(tied.DerivedRecords, "module_rank")
	if tiedRanks[0].Subject != "a.go" || tiedRanks[1].Subject != "z.go" {
		t.Fatalf("normalized path tie break failed: %+v", tiedRanks)
	}
	if ranks[0].Fields["latest_observed_at"] != ranks[0].OccurredAt {
		t.Fatalf("module ranking did not persist its audited latest timestamp: %+v", ranks[0])
	}
}

func TestReduceCountsOnlySuccessfulEncodedFileChanges(t *testing.T) {
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-08-28T00:00:00Z", []summarySpec{
		{sequence: 1, kind: "file", subject: "success", occurredAt: "2026-08-28T00:00:00Z", operation: "file_change", outcome: "success", fields: map[string]string{"path": "a.go"}},
		{sequence: 2, kind: "file", subject: "failed", occurredAt: "2026-08-28T00:01:00Z", operation: "file_change", outcome: "failure", fields: map[string]string{"path": "a.go"}},
		{sequence: 3, kind: "file", subject: "generic", occurredAt: "2026-08-28T00:02:00Z", operation: "file_read", outcome: "success", fields: map[string]string{"path": "a.go"}},
	})
	got := reduceFixture(t, []memory.SessionView{s1}, "2026-09-01T00:00:00Z", nil)
	rank := recordsOfKind(got.DerivedRecords, "module_rank")[0]
	if rank.Fields["change_count"] != "1" || rank.Fields["session_coverage"] != "1" || rank.Fields["verification_count"] != "0" || rank.Fields["score"] != "8" {
		t.Fatalf("module score counted failed/generic file facts as changes: %+v", rank)
	}
}

func TestReduceCreatesOnlyStructuralDateOrVersionPhaseBoundaries(t *testing.T) {
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-01-01T00:00:00Z", []summarySpec{
		{sequence: 1, kind: "branch", subject: "main", occurredAt: "2026-01-01T00:00:00Z", fields: map[string]string{"branch": "main"}},
		{sequence: 2, kind: "file", subject: "ordinary", occurredAt: "2026-01-02T00:00:00Z", fields: map[string]string{"path": "a.go"}},
		{sequence: 3, kind: "branch", subject: "release", occurredAt: "2026-01-03T00:00:00Z", fields: map[string]string{"branch": "release/v2"}},
		{sequence: 4, kind: "version", subject: "v2", occurredAt: "2026-01-04T00:00:00Z", fields: map[string]string{"version": "v2.0.0"}},
		{sequence: 5, kind: "file", subject: "gap", occurredAt: "2026-02-05T00:00:01Z", fields: map[string]string{"path": "b.go"}},
	})
	got := reduceFixture(t, []memory.SessionView{s1}, "2026-03-01T00:00:00Z", nil)
	phases := recordsOfKind(got.DerivedRecords, "phase_boundary")
	if len(phases) != 3 {
		t.Fatalf("phase boundaries=%+v want branch, version, gap only", phases)
	}
	gotNames := []string{phases[0].Subject, phases[1].Subject, phases[2].Subject}
	wantNames := []string{"2026-01-03", "v2.0.0", "2026-02-05"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("phase names=%v want %v", gotNames, wantNames)
	}
	for _, phase := range phases {
		if phase.Fields["trigger"] == "" || phase.Fields["rationale"] != "" || strings.Contains(strings.ToLower(phase.Subject), "phase") {
			t.Fatalf("phase invented semantic rationale/name: %+v", phase)
		}
	}
}

func TestReduceRetainsAllWitnessedGitFieldsAndUsesGitStatusStructuralFacts(t *testing.T) {
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-01-01T00:00:00Z", []summarySpec{
		{sequence: 1, kind: "git_status", subject: "first", occurredAt: "2026-01-01T00:00:00Z", operation: "git_observation", fields: map[string]string{"branch": "main", "status": "clean", "git_head": strings.Repeat("1", 40)}},
		{sequence: 2, kind: "git_status", subject: "second", occurredAt: "2026-01-02T00:00:00Z", operation: "git_observation", fields: map[string]string{"branch": "release/v2", "status": "dirty", "git_head": strings.Repeat("2", 40), "tag": "v2.0.0"}},
	})
	got := reduceFixture(t, []memory.SessionView{s1}, "2026-02-01T00:00:00Z", nil)
	checks := map[string]string{"branch": "release/v2", "git_status": "dirty", "head": strings.Repeat("2", 40)}
	for subject, want := range checks {
		record := findRecord(t, got.WitnessedState, "witnessed_state", subject)
		if record.Fields["value"] != want || record.OccurredAt != "2026-01-02T00:00:00Z" {
			t.Fatalf("witnessed %s=%+v want %q", subject, record, want)
		}
	}
	phases := recordsOfKind(got.DerivedRecords, "phase_boundary")
	if len(phases) != 2 || phases[0].Fields["trigger"] != "branch_change" || phases[0].Subject != "2026-01-02" || phases[1].Fields["trigger"] != "tag_change" || phases[1].Subject != "v2.0.0" {
		t.Fatalf("Git structural facts did not create exact branch/tag boundaries: %+v", phases)
	}
}

func TestReduceWitnessesDirectStructuralSubjectsAndBoundsLongStateKeys(t *testing.T) {
	component := strings.Repeat("a", 300)
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-01-01T00:00:00Z", []summarySpec{
		{sequence: 1, kind: "branch", subject: "feature/direct", occurredAt: "2026-01-01T00:00:00Z"},
		{sequence: 2, kind: "version", subject: "v3.0.0", occurredAt: "2026-01-01T00:01:00Z"},
		{sequence: 3, kind: "verification", subject: "long-component", occurredAt: "2026-01-01T00:02:00Z", operation: "verification", outcome: "passed", fields: map[string]string{"component": component}},
	})
	got := reduceFixture(t, []memory.SessionView{s1}, "2026-02-01T00:00:00Z", nil)
	if branch := findRecord(t, got.WitnessedState, "witnessed_state", "branch"); branch.Fields["value"] != "feature/direct" {
		t.Fatalf("direct branch subject not witnessed: %+v", branch)
	}
	if version := findRecord(t, got.WitnessedState, "witnessed_state", "version"); version.Fields["value"] != "v3.0.0" {
		t.Fatalf("direct version subject not witnessed: %+v", version)
	}
	var longState memory.DerivedRecord
	for _, record := range got.WitnessedState {
		if record.Fields["component"] == component {
			longState = record
			break
		}
	}
	if longState.ID == "" || len(longState.Subject) > 256 || longState.Fields["value"] != "passed" {
		t.Fatalf("long witnessed key was lost or exceeded contract: %+v", longState)
	}
}

func TestReduceDoesNotRecoverAgainAfterSessionViewAlreadyConsumedFailure(t *testing.T) {
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-08-01T00:00:00Z", []summarySpec{
		{sequence: 1, kind: "verification", subject: "failure", occurredAt: "2026-08-01T00:00:00Z", operation: "verification", outcome: "failed", fields: map[string]string{"component": "package"}},
		{sequence: 2, kind: "verification", subject: "success", occurredAt: "2026-08-01T00:01:00Z", operation: "verification", outcome: "passed", fields: map[string]string{"component": "package"}},
	})
	s1.DerivedRecords = []memory.DerivedRecord{{
		ID: "session-recovery", Kind: "recovery_link", Subject: "verification:package", OccurredAt: s1.ObservationSummaries[1].OccurredAt,
		DependencyRevisionIDs: []string{s1.ObservationSummaries[0].RevisionID, s1.ObservationSummaries[1].RevisionID},
		RuleID:                "matching-operation-component", RuleVersion: "session-view-v1", Fields: map[string]string{"operation": "verification", "component": "package", "outcome": "recovered"},
	}}
	s1.Digest = mustSessionDigest(t, s1)
	s2 := viewFixture(t, "s2", memory.Indexed, "2026-08-02T00:00:00Z", []summarySpec{{sequence: 1, kind: "verification", subject: "later", occurredAt: "2026-08-02T00:00:00Z", operation: "verification", outcome: "passed", fields: map[string]string{"component": "package"}}})
	got := reduceFixture(t, []memory.SessionView{s1, s2}, "2026-09-01T00:00:00Z", nil)
	recoveries := recordsOfKind(got.DerivedRecords, "recovery_link")
	if len(recoveries) != 1 || !reflect.DeepEqual(recoveries[0], s1.DerivedRecords[0]) {
		t.Fatalf("same-Session recovery was lost or recovered again: %+v", recoveries)
	}
}

func TestReduceRecomputesOutputInsteadOfTrustingForgedPreviousDependencyDigest(t *testing.T) {
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-08-01T00:00:00Z", []summarySpec{{sequence: 1, kind: "file", subject: "a", occurredAt: "2026-08-01T00:00:00Z", fields: map[string]string{"path": "a.go"}}})
	previous := reduceFixture(t, []memory.SessionView{s1}, "2026-09-01T00:00:00Z", nil)
	forged := previous
	forged.DerivedRecords = []memory.DerivedRecord{}
	var err error
	forged.Digest, err = memory.ProjectViewDigest(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.ValidateProjectView(forged); err != nil {
		t.Fatalf("forged fixture must remain structurally valid: %v", err)
	}
	got, changed, err := Reduce(inputFixture(t, []memory.SessionView{s1}, "2026-09-01T00:00:00Z", &forged))
	if err != nil || !changed || len(got.DerivedRecords) == 0 || got.Generation != forged.Generation+1 || got.PreviousViewDigest != forged.Digest {
		t.Fatalf("forged prior trusted: changed=%v err=%v view=%+v", changed, err, got)
	}
}

func TestReduceBoundsMultibyteRequestExcerptWithoutBreakingValidation(t *testing.T) {
	excerpt := strings.Repeat("界", 200)
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-08-01T00:00:00Z", []summarySpec{{sequence: 1, kind: "request", subject: "request", occurredAt: "2026-08-01T00:00:00Z", operation: "user_request", excerpt: excerpt}})
	got := reduceFixture(t, []memory.SessionView{s1}, "2026-09-01T00:00:00Z", nil)
	event := recordsOfKind(got.DerivedRecords, "event_ref")[0]
	if !strings.HasPrefix(excerpt, event.Fields["excerpt"]) || len(event.Fields["excerpt"]) > 512 || !json.Valid([]byte(fmt.Sprintf("%q", event.Fields["excerpt"]))) {
		t.Fatalf("excerpt was not safely bounded: bytes=%d value=%q", len(event.Fields["excerpt"]), event.Fields["excerpt"])
	}
}

func TestReduceUsesTimestampInstantsForProjectRangeAndRejectsOutOfRangeFacts(t *testing.T) {
	early := viewFixture(t, "s1", memory.Indexed, "2026-08-01T10:00:00+08:00", []summarySpec{{sequence: 1, kind: "file", subject: "early", occurredAt: "2026-08-01T10:01:00+08:00", fields: map[string]string{"path": "early.go"}}})
	late := viewFixture(t, "s2", memory.Indexed, "2026-08-01T03:00:00Z", []summarySpec{{sequence: 1, kind: "file", subject: "late", occurredAt: "2026-08-01T04:00:00Z", fields: map[string]string{"path": "late.go"}}})
	got := reduceFixture(t, []memory.SessionView{late, early}, "2026-09-01T00:00:00Z", nil)
	if got.StartedAt != early.StartedAt || got.EndedAt != late.EndedAt {
		t.Fatalf("project range used lexical timestamps: got %s..%s want %s..%s", got.StartedAt, got.EndedAt, early.StartedAt, late.EndedAt)
	}

	invalid := early
	invalid.ObservationSummaries[0].OccurredAt = "2026-08-01T01:59:59Z"
	invalid.Digest = mustSessionDigest(t, invalid)
	if err := memory.ValidateSessionView(invalid); err != nil {
		t.Fatalf("fixture should expose the upstream range gap while remaining structurally valid: %v", err)
	}
	input := inputFixture(t, []memory.SessionView{invalid}, "2026-09-01T00:00:00Z", nil)
	if view, changed, err := Reduce(input); err == nil || changed || view.Digest != "" {
		t.Fatalf("out-of-range observation accepted: changed=%v err=%v view=%+v", changed, err, view)
	}
}

func TestReduceIsDeterministicReusesPriorByteForByteAndDefensivelyCopies(t *testing.T) {
	s1 := viewFixture(t, "s1", memory.Indexed, "2026-08-01T00:00:00Z", []summarySpec{{sequence: 1, kind: "file", subject: "a", occurredAt: "2026-08-01T00:00:00Z", fields: map[string]string{"path": "a.go"}}})
	s2 := viewFixture(t, "s2", memory.Unsupported, "2026-08-02T00:00:00Z", nil)
	first := reduceFixture(t, []memory.SessionView{s1, s2}, "2026-09-01T00:00:00Z", nil)
	second := reduceFixture(t, []memory.SessionView{s2, s1}, "2026-09-01T00:00:00Z", nil)
	if first.Digest != second.Digest || first.DependencyDigest != second.DependencyDigest {
		t.Fatalf("shuffled equivalent inputs churned digest: %s/%s vs %s/%s", first.Digest, first.DependencyDigest, second.Digest, second.DependencyDigest)
	}

	input := inputFixture(t, []memory.SessionView{s2, s1}, "2026-09-01T00:00:00Z", &first)
	unchanged, changed, err := Reduce(input)
	if err != nil || changed {
		t.Fatalf("unchanged reduction changed=%v err=%v", changed, err)
	}
	firstJSON, _ := json.Marshal(first)
	unchangedJSON, _ := json.Marshal(unchanged)
	if !bytes.Equal(firstJSON, unchangedJSON) {
		t.Fatalf("unchanged prior not byte-identical\nfirst=%s\nnext=%s", firstJSON, unchangedJSON)
	}

	input.SessionViews[0].ObservationSummaries = nil
	input.AssociatedUsage[0].Shared = !input.AssociatedUsage[0].Shared
	input.ProbeState.Branch = "mutated"
	if unchanged.Digest != first.Digest || unchanged.LiveState.Branch != "main" || len(unchanged.ObservationRevisionIDs) != 1 {
		t.Fatalf("output aliased input: %+v", unchanged)
	}
	unchanged.DerivedRecords[0].Fields["path"] = "mutated.go"
	unchanged.SessionViewDependencies[0].Digest = digestFor("mutated")
	again, changed, err := Reduce(inputFixture(t, []memory.SessionView{s1, s2}, "2026-09-01T00:00:00Z", &first))
	if err != nil || changed || again.DerivedRecords[0].Fields["path"] == "mutated.go" || again.SessionViewDependencies[0].Digest != first.SessionViewDependencies[0].Digest {
		t.Fatalf("prior/output alias leaked: changed=%v err=%v view=%+v", changed, err, again)
	}

	sameBucketInput := inputFixture(t, []memory.SessionView{s1, s2}, "2026-09-02T00:00:00Z", &first)
	sameBucket, changed, err := Reduce(sameBucketInput)
	if err != nil || changed || sameBucket.Digest != first.Digest || sameBucket.DependencyDigest != first.DependencyDigest {
		t.Fatalf("same effective ranking bucket churned view: changed=%v err=%v next=%+v", changed, err, sameBucket)
	}

	crossedInput := inputFixture(t, []memory.SessionView{s1, s2}, "2026-12-01T00:00:00Z", &first)
	next, changed, err := Reduce(crossedInput)
	if err != nil || !changed || next.Generation != first.Generation+1 || next.PreviousViewDigest != first.Digest || next.DependencyDigest == first.DependencyDigest {
		t.Fatalf("changed recency bucket did not advance generation: changed=%v err=%v next=%+v", changed, err, next)
	}
}

func TestProjectReductionBoundsEventsAndEachDerivedPhaseBeforeAppend(t *testing.T) {
	if err := ensureRecordCapacity(maxProjectRecords-1, 1); err != nil {
		t.Fatalf("exact near-limit append rejected: %v", err)
	}
	if err := ensureRecordCapacity(maxProjectRecords-1, 2); err == nil {
		t.Fatal("append crossing the cumulative record limit was accepted")
	}
	tooMany := []memory.SessionView{
		{ObservationSummaries: make([]memory.ObservationSummary, maxProjectRecords)},
		{ObservationSummaries: make([]memory.ObservationSummary, 1)},
	}
	if events, revisions, err := collectEvents(tooMany); err == nil || events != nil || revisions != nil {
		t.Fatalf("oversized event union was allocated/accepted: events=%d revisions=%d err=%v", len(events), len(revisions), err)
	}

	item := event{provider: "codex", sessionID: "s1", time: mustTime(t, "2026-08-01T00:00:00Z"), summary: memory.ObservationSummary{
		RevisionID: digestFor("limit-event"), Sequence: 1, Kind: "file", Subject: "a", OccurredAt: "2026-08-01T00:00:00Z",
		Operation: "file_change", Outcome: "success", Fields: map[string]string{"path": "a.go", "component": "a.go", "branch": "main"},
	}}
	if records, err := deriveEventReferences([]event{item}, 0); err == nil || records != nil {
		t.Fatalf("event phase exceeded zero budget: records=%d err=%v", len(records), err)
	}
	if records, err := rankModules([]event{item}, mustTime(t, "2026-09-01T00:00:00Z"), 0); err == nil || records != nil {
		t.Fatalf("ranking phase exceeded zero budget: records=%d err=%v", len(records), err)
	}
	if records, err := derivePhaseBoundaries([]event{item}, 0); err != nil || len(records) != 0 {
		t.Fatalf("non-boundary phase should consume no budget: records=%d err=%v", len(records), err)
	}

	branch1 := item
	branch1.summary.Kind = "branch"
	branch1.summary.Fields = map[string]string{"branch": "main"}
	branch2 := branch1
	branch2.summary.RevisionID = digestFor("limit-branch-2")
	branch2.summary.Sequence = 2
	branch2.summary.OccurredAt = "2026-08-02T00:00:00Z"
	branch2.summary.Fields = map[string]string{"branch": "release"}
	branch2.time = mustTime(t, branch2.summary.OccurredAt)
	if records, err := derivePhaseBoundaries([]event{branch1, branch2}, 0); err == nil || records != nil {
		t.Fatalf("phase boundary exceeded zero budget: records=%d err=%v", len(records), err)
	}
	if records, err := deriveWitnessedState([]event{branch1}, 0); err == nil || records != nil {
		t.Fatalf("witness phase exceeded zero budget: records=%d err=%v", len(records), err)
	}

	sessionDerived := memory.DerivedRecord{ID: "session-derived", Kind: "recovery_link", Subject: "test:a.go", OccurredAt: item.summary.OccurredAt,
		DependencyRevisionIDs: []string{item.summary.RevisionID}, RuleID: "session-rule", RuleVersion: "session-view-v1"}
	if records, _, err := copySessionDerivedRecords([]memory.SessionView{{Provider: "codex", SessionID: "s1", DerivedRecords: []memory.DerivedRecord{sessionDerived}}}, 0); err == nil || records != nil {
		t.Fatalf("Session-derived phase exceeded zero budget: records=%d err=%v", len(records), err)
	}

	failure := item
	failure.summary.Kind = "verification"
	failure.summary.Operation = "verification"
	failure.summary.Outcome = "failed"
	failure.summary.Fields = map[string]string{"status": "test", "component": "a.go"}
	success := failure
	success.sessionID = "s2"
	success.summary.RevisionID = digestFor("limit-success")
	success.summary.Outcome = "passed"
	success.summary.OccurredAt = "2026-08-02T00:00:00Z"
	success.time = mustTime(t, success.summary.OccurredAt)
	if records, err := deriveRecoveryLinks([]event{failure, success}, nil, nil, 0); err == nil || records != nil {
		t.Fatalf("recovery phase exceeded zero budget: records=%d err=%v", len(records), err)
	}
}

type summarySpec struct {
	sequence   int
	kind       string
	subject    string
	occurredAt string
	operation  string
	object     string
	outcome    string
	fields     map[string]string
	excerpt    string
}

func viewFixture(t *testing.T, sessionID string, state memory.TerminalState, startedAt string, specs []summarySpec) memory.SessionView {
	t.Helper()
	summaries := make([]memory.ObservationSummary, 0, len(specs))
	for _, spec := range specs {
		revisionID := digestFor(fmt.Sprintf("%s:%d:%s:%s:%s", sessionID, spec.sequence, spec.kind, spec.subject, spec.occurredAt))
		summaries = append(summaries, memory.ObservationSummary{
			RevisionID: revisionID, Sequence: spec.sequence, Kind: spec.kind, Subject: spec.subject, OccurredAt: spec.occurredAt,
			Operation: spec.operation, Object: spec.object, Outcome: spec.outcome, Fields: cloneMap(spec.fields), Excerpt: spec.excerpt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Sequence != summaries[j].Sequence {
			return summaries[i].Sequence < summaries[j].Sequence
		}
		return summaries[i].RevisionID < summaries[j].RevisionID
	})
	availability := memory.SourceAvailable
	if state == memory.Missing {
		availability = memory.SourceUnavailable
	}
	view := memory.SessionView{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: "project-a", Provider: "codex", SessionID: sessionID, SourceIdentity: "source-" + sessionID,
		SourceRecordDigest: digestFor("source-" + sessionID), UsageRecordDigest: digestFor("source-" + sessionID),
		StartedAt: startedAt, EndedAt: startedAt, TerminalState: state, SourceAvailability: availability,
		ObservationSummaries: summaries, ObservationChunkDigests: []string{}, DerivedRecords: []memory.DerivedRecord{}, Diagnostics: []memory.Diagnostic{},
		DependencyDigest: digestFor("session-dependency-" + sessionID), MaterializerVersion: "session-view-v1",
	}
	for _, summary := range summaries {
		view.ActiveRevisionIDs = append(view.ActiveRevisionIDs, summary.RevisionID)
		if view.EndedAt < summary.OccurredAt {
			view.EndedAt = summary.OccurredAt
		}
	}
	view.Digest = mustSessionDigest(t, view)
	if err := memory.ValidateSessionView(view); err != nil {
		t.Fatalf("invalid SessionView fixture: %v\n%+v", err, view)
	}
	return view
}

func probeFixture(t *testing.T, projectID, branch, head string, dirty int) memory.ProjectProbeState {
	t.Helper()
	probe := memory.ProjectProbeState{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: projectID, CanonicalRoot: "/workspace/" + projectID,
		Branch: branch, Head: head, DirtyPathCount: dirty, RemoteIdentityHashes: []string{}, VersionFiles: []memory.ProbeFile{},
		RequiredProjectionFiles: []memory.ProbeFile{}, ProbeVersion: "project-probe-v1", Diagnostics: []memory.Diagnostic{},
	}
	var err error
	probe.Digest, err = memory.ProjectProbeStateDigest(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.ValidateProjectProbeState(probe); err != nil {
		t.Fatalf("invalid probe fixture: %v", err)
	}
	return probe
}

func inputFixture(t *testing.T, views []memory.SessionView, reference string, previous *memory.ProjectView) Input {
	t.Helper()
	usage := make([]memory.AssociatedUsage, 0, len(views))
	for _, view := range views {
		usage = append(usage, memory.AssociatedUsage{Provider: view.Provider, SessionID: view.SessionID, UsageRecordDigest: view.UsageRecordDigest, Shared: view.SessionID == "s2"})
	}
	return Input{ProjectID: "project-a", SessionViews: append([]memory.SessionView(nil), views...), ProbeState: probeFixture(t, "project-a", "main", strings.Repeat("a", 40), 0), AssociatedUsage: usage, Previous: previous, ReducerVersion: ReducerVersion, ReferenceTime: reference}
}

func reduceFixture(t *testing.T, views []memory.SessionView, reference string, previous *memory.ProjectView) memory.ProjectView {
	t.Helper()
	view, changed, err := Reduce(inputFixture(t, views, reference, previous))
	if err != nil || !changed {
		t.Fatalf("Reduce fixture changed=%v err=%v", changed, err)
	}
	return view
}

func mustSessionDigest(t *testing.T, view memory.SessionView) string {
	t.Helper()
	digest, err := memory.SessionViewDigest(view)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func digestFor(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func cloneMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func recordsOfKind(records []memory.DerivedRecord, kind string) []memory.DerivedRecord {
	var result []memory.DerivedRecord
	for _, record := range records {
		if record.Kind == kind {
			result = append(result, record)
		}
	}
	return result
}

func findRecord(t *testing.T, records []memory.DerivedRecord, kind, subject string) memory.DerivedRecord {
	t.Helper()
	for _, record := range records {
		if record.Kind == kind && record.Subject == subject {
			return record
		}
	}
	t.Fatalf("missing %s/%s in %+v", kind, subject, records)
	return memory.DerivedRecord{}
}

// totalForTest keeps the production TerminalCounts contract opaque while the
// test independently verifies that every source Session is terminal.
func terminalTotal(counts memory.TerminalCounts) int {
	return counts.Indexed + counts.Unsupported + counts.Missing + counts.Unreadable + counts.Ambiguous
}
