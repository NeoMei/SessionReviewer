package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
)

func TestObservationRevisionIdentitySeparatesStableKeyFromExtractorRevision(t *testing.T) {
	key := ObservationKey{Provider: "codex", SessionID: "s1", SourceIdentity: "src1", Sequence: 7, ProjectID: "project-a", Kind: "command", Subject: "go-test"}
	first := validObservation(key, "adapter-1", map[string]string{"exit_code": "1"})
	second := validObservation(key, "adapter-2", map[string]string{"exit_code": "0"})
	if first.Key != second.Key {
		t.Fatal("stable key changed")
	}
	if ObservationRevisionID(first) == ObservationRevisionID(second) {
		t.Fatal("revision did not change")
	}
}

func TestPrivateWireSchemasRejectSemanticAndRawConversationFields(t *testing.T) {
	for _, forbidden := range []string{"rationale", "intent", "full_transcript", "raw_tool_output"} {
		assertSchemaRejectsUnknownProperty(t, "../../schemas/observation-v1.schema.json", forbidden)
	}
}

func TestObservationPrivacyContractAllowsOnlyDirectObservedStructuredPayloads(t *testing.T) {
	for _, kind := range []string{"conversation", "rationale", "summary"} {
		t.Run("kind "+kind, func(t *testing.T) {
			value := validObservation(validObservationKey(), "adapter-1", map[string]string{"exit_code": "0"})
			value.Key.Kind = kind
			value.RevisionID = ObservationRevisionID(value)
			if err := ValidateObservationRevision(value); err == nil {
				t.Fatalf("unobserved kind %q accepted", kind)
			}
			assertObservationSchemaRejectsMutation(t, func(object map[string]any) {
				object["key"].(map[string]any)["kind"] = kind
			})
		})
	}

	for _, field := range []string{"raw_tool_output", "full_transcript", "rationale", "summary"} {
		t.Run("field "+field, func(t *testing.T) {
			value := validObservation(validObservationKey(), "adapter-1", map[string]string{field: "payload"})
			if err := ValidateObservationRevision(value); err == nil {
				t.Fatalf("raw or semantic field %q accepted", field)
			}
			assertObservationSchemaRejectsMutation(t, func(object map[string]any) {
				object["fields"] = map[string]any{field: "payload"}
			})
		})
	}

	t.Run("free text outside excerpt", func(t *testing.T) {
		value := validObservation(validObservationKey(), "adapter-1", map[string]string{"exit_code": "0"})
		value.Operation = "go test\ncomplete raw output"
		value.RevisionID = ObservationRevisionID(value)
		if err := ValidateObservationRevision(value); err == nil {
			t.Fatal("free text outside bounded excerpt accepted")
		}
		assertObservationSchemaRejectsMutation(t, func(object map[string]any) {
			object["operation"] = "go test\ncomplete raw output"
		})
	})
}

func TestPrivateWireSchemasAcceptMatchingVersionOneFixtures(t *testing.T) {
	fixtures := []struct {
		name  string
		path  string
		value any
	}{
		{name: "source catalog", path: "../../schemas/source-catalog-v1.schema.json", value: validSourceRecord()},
		{name: "observation", path: "../../schemas/observation-v1.schema.json", value: validObservation(validObservationKey(), "adapter-1", map[string]string{"exit_code": "0"})},
		{name: "session view", path: "../../schemas/session-view-v1.schema.json", value: validSessionView()},
		{name: "session lineage", path: "../../schemas/session-lineage-v1.schema.json", value: validSessionLineage()},
		{name: "project view", path: "../../schemas/project-view-v1.schema.json", value: validProjectView()},
		{name: "generation manifest", path: "../../schemas/generation-manifest-v1.schema.json", value: validGenerationManifest()},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			body, err := json.Marshal(fixture.value)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateJSONSchemaFixture(fixture.path, body); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidationRejectsSemanticRawDuplicateAndImpossibleContracts(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{
			name: "semantic observation kind",
			run: func() error {
				value := validObservation(validObservationKey(), "adapter-1", nil)
				value.Key.Kind = "rationale"
				value.RevisionID = ObservationRevisionID(value)
				return ValidateObservationRevision(value)
			},
			want: "not directly observed",
		},
		{
			name: "raw conversation field",
			run: func() error {
				value := validObservation(validObservationKey(), "adapter-1", map[string]string{"raw_tool_output": "complete output"})
				return ValidateObservationRevision(value)
			},
			want: "raw conversation",
		},
		{
			name: "duplicate active revisions",
			run: func() error {
				value := validSessionView()
				value.ActiveRevisionIDs = append(value.ActiveRevisionIDs, value.ActiveRevisionIDs[0])
				return ValidateSessionView(value)
			},
			want: "duplicate",
		},
		{
			name: "duplicate project dependency",
			run: func() error {
				value := validProjectView()
				value.SessionViewDependencies = append(value.SessionViewDependencies, value.SessionViewDependencies[0])
				value.SourceSessions++
				value.TerminalCounts.Indexed++
				return ValidateProjectView(value)
			},
			want: "duplicate",
		},
		{
			name: "duplicate derived record across project collections",
			run: func() error {
				value := validProjectView()
				value.DerivedRecords = []DerivedRecord{validDerivedRecord()}
				return ValidateProjectView(value)
			},
			want: "duplicate",
		},
		{
			name: "impossible terminal count",
			run: func() error {
				value := validProjectView()
				value.TerminalCounts.Missing = 1
				return ValidateProjectView(value)
			},
			want: "terminal",
		},
		{
			name: "terminal session without SessionView dependency",
			run: func() error {
				value := validProjectView()
				value.SessionViewDependencies = nil
				return ValidateProjectView(value)
			},
			want: "SessionView dependency count",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q rejection", err, test.want)
			}
		})
	}
}

