package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/session"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

const longUserSecret = "sk-phase-one-123456789012345678901234"

type adapterFixture struct {
	sessions string
	projectA string
	projectB string
	catalog  *sourcecatalog.Catalog
	bindings []projectidentity.Binding
}

func newAdapterFixture(t *testing.T) *adapterFixture {
	t.Helper()
	root := t.TempDir()
	projectA := filepath.Join(root, "project-a")
	projectB := filepath.Join(root, "project-b")
	sessions := filepath.Join(root, "sessions")
	dataRoot := filepath.Join(root, "data")
	for _, path := range []string{projectA, projectB, sessions, dataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bindingA := authenticateBinding(t, "project-a", projectA)
	bindingB := authenticateBinding(t, "project-b", projectB)
	catalog, err := sourcecatalog.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	return &adapterFixture{
		sessions: sessions,
		projectA: bindingA.CanonicalRoot,
		projectB: bindingB.CanonicalRoot,
		catalog:  catalog,
		bindings: []projectidentity.Binding{bindingA, bindingB},
	}
}

func authenticateBinding(t *testing.T, id, root string) projectidentity.Binding {
	t.Helper()
	binding, err := projectidentity.Resolve(config.ProjectMapping{ID: id, Root: root}, root, runtime.GOOS)
	if err != nil {
		t.Fatalf("authenticate %s: %v", id, err)
	}
	return binding
}

func (fixture *adapterFixture) adapter(t *testing.T, version string, supersedes ...string) source.Adapter {
	t.Helper()
	redactor := redact.Default()
	adapter, err := New(AdapterOptions{
		SessionsRoot:              fixture.sessions,
		Bindings:                  fixture.bindings,
		Catalog:                   fixture.catalog,
		Redactor:                  &redactor,
		AdapterVersion:            version,
		SupersedesAdapterVersions: append([]string(nil), supersedes...),
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func (fixture *adapterFixture) installFixture(t *testing.T, name string) string {
	t.Helper()
	templatePath := filepath.Join("..", "..", "..", "testdata", "zero-token", "codex", name)
	body, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	longText := strings.Repeat("界", 600) + " OPENAI_API_KEY=" + longUserSecret + " COMPLETE-USER-MESSAGE-MUST-NOT-PERSIST"
	rendered := strings.NewReplacer(
		"{{PROJECT_A}}", filepath.ToSlash(fixture.projectA),
		"{{PROJECT_B}}", filepath.ToSlash(fixture.projectB),
		"{{LONG_USER_TEXT}}", longText,
	).Replace(string(body))
	path := filepath.Join(fixture.sessions, name)
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func discoverCandidate(t *testing.T, adapter source.Adapter, sessionID string) source.Candidate {
	t.Helper()
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range discovery.Candidates {
		if candidate.SessionID == sessionID {
			return candidate
		}
	}
	t.Fatalf("session %q not discovered: %+v issues=%+v", sessionID, discovery.Candidates, discovery.Issues)
	return source.Candidate{}
}

func decodeBoundary(t *testing.T, adapter source.Adapter, boundary source.Boundary) ([]memory.ObservationRevision, source.DecodeReport) {
	t.Helper()
	var observations []memory.ObservationRevision
	report, err := adapter.Decode(context.Background(), boundary, func(observation memory.ObservationRevision) error {
		if err := memory.ValidateObservationRevision(observation); err != nil {
			t.Fatalf("invalid emitted observation: %v\n%+v", err, observation)
		}
		observations = append(observations, observation)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return observations, report
}

func TestNewValidatesDependenciesAndDiscoveryClaimsOnlyCodex(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-project-a.jsonl")
	redactor := redact.Default()
	valid := AdapterOptions{
		SessionsRoot: fixture.sessions, Bindings: fixture.bindings, Catalog: fixture.catalog,
		Redactor: &redactor, AdapterVersion: "v1",
	}
	tests := map[string]func(*AdapterOptions){
		"sessions root": func(options *AdapterOptions) { options.SessionsRoot = "" },
		"bindings":      func(options *AdapterOptions) { options.Bindings = nil },
		"catalog":       func(options *AdapterOptions) { options.Catalog = nil },
		"redactor":      func(options *AdapterOptions) { options.Redactor = nil },
		"version":       func(options *AdapterOptions) { options.AdapterVersion = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			options := valid
			mutate(&options)
			if _, err := New(options); err == nil {
				t.Fatalf("New accepted missing %s", name)
			}
		})
	}
	adapter, err := New(valid)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := adapter.Discover(context.Background())
	if err != nil || len(discovery.Candidates) != 1 {
		t.Fatalf("discovery=%+v err=%v", discovery, err)
	}
	if discovery.Candidates[0].Provider != "codex" {
		t.Fatalf("provider=%q want codex", discovery.Candidates[0].Provider)
	}
}

func TestDiscoverFreezeEveryFixtureAndReadVerifiesHashAndLimit(t *testing.T) {
	fixture := newAdapterFixture(t)
	for _, name := range []string{"session-project-a.jsonl", "session-shared.jsonl", "session-malformed.jsonl"} {
		fixture.installFixture(t, name)
	}
	adapter := fixture.adapter(t, "v1")
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sessionIDs []string
	boundaries := make(map[string]source.Boundary)
	for _, candidate := range discovery.Candidates {
		sessionIDs = append(sessionIDs, candidate.SessionID)
		boundary, err := adapter.Freeze(context.Background(), candidate)
		if err != nil {
			t.Fatalf("freeze %s: %v", candidate.SessionID, err)
		}
		if boundary.TerminalState != memory.Indexed || len(boundary.Frozen.SourceHash) != 64 || boundary.Frozen.Location.JSONL == nil {
			t.Fatalf("invalid frozen boundary for %s: %+v", candidate.SessionID, boundary)
		}
		boundaries[candidate.SessionID] = boundary
	}
	sort.Strings(sessionIDs)
	wantIDs := []string{"session-malformed", "session-project-a", "session-shared"}
	if fmt.Sprint(sessionIDs) != fmt.Sprint(wantIDs) {
		t.Fatalf("first discovery sessions = %v, want %v; issues=%+v", sessionIDs, wantIDs, discovery.Issues)
	}
	boundary := boundaries["session-project-a"]
	var sourceRecord session.Record
	path := filepath.Join(fixture.sessions, "session-project-a.jsonl")
	if _, err := session.Stream(path, session.DecodeOptions{}, func(record session.Record) error {
		if record.Line == 3 {
			sourceRecord = record
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if sourceRecord.SourceHash == "" {
		t.Fatal("fixture source record not found")
	}
	ref := memory.SourceRef{
		Provider: "codex", SessionID: "session-project-a", SourceIdentity: boundary.SourceIdentity,
		Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{
			Line: sourceRecord.Line, ByteOffset: sourceRecord.ByteOffset,
		}},
		SourceHash: sourceRecord.SourceHash,
	}
	span, err := adapter.Read(context.Background(), ref, 64)
	if err != nil || len(span) == 0 || len(span) > 64 {
		t.Fatalf("bounded read len=%d err=%v", len(span), err)
	}
	for _, limit := range []int64{0, source.MaxReadBytes + 1} {
		if _, err := adapter.Read(context.Background(), ref, limit); err == nil {
			t.Fatalf("Read accepted limit %d", limit)
		}
	}
	invalidRefs := map[string]func(*memory.SourceRef){
		"provider":        func(value *memory.SourceRef) { value.Provider = "future" },
		"location":        func(value *memory.SourceRef) { value.Location.Kind = "future" },
		"source identity": func(value *memory.SourceRef) { value.SourceIdentity = "source-other" },
		"source hash":     func(value *memory.SourceRef) { value.SourceHash = strings.Repeat("0", 64) },
	}
	for name, mutate := range invalidRefs {
		t.Run("rejects "+name, func(t *testing.T) {
			invalid := ref
			mutate(&invalid)
			if _, err := adapter.Read(context.Background(), invalid, 64); err == nil {
				t.Fatalf("Read accepted mismatched %s", name)
			}
		})
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"timestamp\":\"2026-08-31T10:00:11Z\",\"type\":\"future_record\",\"payload\":{}}\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	afterAppend, err := adapter.Read(context.Background(), ref, 64)
	if err != nil || !bytes.Equal(afterAppend, span) {
		t.Fatalf("append changed frozen-prefix read: equal=%v err=%v", bytes.Equal(afterAppend, span), err)
	}
	file, err = os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{'X'}, sourceRecord.ByteOffset+10); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Read(context.Background(), ref, 64); err == nil {
		t.Fatal("Read accepted an interior mutation of the frozen source")
	}
}

func TestFreezeRejectsCandidateMetadataChangedAfterDiscovery(t *testing.T) {
	fixture := newAdapterFixture(t)
	fixture.installFixture(t, "session-project-a.jsonl")
	adapter := fixture.adapter(t, "v1")
	candidate := discoverCandidate(t, adapter, "session-project-a")
	candidate.StartedAt = "2026-08-31T10:00:01Z"
	if _, err := adapter.Freeze(context.Background(), candidate); err == nil {
		t.Fatal("Freeze accepted candidate metadata changed after discovery")
	}
}

func TestFreezeOrdersPhysicalSegmentsAsOneBoundaryAndAppendCreatesSuccessorBoundary(t *testing.T) {
	fixture := newAdapterFixture(t)
	firstPath := filepath.Join(fixture.sessions, "segment-1.jsonl")
	secondPath := filepath.Join(fixture.sessions, "segment-2.jsonl")
	first := fmt.Sprintf("{\"timestamp\":\"2026-08-31T09:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"segmented\",\"cwd\":%q}}\n{\"timestamp\":\"2026-08-31T09:00:01Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"first\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"first\"}]}}", filepath.ToSlash(fixture.projectA))
	second := fmt.Sprintf("{\"timestamp\":\"2026-08-31T10:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"segmented\",\"cwd\":%q}}\n{\"timestamp\":\"2026-08-31T10:00:01Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"second\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"second\"}]}}\n", filepath.ToSlash(fixture.projectA))
	if err := os.WriteFile(firstPath, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v1")
	candidate := discoverCandidate(t, adapter, "segmented")
	boundary, err := adapter.Freeze(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if got := boundary.Frozen.Location.JSONL.Line; got != 4 {
		t.Fatalf("logical boundary line=%d want 4", got)
	}
	if len(boundary.Segments) != 2 {
		t.Fatalf("frozen segments=%+v", boundary.Segments)
	}
	for _, segment := range boundary.Segments {
		if segment.Size < 1 || len(segment.SourceHash) != 64 {
			t.Fatalf("segment lacks exact size/hash: %+v", segment)
		}
	}

	appendFile, err := os.OpenFile(secondPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	appended := "{\"timestamp\":\"2026-08-31T10:00:02Z\",\"type\":\"future_record\",\"payload\":{}}\n"
	if _, err := appendFile.WriteString(appended); err != nil {
		_ = appendFile.Close()
		t.Fatal(err)
	}
	if err := appendFile.Close(); err != nil {
		t.Fatal(err)
	}
	laterCandidate := discoverCandidate(t, adapter, "segmented")
	later, err := adapter.Freeze(context.Background(), laterCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if later.SourceIdentity != boundary.SourceIdentity || later.Frozen.SourceHash == boundary.Frozen.SourceHash || later.Frozen.Location.JSONL.Line != 5 {
		t.Fatalf("append did not create a stable-identity successor boundary: before=%+v after=%+v", boundary, later)
	}
}

func TestDiscoverMixedValidAndCorruptSegmentsYieldsOnlyTerminalIssue(t *testing.T) {
	fixture := newAdapterFixture(t)
	valid := fmt.Sprintf("{\"timestamp\":\"2026-08-31T09:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"mixed-segments\",\"cwd\":%q}}\n", filepath.ToSlash(fixture.projectA))
	corrupt := fmt.Sprintf("{\"timestamp\":\"not-a-time\",\"type\":\"session_meta\",\"payload\":{\"id\":\"mixed-segments\",\"cwd\":%q}}\n", filepath.ToSlash(fixture.projectA))
	if err := os.WriteFile(filepath.Join(fixture.sessions, "mixed-valid.jsonl"), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.sessions, "mixed-corrupt.jsonl"), []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}

	discovery, err := fixture.adapter(t, "v1").Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range discovery.Candidates {
		if candidate.SessionID == "mixed-segments" {
			t.Fatalf("mixed valid/corrupt Session became incomplete indexed candidate: %+v", candidate)
		}
	}
	found := false
	for _, issue := range discovery.Issues {
		if issue.SessionID == "mixed-segments" && issue.Code == "duplicate_segment" && issue.TerminalState == memory.Ambiguous {
			found = true
		}
	}
	if !found {
		t.Fatalf("mixed Session terminal issue missing: %+v", discovery.Issues)
	}
}

func TestDecodeOlderFrozenBoundaryAfterAppendReadsOnlyAuthenticatedPrefix(t *testing.T) {
	fixture := newAdapterFixture(t)
	path := fixture.installFixture(t, "session-shared.jsonl")
	adapter := fixture.adapter(t, "v1")
	boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-shared"))
	if err != nil {
		t.Fatal(err)
	}
	appended := "{\"timestamp\":\"2026-08-31T11:00:08Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"appended-must-not-decode\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"new suffix\"}]}}\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(appended); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	observations, report := decodeBoundary(t, adapter, boundary)
	if report.UnsupportedRecords != 0 {
		t.Fatalf("appended suffix affected old boundary report: %+v", report)
	}
	for _, observation := range observations {
		if observation.Key.Subject == "appended-must-not-decode" || observation.Key.Sequence > boundary.Frozen.Location.JSONL.Line {
			t.Fatalf("old boundary decoded appended suffix: %+v", observation)
		}
	}
}

func TestDecodeClassifiesCatalogAppendTruncationAndInteriorMutation(t *testing.T) {
	t.Run("append", func(t *testing.T) {
		fixture := newAdapterFixture(t)
		path := fixture.installFixture(t, "session-project-a.jsonl")
		adapter := fixture.adapter(t, "v1")
		first := discoverCandidate(t, adapter, "session-project-a")
		boundary, err := adapter.Freeze(context.Background(), first)
		if err != nil {
			t.Fatal(err)
		}
		decodeBoundary(t, adapter, boundary)
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.WriteString("{\"timestamp\":\"2026-08-31T10:00:11Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"task_complete\"}}\n")
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal(errors.Join(writeErr, closeErr))
		}
		boundary, err = adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
		if err != nil {
			t.Fatal(err)
		}
		_, report := decodeBoundary(t, adapter, boundary)
		if report.BoundaryRelation != source.BoundaryAppend {
			t.Fatalf("append relation=%q", report.BoundaryRelation)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "truncation", mutate: func(t *testing.T, path string) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.SplitAfter(string(body), "\n")
			if err := os.WriteFile(path, []byte(strings.Join(lines[:10], "")), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "same-coordinate-interior", mutate: func(t *testing.T, path string) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := bytes.Replace(body, []byte("## main...origin/main"), []byte("## devx...origin/devx"), 1)
			if len(changed) != len(body) || bytes.Equal(changed, body) {
				t.Fatal("interior fixture mutation did not preserve coordinates")
			}
			if err := os.WriteFile(path, changed, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAdapterFixture(t)
			path := fixture.installFixture(t, "session-project-a.jsonl")
			adapter := fixture.adapter(t, "v1")
			boundary, err := adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
			if err != nil {
				t.Fatal(err)
			}
			decodeBoundary(t, adapter, boundary)
			test.mutate(t, path)
			boundary, err = adapter.Freeze(context.Background(), discoverCandidate(t, adapter, "session-project-a"))
			if err != nil {
				t.Fatal(err)
			}
			_, report := decodeBoundary(t, adapter, boundary)
			if report.BoundaryRelation != source.BoundaryReplacement {
				t.Fatalf("mutation relation=%q", report.BoundaryRelation)
			}
			stored, found, err := fixture.catalog.GetSource("codex", "session-project-a")
			if err != nil || !found || !reflect.DeepEqual(stored.FrozenBoundary, boundary.Frozen) {
				t.Fatalf("replacement catalog boundary=%+v found=%v err=%v want=%+v", stored.FrozenBoundary, found, err, boundary.Frozen)
			}
		})
	}
}

func TestMissingAndDuplicateSegmentsBecomeTerminalIssues(t *testing.T) {
	fixture := newAdapterFixture(t)
	duplicate := func(path string) {
		body := fmt.Sprintf("{\"timestamp\":\"2026-08-31T09:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"duplicate\",\"cwd\":%q}}\n", filepath.ToSlash(fixture.projectA))
		if err := os.WriteFile(filepath.Join(fixture.sessions, path), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	duplicate("duplicate-1.jsonl")
	duplicate("duplicate-2.jsonl")
	missingPath := filepath.Join(fixture.sessions, "missing.jsonl")
	missingBody := fmt.Sprintf("{\"timestamp\":\"2026-08-31T11:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"missing\",\"cwd\":%q}}\n", filepath.ToSlash(fixture.projectA))
	if err := os.WriteFile(missingPath, []byte(missingBody), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := fixture.adapter(t, "v1")
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foundDuplicateIssue := false
	for _, issue := range discovery.Issues {
		if issue.SessionID == "duplicate" && issue.Code == "duplicate_segment" && issue.TerminalState == memory.Ambiguous {
			foundDuplicateIssue = true
		}
	}
	if !foundDuplicateIssue {
		t.Fatalf("duplicate terminal issue missing: %+v", discovery.Issues)
	}
	var missing source.Candidate
	for _, candidate := range discovery.Candidates {
		if candidate.SessionID == "missing" {
			missing = candidate
		}
	}
	if missing.SessionID == "" {
		t.Fatalf("missing candidate absent before deletion: %+v", discovery)
	}
	if err := os.Remove(missingPath); err != nil {
		t.Fatal(err)
	}
	boundary, err := adapter.Freeze(context.Background(), missing)
	if err != nil {
		t.Fatalf("missing segment should be terminal, not global error: %v", err)
	}
	if boundary.TerminalState != memory.Missing || len(boundary.Issues) != 1 || boundary.Issues[0].Code != "missing_segment" {
		t.Fatalf("missing terminal boundary=%+v", boundary)
	}
}

func TestFixtureTemplatesContainNoUnsanitizedAbsolutePaths(t *testing.T) {
	for _, name := range []string{"session-project-a.jsonl", "session-shared.jsonl", "session-malformed.jsonl"} {
		path := filepath.Join("..", "..", "..", "testdata", "zero-token", "codex", name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for lineNumber, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			if strings.Contains(line, "/Users/") {
				t.Fatalf("%s line %d contains an absolute user path", name, lineNumber+1)
			}
			if strings.Contains(line, "{{") {
				line = strings.NewReplacer(
					"{{PROJECT_A}}", "/project/a",
					"{{PROJECT_B}}", "/project/b",
					"{{LONG_USER_TEXT}}", "safe",
				).Replace(line)
			}
			var value any
			if err := json.Unmarshal([]byte(line), &value); err != nil && name != "session-malformed.jsonl" {
				t.Fatalf("%s line %d is not JSON: %v", name, lineNumber+1, err)
			}
		}
	}
}
