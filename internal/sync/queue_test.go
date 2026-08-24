package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

func TestQueueIsDurableDeduplicatedAndContentFree(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	now := fixedTime
	queue := Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return now }}
	item := QueueItem{
		Version:          1,
		EntityID:         "decision-1",
		Target:           SideVault,
		ExpectedBaseHash: hash("base"),
		CreatedAt:        fixedTime,
		UpdatedAt:        fixedTime,
	}
	first, err := queue.Enqueue(item)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	second, err := (Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return now }}).Enqueue(item)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !reflect.DeepEqual(first, second) {
		t.Fatalf("not deduplicated across restart: first=%+v second=%+v", first, second)
	}
	wantID := queueID(item.EntityID, item.Target, item.ExpectedBaseHash)
	if first.ID != wantID || len(first.ID) != 32 {
		t.Fatalf("id=%q want=%q", first.ID, wantID)
	}
	recordPath := filepath.Join(directory, queueRecordPath(first.ID))
	encoded, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"CANARY-CONTENT", "decisions/", `"content"`, `"path"`, `"error"`, `"bytes"`} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("queue leaked %q: %s", forbidden, encoded)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"attempts", "created_at", "entity_id", "expected_base_hash", "id", "last_error_class", "not_before", "state", "target", "updated_at", "version"}
	for _, field := range wantFields {
		delete(fields, field)
	}
	if len(fields) != 0 {
		t.Fatalf("undeclared queue fields=%v", fields)
	}
	if info, err := os.Lstat(recordPath); err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("queue record info=%v err=%v", info, err)
	}
}

func TestQueueReadyRescheduleBlockAndAckAreDeterministic(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	now := fixedTime
	queue := Queue{
		Root:  root,
		Retry: RetryPolicy{Initial: 100 * time.Millisecond, Max: 250 * time.Millisecond, InlineAttempts: 1, QueueAttempts: 3},
		Now:   func() time.Time { return now },
	}
	created := make(map[string]QueueItem)
	for index, id := range []string{"decision-z", "decision-a", "decision-m"} {
		createdAt := fixedTime.Add(time.Duration(index) * time.Second)
		item, err := queue.Enqueue(QueueItem{Version: 1, EntityID: id, Target: SideProject, ExpectedBaseHash: hash("base"), NotBefore: createdAt, CreatedAt: createdAt, UpdatedAt: createdAt})
		if err != nil {
			t.Fatal(err)
		}
		created[id] = item
	}
	ready, err := queue.Ready(fixedTime.Add(2*time.Second), 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := queueEntityIDs(ready); !reflect.DeepEqual(got, []string{"decision-z", "decision-a"}) {
		t.Fatalf("ready order=%v", got)
	}
	item := created["decision-m"]
	for attempt, delay := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond} {
		now = fixedTime.Add(10*time.Second + time.Duration(attempt)*time.Second)
		item, err = queue.Reschedule(item.ID, "sharing_violation")
		if err != nil {
			t.Fatal(err)
		}
		if item.Attempts != attempt+1 || !item.NotBefore.Equal(now.Add(delay)) {
			t.Fatalf("attempt=%d item=%+v", attempt+1, item)
		}
	}
	if item.State != QueueBlocked {
		t.Fatalf("blocked item=%+v", item)
	}
	if _, err := queue.Reschedule(item.ID, "sharing_violation"); err == nil {
		t.Fatal("rescheduled blocked queue item")
	}
	restarted := Queue{Root: root, Retry: queue.Retry, Now: queue.Now}
	visible, err := restarted.Ready(now.Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	blockedVisible := false
	for _, candidate := range visible {
		if candidate.ID == item.ID {
			blockedVisible = candidate.State == QueueBlocked
		}
	}
	if !blockedVisible {
		t.Fatalf("blocked item is not visible after restart: %+v", visible)
	}
	encoded := readQueueRecordForTest(t, root, item.ID)
	if !bytes.Contains(encoded, []byte(`"state": "blocked"`)) {
		t.Fatalf("blocked record=%s", encoded)
	}
	if err := restarted.Ack(item.ID); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Ack(item.ID); err != nil {
		t.Fatalf("idempotent ack: %v", err)
	}
}

func TestQueueRejectsInvalidInputAndCorruptPersistentStateWithoutLeakingValues(t *testing.T) {
	for _, mutate := range []func(*QueueItem){
		func(item *QueueItem) { item.Version = 2 },
		func(item *QueueItem) { item.ID = strings.Repeat("f", 32) },
		func(item *QueueItem) { item.EntityID = "../CANARY-CONTENT" },
		func(item *QueueItem) { item.Target = Side("other") },
		func(item *QueueItem) { item.ExpectedBaseHash = "CANARY-CONTENT" },
		func(item *QueueItem) { item.State = QueueState("other") },
		func(item *QueueItem) { item.LastErrorClass = "decisions/CANARY-CONTENT" },
	} {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		item := QueueItem{Version: 1, EntityID: "decision-1", Target: SideVault, ExpectedBaseHash: hash("base"), CreatedAt: fixedTime, UpdatedAt: fixedTime}
		mutate(&item)
		_, err = (Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return fixedTime }}).Enqueue(item)
		_ = root.Close()
		if err == nil || strings.Contains(err.Error(), "CANARY-CONTENT") {
			t.Fatalf("error=%v", err)
		}
	}

	for _, corrupt := range [][]byte{
		[]byte(`{"version":1,"unknown":"CANARY-CONTENT"}`),
		[]byte(`{"version":1}{}`),
		[]byte(`{"version":1,"version":1}`),
	} {
		directory := t.TempDir()
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		id := strings.Repeat("a", 32)
		if err := os.WriteFile(filepath.Join(directory, queueRecordPath(id)), corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = (Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return fixedTime }}).Ready(fixedTime, 0)
		_ = root.Close()
		if err == nil || strings.Contains(err.Error(), "CANARY-CONTENT") {
			t.Fatalf("corrupt error=%v", err)
		}
	}
}

