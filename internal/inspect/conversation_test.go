package inspect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

const conversationSession = "01234567-0123-4567-89ab-0123456789ab"

type conversationFixture struct {
	request ConversationRequest
	paths   []string
	record  memory.SourceRecord
}

func visibleRecord(role, phase, text string) string {
	body, _ := json.Marshal(map[string]any{"timestamp": "2026-09-07T00:00:01Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": role, "phase": phase, "content": []any{map[string]any{"type": "output_text", "text": text}}}})
	return string(body) + "\n"
}

func newConversationFixture(t *testing.T, segments ...string) conversationFixture {
	t.Helper()
	data := t.TempDir()
	home := t.TempDir()
	root := filepath.Join(home, "sessions")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	var logical strings.Builder
	var paths []string
	for i, segment := range segments {
		meta, _ := json.Marshal(map[string]any{"timestamp": "2026-09-07T00:00:0" + string(rune('0'+i)) + "Z", "type": "session_meta", "payload": map[string]string{"id": conversationSession, "cwd": "/fixture-project"}})
		content := string(meta) + "\n" + segment
		name := "rollout-2026-09-07T00-00-0" + string(rune('0'+i)) + "-" + conversationSession
		if i > 0 {
			name += "_11111111-1111-4111-8111-111111111111"
		}
		path := filepath.Join(root, name+".jsonl")
		paths = append(paths, path)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		logical.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			logical.WriteString("\n")
		}
	}
	sum := sha256.Sum256([]byte(logical.String()))
	record := memory.SourceRecord{SchemaVersion: 1, Provider: "codex", SessionID: conversationSession, SourceIdentity: "source-" + conversationSession, StartedAt: "2026-09-07T00:00:00Z", EndedAt: "2026-09-07T00:00:00Z", FrozenBoundary: memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: strings.Count(logical.String(), "\n"), ByteOffset: int64(logical.Len())}}, SourceHash: hex.EncodeToString(sum[:])}, Availability: memory.SourceAvailable, Usage: accounting.SessionUsage{StartedAt: "2026-09-07T00:00:00Z", EndedAt: "2026-09-07T00:00:00Z", Models: []accounting.ModelUsage{}}, ProjectIDs: []string{"project-conversation"}}
	catalog, err := sourcecatalog.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := catalog.UpsertSource(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	fixture := buildEventFixtureCustomizedAt(t, data, "project-conversation", "generation-conversation", []string{conversationSession}, nil, func(_ string, view *memory.SessionView) {
		view.SourceRecordDigest = digest
		view.UsageRecordDigest = digest
	})
	return conversationFixture{request: ConversationRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex", SessionID: conversationSession, ExpectedGenerationID: fixture.generationID, Limit: 1}, paths: paths, record: record}
}

type wireConversationMessage struct {
	Role           string
	Phase          *string
	Text           *string
	VisibleExcerpt string `json:"visible_excerpt"`
	Truncated      bool
}
type wireConversationTurn struct {
	TurnUnitID            string                  `json:"turn_unit_id"`
	AnswerState           string                  `json:"answer_state"`
	AssistantMessageCount int                     `json:"assistant_message_count"`
	UserMessage           wireConversationMessage `json:"user_message"`
}
type wireConversationPage struct {
	Mode           string
	Total          int
	RangeStart     int                    `json:"range_start"`
	RangeEnd       int                    `json:"range_end"`
	NextCursor     *string                `json:"next_cursor"`
	PreviousCursor *string                `json:"previous_cursor"`
	FirstCursor    *string                `json:"first_cursor"`
	LastCursor     *string                `json:"last_cursor"`
	TurnUnits      []wireConversationTurn `json:"turn_units"`
	Messages       []wireConversationMessage
	Coverage       struct {
		OversizedRecords int `json:"oversized_records"`
		MalformedRecords int `json:"malformed_records"`
		ContextMessages  int `json:"context_messages"`
		Complete         bool
	}
}

func readConversation(t *testing.T, request ConversationRequest) (wireConversationPage, []byte) {
	t.Helper()
	page, err := LoadConversationPage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderConversationPage(page)
	if err != nil {
		t.Fatal(err)
	}
	var wire wireConversationPage
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	return wire, body
}

func TestConversationReadsAuthenticatedVisibleTurnsAndFullSelectedText(t *testing.T) {
	long := strings.Repeat("界", 2000)
	first := visibleRecord("user", "", "question one") + visibleRecord("assistant", "analysis", "ANALYSIS_SENTINEL") + visibleRecord("system", "", "SYSTEM_SENTINEL") + visibleRecord("developer", "", "DEVELOPER_SENTINEL") + visibleRecord("assistant", "commentary", "working") + `{"type":"response_item","payload":{"type":"function_call_output","output":"RAW_TOOL_SENTINEL"}}` + "\n" + visibleRecord("user", "", "<environment_context>ambient</environment_context>") + visibleRecord("assistant", "final_answer", long)
	fixture := newConversationFixture(t, strings.TrimSuffix(first, "\n"), visibleRecord("user", "", "question two")+visibleRecord("assistant", "commentary", "partial reply")+visibleRecord("user", "", "tail question"))
	before := snapshotEventTree(t, fixture.request.DataRoot)
	page, body := readConversation(t, fixture.request)
	if page.Mode != "turn_index" || page.Total != 3 || len(page.TurnUnits) != 1 || page.TurnUnits[0].AnswerState != "answered" || page.TurnUnits[0].AssistantMessageCount != 2 || page.NextCursor == nil {
		t.Fatalf("page=%s", body)
	}
	selected := fixture.request
	selected.TurnUnitID = page.TurnUnits[0].TurnUnitID
	selected.Limit = 3
	replies, raw := readConversation(t, selected)
	if replies.Total != 3 || len(replies.Messages) != 3 || replies.Messages[0].Role != "user" || replies.Messages[1].Phase == nil || *replies.Messages[1].Phase != "commentary" || replies.Messages[2].Text == nil || *replies.Messages[2].Text != long || !replies.Messages[2].Truncated || len(replies.Messages[2].VisibleExcerpt) > 4096 || !utf8.ValidString(replies.Messages[2].VisibleExcerpt) {
		t.Fatalf("wrong selected body length=%d page=%+v", len(raw), replies)
	}
	for _, secret := range []string{"ANALYSIS_SENTINEL", "SYSTEM_SENTINEL", "DEVELOPER_SENTINEL", "RAW_TOOL_SENTINEL", "ambient"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	middleReq := fixture.request
	middleReq.Cursor = *page.NextCursor
	middle, _ := readConversation(t, middleReq)
	if middle.RangeStart != 1 || middle.TurnUnits[0].AnswerState != "partial" || middle.PreviousCursor == nil || middle.NextCursor == nil {
		t.Fatalf("middle=%+v", middle)
	}
	lastReq := fixture.request
	lastReq.Cursor = *page.LastCursor
	last, _ := readConversation(t, lastReq)
	if last.RangeStart != 2 || last.TurnUnits[0].AnswerState != "no_answer" || last.NextCursor != nil {
		t.Fatalf("last=%+v", last)
	}
	if !reflect.DeepEqual(before, snapshotEventTree(t, fixture.request.DataRoot)) {
		t.Fatal("read mutated private state")
	}
}

func TestConversationAcceptedPrefixSurvivesAppendButRejectsChangedMissingAndShrunkSource(t *testing.T) {
	for _, change := range []string{"append", "missing", "shrunk", "changed", "symlink", "catalog"} {
		t.Run(change, func(t *testing.T) {
			f := newConversationFixture(t, visibleRecord("user", "", "question")+visibleRecord("assistant", "final_answer", "answer"))
			path := f.paths[0]
			switch change {
			case "append":
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteString(visibleRecord("user", "", "NEW_UNPUBLISHED_SENTINEL"))
				if err != nil {
					t.Fatal(err)
				}
				file.Close()
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "shrunk":
				if err := os.Truncate(path, 20); err != nil {
					t.Fatal(err)
				}
			case "changed":
				body, _ := os.ReadFile(path)
				body = []byte(strings.ReplaceAll(string(body), "answer", "change"))
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				other := filepath.Join(t.TempDir(), "other")
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Skip(err)
				}
			case "catalog":
				c, err := sourcecatalog.Open(f.request.DataRoot)
				if err != nil {
					t.Fatal(err)
				}
				old, _ := memory.Digest(f.record)
				f.record.FrozenBoundary.SourceHash = strings.Repeat("a", 64)
				if _, err := c.ReplaceSource(old, f.record); err != nil {
					t.Fatal(err)
				}
				c.Close()
			}
			page, err := LoadConversationPage(context.Background(), f.request)
			if change == "append" {
				if err != nil {
					t.Fatal(err)
				}
				body, _ := RenderConversationPage(page)
				if strings.Contains(string(body), "NEW_UNPUBLISHED") {
					t.Fatal("read appended record")
				}
			} else if eventErrorCode(err) != "source_unavailable" {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestConversationSkipsOversizedMalformedAndContextRecordsWithCoverage(t *testing.T) {
	f := newConversationFixture(t, visibleRecord("user", "", "question")+`{"type":"response_item","payload":{"type":"function_call_output","output":"`+strings.Repeat("x", 70000)+`"}}`+"\n{bad json}\n"+visibleRecord("assistant", "final_answer", "answer"))
	page, body := readConversation(t, f.request)
	if page.Total != 1 || page.Coverage.OversizedRecords != 1 || page.Coverage.MalformedRecords != 1 || page.Coverage.Complete {
		t.Fatalf("coverage=%s", body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadConversationPage(ctx, f.request); err == nil {
		t.Fatal("canceled read succeeded")
	}
}

func TestConversationRejectsCrossModeSessionTurnLimitAndGenerationCursors(t *testing.T) {
	f := newConversationFixture(t, visibleRecord("user", "", "one")+visibleRecord("assistant", "final_answer", "one answer")+visibleRecord("user", "", "two")+visibleRecord("assistant", "final_answer", "two answer"))
	first, _ := readConversation(t, f.request)
	selected := f.request
	selected.TurnUnitID = first.TurnUnits[0].TurnUnitID
	messages, _ := readConversation(t, selected)
	lastRequest := f.request
	lastRequest.Cursor = *first.LastCursor
	last, _ := readConversation(t, lastRequest)
	for _, mode := range []string{"turn", "limit", "generation", "session", "index_in_turn", "message_without_turn", "tampered"} {
		t.Run(mode, func(t *testing.T) {
			req := selected
			req.MessageCursor = *messages.NextCursor
			switch mode {
			case "turn":
				req.TurnUnitID = last.TurnUnits[0].TurnUnitID
			case "limit":
				req.Limit = 2
			case "generation":
				req.ExpectedGenerationID = "other"
			case "session":
				req.SessionID = "another"
			case "index_in_turn":
				req.Cursor = *first.NextCursor
			case "message_without_turn":
				req.TurnUnitID = ""
			case "tampered":
				req.MessageCursor += "x"
			}
			if _, err := LoadConversationPage(context.Background(), req); err == nil {
				t.Fatal("invalid cursor accepted")
			}
		})
	}
}

func TestConversationFullBodyPagesAdaptToResponseBudgetWithoutLosingMessages(t *testing.T) {
	segment := visibleRecord("user", "", "large answers")
	for i := 0; i < 40; i++ {
		segment += visibleRecord("assistant", "commentary", strings.Repeat("x", 50000))
	}
	f := newConversationFixture(t, segment)
	index, _ := readConversation(t, f.request)
	req := f.request
	req.TurnUnitID = index.TurnUnits[0].TurnUnitID
	req.Limit = 64
	count := 0
	pages := 0
	for {
		page, body := readConversation(t, req)
		if len(body) > 1<<20 || len(page.Messages) == 0 {
			t.Fatalf("unbounded/empty response bytes=%d", len(body))
		}
		count += len(page.Messages)
		pages++
		if page.NextCursor == nil {
			break
		}
		req.MessageCursor = *page.NextCursor
	}
	if count != 41 || pages < 3 {
		t.Fatalf("captured=%d pages=%d", count, pages)
	}
}

func TestConversationRendererRejectsForgedPageShape(t *testing.T) {
	f := newConversationFixture(t, visibleRecord("user", "", "question"))
	page, err := LoadConversationPage(context.Background(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	page.Total = 100
	page.RangeEnd = 50
	if _, err := RenderConversationPage(page); err == nil {
		t.Fatal("renderer accepted range count inconsistent with items")
	}
}

func TestConversationRendererRejectsInvalidIdentityPhaseAndSourceReference(t *testing.T) {
	f := newConversationFixture(t, visibleRecord("user", "", "question"))
	for _, mode := range []string{"identity", "phase", "source", "coverage"} {
		t.Run(mode, func(t *testing.T) {
			page, err := LoadConversationPage(context.Background(), f.request)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "identity":
				page.ProjectID = "/private/path"
			case "phase":
				phase := "analysis"
				page.TurnUnits[0].UserMessage.Phase = &phase
			case "source":
				page.TurnUnits[0].UserMessage.SourceRef.SessionID = "another"
			case "coverage":
				page.Coverage.CapturedMessages++
			}
			if _, err := RenderConversationPage(page); err == nil {
				t.Fatal("renderer accepted forged page")
			}
		})
	}
}

func TestConversationPageFrozenFixtureRendersEmptyArrays(t *testing.T) {
	body, err := os.ReadFile("../../testdata/contracts/v4/conversation-page-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var page ConversationPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	output, err := RenderConversationPage(page)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"turn_units":[]`) || !strings.Contains(string(output), `"messages":[]`) {
		t.Fatal("missing empty collections")
	}
}

func TestConversationCancellationDuringMaterializationReturnsNoPartialPage(t *testing.T) {
	f := newConversationFixture(t, visibleRecord("user", "", "q")+visibleRecord("assistant", "final_answer", "a"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inspectCheckpoint = func(phase string) {
		if phase == "conversation_message" {
			cancel()
		}
	}
	defer func() { inspectCheckpoint = nil }()
	page, err := LoadConversationPage(ctx, f.request)
	if err == nil || page.SchemaVersion != 0 {
		t.Fatalf("returned partial canceled page=%+v err=%v", page, err)
	}
}

func TestConversationUnsupportedProviderHasTypedDiagnostic(t *testing.T) {
	f := buildEventFixture(t, "conversation-claude", "generation-claude", "s")
	_, err := LoadConversationPage(context.Background(), ConversationRequest{DataRoot: f.dataRoot, ProjectID: f.projectID, Provider: "claude", SessionID: "s", ExpectedGenerationID: f.generationID, Limit: 2})
	if eventErrorCode(err) != "unsupported_provider" {
		t.Fatalf("err=%v", err)
	}
}

func TestConversationIgnoresUnrelatedFileContentsAndExcludesOpaqueMessages(t *testing.T) {
	segment := visibleRecord("user", "", "question") + `{"type":"response_item","payload":{"type":"message","role":"assistant","recipient":"agent-child","content":[{"type":"output_text","text":"AGENT_SENTINEL"}]}}` + "\n" + `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"encrypted_text","text":"ENCRYPTED_SENTINEL"}]}}` + "\n" + `{"type":"response_item","payload":{"type":"message","role":"assistant","channel":"analysis","content":[{"type":"output_text","text":"REASONING_SENTINEL"}]}}` + "\n" + visibleRecord("assistant", "final_answer", "answer token sk-abcdefghijklmnopqrstuvwxyz1234567890 path:/Users/private/secret")
	f := newConversationFixture(t, segment)
	unrelated := filepath.Join(filepath.Dir(f.paths[0]), "rollout-2026-09-07T00-00-01-ffffffff-ffff-4fff-8fff-ffffffffffff.jsonl")
	if err := os.WriteFile(unrelated, []byte("corrupt unrelated file"), 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(unrelated, 0600)
	first, _ := readConversation(t, f.request)
	req := f.request
	req.TurnUnitID = first.TurnUnits[0].TurnUnitID
	req.Limit = 64
	page, body := readConversation(t, req)
	if len(page.Messages) != 2 {
		t.Fatalf("messages=%s", body)
	}
	for _, secret := range []string{"AGENT_SENTINEL", "ENCRYPTED_SENTINEL", "REASONING_SENTINEL", "sk-abcdefghijklmnopqrstuvwxyz1234567890", "/Users/private"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
}