func TestSourceRecordOwnsOneValidatedUsageAtFrozenBoundary(t *testing.T) {
	value := validSourceRecord()
	if err := ValidateSourceRecord(value); err != nil {
		t.Fatal(err)
	}
	value.Usage.TotalTokens++
	if err := ValidateSourceRecord(value); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("invalid usage accepted or misclassified: %v", err)
	}
}

func TestCodexV1UsesExactDiscriminatedJSONLSourceLocations(t *testing.T) {
	t.Run("wire envelope has no flat coordinates", func(t *testing.T) {
		body, err := json.Marshal(validObservation(validObservationKey(), "adapter-1", map[string]string{"exit_code": "0"}).Ref)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(body, &object); err != nil {
			t.Fatal(err)
		}
		if object["jsonl_line"] != nil || object["byte_offset"] != nil {
			t.Fatalf("SourceRef retained flat JSONL coordinates: %s", body)
		}
		location, ok := object["source_location"].(map[string]any)
		if !ok || location["kind"] != "jsonl" {
			t.Fatalf("SourceRef lacks JSONL discriminator: %s", body)
		}
		jsonl, ok := location["jsonl"].(map[string]any)
		if !ok || jsonl["line"] != float64(9) || jsonl["byte_offset"] != float64(128) || len(jsonl) != 2 {
			t.Fatalf("SourceRef JSONL variant is not exact: %s", body)
		}
	})

	for name, mutate := range map[string]func(*ObservationRevision){
		"unsupported provider": func(value *ObservationRevision) {
			value.Key.Provider = "claude"
			value.Ref.Provider = "claude"
		},
		"wrong discriminator": func(value *ObservationRevision) {
			value.Ref.Location.Kind = "stream"
		},
		"missing JSONL payload": func(value *ObservationRevision) {
			value.Ref.Location.JSONL = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := validObservation(validObservationKey(), "adapter-1", map[string]string{"exit_code": "0"})
			mutate(&value)
			value.RevisionID = ObservationRevisionID(value)
			if err := ValidateObservationRevision(value); err == nil {
				t.Fatalf("mismatched provider/location shape %q accepted", name)
			}
		})
	}

	t.Run("observation schema mismatches", func(t *testing.T) {
		for _, mutate := range []func(map[string]any){
			func(object map[string]any) { object["key"].(map[string]any)["provider"] = "claude" },
			func(object map[string]any) { object["source_ref"].(map[string]any)["provider"] = "claude" },
			func(object map[string]any) {
				object["source_ref"].(map[string]any)["source_location"].(map[string]any)["kind"] = "stream"
			},
			func(object map[string]any) {
				delete(object["source_ref"].(map[string]any)["source_location"].(map[string]any), "jsonl")
			},
		} {
			assertObservationSchemaRejectsMutation(t, mutate)
		}
	})

	t.Run("source catalog schema mismatch", func(t *testing.T) {
		body, err := json.Marshal(validSourceRecord())
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(body, &object); err != nil {
			t.Fatal(err)
		}
		object["frozen_boundary"].(map[string]any)["source_location"].(map[string]any)["kind"] = "stream"
		body, err = json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateJSONSchemaFixture("../../schemas/source-catalog-v1.schema.json", body); err == nil {
			t.Fatalf("SourceCatalog schema accepted mismatched location: %s", body)
		}
	})

	t.Run("derived provider references do not claim another adapter", func(t *testing.T) {
		view := validProjectView()
		view.SessionViewDependencies[0].Provider = "claude"
		view.AssociatedUsage[0].Provider = "claude"
		view.Digest = mustProjectViewDigest(view)
		if err := ValidateProjectView(view); err == nil {
			t.Fatal("ProjectView accepted a provider without a v1 adapter")
		}

		manifest := validGenerationManifest()
		manifest.SessionViews[0].Provider = "claude"
		if err := ValidateGenerationManifest(manifest); err == nil {
			t.Fatal("GenerationManifest accepted a provider without a v1 adapter")
		}
	})
}

