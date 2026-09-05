package syncproject

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/migrationv4"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/strictjson"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

func TestMarkdownMigrationPreviewRejectsPrivateEvidenceMutationDuringBuild(t *testing.T) {
	for _, target := range []string{"source journal", "prepared pointer", "published pointer", "ProjectView", "SessionView", "private index"} {
		t.Run(target, func(t *testing.T) {
			fixture, manifest := newOldV4EvidenceFixture(t)
			memoryRoot := filepath.Join(fixture.data, "projects", fixture.projectID, "memory-v1")
			paths := map[string]string{
				"source journal":    filepath.Join(fixture.data, "publication-journal", fixture.projectID, publicationstate.IntentLeaf),
				"prepared pointer":  filepath.Join(memoryRoot, "manifest.json"),
				"published pointer": filepath.Join(memoryRoot, "published_generation"),
				"ProjectView":       filepath.Join(memoryRoot, "project-views", strings.TrimPrefix(manifest.ProjectViewDigest, "sha256:")+".json"),
				"SessionView":       filepath.Join(memoryRoot, "sessions", strings.TrimPrefix(manifest.SessionViews[0].Digest, "sha256:")+".json"),
				"private index":     filepath.Join(memoryRoot, "session-indexes", strings.TrimPrefix(manifest.SessionIndexDigest, "sha256:")+".json"),
			}
			_, err := RunMigration(t.Context(), MigrationOptions{
				Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
				Mode:    MigrationDryRun,
				afterMigrationBuild: func() error {
					path := paths[target]
					body, readErr := os.ReadFile(path)
					if readErr != nil {
						return readErr
					}
					switch target {
					case "source journal":
						var intent publicationstate.Intent
						if err := json.Unmarshal(body, &intent); err != nil {
							return err
						}
						intent.CreatedAt = intent.CreatedAt.Add(time.Second)
						var out bytes.Buffer
						encoder := json.NewEncoder(&out)
						encoder.SetEscapeHTML(false)
						encoder.SetIndent("", "  ")
						if err := encoder.Encode(intent); err != nil {
							return err
						}
						body = out.Bytes()
					case "published pointer":
						body = []byte("generation-markdown-lock\n")
					default:
						body = append(body, '\n')
					}
					return os.WriteFile(path, body, 0o600)
				},
			})
			if !errors.Is(err, ErrMigrationPreviewStale) {
				t.Fatalf("%s mutation error=%v, want migration preview stale", target, err)
			}
		})
	}
}

