package annotation

import (
	"errors"
	"reflect"
	"time"
)

func validateStoreTransition(current, next StoreRecord, existed bool) error {
	if err := Validate(next); err != nil {
		return err
	}
	if err := validateStoreRecordSemantics(next); err != nil {
		return err
	}
	if !existed {
		for _, candidate := range next.Annotations {
			if candidate.Revision != 1 || candidate.Status != CandidatePending {
				return errors.New("new candidate must begin pending at revision 1")
			}
		}
		return nil
	}
	if current.ProjectID != next.ProjectID || current.SchemaVersion != next.SchemaVersion || current.MinimumReaderVersion != next.MinimumReaderVersion || len(next.Annotations) < len(current.Annotations) || len(next.ExtractionRuns) < len(current.ExtractionRuns) {
		return errors.New("annotation store transition removed or changed existing identity")
	}
	for index := range current.ExtractionRuns {
		if err := validateRunTransition(current.ExtractionRuns[index], next.ExtractionRuns[index]); err != nil {
			return err
		}
	}
	for _, run := range next.ExtractionRuns[len(current.ExtractionRuns):] {
		if run.Status != "pending" && run.Status != "running" && run.Status != "completed" && run.Status != "failed" && run.Status != "cancelled" {
			return errors.New("new extraction run has invalid state")
		}
	}
	for index := range current.Annotations {
		if err := validateCandidateTransition(current.Annotations[index], next.Annotations[index]); err != nil {
			return err
		}
	}
	for _, candidate := range next.Annotations[len(current.Annotations):] {
		if candidate.Revision != 1 || candidate.Status != CandidatePending {
			return errors.New("new candidate must begin pending at revision 1")
		}
	}
	return nil
}

func validateStoreRecordSemantics(record StoreRecord) error {
	runs := make(map[string]Run, len(record.ExtractionRuns))
	for _, run := range record.ExtractionRuns {
		created, createdErr := time.Parse(time.RFC3339Nano, run.CreatedAt)
		updated, updatedErr := time.Parse(time.RFC3339Nano, run.UpdatedAt)
		if createdErr != nil || updatedErr != nil || updated.Before(created) {
			return errors.New("extraction run timestamps are invalid")
		}
		runs[run.RunID] = run
	}
	for _, candidate := range record.Annotations {
		if _, err := time.Parse(time.RFC3339Nano, candidate.CreatedAt); err != nil {
			return errors.New("candidate creation timestamp is invalid")
		}
		run, ok := runs[candidate.AgentRunID]
		if !ok || run.Status != "completed" {
			return errors.New("candidate must reference a completed extraction run")
		}
		if candidate.AnnotationKind == "milestone_conclusion_candidate" && candidate.Status == CandidateNotDecision {
			return errors.New("milestone candidate cannot be marked not_decision")
		}
	}
	return nil
}

func validateRunTransition(current, next Run) error {
	left, right := current, next
	left.Status, right.Status = "", ""
	left.UpdatedAt, right.UpdatedAt = "", ""
	if !reflect.DeepEqual(left, right) {
		return errors.New("extraction run immutable fields changed")
	}
	allowed := current.Status == next.Status ||
		current.Status == "pending" && (next.Status == "running" || next.Status == "cancelled") ||
		current.Status == "running" && (next.Status == "completed" || next.Status == "failed" || next.Status == "cancelled")
	if !allowed {
		return errors.New("extraction run transition is not allowed")
	}
	currentUpdated, currentErr := time.Parse(time.RFC3339Nano, current.UpdatedAt)
	nextUpdated, nextErr := time.Parse(time.RFC3339Nano, next.UpdatedAt)
	if currentErr != nil || nextErr != nil || nextUpdated.Before(currentUpdated) {
		return errors.New("extraction run updated time cannot move backwards")
	}
	if current.Status == next.Status && current.UpdatedAt != next.UpdatedAt {
		return errors.New("unchanged extraction run cannot rewrite updated time")
	}
	return nil
}

func validateCandidateTransition(current, next Annotation) error {
	left, right := current, next
	left.Status, right.Status = "", ""
	left.Revision, right.Revision = 0, 0
	left.ConfirmedEntityID, right.ConfirmedEntityID = nil, nil
	if !reflect.DeepEqual(left, right) {
		return errors.New("candidate immutable fields changed")
	}
	if current.Status == next.Status {
		if current.Revision != next.Revision || !sameOptionalString(current.ConfirmedEntityID, next.ConfirmedEntityID) {
			return errors.New("unchanged candidate revision changed")
		}
		return nil
	}
	if next.Revision != current.Revision+1 {
		return errors.New("candidate revision must advance exactly once")
	}
	allowed := current.Status == CandidatePending && (next.Status == CandidateConfirmed || next.Status == CandidateIgnored || next.Status == CandidateNotDecision || next.Status == CandidateStale) ||
		current.Status == CandidateIgnored && (next.Status == CandidatePending || next.Status == CandidateStale)
	if !allowed {
		return errors.New("candidate transition is not allowed")
	}
	return nil
}

func sameOptionalString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
