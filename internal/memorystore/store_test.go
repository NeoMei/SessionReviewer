package memorystore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestStoreCreatesPrivateRootedLayoutAndRoundTripsCanonicalObjects(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fixture := buildStoredFixture(t, store, "generation-1")
	prepared, err := store.PrepareGeneration(fixture.manifest)
	if err != nil {
		t.Fatalf("prepare generation: %v", err)
	}
	wantManifestDigest, err := memory.Digest(fixture.manifest)
	if err != nil {
		t.Fatalf("digest manifest: %v", err)
	}
	if prepared != (Prepared{GenerationID: "generation-1", ManifestDigest: wantManifestDigest, ProjectViewDigest: fixture.project.Digest}) {
		t.Fatalf("unexpected prepared pointer: %#v", prepared)
	}

	loaded, manifest, err := store.LoadPrepared()
	if err != nil {
		t.Fatalf("load prepared generation: %v", err)
	}
	if loaded != prepared || manifest.GenerationID != fixture.manifest.GenerationID || manifest.ProjectViewDigest != fixture.manifest.ProjectViewDigest {
		t.Fatalf("prepared round trip changed: %#v %#v", loaded, manifest)
	}
	body, err := store.LoadObject(ObjectSessionView, fixture.session.Digest)
	if err != nil {
		t.Fatalf("load session object: %v", err)
	}
	var session memory.SessionView
	if err := json.Unmarshal(bytes.TrimSpace(body), &session); err != nil || session.Digest != fixture.session.Digest {
		t.Fatalf("invalid loaded session: %#v, %v", session, err)
	}

	memoryRoot := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1")
	directories := []string{
		filepath.Join(dataRoot, "source-catalog"),
		filepath.Join(dataRoot, "projects"),
		filepath.Join(dataRoot, "projects", testProjectID),
		memoryRoot,
	}
	for _, name := range []string{"observations", "sessions", "project-probes", "project-views", "generations", "diagnostics", "staging", "locks"} {
		directories = append(directories, filepath.Join(memoryRoot, name))
	}
	if runtime.GOOS != "windows" {
		for _, path := range directories {
			assertMode(t, path, 0o700)
		}
		files, err := regularFilesUnder(dataRoot)
		if err != nil {
			t.Fatalf("inventory store files: %v", err)
		}
		for _, path := range files {
			assertMode(t, path, 0o600)
		}
	}
	if _, err := os.Lstat(filepath.Join(memoryRoot, "published_generation")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Gate A created published_generation: %v", err)
	}
}

func TestStoreRejectsRootEscapeProjectFragmentsAndRedirects(t *testing.T) {
	t.Run("absolute root required", func(t *testing.T) {
		if _, err := Open("relative-data", testProjectID); err == nil {
			t.Fatal("relative data root was accepted")
		}
	})
	for _, projectID := range []string{"../escape", "project/escape", `project\\escape`, ".", "CON", "project id"} {
		t.Run(projectID, func(t *testing.T) {
			if _, err := Open(t.TempDir(), projectID); err == nil {
				t.Fatalf("unsafe project ID %q was accepted", projectID)
			}
		})
	}

	if runtime.GOOS == "windows" {
		t.Skip("ordinary symlink setup requires Windows privileges; reparse rejection is exercised by pathguard")
	}
	t.Run("layout redirect", func(t *testing.T) {
		dataRoot := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(dataRoot, "projects")); err != nil {
			t.Fatalf("create projects symlink: %v", err)
		}
		if _, err := Open(dataRoot, testProjectID); err == nil {
			t.Fatal("redirected projects directory was accepted")
		}
		if _, err := os.Lstat(filepath.Join(target, testProjectID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("redirect target was mutated: %v", err)
		}
	})
	t.Run("object redirect", func(t *testing.T) {
		dataRoot := t.TempDir()
		store, err := Open(dataRoot, testProjectID)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer store.Close()
		outside := t.TempDir()
		sessions := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", "sessions")
		if err := os.Rename(sessions, sessions+"-moved"); err != nil {
			t.Fatalf("move sessions directory: %v", err)
		}
		if err := os.Symlink(outside, sessions); err != nil {
			t.Fatalf("replace sessions with redirect: %v", err)
		}
		session := validSessionView(t, prefixedDigest("chunk"))
		if _, err := store.PutSessionView(session); err == nil {
			t.Fatal("write through replaced sessions directory succeeded")
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Fatalf("redirect target was touched: %v, entries=%v", err, entries)
		}
	})
}

