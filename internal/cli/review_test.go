package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/reviewjob"
)

func TestRunReviewHelpDocumentsOnlyPublicCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"review", "--help"}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, command := range []string{
		"review agent verify --executable ABSOLUTE_PATH --json",
		"review start --project-id ID --agent-executable ABSOLUTE_PATH --json",
		"review status --project-id ID --json",
		"review cancel --job-id ID --json",
		"review retry --job-id ID --agent-executable ABSOLUTE_PATH --expected-attempt N --expected-revision N --json",
	} {
		if !strings.Contains(out.String(), command) {
			t.Fatalf("help=%q missing %q", out.String(), command)
		}
	}
	if strings.Contains(out.String(), "review worker") || strings.Contains(out.String(), "launch-token") {
		t.Fatalf("public help exposed private worker contract: %q", out.String())
	}
}

func TestRunReviewRejectsInvalidPublicArgvWithoutJSON(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "agent")
	tests := [][]string{
		{"review"},
		{"review", "unknown", "--json"},
		{"review", "agent", "--json"},
		{"review", "agent", "verify", "--executable", absolute},
		{"review", "agent", "verify", "--executable", absolute, "--executable", absolute, "--json"},
		{"review", "agent", "verify", "--json", "--executable", "relative"},
		{"review", "status", "--project-id", "Project With Spaces", "--json"},
		{"review", "status", "--project-id", "project-1", "--json", "extra"},
		{"review", "status", "--", "--project-id", "project-1", "--json"},
		{"review", "cancel", "--job-id", "../job", "--json"},
		{"review", "retry", "--job-id", "job-1", "--agent-executable", absolute, "--expected-attempt", "NaN", "--expected-revision", "1", "--json"},
		{"review", "retry", "--job-id", "job-1", "--agent-executable", absolute, "--expected-attempt", "+1", "--expected-revision", "1", "--json"},
		{"review", "retry", "--job-id", "job-1", "--agent-executable", absolute, "--expected-attempt", "01", "--expected-revision", "1", "--json"},
		{"review", "retry", "--job-id", "job-1", "--agent-executable", absolute, "--expected-attempt", "1", "--expected-revision", "0", "--json"},
		{"review", "start", "--project-id", "project-1", "--agent-executable", absolute, "--json", "--extra"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := Run(args, &out, &errOut)
			if code != 2 || out.Len() != 0 || errOut.Len() == 0 {
				t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
			}
		})
	}
}

func TestRunReviewAgentVerifyFailureIsOneSafeJSONObject(t *testing.T) {
	home := t.TempDir()
	setCurrentEnv(t, platform.Env{GOOS: "darwin", Home: home})
	canary := filepath.Join(home, "private-canary-agent")
	var out, errOut bytes.Buffer
	code := Run([]string{"review", "agent", "verify", "--json", "--executable", canary}, &out, &errOut)
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var response struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Compatible    bool   `json:"compatible"`
		ErrorCode     string `json:"error_code"`
	}
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if decoder.More() || response.SchemaVersion != 1 || response.Kind != "codex" || response.Compatible || response.ErrorCode != "E_AGENT_UNCONFIGURED" {
		t.Fatalf("response=%+v stdout=%q", response, out.String())
	}
	if strings.Contains(out.String(), canary) || len(out.Bytes()) > 4096 {
		t.Fatalf("unsafe or unbounded output: %q", out.String())
	}
}

func TestRunReviewAgentVerifyProjectsCompatibleAuthAndUnsupportedResults(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "codex")
	canary := filepath.Join(t.TempDir(), "private-verify-canary")
	t.Cleanup(resetReviewCLISeams)
	tests := []struct {
		name       string
		verified   reviewVerifiedAgent
		err        error
		code       int
		compatible bool
		version    string
		errorCode  string
	}{
		{name: "compatible", verified: reviewVerifiedAgent{Agent: reviewjob.VerifiedAgent{Kind: "codex", Version: "0.148.0"}}, compatible: true, version: "0.148.0"},
		{name: "auth", err: agent.NewError(agent.CodeAuth, errors.New(canary)), code: 1, errorCode: string(agent.CodeAuth)},
		{name: "unsupported", err: agent.NewError(agent.CodeIncompatible, errors.New(canary)), code: 1, errorCode: string(agent.CodeIncompatible)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) { return test.verified, test.err }
			var out, errOut bytes.Buffer
			code := Run([]string{"review", "agent", "verify", "--executable", executable, "--json"}, &out, &errOut)
			if code != test.code || errOut.Len() != 0 || strings.Contains(out.String(), canary) || len(out.Bytes()) > maxReviewPublicJSONBytes {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			var response reviewVerifyResponse
			decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&response); err != nil {
				t.Fatal(err)
			}
			if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
				t.Fatalf("verify emitted more than one JSON object: %v", err)
			}
			if response.SchemaVersion != 1 || response.Kind != "codex" || response.Compatible != test.compatible || response.Version != test.version || response.ErrorCode != test.errorCode {
				t.Fatalf("response=%#v", response)
			}
		})
	}
}

func TestRunReviewAcceptsDigitLeadingStableIDsAsOperationalInput(t *testing.T) {
	home := t.TempDir()
	setCurrentEnv(t, platform.Env{GOOS: "darwin", Home: home})
	var out, errOut bytes.Buffer
	code := Run([]string{"review", "status", "--project-id", "1-project", "--json"}, &out, &errOut)
	if code != 1 || errOut.Len() != 0 || out.Len() == 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.ProjectID != "1-project" || status.State != reviewjob.Idle {
		t.Fatalf("status=%#v", status)
	}
}

