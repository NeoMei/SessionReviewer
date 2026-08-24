package sync

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/platform"
)

func TestEventGateSuppressesSelfLoopAndDebouncesRapidSaves(t *testing.T) {
	lookup := &fakeHashLookup{
		entities: map[string]string{"decisions/d1.md": "decision-1"},
		hashes:   map[string]string{"decision-1|project": "written-hash"},
	}
	gate := NewEventGate(200*time.Millisecond, lookup)
	if got, err := gate.Observe(FileEvent{Side: SideProject, RelativePath: "decisions/d1.md", ObservedHash: "written-hash", At: fixedTime}); err != nil || got != EventIgnoredSelf {
		t.Fatalf("self disposition=%s err=%v", got, err)
	}
	if got, err := gate.Observe(FileEvent{Side: SideVault, RelativePath: "decisions/d1.md", ObservedHash: "edit-1", At: fixedTime}); err != nil || got != EventDebounced {
		t.Fatalf("human disposition=%s err=%v", got, err)
	}
	if got, err := gate.Observe(FileEvent{Side: SideVault, RelativePath: "decisions/d1.md", ObservedHash: "edit-2", At: fixedTime.Add(100 * time.Millisecond)}); err != nil || got != EventDebounced {
		t.Fatalf("second disposition=%s err=%v", got, err)
	}
	if ready := gate.Ready(fixedTime.Add(299 * time.Millisecond)); len(ready) != 0 {
		t.Fatalf("early=%v", ready)
	}
	if ready := gate.Ready(fixedTime.Add(300 * time.Millisecond)); !reflect.DeepEqual(ready, []string{"decision-1"}) {
		t.Fatalf("ready=%v", ready)
	}
	if ready := gate.Ready(fixedTime.Add(time.Hour)); len(ready) != 0 {
		t.Fatalf("ready item was not removed: %v", ready)
	}
}

func TestEventGateNormalizesUnicodeCaseAndIgnoresOperationalArtifacts(t *testing.T) {
	canonical, err := platform.PathKey("windows", platform.CaseInsensitive, "Decisions/Café.md")
	if err != nil {
		t.Fatal(err)
	}
	lookup := &fakeHashLookup{entities: map[string]string{canonical: "decision-cafe"}, hashes: map[string]string{}}
	gate := NewEventGate(100*time.Millisecond, lookup)

	for index, relative := range []string{"Decisions/Café.md", "DECISIONS/Cafe\u0301.md"} {
		got, err := gate.Observe(FileEvent{Side: SideProject, RelativePath: relative, ObservedHash: "human", At: fixedTime.Add(time.Duration(index) * 50 * time.Millisecond)})
		if err != nil || got != EventDebounced {
			t.Fatalf("observe %q disposition=%s err=%v", relative, got, err)
		}
	}
	if ready := gate.Ready(fixedTime.Add(149 * time.Millisecond)); len(ready) != 0 {
		t.Fatalf("unicode/case event became ready early: %v", ready)
	}
	if ready := gate.Ready(fixedTime.Add(150 * time.Millisecond)); !reflect.DeepEqual(ready, []string{"decision-cafe"}) {
		t.Fatalf("unicode/case coalescing=%v", ready)
	}

	temporary := ".session-reviewer-" + strings.Repeat("a", 32)
	for _, relative := range []string{
		"sync-conflicts/decision-cafe.md",
		"SYNC-CONFLICTS/decision-cafe.md",
		"decisions/" + temporary,
		"decisions/d1.md" + strings.TrimPrefix(atomicfile.BackupPath("x"), "x"),
	} {
		got, err := gate.Observe(FileEvent{Side: SideVault, RelativePath: relative, ObservedHash: "human", At: fixedTime})
		if err != nil || got != EventIgnoredSelf {
			t.Fatalf("artifact %q disposition=%s err=%v", relative, got, err)
		}
	}
	if lookup.entityCalls != 2 {
		t.Fatalf("ignored paths reached lookup: calls=%d", lookup.entityCalls)
	}
}