func TestAssociatedUsageReferencesSourceCatalogWithoutCopiedTotals(t *testing.T) {
	t.Run("Go wire record", func(t *testing.T) {
		body, err := json.Marshal(validProjectView().AssociatedUsage[0])
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte(`"total_tokens"`)) {
			t.Fatalf("AssociatedUsage copied SourceCatalog totals: %s", body)
		}
	})

	t.Run("project schema", func(t *testing.T) {
		body, err := json.Marshal(validProjectView())
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(body, &object); err != nil {
			t.Fatal(err)
		}
		usage := object["associated_usage"].([]any)[0].(map[string]any)
		usage["total_tokens"] = float64(10)
		body, err = json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateJSONSchemaFixture("../../schemas/project-view-v1.schema.json", body); err == nil {
			t.Fatalf("ProjectView schema accepted copied SourceCatalog totals: %s", body)
		}
	})
}

func TestSessionViewCarriesOnlyCompactDependencyBoundObservationSummaries(t *testing.T) {
	view := validSessionView()
	if err := ValidateSessionView(view); err != nil {
		t.Fatal(err)
	}
	if len(view.ObservationSummaries) != 1 || view.ObservationSummaries[0].RevisionID != view.ActiveRevisionIDs[0] {
		t.Fatalf("summary dependency mismatch: %+v", view)
	}
	body, err := json.Marshal(view.ObservationSummaries[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"source_ref", "source_hash", "adapter_id", "adapter_version", "total_tokens", "raw_tool_output", "assistant_message", "transcript"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("summary copied forbidden field %q: %s", forbidden, body)
		}
	}
}

func TestSessionViewAllowsIndependentUsageDigestAndRejectsSummaryDependencyMismatches(t *testing.T) {
	independent := validSessionView()
	independent.UsageRecordDigest = objectDigest("9")
	independent.Digest = mustSessionViewDigest(independent)
	if err := ValidateSessionView(independent); err != nil {
		t.Fatalf("SessionView rejected independently authenticated usage digest: %v", err)
	}
	tests := []struct {
		name string
		run  func(*SessionView)
	}{
		{name: "missing summary", run: func(value *SessionView) { value.ObservationSummaries = nil }},
		{name: "extra summary", run: func(value *SessionView) {
			value.ObservationSummaries = append(value.ObservationSummaries, value.ObservationSummaries[0])
		}},
		{name: "summary revision order mismatch", run: func(value *SessionView) { value.ObservationSummaries[0].RevisionID = revisionDigest("2") }},
		{name: "duplicate stable observation key", run: func(value *SessionView) {
			duplicate := value.ObservationSummaries[0]
			duplicate.RevisionID = revisionDigest("2")
			value.ActiveRevisionIDs = append(value.ActiveRevisionIDs, duplicate.RevisionID)
			value.ObservationSummaries = append(value.ObservationSummaries, duplicate)
		}},
		{name: "noncanonical summary and revision order", run: func(value *SessionView) {
			later := value.ObservationSummaries[0]
			later.RevisionID = revisionDigest("2")
			later.Sequence++
			later.Subject = "go-test-later"
			value.ActiveRevisionIDs = []string{later.RevisionID, value.ActiveRevisionIDs[0]}
			value.ObservationSummaries = []ObservationSummary{later, value.ObservationSummaries[0]}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validSessionView()
			test.run(&value)
			value.Digest = mustSessionViewDigest(value)
			if err := ValidateSessionView(value); err == nil {
				t.Fatalf("invalid SessionView accepted: %+v", value)
			}
		})
	}
}

func TestSessionViewSchemaRejectsMissingOrForbiddenSummaryContent(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"missing usage digest": func(object map[string]any) { delete(object, "usage_record_digest") },
		"missing summaries":    func(object map[string]any) { delete(object, "observation_summaries") },
		"source reference": func(object map[string]any) {
			object["observation_summaries"].([]any)[0].(map[string]any)["source_ref"] = map[string]any{"provider": "codex"}
		},
		"usage totals": func(object map[string]any) {
			object["observation_summaries"].([]any)[0].(map[string]any)["total_tokens"] = float64(10)
		},
		"raw output": func(object map[string]any) {
			object["observation_summaries"].([]any)[0].(map[string]any)["raw_tool_output"] = "complete output"
		},
		"assistant text": func(object map[string]any) {
			object["observation_summaries"].([]any)[0].(map[string]any)["assistant_message"] = "complete message"
		},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(validSessionView())
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(body, &object); err != nil {
				t.Fatal(err)
			}
			mutate(object)
			body, err = json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateJSONSchemaFixture("../../schemas/session-view-v1.schema.json", body); err == nil {
				t.Fatalf("SessionView schema accepted %s: %s", name, body)
			}
		})
	}
}

