package problemmap

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestParseFrozenProblemMapCandidateFixtures(t *testing.T) {
	valid, err := os.ReadFile("../../testdata/contracts/v4/problem-map-candidate-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCandidates(valid); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	invalid, err := os.ReadFile("../../testdata/contracts/v4/problem-map-candidate-v1.invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCandidates(invalid); err == nil {
		t.Fatal("deterministic candidate with an Agent run was accepted")
	} else if got := strictjson.CodeOf(err); got != "wire_contract_invalid" {
		t.Fatalf("rejection code = %q, want wire_contract_invalid: %v", got, err)
	}
}

func TestProblemCandidateLimitsAlternatesAndRelatedNodes(t *testing.T) {
	store := frozenCandidates()
	store.Candidates[0].AlternateTargetIDs = []string{"p-1", "p-2", "p-3"}
	if err := ValidateCandidates(store); err == nil {
		t.Fatal("accepted more than two alternate targets")
	}
	store = frozenCandidates()
	store.Candidates[0].RelatedNodeIDs = []string{"p-1", "p-2", "p-3"}
	if err := ValidateCandidates(store); err == nil {
		t.Fatal("accepted more than two related nodes")
	}
}

func TestRenderProblemCandidatesNormalizesCollectionsAndBindsDigest(t *testing.T) {
	store := frozenCandidates()
	store.Candidates[0].AlternateTargetIDs = nil
	store.Candidates[0].RelatedNodeIDs = nil
	store.Candidates[0].Grounds[0].MatchedFactRefs = nil
	rendered, err := RenderCandidates(store)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(rendered, &raw); err != nil {
		t.Fatal(err)
	}
	candidate := raw["candidates"].([]any)[0].(map[string]any)
	for _, key := range []string{"alternate_target_ids", "related_node_ids"} {
		if _, ok := candidate[key].([]any); !ok {
			t.Fatalf("%s did not render as an array", key)
		}
	}
	parsed, err := ParseCandidates(rendered)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Candidates[0].Question = "tampered"
	tampered, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCandidates(tampered); err == nil {
		t.Fatal("accepted tampered candidate store digest")
	}
}

func TestParseProblemCandidatesRejectsZeroDigest(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/contracts/v4/problem-map-candidate-v1.valid.json")
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
	if _, err := ParseCandidates(body); err == nil {
		t.Fatal("accepted an unbound all-zero persisted digest")
	}
}

func TestProblemCandidatesRejectRevisionAboveJavaScriptSafeMaximum(t *testing.T) {
	store := frozenCandidates()
	store.Candidates[0].Revision = 1 << 53
	if err := ValidateCandidates(store); err == nil {
		t.Fatal("accepted candidate revision above JavaScript safe integer maximum")
	}
}

func TestProblemCandidatesRequireQualifiedReferenceCapabilityWithoutInventingDependencyAuthority(t *testing.T) {
	oldView := "sha256:" + strings.Repeat("2", 64)
	newView := "sha256:" + strings.Repeat("3", 64)
	store := frozenCandidates()
	store.MinimumReaderVersion = "0.4.3"
	store.Candidates[0].SourceTurnRefs = []reviewv4.SourceTurnRef{
		{Provider: "opencode", SessionID: "session-1", TurnUnitID: "turn-1", SessionViewDigest: oldView},
		{Provider: "opencode", SessionID: "session-1", TurnUnitID: "turn-1", SessionViewDigest: newView},
	}
	if err := ValidateCandidates(store); err != nil {
		t.Fatalf("qualified multi-view candidate rejected: %v", err)
	}

	legacyFloor := store
	legacyFloor.MinimumReaderVersion = "0.4.0"
	if err := ValidateCandidates(legacyFloor); err == nil {
		t.Fatal("qualified candidate used legacy reader floor")
	}

	badDigest := store
	badDigest.Candidates = append([]Candidate(nil), store.Candidates...)
	badDigest.Candidates[0].SourceTurnRefs = append([]reviewv4.SourceTurnRef(nil), store.Candidates[0].SourceTurnRefs...)
	badDigest.Candidates[0].SourceTurnRefs[0].SessionViewDigest = "not-a-digest"
	if err := ValidateCandidates(badDigest); err == nil {
		t.Fatal("candidate accepted malformed snapshot qualifier")
	}

	future := store
	future.MinimumReaderVersion = "0.4.4"
	if err := ValidateCandidates(future); err == nil {
		t.Fatal("candidate accepted unsupported future reader capability")
	}
}

func TestProblemCandidateLegacyCanonicalBytesRemainUnqualified(t *testing.T) {
	store := frozenCandidates()
	rendered, err := RenderCandidates(store)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), "session_view_digest") {
		t.Fatalf("legacy candidate gained a snapshot qualifier: %s", rendered)
	}
	parsed, err := ParseCandidates(rendered)
	if err != nil || parsed.MinimumReaderVersion != "0.4.0" {
		t.Fatalf("legacy candidate round trip changed capability: err=%v version=%q", err, parsed.MinimumReaderVersion)
	}
}

