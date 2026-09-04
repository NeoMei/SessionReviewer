package conversationchain

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestParseFrozenConversationChainFixtures(t *testing.T) {
	valid, err := os.ReadFile("../../testdata/contracts/v4/conversation-chain-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(valid); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	invalid, err := os.ReadFile("../../testdata/contracts/v4/conversation-chain-v1.invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(invalid); err == nil {
		t.Fatal("hidden message role was accepted")
	} else if got := strictjson.CodeOf(err); got != "wire_contract_invalid" {
		t.Fatalf("rejection code = %q, want wire_contract_invalid: %v", got, err)
	}
}

func TestRenderConversationChainNormalizesCollectionsAndBindsDigest(t *testing.T) {
	document := frozenChain()
	document.TurnUnits[0].AssistantMessages = nil
	document.TurnUnits[0].Actions = nil
	document.TurnUnits[0].Results = nil
	rendered, err := Render(document)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(rendered, &raw); err != nil {
		t.Fatal(err)
	}
	turn := raw["turn_units"].([]any)[0].(map[string]any)
	for _, key := range []string{"assistant_messages", "actions", "results"} {
		if _, ok := turn[key].([]any); !ok {
			t.Fatalf("%s did not render as an array: %#v", key, turn[key])
		}
	}
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatal(err)
	}
	parsed.SessionViewDigest = "sha256:" + strings.Repeat("9", 64)
	tampered, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(tampered); err == nil {
		t.Fatal("accepted a chain whose exact session dependency no longer matches its digest")
	}
}

func TestParseConversationChainRejectsZeroDigest(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/contracts/v4/conversation-chain-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(fixture, &raw); err != nil {
		t.Fatal(err)
	}
	raw["digest"] = "sha256:" + strings.Repeat("0", 64)
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(body); err == nil {
		t.Fatal("accepted an unbound all-zero persisted digest")
	}
}

func TestConversationChainRejectsIntegersAboveJavaScriptSafeMaximum(t *testing.T) {
	document := frozenChain()
	document.TurnUnits[0].UserMessage.SourceRef.RecordOrdinal = 1 << 53
	if err := Validate(document); err == nil {
		t.Fatal("accepted record ordinal above JavaScript safe integer maximum")
	}
	document = frozenChain()
	document.Coverage.SourceMessages = 1 << 53
	if err := Validate(document); err == nil {
		t.Fatal("accepted coverage count above JavaScript safe integer maximum")
	}
}

func TestConversationChainRejectsOversizedVisibleExcerptAndUnauthenticatedSource(t *testing.T) {
	document := frozenChain()
	document.TurnUnits[0].UserMessage.VisibleExcerpt = strings.Repeat("界", 1366)
	if err := Validate(document); err == nil {
		t.Fatal("accepted visible excerpt above 4,096 UTF-8 bytes")
	}
	document = frozenChain()
	document.TurnUnits[0].UserMessage.SourceRef.SourceHash = ""
	if err := Validate(document); err == nil {
		t.Fatal("accepted unauthenticated source reference")
	}
	document = frozenChain()
	document.TurnUnits[0].UserMessage.SourceRef.Provider = "codex"
	if err := Validate(document); err == nil {
		t.Fatal("accepted source reference bound to a different provider")
	}
}

func TestConversationChainExactObjectsRejectRawToolOutput(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/contracts/v4/conversation-chain-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(fixture, &raw); err != nil {
		t.Fatal(err)
	}
	turn := raw["turn_units"].([]any)[0].(map[string]any)
	action := turn["actions"].([]any)[0].(map[string]any)
	action["raw_tool_output"] = map[string]any{"secret": true}
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(body); err == nil {
		t.Fatal("accepted arbitrary raw tool output")
	} else if got := strictjson.CodeOf(err); got != "wire_shape_invalid" {
		t.Fatalf("rejection code = %q, want wire_shape_invalid: %v", got, err)
	}
}

func frozenChain() Document {
	return Document{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Digest: "sha256:" + strings.Repeat("0", 64),
		ProjectID: "project-p", Provider: "claude", SessionID: "session-1",
		SessionViewDigest: "sha256:" + strings.Repeat("1", 64), DependencyDigest: "sha256:" + strings.Repeat("2", 64),
		SegmentationRuleVersion: "visible-turn-v1",
		Coverage:                Coverage{SourceMessages: 1, CapturedMessages: 1, TurnUnits: 1, UnansweredUnits: 1},
		TurnUnits: []TurnUnit{{
			TurnUnitID: "turn-1", Ordinal: 1, StartedAt: "2026-09-04T00:00:00Z", EndedAt: nil,
			UserMessage:       Message{Role: RoleUser, RevisionID: "revision-user-1", SourceRef: frozenSourceRef(), OccurredAt: "2026-09-04T00:00:00Z", VisibleExcerpt: "question", Truncated: false},
			AssistantMessages: []Message{}, Actions: []Action{}, Results: []Result{}, AnswerState: AnswerNone,
		}},
	}
}

func frozenSourceRef() SourceRef {
	return SourceRef{Provider: "claude", SessionID: "session-1", SourceIdentity: "source-1", RecordOrdinal: 7, SourceHash: strings.Repeat("3", 64)}
}
