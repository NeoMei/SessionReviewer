// Package projectview deterministically reduces validated project SessionViews
// and one read-only probe into a durable ProjectView. It never opens source
// Sessions or invokes a model, command, or network service.
package projectview

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
)

const ReducerVersion = "project-view-v1"

// Input contains the complete, generation-frozen reducer boundary. ReferenceTime
// is an explicit UTC RFC3339 timestamp used only by documented recency buckets.
type Input struct {
	ProjectID       string
	SessionViews    []memory.SessionView
	ProbeState      memory.ProjectProbeState
	AssociatedUsage []memory.AssociatedUsage
	Previous        *memory.ProjectView
	ReducerVersion  string
	ReferenceTime   string
}

type event struct {
	provider  string
	sessionID string
	summary   memory.ObservationSummary
	time      time.Time
}

// Reduce creates one dependency-bound ProjectView. changed is false only when
// the validated previous view has the exact same deterministic input digest.
func Reduce(input Input) (memory.ProjectView, bool, error) {
	referenceTime, err := validateInputEnvelope(input)
	if err != nil {
		return memory.ProjectView{}, false, err
	}

	views := append([]memory.SessionView(nil), input.SessionViews...)
	sort.Slice(views, func(i, j int) bool {
		if views[i].Provider != views[j].Provider {
			return views[i].Provider < views[j].Provider
		}
		return views[i].SessionID < views[j].SessionID
	})
	if err := validateViews(input.ProjectID, views, referenceTime); err != nil {
		return memory.ProjectView{}, false, err
	}

	usage, err := reconcileUsage(views, input.AssociatedUsage)
	if err != nil {
		return memory.ProjectView{}, false, err
	}
	dependencies := make([]memory.SessionViewDependency, 0, len(views))
	counts := memory.TerminalCounts{}
	for _, view := range views {
		dependencies = append(dependencies, memory.SessionViewDependency{Provider: view.Provider, SessionID: view.SessionID, Digest: view.Digest})
		incrementTerminalCount(&counts, view.TerminalState)
	}

	events, revisionIDs, err := collectEvents(views)
	if err != nil {
		return memory.ProjectView{}, false, err
	}
	dependencyDigest, err := memory.Digest(struct {
		SessionViews   []memory.SessionViewDependency `json:"session_views"`
		ProbeState     string                         `json:"probe_state_digest"`
		Usage          []memory.AssociatedUsage       `json:"associated_usage"`
		ReducerVersion string                         `json:"reducer_version"`
		ReferenceTime  string                         `json:"reference_time"`
	}{dependencies, input.ProbeState.Digest, usage, input.ReducerVersion, input.ReferenceTime})
	if err != nil {
		return memory.ProjectView{}, false, fmt.Errorf("digest ProjectView dependencies: %w", err)
	}

	witnessed := deriveWitnessedState(events)
	derived := make([]memory.DerivedRecord, 0, len(events)+len(events)/2)
	derived = append(derived, deriveEventReferences(events)...)
	derived = append(derived, deriveRecoveryLinks(events, views)...)
	derived = append(derived, derivePhaseBoundaries(events)...)
	derived = append(derived, rankModules(events, referenceTime)...)
	if len(derived) > 65536 {
		return memory.ProjectView{}, false, errors.New("ProjectView derived record limit exceeded")
	}

	generation := 1
	previousDigest := ""
	if input.Previous != nil {
		if input.Previous.DependencyDigest == dependencyDigest {
			generation = input.Previous.Generation
			previousDigest = input.Previous.PreviousViewDigest
		} else {
			generation = input.Previous.Generation + 1
			previousDigest = input.Previous.Digest
		}
	}
	view := memory.ProjectView{
		SchemaVersion:           memory.MemorySchemaVersion,
		ProjectID:               input.ProjectID,
		Generation:              generation,
		StartedAt:               views[0].StartedAt,
		EndedAt:                 views[0].EndedAt,
		SourceSessions:          len(views),
		TerminalCounts:          counts,
		SessionViewDependencies: dependencies,
		ObservationRevisionIDs:  revisionIDs,
		ProbeStateDigest:        input.ProbeState.Digest,
		LiveState: memory.StateSnapshot{
			Branch: input.ProbeState.Branch, Head: input.ProbeState.Head, DirtyPathCount: input.ProbeState.DirtyPathCount,
		},
		WitnessedState:     witnessed,
		DerivedRecords:     derived,
		AssociatedUsage:    usage,
		PreviousViewDigest: previousDigest,
		DependencyDigest:   dependencyDigest,
		ReducerVersion:     input.ReducerVersion,
	}
	startedInstant, _ := time.Parse(time.RFC3339Nano, view.StartedAt)
	endedInstant, _ := time.Parse(time.RFC3339Nano, view.EndedAt)
	for _, session := range views[1:] {
		sessionStarted, _ := time.Parse(time.RFC3339Nano, session.StartedAt)
		sessionEnded, _ := time.Parse(time.RFC3339Nano, session.EndedAt)
		if sessionStarted.Before(startedInstant) {
			view.StartedAt = session.StartedAt
			startedInstant = sessionStarted
		}
		if sessionEnded.After(endedInstant) {
			view.EndedAt = session.EndedAt
			endedInstant = sessionEnded
		}
	}
	view.Digest, err = memory.ProjectViewDigest(view)
	if err != nil {
		return memory.ProjectView{}, false, fmt.Errorf("digest ProjectView: %w", err)
	}
	if err := memory.ValidateProjectView(view); err != nil {
		return memory.ProjectView{}, false, fmt.Errorf("validate ProjectView: %w", err)
	}
	if input.Previous != nil && input.Previous.DependencyDigest == dependencyDigest {
		if input.Previous.Digest == view.Digest {
			return cloneProjectView(*input.Previous), false, nil
		}
		view.Generation = input.Previous.Generation + 1
		view.PreviousViewDigest = input.Previous.Digest
		view.Digest, err = memory.ProjectViewDigest(view)
		if err != nil {
			return memory.ProjectView{}, false, fmt.Errorf("digest corrected ProjectView: %w", err)
		}
		if err := memory.ValidateProjectView(view); err != nil {
			return memory.ProjectView{}, false, fmt.Errorf("validate corrected ProjectView: %w", err)
		}
	}
	return view, true, nil
}