func TestProblemCandidateRejectsExplicitEmptySourceViewQualifier(t *testing.T) {
	body, err := os.ReadFile("../../testdata/contracts/v4/problem-map-candidate-v1.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"turn_unit_id": "turn-1"`), []byte(`"turn_unit_id": "turn-1", "session_view_digest": ""`), 1)
	if _, err := ParseCandidates(body); err == nil || strictjson.CodeOf(err) != "wire_shape_invalid" {
		t.Fatalf("explicit empty candidate qualifier rejection = %v", err)
	}
}

func TestProblemGraphRejectsCycle(t *testing.T) {
	nodes := []reviewv4.ProblemNode{
		{ID: "p-a", PrimaryParentID: stringPtr("p-b")},
		{ID: "p-b", PrimaryParentID: stringPtr("p-a")},
	}
	if err := ValidateGraph(nodes); err == nil {
		t.Fatal("accepted problem cycle")
	}
}

func TestProblemGraphRejectsMissingRelationsAndDuplicateSiblingOrder(t *testing.T) {
	base := []reviewv4.ProblemNode{problemNode("p-a", nil, 0), problemNode("p-b", stringPtr("p-a"), 0)}
	bad := append([]reviewv4.ProblemNode(nil), base...)
	bad[1].RelatedNodeIDs = []string{"missing"}
	if err := ValidateGraph(bad); err == nil {
		t.Fatal("accepted missing related node")
	}
	bad = append(bad[:0:0], base...)
	bad = append(bad, problemNode("p-c", stringPtr("p-a"), 0))
	if err := ValidateGraph(bad); err == nil {
		t.Fatal("accepted duplicate sibling order")
	}
}

func TestPreviewMoveRejectsCycleAndReportsAffectedSubtree(t *testing.T) {
	nodes := []reviewv4.ProblemNode{
		problemNode("root", nil, 0),
		problemNode("child", stringPtr("root"), 0),
		problemNode("grandchild", stringPtr("child"), 0),
		problemNode("other", nil, 1),
	}
	if _, err := PreviewMove(nodes, "root", "grandchild"); err == nil {
		t.Fatal("accepted a move below its own descendant")
	}
	preview, err := PreviewMove(nodes, "child", "other")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(preview.OldPath, "/") != "root/child" || strings.Join(preview.NewPath, "/") != "other/child" || strings.Join(preview.AffectedNodeIDs, ",") != "child,grandchild" {
		t.Fatalf("unexpected move preview: %+v", preview)
	}
}

func frozenCandidates() CandidateStore {
	return CandidateStore{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Digest: "sha256:" + strings.Repeat("0", 64), ProjectID: "project-p",
		Candidates: []Candidate{{
			CandidateID: "candidate-1", ProjectID: "project-p", Question: "Where does this belong?",
			SourceTurnRefs:      []reviewv4.SourceTurnRef{{Provider: "opencode", SessionID: "session-1", TurnUnitID: "turn-1"}},
			RecommendedRelation: RelationKeepPending, RecommendedTargetID: nil, AlternateTargetIDs: []string{}, RelatedNodeIDs: []string{},
			Grounds:    []Ground{{RuleID: "rule-1", RuleVersion: "v1", MatchedFactRefs: []string{"fact-1"}, Explanation: "No stable parent signal."}},
			Confidence: ConfidenceLow, Status: CandidatePending, DependencyDigests: []string{"sha256:" + strings.Repeat("1", 64)},
			AnalysisMode: AnalysisDeterministic, AgentRunID: nil, Revision: 1, CreatedAt: "2026-09-04T00:00:00Z", UpdatedAt: "2026-09-04T00:00:00Z",
		}},
	}
}

func problemNode(id string, parent *string, order int) reviewv4.ProblemNode {
	return reviewv4.ProblemNode{
		ID: id, Question: id + "?", PrimaryParentID: parent, RelatedNodeIDs: []string{}, WorkflowState: "not_started", AnswerState: "no_answer",
		CompletionCriterion: "", CurrentConclusion: "", SourceTurnRefs: []reviewv4.SourceTurnRef{}, Provenance: "human_created",
		FirstProposedAt: "2026-09-04T00:00:00Z", SiblingOrder: order, ConfirmedAt: nil, Revision: 1,
	}
}

func stringPtr(value string) *string { return &value }
