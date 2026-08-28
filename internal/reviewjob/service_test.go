package reviewjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/proposal"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

type workerAccepted struct {
	legacy ledger.State
}

func (state *workerAccepted) snapshot(t *testing.T) reviewv2.Accepted {
	t.Helper()
	projected, err := reviewv2.ProjectLegacy(state.legacy)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := reviewv2.LegacyState(projected)
	if err != nil {
		t.Fatal(err)
	}
	state.legacy = canonical
	return reviewv2.Accepted{State: projected, Legacy: canonical}
}

func (state *workerAccepted) apply(t *testing.T, changes ledger.ChangeSet) {
	t.Helper()
	next, err := ledger.ApplyChangeSetModel(state.legacy, changes)
	if err != nil {
		t.Fatal(err)
	}
	state.legacy = next
}

type verifiedWorkerAgent struct {
	capability      agent.Capability
	capabilityCalls int
	generateCalls   int
	generate        func(agent.Request) (agent.Result, error)
}

func (adapter *verifiedWorkerAgent) VerifiedCapability() agent.Capability {
	adapter.capabilityCalls++
	return adapter.capability
}

func (adapter *verifiedWorkerAgent) GenerateProposal(_ context.Context, request agent.Request) (agent.Result, error) {
	adapter.generateCalls++
	return adapter.generate(request)
}

func (adapter *verifiedWorkerAgent) Cancel(context.Context) error { return nil }

type workerFixture struct {
	root, project, vault, data string
	store                      Store
	job                        Job
	now                        time.Time
}

func newWorkerFixture(t *testing.T, sessions []FrozenSession) workerFixture {
	t.Helper()
	root := t.TempDir()
	fixture := workerFixture{
		root:    root,
		vault:   filepath.Join(root, "vault"),
		project: filepath.Join(root, "vault", "project"),
		data:    filepath.Join(root, "data"),
		now:     time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC),
	}
	for _, path := range []string{fixture.project, fixture.vault, fixture.data} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []*string{&fixture.project, &fixture.vault, &fixture.data} {
		directory, err := pathguard.Open(*target)
		if err != nil {
			t.Fatal(err)
		}
		*target = directory.Path
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
	}
	projectRoot, err := pathguard.Open(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	projectIdentity, err := projectRoot.PhysicalIdentity()
	if closeErr := projectRoot.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = Store{Root: fixture.data}
	fixture.job = Job{
		SchemaVersion:   PublicStatusSchemaVersion,
		ID:              "job-worker-1",
		ProjectID:       "project-1111111111111111",
		ProjectIdentity: projectIdentity,
		Agent: VerifiedAgent{
			Kind:       "fixture",
			Identity:   projectIdentity,
			Version:    "1.0.0",
			Executable: "/fixture/agent",
		},
		State:          Queued,
		Phase:          Preflight,
		Attempt:        1,
		FrozenSessions: append([]FrozenSession(nil), sessions...),
		CreatedAt:      fixture.now,
		UpdatedAt:      fixture.now,
	}
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: fixture.job.ProjectID, Root: fixture.project, VaultRoot: fixture.vault,
			VaultReviewPath: "Projects/Worker--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Create(fixture.job); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func workerInitialAccepted(projectID string) workerAccepted {
	return workerAccepted{legacy: ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID:    projectID,
			Revision:     1,
			Goal:         "Review the project",
			Branch:       "main",
			NextAction:   "Start",
			LastVerified: "2026-08-29T08:00:00Z",
			LastUpdated:  "2026-08-29T08:00:00Z",
		},
		Decisions: map[string]ledger.Decision{},
		OpenLoops: map[string]ledger.OpenLoop{},
		Sessions:  map[string]ledger.SessionReport{},
	}}
}

func workerCapability(provider, version string) agent.Capability {
	return agent.Capability{
		Provider: provider, Version: version, ProposalOnly: true, NoTools: true,
		ReadOnly: true, Containment: agent.ContainmentRestrictedReadOnly,
		StructuredOutput: true, NativeCancellation: true,
		ModelProvenance: agent.ModelProvenanceUnavailable,
	}
}

