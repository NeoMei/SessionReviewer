package reviewjob

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const jsSafeInteger = 1<<53 - 1

// This test catches a projection that serializes any private Job member, or
// whose public JSON diverges from the published v1 status contract.
func TestPublicStatusIsSchemaValidAndCannotExposePrivateFields(t *testing.T) {
	job := validJobFixture()
	job.PrivateError = "/Users/mei/.codex/sessions/raw secret prompt"
	status, err := ProjectStatus(&job, job.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	body := mustJSON(t, status)
	for _, forbidden := range []string{"/Users/", "raw secret", "source_hash", "prompt", "stdout", "stderr"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("public leak %q: %s", forbidden, body)
		}
	}
	validateAgainstSchema(t, "../../schemas/review-job-status-v1.schema.json", body)
}

// This catches an idle response that accidentally leaks a historical job ID or
// depends on private job storage to describe a project with no job.
func TestPublicStatusForNilJobIsIdleWithoutJobID(t *testing.T) {
	status, err := ProjectStatus(nil, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if status != (PublicStatus{SchemaVersion: PublicStatusSchemaVersion, ProjectID: "project-1", State: Idle}) {
		t.Fatalf("idle status = %#v", status)
	}
	if bytes.Contains(mustJSON(t, status), []byte(`"job_id"`)) {
		t.Fatal("idle status exposed job_id")
	}
}

// This catches accepting a private job whose state cannot safely be projected
// because progress exceeds the frozen session set.
func TestJobValidationRejectsImpossibleProgress(t *testing.T) {
	job := validJobFixture()
	job.SessionIndex = len(job.FrozenSessions) + 1
	if err := Validate(job); err == nil || !strings.Contains(err.Error(), "session index") {
		t.Fatalf("Validate() error = %v, want session index rejection", err)
	}
}

// This catches terminal jobs that retain a live worker owner and could be
// mistaken for an active job during restart recovery.
func TestJobValidationRejectsTerminalJobWithLiveOwner(t *testing.T) {
	job := validJobFixture()
	job.State = Completed
	if err := Validate(job); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("Validate() error = %v, want terminal ownership rejection", err)
	}
}

// This catches accepting the standard evidence digest in a private job only
// when it happens not to use the repository's sha256: prefix.
func TestJobValidationAcceptsEvidenceDigest(t *testing.T) {
	packet := evidence.Packet{SchemaVersion: 2, ProjectID: "project-1", SessionID: "session-1"}
	digest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	job := validJobFixture()
	job.PacketDigest = digest
	if err := Validate(job); err != nil {
		t.Fatalf("Validate() rejected evidence digest %q: %v", digest, err)
	}
}

// This catches cursor/accounting completion states that could advance beyond
// the click-time frozen evidence boundary or claim completed work that was not
// accepted.
func TestJobValidationRejectsInvalidProgressInvariants(t *testing.T) {
	completed := func() Job {
		job := validJobFixture()
		job.State = Completed
		job.Phase = ""
		job.Owner = Owner{}
		job.CompletedAt = job.UpdatedAt
		job.SessionIndex = len(job.FrozenSessions)
		job.AcceptedPackets = len(job.FrozenSessions)
		job.AcceptedSessions = len(job.FrozenSessions)
		return job
	}
	tests := []struct {
		name string
		job  Job
	}{
		{
			name: "current boundary is beyond frozen upper line",
			job: func() Job {
				job := validJobFixture()
				job.CurrentPacket = evidence.CursorBoundary{Line: 2, SourceHash: strings.Repeat("b", 64)}
				return job
			}(),
		},
		{
			name: "current boundary equal line has a different source hash",
			job: func() Job {
				job := validJobFixture()
				job.CurrentPacket = evidence.CursorBoundary{Line: 1, SourceHash: strings.Repeat("b", 64)}
				return job
			}(),
		},
		{
			name: "nonzero current boundary without frozen session",
			job: func() Job {
				job := validJobFixture()
				job.FrozenSessions = nil
				job.CurrentPacket = evidence.CursorBoundary{Line: 1, SourceHash: strings.Repeat("a", 64)}
				return job
			}(),
		},
		{
			name: "accepted sessions exceed accepted packets",
			job: func() Job {
				job := validJobFixture()
				job.AcceptedSessions = 1
				return job
			}(),
		},
		{
			name: "completed job has unaccepted frozen session",
			job: func() Job {
				job := completed()
				job.AcceptedSessions = 0
				return job
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(test.job); err == nil {
				t.Fatal("Validate() accepted invalid progress")
			}
		})
	}
}