func TestSessionViewBindsRequiredOpaqueSourceIdentity(t *testing.T) {
	view := validSessionView()
	if err := ValidateSessionView(view); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	if object["source_identity"] != "src1" {
		t.Fatalf("source identity wire value=%v", object["source_identity"])
	}
	for _, forbidden := range []string{"source_path", "source_location", "source_hash"} {
		if object[forbidden] != nil {
			t.Fatalf("SessionView copied forbidden source detail %q: %s", forbidden, body)
		}
	}

	invalid := view
	invalid.SourceIdentity = "/private/session.jsonl"
	invalid.Digest = mustSessionViewDigest(invalid)
	if err := ValidateSessionView(invalid); err == nil {
		t.Fatal("SessionView accepted a path as opaque SourceIdentity")
	}

	delete(object, "source_identity")
	body, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchemaFixture("../../schemas/session-view-v1.schema.json", body); err == nil {
		t.Fatalf("SessionView schema accepted missing source identity: %s", body)
	}
}

func TestSessionViewRejectsImpossibleTerminalAvailabilityAfterDigestRecompute(t *testing.T) {
	for name, mutate := range map[string]func(*SessionView){
		"missing available": func(value *SessionView) {
			value.TerminalState = Missing
			value.SourceAvailability = SourceAvailable
		},
		"indexed unavailable": func(value *SessionView) {
			value.TerminalState = Indexed
			value.SourceAvailability = SourceUnavailable
		},
		"unsupported unavailable": func(value *SessionView) {
			value.TerminalState = Unsupported
			value.SourceAvailability = SourceUnavailable
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := validSessionView()
			mutate(&value)
			value.Digest = mustSessionViewDigest(value)
			if err := ValidateSessionView(value); err == nil {
				t.Fatalf("runtime accepted impossible recomputed pair: %+v", value)
			}
			body, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateJSONSchemaFixture("../../schemas/session-view-v1.schema.json", body); err == nil {
				t.Fatalf("schema accepted impossible recomputed pair: %s", body)
			}
		})
	}
}

func TestSessionViewAcceptsUnavailableUnreadableAndAmbiguousTerminalStates(t *testing.T) {
	for _, state := range []TerminalState{Unreadable, Ambiguous} {
		t.Run(string(state), func(t *testing.T) {
			value := validSessionView()
			value.TerminalState = state
			value.SourceAvailability = SourceUnavailable
			value.Digest = mustSessionViewDigest(value)
			if err := ValidateSessionView(value); err != nil {
				t.Fatalf("runtime rejected unavailable %s SessionView: %v", state, err)
			}
			body, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateJSONSchemaFixture("../../schemas/session-view-v1.schema.json", body); err != nil {
				t.Fatalf("schema rejected unavailable %s SessionView: %v", state, err)
			}
		})
	}
}

func TestProjectProbeStateHasNoWallClockTimeButProbeCheckDoes(t *testing.T) {
	stateType := reflect.TypeOf(ProjectProbeState{})
	if _, ok := stateType.FieldByName("CheckedAt"); ok {
		t.Fatal("content-addressed ProjectProbeState includes wall-clock CheckedAt")
	}
	checkType := reflect.TypeOf(ProbeCheck{})
	if _, ok := checkType.FieldByName("CheckedAt"); !ok {
		t.Fatal("ProbeCheck does not record CheckedAt")
	}
	state := validProbeState()
	if err := ValidateProjectProbeState(state); err != nil {
		t.Fatal(err)
	}
	check := validProbeCheck()
	if err := ValidateProbeCheck(check); err != nil {
		t.Fatal(err)
	}
	check.CheckedAt = "not-a-time"
	if err := ValidateProbeCheck(check); err == nil {
		t.Fatal("ProbeCheck accepted a non-RFC3339Nano time")
	}
}

func TestGenerationManifestReconcilesFrozenSourcesWithSessionViews(t *testing.T) {
	manifest := validGenerationManifest()
	manifest.SessionViews = nil
	if err := ValidateGenerationManifest(manifest); err == nil || !strings.Contains(err.Error(), "SessionView dependency count") {
		t.Fatalf("unmaterialized frozen source accepted or misclassified: %v", err)
	}
}

func TestSessionLineageIsPerSessionBoundedAndManifestResolved(t *testing.T) {
	lineage := validSessionLineage()
	if err := ValidateSessionLineage(lineage); err != nil {
		t.Fatalf("valid lineage rejected: %v", err)
	}
	manifest := validGenerationManifest()
	if err := ValidateGenerationManifest(manifest); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	manifest.SessionLineages[0].SessionID = "other"
	if err := ValidateGenerationManifest(manifest); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("manifest accepted mismatched lineage dependency: %v", err)
	}
}

func TestSessionLineageAndGenerationManifestSchemasAreStrict(t *testing.T) {
	fixtures := []struct {
		name  string
		path  string
		value any
	}{
		{name: "SessionLineage", path: "../../schemas/session-lineage-v1.schema.json", value: validSessionLineage()},
		{name: "GenerationManifest", path: "../../schemas/generation-manifest-v1.schema.json", value: validGenerationManifest()},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			body, err := json.Marshal(fixture.value)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(body, &object); err != nil {
				t.Fatal(err)
			}
			object["full_transcript"] = "forbidden"
			body, err = json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateJSONSchemaFixture(fixture.path, body); err == nil {
				t.Fatalf("%s schema accepted an unknown raw field", fixture.name)
			}
		})
	}
}

