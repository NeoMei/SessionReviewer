package projectview

import (
	"fmt"
	"sort"
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

type sessionDerivedRecord struct {
	provider  string
	sessionID string
	record    memory.DerivedRecord
	time      time.Time
}

func copySessionDerivedRecords(views []memory.SessionView, limit int) ([]memory.DerivedRecord, map[string]struct{}, error) {
	items := make([]sessionDerivedRecord, 0)
	seenIDs := make(map[string]string)
	representedRecoveries := make(map[string]struct{})
	for _, view := range views {
		for _, record := range view.DerivedRecords {
			digest, err := memory.Digest(record)
			if err != nil {
				return nil, nil, fmt.Errorf("digest SessionView derived record %s: %w", record.ID, err)
			}
			if previous, duplicate := seenIDs[record.ID]; duplicate {
				if previous != digest {
					return nil, nil, fmt.Errorf("SessionView derived record ID %s has conflicting content", record.ID)
				}
				continue
			}
			if err := ensureRecordCapacity(len(items), 1, limit); err != nil {
				return nil, nil, fmt.Errorf("SessionView derived record limit exceeded: %w", err)
			}
			seenIDs[record.ID] = digest
			occurredAt := time.Time{}
			if record.OccurredAt != "" {
				occurredAt, _ = time.Parse(time.RFC3339Nano, record.OccurredAt)
			}
			items = append(items, sessionDerivedRecord{provider: view.Provider, sessionID: view.SessionID, record: cloneDerivedRecords([]memory.DerivedRecord{record})[0], time: occurredAt})
			if record.Kind == "recovery_link" && len(record.DependencyRevisionIDs) == 2 {
				representedRecoveries[record.DependencyRevisionIDs[0]+"\x00"+record.DependencyRevisionIDs[1]] = struct{}{}
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].time.Equal(items[j].time) {
			return items[i].time.Before(items[j].time)
		}
		if items[i].provider != items[j].provider {
			return items[i].provider < items[j].provider
		}
		if items[i].sessionID != items[j].sessionID {
			return items[i].sessionID < items[j].sessionID
		}
		return items[i].record.ID < items[j].record.ID
	})
	result := make([]memory.DerivedRecord, len(items))
	for index := range items {
		result[index] = items[index].record
	}
	return result, representedRecoveries, nil
}

func deriveRecoveryLinks(events []event, views []memory.SessionView, represented map[string]struct{}, limit int) ([]memory.DerivedRecord, error) {
	existing := make(map[string]struct{}, len(represented))
	for pair := range represented {
		existing[pair] = struct{}{}
	}
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
				if err := ensureRecordCapacity(len(result), 1, limit); err != nil {
					return nil, fmt.Errorf("recovery link limit exceeded: %w", err)
				}
				result = append(result, recoveryRecord(failure, item, key))
				existing[pair] = struct{}{}
			}
			delete(pending, key)
		}
	}
	return result, nil
}

func recoveryIdentity(summary memory.ObservationSummary) recoveryKey {
	component := normalizeIdentity(summary.Fields["component"])
	operationValue := summary.Fields["status"]
	if operationValue == "" {
		operationValue = summary.Operation
	}
	operation := normalizeIdentity(operationValue)
	return recoveryKey{kind: recoveryFactClass(summary.Kind), operation: operation, component: component}
}

func recoveryFactClass(kind string) string {
	return normalizeIdentity(kind)
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

func derivePhaseBoundaries(events []event, limit int) ([]memory.DerivedRecord, error) {
	var result []memory.DerivedRecord
	var previous *event
	lastValue := make(map[string]event)
	for index := range events {
		item := events[index]
		if previous != nil && item.time.Sub(previous.time) > 30*24*time.Hour {
			if err := ensureRecordCapacity(len(result), 1, limit); err != nil {
				return nil, fmt.Errorf("phase boundary limit exceeded: %w", err)
			}
			result = append(result, phaseBoundary(item.time.Format("2006-01-02"), "time_gap", item, []string{previous.summary.RevisionID, item.summary.RevisionID}))
		}
		switch item.summary.Kind {
		case "branch", "git_status":
			value := structuralValue(item.summary, "branch")
			if prior, exists := lastValue["branch"]; exists && structuralValue(prior.summary, "branch") != value && value != "" {
				if err := ensureRecordCapacity(len(result), 1, limit); err != nil {
					return nil, fmt.Errorf("phase boundary limit exceeded: %w", err)
				}
				result = append(result, phaseBoundary(item.time.Format("2006-01-02"), "branch_change", item, []string{prior.summary.RevisionID, item.summary.RevisionID}))
			}
			if value != "" {
				lastValue["branch"] = item
			}
			if item.summary.Kind == "git_status" {
				for _, kind := range []string{"version", "tag", "release"} {
					var err error
					result, err = appendStructuralBoundary(result, lastValue, item, kind, limit)
					if err != nil {
						return nil, err
					}
				}
			}
		case "version", "tag", "release":
			var err error
			result, err = appendStructuralBoundary(result, lastValue, item, item.summary.Kind, limit)
			if err != nil {
				return nil, err
			}
		}
		previous = &events[index]
	}
	return result, nil
}

func appendStructuralBoundary(result []memory.DerivedRecord, lastValue map[string]event, item event, kind string, limit int) ([]memory.DerivedRecord, error) {
	value := structuralValue(item.summary, kind)
	prior, exists := lastValue[kind]
	if value == "" || (exists && structuralValue(prior.summary, kind) == value) {
		return result, nil
	}
	dependencies := []string{item.summary.RevisionID}
	if exists {
		dependencies = []string{prior.summary.RevisionID, item.summary.RevisionID}
	}
	lastValue[kind] = item
	if err := ensureRecordCapacity(len(result), 1, limit); err != nil {
		return nil, fmt.Errorf("phase boundary limit exceeded: %w", err)
	}
	return append(result, phaseBoundary(value, kind+"_change", item, dependencies)), nil
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
