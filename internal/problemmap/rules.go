package problemmap

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

const DeterministicRuleVersion = "problem-placement-v2"

var shortControlReply = regexp.MustCompile(`^(?:[0-9]+|[一二三四五六七八九十]|(?:好的)?继续|确认|同意|符合|可以|允许|发布|补吧|好|好的|是|否|不|开始|重试)$`)

// ChainInput is an authenticated current conversation chain and its immutable
// object digest. It contains only the retained visible conversation projection.
type ChainInput struct {
	Document conversationchain.Document
	Digest   string
}

type PlacementInput struct {
	ProjectID    string
	Question     string
	SourceTurns  []reviewv4.SourceTurnRef
	Graph        []reviewv4.ProblemNode
	Dependencies []string
	Now          time.Time
}

// RecommendPlacement applies bounded exact-text rules only. A low-confidence
// result remains pending; this function has no Agent dependency.
func RecommendPlacement(input PlacementInput) Candidate {
	dependencies := append([]string(nil), input.Dependencies...)
	sort.Strings(dependencies)
	stamp := input.Now.UTC().Round(0).Format(time.RFC3339Nano)
	candidate := Candidate{
		CandidateID: AnalysisIdentity(input.ProjectID, input.Question, DeterministicRuleVersion, dependencies), ProjectID: input.ProjectID, Question: input.Question,
		SourceTurnRefs: cloneNonNilSlice(input.SourceTurns), RecommendedRelation: RelationKeepPending, AlternateTargetIDs: []string{}, RelatedNodeIDs: []string{},
		Grounds:    []Ground{{RuleID: "no-exact-structure", RuleVersion: DeterministicRuleVersion, MatchedFactRefs: []string{}, Explanation: "未找到可靠的显式层级信号，继续待人工归类。"}},
		Confidence: ConfidenceLow, Status: CandidatePending, DependencyDigests: dependencies, AnalysisMode: AnalysisDeterministic, Revision: 1, CreatedAt: stamp, UpdatedAt: stamp,
	}
	normalized := normalizeQuestion(input.Question)
	for _, node := range input.Graph {
		if normalizeQuestion(node.Question) == normalized {
			target := node.ID
			candidate.RecommendedRelation, candidate.RecommendedTargetID, candidate.Confidence = RelationMerge, &target, ConfidenceHigh
			candidate.Grounds = []Ground{{RuleID: "exact-question", RuleVersion: DeterministicRuleVersion, MatchedFactRefs: []string{node.ID}, Explanation: "用户问题与已确认问题文本完全一致，建议合并来源。"}}
			return candidate
		}
	}
	matches := []string{}
	for _, node := range input.Graph {
		if node.Question != "" && strings.Contains(input.Question, node.Question) {
			matches = append(matches, node.ID)
		}
	}
	sort.Strings(matches)
	if len(matches) == 1 {
		target := matches[0]
		candidate.RecommendedRelation, candidate.RecommendedTargetID, candidate.Confidence = RelationChild, &target, ConfidenceHigh
		candidate.Grounds = []Ground{{RuleID: "quoted-parent-question", RuleVersion: DeterministicRuleVersion, MatchedFactRefs: []string{target}, Explanation: "新问题明确引用了一个已确认父问题，建议作为子问题。"}}
	}
	return candidate
}

// DiscoverCandidates turns every authenticated current visible user turn into
// one deterministic private candidate. Repeated equal questions within one
// dependency set are combined while preserving all exact coordinates.
func DiscoverCandidates(projectID string, chains []ChainInput, graph []reviewv4.ProblemNode, now time.Time) []Candidate {
	formalRefs := map[string]bool{}
	for _, node := range graph {
		for _, ref := range node.SourceTurnRefs {
			formalRefs[sourceKey(ref)] = true
		}
	}
	byID := map[string]Candidate{}
	for _, chain := range chains {
		doc := chain.Document
		if doc.ProjectID != projectID || doc.Digest != chain.Digest {
			continue
		}
		deps := []string{doc.Digest, doc.SessionViewDigest}
		for _, turn := range doc.TurnUnits {
			question := turn.UserMessage.VisibleExcerpt
			ref := reviewv4.SourceTurnRef{Provider: doc.Provider, SessionID: doc.SessionID, TurnUnitID: turn.TurnUnitID, SessionViewDigest: doc.SessionViewDigest}
			if !candidateEligible(question) || formalRefs[sourceKey(ref)] {
				continue
			}
			candidate := RecommendPlacement(PlacementInput{ProjectID: projectID, Question: question, SourceTurns: []reviewv4.SourceTurnRef{ref}, Graph: graph, Dependencies: deps, Now: now})
			if prior, ok := byID[candidate.CandidateID]; ok {
				prior.SourceTurnRefs = mergeSourceTurns(prior.SourceTurnRefs, candidate.SourceTurnRefs)
				byID[candidate.CandidateID] = prior
			} else {
				byID[candidate.CandidateID] = candidate
			}
		}
	}
	result := make([]Candidate, 0, len(byID))
	for _, value := range byID {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CandidateID < result[j].CandidateID })
	return result
}

func candidateEligible(question string) bool {
	normalized := normalizeQuestion(question)
	if normalized == "" || shortControlReply.MatchString(normalized) {
		return false
	}
	trimmed := strings.TrimSpace(question)
	for _, tag := range []string{"turn_aborted", "subagent_notification", "app-context", "environment_context", "recommended_plugins"} {
		if strings.HasPrefix(trimmed, "<"+tag+">") && strings.HasSuffix(trimmed, "</"+tag+">") {
			return false
		}
	}
	return true
}

func normalizeQuestion(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
func sourceKey(ref reviewv4.SourceTurnRef) string {
	return ref.Provider + "\x00" + ref.SessionID + "\x00" + ref.TurnUnitID + "\x00" + ref.SessionViewDigest
}
