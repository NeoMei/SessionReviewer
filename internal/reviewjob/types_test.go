package reviewjob

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

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
		ReviewUsage: ReviewUsage{TokenUsage: accounting.TokenUsage{
			InputTokens: 1, OutputTokens: 1, TotalTokens: 2,
		}},
	}
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
	schemaBody, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	var status map[string]any
	if err := json.Unmarshal(schemaBody, &schema); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["additionalProperties"] != false {
		t.Fatalf("status schema is not a closed draft-2020-12 object: %s", schemaBody)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("status schema properties missing")
	}
	for name := range status {
		if _, ok := properties[name]; !ok {
			t.Fatalf("public JSON field %q is absent from schema", name)
		}
	}
	for _, name := range []string{"schema_version", "project_id", "state", "attempt", "session_index", "session_count", "accepted_packets", "accepted_sessions", "can_retry", "can_cancel", "can_sync_only"} {
		if _, ok := status[name]; !ok {
			t.Fatalf("public JSON missing required field %q: %s", name, body)
		}
	}
}
