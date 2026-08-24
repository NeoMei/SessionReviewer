package proposal

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

const (
	projectID = "project-1111111111111111"
	sessionID = "session-1"
	hashA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	jsMaxInt  = 9007199254740991
)

func fixedPacket() evidence.Packet {
	return evidence.Packet{
		SchemaVersion: 2,
		ProjectID:     projectID,
		SessionID:     sessionID,
		CWD:           "/repo",
		FromCursor:    1,
		ToCursor:      2,
		ExpectedCursor: evidence.CursorBoundary{
			Line: 0,
		},
		NextCursor: evidence.CursorBoundary{Line: 2, SourceHash: hashB},
		Events: []evidence.Item{
			{ID: "ev-message", Timestamp: "2026-08-23T01:02:03Z", JSONLLine: 1, SourceHash: hashA, Kind: "message", Role: "user", Summary: "Choose durable ledger"},
			{ID: "ev-verify", Timestamp: "2026-08-23T01:03:03Z", JSONLLine: 2, SourceHash: hashB, Kind: "tool_result", ToolName: "exec_command", Summary: "go test passed"},
		},
	}
}

func fixedState() ledger.State {
	return ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID,
			Revision:  0,
		},
		Decisions: make(map[string]ledger.Decision),
		OpenLoops: make(map[string]ledger.OpenLoop),
		Sessions:  make(map[string]ledger.SessionReport),
	}
}

func fixturePath(name string) string {
	return filepath.Join("..", "..", "testdata", "proposals", name)
}