func TestGenerationManifestSchemaAllowsUnicodeButRejectsStructuredControlCharacters(t *testing.T) {
	manifest := validGenerationManifest()
	manifest.ProbeCheck.Diagnostics = []Diagnostic{{Code: "probe-note", Path: "模块/文件.md"}}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchemaFixture("../../schemas/generation-manifest-v1.schema.json", body); err != nil {
		t.Fatalf("schema rejected valid Unicode diagnostic path: %v", err)
	}
	manifest.ProbeCheck.Diagnostics[0].Path = "bad\u0001path"
	body, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchemaFixture("../../schemas/generation-manifest-v1.schema.json", body); err == nil {
		t.Fatal("schema accepted a C0 control character in structured text")
	}
}

func TestSessionLineageRejectsOversizedSingleSourceWithoutProjectWideMaps(t *testing.T) {
	lineage := validSessionLineage()
	lineage.ActiveRevisions = make(map[string]string, 65537)
	for index := 0; index < 65537; index++ {
		key := fmt.Sprintf("sha256:%064x", index+1)
		revision := fmt.Sprintf("sha256:%064x", index+65538)
		lineage.ActiveRevisions[key] = revision
	}
	lineage.Digest, _ = SessionLineageDigest(lineage)
	if err := ValidateSessionLineage(lineage); err == nil || !strings.Contains(err.Error(), "65536") {
		t.Fatalf("oversized source lineage accepted: %v", err)
	}
}

func TestDerivedDependenciesMustBelongToEnclosingSelectedEvidence(t *testing.T) {
	inactive := map[string]string{
		"unknown":    revisionDigest("2"),
		"superseded": revisionDigest("3"),
		"withdrawn":  revisionDigest("4"),
	}
	for state, revisionID := range inactive {
		t.Run("SessionView "+state, func(t *testing.T) {
			value := validSessionView()
			value.DerivedRecords[0].DependencyRevisionIDs = []string{revisionID}
			if err := ValidateSessionView(value); err == nil || !strings.Contains(err.Error(), "active revision") {
				t.Fatalf("%s dependency accepted or misclassified: %v", state, err)
			}
		})

		t.Run("ProjectView "+state, func(t *testing.T) {
			value := validProjectView()
			if state == "unknown" {
				value.WitnessedState[0].DependencyRevisionIDs = []string{revisionID}
			} else {
				value.DerivedRecords = []DerivedRecord{validDerivedRecord()}
				value.DerivedRecords[0].ID = "derived-" + state
				value.DerivedRecords[0].DependencyRevisionIDs = []string{revisionID}
			}
			if err := ValidateProjectView(value); err == nil || !strings.Contains(err.Error(), "selected observation") {
				t.Fatalf("%s dependency accepted or misclassified: %v", state, err)
			}
		})
	}
}

func validSourceRecord() SourceRecord {
	return SourceRecord{
		SchemaVersion:  MemorySchemaVersion,
		Provider:       "codex",
		SessionID:      "s1",
		SourceIdentity: "src1",
		StartedAt:      "2026-08-31T10:00:00Z",
		EndedAt:        "2026-08-31T10:00:01Z",
		FrozenBoundary: FrozenBoundary{Location: SourceLocation{Kind: SourceLocationJSONL, JSONL: &JSONLSourceLocation{Line: 9, ByteOffset: 128}}, SourceHash: strings.Repeat("a", 64)},
		Availability:   SourceAvailable,
		Usage: accounting.SessionUsage{
			StartedAt:  "2026-08-31T10:00:00Z",
			EndedAt:    "2026-08-31T10:00:01Z",
			DurationMS: 1000,
			Models: []accounting.ModelUsage{{
				Model: "gpt-5",
				TokenUsage: accounting.TokenUsage{
					InputTokens:  7,
					OutputTokens: 3,
					TotalTokens:  10,
				},
			}},
			TotalTokens: 10,
		},
		ProjectIDs: []string{"project-a"},
	}
}

func validObservationKey() ObservationKey {
	return ObservationKey{Provider: "codex", SessionID: "s1", SourceIdentity: "src1", Sequence: 7, ProjectID: "project-a", Kind: "command", Subject: "go-test"}
}

func validObservation(key ObservationKey, adapterVersion string, fields map[string]string) ObservationRevision {
	value := ObservationRevision{
		SchemaVersion:  MemorySchemaVersion,
		Key:            key,
		Ref:            SourceRef{Provider: key.Provider, SessionID: key.SessionID, SourceIdentity: key.SourceIdentity, Location: SourceLocation{Kind: SourceLocationJSONL, JSONL: &JSONLSourceLocation{Line: 9, ByteOffset: 128}}, SourceHash: strings.Repeat("a", 64)},
		Timestamp:      "2026-08-31T10:00:00.123456789Z",
		Operation:      "go test",
		Object:         "./internal/memory",
		Outcome:        "success",
		Fields:         fields,
		Excerpt:        "ok internal/memory",
		AdapterID:      "codex-jsonl",
		AdapterVersion: adapterVersion,
	}
	value.RevisionID = ObservationRevisionID(value)
	return value
}

