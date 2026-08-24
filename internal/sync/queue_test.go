package sync

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
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
		item, err := queue.Enqueue(QueueItem{Version: 1, EntityID: id, Target: SideProject, ExpectedBaseHash: hash("base"), CreatedAt: fixedTime.Add(time.Duration(index) * time.Second), UpdatedAt: fixedTime})
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
		now = fixedTime.Add(time.Duration(attempt) * time.Second)
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