func TestJobValidationRejectsNonFiniteCostAndUnsafePublicCounts(t *testing.T) {
	for name, mutate := range map[string]func(*Job){
		"NaN cost": func(job *Job) {
			job.ReviewAccounting = validReviewAccountingFixture()
			*job.ReviewAccounting.TotalCostUSD = math.NaN()
		},
		"infinite cost": func(job *Job) {
			job.ReviewAccounting = validReviewAccountingFixture()
			*job.ReviewAccounting.TotalCostUSD = math.Inf(1)
		},
		"unsafe attempt":           func(job *Job) { job.Attempt = jsSafeInteger + 1 },
		"unsafe session index":     func(job *Job) { job.SessionIndex = jsSafeInteger + 1 },
		"unsafe accepted packets":  func(job *Job) { job.AcceptedPackets = jsSafeInteger + 1 },
		"unsafe accepted sessions": func(job *Job) { job.AcceptedSessions = jsSafeInteger + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			job := validJobFixture()
			mutate(&job)
			if err := Validate(job); err == nil {
				t.Fatal("Validate() accepted non-finite cost or unsafe public count")
			}
		})
	}
}

func TestPublicCountValidationRejectsUnsafeSessionCount(t *testing.T) {
	if err := validatePublicCounts(1, 0, jsSafeInteger+1, 0, 0); err == nil {
		t.Fatal("public count validation accepted an unsafe session count")
	}
}

func TestJobValidationAllowsSyncOnlyFailureRecoveryOnly(t *testing.T) {
	for _, state := range []State{Completed, Cancelled} {
		job := terminalJobFixture(state)
		job.AcceptedPackets = 1
		job.AcceptedSessions = 1
		job.AcceptedSyncPending = true
		if err := Validate(job); err == nil {
			t.Fatalf("Validate() accepted sync-only %s job", state)
		}
	}
	job := terminalJobFixture(Failed)
	job.AcceptedPackets = 1
	job.AcceptedSyncPending = true
	if err := Validate(job); err != nil {
		t.Fatalf("Validate() rejected failed sync-only recovery: %v", err)
	}
}

func validJobFixture() Job {
	created := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	return Job{
		SchemaVersion:   PublicStatusSchemaVersion,
		ID:              "job-1",
		ProjectID:       "project-1",
		ProjectIdentity: pathguard.IdentityToken{Kind: "posix-dev-inode", Volume: "1", File: "2"},
		Agent: VerifiedAgent{
			Kind:       "codex",
			Identity:   pathguard.IdentityToken{Kind: "posix-dev-inode", Volume: "1", File: "3"},
			Version:    "1.2.3",
			Executable: "/usr/local/bin/codex",
		},
		State:   Running,
		Phase:   Reviewing,
		Attempt: 1,
		FrozenSessions: []FrozenSession{{
			SessionID: "session-1",
			StartedAt: created,
			Upper:     evidence.CursorBoundary{Line: 1, SourceHash: strings.Repeat("a", 64)},
		}},
		SessionIndex:     0,
		AcceptedPackets:  0,
		AcceptedSessions: 0,
		CreatedAt:        created,
		UpdatedAt:        created,
		Owner:            Owner{ID: "owner-1", AcquiredAt: created},
	}
}

func validReviewAccountingFixture() ReviewAccounting {
	cost := .000002
	return ReviewAccounting{
		SnapshotAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		Models: []accounting.ModelAccounting{{
			ModelUsage: accounting.ModelUsage{Model: "fixture-model", TokenUsage: accounting.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}},
			Pricing:    fixturePricing(1, 0, 0, 1),
			CostUSD:    cost,
		}},
		TotalTokens:     2,
		TotalCostUSD:    &cost,
		PricingComplete: true,
	}
}

