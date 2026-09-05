package syncproject

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/migrationv4"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

type MigrationMode string

const (
	MigrationDryRun  MigrationMode = "dry-run"
	MigrationConfirm MigrationMode = "confirm-migration"
)

var (
	ErrMigrationRequired        = errors.New("migration_required")
	ErrMigrationPreviewStale    = errors.New("migration_preview_stale")
	ErrMarkdownRecoveryRequired = errors.New("markdown_recovery_required")
)

type MigrationPublication struct {
	ProjectID          string
	PreparedGeneration string
	Plan               presentation.RenderPlan
	Mapping            config.ProjectMapping
	DataRoot           string
	Preview            migrationv4.MigrationPreview
	PublicationLock    *publicationlock.Owner
	SuccessorManifest  *memory.GenerationManifest
	VaultExpected      map[string][]byte

	syncDataRoot *os.Root
}

type MigrationPublisher func(context.Context, MigrationPublication) error
type MigrationRecoverer func(context.Context, config.ProjectMapping, string, *publicationlock.Owner) error

type MigrationOptions struct {
	Options
	Mode                  MigrationMode
	ExpectedPreviewDigest string
	Publish               MigrationPublisher
	Recover               MigrationRecoverer

	build               func(*MappingPin) (migrationv4.Result, error)
	afterMigrationBuild func() error
}

type MigrationResult struct {
	Preview migrationv4.MigrationPreview `json:"preview"`
	Applied bool                         `json:"applied"`
}

