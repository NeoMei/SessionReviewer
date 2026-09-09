package opencode_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/cli"
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
	opencodesource "github.com/neomei/SessionReviewer/internal/source/opencode"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
	_ "modernc.org/sqlite"
)

func TestOpenCodeScanBuildsIndexAndPublishedRetainedConversation(t *testing.T) {
	const (
		projectID = "project-opencode-scan"
		sessionID = "ses_scanMixed"
	)
	projectRoot, sessionsRoot, dataRoot := t.TempDir(), t.TempDir(), t.TempDir()
	databasePath := filepath.Join(sessionsRoot, "opencode.db")
	t.Setenv("SESSION_REVIEWER_OPENCODE_DB", databasePath)
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

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY,directory TEXT NOT NULL,time_created INTEGER NOT NULL,parent_id TEXT,revert TEXT); CREATE TABLE message(id TEXT PRIMARY KEY,session_id TEXT NOT NULL,time_created INTEGER NOT NULL,data TEXT NOT NULL); CREATE TABLE part(id TEXT PRIMARY KEY,message_id TEXT NOT NULL,session_id TEXT NOT NULL,data TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC).UnixMilli()
	if _, err = db.Exec(`INSERT INTO session VALUES(?,?,?,NULL,NULL)`, sessionID, projectRoot, created); err != nil {
		t.Fatal(err)
	}
	user, _ := json.Marshal(map[string]any{"role": "user", "time": map[string]any{"created": created}})
	assistant, _ := json.Marshal(map[string]any{"role": "assistant", "time": map[string]any{"created": created + 1000, "completed": created + 2000}, "finish": "stop", "modelID": "model", "providerID": "provider", "tokens": map[string]any{"input": 10, "output": 5, "reasoning": 0, "cache": map[string]any{"read": 0, "write": 0}}})
	for _, m := range []struct {
		id      string
		created int64
		data    []byte
	}{{"msg_user", created, user}, {"msg_assistant", created + 1000, assistant}} {
		if _, err = db.Exec(`INSERT INTO message VALUES(?,?,?,?)`, m.id, sessionID, m.created, string(m.data)); err != nil {
			t.Fatal(err)
		}
	}
	for _, part := range []struct{ id, owner, body string }{{"prt_user", "msg_user", `{"type":"text","text":"Which test should I run?"}`}, {"prt_answer", "msg_assistant", `{"type":"text","text":"Run go test ./internal/source/opencode."}`}, {"prt_reasoning", "msg_assistant", `{"type":"reasoning","text":"HIDDEN_CHAIN_OF_THOUGHT"}`}, {"prt_tool", "msg_assistant", `{"type":"tool","tool":"bash","callID":"call_test","state":{"status":"completed","input":{"command":"go test ./..."},"output":"ok","metadata":{"exit":0}}}`}} {
		if _, err = db.Exec(`INSERT INTO part VALUES(?,?,?,?)`, part.id, part.owner, sessionID, part.body); err != nil {
			t.Fatal(err)
		}
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
	adapter, err := opencodesource.New(opencodesource.AdapterOptions{DatabasePath: databasePath, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 2, 1, 0, 0, time.UTC)
	scanOptions := scan.Options{
		ProjectID: projectID, Binding: binding, SessionsRoot: sessionsRoot, DataRoot: dataRoot,
		Adapters: []source.NamedAdapter{{Provider: "opencode", Adapter: adapter, Required: true}}, Catalog: catalog, Store: store, Workers: 1,
		Now: func() time.Time { return now }, Materialize: sessionview.Materialize, Reduce: projectview.Reduce,
		Probe: func(ctx context.Context, options projectprobe.Options) (memory.ProjectProbeState, memory.ProbeCheck, error) {
			state := memory.ProjectProbeState{SchemaVersion: memory.MemorySchemaVersion, ProjectID: projectID, CanonicalRoot: projectRoot, RemoteIdentityHashes: []string{}, VersionFiles: []memory.ProbeFile{}, RequiredProjectionFiles: []memory.ProbeFile{}, ProbeVersion: projectprobe.ProbeVersion, Diagnostics: []memory.Diagnostic{}}
			state.Digest, err = memory.ProjectProbeStateDigest(state)
			return state, memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: now.Format(time.RFC3339Nano), StateDigest: state.Digest, Available: true, Diagnostics: []memory.Diagnostic{}}, err
		},
	}
	result, err := scan.Run(context.Background(), scanOptions)
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
	if err != nil || len(index.Sessions) != 1 || index.Sessions[0].Provider != "opencode" || index.Sessions[0].SessionID != sessionID || index.Sessions[0].ProcessingState != sessionindex.ProcessingComplete {
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
	if err != nil || len(chain.TurnUnits) != 1 || chain.TurnUnits[0].UserMessage.VisibleExcerpt != "Which test should I run?" || len(chain.TurnUnits[0].AssistantMessages) != 1 || chain.TurnUnits[0].AssistantMessages[0].VisibleExcerpt != "Run go test ./internal/source/opencode." {
		t.Fatalf("retained chain=%+v err=%v", chain, err)
	}
	if len(chain.TurnUnits[0].Actions) != 1 || len(chain.TurnUnits[0].Results) != 2 {
		t.Fatalf("execution facts not retained: %+v", chain.TurnUnits[0])
	}
	if strings.Contains(string(chainBody), "HIDDEN_CHAIN_OF_THOUGHT") {
		t.Fatal("reasoning retained")
	}
	proof := memory.PublicationProof{Version: 4, ProjectID: projectID, GenerationID: result.GenerationID, ManifestDigest: prepared.ManifestDigest, ProjectViewDigest: prepared.ProjectViewDigest, ReviewSHA256: strings.Repeat("1", 64), HistorySHA256: strings.Repeat("2", 64), LedgerSHA256: strings.Repeat("3", 64), SessionIndexSHA256: strings.TrimPrefix(manifest.SessionIndexDigest, "sha256:"), JournalVerified: true}
	if err := store.CommitPublished(result.GenerationID, proof); err != nil {
		t.Fatal(err)
	}
	page, err := inspect.LoadConversationPage(context.Background(), inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "opencode", SessionID: sessionID, ExpectedGenerationID: result.GenerationID, Limit: 20})
	if err != nil || page.BodyAvailability != "source_full" || len(page.TurnUnits) != 1 || page.TurnUnits[0].UserMessage.VisibleExcerpt != "Which test should I run?" {
		t.Fatalf("source-backed page=%+v err=%v", page, err)
	}
	detail, err := inspect.LoadConversationPage(context.Background(), inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "opencode", SessionID: sessionID, ExpectedGenerationID: result.GenerationID, TurnUnitID: page.TurnUnits[0].TurnUnitID, Limit: 20})
	if err != nil || len(detail.Messages) != 2 || detail.Messages[1].Text == nil || *detail.Messages[1].Text != "Run go test ./internal/source/opencode." {
		t.Fatalf("source-backed detail=%+v err=%v", detail, err)
	}

	// Exercise public CLI ingress and published queries, preserving native mixed case.
	for _, command := range []string{"session-summary", "session-events", "conversation-chain"} {
		args := []string{"inspect", command, "--project-id", projectID, "--provider", "opencode", "--session-id", sessionID, "--expected-generation-id", result.GenerationID, "--data-dir", dataRoot, "--json"}
		if command != "session-summary" {
			args = append(args, "--limit", "20")
		}
		var stdout, stderr bytes.Buffer
		if code := cli.Run(args, &stdout, &stderr); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"session_id":"`+sessionID+`"`) {
			t.Fatalf("mixed-case CLI %s code=%d stdout=%s stderr=%s", command, code, stdout.String(), stderr.String())
		}
		if command == "conversation-chain" {
			stdout.Reset()
			args = append(args, "--turn-unit-id", page.TurnUnits[0].TurnUnitID)
			if code := cli.Run(args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "Run go test ./internal/source/opencode.") {
				t.Fatalf("mixed-case CLI selected messages code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
		}
	}

	// Refresh accepted accounting in both directions without changing messages.
	if _, err := db.Exec(`ALTER TABLE session ADD COLUMN tokens_input INTEGER`); err != nil {
		t.Fatal(err)
	}
	previousViewDigest := manifest.SessionViews[0].Digest
	for _, supplied := range []int{0, 10} {
		if _, err := db.Exec(`UPDATE session SET tokens_input=?`, supplied); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
		result, err = scan.Run(context.Background(), scanOptions)
		if err != nil || !result.Prepared {
			t.Fatalf("accepted usage rescan supplied=%d result=%+v err=%v", supplied, result, err)
		}
		prepared, manifest, err = store.LoadPrepared()
		if err != nil {
			t.Fatal(err)
		}
		viewBody, err := store.LoadObject(memorystore.ObjectSessionView, manifest.SessionViews[0].Digest)
		if err != nil {
			t.Fatal(err)
		}
		var view memory.SessionView
		if err := json.Unmarshal(viewBody, &view); err != nil {
			t.Fatal(err)
		}
		unknown := false
		for _, diagnostic := range view.Diagnostics {
			if diagnostic.Code == "usage_unavailable" {
				unknown = true
			}
		}
		if unknown != (supplied == 0) || manifest.SessionViews[0].Digest == previousViewDigest {
			t.Fatalf("accounting-dependent view did not refresh: supplied=%d view=%+v", supplied, view)
		}
		previousViewDigest = manifest.SessionViews[0].Digest
		refreshedBody, err := store.LoadObject(memorystore.ObjectConversationChain, manifest.ConversationChains[0].Digest)
		if err != nil {
			t.Fatal(err)
		}
		refreshed, err := conversationchain.Parse(refreshedBody)
		if err != nil || len(refreshed.TurnUnits) != 1 || refreshed.TurnUnits[0].UserMessage.SourceRef != chain.TurnUnits[0].UserMessage.SourceRef || refreshed.TurnUnits[0].AssistantMessages[0].SourceRef != chain.TurnUnits[0].AssistantMessages[0].SourceRef {
			t.Fatalf("usage refresh changed canonical conversation: %+v err=%v", refreshed, err)
		}
		proof.GenerationID, proof.ManifestDigest, proof.ProjectViewDigest = result.GenerationID, prepared.ManifestDigest, prepared.ProjectViewDigest
		proof.SessionIndexSHA256 = strings.TrimPrefix(manifest.SessionIndexDigest, "sha256:")
		if err := store.CommitPublished(result.GenerationID, proof); err != nil {
			t.Fatal(err)
		}
		refreshedPage, err := inspect.LoadConversationPage(context.Background(), inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "opencode", SessionID: sessionID, ExpectedGenerationID: result.GenerationID, Limit: 20})
		if err != nil || refreshedPage.BodyAvailability != "source_full" || len(refreshedPage.TurnUnits) != 1 {
			t.Fatalf("refreshed conversation query=%+v err=%v", refreshedPage, err)
		}
	}

	if _, err = db.Exec(`UPDATE part SET data='{"type":"text","text":"Whose test should I run?"}' WHERE id='prt_user'`); err != nil {
		t.Fatal(err)
	}

	retained, err := inspect.LoadConversationPage(context.Background(), inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "opencode", SessionID: sessionID, ExpectedGenerationID: result.GenerationID, Limit: 20})
	if err != nil || retained.BodyAvailability != "retained_excerpt" || len(retained.TurnUnits) != 1 || retained.TurnUnits[0].UserMessage.VisibleExcerpt != "Which test should I run?" {
		t.Fatalf("retained fallback=%+v err=%v", retained, err)
	}
}
