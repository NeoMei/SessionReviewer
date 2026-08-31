package reviewv2

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/ledger"
)

func TestMachineLedgerRoundTripIsDeterministicAndRejectsDuplicateIdentity(t *testing.T) {
	valid := mustFixture(t, "../../testdata/review-v2/ledger.valid.json")
	first, err := ParseMachineLedger(valid)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderMachineLedger(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseMachineLedger(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("round trip changed state")
	}
	renderedAgain, err := RenderMachineLedger(second)
	if err != nil || !bytes.Equal(rendered, renderedAgain) {
		t.Fatalf("non-deterministic render: err=%v", err)
	}
	if _, err := ParseMachineLedger(mustFixture(t, "../../testdata/review-v2/ledger.invalid-duplicate-id.json")); err == nil || !strings.Contains(err.Error(), "duplicate evidence identity") {
		t.Fatalf("duplicate evidence identity accepted or misclassified: %v", err)
	}
}

func TestMachineLedgerRenderUsesStableOrderAndJSONFormatting(t *testing.T) {
	machine, err := ParseMachineLedger(mustFixture(t, "../../testdata/review-v2/ledger.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	machine.Sessions[0], machine.Sessions[1] = machine.Sessions[1], machine.Sessions[0]
	machine.Evidence = map[string][]ledger.EvidenceRef{
		"session-report-z": machine.Evidence["session-report-z"],
		"decision-a":       machine.Evidence["decision-a"],
	}

	rendered, err := RenderMachineLedger(machine)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(rendered, []byte("\n")) || bytes.Contains(rendered, []byte(`\u003c`)) {
		t.Fatalf("render is not newline-terminated, unescaped JSON: %q", rendered)
	}
	if bytes.Index(rendered, []byte(`"session_id": "session-a"`)) > bytes.Index(rendered, []byte(`"session_id": "session-z"`)) {
		t.Fatalf("sessions are not sorted by session_id: %s", rendered)
	}
	evidenceStart := bytes.LastIndex(rendered, []byte(`"evidence": [`))
	if evidenceStart < 0 || bytes.Index(rendered[evidenceStart:], []byte(`"id": "decision-a"`)) > bytes.Index(rendered[evidenceStart:], []byte(`"id": "session-report-z"`)) {
		t.Fatalf("evidence entries are not sorted by id: %s", rendered)
	}
}

func TestMachineLedgerRenderNormalizesEmptyArraysWithoutMutatingInput(t *testing.T) {
	value := MachineLedger{
		SchemaVersion: SchemaVersion,
		ProjectID:     "project-empty",
		ReviewSHA256:  strings.Repeat("a", 64),
		HistorySHA256: strings.Repeat("b", 64),
	}
	rendered, err := RenderMachineLedger(value)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rendered, []byte(": null")) {
		t.Fatalf("schema array rendered as null: %s", rendered)
	}
	if value.Sessions != nil || value.Evidence != nil || value.Accounting.Models != nil {
		t.Fatalf("render mutated caller value: %+v", value)
	}
	if _, err := ParseMachineLedger(rendered); err != nil {
		t.Fatalf("normalized render did not parse: %v", err)
	}
}

func TestMachineLedgerRenderNormalizesNilEvidenceRefsWithoutMutatingCaller(t *testing.T) {
	value, err := ParseMachineLedger(mustFixture(t, "../../testdata/review-v2/ledger.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	value.Evidence["session-report-a"] = nil

	rendered, err := RenderMachineLedger(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMachineLedger(rendered); err != nil {
		t.Fatalf("rendered ledger cannot be parsed: %v\n%s", err, rendered)
	}
	if !bytes.Contains(rendered, []byte(`"refs": []`)) || bytes.Contains(rendered, []byte(`"refs": null`)) {
		t.Fatalf("nil evidence refs were not normalized to an array: %s", rendered)
	}
	if value.Evidence["session-report-a"] != nil {
		t.Fatalf("render mutated caller evidence refs: %+v", value.Evidence["session-report-a"])
	}
}

func TestValidateRejectsInvalidStateContracts(t *testing.T) {
	valid := validState(t)
	if err := Validate(valid); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*State)
	}{
		{"duplicate risk ID", func(state *State) { state.Review.Risks = append(state.Review.Risks, state.Review.Risks[0]) }},
		{"duplicate decision ID", func(state *State) { state.Review.Decisions = append(state.Review.Decisions, state.Review.Decisions[0]) }},
		{"duplicate event ID", func(state *State) { state.Events = append(state.Events, state.Events[0]) }},
		{"missing decision reference", func(state *State) { state.Events[0].DecisionIDs = []string{"decision-missing"} }},
		{"duplicate session ID", func(state *State) {
			duplicate := state.Machine.Sessions[0]
			duplicate.ID = "session-copy"
			state.Machine.Sessions = append(state.Machine.Sessions, duplicate)
		}},
		{"duplicate session report identity", func(state *State) {
			duplicate := state.Machine.Sessions[0]
			duplicate.SessionID = "source-copy"
			state.Machine.Sessions = append(state.Machine.Sessions, duplicate)
		}},
		{"duplicate evidence ID", func(state *State) {
			duplicate := state.Machine.Evidence["decision-a"][0]
			state.Machine.Evidence["decision-a"] = append(state.Machine.Evidence["decision-a"], duplicate)
		}},
		{"project ID mismatch", func(state *State) { state.Machine.ProjectID = "project-other" }},
		{"revision mismatch", func(state *State) { state.Machine.AcceptedRevision++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cloneState(t, valid)
			test.mutate(&state)
			if err := Validate(state); err == nil {
				t.Fatalf("%s accepted", test.name)
			}
		})
	}
}

func TestValidateRejectsBrokenIdentityAndReferenceClosure(t *testing.T) {
	valid := validState(t)
	tests := []struct {
		name   string
		mutate func(*State)
	}{
		{"risk and decision share an ID", func(state *State) { state.Review.Risks[0].ID = "decision-a" }},
		{"event and session report share an ID", func(state *State) { state.Events[0].ID = "session-report-a" }},
		{"unknown evidence owner", func(state *State) { state.Machine.Evidence["entity-missing"] = []ledger.EvidenceRef{} }},
		{"unknown evidence source session", func(state *State) {
			refs := state.Machine.Evidence["decision-a"]
			refs[0].SessionID = "session-missing"
			state.Machine.Evidence["decision-a"] = refs
		}},
		{"unknown session-report evidence source session", func(state *State) {
			ref := state.Machine.Evidence["decision-a"][0]
			ref.SessionID = "session-missing"
			state.Machine.Sessions[0].Evidence = []ledger.EvidenceRef{ref}
		}},
		{"unknown session-phase evidence source session", func(state *State) {
			ref := state.Machine.Evidence["decision-a"][0]
			ref.SessionID = "session-missing"
			state.Machine.Sessions[0].Phases = []ledger.SessionPhase{{Evidence: []ledger.EvidenceRef{ref}}}
		}},
		{"duplicate event decision reference", func(state *State) { state.Events[0].DecisionIDs = []string{"decision-a", "decision-a"} }},
		{"missing session decision reference", func(state *State) { state.Machine.Sessions[0].DecisionsAdded = []string{"decision-missing"} }},
		{"duplicate session decision reference", func(state *State) {
			state.Machine.Sessions[0].DecisionsAdded = []string{"decision-a"}
			state.Machine.Sessions[0].DecisionsRevised = []string{"decision-a"}
		}},
		{"missing session risk reference", func(state *State) { state.Machine.Sessions[0].OpenLoopsCreated = []string{"risk-missing"} }},
		{"duplicate session risk reference", func(state *State) {
			state.Machine.Sessions[0].OpenLoopsCreated = []string{"risk-a"}
			state.Machine.Sessions[0].OpenLoopsClosed = []string{"risk-a"}
		}},
		{"missing previous session", func(state *State) { state.Machine.Sessions[1].PreviousSessionID = "session-missing" }},
		{"missing next session", func(state *State) { state.Machine.Sessions[0].NextSessionID = "session-missing" }},
		{"nonreciprocal session link", func(state *State) { state.Machine.Sessions[0].NextSessionID = "" }},
		{"disconnected session chain", func(state *State) {
			state.Machine.Sessions[0].NextSessionID = ""
			state.Machine.Sessions[1].PreviousSessionID = ""
		}},
		{"cyclic session chain", func(state *State) {
			state.Machine.Sessions[0].PreviousSessionID = "session-z"
			state.Machine.Sessions[0].NextSessionID = "session-z"
			state.Machine.Sessions[1].PreviousSessionID = "session-a"
			state.Machine.Sessions[1].NextSessionID = "session-a"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cloneState(t, valid)
			test.mutate(&state)
			if err := Validate(state); err == nil {
				t.Fatalf("%s accepted", test.name)
			}
		})
	}
}

func TestMachineLedgerRejectsUnsafeNumbersHashesVersionsAndSizes(t *testing.T) {
	machine, err := ParseMachineLedger(mustFixture(t, "../../testdata/review-v2/ledger.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*MachineLedger)
	}{
		{"schema version", func(value *MachineLedger) { value.SchemaVersion++ }},
		{"uppercase review hash", func(value *MachineLedger) { value.ReviewSHA256 = strings.ToUpper(value.ReviewSHA256) }},
		{"short history hash", func(value *MachineLedger) { value.HistorySHA256 = "abc" }},
		{"negative project duration", func(value *MachineLedger) { value.Accounting.TotalDurationMS = -1 }},
		{"negative project tokens", func(value *MachineLedger) { value.Accounting.TotalTokens = -1 }},
		{"negative project cost", func(value *MachineLedger) { value.Accounting.TotalCostUSD = -1 }},
		{"non-finite project cost", func(value *MachineLedger) { value.Accounting.TotalCostUSD = math.Inf(1) }},
		{"negative model tokens", func(value *MachineLedger) { value.Accounting.Models[0].TotalTokens = -1 }},
		{"non-finite session cost", func(value *MachineLedger) { value.Sessions[0].Accounting.TotalCostUSD = math.NaN() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneMachine(t, machine)
			test.mutate(&value)
			if _, err := RenderMachineLedger(value); err == nil {
				t.Fatalf("%s accepted", test.name)
			}
		})
	}

	if _, err := ParseMachineLedger(bytes.Repeat([]byte(" "), MaxMachineLedgerBytes+1)); err == nil {
		t.Fatal("oversized machine ledger accepted")
	}
	if err := validateDocumentSize("review", bytes.Repeat([]byte("x"), MaxDocumentBytes)); err != nil {
		t.Fatalf("document at limit rejected: %v", err)
	}
	if err := validateDocumentSize("review", bytes.Repeat([]byte("x"), MaxDocumentBytes+1)); err == nil {
		t.Fatal("oversized human document accepted")
	}
}

func TestParseMachineLedgerRejectsDuplicateJSONKeysAtAnyDepth(t *testing.T) {
	valid := string(mustFixture(t, "../../testdata/review-v2/ledger.valid.json"))
	tests := map[string]string{
		"top level": strings.Replace(valid,
			`"schema_version": 2,`,
			`"schema_version": 2, "schema_version": 2,`, 1),
		"nested accounting": strings.Replace(valid,
			`"total_cost_usd": 0.0038,`,
			`"total_cost_usd": 0.0038, "total_cost_usd": 0.0038,`, 1),
		"nested evidence": strings.Replace(valid,
			`"evidence_id": "evidence-decision-a",`,
			`"evidence_id": "evidence-decision-a", "evidence_id": "evidence-decision-a",`, 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseMachineLedger([]byte(body))
			if err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
				t.Fatalf("duplicate key accepted or misclassified: %v", err)
			}
		})
	}
}

func TestParseMachineLedgerRejectsExactKeyWithCaseAliasConflict(t *testing.T) {
	valid := string(mustFixture(t, "../../testdata/review-v2/ledger.valid.json"))
	body := strings.Replace(valid,
		`"schema_version": 2,`,
		`"schema_version": 999, "SCHEMA_VERSION": 2,`, 1)
	if body == valid {
		t.Fatal("test did not inject schema-version alias")
	}
	if _, err := ParseMachineLedger([]byte(body)); err == nil || !strings.Contains(err.Error(), `unknown JSON object key "SCHEMA_VERSION"`) {
		t.Fatalf("exact key plus case alias conflict accepted or misclassified: %v", err)
	}
}

func TestParseMachineLedgerRejectsCaseAliasesThroughoutWireTree(t *testing.T) {
	valid := string(mustFixture(t, "../../testdata/review-v2/ledger.valid.json"))
	tests := map[string][2]string{
		"root":               {`"project_id":`, `"PROJECT_ID":`},
		"project accounting": {`"total_tokens":`, `"TOTAL_TOKENS":`},
		"project model":      {`"token_share_pct":`, `"TOKEN_SHARE_PCT":`},
		"session report":     {`"initial_goal":`, `"INITIAL_GOAL":`},
		"session phase":      {`"title": "Contract",`, `"TITLE": "Contract",`},
		"evidence entry":     {`"refs":`, `"REFS":`},
		"evidence ref":       {`"evidence_id":`, `"EVIDENCE_ID":`},
		"session accounting": {`"started_at":`, `"STARTED_AT":`},
		"model accounting":   {`"input_tokens":`, `"INPUT_TOKENS":`},
		"pricing":            {`"currency":`, `"CURRENCY":`},
	}
	for name, replacement := range tests {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(valid, replacement[0], replacement[1], 1)
			if body == valid {
				t.Fatalf("test did not inject alias %s", replacement[1])
			}
			if _, err := ParseMachineLedger([]byte(body)); err == nil || !strings.Contains(err.Error(), "unknown JSON object key") {
				t.Fatalf("case alias %s accepted or misclassified: %v", replacement[1], err)
			}
		})
	}
}