func TestStoreAcceptsExactReplayAndRejectsDifferentBytesAtDigest(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	session := validSessionView(t, prefixedDigest("chunk"))
	digest, err := store.PutSessionView(session)
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	path := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", "sessions", digestLeaf(digest, ".json"))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored session: %v", err)
	}
	if replay, err := store.PutSessionView(session); err != nil || replay != digest {
		t.Fatalf("exact replay failed: digest=%q err=%v", replay, err)
	}
	afterReplay, _ := os.ReadFile(path)
	if !bytes.Equal(original, afterReplay) {
		t.Fatal("exact replay rewrote immutable bytes")
	}

	tampered := append([]byte(" \n"), original...)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("tamper object: %v", err)
	}
	if _, err := store.PutSessionView(session); err == nil {
		t.Fatal("different bytes at an existing digest were accepted")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, tampered) {
		t.Fatal("conflicting immutable object was overwritten")
	}
}

func TestStoreNoReplaceCollisionNeverOverwritesWinner(t *testing.T) {
	t.Run("content addressed object", func(t *testing.T) {
		dataRoot := t.TempDir()
		store, err := Open(dataRoot, testProjectID)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer store.Close()
		session := validSessionView(t, prefixedDigest("chunk"))
		destination := filepath.Join(store.memory.Path, "sessions", digestLeaf(session.Digest, ".json"))
		winner := []byte("WINNER-CONTENT")
		var publish sync.Once
		store.objectCheckpoint = func() error {
			publish.Do(func() {
				if err := os.WriteFile(destination, winner, 0o600); err != nil {
					t.Errorf("publish racing object winner: %v", err)
				}
			})
			return nil
		}
		if _, err := store.PutSessionView(session); !errors.Is(err, ErrImmutableConflict) {
			t.Fatalf("collision error = %v, want immutable conflict", err)
		}
		got, err := os.ReadFile(destination)
		if err != nil || !bytes.Equal(got, winner) {
			t.Fatalf("racing object winner was overwritten: body=%q err=%v", got, err)
		}
	})

	t.Run("generation object", func(t *testing.T) {
		dataRoot := t.TempDir()
		store, err := Open(dataRoot, testProjectID)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer store.Close()
		fixture := buildStoredFixture(t, store, "generation-race")
		destination := filepath.Join(store.memory.Path, "generations", fixture.manifest.GenerationID+".json")
		winner := []byte("GENERATION-WINNER")
		var publish sync.Once
		store.objectCheckpoint = func() error {
			publish.Do(func() {
				if err := os.WriteFile(destination, winner, 0o600); err != nil {
					t.Errorf("publish racing generation winner: %v", err)
				}
			})
			return nil
		}
		if _, err := store.PrepareGeneration(fixture.manifest); !errors.Is(err, ErrImmutableConflict) {
			t.Fatalf("generation collision error = %v, want immutable conflict", err)
		}
		got, err := os.ReadFile(destination)
		if err != nil || !bytes.Equal(got, winner) {
			t.Fatalf("racing generation winner was overwritten: body=%q err=%v", got, err)
		}
		if _, err := os.Lstat(filepath.Join(store.memory.Path, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("generation collision committed manifest.json: %v", err)
		}
	})
}

func TestStoreRejectsInvalidOrCrossProjectRecords(t *testing.T) {
	store, err := Open(t.TempDir(), testProjectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	session := validSessionView(t, prefixedDigest("chunk"))
	session.ProjectID = "project-2"
	session.Digest, _ = memory.SessionViewDigest(session)
	if _, err := store.PutSessionView(session); err == nil {
		t.Fatal("cross-project SessionView was stored")
	}
	session = validSessionView(t, prefixedDigest("chunk"))
	session.Digest = prefixedDigest("forged")
	if _, err := store.PutSessionView(session); err == nil {
		t.Fatal("invalid SessionView was stored")
	}
	observation := validObservation()
	observation.Excerpt = strings.Repeat("x", 1025)
	observation.RevisionID = memory.ObservationRevisionID(observation)
	if _, err := store.PutObservationChunk([]memory.ObservationRevision{observation}); err == nil {
		t.Fatal("invalid observation was stored")
	}
}

func TestStoreInterruptedObjectWriteLeavesNoObjectOrTemporary(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	store.objectCheckpoint = func() error { return errInjectedCrash }
	session := validSessionView(t, prefixedDigest("chunk"))
	if _, err := store.PutSessionView(session); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("before-publish failure = %v, want injected crash", err)
	}
	sessions := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", "sessions")
	entries, err := os.ReadDir(sessions)
	if err != nil {
		t.Fatalf("read sessions directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".session-reviewer-") || strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("interrupted write left artifact %q", entry.Name())
		}
	}
}

func TestPreparedGenerationRejectsCorruptManifestBackupAndDuplicateJSON(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		dataRoot, store, fixture := preparedStore(t)
		defer store.Close()
		manifestPath := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", "manifest.json")
		body, _ := os.ReadFile(manifestPath)
		duplicate := bytes.Replace(body, []byte(`"generation_id":`), []byte(`"generation_id":"other","generation_id":`), 1)
		if err := os.WriteFile(manifestPath, duplicate, 0o600); err != nil {
			t.Fatalf("write duplicate manifest: %v", err)
		}
		if _, _, err := store.LoadPrepared(); err == nil {
			t.Fatal("duplicate manifest fields were accepted")
		}
		generationPath := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", "generations", fixture.manifest.GenerationID+".json")
		generation, _ := os.ReadFile(generationPath)
		generation = bytes.Replace(generation, []byte(`"project_id":`), []byte(`"project_id":"other","project_id":`), 1)
		if err := os.WriteFile(generationPath, generation, 0o600); err != nil {
			t.Fatalf("write duplicate generation: %v", err)
		}
		if _, _, err := store.LoadPrepared(); err == nil {
			t.Fatal("duplicate generation fields were accepted")
		}
	})

	t.Run("backup", func(t *testing.T) {
		dataRoot, store, _ := preparedStore(t)
		defer store.Close()
		backupPath := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", atomicfile.BackupPath("manifest.json"))
		if err := os.WriteFile(backupPath, []byte(`{"generation_id":"corrupt"}`), 0o600); err != nil {
			t.Fatalf("write corrupt backup: %v", err)
		}
		if _, _, err := store.LoadPrepared(); err == nil {
			t.Fatal("corrupt rollback backup was ignored")
		}
	})

	t.Run("object", func(t *testing.T) {
		dataRoot, store, fixture := preparedStore(t)
		defer store.Close()
		path := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", "sessions", digestLeaf(fixture.session.Digest, ".json"))
		body, _ := os.ReadFile(path)
		body = bytes.Replace(body, []byte(`"project_id":`), []byte(`"project_id":"other","project_id":`), 1)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("write duplicate object: %v", err)
		}
		if _, err := store.LoadObject(ObjectSessionView, fixture.session.Digest); err == nil {
			t.Fatal("duplicate object fields were accepted")
		}
	})
}

