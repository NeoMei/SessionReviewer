package contextupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/projectprobe"
	"github.com/neomei/SessionReviewer/internal/projectview"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/scan"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/sessionview"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

func TestScanPublicationKeepsEqualNativeSessionIDsProviderQualified(t *testing.T) {
	const projectID = "project-provider-collision"
	const nativeSessionID = "same-native-session"
	projectRoot, vaultRoot, dataRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	mapping := config.ProjectMapping{ID: projectID, Root: projectRoot, VaultRoot: vaultRoot, VaultReviewPath: "Projects/Collision/Session Review", VaultCaseMode: platform.CaseSensitive}
	binding, err := projectidentity.Resolve(mapping, projectRoot, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := sourcecatalog.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 9, 4, 0, 0, 0, time.UTC)
	adapters := []source.NamedAdapter{
		{Provider: "codex", Adapter: newProviderCollisionAdapter("codex", nativeSessionID, projectID, projectRoot), Required: true},
		{Provider: "claude", Adapter: newProviderCollisionAdapter("claude", nativeSessionID, projectID, projectRoot), Required: true},
	}
	probe := func(ctx context.Context, options projectprobe.Options) (memory.ProjectProbeState, memory.ProbeCheck, error) {
		if err := ctx.Err(); err != nil {
			return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
		}
		state := memory.ProjectProbeState{SchemaVersion: memory.MemorySchemaVersion, ProjectID: projectID, CanonicalRoot: options.Binding.CanonicalRoot, Branch: "main", Head: strings.Repeat("a", 40), RemoteIdentityHashes: []string{}, VersionFiles: []memory.ProbeFile{}, RequiredProjectionFiles: []memory.ProbeFile{}, ProbeVersion: projectprobe.ProbeVersion, Diagnostics: []memory.Diagnostic{}}
		state.Digest, err = memory.ProjectProbeStateDigest(state)
		if err != nil {
			return memory.ProjectProbeState{}, memory.ProbeCheck{}, err
		}
		return state, memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: options.Now().UTC().Format(time.RFC3339Nano), StateDigest: state.Digest, Available: true, Diagnostics: []memory.Diagnostic{}}, nil
	}
	result, err := scan.Run(t.Context(), scan.Options{
		ProjectID: projectID, Binding: binding, SessionsRoot: sessionsRoot, DataRoot: dataRoot,
		Adapters: adapters, Catalog: catalog, Store: store, Workers: 2, Now: func() time.Time { return now },
		Materialize: sessionview.Materialize, Probe: probe, ProbeOptions: projectprobe.Options{Binding: binding, Now: func() time.Time { return now }}, Reduce: projectview.Reduce,
	})
	if err != nil || !result.Prepared || result.IndexedSessions != 2 || result.ReviewRunTokens != 0 {
		t.Fatalf("two-provider scan result=%+v err=%v", result, err)
	}
	prepared, manifest, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	viewBody, err := store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		t.Fatal(err)
	}
	var view memory.ProjectView
	if err := json.Unmarshal(viewBody, &view); err != nil {
		t.Fatal(err)
	}
	indexBody, err := store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
	if err != nil {
		t.Fatal(err)
	}
	index, err := sessionindex.Parse(indexBody)
	if err != nil {
		t.Fatal(err)
	}
	projectAccounting, _, err := loadProjectionAccounting(catalog, view)
	if err != nil {
		t.Fatal(err)
	}
	milestones, err := loadScanMilestones(t.Context(), store, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(milestones.Timeline) != 2 || len(milestones.ChainDependencies) != 2 {
		t.Fatalf("provider-qualified projection collapsed before publication: timeline=%+v dependencies=%+v", milestones.Timeline, milestones.ChainDependencies)
	}
	publicationResult, err := publishV4Scan(t.Context(), v4PublishInput{ProjectID: projectID, DataRoot: dataRoot, Mapping: mapping, PreparedGeneration: prepared.GenerationID, Index: index, Accounting: projectAccounting, Milestones: milestones, Store: store, Manifest: manifest, Now: func() time.Time { return now }})
	if err != nil || publicationResult.GenerationID != manifest.GenerationID {
		t.Fatalf("publish provider-qualified scan: result=%+v err=%v", publicationResult, err)
	}
	project := loadProviderCollisionProjection(t, projectRoot, "")
	vault := loadProviderCollisionProjection(t, vaultRoot, mapping.VaultReviewPath)
	if !reflect.DeepEqual(project.Review.Timeline, vault.Review.Timeline) || !reflect.DeepEqual(project.Review.ChainDependencies, vault.Review.ChainDependencies) {
		t.Fatal("Project and Vault lost provider-qualified equality")
	}
	assertProviderCollisionProjection(t, project, nativeSessionID)
	assertProviderCollisionProjection(t, vault, nativeSessionID)
}