func workerSyncReport(projectID string) syncengine.Report {
	return syncengine.Report{
		ProjectID: projectID,
		Derived:   syncengine.DerivedReport{State: syncengine.DerivedCurrent, Operations: []syncengine.Operation{}},
		Machine:   syncengine.MachineReport{State: syncengine.MachineCurrent, Operations: []syncengine.Operation{}},
	}
}

func workerPacket(projectID, sessionID string, from, to int, expectedHash, nextHash string, hasMore bool, eventID string) evidence.Packet {
	expected := evidence.CursorBoundary{Line: from - 1, SourceHash: expectedHash}
	if from == 1 {
		expected.SourceHash = ""
	}
	return evidence.Packet{
		SchemaVersion: 2, ProjectID: projectID, SessionID: sessionID,
		CWD:        "/private/project-path-is-replaced-by-test",
		FromCursor: from, ToCursor: to,
		ExpectedCursor: expected,
		NextCursor:     evidence.CursorBoundary{Line: to, SourceHash: nextHash},
		HasMore:        hasMore,
		Events: []evidence.Item{{
			ID: eventID, Timestamp: "2026-08-29T08:01:00Z", JSONLLine: to,
			SourceHash: nextHash, Kind: "message", Role: "user", Summary: "bounded event",
		}},
	}
}

func workerDraft(t *testing.T, packet evidence.Packet, state ledger.State) []byte {
	t.Helper()
	digest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	item := packet.Events[len(packet.Events)-1]
	ref := ledger.EvidenceRef{
		EvidenceID: item.ID, SessionID: packet.SessionID, JSONLLine: item.JSONLLine,
		SourceHash: item.SourceHash, Summary: item.Summary,
	}
	nextAction := "Accepted " + item.ID
	sourceSessions := []string{packet.SessionID}
	refs := []ledger.EvidenceRef{ref}
	report := ledger.SessionReport{
		ID:        "session-report-" + strings.TrimPrefix(packet.SessionID, "session-"),
		ProjectID: packet.ProjectID, SessionID: packet.SessionID, Revision: 1,
		InitialGoal: "Review bounded evidence", GoalChanges: []string{}, Phases: []ledger.SessionPhase{},
		Files: []string{}, Commits: []string{}, Verification: []string{},
		DecisionsAdded: []string{}, DecisionsRevised: []string{},
		OpenLoopsCreated: []string{}, OpenLoopsClosed: []string{}, Evidence: refs,
	}
	for _, existing := range state.Sessions {
		if existing.SessionID == packet.SessionID {
			report = existing
			report.Revision++
			report.InitialGoal = "Review " + item.ID
			report.Evidence = refs
			report.Phases = []ledger.SessionPhase{}
			report.GoalChanges = []string{}
			report.Files = []string{}
			report.Commits = []string{}
			report.Verification = []string{}
			report.DecisionsAdded = []string{}
			report.DecisionsRevised = []string{}
			report.OpenLoopsCreated = []string{}
			report.OpenLoopsClosed = []string{}
		}
	}
	if report.Revision == 1 && len(state.Sessions) != 0 {
		for _, existing := range state.Sessions {
			if existing.NextSessionID == "" {
				report.PreviousSessionID = existing.SessionID
				break
			}
		}
	}
	draft := proposal.Proposal{
		SchemaVersion: 1, ProjectID: packet.ProjectID, SessionID: packet.SessionID,
		FromCursor: packet.FromCursor, ToCursor: packet.ToCursor, EvidencePacketSHA256: digest,
		NewDecisions: []ledger.Decision{}, UpdatedDecisions: []proposal.DecisionPatch{},
		OpenLoops: []proposal.OpenLoopChange{}, TimelineEvents: []ledger.TimelineEvent{},
		CurrentStatePatch: proposal.CurrentStatePatch{
			ExpectedRevision: state.CurrentState.Revision,
			NextAction:       &nextAction, SourceSessions: &sourceSessions, Evidence: &refs,
		},
		SessionReport: report,
		EvidenceLinks: []proposal.EvidenceLink{
			{EntityID: "current-state", EvidenceID: item.ID, Relation: "supports"},
			{EntityID: report.ID, EvidenceID: item.ID, Relation: "supports"},
		},
	}
	body, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proposal.Decode(bytes.NewReader(body)); err != nil {
		t.Fatalf("invalid fake draft: %v\n%s", err, body)
	}
	return body
}

