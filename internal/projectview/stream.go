package projectview

import (
	"container/heap"
	"sort"
	"strconv"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
)

const (
	maxWitnessedRecords        = 2048
	maxSessionRecoveryRecords  = 4096
	maxCrossRecoveryRecords    = 4096
	maxPhaseRecords            = 2048
	maxModuleRecords           = 2048
	maxModuleDependencies      = 4
	maxModuleSessions          = 256
	maxEventReferenceRecords   = 8192
	maxAggregateResidentEvents = maxWitnessedRecords + 2*maxCrossRecoveryRecords + maxPhaseRecords + maxModuleRecords + maxEventReferenceRecords + 5
)

type aggregateStats struct {
	EventsSeen     int
	RetainedEvents int
	MergeCursors   int
}

type streamAggregation struct {
	witnessed      []memory.DerivedRecord
	recoveries     []memory.DerivedRecord
	phases         []memory.DerivedRecord
	moduleRankings []memory.DerivedRecord
	eventRefs      []memory.DerivedRecord
}

type summaryCursor struct {
	viewIndex    int
	summaryIndex int
	item         event
}

type summaryHeap []summaryCursor

func (values summaryHeap) Len() int { return len(values) }
func (values summaryHeap) Less(i, j int) bool {
	left, right := values[i].item, values[j].item
	if !left.time.Equal(right.time) {
		return left.time.Before(right.time)
	}
	if left.provider != right.provider {
		return left.provider < right.provider
	}
	if left.sessionID != right.sessionID {
		return left.sessionID < right.sessionID
	}
	if left.summary.Sequence != right.summary.Sequence {
		return left.summary.Sequence < right.summary.Sequence
	}
	return left.summary.RevisionID < right.summary.RevisionID
}
func (values summaryHeap) Swap(i, j int)   { values[i], values[j] = values[j], values[i] }
func (values *summaryHeap) Push(value any) { *values = append(*values, value.(summaryCursor)) }
func (values *summaryHeap) Pop() any {
	old := *values
	last := old[len(old)-1]
	*values = old[:len(old)-1]
	return last
}

type streamingAggregator struct {
	reference    time.Time
	witnessed    map[string]memory.DerivedRecord
	pending      map[recoveryKey][]event
	pendingN     int
	recoveries   []memory.DerivedRecord
	phases       []memory.DerivedRecord
	eventRefs    []memory.DerivedRecord
	previous     *event
	structural   map[string]event
	modules      boundedModuleHeap
	moduleByPath map[string]*boundedModuleStats
}

type boundedModuleStats struct {
	path             string
	activity         int
	filesSeen        bool
	verifications    int
	changes          int
	latest           time.Time
	sessions         map[string]struct{}
	dependencies     []string
	seenDeps         map[string]struct{}
	errorUpperBound  int
	sessionsComplete bool
	heapIndex        int
}

type boundedModuleHeap []*boundedModuleStats

func (values boundedModuleHeap) Len() int { return len(values) }
func (values boundedModuleHeap) Less(i, j int) bool {
	if values[i].activity != values[j].activity {
		return values[i].activity < values[j].activity
	}
	return values[i].path > values[j].path
}
func (values boundedModuleHeap) Swap(i, j int) {
	values[i], values[j] = values[j], values[i]
	values[i].heapIndex, values[j].heapIndex = i, j
}
func (values *boundedModuleHeap) Push(value any) {
	item := value.(*boundedModuleStats)
	item.heapIndex = len(*values)
	*values = append(*values, item)
}
func (values *boundedModuleHeap) Pop() any {
	old := *values
	item := old[len(old)-1]
	item.heapIndex = -1
	*values = old[:len(old)-1]
	return item
}

func aggregateSessionViews(views []memory.SessionView, reference time.Time, observer func(aggregateStats)) (streamAggregation, error) {
	state := streamingAggregator{
		reference:    reference,
		witnessed:    make(map[string]memory.DerivedRecord),
		pending:      make(map[recoveryKey][]event),
		structural:   make(map[string]event),
		moduleByPath: make(map[string]*boundedModuleStats),
	}
	cursors := make(summaryHeap, 0, len(views))
	for viewIndex := range views {
		if len(views[viewIndex].ObservationSummaries) == 0 {
			continue
		}
		cursor := cursorAt(views, viewIndex, 0)
		cursors = append(cursors, cursor)
	}
	heap.Init(&cursors)
	eventsSeen := 0
	for cursors.Len() > 0 {
		cursor := heap.Pop(&cursors).(summaryCursor)
		state.accept(cursor.item)
		eventsSeen++
		if next := cursor.summaryIndex + 1; next < len(views[cursor.viewIndex].ObservationSummaries) {
			heap.Push(&cursors, cursorAt(views, cursor.viewIndex, next))
		}
		if observer != nil {
			observer(aggregateStats{EventsSeen: eventsSeen, RetainedEvents: state.residentCount(), MergeCursors: cursors.Len()})
		}
	}
	witnessedKeys := make([]string, 0, len(state.witnessed))
	for key := range state.witnessed {
		witnessedKeys = append(witnessedKeys, key)
	}
	sort.Strings(witnessedKeys)
	witnessed := make([]memory.DerivedRecord, 0, len(witnessedKeys))
	for _, key := range witnessedKeys {
		witnessed = append(witnessed, state.witnessed[key])
	}
	return streamAggregation{
		witnessed:      witnessed,
		recoveries:     state.recoveries,
		phases:         state.phases,
		moduleRankings: state.rankModules(),
		eventRefs:      state.eventRefs,
	}, nil
}