func TestPreparedGenerationRejectsBrokenTransitiveGraph(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Store, *storedFixture)
	}{
		{
			name: "session source record absent from manifest",
			mutate: func(_ *testing.T, _ *Store, fixture *storedFixture) {
				fixture.manifest.SourceRecordDigests[0] = prefixedDigest("other-source-record")
			},
		},
		{
			name: "session chunk absent from manifest",
			mutate: func(t *testing.T, store *Store, fixture *storedFixture) {
				other := fixture.observation
				other.Key.Sequence++
				other.Object = "other focused tests"
				other.RevisionID = memory.ObservationRevisionID(other)
				digest, err := store.PutObservationChunk([]memory.ObservationRevision{other})
				if err != nil {
					t.Fatalf("put unrelated manifest chunk: %v", err)
				}
				fixture.manifest.ObservationChunkDigests[0] = digest
			},
		},
		{
			name: "session active revision absent from its chunks",
			mutate: func(t *testing.T, store *Store, fixture *storedFixture) {
				missing := prefixedDigest("missing-session-revision")
				session := fixture.session
				session.ActiveRevisionIDs = []string{missing}
				session.ObservationSummaries[0].RevisionID = missing
				session.Digest, _ = memory.SessionViewDigest(session)
				if _, err := store.PutSessionView(session); err != nil {
					t.Fatalf("put mismatched SessionView: %v", err)
				}
				dependency := memory.SessionViewDependency{Provider: session.Provider, SessionID: session.SessionID, Digest: session.Digest}
				fixture.manifest.SessionViews = []memory.SessionViewDependency{dependency}
				fixture.manifest.ActiveRevisions = map[string]string{observationKeyDigest(t, fixture.observation.Key): missing}
				projectView := fixture.project
				projectView.SessionViewDependencies = []memory.SessionViewDependency{dependency}
				projectView.ObservationRevisionIDs = []string{missing}
				projectView.Digest, _ = memory.ProjectViewDigest(projectView)
				if _, err := store.PutProjectView(projectView); err != nil {
					t.Fatalf("put mismatched ProjectView: %v", err)
				}
				fixture.manifest.ProjectViewDigest = projectView.Digest
			},
		},
		{
			name: "project ordered session dependencies mismatch manifest",
			mutate: func(t *testing.T, store *Store, fixture *storedFixture) {
				projectView := fixture.project
				projectView.SessionViewDependencies = []memory.SessionViewDependency{{Provider: "codex", SessionID: "other-session", Digest: prefixedDigest("other-session-view")}}
				projectView.Digest, _ = memory.ProjectViewDigest(projectView)
				if _, err := store.PutProjectView(projectView); err != nil {
					t.Fatalf("put project dependency mismatch: %v", err)
				}
				fixture.manifest.ProjectViewDigest = projectView.Digest
			},
		},
		{
			name: "project probe digest mismatch manifest",
			mutate: func(t *testing.T, store *Store, fixture *storedFixture) {
				projectView := fixture.project
				projectView.ProbeStateDigest = prefixedDigest("other-probe")
				projectView.Digest, _ = memory.ProjectViewDigest(projectView)
				if _, err := store.PutProjectView(projectView); err != nil {
					t.Fatalf("put project probe mismatch: %v", err)
				}
				fixture.manifest.ProjectViewDigest = projectView.Digest
			},
		},
		{
			name: "active key does not classify its observation",
			mutate: func(_ *testing.T, _ *Store, fixture *storedFixture) {
				fixture.manifest.ActiveRevisions = map[string]string{prefixedDigest("wrong-observation-key"): fixture.observation.RevisionID}
			},
		},
		{
			name: "active revision is missing",
			mutate: func(t *testing.T, _ *Store, fixture *storedFixture) {
				fixture.manifest.ActiveRevisions = map[string]string{observationKeyDigest(t, fixture.observation.Key): prefixedDigest("missing-active")}
			},
		},
		{
			name: "superseded predecessor is missing",
			mutate: func(_ *testing.T, _ *Store, fixture *storedFixture) {
				fixture.manifest.SupersededRevisions = map[string]string{prefixedDigest("missing-predecessor"): fixture.observation.RevisionID}
			},
		},
		{
			name: "withdrawn revision is missing",
			mutate: func(_ *testing.T, _ *Store, fixture *storedFixture) {
				fixture.manifest.WithdrawnRevisions = map[string]string{prefixedDigest("withdrawn-key"): prefixedDigest("missing-withdrawn")}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataRoot := t.TempDir()
			store, err := Open(dataRoot, testProjectID)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer store.Close()
			fixture := buildStoredFixture(t, store, "generation-1")
			test.mutate(t, store, &fixture)
			if err := memory.ValidateGenerationManifest(fixture.manifest); err != nil {
				t.Fatalf("fixture must pass Task-1 validation: %v", err)
			}
			if _, err := store.PrepareGeneration(fixture.manifest); err == nil {
				t.Fatal("broken transitive graph was prepared")
			}
			manifestPath := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", "manifest.json")
			if _, err := os.Lstat(manifestPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected graph changed manifest.json: %v", err)
			}
		})
	}
}

