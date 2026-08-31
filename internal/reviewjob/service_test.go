package reviewjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	"github.com/neomei/SessionReviewer/internal/prepare"
	"github.com/neomei/SessionReviewer/internal/proposal"
	"github.com/neomei/SessionReviewer/internal/reviewprompt"
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

func workerPrepared(t *testing.T, packet evidence.Packet, accepted reviewv2.Accepted) Prepared {
	t.Helper()
	canonical, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	return Prepared{Packet: packet, PacketBytes: canonical, Accepted: accepted}
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
	cancelCalls     int
	generate        func(agent.Request) (agent.Result, error)
	cancel          func()
}

func (adapter *verifiedWorkerAgent) VerifiedCapability() agent.Capability {
	adapter.capabilityCalls++
	return adapter.capability
}

func (adapter *verifiedWorkerAgent) Verify(context.Context, string) (agent.Capability, error) {
	panic("worker must not invoke verification")
}

func (adapter *verifiedWorkerAgent) GenerateProposal(_ context.Context, request agent.Request) (agent.Result, error) {
	adapter.generateCalls++
	return adapter.generate(request)
}

func (adapter *verifiedWorkerAgent) Cancel(context.Context) error {
	adapter.cancelCalls++
	if adapter.cancel != nil {
		adapter.cancel()
	}
	return nil
}

type workerFixture struct {
	root, project, vault, data, agent string
	store                             Store
	job                               Job
	now                               time.Time
}