func mustFixture(t *testing.T, name string) Proposal {
	t.Helper()
	f, err := os.Open(fixturePath(name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p, err := Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fixedProposalPacketState(t *testing.T, name string) (Proposal, evidence.Packet, ledger.State) {
	t.Helper()
	return mustFixture(t, name), fixedPacket(), fixedState()
}

func cloneProposal(t *testing.T, p Proposal) Proposal {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var clone Proposal
	if err := json.Unmarshal(b, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func strptr(s string) *string { return &s }

func evidenceptr(refs []ledger.EvidenceRef) *[]ledger.EvidenceRef { return &refs }

func TestDecodeStrictEnvelope(t *testing.T) {
	valid, err := os.ReadFile(fixturePath("valid-first.json"))
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string][]byte{
		"unknown top-level field": bytes.Replace(valid, []byte(`"schema_version": 1,`), []byte(`"schema_version": 1, "extra": true,`), 1),
		"unknown nested field":    bytes.Replace(valid, []byte(`"id": "decision-1",`), []byte(`"id": "decision-1", "extra": true,`), 1),
		"missing nested required": bytes.Replace(valid, []byte("      \"context\": \"Long sessions need durable continuity.\",\n"), nil, 1),
		"trailing value":          append(append([]byte(nil), valid...), []byte(` {}`)...),
		"required null":           bytes.Replace(valid, []byte(`"new_decisions": [`), []byte(`"new_decisions": null, "discarded": [`), 1),
		"optional explicit null":  bytes.Replace(valid, []byte(`"goal": "Ship the durable ledger"`), []byte(`"goal": null`), 1),
		"non-JSON NaN":            bytes.Replace(valid, []byte(`"schema_version": 1`), []byte(`"schema_version": NaN`), 1),
		"integer overflow":        bytes.Replace(valid, []byte(`"expected_revision": 0`), []byte(`"expected_revision": 999999999999999999999999999999999`), 1),
		"non JS-safe integer":     bytes.Replace(valid, []byte(`"expected_revision": 0`), []byte(`"expected_revision": 9007199254740992`), 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(bytes.NewReader(body)); err == nil {
				t.Fatal("accepted malformed proposal")
			}
		})
	}

	t.Run("omitted optional pointer is distinct from null", func(t *testing.T) {
		body := bytes.Replace(valid, []byte("    \"last_verified\": \"2026-08-23T01:03:03Z\",\n"), nil, 1)
		if _, err := Decode(bytes.NewReader(body)); err != nil {
			t.Fatalf("omitted optional field rejected: %v", err)
		}
	})

	t.Run("four MiB limit", func(t *testing.T) {
		body := bytes.Repeat([]byte(" "), 4*1024*1024+1)
		if _, err := Decode(bytes.NewReader(body)); err == nil {
			t.Fatal("accepted oversized proposal")
		}
	})
}

func TestValidateExactPacket(t *testing.T) {
	p, packet, state := fixedProposalPacketState(t, "valid-first.json")
	changes, err := Validate(p, packet, state)
	if err != nil || len(changes.Decisions) != 1 {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	if changes.Current == nil || changes.Current.Revision != 1 || len(changes.Timeline) != 1 || len(changes.Sessions) != 1 {
		t.Fatalf("incomplete changes: %+v", changes)
	}
}

func TestValidateAcceptsDescriptiveLastVerifiedState(t *testing.T) {
	p, packet, state := fixedProposalPacketState(t, "valid-first.json")
	p.CurrentStatePatch.LastVerified = strptr("Focused tests pass")

	changes, err := Validate(p, packet, state)
	if err != nil {
		t.Fatalf("descriptive last-verified state rejected: %v", err)
	}
	if changes.Current == nil || changes.Current.LastVerified != "Focused tests pass" {
		t.Fatalf("current state=%+v", changes.Current)
	}
}

func TestValidateBindsSessionAccountingToPacketUsage(t *testing.T) {
	p, packet, state := fixedProposalPacketState(t, "valid-first.json")
	packet.SessionUsage = &accounting.SessionUsage{StartedAt: "2026-08-23T01:00:00Z", EndedAt: "2026-08-23T01:03:03Z", DurationMS: 183000, Models: []accounting.ModelUsage{{Model: "gpt-5.6-sol", TokenUsage: accounting.TokenUsage{InputTokens: 1000, CachedInputTokens: 400, CacheWriteInputTokens: 100, OutputTokens: 200, ReasoningOutputTokens: 50, TotalTokens: 1200}}}, TotalTokens: 1200}
	digest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	p.EvidencePacketSHA256 = digest
	p.SessionReport.Accounting = &accounting.SessionAccounting{StartedAt: packet.SessionUsage.StartedAt, EndedAt: packet.SessionUsage.EndedAt, DurationMS: packet.SessionUsage.DurationMS, Models: []accounting.ModelAccounting{{ModelUsage: packet.SessionUsage.Models[0], Pricing: accounting.Pricing{Currency: "USD", InputPerMillion: 4, CachedInputPerMillion: .4, CacheWriteInputPerMillion: 5, OutputPerMillion: 20, Source: "https://developers.openai.com/api/docs/models/gpt-5.6-sol", AsOf: "2026-08-24"}, CostUSD: .00666}}, TotalTokens: 1200, TotalCostUSD: .00666}
	if _, err := Validate(p, packet, state); err != nil {
		t.Fatal(err)
	}
	p.SessionReport.Accounting.TotalCostUSD = 1
	if _, err := Validate(p, packet, state); err == nil {
		t.Fatal("accepted incorrect session cost")
	}
}

func TestValidateRejectsUnknownEvidence(t *testing.T) {
	p, packet, state := fixedProposalPacketState(t, "invalid-evidence.json")
	if changes, err := Validate(p, packet, state); err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
		t.Fatalf("accepted unknown evidence: changes=%+v err=%v", changes, err)
	}
}

func TestValidateRejectsPacketMismatchAndRedactionFindings(t *testing.T) {
	base, packet, state := fixedProposalPacketState(t, "valid-first.json")
	tests := map[string]func(*Proposal, *evidence.Packet){
		"schema":        func(p *Proposal, _ *evidence.Packet) { p.SchemaVersion = 2 },
		"project":       func(p *Proposal, _ *evidence.Packet) { p.ProjectID = "other-project" },
		"session":       func(p *Proposal, _ *evidence.Packet) { p.SessionID = "other-session" },
		"from cursor":   func(p *Proposal, _ *evidence.Packet) { p.FromCursor++ },
		"to cursor":     func(p *Proposal, _ *evidence.Packet) { p.ToCursor-- },
		"digest":        func(p *Proposal, _ *evidence.Packet) { p.EvidencePacketSHA256 = "sha256:" + hashA },
		"packet schema": func(_ *Proposal, packet *evidence.Packet) { packet.SchemaVersion = 1 },
		"redaction": func(p *Proposal, _ *evidence.Packet) {
			p.NewDecisions[0].Title = "Authorization: Bearer abcdefghijklmnop"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := cloneProposal(t, base)
			gotPacket := packet
			mutate(&p, &gotPacket)
			if _, err := Validate(p, gotPacket, state); err == nil {
				t.Fatal("accepted mismatched or unsafe proposal")
			}
		})
	}
}

func TestValidateRejectsSecretsInPersistedStringsAcrossNestedEntities(t *testing.T) {
	base, packet, state := fixedProposalPacketState(t, "valid-first.json")
	secret := "Authorization: Bearer abcdefghijklmnop"
	addLoop := func(p *Proposal) *ledger.OpenLoop {
		loop := &ledger.OpenLoop{
			ID:                  "loop-canary",
			ProjectID:           projectID,
			Title:               "Investigate",
			Status:              "open",
			Revision:            1,
			Tags:                []string{},
			SourceSessions:      []string{sessionID},
			Evidence:            []ledger.EvidenceRef{p.NewDecisions[0].Evidence[0]},
			Question:            "What remains?",
			Attempts:            []string{},
			Blocker:             "",
			NextExperiment:      "Inspect",
			CompletionCriterion: "Verified",
		}
		p.OpenLoops = []OpenLoopChange{{Operation: "create", Entity: loop}}
		return loop
	}
	tests := map[string]func(*Proposal, *evidence.Packet){
		"packet cwd":           func(_ *Proposal, packet *evidence.Packet) { packet.CWD = secret },
		"packet tool name":     func(_ *Proposal, packet *evidence.Packet) { packet.Events[1].ToolName = secret },
		"packet event summary": func(_ *Proposal, packet *evidence.Packet) { packet.Events[0].Summary = secret },
		"decision title":       func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Title = secret },
		"decision tag":         func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Tags = []string{secret} },
		"decision source session": func(p *Proposal, _ *evidence.Packet) {
			p.NewDecisions[0].SourceSessions = append(p.NewDecisions[0].SourceSessions, secret)
		},
		"decision evidence summary":  func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Evidence[0].Summary = secret },
		"decision context":           func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Context = secret },
		"decision rationale":         func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Rationale = secret },
		"decision consequences":      func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Consequences = secret },
		"decision reevaluate when":   func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].ReevaluateWhen = secret },
		"decision alternative":       func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Alternatives = []string{secret} },
		"decision rejected path":     func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].RejectedPaths = []string{secret} },
		"open-loop title":            func(p *Proposal, _ *evidence.Packet) { addLoop(p).Title = secret },
		"open-loop tag":              func(p *Proposal, _ *evidence.Packet) { addLoop(p).Tags = []string{secret} },
		"open-loop source session":   func(p *Proposal, _ *evidence.Packet) { addLoop(p).SourceSessions = []string{secret} },
		"open-loop evidence summary": func(p *Proposal, _ *evidence.Packet) { addLoop(p).Evidence[0].Summary = secret },
		"open-loop question":         func(p *Proposal, _ *evidence.Packet) { addLoop(p).Question = secret },
		"open-loop attempt":          func(p *Proposal, _ *evidence.Packet) { addLoop(p).Attempts = []string{secret} },
		"open-loop blocker":          func(p *Proposal, _ *evidence.Packet) { addLoop(p).Blocker = secret },
		"open-loop next experiment":  func(p *Proposal, _ *evidence.Packet) { addLoop(p).NextExperiment = secret },
		"open-loop completion":       func(p *Proposal, _ *evidence.Packet) { addLoop(p).CompletionCriterion = secret },
		"timeline title":             func(p *Proposal, _ *evidence.Packet) { p.TimelineEvents[0].Title = secret },
		"timeline summary":           func(p *Proposal, _ *evidence.Packet) { p.TimelineEvents[0].Summary = secret },
		"timeline evidence summary":  func(p *Proposal, _ *evidence.Packet) { p.TimelineEvents[0].Evidence[0].Summary = secret },
		"current goal":               func(p *Proposal, _ *evidence.Packet) { p.CurrentStatePatch.Goal = strptr(secret) },
		"current branch":             func(p *Proposal, _ *evidence.Packet) { p.CurrentStatePatch.Branch = strptr(secret) },
		"current next action":        func(p *Proposal, _ *evidence.Packet) { p.CurrentStatePatch.NextAction = strptr(secret) },
		"current first inspection":   func(p *Proposal, _ *evidence.Packet) { p.CurrentStatePatch.FirstInspection = strptr(secret) },
		"current uncommitted change": func(p *Proposal, _ *evidence.Packet) {
			values := []string{secret}
			p.CurrentStatePatch.UncommittedChanges = &values
		},
		"current blocker": func(p *Proposal, _ *evidence.Packet) {
			values := []string{secret}
			p.CurrentStatePatch.Blockers = &values
		},
		"current risk": func(p *Proposal, _ *evidence.Packet) {
			values := []string{secret}
			p.CurrentStatePatch.OpenRisks = &values
		},
		"current source session": func(p *Proposal, _ *evidence.Packet) {
			values := []string{secret}
			p.CurrentStatePatch.SourceSessions = &values
		},
		"current evidence summary": func(p *Proposal, _ *evidence.Packet) { (*p.CurrentStatePatch.Evidence)[0].Summary = secret },
		"session initial goal":     func(p *Proposal, _ *evidence.Packet) { p.SessionReport.InitialGoal = secret },
		"session goal change":      func(p *Proposal, _ *evidence.Packet) { p.SessionReport.GoalChanges = []string{secret} },
		"session phase title":      func(p *Proposal, _ *evidence.Packet) { p.SessionReport.Phases[0].Title = secret },
		"session phase summary":    func(p *Proposal, _ *evidence.Packet) { p.SessionReport.Phases[0].Summary = secret },
		"session phase evidence":   func(p *Proposal, _ *evidence.Packet) { p.SessionReport.Phases[0].Evidence[0].Summary = secret },
		"session report files":     func(p *Proposal, _ *evidence.Packet) { p.SessionReport.Files = []string{secret} },
		"session report commits":   func(p *Proposal, _ *evidence.Packet) { p.SessionReport.Commits = []string{secret} },
		"session verification":     func(p *Proposal, _ *evidence.Packet) { p.SessionReport.Verification = []string{secret} },
		"previous session id":      func(p *Proposal, _ *evidence.Packet) { p.SessionReport.PreviousSessionID = secret },
		"next session id":          func(p *Proposal, _ *evidence.Packet) { p.SessionReport.NextSessionID = secret },
		"session report evidence":  func(p *Proposal, _ *evidence.Packet) { p.SessionReport.Evidence[0].Summary = secret },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := cloneProposal(t, base)
			gotPacket := packet
			gotPacket.Events = append([]evidence.Item(nil), packet.Events...)
			mutate(&p, &gotPacket)
			digest, err := evidence.Digest(gotPacket)
			if err != nil {
				t.Fatal(err)
			}
			p.EvidencePacketSHA256 = digest
			changes, err := Validate(p, gotPacket, state)
			if err == nil || !strings.Contains(err.Error(), "redaction findings") || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
				t.Fatalf("secret canary persisted: changes=%+v err=%v", changes, err)
			}
		})
	}
}

