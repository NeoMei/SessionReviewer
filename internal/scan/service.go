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

var scanIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type MaterializeFunc func(sessionview.Input) (memory.SessionView, bool, error)
type ProbeFunc func(context.Context, projectprobe.Options) (memory.ProjectProbeState, memory.ProbeCheck, error)
type ReduceFunc func(projectview.Input) (memory.ProjectView, bool, error)

type Options struct {
	ProjectID    string
	Binding      projectidentity.Binding
	SessionsRoot string
	DataRoot     string
	Adapter      source.Adapter
	Catalog      *sourcecatalog.Catalog
	Store        *memorystore.Store
	Workers      int
	Now          func() time.Time
	Materialize  MaterializeFunc
	Probe        ProbeFunc
	ProbeOptions projectprobe.Options
	Reduce       ReduceFunc
}

type frozenTask struct {
	key      string
	boundary source.Boundary
}

type decodedTask struct {
	task         frozenTask
	report       source.DecodeReport
	observations []memory.ObservationRevision
	err          error
}

type terminalSource struct {
	record       memory.SourceRecord
	recordDigest string
	state        memory.TerminalState
	observations []memory.ObservationRevision
	chunks       []string
	diagnostics  []memory.Diagnostic
	issue        bool
	shared       bool
}

type baseline struct {
	present   bool
	prepared  memorystore.Prepared
	manifest  memory.GenerationManifest
	sessions  map[string]memory.SessionView
	project   *memory.ProjectView
	revisions map[string]memory.ObservationRevision
}

// Run prepares one complete private generation. Source-local failures are
// isolated; integrity, store, identity, probe, and reduction failures fail the
// project scan before changing the prepared pointer.
func Run(ctx context.Context, options Options) (Result, error) {
	result := Result{SchemaVersion: resultSchemaVersion, ProjectID: options.ProjectID, State: Failed, ReviewRunTokens: 0}
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
	if err != nil {
		return result, fmt.Errorf("discover source sessions: %w", err)
	}
	tasks, issueSources, err := freezeDiscovery(ctx, options, discovery)
	if err != nil {
		return result, err
	}
	decoded, err := decodeFrozen(ctx, options, tasks)
	if err != nil {
		return result, err
	}
	terminals, err := collectTerminals(ctx, options, decoded, issueSources, previous)
	if err != nil {
		return result, err
	}
	if len(terminals) == 0 {
		return result, errors.New("no authenticated target-project source sessions reached a terminal state")
	}

	views := make([]memory.SessionView, 0, len(terminals))
	sourceDigests := make([]string, 0, len(terminals))
	allChunkDigests := make([]string, 0)
	usage := make([]memory.AssociatedUsage, 0, len(terminals))
	for _, terminal := range terminals {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		previousView := previous.sessions[sourceKey(terminal.record.Provider, terminal.record.SessionID)]
		var previousPointer *memory.SessionView
		if previousView.Digest != "" {
			previousCopy := previousView
			previousPointer = &previousCopy
		}
		view, _, err := options.Materialize(sessionview.Input{
			ProjectID: options.ProjectID, Source: terminal.record,
			SourceRecordDigest: terminal.recordDigest, UsageRecordDigest: terminal.recordDigest,
			Observations: terminal.observations, ObservationChunkDigests: terminal.chunks,
			TerminalState: terminal.state, Diagnostics: terminal.diagnostics,
			Previous: previousPointer, MaterializerVersion: sessionview.MaterializerVersion,
		})
		if err != nil {
			return result, fmt.Errorf("materialize SessionView %s/%s: %w", terminal.record.Provider, terminal.record.SessionID, err)
		}
		if _, err := options.Store.PutSessionView(view); err != nil {
			return result, fmt.Errorf("persist SessionView %s/%s: %w", view.Provider, view.SessionID, err)
		}
		views = append(views, view)
		sourceDigests = append(sourceDigests, view.SourceRecordDigest)
		allChunkDigests = append(allChunkDigests, view.ObservationChunkDigests...)
		usage = append(usage, memory.AssociatedUsage{Provider: view.Provider, SessionID: view.SessionID, UsageRecordDigest: view.UsageRecordDigest, Shared: terminal.shared})
		incrementResult(&result, view.TerminalState, terminal.issue)
	}
	if result.SourceSessions != len(views) || result.TerminalSessions != len(views) {
		return result, errors.New("source and terminal Session counts do not reconcile")
	}

	probeOptions := options.ProbeOptions
	probeOptions.Binding = options.Binding
	probeOptions.Now = frozenNow
	probeState, probeCheck, err := options.Probe(ctx, probeOptions)
	if err != nil {
		return result, fmt.Errorf("probe project state: %w", err)
	}
	if _, err := options.Store.PutProbeState(probeState); err != nil {
		return result, fmt.Errorf("persist project probe: %w", err)
	}
	var previousProject *memory.ProjectView
	if previous.project != nil {
		copy := *previous.project
		previousProject = &copy
	}
	projectView, _, err := options.Reduce(projectview.Input{
		ProjectID: options.ProjectID, SessionViews: views, ProbeState: probeState,
		AssociatedUsage: usage, Previous: previousProject,
		ReducerVersion: projectview.ReducerVersion, ReferenceTime: referenceTimestamp,
	})
	if err != nil {
		return result, fmt.Errorf("reduce ProjectView: %w", err)
	}
	if projectView.SourceSessions != result.SourceSessions || terminalTotal(projectView.TerminalCounts) != result.TerminalSessions {
		return result, errors.New("ProjectView terminal counts do not reconcile with scan")
	}
	if _, err := options.Store.PutProjectView(projectView); err != nil {
		return result, fmt.Errorf("persist ProjectView: %w", err)
	}

	active, superseded, withdrawn, err := classifyRevisions(views, terminals, previous)
	if err != nil {
		return result, err
	}
	sort.Strings(sourceDigests)
	sourceDigests = uniqueStrings(sourceDigests)
	allChunkDigests = uniqueStrings(allChunkDigests)
	manifest := memory.GenerationManifest{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: options.ProjectID,
		CreatedAt: referenceTimestamp, SourceRecordDigests: sourceDigests,
		ObservationChunkDigests: allChunkDigests,
		SessionViews:            append([]memory.SessionViewDependency(nil), projectView.SessionViewDependencies...),
		ProbeStateDigest:        probeState.Digest, ProbeCheck: probeCheck,
		ProjectViewDigest: projectView.Digest, ActiveRevisions: active,
		SupersededRevisions: superseded, WithdrawnRevisions: withdrawn,
	}
	manifest.GenerationID, err = generationID(manifest)
	if err != nil {
		return result, err
	}
	if err := memory.ValidateGenerationManifest(manifest); err != nil {
		return result, fmt.Errorf("validate complete generation: %w", err)
	}

	prepared, err := prepareOrAdvance(options.Store, previous, manifest)
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
			issues[key] = source.Issue{Provider: candidates[key].Provider, SessionID: candidates[key].SessionID, Code: "freeze_failed", TerminalState: memory.Unreadable}
			continue
		}
		if boundary.TerminalState != memory.Indexed {
			issues[key] = source.Issue{Provider: candidates[key].Provider, SessionID: candidates[key].SessionID, Code: "freeze_terminal", TerminalState: boundary.TerminalState}
			continue
		}
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
	return tasks, issueList, nil
}