func TestPreparedGenerationLoadRejectsBrokenTransitiveGraph(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	fixture := buildStoredFixture(t, store, "generation-load-broken")
	fixture.manifest.ActiveRevisions = map[string]string{observationKeyDigest(t, fixture.observation.Key): prefixedDigest("missing-on-load")}
	if err := memory.ValidateGenerationManifest(fixture.manifest); err != nil {
		t.Fatalf("fixture must pass Task-1 validation: %v", err)
	}
	manifestDigest, err := memory.Digest(fixture.manifest)
	if err != nil {
		t.Fatalf("digest broken manifest: %v", err)
	}
	prepared := Prepared{GenerationID: fixture.manifest.GenerationID, ManifestDigest: manifestDigest, ProjectViewDigest: fixture.manifest.ProjectViewDigest}
	writeCanonicalJSONForTest(t, filepath.Join(store.memory.Path, "generations", fixture.manifest.GenerationID+".json"), fixture.manifest, 0o600)
	writeCanonicalJSONForTest(t, filepath.Join(store.memory.Path, "manifest.json"), prepared, 0o600)

	if _, _, err := store.LoadPrepared(); err == nil {
		t.Fatal("LoadPrepared accepted a broken transitive graph")
	}
}

func TestPreparedGenerationConcurrentPreparationHasOneWinner(t *testing.T) {
	dataRoot := t.TempDir()
	seed, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	first := buildStoredFixture(t, seed, "generation-a").manifest
	second := first
	second.GenerationID = "generation-b"
	second.CreatedAt = "2026-08-31T10:02:00Z"
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	stores := make([]*Store, 2)
	for index := range stores {
		stores[index], err = Open(dataRoot, testProjectID)
		if err != nil {
			t.Fatalf("open concurrent store %d: %v", index, err)
		}
		defer stores[index].Close()
	}
	manifests := []memory.GenerationManifest{first, second}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range stores {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errs[index] = stores[index].PrepareGeneration(manifests[index])
		}(index)
	}
	close(start)
	wait.Wait()
	winners := 0
	for _, err := range errs {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent preparation winners=%d errors=%v", winners, errs)
	}
	prepared, manifest, err := stores[0].LoadPrepared()
	if err != nil {
		t.Fatalf("winner is not readable: %v", err)
	}
	if prepared.GenerationID != manifest.GenerationID || (manifest.GenerationID != "generation-a" && manifest.GenerationID != "generation-b") {
		t.Fatalf("invalid winning generation: %#v %#v", prepared, manifest)
	}
}

