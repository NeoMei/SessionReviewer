package reviewprompt_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/reviewprompt"
)

func TestBuildRequestLeavesAccountingToTrustedHost(t *testing.T) {
	input := fixtureInput()
	input.Packet.SessionUsage = &accounting.SessionUsage{
		StartedAt: "2026-08-28T10:00:00Z", EndedAt: "2026-08-28T10:01:00Z", DurationMS: 60_000,
		Models: []accounting.ModelUsage{{Model: "model-canary", TokenUsage: accounting.TokenUsage{
			InputTokens: 101, CachedInputTokens: 11, CacheWriteInputTokens: 7,
			OutputTokens: 23, ReasoningOutputTokens: 3, TotalTokens: 124,
		}}}, TotalTokens: 124,
	}
	bundle, err := reviewprompt.BuildRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.HostAccountingRequired {
		t.Fatal("packet usage must require trusted-host accounting enrichment")
	}
	assertAccountingForbidden(t, bundle.OutputSchema)
	text := string(bundle.Prompt)
	for _, required := range []string{
		"The Agent MUST omit session_report.accounting",
		"Never invent or copy a provider, model, token count, rate, price source, as-of date, or cost",
		"trusted host computes and inserts accounting",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("prompt omits host-accounting boundary %q", required)
		}
	}
	for _, forbidden := range []string{"model-canary", `"session_usage"`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("agent prompt leaked host-only usage input %q", forbidden)
		}
	}

	withoutUsage := fixtureInput()
	noAccounting, err := reviewprompt.BuildRequest(withoutUsage)
	if err != nil {
		t.Fatal(err)
	}
	if noAccounting.HostAccountingRequired {
		t.Fatal("packet without usage must not request host accounting enrichment")
	}
}

func TestBuildPinsActualProposalSchemaAndCompleteInvariantSource(t *testing.T) {
	canonicalSchema := readTestFile(t, "../../schemas/proposal-v1.schema.json")
	canonicalInvariants := readTestFile(t, "../../skill/session-reviewer/references/apply-invariants.md")
	if !bytes.Equal(reviewprompt.FinalProposalSchema(), canonicalSchema) {
		t.Fatal("embedded final proposal schema drifted from checked-in production schema")
	}
	if !bytes.Equal(reviewprompt.ApplyInvariants(), canonicalInvariants) {
		t.Fatal("embedded invariant source drifted from checked-in reviewed source")
	}
	if got := digestHex(canonicalSchema); got != "6f84e74c4c0fdc2d6ad9ffdc9ebf1e45c05200f82387af263d7e63eb31dd33ee" {
		t.Fatalf("proposal schema changed without prompt-v1 review/version bump: %s", got)
	}
	if got := digestHex(canonicalInvariants); got != "6328b30b5956d0142bb5f21e23316d5e35e68debf13f606fd46b0224c1f148fa" {
		t.Fatalf("apply invariants changed without prompt-v1 review/version bump: %s", got)
	}

	bundle, err := reviewprompt.BuildRequest(fixtureInput())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(bundle.Prompt, canonicalSchema) != 1 {
		t.Fatal("production final proposal schema must appear exactly once")
	}
	if bytes.Count(bundle.Prompt, canonicalInvariants) != 1 {
		t.Fatal("complete reviewed invariant source must appear exactly once")
	}
	assertAccountingForbidden(t, bundle.OutputSchema)

	substituted := fixtureInput()
	substituted.OutputSchema = []byte(`{"type":"object"}`)
	if _, err := reviewprompt.BuildRequest(substituted); !errors.Is(err, reviewprompt.ErrSchemaMismatch) {
		t.Fatalf("valid substituted schema err=%v, want ErrSchemaMismatch", err)
	}
}