func workerRunOptions(fixture workerFixture, prepare PrepareFunc, adapter VerifiedAgentAdapter, applyFn ApplyFunc, syncFn SyncFunc, pricing PricingResolver) RunOptions {
	return RunOptions{
		Store: fixture.store, JobID: fixture.job.ID, OwnerID: "worker-owner-1",
		LeaseTimeout: 0, ProjectRoot: fixture.project, VaultRoot: fixture.vault,
		DataDir: fixture.data, GOOS: "test", AgentTimeout: time.Minute,
		Now:     func() time.Time { return fixture.now },
		Prepare: prepare, Agent: adapter, Apply: applyFn, Sync: syncFn, Pricing: pricing,
	}
}

// Reordering apply/sync, processing live sessions instead of the frozen order,
// advancing a session before sync, or retaining packet/proposal bytes breaks
// this complete happy-path assertion.
func TestWorkerHappyPathPersistsExactPacketOrderAndCleansPrivateBytes(t *testing.T) {
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	hashC := strings.Repeat("c", 64)
	sessions := []FrozenSession{
		{SessionID: "session-s1", StartedAt: time.Date(2026, 8, 29, 7, 0, 0, 0, time.UTC), Upper: evidence.CursorBoundary{Line: 2, SourceHash: hashB}},
		{SessionID: "session-s2", StartedAt: time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), Upper: evidence.CursorBoundary{Line: 1, SourceHash: hashC}},
	}
	fixture := newWorkerFixture(t, sessions)
	packets := []evidence.Packet{
		workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hashA, true, "ev-000000000001"),
		workerPacket(fixture.job.ProjectID, "session-s1", 2, 2, hashA, hashB, false, "ev-000000000002"),
		workerPacket(fixture.job.ProjectID, "session-s2", 1, 1, "", hashC, false, "ev-000000000003"),
	}
	for index := range packets {
		packets[index].CWD = "/repo"
	}
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	var sequence []string
	packetIndex := 0
	var current evidence.Packet
	var currentRequest PrepareRequest
	loadPhase := func(stage string) Job {
		t.Helper()
		job, _, found, err := fixture.store.Load(fixture.job.ID)
		if err != nil || !found {
			t.Fatalf("%s Load() found=%v err=%v", stage, found, err)
		}
		return job
	}
	prepare := func(_ context.Context, request PrepareRequest) (Prepared, error) {
		job := loadPhase("prepare")
		if job.State != Running || job.Phase != Preparing || job.AcceptedPackets != packetIndex ||
			job.AcceptedSessions != request.SessionIndex || job.SessionIndex != request.SessionIndex ||
			job.CurrentPacket != request.AcceptedCursor {
			t.Fatalf("prepare durable boundary = %#v, request = %#v", job, request)
		}
		packet := packets[packetIndex]
		packetIndex++
		packetNumber := 1
		if request.SessionID == "session-s1" && packet.FromCursor == 2 {
			packetNumber = 2
		}
		sequence = append(sequence, "prepare("+request.SessionID+",p"+string(rune('0'+packetNumber))+")")
		if request.ProjectID != fixture.job.ProjectID || request.UpperBoundary != sessions[request.SessionIndex].Upper || request.EvidencePath == "" {
			return Prepared{}, errors.New("worker prepare request lost frozen identity")
		}
		current = packet
		currentRequest = request
		return Prepared{Packet: packet, Accepted: accepted.snapshot(t)}, nil
	}
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(request agent.Request) (agent.Result, error) {
		sequence = append(sequence, "propose")
		job := loadPhase("review")
		if job.State != Running || job.Phase != Reviewing || job.PacketDigest == "" ||
			job.AcceptedPackets != packetIndex-1 || job.AcceptedSessions != currentRequest.SessionIndex ||
			job.SessionIndex != currentRequest.SessionIndex || job.CurrentPacket != current.ExpectedCursor {
			t.Fatalf("review durable boundary = %#v", job)
		}
		if bytes.Contains(request.Prompt, []byte(fixture.project)) || bytes.Contains(request.Prompt, []byte(fixture.vault)) {
			return agent.Result{}, errors.New("prompt leaked a forbidden root")
		}
		wantRoots := []agent.ForbiddenRoot{
			{Kind: agent.ForbiddenRootProject, CanonicalPath: fixture.project},
			{Kind: agent.ForbiddenRootVault, CanonicalPath: fixture.vault},
		}
		if !reflect.DeepEqual(request.ForbiddenRoots, wantRoots) || request.WorkingDirectory == fixture.project || request.WorkingDirectory == fixture.vault {
			return agent.Result{}, errors.New("agent request lost physical root isolation")
		}
		return agent.Result{Proposal: workerDraft(t, current, accepted.legacy)}, nil
	}
	applyFn := func(_ context.Context, request ApplyRequest) (apply.Result, error) {
		sequence = append(sequence, "apply")
		job := loadPhase("apply")
		if job.State != Running || job.Phase != Applying || job.ResultDigest == "" ||
			job.AcceptedPackets != packetIndex-1 || job.AcceptedSessions != currentRequest.SessionIndex ||
			job.SessionIndex != currentRequest.SessionIndex || job.CurrentPacket != current.ExpectedCursor {
			t.Fatalf("apply durable boundary = %#v", job)
		}
		if request.Proposal.SessionReport.Accounting != nil {
			return apply.Result{}, errors.New("unexpected host accounting")
		}
		accepted.apply(t, request.Changes)
		return apply.Result{
			ProjectID: request.Packet.ProjectID, SessionID: request.Packet.SessionID,
			FromCursor: request.Packet.FromCursor, ToCursor: request.Packet.ToCursor, CursorAdvanced: true,
		}, nil
	}
	syncFn := func(_ context.Context, options syncproject.Options) (syncengine.Report, error) {
		sequence = append(sequence, "sync")
		job := loadPhase("sync")
		if job.State != Running || job.Phase != Syncing || job.AcceptedPackets != packetIndex ||
			job.AcceptedSessions != currentRequest.SessionIndex || job.SessionIndex != currentRequest.SessionIndex ||
			job.CurrentPacket != current.NextCursor {
			t.Fatalf("sync durable boundary = %#v", job)
		}
		if options.ProjectID != fixture.job.ProjectID || options.CWD != fixture.project || options.DataDir != fixture.data || options.Trigger != syncengine.TriggerCLI {
			return syncengine.Report{}, errors.New("worker sync request lost project identity")
		}
		return workerSyncReport(fixture.job.ProjectID), nil
	}

	if err := Run(t.Context(), workerRunOptions(fixture, prepare, adapter, applyFn, syncFn, nil)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"prepare(session-s1,p1)", "propose", "apply", "sync",
		"prepare(session-s1,p2)", "propose", "apply", "sync",
		"prepare(session-s2,p1)", "propose", "apply", "sync",
	}
	if !reflect.DeepEqual(sequence, want) {
		t.Fatalf("sequence = %v, want %v", sequence, want)
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found {
		t.Fatalf("Load() found=%v err=%v", found, err)
	}
	if job.State != Completed || job.SessionIndex != 2 || job.AcceptedPackets != 3 || job.AcceptedSessions != 2 || job.Owner.ID != "" || job.CurrentPacket != (evidence.CursorBoundary{}) {
		t.Fatalf("completed job = %#v", job)
	}
	inputs := filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs")
	entries, err := os.ReadDir(inputs)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private inputs after accepted sync = %v, %v", entries, err)
	}
	jobBody, err := os.ReadFile(filepath.Join(fixture.data, "review-jobs", "jobs", fixture.job.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{fixture.project, fixture.vault, "bounded event", "prompt", "proposal_path", "evidence_path"} {
		if bytes.Contains(jobBody, []byte(forbidden)) {
			t.Fatalf("job persisted private material %q: %s", forbidden, jobBody)
		}
	}
	leases, err := fixture.store.AcquireLeases(fixture.job.ProjectID, "job-after", 0)
	if err != nil {
		t.Fatalf("worker did not release full-lifetime leases: %v", err)
	}
	if err := leases.Release(); err != nil {
		t.Fatal(err)
	}
}

// Calling capability verification or generation when the frozen queue is empty
// would violate the deterministic sync-only fast path.
func TestWorkerNoPendingSyncsExactlyOnceWithoutAgent(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	adapter := &verifiedWorkerAgent{
		capability: workerCapability("fixture", "1.0.0"),
		generate: func(agent.Request) (agent.Result, error) {
			panic("GenerateProposal must not be called for an empty frozen queue")
		},
	}
	syncCalls := 0
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) { panic("Prepare must not be called") },
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { panic("Apply must not be called") },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			return workerSyncReport(fixture.job.ProjectID), nil
		},
		nil,
	)
	if err := Run(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if syncCalls != 1 || adapter.capabilityCalls != 0 || adapter.generateCalls != 0 {
		t.Fatalf("sync=%d capability=%d generate=%d", syncCalls, adapter.capabilityCalls, adapter.generateCalls)
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Completed || job.AcceptedPackets != 0 || job.SessionIndex != 0 {
		t.Fatalf("no-pending job = %#v found=%v err=%v", job, found, err)
	}
}

// The forbidden Vault root must be the physically configured mapping target,
// not merely any disjoint directory supplied by a caller.
func TestWorkerRejectsVaultRootOutsideConfiguredMapping(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	wrongVault := filepath.Join(fixture.root, "wrong-vault")
	if err := os.Mkdir(wrongVault, 0o700); err != nil {
		t.Fatal(err)
	}
	syncCalls := 0
	options := workerRunOptions(
		fixture, nil, nil, nil,
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			return workerSyncReport(fixture.job.ProjectID), nil
		},
		nil,
	)
	options.VaultRoot = wrongVault
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted a Vault outside the configured mapping")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Failed || job.Error.Code != ApplyRecovery || syncCalls != 0 {
		t.Fatalf("mapping failure job=%#v found=%v err=%v sync=%d", job, found, err, syncCalls)
	}
}

