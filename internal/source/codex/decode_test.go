package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/source"
)

func TestDecodeEmitsExactObservedRulesWithPerObservationProjectAffinity(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-shared.jsonl")
	adapter := fixture.adapter(t, "v1")
	candidate := discoverCandidate(t, adapter, "session-shared")
	boundary, err := adapter.Freeze(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)

	wantProject := map[string]string{
		"session_started":  "project-a",
		"cwd_changed":      "project-b",
		"user_request":     "project-b",
		"command_started":  "project-a",
		"command_finished": "project-a",
		"verification":     "project-a",
		"file_change":      "project-b",
	}
	seen := make(map[string]bool)
	for _, observation := range observations {
		projectID, wanted := wantProject[observation.Operation]
		if !wanted {
			continue
		}
		if observation.Key.ProjectID != projectID {
			t.Fatalf("operation %s project=%s want %s: %+v", observation.Operation, observation.Key.ProjectID, projectID, observation)
		}
		seen[observation.Operation] = true
	}
	for operation := range wantProject {
		if !seen[operation] {
			t.Errorf("operation %q not emitted; observations=%+v", operation, observations)
		}
	}
	if fmt.Sprint(report.ProjectIDs) != fmt.Sprint([]string{"project-a", "project-b"}) {
		t.Fatalf("report project IDs=%v", report.ProjectIDs)
	}
	record, found, err := fixture.catalog.GetSource("codex", "session-shared")
	if err != nil || !found {
		t.Fatalf("catalog source found=%v err=%v", found, err)
	}
	if record.Usage.TotalTokens != 3 || fmt.Sprint(record.ProjectIDs) != fmt.Sprint([]string{"project-a", "project-b"}) {
		t.Fatalf("shared source catalog record=%+v", record)
	}
}

func TestDecodeParsesGitAndVerificationFactsWithoutCopyingMessagesOutputsOrUsage(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-project-a.jsonl")
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)

	var gitFound, verificationFound, redactionFound bool
	for _, observation := range observations {
		if observation.Operation == "git_observation" && observation.Fields["branch"] == "main" && observation.Fields["status"] == "dirty" {
			gitFound = true
		}
		if observation.Operation == "verification" && observation.Fields["status"] == "test" && observation.Fields["exit_code"] == "0" && observation.Outcome == "passed" {
			verificationFound = true
		}
		if strings.Contains(observation.Excerpt, "[REDACTED:") {
			redactionFound = true
		}
		if utf8.RuneCountInString(observation.Excerpt) > 512 {
			t.Fatalf("excerpt has %d code points", utf8.RuneCountInString(observation.Excerpt))
		}
	}
	if !gitFound || !verificationFound || !redactionFound {
		t.Fatalf("typed facts missing: git=%v verification=%v redaction=%v observations=%+v", gitFound, verificationFound, redactionFound, observations)
	}
	body, err := json.Marshal(observations)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		longUserSecret,
		"COMPLETE-USER-MESSAGE-MUST-NOT-PERSIST",
		"TOOL-OUTPUT-MUST-NOT-PERSIST",
		"UNSUPPORTED-RAW-MUST-NOT-PERSIST",
		"total_tokens",
		"input_tokens",
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("observation persistence copied forbidden source content %q: %s", forbidden, body)
		}
	}
	if report.UnsupportedRecords < 1 {
		t.Fatalf("unsupported record count=%d", report.UnsupportedRecords)
	}
	record, found, err := fixture.catalog.GetSource("codex", "session-project-a")
	if err != nil || !found || record.Usage.TotalTokens != 15 {
		t.Fatalf("catalog usage record=%+v found=%v err=%v", record, found, err)
	}
}

func TestGitStatusGrammarRejectsProseAsAStatusEntry(t *testing.T) {
	if fields, valid := parseGitOutput("status", "exit code: 0\n## main...origin/main\nthis prose only sounds dirty"); valid {
		t.Fatalf("prose accepted as typed Git status: %+v", fields)
	}
	fields, valid := parseGitOutput("status", "exit code: 0\n## main...origin/main\n M internal/source/decode.go")
	if !valid || fields["branch"] != "main" || fields["status"] != "dirty" {
		t.Fatalf("valid porcelain rejected: fields=%+v valid=%v", fields, valid)
	}
}

func TestBoundedExcerptRejectsOversizedMarkerWithoutExceedingPersistenceLimits(t *testing.T) {
	input := strings.Repeat("界", 600) + " [REDACTED:" + strings.Repeat("A", 600) + "] COMPLETE-MESSAGE"
	excerpt := boundedExcerpt(input)
	if utf8.RuneCountInString(excerpt) > 512 {
		t.Fatalf("excerpt has %d code points", utf8.RuneCountInString(excerpt))
	}
	if len(excerpt) > 1024 {
		t.Fatalf("excerpt has %d bytes", len(excerpt))
	}
	if excerpt == input || strings.Contains(excerpt, "COMPLETE-MESSAGE") {
		t.Fatalf("excerpt retained complete input: %q", excerpt)
	}
}