type providerCollisionAdapter struct {
	provider  string
	candidate source.Candidate
	boundary  source.Boundary
	record    memory.SourceRecord
	revision  memory.ObservationRevision
	messages  []conversationchain.SourceMessage
}

func newProviderCollisionAdapter(provider, sessionID, projectID, projectRoot string) *providerCollisionAdapter {
	started, ended := "2026-09-09T03:00:00Z", "2026-09-09T03:00:03Z"
	identity := provider + "-source-identity"
	candidate := source.Candidate{Provider: provider, SessionID: sessionID, StartedAt: started, InitialCWD: projectRoot, Handle: provider + "-candidate"}
	boundary := source.Boundary{Candidate: candidate, SourceIdentity: identity, Frozen: memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 3, ByteOffset: 300}}, SourceHash: providerCollisionHash(provider + "-boundary")}, Segments: []source.SegmentBoundary{{Ordinal: 1, Size: 300, SourceHash: providerCollisionHash(provider + "-segment")}}, TerminalState: memory.Indexed, Handle: provider + "-boundary"}
	record := memory.SourceRecord{SchemaVersion: memory.MemorySchemaVersion, Provider: provider, SessionID: sessionID, SourceIdentity: identity, StartedAt: started, EndedAt: ended, FrozenBoundary: boundary.Frozen, Availability: memory.SourceAvailable, Usage: accounting.SessionUsage{StartedAt: started, EndedAt: ended, DurationMS: 3000, Models: []accounting.ModelUsage{{Model: "fixture-model", TokenUsage: accounting.TokenUsage{}}}, TotalTokens: 0}, ProjectIDs: []string{projectID}}
	revision := memory.ObservationRevision{SchemaVersion: memory.MemorySchemaVersion, Key: memory.ObservationKey{Provider: provider, SessionID: sessionID, SourceIdentity: identity, Sequence: 2, ProjectID: projectID, Kind: "verification", Subject: provider + "-tests"}, Ref: memory.SourceRef{Provider: provider, SessionID: sessionID, SourceIdentity: identity, Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 2, ByteOffset: 100}}, SourceHash: providerCollisionHash(provider + "-verification")}, Timestamp: "2026-09-09T03:00:02Z", Operation: "verification", Object: provider + "-suite", Outcome: "passed", Fields: map[string]string{"component": provider + ":suite", "passed": "true", "failed": "false"}, AdapterID: "fixture-" + provider, AdapterVersion: "v1"}
	revision.RevisionID = memory.ObservationRevisionID(revision)
	return &providerCollisionAdapter{provider: provider, candidate: candidate, boundary: boundary, record: record, revision: revision, messages: []conversationchain.SourceMessage{{Role: conversationchain.RoleUser, Text: "Verify " + provider + " provider.", OccurredAt: started, RecordHash: providerCollisionHash(provider + "-user"), RecordOrdinal: 1}, {Role: conversationchain.RoleAssistant, Phase: "final_answer", Text: provider + " provider completed independently.", OccurredAt: ended, RecordHash: providerCollisionHash(provider + "-answer"), RecordOrdinal: 3}}}
}

func (adapter *providerCollisionAdapter) Discover(ctx context.Context) (source.Discovery, error) {
	if err := ctx.Err(); err != nil {
		return source.Discovery{}, err
	}
	return source.Discovery{Candidates: []source.Candidate{adapter.candidate}}, nil
}

