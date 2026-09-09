package decisions

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/annotation"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestExtractionIdentityUsesOnlySortedNewSessionViewDigests(t *testing.T) {
	a := "sha256:" + strings.Repeat("1", 64)
	b := "sha256:" + strings.Repeat("2", 64)
	first := ExtractionIdentity("project-p", []string{b, a})
	second := ExtractionIdentity("project-p", []string{a, b})
	if first == "" || first != second {
		t.Fatalf("first=%q second=%q", first, second)
	}
}

func TestBuildExtractionPromptUsesBoundedAuthenticatedVisibleEvidence(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	chain := conversationchain.Document{Provider: "codex", SessionID: "session-1", SessionViewDigest: digest, TurnUnits: []conversationchain.TurnUnit{{TurnUnitID: "turn-1", UserMessage: conversationchain.Message{RevisionID: "revision-user-1", VisibleExcerpt: "Why choose CAS?"}, AssistantMessages: []conversationchain.Message{{RevisionID: "revision-agent-1", VisibleExcerpt: "It prevents stale writes."}}}}}
	prompt, schema, err := BuildExtractionPrompt("project-p", []memory.ConversationChainDependency{{Provider: "codex", SessionID: "session-1", SessionViewDigest: digest, Digest: "sha256:" + strings.Repeat("3", 64)}}, map[string]conversationchain.Document{digest: chain})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompt) == 0 || len(prompt) > MaxExtractionPromptBytes || !strings.Contains(string(prompt), "Why choose CAS?") || !strings.Contains(string(schema), "decision-candidate-proposal-v1") {
		t.Fatalf("prompt bytes=%d schema=%s", len(prompt), schema)
	}
}

func TestBuildExtractionPromptBoundsLargeChainsWithoutDroppingSessionIdentity(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("1", 64)
	digestB := "sha256:" + strings.Repeat("2", 64)
	dependencies := []memory.ConversationChainDependency{
		{Provider: "codex", SessionID: "session-a", SessionViewDigest: digestA, Digest: "sha256:" + strings.Repeat("3", 64)},
		{Provider: "codex", SessionID: "session-b", SessionViewDigest: digestB, Digest: "sha256:" + strings.Repeat("4", 64)},
	}
	chains := map[string]conversationchain.Document{}
	for _, dependency := range dependencies {
		turns := make([]conversationchain.TurnUnit, 100)
		for index := range turns {
			answers := make([]conversationchain.Message, 8)
			for answer := range answers {
				answers[answer].RevisionID = fmt.Sprintf("revision-agent-%03d-%03d", index, answer)
				answers[answer].VisibleExcerpt = strings.Repeat("界", 1365)
			}
			turns[index] = conversationchain.TurnUnit{TurnUnitID: fmt.Sprintf("%s-turn-%03d", dependency.SessionID, index), UserMessage: conversationchain.Message{RevisionID: fmt.Sprintf("revision-user-%03d", index), VisibleExcerpt: strings.Repeat("问", 1365)}, AssistantMessages: answers}
		}
		chains[dependency.SessionViewDigest] = conversationchain.Document{Provider: dependency.Provider, SessionID: dependency.SessionID, SessionViewDigest: dependency.SessionViewDigest, TurnUnits: turns}
	}
	batches, _, err := BuildExtractionBatches("project-p", dependencies, chains)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) < 2 {
		t.Fatalf("batches=%d", len(batches))
	}
	seenTurns := map[string]bool{}
	seenSessions := map[string]bool{}
	for _, batch := range batches {
		if len(batch.Prompt) > MaxExtractionPromptBytes {
			t.Fatalf("prompt bytes=%d", len(batch.Prompt))
		}
		var decoded extractionPrompt
		if err := strictjson.Decode(batch.Prompt, &decoded); err != nil {
			t.Fatal(err)
		}
		for _, session := range decoded.Sessions {
			seenSessions[session.SessionID] = true
			for _, turn := range session.Turns {
				seenTurns[turn.TurnUnitID] = true
				if !turn.QuestionTruncated || !strings.HasSuffix(turn.Question, extractionTruncationMarker) {
					t.Fatalf("turn was not explicitly truncated: %+v", turn)
				}
			}
		}
	}
	if len(seenSessions) != 2 || len(seenTurns) != 200 {
		t.Fatalf("sessions=%v turns=%d", seenSessions, len(seenTurns))
	}
}