func TestPreparedGenerationRestartAtEveryManifestCheckpoint(t *testing.T) {
	for failAt := 1; failAt <= manifestCheckpointCount+1; failAt++ {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			dataRoot := t.TempDir()
			store, err := Open(dataRoot, testProjectID)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			fixture := buildStoredFixture(t, store, "generation-1")
			var calls atomic.Int32
			store.manifestCheckpoint = func() error {
				if calls.Add(1) == int32(failAt) {
					return errInjectedCrash
				}
				return nil
			}
			_, prepareErr := store.PrepareGeneration(fixture.manifest)
			_ = store.Close()

			restarted, err := Open(dataRoot, testProjectID)
			if err != nil {
				t.Fatalf("restart store: %v", err)
			}
			defer restarted.Close()
			prepared, manifest, loadErr := restarted.LoadPrepared()
			if failAt <= manifestCheckpointCount {
				if !errors.Is(prepareErr, errInjectedCrash) {
					t.Fatalf("prepare error=%v, want crash", prepareErr)
				}
				if !errors.Is(loadErr, ErrNoPreparedGeneration) {
					t.Fatalf("checkpoint %d left partial prepared state: %#v %#v %v", failAt, prepared, manifest, loadErr)
				}
				return
			}
			if prepareErr != nil || loadErr != nil || prepared.GenerationID != fixture.manifest.GenerationID || manifest.GenerationID != fixture.manifest.GenerationID {
				t.Fatalf("completed preparation not readable: prepared=%#v manifest=%#v prepare=%v load=%v", prepared, manifest, prepareErr, loadErr)
			}
		})
	}
}

func TestPreparedGenerationRestartRejectsWeakModeExactOrphan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not authoritative on Windows")
	}
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	fixture := buildStoredFixture(t, store, "generation-weak-orphan")
	generationPath := filepath.Join(store.memory.Path, "generations", fixture.manifest.GenerationID+".json")
	writeCanonicalJSONForTest(t, generationPath, fixture.manifest, 0o600)
	if err := os.Chmod(generationPath, 0o644); err != nil {
		t.Fatalf("weaken orphan generation mode: %v", err)
	}
	before, err := os.ReadFile(generationPath)
	if err != nil {
		t.Fatalf("read orphan generation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close pre-restart store: %v", err)
	}

	restarted, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("restart store: %v", err)
	}
	defer restarted.Close()
	if _, err := restarted.PrepareGeneration(fixture.manifest); err == nil {
		t.Fatal("weak-mode orphan generation was prepared")
	}
	manifestPath := filepath.Join(restarted.memory.Path, "manifest.json")
	if _, err := os.Lstat(manifestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("weak-mode orphan changed manifest.json: %v", err)
	}
	after, err := os.ReadFile(generationPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("weak-mode orphan was changed: body_equal=%v err=%v", bytes.Equal(after, before), err)
	}
	assertMode(t, generationPath, 0o644)
}