func validDerivedRecord() DerivedRecord {
	return DerivedRecord{
		ID:                    "recovery-1",
		Kind:                  "recovery_link",
		Subject:               "go-test",
		OccurredAt:            "2026-08-31T10:00:00.123456789Z",
		DependencyRevisionIDs: []string{revisionDigest("1")},
		RuleID:                "compatible-operation",
		RuleVersion:           "1",
		Fields:                map[string]string{"outcome": "recovered"},
	}
}

func validObservationSummary() ObservationSummary {
	return ObservationSummary{
		RevisionID: revisionDigest("1"),
		Sequence:   7,
		Kind:       "verification",
		Subject:    "go-test",
		OccurredAt: "2026-08-31T10:00:00.123456789Z",
		Operation:  "verification",
		Object:     "package",
		Outcome:    "passed",
		Fields:     map[string]string{"component": "package", "status": "test"},
		Excerpt:    "ok internal/memory",
	}
}

func validSessionView() SessionView {
	value := SessionView{
		SchemaVersion:           MemorySchemaVersion,
		ProjectID:               "project-a",
		Provider:                "codex",
		SessionID:               "s1",
		SourceIdentity:          "src1",
		SourceRecordDigest:      objectDigest("2"),
		UsageRecordDigest:       objectDigest("2"),
		StartedAt:               "2026-08-31T10:00:00Z",
		EndedAt:                 "2026-08-31T10:00:01Z",
		TerminalState:           Indexed,
		SourceAvailability:      SourceAvailable,
		ActiveRevisionIDs:       []string{revisionDigest("1")},
		ObservationSummaries:    []ObservationSummary{validObservationSummary()},
		ObservationChunkDigests: []string{objectDigest("3")},
		DerivedRecords:          []DerivedRecord{validDerivedRecord()},
		Diagnostics:             []Diagnostic{},
		DependencyDigest:        objectDigest("4"),
		MaterializerVersion:     "session-view-v1",
	}
	value.Digest = mustSessionViewDigest(value)
	return value
}

func validProbeState() ProjectProbeState {
	value := ProjectProbeState{
		SchemaVersion:           MemorySchemaVersion,
		ProjectID:               "project-a",
		CanonicalRoot:           "/workspace/project-a",
		Branch:                  "main",
		Head:                    strings.Repeat("b", 40),
		DirtyPathCount:          0,
		RemoteIdentityHashes:    []string{objectDigest("6")},
		VersionFiles:            []ProbeFile{{Path: "VERSION", Exists: true, ContentHash: strings.Repeat("c", 64)}},
		RequiredProjectionFiles: []ProbeFile{{Path: "docs/session-review/项目回顾.md", Exists: false}},
		ProbeVersion:            "project-probe-v1",
		Diagnostics:             []Diagnostic{},
	}
	value.Digest = mustProjectProbeStateDigest(value)
	return value
}

func validProbeCheck() ProbeCheck {
	return ProbeCheck{SchemaVersion: MemorySchemaVersion, CheckedAt: "2026-08-31T10:00:02.123456789Z", StateDigest: objectDigest("5"), Available: true, Diagnostics: []Diagnostic{}}
}

func validProjectView() ProjectView {
	value := ProjectView{
		SchemaVersion:           MemorySchemaVersion,
		ProjectID:               "project-a",
		Generation:              1,
		StartedAt:               "2026-08-31T10:00:00Z",
		EndedAt:                 "2026-08-31T10:00:01Z",
		SourceSessions:          1,
		TerminalCounts:          TerminalCounts{Indexed: 1},
		SessionViewDependencies: []SessionViewDependency{{Provider: "codex", SessionID: "s1", Digest: objectDigest("1")}},
		ObservationRevisionIDs:  []string{revisionDigest("1")},
		ProbeStateDigest:        objectDigest("5"),
		LiveState:               StateSnapshot{Branch: "main", Head: strings.Repeat("b", 40), DirtyPathCount: 0},
		WitnessedState:          []DerivedRecord{validDerivedRecord()},
		DerivedRecords:          []DerivedRecord{},
		AssociatedUsage:         []AssociatedUsage{{Provider: "codex", SessionID: "s1", UsageRecordDigest: objectDigest("2"), Shared: false}},
		DependencyDigest:        objectDigest("8"),
		ReducerVersion:          "project-view-v1",
	}
	value.Digest = mustProjectViewDigest(value)
	return value
}

func mustSessionViewDigest(value SessionView) string {
	digest, err := SessionViewDigest(value)
	if err != nil {
		panic(err)
	}
	return digest
}