func validateInputEnvelope(input Input) (time.Time, error) {
	if input.ReducerVersion != ReducerVersion {
		return time.Time{}, fmt.Errorf("unsupported reducer version %q", input.ReducerVersion)
	}
	referenceTime, err := time.Parse(time.RFC3339Nano, input.ReferenceTime)
	if err != nil || referenceTime.Location() != time.UTC || referenceTime.Format(time.RFC3339Nano) != input.ReferenceTime {
		return time.Time{}, errors.New("reference time must be canonical UTC RFC3339")
	}
	if len(input.SessionViews) == 0 {
		return time.Time{}, errors.New("project reduction requires at least one terminal SessionView")
	}
	if err := memory.ValidateProjectProbeState(input.ProbeState); err != nil {
		return time.Time{}, fmt.Errorf("validate ProjectProbeState: %w", err)
	}
	if input.ProbeState.ProjectID != input.ProjectID {
		return time.Time{}, errors.New("ProjectProbeState belongs to another project")
	}
	if input.Previous != nil {
		if err := memory.ValidateProjectView(*input.Previous); err != nil {
			return time.Time{}, fmt.Errorf("validate previous ProjectView: %w", err)
		}
		if input.Previous.ProjectID != input.ProjectID || input.Previous.ReducerVersion != input.ReducerVersion {
			return time.Time{}, errors.New("previous ProjectView identity does not match reducer input")
		}
	}
	return referenceTime, nil
}

func validateViews(projectID string, views []memory.SessionView, referenceTime time.Time) error {
	for index, view := range views {
		if err := memory.ValidateSessionView(view); err != nil {
			return fmt.Errorf("validate SessionView %d: %w", index, err)
		}
		if view.ProjectID != projectID {
			return fmt.Errorf("SessionView %s belongs to another project", view.SessionID)
		}
		if index > 0 && views[index-1].Provider == view.Provider && views[index-1].SessionID == view.SessionID {
			return fmt.Errorf("duplicate SessionView identity %s/%s", view.Provider, view.SessionID)
		}
		startedAt, _ := time.Parse(time.RFC3339Nano, view.StartedAt)
		endedAt, err := time.Parse(time.RFC3339Nano, view.EndedAt)
		if err != nil || endedAt.After(referenceTime) {
			return fmt.Errorf("reference time precedes SessionView %s", view.SessionID)
		}
		for _, summary := range view.ObservationSummaries {
			occurredAt, err := time.Parse(time.RFC3339Nano, summary.OccurredAt)
			if err != nil || occurredAt.After(referenceTime) {
				return fmt.Errorf("reference time precedes observation %s", summary.RevisionID)
			}
			if occurredAt.Before(startedAt) || occurredAt.After(endedAt) {
				return fmt.Errorf("observation %s is outside SessionView time range", summary.RevisionID)
			}
		}
	}
	return nil
}