func newOldV4EvidenceFixture(t *testing.T) (migrationServiceFixture, memory.GenerationManifest) {
	t.Helper()
	fixture, accepted := newMarkdownLockFixture(t)
	store, err := memorystore.Open(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, manifest, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	projectBody, err := store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		t.Fatal(err)
	}
	var projectView memory.ProjectView
	if err := json.Unmarshal(projectBody, &projectView); err != nil {
		t.Fatal(err)
	}
	session := memory.SessionView{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: fixture.projectID, Provider: "codex", SessionID: "session-evidence", SourceIdentity: "source-evidence",
		SourceRecordDigest: "sha256:" + strings.Repeat("3", 64), UsageRecordDigest: "sha256:" + strings.Repeat("4", 64),
		StartedAt: manifest.CreatedAt, EndedAt: manifest.CreatedAt,
		TerminalState: memory.Missing, SourceAvailability: memory.SourceUnavailable,
		ActiveRevisionIDs: []string{}, ObservationSummaries: []memory.ObservationSummary{}, ObservationChunkDigests: []string{}, DerivedRecords: []memory.DerivedRecord{}, Diagnostics: []memory.Diagnostic{},
		DependencyDigest: "sha256:" + strings.Repeat("5", 64), MaterializerVersion: "v1",
	}
	session.Digest, err = memory.SessionViewDigest(session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionView(session); err != nil {
		t.Fatal(err)
	}
	lineage := memory.SessionLineage{SchemaVersion: memory.MemorySchemaVersion, ProjectID: fixture.projectID, Provider: session.Provider, SessionID: session.SessionID, SourceIdentity: session.SourceIdentity, ActiveRevisions: map[string]string{}, SupersededRevisions: map[string]string{}, WithdrawnRevisions: map[string]string{}}
	lineage.Digest, err = memory.SessionLineageDigest(lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionLineage(lineage); err != nil {
		t.Fatal(err)
	}
	dependency := memory.SessionViewDependency{Provider: session.Provider, SessionID: session.SessionID, Digest: session.Digest}
	projectView.Generation++
	projectView.SourceSessions = 1
	projectView.TerminalCounts = memory.TerminalCounts{Missing: 1}
	projectView.SessionViewDependencies = []memory.SessionViewDependency{dependency}
	projectView.DependencyDigest = "sha256:" + strings.Repeat("6", 64)
	projectView.Digest, err = memory.ProjectViewDigest(projectView)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(projectView); err != nil {
		t.Fatal(err)
	}
	manifest.GenerationID = "generation-private-evidence"
	manifest.SourceRecordDigests = []string{session.SourceRecordDigest}
	manifest.SessionViews = []memory.SessionViewDependency{dependency}
	manifest.SessionLineages = []memory.SessionLineageDependency{{Provider: session.Provider, SessionID: session.SessionID, Digest: lineage.Digest}}
	manifest.ProjectViewDigest = projectView.Digest
	manifest.SessionIndexDigest = ""
	manifest.SessionIndexMeasurements = []memory.SessionIndexMeasurement{{Provider: session.Provider, SessionID: session.SessionID}}
	generatedAt, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	indexDocument, err := sessionindex.Build(sessionindex.BuildInput{ProjectView: projectView, Manifest: manifest, SessionViews: map[sessionindex.SessionKey]*memory.SessionView{{Provider: session.Provider, SessionID: session.SessionID}: &session}, GeneratedAt: generatedAt})
	if err != nil {
		t.Fatal(err)
	}
	manifest.SessionIndexDigest, err = store.PutSessionIndex(indexDocument)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvancePrepared(prepared, manifest); err != nil {
		t.Fatal(err)
	}
	index, err := store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
	if err != nil {
		t.Fatal(err)
	}
	accepted.Review.GenerationID, accepted.Review.ProjectViewDigest = manifest.GenerationID, manifest.ProjectViewDigest
	for index := range accepted.Review.Timeline {
		accepted.Review.Timeline[index].GenerationID = manifest.GenerationID
	}
	for index := range accepted.Review.GeneratedBaselines {
		accepted.Review.GeneratedBaselines[index].GenerationID = manifest.GenerationID
	}
	accepted.Ledger.GenerationID, accepted.Ledger.ProjectViewDigest = manifest.GenerationID, manifest.ProjectViewDigest
	accepted.Ledger.SyncHashes.SessionIndexDigest = manifest.SessionIndexDigest
	manifestDigest, err := memory.Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	accepted.Review.MinimumReaderVersion, accepted.Review.MinimumWriterVersion = "0.4.0", "0.4.0"
	review, err := strictjson.Encode(accepted.Review)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reviewv2.RenderHistoryV3(fixture.projectID, accepted.Review.Revision, manifest.GenerationID, []reviewv2.Event{})
	if err != nil {
		t.Fatal(err)
	}
	ledger := accepted.Ledger
	ledger.MinimumReaderVersion, ledger.MinimumWriterVersion = "0.4.0", "0.4.0"
	ledger.DocumentProjection = nil
	ledger.ReviewSHA256, ledger.HistorySHA256 = fmt.Sprintf("%x", sha256.Sum256(review)), fmt.Sprintf("%x", sha256.Sum256(history))
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	ledgerBody, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{migrationv4.ReviewRelativePath: review, migrationv4.HistoryRelativePath: history, migrationv4.LedgerRelativePath: ledgerBody, migrationv4.SessionIndexRelativePath: index}
	destinations := make([]publicationstate.Destination, 0, 8)
	for _, side := range []string{"project", "vault"} {
		for _, relative := range []string{migrationv4.HistoryRelativePath, migrationv4.LedgerRelativePath, migrationv4.ReviewRelativePath, migrationv4.SessionIndexRelativePath} {
			path := filepath.Join(fixture.project, filepath.FromSlash(relative))
			journalRelative := relative
			if side == "vault" {
				journalRelative = filepath.ToSlash(filepath.Join("Projects/Migration/Session Review", strings.TrimPrefix(relative, "docs/session-review/")))
				path = filepath.Join(fixture.vault, filepath.FromSlash(journalRelative))
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, files[relative], 0o600); err != nil {
				t.Fatal(err)
			}
			destinations = append(destinations, publicationstate.Destination{Side: side, Relative: journalRelative, DesiredSHA256: fmt.Sprintf("%x", sha256.Sum256(files[relative]))})
		}
	}
	sort.Slice(destinations, func(i, j int) bool {
		if destinations[i].Side != destinations[j].Side {
			return destinations[i].Side < destinations[j].Side
		}
		return destinations[i].Relative < destinations[j].Relative
	})
	intent := publicationstate.Intent{Version: 1, ProjectID: fixture.projectID, GenerationID: manifest.GenerationID, ManifestDigest: manifestDigest, ProjectViewDigest: manifest.ProjectViewDigest, Stage: publicationstate.StageCommitted, CreatedAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), Destinations: destinations}
	if err := publicationstate.ValidateIntent(intent, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(intent); err != nil {
		t.Fatal(err)
	}
	journalDir := filepath.Join(fixture.data, "publication-journal", fixture.projectID)
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalDir, publicationstate.IntentLeaf), encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(journalDir, publicationstate.AcceptedReceiptLeaf))
	if err := store.CommitPublished(manifest.GenerationID, memory.PublicationProof{Version: 4, ProjectID: fixture.projectID, GenerationID: manifest.GenerationID, ManifestDigest: manifestDigest, ProjectViewDigest: manifest.ProjectViewDigest, ReviewSHA256: fmt.Sprintf("%x", sha256.Sum256(review)), HistorySHA256: fmt.Sprintf("%x", sha256.Sum256(history)), LedgerSHA256: fmt.Sprintf("%x", sha256.Sum256(ledgerBody)), SessionIndexSHA256: fmt.Sprintf("%x", sha256.Sum256(index)), JournalVerified: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return fixture, manifest
}

func TestSyncProjectMigrationConfirmationRecomputesUnderProjectLock(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	hash := "sha256:" + strings.Repeat("1", 64)
	preview := migrationv4.MigrationPreview{PreviewDigest: hash, TargetHashes: migrationv4.ArtifactHashes{Review: hash, History: hash, Ledger: hash, SessionIndex: hash}}
	buildCalls := 0
	publishCalls := 0
	options := MigrationOptions{
		Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
		Mode:    MigrationDryRun,
		build: func(pin *MappingPin) (migrationv4.Result, error) {
			buildCalls++
			contender, err := publicationlock.Acquire(pin.data.Path, pin.mapping.ID, 0)
			if buildCalls == 1 {
				if err != nil {
					t.Fatalf("dry-run unexpectedly held publication lock: %v", err)
				}
				if err := contender.Release(); err != nil {
					t.Fatal(err)
				}
			} else {
				if contender != nil {
					_ = contender.Release()
				}
				if !errors.Is(err, project.ErrProjectLocked) {
					t.Fatalf("confirmation recomputation ran without publication lock: %v", err)
				}
			}
			return migrationv4.Result{Preview: preview}, nil
		},
		Publish: func(_ context.Context, publication MigrationPublication) error {
			publishCalls++
			if publication.Preview.PreviewDigest != preview.PreviewDigest {
				t.Fatalf("publication preview = %+v", publication.Preview)
			}
			if publication.PublicationLock == nil {
				t.Fatal("publisher did not receive publication lock ownership")
			}
			contender, publicationErr := publicationlock.Acquire(publication.DataRoot, publication.ProjectID, 0)
			if contender != nil {
				_ = contender.Release()
			}
			if !errors.Is(publicationErr, project.ErrProjectLocked) {
				t.Fatalf("publisher ran without publication lock: %v", publicationErr)
			}
			lock, err := project.AcquireProjectLock(publication.syncDataRoot, "locks/sync.lock", 0)
			if lock != nil {
				_ = lock.Release()
			}
			if !errors.Is(err, project.ErrProjectLocked) {
				t.Fatalf("publisher ran without project lock: %v", err)
			}
			return nil
		},
	}
	dry, err := RunMigration(t.Context(), options)
	if err != nil || dry.Applied || dry.Preview.PreviewDigest != preview.PreviewDigest || buildCalls != 1 || publishCalls != 0 {
		t.Fatalf("dry=%+v build=%d publish=%d err=%v", dry, buildCalls, publishCalls, err)
	}

	options.Mode = MigrationConfirm
	options.ExpectedPreviewDigest = preview.PreviewDigest
	confirmed, err := RunMigration(t.Context(), options)
	if err != nil || !confirmed.Applied || buildCalls != 2 || publishCalls != 1 {
		t.Fatalf("confirmed=%+v build=%d publish=%d err=%v", confirmed, buildCalls, publishCalls, err)
	}
}

func TestSyncProjectMigrationConfirmationRejectsRecomputedStaleDigest(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	want := "sha256:" + strings.Repeat("1", 64)
	complete := migrationv4.ArtifactHashes{Review: want, History: want, Ledger: want, SessionIndex: want}
	options := MigrationOptions{
		Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
		Mode:    MigrationConfirm, ExpectedPreviewDigest: want,
		build: func(*MappingPin) (migrationv4.Result, error) {
			return migrationv4.Result{Preview: migrationv4.MigrationPreview{PreviewDigest: "sha256:" + strings.Repeat("2", 64), TargetHashes: complete}}, nil
		},
		Publish: func(context.Context, MigrationPublication) error {
			t.Fatal("stale preview reached publisher")
			return nil
		},
	}
	if _, err := RunMigration(t.Context(), options); !errors.Is(err, ErrMigrationPreviewStale) {
		t.Fatalf("RunMigration error = %v", err)
	}
}

// Checking only the digest would let an informative blocked preview reach the
// publisher. Confirmation must reject the state before invoking any writer.
func TestMarkdownMigrationConfirmationRejectsBlockedPreviewEvenWithExactDigest(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	digest := "sha256:" + strings.Repeat("1", 64)
	preview := migrationv4.MigrationPreview{
		SchemaVersion: 1, SourceVersion: 3, TargetVersion: 4,
		ProjectID: fixture.projectID, GenerationID: "generation-1", RequiresSessionIndex: true,
		SourceFormat: migrationv4.FormatMarkdownV3, TargetFormat: migrationv4.FormatMarkdownV1,
		BlockingReasons: []string{"verified_conversation_chain_required"}, PreviewDigest: digest,
	}
	_, err := RunMigration(t.Context(), MigrationOptions{
		Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
		Mode:    MigrationConfirm, ExpectedPreviewDigest: digest,
		build: func(*MappingPin) (migrationv4.Result, error) {
			return migrationv4.Result{Preview: preview}, nil
		},
		Publish: func(context.Context, MigrationPublication) error {
			t.Fatal("blocked preview reached publisher")
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("blocked confirmation error = %v", err)
	}
}

func TestMarkdownMigrationConfirmationRejectsEachMissingTargetHash(t *testing.T) {
	for _, missing := range []string{"review", "history", "ledger", "session-index"} {
		t.Run(missing, func(t *testing.T) {
			fixture := newMigrationServiceFixture(t)
			hash := "sha256:" + strings.Repeat("1", 64)
			target := migrationv4.ArtifactHashes{Review: hash, History: hash, Ledger: hash, SessionIndex: hash}
			switch missing {
			case "review":
				target.Review = ""
			case "history":
				target.History = ""
			case "ledger":
				target.Ledger = ""
			case "session-index":
				target.SessionIndex = ""
			}
			preview := migrationv4.MigrationPreview{PreviewDigest: hash, TargetHashes: target}
			_, err := RunMigration(t.Context(), MigrationOptions{
				Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
				Mode:    MigrationConfirm, ExpectedPreviewDigest: hash,
				build: func(*MappingPin) (migrationv4.Result, error) { return migrationv4.Result{Preview: preview}, nil },
				Publish: func(context.Context, MigrationPublication) error {
					t.Fatal("incomplete target reached publisher")
					return nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), "blocked") {
				t.Fatalf("missing %s target hash error=%v", missing, err)
			}
		})
	}
}

func TestSyncProjectPlainV3RequiresExplicitMigration(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	for relative, body := range map[string][]byte{
		reviewv2.ReviewRelativePath:        []byte("---\nid: project-overview\nentity_type: project_review\nproject_id: project-migration\nschema_version: 3\nrevision: 1\n---\n# v3\n"),
		reviewv2.HistoryRelativePath:       []byte("---\nid: project-history\nentity_type: project_history\nproject_id: project-migration\nschema_version: 3\nrevision: 1\n---\n# history\n"),
		reviewv2.MachineLedgerRelativePath: []byte("{}\n"),
	} {
		path := filepath.Join(fixture.project, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Run(t.Context(), Options{
		ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data,
		GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI,
	})
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("plain v3 sync error = %v", err)
	}
}

func TestSyncProjectBlocksLegacyPreparedGenerationWithoutVerifiedChains(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	manifest := seedMigrationPreparedGeneration(t, fixture)
	before := snapshotMigrationPublicFiles(t, fixture)
	dry, err := RunMigration(t.Context(), MigrationOptions{
		Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
		Mode:    MigrationDryRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Applied || dry.Preview.ProjectID != fixture.projectID || dry.Preview.GenerationID != manifest.GenerationID || dry.Preview.TargetPreimageHashes.SessionIndex != migrationv4.AbsentPreimageSHA256 || len(dry.Preview.BlockingReasons) == 0 {
		t.Fatalf("dry migration = %+v", dry)
	}
	if after := snapshotMigrationPublicFiles(t, fixture); !reflect.DeepEqual(before, after) {
		t.Fatalf("dry-run wrote public files: before=%v after=%v", before, after)
	}

	published := 0
	_, err = RunMigration(t.Context(), MigrationOptions{
		Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
		Mode:    MigrationConfirm, ExpectedPreviewDigest: dry.Preview.PreviewDigest,
		Publish: func(_ context.Context, publication MigrationPublication) error {
			published++
			if len(publication.Plan.Files) != 4 {
				t.Fatalf("publication files = %d", len(publication.Plan.Files))
			}
			for _, file := range publication.Plan.Files {
				if file.Relative == migrationv4.SessionIndexRelativePath {
					if file.ExpectedExists {
						t.Fatal("new session index unexpectedly had a preimage")
					}
				} else if !file.ExpectedExists || len(file.Expected) == 0 {
					t.Fatalf("source preimage missing for %s", file.Relative)
				}
			}
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "blocked") || published != 0 {
		t.Fatalf("published=%d err=%v", published, err)
	}
}

// A Markdown migration preview is a read-only observation: lock files,
// journals, immutable objects, and public files must all remain byte-identical.
func TestMarkdownMigrationDryRunDoesNotCreateLocksOrMutateAnyTree(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	seedMigrationPreparedGeneration(t, fixture)
	if err := os.Remove(filepath.Join(fixture.data, "projects", fixture.projectID, "locks", "sync.lock")); err != nil {
		t.Fatal(err)
	}
	before := snapshotMigrationTrees(t, fixture)
	result, err := RunMigration(t.Context(), MigrationOptions{
		Options: Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI},
		Mode:    MigrationDryRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Preview.BlockingReasons) == 0 {
		t.Fatalf("legacy dry-run was not explicitly blocked: %+v", result.Preview)
	}
	if after := snapshotMigrationTrees(t, fixture); !reflect.DeepEqual(before, after) {
		t.Fatalf("dry-run mutated Project/Vault/private trees:\n before=%v\n after=%v", before, after)
	}
}

func snapshotMigrationTrees(t *testing.T, fixture migrationServiceFixture) map[string]string {
	t.Helper()
	result := map[string]string{}
	for label, root := range map[string]string{"project": fixture.project, "vault": fixture.vault, "data": fixture.data} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			key := label + "/" + filepath.ToSlash(relative)
			if entry.IsDir() {
				result[key] = "dir:" + info.Mode().String()
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[key] = fmt.Sprintf("file:%s:%x", info.Mode().String(), sha256.Sum256(body))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func TestMigrationSessionIndexPreservesUnknownTimestamps(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	entry, err := migrationIndexEntry(memory.SessionView{
		Provider: "codex", SessionID: "unknown-times", TerminalState: memory.Indexed,
		SourceAvailability: memory.SourceAvailable, UsageRecordDigest: digest,
		ObservationSummaries: []memory.ObservationSummary{}, ActiveRevisionIDs: []string{}, Diagnostics: []memory.Diagnostic{},
	}, memory.SessionViewDependency{Digest: digest}, "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	if entry.StartedAt != nil || entry.EndedAt != nil || entry.DurationMS != nil {
		t.Fatalf("migration fabricated unknown timestamps: %+v", entry)
	}
	coverage := sessionindex.IndexCoverage{Total: 1}
	addIndexCoverage(&coverage, entry)
	if coverage.StartedAtKnown != 0 || coverage.EndedAtKnown != 0 {
		t.Fatalf("migration counted unknown timestamps as known: %+v", coverage)
	}
}

type migrationServiceFixture struct {
	projectID string
	project   string
	vault     string
	data      string
}

func seedMigrationPreparedGeneration(t *testing.T, fixture migrationServiceFixture) memory.GenerationManifest {
	t.Helper()
	store, err := memorystore.Open(fixture.data, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := "2026-09-04T08:00:00Z"
	probe := memory.ProjectProbeState{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: fixture.projectID,
		CanonicalRoot: fixture.project, Branch: "main", Head: strings.Repeat("a", 40),
		RemoteIdentityHashes: []string{}, VersionFiles: []memory.ProbeFile{}, RequiredProjectionFiles: []memory.ProbeFile{},
		ProbeVersion: "v1", Diagnostics: []memory.Diagnostic{},
	}
	probe.Digest, err = memory.ProjectProbeStateDigest(probe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProbeState(probe); err != nil {
		t.Fatal(err)
	}
	view := memory.ProjectView{
		SchemaVersion: memory.MemorySchemaVersion, ProjectID: fixture.projectID, Generation: 1,
		StartedAt: created, EndedAt: created, SourceSessions: 0, TerminalCounts: memory.TerminalCounts{},
		SessionViewDependencies: []memory.SessionViewDependency{}, ObservationRevisionIDs: []string{}, ProbeStateDigest: probe.Digest,
		LiveState: memory.StateSnapshot{Branch: "main", Head: probe.Head}, WitnessedState: []memory.DerivedRecord{}, DerivedRecords: []memory.DerivedRecord{},
		AggregationCoverage: memory.ProjectAggregationCoverage{}, AssociatedUsage: []memory.AssociatedUsage{},
		DependencyDigest: "sha256:" + strings.Repeat("b", 64), ReducerVersion: "v1",
	}
	view.Digest, err = memory.ProjectViewDigest(view)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProjectView(view); err != nil {
		t.Fatal(err)
	}
	manifest := memory.GenerationManifest{
		SchemaVersion: memory.MemorySchemaVersion, GenerationID: "generation-migration", ProjectID: fixture.projectID, CreatedAt: created,
		SourceRecordDigests: []string{}, SessionViews: []memory.SessionViewDependency{}, SessionLineages: []memory.SessionLineageDependency{},
		ProbeStateDigest:  probe.Digest,
		ProbeCheck:        memory.ProbeCheck{SchemaVersion: memory.MemorySchemaVersion, CheckedAt: created, StateDigest: probe.Digest, Available: true, Diagnostics: []memory.Diagnostic{}},
		ProjectViewDigest: view.Digest,
	}
	if _, err := store.PrepareGeneration(manifest); err != nil {
		t.Fatal(err)
	}
	reviewModel := reviewv2.Review{
		ProjectID: fixture.projectID, GenerationID: manifest.GenerationID, MinimumWriterVersion: reviewv2.MinimumWriterVersion,
		Revision: 1, Name: "Migration", Goal: "Preserve", Stage: "implementation", Status: "active", NextAction: "confirm", LastVerification: "2026-09-04",
		Risks: []reviewv2.Risk{}, Decisions: []reviewv2.Decision{{ID: "decision-1", OccurredAt: "2026-09-04", Title: "Keep", Rationale: "because", Impact: "scope", Status: "active"}},
	}
	reviewBody, err := reviewv2.RenderReviewV3(reviewModel)
	if err != nil {
		t.Fatal(err)
	}
	historyBody, err := reviewv2.RenderHistoryV3(fixture.projectID, 1, manifest.GenerationID, []reviewv2.Event{})
	if err != nil {
		t.Fatal(err)
	}
	ledgerBody, err := reviewv2.RenderMachineLedgerV3(reviewv2.MachineLedgerV3{
		SchemaVersion: 3, MinimumWriterVersion: reviewv2.MinimumWriterVersion,
		ProjectID: fixture.projectID, GenerationID: manifest.GenerationID, ProjectViewDigest: strings.TrimPrefix(view.Digest, "sha256:"),
		AcceptedRevision: 1, ReviewSHA256: fmt.Sprintf("%x", sha256.Sum256(reviewBody)), HistorySHA256: fmt.Sprintf("%x", sha256.Sum256(historyBody)),
		Sessions: []ledger.SessionReport{}, HumanPatches: []reviewv2.HumanPatchWire{}, OrphanPatches: []reviewv2.HumanPatchWire{}, GeneratedBaselines: []reviewv2.GeneratedBaselineWire{},
		LegacyCompatibility: reviewv2.LegacyCompatibility{Timeline: []ledger.TimelineEvent{}, Decisions: []ledger.Decision{}, OpenLoops: []ledger.OpenLoop{}, CurrentRisks: []reviewv2.CurrentRiskProvenance{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for relative, body := range map[string][]byte{reviewv2.ReviewRelativePath: reviewBody, reviewv2.HistoryRelativePath: historyBody, reviewv2.MachineLedgerRelativePath: ledgerBody} {
		path := filepath.Join(fixture.project, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return manifest
}

func snapshotMigrationPublicFiles(t *testing.T, fixture migrationServiceFixture) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, relative := range []string{migrationv4.ReviewRelativePath, migrationv4.HistoryRelativePath, migrationv4.LedgerRelativePath, migrationv4.SessionIndexRelativePath} {
		path := filepath.Join(fixture.project, filepath.FromSlash(relative))
		body, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[relative] = body
	}
	return result
}

func newMigrationServiceFixture(t *testing.T) migrationServiceFixture {
	t.Helper()
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	vaultRoot := filepath.Join(root, "vault")
	dataRoot := filepath.Join(root, "data")
	for _, directory := range []string{projectRoot, vaultRoot, filepath.Join(dataRoot, "projects", "project-migration", "locks")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "projects", "project-migration", "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: "project-migration", Root: projectRoot, VaultRoot: vaultRoot,
		VaultReviewPath: "Projects/Migration/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}); err != nil {
		t.Fatal(err)
	}
	return migrationServiceFixture{projectID: "project-migration", project: projectRoot, vault: vaultRoot, data: dataRoot}
}

// Removing configured-mapping authentication, passing the global data root to
// the engine, or reconciling with a different trigger makes this test fail.
func TestSyncProjectServiceAuthenticatesMappingAndReconciles(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	vaultRoot := filepath.Join(root, "vault")
	dataRoot := filepath.Join(root, "data")
	for _, path := range []string{projectRoot, vaultRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	initialized, err := project.Initialize(project.InitOptions{
		ProjectRoot: projectRoot,
		VaultRoot:   vaultRoot,
		DataDir:     dataRoot,
		GOOS:        runtime.GOOS,
		Now:         func() time.Time { return now },
		Random:      bytes.NewReader(bytes.Repeat([]byte{0x11}, 8)),
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := Run(t.Context(), Options{
		ProjectID: initialized.ProjectID,
		CWD:       projectRoot,
		DataDir:   dataRoot,
		GOOS:      runtime.GOOS,
		Now:       func() time.Time { return now },
		Trigger:   syncengine.TriggerCLI,
		DryRun:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectID != initialized.ProjectID || !report.DryRun || len(report.Operations) == 0 {
		t.Fatalf("Run() report = %#v", report)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "Projects")); !os.IsNotExist(err) {
		t.Fatalf("dry-run mutated the Vault: %v", err)
	}

	if _, err := Run(t.Context(), Options{
		ProjectID: initialized.ProjectID,
		CWD:       filepath.Join(root, "other"),
		DataDir:   dataRoot,
		GOOS:      runtime.GOOS,
		Now:       func() time.Time { return now },
		Trigger:   syncengine.TriggerCLI,
		DryRun:    true,
	}); err == nil {
		t.Fatal("Run() accepted a CWD that does not authenticate the configured project")
	}
}

func TestSyncProjectServiceReconcilesProjectNestedInsideVault(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	projectRoot := filepath.Join(vaultRoot, "project")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	for _, path := range []string{projectRoot, dataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID, Revision: 1, Goal: "Nested project sync", Branch: "main",
			NextAction: "Sync", LastVerified: "2026-08-29T08:00:00Z", LastUpdated: "2026-08-29T08:00:00Z",
		},
		Decisions: map[string]ledger.Decision{}, OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(projectRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(dataRoot, "projects", projectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: "Projects/Nested--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	report, err := Run(t.Context(), Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot,
		GOOS: runtime.GOOS, Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) },
		Trigger: syncengine.TriggerCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectID != projectID || report.DryRun || len(report.Conflicts) != 0 || len(report.Errors) != 0 {
		t.Fatalf("Run() report = %#v", report)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "Projects", "Nested--11111111", "Session Review", "项目回顾.md")); err != nil {
		t.Fatalf("real nested sync did not publish into Vault: %v", err)
	}
}

// Removing the exact review-target capability from the MappingPin -> Engine
// handoff lets NewEngine independently reopen an ordinary replacement at the
// configured path and publish trusted Project bytes into it.
func TestSyncProjectRunNeverAdoptsReviewTargetAtPinToEngineHandoff(t *testing.T) {
	for _, targetInitiallyExists := range []bool{false, true} {
		name := "missing target with racing creator"
		if targetInitiallyExists {
			name = "existing target replaced"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			projectRoot := filepath.Join(root, "project")
			vaultRoot := filepath.Join(root, "vault")
			dataRoot := filepath.Join(root, "data")
			for _, directory := range []string{projectRoot, vaultRoot} {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
			initialized, err := project.Initialize(project.InitOptions{
				ProjectRoot: projectRoot, VaultRoot: vaultRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: func() time.Time { return now },
				Random: bytes.NewReader(bytes.Repeat([]byte{0x61}, 8)),
			})
			if err != nil {
				t.Fatal(err)
			}
			baseOptions := Options{
				ProjectID: initialized.ProjectID, CWD: projectRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: func() time.Time { return now }, Trigger: syncengine.TriggerCLI,
			}
			if targetInitiallyExists {
				if _, err := Run(t.Context(), baseOptions); err != nil {
					t.Fatal(err)
				}
			}
			pin, err := PinMapping(baseOptions)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()

			target := filepath.Join(vaultRoot, filepath.FromSlash(pin.mapping.VaultReviewPath))
			detached := filepath.Join(root, "detached-authority")
			options := baseOptions
			options.Pin = pin
			options.beforeEngine = func() error {
				if targetInitiallyExists {
					if err := os.Rename(target, detached); err != nil {
						return err
					}
				}
				return os.MkdirAll(target, 0o700)
			}
			if _, err := Run(t.Context(), options); err == nil {
				t.Fatal("Run() accepted a replaced review-target namespace")
			}
			entries, err := os.ReadDir(target)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("ordinary replacement received trusted writes: %v", entries)
			}
			if targetInitiallyExists {
				if _, err := os.Stat(filepath.Join(detached, "项目回顾.md")); err != nil {
					t.Fatalf("pinned detached authority was lost: %v", err)
				}
			}
		})
	}
}

func TestPinMappingRejectsReviewTargetContainingOrEqualProject(t *testing.T) {
	for _, test := range []struct {
		name            string
		projectRelative string
	}{
		{name: "target contains project", projectRelative: "project"},
		{name: "target equals project"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			vaultRoot := filepath.Join(root, "vault")
			dataRoot := filepath.Join(root, "data")
			projectID := "project-1111111111111111"
			reviewPath := "Projects/Unsafe--11111111/Session Review"
			target := filepath.Join(vaultRoot, filepath.FromSlash(reviewPath))
			projectRoot := target
			if test.projectRelative != "" {
				projectRoot = filepath.Join(target, test.projectRelative)
			}
			for _, directory := range []string{projectRoot, filepath.Join(dataRoot, "projects", projectID)} {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
				Version: 1,
				Projects: []config.ProjectMapping{{
					ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
					VaultReviewPath: reviewPath, VaultCaseMode: platform.CaseSensitive,
				}},
			}); err != nil {
				t.Fatal(err)
			}

			pin, err := PinMapping(Options{
				ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI,
			})
			if pin != nil {
				_ = pin.Close()
				t.Fatal("PinMapping returned a pin for an overlapping review target")
			}
			if err == nil {
				t.Fatal("PinMapping accepted an overlapping review target")
			}
		})
	}
}

func TestPinMappingRecheckRejectsReviewTargetRedirectedToContainProject(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	projectRoot := filepath.Join(vaultRoot, "Session Review", "project")
	reviewPath := "Projects/Alias--11111111/Session Review"
	for _, directory := range []string{
		projectRoot,
		filepath.Join(vaultRoot, "Projects"),
		filepath.Join(dataRoot, "projects", projectID),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: reviewPath, VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	options := Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot,
		GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI,
	}
	pin, err := PinMapping(options)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	alias := filepath.Join(vaultRoot, "Projects", "Alias--11111111")
	if err := os.Symlink(vaultRoot, alias); err != nil {
		t.Skipf("symlink/reparse-point creation is unavailable: %v", err)
	}
	if err := pin.Recheck(options); err == nil {
		t.Fatal("MappingPin accepted a review-target redirect to an ancestor of Project")
	}
}

func TestSyncProjectRejectsReviewTargetInsideNestedProjectWithoutWrites(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	projectRoot := filepath.Join(vaultRoot, "Projects", "Nested--11111111")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	for _, directory := range []string{projectRoot, dataRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID, Revision: 1, Goal: "Reject nested target", Branch: "main", NextAction: "Do not write",
			LastVerified: "2026-08-29T08:00:00Z", LastUpdated: "2026-08-29T08:00:00Z",
		},
		Decisions: map[string]ledger.Decision{}, OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(projectRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(dataRoot, "projects", projectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: "Projects/Nested--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(projectRoot, "Session Review")
	if _, err := Run(t.Context(), Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS,
		Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }, Trigger: syncengine.TriggerCLI,
	}); err == nil {
		t.Fatal("Run() accepted a Vault review target inside the authoritative Project")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("unsafe target received writes: %v", err)
	}
	redirectName := "Redirect--11111111"
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectRoot, filepath.Join(vaultRoot, "Projects", redirectName)); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{{
			ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
			VaultReviewPath: "Projects/" + redirectName + "/Session Review", VaultCaseMode: platform.CaseSensitive,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(t.Context(), Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS,
		Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }, Trigger: syncengine.TriggerCLI,
	}); err == nil {
		t.Fatal("Run() followed a redirect in the configured Vault review target")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("redirected target received writes: %v", err)
	}
}

// Changing the service contract to silently resolve a relative machine-data
// root would let worker and CLI callers disagree about the protected root.
func TestSyncProjectServiceRequiresExplicitAbsoluteDataDir(t *testing.T) {
	if _, err := Run(t.Context(), Options{
		ProjectID: "project-1111111111111111",
		DataDir:   "relative-data",
		GOOS:      runtime.GOOS,
		Now:       time.Now,
		Trigger:   syncengine.TriggerCLI,
	}); err == nil {
		t.Fatal("Run() accepted a relative data directory")
	}
}

// Extraction must preserve the old CLI lookup error when the data directory
// has not been initialized; opening the data root eagerly changes that public
// diagnostic into a lower-level filesystem error.
func TestSyncProjectServicePreservesMissingConfigLookupError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-data")
	_, err := Run(t.Context(), Options{
		ProjectID: "project-1111111111111111",
		DataDir:   missing,
		GOOS:      runtime.GOOS,
		Now:       time.Now,
		Trigger:   syncengine.TriggerCLI,
	})
	if err == nil || err.Error() != "configured project ID was not found" || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Run() error = %v, want configured mapping lookup diagnostic", err)
	}
}

func TestPinnedSyncRejectsConfigOrVaultReplacementWithoutDecoyWrites(t *testing.T) {
	for _, target := range []string{"config", "vault"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			projectRoot := filepath.Join(root, "project")
			vaultRoot := filepath.Join(root, "vault")
			dataRoot := filepath.Join(root, "data")
			for _, path := range []string{projectRoot, vaultRoot} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
			initialized, err := project.Initialize(project.InitOptions{
				ProjectRoot: projectRoot, VaultRoot: vaultRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: func() time.Time { return now },
				Random: bytes.NewReader(bytes.Repeat([]byte{0x22}, 8)),
			})
			if err != nil {
				t.Fatal(err)
			}
			options := Options{
				ProjectID: initialized.ProjectID, CWD: projectRoot, DataDir: dataRoot,
				GOOS: runtime.GOOS, Now: func() time.Time { return now }, Trigger: syncengine.TriggerCLI,
			}
			pin, err := PinMapping(options)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.Close()
			if target == "config" {
				fragments, err := os.ReadDir(filepath.Join(dataRoot, "projects.d"))
				if err != nil || len(fragments) != 1 {
					t.Fatalf("project fragments=%v err=%v", fragments, err)
				}
				configPath := filepath.Join(dataRoot, "projects.d", fragments[0].Name())
				body, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(configPath, configPath+".pinned"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(configPath, body, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(vaultRoot, vaultRoot+".pinned"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(vaultRoot, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			options.Pin = pin
			if _, err := Run(t.Context(), options); err == nil {
				t.Fatalf("Run() accepted %s replacement after mapping pin", target)
			}
			entries, err := os.ReadDir(vaultRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("replacement Vault received writes: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestPinMappingParsesOneCapturedConfigSnapshotAcrossABA(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	vaultRoot := filepath.Join(root, "vault-original")
	decoyVault := filepath.Join(root, "vault-decoy")
	dataRoot := filepath.Join(root, "data")
	projectID := "project-1111111111111111"
	for _, directory := range []string{projectRoot, vaultRoot, decoyVault, dataRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := ledger.State{
		ProjectID: projectID,
		CurrentState: ledger.CurrentState{
			ProjectID: projectID, Revision: 1, Goal: "Captured config", Branch: "main", NextAction: "Sync",
			LastVerified: "2026-08-29T08:00:00Z", LastUpdated: "2026-08-29T08:00:00Z",
		},
		Decisions: map[string]ledger.Decision{}, OpenLoops: map[string]ledger.OpenLoop{}, Sessions: map[string]ledger.SessionReport{},
	}
	state, err := reviewv2.ProjectLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reviewv2.Render(projectRoot, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(plan); err != nil {
		t.Fatal(err)
	}
	syncData := filepath.Join(dataRoot, "projects", projectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(syncData, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syncData, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	original := config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
		VaultReviewPath: "Projects/Original--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}}
	decoy := original
	decoy.Projects = []config.ProjectMapping{{
		ID: projectID, Root: projectRoot, VaultRoot: decoyVault,
		VaultReviewPath: "Projects/Decoy--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
	}}
	configPath := filepath.Join(dataRoot, "config.toml")
	if err := config.Save(configPath, original); err != nil {
		t.Fatal(err)
	}
	originalBody, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	decoyPath := filepath.Join(root, "decoy.toml")
	if err := config.Save(decoyPath, decoy); err != nil {
		t.Fatal(err)
	}
	decoyBody, err := os.ReadFile(decoyPath)
	if err != nil {
		t.Fatal(err)
	}
	options := Options{
		ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS,
		Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }, Trigger: syncengine.TriggerCLI,
	}
	seenCapture, seenParse := false, false
	options.pinCheckpoint = func(stage pinCheckpointStage) error {
		switch stage {
		case pinAfterCapture:
			seenCapture = true
			return os.WriteFile(configPath, decoyBody, 0o600)
		case pinAfterParse:
			seenParse = true
			return os.WriteFile(configPath, originalBody, 0o600)
		default:
			return nil
		}
	}
	pin, err := PinMapping(options)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	if !seenCapture || !seenParse || pin.mapping.VaultRoot != vaultRoot || pin.mapping.VaultReviewPath != "Projects/Original--11111111/Session Review" {
		t.Fatalf("pin mapping=%#v capture=%v parse=%v", pin.mapping, seenCapture, seenParse)
	}
	options.Pin = pin
	options.pinCheckpoint = nil
	if _, err := Run(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "Projects", "Original--11111111", "Session Review", "项目回顾.md")); err != nil {
		t.Fatalf("captured mapping target was not written: %v", err)
	}
	entries, err := os.ReadDir(decoyVault)
	if err != nil || len(entries) != 0 {
		t.Fatalf("decoy Vault received writes: entries=%v err=%v", entries, err)
	}
}

func TestPinMappingNeverUsesDecoyConfigAcrossEverySnapshotCheckpoint(t *testing.T) {
	stages := []pinCheckpointStage{
		pinAfterCapture, pinAfterParse, pinAfterMapping, pinBeforeVaultOpen, pinAfterVaultOpen, pinBeforeFinalVerify,
	}
	for index, mutateAt := range stages {
		t.Run(string(mutateAt), func(t *testing.T) {
			root := t.TempDir()
			projectRoot := filepath.Join(root, "project")
			vaultRoot := filepath.Join(root, "vault-original")
			decoyVault := filepath.Join(root, "vault-decoy")
			dataRoot := filepath.Join(root, "data")
			projectID := "project-1111111111111111"
			for _, directory := range []string{projectRoot, vaultRoot, decoyVault, filepath.Join(dataRoot, "projects", projectID)} {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			original := config.Config{Version: 1, Projects: []config.ProjectMapping{{
				ID: projectID, Root: projectRoot, VaultRoot: vaultRoot,
				VaultReviewPath: "Projects/Original--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
			}}}
			decoy := config.Config{Version: 1, Projects: []config.ProjectMapping{{
				ID: projectID, Root: projectRoot, VaultRoot: decoyVault,
				VaultReviewPath: "Projects/Decoy--11111111/Session Review", VaultCaseMode: platform.CaseSensitive,
			}}}
			configPath := filepath.Join(dataRoot, "config.toml")
			if err := config.Save(configPath, original); err != nil {
				t.Fatal(err)
			}
			originalBody, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			decoyPath := filepath.Join(root, "decoy.toml")
			if err := config.Save(decoyPath, decoy); err != nil {
				t.Fatal(err)
			}
			decoyBody, err := os.ReadFile(decoyPath)
			if err != nil {
				t.Fatal(err)
			}
			options := Options{ProjectID: projectID, CWD: projectRoot, DataDir: dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}
			restored := false
			options.pinCheckpoint = func(stage pinCheckpointStage) error {
				if stage == mutateAt {
					return os.WriteFile(configPath, decoyBody, 0o600)
				}
				if index+1 < len(stages) && stage == stages[index+1] {
					restored = true
					return os.WriteFile(configPath, originalBody, 0o600)
				}
				return nil
			}
			pin, err := PinMapping(options)
			if index == len(stages)-1 {
				if err == nil {
					_ = pin.Close()
					t.Fatal("PinMapping accepted an unrestored namespace mutation")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer pin.Close()
				if !restored || pin.mapping.VaultRoot != vaultRoot || pin.mapping.VaultReviewPath != "Projects/Original--11111111/Session Review" {
					t.Fatalf("pin mapping=%#v restored=%v", pin.mapping, restored)
				}
			}
			entries, err := os.ReadDir(decoyVault)
			if err != nil || len(entries) != 0 {
				t.Fatalf("decoy Vault received writes: entries=%v err=%v", entries, err)
			}
		})
	}
}