// RecoverMarkdownBeforeFormat resolves a validated nonterminal Markdown
// intent before callers inspect a possibly mixed public projection. Dry-run
// reports the durable recovery requirement without acquiring or creating
// either lock and without invoking recovery.
func RecoverMarkdownBeforeFormat(ctx context.Context, options Options, recover MigrationRecoverer) (_ bool, retErr error) {
	if ctx == nil {
		return false, errors.New("migration context is required")
	}
	pin, err := PinMapping(options)
	if err != nil {
		return false, err
	}
	defer func() { retErr = errors.Join(retErr, pin.Close()) }()
	state, err := publicationstate.OpenReadOnly(pin.data.Path, pin.mapping.ID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	intent, intentErr := state.Intent()
	closeErr := state.Close()
	if intentErr != nil || closeErr != nil {
		return false, errors.Join(intentErr, closeErr)
	}
	if intent.Version != 2 || intent.Kind != publicationstate.KindMarkdown || intent.Stage == publicationstate.StageCommitted {
		return false, nil
	}
	if options.DryRun {
		return false, ErrMarkdownRecoveryRequired
	}
	if recover == nil {
		return false, errors.New("Markdown recovery callback is required")
	}
	publicationOwner, err := publicationlock.Acquire(pin.data.Path, pin.mapping.ID, 10*time.Second)
	if err != nil {
		return false, errors.New("public projection is locked or unsafe")
	}
	defer func() { retErr = errors.Join(retErr, publicationOwner.Release()) }()
	if err := pin.syncData.EnsureDirectory("locks", 0o700); err != nil {
		return false, fmt.Errorf("initialize recovery sync lock directory: %w", err)
	}
	lock, err := project.AcquireProjectLock(pin.syncData.Root, "locks/sync.lock", 10*time.Second)
	if err != nil {
		return false, fmt.Errorf("sync project is locked or unsafe: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	if err := recover(ctx, pin.mapping, pin.data.Path, publicationOwner); err != nil {
		return false, err
	}
	if err := pin.verify(options); err != nil {
		return false, err
	}
	return true, nil
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
	build := options.build
	if build == nil {
		build = func(pin *MappingPin) (migrationv4.Result, error) {
			return buildMigrationFromPin(pin, options.afterMigrationBuild)
		}
	}
	if options.Mode == MigrationDryRun {
		if err := pin.verify(options.Options); err != nil {
			return MigrationResult{}, err
		}
		plan, err := build(pin)
		if err != nil {
			return MigrationResult{}, fmt.Errorf("build v4 migration preview: %w", err)
		}
		if err := pin.verify(options.Options); err != nil {
			return MigrationResult{}, err
		}
		return MigrationResult{Preview: plan.Preview}, nil
	}
	publicationOwner, err := publicationlock.Acquire(pin.data.Path, pin.mapping.ID, 10*time.Second)
	if err != nil {
		return MigrationResult{}, errors.New("public projection is locked or unsafe")
	}
	defer func() { retErr = errors.Join(retErr, publicationOwner.Release()) }()
	if err := pin.syncData.EnsureDirectory("locks", 0o700); err != nil {
		return MigrationResult{}, fmt.Errorf("initialize migration sync lock directory: %w", err)
	}
	lock, err := project.AcquireProjectLock(pin.syncData.Root, "locks/sync.lock", 10*time.Second)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("sync project is locked or unsafe: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	if options.Recover != nil {
		if err := options.Recover(ctx, pin.mapping, pin.data.Path, publicationOwner); err != nil {
			return MigrationResult{}, err
		}
	}
	if err := pin.verify(options.Options); err != nil {
		return MigrationResult{}, err
	}
	plan, err := build(pin)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("build v4 migration preview: %w", err)
	}
	result := MigrationResult{Preview: plan.Preview}
	if len(plan.Preview.BlockingReasons) != 0 || !completeMigrationTargetHashes(plan.Preview.TargetHashes) {
		return MigrationResult{}, errors.New("blocked migration preview cannot be confirmed")
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
		PublicationLock:   publicationOwner,
		SuccessorManifest: plan.SuccessorManifest,
		VaultExpected:     migrationVaultExpected(plan.TargetVaultPreimages),
		syncDataRoot:      pin.syncData.Root,
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

func completeMigrationTargetHashes(hashes migrationv4.ArtifactHashes) bool {
	return hashes.Review != "" && hashes.History != "" && hashes.Ledger != "" && hashes.SessionIndex != ""
}

func migrationVaultExpected(preimages map[string]migrationv4.Preimage) map[string][]byte {
	result := make(map[string][]byte, len(preimages))
	for relative, preimage := range preimages {
		if preimage.Exists {
			result[relative] = append([]byte(nil), preimage.Bytes...)
		} else {
			result[relative] = nil
		}
	}
	return result
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

func buildMigrationFromPin(pin *MappingPin, afterBuild ...func() error) (migrationv4.Result, error) {
	preimages := make(map[string]migrationv4.Preimage, 4)
	vaultPreimages := make(map[string]migrationv4.Preimage, 4)
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
	indexSource, err := read(migrationv4.SessionIndexRelativePath, false)
	if err != nil {
		return migrationv4.Result{}, err
	}

	store, err := memorystore.OpenReadOnly(pin.data.Path, pin.mapping.ID)
	if err != nil {
		return migrationv4.Result{}, err
	}
	defer store.Close()
	prepared, preparedManifest, err := store.LoadPrepared()
	if err != nil {
		return migrationv4.Result{}, err
	}
	manifest := preparedManifest
	var successor *migrationv4.BindingSuccessor
	var sourceManifestDigest string
	var sourceIndexDigest string
	var sourceJournalDigest string
	var sourceIntent publicationstate.Intent
	var intentErr error
	var privateEvidence map[string][]byte
	if len(indexSource) != 0 {
		publishedID, publishedManifest, loadErr := store.LoadPublished()
		if loadErr != nil || publishedID != publishedManifest.GenerationID {
			return migrationv4.Result{}, errors.Join(errors.New("old v4 migration source is not an accepted published generation"), loadErr)
		}
		manifest = publishedManifest
		manifestDigest, digestErr := memory.Digest(manifest)
		if digestErr != nil {
			return migrationv4.Result{}, digestErr
		}
		sourceManifestDigest = manifestDigest
		state, stateErr := publicationstate.OpenReadOnly(pin.data.Path, pin.mapping.ID)
		if stateErr != nil {
			return migrationv4.Result{}, stateErr
		}
		sourceIntent, intentErr = state.Intent()
		closeErr := state.Close()
		if intentErr != nil || closeErr != nil {
			return migrationv4.Result{}, errors.Join(intentErr, closeErr)
		}
		sourceJournalDigest, err = memory.Digest(sourceIntent)
		if err != nil {
			return migrationv4.Result{}, err
		}
		if sourceIntent.Version == 2 && sourceIntent.Stage == publicationstate.StageCommitted && sourceIntent.Outcome == publicationstate.OutcomeRolledBack && sourceIntent.MigrationSource != nil {
			sourceJournalDigest = sourceIntent.MigrationSource.JournalDigest
		}
		privateEvidence, loadErr = migrationPrivateEvidence(store, manifest)
		if loadErr != nil {
			return migrationv4.Result{}, loadErr
		}
		projectViewBody := privateEvidence["project-view"]
		var projectView memory.ProjectView
		if err := json.Unmarshal(projectViewBody, &projectView); err != nil {
			return migrationv4.Result{}, err
		}
		parsedSourceIndex, parseErr := sessionindex.Parse(indexSource)
		if parseErr != nil {
			return migrationv4.Result{}, parseErr
		}
		if parsedSourceIndex.ProjectID != manifest.ProjectID || parsedSourceIndex.GenerationID != manifest.GenerationID || parsedSourceIndex.ProjectViewDigest != manifest.ProjectViewDigest {
			return migrationv4.Result{}, errors.New("public source index identity does not match published generation")
		}
		sourceIndexDigest = parsedSourceIndex.Digest
		if manifest.SessionIndexDigest != "" {
			privateIndexBody, loadErr := store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
			if loadErr != nil || !bytes.Equal(privateIndexBody, indexSource) {
				return migrationv4.Result{}, errors.Join(errors.New("public source index does not match authenticated private object"), loadErr)
			}
		} else {
			views, loadErr := migrationSessionViews(store, manifest)
			if loadErr != nil {
				return migrationv4.Result{}, loadErr
			}
			built, buildErr := migrationv4.BuildBindingSuccessor(migrationv4.BindingSuccessorInput{
				SourceManifest: manifest, SourceManifestDigest: manifestDigest,
				ProjectView: projectView, SessionViews: views, SourceIndex: &parsedSourceIndex,
			})
			if buildErr != nil {
				return migrationv4.Result{}, buildErr
			}
			successor = &built
		}
	}
	index := indexSource
	if successor != nil {
		index, err = sessionindex.Render(successor.Index)
		if err != nil {
			return migrationv4.Result{}, err
		}
	}
	if len(index) == 0 {
		index, err = migrationSessionIndex(store, manifest)
		if err != nil {
			return migrationv4.Result{}, err
		}
	}
	for _, relative := range []string{migrationv4.ReviewRelativePath, migrationv4.HistoryRelativePath, migrationv4.LedgerRelativePath, migrationv4.SessionIndexRelativePath} {
		vaultRelative := filepath.ToSlash(filepath.Join(pin.mapping.VaultReviewPath, strings.TrimPrefix(relative, "docs/session-review/")))
		body, found, readErr := pin.vault.ReadRegularOptional(vaultRelative, 64<<20)
		if readErr != nil {
			return migrationv4.Result{}, readErr
		}
		vaultPreimages[relative] = migrationv4.Preimage{Exists: found, Bytes: append([]byte(nil), body...)}
	}
	if len(indexSource) != 0 {
		if err := validateMigrationSourceIntent(sourceIntent, pin.mapping, manifest, sourceManifestDigest, sourceIndexDigest, preimages, vaultPreimages); err != nil {
			return migrationv4.Result{}, err
		}
	}
	result, err := migrationv4.BuildMarkdownPreview(migrationv4.MarkdownMigrationInput{Source: migrationv4.Input{
		Review: review, History: history, Ledger: ledger, SessionIndex: index,
		SourceSessionIndex: indexSource,
		GenerationID: func() string {
			if successor != nil {
				return successor.Manifest.GenerationID
			}
			return manifest.GenerationID
		}(), SessionViewDependencyDigests: manifestSessionDigests(manifest),
		SourceManifestDigest: sourceManifestDigest,
		TargetManifestDigest: func() string {
			if successor != nil {
				return successor.TargetManifestDigest
			}
			return sourceManifestDigest
		}(), SourceJournalDigest: sourceJournalDigest,
		TargetPreimages: preimages, TargetVaultPreimages: vaultPreimages,
	}})
	if err != nil {
		return migrationv4.Result{}, err
	}
	if successor != nil {
		result.SuccessorManifest = &successor.Manifest
	}
	if len(afterBuild) != 0 && afterBuild[0] != nil {
		if err := afterBuild[0](); err != nil {
			return migrationv4.Result{}, err
		}
	}
	for relative, before := range preimages {
		after, found, readErr := pin.project.ReadRegularOptional(relative, 64<<20)
		if readErr != nil || found != before.Exists || !bytes.Equal(after, before.Bytes) {
			return migrationv4.Result{}, errors.Join(ErrMigrationPreviewStale, readErr)
		}
	}
	for relative, before := range vaultPreimages {
		vaultRelative := filepath.ToSlash(filepath.Join(pin.mapping.VaultReviewPath, strings.TrimPrefix(relative, "docs/session-review/")))
		after, found, readErr := pin.vault.ReadRegularOptional(vaultRelative, 64<<20)
		if readErr != nil || found != before.Exists || !bytes.Equal(after, before.Bytes) {
			return migrationv4.Result{}, errors.Join(ErrMigrationPreviewStale, readErr)
		}
	}
	afterPrepared, afterPreparedManifest, loadErr := store.LoadPrepared()
	if loadErr != nil || afterPrepared != prepared || !reflect.DeepEqual(afterPreparedManifest, preparedManifest) {
		return migrationv4.Result{}, errors.Join(ErrMigrationPreviewStale, loadErr)
	}
	if len(indexSource) != 0 {
		afterID, afterManifest, loadErr := store.LoadPublished()
		if loadErr != nil || afterID != manifest.GenerationID || !reflect.DeepEqual(afterManifest, manifest) {
			return migrationv4.Result{}, errors.Join(ErrMigrationPreviewStale, loadErr)
		}
		afterEvidence, evidenceErr := migrationPrivateEvidence(store, manifest)
		if evidenceErr != nil || !reflect.DeepEqual(afterEvidence, privateEvidence) {
			return migrationv4.Result{}, errors.Join(ErrMigrationPreviewStale, evidenceErr)
		}
		state, stateErr := publicationstate.OpenReadOnly(pin.data.Path, pin.mapping.ID)
		if stateErr != nil {
			return migrationv4.Result{}, errors.Join(ErrMigrationPreviewStale, stateErr)
		}
		afterIntent, intentReadErr := state.Intent()
		closeErr := state.Close()
		if intentReadErr != nil || closeErr != nil || !reflect.DeepEqual(afterIntent, sourceIntent) {
			return migrationv4.Result{}, errors.Join(ErrMigrationPreviewStale, intentReadErr, closeErr)
		}
	}
	return result, nil
}

func migrationPrivateEvidence(store *memorystore.Store, manifest memory.GenerationManifest) (map[string][]byte, error) {
	result := make(map[string][]byte, 2+len(manifest.SessionViews))
	projectView, err := store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		return nil, err
	}
	result["project-view"] = projectView
	for _, dependency := range manifest.SessionViews {
		body, loadErr := store.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if loadErr != nil {
			return nil, loadErr
		}
		result["session-view\x00"+dependency.Provider+"\x00"+dependency.SessionID] = body
	}
	if manifest.SessionIndexDigest != "" {
		body, loadErr := store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
		if loadErr != nil {
			return nil, loadErr
		}
		result["session-index"] = body
	}
	return result, nil
}

func validateMigrationSourceIntent(intent publicationstate.Intent, mapping config.ProjectMapping, manifest memory.GenerationManifest, manifestDigest, sourceIndexDigest string, project, vault map[string]migrationv4.Preimage) error {
	var destinations []publicationstate.Destination
	if intent.Version == 1 && intent.Stage == publicationstate.StageCommitted && intent.ProjectID == mapping.ID && intent.GenerationID == manifest.GenerationID && intent.ManifestDigest == manifestDigest && intent.ProjectViewDigest == manifest.ProjectViewDigest {
		destinations = intent.Destinations
	} else if intent.Version == 2 && intent.Stage == publicationstate.StageCommitted && intent.Outcome == publicationstate.OutcomeRolledBack && intent.ProjectID == mapping.ID && intent.MigrationSource != nil {
		proof := intent.MigrationSource
		if proof.GenerationID != manifest.GenerationID || proof.ManifestDigest != manifestDigest || proof.ProjectViewDigest != manifest.ProjectViewDigest || proof.IndexDigest != sourceIndexDigest {
			return errors.New("rolled-back migration source proof no longer matches published source")
		}
		destinations = proof.Destinations
	} else {
		return errors.New("old v4 source has no matching committed publication journal")
	}
	want := make(map[string]string, 8)
	for relative, before := range project {
		if !before.Exists {
			return errors.New("old v4 source publication is incomplete")
		}
		want["project\x00"+relative] = bareMigrationHash(before.Bytes)
		vaultRelative := filepath.ToSlash(filepath.Join(mapping.VaultReviewPath, strings.TrimPrefix(relative, "docs/session-review/")))
		vaultBefore := vault[relative]
		if !vaultBefore.Exists {
			return errors.New("old v4 Vault source publication is incomplete")
		}
		want["vault\x00"+vaultRelative] = bareMigrationHash(vaultBefore.Bytes)
	}
	if len(destinations) != len(want) {
		return errors.New("old v4 source journal destination set is incomplete")
	}
	for _, destination := range destinations {
		if want[destination.Side+"\x00"+destination.Relative] != destination.DesiredSHA256 {
			return errors.New("old v4 source journal does not authenticate exact public bytes")
		}
	}
	return nil
}

func bareMigrationHash(body []byte) string {
	digest := sha256.Sum256(body)
	return fmt.Sprintf("%x", digest)
}

func migrationSessionViews(store *memorystore.Store, manifest memory.GenerationManifest) (map[sessionindex.SessionKey]*memory.SessionView, error) {
	views := make(map[sessionindex.SessionKey]*memory.SessionView, len(manifest.SessionViews))
	for _, dependency := range manifest.SessionViews {
		body, err := store.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
		if err != nil {
			return nil, err
		}
		var view memory.SessionView
		if err := json.Unmarshal(body, &view); err != nil {
			return nil, err
		}
		key := sessionindex.SessionKey{Provider: dependency.Provider, SessionID: dependency.SessionID}
		if view.Provider != key.Provider || view.SessionID != key.SessionID || view.ProjectID != manifest.ProjectID || view.Digest != dependency.Digest {
			return nil, errors.New("authenticated SessionView identity mismatch")
		}
		if _, duplicate := views[key]; duplicate {
			return nil, errors.New("duplicate authenticated SessionView identity")
		}
		copy := view
		views[key] = &copy
	}
	return views, nil
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
		if entries[i].StartedAt != nil || entries[j].StartedAt != nil {
			if entries[i].StartedAt == nil {
				return false
			}
			if entries[j].StartedAt == nil {
				return true
			}
			if *entries[i].StartedAt != *entries[j].StartedAt {
				return *entries[i].StartedAt > *entries[j].StartedAt
			}
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
		StartedAt: nullableTimestamp(view.StartedAt), EndedAt: nullableTimestamp(view.EndedAt), DurationMS: duration,
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

func nullableTimestamp(value string) *string {
	if value == "" {
		return nil
	}
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
	if entry.StartedAt != nil {
		coverage.StartedAtKnown++
	}
	if entry.EndedAt != nil {
		coverage.EndedAtKnown++
	}
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