func cursorAt(views []memory.SessionView, viewIndex, summaryIndex int) summaryCursor {
	view := &views[viewIndex]
	summary := view.ObservationSummaries[summaryIndex]
	instant, _ := time.Parse(time.RFC3339Nano, summary.OccurredAt)
	return summaryCursor{viewIndex: viewIndex, summaryIndex: summaryIndex, item: event{provider: view.Provider, sessionID: view.SessionID, summary: summary, time: instant}}
}

func (state *streamingAggregator) accept(item event) {
	state.acceptWitnessed(item)
	state.acceptRecovery(item)
	state.acceptPhase(item)
	state.acceptModule(item)
	if len(state.eventRefs) < maxEventReferenceRecords {
		records, _ := deriveEventReferences([]event{item}, 1)
		state.eventRefs = append(state.eventRefs, records...)
	}
}

func (state *streamingAggregator) acceptWitnessed(item event) {
	for _, witnessed := range witnessedValues(item.summary) {
		if _, exists := state.witnessed[witnessed.key]; !exists && len(state.witnessed) >= maxWitnessedRecords {
			continue
		}
		witnessed.fields["value"] = witnessed.value
		subject := witnessed.key
		if len(subject) > 256 {
			subject = derivedID("witness-key", witnessed.key)
		}
		state.witnessed[witnessed.key] = memory.DerivedRecord{
			ID: derivedID("witness", witnessed.key, item.summary.RevisionID), Kind: "witnessed_state", Subject: subject,
			OccurredAt: item.summary.OccurredAt, DependencyRevisionIDs: []string{item.summary.RevisionID},
			RuleID: "newest-observed-state", RuleVersion: ReducerVersion, Fields: witnessed.fields,
		}
	}
}

func (state *streamingAggregator) acceptRecovery(item event) {
	key := recoveryIdentity(item.summary)
	if key.operation == "" || key.component == "" {
		return
	}
	switch normalizedOutcome(item.summary.Outcome) {
	case "failure":
		if state.pendingN < maxCrossRecoveryRecords {
			state.pending[key] = append(state.pending[key], item)
			state.pendingN++
		}
	case "success":
		for _, failure := range state.pending[key] {
			if len(state.recoveries) >= maxCrossRecoveryRecords {
				break
			}
			if failure.sessionID == item.sessionID && failure.provider == item.provider {
				continue
			}
			state.recoveries = append(state.recoveries, recoveryRecord(failure, item, key))
		}
		state.pendingN -= len(state.pending[key])
		delete(state.pending, key)
	}
}

func (state *streamingAggregator) acceptPhase(item event) {
	appendBoundary := func(record memory.DerivedRecord) {
		if len(state.phases) < maxPhaseRecords {
			state.phases = append(state.phases, record)
		}
	}
	if state.previous != nil && item.time.Sub(state.previous.time) > 30*24*time.Hour {
		appendBoundary(phaseBoundary(item.time.Format("2006-01-02"), "time_gap", item, []string{state.previous.summary.RevisionID, item.summary.RevisionID}))
	}
	kinds := []string{}
	switch item.summary.Kind {
	case "branch":
		kinds = []string{"branch"}
	case "git_status":
		kinds = []string{"branch", "version", "tag", "release"}
	case "version", "tag", "release":
		kinds = []string{item.summary.Kind}
	}
	for _, kind := range kinds {
		value := structuralValue(item.summary, kind)
		prior, exists := state.structural[kind]
		if value == "" || (exists && structuralValue(prior.summary, kind) == value) {
			continue
		}
		if kind == "branch" && !exists {
			state.structural[kind] = item
			continue
		}
		dependencies := []string{item.summary.RevisionID}
		if exists {
			dependencies = []string{prior.summary.RevisionID, item.summary.RevisionID}
		}
		state.structural[kind] = item
		subject := value
		if kind == "branch" {
			subject = item.time.Format("2006-01-02")
		}
		appendBoundary(phaseBoundary(subject, kind+"_change", item, dependencies))
	}
	copy := item
	state.previous = &copy
}