func TestValidateSafeTextCoversNarrativePatchFields(t *testing.T) {
	secret := "Authorization: Bearer abcdefghijklmnop"
	tests := map[string]func(*Proposal){
		"decision title": func(p *Proposal) { p.UpdatedDecisions = []DecisionPatch{{Title: strptr(secret)}} },
		"decision tags":  func(p *Proposal) { values := []string{secret}; p.UpdatedDecisions = []DecisionPatch{{Tags: &values}} },
		"decision source sessions": func(p *Proposal) {
			values := []string{secret}
			p.UpdatedDecisions = []DecisionPatch{{SourceSessions: &values}}
		},
		"decision evidence summary": func(p *Proposal) {
			values := []ledger.EvidenceRef{{Summary: secret}}
			p.UpdatedDecisions = []DecisionPatch{{Evidence: &values}}
		},
		"decision context":         func(p *Proposal) { p.UpdatedDecisions = []DecisionPatch{{Context: strptr(secret)}} },
		"decision rationale":       func(p *Proposal) { p.UpdatedDecisions = []DecisionPatch{{Rationale: strptr(secret)}} },
		"decision consequences":    func(p *Proposal) { p.UpdatedDecisions = []DecisionPatch{{Consequences: strptr(secret)}} },
		"decision reevaluate when": func(p *Proposal) { p.UpdatedDecisions = []DecisionPatch{{ReevaluateWhen: strptr(secret)}} },
		"decision alternatives": func(p *Proposal) {
			values := []string{secret}
			p.UpdatedDecisions = []DecisionPatch{{Alternatives: &values}}
		},
		"decision rejected paths": func(p *Proposal) {
			values := []string{secret}
			p.UpdatedDecisions = []DecisionPatch{{RejectedPaths: &values}}
		},
		"open-loop title": func(p *Proposal) { p.OpenLoops = []OpenLoopChange{{Patch: &OpenLoopPatch{Title: strptr(secret)}}} },
		"open-loop tags": func(p *Proposal) {
			values := []string{secret}
			p.OpenLoops = []OpenLoopChange{{Patch: &OpenLoopPatch{Tags: &values}}}
		},
		"open-loop source sessions": func(p *Proposal) {
			values := []string{secret}
			p.OpenLoops = []OpenLoopChange{{Patch: &OpenLoopPatch{SourceSessions: &values}}}
		},
		"open-loop evidence summary": func(p *Proposal) {
			values := []ledger.EvidenceRef{{Summary: secret}}
			p.OpenLoops = []OpenLoopChange{{Patch: &OpenLoopPatch{Evidence: &values}}}
		},
		"open-loop question": func(p *Proposal) { p.OpenLoops = []OpenLoopChange{{Patch: &OpenLoopPatch{Question: strptr(secret)}}} },
		"open-loop attempts": func(p *Proposal) {
			values := []string{secret}
			p.OpenLoops = []OpenLoopChange{{Patch: &OpenLoopPatch{Attempts: &values}}}
		},
		"open-loop blocker": func(p *Proposal) { p.OpenLoops = []OpenLoopChange{{Patch: &OpenLoopPatch{Blocker: strptr(secret)}}} },
		"open-loop next experiment": func(p *Proposal) {
			p.OpenLoops = []OpenLoopChange{{Patch: &OpenLoopPatch{NextExperiment: strptr(secret)}}}
		},
		"open-loop completion": func(p *Proposal) {
			p.OpenLoops = []OpenLoopChange{{Patch: &OpenLoopPatch{CompletionCriterion: strptr(secret)}}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			var p Proposal
			mutate(&p)
			if err := validateSafeText(p, evidence.Packet{}); err == nil || !strings.Contains(err.Error(), "redaction findings") {
				t.Fatalf("secret canary in patch accepted: %v", err)
			}
		})
	}
}

func TestValidateRejectsCrossSessionReportOverwrite(t *testing.T) {
	p, packet, state := fixedProposalPacketState(t, "valid-first.json")
	existing := p.SessionReport
	existing.SessionID = "other-session"
	existing.Revision = 1
	state.Sessions[existing.ID] = existing
	p.SessionReport.Revision = 2

	changes, err := Validate(p, packet, state)
	if err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
		t.Fatalf("cross-session report overwrite accepted: changes=%+v err=%v", changes, err)
	}
}

func TestValidateEnforcesOneSessionReportPerSessionID(t *testing.T) {
	base, packet, _ := fixedProposalPacketState(t, "valid-first.json")

	t.Run("same report id updates same session", func(t *testing.T) {
		p := cloneProposal(t, base)
		state := fixedState()
		existing := p.SessionReport
		existing.Revision = 1
		state.Sessions[existing.ID] = existing
		p.SessionReport.Revision = 2
		if _, err := Validate(p, packet, state); err != nil {
			t.Fatalf("same-session report update rejected: %v", err)
		}
	})

	t.Run("different report id cannot claim represented session", func(t *testing.T) {
		p := cloneProposal(t, base)
		state := fixedState()
		existing := p.SessionReport
		existing.ID = "session-report-existing"
		state.Sessions[existing.ID] = existing

		changes, err := Validate(p, packet, state)
		if err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
			t.Fatalf("second report id accepted for one session: changes=%+v err=%v", changes, err)
		}
	})

	t.Run("malformed state with duplicate session ids fails closed", func(t *testing.T) {
		p := cloneProposal(t, base)
		state := fixedState()
		first := p.SessionReport
		first.Revision = 1
		second := first
		second.ID = "session-report-duplicate"
		state.Sessions[first.ID] = first
		state.Sessions[second.ID] = second
		p.SessionReport.Revision = 2

		changes, err := Validate(p, packet, state)
		if err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
			t.Fatalf("duplicate existing session reports accepted: changes=%+v err=%v", changes, err)
		}
	})
}

