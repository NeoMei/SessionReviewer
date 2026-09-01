package scan

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectview"
)

func TestObservationSpoolIsPrivateCanonicalBoundedAndCleaned(t *testing.T) {
	harness := newScanHarness(t)
	observation := harness.addSource(1, memory.Indexed, scanTestProject).observations[0]
	spools, err := openObservationSpools(context.Background(), harness.options.DataRoot, scanTestProject, nil)
	if err != nil {
		t.Fatal(err)
	}
	spool, err := spools.create(context.Background(), "codex", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.append(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if err := spool.seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		runInfo, err := os.Stat(spools.run.Path)
		if err != nil || runInfo.Mode().Perm() != 0o700 {
			t.Fatalf("spool run mode=%v err=%v", runInfo.Mode().Perm(), err)
		}
		fileInfo, err := os.Stat(filepath.Join(spools.run.Path, spool.leaf))
		if err != nil || fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("spool file mode=%v err=%v", fileInfo.Mode().Perm(), err)
		}
	}
	var replayed []memory.ObservationRevision
	if err := spool.replay(context.Background(), func(value memory.ObservationRevision) error {
		replayed = append(replayed, value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 || replayed[0].RevisionID != observation.RevisionID {
		t.Fatalf("canonical replay=%+v", replayed)
	}
	spools.maxBytes = 1
	overflow, err := spools.create(context.Background(), "codex", "session-2")
	if err != nil {
		t.Fatal(err)
	}
	overflowObservation := harness.addSource(2, memory.Indexed, scanTestProject).observations[0]
	if err := overflow.append(context.Background(), overflowObservation); !errors.Is(err, ErrObservationBudget) {
		t.Fatalf("byte overflow error=%v", err)
	}
	if err := spools.close(); err != nil {
		t.Fatal(err)
	}
	assertNoObservationSpools(t, harness.options.DataRoot, scanTestProject)
}

func TestObservationSpoolStartupCleansOnlyPrivateStaleRun(t *testing.T) {
	dataRoot := t.TempDir()
	stale := filepath.Join(dataRoot, observationSpoolNamespace, scanTestProject, "run-stale")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "source-dead.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spools, err := openObservationSpools(context.Background(), dataRoot, scanTestProject, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale run survived startup: %v", err)
	}
	if err := spools.close(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationSpoolStaleCleanupFailsClosedOnRedirect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unprivileged symlink creation is not portable on Windows")
	}
	dataRoot := t.TempDir()
	namespace := filepath.Join(dataRoot, observationSpoolNamespace, scanTestProject)
	if err := os.MkdirAll(namespace, 0o700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	marker := filepath.Join(external, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	redirect := filepath.Join(namespace, "run-redirect")
	if err := os.Symlink(external, redirect); err != nil {
		t.Fatal(err)
	}
	if _, err := openObservationSpools(context.Background(), dataRoot, scanTestProject, nil); err == nil || !strings.Contains(err.Error(), "redirected") {
		t.Fatalf("redirected stale spool was accepted: %v", err)
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "keep" {
		t.Fatalf("stale cleanup escaped private namespace: body=%q err=%v", body, err)
	}
}

func TestObservationSpoolRejectsSameSizeInPlaceMutationDuringReplay(t *testing.T) {
	harness := newScanHarness(t)
	observation := harness.addSource(1, memory.Indexed, scanTestProject).observations[0]
	spools, err := openObservationSpools(context.Background(), harness.options.DataRoot, scanTestProject, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer spools.close()
	spool, err := spools.create(context.Background(), observation.Key.Provider, observation.Key.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.append(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if err := spool.seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(spools.run.Path, spool.leaf)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	err = spool.replay(context.Background(), func(memory.ObservationRevision) error {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		mutated := bytes.Replace(body, []byte(`"outcome":"success"`), []byte(`"outcome":"failure"`), 1)
		if len(mutated) != len(body) || bytes.Equal(mutated, body) {
			return errors.New("test mutation did not preserve spool size")
		}
		if writeErr := os.WriteFile(path, mutated, 0o600); writeErr != nil {
			return writeErr
		}
		return os.Chtimes(path, before.ModTime().Add(time.Hour), before.ModTime().Add(time.Hour))
	})
	if err == nil || !strings.Contains(err.Error(), "changed during replay") {
		t.Fatalf("same-size in-place spool mutation was accepted: %v", err)
	}
}

func TestRunCleansObservationSpoolsOnCancellationErrorAndPanic(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*scanHarness, context.CancelFunc)
		wantPanic bool
	}{
		{name: "cancellation", configure: func(harness *scanHarness, cancel context.CancelFunc) {
			base := harness.options.Reduce
			harness.options.Reduce = func(input projectview.Input) (memory.ProjectView, bool, error) {
				view, changed, err := base(input)
				cancel()
				return view, changed, err
			}
		}},
		{name: "error", configure: func(harness *scanHarness, _ context.CancelFunc) {
			harness.options.Reduce = func(projectview.Input) (memory.ProjectView, bool, error) {
				return memory.ProjectView{}, false, errors.New("injected reduce failure")
			}
		}},
		{name: "panic", wantPanic: true, configure: func(harness *scanHarness, _ context.CancelFunc) {
			harness.options.Reduce = func(projectview.Input) (memory.ProjectView, bool, error) {
				panic("injected reduce panic")
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newScanHarness(t)
			harness.addSource(1, memory.Indexed, scanTestProject)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			test.configure(&harness, cancel)
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				_, _ = Run(ctx, harness.options)
			}()
			if panicked != test.wantPanic {
				t.Fatalf("panic=%v want=%v", panicked, test.wantPanic)
			}
			assertNoObservationSpools(t, harness.options.DataRoot, scanTestProject)
		})
	}
}

func TestRunReplaysAtMostOneObservationSourcePayload(t *testing.T) {
	harness := newScanHarness(t)
	for index := 1; index <= 4; index++ {
		harness.addSource(index, memory.Indexed, scanTestProject)
	}
	maxResident := 0
	harness.options.spoolObserver = func(stats observationSpoolStats) {
		if stats.ResidentSources > maxResident {
			maxResident = stats.ResidentSources
		}
	}
	result, err := Run(context.Background(), harness.options)
	if err != nil || !result.Prepared {
		t.Fatalf("scan result=%+v err=%v", result, err)
	}
	if maxResident != 1 {
		t.Fatalf("resident source payloads=%d want exactly one", maxResident)
	}
	assertNoObservationSpools(t, harness.options.DataRoot, scanTestProject)
}

func assertNoObservationSpools(t *testing.T, dataRoot, projectID string) {
	t.Helper()
	root := filepath.Join(dataRoot, observationSpoolNamespace, projectID)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("observation spool leaked entries: %v", entries)
	}
}