func decodeFrozen(ctx context.Context, options Options, tasks []frozenTask) ([]decodedTask, error) {
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
			defer wait.Done()
			for {
				index := int(next.Add(1) - 1)
				if index >= len(tasks) {
					return
				}
				task := tasks[index]
				result := decodedTask{task: task}
				result.report, result.err = options.Adapter.Decode(ctx, task.boundary, func(observation memory.ObservationRevision) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					if err := memory.ValidateObservationRevision(observation); err != nil {
						return fmt.Errorf("validate decoded observation: %w", err)
					}
					if observation.Key.ProjectID != options.ProjectID {
						return nil
					}
					if len(result.observations) >= maxSourceRevisions {
						return errors.New("source observation limit exceeded")
					}
					result.observations = append(result.observations, observation)
					return nil
				})
				results[index] = result
			}
		}()
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func collectTerminals(ctx context.Context, options Options, decoded []decodedTask, issues []source.Issue, previous baseline) ([]terminalSource, error) {
	terminals := make([]terminalSource, 0, len(decoded)+len(issues))
	for _, item := range decoded {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.err != nil {
			issues = append(issues, source.Issue{Provider: item.task.boundary.Candidate.Provider, SessionID: item.task.boundary.Candidate.SessionID, Code: "decode_failed", TerminalState: memory.Unreadable})
			continue
		}
		record, found, err := options.Catalog.GetSource(item.task.boundary.Candidate.Provider, item.task.boundary.Candidate.SessionID)
		if err != nil {
			return nil, fmt.Errorf("read decoded source catalog record: %w", err)
		}
		if !found || !containsProject(record.ProjectIDs, options.ProjectID) {
			continue
		}
		digest, err := memory.Digest(record)
		if err != nil || digest != item.report.CatalogRecordDigest {
			return nil, errors.New("decoded source catalog digest does not match authenticated record")
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
		chunkDigests := previousChunks(previous, record.Provider, record.SessionID)
		if len(item.observations) > 0 {
			chunk, err := options.Store.PutObservationChunk(item.observations)
			if err != nil {
				return nil, fmt.Errorf("persist observation chunk %s/%s: %w", record.Provider, record.SessionID, err)
			}
			chunkDigests = appendUnique(chunkDigests, chunk)
		}
		terminals = append(terminals, terminalSource{
			record: record, recordDigest: digest, state: state,
			observations: item.observations, chunks: chunkDigests,
			diagnostics: diagnostics,
			issue:       state != memory.Indexed || item.report.MalformedLines > 0 || item.report.UnsupportedRecords > 0 || len(item.report.Quarantined) > 0 || len(item.report.Diagnostics) > 0,
			shared:      len(record.ProjectIDs) > 1,
		})
	}
	for _, issue := range issues {
		record, found, err := options.Catalog.GetSource(issue.Provider, issue.SessionID)
		if err != nil {
			return nil, fmt.Errorf("read issue source catalog record: %w", err)
		}
		if !found || !containsProject(record.ProjectIDs, options.ProjectID) {
			continue
		}
		record.Availability = memory.SourceUnavailable
		digest, err := options.Catalog.UpsertSource(record)
		if err != nil {
			return nil, fmt.Errorf("mark issue source unavailable: %w", err)
		}
		terminals = append(terminals, terminalSource{
			record: record, recordDigest: digest, state: memory.Missing,
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

func loadBaseline(store *memorystore.Store) (baseline, error) {
	result := baseline{sessions: make(map[string]memory.SessionView), revisions: make(map[string]memory.ObservationRevision)}
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
	for _, digest := range manifest.ObservationChunkDigests {
		body, err := store.LoadObject(memorystore.ObjectObservationChunk, digest)
		if err != nil {
			return baseline{}, err
		}
		records, err := decodeObservationChunk(body)
		if err != nil {
			return baseline{}, err
		}
		for _, record := range records {
			result.revisions[record.RevisionID] = record
		}
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

func classifyRevisions(views []memory.SessionView, terminals []terminalSource, previous baseline) (map[string]string, map[string]string, map[string]string, error) {
	revisions := make(map[string]memory.ObservationRevision, len(previous.revisions))
	for id, revision := range previous.revisions {
		revisions[id] = revision
	}
	for _, terminal := range terminals {
		for _, revision := range terminal.observations {
			revisions[revision.RevisionID] = revision
		}
	}
	active := make(map[string]string)
	for _, view := range views {
		for _, revisionID := range view.ActiveRevisionIDs {
			revision, found := revisions[revisionID]
			if !found {
				return nil, nil, nil, fmt.Errorf("active revision %s is unavailable", revisionID)
			}
			key, err := memory.Digest(revision.Key)
			if err != nil {
				return nil, nil, nil, err
			}
			if existing, duplicate := active[key]; duplicate && existing != revisionID {
				return nil, nil, nil, errors.New("multiple active revisions share one stable key")
			}
			active[key] = revisionID
		}
	}
	superseded := make(map[string]string)
	withdrawn := make(map[string]string)
	for revisionID, revision := range revisions {
		key, err := memory.Digest(revision.Key)
		if err != nil {
			return nil, nil, nil, err
		}
		if selected, found := active[key]; found {
			if selected != revisionID {
				superseded[revisionID] = selected
			}
			continue
		}
		if previousSelected, found := previous.manifest.ActiveRevisions[key]; found && previousSelected == revisionID {
			withdrawn[key] = revisionID
			continue
		}
		if successor, found := previous.manifest.SupersededRevisions[revisionID]; found {
			superseded[revisionID] = successor
			continue
		}
		if withdrawnRevision, found := previous.manifest.WithdrawnRevisions[key]; found && withdrawnRevision == revisionID {
			withdrawn[key] = revisionID
			continue
		}
		return nil, nil, nil, fmt.Errorf("historical revision %s has no deterministic inactive lineage", revisionID)
	}
	return active, superseded, withdrawn, nil
}

func prepareOrAdvance(store *memorystore.Store, previous baseline, manifest memory.GenerationManifest) (memorystore.Prepared, error) {
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
	first.CreatedAt, second.CreatedAt = "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"
	first.ProbeCheck.CheckedAt, second.ProbeCheck.CheckedAt = "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"
	return equalJSON(first, second)
}

func generationID(value memory.GenerationManifest) (string, error) {
	identity := value
	identity.GenerationID = "scan"
	identity.CreatedAt = "2026-01-01T00:00:00Z"
	identity.ProbeCheck.CheckedAt = "2026-01-01T00:00:00Z"
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
