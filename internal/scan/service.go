// Package scan orchestrates deterministic, zero-token project scans into one
// complete private prepared generation. It does not project or publish human
// files and never invokes an Agent or model.
package scan

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/projectprobe"
	"github.com/neomei/SessionReviewer/internal/projectview"
	"github.com/neomei/SessionReviewer/internal/sessionview"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

const (
	resultSchemaVersion = 1
	maxScanSources      = 65536
	maxSourceRevisions  = 65536
	scanLockPoll        = 20 * time.Millisecond
)

var ErrObservationBudget = errors.New("project observation budget exceeded")

var scanIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type MaterializeFunc func(sessionview.Input) (memory.SessionView, bool, error)
type ProbeFunc func(context.Context, projectprobe.Options) (memory.ProjectProbeState, memory.ProbeCheck, error)
type ReduceFunc func(projectview.Input) (memory.ProjectView, bool, error)

type MemoryStore interface {
	PutObservationChunk([]memory.ObservationRevision) (string, error)
	PutSessionView(memory.SessionView) (string, error)
	PutSessionLineage(memory.SessionLineage) (string, error)
	PutProbeState(memory.ProjectProbeState) (string, error)
	PutProjectView(memory.ProjectView) (string, error)
	LoadPrepared() (memorystore.Prepared, memory.GenerationManifest, error)
	LoadObject(memorystore.ObjectKind, string) ([]byte, error)
	PrepareGeneration(memory.GenerationManifest) (memorystore.Prepared, error)
	AdvancePrepared(memorystore.Prepared, memory.GenerationManifest) (memorystore.Prepared, error)
}

type Options struct {
	ProjectID     string
	Binding       projectidentity.Binding
	SessionsRoot  string
	DataRoot      string
	Adapter       source.Adapter
	Catalog       *sourcecatalog.Catalog
	Store         MemoryStore
	Workers       int
	Now           func() time.Time
	Materialize   MaterializeFunc
	Probe         ProbeFunc
	ProbeOptions  projectprobe.Options
	Reduce        ReduceFunc
	spoolObserver func(observationSpoolStats)
}

type frozenTask struct {
	key      string
	boundary source.Boundary
}

type decodedTask struct {
	task   frozenTask
	report source.DecodeReport
	spool  *observationSpool
	err    error
}

type terminalSource struct {
	record              memory.SourceRecord
	recordDigest        string
	mutation            sourcecatalog.BatchMutation
	state               memory.TerminalState
	spool               *observationSpool
	newObservationCount int
	chunks              []string
	diagnostics         []memory.Diagnostic
	lineage             memory.SessionLineage
	issue               bool
	shared              bool
}

type baseline struct {
	present  bool
	prepared memorystore.Prepared
	manifest memory.GenerationManifest
	sessions map[string]memory.SessionView
	project  *memory.ProjectView
	lineages map[string]memory.SessionLineageDependency
}