func TestQueueNormalizesNewTimesToUTC(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	local := fixedTime.In(time.FixedZone("local", 8*60*60))
	item, err := (Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return local }}).Enqueue(QueueItem{
		Version:          1,
		EntityID:         "decision-utc",
		Target:           SideVault,
		ExpectedBaseHash: hash("base"),
		CreatedAt:        local,
		UpdatedAt:        local,
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.CreatedAt.Location() != time.UTC || item.UpdatedAt.Location() != time.UTC || item.NotBefore.Location() != time.UTC {
		t.Fatalf("queue times are not normalized: %+v", item)
	}
}

func TestQueueRejectsAttemptStateAndTimePolicyViolations(t *testing.T) {
	policy := RetryPolicy{Initial: time.Nanosecond, Max: time.Duration(math.MaxInt64), InlineAttempts: 1, QueueAttempts: 3}
	valid := QueueItem{
		Version:          1,
		EntityID:         "decision-policy",
		Target:           SideVault,
		ExpectedBaseHash: hash("base"),
		Attempts:         1,
		NotBefore:        fixedTime.Add(time.Second),
		State:            QueuePending,
		LastErrorClass:   "sharing_violation",
		CreatedAt:        fixedTime,
		UpdatedAt:        fixedTime,
	}
	valid.ID = queueID(valid.EntityID, valid.Target, valid.ExpectedBaseHash)
	for name, mutate := range map[string]func(*QueueItem){
		"max-int": func(item *QueueItem) { item.Attempts = math.MaxInt },
		"updated-before-created": func(item *QueueItem) {
			item.UpdatedAt = item.CreatedAt.Add(-time.Nanosecond)
		},
		"not-before-created": func(item *QueueItem) {
			item.NotBefore = item.CreatedAt.Add(-time.Nanosecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			item := valid
			mutate(&item)
			writeRawQueueItemForTest(t, directory, item)
			started := time.Now()
			_, err = (Queue{Root: root, Retry: policy, Now: func() time.Time { return fixedTime.Add(time.Hour) }}).Ready(fixedTime.Add(time.Hour), 0)
			if err == nil {
				t.Fatalf("accepted invalid item: %+v", item)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("validation/backoff depended on attempts: %s", elapsed)
			}
		})
	}
}

func TestQueueReschedulePersistsMonotonicTimesAcrossClockRollback(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	now := fixedTime.Add(10 * time.Second)
	policy := RetryPolicy{Initial: 100 * time.Millisecond, Max: time.Second, InlineAttempts: 1, QueueAttempts: 4}
	queue := Queue{Root: root, Retry: policy, Now: func() time.Time { return now }}
	item, err := queue.Enqueue(QueueItem{Version: 1, EntityID: "decision-clock", Target: SideVault, ExpectedBaseHash: hash("base"), CreatedAt: fixedTime, UpdatedAt: fixedTime})
	if err != nil {
		t.Fatal(err)
	}
	first, err := queue.Reschedule(item.ID, string(QueueErrorSharingViolation))
	if err != nil {
		t.Fatal(err)
	}
	now = fixedTime.Add(5 * time.Second)
	second, err := queue.Reschedule(item.ID, string(QueueErrorLockViolation))
	if err != nil {
		t.Fatal(err)
	}
	if !second.UpdatedAt.Equal(first.UpdatedAt) || second.NotBefore.Before(first.NotBefore) || !second.NotBefore.Equal(first.UpdatedAt.Add(200*time.Millisecond)) {
		t.Fatalf("clock rollback moved durable times: first=%+v second=%+v", first, second)
	}
}

func TestQueueBlockedStateSurvivesRetryPolicyChanges(t *testing.T) {
	for _, test := range []struct {
		attempts int
		limit    int
	}{
		{attempts: 2, limit: 3},
		{attempts: 3, limit: 2},
	} {
		t.Run(fmt.Sprintf("attempts-%d-limit-%d", test.attempts, test.limit), func(t *testing.T) {
			directory := t.TempDir()
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			item := QueueItem{
				Version: 1, EntityID: "decision-policy-change", Target: SideVault, ExpectedBaseHash: hash("base"),
				Attempts: test.attempts, State: QueueBlocked, LastErrorClass: QueueErrorTransientWrite,
				CreatedAt: fixedTime, UpdatedAt: fixedTime.Add(time.Second), NotBefore: fixedTime.Add(time.Second),
			}
			item.ID = queueID(item.EntityID, item.Target, item.ExpectedBaseHash)
			writeRawQueueItemForTest(t, directory, item)
			policy := RetryPolicy{Initial: time.Millisecond, Max: time.Second, InlineAttempts: 1, QueueAttempts: test.limit}
			queue := Queue{Root: root, Retry: policy, Now: func() time.Time { return fixedTime.Add(time.Hour) }}
			ready, err := queue.Ready(fixedTime.Add(time.Hour), 0)
			if err != nil || len(ready) != 1 || ready[0].State != QueueBlocked {
				t.Fatalf("blocked state lost after policy change: ready=%+v err=%v", ready, err)
			}
			if _, err := queue.Reschedule(item.ID, string(QueueErrorSharingViolation)); err == nil {
				t.Fatal("rescheduled blocked item after policy change")
			}
		})
	}

	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	policy := RetryPolicy{Initial: time.Nanosecond, Max: time.Duration(math.MaxInt64), InlineAttempts: 1, QueueAttempts: math.MaxInt}
	started := time.Now()
	_, err = (Queue{Root: root, Retry: policy, Now: func() time.Time { return fixedTime }}).Ready(fixedTime, 0)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("unbounded queue retry policy error=%v elapsed=%s", err, time.Since(started))
	}
}