func TestAdvancePreparedRequiresExactExpectedAndPreservesOldGeneration(t *testing.T) {
	dataRoot, store, fixture := preparedStore(t)
	defer store.Close()

	expected, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatalf("load prepared baseline: %v", err)
	}
	successor := fixture.manifest
	successor.GenerationID = "generation-2"
	successor.CreatedAt = "2026-08-31T10:02:00Z"

	advanced, err := store.AdvancePrepared(expected, successor)
	if err != nil {
		t.Fatalf("advance prepared generation: %v", err)
	}
	loaded, manifest, err := store.LoadPrepared()
	if err != nil || loaded != advanced || manifest.GenerationID != successor.GenerationID {
		t.Fatalf("load advanced generation: prepared=%#v manifest=%#v err=%v", loaded, manifest, err)
	}
	oldPath := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", "generations", fixture.manifest.GenerationID+".json")
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old immutable generation was not retained: %v", err)
	}

	staleSuccessor := successor
	staleSuccessor.GenerationID = "generation-stale"
	staleSuccessor.CreatedAt = "2026-08-31T10:03:00Z"
	if _, err := store.AdvancePrepared(expected, staleSuccessor); !errors.Is(err, ErrPreparedGeneration) {
		t.Fatalf("stale advance error = %v, want ErrPreparedGeneration", err)
	}
	after, afterManifest, err := store.LoadPrepared()
	if err != nil || after != advanced || afterManifest.GenerationID != successor.GenerationID {
		t.Fatalf("stale advance changed pointer: prepared=%#v manifest=%#v err=%v", after, afterManifest, err)
	}
	if _, err := os.Lstat(filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", "published_generation")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AdvancePrepared created published_generation: %v", err)
	}
}