func TestMachineLedgerSchemaMatchesGoContractAndFixtures(t *testing.T) {
	body := mustFixture(t, "../../schemas/review-ledger-v2.schema.json")
	var schema map[string]any
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["additionalProperties"] != false {
		t.Fatalf("schema is not a closed draft-2020-12 object: %s", body)
	}
	comment, _ := schema["$comment"].(string)
	for _, semanticContract := range []string{"duplicate JSON object keys", "case-sensitive object-key allowlists", "cross-item identity uniqueness", "evidence owner and source-session existence", "entity-reference closure", "reciprocal acyclic session chain"} {
		if !strings.Contains(comment, semanticContract) {
			t.Fatalf("schema does not document codec-only semantic contract %q: %q", semanticContract, comment)
		}
	}
	required, _ := schema["required"].([]any)
	for _, field := range requiredMachineLedgerJSONFields(t) {
		if !containsJSONValue(required, field) {
			t.Fatalf("MachineLedger field %q is not required by schema", field)
		}
	}
	assertClosedObjectDefinitions(t, schema)
	assertNonnegativeNumericDefinitions(t, schema)
	assertFixtureTopLevelShape(t, schema, mustFixture(t, "../../testdata/review-v2/ledger.valid.json"))
	assertFixtureTopLevelShape(t, schema, mustFixture(t, "../../testdata/review-v2/ledger.invalid-duplicate-id.json"))
}

