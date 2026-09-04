package syncproject

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/migrationv4"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

type MigrationMode string

const (
	MigrationDryRun  MigrationMode = "dry-run"
	MigrationConfirm MigrationMode = "confirm-migration"
)

var (
	ErrMigrationRequired     = errors.New("migration_required")
	ErrMigrationPreviewStale = errors.New("migration_preview_stale")
)

type MigrationPublication struct {
	ProjectID          string
	PreparedGeneration string
	Plan               presentation.RenderPlan
	Mapping            config.ProjectMapping
	DataRoot           string
	Preview            migrationv4.MigrationPreview
	PublicationLock    *publicationlock.Owner

	syncDataRoot *os.Root
}

type MigrationPublisher func(context.Context, MigrationPublication) error

type MigrationOptions struct {
	Options
	Mode                  MigrationMode
	ExpectedPreviewDigest string
	Publish               MigrationPublisher

	build func(*MappingPin) (migrationv4.Result, error)
}

type MigrationResult struct {
	Preview migrationv4.MigrationPreview `json:"preview"`
	Applied bool                         `json:"applied"`
}

// RunMigration owns the same project lock as ordinary reconciliation, rebuilds
// the complete migration preview inside that lock, and invokes one publisher
// for the four-file atom only after the expected digest still matches.
func RunMigration(ctx context.Context, options MigrationOptions) (_ MigrationResult, retErr error) {
	if ctx == nil {
		return MigrationResult{}, errors.New("migration context is required")
	}
	if options.Mode != MigrationDryRun && options.Mode != MigrationConfirm {
		return MigrationResult{}, errors.New("invalid migration mode")
	}
	if options.Mode == MigrationConfirm && options.ExpectedPreviewDigest == "" {
		return MigrationResult{}, errors.New("expected migration preview digest is required")
	}
	pin, err := PinMapping(options.Options)
	if err != nil {
		return MigrationResult{}, err
	}
	defer func() { retErr = errors.Join(retErr, pin.Close()) }()
	publicationOwner, err := publicationlock.Acquire(pin.data.Path, pin.mapping.ID, 10*time.Second)
	if err != nil {
		return MigrationResult{}, errors.New("public projection is locked or unsafe")
	}
	defer func() { retErr = errors.Join(retErr, publicationOwner.Release()) }()
	lock, err := project.AcquireProjectLock(pin.syncData.Root, "locks/sync.lock", 10*time.Second)
	if err != nil {
		return MigrationResult{}, errors.New("sync project is locked or unsafe")
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	if err := pin.verify(options.Options); err != nil {
		return MigrationResult{}, err
	}
	build := options.build
	if build == nil {
		build = buildMigrationFromPin
	}
	plan, err := build(pin)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("build v4 migration preview: %w", err)
	}
	result := MigrationResult{Preview: plan.Preview}
	if options.Mode == MigrationDryRun {
		return result, nil
	}
	if plan.Preview.PreviewDigest != options.ExpectedPreviewDigest {
		return MigrationResult{}, ErrMigrationPreviewStale
	}
	if options.Publish == nil {
		return MigrationResult{}, errors.New("migration publisher is required")
	}
	if err := pin.verify(options.Options); err != nil {
		return MigrationResult{}, err
	}
	publication := MigrationPublication{
		ProjectID: pin.mapping.ID, PreparedGeneration: plan.Preview.GenerationID,
		Plan: presentation.RenderPlan{
			ProjectID: pin.mapping.ID, GenerationID: plan.Preview.GenerationID,
			ProjectViewDigest: plan.Accepted.Review.ProjectViewDigest,
			Files:             migrationFilePlan(plan),
		},
		Mapping: pin.mapping, DataRoot: pin.data.Path, Preview: plan.Preview,
		PublicationLock: publicationOwner,
		syncDataRoot:    pin.syncData.Root,
	}
	if err := options.Publish(ctx, publication); err != nil {
		return MigrationResult{}, err
	}
	if err := pin.verify(options.Options); err != nil {
		return MigrationResult{}, err
	}
	result.Applied = true
	return result, nil
}

