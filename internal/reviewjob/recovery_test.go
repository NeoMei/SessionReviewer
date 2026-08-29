package reviewjob

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/evidence"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

func TestRetryTransitionIsAtomicIdempotentAndPreservesAcceptedState(t *testing.T) {
	firstHash, upperHash := strings.Repeat("1", 64), strings.Repeat("2", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 2, SourceHash: upperHash},
	}})
	job, revision, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found {
		t.Fatalf("load job: found=%v err=%v", found, err)
	}
	accountingState, err := AddReviewResult(ReviewAccounting{}, agent.Result{
		Usage: accounting.TokenUsage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
	}, fixture.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	packetDigest, resultDigest := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	failedAt := fixture.now.Add(time.Minute)
	job, revision, err = fixture.store.Update(job.ID, revision, func(job *Job) error {
		job.State, job.Phase = Failed, ""
		job.CompletedAt, job.UpdatedAt = failedAt, failedAt
		job.Error = SafeError{Code: ProposalRejected}
		job.AcceptedPackets = 1
		job.CurrentPacket = evidence.CursorBoundary{Line: 1, SourceHash: firstHash}
		job.PacketDigest, job.ResultDigest = packetDigest, resultDigest
		job.ReviewAccounting = accountingState
		job.PayloadState = PayloadCleanupComplete
		job.PayloadPublications = []PayloadPublication{
			{Kind: PayloadPacket, Name: packetWorkName, Digest: packetDigest, State: PayloadCleanupComplete, CleanupAuthority: PayloadCleanupByDigest},
			{Kind: PayloadProposal, Name: proposalWorkName, Digest: resultDigest, State: PayloadCleanupComplete, CleanupAuthority: PayloadCleanupByDigest},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	originalFrozen := append([]FrozenSession(nil), job.FrozenSessions...)

	type result struct {
		job      Job
		revision int
		err      error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(2)
	for range 2 {
		go func() {
			start.Done()
			start.Wait()
			retried, nextRevision, retryErr := RequestRetry(fixture.store, fixture.job.ID, failedAt.Add(time.Minute))
			results <- result{job: retried, revision: nextRevision, err: retryErr}
		}()
	}
	first, second := <-results, <-results
	for _, got := range []result{first, second} {
		if got.err != nil || got.job.State != Retrying || got.job.Phase != Preflight || got.job.Attempt != 2 {
			t.Fatalf("idempotent retry result=%#v", got)
		}
	}
	stored, storedRevision, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || storedRevision != revision+1 ||
		stored.ResultDigest != resultDigest || stored.PacketDigest != packetDigest ||
		stored.CurrentPacket != (evidence.CursorBoundary{Line: 1, SourceHash: firstHash}) ||
		stored.AcceptedPackets != 1 || stored.ReviewAccounting.TotalTokens != 7 ||
		!reflect.DeepEqual(stored.FrozenSessions, originalFrozen) {
		t.Fatalf("retried job lost durable state: job=%#v revision=%d found=%v err=%v", stored, storedRevision, found, err)
	}
}

func TestRetryRecoveryResolvesExactApplyBeforePrepareAndPreservesProgress(t *testing.T) {
	hash := strings.Repeat("6", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000041")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	firstAdapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	firstAdapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{
			Proposal: workerDraft(t, packet, accepted.legacy),
			Usage:    accounting.TokenUsage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		}, nil
	}
	first := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		firstAdapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			return apply.Result{}, errors.New("uncertain apply receipt")
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("uncertain apply reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), first); err == nil {
		t.Fatal("first attempt did not retain uncertain apply")
	}
	failed, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || failed.PayloadState != PayloadApplyRecovery || failed.ResultDigest == "" || failed.ReviewAccounting.TotalTokens != 7 {
		t.Fatalf("failed apply-recovery job=%#v found=%v err=%v", failed, found, err)
	}
	resultDigest := failed.ResultDigest
	if _, _, err := RequestRetry(fixture.store, fixture.job.ID, fixture.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	retryAdapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	retryAdapter.generate = func(agent.Request) (agent.Result, error) {
		panic("retry regenerated before resolving retained apply")
	}
	prepareCalls, applyCalls, syncCalls := 0, 0, 0
	retry := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			prepareCalls++
			return Prepared{}, errors.New("retry prepared before apply recovery")
		},
		retryAdapter,
		func(_ context.Context, request ApplyRequest) (apply.Result, error) {
			applyCalls++
			current, _, found, err := fixture.store.Load(fixture.job.ID)
			if err != nil || !found || current.ResultDigest != resultDigest || current.AcceptedPackets != 0 || current.CurrentPacket != packet.ExpectedCursor {
				t.Fatalf("apply recovery lost retained boundary: job=%#v found=%v err=%v", current, found, err)
			}
			if !reflect.DeepEqual(request.Packet, packet) || digestPrivate(mustMarshalProposal(t, request.Proposal)) != resultDigest {
				t.Fatal("apply recovery did not reuse the exact retained packet/proposal")
			}
			return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, AlreadyApplied: true}, nil
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			return workerSyncReport(packet.ProjectID), nil
		},
		nil,
	)
	if err := Run(t.Context(), retry); err != nil {
		t.Fatalf("retry recovery failed: %v", err)
	}
	completed, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || completed.State != Completed || completed.Attempt != 2 ||
		completed.AcceptedPackets != 1 || completed.AcceptedSessions != 1 ||
		completed.CurrentPacket != (evidence.CursorBoundary{}) || completed.ResultDigest != resultDigest ||
		completed.ReviewAccounting.TotalTokens != 7 || prepareCalls != 0 || retryAdapter.generateCalls != 0 || applyCalls != 1 || syncCalls != 1 {
		t.Fatalf("completed retry=%#v found=%v err=%v prepare=%d generate=%d apply=%d sync=%d", completed, found, err, prepareCalls, retryAdapter.generateCalls, applyCalls, syncCalls)
	}
}

func TestRetryRecoverySyncsDurableAcceptedCursorWithoutRegeneration(t *testing.T) {
	hash := strings.Repeat("7", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000042")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
	}
	first := workerRunOptions(
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
	if err := Run(t.Context(), first); err == nil {
		t.Fatal("first attempt accepted partial sync")
	}
	failed, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || !failed.AcceptedSyncPending || failed.CurrentPacket != packet.NextCursor {
		t.Fatalf("failed sync job=%#v found=%v err=%v", failed, found, err)
	}
	if _, _, err := RequestRetry(fixture.store, fixture.job.ID, fixture.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	retryAdapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	retryAdapter.generate = func(agent.Request) (agent.Result, error) { panic("sync-only retry regenerated") }
	syncCalls := 0
	retry := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) { panic("sync-only retry prepared") },
		retryAdapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { panic("sync-only retry reapplied") },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			current, _, _, _ := fixture.store.Load(fixture.job.ID)
			if !current.AcceptedSyncPending || current.CurrentPacket != packet.NextCursor {
				t.Fatalf("retry sync lost accepted cursor: %#v", current)
			}
			return workerSyncReport(packet.ProjectID), nil
		},
		nil,
	)
	if err := Run(t.Context(), retry); err != nil {
		t.Fatal(err)
	}
	completed, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || completed.State != Completed || completed.Attempt != 2 ||
		completed.AcceptedPackets != 1 || completed.AcceptedSessions != 1 ||
		completed.AcceptedSyncPending || syncCalls != 1 || retryAdapter.generateCalls != 0 {
		t.Fatalf("sync-only retry=%#v found=%v err=%v sync=%d", completed, found, err, syncCalls)
	}
}