func TestValidateRejectsEvidenceTupleMismatchAndDuplicates(t *testing.T) {
	base, packet, state := fixedProposalPacketState(t, "valid-first.json")
	tests := map[string]func(*Proposal, *evidence.Packet){
		"duplicate packet id": func(_ *Proposal, packet *evidence.Packet) { packet.Events[1].ID = packet.Events[0].ID },
		"session":             func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Evidence[0].SessionID = "other-session" },
		"line":                func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Evidence[0].JSONLLine = 2 },
		"hash":                func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Evidence[0].SourceHash = hashB },
		"summary":             func(p *Proposal, _ *evidence.Packet) { p.NewDecisions[0].Evidence[0].Summary = "changed" },
		"duplicate evidence ref": func(p *Proposal, _ *evidence.Packet) {
			p.NewDecisions[0].Evidence = append(p.NewDecisions[0].Evidence, p.NewDecisions[0].Evidence[0])
		},
		"duplicate evidence link": func(p *Proposal, _ *evidence.Packet) {
			p.EvidenceLinks = append(p.EvidenceLinks, p.EvidenceLinks[0])
		},
		"unlinked evidence":     func(p *Proposal, _ *evidence.Packet) { p.EvidenceLinks = p.EvidenceLinks[1:] },
		"link for other entity": func(p *Proposal, _ *evidence.Packet) { p.EvidenceLinks[0].EntityID = "missing" },
		"unknown relation":      func(p *Proposal, _ *evidence.Packet) { p.EvidenceLinks[0].Relation = "maybe" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := cloneProposal(t, base)
			gotPacket := packet
			mutate(&p, &gotPacket)
			digest, err := evidence.Digest(gotPacket)
			if err != nil {
				t.Fatal(err)
			}
			p.EvidencePacketSHA256 = digest
			if _, err := Validate(p, gotPacket, state); err == nil {
				t.Fatal("accepted invalid evidence binding")
			}
		})
	}
}

func TestValidateBindsTailEventHashToNextCursor(t *testing.T) {
	p, packet, state := fixedProposalPacketState(t, "valid-first.json")
	packet.Events[1].SourceHash = hashA
	for i := range p.TimelineEvents[0].Evidence {
		p.TimelineEvents[0].Evidence[i].SourceHash = hashA
	}
	for i := range *p.CurrentStatePatch.Evidence {
		(*p.CurrentStatePatch.Evidence)[i].SourceHash = hashA
	}
	for i := range p.SessionReport.Evidence {
		p.SessionReport.Evidence[i].SourceHash = hashA
	}
	for phaseIndex := range p.SessionReport.Phases {
		for evidenceIndex := range p.SessionReport.Phases[phaseIndex].Evidence {
			p.SessionReport.Phases[phaseIndex].Evidence[evidenceIndex].SourceHash = hashA
		}
	}
	digest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	p.EvidencePacketSHA256 = digest

	changes, err := Validate(p, packet, state)
	if err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
		t.Fatalf("tail event hash was not bound to next cursor: changes=%+v err=%v", changes, err)
	}
}

func TestValidateAllowsShapeValidatedBalancedSHA256(t *testing.T) {
	p, packet, state := fixedProposalPacketState(t, "valid-first.json")
	balancedHash := strings.Repeat("0123456789abcdef", 4)
	packet.NextCursor.SourceHash = balancedHash
	packet.Events[1].SourceHash = balancedHash
	for i := range p.TimelineEvents[0].Evidence {
		p.TimelineEvents[0].Evidence[i].SourceHash = balancedHash
	}
	for i := range *p.CurrentStatePatch.Evidence {
		(*p.CurrentStatePatch.Evidence)[i].SourceHash = balancedHash
	}
	for i := range p.SessionReport.Evidence {
		p.SessionReport.Evidence[i].SourceHash = balancedHash
	}
	for phaseIndex := range p.SessionReport.Phases {
		for evidenceIndex := range p.SessionReport.Phases[phaseIndex].Evidence {
			p.SessionReport.Phases[phaseIndex].Evidence[evidenceIndex].SourceHash = balancedHash
		}
	}
	digest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	p.EvidencePacketSHA256 = digest

	if _, err := Validate(p, packet, state); err != nil {
		t.Fatalf("shape-validated balanced SHA-256 rejected as text: %v", err)
	}
}

func TestValidateRejectsUnsafeStableIDsAtomically(t *testing.T) {
	base, packet, state := fixedProposalPacketState(t, "valid-first.json")
	bearerID := "Authorization: Bearer abcdefghijklmnop"
	unsafeByShape := map[string]string{
		"bearer secret":  bearerID,
		"uppercase":      "Decision-1",
		"path traversal": "decision/../one",
		"too long":       "a" + strings.Repeat("b", 128),
		"unicode":        "décision-1",
		"control":        "decision-\n1",
		"colon":          "decision:1",
		"leading dash":   "-decision-1",
	}
	for name, unsafeID := range unsafeByShape {
		t.Run("decision id "+name, func(t *testing.T) {
			p := cloneProposal(t, base)
			p.NewDecisions[0].ID = unsafeID
			changes, err := Validate(p, packet, state)
			if err == nil || !strings.Contains(err.Error(), "invalid stable id") || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
				t.Fatalf("unsafe decision id accepted atomically: changes=%+v err=%v", changes, err)
			}
		})
	}

	tests := map[string]func(*Proposal){
		"decision patch id": func(p *Proposal) {
			p.UpdatedDecisions = []DecisionPatch{{ID: bearerID, ExpectedRevision: 1, Title: strptr("changed")}}
		},
		"decision supersedes id": func(p *Proposal) { p.NewDecisions[0].Supersedes = []string{bearerID} },
		"open-loop entity id": func(p *Proposal) {
			p.OpenLoops = []OpenLoopChange{{Operation: "create", Entity: &ledger.OpenLoop{
				ID: bearerID, ProjectID: projectID, Title: "Loop", Status: "open", Revision: 1,
				Tags: []string{}, SourceSessions: []string{sessionID}, Evidence: []ledger.EvidenceRef{}, Attempts: []string{},
			}}}
		},
		"open-loop patch id": func(p *Proposal) {
			p.OpenLoops = []OpenLoopChange{{Operation: "update", Patch: &OpenLoopPatch{ID: bearerID, ExpectedRevision: 1, Title: strptr("changed")}}}
		},
		"timeline id":               func(p *Proposal) { p.TimelineEvents[0].ID = bearerID },
		"timeline decision ref":     func(p *Proposal) { p.TimelineEvents[0].DecisionIDs = []string{bearerID} },
		"timeline open-loop ref":    func(p *Proposal) { p.TimelineEvents[0].OpenLoopIDs = []string{bearerID} },
		"session report id":         func(p *Proposal) { p.SessionReport.ID = bearerID },
		"decisions-added effect":    func(p *Proposal) { p.SessionReport.DecisionsAdded = []string{bearerID} },
		"decisions-revised effect":  func(p *Proposal) { p.SessionReport.DecisionsRevised = []string{bearerID} },
		"open-loops-created effect": func(p *Proposal) { p.SessionReport.OpenLoopsCreated = []string{bearerID} },
		"open-loops-closed effect":  func(p *Proposal) { p.SessionReport.OpenLoopsClosed = []string{bearerID} },
		"evidence reference id":     func(p *Proposal) { p.NewDecisions[0].Evidence[0].EvidenceID = bearerID },
		"evidence-link entity id":   func(p *Proposal) { p.EvidenceLinks[0].EntityID = bearerID },
		"evidence-link evidence id": func(p *Proposal) { p.EvidenceLinks[0].EvidenceID = bearerID },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := cloneProposal(t, base)
			mutate(&p)
			changes, err := Validate(p, packet, state)
			if err == nil || !strings.Contains(err.Error(), "invalid stable id") || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
				t.Fatalf("unsafe stable id accepted atomically: changes=%+v err=%v", changes, err)
			}
		})
	}
}