func migrationFilePlan(result migrationv4.Result) []presentation.FilePlan {
	files := []presentation.FilePlan{
		{Relative: migrationv4.ReviewRelativePath, Desired: result.Review, Mode: 0o644},
		{Relative: migrationv4.HistoryRelativePath, Desired: result.History, Mode: 0o644},
		{Relative: migrationv4.LedgerRelativePath, Desired: result.Ledger, Mode: 0o600},
		{Relative: migrationv4.SessionIndexRelativePath, Desired: result.SessionIndex, Mode: 0o600},
	}
	for index := range files {
		preimage := result.TargetPreimages[files[index].Relative]
		files[index].ExpectedExists = preimage.Exists
		files[index].Expected = append([]byte(nil), preimage.Bytes...)
	}
	return files
}

func buildMigrationFromPin(pin *MappingPin) (migrationv4.Result, error) {
	preimages := make(map[string]migrationv4.Preimage, 4)
	read := func(relative string, required bool) ([]byte, error) {
		body, found, err := pin.project.ReadRegularOptional(relative, 64<<20)
		if err != nil {
			return nil, err
		}
		if required && !found {
			return nil, fmt.Errorf("required v3 source %q is missing", relative)
		}
		preimages[relative] = migrationv4.Preimage{Exists: found, Bytes: append([]byte(nil), body...)}
		return body, nil
	}
	review, err := read(migrationv4.ReviewRelativePath, true)
	if err != nil {
		return migrationv4.Result{}, err
	}
	history, err := read(migrationv4.HistoryRelativePath, true)
	if err != nil {
		return migrationv4.Result{}, err
	}
	ledger, err := read(migrationv4.LedgerRelativePath, true)
	if err != nil {
		return migrationv4.Result{}, err
	}
	if _, err := read(migrationv4.SessionIndexRelativePath, false); err != nil {
		return migrationv4.Result{}, err
	} else if preimages[migrationv4.SessionIndexRelativePath].Exists {
		return migrationv4.Result{}, errors.New("partial v4 projection cannot be migrated")
	}

	store, err := memorystore.Open(pin.data.Path, pin.mapping.ID)
	if err != nil {
		return migrationv4.Result{}, err
	}
	defer store.Close()
	_, manifest, err := store.LoadPrepared()
	if err != nil {
		return migrationv4.Result{}, err
	}
	index, err := migrationSessionIndex(store, manifest)
	if err != nil {
		return migrationv4.Result{}, err
	}
	return migrationv4.BuildPreview(migrationv4.Input{
		Review: review, History: history, Ledger: ledger, SessionIndex: index,
		GenerationID: manifest.GenerationID, SessionViewDependencyDigests: manifestSessionDigests(manifest),
		TargetPreimages: preimages,
	})
}

func migrationSessionIndex(store *memorystore.Store, manifest memory.GenerationManifest) ([]byte, error) {
	entries := make([]sessionindex.Entry, 0, len(manifest.SessionViews))
	coverage := sessionindex.IndexCoverage{Total: uint64(len(manifest.SessionViews))}
	for _, dependency := range manifest.SessionViews {
		body, err := store.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if err != nil {
			return nil, err
		}
		var view memory.SessionView
		if err := json.Unmarshal(body, &view); err != nil {
			return nil, err
		}
		entry, err := migrationIndexEntry(view, dependency, manifest.GenerationID)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
		addIndexCoverage(&coverage, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].StartedAt != entries[j].StartedAt {
			return entries[i].StartedAt > entries[j].StartedAt
		}
		if entries[i].Provider != entries[j].Provider {
			return entries[i].Provider < entries[j].Provider
		}
		return entries[i].SessionID < entries[j].SessionID
	})
	return sessionindex.Render(sessionindex.Document{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: manifest.ProjectID,
		GenerationID: manifest.GenerationID, ProjectViewDigest: manifest.ProjectViewDigest,
		GeneratedAt: manifest.CreatedAt, SortVersion: sessionindex.SortVersion,
		Coverage: coverage, Sessions: entries,
	})
}