func TestQueuePendingBeyondLoweredPolicyIsPresentedBlocked(t *testing.T) {
	for _, test := range []struct {
		name      string
		attempts  int
		limit     int
		wantState QueueState
	}{
		{name: "lowered", attempts: 3, limit: 2, wantState: QueueBlocked},
		{name: "raised", attempts: 1, limit: 4, wantState: QueuePending},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			item := QueueItem{
				Version: 1, EntityID: "decision-pending-policy", Target: SideVault, ExpectedBaseHash: hash("base"),
				Attempts: test.attempts, State: QueuePending, LastErrorClass: QueueErrorTransientWrite,
				CreatedAt: fixedTime, UpdatedAt: fixedTime.Add(time.Second), NotBefore: fixedTime.Add(time.Second),
			}
			item.ID = queueID(item.EntityID, item.Target, item.ExpectedBaseHash)
			writeRawQueueItemForTest(t, directory, item)
			policy := RetryPolicy{Initial: time.Millisecond, Max: time.Second, InlineAttempts: 1, QueueAttempts: test.limit}
			queue := Queue{Root: root, Retry: policy, Now: func() time.Time { return fixedTime.Add(time.Hour) }}
			ready, err := queue.Ready(fixedTime.Add(time.Hour), 0)
			if err != nil || len(ready) != 1 || ready[0].State != test.wantState {
				t.Fatalf("policy-derived state=%+v err=%v", ready, err)
			}
			if test.wantState == QueueBlocked {
				if _, err := queue.Reschedule(item.ID, string(QueueErrorSharingViolation)); err == nil {
					t.Fatal("lowered-policy item was automatically retried")
				}
			}
		})
	}
}