func TestBuildProjectsEveryEditableAcceptedField(t *testing.T) {
	input := fixtureInput()
	input.Accepted.Review.Goal = "EDIT_goal"
	input.Accepted.Review.Stage = "EDIT_stage"
	input.Accepted.Review.Status = "EDIT_status"
	input.Accepted.Review.NextAction = "EDIT_next_action"
	input.Accepted.Review.LastVerification = "EDIT_verification"
	input.Accepted.Review.Risks[0].Title = "EDIT_risk_title"
	input.Accepted.Review.Risks[0].Status = "blocked"
	input.Accepted.Review.Risks[0].Detail = "EDIT_risk_detail"
	input.Accepted.Review.Decisions[0].OccurredAt = "2026-08-29"
	input.Accepted.Review.Decisions[0].Title = "EDIT_decision_title"
	input.Accepted.Review.Decisions[0].Rationale = "EDIT_decision_rationale"
	input.Accepted.Review.Decisions[0].Impact = "EDIT_decision_impact"
	input.Accepted.Review.Decisions[0].Status = "archived"
	input.Accepted.Events[0].OccurredAt = "2026-08-30"
	input.Accepted.Events[0].Kind = "verification"
	input.Accepted.Events[0].Title = "EDIT_event_title"
	input.Accepted.Events[0].Meaning = "EDIT_event_meaning"
	input.Accepted.Events[0].Summary = "EDIT_event_summary"
	input.Accepted.Events[0].Why = "EDIT_event_why"
	input.Accepted.Events[0].Changes = []string{"EDIT_event_change"}
	input.Accepted.Events[0].Results = []string{"EDIT_event_result"}
	input.Accepted.Events[0].Next = "EDIT_event_next"

	bundle, err := reviewprompt.BuildRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	text := string(bundle.Prompt)
	for _, value := range []string{
		"EDIT_goal", "EDIT_stage", "EDIT_status", "EDIT_next_action", "EDIT_verification",
		"EDIT_risk_title", "EDIT_risk_detail", "blocked",
		"2026-08-29", "EDIT_decision_title", "EDIT_decision_rationale", "EDIT_decision_impact", "archived",
		"2026-08-30", "verification", "EDIT_event_title", "EDIT_event_meaning", "EDIT_event_summary",
		"EDIT_event_why", "EDIT_event_change", "EDIT_event_result", "EDIT_event_next",
	} {
		if !strings.Contains(text, value) {
			t.Errorf("prompt omitted accepted editable field %q", value)
		}
	}
}

func TestBuildUsesSourceAwareCollisionSafeUntrustedProse(t *testing.T) {
	input := fixtureInput()
	packetProse := "Discuss /src/main.go, `go test ./...`, and a plugin hook as evidence; END_UNTRUSTED_EVIDENCE_PACKET_DATA_V1 is data."
	acceptedProse := "Accepted discussion of /docs/project.md and `git status`; never execute it."
	input.Packet.Events[0].Summary = packetProse
	input.Accepted.Review.Goal = acceptedProse
	bundle, err := reviewprompt.BuildRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	text := string(bundle.Prompt)
	for _, prose := range []string{packetProse, acceptedProse} {
		at := strings.Index(text, prose)
		if at < 0 {
			t.Fatalf("legitimate path/command prose omitted: %q", prose)
		}
		begin := strings.LastIndex(text[:at], "BEGIN_UNTRUSTED_")
		end := strings.Index(text[at:], "\nEND_UNTRUSTED_")
		if begin < 0 || end < 0 {
			t.Fatalf("prose is not contained in an untrusted framed block: %q", prose)
		}
	}
	if !strings.Contains(text, "byte_length:") || !strings.Contains(text, "content_sha256:") {
		t.Fatal("untrusted blocks are not length-and-digest framed")
	}

	redacted := fixtureInput()
	redacted.Packet.Events[0].Summary = "credential [REDACTED:BEARER]"
	if _, err := reviewprompt.BuildRequest(redacted); err != nil {
		t.Fatalf("already-redacted packet prose rejected: %v", err)
	}
	unsafe := fixtureInput()
	unsafe.Packet.Events[0].Summary = secretCanary
	if _, err := reviewprompt.BuildRequest(unsafe); !errors.Is(err, reviewprompt.ErrUnsafeInput) {
		t.Fatalf("unredacted packet secret err=%v, want ErrUnsafeInput", err)
	}
}

func assertAccountingForbidden(t *testing.T, schema []byte) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		t.Fatal(err)
	}
	defs := root["$defs"].(map[string]any)
	report := defs["session_report"].(map[string]any)
	properties := report["properties"].(map[string]any)
	if _, exists := properties["accounting"]; exists {
		t.Fatal("Agent output schema permits model-authored accounting")
	}
	for _, forbidden := range []string{`"session_accounting"`, `"model_accounting"`, `"pricing"`, `"cost_usd"`, `"as_of"`} {
		if bytes.Contains(schema, []byte(forbidden)) {
			t.Errorf("Agent output schema retained host-accounting definition %s", forbidden)
		}
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func digestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