func TestBuildExtractionPromptSplitsOneTurnWithoutDroppingAnswers(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	answers := make([]conversationchain.Message, 300)
	for index := range answers {
		answers[index].RevisionID = fmt.Sprintf("revision-agent-%03d", index)
		answers[index].VisibleExcerpt = fmt.Sprintf("answer-%03d-%s", index, strings.Repeat("x", 2000))
	}
	chain := conversationchain.Document{Provider: "codex", SessionID: "session-a", SessionViewDigest: digest, TurnUnits: []conversationchain.TurnUnit{{TurnUnitID: "turn-a", UserMessage: conversationchain.Message{RevisionID: "revision-user-a", VisibleExcerpt: "question"}, AssistantMessages: answers}}}
	batches, _, err := BuildExtractionBatches("project-p", []memory.ConversationChainDependency{{Provider: "codex", SessionID: "session-a", SessionViewDigest: digest}}, map[string]conversationchain.Document{digest: chain})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	seenEvidence := map[string]bool{}
	for _, batch := range batches {
		if len(batch.Prompt) > MaxExtractionPromptBytes {
			t.Fatalf("prompt bytes=%d", len(batch.Prompt))
		}
		var decoded extractionPrompt
		if err := strictjson.Decode(batch.Prompt, &decoded); err != nil {
			t.Fatal(err)
		}
		for _, session := range decoded.Sessions {
			for _, turn := range session.Turns {
				for _, answer := range turn.Answers {
					seen[answer.Text[:10]] = true
				}
			}
		}
		for _, ref := range batch.Evidence {
			seenEvidence[ref.RevisionID] = true
		}
	}
	if len(seen) != len(answers) {
		t.Fatalf("covered answers=%d want=%d", len(seen), len(answers))
	}
	if !seenEvidence["revision-user-a"] || len(seenEvidence) != len(answers)+1 {
		t.Fatalf("authenticated evidence=%d want=%d", len(seenEvidence), len(answers)+1)
	}
}

func TestParseExtractionProposalRequiresEvidenceFromItsCitedViewAndTotalDependencyBound(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("1", 64)
	digestB := "sha256:" + strings.Repeat("2", 64)
	dependencyA := memory.ConversationChainDependency{Provider: "codex", SessionID: "session-a", SessionViewDigest: digestA}
	dependencyB := memory.ConversationChainDependency{Provider: "codex", SessionID: "session-b", SessionViewDigest: digestB}
	evidence := ExtractionEvidenceRef{Provider: "codex", SessionID: "session-b", SessionViewDigest: digestB, TurnUnitID: "turn-b", RevisionID: "revision-b"}
	body := proposalForTest(dependencyA, evidence, "Mixed evidence")
	if _, err := ParseExtractionProposal(body, "project-p", "generation-1", "run-1", []memory.ConversationChainDependency{dependencyA, dependencyB}, []ExtractionEvidenceRef{evidence}, time.Now()); err == nil {
		t.Fatal("evidence from an uncited SessionView was accepted")
	}
	evidenceRefs := make([]ExtractionEvidenceRef, 256)
	for index := range evidenceRefs {
		evidenceRefs[index] = ExtractionEvidenceRef{Provider: "codex", SessionID: "session-a", SessionViewDigest: digestA, TurnUnitID: fmt.Sprintf("turn-%03d", index), RevisionID: fmt.Sprintf("revision-%03d", index)}
	}
	var proposal strings.Builder
	proposal.WriteString(`{"schema_version":1,"contract":"decision-candidate-proposal-v1","candidates":[{"kind":"decision","occurred_at":"2026-09-09","title":"Too many dependencies","rationale":"Reason","impact":"Impact","reevaluate_when":"Later","session_refs":[{"provider":"codex","session_id":"session-a"}],"session_view_digests":["` + digestA + `"],"evidence_refs":[`)
	for index, ref := range evidenceRefs {
		if index > 0 {
			proposal.WriteByte(',')
		}
		fmt.Fprintf(&proposal, `{"provider":"codex","session_id":"session-a","session_view_digest":%q,"turn_unit_id":%q,"revision_id":%q}`, digestA, ref.TurnUnitID, ref.RevisionID)
	}
	proposal.WriteString(`]}]}`)
	if _, err := ParseExtractionProposal([]byte(proposal.String()), "project-p", "generation-1", "run-1", []memory.ConversationChainDependency{dependencyA}, evidenceRefs, time.Now()); err == nil {
		t.Fatal("257 total annotation dependencies were accepted")
	}
}

