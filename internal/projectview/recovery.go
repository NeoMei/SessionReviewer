package projectview

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
)

type recoveryKey struct {
	kind      string
	operation string
	component string
}

func deriveRecoveryLinks(events []event, views []memory.SessionView) []memory.DerivedRecord {
	existing := make(map[string]struct{})
	for _, view := range views {
		for _, record := range view.DerivedRecords {
			if record.Kind == "recovery_link" && len(record.DependencyRevisionIDs) == 2 {
				existing[record.DependencyRevisionIDs[0]+"\x00"+record.DependencyRevisionIDs[1]] = struct{}{}
			}
		}
	}
	pending := make(map[recoveryKey][]event)
	var result []memory.DerivedRecord
	for _, item := range events {
		key := recoveryIdentity(item.summary)
		if key.operation == "" || key.component == "" {
			continue
		}
		switch normalizedOutcome(item.summary.Outcome) {
		case "failure":
			pending[key] = append(pending[key], item)
		case "success":
			for _, failure := range pending[key] {
				if failure.sessionID == item.sessionID && failure.provider == item.provider {
					continue
				}
				pair := failure.summary.RevisionID + "\x00" + item.summary.RevisionID
				if _, alreadyDerived := existing[pair]; alreadyDerived {
					continue
				}
				result = append(result, recoveryRecord(failure, item, key))
				existing[pair] = struct{}{}
			}
			delete(pending, key)
		}
	}
	return result
}

func recoveryIdentity(summary memory.ObservationSummary) recoveryKey {
	component := normalizeIdentity(summary.Fields["component"])
	if component == "" {
		component = normalizeIdentity(summary.Object)
	}
	return recoveryKey{kind: normalizeIdentity(summary.Kind), operation: normalizeIdentity(summary.Operation), component: component}
}

func normalizedOutcome(value string) string {
	switch normalizeIdentity(value) {
	case "failed", "failure", "error":
		return "failure"
	case "passed", "success":
		return "success"
	default:
		return ""
	}
}

func recoveryRecord(failure, success event, key recoveryKey) memory.DerivedRecord {
	subject := key.operation + ":" + key.component
	if len(subject) > 256 || utf8.RuneCountInString(subject) > 256 {
		subject = derivedID("recovery", failure.summary.RevisionID, success.summary.RevisionID)
	}
	return memory.DerivedRecord{
		ID: derivedID("recovery", failure.summary.RevisionID, success.summary.RevisionID), Kind: "recovery_link", Subject: subject,
		OccurredAt: success.summary.OccurredAt, DependencyRevisionIDs: []string{failure.summary.RevisionID, success.summary.RevisionID},
		RuleID: "matching-operation-component-kind", RuleVersion: ReducerVersion,
		Fields: map[string]string{"operation": key.operation, "component": key.component, "fact_kind": key.kind, "outcome": "recovered"},
	}
}

func derivePhaseBoundaries(events []event) []memory.DerivedRecord {
	var result []memory.DerivedRecord
	var previous *event
	lastValue := make(map[string]event)
	for index := range events {
		item := events[index]
		if previous != nil && item.time.Sub(previous.time) > 30*24*time.Hour {
			result = append(result, phaseBoundary(item.time.Format("2006-01-02"), "time_gap", item, []string{previous.summary.RevisionID, item.summary.RevisionID}))
		}
		switch item.summary.Kind {
		case "branch", "git_status":
			value := structuralValue(item.summary, "branch")
			if prior, exists := lastValue["branch"]; exists && structuralValue(prior.summary, "branch") != value && value != "" {
				result = append(result, phaseBoundary(item.time.Format("2006-01-02"), "branch_change", item, []string{prior.summary.RevisionID, item.summary.RevisionID}))
			}
			if value != "" {
				lastValue["branch"] = item
			}
			if item.summary.Kind == "git_status" {
				for _, kind := range []string{"version", "tag", "release"} {
					result = appendStructuralBoundary(result, lastValue, item, kind)
				}
			}
		case "version", "tag", "release":
			result = appendStructuralBoundary(result, lastValue, item, item.summary.Kind)
		}
		previous = &events[index]
	}
	return result
}

func appendStructuralBoundary(result []memory.DerivedRecord, lastValue map[string]event, item event, kind string) []memory.DerivedRecord {
	value := structuralValue(item.summary, kind)
	prior, exists := lastValue[kind]
	if value == "" || (exists && structuralValue(prior.summary, kind) == value) {
		return result
	}
	dependencies := []string{item.summary.RevisionID}
	if exists {
		dependencies = []string{prior.summary.RevisionID, item.summary.RevisionID}
	}
	lastValue[kind] = item
	return append(result, phaseBoundary(value, kind+"_change", item, dependencies))
}

func structuralValue(summary memory.ObservationSummary, kind string) string {
	value := strings.TrimSpace(summary.Fields[kind])
	if kind == "release" && value == "" {
		value = strings.TrimSpace(summary.Fields["release_id"])
	}
	if value == "" && summary.Kind == kind {
		value = strings.TrimSpace(summary.Subject)
	}
	return value
}

func phaseBoundary(name, trigger string, item event, dependencies []string) memory.DerivedRecord {
	if len(name) > 256 || utf8.RuneCountInString(name) > 256 {
		name = item.time.Format("2006-01-02")
	}
	return memory.DerivedRecord{
		ID: derivedID("phase", trigger, item.summary.RevisionID), Kind: "phase_boundary", Subject: name,
		OccurredAt: item.summary.OccurredAt, DependencyRevisionIDs: append([]string(nil), dependencies...),
		RuleID: "structural-boundary", RuleVersion: ReducerVersion, Fields: map[string]string{"trigger": trigger},
	}
}