func terminalJobFixture(state State) Job {
	job := validJobFixture()
	job.State = state
	job.Phase = ""
	job.Owner = Owner{}
	job.CompletedAt = job.UpdatedAt
	return job
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// Existing schema tests in this repository inspect the published structural
// contract directly instead of adding a runtime JSON-schema dependency.
func validateAgainstSchema(t *testing.T, filename string, body []byte) {
	t.Helper()
	if err := validateStatusAgainstSchema(filename, body); err != nil {
		t.Fatal(err)
	}
}

// The repository intentionally validates this small published protocol with
// Go assertions rather than taking a runtime JSON-schema dependency.
func validateStatusAgainstSchema(filename string, body []byte) error {
	schemaBody, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	var schema map[string]any
	var status map[string]any
	if err := json.Unmarshal(schemaBody, &schema); err != nil {
		return err
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return err
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["additionalProperties"] != false {
		return fmt.Errorf("status schema is not a closed draft-2020-12 object")
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("status schema properties missing")
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		return fmt.Errorf("status schema definitions missing")
	}
	reviewDefinition, ok := definitions["review_usage"].(map[string]any)
	if !ok || reviewDefinition["additionalProperties"] != false {
		return fmt.Errorf("review_usage schema is missing or open")
	}
	reviewProperties, ok := reviewDefinition["properties"].(map[string]any)
	if !ok || len(reviewProperties) != 3 || reviewProperties["total_tokens"] == nil || reviewProperties["total_cost_usd"] == nil || reviewProperties["pricing_complete"] == nil {
		return fmt.Errorf("review_usage schema fields do not match public totals contract")
	}
	totalTokensSchema, _ := reviewProperties["total_tokens"].(map[string]any)
	pricingCompleteSchema, _ := reviewProperties["pricing_complete"].(map[string]any)
	totalCostSchema, _ := reviewProperties["total_cost_usd"].(map[string]any)
	if totalTokensSchema["$ref"] != "#/$defs/nonnegative_integer" || pricingCompleteSchema["type"] != "boolean" || !reflect.DeepEqual(totalCostSchema["type"], []any{"number", "null"}) || totalCostSchema["minimum"] != float64(0) {
		return fmt.Errorf("review_usage schema scalar constraints are invalid")
	}
	requiredReview, ok := reviewDefinition["required"].([]any)
	if !ok || !reflect.DeepEqual(requiredReview, []any{"total_tokens", "pricing_complete"}) {
		return fmt.Errorf("review_usage schema required fields are invalid")
	}
	conditions, ok := reviewDefinition["allOf"].([]any)
	if !ok || len(conditions) != 1 {
		return fmt.Errorf("review_usage schema pricing condition is missing")
	}
	condition, ok := conditions[0].(map[string]any)
	if !ok || condition["if"] == nil || condition["then"] == nil || condition["else"] == nil {
		return fmt.Errorf("review_usage schema must use if/then/else pricing semantics")
	}
	ifBranch, _ := condition["if"].(map[string]any)
	thenBranch, _ := condition["then"].(map[string]any)
	elseBranch, _ := condition["else"].(map[string]any)
	ifProperties, _ := ifBranch["properties"].(map[string]any)
	ifComplete, _ := ifProperties["pricing_complete"].(map[string]any)
	thenProperties, _ := thenBranch["properties"].(map[string]any)
	thenCost, _ := thenProperties["total_cost_usd"].(map[string]any)
	elseProperties, _ := elseBranch["properties"].(map[string]any)
	elseCost, _ := elseProperties["total_cost_usd"].(map[string]any)
	if ifComplete["const"] != true || !reflect.DeepEqual(ifBranch["required"], []any{"pricing_complete"}) || !reflect.DeepEqual(thenBranch["required"], []any{"total_cost_usd"}) || thenCost["type"] != "number" || thenCost["minimum"] != float64(0) || elseCost["type"] != "null" {
		return fmt.Errorf("review_usage schema pricing condition is incomplete")
	}
	for name := range status {
		if _, ok := properties[name]; !ok {
			return fmt.Errorf("public JSON field %q is absent from schema", name)
		}
	}
	for _, name := range []string{"schema_version", "project_id", "state", "attempt", "session_index", "session_count", "accepted_packets", "accepted_sessions", "can_retry", "can_cancel", "can_sync_only"} {
		if _, ok := status[name]; !ok {
			return fmt.Errorf("public JSON missing required field %q", name)
		}
	}
	for _, name := range []string{"schema_version", "attempt", "session_index", "session_count", "accepted_packets", "accepted_sessions"} {
		value, ok := status[name].(float64)
		if !ok || math.Trunc(value) != value || value < 0 || value > float64(jsSafeInteger) {
			return fmt.Errorf("public numeric field %q is not a JS-safe nonnegative integer", name)
		}
	}
	if status["schema_version"] != float64(PublicStatusSchemaVersion) {
		return fmt.Errorf("public schema_version is invalid")
	}
	for _, name := range []string{"can_retry", "can_cancel", "can_sync_only"} {
		if _, ok := status[name].(bool); !ok {
			return fmt.Errorf("public boolean field %q has wrong type", name)
		}
	}
	for _, name := range []string{"project_id", "state"} {
		if _, ok := status[name].(string); !ok {
			return fmt.Errorf("public string field %q has wrong type", name)
		}
	}
	if !safeID.MatchString(status["project_id"].(string)) || !validPublicState(status["state"].(string)) {
		return fmt.Errorf("public project ID or state is invalid")
	}
	if value, exists := status["job_id"]; exists {
		jobID, ok := value.(string)
		if !ok || !safeID.MatchString(jobID) {
			return fmt.Errorf("public job_id is invalid")
		}
	}
	if value, exists := status["phase"]; exists {
		phase, ok := value.(string)
		if !ok || phase == "" || !validPhase(Phase(phase)) {
			return fmt.Errorf("public phase is invalid")
		}
	}
	if value, exists := status["error_code"]; exists {
		code, ok := value.(string)
		if !ok || !validErrorCode(ErrorCode(code)) {
			return fmt.Errorf("public error_code is invalid")
		}
	}
	if usage, exists := status["review_usage"]; exists {
		usageMap, ok := usage.(map[string]any)
		if !ok || (len(usageMap) != 2 && len(usageMap) != 3) {
			return fmt.Errorf("review_usage is not a closed object")
		}
		for name := range usageMap {
			if name != "total_tokens" && name != "total_cost_usd" && name != "pricing_complete" {
				return fmt.Errorf("review_usage field %q is unknown", name)
			}
		}
		tokens, ok := usageMap["total_tokens"].(float64)
		if !ok || math.Trunc(tokens) != tokens || tokens < 0 || tokens > float64(jsSafeInteger) {
			return fmt.Errorf("review_usage total_tokens is invalid")
		}
		complete, ok := usageMap["pricing_complete"].(bool)
		if !ok {
			return fmt.Errorf("review_usage pricing_complete is invalid")
		}
		costValue, hasCost := usageMap["total_cost_usd"]
		nonNullCost := hasCost && costValue != nil
		if complete != nonNullCost {
			return fmt.Errorf("review_usage cost presence disagrees with pricing_complete")
		}
		if nonNullCost {
			cost, ok := costValue.(float64)
			if !ok || math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
				return fmt.Errorf("review_usage total_cost_usd is invalid")
			}
		}
	}
	return nil
}

func TestPublicStatusSchemaAllowsOmittedOrNullCostOnlyWhenPricingIsIncomplete(t *testing.T) {
	base := mustJSON(t, PublicStatus{
		SchemaVersion: PublicStatusSchemaVersion,
		ProjectID:     "project-1",
		State:         Idle,
		ReviewUsage:   &PublicReviewUsage{TotalTokens: 7, PricingComplete: false},
	})
	if err := validateStatusAgainstSchema("../../schemas/review-job-status-v1.schema.json", base); err != nil {
		t.Fatalf("omitted incomplete cost rejected: %v", err)
	}
	var withNull map[string]any
	if err := json.Unmarshal(base, &withNull); err != nil {
		t.Fatal(err)
	}
	withNull["review_usage"].(map[string]any)["total_cost_usd"] = nil
	nullBody, err := json.Marshal(withNull)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStatusAgainstSchema("../../schemas/review-job-status-v1.schema.json", nullBody); err != nil {
		t.Fatalf("null incomplete cost rejected: %v", err)
	}
}

func TestPublicStatusSchemaPricingCompletenessStates(t *testing.T) {
	base := mustJSON(t, PublicStatus{SchemaVersion: PublicStatusSchemaVersion, ProjectID: "project-1", State: Idle})
	tests := []struct {
		name  string
		usage map[string]any
		valid bool
	}{
		{name: "complete numeric", usage: map[string]any{"total_tokens": float64(1), "total_cost_usd": float64(.25), "pricing_complete": true}, valid: true},
		{name: "incomplete omitted", usage: map[string]any{"total_tokens": float64(1), "pricing_complete": false}, valid: true},
		{name: "incomplete null", usage: map[string]any{"total_tokens": float64(1), "total_cost_usd": nil, "pricing_complete": false}, valid: true},
		{name: "complete omitted", usage: map[string]any{"total_tokens": float64(1), "pricing_complete": true}, valid: false},
		{name: "complete null", usage: map[string]any{"total_tokens": float64(1), "total_cost_usd": nil, "pricing_complete": true}, valid: false},
		{name: "incomplete numeric", usage: map[string]any{"total_tokens": float64(1), "total_cost_usd": float64(0), "pricing_complete": false}, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var status map[string]any
			if err := json.Unmarshal(base, &status); err != nil {
				t.Fatal(err)
			}
			status["review_usage"] = test.usage
			body, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			err = validateStatusAgainstSchema("../../schemas/review-job-status-v1.schema.json", body)
			if (err == nil) != test.valid {
				t.Fatalf("schema validity=%v want=%v err=%v body=%s", err == nil, test.valid, err, body)
			}
		})
	}
}

