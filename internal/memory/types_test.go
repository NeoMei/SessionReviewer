package memory

import (
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

func TestPrivateWireSchemasAcceptMatchingVersionOneFixtures(t *testing.T) {
	fixtures := []struct {
		name  string
		path  string
		value any
	}{
		{name: "source catalog", path: "../../schemas/source-catalog-v1.schema.json", value: validSourceRecord()},
		{name: "observation", path: "../../schemas/observation-v1.schema.json", value: validObservation(validObservationKey(), "adapter-1", map[string]string{"exit_code": "0"})},
		{name: "session view", path: "../../schemas/session-view-v1.schema.json", value: validSessionView()},
		{name: "project view", path: "../../schemas/project-view-v1.schema.json", value: validProjectView()},
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
			want: "semantic",
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

func TestGenerationManifestRejectsInactiveRevisionSelectedAsActive(t *testing.T) {
	manifest := validGenerationManifest()
	if err := ValidateGenerationManifest(manifest); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*GenerationManifest){
		func(value *GenerationManifest) { value.SupersededRevisions[revisionDigest("1")] = revisionDigest("2") },
		func(value *GenerationManifest) { value.WithdrawnRevisions[stableKeyDigest("1")] = revisionDigest("1") },
	} {
		value := validGenerationManifest()
		mutate(&value)
		if err := ValidateGenerationManifest(value); err == nil || !strings.Contains(err.Error(), "inactive") {
			t.Fatalf("inactive active revision accepted or misclassified: %v", err)
		}
	}
}

func TestGenerationManifestReconcilesFrozenSourcesWithSessionViews(t *testing.T) {
	manifest := validGenerationManifest()
	manifest.SessionViews = nil
	if err := ValidateGenerationManifest(manifest); err == nil || !strings.Contains(err.Error(), "SessionView dependency count") {
		t.Fatalf("unmaterialized frozen source accepted or misclassified: %v", err)
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
		FrozenBoundary: FrozenBoundary{JSONLLine: 9, ByteOffset: 128, SourceHash: strings.Repeat("a", 64)},
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
		Ref:            SourceRef{Provider: key.Provider, SessionID: key.SessionID, SourceIdentity: key.SourceIdentity, JSONLLine: 9, ByteOffset: 128, SourceHash: strings.Repeat("a", 64)},
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

func validSessionView() SessionView {
	return SessionView{
		SchemaVersion:           MemorySchemaVersion,
		Digest:                  objectDigest("1"),
		ProjectID:               "project-a",
		Provider:                "codex",
		SessionID:               "s1",
		SourceRecordDigest:      objectDigest("2"),
		StartedAt:               "2026-08-31T10:00:00Z",
		EndedAt:                 "2026-08-31T10:00:01Z",
		TerminalState:           Indexed,
		SourceAvailability:      SourceAvailable,
		ActiveRevisionIDs:       []string{revisionDigest("1")},
		ObservationChunkDigests: []string{objectDigest("3")},
		DerivedRecords:          []DerivedRecord{validDerivedRecord()},
		Diagnostics:             []Diagnostic{},
		DependencyDigest:        objectDigest("4"),
		MaterializerVersion:     "session-view-v1",
	}
}

func validProbeState() ProjectProbeState {
	return ProjectProbeState{
		SchemaVersion:           MemorySchemaVersion,
		Digest:                  objectDigest("5"),
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
}

func validProbeCheck() ProbeCheck {
	return ProbeCheck{SchemaVersion: MemorySchemaVersion, CheckedAt: "2026-08-31T10:00:02.123456789Z", StateDigest: objectDigest("5"), Available: true, Diagnostics: []Diagnostic{}}
}

func validProjectView() ProjectView {
	return ProjectView{
		SchemaVersion:           MemorySchemaVersion,
		Digest:                  objectDigest("7"),
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
		AssociatedUsage:         []AssociatedUsage{{Provider: "codex", SessionID: "s1", UsageRecordDigest: objectDigest("2"), Shared: false, TotalTokens: 10}},
		DependencyDigest:        objectDigest("8"),
		ReducerVersion:          "project-view-v1",
	}
}

func validGenerationManifest() GenerationManifest {
	return GenerationManifest{
		SchemaVersion:           MemorySchemaVersion,
		GenerationID:            "generation-1",
		ProjectID:               "project-a",
		CreatedAt:               "2026-08-31T10:00:03Z",
		SourceRecordDigests:     []string{objectDigest("2")},
		ObservationChunkDigests: []string{objectDigest("3")},
		SessionViews:            []SessionViewDependency{{Provider: "codex", SessionID: "s1", Digest: objectDigest("1")}},
		ProbeStateDigest:        objectDigest("5"),
		ProbeCheck:              validProbeCheck(),
		ProjectViewDigest:       objectDigest("7"),
		ActiveRevisions:         map[string]string{stableKeyDigest("1"): revisionDigest("1")},
		SupersededRevisions:     map[string]string{},
		WithdrawnRevisions:      map[string]string{},
	}
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