func reconcileUsage(views []memory.SessionView, supplied []memory.AssociatedUsage) ([]memory.AssociatedUsage, error) {
	usage := append([]memory.AssociatedUsage(nil), supplied...)
	sort.Slice(usage, func(i, j int) bool {
		if usage[i].Provider != usage[j].Provider {
			return usage[i].Provider < usage[j].Provider
		}
		return usage[i].SessionID < usage[j].SessionID
	})
	if len(usage) != len(views) {
		return nil, errors.New("associated usage does not exactly cover SessionViews")
	}
	for index := range views {
		if usage[index].Provider != views[index].Provider || usage[index].SessionID != views[index].SessionID {
			return nil, errors.New("associated usage identity does not match SessionView")
		}
		if usage[index].UsageRecordDigest != views[index].UsageRecordDigest {
			return nil, errors.New("associated usage digest does not match authenticated SessionView reference")
		}
	}
	return usage, nil
}

func collectEvents(views []memory.SessionView) ([]event, []string, error) {
	var events []event
	seen := make(map[string]string)
	for _, view := range views {
		for _, summary := range view.ObservationSummaries {
			provenance := view.Provider + "\x00" + view.SessionID
			if previous, duplicate := seen[summary.RevisionID]; duplicate {
				return nil, nil, fmt.Errorf("observation revision %s appears in both %s and %s", summary.RevisionID, previous, provenance)
			}
			seen[summary.RevisionID] = provenance
			occurredAt, _ := time.Parse(time.RFC3339Nano, summary.OccurredAt)
			events = append(events, event{provider: view.Provider, sessionID: view.SessionID, summary: cloneSummary(summary), time: occurredAt})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if !events[i].time.Equal(events[j].time) {
			return events[i].time.Before(events[j].time)
		}
		if events[i].sessionID != events[j].sessionID {
			return events[i].sessionID < events[j].sessionID
		}
		if events[i].summary.Sequence != events[j].summary.Sequence {
			return events[i].summary.Sequence < events[j].summary.Sequence
		}
		return events[i].summary.RevisionID < events[j].summary.RevisionID
	})
	revisions := make([]string, len(events))
	for index := range events {
		revisions[index] = events[index].summary.RevisionID
	}
	return events, revisions, nil
}

func deriveEventReferences(events []event) []memory.DerivedRecord {
	result := make([]memory.DerivedRecord, 0, len(events))
	for _, item := range events {
		fields := map[string]string{
			"provider": item.provider, "session_id": item.sessionID, "sequence": strconv.Itoa(item.summary.Sequence), "fact_kind": item.summary.Kind,
		}
		copyNonEmpty(fields, "operation", item.summary.Operation)
		copyNonEmpty(fields, "object", item.summary.Object)
		copyNonEmpty(fields, "outcome", item.summary.Outcome)
		for _, name := range []string{"artifact_id", "branch", "component", "error_signature", "git_head", "path", "release_id", "status", "tag", "version"} {
			copyNonEmpty(fields, name, item.summary.Fields[name])
		}
		if item.summary.Excerpt != "" {
			copyNonEmpty(fields, "excerpt", boundedField(item.summary.Excerpt, 512))
		}
		result = append(result, memory.DerivedRecord{
			ID: derivedID("event", item.sessionID, item.summary.RevisionID), Kind: "event_ref", Subject: item.summary.Subject,
			OccurredAt: item.summary.OccurredAt, DependencyRevisionIDs: []string{item.summary.RevisionID},
			RuleID: "typed-event-order", RuleVersion: ReducerVersion, Fields: fields,
		})
	}
	return result
}

