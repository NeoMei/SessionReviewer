package reviewjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

func TestLeaseSameProjectHasExactlyOneCrossProcessWinner(t *testing.T) {
	root := newStoreWithJob(t)
	results := runLeaseContenders(t, root, []string{"project-1", "project-1"})
	assertLeaseResults(t, results)
}

func TestLeaseDifferentProjectsHaveExactlyOneGlobalCrossProcessWinner(t *testing.T) {
	root := newStoreWithJob(t)
	results := runLeaseContenders(t, root, []string{"project-1", "project-2"})
	assertLeaseResults(t, results)
}

func TestLeaseGlobalAcquisitionFailureRollsBackProjectLease(t *testing.T) {
	root := newStoreWithJob(t)
	first, err := (Store{Root: root}).AcquireLeases("project-1", "job-first", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Root: root}).AcquireLeases("project-2", "job-loser", 0); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("second AcquireLeases() = %v, want global busy", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	reacquired, err := (Store{Root: root}).AcquireLeases("project-2", "job-next", 0)
	if err != nil {
		t.Fatalf("project lease leaked after global acquisition failure: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseAbruptProcessExitReleasesKernelOwnership(t *testing.T) {
	root := newStoreWithJob(t)
	exitGate := filepath.Join(t.TempDir(), "exit")
	command, result := startLeaseHelper(t, root, "project-1", "job-child", exitGate)
	waitForLeaseResult(t, result, "acquired")

	if _, err := (Store{Root: root}).AcquireLeases("project-1", "job-parent", 0); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("AcquireLeases() while child is live = %v, want %v", err, ErrAgentBusy)
	}
	if err := os.WriteFile(exitGate, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("lease child exit: %v", err)
	}

	leases, err := (Store{Root: root}).AcquireLeases("project-1", "job-parent", 0)
	if err != nil {
		t.Fatalf("AcquireLeases() after abrupt process exit: %v", err)
	}
	if err := leases.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseStaleMetadataNeverOverridesLiveKernelLock(t *testing.T) {
	root := newStoreWithJob(t)
	exitGate := filepath.Join(t.TempDir(), "exit")
	command, result := startLeaseHelper(t, root, "project-1", "job-child", exitGate)
	waitForLeaseResult(t, result, "acquired")

	stale := []byte("{\n  \"schema_version\": 1,\n  \"job_id\": \"stale-job\",\n  \"pid\": 1,\n  \"process_start_token\": \"stale-token\",\n  \"acquired_at\": \"2000-01-01T00:00:00Z\"\n}\n")
	for _, name := range []string{"review-jobs/locks/projects/project-1.lock", "review-jobs/locks/global.lock"} {
		if err := os.WriteFile(filepath.Join(root, name), stale, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (Store{Root: root}).AcquireLeases("project-1", "job-parent", 0); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("AcquireLeases() trusted stale metadata over live lock: %v", err)
	}
	job, revision, disposition, err := (Store{Root: root}).RecoverInterrupted("job-1")
	if err != nil || disposition != RecoveryNotInterrupted || revision != 1 || job.State != Running {
		t.Fatalf("RecoverInterrupted() trusted stale metadata over live lock = %#v, %d, %q, %v", job, revision, disposition, err)
	}

	if err := os.WriteFile(exitGate, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("lease child exit: %v", err)
	}
}

func TestLeaseMetadataUsesExactPrivateStablePaths(t *testing.T) {
	root := newStoreWithJob(t)
	leases, err := (Store{Root: root}).AcquireLeases("project-1", "job-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer leases.Release()

	var owners []leaseOwner
	for _, name := range []string{"review-jobs/locks/projects/project-1.lock", "review-jobs/locks/global.lock"} {
		path := filepath.Join(root, name)
		assertMode(t, path, 0o600)
		var owner leaseOwner
		if err := json.Unmarshal(readFile(t, path), &owner); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if owner.SchemaVersion != 1 || owner.JobID != "job-1" || owner.PID != os.Getpid() || owner.ProcessStartToken == "" || owner.AcquiredAt.IsZero() {
			t.Fatalf("%s metadata = %#v", name, owner)
		}
		owners = append(owners, owner)
	}
	if owners[0].ProcessStartToken != owners[1].ProcessStartToken {
		t.Fatalf("project/global process tokens differ: %#v", owners)
	}
}

func TestLeaseReleaseIsIdempotentAndBothLeasesAreReacquirable(t *testing.T) {
	root := newStoreWithJob(t)
	leases, err := (Store{Root: root}).AcquireLeases("project-1", "job-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	errorsOut := make(chan error, 8)
	var wait sync.WaitGroup
	for index := 0; index < cap(errorsOut); index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsOut <- leases.Release()
		}()
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatalf("concurrent idempotent Release() = %v", err)
		}
	}

	reacquired, err := (Store{Root: root}).AcquireLeases("project-1", "job-2", 0)
	if err != nil {
		t.Fatalf("AcquireLeases() after release: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatal(err)
	}
	var nilLeases *LeaseSet
	if err := nilLeases.Release(); err != nil {
		t.Fatalf("nil Release() = %v", err)
	}
}

func TestLeaseReleaseDropsGlobalBeforeProject(t *testing.T) {
	root := newStoreWithJob(t)
	leases, err := (Store{Root: root}).AcquireLeases("project-1", "job-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Pause the first (global) leaf release. While Release is paused, the
	// project leaf must still be kernel-owned; swapping release order breaks
	// this observable contract.
	global := leases.global
	global.mu.Lock()
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- leases.Release() }()
	deadline := time.Now().Add(5 * time.Second)
	releaseStarted := false
	for time.Now().Before(deadline) {
		if !leases.mu.TryLock() {
			releaseStarted = true
			break
		}
		leases.mu.Unlock()
		time.Sleep(time.Millisecond)
	}

	layout, openErr := (Store{Root: root}).openLayout(false)
	var probeErr error
	if openErr == nil {
		var probe *storeFileLock
		probe, probeErr = acquirePrivateFileLock(layout.projectLocks, "project-1.lock", 0)
		if probe != nil {
			_ = probe.release()
		}
		openErr = layout.finish()
	}
	global.mu.Unlock()
	releaseErr := <-releaseDone
	if !releaseStarted {
		t.Fatal("Release() did not begin")
	}
	if openErr != nil {
		t.Fatal(openErr)
	}
	if !errors.Is(probeErr, errPrivateFileLocked) {
		t.Fatalf("project lease was released before global lease: %v", probeErr)
	}
	if releaseErr != nil {
		t.Fatal(releaseErr)
	}
}

func TestLeaseStoreCASCoexistsWhileBothWorkerLeasesAreHeld(t *testing.T) {
	root := newStoreWithJob(t)
	leases, err := (Store{Root: root}).AcquireLeases("project-1", "job-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer leases.Release()

	updated, revision, err := (Store{Root: root}).Update("job-1", 1, func(job *Job) error {
		job.AcceptedPackets = 1
		return nil
	})
	if err != nil || revision != 2 || updated.AcceptedPackets != 1 {
		t.Fatalf("Update() while both leases held = %#v, %d, %v", updated, revision, err)
	}
}

func TestLeaseRejectsRedirectsCaseCollisionsAndWeakModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink, case-sensitive namespace, and mode assertions")
	}
	t.Run("project symlink", func(t *testing.T) {
		root := newStoreWithJob(t)
		path := filepath.Join(root, "review-jobs/locks/projects/project-1.lock")
		if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
			t.Fatal(err)
		}
		if _, err := (Store{Root: root}).AcquireLeases("project-1", "job-1", 0); err == nil {
			t.Fatal("AcquireLeases() accepted redirected project lock")
		}
	})
	t.Run("global case collision", func(t *testing.T) {
		root := newStoreWithJob(t)
		path := filepath.Join(root, "review-jobs/locks/GLOBAL.lock")
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (Store{Root: root}).AcquireLeases("project-1", "job-1", 0); err == nil {
			t.Fatal("AcquireLeases() accepted case-colliding global lock")
		}
	})
	t.Run("weak global mode", func(t *testing.T) {
		root := newStoreWithJob(t)
		path := filepath.Join(root, "review-jobs/locks/global.lock")
		if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := (Store{Root: root}).AcquireLeases("project-1", "job-1", 0); err == nil {
			t.Fatal("AcquireLeases() accepted weak global lock")
		}
	})
}

func TestInterruptedLeaseBackedJobBecomesApplyRecoveryWithoutInferringReceiptState(t *testing.T) {
	for _, state := range []State{Running, CancelRequested} {
		t.Run(string(state), func(t *testing.T) {
			root := newStoreWithJob(t)
			store := Store{Root: root}
			if _, _, err := store.Update("job-1", 1, func(job *Job) error {
				job.State = state
				job.Phase = Applying
				job.PacketDigest = "sha256:" + strings.Repeat("b", 64)
				if state == CancelRequested {
					job.CancellationRequested = job.UpdatedAt
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			recovered, revision, disposition, err := store.RecoverInterrupted("job-1")
			if err != nil || disposition != RecoveryApplyInspectionNeeded || revision != 3 {
				t.Fatalf("RecoverInterrupted() = %#v, %d, %q, %v", recovered, revision, disposition, err)
			}
			if recovered.State != Failed || recovered.Phase != "" || recovered.Error.Code != ApplyRecovery || recovered.Owner.ID != "" || recovered.CompletedAt.IsZero() {
				t.Fatalf("recovered job classification = %#v", recovered)
			}
			if recovered.PacketDigest == "" || recovered.AcceptedPackets != 0 || recovered.AcceptedSessions != 0 {
				t.Fatalf("recovery inferred or discarded apply-boundary evidence: %#v", recovered)
			}
		})
	}
}

func TestInterruptedRecoveryUsesOnePinnedStoreAcrossDataReplacement(t *testing.T) {
	for _, replaceAtRead := range []int{1, 2} {
		name := "before_lease"
		if replaceAtRead == 2 {
			name = "after_lease"
		}
		t.Run(name, func(t *testing.T) {
			root := newStoreWithJob(t)
			decoy := newStoreWithJob(t)
			decoyStore := Store{Root: decoy}
			if _, _, err := decoyStore.Update("job-1", 1, func(job *Job) error {
				job.PrivateError = "decoy must remain byte exact"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			decoyPrimary := readFile(t, filepath.Join(decoy, "review-jobs/jobs/job-1.json"))
			decoyBackup := readFile(t, filepath.Join(decoy, "review-jobs/jobs/job-1.json.bak"))
			pinnedRoot := root + ".pinned"
			reads := 0
			replaced := false
			store := Store{Root: root, afterJobRead: func() error {
				reads++
				if reads != replaceAtRead || replaced {
					return nil
				}
				replaced = true
				if err := os.Rename(root, pinnedRoot); err != nil {
					return err
				}
				return os.Rename(decoy, root)
			}}

			recovered, revision, disposition, err := store.RecoverInterrupted("job-1")
			if err != nil || !replaced || disposition != RecoveryApplyInspectionNeeded || revision != 2 || recovered.State != Failed {
				t.Fatalf("RecoverInterrupted() = %#v, %d, %q, %v; replaced=%v reads=%d", recovered, revision, disposition, err, replaced, reads)
			}
			if got := readFile(t, filepath.Join(root, "review-jobs/jobs/job-1.json")); !bytes.Equal(got, decoyPrimary) {
				t.Fatal("replacement Data received an authoritative recovery write")
			}
			if got := readFile(t, filepath.Join(root, "review-jobs/jobs/job-1.json.bak")); !bytes.Equal(got, decoyBackup) {
				t.Fatal("replacement Data received an authoritative recovery backup write")
			}
			persisted, persistedRevision, found, err := (Store{Root: pinnedRoot}).Load("job-1")
			if err != nil || !found || persistedRevision != 2 || persisted.State != Failed || persisted.Error.Code != ApplyRecovery {
				t.Fatalf("pinned recovery state=%#v revision=%d found=%v err=%v", persisted, persistedRevision, found, err)
			}
		})
	}
}

func TestInterruptedRecoveryTerminalizesEveryUnleasedActiveState(t *testing.T) {
	for _, test := range []struct {
		name         string
		state        State
		launchIntent bool
	}{
		{name: "queued_expired_launch", state: Queued, launchIntent: true},
		{name: "retrying_without_launch_token", state: Retrying},
		{name: "retrying_expired_launch", state: Retrying, launchIntent: true},
		{name: "running", state: Running},
		{name: "cancel_requested", state: CancelRequested},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newStoreWithJob(t)
			store := Store{Root: root}
			_, wantedRevision, err := store.Update("job-1", 1, func(job *Job) error {
				job.State = test.state
				job.Phase = Preflight
				if test.state == Queued || test.state == Retrying {
					job.Owner = Owner{}
				}
				if test.launchIntent {
					job.LaunchTokenDigest = "sha256:" + strings.Repeat("e", 64)
					job.LaunchIntentAt = job.CreatedAt
				}
				if test.state == CancelRequested {
					job.CancellationRequested = job.UpdatedAt
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}

			job, revision, disposition, err := store.RecoverInterrupted("job-1")
			if err != nil || disposition != RecoveryApplyInspectionNeeded || revision != wantedRevision+1 ||
				job.State != Failed || job.Error.Code != ApplyRecovery || job.Owner.ID != "" ||
				job.LaunchTokenDigest != "" || !job.LaunchIntentAt.IsZero() {
				t.Fatalf("RecoverInterrupted(%s) = %#v, %d, %q, %v", test.state, job, revision, disposition, err)
			}
		})
	}
}

func TestInterruptedRecoveryLeavesRecentLaunchIntentPending(t *testing.T) {
	for _, state := range []State{Queued, Retrying} {
		t.Run(string(state), func(t *testing.T) {
			root := newStoreWithJob(t)
			store := Store{Root: root}
			now := time.Now().UTC().Round(0)
			wanted, wantedRevision, err := store.Update("job-1", 1, func(job *Job) error {
				job.State = state
				job.Phase = Preflight
				job.Owner = Owner{}
				job.UpdatedAt = now
				job.LaunchTokenDigest = "sha256:" + strings.Repeat("e", 64)
				job.LaunchIntentAt = now
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			got, revision, disposition, err := store.RecoverInterrupted("job-1")
			if err != nil || disposition != RecoveryNotInterrupted || revision != wantedRevision || !reflect.DeepEqual(got, wanted) {
				t.Fatalf("RecoverInterrupted(recent %s)=%#v revision=%d disposition=%q err=%v", state, got, revision, disposition, err)
			}
		})
	}
}

func TestRecoverInterruptedAtHonorsExplicitObservationTime(t *testing.T) {
	for _, test := range []struct {
		name        string
		observedIn  time.Duration
		disposition RecoveryDisposition
	}{
		{name: "within_grace", observedIn: 2 * time.Second, disposition: RecoveryNotInterrupted},
		{name: "after_grace", observedIn: interruptedLaunchGrace + time.Second, disposition: RecoveryApplyInspectionNeeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newStoreWithJob(t)
			store := Store{Root: root}
			launchAt := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
			wanted, wantedRevision, err := store.Update("job-1", 1, func(job *Job) error {
				job.State = Queued
				job.Phase = Preflight
				job.Owner = Owner{}
				job.CreatedAt = launchAt
				job.UpdatedAt = launchAt
				job.LaunchTokenDigest = "sha256:" + strings.Repeat("e", 64)
				job.LaunchIntentAt = launchAt
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			got, revision, disposition, err := store.RecoverInterruptedAt("job-1", launchAt.Add(test.observedIn))
			if err != nil || disposition != test.disposition {
				t.Fatalf("RecoverInterruptedAt(%s) disposition=%q err=%v", test.name, disposition, err)
			}
			if test.disposition == RecoveryNotInterrupted {
				if revision != wantedRevision || !reflect.DeepEqual(got, wanted) {
					t.Fatalf("RecoverInterruptedAt(%s) mutated job=%#v revision=%d want revision=%d", test.name, got, revision, wantedRevision)
				}
				return
			}
			if revision != wantedRevision+1 || got.State != Failed || got.Error.Code != ApplyRecovery {
				t.Fatalf("RecoverInterruptedAt(%s) job=%#v revision=%d", test.name, got, revision)
			}
		})
	}
}

func TestInterruptedRecoveryCancelledRetryLaunchWindowMatrix(t *testing.T) {
	for _, test := range []struct {
		name          string
		launchAge     time.Duration
		liveLease     bool
		wantProtected bool
	}{
		{name: "recent_ownerless", launchAge: 100 * time.Millisecond, wantProtected: true},
		{name: "expired_ownerless", launchAge: interruptedLaunchGrace + time.Second},
		{name: "expired_live_lease", launchAge: interruptedLaunchGrace + time.Second, liveLease: true, wantProtected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, wanted, wantedRevision, token := cancelledRetryLaunchFixture(t, time.Now().UTC().Add(-test.launchAge).Round(0))
			var leases *LeaseSet
			if test.liveLease {
				var err error
				leases, err = store.AcquireLeases(wanted.ProjectID, wanted.ID, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer leases.Release()
			}

			got, revision, disposition, err := store.RecoverInterrupted(wanted.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantProtected {
				if disposition != RecoveryNotInterrupted || revision != wantedRevision || !reflect.DeepEqual(got, wanted) ||
					got.State != CancelRequested || got.Attempt != 2 || got.Owner.ID != "" ||
					got.LaunchTokenDigest != launchTokenDigest(token) || got.LaunchIntentAt.IsZero() {
					t.Fatalf("protected cancelled retry=%#v revision=%d disposition=%q", got, revision, disposition)
				}
				return
			}
			if disposition != RecoveryApplyInspectionNeeded || revision != wantedRevision+1 || got.State != Failed ||
				got.Error.Code != ApplyRecovery || got.LaunchTokenDigest != "" || !got.LaunchIntentAt.IsZero() {
				t.Fatalf("expired cancelled retry=%#v revision=%d disposition=%q", got, revision, disposition)
			}
		})
	}
}

func TestInterruptedRecoveryDoesNotGraceOrdinaryOrCommitWindowCancellation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Job, time.Time)
	}{
		{
			name: "first_attempt_preflight",
			mutate: func(job *Job, now time.Time) {
				job.State, job.Phase, job.Owner = CancelRequested, Preflight, Owner{}
				job.CancellationRequested = now
				job.LaunchTokenDigest = launchTokenDigest("ordinary-cancel-token-with-at-least-32-bytes")
				job.LaunchIntentAt = now
			},
		},
		{
			name: "applying_commit_window",
			mutate: func(job *Job, now time.Time) {
				job.State, job.Phase = CancelRequested, Applying
				job.CancellationRequested = now
			},
		},
		{
			name: "syncing_commit_window",
			mutate: func(job *Job, now time.Time) {
				job.State, job.Phase = CancelRequested, Syncing
				job.CancellationRequested = now
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newStoreWithJob(t)
			store := Store{Root: root}
			now := time.Now().UTC().Add(-100 * time.Millisecond).Round(0)
			_, revision, err := store.Update("job-1", 1, func(job *Job) error {
				job.UpdatedAt = now
				test.mutate(job, now)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			got, gotRevision, disposition, err := store.RecoverInterrupted("job-1")
			if err != nil || disposition != RecoveryApplyInspectionNeeded || gotRevision != revision+1 ||
				got.State != Failed || got.Error.Code != ApplyRecovery {
				t.Fatalf("RecoverInterrupted(%s)=%#v revision=%d disposition=%q err=%v", test.name, got, gotRevision, disposition, err)
			}
		})
	}
}

func TestInterruptedRecoveryConvergesWithConcurrentCancelledRetryLaunch(t *testing.T) {
	for iteration := 0; iteration < 8; iteration++ {
		store, retried, _, token := retryLaunchFixture(t, time.Now().UTC().Add(-100*time.Millisecond).Round(0))
		cancelPublished := make(chan struct{})
		recoveryRead := make(chan struct{})
		cancelStore := Store{Root: store.Root, writeRoot: func(root *os.Root, name string, body []byte, mode fs.FileMode) error {
			if err := atomicfile.WriteRoot(root, name, body, mode); err != nil {
				return err
			}
			if name == retried.ID+".json" {
				close(cancelPublished)
				<-recoveryRead
			}
			return nil
		}}
		var readOnce sync.Once
		recoveryStore := Store{Root: store.Root, afterJobRead: func() error {
			readOnce.Do(func() { close(recoveryRead) })
			return nil
		}}
		type cancelResult struct {
			job Job
			err error
		}
		type recoveryResult struct {
			disposition RecoveryDisposition
			err         error
		}
		cancelled := make(chan cancelResult, 1)
		recovered := make(chan recoveryResult, 1)
		go func() {
			job, _, err := RequestCancel(cancelStore, retried.ID, time.Now().UTC().Round(0))
			cancelled <- cancelResult{job: job, err: err}
		}()
		go func() {
			<-cancelPublished
			_, _, disposition, err := recoveryStore.RecoverInterrupted(retried.ID)
			recovered <- recoveryResult{disposition: disposition, err: err}
		}()
		gotCancel, gotRecovery := <-cancelled, <-recovered
		if gotCancel.err != nil || gotRecovery.err != nil || gotRecovery.disposition != RecoveryNotInterrupted {
			t.Fatalf("iteration %d cancel=%#v recovery=%#v", iteration, gotCancel, gotRecovery)
		}
		final, _, found, err := store.Load(retried.ID)
		if err != nil || !found || final.State != CancelRequested || final.Attempt != 2 || final.Owner.ID != "" ||
			final.LaunchTokenDigest != launchTokenDigest(token) || final.LaunchIntentAt.IsZero() {
			t.Fatalf("iteration %d final=%#v found=%v err=%v", iteration, final, found, err)
		}
	}
}

func cancelledRetryLaunchFixture(t *testing.T, launchAt time.Time) (Store, Job, int, string) {
	t.Helper()
	store, retried, _, token := retryLaunchFixture(t, launchAt)
	cancelled, revision, err := RequestCancel(store, retried.ID, launchAt.Add(time.Millisecond))
	if err != nil || cancelled.State != CancelRequested || cancelled.Attempt != 2 || cancelled.Owner.ID != "" ||
		cancelled.LaunchTokenDigest != launchTokenDigest(token) || cancelled.LaunchIntentAt.IsZero() {
		t.Fatalf("RequestCancel(retrying)=%#v revision=%d err=%v", cancelled, revision, err)
	}
	return store, cancelled, revision, token
}

func retryLaunchFixture(t *testing.T, launchAt time.Time) (Store, Job, int, string) {
	t.Helper()
	root := newStoreRoot(t)
	store := Store{Root: root}
	job := terminalJobFixture(Failed)
	job.CreatedAt = launchAt.Add(-time.Minute)
	job.UpdatedAt = launchAt.Add(-time.Second)
	job.CompletedAt = job.UpdatedAt
	job.Error = SafeError{Code: AgentAuth}
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	token := "cancelled-retry-launch-token-with-at-least-32-bytes"
	retried, revision, err := RequestRetry(store, RetryRequest{
		JobID: job.ID, ExpectedAttempt: 1, ExpectedRevision: 1,
		RequestID: "cancelled-retry-launch", At: launchAt,
		LaunchTokenDigest: launchTokenDigest(token), LaunchIntentAt: launchAt,
	})
	if err != nil || retried.State != Retrying || retried.Attempt != 2 || retried.Owner.ID != "" {
		t.Fatalf("RequestRetry()=%#v revision=%d err=%v", retried, revision, err)
	}
	return store, retried, revision, token
}

func TestInterruptedRecoveryReturnsTypedDisposition(t *testing.T) {
	root := newStoreWithJob(t)
	store := Store{Root: root}
	if _, _, err := store.Update("job-1", 1, func(job *Job) error {
		job.State = Queued
		job.Phase = Preflight
		job.Owner = Owner{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, _, disposition, err := store.RecoverInterrupted("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := reflect.TypeOf(disposition).Name(); got != "RecoveryDisposition" {
		t.Fatalf("RecoverInterrupted() disposition type = %q, want RecoveryDisposition", got)
	}
}

func TestInterruptedRecoveryLeavesLiveAndTerminalJobsUnchanged(t *testing.T) {
	for _, state := range []State{Queued, Running, CancelRequested, Retrying} {
		t.Run("live_"+string(state), func(t *testing.T) {
			root := newStoreWithJob(t)
			store := Store{Root: root}
			wantedRevision := 1
			if state != Running {
				var updateErr error
				_, wantedRevision, updateErr = store.Update("job-1", 1, func(job *Job) error {
					job.State = state
					job.Phase = Preflight
					job.Owner = Owner{}
					if state == CancelRequested {
						job.CancellationRequested = job.UpdatedAt
					}
					return nil
				})
				if updateErr != nil {
					t.Fatal(updateErr)
				}
			}
			path := filepath.Join(root, "review-jobs/jobs/job-1.json")
			before := readFile(t, path)
			leases, err := store.AcquireLeases("project-1", "job-1", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer leases.Release()
			job, revision, disposition, err := store.RecoverInterrupted("job-1")
			if err != nil || disposition != RecoveryNotInterrupted || revision != wantedRevision || job.State != state {
				t.Fatalf("RecoverInterrupted(live %s) = %#v, %d, %q, %v", state, job, revision, disposition, err)
			}
			if after := readFile(t, path); !bytes.Equal(after, before) {
				t.Fatalf("RecoverInterrupted(live %s) mutated persisted bytes", state)
			}
		})
	}

	t.Run("live_terminal", func(t *testing.T) {
		root := newStoreWithJob(t)
		store := Store{Root: root}
		terminalAt := time.Now().UTC()
		if _, _, err := store.Update("job-1", 1, func(job *Job) error {
			job.State = Completed
			job.Phase = ""
			job.Owner = Owner{}
			job.SessionIndex = 1
			job.AcceptedPackets = 1
			job.AcceptedSessions = 1
			job.CompletedAt = terminalAt
			job.UpdatedAt = terminalAt
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "review-jobs/jobs/job-1.json")
		backupPath := path + ".bak"
		before := readFile(t, path)
		beforeBackup := readFile(t, backupPath)
		leases, err := store.AcquireLeases("project-1", "job-1", 0)
		if err != nil {
			t.Fatal(err)
		}
		defer leases.Release()
		job, revision, disposition, err := store.RecoverInterrupted("job-1")
		if err != nil || disposition != RecoveryNotInterrupted || revision != 2 || job.State != Completed {
			t.Fatalf("RecoverInterrupted(live terminal) = %#v, %d, %q, %v", job, revision, disposition, err)
		}
		if after := readFile(t, path); !bytes.Equal(after, before) {
			t.Fatal("RecoverInterrupted(live terminal) mutated persisted bytes")
		}
		if after := readFile(t, backupPath); !bytes.Equal(after, beforeBackup) {
			t.Fatal("RecoverInterrupted(live terminal) mutated recovery-backup bytes")
		}
	})

	t.Run("terminal", func(t *testing.T) {
		root := newStoreWithJob(t)
		store := Store{Root: root}
		terminalAt := time.Now().UTC()
		if _, _, err := store.Update("job-1", 1, func(job *Job) error {
			job.State = Completed
			job.Phase = ""
			job.Owner = Owner{}
			job.SessionIndex = 1
			job.AcceptedPackets = 1
			job.AcceptedSessions = 1
			job.CompletedAt = terminalAt
			job.UpdatedAt = terminalAt
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "review-jobs/jobs/job-1.json")
		backupPath := path + ".bak"
		before := readFile(t, path)
		beforeBackup := readFile(t, backupPath)
		job, revision, disposition, err := store.RecoverInterrupted("job-1")
		if err != nil || disposition != RecoveryNotRecoverable || revision != 2 || job.State != Completed {
			t.Fatalf("RecoverInterrupted(terminal) = %#v, %d, %q, %v", job, revision, disposition, err)
		}
		if after := readFile(t, path); !bytes.Equal(after, before) {
			t.Fatal("RecoverInterrupted(terminal) mutated persisted bytes")
		}
		if after := readFile(t, backupPath); !bytes.Equal(after, beforeBackup) {
			t.Fatal("RecoverInterrupted(terminal) mutated recovery-backup bytes")
		}
	})
}

type leaseContentionResults struct {
	values []string
}

func runLeaseContenders(t *testing.T, root string, projects []string) leaseContentionResults {
	t.Helper()
	gate := filepath.Join(t.TempDir(), "start")
	releaseGate := filepath.Join(t.TempDir(), "release")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	commands := make([]*exec.Cmd, len(projects))
	resultPaths := make([]string, len(projects))
	for index, projectID := range projects {
		result := filepath.Join(t.TempDir(), "result")
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLeaseCrossProcessHelper$")
		command.Env = append(os.Environ(),
			"SESSION_REVIEWER_LEASE_HELPER_ROOT="+root,
			"SESSION_REVIEWER_LEASE_HELPER_PROJECT="+projectID,
			"SESSION_REVIEWER_LEASE_HELPER_JOB=job-child-"+strconv.Itoa(index),
			"SESSION_REVIEWER_LEASE_HELPER_GATE="+gate,
			"SESSION_REVIEWER_LEASE_HELPER_RESULT="+result,
			"SESSION_REVIEWER_LEASE_HELPER_TIMEOUT_MS=40",
			"SESSION_REVIEWER_LEASE_HELPER_RELEASE_GATE="+releaseGate,
		)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands[index], resultPaths[index] = command, result
	}
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range resultPaths {
		waitForAnyLeaseResult(t, path)
	}
	if err := os.WriteFile(releaseGate, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("lease helper %d: %v", index, err)
		}
	}
	results := leaseContentionResults{values: make([]string, len(projects))}
	for index, path := range resultPaths {
		results.values[index] = strings.TrimSpace(string(readFile(t, path)))
	}
	return results
}

func assertLeaseResults(t *testing.T, results leaseContentionResults) {
	t.Helper()
	acquired, busy := 0, 0
	for _, value := range results.values {
		switch value {
		case "acquired":
			acquired++
		case "busy":
			busy++
		default:
			t.Fatalf("lease helper result = %q", value)
		}
	}
	if acquired != 1 || busy != 1 {
		t.Fatalf("lease results=%v, want one acquired and one busy", results.values)
	}
}

func startLeaseHelper(t *testing.T, root, projectID, jobID, exitGate string) (*exec.Cmd, string) {
	t.Helper()
	gate := filepath.Join(t.TempDir(), "start")
	result := filepath.Join(t.TempDir(), "result")
	command := exec.Command(os.Args[0], "-test.run=^TestLeaseCrossProcessHelper$")
	command.Env = append(os.Environ(),
		"SESSION_REVIEWER_LEASE_HELPER_ROOT="+root,
		"SESSION_REVIEWER_LEASE_HELPER_PROJECT="+projectID,
		"SESSION_REVIEWER_LEASE_HELPER_JOB="+jobID,
		"SESSION_REVIEWER_LEASE_HELPER_GATE="+gate,
		"SESSION_REVIEWER_LEASE_HELPER_RESULT="+result,
		"SESSION_REVIEWER_LEASE_HELPER_EXIT_GATE="+exitGate,
		"SESSION_REVIEWER_LEASE_HELPER_TIMEOUT_MS=0",
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	return command, result
}

func waitForLeaseResult(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(body)) == want {
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for lease helper result %q", want)
}

func waitForAnyLeaseResult(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil && (strings.TrimSpace(string(body)) == "acquired" || strings.TrimSpace(string(body)) == "busy") {
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for lease helper result")
}

func TestLeaseCrossProcessHelper(t *testing.T) {
	root := os.Getenv("SESSION_REVIEWER_LEASE_HELPER_ROOT")
	if root == "" {
		return
	}
	waitForHelperGate(t, os.Getenv("SESSION_REVIEWER_LEASE_HELPER_GATE"))
	timeoutMilliseconds, err := strconv.Atoi(os.Getenv("SESSION_REVIEWER_LEASE_HELPER_TIMEOUT_MS"))
	if err != nil {
		t.Fatal(err)
	}
	leases, err := (Store{Root: root}).AcquireLeases(
		os.Getenv("SESSION_REVIEWER_LEASE_HELPER_PROJECT"),
		os.Getenv("SESSION_REVIEWER_LEASE_HELPER_JOB"),
		time.Duration(timeoutMilliseconds)*time.Millisecond,
	)
	result := os.Getenv("SESSION_REVIEWER_LEASE_HELPER_RESULT")
	if errors.Is(err, ErrAgentBusy) {
		if writeErr := os.WriteFile(result, []byte("busy"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result, []byte("acquired"), 0o600); err != nil {
		t.Fatal(err)
	}
	if exitGate := os.Getenv("SESSION_REVIEWER_LEASE_HELPER_EXIT_GATE"); exitGate != "" {
		waitForHelperGate(t, exitGate)
		os.Exit(0) // Deliberately skip deferred cleanup to prove kernel release.
	}
	if releaseGate := os.Getenv("SESSION_REVIEWER_LEASE_HELPER_RELEASE_GATE"); releaseGate != "" {
		waitForHelperGate(t, releaseGate)
	}
	if err := leases.Release(); err != nil {
		t.Fatal(err)
	}
}

func waitForHelperGate(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for helper gate %s", path)
}