func TestRunReviewStartPersistsFrozenLaunchIntentBeforeSpawn(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	called := make([]string, 0, 3)
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		called = append(called, "verify")
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	reviewFreeze = func(reviewjob.FreezeOptions) ([]reviewjob.FrozenSession, error) {
		called = append(called, "freeze")
		return nil, nil
	}
	reviewLaunch = func(request reviewLaunchRequest) error {
		called = append(called, "launch")
		job, revision, found, err := (reviewjob.Store{Root: fixture.data}).Load(request.JobID)
		if err != nil || !found || job.State != reviewjob.Queued || job.LaunchTokenDigest == "" || job.LaunchIntentAt.IsZero() || request.Token == "" {
			t.Fatalf("durable launch boundary job=%#v revision=%d found=%v err=%v request=%#v", job, revision, found, err, request)
		}
		_, _, err = (reviewjob.Store{Root: fixture.data}).Update(job.ID, revision, func(next *reviewjob.Job) error {
			next.State = reviewjob.Running
			next.Owner = reviewjob.Owner{ID: "owner-fixture", AcquiredAt: fixture.now}
			next.LaunchTokenDigest = ""
			next.LaunchIntentAt = time.Time{}
			return nil
		})
		return err
	}
	t.Cleanup(resetReviewCLISeams)

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "start", "--json", "--agent-executable", fixture.executable, "--project-id", fixture.projectID}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || strings.Join(called, ",") != "verify,freeze,launch" {
		t.Fatalf("code=%d calls=%v stdout=%q stderr=%q", code, called, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.ProjectID != fixture.projectID || status.State != reviewjob.PublicState(reviewjob.Running) || status.JobID == "" || !status.CanCancel {
		t.Fatalf("status=%#v", status)
	}
}

func TestRunReviewStartRepairsPartialPointerCommitAndLaunchesSameJob(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	reviewFreeze = func(reviewjob.FreezeOptions) ([]reviewjob.FrozenSession, error) { return nil, nil }
	createdJobID := ""
	reviewCreate = func(store reviewjob.Store, job reviewjob.Job) (int, error) {
		createdJobID = job.ID
		revision, err := store.Create(job)
		if err != nil {
			return revision, err
		}
		pointer := filepath.Join(fixture.data, "review-jobs", "projects", fixture.projectID+".json")
		if err := os.Remove(pointer); err != nil {
			return revision, err
		}
		return revision, errors.New("injected pointer publication interruption")
	}
	launches := 0
	reviewLaunch = func(request reviewLaunchRequest) error {
		launches++
		if request.JobID != createdJobID {
			t.Fatalf("launch job=%s created=%s", request.JobID, createdJobID)
		}
		store := reviewjob.Store{Root: fixture.data}
		latest, latestRevision, found, err := store.LatestForProject(fixture.projectID)
		if err != nil || !found || latest.ID != createdJobID {
			t.Fatalf("repaired pointer latest=%#v revision=%d found=%v err=%v", latest, latestRevision, found, err)
		}
		_, _, err = store.Update(latest.ID, latestRevision, func(next *reviewjob.Job) error {
			next.State = reviewjob.Running
			next.Owner = reviewjob.Owner{ID: "owner-partial", AcquiredAt: fixture.now}
			next.LaunchTokenDigest = ""
			next.LaunchIntentAt = time.Time{}
			return nil
		})
		return err
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "start", "--project-id", fixture.projectID, "--agent-executable", fixture.executable, "--json"}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || launches != 1 {
		t.Fatalf("code=%d launches=%d stdout=%q stderr=%q", code, launches, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.PublicState(reviewjob.Running) || status.JobID != createdJobID {
		t.Fatalf("status=%#v", status)
	}
}

func TestRunReviewStartAgentVerificationIdentitySwapCannotRepairMissingPointer(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Failed)
	job.Error = reviewjob.SafeError{Code: reviewjob.AgentAuth}
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	pointer := filepath.Join(fixture.data, "review-jobs", "projects", fixture.projectID+".json")
	if err := os.Remove(pointer); err != nil {
		t.Fatal(err)
	}
	dataBefore := snapshotCLITree(t, fixture.data)
	originalProject := fixture.project + "-before-agent-verify"
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		if err := os.Rename(fixture.project, originalProject); err != nil {
			return reviewVerifiedAgent{}, err
		}
		if err := os.Mkdir(fixture.project, 0o700); err != nil {
			return reviewVerifiedAgent{}, err
		}
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	freezeCalls := 0
	reviewFreeze = func(reviewjob.FreezeOptions) ([]reviewjob.FrozenSession, error) {
		freezeCalls++
		return nil, errors.New("identity drift must stop before freeze")
	}
	launchCalls := 0
	reviewLaunch = func(reviewLaunchRequest) error {
		launchCalls++
		return errors.New("identity drift must not launch")
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "start", "--project-id", fixture.projectID, "--agent-executable", fixture.executable, "--json"}, &out, &errOut)
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.Idle || status.ErrorCode != string(reviewjob.ApplyRecovery) {
		t.Fatalf("status=%#v", status)
	}
	if got := snapshotCLITree(t, fixture.data); got != dataBefore {
		t.Fatalf("start repaired the missing pointer with stale Project authority\nbefore:\n%s\nafter:\n%s", dataBefore, got)
	}
	if freezeCalls != 0 || launchCalls != 0 {
		t.Fatalf("freeze=%d launch=%d", freezeCalls, launchCalls)
	}
}

func TestRunReviewConcurrentStartCreatesAndLaunchesOnlyOneActiveJob(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	var createMu sync.Mutex
	var createErrors []error
	reviewCreate = func(store reviewjob.Store, job reviewjob.Job) (int, error) {
		revision, err := store.Create(job)
		if err != nil {
			createMu.Lock()
			createErrors = append(createErrors, err)
			createMu.Unlock()
		}
		return revision, err
	}
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	reviewFreeze = func(reviewjob.FreezeOptions) ([]reviewjob.FrozenSession, error) {
		ready <- struct{}{}
		<-release
		return nil, nil
	}
	var launchMu sync.Mutex
	launches := 0
	reviewLaunch = func(request reviewLaunchRequest) error {
		launchMu.Lock()
		launches++
		launchMu.Unlock()
		store := reviewjob.Store{Root: fixture.data}
		job, revision, found, err := store.Load(request.JobID)
		if err != nil || !found {
			return err
		}
		_, _, err = store.Update(job.ID, revision, func(next *reviewjob.Job) error {
			next.State = reviewjob.Running
			next.Owner = reviewjob.Owner{ID: "owner-concurrent", AcquiredAt: fixture.now}
			next.LaunchTokenDigest = ""
			next.LaunchIntentAt = time.Time{}
			return nil
		})
		return err
	}
	t.Cleanup(resetReviewCLISeams)

	type result struct {
		code   int
		out    bytes.Buffer
		errOut bytes.Buffer
	}
	results := make(chan result, 2)
	args := []string{"review", "start", "--project-id", fixture.projectID, "--agent-executable", fixture.executable, "--json"}
	for range 2 {
		go func() {
			var result result
			result.code = Run(args, &result.out, &result.errOut)
			results <- result
		}()
	}
	<-ready
	<-ready
	close(release)
	first, second := <-results, <-results
	firstStatus, secondStatus := decodeReviewStatus(t, first.out.Bytes()), decodeReviewStatus(t, second.out.Bytes())
	launchMu.Lock()
	launchCount := launches
	launchMu.Unlock()
	createMu.Lock()
	gotCreateErrors := append([]error(nil), createErrors...)
	createMu.Unlock()
	if first.code != 0 || second.code != 0 || first.errOut.Len() != 0 || second.errOut.Len() != 0 ||
		firstStatus.JobID == "" || firstStatus.JobID != secondStatus.JobID || launchCount != 1 {
		t.Fatalf("first=%d/%#v/%q second=%d/%#v/%q launches=%d createErrors=%v", first.code, firstStatus, first.errOut.String(), second.code, secondStatus, second.errOut.String(), launchCount, gotCreateErrors)
	}
}

func TestRunReviewStartRecoversExpiredOwnerlessLaunchBeforeCreatingSuccessor(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	stale := fixture.job(reviewjob.Queued)
	stale.LaunchTokenDigest = digestReviewToken("expired-start-token-with-at-least-32-bytes")
	stale.LaunchIntentAt = time.Now().UTC().Add(-time.Minute).Round(0)
	if _, err := store.Create(stale); err != nil {
		t.Fatal(err)
	}
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	reviewFreeze = func(reviewjob.FreezeOptions) ([]reviewjob.FrozenSession, error) { return nil, nil }
	launches := 0
	reviewLaunch = func(request reviewLaunchRequest) error {
		launches++
		if request.JobID == stale.ID {
			t.Fatal("start relaunched the interrupted job")
		}
		job, revision, found, err := store.Load(request.JobID)
		if err != nil || !found {
			return err
		}
		_, _, err = store.Update(job.ID, revision, func(next *reviewjob.Job) error {
			next.State = reviewjob.Running
			next.Owner = reviewjob.Owner{ID: "owner-successor", AcquiredAt: fixture.now.Add(time.Minute)}
			next.UpdatedAt = fixture.now.Add(time.Minute)
			next.LaunchTokenDigest = ""
			next.LaunchIntentAt = time.Time{}
			return nil
		})
		return err
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "start", "--project-id", fixture.projectID, "--agent-executable", fixture.executable, "--json"}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 || launches != 1 {
		t.Fatalf("code=%d launches=%d stdout=%q stderr=%q", code, launches, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.PublicState(reviewjob.Running) || status.JobID == stale.ID {
		t.Fatalf("status=%#v", status)
	}
	recovered, _, found, err := store.Load(stale.ID)
	if err != nil || !found || recovered.State != reviewjob.Failed || recovered.Error.Code != reviewjob.ApplyRecovery {
		t.Fatalf("recovered stale job=%#v found=%v err=%v", recovered, found, err)
	}
}

func TestRunReviewStatusAndRetryBindExactFailedRevision(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Failed)
	job.Error = reviewjob.SafeError{Code: reviewjob.AgentAuth}
	revision, err := store.Create(job)
	if err != nil {
		t.Fatal(err)
	}
	verifyCalls := 0
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		verifyCalls++
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	launches := 0
	reviewLaunch = func(request reviewLaunchRequest) error {
		launches++
		current, currentRevision, found, err := store.Load(request.JobID)
		if err != nil || !found {
			return err
		}
		_, _, err = store.Update(request.JobID, currentRevision, func(next *reviewjob.Job) error {
			next.State = reviewjob.Failed
			next.Phase = ""
			next.CompletedAt = fixture.now.Add(time.Minute)
			next.UpdatedAt = next.CompletedAt
			next.Owner = reviewjob.Owner{}
			next.LaunchTokenDigest = ""
			next.LaunchIntentAt = time.Time{}
			next.Error = reviewjob.SafeError{Code: reviewjob.AgentAuth}
			return nil
		})
		_ = current
		return err
	}
	t.Cleanup(resetReviewCLISeams)

	var out, errOut bytes.Buffer
	if code := Run([]string{"review", "status", "--json", "--project-id", fixture.projectID}, &out, &errOut); code != 0 || errOut.Len() != 0 {
		t.Fatalf("status code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if !status.CanRetry || status.RetryExpectedAttempt != 1 || status.RetryExpectedRevision != revision {
		t.Fatalf("status=%#v", status)
	}

	out.Reset()
	args := []string{"review", "retry", "--json", "--job-id", job.ID, "--agent-executable", fixture.executable,
		"--expected-attempt", "1", "--expected-revision", "1"}
	if code := Run(args, &out, &errOut); code != 0 || errOut.Len() != 0 {
		t.Fatalf("retry code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	first := decodeReviewStatus(t, out.Bytes())
	if first.Attempt != 2 || launches != 1 || !first.CanRetry {
		t.Fatalf("first retry=%#v launches=%d", first, launches)
	}

	// A delayed transport duplicate carries the old attempt/revision and must
	// return the current second failure without creating attempt three or
	// touching an Agent executable that has since disappeared.
	if err := os.Remove(fixture.executable); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run(args, &out, &errOut); code != 0 || errOut.Len() != 0 {
		t.Fatalf("duplicate code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	duplicate := decodeReviewStatus(t, out.Bytes())
	if duplicate.Attempt != 2 || launches != 1 || verifyCalls != 1 {
		t.Fatalf("duplicate=%#v launches=%d verifyCalls=%d", duplicate, launches, verifyCalls)
	}
	if err := os.WriteFile(fixture.executable, []byte("replacement Agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run(args, &out, &errOut); code != 0 || errOut.Len() != 0 {
		t.Fatalf("duplicate after replacement code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	replaced := decodeReviewStatus(t, out.Bytes())
	if replaced.Attempt != 2 || launches != 1 || verifyCalls != 1 {
		t.Fatalf("duplicate after replacement=%#v launches=%d verifyCalls=%d", replaced, launches, verifyCalls)
	}
}

func TestRunReviewRetryPreparationFailuresLeaveOriginalFailureRetryable(t *testing.T) {
	for _, test := range []struct {
		failure   string
		errorCode string
	}{{"current binary", string(reviewjob.AgentUnconfigured)}, {"launch authority", string(reviewjob.ApplyRecovery)}} {
		t.Run(test.failure, func(t *testing.T) {
			fixture := newReviewCLIFixture(t)
			store := reviewjob.Store{Root: fixture.data}
			job := fixture.job(reviewjob.Failed)
			job.Error = reviewjob.SafeError{Code: reviewjob.AgentAuth}
			if _, err := store.Create(job); err != nil {
				t.Fatal(err)
			}
			reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
				return reviewVerifiedAgent{Agent: fixture.agent}, nil
			}
			if test.failure == "current binary" {
				reviewCurrentExecutable = func() (string, error) { return "", errors.New("injected current binary failure") }
			} else {
				reviewAuthority = func() (string, string, error) {
					return "", "", errors.New("injected launch authority failure")
				}
			}

			var out, errOut bytes.Buffer
			code := Run([]string{"review", "retry", "--job-id", job.ID, "--agent-executable", fixture.executable,
				"--expected-attempt", "1", "--expected-revision", "1", "--json"}, &out, &errOut)
			if code != 1 || errOut.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			status := decodeReviewStatus(t, out.Bytes())
			if status.State != reviewjob.Idle || status.ErrorCode != test.errorCode {
				t.Fatalf("status=%#v", status)
			}
			stored, revision, found, err := store.Load(job.ID)
			if err != nil || !found || revision != 1 || stored.State != reviewjob.Failed || stored.Attempt != 1 {
				t.Fatalf("stored=%#v revision=%d found=%v err=%v", stored, revision, found, err)
			}
		})
	}
}

func TestRunReviewRetryRecoversDeadWorkerBeforePreflight(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Running)
	job.Owner = reviewjob.Owner{ID: "owner-crashed", AcquiredAt: fixture.now}
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	verifyCalls := 0
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		verifyCalls++
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	t.Cleanup(resetReviewCLISeams)

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "retry", "--job-id", job.ID, "--agent-executable", fixture.executable,
		"--expected-attempt", "1", "--expected-revision", "1", "--json"}, &out, &errOut)
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.PublicState(reviewjob.Failed) || status.ErrorCode != string(reviewjob.ApplyRecovery) || !status.CanRetry || verifyCalls != 0 {
		t.Fatalf("status=%#v verifyCalls=%d", status, verifyCalls)
	}
}

func TestRunReviewCommandsRejectProjectIdentitySwapWithoutStoreMutation(t *testing.T) {
	for _, command := range []string{"status", "cancel", "start", "retry"} {
		t.Run(command, func(t *testing.T) {
			fixture := newReviewCLIFixture(t)
			store := reviewjob.Store{Root: fixture.data}
			job := fixture.job(reviewjob.Retrying)
			job.Attempt = 2
			job.RetryRequestID = deterministicRetryRequestID(job.ID, 1, 1)
			job.RetryAttempt = 1
			job.RetryRevision = 1
			token := "identity-swap-review-token-with-at-least-32-bytes"
			job.LaunchTokenDigest = digestReviewToken(token)
			job.LaunchIntentAt = time.Now().UTC().Add(-time.Minute).Round(0)
			if _, err := store.Create(job); err != nil {
				t.Fatal(err)
			}
			pointer := filepath.Join(fixture.data, "review-jobs", "projects", fixture.projectID+".json")
			if err := os.Remove(pointer); err != nil {
				t.Fatal(err)
			}
			dataBefore := snapshotCLITree(t, fixture.data)

			originalProject := fixture.project + "-original"
			if err := os.Rename(fixture.project, originalProject); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(fixture.project, 0o700); err != nil {
				t.Fatal(err)
			}
			replacementBefore := snapshotCLITree(t, fixture.project)
			verifyCalls, freezeCalls, launchCalls := 0, 0, 0
			reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
				verifyCalls++
				return reviewVerifiedAgent{Agent: fixture.agent}, nil
			}
			reviewFreeze = func(reviewjob.FreezeOptions) ([]reviewjob.FrozenSession, error) {
				freezeCalls++
				return nil, nil
			}
			reviewLaunch = func(reviewLaunchRequest) error {
				launchCalls++
				return errors.New("identity swap test must not launch")
			}

			args := map[string][]string{
				"status": {"review", "status", "--project-id", fixture.projectID, "--json"},
				"cancel": {"review", "cancel", "--job-id", job.ID, "--json"},
				"start":  {"review", "start", "--project-id", fixture.projectID, "--agent-executable", fixture.executable, "--json"},
				"retry": {"review", "retry", "--job-id", job.ID, "--agent-executable", fixture.executable,
					"--expected-attempt", "1", "--expected-revision", "1", "--json"},
			}[command]
			var out, errOut bytes.Buffer
			code := Run(args, &out, &errOut)
			if code != 1 || errOut.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			status := decodeReviewStatus(t, out.Bytes())
			if status.State != reviewjob.Idle || status.ErrorCode != string(reviewjob.ApplyRecovery) {
				t.Fatalf("status=%#v", status)
			}
			if got := snapshotCLITree(t, fixture.data); got != dataBefore {
				t.Fatalf("%s mutated review store after project identity swap\nbefore:\n%s\nafter:\n%s", command, dataBefore, got)
			}
			if got := snapshotCLITree(t, fixture.project); got != replacementBefore {
				t.Fatalf("%s mutated replacement project\nbefore:\n%s\nafter:\n%s", command, replacementBefore, got)
			}
			stored, revision, found, err := store.Load(job.ID)
			if err != nil || !found || revision != 1 || stored.State != reviewjob.Retrying ||
				stored.LaunchTokenDigest != digestReviewToken(token) || stored.LaunchIntentAt.IsZero() {
				t.Fatalf("stored=%#v revision=%d found=%v err=%v", stored, revision, found, err)
			}
			if freezeCalls != 0 || launchCalls != 0 || (command == "retry" && verifyCalls != 0) {
				t.Fatalf("command=%s verify=%d freeze=%d launch=%d", command, verifyCalls, freezeCalls, launchCalls)
			}
		})
	}
}

func TestRunReviewConcurrentRetryLaunchesOnlyWinningAuthority(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Failed)
	job.Error = reviewjob.SafeError{Code: reviewjob.AgentAuth}
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	verified := make(chan struct{}, 2)
	release := make(chan struct{})
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		verified <- struct{}{}
		<-release
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	var authorityMu sync.Mutex
	authorityCount := 0
	reviewAuthority = func() (string, string, error) {
		authorityMu.Lock()
		defer authorityMu.Unlock()
		authorityCount++
		return fmt.Sprintf("unused-job-%d", authorityCount), fmt.Sprintf("private-retry-token-%032d", authorityCount), nil
	}
	var launchMu sync.Mutex
	launches := 0
	reviewLaunch = func(request reviewLaunchRequest) error {
		launchMu.Lock()
		launches++
		launchMu.Unlock()
		current, revision, found, err := store.Load(request.JobID)
		if err != nil || !found {
			return err
		}
		_, _, err = store.Update(current.ID, revision, func(next *reviewjob.Job) error {
			next.State = reviewjob.Running
			next.Owner = reviewjob.Owner{ID: "owner-retry-winner", AcquiredAt: fixture.now}
			next.LaunchTokenDigest = ""
			next.LaunchIntentAt = time.Time{}
			return nil
		})
		return err
	}
	t.Cleanup(resetReviewCLISeams)

	type result struct {
		code   int
		out    bytes.Buffer
		errOut bytes.Buffer
	}
	results := make(chan result, 2)
	args := []string{"review", "retry", "--job-id", job.ID, "--agent-executable", fixture.executable,
		"--expected-attempt", "1", "--expected-revision", "1", "--json"}
	for range 2 {
		go func() {
			var got result
			got.code = Run(args, &got.out, &got.errOut)
			results <- got
		}()
	}
	<-verified
	<-verified
	close(release)
	first, second := <-results, <-results
	firstStatus, secondStatus := decodeReviewStatus(t, first.out.Bytes()), decodeReviewStatus(t, second.out.Bytes())
	launchMu.Lock()
	launchCount := launches
	launchMu.Unlock()
	if first.code != 0 || second.code != 0 || first.errOut.Len() != 0 || second.errOut.Len() != 0 ||
		firstStatus.JobID != job.ID || secondStatus.JobID != job.ID || firstStatus.Attempt != 2 || secondStatus.Attempt != 2 || launchCount != 1 {
		t.Fatalf("first=%d/%#v/%q second=%d/%#v/%q launches=%d", first.code, firstStatus, first.errOut.String(), second.code, secondStatus, second.errOut.String(), launchCount)
	}
}

func TestRunReviewCancelIsDurableAndIdempotent(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Queued)
	job.LaunchTokenDigest = digestReviewToken("private-cancel-token-with-at-least-32-bytes")
	job.LaunchIntentAt = fixture.now
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetReviewCLISeams)
	args := []string{"review", "cancel", "--json", "--job-id", job.ID}
	for attempt := 0; attempt < 2; attempt++ {
		var out, errOut bytes.Buffer
		if code := Run(args, &out, &errOut); code != 0 || errOut.Len() != 0 {
			t.Fatalf("cancel %d code=%d stdout=%q stderr=%q", attempt, code, out.String(), errOut.String())
		}
		status := decodeReviewStatus(t, out.Bytes())
		if status.State != reviewjob.PublicState(reviewjob.Cancelled) || status.CanCancel || status.CanRetry {
			t.Fatalf("cancel %d status=%#v", attempt, status)
		}
	}
	stored, _, found, err := store.Load(job.ID)
	if err != nil || !found || stored.LaunchTokenDigest != "" || !stored.LaunchIntentAt.IsZero() || stored.Owner.ID != "" {
		t.Fatalf("cancelled job=%#v found=%v err=%v", stored, found, err)
	}
}

func TestRunReviewCancelRecoversDeadWorkerBeforeTransition(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Running)
	job.Owner = reviewjob.Owner{ID: "owner-crashed", AcquiredAt: fixture.now}
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "cancel", "--job-id", job.ID, "--json"}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.PublicState(reviewjob.Failed) || status.ErrorCode != string(reviewjob.ApplyRecovery) || !status.CanRetry {
		t.Fatalf("status=%#v", status)
	}
}