func TestPublicStatusSchemaRejectsWrongTypesUnsafeNumbersAndUnknownFields(t *testing.T) {
	valid := mustJSON(t, PublicStatus{SchemaVersion: 1, ProjectID: "project-1", State: Idle})
	tests := map[string]func(map[string]any){
		"wrong schema version":  func(status map[string]any) { status["schema_version"] = float64(2) },
		"unknown state":         func(status map[string]any) { status["state"] = "unexpected" },
		"unsafe attempt":        func(status map[string]any) { status["attempt"] = float64(jsSafeInteger + 1) },
		"fractional count":      func(status map[string]any) { status["session_count"] = 0.5 },
		"wrong boolean":         func(status map[string]any) { status["can_cancel"] = "false" },
		"unknown field":         func(status map[string]any) { status["private_error"] = "secret" },
		"invalid job ID":        func(status map[string]any) { status["job_id"] = "../job" },
		"wrong job ID type":     func(status map[string]any) { status["job_id"] = float64(1) },
		"invalid phase":         func(status map[string]any) { status["phase"] = "invalid" },
		"wrong phase type":      func(status map[string]any) { status["phase"] = float64(1) },
		"invalid error code":    func(status map[string]any) { status["error_code"] = "E_UNKNOWN" },
		"wrong error code type": func(status map[string]any) { status["error_code"] = float64(1) },
		"negative cost": func(status map[string]any) {
			status["review_usage"] = map[string]any{"total_tokens": float64(1), "total_cost_usd": float64(-1), "pricing_complete": true}
		},
		"unsafe review total": func(status map[string]any) {
			status["review_usage"] = map[string]any{"total_tokens": float64(jsSafeInteger + 1), "pricing_complete": false}
		},
		"fake incomplete zero cost": func(status map[string]any) {
			status["review_usage"] = map[string]any{"total_tokens": float64(1), "total_cost_usd": float64(0), "pricing_complete": false}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			var status map[string]any
			if err := json.Unmarshal(valid, &status); err != nil {
				t.Fatal(err)
			}
			mutate(status)
			body, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateStatusAgainstSchema("../../schemas/review-job-status-v1.schema.json", body); err == nil {
				t.Fatal("schema helper accepted invalid public JSON")
			}
		})
	}
}