func migrationIndexEntry(view memory.SessionView, dependency memory.SessionViewDependency, generationID string) (sessionindex.Entry, error) {
	state, reason := sessionindex.ProcessingComplete, ""
	availability := "available"
	if view.SourceAvailability != memory.SourceAvailable {
		availability = "unavailable"
	}
	switch view.TerminalState {
	case memory.Indexed:
	case memory.Unsupported:
		state, reason = sessionindex.ProcessingError, "unsupported_source_records"
	case memory.Missing:
		state, reason = sessionindex.ProcessingUnprocessed, "source_missing"
	case memory.Unreadable:
		state, reason = sessionindex.ProcessingError, "source_unreadable"
	case memory.Ambiguous:
		state, reason = sessionindex.ProcessingError, "source_ambiguous"
	default:
		return sessionindex.Entry{}, errors.New("unsupported SessionView terminal state")
	}
	reasons := []string{}
	if reason != "" {
		reasons = append(reasons, reason)
	}
	terminal := string(view.TerminalState)
	duration := migrationDuration(view.StartedAt, view.EndedAt)
	recordCount := uint64(len(view.ActiveRevisionIDs))
	sessionDigest := dependency.Digest
	usageDigest := view.UsageRecordDigest
	lastSeen := generationID
	var lastSuccessful *string
	if state == sessionindex.ProcessingComplete {
		value := generationID
		lastSuccessful = &value
	}
	entryCoverage := sessionindex.Coverage{Seen: uint64(len(view.ObservationSummaries)), Indexed: uint64(len(view.ObservationSummaries))}
	facts := sessionindex.FactCounts{}
	for _, observation := range view.ObservationSummaries {
		switch observation.Kind {
		case "file":
			facts.FileChange++
		case "command", "tool":
			facts.Command++
		case "test", "verification", "build":
			facts.Verification++
		case "error":
			facts.Error++
		case "artifact", "commit", "release", "deployment":
			facts.Artifact++
		}
	}
	return sessionindex.Entry{
		Provider: view.Provider, SessionID: view.SessionID, ProcessingState: state,
		StateReasonCodes: reasons, SourceAvailability: availability, SourceTerminalState: &terminal,
		StartedAt: view.StartedAt, EndedAt: view.EndedAt, DurationMS: duration,
		WarningCount: uint64(len(view.Diagnostics)), RecordCount: &recordCount,
		IndexedEventCount: entryCoverage.Indexed, Coverage: entryCoverage, FactCounts: facts,
		SessionViewDigest: &sessionDigest, UsageRecordDigest: &usageDigest,
		SummaryDigest: nil, LastSeenGenerationID: &lastSeen, LastSuccessfulGenerationID: lastSuccessful,
	}, nil
}

func migrationDuration(startedAt, endedAt string) *uint64 {
	start, startErr := time.Parse(time.RFC3339Nano, startedAt)
	end, endErr := time.Parse(time.RFC3339Nano, endedAt)
	if startErr != nil || endErr != nil || end.Before(start) {
		return nil
	}
	value := uint64(end.Sub(start) / time.Millisecond)
	return &value
}

func addIndexCoverage(coverage *sessionindex.IndexCoverage, entry sessionindex.Entry) {
	switch entry.ProcessingState {
	case sessionindex.ProcessingComplete:
		coverage.Complete++
	case sessionindex.ProcessingPartial:
		coverage.Partial++
	case sessionindex.ProcessingError:
		coverage.Error++
	case sessionindex.ProcessingUnprocessed:
		coverage.Unprocessed++
	}
	if entry.SourceAvailability == "available" {
		coverage.SourceAvailable++
	} else {
		coverage.SourceUnavailable++
	}
	coverage.StartedAtKnown++
	coverage.EndedAtKnown++
	if entry.UsageRecordDigest != nil {
		coverage.UsageKnown++
	}
}

func manifestSessionDigests(manifest memory.GenerationManifest) []string {
	result := make([]string, 0, len(manifest.SessionViews))
	for _, dependency := range manifest.SessionViews {
		result = append(result, dependency.Digest)
	}
	sort.Strings(result)
	return result
}