func TestRunReviewLaunchFailureTerminalizesWithoutPublicLeak(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	canary := filepath.Join(fixture.project, "private-launch-canary")
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	reviewFreeze = func(reviewjob.FreezeOptions) ([]reviewjob.FrozenSession, error) { return nil, nil }
	reviewLaunch = func(reviewLaunchRequest) error { return fmt.Errorf("launch failed at %s", canary) }
	t.Cleanup(resetReviewCLISeams)

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "start", "--project-id", fixture.projectID, "--agent-executable", fixture.executable, "--json"}, &out, &errOut)
	if code != 1 || errOut.Len() != 0 || strings.Contains(out.String(), canary) || strings.Contains(out.String(), "launch failed") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.PublicState(reviewjob.Failed) || status.ErrorCode != string(reviewjob.ApplyRecovery) || !status.CanRetry || status.RetryExpectedRevision <= 1 {
		t.Fatalf("failed status=%#v", status)
	}
	stored, _, found, err := (reviewjob.Store{Root: fixture.data}).Load(status.JobID)
	if err != nil || !found || stored.Owner.ID != "" || stored.LaunchTokenDigest != "" || !stored.LaunchIntentAt.IsZero() || stored.Phase != "" {
		t.Fatalf("failed job=%#v found=%v err=%v", stored, found, err)
	}
}

