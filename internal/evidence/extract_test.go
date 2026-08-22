package evidence

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/session"
)

const (
	testTimestamp = "2026-08-22T10:00:00Z"
	testHash      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	canary        = "sk-canary-123456789012345678901234567890"
)

func record(t *testing.T, line int, payload string) session.Record {
	t.Helper()
	return session.Record{
		Line:       line,
		Timestamp:  testTimestamp,
		Type:       "response_item",
		Payload:    json.RawMessage(payload),
		SourceHash: testHash,
	}
}

func messageRecord(t *testing.T, line int, id, role, text string) session.Record {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type": "message",
		"id":   id,
		"role": role,
		"content": []map[string]string{{
			"type": "input_text",
			"text": text,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return session.Record{Line: line, Timestamp: testTimestamp, Type: "response_item", Payload: payload, SourceHash: testHash}
}

func newExtractor(t *testing.T, sessionID, cwd string, from int, limits Limits) *Extractor {
	t.Helper()
	x, err := New(sessionID, cwd, from, redact.Default(), limits)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestExtractorIncludesOnlyAllowlistedEvidence(t *testing.T) {
	x := newExtractor(t, "s1", "/work/project", 1, DefaultLimits())
	inputs := []session.Record{
		record(t, 1, `{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"goal"},{"type":"image_url","text":"hidden"}]}`),
		record(t, 2, `{"type":"message","id":"a1","role":"assistant","content":[{"type":"output_text","text":"result"}]}`),
		record(t, 3, `{"type":"custom_tool_call","id":"c1","name":"exec_command","input":"{\"cmd\":\"go test ./...\"}"}`),
		record(t, 4, `{"type":"custom_tool_call_output","id":"o1","call_id":"c1","output":"PASS"}`),
		{Line: 5, Timestamp: testTimestamp, Type: "turn_context", Payload: json.RawMessage(`{"cwd":"/work/next"}`), SourceHash: testHash},
	}
	for _, input := range inputs {
		if err := x.Add(input); err != nil {
			t.Fatal(err)
		}
	}

	got := x.Packet()
	if got.SchemaVersion != 1 || got.FromCursor != 1 || got.ToCursor != 5 || got.HasMore {
		t.Fatalf("packet header=%+v", got)
	}
	if len(got.Events) != 5 {
		t.Fatalf("events=%+v", got.Events)
	}
	wantKinds := []string{"message", "message", "tool_call", "tool_result", "cwd_change"}
	for i, want := range wantKinds {
		if got.Events[i].Kind != want {
			t.Fatalf("event %d kind=%q want %q", i, got.Events[i].Kind, want)
		}
	}
	if got.Events[0].Summary != "goal" || got.Events[1].Summary != "result" {
		t.Fatalf("message summaries=%q, %q", got.Events[0].Summary, got.Events[1].Summary)
	}
	if got.Events[2].ToolName != "exec_command" || got.Events[4].Summary != "/work/next" {
		t.Fatalf("tool/cwd evidence=%+v", got.Events)
	}
}

func TestExtractorExcludesUnsafeAndUnknownContent(t *testing.T) {
	x := newExtractor(t, "s1", "/work/project", 1, DefaultLimits())
	inputs := []session.Record{
		record(t, 1, `{"type":"message","id":"d1","role":"developer","content":[{"type":"input_text","text":"developer-canary"}]}`),
		record(t, 2, `{"type":"message","id":"s1","role":"system","content":[{"type":"input_text","text":"system-canary"}]}`),
		record(t, 3, `{"type":"reasoning","id":"r1","summary":[{"text":"reasoning-canary"}]}`),
		record(t, 4, `{"type":"encrypted_content","id":"e1","content":"encrypted-canary"}`),
		record(t, 5, `{"type":"compaction","id":"c1","summary":"compaction-canary"}`),
		record(t, 6, `{"type":"future_unknown","payload":"unknown-canary"}`),
		{Line: 7, Type: "session_meta", Payload: json.RawMessage(`{"environment":"environment-canary"}`)},
	}
	for _, input := range inputs {
		if err := x.Add(input); err != nil {
			t.Fatal(err)
		}
	}

	got := x.Packet()
	if len(got.Events) != 0 || got.ToCursor != 7 {
		t.Fatalf("packet=%+v", got)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "canary") {
		t.Fatalf("excluded content leaked: %s", b)
	}
}

func TestExtractorMalformedPayloadErrorDoesNotEchoPayload(t *testing.T) {
	x := newExtractor(t, "s1", "/work/project", 1, DefaultLimits())
	secret := "malformed-canary"
	err := x.Add(record(t, 1, `{"type":"message","role":"user","content":"`+secret))
	if err == nil {
		t.Fatal("expected malformed payload error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked payload: %v", err)
	}
	if got := x.Packet(); got.ToCursor != 0 || len(got.Events) != 0 {
		t.Fatalf("malformed record advanced packet: %+v", got)
	}
}

func TestExtractorSkipsExcludedMessageBeforeDecodingContent(t *testing.T) {
	x := newExtractor(t, "s1", "/work/project", 1, DefaultLimits())
	inputs := []session.Record{
		record(t, 1, `{"type":"message","id":"d1","role":"developer","content":"excluded-canary"}`),
		record(t, 2, `{"type":"message","id":"s1","role":"system","content":{"nested":"excluded-canary"}}`),
		record(t, 3, `{"type":"message","id":"x1","role":"future_role","content":42}`),
	}
	for _, input := range inputs {
		if err := x.Add(input); err != nil {
			t.Fatalf("excluded role %d decoded content: %v", input.Line, err)
		}
	}
	packet := x.Packet()
	if len(packet.Events) != 0 || packet.ToCursor != 3 {
		t.Fatalf("excluded messages changed evidence: %+v", packet)
	}
}

func TestExtractorRedactsEveryPersistenceVisibleTextField(t *testing.T) {
	x := newExtractor(t, "session "+canary, "/work/"+canary, 1, DefaultLimits())
	payload, err := json.Marshal(map[string]any{
		"type":   "custom_tool_call",
		"id":     "item " + canary,
		"name":   "tool " + canary,
		"input":  "OPENAI_API_KEY=" + canary,
		"unused": "ignored " + canary,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := session.Record{
		Line:       1,
		Timestamp:  "timestamp " + canary,
		Type:       "response_item",
		Payload:    payload,
		SourceHash: "source " + canary,
	}
	if err := x.Add(input); err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(x.Packet())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "canary") {
		t.Fatalf("packet leaked canary: %s", b)
	}
	if !strings.Contains(string(b), "redacted:openai_key:") {
		t.Fatalf("redaction warning missing: %s", b)
	}
	for _, warning := range x.Packet().Warnings {
		if ok, _ := regexp.MatchString(`^redacted:[a-z0-9_]+:[1-9][0-9]*$`, warning); !ok {
			t.Fatalf("warning contains more than rule/count: %q", warning)
		}
	}
}

func TestExtractorBoundsUnicodeSummaryIncludingMarker(t *testing.T) {
	x := newExtractor(t, "s", "/", 1, Limits{MaxEvents: 1, MaxSummaryRunes: 5, MaxPacketRunes: 1000})
	if err := x.Add(messageRecord(t, 1, "u", "user", "甲乙丙丁戊己庚")); err != nil {
		t.Fatal(err)
	}
	got := x.Packet().Events[0].Summary
	if utf8.RuneCountInString(got) != 5 || !strings.Contains(got, "…") {
		t.Fatalf("bounded summary=%q runes=%d", got, utf8.RuneCountInString(got))
	}
}

func TestExtractorRedactsBeforeTruncating(t *testing.T) {
	x := newExtractor(t, "s", "/", 1, Limits{MaxEvents: 1, MaxSummaryRunes: 24, MaxPacketRunes: 1000})
	if err := x.Add(messageRecord(t, 1, "u", "user", strings.Repeat("x", 30)+" OPENAI_API_KEY="+canary)); err != nil {
		t.Fatal(err)
	}
	packet := x.Packet()
	if len(packet.Warnings) != 1 || !strings.HasSuffix(packet.Warnings[0], ":1") {
		t.Fatalf("secret beyond truncation boundary was not inspected: %+v", packet)
	}
	if strings.Contains(packet.Events[0].Summary, "canary") || utf8.RuneCountInString(packet.Events[0].Summary) > 24 {
		t.Fatalf("summary exceeded limit: %q", packet.Events[0].Summary)
	}
}

func TestExtractorPacketTextLimitHasExactBoundary(t *testing.T) {
	base := newExtractor(t, "s", "/", 1, Limits{MaxEvents: 1, MaxSummaryRunes: 100, MaxPacketRunes: 1000})
	input := messageRecord(t, 1, "u", "user", "甲乙")
	if err := base.Add(input); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(base.Packet())
	if err != nil {
		t.Fatal(err)
	}
	exact := utf8.RuneCount(encoded)

	atLimit := newExtractor(t, "s", "/", 1, Limits{MaxEvents: 1, MaxSummaryRunes: 100, MaxPacketRunes: exact})
	if err := atLimit.Add(input); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}

	belowLimit := newExtractor(t, "s", "/", 1, Limits{MaxEvents: 1, MaxSummaryRunes: 100, MaxPacketRunes: exact - 1})
	if err := belowLimit.Add(input); !errors.Is(err, ErrPacketFull) {
		t.Fatalf("below exact limit err=%v", err)
	}
	packet := belowLimit.Packet()
	if len(packet.Events) != 0 || packet.ToCursor != 0 || !packet.HasMore {
		t.Fatalf("rejected first event changed cursor: %+v", packet)
	}
}

func TestExtractorPacketLimitCountsMetadataAndWarnings(t *testing.T) {
	plain := newExtractor(t, "s", "/", 1, Limits{MaxEvents: 1, MaxSummaryRunes: 100, MaxPacketRunes: 1000})
	if err := plain.Add(messageRecord(t, 1, "u", "user", "visible")); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plain.Packet())
	if err != nil {
		t.Fatal(err)
	}
	plainRunes := utf8.RuneCount(encoded)

	withMetadata := newExtractor(t, "session-name", "/longer/project/path", 1, Limits{MaxEvents: 1, MaxSummaryRunes: 100, MaxPacketRunes: plainRunes})
	err = withMetadata.Add(messageRecord(t, 1, "u", "user", "visible"))
	if !errors.Is(err, ErrPacketFull) {
		t.Fatalf("metadata was not counted: err=%v packet=%+v", err, withMetadata.Packet())
	}

	withWarning := newExtractor(t, "s", "/", 1, Limits{MaxEvents: 1, MaxSummaryRunes: 100, MaxPacketRunes: plainRunes})
	if err := withWarning.Add(messageRecord(t, 1, "u", "user", "OPENAI_API_KEY="+canary)); !errors.Is(err, ErrPacketFull) {
		t.Fatalf("warning was not counted: err=%v packet=%+v", err, withWarning.Packet())
	}
}

func TestExtractorMeasuresAcceptedCursorInProspectivePacket(t *testing.T) {
	input := messageRecord(t, 10, "u", "user", "visible")
	probe := newExtractor(t, "s", "/", 1, DefaultLimits())
	if err := probe.Add(input); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(probe.Packet())
	if err != nil {
		t.Fatal(err)
	}
	limit := utf8.RuneCount(encoded) - 1

	x := newExtractor(t, "s", "/", 1, Limits{MaxEvents: 1, MaxSummaryRunes: 100, MaxPacketRunes: limit})
	if err := x.Add(input); !errors.Is(err, ErrPacketFull) {
		t.Fatalf("cursor digit growth bypassed packet limit: err=%v packet=%+v", err, x.Packet())
	}
	packet := x.Packet()
	if packet.ToCursor != 0 || !packet.HasMore || len(packet.Events) != 0 {
		t.Fatalf("rejected event mutated accepted boundary: %+v", packet)
	}
	encoded, err = json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if got := utf8.RuneCount(encoded); got > limit {
		t.Fatalf("full packet has %d runes, limit %d", got, limit)
	}
}

func TestExtractorMeasuresSkippedCursorInProspectivePacket(t *testing.T) {
	input := record(t, 1000, `{"type":"reasoning","id":"skip"}`)
	probe := newExtractor(t, "s", "/", 1, DefaultLimits())
	if err := probe.Add(input); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(probe.Packet())
	if err != nil {
		t.Fatal(err)
	}
	limit := utf8.RuneCount(encoded) - 1

	x := newExtractor(t, "s", "/", 1, Limits{MaxEvents: 1, MaxSummaryRunes: 100, MaxPacketRunes: limit})
	if err := x.Add(input); !errors.Is(err, ErrPacketFull) {
		t.Fatalf("skipped cursor digit growth bypassed packet limit: err=%v packet=%+v", err, x.Packet())
	}
	packet := x.Packet()
	if packet.ToCursor != 0 || !packet.HasMore || len(packet.Events) != 0 {
		t.Fatalf("rejected skipped record mutated boundary: %+v", packet)
	}
	encoded, err = json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if got := utf8.RuneCount(encoded); got > limit {
		t.Fatalf("full packet has %d runes, limit %d", got, limit)
	}
}

func TestExtractorFullDoesNotAdvancePastRejectedEvent(t *testing.T) {
	x := newExtractor(t, "s1", "/work/project", 1, Limits{MaxEvents: 2, MaxSummaryRunes: 100, MaxPacketRunes: 1000})
	inputs := []session.Record{
		messageRecord(t, 1, "u1", "user", "one"),
		record(t, 2, `{"type":"reasoning","id":"skip"}`),
		messageRecord(t, 3, "u2", "user", "two"),
		messageRecord(t, 4, "u3", "user", "three"),
	}
	for i, input := range inputs {
		err := x.Add(input)
		if i < len(inputs)-1 && err != nil {
			t.Fatal(err)
		}
		if i == len(inputs)-1 && !errors.Is(err, ErrPacketFull) {
			t.Fatalf("err=%v", err)
		}
	}
	packet := x.Packet()
	if len(packet.Events) != 2 || packet.ToCursor != 3 || !packet.HasMore {
		t.Fatalf("packet=%+v", packet)
	}
	if err := x.Add(record(t, 5, `{"type":"reasoning"}`)); !errors.Is(err, ErrPacketFull) {
		t.Fatalf("full extractor resumed unexpectedly: %v", err)
	}
	if x.Packet().ToCursor != 3 {
		t.Fatal("full extractor advanced after boundary")
	}
}

func TestExtractorRejectsInvalidLimitsWithoutPanicking(t *testing.T) {
	cases := []Limits{
		{},
		{MaxEvents: -1, MaxSummaryRunes: 1, MaxPacketRunes: 1},
		{MaxEvents: 1, MaxSummaryRunes: -1, MaxPacketRunes: 1},
		{MaxEvents: 1, MaxSummaryRunes: 1, MaxPacketRunes: -1},
	}
	for _, limits := range cases {
		x, err := New("s", "/", 1, redact.Default(), limits)
		if x != nil || !errors.Is(err, ErrInvalidLimits) {
			t.Fatalf("limits %+v: extractor=%v err=%v", limits, x, err)
		}
	}
}

func TestNewRejectsPacketLimitBelowEnvelopeAndAcceptsExactEquality(t *testing.T) {
	envelope := Packet{
		SchemaVersion: 1,
		SessionID:     "s",
		CWD:           "/",
		FromCursor:    1,
		ToCursor:      0,
		Events:        []Item{},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	exact := utf8.RuneCount(encoded)

	if x, err := New("s", "/", 1, redact.Default(), Limits{MaxEvents: 1, MaxSummaryRunes: 1, MaxPacketRunes: exact}); err != nil || x == nil {
		t.Fatalf("exact envelope limit rejected: extractor=%v err=%v", x, err)
	}
	for _, limit := range []int{exact - 1, 1} {
		x, err := New("s", "/", 1, redact.Default(), Limits{MaxEvents: 1, MaxSummaryRunes: 1, MaxPacketRunes: limit})
		if x != nil || !errors.Is(err, ErrInvalidLimits) {
			t.Fatalf("impossible packet limit %d: extractor=%v err=%v", limit, x, err)
		}
	}
}

func TestNilExtractorDoesNotPanic(t *testing.T) {
	var x *Extractor
	if err := x.Add(messageRecord(t, 1, "u", "user", "text")); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("nil extractor err=%v", err)
	}
	if got := x.Packet(); got.SchemaVersion != 0 || len(got.Events) != 0 || len(got.Warnings) != 0 {
		t.Fatalf("nil extractor packet=%+v", got)
	}
}

func TestPacketReturnsIndependentDeterministicSnapshot(t *testing.T) {
	x := newExtractor(t, "s1", "/work/project", 1, DefaultLimits())
	if err := x.Add(messageRecord(t, 1, "u1", "user", "OPENAI_API_KEY="+canary)); err != nil {
		t.Fatal(err)
	}

	first := x.Packet()
	first.Events[0].Summary = "mutated"
	first.Events = append(first.Events, Item{Summary: "injected"})
	first.Warnings[0] = "mutated"
	second := x.Packet()
	third := x.Packet()
	if second.Events[0].Summary == "mutated" || len(second.Events) != 1 || second.Warnings[0] == "mutated" {
		t.Fatalf("Packet exposed mutable internal slices: %+v", second)
	}
	b2, _ := json.Marshal(second)
	b3, _ := json.Marshal(third)
	if string(b2) != string(b3) {
		t.Fatalf("repeated Packet calls differ:\n%s\n%s", b2, b3)
	}
}

func TestExtractorGeneratesUniqueStableEventIDsForDuplicateItemIDs(t *testing.T) {
	build := func() Packet {
		x := newExtractor(t, "s1", "/work/project", 1, DefaultLimits())
		if err := x.Add(messageRecord(t, 1, "duplicate", "user", "one")); err != nil {
			t.Fatal(err)
		}
		if err := x.Add(messageRecord(t, 2, "duplicate", "user", "two")); err != nil {
			t.Fatal(err)
		}
		return x.Packet()
	}
	first, second := build(), build()
	if first.Events[0].ID == first.Events[1].ID {
		t.Fatalf("duplicate event IDs: %+v", first.Events)
	}
	if first.Events[0].ID != second.Events[0].ID || first.Events[1].ID != second.Events[1].ID {
		t.Fatalf("event IDs are not stable: %+v vs %+v", first.Events, second.Events)
	}
}