func newWorkerFixture(t *testing.T, sessions []FrozenSession) workerFixture {
	t.Helper()
	root := t.TempDir()
	fixture := workerFixture{
		root:    root,
		vault:   filepath.Join(root, "vault"),
		project: filepath.Join(root, "vault", "project"),
		data:    filepath.Join(root, "data"),
		agent:   filepath.Join(root, "fixture-agent"),
		now:     time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC),
	}
	for _, path := range []string{fixture.project, fixture.vault, fixture.data} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(fixture.agent, []byte("provider-neutral fixture Agent\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	agentPath, _, agentIdentity, _, err := inspectAgentExecutable(fixture.agent)
	if err != nil {
		t.Fatal(err)
	}
	fixture.agent = agentPath
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
			Identity:   agentIdentity,
			Version:    "1.0.0",
			Executable: fixture.agent,
		},
		State:          Queued,
		Phase:          Preflight,
		Attempt:        1,
		FrozenSessions: append([]FrozenSession(nil), sessions...),
		CreatedAt:      fixture.now,
		UpdatedAt:      fixture.now,
	}
	if err := os.MkdirAll(filepath.Join(fixture.data, "projects", fixture.job.ProjectID), 0o700); err != nil {
		t.Fatal(err)
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

func seedWorkerReviewV2AndSyncData(t *testing.T, fixture workerFixture, accepted workerAccepted) {
	t.Helper()
	projected, err := reviewv2.ProjectLegacy(accepted.legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(fixture.project, projected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(fixture.data, "projects", fixture.job.ProjectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func seedWorkerLegacyReviewAndSyncData(t *testing.T, fixture workerFixture) {
	t.Helper()
	directory := filepath.Join(fixture.project, "docs", "session-review")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	overview := "---\nid: project-overview\nentity_type: project_overview\nproject_id: " + fixture.job.ProjectID +
		"\nrevision: 1\nsync_status: synced\ncreated_at: 2026-08-24T00:00:00Z\nnote: base\n---\n\n# Worker legacy review\n"
	if err := os.WriteFile(filepath.Join(directory, "project-overview.md"), []byte(overview), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy, err := ledger.Load(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	current := ledger.CurrentState{
		ProjectID: legacy.ProjectID, Revision: 1, Goal: "migrate worker fixture", LastVerified: "legacy accepted",
		Branch: "codex/legacy", Blockers: []string{}, OpenRisks: []string{}, NextAction: "sync",
		FirstInspection: "docs/session-review/project-overview.md", LastUpdated: "2026-08-29T08:00:00Z",
		SourceSessions: []string{}, Evidence: []ledger.EvidenceRef{},
	}
	plan, err := ledger.Render(legacy, ledger.ChangeSet{Current: &current})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(fixture.data, "projects", fixture.job.ProjectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
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

func TestRestoreEvidenceSummariesUsesAuthenticatedPacketTuples(t *testing.T) {
	hash := strings.Repeat("a", 64)
	packet := workerPacket("project-1111111111111111", "session-s1", 1, 1, "", hash, false, "ev-111111111111")
	packet.Events[0].Summary = `run in "/Users/Neo/AgentWiki /agentwiki"`
	redacted := ledger.EvidenceRef{
		EvidenceID: packet.Events[0].ID, SessionID: packet.SessionID, JSONLLine: 1,
		SourceHash: hash, Summary: `[REDACTED:HOST_PATH]`,
	}
	draft := proposal.Proposal{
		NewDecisions:     []ledger.Decision{{Evidence: []ledger.EvidenceRef{redacted}}},
		UpdatedDecisions: []proposal.DecisionPatch{{Evidence: &[]ledger.EvidenceRef{redacted}}},
		OpenLoops: []proposal.OpenLoopChange{
			{Entity: &ledger.OpenLoop{Evidence: []ledger.EvidenceRef{redacted}}},
			{Patch: &proposal.OpenLoopPatch{Evidence: &[]ledger.EvidenceRef{redacted}}},
		},
		TimelineEvents:    []ledger.TimelineEvent{{Evidence: []ledger.EvidenceRef{redacted}}},
		CurrentStatePatch: proposal.CurrentStatePatch{Evidence: &[]ledger.EvidenceRef{redacted}},
		SessionReport: ledger.SessionReport{
			Evidence: []ledger.EvidenceRef{redacted},
			Phases:   []ledger.SessionPhase{{Evidence: []ledger.EvidenceRef{redacted}}},
		},
	}

	if err := restoreEvidenceSummaries(&draft, packet); err != nil {
		t.Fatal(err)
	}
	want := packet.Events[0].Summary
	refs := []*ledger.EvidenceRef{
		&draft.NewDecisions[0].Evidence[0],
		&(*draft.UpdatedDecisions[0].Evidence)[0],
		&draft.OpenLoops[0].Entity.Evidence[0],
		&(*draft.OpenLoops[1].Patch.Evidence)[0],
		&draft.TimelineEvents[0].Evidence[0],
		&(*draft.CurrentStatePatch.Evidence)[0],
		&draft.SessionReport.Evidence[0],
		&draft.SessionReport.Phases[0].Evidence[0],
	}
	for index, ref := range refs {
		if ref.Summary != want {
			t.Fatalf("reference %d summary=%q want authenticated %q", index, ref.Summary, want)
		}
	}

	draft.NewDecisions[0].Evidence[0] = redacted
	draft.NewDecisions[0].Evidence[0].JSONLLine = 2
	if err := restoreEvidenceSummaries(&draft, packet); err == nil {
		t.Fatal("restored a summary for a mismatched evidence tuple")
	}
}

func TestClassifyWorkerInputFailuresSeparately(t *testing.T) {
	segmentErr := errors.Join(errors.New("private candidate path"), prepare.ErrSessionSegmentConflict)
	if got := classifyPrepareFailure(segmentErr); got != SessionSegmentConflict {
		t.Fatalf("prepare code=%s want %s", got, SessionSegmentConflict)
	}
	if got := classifyPromptFailure(reviewprompt.ErrUnsafeInput); got != ProposalUnsafeInput {
		t.Fatalf("prompt code=%s want %s", got, ProposalUnsafeInput)
	}
	if got := classifyPromptFailure(errors.New("malformed prompt input")); got != ProposalRejected {
		t.Fatalf("generic prompt code=%s want %s", got, ProposalRejected)
	}
}

func TestDistinctAliasesPreservesTrailingSpacePathIdentity(t *testing.T) {
	root := "/Users/Neo/AgentWiki "
	if aliases := distinctAliases(root, root); len(aliases) != 0 {
		t.Fatalf("same trailing-space root became aliases=%q", aliases)
	}
	aliases := distinctAliases("/private/physical", root)
	if len(aliases) != 1 || aliases[0] != root {
		t.Fatalf("aliases=%q want exact trailing-space spelling", aliases)
	}
}

func TestBoundedPrivateErrorRetainsWrappedAgentCause(t *testing.T) {
	err := agent.NewError(agent.CodeIncompatible, errors.New("private adapter diagnostic"))
	got := boundedPrivateError(err)
	if !strings.Contains(got, "E_AGENT_INCOMPATIBLE") || !strings.Contains(got, "private adapter diagnostic") {
		t.Fatalf("private error lost wrapped cause: %q", got)
	}
}

func workerRunOptions(fixture workerFixture, prepare PrepareFunc, adapter *verifiedWorkerAgent, applyFn ApplyFunc, syncFn SyncFunc, pricing PricingResolver) RunOptions {
	var handle *AgentHandle
	if adapter != nil {
		physical, info, identity, digest, err := inspectAgentExecutable(fixture.agent)
		if err != nil {
			panic(err)
		}
		handle = &AgentHandle{
			adapter: adapter, capability: adapter.capability, executable: physical,
			executableInfo: info, executableIdentity: identity, executableDigest: digest,
		}
	}
	return RunOptions{
		Store: fixture.store, JobID: fixture.job.ID, OwnerID: "worker-owner-1",
		LeaseTimeout: 0, ProjectRoot: fixture.project, VaultRoot: fixture.vault,
		DataDir: fixture.data, GOOS: runtime.GOOS, AgentTimeout: time.Minute,
		Now:     func() time.Time { return fixture.now },
		Prepare: prepare, Agent: handle, Apply: applyFn, Sync: syncFn, Pricing: pricing,
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
		if request.ProjectID != fixture.job.ProjectID || request.UpperBoundary != sessions[request.SessionIndex].Upper ||
			request.ProjectIdentity != fixture.job.ProjectIdentity || !request.DataIdentity.Valid() {
			return Prepared{}, errors.New("worker prepare request lost frozen identity")
		}
		current = packet
		currentRequest = request
		return workerPrepared(t, packet, accepted.snapshot(t)), nil
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
		if request.ProjectIdentity != fixture.job.ProjectIdentity || !request.DataIdentity.Valid() {
			return apply.Result{}, errors.New("apply request lost pinned root identities")
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
		if options.ProjectID != fixture.job.ProjectID || options.CWD != fixture.project || options.DataDir != fixture.data || options.Trigger != syncengine.TriggerCLI || options.Pin == nil {
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

// Even an empty frozen snapshot gets one deterministic reconciliation sync.
// The durable Syncing phase is the point after which cancellation cannot
// interrupt that externally visible commit.
func TestWorkerEmptyQueueWithoutAcceptedApplyPerformsOneSyncWithoutAgent(t *testing.T) {
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
		func(syncCtx context.Context, _ syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			job, _, found, err := fixture.store.Load(fixture.job.ID)
			if err != nil || !found || job.State != Running || job.Phase != Syncing || syncCtx.Err() != nil {
				t.Fatalf("sync durable boundary job=%#v found=%v err=%v ctx=%v", job, found, err, syncCtx.Err())
			}
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

func TestWorkerEmptyQueueCancellationAfterDurableSyncingStillFinishesSync(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	syncCalls := 0
	options := workerRunOptions(fixture, nil, nil, nil, func(syncCtx context.Context, _ syncproject.Options) (syncengine.Report, error) {
		syncCalls++
		job, _, found, err := fixture.store.Load(fixture.job.ID)
		if err != nil || !found || job.Phase != Syncing {
			t.Fatalf("sync phase job=%#v found=%v err=%v", job, found, err)
		}
		cancel()
		if syncCtx.Err() != nil {
			t.Fatalf("durable sync received cancellable context: %v", syncCtx.Err())
		}
		return workerSyncReport(fixture.job.ProjectID), nil
	}, nil)
	if err := Run(ctx, options); err == nil {
		t.Fatal("Run() completed instead of terminalizing cancellation after durable Syncing")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Cancelled || job.Error.Code != AgentCancelled || syncCalls != 1 {
		t.Fatalf("cancelled job=%#v found=%v err=%v sync=%d", job, found, err, syncCalls)
	}
}

func TestWorkerEmptyQueueRunsRealReconciliationSync(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	seedWorkerReviewV2AndSyncData(t, fixture, workerInitialAccepted(fixture.job.ProjectID))
	options := workerRunOptions(fixture, nil, nil, nil, syncproject.Run, nil)
	options.GOOS = "darwin"
	if err := Run(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.vault, "Projects", "Worker--11111111", "Session Review", "项目回顾.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("empty worker did not perform real sync: %v", err)
	}
}

func TestWorkerEmptyQueueHonorsCancellationWithoutSync(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	syncCalls := 0
	options := workerRunOptions(fixture, nil, nil, nil, func(context.Context, syncproject.Options) (syncengine.Report, error) {
		syncCalls++
		return workerSyncReport(fixture.job.ProjectID), nil
	}, nil)
	if err := Run(ctx, options); err == nil {
		t.Fatal("Run() ignored cancellation with no mandatory commit")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Cancelled || job.Error.Code != AgentCancelled || syncCalls != 0 {
		t.Fatalf("cancelled empty job=%#v found=%v err=%v sync=%d", job, found, err, syncCalls)
	}
}

func TestWorkerEmptyQueueFinishesDurableAcceptedApplyDespiteCancellation(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	job, _, err := fixture.store.Update(fixture.job.ID, 1, func(job *Job) error {
		job.AcceptedPackets = 1
		job.AcceptedSyncPending = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.job = job
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	syncCalls := 0
	options := workerRunOptions(fixture, nil, nil, nil, func(commitCtx context.Context, _ syncproject.Options) (syncengine.Report, error) {
		syncCalls++
		if commitCtx.Err() != nil {
			t.Fatalf("mandatory sync received cancellable context: %v", commitCtx.Err())
		}
		return workerSyncReport(fixture.job.ProjectID), nil
	}, nil)
	if err := Run(ctx, options); err == nil {
		t.Fatal("Run() completed instead of terminalizing cancellation after mandatory sync")
	}
	completed, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || completed.State != Cancelled || completed.Error.Code != AgentCancelled || completed.AcceptedSyncPending || syncCalls != 1 {
		t.Fatalf("mandatory-sync job=%#v found=%v err=%v sync=%d", completed, found, err, syncCalls)
	}
}

func TestWorkerRunsRealSyncForNestedProjectAfterDurableAcceptedApply(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	seedWorkerReviewV2AndSyncData(t, fixture, workerInitialAccepted(fixture.job.ProjectID))
	job, _, err := fixture.store.Update(fixture.job.ID, 1, func(job *Job) error {
		job.AcceptedPackets = 1
		job.AcceptedSyncPending = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.job = job
	options := workerRunOptions(fixture, nil, nil, nil, syncproject.Run, nil)
	options.GOOS = "darwin"
	if err := Run(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	completed, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || completed.State != Completed || completed.AcceptedSyncPending {
		t.Fatalf("real-sync job=%#v found=%v err=%v", completed, found, err)
	}
	path := filepath.Join(fixture.vault, "Projects", "Worker--11111111", "Session Review", "项目回顾.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worker real sync did not publish nested Project: %v", err)
	}
}

func TestWorkerRejectsReviewTargetContainingProjectBeforePrepareApplyOrSync(t *testing.T) {
	hash := strings.Repeat("a", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	reviewPath := "Projects/Worker--11111111/Session Review"
	target := filepath.Join(fixture.vault, filepath.FromSlash(reviewPath))
	projectRoot := filepath.Join(target, "project")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.project, projectRoot); err != nil {
		t.Fatal(err)
	}
	fixture.project = projectRoot
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: fixture.job.ProjectID, Root: fixture.project, VaultRoot: fixture.vault,
			VaultReviewPath: reviewPath, VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	prepareCalls, applyCalls, syncCalls := 0, 0, 0
	adapter := &verifiedWorkerAgent{
		capability: workerCapability("fixture", "1.0.0"),
		generate: func(agent.Request) (agent.Result, error) {
			t.Fatal("unsafe mapping reached Agent generation")
			return agent.Result{}, nil
		},
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			prepareCalls++
			return Prepared{}, errors.New("unsafe mapping reached Prepare")
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			applyCalls++
			return apply.Result{}, errors.New("unsafe mapping reached Apply")
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			return syncengine.Report{}, errors.New("unsafe mapping reached Sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted a review target containing the authoritative Project")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Failed || job.Error.Code != ApplyRecovery {
		t.Fatalf("unsafe mapping job=%#v found=%v err=%v", job, found, err)
	}
	if prepareCalls != 0 || adapter.generateCalls != 0 || applyCalls != 0 || syncCalls != 0 {
		t.Fatalf("unsafe mapping crossed preflight: prepare=%d generate=%d apply=%d sync=%d", prepareCalls, adapter.generateCalls, applyCalls, syncCalls)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 1 || entries[0].Name() != "project" {
		t.Fatalf("unsafe target was mutated: entries=%v err=%v", entries, err)
	}
}

func TestWorkerAcceptsRealCompletedLegacyMigrationAudit(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	seedWorkerLegacyReviewAndSyncData(t, fixture)
	job, _, err := fixture.store.Update(fixture.job.ID, 1, func(job *Job) error {
		job.AcceptedPackets = 1
		job.AcceptedSyncPending = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.job = job
	var migration syncengine.MigrationReport
	syncReal := func(ctx context.Context, options syncproject.Options) (syncengine.Report, error) {
		report, err := syncproject.Run(ctx, options)
		migration = report.Migration
		return report, err
	}
	options := workerRunOptions(fixture, nil, nil, nil, syncReal, nil)
	options.GOOS = "darwin"
	if err := Run(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	completed, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || completed.State != Completed || completed.AcceptedSyncPending {
		t.Fatalf("legacy migration job=%#v found=%v err=%v", completed, found, err)
	}
	if migration.Required || migration.DryRun || len(migration.Creates) == 0 || len(migration.Archives) == 0 {
		t.Fatalf("real migration audit was not complete: %#v", migration)
	}
	for _, name := range []string{"项目回顾.md", "项目历史.md", filepath.Join(".session-reviewer", "ledger.json")} {
		if _, err := os.Stat(filepath.Join(fixture.project, "docs", "session-review", name)); err != nil {
			t.Fatalf("legacy migration did not publish %s: %v", name, err)
		}
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
// a provider-specific production verification bypass.
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
			return workerPrepared(t, packet, accepted), nil
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
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
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
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
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
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
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

// Generate returning successfully is not the commit handoff: cancellation
// observed after Generate must stop before the durable Applying transition.
func TestWorkerCancellationAfterGenerateStopsBeforeApply(t *testing.T) {
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
		return agent.Result{
			Proposal: body,
			Usage:    accounting.TokenUsage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		}, nil
	}
	applyCalls, syncCalls := 0, 0
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			applyCalls++
			return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			return workerSyncReport(packet.ProjectID), nil
		},
		nil,
	)
	if err := Run(ctx, options); err == nil {
		t.Fatal("Run() ignored cancellation after Generate")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Cancelled || job.Error.Code != AgentCancelled ||
		job.AcceptedPackets != 0 || job.AcceptedSessions != 0 || hasReviewAccounting(job.ReviewAccounting) ||
		applyCalls != 0 || syncCalls != 0 || adapter.cancelCalls != 1 {
		t.Fatalf("cancelled job = %#v found=%v err=%v apply=%d sync=%d cancel=%d", job, found, err, applyCalls, syncCalls, adapter.cancelCalls)
	}
}

func TestCancelRequestDuringReviewCallsNativeAgentCancelAndDiscardsPayload(t *testing.T) {
	hash := strings.Repeat("3", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000033")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	entered, released := make(chan struct{}), make(chan struct{})
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.cancel = func() { close(released) }
	adapter.generate = func(agent.Request) (agent.Result, error) {
		close(entered)
		<-released
		return agent.Result{}, agent.NewError(agent.CodeCancelled, errors.New("provider acknowledged cancellation"))
	}
	applyCalls, syncCalls := 0, 0
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			applyCalls++
			return apply.Result{}, errors.New("cancelled review reached apply")
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			return workerSyncReport(packet.ProjectID), nil
		},
		nil,
	)
	done := make(chan error, 1)
	go func() { done <- Run(t.Context(), options) }()
	<-entered
	requested, _, err := RequestCancel(fixture.store, fixture.job.ID, fixture.now.Add(time.Minute))
	if err != nil || requested.State != CancelRequested || requested.Phase != Reviewing {
		t.Fatalf("RequestCancel()=%#v err=%v", requested, err)
	}
	if err := <-done; err == nil {
		t.Fatal("worker ignored persisted cancellation during review")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Cancelled || job.Error.Code != AgentCancelled ||
		job.AcceptedPackets != 0 || applyCalls != 0 || syncCalls != 0 || adapter.cancelCalls != 1 {
		t.Fatalf("cancelled review job=%#v found=%v err=%v apply=%d sync=%d cancel=%d", job, found, err, applyCalls, syncCalls, adapter.cancelCalls)
	}
	entries, err := os.ReadDir(filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled review retained private input bytes: entries=%v err=%v", entries, err)
	}
}

type cancellingWorkerPrices struct {
	cancel context.CancelFunc
	price  accounting.Pricing
}

func (prices cancellingWorkerPrices) Resolve(string, time.Time) (accounting.Pricing, bool) {
	prices.cancel()
	return prices.price, true
}

// Cancellation may arrive while the host enriches and validates a draft. The
// second handoff check must still stop before Applying becomes durable.
func TestWorkerCancellationAfterFinalValidationStopsBeforeApply(t *testing.T) {
	hash := strings.Repeat("4", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000014")
	packet.CWD = "/repo"
	packet.SessionUsage = &accounting.SessionUsage{
		StartedAt: "2026-08-29T07:00:00Z", EndedAt: "2026-08-29T07:00:01Z", DurationMS: 1000,
		Models: []accounting.ModelUsage{{Model: "source-model", TokenUsage: accounting.TokenUsage{
			InputTokens: 1, TotalTokens: 1,
		}}},
		TotalTokens: 1,
	}
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	ctx, cancel := context.WithCancel(t.Context())
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
	}
	applyCalls, syncCalls := 0, 0
	price := accounting.Pricing{
		Currency: "USD", InputPerMillion: 1, Source: "https://example.com/pricing", AsOf: "2026-08-29",
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			applyCalls++
			return apply.Result{}, errors.New("cancelled draft reached apply")
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			return workerSyncReport(packet.ProjectID), nil
		},
		cancellingWorkerPrices{cancel: cancel, price: price},
	)
	if err := Run(ctx, options); err == nil {
		t.Fatal("Run() ignored cancellation after final validation")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Cancelled || job.Error.Code != AgentCancelled ||
		job.AcceptedPackets != 0 || applyCalls != 0 || syncCalls != 0 || adapter.cancelCalls != 1 {
		t.Fatalf("cancelled validation job=%#v found=%v err=%v apply=%d sync=%d cancel=%d", job, found, err, applyCalls, syncCalls, adapter.cancelCalls)
	}
}

func TestWorkerCancellationAfterDurableApplyingStillFinishesApplyAndMandatorySync(t *testing.T) {
	hash := strings.Repeat("c", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000021")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	ctx, cancel := context.WithCancel(t.Context())
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
	}
	applyCalls, syncCalls := 0, 0
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(commitCtx context.Context, request ApplyRequest) (apply.Result, error) {
			applyCalls++
			cancel()
			if commitCtx.Err() != nil {
				t.Fatalf("Apply received cancellable commit context: %v", commitCtx.Err())
			}
			accepted.apply(t, request.Changes)
			return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
		},
		func(commitCtx context.Context, _ syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			if commitCtx.Err() != nil {
				t.Fatalf("Sync received cancellable commit context: %v", commitCtx.Err())
			}
			return workerSyncReport(packet.ProjectID), nil
		},
		nil,
	)
	if err := Run(ctx, options); err == nil {
		t.Fatal("Run() completed instead of terminalizing the durable cancellation")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Cancelled || job.Error.Code != AgentCancelled ||
		job.AcceptedPackets != 1 || job.AcceptedSessions != 1 || applyCalls != 1 || syncCalls != 1 {
		t.Fatalf("post-Applying cancellation job=%#v found=%v err=%v apply=%d sync=%d", job, found, err, applyCalls, syncCalls)
	}
}

func TestCancelDuringApplyingPersistsRequestBeforeCommitReturns(t *testing.T) {
	hash := strings.Repeat("d", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000031")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	ctx, cancel := context.WithCancel(t.Context())
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
	}
	entered, release := make(chan struct{}), make(chan struct{})
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(commitCtx context.Context, request ApplyRequest) (apply.Result, error) {
			close(entered)
			<-release
			if commitCtx.Err() != nil {
				return apply.Result{}, errors.New("apply commit context was cancelled")
			}
			accepted.apply(t, request.Changes)
			return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
		},
		func(commitCtx context.Context, _ syncproject.Options) (syncengine.Report, error) {
			if commitCtx.Err() != nil {
				return syncengine.Report{}, errors.New("sync commit context was cancelled")
			}
			return workerSyncReport(packet.ProjectID), nil
		},
		nil,
	)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options) }()
	<-entered
	cancel()
	waitForWorkerState(t, fixture.store, fixture.job.ID, CancelRequested, Applying)
	close(release)
	if err := <-done; err == nil {
		t.Fatal("worker ignored durable cancellation during apply")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Cancelled || job.Error.Code != AgentCancelled ||
		job.AcceptedPackets != 1 || job.AcceptedSessions != 1 || job.AcceptedSyncPending {
		t.Fatalf("cancelled apply job=%#v found=%v err=%v", job, found, err)
	}
}

func TestCancelDuringSyncPersistsRequestAndFinishesTypedSync(t *testing.T) {
	hash := strings.Repeat("e", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000032")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	ctx, cancel := context.WithCancel(t.Context())
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
	}
	entered, release := make(chan struct{}), make(chan struct{})
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(_ context.Context, request ApplyRequest) (apply.Result, error) {
			accepted.apply(t, request.Changes)
			return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
		},
		func(commitCtx context.Context, _ syncproject.Options) (syncengine.Report, error) {
			close(entered)
			<-release
			if commitCtx.Err() != nil {
				return syncengine.Report{}, errors.New("sync commit context was cancelled")
			}
			return workerSyncReport(packet.ProjectID), nil
		},
		nil,
	)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options) }()
	<-entered
	cancel()
	waitForWorkerState(t, fixture.store, fixture.job.ID, CancelRequested, Syncing)
	close(release)
	if err := <-done; err == nil {
		t.Fatal("worker ignored durable cancellation during sync")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Cancelled || job.Error.Code != AgentCancelled ||
		job.AcceptedPackets != 1 || job.AcceptedSessions != 1 || job.AcceptedSyncPending {
		t.Fatalf("cancelled sync job=%#v found=%v err=%v", job, found, err)
	}
}

func waitForWorkerState(t *testing.T, store Store, jobID string, state State, phase Phase) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, _, found, err := store.Load(jobID)
		if err != nil {
			t.Fatal(err)
		}
		if found && job.State == state && job.Phase == phase {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	job, _, _, err := store.Load(jobID)
	t.Fatalf("job did not reach %s/%s: job=%#v err=%v", state, phase, job, err)
	return Job{}
}

func TestWorkerFailureMatrixStopsAtFirstFailureWithFixedSafeCode(t *testing.T) {
	tests := []struct {
		name            string
		missingAgent    bool
		prepareErr      error
		agentErr        error
		malformed       bool
		applyErr        error
		syncReport      func(string) syncengine.Report
		want            ErrorCode
		prepareCalls    int
		generateCalls   int
		applyCalls      int
		syncCalls       int
		wantSyncPending bool
	}{
		{name: "preflight unconfigured", missingAgent: true, want: AgentUnconfigured},
		{name: "discovery rejected", prepareErr: errors.New("private discovery failure"), want: ProposalRejected, prepareCalls: 1},
		{name: "Agent unconfigured", agentErr: agent.NewError(agent.CodeUnconfigured, errors.New("private")), want: AgentUnconfigured, prepareCalls: 1, generateCalls: 1},
		{name: "Agent incompatible", agentErr: agent.NewError(agent.CodeIncompatible, errors.New("private")), want: AgentIncompatible, prepareCalls: 1, generateCalls: 1},
		{name: "Agent auth", agentErr: agent.NewError(agent.CodeAuth, errors.New("private")), want: AgentAuth, prepareCalls: 1, generateCalls: 1},
		{name: "Agent busy", agentErr: agent.NewError(agent.CodeBusy, errors.New("private")), want: AgentBusy, prepareCalls: 1, generateCalls: 1},
		{name: "Agent timeout", agentErr: agent.NewError(agent.CodeTimeout, errors.New("private")), want: AgentTimeout, prepareCalls: 1, generateCalls: 1},
		{name: "Agent forbidden tool", agentErr: agent.NewError(agent.CodeToolForbidden, errors.New("private")), want: AgentToolForbidden, prepareCalls: 1, generateCalls: 1},
		{name: "malformed proposal", malformed: true, want: ProposalRejected, prepareCalls: 1, generateCalls: 1},
		{name: "uncertain apply", applyErr: errors.New("private uncertain receipt"), want: ApplyRecovery, prepareCalls: 1, generateCalls: 1, applyCalls: 1},
		{name: "sync conflict", syncReport: func(projectID string) syncengine.Report {
			report := workerSyncReport(projectID)
			report.Conflicts = []string{"private-conflict"}
			return report
		}, want: SyncConflict, prepareCalls: 1, generateCalls: 1, applyCalls: 1, syncCalls: 1, wantSyncPending: true},
		{name: "partial sync", syncReport: func(projectID string) syncengine.Report {
			return syncengine.Report{ProjectID: projectID}
		}, want: SyncPartial, prepareCalls: 1, generateCalls: 1, applyCalls: 1, syncCalls: 1, wantSyncPending: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hash := strings.Repeat(string("0123456789abcdef"[index%16]), 64)
			fixture := newWorkerFixture(t, []FrozenSession{{
				SessionID: "session-s1", StartedAt: fixtureTime(7),
				Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
			}})
			packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000051")
			packet.CWD = "/repo"
			accepted := workerInitialAccepted(fixture.job.ProjectID)
			prepareCalls, applyCalls, syncCalls := 0, 0, 0
			adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
			adapter.generate = func(agent.Request) (agent.Result, error) {
				if test.agentErr != nil {
					return agent.Result{}, test.agentErr
				}
				if test.malformed {
					return agent.Result{Proposal: []byte(`{"malformed":true}`)}, nil
				}
				return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
			}
			options := workerRunOptions(
				fixture,
				func(context.Context, PrepareRequest) (Prepared, error) {
					prepareCalls++
					if test.prepareErr != nil {
						return Prepared{}, test.prepareErr
					}
					return workerPrepared(t, packet, accepted.snapshot(t)), nil
				},
				adapter,
				func(_ context.Context, request ApplyRequest) (apply.Result, error) {
					applyCalls++
					if test.applyErr != nil {
						return apply.Result{}, test.applyErr
					}
					accepted.apply(t, request.Changes)
					return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
				},
				func(context.Context, syncproject.Options) (syncengine.Report, error) {
					syncCalls++
					if test.syncReport != nil {
						return test.syncReport(packet.ProjectID), nil
					}
					return workerSyncReport(packet.ProjectID), nil
				},
				nil,
			)
			if test.missingAgent {
				options.Agent = nil
			}
			if err := Run(t.Context(), options); err == nil {
				t.Fatal("Run() accepted injected first failure")
			}
			job, revision, found, err := fixture.store.Load(fixture.job.ID)
			status, statusErr := ProjectStatusAtRevision(&job, fixture.job.ProjectID, revision)
			if err != nil || statusErr != nil || !found || job.State != Failed || job.Error.Code != test.want ||
				prepareCalls != test.prepareCalls || adapter.generateCalls != test.generateCalls ||
				applyCalls != test.applyCalls || syncCalls != test.syncCalls ||
				job.AcceptedSyncPending != test.wantSyncPending || status.CanSyncOnly != test.wantSyncPending {
				t.Fatalf("failure matrix job=%#v status=%#v found=%v loadErr=%v statusErr=%v calls=%d/%d/%d/%d", job, status, found, err, statusErr, prepareCalls, adapter.generateCalls, applyCalls, syncCalls)
			}
		})
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
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
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
	job, revision, found, err := fixture.store.Load(fixture.job.ID)
	status, statusErr := ProjectStatusAtRevision(&job, fixture.job.ProjectID, revision)
	if err != nil || statusErr != nil || !found || job.State != Failed || job.Error.Code != SyncPartial || job.AcceptedPackets != 1 || job.AcceptedSessions != 0 || job.SessionIndex != 0 || job.CurrentPacket != packet.NextCursor || !job.AcceptedSyncPending || !status.CanSyncOnly {
		t.Fatalf("incomplete-sync job=%#v found=%v err=%v", job, found, err)
	}
}

func TestWorkerApplyRecoveryRetainsAuthenticatedExactPacketAndProposal(t *testing.T) {
	hash := strings.Repeat("6", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000019")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			return apply.Result{}, errors.New("apply receipt is uncertain")
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("uncertain apply reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted uncertain apply")
	}
	job, revision, found, err := fixture.store.Load(fixture.job.ID)
	status, statusErr := ProjectStatusAtRevision(&job, fixture.job.ProjectID, revision)
	if err != nil || statusErr != nil || !found || job.Error.Code != ApplyRecovery ||
		job.PayloadState != PayloadApplyRecovery || job.PayloadRetainedFor != ApplyRecovery ||
		job.AcceptedSyncPending || status.CanSyncOnly {
		t.Fatalf("apply-recovery job=%#v status=%#v found=%v err=%v statusErr=%v", job, status, found, err, statusErr)
	}
	inputs := filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs")
	packetBody, packetErr := os.ReadFile(filepath.Join(inputs, packetWorkName))
	proposalBody, proposalErr := os.ReadFile(filepath.Join(inputs, proposalWorkName))
	if packetErr != nil || proposalErr != nil || digestPrivate(packetBody) != job.PacketDigest || digestPrivate(proposalBody) != job.ResultDigest {
		t.Fatalf("retained packet=%v proposal=%v packetDigest=%q/%q proposalDigest=%q/%q", packetErr, proposalErr, digestPrivate(packetBody), job.PacketDigest, digestPrivate(proposalBody), job.ResultDigest)
	}
}

func TestWorkerPayloadPublicationWALAtEveryBoundary(t *testing.T) {
	stages := []payloadCheckpointStage{
		payloadBeforeIntentCAS, payloadAfterIntentCAS, payloadBeforeWrite, payloadAfterWrite,
		payloadBeforeRename, payloadAfterRename, payloadBeforeVerify, payloadAfterVerify,
		payloadBeforeRetainedCAS, payloadAfterRetainedCAS,
	}
	for _, kind := range []PayloadKind{PayloadPacket, PayloadProposal} {
		for _, stage := range stages {
			t.Run(string(kind)+"_"+string(stage), func(t *testing.T) {
				hash := strings.Repeat("7", 64)
				fixture := newWorkerFixture(t, []FrozenSession{{
					SessionID: "session-s1", StartedAt: fixtureTime(7),
					Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
				}})
				packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000029")
				packet.CWD = "/repo"
				accepted := workerInitialAccepted(fixture.job.ProjectID)
				adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
				adapter.generate = func(agent.Request) (agent.Result, error) {
					return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
				}
				options := workerRunOptions(
					fixture,
					func(context.Context, PrepareRequest) (Prepared, error) {
						return workerPrepared(t, packet, accepted.snapshot(t)), nil
					},
					adapter,
					func(context.Context, ApplyRequest) (apply.Result, error) { panic("WAL crash reached apply") },
					func(context.Context, syncproject.Options) (syncengine.Report, error) { panic("WAL crash reached sync") },
					nil,
				)
				crash := errors.New("simulated payload publication crash")
				observed := false
				options.payloadCheckpoint = func(gotKind PayloadKind, gotStage payloadCheckpointStage) error {
					if gotKind != kind || gotStage != stage {
						return nil
					}
					observed = true
					assertPayloadCheckpointBoundary(t, fixture, kind, stage)
					return crash
				}
				if err := Run(t.Context(), options); !errors.Is(err, crash) {
					t.Fatalf("Run() err=%v want simulated crash", err)
				}
				if !observed {
					t.Fatal("target payload checkpoint was not reached")
				}

				recovered, revision, disposition, err := fixture.store.RecoverInterrupted(fixture.job.ID)
				if err != nil || disposition != RecoveryApplyInspectionNeeded || recovered.State != Failed || recovered.Error.Code != ApplyRecovery {
					t.Fatalf("RecoverInterrupted()=%#v revision=%d disposition=%q err=%v", recovered, revision, disposition, err)
				}
				if !(kind == PayloadPacket && stage == payloadBeforeIntentCAS) && recovered.PayloadState != PayloadCleanupPending {
					t.Fatalf("recovery did not authorize bounded cleanup: %#v", recovered)
				}
				inputsPath := filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs")
				if recovered.PayloadState == "" {
					entries, readErr := os.ReadDir(inputsPath)
					if readErr != nil || len(entries) != 0 {
						t.Fatalf("pre-intent crash left private payloads: entries=%v err=%v", entries, readErr)
					}
					return
				}
				inputs, err := os.OpenRoot(inputsPath)
				if err != nil {
					t.Fatal(err)
				}
				cleanupErr := cleanupPrivatePayloads(inputs, recovered)
				closeErr := inputs.Close()
				if cleanupErr != nil || closeErr != nil {
					t.Fatalf("cleanup=%v close=%v", cleanupErr, closeErr)
				}
				completed, _, err := fixture.store.Update(fixture.job.ID, revision, func(job *Job) error {
					setPayloadLifecycle(job, PayloadCleanupComplete, PayloadCleanupByDigest)
					return nil
				})
				if err != nil || completed.PayloadState != PayloadCleanupComplete {
					t.Fatalf("persist cleanup-complete job=%#v err=%v", completed, err)
				}
				entries, err := os.ReadDir(inputsPath)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() == packetWorkName || entry.Name() == proposalWorkName || privateAtomicTempName(entry.Name()) {
						t.Fatalf("recovery cleanup retained private publication %q", entry.Name())
					}
				}
			})
		}
	}
}

func assertPayloadCheckpointBoundary(t *testing.T, fixture workerFixture, kind PayloadKind, stage payloadCheckpointStage) {
	t.Helper()
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found {
		t.Fatalf("checkpoint Load() found=%v err=%v", found, err)
	}
	wantCount := 0
	wantGlobal := PayloadState("")
	wantTarget := PayloadState("")
	if kind == PayloadProposal || stage != payloadBeforeIntentCAS {
		wantCount = 1
		wantGlobal = PayloadRetained
		wantTarget = PayloadRetained
	}
	if stage != payloadBeforeIntentCAS {
		wantCount = 1
		if kind == PayloadProposal {
			wantCount = 2
		}
		wantGlobal = PayloadPublishing
		wantTarget = PayloadPublishing
	}
	if stage == payloadAfterRetainedCAS {
		wantGlobal = PayloadRetained
		wantTarget = PayloadRetained
	}
	if len(job.PayloadPublications) != wantCount || job.PayloadState != wantGlobal {
		t.Fatalf("checkpoint job publications=%#v global=%q want count=%d global=%q", job.PayloadPublications, job.PayloadState, wantCount, wantGlobal)
	}
	if wantCount != 0 && !(kind == PayloadProposal && stage == payloadBeforeIntentCAS) {
		target := job.PayloadPublications[wantCount-1]
		if target.Kind != kind || target.State != wantTarget || target.CleanupAuthority != PayloadCleanupNotAuthorized ||
			(kind == PayloadPacket && target.Name != packetWorkName) || (kind == PayloadProposal && target.Name != proposalWorkName) {
			t.Fatalf("checkpoint target publication=%#v want kind=%q state=%q", target, kind, wantTarget)
		}
	} else if kind == PayloadProposal && stage == payloadBeforeIntentCAS {
		packet := job.PayloadPublications[0]
		if packet.Kind != PayloadPacket || packet.State != PayloadRetained || packet.CleanupAuthority != PayloadCleanupNotAuthorized {
			t.Fatalf("proposal pre-intent lost retained packet authority: %#v", packet)
		}
	}
	inputs := filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs")
	entries, err := os.ReadDir(inputs)
	if err != nil {
		t.Fatal(err)
	}
	targetName := packetWorkName
	if kind == PayloadProposal {
		targetName = proposalWorkName
	}
	named, temps := false, 0
	for _, entry := range entries {
		if entry.Name() == targetName {
			named = true
		}
		if privateAtomicTempName(entry.Name()) {
			temps++
		}
	}
	wantNamed := stage == payloadAfterRename || stage == payloadBeforeVerify || stage == payloadAfterVerify ||
		stage == payloadBeforeRetainedCAS || stage == payloadAfterRetainedCAS
	wantTemp := stage == payloadAfterWrite || stage == payloadBeforeRename
	if named != wantNamed || (temps != 0) != wantTemp {
		t.Fatalf("checkpoint files named=%v temps=%d want named=%v temp=%v entries=%v", named, temps, wantNamed, wantTemp, entries)
	}
}

func TestWorkerRejectsPrepareBytesThatDoNotAuthenticatePacketBeforeIntent(t *testing.T) {
	hash := strings.Repeat("8", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000030")
	packet.CWD = "/repo"
	canonical, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	packet.Events[0].Summary = "packet changed after canonical prepare bytes"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	generateCalls := 0
	adapter := &verifiedWorkerAgent{
		capability: workerCapability("fixture", "1.0.0"),
		generate: func(agent.Request) (agent.Result, error) {
			generateCalls++
			return agent.Result{}, agent.NewError(agent.CodeAuth, errors.New("mismatched packet reached Agent"))
		},
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return Prepared{Packet: packet, PacketBytes: canonical, Accepted: accepted.snapshot(t)}, nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { panic("mismatched packet reached apply") },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("mismatched packet reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted packet structure that did not match Prepare canonical bytes")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.Error.Code != ProposalRejected || job.PacketDigest != "" || len(job.PayloadPublications) != 0 || generateCalls != 0 {
		t.Fatalf("mismatched prepare job=%#v found=%v err=%v generate=%d", job, found, err, generateCalls)
	}
}

func TestWorkerConsumesLaunchTokenOnlyWithDurableOwnership(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	token := "private-launch-token-with-at-least-32-bytes"
	loaded, revision, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found {
		t.Fatalf("Load() found=%v err=%v", found, err)
	}
	loaded, _, err = fixture.store.Update(fixture.job.ID, revision, func(job *Job) error {
		job.LaunchTokenDigest = launchTokenDigest(token)
		job.LaunchIntentAt = fixture.now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.job = loaded

	wrong := workerRunOptions(fixture, nil, nil, nil, func(context.Context, syncproject.Options) (syncengine.Report, error) {
		panic("wrong launch token reached sync")
	}, nil)
	wrong.LaunchToken = token + "-wrong"
	if err := Run(t.Context(), wrong); err == nil {
		t.Fatal("Run() accepted wrong launch token")
	}
	unchanged, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || unchanged.State != Queued || unchanged.Owner.ID != "" || unchanged.LaunchTokenDigest == "" {
		t.Fatalf("wrong-token job=%#v found=%v err=%v", unchanged, found, err)
	}

	owned := false
	options := workerRunOptions(fixture, nil, nil, nil, func(context.Context, syncproject.Options) (syncengine.Report, error) {
		return workerSyncReport(fixture.job.ProjectID), nil
	}, nil)
	options.LaunchToken = token
	options.OwnershipReady = func() error {
		current, _, found, err := fixture.store.Load(fixture.job.ID)
		if err != nil || !found || current.State != Running || current.Owner.ID == "" || current.LaunchTokenDigest != "" || !current.LaunchIntentAt.IsZero() {
			t.Fatalf("ownership callback job=%#v found=%v err=%v", current, found, err)
		}
		owned = true
		return nil
	}
	if err := Run(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if !owned {
		t.Fatal("worker did not signal durable ownership")
	}
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("replayed launch token was accepted")
	}
}

func TestWorkerDoesNotConsumeLaunchAuthorityBeforeRootAuthentication(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	token := "private-root-auth-token-with-at-least-32-bytes"
	loaded, revision, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found {
		t.Fatalf("Load() found=%v err=%v", found, err)
	}
	loaded, _, err = fixture.store.Update(fixture.job.ID, revision, func(job *Job) error {
		job.LaunchTokenDigest = launchTokenDigest(token)
		job.LaunchIntentAt = fixture.now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.job = loaded

	originalProject := fixture.project + "-before-root-auth"
	if err := os.Rename(fixture.project, originalProject); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.project, 0o700); err != nil {
		t.Fatal(err)
	}

	readyCalls := 0
	options := workerRunOptions(fixture, nil, nil, nil, func(context.Context, syncproject.Options) (syncengine.Report, error) {
		t.Fatal("unauthenticated worker roots reached sync")
		return syncengine.Report{}, nil
	}, nil)
	options.LaunchToken = token
	options.OwnershipReady = func() error {
		readyCalls++
		return nil
	}
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted a replaced Project before durable ownership")
	}

	unchanged, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found {
		t.Fatalf("Load() found=%v err=%v", found, err)
	}
	if unchanged.State != Queued || unchanged.Owner.ID != "" || unchanged.LaunchTokenDigest != launchTokenDigest(token) ||
		!unchanged.LaunchIntentAt.Equal(fixture.now) || readyCalls != 0 {
		t.Fatalf("pre-auth failure mutated launch authority: job=%#v readyCalls=%d", unchanged, readyCalls)
	}
}

func TestWorkerOwnsRealPreparePayloadPublicationAcrossPreIntentCrashes(t *testing.T) {
	tests := []struct {
		name    string
		prepare prepareCheckpointStage
		payload payloadCheckpointStage
	}{
		{name: string(prepareAfterReturn), prepare: prepareAfterReturn},
		{name: string(prepareAfterRootCheck), prepare: prepareAfterRootCheck},
		{name: string(prepareAfterValidation), prepare: prepareAfterValidation},
		{name: string(payloadBeforeIntentCAS), payload: payloadBeforeIntentCAS},
		{name: string(payloadAfterIntentCAS), payload: payloadAfterIntentCAS},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkerFixture(t, nil)
			sessionsRoot := filepath.Join(fixture.root, "sessions")
			if err := os.MkdirAll(sessionsRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			meta := `{"timestamp":"2026-08-29T07:00:00Z","type":"session_meta","payload":{"id":"session-real","cwd":"` + filepath.ToSlash(fixture.project) + `","source":"vscode"}}`
			record := responseRecord("2026-08-29T07:01:00Z", "one", "review this exact session")
			if err := os.WriteFile(filepath.Join(sessionsRoot, "real.jsonl"), []byte(meta+"\n"+record+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			upper := evidence.CursorBoundary{Line: 2, SourceHash: recordHash(record)}
			loaded, revision, found, err := fixture.store.Load(fixture.job.ID)
			if err != nil || !found {
				t.Fatalf("Load() found=%v err=%v", found, err)
			}
			loaded, _, err = fixture.store.Update(fixture.job.ID, revision, func(job *Job) error {
				job.FrozenSessions = []FrozenSession{{SessionID: "session-real", StartedAt: fixtureTime(7), Upper: upper}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			fixture.job = loaded
			accepted := workerInitialAccepted(fixture.job.ProjectID)
			seedWorkerReviewV2AndSyncData(t, fixture, accepted)

			prepareCalls := 0
			options := workerRunOptions(
				fixture,
				func(_ context.Context, request PrepareRequest) (Prepared, error) {
					prepareCalls++
					result, err := prepare.Run(prepare.Options{
						Mode: "review", SessionsRoot: sessionsRoot, SessionID: request.SessionID,
						CWD: request.ProjectRoot, DataDir: request.DataDir, GOOS: "test",
						Now: fixture.now, AmbiguityWindow: time.Second, UpperBoundary: &request.UpperBoundary,
					})
					if err != nil {
						return Prepared{}, err
					}
					return Prepared{Packet: result.Packet, PacketBytes: result.Canonical, Accepted: accepted.snapshot(t)}, nil
				},
				&verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0"), generate: func(agent.Request) (agent.Result, error) {
					t.Fatal("pre-intent prepare crash reached Agent")
					return agent.Result{}, nil
				}},
				func(context.Context, ApplyRequest) (apply.Result, error) {
					t.Fatal("pre-intent prepare crash reached apply")
					return apply.Result{}, nil
				},
				func(context.Context, syncproject.Options) (syncengine.Report, error) {
					t.Fatal("pre-intent prepare crash reached sync")
					return syncengine.Report{}, nil
				},
				nil,
			)
			crash := errors.New("simulated prepare crash")
			observed := false
			options.prepareCheckpoint = func(got prepareCheckpointStage) error {
				if test.prepare == "" || got != test.prepare {
					return nil
				}
				observed = true
				return crash
			}
			options.payloadCheckpoint = func(kind PayloadKind, got payloadCheckpointStage) error {
				if test.payload == "" || kind != PayloadPacket || got != test.payload {
					return nil
				}
				observed = true
				return crash
			}
			if err := Run(t.Context(), options); !errors.Is(err, crash) {
				t.Fatalf("Run() err=%v want simulated prepare crash", err)
			}
			if !observed || prepareCalls != 1 {
				t.Fatalf("checkpoint observed=%v prepareCalls=%d", observed, prepareCalls)
			}
			job, _, found, err := fixture.store.Load(fixture.job.ID)
			wantIntent := test.payload == payloadAfterIntentCAS
			if err != nil || !found || (job.PacketDigest != "") != wantIntent || len(job.PayloadPublications) != map[bool]int{false: 0, true: 1}[wantIntent] {
				t.Fatalf("pre-intent job=%#v found=%v err=%v", job, found, err)
			}
			entries, err := os.ReadDir(filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("prepare leaked named or temp payload entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestWorkerNextPacketPublicationIntentDropsStalePriorDigests(t *testing.T) {
	firstHash, secondHash := strings.Repeat("8", 64), strings.Repeat("9", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 2, SourceHash: secondHash},
	}})
	packets := []evidence.Packet{
		workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", firstHash, true, "ev-000000000030"),
		workerPacket(fixture.job.ProjectID, "session-s1", 2, 2, firstHash, secondHash, false, "ev-000000000031"),
	}
	for index := range packets {
		packets[index].CWD = "/repo"
	}
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	packetIndex := 0
	current := packets[0]
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, current, accepted.legacy)}, nil
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			current = packets[packetIndex]
			packetIndex++
			return workerPrepared(t, current, accepted.snapshot(t)), nil
		},
		adapter,
		func(_ context.Context, request ApplyRequest) (apply.Result, error) {
			accepted.apply(t, request.Changes)
			return apply.Result{ProjectID: current.ProjectID, SessionID: current.SessionID, FromCursor: current.FromCursor, ToCursor: current.ToCursor, CursorAdvanced: true}, nil
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			return workerSyncReport(fixture.job.ProjectID), nil
		},
		nil,
	)
	var priorPacket, priorProposal string
	crash := errors.New("stop after second packet intent")
	options.payloadCheckpoint = func(kind PayloadKind, stage payloadCheckpointStage) error {
		job, _, found, err := fixture.store.Load(fixture.job.ID)
		if err != nil || !found {
			return errors.New("cannot inspect publication job")
		}
		if kind == PayloadProposal && stage == payloadAfterRetainedCAS && job.AcceptedPackets == 0 {
			priorPacket, priorProposal = job.PacketDigest, job.ResultDigest
		}
		if kind == PayloadPacket && stage == payloadAfterIntentCAS && job.AcceptedPackets == 1 {
			if len(job.PayloadPublications) != 1 || job.ResultDigest != "" || job.PacketDigest == priorPacket ||
				job.PayloadPublications[0].Digest != job.PacketDigest || job.PayloadPublications[0].Digest == priorProposal {
				return errors.New("new packet intent retained stale digest authority")
			}
			entries, readErr := os.ReadDir(filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs"))
			if readErr != nil || len(entries) != 0 {
				return errors.New("new packet intent inherited stale private files")
			}
			return crash
		}
		return nil
	}
	if err := Run(t.Context(), options); !errors.Is(err, crash) {
		t.Fatalf("Run() err=%v want second intent crash", err)
	}
	if priorPacket == "" || priorProposal == "" {
		t.Fatal("first packet never established retained digest evidence")
	}
}

func TestRecoverInterruptedPayloadIntentHandlesMissingNamedAndAtomicTemp(t *testing.T) {
	for _, shape := range []string{"missing", "named", "atomic_temp"} {
		t.Run(shape, func(t *testing.T) {
			hash := strings.Repeat("a", 64)
			fixture := newWorkerFixture(t, []FrozenSession{{
				SessionID: "session-s1", StartedAt: fixtureTime(7),
				Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
			}})
			body := []byte(`{"packet":"durable intent"}`)
			digest := digestPrivate(body)
			if _, _, err := fixture.store.Update(fixture.job.ID, 1, func(job *Job) error {
				job.State = Running
				job.Phase = Reviewing
				job.StartedAt = fixture.now
				job.Owner = Owner{ID: "worker-owner-1", AcquiredAt: fixture.now}
				job.PacketDigest = digest
				job.PayloadState = PayloadPublishing
				job.PayloadPublications = []PayloadPublication{{
					Kind: PayloadPacket, Name: packetWorkName, Digest: digest,
					State: PayloadPublishing, CleanupAuthority: PayloadCleanupNotAuthorized,
				}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			inputsPath := filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs")
			if err := os.MkdirAll(inputsPath, 0o700); err != nil {
				t.Fatal(err)
			}
			switch shape {
			case "named":
				if err := os.WriteFile(filepath.Join(inputsPath, packetWorkName), body, 0o600); err != nil {
					t.Fatal(err)
				}
			case "atomic_temp":
				if err := os.WriteFile(filepath.Join(inputsPath, ".session-reviewer-"+strings.Repeat("a", 32)), body, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			recovered, revision, disposition, err := fixture.store.RecoverInterrupted(fixture.job.ID)
			if err != nil || disposition != RecoveryApplyInspectionNeeded || recovered.PayloadState != PayloadCleanupPending ||
				len(recovered.PayloadPublications) != 1 || recovered.PayloadPublications[0].CleanupAuthority != PayloadCleanupByDigest {
				t.Fatalf("RecoverInterrupted()=%#v revision=%d disposition=%q err=%v", recovered, revision, disposition, err)
			}
			inputs, err := os.OpenRoot(inputsPath)
			if err != nil {
				t.Fatal(err)
			}
			cleanupErr := cleanupPrivatePayloads(inputs, recovered)
			closeErr := inputs.Close()
			if cleanupErr != nil || closeErr != nil {
				t.Fatalf("cleanup=%v close=%v", cleanupErr, closeErr)
			}
			completed, _, err := fixture.store.Update(fixture.job.ID, revision, func(job *Job) error {
				setPayloadLifecycle(job, PayloadCleanupComplete, PayloadCleanupByDigest)
				return nil
			})
			if err != nil || completed.PayloadState != PayloadCleanupComplete {
				t.Fatalf("cleanup completion=%#v err=%v", completed, err)
			}
			entries, err := os.ReadDir(inputsPath)
			if err != nil || len(entries) != 0 {
				t.Fatalf("cleanup entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestWorkerNeverCleansPayloadBeforeDurableCleanupBoundary(t *testing.T) {
	for _, boundary := range []string{"before", "after"} {
		t.Run(boundary+" cleanup CAS crash", func(t *testing.T) {
			hash := strings.Repeat("b", 64)
			fixture := newWorkerFixture(t, []FrozenSession{{
				SessionID: "session-s1", StartedAt: fixtureTime(7),
				Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
			}})
			packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000022")
			packet.CWD = "/repo"
			accepted := workerInitialAccepted(fixture.job.ProjectID)
			adapter := &verifiedWorkerAgent{
				capability: workerCapability("fixture", "1.0.0"),
				generate: func(agent.Request) (agent.Result, error) {
					return agent.Result{}, agent.NewError(agent.CodeAuth, errors.New("fixture auth failure"))
				},
			}
			options := workerRunOptions(
				fixture,
				func(context.Context, PrepareRequest) (Prepared, error) {
					return workerPrepared(t, packet, accepted.snapshot(t)), nil
				},
				adapter,
				func(context.Context, ApplyRequest) (apply.Result, error) { panic("failed Agent reached apply") },
				func(context.Context, syncproject.Options) (syncengine.Report, error) {
					panic("failed Agent reached sync")
				},
				nil,
			)
			crash := errors.New("simulated process crash at cleanup boundary")
			if boundary == "before" {
				options.beforeCleanupBoundary = func() error { return crash }
			} else {
				options.afterCleanupBoundary = func() error { return crash }
			}
			if err := Run(t.Context(), options); !errors.Is(err, crash) {
				t.Fatalf("Run() err=%v want simulated crash", err)
			}
			job, _, found, err := fixture.store.Load(fixture.job.ID)
			if err != nil || !found {
				t.Fatalf("Load() found=%v err=%v", found, err)
			}
			if boundary == "before" {
				if job.State != Running || job.PayloadState != PayloadRetained {
					t.Fatalf("pre-CAS crash mutated cleanup state: %#v", job)
				}
			} else if job.State != Failed || job.Error.Code != AgentAuth || job.PayloadState != PayloadCleanupPending {
				t.Fatalf("post-CAS crash did not retain cleanup-pending state: %#v", job)
			}
			body, err := os.ReadFile(filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs", packetWorkName))
			if err != nil || digestPrivate(body) != job.PacketDigest {
				t.Fatalf("crash lost authenticated packet: err=%v digest=%q want=%q", err, digestPrivate(body), job.PacketDigest)
			}
			if boundary == "before" {
				recovered, _, disposition, err := fixture.store.RecoverInterrupted(fixture.job.ID)
				if err != nil || disposition != RecoveryApplyInspectionNeeded || recovered.State != Failed ||
					recovered.Error.Code != ApplyRecovery || recovered.PayloadState != PayloadCleanupPending {
					t.Fatalf("RecoverInterrupted() after pre-CAS crash = %#v, %q, %v", recovered, disposition, err)
				}
				retained, readErr := os.ReadFile(filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs", packetWorkName))
				if readErr != nil || !bytes.Equal(retained, body) || digestPrivate(retained) != recovered.PacketDigest {
					t.Fatalf("interrupted recovery changed retained packet: err=%v digest=%q want=%q", readErr, digestPrivate(retained), recovered.PacketDigest)
				}
			}
		})
	}
}

func TestWorkerRejectsDryRunAndPartialMigrationSyncReports(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*syncengine.Report)
	}{
		{name: "report dry run", mutate: func(report *syncengine.Report) { report.DryRun = true }},
		{name: "migration dry run", mutate: func(report *syncengine.Report) { report.Migration.DryRun = true }},
		{name: "partial migration plan", mutate: func(report *syncengine.Report) {
			report.Migration.Required = true
			report.Migration.Creates = []string{"review-v2.json"}
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hash := strings.Repeat(string(rune('a'+index)), 64)
			fixture := newWorkerFixture(t, []FrozenSession{{
				SessionID: "session-s1", StartedAt: fixtureTime(7),
				Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
			}})
			packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000015")
			packet.CWD = "/repo"
			accepted := workerInitialAccepted(fixture.job.ProjectID)
			adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
			adapter.generate = func(agent.Request) (agent.Result, error) {
				return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
			}
			options := workerRunOptions(
				fixture,
				func(context.Context, PrepareRequest) (Prepared, error) {
					return workerPrepared(t, packet, accepted.snapshot(t)), nil
				},
				adapter,
				func(_ context.Context, request ApplyRequest) (apply.Result, error) {
					accepted.apply(t, request.Changes)
					return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
				},
				func(context.Context, syncproject.Options) (syncengine.Report, error) {
					report := workerSyncReport(packet.ProjectID)
					test.mutate(&report)
					return report, nil
				},
				nil,
			)
			if err := Run(t.Context(), options); err == nil {
				t.Fatal("Run() accepted a non-real or partial sync report")
			}
			job, revision, found, err := fixture.store.Load(fixture.job.ID)
			status, statusErr := ProjectStatusAtRevision(&job, fixture.job.ProjectID, revision)
			if err != nil || statusErr != nil || !found || job.Error.Code != SyncPartial ||
				!job.AcceptedSyncPending || !status.CanSyncOnly || job.CurrentPacket != packet.NextCursor {
				t.Fatalf("rejected sync job=%#v status=%#v found=%v err=%v statusErr=%v", job, status, found, err, statusErr)
			}
		})
	}
}

func TestWorkerAcceptsCompletedMigrationAudit(t *testing.T) {
	hash := strings.Repeat("8", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000028")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(_ context.Context, request ApplyRequest) (apply.Result, error) {
			accepted.apply(t, request.Changes)
			return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			report := workerSyncReport(packet.ProjectID)
			report.Migration.Creates = []string{"docs/session-review/项目回顾.md", "docs/session-review/项目历史.md"}
			report.Migration.Archives = []string{"docs/session-review/project-overview.md"}
			return report, nil
		},
		nil,
	)
	if err := Run(t.Context(), options); err != nil {
		t.Fatalf("Run() rejected completed migration audit: %v", err)
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Completed || job.AcceptedSessions != 1 {
		t.Fatalf("completed-migration job=%#v found=%v err=%v", job, found, err)
	}
}

func TestWorkerEarlierSyncedPacketDoesNotOfferSyncOnlyAfterLaterAgentFailure(t *testing.T) {
	firstHash, secondHash := strings.Repeat("1", 64), strings.Repeat("2", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 2, SourceHash: secondHash},
	}})
	packets := []evidence.Packet{
		workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", firstHash, true, "ev-000000000016"),
		workerPacket(fixture.job.ProjectID, "session-s1", 2, 2, firstHash, secondHash, false, "ev-000000000017"),
	}
	for index := range packets {
		packets[index].CWD = "/repo"
	}
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	index := 0
	current := packets[0]
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		if current.FromCursor == 2 {
			return agent.Result{}, agent.NewError(agent.CodeAuth, errors.New("later auth failure"))
		}
		return agent.Result{Proposal: workerDraft(t, current, accepted.legacy)}, nil
	}
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			current = packets[index]
			index++
			return workerPrepared(t, current, accepted.snapshot(t)), nil
		},
		adapter,
		func(_ context.Context, request ApplyRequest) (apply.Result, error) {
			accepted.apply(t, request.Changes)
			return apply.Result{ProjectID: current.ProjectID, SessionID: current.SessionID, FromCursor: current.FromCursor, ToCursor: current.ToCursor, CursorAdvanced: true}, nil
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			return workerSyncReport(fixture.job.ProjectID), nil
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted later Agent failure")
	}
	job, revision, found, err := fixture.store.Load(fixture.job.ID)
	status, statusErr := ProjectStatusAtRevision(&job, fixture.job.ProjectID, revision)
	if err != nil || statusErr != nil || !found || job.Error.Code != AgentAuth || job.AcceptedPackets != 1 ||
		job.AcceptedSyncPending || status.CanSyncOnly {
		t.Fatalf("later failure job=%#v status=%#v found=%v err=%v statusErr=%v", job, status, found, err, statusErr)
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

func TestWorkerRejectsMutationRootOrConfigReplacementAtEveryExternalPhaseWithoutDecoyWrites(t *testing.T) {
	for _, rootKind := range []string{"project", "data", "vault", "config"} {
		for _, phase := range []string{"prepare", "generate", "apply", "sync"} {
			t.Run(rootKind+"_during_"+phase, func(t *testing.T) {
				hash := strings.Repeat("d", 64)
				fixture := newWorkerFixture(t, []FrozenSession{{
					SessionID: "session-s1", StartedAt: fixtureTime(7),
					Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
				}})
				packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000020")
				packet.CWD = "/repo"
				accepted := workerInitialAccepted(fixture.job.ProjectID)
				target := fixture.project
				if rootKind == "data" {
					target = fixture.data
				} else if rootKind == "vault" {
					target = fixture.vault
				} else if rootKind == "config" {
					target = filepath.Join(fixture.data, "config.toml")
				}
				pinnedTarget := target + ".pinned-" + phase
				var configReplacement []byte
				mutated := false
				replacementBlocked := false
				mutate := func(at string) {
					if mutated || at != phase {
						return
					}
					mutated = true
					if rootKind == "config" {
						body, err := os.ReadFile(target)
						if err != nil {
							t.Fatal(err)
						}
						configReplacement = append([]byte(nil), body...)
					}
					if err := os.Rename(target, pinnedTarget); err != nil {
						// Windows refuses to rename Data/Vault directories while
						// their authenticated handles are open. That is the secure
						// outcome this adversarial replacement test is exercising.
						if runtime.GOOS == "windows" && rootKind != "config" && errors.Is(err, os.ErrPermission) {
							replacementBlocked = true
							return
						}
						t.Fatal(err)
					}
					if rootKind == "config" {
						if err := os.WriteFile(target, configReplacement, 0o600); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.Mkdir(target, 0o700); err != nil {
							t.Fatal(err)
						}
					}
				}
				adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
				adapter.generate = func(agent.Request) (agent.Result, error) {
					mutate("generate")
					return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
				}
				options := workerRunOptions(
					fixture,
					func(context.Context, PrepareRequest) (Prepared, error) {
						prepared := workerPrepared(t, packet, accepted.snapshot(t))
						mutate("prepare")
						return prepared, nil
					},
					adapter,
					func(_ context.Context, request ApplyRequest) (apply.Result, error) {
						accepted.apply(t, request.Changes)
						mutate("apply")
						return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, CursorAdvanced: true}, nil
					},
					func(context.Context, syncproject.Options) (syncengine.Report, error) {
						mutate("sync")
						return workerSyncReport(packet.ProjectID), nil
					},
					nil,
				)
				runErr := Run(t.Context(), options)
				if replacementBlocked {
					if _, err := os.Stat(target); err != nil {
						t.Fatalf("blocked replacement lost authoritative root: %v", err)
					}
					if _, err := os.Stat(pinnedTarget); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("blocked replacement created pinned decoy: %v", err)
					}
					return
				}
				if runErr == nil || !mutated {
					t.Fatalf("Run() err=%v mutated=%v", runErr, mutated)
				}
				authoritativeStore := fixture.store
				if rootKind == "data" {
					authoritativeStore.Root = pinnedTarget
				}
				job, _, found, err := authoritativeStore.Load(fixture.job.ID)
				if err != nil || !found || job.State != Failed {
					t.Fatalf("authoritative job=%#v found=%v err=%v", job, found, err)
				}
				if phase == "sync" {
					if job.AcceptedPackets != 1 || job.CurrentPacket != packet.NextCursor || !job.AcceptedSyncPending {
						t.Fatalf("sync-boundary cursor was lost: %#v", job)
					}
				} else if job.AcceptedPackets != 0 || job.CurrentPacket != (evidence.CursorBoundary{}) {
					t.Fatalf("pre-sync replacement advanced authoritative cursor: %#v", job)
				}
				if rootKind == "config" {
					body, err := os.ReadFile(target)
					if err != nil || !bytes.Equal(body, configReplacement) {
						t.Fatalf("replacement config was mutated: bytes=%q err=%v", body, err)
					}
				} else {
					entries, err := os.ReadDir(target)
					if err != nil || len(entries) != 0 {
						t.Fatalf("replacement %s received writes: entries=%v err=%v", rootKind, entries, err)
					}
				}
			})
		}
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

func TestWorkerRejectsModelWhenVerifiedProvenanceIsUnavailableBeforeAccounting(t *testing.T) {
	hash := strings.Repeat("7", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000007")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	adapter := &verifiedWorkerAgent{
		capability: workerCapability("fixture", "1.0.0"),
		generate: func(agent.Request) (agent.Result, error) {
			return agent.Result{
				Proposal: workerDraft(t, packet, accepted.legacy),
				Model:    " \t",
				Usage:    accounting.TokenUsage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
			}, nil
		},
	}
	applyCalls := 0
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { applyCalls++; return apply.Result{}, nil },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("hostile model reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted model provenance denied by the verified capability")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Failed || job.Error.Code != AgentIncompatible ||
		hasReviewAccounting(job.ReviewAccounting) || applyCalls != 0 {
		t.Fatalf("hostile model job=%#v found=%v err=%v apply=%d", job, found, err, applyCalls)
	}
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
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
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

func TestEnrichSourceAccountingKeepsUnknownSourceModelUnpriced(t *testing.T) {
	usage := &accounting.SessionUsage{
		StartedAt: "2026-08-31T01:00:00Z", EndedAt: "2026-08-31T01:00:01Z", DurationMS: 1000,
		Models: []accounting.ModelUsage{{
			Model:      "unpriced-source-model",
			TokenUsage: accounting.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
		}},
		TotalTokens: 12,
	}
	draft := proposal.Proposal{}

	if err := enrichSourceAccounting(&draft, usage, fixtureTime(0), workerPrices{}); err != nil {
		t.Fatalf("enrichSourceAccounting() rejected unknown pricing: %v", err)
	}
	report := draft.SessionReport.Accounting
	if report == nil || report.TotalTokens != 12 || report.TotalCostUSD != 0 || len(report.Models) != 1 ||
		report.Models[0].ModelUsage != usage.Models[0] || report.Models[0].Pricing != (accounting.Pricing{}) || report.Models[0].CostUSD != 0 {
		t.Fatalf("unpriced source accounting = %#v", report)
	}
}