func TestRunReviewStartDataSwapDuringLaunchFailureTerminalizesPinnedStore(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	reviewFreeze = func(reviewjob.FreezeOptions) ([]reviewjob.FrozenSession, error) { return nil, nil }
	originalData := fixture.data + "-launch-original"
	replacementBefore := ""
	reviewLaunch = func(reviewLaunchRequest) error {
		if err := os.Rename(fixture.data, originalData); err != nil {
			return err
		}
		if err := os.Mkdir(fixture.data, 0o700); err != nil {
			return err
		}
		replacementBefore = snapshotCLITree(t, fixture.data)
		return errors.New("detached worker rejected the swapped Data namespace")
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "start", "--project-id", fixture.projectID, "--agent-executable", fixture.executable, "--json"}, &out, &errOut)
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.PublicState(reviewjob.Failed) || status.ErrorCode != string(reviewjob.ApplyRecovery) || status.JobID == "" || !status.CanRetry {
		t.Fatalf("status=%#v", status)
	}
	stored, _, found, err := (reviewjob.Store{Root: originalData}).Load(status.JobID)
	if err != nil || !found || stored.State != reviewjob.Failed || stored.Owner.ID != "" || stored.LaunchTokenDigest != "" || !stored.LaunchIntentAt.IsZero() {
		t.Fatalf("original Data job=%#v found=%v err=%v", stored, found, err)
	}
	if got := snapshotCLITree(t, fixture.data); got != replacementBefore {
		t.Fatalf("launch cleanup wrote the replacement Data root\nbefore:\n%s\nafter:\n%s", replacementBefore, got)
	}
}