// Run prepares one complete private generation. Every adapter error is fatal;
// expected source terminal outcomes travel as typed reports or boundaries.
func Run(ctx context.Context, options Options) (result Result, returnedErr error) {
	result = Result{SchemaVersion: resultSchemaVersion, ProjectID: options.ProjectID, State: Failed, ReviewRunTokens: 0}
	if err := validateOptions(options); err != nil {
		return result, err
	}
	if err := projectidentity.Reauthenticate(options.Binding); err != nil {
		return result, fmt.Errorf("authenticate project identity: %w", err)
	}
	lock, err := acquireScanLock(ctx, options.DataRoot, options.ProjectID)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	if err := projectidentity.Reauthenticate(options.Binding); err != nil {
		return result, fmt.Errorf("reauthenticate project identity after scan lock: %w", err)
	}
	referenceTime := options.Now().UTC()
	referenceTimestamp := referenceTime.Format(time.RFC3339Nano)
	frozenNow := func() time.Time { return referenceTime }

	previous, err := loadBaseline(options.Store)
	if err != nil {
		return result, err
	}
	discovery, err := options.Adapter.Discover(ctx)
	defer abandonCandidates(options.Adapter, discovery.Candidates)
	if err != nil {
		return result, fmt.Errorf("discover source sessions: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	tasks, issueSources, err := freezeDiscovery(ctx, options, discovery)
	if err != nil {
		return result, err
	}
	defer abandonFrozenTasks(options.Adapter, tasks)
	spools, err := openObservationSpools(ctx, options.DataRoot, options.ProjectID, options.spoolObserver)
	if err != nil {
		return result, err
	}
	defer func() {
		if spools != nil {
			returnedErr = errors.Join(returnedErr, spools.close())
		}
	}()
	decoded, err := decodeFrozen(ctx, options, tasks, spools)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	terminals, err := collectTerminals(ctx, options, decoded, issueSources, previous)
	if err != nil {
		return result, err
	}
	if len(terminals) == 0 {
		return result, errors.New("no authenticated target-project source sessions reached a terminal state")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	probeOptions := options.ProbeOptions
	probeOptions.Binding = options.Binding
	probeOptions.Now = frozenNow
	probeState, probeCheck, err := options.Probe(ctx, probeOptions)
	if err != nil {
		return result, fmt.Errorf("probe project state: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	mutations := make([]sourcecatalog.BatchMutation, len(terminals))
	for index := range terminals {
		mutations[index] = terminals[index].mutation
	}
	plannedResults, err := options.Catalog.PlanBatch(mutations)
	if err != nil {
		return result, fmt.Errorf("plan source catalog batch: %w", err)
	}
	if len(plannedResults) != len(terminals) {
		return result, errors.New("source catalog batch plan count mismatch")
	}
	for index := range terminals {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		planned := terminals[index].mutation.Desired
		returned := plannedResults[index]
		if returned.Record.Provider != planned.Provider || returned.Record.SessionID != planned.SessionID || !containsProject(returned.Record.ProjectIDs, options.ProjectID) {
			return result, errors.New("source catalog batch returned a mismatched proposal")
		}
		digest, digestErr := memory.Digest(returned.Record)
		if digestErr != nil || digest != returned.Digest {
			return result, errors.Join(errors.New("source catalog batch digest mismatch"), digestErr)
		}
		terminals[index].record, terminals[index].recordDigest = returned.Record, returned.Digest
		terminals[index].shared = len(returned.Record.ProjectIDs) > 1
	}

	views := make([]memory.SessionView, 0, len(terminals))
	sourceDigests := make([]string, 0, len(terminals))
	lineageDependencies := make([]memory.SessionLineageDependency, 0, len(terminals))
	usage := make([]memory.AssociatedUsage, 0, len(terminals))
	for index := range terminals {
		terminal := &terminals[index]
		if err := ctx.Err(); err != nil {
			return result, err
		}
		observations, err := replayObservationSpool(ctx, terminal.spool)
		if err != nil {
			return result, fmt.Errorf("replay source observations %s/%s: %w", terminal.record.Provider, terminal.record.SessionID, err)
		}
		previousView := previous.sessions[sourceKey(terminal.record.Provider, terminal.record.SessionID)]
		newObservations, err := selectNewObservations(ctx, observations, previousView, options.Store)
		if err != nil {
			return result, fmt.Errorf("classify new source observations %s/%s: %w", terminal.record.Provider, terminal.record.SessionID, err)
		}
		terminal.newObservationCount = len(newObservations)
		if len(newObservations) > 0 {
			chunk, digestErr := memory.Digest(newObservations)
			if digestErr != nil {
				return result, digestErr
			}
			terminal.chunks = appendUnique(terminal.chunks, chunk)
		}
		var previousPointer *memory.SessionView
		if previousView.Digest != "" {
			previousCopy := previousView
			previousPointer = &previousCopy
		}
		usageDigest, err := memory.Digest(terminal.record.Usage)
		if err != nil {
			return result, fmt.Errorf("digest source usage %s/%s: %w", terminal.record.Provider, terminal.record.SessionID, err)
		}
		view, _, err := options.Materialize(sessionview.Input{
			ProjectID: options.ProjectID, Source: terminal.record,
			SourceRecordDigest: terminal.recordDigest, UsageRecordDigest: usageDigest,
			Observations: observations, ObservationChunkDigests: terminal.chunks,
			TerminalState: terminal.state, Diagnostics: terminal.diagnostics,
			Previous: previousPointer, MaterializerVersion: sessionview.MaterializerVersion,
		})
		if err != nil {
			return result, fmt.Errorf("materialize SessionView %s/%s: %w", terminal.record.Provider, terminal.record.SessionID, err)
		}
		previousLineage, err := loadPreviousLineage(ctx, options.Store, previous, terminal.record.Provider, terminal.record.SessionID)
		if err != nil {
			return result, err
		}
		lineage, err := buildSessionLineage(ctx, options.ProjectID, terminal.record, observations, previousLineage)
		if err != nil {
			return result, fmt.Errorf("materialize SessionLineage %s/%s: %w", terminal.record.Provider, terminal.record.SessionID, err)
		}
		if !sameDigestSet(view.ActiveRevisionIDs, lineage.ActiveRevisions) {
			return result, errors.New("SessionView and SessionLineage active revisions disagree")
		}
		terminal.lineage = lineage
		if err := ctx.Err(); err != nil {
			return result, err
		}
		views = append(views, view)
		sourceDigests = append(sourceDigests, view.SourceRecordDigest)
		lineageDependencies = append(lineageDependencies, memory.SessionLineageDependency{Provider: view.Provider, SessionID: view.SessionID, Digest: lineage.Digest})
		usage = append(usage, memory.AssociatedUsage{Provider: view.Provider, SessionID: view.SessionID, UsageRecordDigest: view.UsageRecordDigest, Shared: terminal.shared})
		incrementResult(&result, view.TerminalState, terminal.issue)
	}
	if result.SourceSessions != len(views) || result.TerminalSessions != len(views) {
		return result, errors.New("source and terminal Session counts do not reconcile")
	}

	if err := ctx.Err(); err != nil {
		return result, err
	}
	var previousProject *memory.ProjectView
	if previous.project != nil {
		copy := *previous.project
		previousProject = &copy
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	projectView, _, err := options.Reduce(projectview.Input{
		ProjectID: options.ProjectID, SessionViews: views, ProbeState: probeState,
		AssociatedUsage: usage, Previous: previousProject,
		ReducerVersion: projectview.ReducerVersion, ReferenceTime: referenceTimestamp,
	})
	if err != nil {
		return result, fmt.Errorf("reduce ProjectView: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if projectView.SourceSessions != result.SourceSessions || terminalTotal(projectView.TerminalCounts) != result.TerminalSessions {
		return result, errors.New("ProjectView terminal counts do not reconcile with scan")
	}
	sort.Strings(sourceDigests)
	sourceDigests = uniqueStrings(sourceDigests)
	manifest := memory.GenerationManifest{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: options.ProjectID,
		CreatedAt: referenceTimestamp, SourceRecordDigests: sourceDigests,
		SessionViews:     append([]memory.SessionViewDependency(nil), projectView.SessionViewDependencies...),
		SessionLineages:  lineageDependencies,
		ProbeStateDigest: probeState.Digest, ProbeCheck: probeCheck,
		ProjectViewDigest: projectView.Digest,
	}
	manifest.GenerationID, err = generationID(manifest)
	if err != nil {
		return result, err
	}
	if err := memory.ValidateGenerationManifest(manifest); err != nil {
		return result, fmt.Errorf("validate complete generation: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	batchResults, err := options.Catalog.ApplyBatch(mutations)
	if err != nil {
		return result, fmt.Errorf("apply source catalog batch: %w", err)
	}
	if len(batchResults) != len(plannedResults) {
		return result, errors.New("source catalog batch result count mismatch")
	}
	for index := range batchResults {
		if batchResults[index].Digest != plannedResults[index].Digest || !equalJSON(batchResults[index].Record, plannedResults[index].Record) {
			return result, errors.New("source catalog batch changed after validated plan")
		}
	}
	for index := range terminals {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if terminals[index].newObservationCount > 0 {
			observations, replayErr := replayObservationSpool(ctx, terminals[index].spool)
			if replayErr != nil {
				return result, fmt.Errorf("replay source observations for persistence: %w", replayErr)
			}
			previousView := previous.sessions[sourceKey(terminals[index].record.Provider, terminals[index].record.SessionID)]
			newObservations, classifyErr := selectNewObservations(ctx, observations, previousView, options.Store)
			if classifyErr != nil {
				return result, classifyErr
			}
			if len(newObservations) != terminals[index].newObservationCount {
				return result, errors.New("observation spool changed after catalog apply")
			}
			chunk, putErr := options.Store.PutObservationChunk(newObservations)
			if putErr != nil {
				return result, fmt.Errorf("persist observation chunk: %w", putErr)
			}
			if !containsString(terminals[index].chunks, chunk) {
				return result, errors.New("persisted observation chunk digest changed after planning")
			}
		}
	}
	if err := spools.close(); err != nil {
		return result, fmt.Errorf("cleanup observation spools: %w", err)
	}
	spools = nil
	for _, view := range views {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, err := options.Store.PutSessionView(view); err != nil {
			return result, fmt.Errorf("persist SessionView %s/%s: %w", view.Provider, view.SessionID, err)
		}
	}
	for _, terminal := range terminals {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, err := options.Store.PutSessionLineage(terminal.lineage); err != nil {
			return result, fmt.Errorf("persist SessionLineage %s/%s: %w", terminal.record.Provider, terminal.record.SessionID, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if _, err := options.Store.PutProbeState(probeState); err != nil {
		return result, fmt.Errorf("persist project probe: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if _, err := options.Store.PutProjectView(projectView); err != nil {
		return result, fmt.Errorf("persist ProjectView: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	var prepared memorystore.Prepared
	err = options.Catalog.WithBatchSnapshot(batchResults, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var prepareErr error
		prepared, prepareErr = prepareOrAdvance(options.Store, previous, manifest)
		return prepareErr
	})
	if err != nil {
		return result, err
	}
	loaded, loadedManifest, err := options.Store.LoadPrepared()
	if err != nil || loaded != prepared || loadedManifest.GenerationID != manifest.GenerationID {
		return result, errors.Join(errors.New("prepared generation re-read failed"), err)
	}
	result.GenerationID = prepared.GenerationID
	result.ProjectViewDigest = prepared.ProjectViewDigest
	result.Prepared = true
	if result.IssueSessions > 0 {
		result.State = CompletedWithIssues
	} else {
		result.State = Completed
	}
	return result, nil
}

func validateOptions(options Options) error {
	if !scanIDPattern.MatchString(options.ProjectID) || options.Binding.ProjectID != options.ProjectID {
		return errors.New("scan project identity is required")
	}
	for name, value := range map[string]string{"data root": options.DataRoot, "sessions root": options.SessionsRoot} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return fmt.Errorf("scan %s must be an absolute clean path", name)
		}
	}
	if options.Adapter == nil || options.Catalog == nil || options.Store == nil || options.Materialize == nil || options.Probe == nil || options.Reduce == nil || options.Now == nil {
		return errors.New("scan dependencies are incomplete")
	}
	if options.Workers < 1 {
		return errors.New("scan worker count must be positive")
	}
	return nil
}

func acquireScanLock(ctx context.Context, dataRoot, projectID string) (*project.ProjectLock, error) {
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("open scan lock root: %w", err)
	}
	defer data.Close()
	lockNamespace := filepath.ToSlash(filepath.Join("scan-locks", projectID))
	if err := data.EnsureDirectory(lockNamespace, 0o700); err != nil {
		return nil, fmt.Errorf("create scan lock namespace: %w", err)
	}
	locks, err := pathguard.Open(filepath.Join(data.Path, filepath.FromSlash(lockNamespace)))
	if err != nil {
		return nil, fmt.Errorf("pin scan lock namespace: %w", err)
	}
	defer locks.Close()
	name := "run.lock"
	for {
		lock, err := project.AcquireProjectLock(locks.Root, name, 0)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, project.ErrProjectLocked) {
			return nil, fmt.Errorf("acquire project scan lock: %w", err)
		}
		timer := time.NewTimer(scanLockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func freezeDiscovery(ctx context.Context, options Options, discovery source.Discovery) ([]frozenTask, []source.Issue, error) {
	defer abandonCandidates(options.Adapter, discovery.Candidates)
	transferred := false
	ownedBoundaries := make([]source.Boundary, 0, len(discovery.Candidates))
	defer func() {
		if !transferred {
			abandonBoundaries(options.Adapter, ownedBoundaries)
		}
	}()
	if len(discovery.Candidates)+len(discovery.Issues) > maxScanSources {
		return nil, nil, errors.New("source discovery exceeds scan limit")
	}
	candidates := make(map[string]source.Candidate, len(discovery.Candidates))
	issues := make(map[string]source.Issue, len(discovery.Issues))
	for _, issue := range discovery.Issues {
		key := sourceKey(issue.Provider, issue.SessionID)
		if key == "\x00" {
			continue
		}
		if _, duplicate := issues[key]; duplicate {
			issue.TerminalState = memory.Ambiguous
			issue.Code = "duplicate_discovery_issue"
		}
		issues[key] = issue
	}
	for _, candidate := range discovery.Candidates {
		key := sourceKey(candidate.Provider, candidate.SessionID)
		if candidate.Provider == "" || candidate.SessionID == "" || candidate.Handle == "" {
			continue
		}
		if _, issue := issues[key]; issue {
			continue
		}
		if _, duplicate := candidates[key]; duplicate {
			issues[key] = source.Issue{Provider: candidate.Provider, SessionID: candidate.SessionID, Code: "duplicate_candidate", TerminalState: memory.Ambiguous}
			delete(candidates, key)
			continue
		}
		candidates[key] = candidate
	}
	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tasks := make([]frozenTask, 0, len(keys))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		boundary, err := options.Adapter.Freeze(ctx, candidates[key])
		if err != nil {
			return nil, nil, fmt.Errorf("freeze source %s/%s: %w", candidates[key].Provider, candidates[key].SessionID, err)
		}
		if boundary.TerminalState != memory.Indexed {
			issues[key] = source.Issue{Provider: candidates[key].Provider, SessionID: candidates[key].SessionID, Code: "freeze_terminal", TerminalState: boundary.TerminalState}
			abandonBoundary(options.Adapter, boundary)
			continue
		}
		ownedBoundaries = append(ownedBoundaries, boundary)
		tasks = append(tasks, frozenTask{key: key, boundary: boundary})
	}
	known, err := options.Catalog.ListCandidates(options.ProjectID)
	if err != nil {
		return nil, nil, fmt.Errorf("list catalog sources: %w", err)
	}
	for _, record := range known {
		key := sourceKey(record.Provider, record.SessionID)
		if _, found := candidates[key]; found {
			continue
		}
		if _, found := issues[key]; !found {
			issues[key] = source.Issue{Provider: record.Provider, SessionID: record.SessionID, Code: "not_discovered", TerminalState: memory.Missing}
		}
	}
	issueList := make([]source.Issue, 0, len(issues))
	for _, issue := range issues {
		issueList = append(issueList, issue)
	}
	sort.Slice(issueList, func(i, j int) bool {
		return sourceKey(issueList[i].Provider, issueList[i].SessionID) < sourceKey(issueList[j].Provider, issueList[j].SessionID)
	})
	transferred = true
	return tasks, issueList, nil
}

func decodeFrozen(ctx context.Context, options Options, tasks []frozenTask, spools *observationSpools) ([]decodedTask, error) {
	defer abandonFrozenTasks(options.Adapter, tasks)
	decodeContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	workers := options.Workers
	if workers > 4 {
		workers = 4
	}
	if maximum := runtime.GOMAXPROCS(0); workers > maximum {
		workers = maximum
	}
	if workers < 1 {
		workers = 1
	}
	results := make([]decodedTask, len(tasks))
	var next atomic.Int64
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer func() {
				if recover() != nil {
					cancel(errors.New("source adapter Decode panicked"))
				}
				wait.Done()
			}()
			for {
				if decodeContext.Err() != nil {
					return
				}
				index := int(next.Add(1) - 1)
				if index >= len(tasks) {
					return
				}
				task := tasks[index]
				spool, err := spools.create(decodeContext, task.boundary.Candidate.Provider, task.boundary.Candidate.SessionID)
				if err != nil {
					cancel(err)
					return
				}
				result := decodedTask{task: task, spool: spool}
				result.report, result.err = options.Adapter.Decode(decodeContext, task.boundary, func(observation memory.ObservationRevision) error {
					if err := decodeContext.Err(); err != nil {
						return err
					}
					if err := memory.ValidateObservationRevision(observation); err != nil {
						return fmt.Errorf("validate decoded observation: %w", err)
					}
					if observation.Key.ProjectID != options.ProjectID {
						return nil
					}
					return spool.append(decodeContext, observation)
				})
				if result.err == nil {
					result.err = spool.seal(decodeContext)
				}
				results[index] = result
				if decodeContext.Err() != nil {
					return
				}
			}
		}()
	}
	wait.Wait()
	if cause := context.Cause(decodeContext); cause != nil {
		return nil, cause
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func abandonCandidates(adapter source.Adapter, candidates []source.Candidate) {
	lifecycle, ok := adapter.(source.LeaseLifecycle)
	if !ok {
		return
	}
	for _, candidate := range candidates {
		lifecycle.AbandonCandidate(candidate)
	}
}

func abandonBoundaries(adapter source.Adapter, boundaries []source.Boundary) {
	lifecycle, ok := adapter.(source.LeaseLifecycle)
	if !ok {
		return
	}
	for _, boundary := range boundaries {
		lifecycle.AbandonBoundary(boundary)
	}
}

func abandonBoundary(adapter source.Adapter, boundary source.Boundary) {
	if lifecycle, ok := adapter.(source.LeaseLifecycle); ok {
		lifecycle.AbandonBoundary(boundary)
	}
}

func abandonFrozenTasks(adapter source.Adapter, tasks []frozenTask) {
	lifecycle, ok := adapter.(source.LeaseLifecycle)
	if !ok {
		return
	}
	for _, task := range tasks {
		lifecycle.AbandonBoundary(task.boundary)
	}
}

func collectTerminals(ctx context.Context, options Options, decoded []decodedTask, issues []source.Issue, previous baseline) ([]terminalSource, error) {
	terminals := make([]terminalSource, 0, len(decoded)+len(issues))
	for _, item := range decoded {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.err != nil {
			return nil, fmt.Errorf("decode source %s/%s: %w", item.task.boundary.Candidate.Provider, item.task.boundary.Candidate.SessionID, item.err)
		}
		record := item.report.ProposedSource
		if err := memory.ValidateSourceRecord(record); err != nil {
			return nil, fmt.Errorf("invalid decoded source proposal: %w", err)
		}
		if record.Provider != item.task.boundary.Candidate.Provider || record.SessionID != item.task.boundary.Candidate.SessionID || record.SourceIdentity != item.task.boundary.SourceIdentity || !equalJSON(record.FrozenBoundary, item.task.boundary.Frozen) {
			return nil, errors.New("decoded source proposal does not match frozen boundary")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !containsProject(record.ProjectIDs, options.ProjectID) {
			if item.spool != nil && item.spool.count != 0 {
				return nil, projectidentity.ErrAssociationRequired
			}
			continue
		}
		state := item.report.TerminalState
		if state == "" {
			state = memory.Indexed
		}
		diagnostics := append([]memory.Diagnostic(nil), item.report.Diagnostics...)
		if item.report.MalformedLines > 0 {
			diagnostics = append(diagnostics, memory.Diagnostic{Code: "malformed_source_records"})
		}
		if item.report.UnsupportedRecords > 0 {
			diagnostics = append(diagnostics, memory.Diagnostic{Code: "unsupported_source_records"})
		}
		for _, quarantined := range item.report.Quarantined {
			diagnostics = append(diagnostics, memory.Diagnostic{Code: quarantineDiagnosticCode(quarantined.ReasonCode)})
		}
		switch item.report.BoundaryRelation {
		case source.BoundaryInitial, source.BoundaryUnchanged, source.BoundaryAppend, source.BoundaryReplacement:
		default:
			return nil, fmt.Errorf("decoded source %s/%s has invalid boundary relation %q", record.Provider, record.SessionID, item.report.BoundaryRelation)
		}
		chunkDigests := previousChunks(previous, record.Provider, record.SessionID)
		terminals = append(terminals, terminalSource{
			record: record, mutation: sourcecatalog.BatchMutation{Relation: item.report.BoundaryRelation, ExpectedDigest: item.report.ExpectedCatalogDigest, Desired: record}, state: state,
			spool: item.spool, chunks: chunkDigests,
			diagnostics: diagnostics,
			issue:       state != memory.Indexed || item.report.MalformedLines > 0 || item.report.UnsupportedRecords > 0 || len(item.report.Quarantined) > 0 || len(item.report.Diagnostics) > 0,
			shared:      len(record.ProjectIDs) > 1,
		})
	}
	for _, issue := range issues {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record, found, err := options.Catalog.GetSource(issue.Provider, issue.SessionID)
		if err != nil {
			return nil, fmt.Errorf("read issue source catalog record: %w", err)
		}
		if !found || !containsProject(record.ProjectIDs, options.ProjectID) {
			continue
		}
		if issue.TerminalState != memory.Missing && issue.TerminalState != memory.Unreadable && issue.TerminalState != memory.Ambiguous {
			return nil, fmt.Errorf("issue source %s/%s has incompatible unavailable terminal state %q", issue.Provider, issue.SessionID, issue.TerminalState)
		}
		expectedDigest, err := memory.Digest(record)
		if err != nil {
			return nil, err
		}
		record.Availability = memory.SourceUnavailable
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		terminals = append(terminals, terminalSource{
			record: record, mutation: sourcecatalog.BatchMutation{Relation: source.BoundaryUnchanged, ExpectedDigest: expectedDigest, Desired: record}, state: issue.TerminalState,
			diagnostics: []memory.Diagnostic{{Code: terminalIssueCode(issue.TerminalState)}},
			issue:       true, shared: len(record.ProjectIDs) > 1,
		})
	}
	sort.Slice(terminals, func(i, j int) bool {
		return sourceKey(terminals[i].record.Provider, terminals[i].record.SessionID) < sourceKey(terminals[j].record.Provider, terminals[j].record.SessionID)
	})
	for index := 1; index < len(terminals); index++ {
		if sourceKey(terminals[index-1].record.Provider, terminals[index-1].record.SessionID) == sourceKey(terminals[index].record.Provider, terminals[index].record.SessionID) {
			return nil, errors.New("logical source reached multiple terminal states")
		}
	}
	return terminals, nil
}

func quarantineDiagnosticCode(reason string) string {
	switch reason {
	case "ambiguous_project_root", "foreign_project_root":
		return "quarantined_" + reason
	default:
		return "quarantined_observation"
	}
}

func loadBaseline(store MemoryStore) (baseline, error) {
	result := baseline{sessions: make(map[string]memory.SessionView), lineages: make(map[string]memory.SessionLineageDependency)}
	prepared, manifest, err := store.LoadPrepared()
	if errors.Is(err, memorystore.ErrNoPreparedGeneration) {
		return result, nil
	}
	if err != nil {
		return baseline{}, fmt.Errorf("load prepared scan baseline: %w", err)
	}
	result.present, result.prepared, result.manifest = true, prepared, manifest
	for _, dependency := range manifest.SessionViews {
		body, err := store.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if err != nil {
			return baseline{}, err
		}
		var view memory.SessionView
		if err := decodeExactJSON(body, &view); err != nil {
			return baseline{}, err
		}
		result.sessions[sourceKey(view.Provider, view.SessionID)] = view
	}
	for _, dependency := range manifest.SessionLineages {
		result.lineages[sourceKey(dependency.Provider, dependency.SessionID)] = dependency
	}
	body, err := store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		return baseline{}, err
	}
	var projectView memory.ProjectView
	if err := decodeExactJSON(body, &projectView); err != nil {
		return baseline{}, err
	}
	result.project = &projectView
	return result, nil
}

func replayObservationSpool(ctx context.Context, spool *observationSpool) ([]memory.ObservationRevision, error) {
	if spool == nil {
		return nil, nil
	}
	result := make([]memory.ObservationRevision, 0, spool.count)
	revisions := make(map[string]struct{}, spool.count)
	if err := spool.replay(ctx, func(value memory.ObservationRevision) error {
		if _, duplicate := revisions[value.RevisionID]; duplicate {
			return fmt.Errorf("decoded source %s/%s emitted duplicate revision %s", value.Key.Provider, value.Key.SessionID, value.RevisionID)
		}
		revisions[value.RevisionID] = struct{}{}
		result = append(result, value)
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func selectNewObservations(ctx context.Context, values []memory.ObservationRevision, previous memory.SessionView, store MemoryStore) ([]memory.ObservationRevision, error) {
	unknown := make(map[string]struct{}, len(values))
	for _, value := range values {
		unknown[value.RevisionID] = struct{}{}
	}
	for _, digest := range previous.ObservationChunkDigests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, err := store.LoadObject(memorystore.ObjectObservationChunk, digest)
		if err != nil {
			return nil, err
		}
		records, err := decodeObservationChunk(body)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			delete(unknown, record.RevisionID)
		}
	}
	result := make([]memory.ObservationRevision, 0, len(values))
	for _, value := range values {
		if _, exists := unknown[value.RevisionID]; exists {
			result = append(result, value)
		}
	}
	return result, nil
}

func loadPreviousLineage(ctx context.Context, store MemoryStore, previous baseline, provider, sessionID string) (*memory.SessionLineage, error) {
	dependency, exists := previous.lineages[sourceKey(provider, sessionID)]
	if !exists {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := store.LoadObject(memorystore.ObjectSessionLineage, dependency.Digest)
	if err != nil {
		return nil, fmt.Errorf("load previous SessionLineage %s/%s: %w", provider, sessionID, err)
	}
	var lineage memory.SessionLineage
	if err := decodeExactJSON(body, &lineage); err != nil {
		return nil, err
	}
	if lineage.Provider != provider || lineage.SessionID != sessionID || lineage.Digest != dependency.Digest {
		return nil, errors.New("previous SessionLineage dependency identity mismatch")
	}
	return &lineage, nil
}

func buildSessionLineage(ctx context.Context, projectID string, record memory.SourceRecord, observations []memory.ObservationRevision, previous *memory.SessionLineage) (memory.SessionLineage, error) {
	active := make(map[string]string, len(observations))
	if record.Availability == memory.SourceUnavailable {
		if previous == nil {
			return memory.SessionLineage{}, errors.New("unavailable source has no previous SessionLineage")
		}
		for key, revision := range previous.ActiveRevisions {
			active[key] = revision
		}
	} else {
		for _, observation := range observations {
			if err := ctx.Err(); err != nil {
				return memory.SessionLineage{}, err
			}
			key, err := memory.DigestContext(ctx, observation.Key)
			if err != nil {
				return memory.SessionLineage{}, err
			}
			if existing, duplicate := active[key]; duplicate && existing != observation.RevisionID {
				return memory.SessionLineage{}, errors.New("multiple active revisions share one stable key")
			}
			active[key] = observation.RevisionID
		}
	}
	if previous != nil && previous.ProjectID == projectID && previous.Provider == record.Provider && previous.SessionID == record.SessionID && previous.SourceIdentity == record.SourceIdentity && equalStringMap(active, previous.ActiveRevisions) {
		copy := *previous
		copy.ActiveRevisions = cloneStringMap(previous.ActiveRevisions)
		copy.SupersededRevisions = cloneStringMap(previous.SupersededRevisions)
		copy.WithdrawnRevisions = cloneStringMap(previous.WithdrawnRevisions)
		return copy, nil
	}
	lineage := memory.SessionLineage{
		SchemaVersion:       memory.MemorySchemaVersion,
		ProjectID:           projectID,
		Provider:            record.Provider,
		SessionID:           record.SessionID,
		SourceIdentity:      record.SourceIdentity,
		ActiveRevisions:     active,
		SupersededRevisions: map[string]string{},
		WithdrawnRevisions:  map[string]string{},
	}
	if previous != nil {
		if previous.ProjectID != projectID || previous.Provider != record.Provider || previous.SessionID != record.SessionID || previous.SourceIdentity != record.SourceIdentity {
			return memory.SessionLineage{}, errors.New("previous SessionLineage identity mismatch")
		}
		lineage.PreviousLineageDigest = previous.Digest
		for key, oldRevision := range previous.ActiveRevisions {
			currentRevision, exists := active[key]
			switch {
			case !exists:
				lineage.WithdrawnRevisions[key] = oldRevision
			case currentRevision != oldRevision:
				lineage.SupersededRevisions[oldRevision] = currentRevision
			}
		}
	}
	var err error
	lineage.Digest, err = memory.SessionLineageDigestContext(ctx, lineage)
	if err != nil {
		return memory.SessionLineage{}, err
	}
	if err := memory.ValidateSessionLineageContext(ctx, lineage); err != nil {
		return memory.SessionLineage{}, err
	}
	return lineage, nil
}

func sameDigestSet(revisions []string, active map[string]string) bool {
	if len(revisions) != len(active) {
		return false
	}
	selected := make(map[string]struct{}, len(active))
	for _, revision := range active {
		selected[revision] = struct{}{}
	}
	for _, revision := range revisions {
		if _, exists := selected[revision]; !exists {
			return false
		}
	}
	return true
}

func equalStringMap(first, second map[string]string) bool {
	if len(first) != len(second) {
		return false
	}
	for key, value := range first {
		if second[key] != value {
			return false
		}
	}
	return true
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func prepareOrAdvance(store MemoryStore, previous baseline, manifest memory.GenerationManifest) (memorystore.Prepared, error) {
	if !previous.present {
		return store.PrepareGeneration(manifest)
	}
	if sameGenerationContent(previous.manifest, manifest) {
		return previous.prepared, nil
	}
	return store.AdvancePrepared(previous.prepared, manifest)
}

func sameGenerationContent(first, second memory.GenerationManifest) bool {
	first.GenerationID, second.GenerationID = "same", "same"
	return equalJSON(first, second)
}

func generationID(value memory.GenerationManifest) (string, error) {
	identity := value
	identity.GenerationID = "scan"
	digest, err := memory.Digest(identity)
	if err != nil {
		return "", fmt.Errorf("digest scan generation identity: %w", err)
	}
	return "scan-" + strings.TrimPrefix(digest, "sha256:")[:32], nil
}

func previousChunks(previous baseline, provider, sessionID string) []string {
	view := previous.sessions[sourceKey(provider, sessionID)]
	return append([]string(nil), view.ObservationChunkDigests...)
}

func incrementResult(result *Result, state memory.TerminalState, issue bool) {
	result.SourceSessions++
	result.TerminalSessions++
	if state == memory.Indexed {
		result.IndexedSessions++
	}
	if issue {
		result.IssueSessions++
	}
}

func terminalTotal(counts memory.TerminalCounts) int {
	return counts.Indexed + counts.Unsupported + counts.Missing + counts.Unreadable + counts.Ambiguous
}

func terminalIssueCode(state memory.TerminalState) string {
	switch state {
	case memory.Missing:
		return "source_missing"
	case memory.Unreadable:
		return "source_unreadable"
	case memory.Ambiguous:
		return "source_ambiguous"
	case memory.Unsupported:
		return "source_unsupported"
	default:
		return "source_issue"
	}
}

func containsProject(values []string, projectID string) bool {
	for _, value := range values {
		if value == projectID {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a := append([]string(nil), left...)
	b := append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func sourceKey(provider, sessionID string) string { return provider + "\x00" + sessionID }

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func decodeExactJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("JSON object has trailing data")
	}
	return nil
}

func decodeObservationChunk(body []byte) ([]memory.ObservationRevision, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var result []memory.ObservationRevision
	for scanner.Scan() {
		var revision memory.ObservationRevision
		if err := decodeExactJSON(scanner.Bytes(), &revision); err != nil {
			return nil, err
		}
		if err := memory.ValidateObservationRevision(revision); err != nil {
			return nil, err
		}
		result = append(result, revision)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func equalJSON(left, right any) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}