func TestValidateStableIDLengthBoundaryAndExactReferences(t *testing.T) {
	base, packet, state := fixedProposalPacketState(t, "valid-first.json")
	rewriteDecisionID := func(p *Proposal, id string) {
		oldID := p.NewDecisions[0].ID
		p.NewDecisions[0].ID = id
		p.TimelineEvents[0].DecisionIDs = []string{id}
		p.SessionReport.DecisionsAdded = []string{id}
		for i := range p.EvidenceLinks {
			if p.EvidenceLinks[i].EntityID == oldID {
				p.EvidenceLinks[i].EntityID = id
			}
		}
	}

	t.Run("128 bytes accepted", func(t *testing.T) {
		p := cloneProposal(t, base)
		rewriteDecisionID(&p, "a"+strings.Repeat("b", 127))
		if _, err := Validate(p, packet, state); err != nil {
			t.Fatalf("maximum-length stable id rejected: %v", err)
		}
	})

	t.Run("129 bytes rejected", func(t *testing.T) {
		p := cloneProposal(t, base)
		rewriteDecisionID(&p, "a"+strings.Repeat("b", 128))
		if changes, err := Validate(p, packet, state); err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
			t.Fatalf("overlength stable id accepted: changes=%+v err=%v", changes, err)
		}
	})

	t.Run("cross-reference mismatch rejected without normalization", func(t *testing.T) {
		p := cloneProposal(t, base)
		p.NewDecisions[0].ID = "decision-two"
		if changes, err := Validate(p, packet, state); err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
			t.Fatalf("cross-reference mismatch accepted: changes=%+v err=%v", changes, err)
		}
	})
}

func TestValidateKeepsExternalProjectAndSessionIDContractsSeparate(t *testing.T) {
	p, packet, state := fixedProposalPacketState(t, "valid-first.json")
	externalProjectID := "Project:External/One"
	externalSessionID := "Session:External/One"
	p.ProjectID = externalProjectID
	p.SessionID = externalSessionID
	p.NewDecisions[0].ProjectID = externalProjectID
	p.NewDecisions[0].SourceSessions = []string{externalSessionID}
	p.SessionReport.ProjectID = externalProjectID
	p.SessionReport.SessionID = externalSessionID
	*p.CurrentStatePatch.SourceSessions = []string{externalSessionID}
	for _, refs := range [][]ledger.EvidenceRef{
		p.NewDecisions[0].Evidence,
		p.TimelineEvents[0].Evidence,
		*p.CurrentStatePatch.Evidence,
		p.SessionReport.Phases[0].Evidence,
		p.SessionReport.Evidence,
	} {
		for i := range refs {
			refs[i].SessionID = externalSessionID
		}
	}
	packet.ProjectID = externalProjectID
	packet.SessionID = externalSessionID
	state.ProjectID = externalProjectID
	state.CurrentState.ProjectID = externalProjectID
	digest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	p.EvidencePacketSHA256 = digest

	if _, err := Validate(p, packet, state); err != nil {
		t.Fatalf("external project/session id contract was forced into stable-id grammar: %v", err)
	}
}

func TestValidateRejectsUnsafeIDsInExistingState(t *testing.T) {
	base, packet, _ := fixedProposalPacketState(t, "valid-first.json")
	unsafeID := "Authorization: Bearer abcdefghijklmnop"
	tests := map[string]func(*ledger.State, Proposal){
		"decision": func(state *ledger.State, p Proposal) {
			item := p.NewDecisions[0]
			item.ID = unsafeID
			state.Decisions[unsafeID] = item
		},
		"open loop": func(state *ledger.State, _ Proposal) {
			state.OpenLoops[unsafeID] = ledger.OpenLoop{ID: unsafeID, ProjectID: projectID, Status: "open", Revision: 1}
		},
		"timeline": func(state *ledger.State, _ Proposal) {
			state.Timeline = []ledger.TimelineEvent{{ID: unsafeID, Revision: 1, Class: ledger.Verified}}
		},
		"session report": func(state *ledger.State, p Proposal) {
			item := p.SessionReport
			item.ID = unsafeID
			state.Sessions[unsafeID] = item
		},
		"decision supersedes reference": func(state *ledger.State, p Proposal) {
			item := p.NewDecisions[0]
			item.ID = "decision-existing"
			item.Supersedes = []string{unsafeID}
			state.Decisions[item.ID] = item
		},
		"timeline decision reference": func(state *ledger.State, _ Proposal) {
			state.Timeline = []ledger.TimelineEvent{{
				ID: "timeline-existing", Revision: 1, Class: ledger.Verified,
				DecisionIDs: []string{unsafeID}, OpenLoopIDs: []string{},
			}}
		},
		"session effect reference": func(state *ledger.State, p Proposal) {
			item := p.SessionReport
			item.ID = "session-report-existing"
			item.SessionID = "historic-session"
			item.DecisionsAdded = []string{unsafeID}
			state.Sessions[item.ID] = item
		},
		"evidence reference": func(state *ledger.State, p Proposal) {
			item := p.NewDecisions[0]
			item.ID = "decision-existing"
			item.Evidence[0].EvidenceID = unsafeID
			state.Decisions[item.ID] = item
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := cloneProposal(t, base)
			stateSource := cloneProposal(t, base)
			state := fixedState()
			mutate(&state, stateSource)
			changes, err := Validate(p, packet, state)
			if err == nil || !strings.Contains(err.Error(), "invalid stable id") || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
				t.Fatalf("unsafe existing %s id accepted: changes=%+v err=%v", name, changes, err)
			}
		})
	}
}

func TestValidateRequiresEqualBoundariesForEmptyConsumedRange(t *testing.T) {
	p, packet, state := fixedProposalPacketState(t, "valid-first.json")
	packet.FromCursor = 3
	packet.ToCursor = 2
	packet.ExpectedCursor = evidence.CursorBoundary{Line: 2, SourceHash: hashB}
	packet.NextCursor = evidence.CursorBoundary{Line: 2, SourceHash: hashA}
	packet.Events = []evidence.Item{}
	p.FromCursor = 3
	p.ToCursor = 2
	digest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	p.EvidencePacketSHA256 = digest

	_, err = Validate(p, packet, state)
	if err == nil || !strings.Contains(err.Error(), "empty packet cursor boundaries") {
		t.Fatalf("empty consumed range boundary mismatch not detected first: %v", err)
	}
}

