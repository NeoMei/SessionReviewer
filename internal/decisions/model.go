// Package decisions implements human-owned decisions and private proposal
// candidates. Agent output remains a candidate until a trusted publication
// operation creates a reviewv4 Decision.
package decisions

import (
	"errors"
	"fmt"
	"strings"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

const MaxInputBytes = 64 << 10

var (
	ErrInputTooLarge            = errors.New("decision input exceeds 64 KiB")
	ErrDecisionRevisionConflict = errors.New("decision revision conflict")
)

// DecisionInput is the complete bounded human-owned body accepted from stdin.
type DecisionInput struct {
	SchemaVersion  int                     `json:"schema_version" required:"true"`
	Kind           string                  `json:"kind" required:"true"`
	OccurredAt     string                  `json:"occurred_at" required:"true"`
	Title          string                  `json:"title" required:"true"`
	Rationale      string                  `json:"rationale" required:"true"`
	Impact         string                  `json:"impact" required:"true"`
	Status         reviewv4.DecisionStatus `json:"status" required:"true"`
	ReevaluateWhen string                  `json:"reevaluate_when" required:"true"`
	Supersedes     []string                `json:"supersedes" required:"true"`
	MilestoneIDs   []string                `json:"milestone_ids" required:"true"`
	SessionRefs    []reviewv4.SessionRef   `json:"session_refs" required:"true"`
	Pinned         bool                    `json:"pinned" required:"true"`
}

func ParseDecisionInput(body []byte) (DecisionInput, error) {
	if len(body) > MaxInputBytes {
		return DecisionInput{}, ErrInputTooLarge
	}
	var input DecisionInput
	if err := strictjson.Decode(body, &input); err != nil {
		return DecisionInput{}, err
	}
	if err := validateDecisionInput(input); err != nil {
		return DecisionInput{}, err
	}
	return input, nil
}

func CreateDecision(p reviewv4.Presentation, id string, input DecisionInput) (reviewv4.Presentation, error) {
	if id == "" || strings.ContainsAny(id, " /\\") {
		return reviewv4.Presentation{}, errors.New("decision ID is invalid")
	}
	if err := validateDecisionInput(input); err != nil {
		return reviewv4.Presentation{}, err
	}
	next := clonePresentation(p)
	for _, existing := range next.Decisions {
		if existing.ID == id {
			return reviewv4.Presentation{}, errors.New("decision already exists")
		}
	}
	if err := markSuperseded(next.Decisions, input.Supersedes); err != nil {
		return reviewv4.Presentation{}, err
	}
	next.Decisions = append(next.Decisions, decisionFromInput(id, "human_created", 1, input))
	next.Revision++
	if err := reviewv4.ValidatePresentation(next); err != nil {
		return reviewv4.Presentation{}, fmt.Errorf("invalid decision create: %w", err)
	}
	return next, nil
}

func EditDecision(p reviewv4.Presentation, id string, expectedRevision int, input DecisionInput) (reviewv4.Presentation, error) {
	if err := validateDecisionInput(input); err != nil {
		return reviewv4.Presentation{}, err
	}
	next := clonePresentation(p)
	position := -1
	for index := range next.Decisions {
		if next.Decisions[index].ID == id {
			position = index
			break
		}
	}
	if position < 0 {
		return reviewv4.Presentation{}, errors.New("decision does not exist")
	}
	prior := next.Decisions[position]
	if prior.Revision != expectedRevision {
		return reviewv4.Presentation{}, ErrDecisionRevisionConflict
	}
	if err := markSupersededExcept(next.Decisions, input.Supersedes, id); err != nil {
		return reviewv4.Presentation{}, err
	}
	next.Decisions[position] = decisionFromInput(id, prior.Provenance, prior.Revision+1, input)
	next.Revision++
	if err := reviewv4.ValidatePresentation(next); err != nil {
		return reviewv4.Presentation{}, fmt.Errorf("invalid decision edit: %w", err)
	}
	return next, nil
}

func ConfirmCandidateDecision(p reviewv4.Presentation, id string, input DecisionInput) (reviewv4.Presentation, error) {
	if err := validateDecisionInput(input); err != nil {
		return reviewv4.Presentation{}, err
	}
	next := clonePresentation(p)
	for _, existing := range next.Decisions {
		if existing.ID == id {
			return reviewv4.Presentation{}, errors.New("candidate decision already exists")
		}
	}
	if err := markSuperseded(next.Decisions, input.Supersedes); err != nil {
		return reviewv4.Presentation{}, err
	}
	next.Decisions = append(next.Decisions, decisionFromInput(id, "ai_candidate_confirmed", 1, input))
	next.Revision++
	if err := reviewv4.ValidatePresentation(next); err != nil {
		return reviewv4.Presentation{}, fmt.Errorf("invalid candidate confirmation: %w", err)
	}
	return next, nil
}

func validateDecisionInput(input DecisionInput) error {
	if input.SchemaVersion != 1 || (input.Kind != "decision" && input.Kind != "agreement") || strings.TrimSpace(input.OccurredAt) == "" || strings.TrimSpace(input.Title) == "" {
		return errors.New("invalid decision input identity")
	}
	if input.Status != reviewv4.DecisionActive && input.Status != reviewv4.DecisionArchived {
		return errors.New("decision input status must be active or archived")
	}
	if input.Status != reviewv4.DecisionActive && len(input.Supersedes) != 0 {
		return errors.New("only an active decision can supersede another decision")
	}
	if input.Supersedes == nil || input.MilestoneIDs == nil || input.SessionRefs == nil {
		return errors.New("decision input arrays are required")
	}
	return nil
}

func decisionFromInput(id, provenance string, revision int, input DecisionInput) reviewv4.Decision {
	return reviewv4.Decision{ID: id, Kind: input.Kind, OccurredAt: input.OccurredAt, Title: input.Title, Rationale: input.Rationale, Impact: input.Impact, Status: input.Status, LegacyStatusText: nil, ReevaluateWhen: input.ReevaluateWhen, Supersedes: cloneSlice(input.Supersedes), MilestoneIDs: cloneSlice(input.MilestoneIDs), SessionRefs: cloneSlice(input.SessionRefs), Provenance: provenance, Pinned: input.Pinned, Revision: revision}
}

func markSuperseded(decisions []reviewv4.Decision, ids []string) error {
	return markSupersededExcept(decisions, ids, "")
}

func markSupersededExcept(decisions []reviewv4.Decision, ids []string, excluded string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if id == excluded || seen[id] {
			return errors.New("decision supersession target is invalid or duplicated")
		}
		seen[id] = true
		found := false
		for index := range decisions {
			if decisions[index].ID != id {
				continue
			}
			found = true
			if decisions[index].Status != reviewv4.DecisionSuperseded {
				decisions[index].Status = reviewv4.DecisionSuperseded
				decisions[index].LegacyStatusText = nil
				decisions[index].Revision++
			}
			break
		}
		if !found {
			return fmt.Errorf("superseded decision %q does not exist", id)
		}
	}
	return nil
}

func clonePresentation(p reviewv4.Presentation) reviewv4.Presentation {
	next := p
	next.Decisions = cloneSlice(p.Decisions)
	for index := range next.Decisions {
		next.Decisions[index].Supersedes = cloneSlice(p.Decisions[index].Supersedes)
		next.Decisions[index].MilestoneIDs = cloneSlice(p.Decisions[index].MilestoneIDs)
		next.Decisions[index].SessionRefs = cloneSlice(p.Decisions[index].SessionRefs)
		if p.Decisions[index].LegacyStatusText != nil {
			value := *p.Decisions[index].LegacyStatusText
			next.Decisions[index].LegacyStatusText = &value
		}
	}
	return next
}

func cloneSlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	result := make([]T, len(values))
	copy(result, values)
	return result
}
