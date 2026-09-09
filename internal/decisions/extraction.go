package decisions

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/annotation"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

const (
	ExtractorVersion           = "decision-extract-v1"
	PromptSchemaVersion        = "decision-candidate-v1"
	MaxExtractionPromptBytes   = 256 << 10
	MaxExtractionOutputBytes   = 64 << 10
	maxExtractionExcerptBytes  = 1024
	extractionTruncationMarker = "\n[SessionReviewer excerpt truncated]"
)

var extractionOutputSchema = []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","title":"decision-candidate-proposal-v1","type":"object","additionalProperties":false,"required":["schema_version","contract","candidates"],"properties":{"schema_version":{"const":1},"contract":{"const":"decision-candidate-proposal-v1"},"candidates":{"type":"array","maxItems":128,"items":{"type":"object","additionalProperties":false,"required":["kind","occurred_at","title","rationale","impact","reevaluate_when","session_refs","session_view_digests"],"properties":{"kind":{"enum":["decision","agreement"]},"occurred_at":{"type":"string","maxLength":128},"title":{"type":"string","minLength":1,"maxLength":4096},"rationale":{"type":"string","maxLength":4096},"impact":{"type":"string","maxLength":4096},"reevaluate_when":{"type":"string","maxLength":4096},"session_refs":{"type":"array","maxItems":256,"items":{"type":"object","additionalProperties":false,"required":["provider","session_id"],"properties":{"provider":{"type":"string"},"session_id":{"type":"string"}}}},"session_view_digests":{"type":"array","minItems":1,"maxItems":256,"items":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"}}}}}}}`)

type extractionPrompt struct {
	SchemaVersion int                     `json:"schema_version" required:"true"`
	Contract      string                  `json:"contract" required:"true"`
	ProjectID     string                  `json:"project_id" required:"true"`
	Sessions      []extractionPromptChain `json:"sessions" required:"true"`
}

type extractionPromptChain struct {
	Provider          string                 `json:"provider" required:"true"`
	SessionID         string                 `json:"session_id" required:"true"`
	SessionViewDigest string                 `json:"session_view_digest" required:"true"`
	Turns             []extractionPromptTurn `json:"turns" required:"true"`
}

type extractionPromptTurn struct {
	TurnUnitID        string                   `json:"turn_unit_id" required:"true"`
	PartIndex         int                      `json:"part_index" required:"true"`
	PartCount         int                      `json:"part_count" required:"true"`
	Question          string                   `json:"question" required:"true"`
	QuestionTruncated bool                     `json:"question_truncated" required:"true"`
	Answers           []extractionPromptAnswer `json:"answers" required:"true"`
}

type extractionPromptAnswer struct {
	Text      string `json:"text" required:"true"`
	Truncated bool   `json:"truncated" required:"true"`
}

type ExtractionBatch struct {
	Prompt       []byte
	Dependencies []memory.ConversationChainDependency
}

type extractionProposal struct {
	SchemaVersion int                           `json:"schema_version" required:"true"`
	Contract      string                        `json:"contract" required:"true"`
	Candidates    []extractionProposalCandidate `json:"candidates" required:"true"`
}

type extractionProposalCandidate struct {
	Kind               string                `json:"kind" required:"true"`
	OccurredAt         string                `json:"occurred_at" required:"true"`
	Title              string                `json:"title" required:"true"`
	Rationale          string                `json:"rationale" required:"true"`
	Impact             string                `json:"impact" required:"true"`
	ReevaluateWhen     string                `json:"reevaluate_when" required:"true"`
	SessionRefs        []reviewv4.SessionRef `json:"session_refs" required:"true"`
	SessionViewDigests []string              `json:"session_view_digests" required:"true"`
}

func ExtractionIdentity(projectID string, newDigests []string) string {
	values := append([]string(nil), newDigests...)
	sort.Strings(values)
	preimage := projectID + "\x00" + ExtractorVersion + "\x00" + PromptSchemaVersion + "\x00" + strings.Join(values, "\x00")
	sum := sha256.Sum256([]byte(preimage))
	return "decision-extract-" + hex.EncodeToString(sum[:16])
}

func BuildExtractionPrompt(projectID string, dependencies []memory.ConversationChainDependency, chains map[string]conversationchain.Document) ([]byte, []byte, error) {
	batches, schema, err := BuildExtractionBatches(projectID, dependencies, chains)
	if err != nil {
		return nil, nil, err
	}
	if len(batches) != 1 {
		return nil, nil, errors.New("decision extraction requires multiple prompt batches")
	}
	return batches[0].Prompt, schema, nil
}

func BuildExtractionBatches(projectID string, dependencies []memory.ConversationChainDependency, chains map[string]conversationchain.Document) ([]ExtractionBatch, []byte, error) {
	sessions := make([]extractionPromptChain, 0, len(dependencies))
	for _, dependency := range dependencies {
		chain, ok := chains[dependency.SessionViewDigest]
		if !ok || chain.Provider != dependency.Provider || chain.SessionID != dependency.SessionID || chain.SessionViewDigest != dependency.SessionViewDigest {
			return nil, nil, errors.New("extraction chain does not match its authenticated dependency")
		}
		turns := make([]extractionPromptTurn, 0, len(chain.TurnUnits))
		for _, turn := range chain.TurnUnits {
			answers := make([]extractionPromptAnswer, len(turn.AssistantMessages))
			for index := range turn.AssistantMessages {
				text, truncated := boundExtractionExcerpt(turn.AssistantMessages[index].VisibleExcerpt)
				answers[index] = extractionPromptAnswer{Text: text, Truncated: truncated || turn.AssistantMessages[index].Truncated}
			}
			question, truncated := boundExtractionExcerpt(turn.UserMessage.VisibleExcerpt)
			if len(answers) == 0 {
				turns = append(turns, extractionPromptTurn{TurnUnitID: turn.TurnUnitID, PartIndex: 1, PartCount: 1, Question: question, QuestionTruncated: truncated || turn.UserMessage.Truncated, Answers: []extractionPromptAnswer{}})
				continue
			}
			for index, answer := range answers {
				turns = append(turns, extractionPromptTurn{TurnUnitID: turn.TurnUnitID, PartIndex: index + 1, PartCount: len(answers), Question: question, QuestionTruncated: truncated || turn.UserMessage.Truncated, Answers: []extractionPromptAnswer{answer}})
			}
		}
		sessions = append(sessions, extractionPromptChain{Provider: dependency.Provider, SessionID: dependency.SessionID, SessionViewDigest: dependency.SessionViewDigest, Turns: turns})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].SessionViewDigest < sessions[j].SessionViewDigest })
	batches := []ExtractionBatch{}
	current := extractionPrompt{SchemaVersion: 1, Contract: "decision-extraction-input-v1", ProjectID: projectID, Sessions: []extractionPromptChain{}}
	flush := func() error {
		if len(current.Sessions) == 0 {
			return nil
		}
		prompt, err := strictjson.Encode(current)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		batchDependencies := []memory.ConversationChainDependency{}
		for _, session := range current.Sessions {
			if seen[session.SessionViewDigest] {
				continue
			}
			seen[session.SessionViewDigest] = true
			for _, dependency := range dependencies {
				if dependency.SessionViewDigest == session.SessionViewDigest {
					batchDependencies = append(batchDependencies, dependency)
					break
				}
			}
		}
		batches = append(batches, ExtractionBatch{Prompt: prompt, Dependencies: batchDependencies})
		current.Sessions = []extractionPromptChain{}
		return nil
	}
	appendPart := func(part extractionPromptChain) error {
		candidate := current
		candidate.Sessions = append(append([]extractionPromptChain(nil), current.Sessions...), part)
		encoded, err := strictjson.Encode(candidate)
		if err != nil {
			return err
		}
		if len(encoded) > MaxExtractionPromptBytes {
			if len(current.Sessions) == 0 {
				return errors.New("one decision extraction turn exceeds 256 KiB after bounded excerpts")
			}
			if err := flush(); err != nil {
				return err
			}
			candidate = current
			candidate.Sessions = append([]extractionPromptChain(nil), part)
			encoded, err = strictjson.Encode(candidate)
			if err != nil {
				return err
			}
			if len(encoded) > MaxExtractionPromptBytes {
				return errors.New("one decision extraction turn exceeds 256 KiB after bounded excerpts")
			}
		}
		current.Sessions = append(current.Sessions, part)
		return nil
	}
	for _, session := range sessions {
		turns := session.Turns
		if len(turns) == 0 {
			if err := appendPart(extractionPromptChain{Provider: session.Provider, SessionID: session.SessionID, SessionViewDigest: session.SessionViewDigest, Turns: []extractionPromptTurn{}}); err != nil {
				return nil, nil, err
			}
			continue
		}
		for _, turn := range turns {
			part := extractionPromptChain{Provider: session.Provider, SessionID: session.SessionID, SessionViewDigest: session.SessionViewDigest, Turns: []extractionPromptTurn{turn}}
			if err := appendPart(part); err != nil {
				return nil, nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, nil, err
	}
	if len(batches) == 0 {
		empty, err := strictjson.Encode(current)
		if err != nil {
			return nil, nil, err
		}
		batches = append(batches, ExtractionBatch{Prompt: empty, Dependencies: []memory.ConversationChainDependency{}})
	}
	return batches, append([]byte(nil), extractionOutputSchema...), nil
}

func boundExtractionExcerpt(value string) (string, bool) {
	if len(value) <= maxExtractionExcerptBytes {
		return value, false
	}
	limit := maxExtractionExcerptBytes - len(extractionTruncationMarker)
	for limit > 0 && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return value[:limit] + extractionTruncationMarker, true
}

func ParseExtractionProposal(body []byte, projectID, generationID, runID string, allowedDependencies []memory.ConversationChainDependency, now time.Time) ([]annotation.Annotation, error) {
	if len(body) > MaxExtractionOutputBytes {
		return nil, errors.New("decision extraction proposal exceeds 64 KiB")
	}
	var proposal extractionProposal
	if err := strictjson.Decode(body, &proposal); err != nil {
		return nil, err
	}
	if proposal.SchemaVersion != 1 || proposal.Contract != "decision-candidate-proposal-v1" || proposal.Candidates == nil || len(proposal.Candidates) > 128 || now.IsZero() {
		return nil, errors.New("decision extraction proposal identity is invalid")
	}
	chainsByDigest := map[string]memory.ConversationChainDependency{}
	for _, dependency := range allowedDependencies {
		chainsByDigest[dependency.SessionViewDigest] = dependency
	}
	result := make([]annotation.Annotation, 0, len(proposal.Candidates))
	seen := map[string]bool{}
	for _, candidate := range proposal.Candidates {
		if candidate.SessionViewDigests == nil || len(candidate.SessionViewDigests) == 0 || len(candidate.SessionViewDigests) > 256 || candidate.SessionRefs == nil || len(candidate.SessionRefs) == 0 {
			return nil, errors.New("candidate must cite a SessionView dependency")
		}
		refKeys := map[string]bool{}
		for _, ref := range candidate.SessionRefs {
			refKeys[ref.Provider+"\x00"+ref.SessionID] = true
		}
		dependencies := make([]annotation.Dependency, len(candidate.SessionViewDigests))
		for index, digest := range candidate.SessionViewDigests {
			chain, exists := chainsByDigest[digest]
			if !exists || !refKeys[chain.Provider+"\x00"+chain.SessionID] || seen[digest+"\x00"+candidate.Title] {
				return nil, errors.New("candidate cites a foreign or duplicate SessionView dependency")
			}
			delete(refKeys, chain.Provider+"\x00"+chain.SessionID)
			seen[digest+"\x00"+candidate.Title] = true
			dependencies[index] = annotation.Dependency{Kind: "session_view", RevisionID: "view-" + strings.TrimPrefix(digest, "sha256:")[:16], Digest: digest}
		}
		if len(refKeys) != 0 {
			return nil, errors.New("candidate Session reference is not bound to a cited SessionView")
		}
		input := DecisionInput{SchemaVersion: 1, Kind: candidate.Kind, OccurredAt: candidate.OccurredAt, Title: candidate.Title, Rationale: candidate.Rationale, Impact: candidate.Impact, Status: reviewv4.DecisionActive, ReevaluateWhen: candidate.ReevaluateWhen, Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: candidate.SessionRefs, Pinned: false}
		if err := validateDecisionInput(input); err != nil {
			return nil, err
		}
		text, err := strictjson.Encode(input)
		if err != nil || len(text) > 4096 {
			return nil, errors.Join(errors.New("candidate decision body is too large"), err)
		}
		sum := sha256.Sum256(append([]byte(runID+"\x00"), text...))
		candidateID := "candidate-" + hex.EncodeToString(sum[:16])
		entityID := "decision-" + hex.EncodeToString(sum[16:])
		field := candidate.Kind
		kind := candidate.Kind + "_candidate"
		result = append(result, annotation.Annotation{ID: candidateID, ProjectID: projectID, AnnotationKind: kind, EntityID: &entityID, Field: &field, Status: annotation.CandidatePending, Text: string(text), GenerationID: generationID, SchemaVersion: 1, AnalysisProfile: ExtractorVersion, AgentRunID: runID, Dependencies: dependencies, Revision: 1, CreatedAt: now.UTC().Format(time.RFC3339Nano)})
	}
	return result, nil
}