func TestValidateRejectsNonJSSafePacketNumbers(t *testing.T) {
	p, packet, state := fixedProposalPacketState(t, "valid-first.json")
	unsafeLine := jsMaxInt + 1
	packet.ToCursor = unsafeLine
	packet.NextCursor.Line = unsafeLine
	packet.Events[1].JSONLLine = unsafeLine
	p.ToCursor = unsafeLine
	for i := range p.TimelineEvents[0].Evidence {
		p.TimelineEvents[0].Evidence[i].JSONLLine = unsafeLine
	}
	for i := range *p.CurrentStatePatch.Evidence {
		(*p.CurrentStatePatch.Evidence)[i].JSONLLine = unsafeLine
	}
	for i := range p.SessionReport.Evidence {
		p.SessionReport.Evidence[i].JSONLLine = unsafeLine
	}
	for phaseIndex := range p.SessionReport.Phases {
		for evidenceIndex := range p.SessionReport.Phases[phaseIndex].Evidence {
			p.SessionReport.Phases[phaseIndex].Evidence[evidenceIndex].JSONLLine = unsafeLine
		}
	}
	digest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	p.EvidencePacketSHA256 = digest

	changes, err := Validate(p, packet, state)
	if err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
		t.Fatalf("non-JS-safe packet integer accepted: changes=%+v err=%v", changes, err)
	}
}

func TestValidateDecisionExistenceRevisionAndTransitions(t *testing.T) {
	base, packet, baseState := fixedProposalPacketState(t, "valid-first.json")
	old := base.NewDecisions[0]
	old.Status = "proposed"
	old.Revision = 1

	validUpdate := func() (Proposal, ledger.State) {
		p := cloneProposal(t, base)
		p.NewDecisions = []ledger.Decision{}
		p.UpdatedDecisions = []DecisionPatch{{
			ID:               old.ID,
			ExpectedRevision: 1,
			Status:           strptr("accepted"),
			Evidence:         evidenceptr([]ledger.EvidenceRef{base.NewDecisions[0].Evidence[0]}),
		}}
		p.SessionReport.DecisionsAdded = []string{}
		p.SessionReport.DecisionsRevised = []string{old.ID}
		state := baseState
		state.Decisions[old.ID] = old
		return p, state
	}

	p, state := validUpdate()
	if _, err := Validate(p, packet, state); err != nil {
		t.Fatalf("valid proposed -> accepted transition rejected: %v", err)
	}

	tests := map[string]func(*Proposal, *ledger.State){
		"create existing": func(p *Proposal, state *ledger.State) {
			p.NewDecisions = []ledger.Decision{base.NewDecisions[0]}
		},
		"update missing": func(_ *Proposal, state *ledger.State) { delete(state.Decisions, old.ID) },
		"wrong revision": func(p *Proposal, _ *ledger.State) { p.UpdatedDecisions[0].ExpectedRevision = 2 },
		"revision overflow": func(p *Proposal, state *ledger.State) {
			item := state.Decisions[old.ID]
			item.Revision = jsMaxInt
			state.Decisions[old.ID] = item
			p.UpdatedDecisions[0].ExpectedRevision = jsMaxInt
		},
		"invalid transition": func(p *Proposal, state *ledger.State) {
			item := state.Decisions[old.ID]
			item.Status = "accepted"
			state.Decisions[old.ID] = item
			p.UpdatedDecisions[0].Status = strptr("proposed")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p, state := validUpdate()
			mutate(&p, &state)
			if changes, err := Validate(p, packet, state); err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
				t.Fatalf("changes=%+v err=%v", changes, err)
			}
		})
	}
}

func TestValidateOpenLoopTransitionsAndOperationShape(t *testing.T) {
	base, packet, state := fixedProposalPacketState(t, "valid-first.json")
	ref := base.NewDecisions[0].Evidence[0]
	loop := ledger.OpenLoop{
		ID: "loop-1", ProjectID: projectID, Title: "Resolve renderer", Status: "open", Revision: 1,
		Tags: []string{}, SourceSessions: []string{sessionID}, Evidence: []ledger.EvidenceRef{ref}, Question: "How?", Attempts: []string{},
	}
	create := cloneProposal(t, base)
	create.OpenLoops = []OpenLoopChange{{Operation: "create", Entity: &loop}}
	create.SessionReport.OpenLoopsCreated = []string{loop.ID}
	create.EvidenceLinks = append(create.EvidenceLinks, EvidenceLink{EntityID: loop.ID, EvidenceID: ref.EvidenceID, Relation: "supports"})
	if _, err := Validate(create, packet, state); err != nil {
		t.Fatalf("valid open-loop create rejected: %v", err)
	}

	blocked := "blocked"
	update := cloneProposal(t, base)
	update.OpenLoops = []OpenLoopChange{{Operation: "update", Patch: &OpenLoopPatch{ID: loop.ID, ExpectedRevision: 1, Status: &blocked, Evidence: evidenceptr([]ledger.EvidenceRef{ref})}}}
	update.EvidenceLinks = append(update.EvidenceLinks, EvidenceLink{EntityID: loop.ID, EvidenceID: ref.EvidenceID, Relation: "supports"})
	state.OpenLoops[loop.ID] = loop
	if _, err := Validate(update, packet, state); err != nil {
		t.Fatalf("valid open -> blocked transition rejected: %v", err)
	}

	tests := map[string]func(*Proposal){
		"both entity and patch": func(p *Proposal) { p.OpenLoops[0].Entity = &loop },
		"unknown operation":     func(p *Proposal) { p.OpenLoops[0].Operation = "merge" },
		"invalid transition":    func(p *Proposal) { p.OpenLoops[0].Patch.Status = strptr("archived") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := cloneProposal(t, update)
			mutate(&p)
			if _, err := Validate(p, packet, state); err == nil {
				t.Fatal("accepted invalid open-loop change")
			}
		})
	}
}

func TestValidateSessionReportEffectsAreExactAndSorted(t *testing.T) {
	base, packet, _ := fixedProposalPacketState(t, "valid-first.json")
	ref := base.NewDecisions[0].Evidence[0]

	tests := map[string]func(*Proposal, *ledger.State){
		"omitted decision addition": func(p *Proposal, _ *ledger.State) {
			p.SessionReport.DecisionsAdded = []string{}
		},
		"unchanged decision called revised": func(p *Proposal, state *ledger.State) {
			item := base.NewDecisions[0]
			item.ID = "decision-existing"
			state.Decisions[item.ID] = item
			p.SessionReport.DecisionsRevised = []string{item.ID}
		},
		"unchanged loop called created": func(p *Proposal, state *ledger.State) {
			item := ledger.OpenLoop{ID: "loop-existing", ProjectID: projectID, Title: "Existing", Status: "open", Revision: 1}
			state.OpenLoops[item.ID] = item
			p.SessionReport.OpenLoopsCreated = []string{item.ID}
		},
		"unchanged loop called closed": func(p *Proposal, state *ledger.State) {
			item := ledger.OpenLoop{ID: "loop-existing", ProjectID: projectID, Title: "Existing", Status: "open", Revision: 1}
			state.OpenLoops[item.ID] = item
			p.SessionReport.OpenLoopsClosed = []string{item.ID}
		},
		"unsorted additions": func(p *Proposal, _ *ledger.State) {
			second := p.NewDecisions[0]
			second.ID = "decision-0"
			p.NewDecisions = append(p.NewDecisions, second)
			p.EvidenceLinks = append(p.EvidenceLinks, EvidenceLink{EntityID: second.ID, EvidenceID: ref.EvidenceID, Relation: "supports"})
			p.SessionReport.DecisionsAdded = []string{"decision-1", "decision-0"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := cloneProposal(t, base)
			state := fixedState()
			mutate(&p, &state)
			changes, err := Validate(p, packet, state)
			if err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
				t.Fatalf("false session effects accepted: changes=%+v err=%v", changes, err)
			}
		})
	}
}