func TestRunReviewRetryDataSwapDuringLaunchFailureTerminalizesPinnedStore(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Failed)
	job.Error = reviewjob.SafeError{Code: reviewjob.AgentAuth}
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	originalData := fixture.data + "-retry-launch-original"
	replacementBefore := ""
	reviewLaunch = func(reviewLaunchRequest) error {
		if err := os.Rename(fixture.data, originalData); err != nil {
			return err
		}
		if err := os.Mkdir(fixture.data, 0o700); err != nil {
			return err
		}
		replacementBefore = snapshotCLITree(t, fixture.data)
		return errors.New("detached retry worker rejected the swapped Data namespace")
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "retry", "--job-id", job.ID, "--agent-executable", fixture.executable,
		"--expected-attempt", "1", "--expected-revision", "1", "--json"}, &out, &errOut)
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.PublicState(reviewjob.Failed) || status.ErrorCode != string(reviewjob.ApplyRecovery) ||
		status.JobID != job.ID || status.Attempt != 2 || !status.CanRetry {
		t.Fatalf("status=%#v", status)
	}
	stored, _, found, err := (reviewjob.Store{Root: originalData}).Load(job.ID)
	if err != nil || !found || stored.State != reviewjob.Failed || stored.Attempt != 2 || stored.Owner.ID != "" ||
		stored.LaunchTokenDigest != "" || !stored.LaunchIntentAt.IsZero() {
		t.Fatalf("original Data job=%#v found=%v err=%v", stored, found, err)
	}
	if got := snapshotCLITree(t, fixture.data); got != replacementBefore {
		t.Fatalf("retry cleanup wrote the replacement Data root\nbefore:\n%s\nafter:\n%s", replacementBefore, got)
	}
}