func (state *streamingAggregator) acceptModule(item event) {
	var modulePath string
	isFile := item.summary.Kind == "file"
	isVerification := item.summary.Kind == "verification"
	if isFile {
		modulePath = normalizedPath(item.summary.Fields["path"])
		if modulePath == "" {
			modulePath = normalizedPath(item.summary.Object)
		}
	} else if isVerification {
		modulePath = normalizedPath(item.summary.Fields["component"])
	}
	if modulePath == "" {
		return
	}
	stats := state.moduleByPath[modulePath]
	if stats == nil {
		base := 0
		if len(state.modules) >= maxModuleRecords {
			evicted := heap.Pop(&state.modules).(*boundedModuleStats)
			delete(state.moduleByPath, evicted.path)
			base = evicted.activity
		}
		stats = &boundedModuleStats{path: modulePath, activity: base, errorUpperBound: base, sessionsComplete: true, sessions: make(map[string]struct{}), seenDeps: make(map[string]struct{})}
		state.moduleByPath[modulePath] = stats
		heap.Push(&state.modules, stats)
	}
	stats.activity++
	if len(stats.sessions) < maxModuleSessions {
		stats.sessions[item.provider+"\x00"+item.sessionID] = struct{}{}
	} else if _, exists := stats.sessions[item.provider+"\x00"+item.sessionID]; !exists {
		stats.sessionsComplete = false
	}
	if isFile {
		stats.filesSeen = true
		if item.summary.Operation == "file_change" && item.summary.Outcome == "success" {
			stats.changes++
		}
	}
	if isVerification {
		stats.verifications++
	}
	if item.time.After(stats.latest) {
		stats.latest = item.time
	}
	if len(stats.dependencies) < maxModuleDependencies {
		if _, duplicate := stats.seenDeps[item.summary.RevisionID]; !duplicate {
			stats.seenDeps[item.summary.RevisionID] = struct{}{}
			stats.dependencies = append(stats.dependencies, item.summary.RevisionID)
		}
	}
	heap.Fix(&state.modules, stats.heapIndex)
}

func (state *streamingAggregator) rankModules() []memory.DerivedRecord {
	values := make([]*boundedModuleStats, 0, len(state.modules))
	for _, stats := range state.modules {
		if stats.filesSeen {
			values = append(values, stats)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		left := 4*len(values[i].sessions) + 2*values[i].verifications + values[i].changes + recencyBucket(state.reference, values[i].latest)
		right := 4*len(values[j].sessions) + 2*values[j].verifications + values[j].changes + recencyBucket(state.reference, values[j].latest)
		if left != right {
			return left > right
		}
		return values[i].path < values[j].path
	})
	result := make([]memory.DerivedRecord, 0, len(values))
	for index, stats := range values {
		score := 4*len(stats.sessions) + 2*stats.verifications + stats.changes + recencyBucket(state.reference, stats.latest)
		subject := stats.path
		if len(subject) > 256 {
			subject = derivedID("module", subject)
		}
		complete := stats.errorUpperBound == 0 && stats.sessionsComplete
		result = append(result, memory.DerivedRecord{
			ID: derivedID("module-rank", stats.path), Kind: "module_rank", Subject: subject,
			OccurredAt: stats.latest.UTC().Format(time.RFC3339Nano), DependencyRevisionIDs: append([]string(nil), stats.dependencies...),
			RuleID: "bounded-module-score", RuleVersion: ReducerVersion,
			Fields: map[string]string{
				"path": stats.path, "rank": strconv.Itoa(index + 1), "score": strconv.Itoa(score),
				"session_coverage": strconv.Itoa(len(stats.sessions)), "verification_count": strconv.Itoa(stats.verifications),
				"change_count": strconv.Itoa(stats.changes), "recency_bucket": strconv.Itoa(recencyBucket(state.reference, stats.latest)),
				"latest_observed_at":  stats.latest.UTC().Format(time.RFC3339Nano),
				"candidate_algorithm": "space_saving", "estimated_activity": strconv.Itoa(stats.activity),
				"error_upper_bound": strconv.Itoa(stats.errorUpperBound), "counts_complete": strconv.FormatBool(complete),
			},
		})
	}
	return result
}

func (state *streamingAggregator) residentCount() int {
	return len(state.witnessed) + state.pendingN + len(state.recoveries) + len(state.phases) + len(state.eventRefs) + len(state.modules) + len(state.structural) + 1
}