func TestRetryRecoveryReauthenticatesFrozenAgentBeforeApplyReceipt(t *testing.T) {
	hash := strings.Repeat("5", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000043")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
	}
	first := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			return apply.Result{}, errors.New("uncertain apply receipt")
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("uncertain apply reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), first); err == nil {
		t.Fatal("first attempt did not retain apply recovery")
	}
	failed, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || failed.PayloadState != PayloadApplyRecovery || failed.ResultDigest == "" {
		t.Fatalf("failed recovery job=%#v found=%v err=%v", failed, found, err)
	}
	resultDigest := failed.ResultDigest
	if _, _, err := RequestRetry(fixture.store, fixture.job.ID, fixture.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	retryAdapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	retryAdapter.generate = func(agent.Request) (agent.Result, error) { panic("stale Agent reached generation") }
	applyCalls, prepareCalls, syncCalls := 0, 0, 0
	retry := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) { prepareCalls++; return Prepared{}, nil },
		retryAdapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { applyCalls++; return apply.Result{}, nil },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			return workerSyncReport(packet.ProjectID), nil
		},
		nil,
	)
	moved := fixture.agent + ".original"
	if err := os.Rename(fixture.agent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.agent, []byte("replacement Agent executable\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), retry); err == nil {
		t.Fatal("retry accepted a replaced Agent executable")
	}
	stopped, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || stopped.State != Failed || stopped.Error.Code != AgentIncompatible ||
		stopped.Attempt != 2 || stopped.PayloadState != PayloadApplyRecovery || stopped.ResultDigest != resultDigest ||
		stopped.AcceptedPackets != 0 || prepareCalls != 0 || applyCalls != 0 || syncCalls != 0 {
		t.Fatalf("stale-Agent recovery=%#v found=%v err=%v calls=%d/%d/%d", stopped, found, err, prepareCalls, applyCalls, syncCalls)
	}
}