func TestQueueErrorClassIsFixedAndCannotPersistCallerText(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	queue := Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return fixedTime }}
	item, err := queue.Enqueue(QueueItem{Version: 1, EntityID: "decision-error", Target: SideVault, ExpectedBaseHash: hash("base"), CreatedAt: fixedTime, UpdatedAt: fixedTime})
	if err != nil {
		t.Fatal(err)
	}
	secret := "secret_api_key_abcdef123456"
	if _, err := queue.Reschedule(item.ID, secret); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe error class result=%v", err)
	}
	if encoded := readQueueRecordForTest(t, root, item.ID); bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("unsafe error class persisted: %s", encoded)
	}
}

func TestQueueMutationsRejectReplacementAtAtomicPublicationCheckpoint(t *testing.T) {
	for _, operation := range []string{"enqueue", "reschedule", "ack"} {
		t.Run(operation, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			queue := Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return fixedTime }}
			item := QueueItem{Version: 1, EntityID: "decision-cas", Target: SideVault, ExpectedBaseHash: hash("base"), CreatedAt: fixedTime, UpdatedAt: fixedTime}
			if operation != "enqueue" {
				item, err = queue.Enqueue(item)
				if err != nil {
					t.Fatal(err)
				}
			}
			queue.beforeMutation = func(gotOperation string, checkpoint int, mutationRoot *os.Root, leaf string) error {
				if gotOperation == operation && checkpoint == 2 {
					replaceQueueLeafForTest(t, mutationRoot, leaf, []byte("THIRD-PARTY"))
				}
				return nil
			}
			switch operation {
			case "enqueue":
				_, err = queue.Enqueue(item)
			case "reschedule":
				_, err = queue.Reschedule(item.ID, string(QueueErrorSharingViolation))
			case "ack":
				err = queue.Ack(item.ID)
			}
			if err == nil || strings.Contains(err.Error(), "THIRD-PARTY") {
				t.Fatalf("mutation error=%v", err)
			}
			file, openErr := root.Open(queueRecordPath(queueID(item.EntityID, item.Target, item.ExpectedBaseHash)))
			if openErr != nil {
				t.Fatal(openErr)
			}
			got, readErr := io.ReadAll(file)
			closeErr := file.Close()
			if readErr != nil || closeErr != nil || !bytes.Equal(got, []byte("THIRD-PARTY")) {
				t.Fatalf("replacement overwritten or removed: got=%q read=%v close=%v", got, readErr, closeErr)
			}
		})
	}
}

