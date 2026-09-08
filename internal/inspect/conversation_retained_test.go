package inspect_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/cli"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/inspect"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

func TestRetainedConversationSurvivesRemovalRescanAndRestoration(t *testing.T) {
	projectRoot, vaultRoot, dataRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = projectRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, output)
		}
	}
	const projectID = "project-retained-query"
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: projectID, Root: projectRoot, VaultRoot: vaultRoot, VaultReviewPath: "Projects/Retained/Session Review", VaultCaseMode: platform.CaseSensitive}}}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	const sessionID = "77777777-7777-4777-8777-777777777777"
	records := []map[string]any{
		{"timestamp": "2026-09-08T00:00:00Z", "type": "session_meta", "payload": map[string]any{"id": sessionID, "cwd": projectRoot}},
		{"timestamp": "2026-09-08T00:00:01Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Can I read the retained answer?"}}}},
		{"timestamp": "2026-09-08T00:00:02Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Retained answer."}}}},
	}
	var source bytes.Buffer
	for _, record := range records {
		if err := json.NewEncoder(&source).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	sourcePath := filepath.Join(sessionsRoot, "rollout-2026-09-08T00-00-00-"+sessionID+".jsonl")
	if err := os.WriteFile(sourcePath, source.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	runScan := func() string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := cli.Run([]string{"scan", "--project-id", projectID, "--sessions-root", sessionsRoot, "--data-dir", dataRoot, "--json"}, &stdout, &stderr); code != 0 {
			t.Fatalf("scan code %d: %s %s", code, stdout.String(), stderr.String())
		}
		store, err := memorystore.OpenReadOnly(dataRoot, projectID)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		generation, _, err := store.LoadPublished()
		if err != nil {
			t.Fatal(err)
		}
		return generation
	}
	t.Setenv("SESSION_REVIEWER_SESSIONS_ROOT", sessionsRoot)
	generation := runScan()
	request := inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "codex", SessionID: sessionID, ExpectedGenerationID: generation, Limit: 20}
	index, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || len(index.TurnUnits) != 1 {
		t.Fatalf("source index: page=%+v err=%v", index, err)
	}
	originalView := index.SessionViewDigest
	request.TurnUnitID = index.TurnUnits[0].TurnUnitID
	available, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || available.BodyAvailability != "source_full" || len(available.Messages) != 2 || available.Messages[1].Text == nil || *available.Messages[1].Text != "Retained answer." {
		t.Fatalf("available body: page=%+v err=%v", available, err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	beforeRescan, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || beforeRescan.BodyAvailability != "retained_excerpt" || beforeRescan.Messages[1].Text != nil {
		t.Fatalf("retained before rescan: page=%+v err=%v", beforeRescan, err)
	}
	request.ExpectedGenerationID = runScan()
	afterRescan, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || afterRescan.BodyAvailability != "retained_excerpt" || afterRescan.SessionViewDigest == originalView || afterRescan.EvidenceSessionViewDigest == nil || *afterRescan.EvidenceSessionViewDigest != originalView || afterRescan.Messages[1].Text != nil || afterRescan.Messages[1].Phase != nil {
		t.Fatalf("retained after rescan: page=%+v err=%v", afterRescan, err)
	}
	if err := os.WriteFile(sourcePath, source.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	request.ExpectedGenerationID = runScan()
	restored, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || restored.BodyAvailability != "source_full" || restored.SessionViewDigest != originalView || restored.Messages[1].Text == nil || *restored.Messages[1].Text != "Retained answer." {
		t.Fatalf("restored source: page=%+v err=%v", restored, err)
	}
}

func TestHistoricalConversationSelectionAfterAppendUsesExactRetainedSnapshot(t *testing.T) {
	projectRoot, vaultRoot, dataRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = projectRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, output)
		}
	}
	const projectID = "project-historical-query"
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: projectID, Root: projectRoot, VaultRoot: vaultRoot, VaultReviewPath: "Projects/Historical/Session Review", VaultCaseMode: platform.CaseSensitive}}}); err != nil {
		t.Fatal(err)
	}
	const sessionID = "67676767-6767-4767-8767-676767676767"
	records := []map[string]any{
		{"timestamp": "2026-09-08T00:00:00Z", "type": "session_meta", "payload": map[string]any{"id": sessionID, "cwd": projectRoot}},
		{"timestamp": "2026-09-08T00:00:01Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Same turn identity?"}}}},
		{"timestamp": "2026-09-08T00:00:02Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Historical answer."}}}},
	}
	encode := func(values []map[string]any) []byte {
		t.Helper()
		var body bytes.Buffer
		for _, record := range values {
			if err := json.NewEncoder(&body).Encode(record); err != nil {
				t.Fatal(err)
			}
		}
		return body.Bytes()
	}
	sourcePath := filepath.Join(sessionsRoot, "rollout-2026-09-08T00-00-00-"+sessionID+".jsonl")
	if err := os.WriteFile(sourcePath, encode(records), 0o600); err != nil {
		t.Fatal(err)
	}
	runScan := func() string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := cli.Run([]string{"scan", "--project-id", projectID, "--sessions-root", sessionsRoot, "--data-dir", dataRoot, "--json"}, &stdout, &stderr); code != 0 {
			t.Fatalf("scan code %d: %s %s", code, stdout.String(), stderr.String())
		}
		store, err := memorystore.OpenReadOnly(dataRoot, projectID)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		generation, _, err := store.LoadPublished()
		if err != nil {
			t.Fatal(err)
		}
		return generation
	}
	t.Setenv("SESSION_REVIEWER_SESSIONS_ROOT", sessionsRoot)
	firstGeneration := runScan()
	base := inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "codex", SessionID: sessionID, ExpectedGenerationID: firstGeneration, Limit: 1}
	firstIndex, err := inspect.LoadConversationPage(context.Background(), base)
	if err != nil || len(firstIndex.TurnUnits) != 1 {
		t.Fatalf("first index=%+v err=%v", firstIndex, err)
	}
	oldView, turnID := firstIndex.SessionViewDigest, firstIndex.TurnUnits[0].TurnUnitID

	records = append(records, map[string]any{"timestamp": "2026-09-08T00:00:03Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Current revised answer."}}}})
	if err := os.WriteFile(sourcePath, encode(records), 0o600); err != nil {
		t.Fatal(err)
	}
	base.ExpectedGenerationID = runScan()
	current, err := inspect.LoadConversationPage(context.Background(), base)
	if err != nil || current.SessionViewDigest == oldView || current.BodyAvailability != "source_full" || len(current.TurnUnits) != 1 || current.TurnUnits[0].TurnUnitID != turnID || current.TurnUnits[0].AssistantMessageCount != 2 {
		t.Fatalf("default current index=%+v err=%v", current, err)
	}
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	oldViewBody, err := store.LoadObject(memorystore.ObjectSessionView, oldView)
	if err != nil {
		t.Fatal(err)
	}
	var unreferencedView memory.SessionView
	if err := json.Unmarshal(oldViewBody, &unreferencedView); err != nil {
		t.Fatal(err)
	}
	unreferencedView.Diagnostics = append(unreferencedView.Diagnostics, memory.Diagnostic{Code: "unreferenced-test-view"})
	unreferencedView.Digest, err = memory.SessionViewDigest(unreferencedView)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionView(unreferencedView); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	beforeReads := snapshotHistoricalReadTrees(t, dataRoot, projectRoot, vaultRoot, sessionsRoot)

	historical := base
	historical.SessionViewDigest = oldView
	historicalIndex, err := inspect.LoadConversationPage(context.Background(), historical)
	if err != nil || historicalIndex.SessionViewDigest != oldView || historicalIndex.EvidenceSessionViewDigest != nil || historicalIndex.BodyAvailability != "retained_excerpt" || len(historicalIndex.TurnUnits) != 1 || historicalIndex.NextCursor != nil {
		t.Fatalf("historical index=%+v err=%v", historicalIndex, err)
	}
	historical.TurnUnitID, historical.Limit = turnID, 1
	historicalDetail, err := inspect.LoadConversationPage(context.Background(), historical)
	if err != nil || len(historicalDetail.Messages) != 1 || historicalDetail.NextCursor == nil || historicalDetail.Messages[0].Text != nil {
		t.Fatalf("historical detail=%+v err=%v", historicalDetail, err)
	}
	historical.MessageCursor = *historicalDetail.NextCursor
	historicalAnswer, err := inspect.LoadConversationPage(context.Background(), historical)
	if err != nil || len(historicalAnswer.Messages) != 1 || historicalAnswer.Messages[0].VisibleExcerpt != "Historical answer." || historicalAnswer.Messages[0].Text != nil || historicalAnswer.Messages[0].SourceRef.RecordOrdinal != 3 {
		t.Fatalf("historical answer crossed snapshots: page=%+v err=%v", historicalAnswer, err)
	}
	currentDetail := base
	currentDetail.TurnUnitID, currentDetail.Limit = turnID, 1
	currentFull := currentDetail
	currentFull.Limit = 64
	currentAnswer, err := inspect.LoadConversationPage(context.Background(), currentFull)
	if err != nil || len(currentAnswer.Messages) != 3 || currentAnswer.Messages[2].VisibleExcerpt != "Current revised answer." || currentAnswer.Messages[2].Text == nil || *currentAnswer.Messages[2].Text != "Current revised answer." {
		t.Fatalf("current answer crossed snapshots: page=%+v err=%v", currentAnswer, err)
	}
	currentFirst, err := inspect.LoadConversationPage(context.Background(), currentDetail)
	if err != nil || currentFirst.NextCursor == nil {
		t.Fatalf("current detail=%+v err=%v", currentFirst, err)
	}
	historical.MessageCursor = *currentFirst.NextCursor
	if _, err := inspect.LoadConversationPage(context.Background(), historical); retainedErrorCode(err) != "stale_cursor" {
		t.Fatalf("current cursor crossed into historical view: %v", err)
	}
	currentDetail.MessageCursor = *historicalDetail.NextCursor
	if _, err := inspect.LoadConversationPage(context.Background(), currentDetail); retainedErrorCode(err) != "stale_cursor" {
		t.Fatalf("historical cursor crossed into current view: %v", err)
	}
	for name, mutate := range map[string]func(*inspect.ConversationRequest){
		"wrong project":         func(request *inspect.ConversationRequest) { request.ProjectID = "project-foreign" },
		"wrong provider":        func(request *inspect.ConversationRequest) { request.Provider = "claude" },
		"wrong Session":         func(request *inspect.ConversationRequest) { request.SessionID = "session-foreign" },
		"unreferenced CAS view": func(request *inspect.ConversationRequest) { request.SessionViewDigest = unreferencedView.Digest },
	} {
		t.Run(name, func(t *testing.T) {
			request := historical
			request.MessageCursor = ""
			mutate(&request)
			if _, err := inspect.LoadConversationPage(context.Background(), request); retainedErrorCode(err) != "invalid_argument" {
				t.Fatalf("selected foreign or unreferenced view error=%v", err)
			}
		})
	}
	if afterReads := snapshotHistoricalReadTrees(t, dataRoot, projectRoot, vaultRoot, sessionsRoot); !reflect.DeepEqual(beforeReads, afterReads) {
		t.Fatal("historical conversation reads mutated synthetic project, Vault, private state, or source bytes")
	}
}

