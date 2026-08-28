package reviewprompt_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/reviewprompt"
	"github.com/neomei/SessionReviewer/internal/session"
)

func TestBuildRejectsEveryMalformedOrSecretPacketEnvelopeWithZeroBundle(t *testing.T) {
	const jsMaxInt = 1<<53 - 1
	tests := map[string]func(*reviewprompt.Input){
		"schema":             func(input *reviewprompt.Input) { input.Packet.SchemaVersion = 1 },
		"project blank":      func(input *reviewprompt.Input) { input.Packet.ProjectID = "" },
		"project secret":     func(input *reviewprompt.Input) { input.Packet.ProjectID = secretCanary },
		"session blank":      func(input *reviewprompt.Input) { input.Packet.SessionID = "" },
		"session whitespace": func(input *reviewprompt.Input) { input.Packet.SessionID = " s1 " },
		"session secret":     func(input *reviewprompt.Input) { input.Packet.SessionID = secretCanary },
		"cwd blank":          func(input *reviewprompt.Input) { input.Packet.CWD = " " },
		"cwd secret":         func(input *reviewprompt.Input) { input.Packet.CWD = secretCanary },
		"from zero":          func(input *reviewprompt.Input) { input.Packet.FromCursor = 0 },
		"from unsafe":        func(input *reviewprompt.Input) { input.Packet.FromCursor = jsMaxInt + 1 },
		"to negative":        func(input *reviewprompt.Input) { input.Packet.ToCursor = -1 },
		"to unsafe":          func(input *reviewprompt.Input) { input.Packet.ToCursor = jsMaxInt + 1 },
		"range gap":          func(input *reviewprompt.Input) { input.Packet.ToCursor = input.Packet.FromCursor - 2 },
		"expected line":      func(input *reviewprompt.Input) { input.Packet.ExpectedCursor.Line++ },
		"expected hash secret": func(input *reviewprompt.Input) {
			input.Packet.ExpectedCursor.SourceHash = secretCanary
		},
		"expected hash uppercase": func(input *reviewprompt.Input) {
			input.Packet.ExpectedCursor.SourceHash = strings.Repeat("A", 64)
		},
		"next line": func(input *reviewprompt.Input) { input.Packet.NextCursor.Line-- },
		"next hash secret": func(input *reviewprompt.Input) {
			input.Packet.NextCursor.SourceHash = secretCanary
		},
		"next hash uppercase": func(input *reviewprompt.Input) {
			input.Packet.NextCursor.SourceHash = strings.Repeat("B", 64)
		},
		"line zero hash": func(input *reviewprompt.Input) {
			input.Packet.FromCursor = 1
			input.Packet.ExpectedCursor = evidence.CursorBoundary{SourceHash: strings.Repeat("a", 64)}
		},
		"empty boundaries unequal": func(input *reviewprompt.Input) {
			input.Packet.ToCursor = input.Packet.FromCursor - 1
			input.Packet.NextCursor = evidence.CursorBoundary{Line: input.Packet.ToCursor, SourceHash: strings.Repeat("5", 64)}
			input.Packet.Events = nil
		},
		"event before range": func(input *reviewprompt.Input) { input.Packet.Events[0].JSONLLine = input.Packet.FromCursor - 1 },
		"event after range":  func(input *reviewprompt.Input) { input.Packet.Events[0].JSONLLine = input.Packet.ToCursor + 1 },
		"tail hash mismatch": func(input *reviewprompt.Input) {
			input.Packet.NextCursor.SourceHash = strings.Repeat("4", 64)
		},
		"invalid warning": func(input *reviewprompt.Input) { input.Packet.Warnings = []string{"redacted:not-canonical"} },
		"invalid usage": func(input *reviewprompt.Input) {
			input.Packet.SessionUsage = &accounting.SessionUsage{}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := fixtureInput()
			mutate(&input)
			bundle, err := reviewprompt.Build(input)
			if err != reviewprompt.ErrInvalidInput {
				t.Fatalf("err=%v want exact ErrInvalidInput", err)
			}
			if !reflect.DeepEqual(bundle, reviewprompt.Bundle{}) {
				t.Fatalf("failure returned nonzero bundle: %+v", bundle)
			}
		})
	}
}

func TestBuildAcceptsCanonicalEmptyPacketBoundary(t *testing.T) {
	input := fixtureInput()
	input.Packet.ToCursor = input.Packet.FromCursor - 1
	input.Packet.NextCursor = input.Packet.ExpectedCursor
	input.Packet.Events = []evidence.Item{}
	bundle, err := reviewprompt.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Prompt) == 0 {
		t.Fatal("canonical empty packet produced no prompt")
	}
}