func TestCancelRequestedUncertainApplyRetryResolvesBeforeTerminalCancelled(t *testing.T) {
	hash := strings.Repeat("4", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000044")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	firstAdapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	firstAdapter.generate = func(agent.Request) (agent.Result, error) {
		return agent.Result{Proposal: workerDraft(t, packet, accepted.legacy)}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	first := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		firstAdapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			cancel()
			return apply.Result{}, errors.New("uncertain apply")
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("uncertain apply reached sync")
		},
		nil,
	)
	if err := Run(ctx, first); err == nil {
		t.Fatal("first attempt did not retain uncertain apply")
	}
	requested, _, err := RequestRetry(fixture.store, fixture.job.ID, fixture.now.Add(time.Minute))
	if err != nil || requested.State != CancelRequested || requested.Phase != Preflight || requested.Attempt != 2 {
		t.Fatalf("retry did not preserve commit-window cancellation=%#v err=%v", requested, err)
	}

	retryAdapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	retryAdapter.generate = func(agent.Request) (agent.Result, error) { panic("cancelled recovery regenerated") }
	applyCalls, syncCalls := 0, 0
	retry := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) { panic("cancelled recovery prepared") },
		retryAdapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			applyCalls++
			return apply.Result{ProjectID: packet.ProjectID, SessionID: packet.SessionID, FromCursor: 1, ToCursor: 1, AlreadyApplied: true}, nil
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			return workerSyncReport(packet.ProjectID), nil
		},
		nil,
	)
	if err := Run(t.Context(), retry); err == nil {
		t.Fatal("cancelled retry completed instead of terminalizing cancellation")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.State != Cancelled || job.Error.Code != AgentCancelled ||
		job.Attempt != 2 || job.AcceptedPackets != 1 || job.AcceptedSessions != 1 ||
		job.AcceptedSyncPending || applyCalls != 1 || syncCalls != 1 || retryAdapter.generateCalls != 0 {
		t.Fatalf("cancelled recovery=%#v found=%v err=%v apply=%d sync=%d", job, found, err, applyCalls, syncCalls)
	}
}