func TestExtractionBatchesCombineIntoOneCommittedWatermark(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("1", 64)
	digestB := "sha256:" + strings.Repeat("2", 64)
	dependencies := []memory.ConversationChainDependency{{Provider: "codex", SessionID: "session-a", SessionViewDigest: digestA}, {Provider: "codex", SessionID: "session-b", SessionViewDigest: digestB}}
	chains := map[string]conversationchain.Document{}
	for _, dependency := range dependencies {
		turns := make([]conversationchain.TurnUnit, 200)
		for index := range turns {
			turns[index] = conversationchain.TurnUnit{TurnUnitID: fmt.Sprintf("%s-%03d", dependency.SessionID, index), UserMessage: conversationchain.Message{RevisionID: fmt.Sprintf("user-%03d", index), VisibleExcerpt: strings.Repeat("q", 4096)}, AssistantMessages: []conversationchain.Message{{RevisionID: fmt.Sprintf("agent-%03d", index), VisibleExcerpt: strings.Repeat("a", 4096)}}}
		}
		chains[dependency.SessionViewDigest] = conversationchain.Document{Provider: dependency.Provider, SessionID: dependency.SessionID, SessionViewDigest: dependency.SessionViewDigest, TurnUnits: turns}
	}
	batches, _, err := BuildExtractionBatches("project-p", dependencies, chains)
	if err != nil || len(batches) < 2 {
		t.Fatalf("batches=%d err=%v", len(batches), err)
	}
	now := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	byID := map[string]annotation.Annotation{}
	for _, batch := range batches {
		dependency := batch.Dependencies[0]
		evidence := batch.Evidence[0]
		body := proposalForTest(dependency, evidence, "Candidate from "+dependency.SessionID)
		parsed, err := ParseExtractionProposal(body, "project-p", "generation-1", "run-1", batch.Dependencies, batch.Evidence, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range parsed {
			byID[candidate.ID] = candidate
		}
	}
	candidates := make([]annotation.Annotation, 0, len(byID))
	for _, candidate := range byID {
		candidates = append(candidates, candidate)
	}
	store, err := OpenStore(t.TempDir(), "project-p")
	if err != nil {
		t.Fatal(err)
	}
	run := annotation.Run{RunID: "run-1", ProjectID: "project-p", Status: "completed", ExtractorVersion: ExtractorVersion, PromptSchemaVersion: PromptSchemaVersion, DependencyDigests: []string{digestA, digestB}, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Add(time.Second).Format(time.RFC3339Nano)}
	if err := store.CommitExtraction(run, candidates); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load()
	if err != nil || len(record.ExtractionRuns) != 1 || len(SuccessfulExtractionDependencies(record)) != 2 {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestExtractionBatchesUseOneCandidateIDAcrossRepeatedSessionParts(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	dependency := memory.ConversationChainDependency{Provider: "codex", SessionID: "session-a", SessionViewDigest: digest}
	chain := conversationchain.Document{Provider: "codex", SessionID: "session-a", SessionViewDigest: digest, TurnUnits: []conversationchain.TurnUnit{}}
	for index := 0; index < 400; index++ {
		chain.TurnUnits = append(chain.TurnUnits, conversationchain.TurnUnit{TurnUnitID: fmt.Sprintf("turn-%03d", index), UserMessage: conversationchain.Message{RevisionID: fmt.Sprintf("user-%03d", index), VisibleExcerpt: strings.Repeat("q", 4096)}, AssistantMessages: []conversationchain.Message{{RevisionID: fmt.Sprintf("agent-%03d", index), VisibleExcerpt: strings.Repeat("a", 4096)}}})
	}
	batches, _, err := BuildExtractionBatches("project-p", []memory.ConversationChainDependency{dependency}, map[string]conversationchain.Document{digest: chain})
	if err != nil || len(batches) < 2 {
		t.Fatalf("batches=%d err=%v", len(batches), err)
	}
	body := proposalForTest(dependency, batches[0].Evidence[0], "Same candidate")
	first, err := ParseExtractionProposal(body, "project-p", "generation-1", "run-1", batches[0].Dependencies, batches[0].Evidence, time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	body = proposalForTest(dependency, batches[1].Evidence[0], "Same candidate")
	second, err := ParseExtractionProposal(body, "project-p", "generation-1", "run-1", batches[1].Dependencies, batches[1].Evidence, time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC))
	if err != nil || first[0].ID != second[0].ID {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestParseExtractionProposalCreatesPendingCandidatesAndRejectsForeignDependency(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	dependencies := []memory.ConversationChainDependency{{Provider: "codex", SessionID: "session-1", SessionViewDigest: digest, Digest: "sha256:" + strings.Repeat("3", 64)}}
	evidence := ExtractionEvidenceRef{Provider: "codex", SessionID: "session-1", SessionViewDigest: digest, TurnUnitID: "turn-1", RevisionID: "revision-agent-1"}
	body := proposalForTest(dependencies[0], evidence, "Use CAS")
	candidates, err := ParseExtractionProposal(body, "project-p", "generation-1", "run-1", dependencies, []ExtractionEvidenceRef{evidence}, time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC))
	if err != nil || len(candidates) != 1 || candidates[0].Status != annotation.CandidatePending || candidates[0].EntityID == nil {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	foreign := strings.Replace(string(body), digest, "sha256:"+strings.Repeat("2", 64), 1)
	if _, err := ParseExtractionProposal([]byte(foreign), "project-p", "generation-1", "run-1", dependencies, []ExtractionEvidenceRef{evidence}, time.Now()); err == nil {
		t.Fatal("foreign dependency accepted")
	}
	foreignRevision := strings.Replace(string(body), "revision-agent-1", "revision-invented", 1)
	if _, err := ParseExtractionProposal([]byte(foreignRevision), "project-p", "generation-1", "run-1", dependencies, []ExtractionEvidenceRef{evidence}, time.Now()); err == nil {
		t.Fatal("foreign evidence revision accepted")
	}
}

func proposalForTest(dependency memory.ConversationChainDependency, evidence ExtractionEvidenceRef, title string) []byte {
	return []byte(fmt.Sprintf(`{"schema_version":1,"contract":"decision-candidate-proposal-v1","candidates":[{"kind":"decision","occurred_at":"2026-09-09","title":%q,"rationale":"Reason","impact":"Impact","reevaluate_when":"Later","session_refs":[{"provider":%q,"session_id":%q}],"session_view_digests":[%q],"evidence_refs":[{"provider":%q,"session_id":%q,"session_view_digest":%q,"turn_unit_id":%q,"revision_id":%q}]}]}`, title, dependency.Provider, dependency.SessionID, dependency.SessionViewDigest, evidence.Provider, evidence.SessionID, evidence.SessionViewDigest, evidence.TurnUnitID, evidence.RevisionID))
}
