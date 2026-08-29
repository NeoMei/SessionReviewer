package reviewjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/project"
)

func TestStoreCreatePublishesCanonicalPrivateLayout(t *testing.T) {
	root := newStoreRoot(t)
	store := Store{Root: root}
	job := validJobFixture()
	revision, err := store.Create(job)
	if err != nil || revision != 1 {
		t.Fatalf("Create() = revision %d, %v", revision, err)
	}

	for _, relative := range []string{
		"review-jobs", "review-jobs/jobs", "review-jobs/projects", "review-jobs/locks",
		"review-jobs/locks/projects", "review-jobs/work", "review-jobs/work/job-1",
	} {
		assertMode(t, filepath.Join(root, relative), 0o700)
	}
	for _, relative := range []string{
		"review-jobs/jobs/job-1.identity.json", "review-jobs/jobs/job-1.json", "review-jobs/projects/project-1.json", "review-jobs/locks/store.lock",
	} {
		assertMode(t, filepath.Join(root, relative), 0o600)
	}
	body := readFile(t, filepath.Join(root, "review-jobs/jobs/job-1.json"))
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(body, []byte("\n")) || !bytes.Contains(body, []byte("\n  \"revision\": 1,")) {
		t.Fatalf("job record is not canonical indented JSON: %q", body)
	}
	loaded, gotRevision, found, err := store.Load(job.ID)
	if err != nil || !found || gotRevision != 1 || loaded.ID != job.ID || loaded.PrivateError != job.PrivateError {
		t.Fatalf("Load() = %#v, %d, %v, %v", loaded, gotRevision, found, err)
	}
}

func TestStoreCreatePublishesJobBeforeProjectPointerAndRepairsMissingPointer(t *testing.T) {
	root := newStoreRoot(t)
	store := Store{Root: root, beforePointerWrite: func() error { return errors.New("injected pointer interruption") }}
	job := validJobFixture()
	if _, err := store.Create(job); err == nil || !strings.Contains(err.Error(), "pointer") {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "review-jobs/jobs/job-1.json")); err != nil {
		t.Fatalf("job was not durably published before pointer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "review-jobs/projects/project-1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pointer unexpectedly exists: %v", err)
	}

	job.PrivateError = "caller copy must not affect persisted job"
	loaded, revision, found, err := (Store{Root: root}).LatestForProject("project-1")
	if err != nil || !found || revision != 1 || loaded.PrivateError != "" {
		t.Fatalf("LatestForProject() = %#v, %d, %v, %v", loaded, revision, found, err)
	}
	assertMode(t, filepath.Join(root, "review-jobs/projects/project-1.json"), 0o600)
}

func TestStoreTerminalPointerCannotHideOrphanActiveJob(t *testing.T) {
	for _, operation := range []string{"latest", "create"} {
		t.Run(operation, func(t *testing.T) {
			root := newStoreRoot(t)
			store := Store{Root: root}
			activeJob := validJobFixture()
			activeJob.ID = "job-active"
			if _, err := store.Create(activeJob); err != nil {
				t.Fatal(err)
			}
			terminalJob := terminalJobFixture(Failed)
			terminalJob.ID = "job-terminal"
			terminalJob.CreatedAt = activeJob.CreatedAt.Add(time.Minute)
			terminalJob.UpdatedAt = terminalJob.CreatedAt
			terminalJob.CompletedAt = terminalJob.CreatedAt
			if _, err := store.Create(terminalJob); err != nil {
				t.Fatal(err)
			}

			switch operation {
			case "latest":
				latest, _, found, err := store.LatestForProject(activeJob.ProjectID)
				if err != nil || !found || latest.ID != activeJob.ID {
					t.Fatalf("LatestForProject()=%#v found=%v err=%v", latest, found, err)
				}
				pointer, found, err := readProjectPointer(mustOpenStoreProjects(t, root), activeJob.ProjectID)
				if err != nil || !found || pointer.JobID != activeJob.ID {
					t.Fatalf("repaired pointer=%#v found=%v err=%v", pointer, found, err)
				}
			case "create":
				candidate := validJobFixture()
				candidate.ID = "job-candidate"
				if _, err := (Store{Root: root, RejectActiveProject: true}).Create(candidate); !errors.Is(err, ErrActiveJob) {
					t.Fatalf("Create() error=%v, want ErrActiveJob", err)
				}
				if _, _, found, err := store.Load(candidate.ID); err != nil || found {
					t.Fatalf("rejected candidate found=%v err=%v", found, err)
				}
				pointer, found, err := readProjectPointer(mustOpenStoreProjects(t, root), activeJob.ProjectID)
				if err != nil || !found || pointer.JobID != activeJob.ID {
					t.Fatalf("repaired pointer=%#v found=%v err=%v", pointer, found, err)
				}
			}
		})
	}
}

