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
	chain := conversationchain.Document{Provider: "codex", SessionID: "session-1", SessionViewDigest: digest, TurnUnits: []conversationchain.TurnUnit{{TurnUnitID: "turn-1", UserMessage: conversationchain.Message{VisibleExcerpt: "Why choose CAS?"}, AssistantMessages: []conversationchain.Message{{VisibleExcerpt: "It prevents stale writes."}}}}}
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
				answers[answer].VisibleExcerpt = strings.Repeat("界", 1365)
			}
			turns[index] = conversationchain.TurnUnit{TurnUnitID: fmt.Sprintf("%s-turn-%03d", dependency.SessionID, index), UserMessage: conversationchain.Message{VisibleExcerpt: strings.Repeat("问", 1365)}, AssistantMessages: answers}
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
		answers[index].VisibleExcerpt = fmt.Sprintf("answer-%03d-%s", index, strings.Repeat("x", 2000))
	}
	chain := conversationchain.Document{Provider: "codex", SessionID: "session-a", SessionViewDigest: digest, TurnUnits: []conversationchain.TurnUnit{{TurnUnitID: "turn-a", UserMessage: conversationchain.Message{VisibleExcerpt: "question"}, AssistantMessages: answers}}}
	batches, _, err := BuildExtractionBatches("project-p", []memory.ConversationChainDependency{{Provider: "codex", SessionID: "session-a", SessionViewDigest: digest}}, map[string]conversationchain.Document{digest: chain})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
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
	}
	if len(seen) != len(answers) {
		t.Fatalf("covered answers=%d want=%d", len(seen), len(answers))
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
			turns[index] = conversationchain.TurnUnit{TurnUnitID: fmt.Sprintf("%s-%03d", dependency.SessionID, index), UserMessage: conversationchain.Message{VisibleExcerpt: strings.Repeat("q", 4096)}, AssistantMessages: []conversationchain.Message{{VisibleExcerpt: strings.Repeat("a", 4096)}}}
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
		body := []byte(fmt.Sprintf(`{"schema_version":1,"contract":"decision-candidate-proposal-v1","candidates":[{"kind":"decision","occurred_at":"2026-09-09","title":"Candidate from %s","rationale":"Reason","impact":"Impact","reevaluate_when":"Later","session_refs":[{"provider":"%s","session_id":"%s"}],"session_view_digests":["%s"]}]}`, dependency.SessionID, dependency.Provider, dependency.SessionID, dependency.SessionViewDigest))
		parsed, err := ParseExtractionProposal(body, "project-p", "generation-1", "run-1", batch.Dependencies, now)
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
		chain.TurnUnits = append(chain.TurnUnits, conversationchain.TurnUnit{TurnUnitID: fmt.Sprintf("turn-%03d", index), UserMessage: conversationchain.Message{VisibleExcerpt: strings.Repeat("q", 4096)}, AssistantMessages: []conversationchain.Message{{VisibleExcerpt: strings.Repeat("a", 4096)}}})
	}
	batches, _, err := BuildExtractionBatches("project-p", []memory.ConversationChainDependency{dependency}, map[string]conversationchain.Document{digest: chain})
	if err != nil || len(batches) < 2 {
		t.Fatalf("batches=%d err=%v", len(batches), err)
	}
	body := []byte(`{"schema_version":1,"contract":"decision-candidate-proposal-v1","candidates":[{"kind":"decision","occurred_at":"2026-09-09","title":"Same candidate","rationale":"Reason","impact":"Impact","reevaluate_when":"Later","session_refs":[{"provider":"codex","session_id":"session-a"}],"session_view_digests":["` + digest + `"]}]}`)
	first, err := ParseExtractionProposal(body, "project-p", "generation-1", "run-1", batches[0].Dependencies, time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseExtractionProposal(body, "project-p", "generation-1", "run-1", batches[1].Dependencies, time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC))
	if err != nil || first[0].ID != second[0].ID {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestParseExtractionProposalCreatesPendingCandidatesAndRejectsForeignDependency(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	body := []byte(`{"schema_version":1,"contract":"decision-candidate-proposal-v1","candidates":[{"kind":"decision","occurred_at":"2026-09-09","title":"Use CAS","rationale":"Avoid stale writes","impact":"All publications","reevaluate_when":"The publication protocol changes","session_refs":[{"provider":"codex","session_id":"session-1"}],"session_view_digests":["` + digest + `"]}]}`)
	dependencies := []memory.ConversationChainDependency{{Provider: "codex", SessionID: "session-1", SessionViewDigest: digest, Digest: "sha256:" + strings.Repeat("3", 64)}}
	candidates, err := ParseExtractionProposal(body, "project-p", "generation-1", "run-1", dependencies, time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC))
	if err != nil || len(candidates) != 1 || candidates[0].Status != annotation.CandidatePending || candidates[0].EntityID == nil {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	foreign := strings.Replace(string(body), digest, "sha256:"+strings.Repeat("2", 64), 1)
	if _, err := ParseExtractionProposal([]byte(foreign), "project-p", "generation-1", "run-1", dependencies, time.Now()); err == nil {
		t.Fatal("foreign dependency accepted")
	}
}