func (adapter *providerCollisionAdapter) Freeze(ctx context.Context, candidate source.Candidate) (source.Boundary, error) {
	if err := ctx.Err(); err != nil {
		return source.Boundary{}, err
	}
	if !reflect.DeepEqual(candidate, adapter.candidate) {
		return source.Boundary{}, errors.New("unexpected provider-collision candidate")
	}
	return adapter.boundary, nil
}

func (adapter *providerCollisionAdapter) Decode(ctx context.Context, boundary source.Boundary, visit func(memory.ObservationRevision) error) (source.DecodeReport, error) {
	if err := ctx.Err(); err != nil {
		return source.DecodeReport{}, err
	}
	if !reflect.DeepEqual(boundary, adapter.boundary) {
		return source.DecodeReport{}, errors.New("unexpected provider-collision boundary")
	}
	if err := visit(adapter.revision); err != nil {
		return source.DecodeReport{}, err
	}
	return source.DecodeReport{BoundaryRelation: source.BoundaryInitial, ProposedSource: adapter.record, TerminalState: memory.Indexed, EmittedRevisions: 1}, nil
}

func (*providerCollisionAdapter) Read(context.Context, memory.SourceRef, int64) ([]byte, error) {
	return nil, errors.New("provider-collision fixture never performs explicit source reads")
}

func (adapter *providerCollisionAdapter) ReadVisiblePrefix(ctx context.Context, record memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	if err := ctx.Err(); err != nil {
		return nil, conversationchain.VisibleCoverage{}, err
	}
	if !reflect.DeepEqual(record, adapter.record) {
		return nil, conversationchain.VisibleCoverage{}, errors.New("unexpected provider-collision source record")
	}
	return append([]conversationchain.SourceMessage(nil), adapter.messages...), conversationchain.VisibleCoverage{SourceRecords: 3, VisibleMessages: 2, CapturedMessages: 2, Complete: true}, nil
}

func loadProviderCollisionProjection(t *testing.T, root, vaultReviewPath string) reviewv4.Accepted {
	t.Helper()
	paths := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, presentation.SessionIndexRelativePath}
	bodies := make([][]byte, 0, len(paths))
	for _, relative := range paths {
		if vaultReviewPath != "" {
			relative = filepath.ToSlash(filepath.Join(vaultReviewPath, strings.TrimPrefix(relative, "docs/session-review/")))
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
	}
	accepted, err := reviewv4.LoadProjection(bodies[0], bodies[1], bodies[2], bodies[3])
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func assertProviderCollisionProjection(t *testing.T, accepted reviewv4.Accepted, nativeSessionID string) {
	t.Helper()
	if len(accepted.Review.Timeline) != 2 || len(accepted.Review.ChainDependencies) != 2 {
		t.Fatalf("provider-qualified output collapsed: timeline=%+v dependencies=%+v", accepted.Review.Timeline, accepted.Review.ChainDependencies)
	}
	providers, views, dependencies, milestones := []string{}, map[string]string{}, map[string]string{}, map[string]string{}
	for _, dependency := range accepted.Review.ChainDependencies {
		if dependency.SessionID != nativeSessionID {
			t.Fatalf("native Session ID changed: %+v", dependency)
		}
		providers = append(providers, dependency.Provider)
		views[dependency.Provider] = dependency.SessionViewDigest
		dependencies[dependency.Provider] = dependency.DependencyDigest
	}
	for _, milestone := range accepted.Review.Timeline {
		if len(milestone.ClosedLoop.SourceTurnRefs) != 1 || milestone.ClosedLoop.SourceTurnRefs[0].SessionID != nativeSessionID {
			t.Fatalf("milestone source identity changed: %+v", milestone)
		}
		milestones[milestone.ClosedLoop.SourceTurnRefs[0].Provider] = milestone.ID
	}
	sort.Strings(providers)
	if !reflect.DeepEqual(providers, []string{"claude", "codex"}) || views["claude"] == views["codex"] || dependencies["claude"] == dependencies["codex"] || milestones["claude"] == milestones["codex"] || milestones["claude"] == "" || milestones["codex"] == "" {
		t.Fatalf("provider-qualified identities merged: providers=%v views=%v dependencies=%v milestones=%v", providers, views, dependencies, milestones)
	}
}

func providerCollisionHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