func TestValidateRejectsRevisionOnlyDecisionAndLoopChanges(t *testing.T) {
	base, packet, _ := fixedProposalPacketState(t, "valid-first.json")
	ref := base.NewDecisions[0].Evidence[0]

	t.Run("decision", func(t *testing.T) {
		state := fixedState()
		existing := base.NewDecisions[0]
		state.Decisions[existing.ID] = existing
		p := cloneProposal(t, base)
		p.NewDecisions = []ledger.Decision{}
		p.UpdatedDecisions = []DecisionPatch{{ID: existing.ID, ExpectedRevision: 1, Evidence: evidenceptr([]ledger.EvidenceRef{ref})}}
		p.SessionReport.DecisionsAdded = []string{}
		p.SessionReport.DecisionsRevised = []string{existing.ID}

		changes, err := Validate(p, packet, state)
		if err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
			t.Fatalf("revision-only decision accepted: changes=%+v err=%v", changes, err)
		}
	})

	t.Run("open loop", func(t *testing.T) {
		state := fixedState()
		existing := ledger.OpenLoop{
			ID: "loop-1", ProjectID: projectID, Title: "Existing", Status: "open", Revision: 1,
			Tags: []string{}, SourceSessions: []string{sessionID}, Evidence: []ledger.EvidenceRef{ref}, Attempts: []string{},
		}
		state.OpenLoops[existing.ID] = existing
		p := cloneProposal(t, base)
		p.OpenLoops = []OpenLoopChange{{Operation: "update", Patch: &OpenLoopPatch{
			ID: existing.ID, ExpectedRevision: 1, Evidence: evidenceptr([]ledger.EvidenceRef{ref}),
		}}}
		p.EvidenceLinks = append(p.EvidenceLinks, EvidenceLink{EntityID: existing.ID, EvidenceID: ref.EvidenceID, Relation: "supports"})

		changes, err := Validate(p, packet, state)
		if err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
			t.Fatalf("revision-only open loop accepted: changes=%+v err=%v", changes, err)
		}
	})
}

func TestValidateOpenLoopCloseEffectsUseOnlyActiveToTerminalTransitions(t *testing.T) {
	base, packet, _ := fixedProposalPacketState(t, "valid-first.json")
	ref := base.NewDecisions[0].Evidence[0]

	makeUpdate := func(oldStatus, nextStatus string, closed []string) (Proposal, ledger.State) {
		state := fixedState()
		loop := ledger.OpenLoop{
			ID: "loop-1", ProjectID: projectID, Title: "Resolve renderer", Status: oldStatus, Revision: 1,
			Tags: []string{}, SourceSessions: []string{sessionID}, Evidence: []ledger.EvidenceRef{ref}, Question: "How?", Attempts: []string{},
		}
		state.OpenLoops[loop.ID] = loop
		p := cloneProposal(t, base)
		p.OpenLoops = []OpenLoopChange{{Operation: "update", Patch: &OpenLoopPatch{
			ID: loop.ID, ExpectedRevision: 1, Status: strptr(nextStatus), Evidence: evidenceptr([]ledger.EvidenceRef{ref}),
		}}}
		p.EvidenceLinks = append(p.EvidenceLinks, EvidenceLink{EntityID: loop.ID, EvidenceID: ref.EvidenceID, Relation: "supports"})
		p.SessionReport.OpenLoopsClosed = closed
		return p, state
	}

	for _, transition := range []struct {
		name, oldStatus, nextStatus string
		closed                      []string
	}{
		{name: "open resolved", oldStatus: "open", nextStatus: "resolved", closed: []string{"loop-1"}},
		{name: "blocked abandoned", oldStatus: "blocked", nextStatus: "abandoned", closed: []string{"loop-1"}},
		{name: "open blocked", oldStatus: "open", nextStatus: "blocked", closed: []string{}},
		{name: "resolved archived", oldStatus: "resolved", nextStatus: "archived", closed: []string{}},
	} {
		t.Run(transition.name, func(t *testing.T) {
			p, state := makeUpdate(transition.oldStatus, transition.nextStatus, transition.closed)
			if _, err := Validate(p, packet, state); err != nil {
				t.Fatalf("valid close accounting rejected: %v", err)
			}
		})
	}

	t.Run("omitted close", func(t *testing.T) {
		p, state := makeUpdate("open", "resolved", []string{})
		if changes, err := Validate(p, packet, state); err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
			t.Fatalf("omitted close accepted: changes=%+v err=%v", changes, err)
		}
	})
	t.Run("archival called close", func(t *testing.T) {
		p, state := makeUpdate("resolved", "archived", []string{"loop-1"})
		if changes, err := Validate(p, packet, state); err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
			t.Fatalf("archival misreported as close: changes=%+v err=%v", changes, err)
		}
	})
}

func TestValidateSupersedesTargetsCyclesAndScopedIDs(t *testing.T) {
	base, packet, state := fixedProposalPacketState(t, "valid-first.json")
	old := base.NewDecisions[0]
	old.ID = "decision-old"
	old.Status = "accepted"
	state.Decisions[old.ID] = old

	valid := cloneProposal(t, base)
	valid.NewDecisions[0].Supersedes = []string{old.ID}
	if _, err := Validate(valid, packet, state); err != nil {
		t.Fatalf("valid predecessor rejected: %v", err)
	}

	tests := map[string]func(*Proposal, *ledger.State){
		"missing predecessor": func(p *Proposal, _ *ledger.State) { p.NewDecisions[0].Supersedes = []string{"missing"} },
		"self predecessor":    func(p *Proposal, _ *ledger.State) { p.NewDecisions[0].Supersedes = []string{p.NewDecisions[0].ID} },
		"cycle": func(p *Proposal, state *ledger.State) {
			item := state.Decisions[old.ID]
			item.Supersedes = []string{p.NewDecisions[0].ID}
			state.Decisions[old.ID] = item
		},
		"cross-project state": func(_ *Proposal, state *ledger.State) {
			item := state.Decisions[old.ID]
			item.ProjectID = "other-project"
			state.Decisions[old.ID] = item
		},
		"cross-kind duplicate id": func(p *Proposal, _ *ledger.State) {
			ref := p.NewDecisions[0].Evidence[0]
			loop := ledger.OpenLoop{ID: p.NewDecisions[0].ID, ProjectID: projectID, Title: "Duplicate", Status: "open", Revision: 1, Tags: []string{}, SourceSessions: []string{sessionID}, Evidence: []ledger.EvidenceRef{ref}}
			p.OpenLoops = []OpenLoopChange{{Operation: "create", Entity: &loop}}
			p.EvidenceLinks = append(p.EvidenceLinks, EvidenceLink{EntityID: loop.ID, EvidenceID: ref.EvidenceID, Relation: "supports"})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := cloneProposal(t, valid)
			gotState := fixedState()
			gotState.Decisions[old.ID] = old
			mutate(&p, &gotState)
			if _, err := Validate(p, packet, gotState); err == nil {
				t.Fatal("accepted invalid scoped identity or supersedes graph")
			}
		})
	}
}

