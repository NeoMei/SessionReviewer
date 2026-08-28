package reviewjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
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
		"review-jobs/jobs/job-1.json", "review-jobs/projects/project-1.json", "review-jobs/locks/store.lock",
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

func TestStoreRejectsUnsafeIDsAndMutationOfStableIdentity(t *testing.T) {
	root := newStoreRoot(t)
	store := Store{Root: root}
	for _, id := range []string{"../escape", "Job-1", "CON", strings.Repeat("a", 129)} {
		if _, _, _, err := store.Load(id); err == nil {
			t.Fatalf("Load(%q) accepted unsafe ID", id)
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
