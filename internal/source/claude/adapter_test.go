package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

const testSessionID = "123e4567-e89b-12d3-a456-426614174000"

func TestAdapterLifecycleProducesClaudeUsageAndVisibleConversation(t *testing.T) {
	fixture := newFixture(t)
	question := claudeLine(t, map[string]any{
		"type": "user", "sessionId": testSessionID, "cwd": fixture.projectRoot,
		"uuid": "user-1", "timestamp": "2026-09-09T01:00:00Z",
		"message": map[string]any{"role": "user", "content": "How do I verify the build?"},
	})
	answer := claudeLine(t, map[string]any{
		"type": "assistant", "sessionId": testSessionID, "cwd": fixture.projectRoot,
		"uuid": "assistant-1", "timestamp": "2026-09-09T01:00:01Z",
		"message": map[string]any{
			"id": "message-1", "role": "assistant", "model": "claude-sonnet-4-6",
			"content": []any{
				map[string]any{"type": "thinking", "thinking": "private chain of thought"},
				map[string]any{"type": "text", "text": "Run the focused package tests."},
			},
			"usage": map[string]any{
				"input_tokens": 100, "cache_read_input_tokens": 20,
				"cache_creation_input_tokens": 30, "output_tokens": 12,
			},
		},
	})
	writeSession(t, fixture, question+answer+`{"type":"assistant"}`)

	adapter := fixture.adapter(t)
	discovery, err := adapter.Discover(context.Background())
	if err != nil || len(discovery.Candidates) != 1 || len(discovery.Issues) != 0 {
		t.Fatalf("Discover() candidates=%+v issues=%+v err=%v", discovery.Candidates, discovery.Issues, err)
	}
	candidate := discovery.Candidates[0]
	if candidate.Provider != "claude" || candidate.SessionID != testSessionID || candidate.InitialCWD != fixture.projectRoot || candidate.StartedAt != "2026-09-09T01:00:00Z" {
		t.Fatalf("candidate=%+v", candidate)
	}
	boundary, err := adapter.Freeze(context.Background(), candidate)
	if err != nil || boundary.TerminalState != memory.Indexed || boundary.Frozen.Location.JSONL == nil || boundary.Frozen.Location.JSONL.Line != 2 {
		t.Fatalf("Freeze() boundary=%+v err=%v", boundary, err)
	}
	var observations []memory.ObservationRevision
	report, err := adapter.Decode(context.Background(), boundary, func(value memory.ObservationRevision) error {
		observations = append(observations, value)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.BoundaryRelation != source.BoundaryInitial || report.TerminalState != memory.Indexed || report.RecordCount == nil || *report.RecordCount != 2 {
		t.Fatalf("decode report=%+v", report)
	}
	if len(observations) != 1 || observations[0].Operation != "session_started" || observations[0].Key.ProjectID != fixture.projectID {
		t.Fatalf("observations=%+v", observations)
	}
	wantUsage := accounting.SessionUsage{
		StartedAt: "2026-09-09T01:00:00Z", EndedAt: "2026-09-09T01:00:01Z", DurationMS: 1000,
		Models: []accounting.ModelUsage{{Model: "claude-sonnet-4-6", TokenUsage: accounting.TokenUsage{
			InputTokens: 150, CachedInputTokens: 20, CacheWriteInputTokens: 30,
			OutputTokens: 12, TotalTokens: 162,
		}}},
		TotalTokens: 162,
	}
	if !reflect.DeepEqual(report.ProposedSource.Usage, wantUsage) || !reflect.DeepEqual(report.ProposedSource.ProjectIDs, []string{fixture.projectID}) {
		t.Fatalf("source=%+v", report.ProposedSource)
	}
	visible := adapter.(source.VisibleReader)
	messages, coverage, err := visible.ReadVisiblePrefix(context.Background(), report.ProposedSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != conversationchain.RoleUser || messages[0].Text != "How do I verify the build?" ||
		messages[1].Role != conversationchain.RoleAssistant || messages[1].Text != "Run the focused package tests." || strings.Contains(messages[1].Text, "private") {
		t.Fatalf("messages=%+v", messages)
	}
	if !coverage.Complete || coverage.SourceRecords != 2 || coverage.VisibleMessages != 2 || coverage.CapturedMessages != 2 {
		t.Fatalf("coverage=%+v", coverage)
	}
	body, err := adapter.Read(context.Background(), observations[0].Ref, source.MaxReadBytes)
	if err != nil || !strings.Contains(string(body), "How do I verify the build?") {
		t.Fatalf("Read() body=%q err=%v", body, err)
	}
}

func TestAdapterCountsRepeatedStreamingUsageOncePerMessageID(t *testing.T) {
	fixture := newFixture(t)
	lines := []map[string]any{{
		"type": "user", "sessionId": testSessionID, "cwd": fixture.projectRoot,
		"uuid": "user-1", "timestamp": "2026-09-09T01:00:00Z",
		"message": map[string]any{"role": "user", "content": "stream the answer"},
	}}
	for index, content := range []any{
		[]any{map[string]any{"type": "thinking", "thinking": "private"}},
		[]any{map[string]any{"type": "text", "text": "visible"}},
		[]any{map[string]any{"type": "tool_use", "id": "tool-1", "name": "Read", "input": map[string]any{}}},
	} {
		lines = append(lines, map[string]any{
			"type": "assistant", "sessionId": testSessionID, "cwd": fixture.projectRoot,
			"uuid": fmt.Sprintf("assistant-%d", index), "timestamp": fmt.Sprintf("2026-09-09T01:00:0%dZ", index+1),
			"message": map[string]any{
				"id": "message-streamed", "role": "assistant", "model": "claude-sonnet-4-6", "content": content,
				"usage": map[string]any{"input_tokens": 100, "cache_read_input_tokens": 20, "cache_creation_input_tokens": 30, "output_tokens": 12},
			},
		})
	}
	var body strings.Builder
	for _, line := range lines {
		body.WriteString(claudeLine(t, line))
	}
	writeSession(t, fixture, body.String())
	report := decodeFixtureReport(t, fixture)
	want := accounting.TokenUsage{InputTokens: 150, CachedInputTokens: 20, CacheWriteInputTokens: 30, OutputTokens: 12, TotalTokens: 162}
	if report.TerminalState != memory.Indexed || len(report.ProposedSource.Usage.Models) != 1 || report.ProposedSource.Usage.Models[0].TokenUsage != want || report.ProposedSource.Usage.TotalTokens != 162 {
		t.Fatalf("streamed usage was not deduplicated: report=%+v", report)
	}
}

func TestAdapterRejectsConflictingUsageForOneMessageID(t *testing.T) {
	fixture := newFixture(t)
	body := claudeLine(t, map[string]any{
		"type": "user", "sessionId": testSessionID, "cwd": fixture.projectRoot, "uuid": "user-1", "timestamp": "2026-09-09T01:00:00Z",
		"message": map[string]any{"role": "user", "content": "question"},
	})
	for index, input := range []int{10, 11} {
		body += claudeLine(t, map[string]any{
			"type": "assistant", "sessionId": testSessionID, "cwd": fixture.projectRoot, "uuid": fmt.Sprintf("assistant-%d", index), "timestamp": fmt.Sprintf("2026-09-09T01:00:0%dZ", index+1),
			"message": map[string]any{"id": "message-conflict", "role": "assistant", "model": "claude-sonnet-4-6", "content": "answer", "usage": map[string]any{"input_tokens": input, "output_tokens": 1}},
		})
	}
	writeSession(t, fixture, body)
	report := decodeFixtureReport(t, fixture)
	if report.TerminalState != memory.Unreadable || report.UndecodableRecords != 1 || len(report.ProposedSource.Usage.Models) != 0 || report.ProposedSource.Usage.TotalTokens != 0 {
		t.Fatalf("conflicting usage identity was accepted: report=%+v", report)
	}
	found := false
	for _, diagnostic := range report.Diagnostics {
		found = found || diagnostic.Code == "conflicting_accounting_identity"
	}
	if !found {
		t.Fatalf("missing explicit accounting conflict diagnostic: %+v", report.Diagnostics)
	}
}

func TestAdapterFallsBackToEnvelopeUUIDForUsageIdentity(t *testing.T) {
	fixture := newFixture(t)
	body := claudeLine(t, map[string]any{
		"type": "user", "sessionId": testSessionID, "cwd": fixture.projectRoot, "uuid": "user-1", "timestamp": "2026-09-09T01:00:00Z",
		"message": map[string]any{"role": "user", "content": "question"},
	})
	for index := 0; index < 2; index++ {
		body += claudeLine(t, map[string]any{
			"type": "assistant", "sessionId": testSessionID, "cwd": fixture.projectRoot, "uuid": "assistant-shared", "timestamp": fmt.Sprintf("2026-09-09T01:00:0%dZ", index+1),
			"message": map[string]any{"role": "assistant", "model": "claude-sonnet-4-6", "content": "answer", "usage": map[string]any{"input_tokens": 10, "output_tokens": 1}},
		})
	}
	writeSession(t, fixture, body)
	report := decodeFixtureReport(t, fixture)
	want := accounting.TokenUsage{InputTokens: 10, OutputTokens: 1, TotalTokens: 11}
	if report.TerminalState != memory.Indexed || len(report.ProposedSource.Usage.Models) != 1 || report.ProposedSource.Usage.Models[0].TokenUsage != want {
		t.Fatalf("envelope UUID did not deduplicate usage: report=%+v", report)
	}
}

func TestAdapterReportsUnavailableRootAndIgnoresUnboundProjects(t *testing.T) {
	projectRoot := t.TempDir()
	identity := physicalIdentity(t, projectRoot)
	catalog, err := sourcecatalog.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	r := redact.Default()
	adapterValue, err := New(AdapterOptions{
		SessionsRoot: filepath.Join(t.TempDir(), "missing"),
		Bindings:     []projectidentity.Binding{{ProjectID: "project-bound", CanonicalRoot: projectRoot, RootIdentity: identity}},
		Catalog:      catalog, Redactor: &r, AdapterVersion: "claude-jsonl-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Discover(context.Background()); !errors.Is(err, source.ErrProviderUnavailable) {
		t.Fatalf("Discover() error=%v, want provider unavailable", err)
	}
}

func TestAdapterClassifiesAuthenticatedAppendAgainstCatalogSnapshot(t *testing.T) {
	fixture := newFixture(t)
	first := claudeLine(t, map[string]any{
		"type": "user", "sessionId": testSessionID, "cwd": fixture.projectRoot,
		"uuid": "user-1", "timestamp": "2026-09-09T01:00:00Z",
		"message": map[string]any{"role": "user", "content": "first"},
	})
	first += claudeLine(t, map[string]any{
		"type": "assistant", "sessionId": testSessionID, "cwd": fixture.projectRoot,
		"uuid": "assistant-0", "timestamp": "2026-09-09T01:00:00.500Z",
		"message": map[string]any{"role": "assistant", "model": "claude-sonnet-4-6", "content": "ack", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}},
	})
	writeSession(t, fixture, first)
	adapter := fixture.adapter(t)
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := adapter.Freeze(context.Background(), discovery.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	initial, err := adapter.Decode(context.Background(), boundary, func(memory.ObservationRevision) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.catalog.ApplyBatch([]sourcecatalog.BatchMutation{{Relation: initial.BoundaryRelation, ExpectedDigest: initial.ExpectedCatalogDigest, Desired: initial.ProposedSource}}); err != nil {
		t.Fatal(err)
	}
	second := claudeLine(t, map[string]any{
		"type": "assistant", "sessionId": testSessionID, "cwd": fixture.projectRoot,
		"uuid": "assistant-1", "timestamp": "2026-09-09T01:00:01Z",
		"message": map[string]any{"role": "assistant", "model": "claude-sonnet-4-6", "content": "second", "usage": map[string]any{"input_tokens": 2, "output_tokens": 1}},
	})
	writeSession(t, fixture, first+second)
	adapter = fixture.adapter(t)
	discovery, err = adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	boundary, err = adapter.Freeze(context.Background(), discovery.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	appended, err := adapter.Decode(context.Background(), boundary, func(memory.ObservationRevision) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if appended.BoundaryRelation != source.BoundaryAppend || appended.ExpectedCatalogDigest == "" || appended.ProposedSource.SourceIdentity != initial.ProposedSource.SourceIdentity {
		t.Fatalf("append report=%+v initial=%+v", appended, initial)
	}
}

type fixture struct {
	projectID, projectRoot, sessionsRoot, child string
	catalog                                     *sourcecatalog.Catalog
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	projectRoot, sessionsRoot, dataRoot := t.TempDir(), t.TempDir(), t.TempDir()
	child, err := encodedProjectKey(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sessionsRoot, child), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog, err := sourcecatalog.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	return fixture{projectID: "project-claude", projectRoot: projectRoot, sessionsRoot: sessionsRoot, child: child, catalog: catalog}
}

func (fixture fixture) adapter(t *testing.T) source.Adapter {
	t.Helper()
	r := redact.Default()
	value, err := New(AdapterOptions{
		SessionsRoot: fixture.sessionsRoot,
		Bindings:     []projectidentity.Binding{{ProjectID: fixture.projectID, CanonicalRoot: fixture.projectRoot, RootIdentity: physicalIdentity(t, fixture.projectRoot)}},
		Catalog:      fixture.catalog, Redactor: &r, AdapterVersion: "claude-jsonl-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func physicalIdentity(t *testing.T, path string) pathguard.IdentityToken {
	t.Helper()
	directory, err := pathguard.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	identity, err := directory.PhysicalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func writeSession(t *testing.T, fixture fixture, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.sessionsRoot, fixture.child, testSessionID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func claudeLine(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body) + "\n"
}

func decodeFixtureReport(t *testing.T, fixture fixture) source.DecodeReport {
	t.Helper()
	adapter := fixture.adapter(t)
	discovery, err := adapter.Discover(context.Background())
	if err != nil || len(discovery.Candidates) != 1 {
		t.Fatalf("discover fixture: candidates=%+v err=%v", discovery.Candidates, err)
	}
	boundary, err := adapter.Freeze(context.Background(), discovery.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	report, err := adapter.Decode(context.Background(), boundary, func(memory.ObservationRevision) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	result, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