func TestReviewProjectAuthorityCopiesSharePinnedDataLifetime(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	job := fixture.job(reviewjob.Failed)
	job.Error = reviewjob.SafeError{Code: reviewjob.AgentAuth}
	if _, err := (reviewjob.Store{Root: fixture.data}).Create(job); err != nil {
		t.Fatal(err)
	}
	mapping, err := authenticateReviewMapping(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := pinReviewProjectAuthority(fixture.data, mapping)
	if err != nil {
		t.Fatal(err)
	}
	copyAuthority := authority
	store := copyAuthority.store(false)
	originalData := fixture.data + "-authority-original"
	if err := os.Rename(fixture.data, originalData); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.data, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementBefore := snapshotCLITree(t, fixture.data)
	loaded, _, found, err := store.Load(job.ID)
	if err != nil || !found || loaded.ID != job.ID {
		t.Fatalf("borrowed Store did not clone the pinned Data handle: job=%#v found=%v err=%v", loaded, found, err)
	}
	if err := copyAuthority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatalf("shallow authority copy double-closed the Data handle: %v", err)
	}
	if _, _, _, err := store.Load(job.ID); err == nil {
		t.Fatal("Store reopened the replacement path after its borrowed authority closed")
	}
	if got := snapshotCLITree(t, fixture.data); got != replacementBefore {
		t.Fatalf("closed authority fallback wrote the replacement Data root\nbefore:\n%s\nafter:\n%s", replacementBefore, got)
	}
}

func TestRunReviewWorkerRootAuthenticationFailureTerminalizesOwnedLaunch(t *testing.T) {
	for _, rootKind := range []string{"Project", "Vault"} {
		t.Run(rootKind, func(t *testing.T) {
			fixture := newReviewCLIFixture(t)
			reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
				return reviewVerifiedAgent{Agent: fixture.agent}, nil
			}
			reviewFreeze = func(reviewjob.FreezeOptions) ([]reviewjob.FrozenSession, error) { return nil, nil }
			launchedJobID := ""
			reviewLaunch = func(request reviewLaunchRequest) error {
				launchedJobID = request.JobID
				target := fixture.project
				if rootKind == "Vault" {
					target = fixture.vault
				}
				if err := os.Rename(target, target+"-worker-auth-failure"); err != nil {
					return err
				}
				if err := os.Mkdir(target, 0o700); err != nil {
					return err
				}
				return errors.New("worker rejected changed root authority")
			}

			var out, errOut bytes.Buffer
			code := Run([]string{"review", "start", "--project-id", fixture.projectID, "--agent-executable", fixture.executable, "--json"}, &out, &errOut)
			if code != 1 || errOut.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			status := decodeReviewStatus(t, out.Bytes())
			if status.State != reviewjob.PublicState(reviewjob.Failed) || status.ErrorCode != string(reviewjob.ApplyRecovery) ||
				status.JobID != launchedJobID || !status.CanRetry {
				t.Fatalf("status=%#v launchedJobID=%q", status, launchedJobID)
			}
			stored, _, found, err := (reviewjob.Store{Root: fixture.data}).Load(launchedJobID)
			if err != nil || !found || stored.State != reviewjob.Failed || stored.Owner.ID != "" ||
				stored.LaunchTokenDigest != "" || !stored.LaunchIntentAt.IsZero() {
				t.Fatalf("stored=%#v found=%v err=%v", stored, found, err)
			}
		})
	}
}

func TestRunReviewRetryWorkerRootAuthenticationFailureTerminalizesOwnedLaunch(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Failed)
	job.Error = reviewjob.SafeError{Code: reviewjob.AgentAuth}
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		return reviewVerifiedAgent{Agent: fixture.agent}, nil
	}
	reviewLaunch = func(reviewLaunchRequest) error {
		if err := os.Rename(fixture.project, fixture.project+"-retry-worker-auth-failure"); err != nil {
			return err
		}
		if err := os.Mkdir(fixture.project, 0o700); err != nil {
			return err
		}
		return errors.New("retry worker rejected changed root authority")
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "retry", "--job-id", job.ID, "--agent-executable", fixture.executable,
		"--expected-attempt", "1", "--expected-revision", "1", "--json"}, &out, &errOut)
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.PublicState(reviewjob.Failed) || status.ErrorCode != string(reviewjob.ApplyRecovery) ||
		status.JobID != job.ID || status.Attempt != 2 || !status.CanRetry {
		t.Fatalf("status=%#v", status)
	}
	stored, _, found, err := store.Load(job.ID)
	if err != nil || !found || stored.State != reviewjob.Failed || stored.Attempt != 2 || stored.Owner.ID != "" ||
		stored.LaunchTokenDigest != "" || !stored.LaunchIntentAt.IsZero() {
		t.Fatalf("stored=%#v found=%v err=%v", stored, found, err)
	}
}

