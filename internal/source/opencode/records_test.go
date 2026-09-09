package opencode

import (
	"encoding/json"
	"strings"
	"testing"
)

func messageFixture(id, role, text, finish string) rawMessage {
	info := map[string]any{"role": role, "time": map[string]any{"created": int64(1789000000000), "completed": int64(1789000001000)}}
	if role == "assistant" {
		info["finish"] = finish
		info["modelID"] = "model"
		info["providerID"] = "provider"
		info["tokens"] = map[string]any{"input": 10, "output": 5, "reasoning": 2, "cache": map[string]any{"read": 3, "write": 1}}
	}
	data, _ := json.Marshal(info)
	p, _ := json.Marshal(map[string]any{"type": "text", "text": text})
	return rawMessage{ID: id, Created: 1789000000000, Data: data, Parts: []rawPart{{ID: "prt_" + id, Data: p}}}
}
func TestStablePrefixDefersEntirePendingToolTurn(t *testing.T) {
	first := []rawMessage{messageFixture("msg_1", "user", "Question 1", ""), messageFixture("msg_2", "assistant", "Answer 1", "stop")}
	tail := []rawMessage{messageFixture("msg_3", "user", "Question 2", ""), messageFixture("msg_4", "assistant", "Working", "tool-calls"), messageFixture("msg_5", "assistant", "Premature answer", "stop")}
	tail[1].Parts = append(tail[1].Parts, rawPart{ID: "prt_tool", Data: json.RawMessage(`{"type":"tool","callID":"call_1","tool":"bash","state":{"status":"running","input":{"command":"go test ./..."}}}`)})
	records, deferred, err := stableRecords(sessionRow{ID: "ses_one", Directory: "/project", Created: 1789000000000}, append(first, tail...))
	if err != nil || len(records) != 2 || !deferred {
		t.Fatalf("records=%d deferred=%v err=%v", len(records), deferred, err)
	}
	before := prefixHash(records)
	tail[1].Parts[1].Data = json.RawMessage(`{"type":"tool","callID":"call_1","tool":"bash","state":{"status":"completed","input":{"command":"go test ./..."},"output":"ok","metadata":{"exit":0}}}`)
	grown, deferred, err := stableRecords(sessionRow{ID: "ses_one", Directory: "/project", Created: 1789000000000}, append(first, tail...))
	if err != nil || len(grown) != 5 || deferred || prefixHash(grown[:2]) != before {
		t.Fatalf("stable append: %d %v %v", len(grown), deferred, err)
	}
}
func TestCanonicalJSONOrderAndUnknownSchema(t *testing.T) {
	first := messageFixture("msg_1", "user", "Question", "")
	answer := messageFixture("msg_2", "assistant", "Answer", "stop")
	row := sessionRow{ID: "ses_one", Directory: "/project", Created: 1789000000000}
	a, _, err := stableRecords(row, []rawMessage{first, answer})
	if err != nil {
		t.Fatal(err)
	}
	first.Parts[0].Data = json.RawMessage(`{"text":"Question","type":"text"}`)
	b, _, err := stableRecords(row, []rawMessage{first, answer})
	if err != nil || prefixHash(a) != prefixHash(b) {
		t.Fatal("JSON object order changed source identity", err)
	}
	first.Parts[0].Data = json.RawMessage(`{"type":"new-unknown-part","text":"secret"}`)
	if _, _, err := stableRecords(row, []rawMessage{first, answer}); err == nil {
		t.Fatal("unknown part accepted as complete")
	}
}
func TestVisibleRecordsExcludeReasoningAndSyntheticText(t *testing.T) {
	q := messageFixture("msg_1", "user", "Question", "")
	a := messageFixture("msg_2", "assistant", "Answer", "stop")
	a.Parts = append(a.Parts, rawPart{ID: "prt_reasoning", Data: json.RawMessage(`{"type":"reasoning","text":"HIDDEN_REASONING"}`)})
	q.Parts = append(q.Parts, rawPart{ID: "prt_synthetic", Data: json.RawMessage(`{"type":"text","text":"SYNTHETIC_CONTEXT","synthetic":true}`)})
	records, _, err := stableRecords(sessionRow{ID: "ses_one", Directory: "/project", Created: 1789000000000}, []rawMessage{q, a})
	if err != nil {
		t.Fatal(err)
	}
	messages, coverage := visibleRecords(records, "ses_one", "source-one")
	if len(messages) != 2 || messages[0].Text != "Question" || messages[1].Text != "Answer" || !coverage.Complete {
		t.Fatalf("messages=%+v coverage=%+v", messages, coverage)
	}
	raw, _ := json.Marshal(messages)
	if strings.Contains(string(raw), "HIDDEN") || strings.Contains(string(raw), "SYNTHETIC") {
		t.Fatal("hidden text exposed")
	}
	usage, err := recordUsage(records)
	if err != nil || len(usage.Models) != 1 || usage.TotalTokens != 21 || usage.Models[0].InputTokens != 14 || usage.Models[0].OutputTokens != 7 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}
func TestDuplicateJSONAndMismatchedIdentityRejected(t *testing.T) {
	q := messageFixture("msg_1", "user", "Question", "")
	a := messageFixture("msg_2", "assistant", "Answer", "stop")
	for _, data := range []string{`{"type":"text","type":"reasoning","text":"ambiguous"}`, `{"type":"text","sessionID":"ses_other","text":"wrong source"}`} {
		q.Parts[0].Data = json.RawMessage(data)
		if _, _, err := stableRecords(sessionRow{ID: "ses_one", Directory: "/project", Created: 1789000000000}, []rawMessage{q, a}); err == nil {
			t.Fatal("ambiguous source accepted", data)
		}
	}
}