func TestEventGateSuppressesOnlyVerifiedHashForObservedSide(t *testing.T) {
	lookup := &fakeHashLookup{
		entities: map[string]string{"decisions/d1.md": "decision-1"},
		hashes: map[string]string{
			"decision-1|project": "project-written",
			"decision-1|vault":   "vault-written",
		},
	}
	gate := NewEventGate(time.Second, lookup)
	for _, event := range []FileEvent{
		{Side: SideVault, RelativePath: "decisions/d1.md", ObservedHash: "project-written", At: fixedTime},
		{Side: SideProject, RelativePath: "decisions/d1.md", ObservedHash: "different", At: fixedTime.Add(time.Second)},
	} {
		got, err := gate.Observe(event)
		if err != nil || got != EventDebounced {
			t.Fatalf("unverified event=%+v disposition=%s err=%v", event, got, err)
		}
	}
	if got, err := gate.Observe(FileEvent{Side: SideVault, RelativePath: "decisions/d1.md", ObservedHash: "vault-written", At: fixedTime}); err != nil || got != EventIgnoredSelf {
		t.Fatalf("verified vault disposition=%s err=%v", got, err)
	}
}

func TestEventGateConcurrentObserveIsSafeAndReadyIsSorted(t *testing.T) {
	lookup := &fakeHashLookup{entities: map[string]string{
		"decisions/a.md": "decision-a",
		"decisions/z.md": "decision-z",
	}, hashes: map[string]string{}}
	gate := NewEventGate(25*time.Millisecond, lookup)
	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			name := "a"
			if index%2 == 0 {
				name = "z"
			}
			_, _ = gate.Observe(FileEvent{Side: SideProject, RelativePath: "DECISIONS/" + name + ".md", ObservedHash: "human", At: fixedTime})
		}(index)
	}
	wait.Wait()
	if ready := gate.Ready(fixedTime.Add(25 * time.Millisecond)); !reflect.DeepEqual(ready, []string{"decision-a", "decision-z"}) {
		t.Fatalf("sorted concurrent ready=%v", ready)
	}
}

func TestEventGateRejectsInvalidEventsWithoutContentInErrors(t *testing.T) {
	lookup := &fakeHashLookup{entities: map[string]string{}, hashes: map[string]string{}}
	for _, event := range []FileEvent{
		{Side: Side("other"), RelativePath: "decisions/d1.md", ObservedHash: "CANARY-CONTENT", At: fixedTime},
		{Side: SideProject, RelativePath: "../CANARY-CONTENT", ObservedHash: "hash", At: fixedTime},
		{Side: SideProject, RelativePath: "decisions/d1.md", ObservedHash: "hash", At: time.Time{}},
	} {
		_, err := NewEventGate(time.Second, lookup).Observe(event)
		if err == nil || strings.Contains(err.Error(), "CANARY-CONTENT") {
			t.Fatalf("event=%+v error=%v", event, err)
		}
	}
	if _, err := NewEventGate(-time.Second, lookup).Observe(FileEvent{Side: SideProject, RelativePath: "decisions/d1.md", ObservedHash: "hash", At: fixedTime}); err == nil {
		t.Fatal("negative debounce window accepted")
	}
	if _, err := NewEventGate(time.Second, nil).Observe(FileEvent{Side: SideProject, RelativePath: "decisions/d1.md", ObservedHash: "hash", At: fixedTime}); err == nil {
		t.Fatal("nil hash lookup accepted")
	}
}

func TestEventGateAcceptsLocalTimeAndMissingObservedHash(t *testing.T) {
	lookup := &fakeHashLookup{entities: map[string]string{"decisions/d1.md": "decision-1"}, hashes: map[string]string{}}
	local := fixedTime.In(time.FixedZone("local", 8*60*60))
	gate := NewEventGate(time.Second, lookup)
	got, err := gate.Observe(FileEvent{Side: SideProject, RelativePath: "decisions/d1.md", At: local})
	if err != nil || got != EventDebounced {
		t.Fatalf("missing-file event disposition=%s err=%v", got, err)
	}
	if ready := gate.Ready(local.Add(time.Second)); !reflect.DeepEqual(ready, []string{"decision-1"}) {
		t.Fatalf("missing-file event ready=%v", ready)
	}
}

type fakeHashLookup struct {
	mu          sync.Mutex
	entities    map[string]string
	hashes      map[string]string
	entityCalls int
}

func (lookup *fakeHashLookup) EntityForPath(_ Side, path string) (string, bool, error) {
	lookup.mu.Lock()
	defer lookup.mu.Unlock()
	lookup.entityCalls++
	value, ok := lookup.entities[path]
	return value, ok, nil
}

func (lookup *fakeHashLookup) LastWrittenHash(entity string, side Side) (string, bool, error) {
	lookup.mu.Lock()
	defer lookup.mu.Unlock()
	value, ok := lookup.hashes[entity+"|"+string(side)]
	return value, ok, nil
}