func TestStableRelativePaths(t *testing.T) {
	if ReviewRelativePath != "docs/session-review/项目回顾.md" ||
		HistoryRelativePath != "docs/session-review/项目历史.md" ||
		MachineLedgerRelativePath != "docs/session-review/.session-reviewer/ledger.json" {
		t.Fatalf("unexpected v2 paths: %q %q %q", ReviewRelativePath, HistoryRelativePath, MachineLedgerRelativePath)
	}
}

func mustFixture(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func cloneMachine(t *testing.T, value MachineLedger) MachineLedger {
	t.Helper()
	body, err := RenderMachineLedger(value)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := ParseMachineLedger(body)
	if err != nil {
		t.Fatal(err)
	}
	return clone
}

func cloneState(t *testing.T, value State) State {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone State
	if err := json.Unmarshal(body, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func validState(t *testing.T) State {
	t.Helper()
	machine, err := ParseMachineLedger(mustFixture(t, "../../testdata/review-v2/ledger.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	return State{
		Review: Review{
			ProjectID: machine.ProjectID,
			Revision:  machine.AcceptedRevision,
			Risks:     []Risk{{ID: "risk-a"}},
			Decisions: []Decision{{ID: "decision-a"}},
		},
		Events:  []Event{{ID: "event-a", DecisionIDs: []string{"decision-a"}}},
		Machine: machine,
	}
}

func requiredMachineLedgerJSONFields(t *testing.T) []string {
	t.Helper()
	typeOfLedger := reflect.TypeOf(MachineLedger{})
	fields := make([]string, 0, typeOfLedger.NumField())
	for index := 0; index < typeOfLedger.NumField(); index++ {
		tag := typeOfLedger.Field(index).Tag.Get("json")
		parts := strings.Split(tag, ",")
		if parts[0] == "" || parts[0] == "-" || containsString(parts[1:], "omitempty") {
			continue
		}
		fields = append(fields, parts[0])
	}
	return fields
}

func containsJSONValue(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertClosedObjectDefinitions(t *testing.T, value any) {
	t.Helper()
	var visit func(path string, current any)
	visit = func(path string, current any) {
		object, ok := current.(map[string]any)
		if !ok {
			if values, ok := current.([]any); ok {
				for index, item := range values {
					visit(path+"["+string(rune('0'+index))+"]", item)
				}
			}
			return
		}
		if object["type"] == "object" && object["additionalProperties"] != false {
			t.Fatalf("machine-owned object %s is not closed: %+v", path, object)
		}
		for name, child := range object {
			visit(path+"."+name, child)
		}
	}
	visit("schema", value)
}

func assertNonnegativeNumericDefinitions(t *testing.T, schema map[string]any) {
	t.Helper()
	var visit func(path string, current any)
	visit = func(path string, current any) {
		object, ok := current.(map[string]any)
		if !ok {
			if values, ok := current.([]any); ok {
				for _, item := range values {
					visit(path, item)
				}
			}
			return
		}
		if properties, ok := object["properties"].(map[string]any); ok {
			for name, raw := range properties {
				property, _ := raw.(map[string]any)
				switch {
				case name == "duration_ms" || name == "total_duration_ms" || strings.HasSuffix(name, "_tokens"):
					if property["type"] != "integer" || property["minimum"] != float64(0) {
						t.Fatalf("%s.%s is not a nonnegative integer: %+v", path, name, property)
					}
				case strings.Contains(name, "cost") || strings.HasSuffix(name, "_per_million"):
					zeroSentinel := property["const"] == float64(0)
					if !zeroSentinel && (property["type"] != "number" || property["minimum"] != float64(0)) {
						t.Fatalf("%s.%s is not a nonnegative number: %+v", path, name, property)
					}
				}
			}
		}
		for name, child := range object {
			visit(path+"."+name, child)
		}
	}
	visit("schema", schema)
}

func assertFixtureTopLevelShape(t *testing.T, schema map[string]any, body []byte) {
	t.Helper()
	var fixture map[string]any
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	properties, _ := schema["properties"].(map[string]any)
	for name := range fixture {
		if _, ok := properties[name]; !ok {
			t.Fatalf("fixture has property %q outside the public schema", name)
		}
	}
	required, _ := schema["required"].([]any)
	for _, raw := range required {
		name, _ := raw.(string)
		if _, ok := fixture[name]; !ok {
			t.Fatalf("fixture omits required property %q", name)
		}
	}
	version, _ := properties["schema_version"].(map[string]any)
	if version["const"] != float64(SchemaVersion) || fixture["schema_version"] != float64(SchemaVersion) {
		t.Fatalf("schema or fixture has wrong schema_version: schema=%+v fixture=%v", version, fixture["schema_version"])
	}
}
