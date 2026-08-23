package proposal

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

const (
	projectID = "project-1111111111111111"
	sessionID = "session-1"
	hashA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
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
		"revision overflow": func(_ *Proposal, state *ledger.State) {
			item := state.Decisions[old.ID]
			item.Revision = math.MaxInt
			state.Decisions[old.ID] = item
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
	p.SessionReport.DecisionsAdded = []string{"decision-1", "decision-0"}
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
}

func containsJSONText(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