func TestAffinityDoesNotFallBackToBroaderProjectWhenNestedBindingLosesAuthentication(t *testing.T) {
	fixture := newAdapterFixture(t)
	nested := filepath.Join(fixture.projectA, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.bindings = []projectidentity.Binding{
		fixture.bindings[0],
		authenticateBinding(t, "project-nested", nested),
	}
	implementation, ok := fixture.adapter(t, "v1").(*adapter)
	if !ok {
		t.Fatal("Codex adapter has unexpected implementation type")
	}
	if err := os.Rename(nested, nested+"-moved"); err != nil {
		t.Fatal(err)
	}

	projectIDs, reason := implementation.classifyAffinity(filepath.Join(nested, "future.go"), true)
	if reason != "ambiguous_project_root" || fmt.Sprint(projectIDs) != fmt.Sprint([]string{"project-a", "project-nested"}) {
		t.Fatalf("affinity fell back after nested binding changed: projects=%v reason=%q", projectIDs, reason)
	}
}

func TestDecodeRejectsBoundaryMetadataChangedAfterFreeze(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-project-a.jsonl")
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
	if err != nil {
		t.Fatal(err)
	}
	boundary.Candidate.InitialCWD = fixture.projectB
	if _, err := adapter.Decode(context.Background(), boundary, func(memory.ObservationRevision) error { return nil }); err == nil {
		t.Fatal("Decode accepted boundary metadata changed after freeze")
	}
}

func TestDecodeDiagnosticsStayBoundedWhileUnsupportedCountRemainsExact(t *testing.T) {
	decoder := recordDecoder{}
	for range 5000 {
		decoder.unsupported()
	}
	if decoder.report.UnsupportedRecords != 5000 {
		t.Fatalf("unsupported count=%d", decoder.report.UnsupportedRecords)
	}
	if len(decoder.report.Diagnostics) != 4096 {
		t.Fatalf("diagnostics=%d want 4096", len(decoder.report.Diagnostics))
	}
}

func TestMalformedLineAdvancesBoundaryAndLaterValidLineDecodes(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-malformed.jsonl")
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-malformed"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	if report.MalformedLines != 1 || report.UnsupportedRecords != 1 {
		t.Fatalf("decode report=%+v", report)
	}
	foundLater := false
	for _, observation := range observations {
		if observation.Operation == "user_request" && observation.Key.Subject == "user-after-malformed" {
			foundLater = true
		}
	}
	if !foundLater {
		t.Fatalf("later valid line not decoded: %+v", observations)
	}
	if boundary.Frozen.Location.JSONL == nil || boundary.Frozen.Location.JSONL.Line != 5 {
		t.Fatalf("malformed line did not advance frozen boundary: %+v", boundary.Frozen)
	}
}

func TestAmbiguousAuthenticatedRootsQuarantineOnlyAffectedRevisions(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-project-a.jsonl")
	duplicateBinding := authenticateBinding(t, "project-shadow", fixture.projectA)
	fixture.bindings = []projectidentity.Binding{fixture.bindings[0], duplicateBinding}
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
	if err != nil {
		t.Fatal(err)
	}
	observations, report := decodeBoundary(t, adapter, boundary)
	if len(observations) != 0 || len(report.Quarantined) == 0 {
		t.Fatalf("ambiguous observations=%d quarantined=%d report=%+v", len(observations), len(report.Quarantined), report)
	}
	if report.TerminalState != memory.Indexed || report.CatalogRecordDigest == "" {
		t.Fatalf("one-revision quarantine poisoned source decoding: %+v", report)
	}
	for _, quarantined := range report.Quarantined {
		if quarantined.ReasonCode != "ambiguous_project_root" || len(quarantined.CandidateProjectIDs) != 2 {
			t.Fatalf("quarantine metadata=%+v", quarantined)
		}
	}
}

func TestAdapterVersionCreatesStableKeySuccessorsWithoutMutatingAnActiveSet(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-shared.jsonl")

	v1 := fixture.adapter(t, "v1")
	v1Boundary, err := v1.Freeze(context.Background(), discoverCandidate(t, v1, "session-shared"))
	if err != nil {
		t.Fatal(err)
	}
	v1Observations, _ := decodeBoundary(t, v1, v1Boundary)

	v2 := fixture.adapter(t, "v2", "v1")
	v2Boundary, err := v2.Freeze(context.Background(), discoverCandidate(t, v2, "session-shared"))
	if err != nil {
		t.Fatal(err)
	}
	v2Observations, report := decodeBoundary(t, v2, v2Boundary)
	if len(v1Observations) == 0 || len(v1Observations) != len(v2Observations) {
		t.Fatalf("revision counts v1=%d v2=%d", len(v1Observations), len(v2Observations))
	}
	v1BySlot := make(map[string]memory.ObservationRevision, len(v1Observations))
	for _, observation := range v1Observations {
		v1BySlot[observationSlot(observation.Key)] = observation
	}
	successors := make(map[string]string, len(report.Supersessions))
	for _, item := range report.Supersessions {
		if item.SupersededAdapter != "v1" || item.SuccessorAdapter != "v2" {
			t.Fatalf("adapter lineage=%+v", item)
		}
		successors[item.SupersededRevisionID] = item.SuccessorRevisionID
	}
	for _, current := range v2Observations {
		previous, found := v1BySlot[observationSlot(current.Key)]
		if !found || !reflect.DeepEqual(previous.Key, current.Key) {
			t.Fatalf("stable key missing for v2 revision: %+v", current.Key)
		}
		if previous.RevisionID == current.RevisionID {
			t.Fatalf("adapter version did not change revision ID for %+v", current.Key)
		}
		if successors[previous.RevisionID] != current.RevisionID {
			t.Fatalf("successor metadata missing: old=%s new=%s report=%+v", previous.RevisionID, current.RevisionID, report.Supersessions)
		}
	}
}

func observationSlot(key memory.ObservationKey) string {
	return strings.Join([]string{
		key.Provider, key.SessionID, key.SourceIdentity, fmt.Sprint(key.Sequence),
		key.ProjectID, key.Kind, key.Subject,
	}, "\x00")
}

var _ source.Adapter
