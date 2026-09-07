package memory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProviderNeutralRuntimeContractsAcceptSafeProviderIDs(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "opencode", "custom-provider", strings.Repeat("a", 128)} {
		t.Run(provider, func(t *testing.T) {
			for _, contract := range providerContractFixtures(provider) {
				if err := contract.validate(); err != nil {
					t.Fatalf("%s rejected generic provider %q: %v", contract.name, provider, err)
				}
			}
		})
	}
}

func TestProviderNeutralSchemasAcceptSafeProviderIDs(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "opencode", "custom-provider", strings.Repeat("a", 128)} {
		t.Run(provider, func(t *testing.T) {
			for _, contract := range providerContractFixtures(provider) {
				body, err := json.Marshal(contract.value)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateJSONSchemaFixture(contract.schema, body); err != nil {
					t.Fatalf("%s schema rejected generic provider %q: %v", contract.name, provider, err)
				}
			}
		})
	}
}

func TestProviderContractGenerationManifestFixtureIncludesProviderMeasurement(t *testing.T) {
	manifest := providerManifest("claude")
	if len(manifest.SessionIndexMeasurements) != 1 {
		t.Fatalf("provider manifest has %d Session index measurements, want 1", len(manifest.SessionIndexMeasurements))
	}
	measurement := manifest.SessionIndexMeasurements[0]
	if measurement.Provider != "claude" || measurement.SessionID != manifest.SessionViews[0].SessionID {
		t.Fatalf("measurement identity=%s/%s want %s/%s", measurement.Provider, measurement.SessionID, manifest.SessionViews[0].Provider, manifest.SessionViews[0].SessionID)
	}
}

func TestProviderContractGenerationMeasurementRejectsMalformedAndMismatchedIdentity(t *testing.T) {
	t.Run("malformed provider", func(t *testing.T) {
		manifest := providerManifest("claude")
		manifest.SessionIndexMeasurements[0].Provider = "Claude"
		if err := ValidateGenerationManifest(manifest); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("runtime accepted malformed measurement provider or misclassified it: %v", err)
		}
		body, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateJSONSchemaFixture("../../schemas/generation-manifest-v1.schema.json", body); err == nil {
			t.Fatal("schema accepted malformed measurement provider")
		}
	})

	t.Run("safe provider mismatches SessionView", func(t *testing.T) {
		manifest := providerManifest("claude")
		manifest.SessionIndexMeasurements[0].Provider = "opencode"
		if err := ValidateGenerationManifest(manifest); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("runtime accepted mismatched measurement provider or misclassified it: %v", err)
		}
		body, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateJSONSchemaFixture("../../schemas/generation-manifest-v1.schema.json", body); err != nil {
			t.Fatalf("schema tried to enforce cross-field provider equality: %v", err)
		}
	})
}

func TestProviderContractRejectsUnsafeProviderIDs(t *testing.T) {
	invalid := map[string]string{
		"empty":          "",
		"uppercase":      "Codex",
		"slash":          "custom/provider",
		"backslash":      `custom\provider`,
		"traversal":      "../codex",
		"whitespace":     "custom provider",
		"non-ascii":      "编码",
		"129-byte value": strings.Repeat("a", 129),
		"NUL":            "custom\x00provider",
	}
	for name, provider := range invalid {
		t.Run(name, func(t *testing.T) {
			for _, contract := range providerContractFixtures(provider) {
				if err := contract.validate(); err == nil {
					t.Errorf("%s runtime accepted unsafe provider %q", contract.name, provider)
				}
				body, err := json.Marshal(contract.value)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateJSONSchemaFixture(contract.schema, body); err == nil {
					t.Errorf("%s schema accepted unsafe provider %q", contract.name, provider)
				}
			}
		})
	}
}

func TestProviderContractPreservesCrossFieldIdentityChecks(t *testing.T) {
	t.Run("observation key and reference", func(t *testing.T) {
		value := providerObservation("claude")
		value.Ref.Provider = "opencode"
		value.RevisionID = ObservationRevisionID(value)
		if err := ValidateObservationRevision(value); err == nil || !strings.Contains(err.Error(), "disagree") {
			t.Fatalf("mismatched observation provider accepted or misclassified: %v", err)
		}
	})

	t.Run("lineage and manifest provider", func(t *testing.T) {
		value := providerManifest("claude")
		value.SessionLineages[0].Provider = "opencode"
		if err := ValidateGenerationManifest(value); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("mismatched lineage provider accepted or misclassified: %v", err)
		}
	})

	t.Run("forged observation revision digest", func(t *testing.T) {
		value := providerObservation("claude")
		value.RevisionID = revisionDigest("f")
		if err := ValidateObservationRevision(value); err == nil || !strings.Contains(err.Error(), "revision_id") {
			t.Fatalf("forged revision digest accepted or misclassified: %v", err)
		}
	})
}

