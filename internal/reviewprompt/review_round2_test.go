package reviewprompt_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/reviewprompt"
)

func TestBuildExcludesAllHistoricalEvidencePayloads(t *testing.T) {
	input := fixtureInput()
	compatibility := &input.Accepted.Machine.LegacyCompatibility
	compatibility.CurrentState.Evidence = []ledger.EvidenceRef{historicalRef("hist-current", "HISTORICAL_CURRENT_SUMMARY")}
	compatibility.Decisions[0].Evidence = []ledger.EvidenceRef{historicalRef("hist-decision", "HISTORICAL_DECISION_SUMMARY")}
	compatibility.OpenLoops[0].Evidence = []ledger.EvidenceRef{historicalRef("hist-loop", "HISTORICAL_LOOP_SUMMARY")}
	compatibility.Timeline[0].Evidence = []ledger.EvidenceRef{historicalRef("hist-event", "HISTORICAL_EVENT_SUMMARY")}
	input.Accepted.Machine.Sessions[0].Evidence = []ledger.EvidenceRef{historicalRef("hist-session", "HISTORICAL_SESSION_SUMMARY")}
	input.Accepted.Machine.Sessions[0].Phases = []ledger.SessionPhase{{
		Title: "Accepted phase", Summary: "Accepted phase summary",
		Evidence: []ledger.EvidenceRef{historicalRef("hist-phase", "HISTORICAL_PHASE_SUMMARY")},
	}}

	bundle, err := reviewprompt.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	context := between(t, string(bundle.Prompt), "BEGIN_UNTRUSTED_ACCEPTED_CONTEXT_DATA_V1", "END_UNTRUSTED_ACCEPTED_CONTEXT_DATA_V1")
	for _, canary := range []string{
		"hist-current", "HISTORICAL_CURRENT_SUMMARY",
		"hist-decision", "HISTORICAL_DECISION_SUMMARY",
		"hist-loop", "HISTORICAL_LOOP_SUMMARY",
		"hist-event", "HISTORICAL_EVENT_SUMMARY",
		"hist-session", "HISTORICAL_SESSION_SUMMARY",
		"hist-phase", "HISTORICAL_PHASE_SUMMARY",
	} {
		if strings.Contains(context, canary) {
			t.Errorf("accepted context leaked historical evidence payload %q", canary)
		}
	}
	for _, historicalField := range []string{`"evidence"`, `"evidence_id"`, `"jsonl_line"`, `"source_hash"`} {
		if strings.Contains(context, historicalField) {
			t.Errorf("accepted context retained historical evidence field %s", historicalField)
		}
	}
}

func TestAgentDraftSchemaHasDistinctStableIdentity(t *testing.T) {
	bundle, err := reviewprompt.Build(fixtureInput())
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(bundle.OutputSchema) {
		t.Fatal("transformed Agent draft schema is not valid JSON")
	}
	var final, draft map[string]any
	if err := json.Unmarshal(reviewprompt.FinalProposalSchema(), &final); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bundle.OutputSchema, &draft); err != nil {
		t.Fatal(err)
	}
	finalID, _ := final["$id"].(string)
	draftID, _ := draft["$id"].(string)
	if finalID == "" || draftID == "" {
		t.Fatalf("schema identities must be explicit: final=%q draft=%q", finalID, draftID)
	}
	if draftID == finalID {
		t.Fatalf("Agent draft schema reused final apply identity %q", draftID)
	}
	const wantDraftID = "https://github.com/neomei/SessionReviewer/schemas/proposal-agent-draft-v1.schema.json"
	if draftID != wantDraftID {
		t.Fatalf("Agent draft schema ID=%q want stable identity %q", draftID, wantDraftID)
	}
}

func TestBuildRejectsSecretsInEverySerializedPacketItemString(t *testing.T) {
	tests := map[string]func(*reviewprompt.Input){
		"id":        func(input *reviewprompt.Input) { input.Packet.Events[0].ID = secretCanary },
		"item_id":   func(input *reviewprompt.Input) { input.Packet.Events[0].ItemID = secretCanary },
		"timestamp": func(input *reviewprompt.Input) { input.Packet.Events[0].Timestamp = secretCanary },
		"kind":      func(input *reviewprompt.Input) { input.Packet.Events[0].Kind = secretCanary },
		"role":      func(input *reviewprompt.Input) { input.Packet.Events[0].Role = secretCanary },
		"tool_name": func(input *reviewprompt.Input) { input.Packet.Events[0].ToolName = secretCanary },
		"summary":   func(input *reviewprompt.Input) { input.Packet.Events[0].Summary = secretCanary },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := fixtureInput()
			mutate(&input)
			bundle, err := reviewprompt.Build(input)
			if !errors.Is(err, reviewprompt.ErrUnsafeInput) {
				t.Fatalf("err=%v want ErrUnsafeInput", err)
			}
			if len(bundle.Prompt) != 0 || bytes.Contains(bundle.Prompt, []byte(secretCanary)) {
				t.Fatal("rejected packet secret leaked into prompt output")
			}
		})
	}
}

func TestBuildRejectsMalformedPacketItemProtocolFields(t *testing.T) {
	tests := map[string]func(*reviewprompt.Input){
		"id":          func(input *reviewprompt.Input) { input.Packet.Events[0].ID = "bad id" },
		"item_id":     func(input *reviewprompt.Input) { input.Packet.Events[0].ItemID = "bad item/id" },
		"timestamp":   func(input *reviewprompt.Input) { input.Packet.Events[0].Timestamp = "not-a-time" },
		"jsonl_line":  func(input *reviewprompt.Input) { input.Packet.Events[0].JSONLLine = 0 },
		"source_hash": func(input *reviewprompt.Input) { input.Packet.Events[0].SourceHash = strings.Repeat("G", 64) },
		"kind":        func(input *reviewprompt.Input) { input.Packet.Events[0].Kind = "future_kind" },
		"role":        func(input *reviewprompt.Input) { input.Packet.Events[0].Role = "developer" },
		"tool_name": func(input *reviewprompt.Input) {
			input.Packet.Events[0].Kind = "tool_call"
			input.Packet.Events[0].Role = ""
			input.Packet.Events[0].ToolName = "bad tool/name"
		},
		"summary": func(input *reviewprompt.Input) { input.Packet.Events[0].Summary = string([]byte{0xff}) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := fixtureInput()
			mutate(&input)
			bundle, err := reviewprompt.Build(input)
			if !errors.Is(err, reviewprompt.ErrInvalidInput) {
				t.Fatalf("err=%v want ErrInvalidInput", err)
			}
			if len(bundle.Prompt) != 0 {
				t.Fatal("malformed packet item produced prompt bytes")
			}
		})
	}
}

func historicalRef(id, summary string) ledger.EvidenceRef {
	return ledger.EvidenceRef{
		EvidenceID: id, SessionID: "s0", JSONLLine: 1,
		SourceHash: strings.Repeat("a", 64), Summary: summary,
	}
}