// Accepting a structurally plausible but unverified capability would recreate
// the forbidden Codex 0.147 production bypass from Ruling P5.
func TestWorkerRejectsUnverifiedCapabilityBeforePrepare(t *testing.T) {
	hash := strings.Repeat("d", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	incompleteCapability := workerCapability("fixture", "1.0.0")
	incompleteCapability.ModelProvenance = ""
	adapter := &verifiedWorkerAgent{
		capability: incompleteCapability,
		generate:   func(agent.Request) (agent.Result, error) { panic("invalid capability reached generation") },
	}
	prepareCalls := 0
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) { prepareCalls++; return Prepared{}, nil },
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { panic("invalid capability reached apply") },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("invalid capability reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted a fabricated/incomplete capability")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Failed || job.Error.Code != AgentIncompatible || job.AcceptedPackets != 0 || job.SessionIndex != 0 {
		t.Fatalf("failed capability job = %#v found=%v err=%v", job, found, err)
	}
	if prepareCalls != 0 || adapter.generateCalls != 0 {
		t.Fatalf("invalid capability crossed preflight: prepare=%d generate=%d", prepareCalls, adapter.generateCalls)
	}
}

// The Agent-visible v2 snapshot and the final-validation legacy snapshot must
// describe the same accepted state; otherwise a proposal can be prompted
// against one revision and applied against another.
func TestWorkerRejectsInconsistentPreparedAcceptedState(t *testing.T) {
	hash := strings.Repeat("6", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000006")
	packet.CWD = "/repo"
	sourceAccepted := workerInitialAccepted(fixture.job.ProjectID)
	accepted := sourceAccepted.snapshot(t)
	accepted.Legacy.CurrentState.NextAction = "different accepted state"
	adapter := &verifiedWorkerAgent{
		capability: workerCapability("fixture", "1.0.0"),
		generate: func(agent.Request) (agent.Result, error) {
			return agent.Result{}, errors.New("inconsistent accepted state reached Agent")
		},
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return Prepared{Packet: packet, Accepted: accepted}, nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			panic("inconsistent accepted state reached apply")
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("inconsistent accepted state reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted inconsistent prepared state")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Failed || job.Error.Code != ProposalRejected || job.PacketDigest != "" || adapter.generateCalls != 0 {
		t.Fatalf("inconsistent prepared job=%#v found=%v err=%v generate=%d", job, found, err, adapter.generateCalls)
	}
}

// The injected prepare seam cannot skip ahead from the durable cursor even
// when its packet envelope is otherwise internally consistent.
func TestWorkerRejectsPreparedPacketThatSkipsDurableCursor(t *testing.T) {
	previous := strings.Repeat("1", 64)
	upper := strings.Repeat("2", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 2, SourceHash: upper},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 2, 2, previous, upper, false, "ev-000000000012")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	adapter := &verifiedWorkerAgent{
		capability: workerCapability("fixture", "1.0.0"),
		generate: func(agent.Request) (agent.Result, error) {
			return agent.Result{}, errors.New("skipped cursor reached Agent")
		},
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return Prepared{Packet: packet, Accepted: accepted.snapshot(t)}, nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { panic("skipped cursor reached apply") },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("skipped cursor reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted a packet that skipped the durable cursor")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Failed || job.Error.Code != ProposalRejected ||
		job.CurrentPacket != (evidence.CursorBoundary{}) || adapter.generateCalls != 0 {
		t.Fatalf("skipped-cursor job=%#v found=%v err=%v generate=%d", job, found, err, adapter.generateCalls)
	}
}

// Reviewing a prepared packet may publish its digest, but the packet cursor is
// authoritative only after apply accepts it. Agent failure must not advance it.
func TestWorkerAgentFailureDoesNotAdvanceAuthoritativeCursor(t *testing.T) {
	hash := strings.Repeat("9", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000009")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	adapter := &verifiedWorkerAgent{
		capability: workerCapability("fixture", "1.0.0"),
		generate: func(agent.Request) (agent.Result, error) {
			return agent.Result{}, agent.NewError(agent.CodeAuth, errors.New("private auth detail"))
		},
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return Prepared{Packet: packet, Accepted: accepted.snapshot(t)}, nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { panic("Agent failure reached apply") },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("Agent failure reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted Agent failure")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Failed || job.Error.Code != AgentAuth || job.AcceptedPackets != 0 || job.SessionIndex != 0 || job.CurrentPacket != (evidence.CursorBoundary{}) {
		t.Fatalf("Agent failure job = %#v found=%v err=%v", job, found, err)
	}
	inputs := filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs")
	entries, err := os.ReadDir(inputs)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private inputs after Agent failure = %v, %v", entries, err)
	}
}

// A completed Agent invocation is billable even when its draft is rejected.
// Its exact unknown-model usage must reach private Task 7 accounting first.
func TestWorkerRejectedDraftStillPersistsExactReviewUsage(t *testing.T) {
	hash := strings.Repeat("8", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000008")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	usage := accounting.TokenUsage{InputTokens: 11, CachedInputTokens: 3, OutputTokens: 2, ReasoningOutputTokens: 1, TotalTokens: 13}
	adapter := &verifiedWorkerAgent{
		capability: workerCapability("fixture", "1.0.0"),
		generate: func(agent.Request) (agent.Result, error) {
			return agent.Result{Proposal: []byte(`{"not":"a proposal"}`), Model: "", Usage: usage}, nil
		},
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return Prepared{Packet: packet, Accepted: accepted.snapshot(t)}, nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { panic("rejected draft reached apply") },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("rejected draft reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted malformed Agent draft")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Failed || job.Error.Code != ProposalRejected {
		t.Fatalf("rejected draft job = %#v found=%v err=%v", job, found, err)
	}
	if job.ReviewAccounting.SnapshotAt != fixture.now || len(job.ReviewAccounting.Models) != 1 || job.ReviewAccounting.Models[0].Model != "" || job.ReviewAccounting.Models[0].TokenUsage != usage || job.ReviewAccounting.PricingComplete || job.ReviewAccounting.TotalCostUSD != nil {
		t.Fatalf("rejected-draft accounting = %#v", job.ReviewAccounting)
	}
}

// Once generation hands off a valid proposal, cancellation must not interrupt
// the trusted apply/sync commit window.
func TestWorkerApplyAndSyncIgnoreCancellationAfterProposal(t *testing.T) {
	hash := strings.Repeat("7", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000007")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	ctx, cancel := context.WithCancel(t.Context())
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		body := workerDraft(t, packet, accepted.legacy)
		cancel()
		return agent.Result{Proposal: body}, nil
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return Prepared{Packet: packet, Accepted: accepted.snapshot(t)}, nil
		},
		adapter,
		func(commitCtx context.Context, request ApplyRequest) (apply.Result, error) {
			if err := commitCtx.Err(); err != nil {
				return apply.Result{}, errors.New("apply observed caller cancellation")
			}
			accepted.apply(t, request.Changes)
			return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
		},
		func(commitCtx context.Context, _ syncproject.Options) (syncengine.Report, error) {
			if err := commitCtx.Err(); err != nil {
				return syncengine.Report{}, errors.New("sync observed caller cancellation")
			}
			return workerSyncReport(packet.ProjectID), nil
		},
		nil,
	)
	if err := Run(ctx, options); err != nil {
		t.Fatal(err)
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Completed || job.AcceptedPackets != 1 || job.AcceptedSessions != 1 {
		t.Fatalf("commit-window job = %#v found=%v err=%v", job, found, err)
	}
}

// An accepted apply advances its cursor/count, but an incomplete sync report
// must stop before session advancement and expose sync-only recovery.
func TestWorkerIncompleteSyncDoesNotAdvanceSession(t *testing.T) {
	hash := strings.Repeat("5", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000005")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return Prepared{Packet: packet, Accepted: accepted.snapshot(t)}, nil
		},
		adapter,
		func(_ context.Context, request ApplyRequest) (apply.Result, error) {
			accepted.apply(t, request.Changes)
			return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			return syncengine.Report{ProjectID: packet.ProjectID}, nil
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted an incomplete sync report")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Failed || job.Error.Code != SyncPartial || job.AcceptedPackets != 1 || job.AcceptedSessions != 0 || job.SessionIndex != 0 || job.CurrentPacket != packet.NextCursor || !job.SyncOnlyAvailable {
		t.Fatalf("incomplete-sync job=%#v found=%v err=%v", job, found, err)
	}
}

// A hostile or corrupt private work entry must fail closed without panicking;
// partial work-root setup is a normal error path, not a process crash boundary.
func TestWorkerPrivateWorkSetupFailureIsDurableAndPanicFree(t *testing.T) {
	hash := strings.Repeat("f", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	inputs := filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs")
	if err := os.WriteFile(inputs, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &verifiedWorkerAgent{
		capability: workerCapability("fixture", "1.0.0"),
		generate:   func(agent.Request) (agent.Result, error) { panic("invalid private work reached generation") },
	}
	prepareCalls := 0
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) { prepareCalls++; return Prepared{}, nil },
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { panic("invalid private work reached apply") },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("invalid private work reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted invalid private work layout")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Failed || job.Error.Code != ApplyRecovery || job.AcceptedPackets != 0 || job.SessionIndex != 0 {
		t.Fatalf("private-work failure job = %#v found=%v err=%v", job, found, err)
	}
	if prepareCalls != 0 || adapter.generateCalls != 0 {
		t.Fatalf("invalid private work crossed setup: prepare=%d generate=%d", prepareCalls, adapter.generateCalls)
	}
}

func fixtureTime(hour int) time.Time {
	return time.Date(2026, 8, 29, hour, 0, 0, 0, time.UTC)
}

type workerPrices map[string]accounting.Pricing

func (prices workerPrices) Resolve(model string, _ time.Time) (accounting.Pricing, bool) {
	price, found := prices[model]
	return price, found
}

// Guessing an Agent model, dropping exact usage, letting the Agent author source
// accounting, or charging an unknown model makes this P3/P6 test fail.
func TestWorkerHostEnrichesSourceAccountingAndKeepsUnknownReviewModelUnpriced(t *testing.T) {
	hash := strings.Repeat("e", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000004")
	packet.CWD = "/repo"
	packet.SessionUsage = &accounting.SessionUsage{
		StartedAt: "2026-08-29T07:00:00Z", EndedAt: "2026-08-29T07:00:01Z", DurationMS: 1000,
		Models:      []accounting.ModelUsage{{Model: "source-model", TokenUsage: accounting.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}},
		TotalTokens: 12,
	}
	price := accounting.Pricing{
		Currency: "USD", InputPerMillion: 1, OutputPerMillion: 2,
		Source: "https://example.com/official-pricing", AsOf: "2026-08-29",
	}
	prices := workerPrices{"source-model": price}
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{
			Proposal: workerDraft(t, packet, accepted.legacy),
			Model:    "",
			Usage: accounting.TokenUsage{
				InputTokens: 7, CachedInputTokens: 3, OutputTokens: 2,
				ReasoningOutputTokens: 1, TotalTokens: 9,
			},
		}, nil
	}
	applyCalls := 0
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return Prepared{Packet: packet, Accepted: accepted.snapshot(t)}, nil
		},
		adapter,
		func(_ context.Context, request ApplyRequest) (apply.Result, error) {
			applyCalls++
			report := request.Proposal.SessionReport.Accounting
			if report == nil || len(report.Models) != 1 || report.Models[0].Model != "source-model" || report.Models[0].Pricing != price {
				return apply.Result{}, errors.New("trusted source accounting was not injected")
			}
			if len(accepted.legacy.Sessions) != 0 {
				return apply.Result{}, errors.New("review accounting mutated accepted source state before apply")
			}
			accepted.apply(t, request.Changes)
			return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			return workerSyncReport(packet.ProjectID), nil
		},
		prices,
	)
	if err := Run(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if applyCalls != 1 {
		t.Fatalf("apply calls = %d", applyCalls)
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found {
		t.Fatalf("Load() found=%v err=%v", found, err)
	}
	if job.ReviewAccounting.SnapshotAt != fixture.now || len(job.ReviewAccounting.Models) != 1 || job.ReviewAccounting.Models[0].Model != "" || job.ReviewAccounting.Models[0].TokenUsage != (accounting.TokenUsage{InputTokens: 7, CachedInputTokens: 3, OutputTokens: 2, ReasoningOutputTokens: 1, TotalTokens: 9}) || job.ReviewAccounting.PricingComplete || job.ReviewAccounting.TotalCostUSD != nil {
		t.Fatalf("unknown-model review accounting = %#v", job.ReviewAccounting)
	}
	projected, err := reviewv2.ProjectLegacy(accepted.legacy)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Machine.Accounting.TotalTokens != packet.SessionUsage.TotalTokens || len(projected.Machine.Sessions) != 1 || projected.Machine.Sessions[0].Accounting == nil {
		t.Fatalf("source machine accounting = %#v", projected.Machine)
	}
}