func TestReviewProduction0147FailsClosedBeforeJobCreation(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	executable := filepath.Join(t.TempDir(), "fake-codex")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	command := exec.Command("go", "build", "-o", executable, "../agent/codex/testdata/fake-agent.go")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake Codex: %v\n%s", err, output)
	}
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success")
	resetReviewCLISeams()
	t.Cleanup(resetReviewCLISeams)
	var out, errOut bytes.Buffer
	code := Run([]string{"review", "start", "--project-id", fixture.projectID, "--agent-executable", executable, "--json"}, &out, &errOut)
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.Idle || status.ErrorCode != string(reviewjob.AgentIncompatible) || status.JobID != "" {
		t.Fatalf("status=%#v", status)
	}
	entries, err := os.ReadDir(filepath.Join(fixture.data, "review-jobs", "jobs"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("P5 preflight created job entries: %v", entries)
	}
}

func TestRunReviewStatusProjectsCorruptPrivateStoreAsSafeJSON(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Failed)
	job.Error = reviewjob.SafeError{Code: reviewjob.AgentAuth}
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(fixture.project, "private-corrupt-canary")
	jobPath := filepath.Join(fixture.data, "review-jobs", "jobs", job.ID+".json")
	if err := os.WriteFile(jobPath, []byte(`{"private_error":"`+canary+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"review", "status", "--project-id", fixture.projectID, "--json"}, &out, &errOut)
	if code != 1 || errOut.Len() != 0 || strings.Contains(out.String(), canary) || len(out.Bytes()) > maxReviewPublicJSONBytes {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.Idle || status.ErrorCode != string(reviewjob.ApplyRecovery) {
		t.Fatalf("status=%#v", status)
	}
}

func TestRunReviewStatusRecoversUnleasedWorkerBeforeProjection(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Running)
	job.Owner = reviewjob.Owner{ID: "owner-crashed", AcquiredAt: fixture.now}
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"review", "status", "--project-id", fixture.projectID, "--json"}, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	status := decodeReviewStatus(t, out.Bytes())
	if status.State != reviewjob.PublicState(reviewjob.Failed) || status.ErrorCode != string(reviewjob.ApplyRecovery) || !status.CanRetry {
		t.Fatalf("status=%#v", status)
	}
}

func TestRunReviewPrivateWorkerRejectsIncompleteOrUnsafeAuthority(t *testing.T) {
	for _, args := range [][]string{
		{"review", "worker"},
		{"review", "worker", "--job-id", "../job", "--launch-token", strings.Repeat("a", 32), "--" + privateReviewHandshakeFlag(), "3"},
		{"review", "worker", "--job-id", "job-1", "--launch-token", "short", "--" + privateReviewHandshakeFlag(), "3"},
		{"review", "worker", "--job-id", "job-1", "--job-id", "job-1", "--launch-token", strings.Repeat("a", 32), "--" + privateReviewHandshakeFlag(), "3"},
	} {
		var out, errOut bytes.Buffer
		if code := Run(args, &out, &errOut); code != 2 || out.Len() != 0 || errOut.Len() != 0 {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
		}
	}
}

func TestPrivateReviewWorkerRejectsWrongTokenBeforeAgentVerification(t *testing.T) {
	fixture := newReviewCLIFixture(t)
	store := reviewjob.Store{Root: fixture.data}
	job := fixture.job(reviewjob.Queued)
	token := "private-worker-token-with-at-least-32-bytes"
	job.LaunchTokenDigest = digestReviewToken(token)
	job.LaunchIntentAt = fixture.now
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	verifyCalls := 0
	reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
		verifyCalls++
		return reviewVerifiedAgent{}, errors.New("Agent verification must not run")
	}
	t.Cleanup(resetReviewCLISeams)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if err := executePrivateReviewWorker(fixture.data, job.ID, token+"-wrong", write); err == nil || verifyCalls != 0 {
		t.Fatalf("err=%v verifyCalls=%d", err, verifyCalls)
	}
}

func TestPrivateReviewWorkerRootSwapDuringAgentVerifyDoesNotHandshakeOrConsumeAuthority(t *testing.T) {
	for _, rootKind := range []string{"Project", "Vault"} {
		t.Run(rootKind, func(t *testing.T) {
			fixture := newReviewCLIFixture(t)
			store := reviewjob.Store{Root: fixture.data}
			job := fixture.job(reviewjob.Queued)
			token := "private-worker-root-swap-token-with-at-least-32-bytes"
			job.LaunchTokenDigest = digestReviewToken(token)
			job.LaunchIntentAt = fixture.now
			if _, err := store.Create(job); err != nil {
				t.Fatal(err)
			}
			target := fixture.project
			if rootKind == "Vault" {
				target = fixture.vault
			}
			original := target + "-during-agent-verify"
			reviewVerify = func(context.Context, string) (reviewVerifiedAgent, error) {
				if err := os.Rename(target, original); err != nil {
					return reviewVerifiedAgent{}, err
				}
				if err := os.Mkdir(target, 0o700); err != nil {
					return reviewVerifiedAgent{}, err
				}
				return reviewVerifiedAgent{Agent: fixture.agent, Handle: &reviewjob.AgentHandle{}}, nil
			}
			t.Cleanup(resetReviewCLISeams)
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			if err := executePrivateReviewWorker(fixture.data, job.ID, token, write); err == nil {
				t.Fatalf("private worker accepted %s replacement during Agent verification", rootKind)
			}
			if err := write.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				t.Fatal(err)
			}
			handshake, err := io.ReadAll(read)
			if err != nil {
				t.Fatal(err)
			}
			if len(handshake) != 0 {
				t.Fatalf("private worker emitted a success handshake before %s authentication: %v", rootKind, handshake)
			}
			unchanged, _, found, err := store.Load(job.ID)
			if err != nil || !found {
				t.Fatalf("Load() found=%v err=%v", found, err)
			}
			if unchanged.State != reviewjob.Queued || unchanged.Owner.ID != "" || unchanged.LaunchTokenDigest != digestReviewToken(token) ||
				!unchanged.LaunchIntentAt.Equal(fixture.now) {
				t.Fatalf("private entry consumed authority before %s authentication: %#v", rootKind, unchanged)
			}
		})
	}
}