func TestStoreFailsClosedOnMultipleOrCorruptMaskedProjectJobs(t *testing.T) {
	t.Run("multiple active", func(t *testing.T) {
		root := newStoreRoot(t)
		store := Store{Root: root}
		for index, id := range []string{"job-active-1", "job-active-2"} {
			job := validJobFixture()
			job.ID = id
			job.CreatedAt = job.CreatedAt.Add(time.Duration(index) * time.Minute)
			job.UpdatedAt = job.CreatedAt
			job.Owner = Owner{ID: "owner-" + id, AcquiredAt: job.CreatedAt}
			if _, err := store.Create(job); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, _, err := store.LatestForProject("project-1"); err == nil {
			t.Fatal("LatestForProject() accepted multiple active jobs")
		}
		candidate := validJobFixture()
		candidate.ID = "job-active-3"
		if _, err := (Store{Root: root, RejectActiveProject: true}).Create(candidate); err == nil {
			t.Fatal("Create() accepted project with multiple active jobs")
		}
	})

	t.Run("masked corrupt job", func(t *testing.T) {
		root := newStoreRoot(t)
		store := Store{Root: root}
		activeJob := validJobFixture()
		activeJob.ID = "job-corrupt"
		if _, err := store.Create(activeJob); err != nil {
			t.Fatal(err)
		}
		terminalJob := terminalJobFixture(Failed)
		terminalJob.ID = "job-terminal"
		terminalJob.CreatedAt = activeJob.CreatedAt.Add(time.Minute)
		terminalJob.UpdatedAt = terminalJob.CreatedAt
		terminalJob.CompletedAt = terminalJob.CreatedAt
		if _, err := store.Create(terminalJob); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "review-jobs/jobs", activeJob.ID+".json"), []byte("{corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := store.LatestForProject(activeJob.ProjectID); err == nil {
			t.Fatal("LatestForProject() ignored masked corrupt project job")
		}
	})
}

