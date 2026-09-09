package memory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCaseSensitiveSessionIdentityPreservesOtherIdentityConstraints(t *testing.T) {
	for _, id := range []string{"ses_nativeRoot", "ses_nativeroot"} {
		source := validSourceRecord()
		source.Provider = "opencode"
		source.SessionID = id
		if err := ValidateSourceRecord(source); err != nil {
			t.Fatalf("source Session ID %q: %v", id, err)
		}
		observation := validObservation(validObservationKey(), "adapter-1", nil)
		observation.Key.Provider, observation.Ref.Provider = "opencode", "opencode"
		observation.Key.SessionID, observation.Ref.SessionID = id, id
		observation.RevisionID = ObservationRevisionID(observation)
		if err := ValidateObservationRevision(observation); err != nil {
			t.Fatalf("observation Session ID %q: %v", id, err)
		}
		view := validSessionView()
		view.Provider = "opencode"
		view.SessionID = id
		view.Digest, _ = SessionViewDigest(view)
		if err := ValidateSessionView(view); err != nil {
			t.Fatalf("SessionView ID %q: %v", id, err)
		}
	}
	for _, identity := range []struct{ provider, session, source string }{
		{"OpenCode", "ses_nativeRoot", "source-a"},
		{"opencode", "ses_nativeRoot", "Source-a"},
		{"opencode", "../ses_nativeRoot", "source-a"},
		{"opencode", "ses/nativeRoot", "source-a"},
		{"opencode", `ses\nativeRoot`, "source-a"},
		{"opencode", "ses:nativeRoot", "source-a"},
		{"opencode", "ses_nativeRoot\x00", "source-a"},
		{"opencode", strings.Repeat("A", 129), "source-a"},
	} {
		if err := validateSourceIdentity(identity.provider, identity.session, identity.source); err == nil {
			t.Fatalf("unsafe identity accepted: %+v", identity)
		}
	}
	source := validSourceRecord()
	source.SessionID = "ses_nativeRoot"
	source.ProjectIDs = []string{"Project-a"}
	if err := ValidateSourceRecord(source); err == nil {
		t.Fatal("mixed-case project identity accepted")
	}
	sessions := []SessionViewDependency{{Provider: "opencode", SessionID: "ses_nativeRoot", Digest: revisionDigest("1")}, {Provider: "opencode", SessionID: "ses_nativeroot", Digest: revisionDigest("2")}}
	if err := validateSessionDependencies(sessions, 2); err != nil {
		t.Fatalf("case-sensitive Session dependencies collided: %v", err)
	}
	lineages := []SessionLineageDependency{{Provider: "opencode", SessionID: "ses_nativeRoot", Digest: revisionDigest("1")}, {Provider: "opencode", SessionID: "ses_nativeroot", Digest: revisionDigest("2")}}
	if err := validateLineageDependencies(lineages, sessions); err != nil {
		t.Fatal(err)
	}
	usage := []AssociatedUsage{{Provider: "opencode", SessionID: "ses_nativeRoot", UsageRecordDigest: revisionDigest("1")}, {Provider: "opencode", SessionID: "ses_nativeroot", UsageRecordDigest: revisionDigest("2")}}
	if err := validateAssociatedUsage(usage); err != nil {
		t.Fatal(err)
	}
}

func TestCaseSensitiveSessionIDsMatchPrivateSchemas(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
	}{
		{"source-catalog-v1", validSourceRecord()},
		{"observation-v1", validObservation(validObservationKey(), "adapter-1", nil)},
		{"session-view-v1", validSessionView()},
		{"session-lineage-v1", validSessionLineage()},
		{"project-view-v1", validProjectView()},
		{"generation-manifest-v1", validGenerationManifest()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal(body, &value); err != nil {
				t.Fatal(err)
			}
			var changeSessions func(any)
			changeSessions = func(v any) {
				switch node := v.(type) {
				case map[string]any:
					for key, child := range node {
						if key == "session_id" {
							node[key] = "ses_nativeRoot"
						} else {
							changeSessions(child)
						}
					}
				case []any:
					for _, child := range node {
						changeSessions(child)
					}
				}
			}
			changeSessions(value)
			body, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateJSONSchemaFixture("../../schemas/"+tt.name+".schema.json", body); err != nil {
				t.Fatal(err)
			}
		})
	}
}