func snapshotHistoricalReadTrees(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for rootIndex, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			result[fmt.Sprintf("%d/%s", rootIndex, relative)] = string(body)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

type removedRetainedFixture struct {
	request    inspect.ConversationRequest
	dataRoot   string
	projectID  string
	generation memory.GenerationManifest
	memoryRoot string
}

func newRemovedRetainedFixture(t *testing.T) removedRetainedFixture {
	t.Helper()
	projectRoot, vaultRoot, dataRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = projectRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, output)
		}
	}
	const projectID = "project-retained-tamper"
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: projectID, Root: projectRoot, VaultRoot: vaultRoot, VaultReviewPath: "Projects/Retained/Tamper/Session Review", VaultCaseMode: platform.CaseSensitive}}}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	const sessionID = "88888888-8888-4888-8888-888888888888"
	records := []map[string]any{
		{"timestamp": "2026-09-08T01:00:00Z", "type": "session_meta", "payload": map[string]any{"id": sessionID, "cwd": projectRoot}},
		{"timestamp": "2026-09-08T01:00:00.500Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "<environment_context>ambient</environment_context>"}}}},
		{"timestamp": "2026-09-08T01:00:01Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Retain this question."}}}},
		{"timestamp": "2026-09-08T01:00:02Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Retain this answer."}}}},
	}
	var source bytes.Buffer
	for _, record := range records {
		if err := json.NewEncoder(&source).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	sourcePath := filepath.Join(sessionsRoot, "rollout-2026-09-08T01-00-00-"+sessionID+".jsonl")
	if err := os.WriteFile(sourcePath, source.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	runScan := func() string {
		var stdout, stderr bytes.Buffer
		if code := cli.Run([]string{"scan", "--project-id", projectID, "--sessions-root", sessionsRoot, "--data-dir", dataRoot, "--json"}, &stdout, &stderr); code != 0 {
			t.Fatalf("scan code %d: %s %s", code, stdout.String(), stderr.String())
		}
		store, err := memorystore.OpenReadOnly(dataRoot, projectID)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		generation, _, err := store.LoadPublished()
		if err != nil {
			t.Fatal(err)
		}
		return generation
	}
	t.Setenv("SESSION_REVIEWER_SESSIONS_ROOT", sessionsRoot)
	_ = runScan()
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	generationID := runScan()
	store, err := memorystore.OpenReadOnly(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := store.LoadPublished()
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	request := inspect.ConversationRequest{DataRoot: dataRoot, ProjectID: projectID, Provider: "codex", SessionID: sessionID, ExpectedGenerationID: generationID, Limit: 20}
	positive, err := inspect.LoadConversationPage(context.Background(), request)
	if err != nil || positive.BodyAvailability != "retained_excerpt" || len(positive.TurnUnits) != 1 {
		t.Fatalf("retained positive control: page=%+v err=%v", positive, err)
	}
	return removedRetainedFixture{request: request, dataRoot: dataRoot, projectID: projectID, generation: manifest, memoryRoot: filepath.Join(dataRoot, "projects", projectID, "memory-v1")}
}

func TestRetainedConversationLegacyCoverageRemainsQueryableAsUnknown(t *testing.T) {
	fixture := newRemovedRetainedFixture(t)
	store, err := memorystore.Open(fixture.dataRoot, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dependency := fixture.generation.RetainedConversationChains[0]
	body, err := store.LoadObject(memorystore.ObjectConversationChain, dependency.Digest)
	if err != nil {
		t.Fatal(err)
	}
	document, err := conversationchain.Parse(body)
	if err != nil || document.Coverage.SourceMessages <= document.Coverage.CapturedMessages {
		t.Fatalf("legacy fixture lacks unknown ambient gap: coverage=%+v err=%v", document.Coverage, err)
	}
	document.MaterializationCoverageV1 = nil
	body, err = conversationchain.Render(document)
	if err != nil {
		t.Fatal(err)
	}
	document, err = conversationchain.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutConversationChain(document); err != nil {
		t.Fatal(err)
	}
	generation := fixture.generation
	generation.RetainedConversationChains[0].Digest = document.Digest
	fixture.request.ExpectedGenerationID = advanceRetainedGeneration(t, store, generation, "generation-retained-legacy")
	page, err := inspect.LoadConversationPage(context.Background(), fixture.request)
	if err != nil || page.Coverage.DiagnosticsAvailable == nil || *page.Coverage.DiagnosticsAvailable || page.Coverage.ContextMessages != 0 || page.Coverage.OrphanMessages != 0 || page.Coverage.CapturedMessages >= page.Coverage.VisibleMessages {
		t.Fatalf("legacy retained query invented diagnostics: page=%+v err=%v", page, err)
	}
	if _, err := inspect.RenderConversationPage(page); err != nil {
		t.Fatalf("legacy retained page is not renderable: %v", err)
	}
}

func TestRetainedConversationTwoCompatiblePublishedRootsAreAmbiguous(t *testing.T) {
	fixture := newRemovedRetainedFixture(t)
	store, err := memorystore.Open(fixture.dataRoot, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := fixture.generation.RetainedConversationChains[0]
	viewBody, err := store.LoadObject(memorystore.ObjectSessionView, first.SessionViewDigest)
	if err != nil {
		t.Fatal(err)
	}
	var secondView memory.SessionView
	if err := json.Unmarshal(viewBody, &secondView); err != nil {
		t.Fatal(err)
	}
	secondView.Diagnostics = append(secondView.Diagnostics, memory.Diagnostic{Code: "compatible-retained-root"})
	secondView.Digest, err = memory.SessionViewDigest(secondView)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionView(secondView); err != nil {
		t.Fatal(err)
	}
	chainBody, err := store.LoadObject(memorystore.ObjectConversationChain, first.Digest)
	if err != nil {
		t.Fatal(err)
	}
	secondChain, err := conversationchain.Parse(chainBody)
	if err != nil {
		t.Fatal(err)
	}
	secondChain.SessionViewDigest = secondView.Digest
	proof := *secondChain.DependencyProofV1
	proof.SessionViewDigest = secondView.Digest
	secondChain.DependencyProofV1 = &proof
	secondChain.DependencyDigest, err = memory.Digest(proof)
	if err != nil {
		t.Fatal(err)
	}
	chainBody, err = conversationchain.Render(secondChain)
	if err != nil {
		t.Fatal(err)
	}
	secondChain, err = conversationchain.Parse(chainBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutConversationChain(secondChain); err != nil {
		t.Fatal(err)
	}
	generation := fixture.generation
	generation.RetainedConversationChains = append(generation.RetainedConversationChains, memory.ConversationChainDependency{Provider: secondChain.Provider, SessionID: secondChain.SessionID, SessionViewDigest: secondView.Digest, Digest: secondChain.Digest})
	fixture.request.ExpectedGenerationID = advanceRetainedGeneration(t, store, generation, "generation-retained-ambiguous")
	if _, err := inspect.LoadConversationPage(context.Background(), fixture.request); retainedErrorCode(err) != "retained_evidence_ambiguous" {
		t.Fatalf("two compatible retained roots error=%v", err)
	}
}

func advanceRetainedGeneration(t *testing.T, store *memorystore.Store, generation memory.GenerationManifest, generationID string) string {
	t.Helper()
	prepared, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	generation.GenerationID = generationID
	generation.CreatedAt = "2026-09-08T01:00:05Z"
	projectBody, err := store.LoadObject(memorystore.ObjectProjectView, generation.ProjectViewDigest)
	if err != nil {
		t.Fatal(err)
	}
	var project memory.ProjectView
	if err := json.Unmarshal(projectBody, &project); err != nil {
		t.Fatal(err)
	}
	views := make(map[sessionindex.SessionKey]*memory.SessionView, len(generation.SessionViews))
	for _, dependency := range generation.SessionViews {
		body, err := store.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if err != nil {
			t.Fatal(err)
		}
		var view memory.SessionView
		if err := json.Unmarshal(body, &view); err != nil {
			t.Fatal(err)
		}
		views[sessionindex.SessionKey{Provider: dependency.Provider, SessionID: dependency.SessionID}] = &view
	}
	var previous *sessionindex.Document
	if generation.PreviousSessionIndexDigest != "" {
		body, err := store.LoadObject(memorystore.ObjectSessionIndex, generation.PreviousSessionIndexDigest)
		if err != nil {
			t.Fatal(err)
		}
		document, err := sessionindex.Parse(body)
		if err != nil {
			t.Fatal(err)
		}
		previous = &document
	}
	index, err := sessionindex.Build(sessionindex.BuildInput{ProjectView: project, Manifest: generation, SessionViews: views, Previous: previous, GeneratedAt: time.Date(2026, 9, 8, 1, 0, 5, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	generation.SessionIndexDigest, err = store.PutSessionIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	successor, err := store.AdvancePrepared(prepared, generation)
	if err != nil {
		t.Fatal(err)
	}
	proof := memory.PublicationProof{Version: 4, ProjectID: generation.ProjectID, GenerationID: generation.GenerationID, ManifestDigest: successor.ManifestDigest, ProjectViewDigest: successor.ProjectViewDigest, ReviewSHA256: strings.Repeat("1", 64), HistorySHA256: strings.Repeat("2", 64), LedgerSHA256: strings.Repeat("3", 64), SessionIndexSHA256: strings.TrimPrefix(generation.SessionIndexDigest, "sha256:"), JournalVerified: true}
	if err := store.CommitPublished(generation.GenerationID, proof); err != nil {
		t.Fatal(err)
	}
	return generation.GenerationID
}

func retainedErrorCode(err error) string {
	if value, ok := err.(*inspect.Error); ok {
		return value.Code
	}
	return ""
}

func TestRetainedConversationPublishedGraphTamperingFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper func(*testing.T, removedRetainedFixture)
	}{
		{"chain document digest", func(t *testing.T, fixture removedRetainedFixture) {
			dependency := fixture.generation.RetainedConversationChains[0]
			mutateRetainedFile(t, filepath.Join(fixture.memoryRoot, "conversation-chains", strings.TrimPrefix(dependency.Digest, "sha256:")+".json"), []byte(`"segmentation_rule_version":"visible-turn-v1"`), []byte(`"segmentation_rule_version":"visible-turn-v2"`))
		}},
		{"evidence view identity", func(t *testing.T, fixture removedRetainedFixture) {
			dependency := fixture.generation.RetainedConversationChains[0]
			path := filepath.Join(fixture.memoryRoot, "sessions", strings.TrimPrefix(dependency.SessionViewDigest, "sha256:")+".json")
			mutateRetainedFile(t, path, []byte(`"provider":"codex"`), []byte(`"provider":"cladx"`))
		}},
		{"selected active revision object", func(t *testing.T, fixture removedRetainedFixture) {
			dependency := fixture.generation.RetainedConversationChains[0]
			viewPath := filepath.Join(fixture.memoryRoot, "sessions", strings.TrimPrefix(dependency.SessionViewDigest, "sha256:")+".json")
			body, err := os.ReadFile(viewPath)
			if err != nil {
				t.Fatal(err)
			}
			var view memory.SessionView
			if err := json.Unmarshal(body, &view); err != nil || len(view.ObservationChunkDigests) == 0 {
				t.Fatalf("evidence view chunks: %+v err=%v", view.ObservationChunkDigests, err)
			}
			path := filepath.Join(fixture.memoryRoot, "observations", strings.TrimPrefix(view.ObservationChunkDigests[0], "sha256:")+".jsonl")
			body, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"manifest dependency", func(t *testing.T, fixture removedRetainedFixture) {
			dependency := fixture.generation.RetainedConversationChains[0]
			path := filepath.Join(fixture.memoryRoot, "generations", fixture.generation.GenerationID+".json")
			bad := "sha256:" + strings.Repeat("f", 64)
			mutateRetainedFile(t, path, []byte(dependency.Digest), []byte(bad))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRemovedRetainedFixture(t)
			if len(fixture.generation.RetainedConversationChains) != 1 {
				t.Fatalf("retained dependencies=%d want 1", len(fixture.generation.RetainedConversationChains))
			}
			test.tamper(t, fixture)
			if page, err := inspect.LoadConversationPage(context.Background(), fixture.request); err == nil || page.SchemaVersion != 0 {
				t.Fatalf("tampered retained graph succeeded: page=%+v err=%v", page, err)
			}
		})
	}
}

func mutateRetainedFile(t *testing.T, path string, old, replacement []byte) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(body, old, replacement, 1)
	if bytes.Equal(changed, body) {
		t.Fatalf("tamper target not found in %s", path)
	}
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
}