func TestProviderContractNamespacesSessionDependenciesAndUsage(t *testing.T) {
	view := validProjectView()
	view.SourceSessions = 2
	view.TerminalCounts.Indexed = 2
	view.SessionViewDependencies = []SessionViewDependency{
		{Provider: "codex", SessionID: "same-native-id", Digest: objectDigest("1")},
		{Provider: "claude", SessionID: "same-native-id", Digest: objectDigest("2")},
	}
	view.AssociatedUsage = []AssociatedUsage{
		{Provider: "codex", SessionID: "same-native-id", UsageRecordDigest: objectDigest("3")},
		{Provider: "claude", SessionID: "same-native-id", UsageRecordDigest: objectDigest("4")},
	}
	view.Digest = mustProjectViewDigest(view)
	if err := ValidateProjectView(view); err != nil {
		t.Fatalf("different-provider native IDs did not remain distinct: %v", err)
	}

	duplicate := view
	duplicate.SessionViewDependencies = append([]SessionViewDependency(nil), view.SessionViewDependencies...)
	duplicate.SessionViewDependencies[1].Provider = "codex"
	duplicate.Digest = mustProjectViewDigest(duplicate)
	if err := ValidateProjectView(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("same-provider duplicate dependency accepted or misclassified: %v", err)
	}

	duplicate = view
	duplicate.AssociatedUsage = append([]AssociatedUsage(nil), view.AssociatedUsage...)
	duplicate.AssociatedUsage[1].Provider = "codex"
	duplicate.Digest = mustProjectViewDigest(duplicate)
	if err := ValidateProjectView(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("same-provider duplicate usage accepted or misclassified: %v", err)
	}

	manifest := validGenerationManifest()
	manifest.SourceRecordDigests = []string{objectDigest("1"), objectDigest("2")}
	manifest.SessionViews = []SessionViewDependency{
		{Provider: "codex", SessionID: "same-native-id", Digest: objectDigest("3")},
		{Provider: "claude", SessionID: "same-native-id", Digest: objectDigest("4")},
	}
	manifest.SessionLineages = []SessionLineageDependency{
		{Provider: "codex", SessionID: "same-native-id", Digest: objectDigest("5")},
		{Provider: "claude", SessionID: "same-native-id", Digest: objectDigest("6")},
	}
	if err := ValidateGenerationManifest(manifest); err != nil {
		t.Fatalf("manifest rejected namespaced same native IDs: %v", err)
	}
	manifest.SessionViews[1].Provider = "codex"
	if err := ValidateGenerationManifest(manifest); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("manifest accepted same-provider duplicate dependency: %v", err)
	}
}

type providerContractFixture struct {
	name     string
	schema   string
	value    any
	validate func() error
}

func providerContractFixtures(provider string) []providerContractFixture {
	source := validSourceRecord()
	source.Provider = provider
	observation := providerObservation(provider)
	session := providerSessionView(provider)
	lineage := providerSessionLineage(provider)
	project := providerProjectView(provider)
	manifest := providerManifest(provider)
	return []providerContractFixture{
		{name: "source record", schema: "../../schemas/source-catalog-v1.schema.json", value: source, validate: func() error { return ValidateSourceRecord(source) }},
		{name: "observation", schema: "../../schemas/observation-v1.schema.json", value: observation, validate: func() error { return ValidateObservationRevision(observation) }},
		{name: "SessionView", schema: "../../schemas/session-view-v1.schema.json", value: session, validate: func() error { return ValidateSessionView(session) }},
		{name: "SessionLineage", schema: "../../schemas/session-lineage-v1.schema.json", value: lineage, validate: func() error { return ValidateSessionLineage(lineage) }},
		{name: "ProjectView", schema: "../../schemas/project-view-v1.schema.json", value: project, validate: func() error { return ValidateProjectView(project) }},
		{name: "GenerationManifest", schema: "../../schemas/generation-manifest-v1.schema.json", value: manifest, validate: func() error { return ValidateGenerationManifest(manifest) }},
	}
}

func providerObservation(provider string) ObservationRevision {
	key := validObservationKey()
	key.Provider = provider
	return validObservation(key, "adapter-1", map[string]string{"exit_code": "0"})
}

func providerSessionView(provider string) SessionView {
	value := validSessionView()
	value.Provider = provider
	value.Digest = mustSessionViewDigest(value)
	return value
}

func providerSessionLineage(provider string) SessionLineage {
	value := validSessionLineage()
	value.Provider = provider
	value.Digest, _ = SessionLineageDigest(value)
	return value
}

func providerProjectView(provider string) ProjectView {
	value := validProjectView()
	value.SessionViewDependencies[0].Provider = provider
	value.AssociatedUsage[0].Provider = provider
	value.Digest = mustProjectViewDigest(value)
	return value
}

func providerManifest(provider string) GenerationManifest {
	value := validGenerationManifest()
	value.SessionViews[0].Provider = provider
	value.SessionLineages[0].Provider = provider
	recordCount := uint64(1)
	value.SessionIndexMeasurements = []SessionIndexMeasurement{{
		Provider: provider, SessionID: value.SessionViews[0].SessionID, RecordCount: &recordCount,
		Seen: 1, Indexed: 1,
	}}
	return value
}