func TestRetryRecoveryFinishesAuthenticatedCleanupPendingBeforePrepare(t *testing.T) {
	hash := strings.Repeat("3", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	staleBody := []byte("authenticated stale private packet")
	staleDigest := digestPrivate(staleBody)
	inputs := filepath.Join(fixture.data, "review-jobs", "work", fixture.job.ID, "inputs")
	if err := os.MkdirAll(inputs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inputs, packetWorkName), staleBody, 0o600); err != nil {
		t.Fatal(err)
	}
	job, revision, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found {
		t.Fatalf("load: found=%v err=%v", found, err)
	}
	failedAt := fixture.now.Add(time.Minute)
	_, _, err = fixture.store.Update(job.ID, revision, func(job *Job) error {
		job.State, job.Phase = Failed, ""
		job.UpdatedAt, job.CompletedAt = failedAt, failedAt
		job.Error = SafeError{Code: ProposalRejected}
		job.PacketDigest = staleDigest
		job.PayloadState = PayloadCleanupPending
		job.PayloadPublications = []PayloadPublication{{
			Kind: PayloadPacket, Name: packetWorkName, Digest: staleDigest,
			State: PayloadCleanupPending, CleanupAuthority: PayloadCleanupByDigest,
		}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := RequestRetry(fixture.store, fixture.job.ID, failedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	adapter := &verifiedWorkerAgent{capability: workerCapability("fixture", "1.0.0")}
	adapter.generate = func(agent.Request) (agent.Result, error) { panic("cleanup recovery reached Agent") }
	prepareCalls := 0
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			prepareCalls++
			if _, err := os.Stat(filepath.Join(inputs, packetWorkName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Prepare observed cleanup-pending payload: %v", err)
			}
			return Prepared{}, errors.New("stop after proving recovery order")
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) { panic("cleanup recovery reached apply") },
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			panic("cleanup recovery reached sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("retry ignored injected Prepare stop")
	}
	stopped, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || stopped.State != Failed || stopped.Error.Code != ProposalRejected ||
		stopped.Attempt != 2 || stopped.PayloadState != PayloadCleanupComplete || prepareCalls != 1 || adapter.generateCalls != 0 {
		t.Fatalf("cleanup retry=%#v found=%v err=%v prepare=%d generate=%d", stopped, found, err, prepareCalls, adapter.generateCalls)
	}
}

func TestRecoveryInterruptedPreservesResultDigestCurrentPacketAndAcceptedProgress(t *testing.T) {
	firstHash, upperHash := strings.Repeat("8", 64), strings.Repeat("9", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 2, SourceHash: upperHash},
	}})
	leases, err := fixture.store.AcquireLeases(fixture.job.ProjectID, fixture.job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	job, revision, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found {
		t.Fatalf("load job: found=%v err=%v", found, err)
	}
	packetDigest, resultDigest := "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64)
	job, revision, err = fixture.store.Update(job.ID, revision, func(job *Job) error {
		job.State, job.Phase = Running, Applying
		job.StartedAt = fixture.now
		job.Owner = Owner{ID: "interrupted-owner", AcquiredAt: fixture.now}
		job.AcceptedPackets = 1
		job.CurrentPacket = evidence.CursorBoundary{Line: 1, SourceHash: firstHash}
		job.PacketDigest, job.ResultDigest = packetDigest, resultDigest
		job.PayloadState, job.PayloadRetainedFor = PayloadRetained, ""
		job.PayloadPublications = []PayloadPublication{
			{Kind: PayloadPacket, Name: packetWorkName, Digest: packetDigest, State: PayloadRetained, CleanupAuthority: PayloadCleanupNotAuthorized},
			{Kind: PayloadProposal, Name: proposalWorkName, Digest: resultDigest, State: PayloadRetained, CleanupAuthority: PayloadCleanupNotAuthorized},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := leases.Release(); err != nil {
		t.Fatal(err)
	}
	recovered, _, disposition, err := fixture.store.RecoverInterrupted(fixture.job.ID)
	if err != nil || disposition != RecoveryApplyInspectionNeeded || recovered.State != Failed ||
		recovered.ResultDigest != resultDigest || recovered.PacketDigest != packetDigest ||
		recovered.CurrentPacket != (evidence.CursorBoundary{Line: 1, SourceHash: firstHash}) ||
		recovered.AcceptedPackets != 1 || recovered.PayloadState != PayloadApplyRecovery {
		t.Fatalf("interrupted recovery lost state: job=%#v disposition=%q err=%v", recovered, disposition, err)
	}
	_ = revision
}

func TestCancelRequestTransitionMatrixAndDoubleClickAreIdempotent(t *testing.T) {
	t.Run("queued without commit terminalizes once", func(t *testing.T) {
		fixture := newWorkerFixture(t, nil)
		at := fixture.now.Add(time.Minute)
		first, firstRevision, err := RequestCancel(fixture.store, fixture.job.ID, at)
		if err != nil || first.State != Cancelled || first.Error.Code != AgentCancelled || firstRevision != 2 {
			t.Fatalf("first cancellation=%#v revision=%d err=%v", first, firstRevision, err)
		}
		second, secondRevision, err := RequestCancel(fixture.store, fixture.job.ID, at.Add(time.Minute))
		if err != nil || secondRevision != firstRevision || !reflect.DeepEqual(second, first) {
			t.Fatalf("double cancellation=%#v revision=%d err=%v", second, secondRevision, err)
		}
	})

	for _, state := range []State{Running, Retrying, CancelRequested} {
		t.Run(string(state), func(t *testing.T) {
			fixture := newWorkerFixture(t, nil)
			job, revision, found, err := fixture.store.Load(fixture.job.ID)
			if err != nil || !found {
				t.Fatalf("load: found=%v err=%v", found, err)
			}
			job, revision, err = fixture.store.Update(job.ID, revision, func(job *Job) error {
				job.State = state
				job.Phase = Preflight
				job.StartedAt = fixture.now
				if state == CancelRequested {
					job.CancellationRequested = fixture.now
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			got, gotRevision, err := RequestCancel(fixture.store, fixture.job.ID, fixture.now.Add(time.Minute))
			if err != nil || got.State != CancelRequested {
				t.Fatalf("RequestCancel(%s)=%#v revision=%d err=%v", state, got, gotRevision, err)
			}
			wantRevision := revision
			if state != CancelRequested {
				wantRevision++
			}
			if gotRevision != wantRevision {
				t.Fatalf("RequestCancel(%s) revision=%d want=%d", state, gotRevision, wantRevision)
			}
		})
	}

	for _, state := range []State{Completed, Failed} {
		t.Run("reject "+string(state), func(t *testing.T) {
			fixture := newWorkerFixture(t, nil)
			job, revision, _, err := fixture.store.Load(fixture.job.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = fixture.store.Update(job.ID, revision, func(job *Job) error {
				job.State, job.Phase = state, ""
				job.CompletedAt = fixture.now
				if state == Failed {
					job.Error = SafeError{Code: ProposalRejected}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := RequestCancel(fixture.store, fixture.job.ID, fixture.now.Add(time.Minute)); err == nil {
				t.Fatalf("RequestCancel accepted terminal %s", state)
			}
		})
	}
}

func TestRetryTransitionRejectsNonFailedTerminalAndPreservesAttemptOnStaleClicks(t *testing.T) {
	fixture := newWorkerFixture(t, nil)
	if _, _, err := RequestRetry(fixture.store, fixture.job.ID, fixture.now.Add(time.Minute)); err == nil {
		t.Fatal("RequestRetry accepted queued job")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.Attempt != 1 || job.State != Queued {
		t.Fatalf("rejected retry mutated job=%#v found=%v err=%v", job, found, err)
	}
	job, revision, err := fixture.store.Update(job.ID, 1, func(job *Job) error {
		job.State = Running
		job.StartedAt = fixture.now
		job.Owner = Owner{ID: "first-attempt-owner", AcquiredAt: fixture.now}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := RequestRetry(fixture.store, fixture.job.ID, fixture.now.Add(2*time.Minute)); err == nil {
		t.Fatal("RequestRetry accepted a first-attempt running job as a double click")
	}
	stored, storedRevision, _, err := fixture.store.Load(fixture.job.ID)
	if err != nil || storedRevision != revision || !reflect.DeepEqual(stored, job) {
		t.Fatalf("stale running retry mutated job=%#v revision=%d err=%v", stored, storedRevision, err)
	}
}

func mustMarshalProposal(t *testing.T, value interface{}) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
