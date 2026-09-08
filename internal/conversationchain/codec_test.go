package conversationchain

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestParseFrozenConversationChainFixtures(t *testing.T) {
	valid, err := os.ReadFile("../../testdata/contracts/v4/conversation-chain-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(valid); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	proofFixture, err := os.ReadFile("../../testdata/contracts/v4/conversation-chain-v1.proof.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	proofDocument, err := Parse(proofFixture)
	if err != nil {
		var raw Document
		if decodeErr := json.Unmarshal(proofFixture, &raw); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		t.Fatalf("valid proof fixture rejected: %v (proof digest %s)", err, dependencyProofDigest(*raw.DependencyProofV1))
	}
	if proofDocument.DependencyProofV1 == nil || dependencyProofDigest(*proofDocument.DependencyProofV1) != proofDocument.DependencyDigest {
		t.Fatal("valid proof fixture lost its dependency preimage")
	}
	invalid, err := os.ReadFile("../../testdata/contracts/v4/conversation-chain-v1.invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(invalid); err == nil {
		t.Fatal("hidden message role was accepted")
	} else if got := strictjson.CodeOf(err); got != "wire_contract_invalid" {
		t.Fatalf("rejection code = %q, want wire_contract_invalid: %v", got, err)
	}
}

func TestLegacyConversationChainWithoutDependencyProofPreservesFrozenBytes(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/contracts/v4/conversation-chain-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if document.DependencyProofV1 != nil {
		t.Fatal("legacy fixture unexpectedly acquired a dependency proof")
	}
	if got := CanonicalDigest(document); got != "sha256:6b047065af598dc399c64ef25f7fad0979665cfbe1976877225fc3e85bbf04a0" {
		t.Fatalf("optional dependency proof changed legacy canonical digest: %s", got)
	}
	rendered, err := Render(document)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), "dependency_proof_v1") {
		t.Fatal("legacy document rendered a previously absent dependency proof")
	}
}

func TestConversationChainDependencyProofIsStrictlyBounded(t *testing.T) {
	document := frozenChain()
	document.DependencyProofV1 = &DependencyProofV1{
		SessionViewDigest:  document.SessionViewDigest,
		SourceRecordDigest: "sha256:" + strings.Repeat("4", 64),
		VisibleRecords:     []DependencyRecordProofV1{{RecordOrdinal: 7, SourceHash: strings.Repeat("3", 64)}},
		ActiveRevisionIDs:  []string{}, RuleVersion: document.SegmentationRuleVersion, RedactionVersion: "redaction-v1",
	}
	document.DependencyDigest = dependencyProofDigest(*document.DependencyProofV1)
	if _, err := Render(document); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	tests := []struct {
		name string
		edit func(*DependencyProofV1)
	}{
		{"zero visible ordinal", func(proof *DependencyProofV1) { proof.VisibleRecords[0].RecordOrdinal = 0 }},
		{"unsafe visible ordinal", func(proof *DependencyProofV1) { proof.VisibleRecords[0].RecordOrdinal = MaxWireInteger + 1 }},
		{"duplicate visible ordinal", func(proof *DependencyProofV1) {
			proof.VisibleRecords = append(proof.VisibleRecords, proof.VisibleRecords[0])
		}},
		{"unordered visible ordinal", func(proof *DependencyProofV1) {
			proof.VisibleRecords = append([]DependencyRecordProofV1{{RecordOrdinal: 8, SourceHash: strings.Repeat("5", 64)}}, proof.VisibleRecords...)
		}},
		{"malformed visible hash", func(proof *DependencyProofV1) { proof.VisibleRecords[0].SourceHash = "bad" }},
		{"duplicate active revision", func(proof *DependencyProofV1) {
			proof.ActiveRevisionIDs = []string{"sha256:" + strings.Repeat("6", 64), "sha256:" + strings.Repeat("6", 64)}
		}},
		{"unordered active revisions", func(proof *DependencyProofV1) {
			proof.ActiveRevisionIDs = []string{"sha256:" + strings.Repeat("7", 64), "sha256:" + strings.Repeat("6", 64)}
		}},
		{"malformed version", func(proof *DependencyProofV1) { proof.RedactionVersion = "bad version" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := document
			proof := *document.DependencyProofV1
			proof.VisibleRecords = append([]DependencyRecordProofV1(nil), document.DependencyProofV1.VisibleRecords...)
			proof.ActiveRevisionIDs = append([]string(nil), document.DependencyProofV1.ActiveRevisionIDs...)
			changed.DependencyProofV1 = &proof
			test.edit(&proof)
			changed.DependencyDigest = dependencyProofDigest(proof)
			if _, err := Render(changed); err == nil {
				t.Fatal("invalid dependency proof accepted")
			}
		})
	}

	t.Run("visible record ceiling", func(t *testing.T) {
		changed := document
		proof := *document.DependencyProofV1
		proof.VisibleRecords = make([]DependencyRecordProofV1, 100001)
		for index := range proof.VisibleRecords {
			proof.VisibleRecords[index] = DependencyRecordProofV1{RecordOrdinal: uint64(index + 1), SourceHash: strings.Repeat("3", 64)}
		}
		changed.DependencyProofV1 = &proof
		changed.DependencyDigest = dependencyProofDigest(proof)
		if err := Validate(changed); err == nil {
			t.Fatal("dependency proof exceeded visible-record ceiling")
		}
	})

	t.Run("active revision ceiling", func(t *testing.T) {
		changed := document
		proof := *document.DependencyProofV1
		proof.ActiveRevisionIDs = make([]string, 65537)
		for index := range proof.ActiveRevisionIDs {
			proof.ActiveRevisionIDs[index] = fmt.Sprintf("sha256:%064x", index)
		}
		changed.DependencyProofV1 = &proof
		changed.DependencyDigest = dependencyProofDigest(proof)
		if err := Validate(changed); err == nil {
			t.Fatal("dependency proof exceeded active-revision ceiling")
		}
	})
}

