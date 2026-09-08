package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestGenerationManifestConversationChainFieldsAreOptionalAndValidated(t *testing.T) {
	legacy := validGenerationManifest()
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "conversation_chain") {
		t.Fatalf("absent extension changed legacy manifest bytes: %s", body)
	}

	current := ConversationChainDependency{
		Provider: "codex", SessionID: "s1", SessionViewDigest: legacy.SessionViews[0].Digest, Digest: objectDigest("a"),
	}
	legacy.ConversationChains = []ConversationChainDependency{current}
	if err := ValidateGenerationManifest(legacy); err != nil {
		t.Fatalf("valid current chain dependency rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*GenerationManifest)
	}{
		{name: "unsafe provider", mutate: func(value *GenerationManifest) { value.ConversationChains[0].Provider = "Codex" }},
		{name: "foreign current view", mutate: func(value *GenerationManifest) { value.ConversationChains[0].SessionViewDigest = objectDigest("b") }},
		{name: "duplicate current identity", mutate: func(value *GenerationManifest) {
			value.ConversationChains = append(value.ConversationChains, ConversationChainDependency{Provider: "codex", SessionID: "s1", SessionViewDigest: value.SessionViews[0].Digest, Digest: objectDigest("b")})
		}},
		{name: "same tuple across sets", mutate: func(value *GenerationManifest) {
			value.RetainedConversationChains = append(value.RetainedConversationChains, value.ConversationChains[0])
		}},
		{name: "conflicting digest binding", mutate: func(value *GenerationManifest) {
			value.RetainedConversationChains = append(value.RetainedConversationChains, ConversationChainDependency{Provider: "codex", SessionID: "s1", SessionViewDigest: objectDigest("b"), Digest: value.ConversationChains[0].Digest})
		}},
		{name: "historical identity absent", mutate: func(value *GenerationManifest) {
			value.RetainedConversationChains = []ConversationChainDependency{{Provider: "codex", SessionID: "gone", SessionViewDigest: objectDigest("b"), Digest: objectDigest("c")}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := legacy
			value.ConversationChains = append([]ConversationChainDependency(nil), legacy.ConversationChains...)
			value.RetainedConversationChains = append([]ConversationChainDependency(nil), legacy.RetainedConversationChains...)
			test.mutate(&value)
			if err := ValidateGenerationManifest(value); err == nil {
				t.Fatal("invalid chain dependency accepted")
			}
		})
	}
}

func TestGenerationManifestAllowsDistinctHistoricalChainViewsAndBoundsCombinedDependencies(t *testing.T) {
	manifest := validGenerationManifest()
	manifest.RetainedConversationChains = []ConversationChainDependency{
		{Provider: "codex", SessionID: "s1", SessionViewDigest: objectDigest("a"), Digest: objectDigest("b")},
		{Provider: "codex", SessionID: "s1", SessionViewDigest: objectDigest("c"), Digest: objectDigest("d")},
	}
	if err := ValidateGenerationManifest(manifest); err != nil {
		t.Fatalf("distinct retained view roots rejected: %v", err)
	}
	manifest.RetainedConversationChains = make([]ConversationChainDependency, 65537)
	for index := range manifest.RetainedConversationChains {
		manifest.RetainedConversationChains[index] = ConversationChainDependency{
			Provider: "codex", SessionID: "s1",
			SessionViewDigest: fmt.Sprintf("sha256:%064x", index+1),
			Digest:            fmt.Sprintf("sha256:%064x", index+65538),
		}
	}
	if err := ValidateGenerationManifest(manifest); err == nil || !strings.Contains(err.Error(), "65536") {
		t.Fatalf("65,537 chain dependencies accepted or misclassified: %v", err)
	}
}

func TestGenerationManifestSchemaAcceptsConversationChainDependencies(t *testing.T) {
	manifest := validGenerationManifest()
	manifest.ConversationChains = []ConversationChainDependency{{Provider: "codex", SessionID: "s1", SessionViewDigest: manifest.SessionViews[0].Digest, Digest: objectDigest("a")}}
	manifest.RetainedConversationChains = []ConversationChainDependency{{Provider: "codex", SessionID: "s1", SessionViewDigest: objectDigest("b"), Digest: objectDigest("c")}}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchemaFixture("../../schemas/generation-manifest-v1.schema.json", body); err != nil {
		t.Fatal(err)
	}
}
