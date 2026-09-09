package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/inspect"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/projectprobe"
	"github.com/neomei/SessionReviewer/internal/projectview"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/scan"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/sessionview"
	"github.com/neomei/SessionReviewer/internal/source"
	claudesource "github.com/neomei/SessionReviewer/internal/source/claude"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

func TestClaudeScanBuildsIndexAndPublishedRetainedConversation(t *testing.T) {
	const (
		projectID = "project-claude-scan"
		sessionID = "99999999-9999-4999-8999-999999999999"
	)
	projectRoot, sessionsRoot, dataRoot := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("SESSION_REVIEWER_CLAUDE_SESSIONS_ROOT", sessionsRoot)
	mapping := config.ProjectMapping{ID: projectID, Root: projectRoot}
	binding, err := projectidentity.Resolve(mapping, projectRoot, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	mapping.AuthenticatedAliases = []config.AuthenticatedProjectAlias{binding.AuthenticatedAlias}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{mapping}}); err != nil {
		t.Fatal(err)
	}
	projectRoot = binding.CanonicalRoot
	child := strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(projectRoot)
	if err := os.Mkdir(filepath.Join(sessionsRoot, child), 0o700); err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{
		{"type": "user", "sessionId": sessionID, "cwd": projectRoot, "uuid": "user-1", "timestamp": "2026-09-09T02:00:00Z", "message": map[string]any{"role": "user", "content": "Which test should I run?"}},
		{"type": "assistant", "sessionId": sessionID, "cwd": projectRoot, "uuid": "assistant-1", "timestamp": "2026-09-09T02:00:01Z", "message": map[string]any{"id": "message-1", "role": "assistant", "model": "claude-sonnet-4-6", "content": []any{map[string]any{"type": "text", "text": "Run go test ./internal/source/claude."}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}}},
	}
	var transcript strings.Builder
	for _, row := range rows {
		body, _ := json.Marshal(row)
		transcript.Write(body)
		transcript.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(sessionsRoot, child, sessionID+".jsonl"), []byte(transcript.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	catalog, err := sourcecatalog.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := redact.Default()
	adapter, err := claudesource.New(claudesource.AdapterOptions{SessionsRoot: sessionsRoot, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r, AdapterVersion: "claude-jsonl-v1"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 2, 1, 0, 0, time.UTC)
	result, err := scan.Run(context.Background(), scan.Options{
		ProjectID: projectID, Binding: binding, SessionsRoot: sessionsRoot, DataRoot: dataRoot,
		Adapters: []source.NamedAdapter{{Provider: "claude", Adapter: adapter, Required: true}}, Catalog: catalog, Store: store, Workers: 1,
		Now: func() time.Time { return now }, Materialize: sessionview.Materialize, Reduce: projectview.Reduce,
		Probe: func(ctx context.Context, options projectprobe.Options) (memory.ProjectProbeState, memory.ProbeCheck, error) {
			state := memory.ProjectProbeState{SchemaVersion: memory.MemorySchemaVersion, ProjectID: projectID, CanonicalRoot: projectRoot, RemoteIdentityHashes: []string{}, VersionFiles: []memory.ProbeFile{}, RequiredProjectionFiles: []memory.ProbeFile{}, ProbeVersion: projectprobe.ProbeVersion, Diagnostics: []memory.Diagnostic{}}
			state.Digest, err = memory.ProjectProbeStateDigest(state)
			return state, memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: now.Format(time.RFC3339Nano), StateDigest: state.Digest, Available: true, Diagnostics: []memory.Diagnostic{}}, err
		},
	})
	if err != nil || !result.Prepared || result.IndexedSessions != 1 {
		t.Fatalf("scan result=%+v err=%v", result, err)
	}
	prepared, manifest, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	indexBody, err := store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
	if err != nil {
		t.Fatal(err)
	}
	index, err := sessionindex.Parse(indexBody)
	if err != nil || len(index.Sessions) != 1 || index.Sessions[0].Provider != "claude" || index.Sessions[0].SessionID != sessionID || index.Sessions[0].ProcessingState != sessionindex.ProcessingComplete {
		t.Fatalf("index=%+v err=%v", index, err)
	}
	if len(manifest.ConversationChains) != 1 {
		t.Fatalf("conversation dependencies=%+v", manifest.ConversationChains)
	}
	chainBody, err := store.LoadObject(memorystore.ObjectConversationChain, manifest.ConversationChains[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := conversationchain.Parse(chainBody)
	if err != nil || len(chain.TurnUnits) != 1 || chain.TurnUnits[0].UserMessage.VisibleExcerpt != "Which test should I run?" || len(chain.TurnUnits[0].AssistantMessages) != 1 || chain.TurnUnits[0].AssistantMessages[0].VisibleExcerpt != "Run go test ./internal/source/claude." {
		t.Fatalf("retained chain=%+v err=%v", chain, err)
	}
	proof := memory.PublicationProof{Version: 4, ProjectID: projectID, GenerationID: result.GenerationID, ManifestDigest: prepared.ManifestDigest, ProjectViewDigest: prepared.ProjectViewDigest, ReviewSHA256: strings.Repeat("1", 64), HistorySHA256: strings.Repeat("2", 64), LedgerSHA256: strings.Repeat("3", 64), SessionIndexSHA256: strings.TrimPrefix(manifest.SessionIndexDigest, "sha256:"), JournalVerified: true}
	if err := store.CommitPublished(result.GenerationID, proof); err != nil {
		t.Fatal(err)
	}
	page, err := inspect.LoadConversationPage(context.Background(), inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "claude", SessionID: sessionID, ExpectedGenerationID: result.GenerationID, Limit: 20})
	if err != nil || page.BodyAvailability != "source_full" || len(page.TurnUnits) != 1 || page.TurnUnits[0].UserMessage.VisibleExcerpt != "Which test should I run?" {
		t.Fatalf("source-backed page=%+v err=%v", page, err)
	}
	detail, err := inspect.LoadConversationPage(context.Background(), inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "claude", SessionID: sessionID, ExpectedGenerationID: result.GenerationID, TurnUnitID: page.TurnUnits[0].TurnUnitID, Limit: 20})
	if err != nil || len(detail.Messages) != 2 || detail.Messages[1].Text == nil || *detail.Messages[1].Text != "Run go test ./internal/source/claude." {
		t.Fatalf("source-backed detail=%+v err=%v", detail, err)
	}
	tampered := strings.Replace(transcript.String(), "Which test", "Whose test", 1)
	if err := os.WriteFile(filepath.Join(sessionsRoot, child, sessionID+".jsonl"), []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	retained, err := inspect.LoadConversationPage(context.Background(), inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "claude", SessionID: sessionID, ExpectedGenerationID: result.GenerationID, Limit: 20})
	if err != nil || retained.BodyAvailability != "retained_excerpt" || len(retained.TurnUnits) != 1 || retained.TurnUnits[0].UserMessage.VisibleExcerpt != "Which test should I run?" {
		t.Fatalf("retained fallback=%+v err=%v", retained, err)
	}
}