func TestQueueConcurrentDifferentReschedulesCannotBothCommit(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	base := Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return fixedTime }}
	item, err := base.Enqueue(QueueItem{Version: 1, EntityID: "decision-concurrent", Target: SideVault, ExpectedBaseHash: hash("base"), CreatedAt: fixedTime, UpdatedAt: fixedTime})
	if err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{})
	release := make(chan struct{})
	first := base
	first.beforeMutation = func(operation string, checkpoint int, _ *os.Root, _ string) error {
		if operation == "reschedule" && checkpoint == 2 {
			close(reached)
			<-release
		}
		return nil
	}
	firstResult := make(chan error, 1)
	go func() {
		_, err := first.Reschedule(item.ID, string(QueueErrorSharingViolation))
		firstResult <- err
	}()
	<-reached
	_, secondErr := base.Reschedule(item.ID, string(QueueErrorLockViolation))
	close(release)
	firstErr := <-firstResult
	if (firstErr == nil) == (secondErr == nil) {
		t.Fatalf("exactly one contender must commit: first=%v second=%v", firstErr, secondErr)
	}
}

func TestQueueAckRechecksOwnedPublicationImmediatelyBeforeRemove(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	queue := Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return fixedTime }}
	item, err := queue.Enqueue(QueueItem{Version: 1, EntityID: "decision-ack-cas", Target: SideVault, ExpectedBaseHash: hash("base"), CreatedAt: fixedTime, UpdatedAt: fixedTime})
	if err != nil {
		t.Fatal(err)
	}
	queue.beforeMutation = func(operation string, checkpoint int, mutationRoot *os.Root, leaf string) error {
		if operation == "ack" && checkpoint == 4 {
			replaceQueueLeafForTest(t, mutationRoot, leaf, []byte("THIRD-PARTY-ACK"))
		}
		return nil
	}
	err = queue.Ack(item.ID)
	if err == nil || strings.Contains(err.Error(), "THIRD-PARTY-ACK") {
		t.Fatalf("ack error=%v", err)
	}
	file, err := root.Open(queueRecordPath(item.ID))
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, []byte("THIRD-PARTY-ACK")) {
		t.Fatalf("ack removed replacement: got=%q read=%v close=%v", got, readErr, closeErr)
	}
}

func TestQueueMutationFailureCleansOnlyAuthenticatedRollbackArtifacts(t *testing.T) {
	for _, operation := range []string{"reschedule", "ack"} {
		for _, checkpoint := range []int{2, 3} {
			t.Run(operation+"-checkpoint-"+strconv.Itoa(checkpoint), func(t *testing.T) {
				directory := t.TempDir()
				root, err := os.OpenRoot(directory)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				queue := Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return fixedTime }}
				item, err := queue.Enqueue(QueueItem{Version: 1, EntityID: "decision-artifact", Target: SideVault, ExpectedBaseHash: hash("base"), CreatedAt: fixedTime, UpdatedAt: fixedTime})
				if err != nil {
					t.Fatal(err)
				}
				original := readQueueRecordForTest(t, root, item.ID)
				queue.beforeMutation = func(gotOperation string, gotCheckpoint int, mutationRoot *os.Root, leaf string) error {
					if gotOperation == operation && gotCheckpoint == checkpoint {
						replaceQueueLeafForTest(t, mutationRoot, leaf, []byte("THIRD-PARTY-ARTIFACT"))
					}
					return nil
				}
				switch operation {
				case "reschedule":
					_, err = queue.Reschedule(item.ID, string(QueueErrorSharingViolation))
				case "ack":
					err = queue.Ack(item.ID)
				}
				if err == nil || strings.Contains(err.Error(), "THIRD-PARTY-ARTIFACT") {
					t.Fatalf("mutation error=%v", err)
				}
				entries, readDirErr := os.ReadDir(directory)
				if readDirErr != nil || len(entries) != 1 || entries[0].Name() != queueRecordPath(item.ID) {
					t.Fatalf("mutation artifacts=%v err=%v", entryNamesForTest(entries), readDirErr)
				}
				if got, readErr := os.ReadFile(filepath.Join(directory, queueRecordPath(item.ID))); readErr != nil || string(got) != "THIRD-PARTY-ARTIFACT" {
					t.Fatalf("third-party target=%q err=%v", got, readErr)
				}
				replaceQueueLeafForTest(t, root, queueRecordPath(item.ID), original)
				queue.beforeMutation = nil
				ready, readyErr := queue.Ready(fixedTime.Add(time.Hour), 0)
				if readyErr != nil || len(ready) != 1 || ready[0].ID != item.ID {
					t.Fatalf("queue did not recover after original restoration: ready=%+v err=%v", ready, readyErr)
				}
			})
		}
	}
}