func TestConversationChainCodecRejectsDocumentLocalDependencyProofMismatchAfterCanonicalRehash(t *testing.T) {
	document, _, err := Materialize(MaterializeInput{
		View: retainedView("codex", "session-1", "source-1", nil),
		Messages: []SourceMessage{
			retainedMessage(RoleUser, "", "question", testTime1, 1, 'a'),
			retainedMessage(RoleAssistant, "final_answer", "answer", testTime2, 2, 'b'),
		},
		SourceCoverage: completeVisibleCoverage(2, 2), RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name string
		edit func(*Document)
	}{
		{"proof view differs from document", func(doc *Document) { doc.DependencyProofV1.SessionViewDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"proof rule differs from document", func(doc *Document) { doc.DependencyProofV1.RuleVersion = "visible-turn-v2" }},
		{"user record absent from proof", func(doc *Document) { doc.DependencyProofV1.VisibleRecords = doc.DependencyProofV1.VisibleRecords[1:] }},
		{"assistant hash differs from proof", func(doc *Document) {
			doc.TurnUnits[0].AssistantMessages[0].SourceRef.SourceHash = strings.Repeat("f", 64)
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneDocumentWithProof(document)
			changed.TurnUnits[0].AssistantMessages = append([]Message(nil), document.TurnUnits[0].AssistantMessages...)
			test.edit(&changed)
			changed.DependencyDigest = dependencyProofDigest(*changed.DependencyProofV1)
			changed.Digest = CanonicalDigest(changed)
			if _, err := Render(changed); err == nil {
				t.Fatal("document-local dependency proof mismatch rendered")
			}
		})
	}
}

func TestRenderConversationChainNormalizesCollectionsAndBindsDigest(t *testing.T) {
	document := frozenChain()
	document.TurnUnits[0].AssistantMessages = nil
	document.TurnUnits[0].Actions = nil
	document.TurnUnits[0].Results = nil
	rendered, err := Render(document)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(rendered, &raw); err != nil {
		t.Fatal(err)
	}
	turn := raw["turn_units"].([]any)[0].(map[string]any)
	for _, key := range []string{"assistant_messages", "actions", "results"} {
		if _, ok := turn[key].([]any); !ok {
			t.Fatalf("%s did not render as an array: %#v", key, turn[key])
		}
	}
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatal(err)
	}
	parsed.SessionViewDigest = "sha256:" + strings.Repeat("9", 64)
	tampered, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(tampered); err == nil {
		t.Fatal("accepted a chain whose exact session dependency no longer matches its digest")
	}
}

func TestParseConversationChainRejectsZeroDigest(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/contracts/v4/conversation-chain-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(fixture, &raw); err != nil {
		t.Fatal(err)
	}
	raw["digest"] = "sha256:" + strings.Repeat("0", 64)
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(body); err == nil {
		t.Fatal("accepted an unbound all-zero persisted digest")
	}
}

func TestConversationChainRejectsIntegersAboveJavaScriptSafeMaximum(t *testing.T) {
	document := frozenChain()
	document.TurnUnits[0].UserMessage.SourceRef.RecordOrdinal = 1 << 53
	if err := Validate(document); err == nil {
		t.Fatal("accepted record ordinal above JavaScript safe integer maximum")
	}
	document = frozenChain()
	document.Coverage.SourceMessages = 1 << 53
	if err := Validate(document); err == nil {
		t.Fatal("accepted coverage count above JavaScript safe integer maximum")
	}
}

func TestConversationChainRejectsOversizedVisibleExcerptAndUnauthenticatedSource(t *testing.T) {
	document := frozenChain()
	document.TurnUnits[0].UserMessage.VisibleExcerpt = strings.Repeat("界", 1366)
	if err := Validate(document); err == nil {
		t.Fatal("accepted visible excerpt above 4,096 UTF-8 bytes")
	}
	document = frozenChain()
	document.TurnUnits[0].UserMessage.SourceRef.SourceHash = ""
	if err := Validate(document); err == nil {
		t.Fatal("accepted unauthenticated source reference")
	}
	document = frozenChain()
	document.TurnUnits[0].UserMessage.SourceRef.Provider = "codex"
	if err := Validate(document); err == nil {
		t.Fatal("accepted source reference bound to a different provider")
	}
}

func TestConversationChainExactObjectsRejectRawToolOutput(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/contracts/v4/conversation-chain-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(fixture, &raw); err != nil {
		t.Fatal(err)
	}
	turn := raw["turn_units"].([]any)[0].(map[string]any)
	action := turn["actions"].([]any)[0].(map[string]any)
	action["raw_tool_output"] = map[string]any{"secret": true}
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(body); err == nil {
		t.Fatal("accepted arbitrary raw tool output")
	} else if got := strictjson.CodeOf(err); got != "wire_shape_invalid" {
		t.Fatalf("rejection code = %q, want wire_shape_invalid: %v", got, err)
	}
}

func frozenChain() Document {
	return Document{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Digest: "sha256:" + strings.Repeat("0", 64),
		ProjectID: "project-p", Provider: "claude", SessionID: "session-1",
		SessionViewDigest: "sha256:" + strings.Repeat("1", 64), DependencyDigest: "sha256:" + strings.Repeat("2", 64),
		SegmentationRuleVersion: "visible-turn-v1",
		Coverage:                Coverage{SourceMessages: 1, CapturedMessages: 1, TurnUnits: 1, UnansweredUnits: 1},
		TurnUnits: []TurnUnit{{
			TurnUnitID: "turn-1", Ordinal: 1, StartedAt: "2026-09-04T00:00:00Z", EndedAt: nil,
			UserMessage:       Message{Role: RoleUser, RevisionID: "revision-user-1", SourceRef: frozenSourceRef(), OccurredAt: "2026-09-04T00:00:00Z", VisibleExcerpt: "question", Truncated: false},
			AssistantMessages: []Message{}, Actions: []Action{}, Results: []Result{}, AnswerState: AnswerNone,
		}},
	}
}

func frozenSourceRef() SourceRef {
	return SourceRef{Provider: "claude", SessionID: "session-1", SourceIdentity: "source-1", RecordOrdinal: 7, SourceHash: strings.Repeat("3", 64)}
}