func TestDetachedReviewLauncherHandlesSuccessBadByteEOFAndTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a bounded helper process")
	}
	binary := buildDetachedReviewHelper(t)
	token := strings.Repeat("a", 43)
	tests := []struct {
		jobID  string
		ok     bool
		maxRun time.Duration
	}{
		{jobID: "job-handshake-success", ok: true, maxRun: 2 * time.Second},
		{jobID: "job-handshake-bad", maxRun: 2 * time.Second},
		{jobID: "job-handshake-busy", maxRun: 2 * time.Second},
		{jobID: "job-handshake-eof", maxRun: 2 * time.Second},
		{jobID: "job-handshake-timeout", maxRun: 5 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.jobID, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			t.Setenv("SESSIONREVIEWER_DETACHED_TEST_PID_FILE", pidFile)
			started := time.Now()
			err := launchDetachedReviewWorker(reviewLaunchRequest{Binary: binary, JobID: test.jobID, Token: token})
			if (err == nil) != test.ok {
				t.Fatalf("err=%v want success=%v", err, test.ok)
			}
			if strings.HasSuffix(test.jobID, "busy") && !errors.Is(err, reviewjob.ErrAgentBusy) {
				t.Fatalf("busy handshake err=%v", err)
			}
			if elapsed := time.Since(started); elapsed > test.maxRun {
				t.Fatalf("detached handshake took %s, limit %s", elapsed, test.maxRun)
			}
			assertDetachedHelperReaped(t, pidFile)
		})
	}
}

func TestDetachedReviewInheritancePolicyAllowsOnlyExplicitHandshakeHandle(t *testing.T) {
	policy, err := detachedReviewInheritancePolicy(uintptr(17))
	if err != nil {
		t.Fatal(err)
	}
	if policy.noInheritHandles || len(policy.additionalHandles) != 1 || policy.additionalHandles[0] != uintptr(17) {
		t.Fatalf("inheritance policy=%#v", policy)
	}
	if _, err := detachedReviewInheritancePolicy(0); err == nil {
		t.Fatal("inheritance policy accepted a null handshake handle")
	}
}

func assertDetachedHelperReaped(t *testing.T, pidFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	body, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read detached helper PID: %v", err)
	}
	pid := strings.TrimSpace(string(body))
	deadline := time.Now().Add(time.Second)
	for {
		output, err := exec.Command("ps", "-o", "stat=", "-p", pid).CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached helper %s was not reaped; ps state=%q", pid, output)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func buildDetachedReviewHelper(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	source := filepath.Join(directory, "main.go")
	body := `package main
import ("os"; "strconv"; "strings"; "time")
func main() {
  var job, handle string
  for index := 0; index+1 < len(os.Args); index++ {
    if os.Args[index] == "--job-id" { job = os.Args[index+1] }
    if os.Args[index] == "--handshake-fd" || os.Args[index] == "--handshake-handle" { handle = os.Args[index+1] }
  }
	if path := os.Getenv("SESSIONREVIEWER_DETACHED_TEST_PID_FILE"); path != "" { os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600) }
  os.Stdout.WriteString("stdout-canary")
  os.Stderr.WriteString("stderr-canary")
  if strings.HasSuffix(job, "timeout") { time.Sleep(10*time.Second); return }
  if strings.HasSuffix(job, "eof") { return }
  value, _ := strconv.ParseUint(handle, 10, 64)
  file := os.NewFile(uintptr(value), "handshake")
  if file == nil { os.Exit(3) }
  if strings.HasSuffix(job, "bad") { file.Write([]byte{7}) } else if strings.HasSuffix(job, "busy") { file.Write([]byte{2}) } else { file.Write([]byte{1}) }
  file.Close()
}`
	if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "helper")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	command := exec.Command("go", "build", "-o", binary, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	return binary
}

type reviewCLIFixture struct {
	data, project, vault, executable, projectID string
	agent                                       reviewjob.VerifiedAgent
	now                                         time.Time
}

func newReviewCLIFixture(t *testing.T) reviewCLIFixture {
	t.Helper()
	t.Cleanup(resetReviewCLISeams)
	home := t.TempDir()
	data := filepath.Join(home, ".local", "share", "session-reviewer")
	project := filepath.Join(home, "project")
	vault := filepath.Join(home, "vault")
	vaultReview := filepath.Join(vault, "Projects", "Test", "Session Review")
	for _, directory := range []string{data, project, vault, vaultReview} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(home, "codex")
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	projectID := "project-review-cli"
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: projectID, Root: project, VaultRoot: vault, VaultReviewPath: "Projects/Test/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}); err != nil {
		t.Fatal(err)
	}
	agentFile, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	agentIdentity, err := pathguard.PhysicalFileIdentity(agentFile)
	if closeErr := agentFile.Close(); err != nil || closeErr != nil {
		t.Fatalf("agent identity=%v close=%v", err, closeErr)
	}
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	fixture := reviewCLIFixture{
		data: data, project: project, vault: vault, executable: executable, projectID: projectID, now: now,
		agent: reviewjob.VerifiedAgent{Kind: "codex", Identity: agentIdentity, Version: "fixture", Executable: executable},
	}
	setCurrentEnv(t, platform.Env{GOOS: "darwin", Home: home, SessionReviewerSessionsRoot: filepath.Join(home, "sessions")})
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	reviewNow = func() time.Time { return now }
	return fixture
}

func (fixture reviewCLIFixture) job(state reviewjob.State) reviewjob.Job {
	projectDirectory, _ := pathguard.Open(fixture.project)
	identity, _ := projectDirectory.PhysicalIdentity()
	_ = projectDirectory.Close()
	job := reviewjob.Job{
		SchemaVersion: reviewjob.PublicStatusSchemaVersion,
		ID:            "job-review-cli", ProjectID: fixture.projectID, ProjectIdentity: identity, Agent: fixture.agent,
		State: state, Attempt: 1, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if state == reviewjob.Failed {
		job.CompletedAt = fixture.now
	} else {
		job.Phase = reviewjob.Preflight
	}
	return job
}

func decodeReviewStatus(t *testing.T, body []byte) reviewjob.PublicStatus {
	t.Helper()
	var status reviewjob.PublicStatus
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("review status emitted more than one JSON value: %v", err)
	}
	if err := reviewjob.ValidatePublicStatus(status); err != nil {
		t.Fatalf("invalid public status: %v", err)
	}
	return status
}