func TestExtractorPacketRoundTripsRedactedIdentifiersAndPunctuation(t *testing.T) {
	const rawKey = "sk-abcdefghijklmnopqrstuvwxyz123456"
	x, err := evidence.NewWithProjectID(
		"project-1111111111111111",
		rawKey,
		"/safe/project",
		1,
		redact.Default(),
		evidence.DefaultLimits(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.SetExpectedCursor(evidence.CursorBoundary{}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"type":  "custom_tool_call",
		"id":    rawKey,
		"name":  "mcp/tool:name+variant?",
		"input": `{"cmd":"go test ./..."}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	record := session.Record{
		Line: 1, Timestamp: "2026-08-28T10:00:00.123456789Z", Type: "response_item",
		Payload: payload, SourceHash: strings.Repeat("a", 64),
	}
	if err := x.Add(record); err != nil {
		t.Fatal(err)
	}
	packet := x.Packet()
	if packet.SessionID != "[REDACTED:OPENAI_KEY]" || packet.Events[0].ItemID != "[REDACTED:OPENAI_KEY]" {
		t.Fatalf("extractor did not emit expected redacted identifiers: session=%q item=%q", packet.SessionID, packet.Events[0].ItemID)
	}
	input := fixtureInput()
	input.Packet = packet
	bundle, err := reviewprompt.Build(input)
	if err != nil {
		t.Fatalf("Build rejected a canonical extractor packet: %v", err)
	}
	text := string(bundle.Prompt)
	for _, value := range []string{"[REDACTED:OPENAI_KEY]", "mcp/tool:name+variant?"} {
		if !strings.Contains(text, value) {
			t.Errorf("prompt omitted extractor-preserved value %q", value)
		}
	}
}

func TestBuildBoundsExternalPacketItemIdentifiers(t *testing.T) {
	tests := map[string]struct {
		set func(*reviewprompt.Input, string)
	}{
		"item_id": {set: func(input *reviewprompt.Input, value string) { input.Packet.Events[0].ItemID = value }},
		"tool_name": {set: func(input *reviewprompt.Input, value string) {
			input.Packet.Events[0].Kind = "tool_result"
			input.Packet.Events[0].Role = ""
			input.Packet.Events[0].ToolName = value
		}},
	}
	for name, test := range tests {
		t.Run(name+" permits boundary", func(t *testing.T) {
			input := fixtureInput()
			test.set(&input, strings.Repeat("x", 4096))
			if _, err := reviewprompt.Build(input); err != nil {
				t.Fatalf("bounded external identifier rejected: %v", err)
			}
		})
		t.Run(name+" rejects over boundary", func(t *testing.T) {
			input := fixtureInput()
			test.set(&input, strings.Repeat("x", 4097))
			bundle, err := reviewprompt.Build(input)
			if err != reviewprompt.ErrInvalidInput || !reflect.DeepEqual(bundle, reviewprompt.Bundle{}) {
				t.Fatalf("bundle=%+v err=%v want zero bundle and ErrInvalidInput", bundle, err)
			}
		})
	}
}

func TestBuildRejectsMissingDuplicateOrNonmonotonicPacketEventIdentity(t *testing.T) {
	hash6 := strings.Repeat("6", 64)
	second := func() evidence.Item {
		item := fixtureInput().Packet.Events[0]
		item.ID = "ev-222222222222"
		item.JSONLLine = 6
		item.SourceHash = hash6
		item.Timestamp = "2026-08-28T10:00:01Z"
		return item
	}
	tests := map[string]func(*reviewprompt.Input){
		"missing id": func(input *reviewprompt.Input) { input.Packet.Events[0].ID = "" },
		"duplicate id": func(input *reviewprompt.Input) {
			input.Packet.ToCursor = 6
			input.Packet.NextCursor = evidence.CursorBoundary{Line: 6, SourceHash: hash6}
			item := second()
			item.ID = input.Packet.Events[0].ID
			input.Packet.Events = append(input.Packet.Events, item)
		},
		"nonmonotonic line": func(input *reviewprompt.Input) {
			input.Packet.ToCursor = 6
			input.Packet.NextCursor = evidence.CursorBoundary{Line: 6, SourceHash: hash6}
			first := second()
			last := input.Packet.Events[0]
			input.Packet.Events = []evidence.Item{first, last}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := fixtureInput()
			mutate(&input)
			bundle, err := reviewprompt.Build(input)
			if err != reviewprompt.ErrInvalidInput || !reflect.DeepEqual(bundle, reviewprompt.Bundle{}) {
				t.Fatalf("bundle=%+v err=%v want zero bundle and ErrInvalidInput", bundle, err)
			}
		})
	}
}