func TestQueueMutationFailurePreservesRecoveryBackupForMissingOrZeroPrimary(t *testing.T) {
	for _, operation := range []string{"reschedule", "ack"} {
		for _, checkpoint := range []int{2, 3} {
			for _, primaryState := range []string{"missing", "zero"} {
				t.Run(fmt.Sprintf("%s-checkpoint-%d-%s", operation, checkpoint, primaryState), func(t *testing.T) {
					directory := t.TempDir()
					root, err := os.OpenRoot(directory)
					if err != nil {
						t.Fatal(err)
					}
					defer root.Close()
					queue := Queue{Root: root, Retry: DefaultRetryPolicy(), Now: func() time.Time { return fixedTime }}
					item, err := queue.Enqueue(QueueItem{Version: 1, EntityID: "decision-recovery", Target: SideVault, ExpectedBaseHash: hash("base"), CreatedAt: fixedTime, UpdatedAt: fixedTime})
					if err != nil {
						t.Fatal(err)
					}
					original := readQueueRecordForTest(t, root, item.ID)
					queue.beforeMutation = func(gotOperation string, gotCheckpoint int, mutationRoot *os.Root, leaf string) error {
						if gotOperation != operation || gotCheckpoint != checkpoint {
							return nil
						}
						if primaryState == "missing" {
							return mutationRoot.Remove(leaf)
						}
						replaceQueueLeafForTest(t, mutationRoot, leaf, nil)
						return nil
					}
					switch operation {
					case "reschedule":
						_, err = queue.Reschedule(item.ID, string(QueueErrorSharingViolation))
					case "ack":
						err = queue.Ack(item.ID)
					}
					if !errors.Is(err, ErrQueueRecoveryRequired) {
						t.Fatalf("error=%v", err)
					}
					backup := atomicfile.BackupPath(queueRecordPath(item.ID))
					if got, readErr := os.ReadFile(filepath.Join(directory, backup)); readErr != nil || !bytes.Equal(got, original) {
						t.Fatalf("recovery backup=%q err=%v", got, readErr)
					}
					digest := sha256.Sum256(original)
					if err := atomicfile.RecoverRootFileRollback(root, queueRecordPath(item.ID), fmt.Sprintf("%x", digest[:])); err != nil {
						t.Fatal(err)
					}
					entries, readDirErr := os.ReadDir(directory)
					if readDirErr != nil || len(entries) != 1 || entries[0].Name() != queueRecordPath(item.ID) {
						t.Fatalf("recovery entries=%v err=%v", entryNamesForTest(entries), readDirErr)
					}
					queue.beforeMutation = nil
					ready, readyErr := queue.Ready(fixedTime.Add(time.Hour), 0)
					if readyErr != nil || len(ready) != 1 || ready[0].ID != item.ID {
						t.Fatalf("recovered queue=%+v err=%v", ready, readyErr)
					}
				})
			}
		}
	}
}

func queueEntityIDs(items []QueueItem) []string {
	result := make([]string, len(items))
	for index := range items {
		result[index] = items[index].EntityID
	}
	return result
}

func readQueueRecordForTest(t *testing.T, root *os.Root, id string) []byte {
	t.Helper()
	file, err := root.Open(queueRecordPath(id))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoded, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func writeRawQueueItemForTest(t *testing.T, directory string, item QueueItem) {
	t.Helper()
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, queueRecordPath(item.ID)), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func replaceQueueLeafForTest(t *testing.T, root *os.Root, leaf string, content []byte) {
	t.Helper()
	if err := root.Remove(leaf); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	file, err := root.OpenFile(leaf, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func entryNamesForTest(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}