func TestAdvancePreparedConcurrentStaleCallersHaveOneWinner(t *testing.T) {
	dataRoot, seed, fixture := preparedStore(t)
	expected, _, err := seed.LoadPrepared()
	if err != nil {
		t.Fatalf("load prepared baseline: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	stores := make([]*Store, 2)
	for index := range stores {
		stores[index], err = Open(dataRoot, testProjectID)
		if err != nil {
			t.Fatalf("open concurrent store %d: %v", index, err)
		}
		defer stores[index].Close()
	}
	manifests := []memory.GenerationManifest{fixture.manifest, fixture.manifest}
	manifests[0].GenerationID = "generation-next-a"
	manifests[0].CreatedAt = "2026-08-31T10:02:00Z"
	manifests[1].GenerationID = "generation-next-b"
	manifests[1].CreatedAt = "2026-08-31T10:03:00Z"

	start := make(chan struct{})
	results := make([]Prepared, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range stores {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = stores[index].AdvancePrepared(expected, manifests[index])
		}(index)
	}
	close(start)
	wait.Wait()

	successes := 0
	stale := 0
	for index, callErr := range errs {
		switch {
		case callErr == nil:
			successes++
		case errors.Is(callErr, ErrPreparedGeneration):
			stale++
		default:
			t.Fatalf("advance %d returned unexpected error: %v", index, callErr)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("successes=%d stale=%d errors=%v results=%v", successes, stale, errs, results)
	}
	prepared, manifest, err := stores[0].LoadPrepared()
	if err != nil || prepared.GenerationID != manifest.GenerationID || (manifest.GenerationID != "generation-next-a" && manifest.GenerationID != "generation-next-b") {
		t.Fatalf("invalid concurrent winner: prepared=%#v manifest=%#v err=%v", prepared, manifest, err)
	}
}

func TestAdvancePreparedCrashAtEveryManifestCheckpointLeavesReadableGeneration(t *testing.T) {
	for failAt := 1; failAt <= 4; failAt++ {
		t.Run(fmt.Sprintf("checkpoint-%d", failAt), func(t *testing.T) {
			dataRoot, store, fixture := preparedStore(t)
			expected, _, err := store.LoadPrepared()
			if err != nil {
				t.Fatalf("load prepared baseline: %v", err)
			}
			successor := fixture.manifest
			successor.GenerationID = fmt.Sprintf("generation-next-%d", failAt)
			successor.CreatedAt = "2026-08-31T10:02:00Z"
			calls := 0
			store.manifestCheckpoint = func() error {
				calls++
				if calls == failAt {
					return errInjectedCrash
				}
				return nil
			}
			_, _ = store.AdvancePrepared(expected, successor)
			if calls < failAt {
				t.Fatalf("checkpoint %d was not reached; callbacks=%d", failAt, calls)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close interrupted store: %v", err)
			}

			restarted, err := Open(dataRoot, testProjectID)
			if err != nil {
				t.Fatalf("restart store: %v", err)
			}
			defer restarted.Close()
			prepared, manifest, err := restarted.LoadPrepared()
			if err != nil {
				t.Fatalf("checkpoint %d left unreadable prepared state after %d callbacks: %v", failAt, calls, err)
			}
			if prepared.GenerationID != manifest.GenerationID || (manifest.GenerationID != fixture.manifest.GenerationID && manifest.GenerationID != successor.GenerationID) {
				t.Fatalf("checkpoint %d left torn state: prepared=%#v manifest=%#v", failAt, prepared, manifest)
			}
		})
	}
}

var errInjectedCrash = errors.New("injected crash")

func validObservation() memory.ObservationRevision {
	value := memory.ObservationRevision{
		SchemaVersion: memory.MemorySchemaVersion,
		Key:           memory.ObservationKey{Provider: "codex", SessionID: "session-1", SourceIdentity: "source-1", Sequence: 1, ProjectID: testProjectID, Kind: "test", Subject: "go test"},
		Ref:           memory.SourceRef{Provider: "codex", SessionID: "session-1", SourceIdentity: "source-1", Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 1, ByteOffset: 0}}, SourceHash: hexDigest("source")},
		Timestamp:     testEndedAt, Operation: "run", Object: "tests", Outcome: "passed", Fields: map[string]string{"passed": "1"}, AdapterID: "codex-jsonl", AdapterVersion: "v1",
	}
	value.RevisionID = memory.ObservationRevisionID(value)
	return value
}

func validSessionView(t *testing.T, chunkDigest string) memory.SessionView {
	t.Helper()
	observation := validObservation()
	value := memory.SessionView{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: testProjectID, Provider: "codex", SessionID: "session-1",
		SourceIdentity:     "source-1",
		SourceRecordDigest: prefixedDigest("source-record"), UsageRecordDigest: prefixedDigest("source-record"), StartedAt: testStartedAt, EndedAt: testEndedAt,
		TerminalState: memory.Indexed, SourceAvailability: memory.SourceAvailable,
		ActiveRevisionIDs: []string{observation.RevisionID},
		ObservationSummaries: []memory.ObservationSummary{{
			RevisionID: observation.RevisionID, Sequence: observation.Key.Sequence,
			Kind: observation.Key.Kind, Subject: observation.Key.Subject, OccurredAt: observation.Timestamp,
			Operation: observation.Operation, Object: observation.Object, Outcome: observation.Outcome,
			Fields: map[string]string{"passed": "1"},
		}},
		ObservationChunkDigests: []string{chunkDigest},
		DerivedRecords:          []memory.DerivedRecord{}, Diagnostics: []memory.Diagnostic{}, DependencyDigest: prefixedDigest("dependency"), MaterializerVersion: "v1",
	}
	var err error
	value.Digest, err = memory.SessionViewDigest(value)
	if err != nil {
		t.Fatalf("digest SessionView: %v", err)
	}
	return value
}

func preparedStore(t *testing.T) (string, *Store, storedFixture) {
	t.Helper()
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	fixture := buildStoredFixture(t, store, "generation-1")
	if _, err := store.PrepareGeneration(fixture.manifest); err != nil {
		_ = store.Close()
		t.Fatalf("prepare generation: %v", err)
	}
	return dataRoot, store, fixture
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
}

func regularFilesUnder(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}