func mustProjectProbeStateDigest(value ProjectProbeState) string {
	digest, err := ProjectProbeStateDigest(value)
	if err != nil {
		panic(err)
	}
	return digest
}

func mustProjectViewDigest(value ProjectView) string {
	digest, err := ProjectViewDigest(value)
	if err != nil {
		panic(err)
	}
	return digest
}

func validGenerationManifest() GenerationManifest {
	return GenerationManifest{
		SchemaVersion:       MemorySchemaVersion,
		GenerationID:        "generation-1",
		ProjectID:           "project-a",
		CreatedAt:           "2026-08-31T10:00:03Z",
		SourceRecordDigests: []string{objectDigest("2")},
		SessionViews:        []SessionViewDependency{{Provider: "codex", SessionID: "s1", Digest: objectDigest("1")}},
		SessionLineages:     []SessionLineageDependency{{Provider: "codex", SessionID: "s1", Digest: objectDigest("9")}},
		ProbeStateDigest:    objectDigest("5"),
		ProbeCheck:          validProbeCheck(),
		ProjectViewDigest:   objectDigest("7"),
	}
}

func validSessionLineage() SessionLineage {
	value := SessionLineage{
		SchemaVersion:       MemorySchemaVersion,
		ProjectID:           "project-a",
		Provider:            "codex",
		SessionID:           "s1",
		SourceIdentity:      "src1",
		ActiveRevisions:     map[string]string{stableKeyDigest("1"): revisionDigest("1")},
		SupersededRevisions: map[string]string{},
		WithdrawnRevisions:  map[string]string{},
	}
	value.Digest, _ = SessionLineageDigest(value)
	return value
}

func objectDigest(seed string) string {
	return "sha256:" + strings.Repeat(seed, 64)
}

func revisionDigest(seed string) string {
	return objectDigest(seed)
}

func stableKeyDigest(seed string) string {
	return objectDigest(seed)
}

func assertSchemaRejectsUnknownProperty(t *testing.T, path, property string) {
	t.Helper()
	fixture := validObservation(validObservationKey(), "adapter-1", map[string]string{"exit_code": "0"})
	body, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	object[property] = "forbidden"
	body, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchemaFixture(path, body); err == nil || !strings.Contains(err.Error(), "unknown property") {
		t.Fatalf("schema accepted unknown property %q or misclassified it: %v", property, err)
	}
}

func assertObservationSchemaRejectsMutation(t *testing.T, mutate func(map[string]any)) {
	t.Helper()
	fixture := validObservation(validObservationKey(), "adapter-1", map[string]string{"exit_code": "0"})
	body, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	mutate(object)
	body, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchemaFixture("../../schemas/observation-v1.schema.json", body); err == nil {
		t.Fatalf("observation schema accepted mutated payload: %s", body)
	}
}

func validateJSONSchemaFixture(path string, body []byte) error {
	schemaBody, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var schema map[string]any
	var value any
	if err := json.Unmarshal(schemaBody, &schema); err != nil {
		return err
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		return fmt.Errorf("schema is not draft 2020-12")
	}
	return validateSchemaValue(schema, schema, value, "$")
}