func deriveWitnessedState(events []event) []memory.DerivedRecord {
	latest := make(map[string]memory.DerivedRecord)
	order := make([]string, 0)
	for _, item := range events {
		for _, witnessed := range witnessedValues(item.summary) {
			if _, exists := latest[witnessed.key]; !exists {
				order = append(order, witnessed.key)
			}
			witnessed.fields["value"] = witnessed.value
			subject := witnessed.key
			if len(subject) > 256 || utf8.RuneCountInString(subject) > 256 {
				subject = derivedID("witness-key", witnessed.key)
			}
			latest[witnessed.key] = memory.DerivedRecord{
				ID: derivedID("witness", witnessed.key, item.summary.RevisionID), Kind: "witnessed_state", Subject: subject,
				OccurredAt: item.summary.OccurredAt, DependencyRevisionIDs: []string{item.summary.RevisionID},
				RuleID: "newest-observed-state", RuleVersion: ReducerVersion, Fields: witnessed.fields,
			}
		}
	}
	sort.Strings(order)
	result := make([]memory.DerivedRecord, 0, len(order))
	for _, key := range order {
		result = append(result, latest[key])
	}
	return result
}

type witnessedFact struct {
	key    string
	value  string
	fields map[string]string
}

func witnessedValues(summary memory.ObservationSummary) []witnessedFact {
	var result []witnessedFact
	add := func(key, value string, fields map[string]string) {
		if key != "" && value != "" {
			result = append(result, witnessedFact{key: key, value: value, fields: fields})
		}
	}
	switch summary.Kind {
	case "branch", "git_status":
		branch := summary.Fields["branch"]
		if branch == "" && summary.Kind == "branch" {
			branch = summary.Subject
		}
		add("branch", branch, map[string]string{"fact_kind": summary.Kind})
		add("git_status", summary.Fields["status"], map[string]string{"fact_kind": summary.Kind})
		add("head", summary.Fields["git_head"], map[string]string{"fact_kind": summary.Kind})
	case "version":
		version := summary.Fields["version"]
		if version == "" {
			version = summary.Subject
		}
		add("version", version, map[string]string{"fact_kind": summary.Kind})
	case "commit":
		add("head", summary.Fields["git_head"], map[string]string{"fact_kind": summary.Kind})
	case "verification":
		component := normalizeIdentity(summary.Fields["component"])
		if component == "" {
			component = normalizeIdentity(summary.Object)
		}
		if component != "" && summary.Outcome != "" {
			add("verification:"+component, normalizeIdentity(summary.Outcome), map[string]string{"fact_kind": summary.Kind, "outcome": normalizeIdentity(summary.Outcome), "component": component})
		}
	}
	return result
}

func incrementTerminalCount(counts *memory.TerminalCounts, state memory.TerminalState) {
	switch state {
	case memory.Indexed:
		counts.Indexed++
	case memory.Unsupported:
		counts.Unsupported++
	case memory.Missing:
		counts.Missing++
	case memory.Unreadable:
		counts.Unreadable++
	case memory.Ambiguous:
		counts.Ambiguous++
	}
}

func derivedID(prefix string, parts ...string) string {
	digest, _ := memory.Digest(struct {
		Prefix string   `json:"prefix"`
		Parts  []string `json:"parts"`
	}{prefix, parts})
	return prefix + "-" + strings.TrimPrefix(digest, "sha256:")[:32]
}

func normalizeIdentity(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func copyNonEmpty(fields map[string]string, name, value string) {
	if value != "" {
		fields[name] = value
	}
}

func boundedField(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.ValidString(value[:maximum]) {
		maximum--
	}
	return value[:maximum]
}

func cloneSummary(value memory.ObservationSummary) memory.ObservationSummary {
	value.Fields = cloneFields(value.Fields)
	return value
}

func cloneFields(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneDerivedRecords(values []memory.DerivedRecord) []memory.DerivedRecord {
	if values == nil {
		return nil
	}
	result := make([]memory.DerivedRecord, len(values))
	for index, value := range values {
		result[index] = value
		result[index].DependencyRevisionIDs = append([]string(nil), value.DependencyRevisionIDs...)
		result[index].Fields = cloneFields(value.Fields)
	}
	return result
}

func cloneProjectView(value memory.ProjectView) memory.ProjectView {
	value.SessionViewDependencies = append([]memory.SessionViewDependency(nil), value.SessionViewDependencies...)
	value.ObservationRevisionIDs = append([]string(nil), value.ObservationRevisionIDs...)
	value.WitnessedState = cloneDerivedRecords(value.WitnessedState)
	value.DerivedRecords = cloneDerivedRecords(value.DerivedRecords)
	value.AssociatedUsage = append([]memory.AssociatedUsage(nil), value.AssociatedUsage...)
	return value
}