func TestStoreRepairsTerminalPointerFromAuthenticatedActiveBackup(t *testing.T) {
	root := newStoreRoot(t)
	store := Store{Root: root}
	activeJob := validJobFixture()
	activeJob.ID = "job-backup-active"
	if _, err := store.Create(activeJob); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Update(activeJob.ID, 1, func(job *Job) error {
		job.State = Failed
		job.Phase = ""
		job.Owner = Owner{}
		job.CompletedAt = job.UpdatedAt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	terminalJob := terminalJobFixture(Failed)
	terminalJob.ID = "job-terminal"
	terminalJob.CreatedAt = activeJob.CreatedAt.Add(time.Minute)
	terminalJob.UpdatedAt = terminalJob.CreatedAt
	terminalJob.CompletedAt = terminalJob.CreatedAt
	if _, err := store.Create(terminalJob); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "review-jobs/jobs", activeJob.ID+".json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	latest, revision, found, err := store.LatestForProject(activeJob.ProjectID)
	if err != nil || !found || latest.ID != activeJob.ID || revision != 1 || !active(latest.State) {
		t.Fatalf("LatestForProject()=%#v revision=%d found=%v err=%v", latest, revision, found, err)
	}
	pointer, found, err := readProjectPointer(mustOpenStoreProjects(t, root), activeJob.ProjectID)
	if err != nil || !found || pointer.JobID != activeJob.ID {
		t.Fatalf("repaired pointer=%#v found=%v err=%v", pointer, found, err)
	}
}

func TestStoreRejectsDuplicateFieldsUnknownFieldsAndOversizedRecords(t *testing.T) {
	root := newStoreWithJob(t)
	path := filepath.Join(root, "review-jobs/jobs/job-1.json")
	original := readFile(t, path)

	cases := []struct {
		name string
		body []byte
	}{
		{"duplicate top-level", bytes.Replace(original, []byte(`"revision": 1`), []byte(`"revision": 1, "revision": 1`), 1)},
		{"duplicate nested", bytes.Replace(original, []byte(`"id": "job-1"`), []byte(`"id": "job-1", "id": "job-1"`), 1)},
		{"unknown field", bytes.Replace(original, []byte(`"revision": 1`), []byte(`"revision": 1, "unknown": true`), 1)},
		{"oversized", bytes.Repeat([]byte("x"), maxJobRecordBytes+1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, test.body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := (Store{Root: root}).Load("job-1"); err == nil {
				t.Fatal("Load() accepted hostile record")
			}
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStoreRecoversFromAuthenticatedBackup(t *testing.T) {
	root := newStoreWithJob(t)
	store := Store{Root: root}
	updated, revision, err := store.Update("job-1", 1, func(job *Job) error {
		job.AcceptedPackets = 1
		return nil
	})
	if err != nil || revision != 2 || updated.AcceptedPackets != 1 {
		t.Fatalf("Update() = %#v, %d, %v", updated, revision, err)
	}
	backup := filepath.Join(root, "review-jobs/jobs/job-1.json.bak")
	assertMode(t, backup, 0o600)
	if err := os.WriteFile(filepath.Join(root, "review-jobs/jobs/job-1.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, gotRevision, found, err := store.Load("job-1")
	if err != nil || !found || gotRevision != 1 || loaded.AcceptedPackets != 0 {
		t.Fatalf("backup Load() = %#v, %d, %v, %v", loaded, gotRevision, found, err)
	}
	if err := os.WriteFile(backup, []byte("also corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.Load("job-1"); err == nil {
		t.Fatal("Load() accepted corrupt primary and backup")
	}
}

func TestStoreConsumedLaunchTokenCannotBeRestoredFromBackup(t *testing.T) {
	root := newStoreRoot(t)
	store := Store{Root: root}
	job := validJobFixture()
	job.State = Queued
	job.Phase = Preflight
	job.Owner = Owner{}
	job.LaunchTokenDigest = "sha256:" + strings.Repeat("c", 64)
	job.LaunchIntentAt = job.CreatedAt
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}

	updated, revision, err := store.Update(job.ID, 1, func(next *Job) error {
		next.State = Running
		next.Owner = Owner{ID: "owner-token", AcquiredAt: job.CreatedAt}
		next.LaunchTokenDigest = ""
		next.LaunchIntentAt = time.Time{}
		return nil
	})
	if err != nil || revision != 2 || updated.LaunchTokenDigest != "" {
		t.Fatalf("Update() = %#v, %d, %v", updated, revision, err)
	}

	primary := filepath.Join(root, "review-jobs/jobs", job.ID+".json")
	if err := os.WriteFile(primary, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, _, found, err := store.Load(job.ID)
	if err != nil || !found {
		t.Fatalf("Load() = %#v, %v, %v", loaded, found, err)
	}
	if loaded.LaunchTokenDigest != "" || !loaded.LaunchIntentAt.IsZero() {
		t.Fatalf("consumed launch authority was restored from backup: %#v", loaded)
	}
}

func TestStoreConcurrentUpdateHasExactlyOneCASWinner(t *testing.T) {
	root := newStoreWithJob(t)
	store := Store{Root: root}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := store.Update("job-1", 1, func(job *Job) error {
				job.AcceptedPackets++
				return nil
			})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	wins, stale := 0, 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrStaleRevision):
			stale++
		default:
			t.Fatalf("Update() unexpected error: %v", err)
		}
	}
	if wins != 1 || stale != 1 {
		t.Fatalf("wins=%d stale=%d", wins, stale)
	}
}

func TestStoreUpdateCannotChangePinnedReviewPricingSnapshot(t *testing.T) {
	root := newStoreRoot(t)
	store := Store{Root: root}
	job := validJobFixture()
	job.ReviewAccounting = validReviewAccountingFixture()
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.Update(job.ID, 1, func(next *Job) error {
		next.ReviewAccounting.SnapshotAt = next.ReviewAccounting.SnapshotAt.Add(time.Nanosecond)
		return nil
	}); err == nil {
		t.Fatal("Store.Update accepted a changed review pricing snapshot")
	}
	loaded, revision, found, err := store.Load(job.ID)
	if err != nil || !found || revision != 1 || !reflect.DeepEqual(loaded.ReviewAccounting, job.ReviewAccounting) {
		t.Fatalf("rejected snapshot update changed durable state: revision=%d found=%v err=%v accounting=%+v", revision, found, err, loaded.ReviewAccounting)
	}

	updated, revision, err := store.Update(job.ID, 1, func(next *Job) error {
		value, addErr := AddReviewResult(next.ReviewAccounting, agent.Result{Model: "fixture-model", Usage: accounting.TokenUsage{InputTokens: 1, TotalTokens: 1}}, next.ReviewAccounting.SnapshotAt, fixturePricingResolver{"fixture-model": fixturePricing(1, 0, 0, 1)})
		if addErr != nil {
			return addErr
		}
		next.ReviewAccounting = value
		return nil
	})
	if err != nil || revision != 2 || updated.ReviewAccounting.TotalTokens != 3 {
		t.Fatalf("append at pinned snapshot = revision %d accounting=%+v err=%v", revision, updated.ReviewAccounting, err)
	}

	if _, _, err := store.Update(job.ID, 2, func(next *Job) error {
		pricing := next.ReviewAccounting.Models[0].Pricing
		pricing.InputPerMillion = 2
		next.ReviewAccounting.Models[0].Pricing = pricing
		cost, priceErr := accounting.PriceUsage(next.ReviewAccounting.Models[0].TokenUsage, pricing)
		if priceErr != nil {
			return priceErr
		}
		next.ReviewAccounting.Models[0].CostUSD = cost
		next.ReviewAccounting.TotalCostUSD = &cost
		return nil
	}); err == nil {
		t.Fatal("Store.Update accepted repricing an existing model at the pinned snapshot")
	}
}

func TestStoreUpdateRejectsReviewUsageReclassificationAndCostTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ReviewAccounting) error
	}{
		{
			name: "uncached input reclassified as cached",
			mutate: func(value *ReviewAccounting) error {
				value.Models[0].CachedInputTokens++
				cost, err := accounting.PriceUsage(value.Models[0].TokenUsage, value.Models[0].Pricing)
				if err != nil {
					return err
				}
				value.Models[0].CostUSD = cost
				value.TotalCostUSD = &cost
				return nil
			},
		},
		{
			name: "output reclassified as reasoning",
			mutate: func(value *ReviewAccounting) error {
				value.Models[0].ReasoningOutputTokens++
				return nil
			},
		},
		{
			name: "row cost changed within former tolerance",
			mutate: func(value *ReviewAccounting) error {
				value.Models[0].CostUSD += 5e-10
				return nil
			},
		},
		{
			name: "aggregate cost changed within former tolerance",
			mutate: func(value *ReviewAccounting) error {
				*value.TotalCostUSD += 5e-10
				return nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newStoreRoot(t)
			store := Store{Root: root}
			job := validJobFixture()
			job.ReviewAccounting = validReviewAccountingFixture()
			if _, err := store.Create(job); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.Update(job.ID, 1, func(next *Job) error {
				return test.mutate(&next.ReviewAccounting)
			}); err == nil {
				t.Fatal("Store.Update accepted non-additive or noncanonical review accounting")
			}
			loaded, revision, found, err := store.Load(job.ID)
			if err != nil || !found || revision != 1 || !reflect.DeepEqual(loaded.ReviewAccounting, job.ReviewAccounting) {
				t.Fatalf("rejected update changed durable state: revision=%d found=%v err=%v accounting=%+v", revision, found, err, loaded.ReviewAccounting)
			}
		})
	}
}

func TestStoreCrossProcessCASHasExactlyOneWinner(t *testing.T) {
	root := newStoreWithJob(t)
	gate := filepath.Join(t.TempDir(), "start")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, 2)
	for index := range commands {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreCrossProcessCASHelper$")
		command.Env = append(os.Environ(), "SESSION_REVIEWER_STORE_HELPER_ROOT="+root, "SESSION_REVIEWER_STORE_HELPER_GATE="+gate)
		command.Stdout = &outputs[index]
		command.Stderr = &outputs[index]
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands[index] = command
	}
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	wins, stale := 0, 0
	for index := range commands {
		if err := commands[index].Wait(); err != nil {
			t.Fatalf("helper %d: %v\n%s", index, err, outputs[index].String())
		}
		switch strings.TrimSpace(outputs[index].String()) {
		case "win":
			wins++
		case "stale":
			stale++
		default:
			t.Fatalf("helper %d output = %q", index, outputs[index].String())
		}
	}
	if wins != 1 || stale != 1 {
		t.Fatalf("cross-process wins=%d stale=%d", wins, stale)
	}
}

func TestStoreCrossProcessCASHelper(t *testing.T) {
	root := os.Getenv("SESSION_REVIEWER_STORE_HELPER_ROOT")
	if root == "" {
		return
	}
	gate := os.Getenv("SESSION_REVIEWER_STORE_HELPER_GATE")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(gate); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if !time.Now().Before(deadline) {
			t.Fatal("timed out waiting for cross-process gate")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, _, err := (Store{Root: root}).Update("job-1", 1, func(job *Job) error {
		job.AcceptedPackets++
		return nil
	})
	switch {
	case err == nil:
		fmt.Print("win")
		os.Exit(0)
	case errors.Is(err, ErrStaleRevision):
		fmt.Print("stale")
		os.Exit(0)
	default:
		t.Fatal(err)
	}
}

func TestStoreCASCoexistsWithLongLivedWorkerLease(t *testing.T) {
	root := newStoreWithJob(t)
	data, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	workerLease, err := project.AcquireProjectLock(data, "review-jobs/locks/projects/project-1.lock", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer workerLease.Release()

	updated, revision, err := (Store{Root: root}).Update("job-1", 1, func(job *Job) error {
		job.AcceptedPackets = 1
		return nil
	})
	if err != nil || revision != 2 || updated.AcceptedPackets != 1 {
		t.Fatalf("Update() while worker lease held = %#v, %d, %v", updated, revision, err)
	}
}

func TestStoreBackupCapturesImmutablePreMutationJob(t *testing.T) {
	root := newStoreWithJob(t)
	store := Store{Root: root}
	if _, _, err := store.Update("job-1", 1, func(job *Job) error {
		job.FrozenSessions[0].SessionID = "session-2"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "review-jobs/jobs/job-1.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, revision, found, err := store.Load("job-1")
	if err != nil || !found || revision != 1 || len(loaded.FrozenSessions) != 1 || loaded.FrozenSessions[0].SessionID != "session-1" {
		t.Fatalf("recovered backup = %#v, revision=%d found=%v err=%v", loaded.FrozenSessions, revision, found, err)
	}
}

func TestStoreRejectsCrossProjectBackupWithSameJobID(t *testing.T) {
	root := newStoreWithJob(t)
	primary := filepath.Join(root, "review-jobs/jobs/job-1.json")
	var foreign storedJob
	if err := json.Unmarshal(readFile(t, primary), &foreign); err != nil {
		t.Fatal(err)
	}
	foreign.Job.ProjectID = "project-2"
	foreign.Job.ProjectIdentity.File = "22"
	foreignBody, err := marshalCanonical(foreign, maxJobRecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(primary+".bak", foreignBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(primary, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := (Store{Root: root}).Load("job-1"); err == nil {
		t.Fatal("Load() trusted a same-ID backup belonging to another project")
	}
}

func TestStoreRecoversHistoricalJobAfterLatestPointerAdvances(t *testing.T) {
	root := newStoreWithJob(t)
	store := Store{Root: root}
	if _, _, err := store.Update("job-1", 1, func(job *Job) error {
		job.AcceptedPackets = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	job2 := validJobFixture()
	job2.ID = "job-2"
	if _, err := store.Create(job2); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "review-jobs/jobs/job-1.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, revision, found, err := store.Load("job-1")
	if err != nil || !found || revision != 1 || loaded.ID != "job-1" || loaded.AcceptedPackets != 0 {
		t.Fatalf("historical Load() = %#v, revision=%d found=%v err=%v", loaded, revision, found, err)
	}
}

func TestStoreIdentityFirstInterruptionLeavesSafeRecoverableOrphan(t *testing.T) {
	root := newStoreRoot(t)
	store := Store{Root: root, afterIdentityWrite: func() error { return errors.New("injected after identity") }}
	if _, err := store.Create(validJobFixture()); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("Create() error = %v", err)
	}
	identity := filepath.Join(root, "review-jobs/jobs/job-1.identity.json")
	assertMode(t, identity, 0o600)
	if _, err := os.Stat(filepath.Join(root, "review-jobs/jobs/job-1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("job unexpectedly published after identity interruption: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "review-jobs/projects/project-1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pointer unexpectedly published after identity interruption: %v", err)
	}
	if _, _, found, err := (Store{Root: root}).LatestForProject("project-1"); err != nil || found {
		t.Fatalf("orphan identity LatestForProject() found=%v err=%v", found, err)
	}
	if _, err := (Store{Root: root}).Create(validJobFixture()); err != nil {
		t.Fatalf("Create() did not safely resume matching orphan identity: %v", err)
	}
}

func TestStoreRejectsHostileImmutableIdentityAuthority(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode and symlink assertions")
	}
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, path string)
	}{
		{
			name: "duplicate field",
			mutate: func(t *testing.T, path string) {
				body := readFile(t, path)
				body = bytes.Replace(body, []byte(`"job_id": "job-1"`), []byte(`"job_id": "job-1", "job_id": "job-1"`), 1)
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "oversized",
			mutate: func(t *testing.T, path string) {
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 20<<10), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "weak mode",
			mutate: func(t *testing.T, path string) {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			mutate: func(t *testing.T, path string) {
				outside := filepath.Join(t.TempDir(), "identity.json")
				if err := os.WriteFile(outside, readFile(t, path), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newStoreWithJob(t)
			identity := filepath.Join(root, "review-jobs/jobs/job-1.identity.json")
			test.mutate(t, identity)
			if _, _, _, err := (Store{Root: root}).Load("job-1"); err == nil {
				t.Fatal("Load() accepted hostile immutable identity authority")
			}
		})
	}
	t.Run("case collision", func(t *testing.T) {
		root := newStoreWithJob(t)
		lower := filepath.Join(root, "review-jobs/jobs/job-1.identity.json")
		upper := filepath.Join(root, "review-jobs/jobs/Job-1.identity.json")
		body := readFile(t, lower)
		if err := os.WriteFile(upper, body, 0o600); err != nil {
			t.Fatal(err)
		}
		lowerInfo, lowerErr := os.Lstat(lower)
		upperInfo, upperErr := os.Lstat(upper)
		if lowerErr == nil && upperErr == nil && os.SameFile(lowerInfo, upperInfo) {
			t.Skip("filesystem is case-insensitive")
		}
		if _, _, _, err := (Store{Root: root}).Load("job-1"); err == nil {
			t.Fatal("Load() accepted identity filename case collision")
		}
	})
}

func TestStoreBackupRepairIgnoresUnrelatedCorruptPointerInventory(t *testing.T) {
	root := newStoreRoot(t)
	for index := 1; index <= 8; index++ {
		job := validJobFixture()
		job.ID = fmt.Sprintf("job-%02d", index)
		job.ProjectID = fmt.Sprintf("project-%02d", index)
		job.ProjectIdentity.File = fmt.Sprintf("%d", 100+index)
		store := Store{Root: root}
		if _, err := store.Create(job); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Update(job.ID, 1, func(job *Job) error {
			job.AcceptedPackets = 1
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "review-jobs/jobs", job.ID+".json"), []byte("{corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(root, "review-jobs/projects/project-01.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "review-jobs/projects/project-08.json"), []byte("{unrelated corrupt pointer"), 0o600); err != nil {
		t.Fatal(err)
	}
	identityReads := 0
	store := Store{
		Root: root,
		afterJobIdentityRead: func() error {
			identityReads++
			return nil
		},
	}
	job, revision, found, err := store.LatestForProject("project-01")
	if err != nil || !found || revision != 1 || job.ID != "job-01" {
		t.Fatalf("LatestForProject() = %#v, %d, %v, %v", job, revision, found, err)
	}
	if identityReads != 8 {
		t.Fatalf("identity reads=%d, want one per candidate", identityReads)
	}
}

func TestStoreRejectsExhaustedRevisionWithoutMutation(t *testing.T) {
	root := newStoreWithJob(t)
	primary := filepath.Join(root, "review-jobs/jobs/job-1.json")
	var record storedJob
	if err := json.Unmarshal(readFile(t, primary), &record); err != nil {
		t.Fatal(err)
	}
	record.Revision = maxSafeInteger
	before, err := marshalCanonical(record, maxJobRecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(primary, before, 0o600); err != nil {
		t.Fatal(err)
	}
	callbackCalled := false
	if _, _, err := (Store{Root: root}).Update("job-1", maxSafeInteger, func(job *Job) error {
		callbackCalled = true
		job.AcceptedPackets = 1
		return nil
	}); !errors.Is(err, ErrRevisionExhausted) {
		t.Fatalf("Update() error = %v, want ErrRevisionExhausted", err)
	}
	if callbackCalled {
		t.Fatal("exhausted revision invoked mutation callback")
	}
	if after := readFile(t, primary); !bytes.Equal(after, before) {
		t.Fatal("exhausted revision changed primary bytes")
	}
	if _, err := os.Stat(primary + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exhausted revision published a backup: %v", err)
	}
}

func TestStoreRejectsRedirectsCaseCollisionsAndPermissionWeakening(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink and mode assertions")
	}
	t.Run("redirect", func(t *testing.T) {
		root := newStoreRoot(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "review-jobs")); err != nil {
			t.Fatal(err)
		}
		if _, err := (Store{Root: root}).Create(validJobFixture()); err == nil {
			t.Fatal("Create() accepted redirected namespace")
		}
	})
	t.Run("case collision", func(t *testing.T) {
		root := newStoreWithJob(t)
		if err := os.WriteFile(filepath.Join(root, "review-jobs/jobs/Job-1.json"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := (Store{Root: root}).Load("job-1"); err == nil {
			t.Fatal("Load() accepted a case collision")
		}
	})
	t.Run("weak directory", func(t *testing.T) {
		root := newStoreWithJob(t)
		if err := os.Chmod(filepath.Join(root, "review-jobs/jobs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := (Store{Root: root}).Load("job-1"); err == nil {
			t.Fatal("Load() accepted weakened directory permissions")
		}
	})
	t.Run("weak file", func(t *testing.T) {
		root := newStoreWithJob(t)
		if err := os.Chmod(filepath.Join(root, "review-jobs/jobs/job-1.json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := (Store{Root: root}).Load("job-1"); err == nil {
			t.Fatal("Load() accepted weakened file permissions")
		}
	})
	t.Run("weak backup", func(t *testing.T) {
		root := newStoreWithJob(t)
		store := Store{Root: root}
		if _, _, err := store.Update("job-1", 1, func(job *Job) error {
			job.AcceptedPackets = 1
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, "review-jobs/jobs/job-1.json.bak"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := store.Load("job-1"); err == nil {
			t.Fatal("Load() accepted weakened backup permissions")
		}
	})
}

func TestStoreRejectsSameInodeMutationDuringRead(t *testing.T) {
	root := newStoreWithJob(t)
	path := filepath.Join(root, "review-jobs/jobs/job-1.json")
	store := Store{Root: root, afterJobRead: func() error {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.WriteAt([]byte(" "), 0)
		return err
	}}
	if _, _, _, err := store.Load("job-1"); err == nil {
		t.Fatal("Load() accepted same-inode mutation")
	}
}

func TestStoreRejectsPinnedNamespaceReplacement(t *testing.T) {
	root := newStoreWithJob(t)
	jobs := filepath.Join(root, "review-jobs/jobs")
	store := Store{Root: root, afterJobRead: func() error {
		if err := os.Rename(jobs, jobs+"-replaced"); err != nil {
			return err
		}
		return os.Mkdir(jobs, 0o700)
	}}
	if _, _, _, err := store.Load("job-1"); err == nil {
		t.Fatal("Load() accepted replacement of the pinned job namespace")
	}
}

func TestStoreBoundsEnumerationAndAuthenticatesProjectPointer(t *testing.T) {
	t.Run("entry bound", func(t *testing.T) {
		root := newStoreWithJob(t)
		jobs := filepath.Join(root, "review-jobs/jobs")
		for i := 0; i <= maxJobDirectoryEntries; i++ {
			name := fmt.Sprintf("extra-%04d", i)
			if err := os.WriteFile(filepath.Join(jobs, name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, _, err := (Store{Root: root}).Load("job-1"); err == nil {
			t.Fatal("Load() accepted an over-budget directory")
		}
	})
	t.Run("cross-project pointer", func(t *testing.T) {
		root := newStoreWithJob(t)
		job2 := validJobFixture()
		job2.ID = "job-2"
		job2.ProjectID = "project-2"
		job2.ProjectIdentity.File = "22"
		if _, err := (Store{Root: root}).Create(job2); err != nil {
			t.Fatal(err)
		}
		wrong := readFile(t, filepath.Join(root, "review-jobs/projects/project-2.json"))
		if err := os.WriteFile(filepath.Join(root, "review-jobs/projects/project-1.json"), wrong, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := (Store{Root: root}).LatestForProject("project-1"); err == nil {
			t.Fatal("LatestForProject() followed another project's pointer")
		}
	})
}

func TestStoreMissingPointerRepairRejectsAggregateRecordBudget(t *testing.T) {
	root := newStoreWithJob(t)
	jobs := filepath.Join(root, "review-jobs/jobs")
	if err := os.Remove(filepath.Join(root, "review-jobs/projects/project-1.json")); err != nil {
		t.Fatal(err)
	}
	for index := 2; index <= 4; index++ {
		job := validJobFixture()
		job.ID = fmt.Sprintf("job-%d", index)
		job.PrivateError = strings.Repeat("x", 3<<20)
		body, err := marshalCanonical(storedJob{Revision: 1, Job: job}, maxJobRecordBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jobs, job.ID+".json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := (Store{Root: root}).LatestForProject("project-1"); err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("LatestForProject() error = %v, want aggregate budget rejection", err)
	}
}

func TestStoreMissingPointerRepairScansAndReadsEachCandidateOnce(t *testing.T) {
	root := newStoreRoot(t)
	for index := 1; index <= 3; index++ {
		job := terminalJobFixture(Failed)
		job.ID = fmt.Sprintf("job-%d", index)
		if _, err := (Store{Root: root}).Create(job); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(root, "review-jobs/projects/project-1.json")); err != nil {
		t.Fatal(err)
	}
	scans, reads := 0, 0
	store := Store{
		Root: root,
		afterJobDirectoryScan: func() error {
			scans++
			return nil
		},
		afterJobRead: func() error {
			reads++
			return nil
		},
	}
	job, revision, found, err := store.LatestForProject("project-1")
	if err != nil || !found || revision != 1 || job.ID != "job-3" {
		t.Fatalf("LatestForProject() = %#v, %d, %v, %v", job, revision, found, err)
	}
	if scans != 1 || reads != 3 {
		t.Fatalf("repair scans=%d reads=%d, want one scan and one read per candidate", scans, reads)
	}
}

func TestStoreMutationGuardPreventsCreatePointerRepairRecoveryAndCASWrites(t *testing.T) {
	guardErr := errors.New("Project mapping authority changed")
	guard := func(Job) error { return guardErr }

	t.Run("create", func(t *testing.T) {
		root := newStoreRoot(t)
		if _, err := (Store{Root: root}).WithMutationGuard(guard).Create(validJobFixture()); !errors.Is(err, guardErr) {
			t.Fatalf("Create() error=%v, want mutation guard", err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("guarded Create mutated empty store: entries=%v err=%v", entries, err)
		}
	})

	t.Run("pointer repair", func(t *testing.T) {
		root := newStoreWithJob(t)
		pointer := filepath.Join(root, "review-jobs/projects/project-1.json")
		if err := os.Remove(pointer); err != nil {
			t.Fatal(err)
		}
		identity := validJobFixture().ProjectIdentity
		if _, _, _, err := (Store{Root: root}).WithMutationGuard(guard).LatestForProjectAuthenticated("project-1", identity); !errors.Is(err, guardErr) {
			t.Fatalf("LatestForProjectAuthenticated() error=%v, want mutation guard", err)
		}
		if _, err := os.Stat(pointer); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("guarded latest repaired pointer: %v", err)
		}
	})

	t.Run("recovery", func(t *testing.T) {
		root := newStoreWithJob(t)
		primary := filepath.Join(root, "review-jobs/jobs/job-1.json")
		before := readFile(t, primary)
		if _, _, _, err := (Store{Root: root}).WithMutationGuard(guard).RecoverInterrupted("job-1"); !errors.Is(err, guardErr) {
			t.Fatalf("RecoverInterrupted() error=%v, want mutation guard", err)
		}
		if got := readFile(t, primary); !bytes.Equal(got, before) {
			t.Fatal("guarded recovery changed the job record")
		}
		lease := filepath.Join(root, "review-jobs/locks/projects/project-1.lock")
		if _, err := os.Stat(lease); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("guarded recovery created a project lease: %v", err)
		}
	})

	t.Run("CAS", func(t *testing.T) {
		root := newStoreWithJob(t)
		primary := filepath.Join(root, "review-jobs/jobs/job-1.json")
		before := readFile(t, primary)
		_, _, err := (Store{Root: root}).WithMutationGuard(guard).Update("job-1", 1, func(job *Job) error {
			job.State = Running
			return nil
		})
		if !errors.Is(err, guardErr) {
			t.Fatalf("Update() error=%v, want mutation guard", err)
		}
		if got := readFile(t, primary); !bytes.Equal(got, before) {
			t.Fatal("guarded CAS changed the job record")
		}
		backup := filepath.Join(root, "review-jobs/jobs/job-1.json.bak")
		if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("guarded CAS created a backup: %v", err)
		}
	})
}

func TestStoreCreateStatefulMutationGuardDriftLeavesEmptyDataRootUnchanged(t *testing.T) {
	root := newStoreRoot(t)
	guardErr := errors.New("Project mapping authority changed after initial check")
	calls := 0
	store := (Store{Root: root}).WithMutationGuard(func(Job) error {
		calls++
		if calls == 1 {
			return nil
		}
		return guardErr
	})

	if _, err := store.Create(validJobFixture()); !errors.Is(err, guardErr) {
		t.Fatalf("Create() error=%v, want stateful mutation guard", err)
	}
	if calls < 2 {
		t.Fatalf("mutation guard calls=%d, want a recheck at the first bootstrap mutation", calls)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("guard drift left bootstrap entries in an initially empty Data root: %v", entries)
	}
}

func TestStoreCreateMutationGuardDriftAfterOwnedBootstrapRollsBackEmptyDataRoot(t *testing.T) {
	for _, checkpoint := range []struct {
		name string
		path string
	}{
		{name: "store_lock", path: filepath.Join("review-jobs", "locks", storeLockName)},
		{name: "job_identity", path: filepath.Join("review-jobs", "jobs", jobIdentityName("job-1"))},
	} {
		t.Run(checkpoint.name, func(t *testing.T) {
			root := newStoreRoot(t)
			guardErr := errors.New("Project mapping authority changed after owned bootstrap publication")
			observed := false
			store := (Store{Root: root}).WithMutationGuard(func(Job) error {
				if _, err := os.Stat(filepath.Join(root, checkpoint.path)); err == nil {
					observed = true
					return guardErr
				}
				return nil
			})

			if _, err := store.Create(validJobFixture()); !errors.Is(err, guardErr) {
				t.Fatalf("Create() error=%v, want stateful mutation guard", err)
			}
			if !observed {
				t.Fatalf("test did not observe owned %s publication", checkpoint.name)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("guard drift after %s left owned bootstrap entries: %v", checkpoint.name, entries)
			}
		})
	}
}

func TestStoreCreateMutationGuardRunsBeforeMissingStoreLockCreation(t *testing.T) {
	root := newStoreRoot(t)
	layout, err := (Store{Root: root}).openLayout(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.finish(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, "review-jobs", "locks", storeLockName)
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test layout unexpectedly has a store lock: %v", err)
	}
	guardErr := errors.New("Project mapping authority changed before lock publication")
	calls := 0
	store := (Store{Root: root}).WithMutationGuard(func(Job) error {
		calls++
		if calls == 1 {
			return nil
		}
		return guardErr
	})

	if _, err := store.Create(validJobFixture()); !errors.Is(err, guardErr) {
		t.Fatalf("Create() error=%v, want lock-publication guard", err)
	}
	if calls != 2 {
		t.Fatalf("mutation guard calls=%d, want initial check plus lock-publication check", calls)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("guard drift published the missing store lock: %v", err)
	}
}

func TestStoreCreateBootstrapRollbackNeverDeletesThirdPartyRaceContent(t *testing.T) {
	root := newStoreRoot(t)
	guardErr := errors.New("Project mapping authority changed after third-party publication")
	sentinel := filepath.Join(root, "review-jobs", "third-party.txt")
	injected := false
	store := (Store{Root: root}).WithMutationGuard(func(Job) error {
		if injected {
			return guardErr
		}
		if info, err := os.Stat(filepath.Join(root, "review-jobs")); err == nil && info.IsDir() {
			if err := os.WriteFile(sentinel, []byte("third-party-owned\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			injected = true
			return guardErr
		}
		return nil
	})

	if _, err := store.Create(validJobFixture()); !errors.Is(err, guardErr) {
		t.Fatalf("Create() error=%v, want stateful mutation guard", err)
	}
	if !injected {
		t.Fatal("test did not reach a post-bootstrap authority checkpoint")
	}
	body, err := os.ReadFile(sentinel)
	if err != nil || string(body) != "third-party-owned\n" {
		t.Fatalf("rollback deleted or changed third-party race content: body=%q err=%v", body, err)
	}
}

func TestStoreRejectsUnsafeIDsAndMutationOfStableIdentity(t *testing.T) {
	root := newStoreRoot(t)
	store := Store{Root: root}
	for _, id := range []string{"../escape", "Job-1", "CON", "job.identity", strings.Repeat("a", 129)} {
		if _, _, _, err := store.Load(id); err == nil {
			t.Fatalf("Load(%q) accepted unsafe ID", id)
		}
		job := validJobFixture()
		job.ID = id
		if _, err := store.Create(job); err == nil {
			t.Fatalf("Create(%q) accepted unsafe ID", id)
		}
	}
	if _, err := store.Create(validJobFixture()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Update("job-1", 1, func(job *Job) error {
		job.ProjectID = "project-2"
		return nil
	}); err == nil {
		t.Fatal("Update() accepted stable identity mutation")
	}
}

func mustOpenStoreProjects(t *testing.T, root string) *os.Root {
	t.Helper()
	projects, err := os.OpenRoot(filepath.Join(root, "review-jobs", "projects"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = projects.Close() })
	return projects
}

func newStoreRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func newStoreWithJob(t *testing.T) string {
	t.Helper()
	root := newStoreRoot(t)
	if _, err := (Store{Root: root}).Create(validJobFixture()); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != want {
		t.Fatalf("%s mode=%#o want=%#o", path, info.Mode().Perm(), want)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