func validateSchemaValue(root, schema map[string]any, value any, path string) error {
	if ref, _ := schema["$ref"].(string); ref != "" {
		const prefix = "#/$defs/"
		if !strings.HasPrefix(ref, prefix) {
			return fmt.Errorf("%s unsupported ref %q", path, ref)
		}
		defs, _ := root["$defs"].(map[string]any)
		target, _ := defs[strings.TrimPrefix(ref, prefix)].(map[string]any)
		if target == nil {
			return fmt.Errorf("%s missing ref %q", path, ref)
		}
		return validateSchemaValue(root, target, value, path)
	}
	for _, raw := range schemaArray(schema["allOf"]) {
		branch, _ := raw.(map[string]any)
		if branch == nil {
			return fmt.Errorf("%s invalid allOf branch", path)
		}
		condition, conditional := branch["if"].(map[string]any)
		if !conditional {
			if err := validateSchemaValue(root, branch, value, path); err != nil {
				return err
			}
			continue
		}
		matched, err := schemaConditionMatches(root, condition, value)
		if err != nil {
			return fmt.Errorf("%s invalid if condition: %w", path, err)
		}
		targetName := "else"
		if matched {
			targetName = "then"
		}
		if target, ok := branch[targetName].(map[string]any); ok {
			accepted, err := schemaConditionMatches(root, target, value)
			if err != nil {
				return fmt.Errorf("%s invalid %s condition: %w", path, targetName, err)
			}
			if !accepted {
				return fmt.Errorf("%s violates %s condition", path, targetName)
			}
		}
	}
	if constant, ok := schema["const"]; ok && !reflect.DeepEqual(constant, value) {
		return fmt.Errorf("%s is not exact const %v", path, constant)
	}
	if values, ok := schema["enum"].([]any); ok {
		found := false
		for _, candidate := range values {
			found = found || reflect.DeepEqual(candidate, value)
		}
		if !found {
			return fmt.Errorf("%s is outside enum", path)
		}
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not object", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		patternProperties, _ := schema["patternProperties"].(map[string]any)
		if schema["additionalProperties"] != false {
			return fmt.Errorf("%s object schema is open", path)
		}
		for name := range object {
			if _, ok := properties[name]; !ok && matchingPatternSchema(patternProperties, name) == nil {
				return fmt.Errorf("%s unknown property %q", path, name)
			}
		}
		for _, raw := range schemaArray(schema["required"]) {
			name, _ := raw.(string)
			if _, ok := object[name]; !ok {
				return fmt.Errorf("%s missing required property %q", path, name)
			}
		}
		for name, child := range object {
			childSchema, _ := properties[name].(map[string]any)
			if childSchema == nil {
				childSchema = matchingPatternSchema(patternProperties, name)
			}
			if childSchema == nil {
				return fmt.Errorf("%s.%s schema missing", path, name)
			}
			if err := validateSchemaValue(root, childSchema, child, path+"."+name); err != nil {
				return err
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s is not array", path)
		}
		if minimum, ok := schema["minItems"].(float64); ok && len(array) < int(minimum) {
			return fmt.Errorf("%s has too few items", path)
		}
		if maximum, ok := schema["maxItems"].(float64); ok && len(array) > int(maximum) {
			return fmt.Errorf("%s has too many items", path)
		}
		itemSchema, _ := schema["items"].(map[string]any)
		seen := make(map[string]struct{}, len(array))
		for index, item := range array {
			if err := validateSchemaValue(root, itemSchema, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			if schema["uniqueItems"] == true {
				encoded, _ := json.Marshal(item)
				key := string(encoded)
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("%s contains duplicate item", path)
				}
				seen[key] = struct{}{}
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s is not string", path)
		}
		if minimum, ok := schema["minLength"].(float64); ok && len([]rune(text)) < int(minimum) {
			return fmt.Errorf("%s is too short", path)
		}
		if maximum, ok := schema["maxLength"].(float64); ok && len([]rune(text)) > int(maximum) {
			return fmt.Errorf("%s is too long", path)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			matched, err := regexp.MatchString(pattern, text)
			if err != nil || !matched {
				return fmt.Errorf("%s does not match pattern", path)
			}
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || math.Trunc(number) != number {
			return fmt.Errorf("%s is not integer", path)
		}
		if minimum, ok := schema["minimum"].(float64); ok && number < minimum {
			return fmt.Errorf("%s is below minimum", path)
		}
		if maximum, ok := schema["maximum"].(float64); ok && number > maximum {
			return fmt.Errorf("%s exceeds maximum", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s is not boolean", path)
		}
	case nil:
		return fmt.Errorf("%s schema type missing", path)
	default:
		return fmt.Errorf("%s unsupported schema type %v", path, schema["type"])
	}
	return nil
}

func schemaConditionMatches(root, schema map[string]any, value any) (bool, error) {
	if ref, _ := schema["$ref"].(string); ref != "" {
		const prefix = "#/$defs/"
		if !strings.HasPrefix(ref, prefix) {
			return false, fmt.Errorf("unsupported ref %q", ref)
		}
		defs, _ := root["$defs"].(map[string]any)
		target, _ := defs[strings.TrimPrefix(ref, prefix)].(map[string]any)
		if target == nil {
			return false, fmt.Errorf("missing ref %q", ref)
		}
		return schemaConditionMatches(root, target, value)
	}
	if constant, ok := schema["const"]; ok && !reflect.DeepEqual(constant, value) {
		return false, nil
	}
	if values, ok := schema["enum"].([]any); ok {
		for _, candidate := range values {
			if reflect.DeepEqual(candidate, value) {
				return true, nil
			}
		}
		return false, nil
	}
	if required := schemaArray(schema["required"]); len(required) != 0 {
		object, ok := value.(map[string]any)
		if !ok {
			return false, nil
		}
		for _, raw := range required {
			name, _ := raw.(string)
			if _, exists := object[name]; !exists {
				return false, nil
			}
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		object, ok := value.(map[string]any)
		if !ok {
			return false, nil
		}
		for name, raw := range properties {
			child, exists := object[name]
			if !exists {
				continue
			}
			childSchema, _ := raw.(map[string]any)
			if childSchema == nil {
				return false, fmt.Errorf("property %q condition is not an object", name)
			}
			matched, err := schemaConditionMatches(root, childSchema, child)
			if err != nil || !matched {
				return false, err
			}
		}
	}
	return true, nil
}

func schemaArray(value any) []any {
	items, _ := value.([]any)
	return items
}

func matchingPatternSchema(patterns map[string]any, name string) map[string]any {
	for pattern, raw := range patterns {
		if matched, err := regexp.MatchString(pattern, name); err == nil && matched {
			schema, _ := raw.(map[string]any)
			return schema
		}
	}
	return nil
}