func TestValidateTimeStatusClassAndInferenceUpgrade(t *testing.T) {
	base, packet, state := fixedProposalPacketState(t, "valid-first.json")
	tests := map[string]func(*Proposal){
		"bad time":            func(p *Proposal) { p.TimelineEvents[0].OccurredAt = "tomorrow" },
		"bad state time":      func(p *Proposal) { p.CurrentStatePatch.LastUpdated = strptr("soon") },
		"bad decision status": func(p *Proposal) { p.NewDecisions[0].Status = "done" },
		"bad class":           func(p *Proposal) { p.TimelineEvents[0].Class = "certain" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := cloneProposal(t, base)
			mutate(&p)
			if _, err := Validate(p, packet, state); err == nil {
				t.Fatal("accepted invalid enum or time")
			}
		})
	}

	old := base.TimelineEvents[0]
	old.Class = ledger.Inference
	old.Revision = 1
	state.Timeline = []ledger.TimelineEvent{old}
	upgrade := cloneProposal(t, base)
	upgrade.TimelineEvents[0].Revision = 2
	if _, err := Validate(upgrade, packet, state); err != nil {
		t.Fatalf("verified upgrade with verification evidence rejected: %v", err)
	}
	upgrade.EvidenceLinks[1].Relation = "supports"
	if _, err := Validate(upgrade, packet, state); err == nil {
		t.Fatal("inference upgraded without verification evidence")
	}
}

func TestValidateSortsOnlyAfterFullValidation(t *testing.T) {
	base, packet, state := fixedProposalPacketState(t, "valid-first.json")
	p := cloneProposal(t, base)
	second := p.NewDecisions[0]
	second.ID = "decision-0"
	second.Title = "Earlier by ID"
	p.NewDecisions = append(p.NewDecisions, second)
	p.SessionReport.DecisionsAdded = []string{"decision-0", "decision-1"}
	p.EvidenceLinks = append(p.EvidenceLinks, EvidenceLink{EntityID: second.ID, EvidenceID: second.Evidence[0].EvidenceID, Relation: "supports"})
	changes, err := Validate(p, packet, state)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{changes.Decisions[0].ID, changes.Decisions[1].ID}; !reflect.DeepEqual(got, []string{"decision-0", "decision-1"}) {
		t.Fatalf("decisions not sorted: %v", got)
	}

	p.TimelineEvents = append(p.TimelineEvents, ledger.TimelineEvent{ID: "timeline-invalid", OccurredAt: "never", Revision: 1, Class: ledger.Verified})
	if changes, err := Validate(p, packet, state); err == nil || !reflect.DeepEqual(changes, ledger.ChangeSet{}) {
		t.Fatalf("partial failure leaked changes: changes=%+v err=%v", changes, err)
	}
}

func TestSchemaDeclaresClosedProtocolAndRequiredFields(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "schemas", "proposal-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["additionalProperties"] != false {
		t.Fatalf("schema is not a closed draft-2020-12 object: %s", b)
	}
	required, _ := schema["required"].([]any)
	for _, field := range []string{"schema_version", "project_id", "session_id", "from_cursor", "to_cursor", "evidence_packet_sha256", "new_decisions", "updated_decisions", "open_loops", "timeline_events", "current_state_patch", "session_report", "evidence_links"} {
		if !containsJSONText(required, field) {
			t.Fatalf("schema does not require %s", field)
		}
	}
	for _, token := range []string{`"enum"`, `"additionalProperties": false`, `"oneOf"`} {
		if !strings.Contains(string(b), token) {
			t.Fatalf("schema missing %s", token)
		}
	}
	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema definitions missing")
	}
	for _, name := range []string{"nonnegative_integer", "positive_integer"} {
		definition, ok := defs[name].(map[string]any)
		if !ok || definition["maximum"] != float64(jsMaxInt) {
			t.Fatalf("%s maximum is not JS-safe: %+v", name, definition)
		}
	}
	stableID, ok := defs["stable_id"].(map[string]any)
	if !ok || stableID["pattern"] != stableIDPattern || stableID["maxLength"] != float64(128) {
		t.Fatalf("schema stable-id grammar differs from validator contract: %+v", stableID)
	}
	stableIDArray, ok := defs["stable_id_array"].(map[string]any)
	if !ok || schemaRef(t, stableIDArray, "items") != "#/$defs/stable_id" {
		t.Fatalf("schema stable-id array does not share the stable-id grammar: %+v", stableIDArray)
	}
	for path, want := range map[string]string{
		"decision.id":                       "#/$defs/stable_id",
		"decision.supersedes":               "#/$defs/stable_id_array",
		"decision_patch.id":                 "#/$defs/stable_id",
		"decision_patch.supersedes":         "#/$defs/stable_id_array",
		"open_loop.id":                      "#/$defs/stable_id",
		"open_loop_patch.id":                "#/$defs/stable_id",
		"timeline_event.id":                 "#/$defs/stable_id",
		"timeline_event.decision_ids":       "#/$defs/stable_id_array",
		"timeline_event.open_loop_ids":      "#/$defs/stable_id_array",
		"session_report.id":                 "#/$defs/stable_id",
		"session_report.decisions_added":    "#/$defs/stable_id_array",
		"session_report.decisions_revised":  "#/$defs/stable_id_array",
		"session_report.open_loops_created": "#/$defs/stable_id_array",
		"session_report.open_loops_closed":  "#/$defs/stable_id_array",
		"evidence_ref.evidence_id":          "#/$defs/stable_id",
		"evidence_link.entity_id":           "#/$defs/stable_id",
		"evidence_link.evidence_id":         "#/$defs/stable_id",
	} {
		parts := strings.Split(path, ".")
		definition, ok := defs[parts[0]].(map[string]any)
		if !ok {
			t.Fatalf("schema definition %s missing", parts[0])
		}
		properties, ok := definition["properties"].(map[string]any)
		if !ok || schemaRef(t, properties, parts[1]) != want {
			t.Fatalf("schema field %s does not use %s", path, want)
		}
	}
	if schemaRef(t, schema["properties"].(map[string]any), "project_id") != "#/$defs/nonempty_string" || schemaRef(t, schema["properties"].(map[string]any), "session_id") != "#/$defs/nonempty_string" {
		t.Fatal("proposal project/session protocol ids were incorrectly forced into the model stable-id grammar")
	}
}

func TestEntitySchemaSharesStableIDGrammar(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "schemas", "entity-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	id := properties["id"].(map[string]any)
	if id["pattern"] != stableIDPattern || id["maxLength"] != float64(128) {
		t.Fatalf("entity schema id grammar differs from proposal validator: %+v", id)
	}
	if _, constrained := properties["project_id"].(map[string]any)["pattern"]; constrained {
		t.Fatal("external project id was incorrectly forced into the model stable-id grammar")
	}
}

func schemaRef(t *testing.T, object map[string]any, field string) string {
	t.Helper()
	value, ok := object[field].(map[string]any)
	if !ok {
		return ""
	}
	ref, _ := value["$ref"].(string)
	return ref
}

func containsJSONText(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
