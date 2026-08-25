package apply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
	preparepkg "github.com/neomei/SessionReviewer/internal/prepare"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/proposal"
)

const (
	testProjectID = "project-1111111111111111"
	testSessionID = "session-1"
	testHashA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHashB     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type applyTestFixture struct {
	projectRoot  string
	dataDir      string
	projectData  string
	proposalPath string
	evidencePath string
	packet       evidence.Packet
	now          time.Time
}

func newApplyTestFixture(t *testing.T) *applyTestFixture {
	t.Helper()
	projectRoot, vaultRoot, dataDir := t.TempDir(), t.TempDir(), t.TempDir()
	_, err := project.Initialize(project.InitOptions{
		ProjectRoot: projectRoot,
		VaultRoot:   vaultRoot,
		DataDir:     dataDir,
		Now:         func() time.Time { return time.Date(2026, 8, 23, 2, 0, 0, 0, time.UTC) },
		Random:      bytes.NewReader(bytes.Repeat([]byte{0x11}, 16)),
	})
	if err != nil {
		t.Fatal(err)
	}
	proposalBody, err := os.ReadFile(filepath.Join("..", "..", "testdata", "proposals", "valid-first.json"))
	if err != nil {
		t.Fatal(err)
	}
	packet := evidence.Packet{
		SchemaVersion: 2, ProjectID: testProjectID, SessionID: testSessionID, CWD: "/repo",
		FromCursor: 1, ToCursor: 2,
		ExpectedCursor: evidence.CursorBoundary{Line: 0},
		NextCursor:     evidence.CursorBoundary{Line: 2, SourceHash: testHashB},
		Events: []evidence.Item{
			{ID: "ev-message", Timestamp: "2026-08-23T01:02:03Z", JSONLLine: 1, SourceHash: testHashA, Kind: "message", Role: "user", Summary: "Choose durable ledger"},
			{ID: "ev-verify", Timestamp: "2026-08-23T01:03:03Z", JSONLLine: 2, SourceHash: testHashB, Kind: "tool_result", ToolName: "exec_command", Summary: "go test passed"},
		},
	}
	packetDigest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	proposalBody = bytes.Replace(proposalBody,
		[]byte("sha256:8bdbc9254ac37b3ea000f15910bd142068a0e991cd6ecafee482cbfd9ba9a4a4"),
		[]byte(packetDigest), 1)
	proposalPath := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(proposalPath, proposalBody, 0o600); err != nil {
		t.Fatal(err)
	}
	evidenceBody, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(evidencePath, append(evidenceBody, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return &applyTestFixture{
		projectRoot: projectRoot, dataDir: dataDir,
		projectData:  filepath.Join(dataDir, "projects", testProjectID),
		proposalPath: proposalPath, evidencePath: evidencePath, packet: packet,
		now: time.Date(2026, 8, 23, 3, 0, 0, 0, time.UTC),
	}
}

func (f *applyTestFixture) options() Options {
	return Options{
		ProposalPath: f.proposalPath, EvidencePath: f.evidencePath,
		ProjectRoot: f.projectRoot, DataDir: f.dataDir,
		Now: func() time.Time { return f.now },
	}
}

func mustLedgerSnapshot(t *testing.T, projectRoot string) []ledger.SnapshotFile {
	t.Helper()
	files, err := ledger.SnapshotExpected(projectRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestRunWritesThenAdvancesCursor(t *testing.T) {
	f := newApplyTestFixture(t)
	got, err := Run(f.options())
	if err != nil || !got.CursorAdvanced {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	c, err := (cursor.Store{Root: f.projectData}).Load(testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if c.LastLine != f.packet.NextCursor.Line || c.LastHash != f.packet.NextCursor.SourceHash || !c.UpdatedAt.Equal(f.now) {
		t.Fatalf("cursor=%+v", c)
	}
}

func TestRunRepeatIsIdempotent(t *testing.T) {
	f := newApplyTestFixture(t)
	if _, err := Run(f.options()); err != nil {
		t.Fatal(err)
	}
	before := hashLedger(t, f.projectRoot)
	got, err := Run(f.options())
	if err != nil || !got.AlreadyApplied || len(got.ChangedFiles) != 0 || before != hashLedger(t, f.projectRoot) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestTwoPacketWorkflowIsIncrementalAndIdempotent(t *testing.T) {
	f := newMultiPacketFixture(t)
	p1 := f.prepare(t, true)
	r1, err := Run(f.applyOptions(t, p1, "valid-first.json"))
	if err != nil || !r1.CursorAdvanced || !p1.HasMore {
		t.Fatalf("r1=%+v err=%v p1=%+v", r1, err, p1)
	}

	reportPath := filepath.Join(f.projectRoot, "docs", "session-review", "sessions", "session-report-1.md")
	reportBody, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	reportBody = bytes.Replace(reportBody, []byte("---\n"), []byte("---\nuser_extension: keep-me\n"), 1)
	reportBody = append(reportBody, []byte("\n## User Notes\n\nKeep this exact editable note.\n")...)
	if err := os.WriteFile(reportPath, reportBody, 0o644); err != nil {
		t.Fatal(err)
	}
	f.initializeGitBaseline(t)
	gitBefore := snapshotTree(t, filepath.Join(f.projectRoot, ".git"))

	p2 := f.prepare(t, false)
	if p2.ExpectedCursor != p1.NextCursor || p2.FromCursor != p1.ToCursor+1 || p2.FromCursor != 4 {
		t.Fatalf("p1=%+v p2=%+v", p1, p2)
	}
	opts := f.applyOptions(t, p2, "valid-second.json")
	r2, err := Run(opts)
	if err != nil || !r2.CursorAdvanced || p2.HasMore {
		t.Fatalf("r2=%+v err=%v p2=%+v", r2, err, p2)
	}

	state, err := ledger.Load(f.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	loop := state.OpenLoops["loop-1"]
	report := state.Sessions["session-report-1"]
	if len(state.Sessions) != 1 || report.Revision != 2 || len(state.Timeline) != 2 ||
		state.CurrentState.Revision != 2 || state.CurrentState.NextAction != "Publish the verified ledger" ||
		len(state.Decisions) != 1 || state.Decisions["decision-1"].Revision != 1 ||
		len(state.OpenLoops) != 1 || loop.Revision != 2 || loop.Status != "resolved" {
		t.Fatalf("state=%+v report=%+v loop=%+v", state, report, loop)
	}
	reportBody, err = os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, preserved := range []string{"user_extension: keep-me", "## User Notes", "Keep this exact editable note."} {
		if !bytes.Contains(reportBody, []byte(preserved)) {
			t.Fatalf("updated report lost editable unknown content %q", preserved)
		}
	}
	diagramPath := filepath.Join(f.projectRoot, "docs", "session-review", "diagrams", "project-evolution.md")
	diagramBody, err := os.ReadFile(diagramPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(diagramBody, []byte("```mermaid")) != 3 {
		t.Fatalf("derived diagram does not contain the recovery mainline and both appendices: %q", diagramBody)
	}

	before := snapshotTree(t, f.projectRoot)
	dataBefore := snapshotTree(t, f.dataDir)
	again, err := Run(opts)
	if err != nil || !again.AlreadyApplied || len(again.ChangedFiles) != 0 {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	if after := snapshotTree(t, f.projectRoot); after != before {
		t.Fatalf("reapply changed bytes, hashes, modes, or mtimes\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if dataAfter := snapshotTree(t, f.dataDir); dataAfter != dataBefore {
		t.Fatalf("reapply changed receipt or cursor bytes, hashes, modes, or mtimes\nbefore:\n%s\nafter:\n%s", dataBefore, dataAfter)
	}
	if got := snapshotTree(t, filepath.Join(f.projectRoot, ".git")); got != gitBefore {
		t.Fatalf("apply mutated Git metadata\nbefore:\n%s\nafter:\n%s", gitBefore, got)
	}
	stableDiagram, err := os.ReadFile(diagramPath)
	if err != nil || !bytes.Equal(stableDiagram, diagramBody) {
		t.Fatalf("derived diagram changed on reapply: err=%v", err)
	}
}

func TestRunStaleCursorWritesNothing(t *testing.T) {
	f := newApplyTestFixture(t)
	if err := os.MkdirAll(f.projectData, 0o700); err != nil {
		t.Fatal(err)
	}
	store := cursor.Store{Root: f.projectData}
	next := cursor.Cursor{SessionID: testSessionID, LastLine: 1, LastHash: testHashA, UpdatedAt: f.now.Add(-time.Hour)}
	if err := store.Commit(testSessionID, cursor.Cursor{}, next); err != nil {
		t.Fatal(err)
	}
	before := hashLedger(t, f.projectRoot)
	_, err := Run(f.options())
	if !errors.Is(err, cursor.ErrStale) || before != hashLedger(t, f.projectRoot) {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(receiptPathForTest(t, f)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("receipt created for stale input: %v", statErr)
	}
}

func TestRunExpectedProjectRootRejectsReplacementBeforeWrites(t *testing.T) {
	f := newApplyTestFixture(t)
	expected := pinnedApplyDirectoryInfo(t, f.projectRoot)
	opts := f.options()
	opts.ExpectedProjectRoot = expected
	original := f.projectRoot + "-expected-original"
	if err := os.Rename(f.projectRoot, original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(f.projectRoot)
		_ = os.Rename(original, f.projectRoot)
	})
	if err := copyTreeForTest(original, f.projectRoot); err != nil {
		t.Fatal(err)
	}
	replacementBefore := snapshotTree(t, f.projectRoot)
	originalBefore := snapshotTree(t, original)
	dataBefore := snapshotTree(t, f.dataDir)

	got, err := Run(opts)
	if err == nil || got.ProjectID != "" || got.SessionID != "" || got.FromCursor != 0 || got.ToCursor != 0 || len(got.ChangedFiles) != 0 || got.CursorAdvanced || got.AlreadyApplied {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if snapshotTree(t, f.projectRoot) != replacementBefore {
		t.Fatal("replacement project was written")
	}
	if snapshotTree(t, original) != originalBefore {
		t.Fatal("expected project was written")
	}
	if snapshotTree(t, f.dataDir) != dataBefore {
		t.Fatal("data directory was written")
	}
}

func TestRunRecoversPreparedAndPartialTransactions(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook func(*Options)
	}{
		{name: "prepared", hook: func(opts *Options) {
			opts.hooks.afterPreparedReceipt = func() error { return errors.New("injected crash after prepared receipt") }
		}},
		{name: "partial", hook: func(opts *Options) {
			opts.hooks.afterFile = func(index int, _ string) error {
				if index == 0 {
					return errors.New("injected crash after first file")
				}
				return nil
			}
		}},
		{name: "applied", hook: func(opts *Options) {
			opts.hooks.afterAppliedReceipt = func() error { return errors.New("injected crash after applied receipt") }
		}},
		{name: "before-cas", hook: func(opts *Options) {
			opts.hooks.beforeCAS = func() error { return errors.New("injected crash before cursor CAS") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newApplyTestFixture(t)
			opts := f.options()
			tc.hook(&opts)
			if _, err := Run(opts); err == nil {
				t.Fatal("injected interruption did not stop apply")
			}
			got, err := Run(f.options())
			if err != nil || !got.CursorAdvanced || len(got.ChangedFiles) == 0 {
				t.Fatalf("recovery got=%+v err=%v", got, err)
			}
		})
	}
}

func TestRunRetryResyncsUncertainLedgerBeforeCAS(t *testing.T) {
	f := newApplyTestFixture(t)
	firstSyncErr := errors.New("injected target publication sync failure")
	first := f.options()
	first.hooks.applyPlan = func(plan ledger.WritePlan, expectedRoot os.FileInfo) ([]string, error) {
		changed, err := ledger.ApplyExpected(plan, expectedRoot)
		if err != nil {
			return changed, err
		}
		return changed, firstSyncErr
	}
	if got, err := Run(first); !errors.Is(err, firstSyncErr) || got.CursorAdvanced || got.AlreadyApplied {
		t.Fatalf("first run got=%+v err=%v", got, err)
	}

	secondSyncErr := errors.New("injected target retry sync failure")
	var syncOrder []string
	second := f.options()
	second.hooks.syncPublication = func(root *os.Root, path string) error {
		syncOrder = append(syncOrder, filepath.ToSlash(path))
		if strings.HasPrefix(filepath.ToSlash(path), "docs/session-review/") {
			return secondSyncErr
		}
		return atomicfile.SyncRootPublication(root, path)
	}
	if got, err := Run(second); !errors.Is(err, secondSyncErr) || got.CursorAdvanced || got.AlreadyApplied {
		t.Fatalf("second run got=%+v err=%v syncOrder=%v", got, err, syncOrder)
	}
	if len(syncOrder) < 2 || !strings.HasSuffix(syncOrder[0], ".json") || !strings.HasPrefix(syncOrder[1], "docs/session-review/") {
		t.Fatalf("sync order=%v", syncOrder)
	}
	assertCursorNotAdvanced(t, f)

	third := f.options()
	var receiptSynced, targetSynced bool
	third.hooks.syncPublication = func(root *os.Root, path string) error {
		if strings.HasSuffix(path, ".json") {
			receiptSynced = true
		}
		if strings.HasPrefix(filepath.ToSlash(path), "docs/session-review/") {
			targetSynced = true
		}
		return atomicfile.SyncRootPublication(root, path)
	}
	third.hooks.beforeCAS = func() error {
		if !receiptSynced || !targetSynced {
			return fmt.Errorf("CAS reached before publication resync: receipt=%v target=%v", receiptSynced, targetSynced)
		}
		return nil
	}
	if got, err := Run(third); err != nil || !got.CursorAdvanced || got.AlreadyApplied {
		t.Fatalf("third run got=%+v err=%v", got, err)
	}
}

func TestRunAlreadyAppliedRequiresReceiptAndLedgerResync(t *testing.T) {
	f := newApplyTestFixture(t)
	if _, err := Run(f.options()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		fail func(string) bool
	}{
		{name: "receipt", fail: func(path string) bool { return strings.HasSuffix(path, ".json") }},
		{name: "ledger", fail: func(path string) bool { return strings.HasPrefix(filepath.ToSlash(path), "docs/session-review/") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			syncErr := errors.New("injected existing publication sync failure")
			opts := f.options()
			opts.hooks.syncPublication = func(root *os.Root, path string) error {
				if tc.fail(path) {
					return syncErr
				}
				return atomicfile.SyncRootPublication(root, path)
			}
			if got, err := Run(opts); !errors.Is(err, syncErr) || got.AlreadyApplied || got.CursorAdvanced {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestRunReceiptScanBudgetsActualReadsAfterGrowth(t *testing.T) {
	f := newApplyTestFixture(t)
	stop := f.options()
	stop.hooks.afterPreparedReceipt = func() error { return errors.New("stop after prepared receipt") }
	if _, err := Run(stop); err == nil {
		t.Fatal("expected interruption")
	}
	receiptPath := receiptPathForTest(t, f)
	info, err := os.Stat(receiptPath)
	if err != nil {
		t.Fatal(err)
	}

	opts := f.options()
	opts.hooks.receiptScanByteLimit = uint64(info.Size()) + 8
	opts.hooks.afterReceiptEnumeration = func() error {
		file, err := os.OpenFile(receiptPath, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(bytes.Repeat([]byte("x"), 16))
		return errors.Join(writeErr, file.Close())
	}
	if got, err := Run(opts); err == nil || !strings.Contains(err.Error(), "aggregate size") || got.CursorAdvanced || got.AlreadyApplied {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	assertCursorNotAdvanced(t, f)
}

func TestRunReceiptScanRejectsDirectoryReplacementAfterEnumeration(t *testing.T) {
	f := newApplyTestFixture(t)
	stop := f.options()
	stop.hooks.afterPreparedReceipt = func() error { return errors.New("stop after prepared receipt") }
	if _, err := Run(stop); err == nil {
		t.Fatal("expected interruption")
	}
	receiptDir := filepath.Join(f.projectData, "applied-proposals")
	moved := receiptDir + "-moved"

	opts := f.options()
	opts.hooks.afterReceiptEnumeration = func() error {
		if err := os.Rename(receiptDir, moved); err != nil {
			return err
		}
		return os.Mkdir(receiptDir, 0o700)
	}
	if got, err := Run(opts); err == nil || !strings.Contains(err.Error(), "directory identity") || got.CursorAdvanced || got.AlreadyApplied {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := os.Stat(receiptPathForTest(t, f)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement receipt directory gained current digest: %v", err)
	}
	assertCursorNotAdvanced(t, f)
}

func TestRunCleansOnlyExactReceiptTemporaryNamesDurably(t *testing.T) {
	t.Run("exact uncertain removal is resynced", func(t *testing.T) {
		f := newApplyTestFixture(t)
		directory := filepath.Join(f.projectData, "applied-proposals")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		name := ".session-reviewer-" + strings.Repeat("a", 32)
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
		removeErr := errors.New("injected orphan removal sync failure")
		first := f.options()
		first.hooks.removeReceipt = func(root *os.Root, candidate string) error {
			if candidate != name {
				t.Fatalf("remove candidate=%q", candidate)
			}
			if err := atomicfile.RemoveRoot(root, candidate); err != nil {
				return err
			}
			return removeErr
		}
		if got, err := Run(first); !errors.Is(err, removeErr) || got.CursorAdvanced || got.AlreadyApplied {
			t.Fatalf("first got=%+v err=%v", got, err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orphan still exists: %v", err)
		}

		syncErr := errors.New("injected receipt directory retry sync failure")
		second := f.options()
		second.hooks.syncReceiptDirectory = func(*os.Root) error { return syncErr }
		if got, err := Run(second); !errors.Is(err, syncErr) || got.CursorAdvanced || got.AlreadyApplied {
			t.Fatalf("second got=%+v err=%v", got, err)
		}
		assertCursorNotAdvanced(t, f)

		if got, err := Run(f.options()); err != nil || !got.CursorAdvanced {
			t.Fatalf("final got=%+v err=%v", got, err)
		}
	})

	t.Run("near match is not removed", func(t *testing.T) {
		f := newApplyTestFixture(t)
		directory := filepath.Join(f.projectData, "applied-proposals")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		name := ".session-reviewer-" + strings.Repeat("a", 31)
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("not an exact temp name"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "receipt name") || got.CursorAdvanced {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("near-match was removed: %v", err)
		}
	})
}

func TestRunPublishedReceiptFailureIsRecoveredOnlyAfterResync(t *testing.T) {
	f := newApplyTestFixture(t)
	publicationErr := errors.New("injected receipt publication failure")
	first := f.options()
	writes := 0
	first.hooks.writeReceipt = func(root *os.Root, path string, data []byte, perm fs.FileMode) error {
		writes++
		if err := atomicfile.WriteRoot(root, path, data, perm); err != nil {
			return err
		}
		return publicationErr
	}
	if got, err := Run(first); !errors.Is(err, publicationErr) || writes != 1 || got.CursorAdvanced || got.AlreadyApplied {
		t.Fatalf("first got=%+v err=%v writes=%d", got, err, writes)
	}
	if _, err := os.Stat(receiptPathForTest(t, f)); err != nil {
		t.Fatalf("receipt was not actually published: %v", err)
	}

	retryErr := errors.New("injected receipt retry sync failure")
	retry := f.options()
	retry.hooks.syncPublication = func(*os.Root, string) error { return retryErr }
	if got, err := Run(retry); !errors.Is(err, retryErr) || got.CursorAdvanced || got.AlreadyApplied {
		t.Fatalf("retry got=%+v err=%v", got, err)
	}
	assertCursorNotAdvanced(t, f)
}

func TestRunRetriesExistingApplyDirectoriesBeforeCrossingBoundary(t *testing.T) {
	for _, target := range []string{"projects", testProjectID, "applied-proposals"} {
		t.Run(target, func(t *testing.T) {
			f := newApplyTestFixture(t)
			firstErr := errors.New("injected apply directory creation sync failure")
			first := f.options()
			first.hooks.ensureRootDir = func(root *os.Root, name string, perm fs.FileMode) error {
				if err := atomicfile.EnsureRootDir(root, name, perm); err != nil {
					return err
				}
				if name == target {
					return firstErr
				}
				return nil
			}
			if got, err := Run(first); !errors.Is(err, firstErr) || got.CursorAdvanced || got.AlreadyApplied {
				t.Fatalf("first got=%+v err=%v", got, err)
			}

			retryErr := errors.New("injected existing apply directory sync failure")
			var retryNames []string
			retry := f.options()
			retry.hooks.ensureRootDir = func(_ *os.Root, name string, _ fs.FileMode) error {
				retryNames = append(retryNames, name)
				if name == target {
					return retryErr
				}
				return nil
			}
			if got, err := Run(retry); !errors.Is(err, retryErr) || got.CursorAdvanced || got.AlreadyApplied {
				t.Fatalf("retry got=%+v err=%v names=%v", got, err, retryNames)
			}
			if _, err := os.Stat(f.projectData); err == nil {
				assertCursorNotAdvanced(t, f)
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("inspect project data: %v", err)
			}
		})
	}
}

func TestRunReceiptScanRejectsEntriesAddedAfterEnumeration(t *testing.T) {
	for _, tc := range []struct {
		name string
		add  func(*testing.T, *applyTestFixture)
	}{
		{name: "invalid extra entry", add: func(t *testing.T, f *applyTestFixture) {
			path := filepath.Join(f.projectData, "applied-proposals", "not-a-receipt.json")
			if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "conflicting valid receipt", add: func(t *testing.T, f *applyTestFixture) {
			ctx := inputContext{
				Packet: f.packet, ProposalDigest: "sha256:" + strings.Repeat("c", 64),
				EvidenceFileDigest: "sha256:" + testHashA, EvidencePacketDigest: "sha256:" + testHashB,
			}
			receipt, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: []ledger.PlannedFile{}}, digestLedgerSnapshot(nil))
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(f.projectData)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := saveReceipt(root, receipt); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newApplyTestFixture(t)
			if err := os.MkdirAll(filepath.Join(f.projectData, "applied-proposals"), 0o700); err != nil {
				t.Fatal(err)
			}
			opts := f.options()
			opts.hooks.afterReceiptEnumeration = func() error {
				tc.add(t, f)
				return nil
			}
			if got, err := Run(opts); err == nil || !strings.Contains(err.Error(), "changed during scan") || got.CursorAdvanced || got.AlreadyApplied {
				t.Fatalf("got=%+v err=%v", got, err)
			}
			if _, err := os.Stat(receiptPathForTest(t, f)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("scan addition allowed a new current digest: %v", err)
			}
			assertCursorNotAdvanced(t, f)
		})
	}
}

func TestRunRejectsDifferentProposalWhileSameBoundaryReceiptIsPending(t *testing.T) {
	f := newApplyTestFixture(t)
	opts := f.options()
	opts.hooks.afterFile = func(index int, _ string) error {
		if index == 0 {
			return errors.New("stop after first file")
		}
		return nil
	}
	if _, err := Run(opts); err == nil {
		t.Fatal("interruption was not injected")
	}
	aReceipt := receiptPathForTest(t, f)
	beforeLedger := snapshotTree(t, f.projectRoot)
	beforeState := snapshotTree(t, f.projectData)

	body, err := os.ReadFile(f.proposalPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(body, []byte("Long sessions need durable continuity."), []byte("Long sessions need auditable continuity."), 1)
	if bytes.Equal(body, changed) {
		t.Fatal("proposal B did not differ from proposal A")
	}
	if err := os.WriteFile(f.proposalPath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	bReceipt := receiptPathForTest(t, f)
	if aReceipt == bReceipt {
		t.Fatal("proposal digests unexpectedly match")
	}

	if _, err := Run(f.options()); !errors.Is(err, ErrPendingReceiptConflict) || err.Error() != "pending apply receipt conflicts with current proposal" {
		t.Fatalf("err=%v", err)
	}
	if got := snapshotTree(t, f.projectRoot); got != beforeLedger {
		t.Fatalf("pending conflict changed ledger\nbefore:\n%s\nafter:\n%s", beforeLedger, got)
	}
	if got := snapshotTree(t, f.projectData); got != beforeState {
		t.Fatalf("pending conflict changed transaction state\nbefore:\n%s\nafter:\n%s", beforeState, got)
	}
	if _, err := os.Stat(bReceipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proposal B receipt exists: %v", err)
	}
}

func TestRunRejectsDifferentProposalWhileAnyPreparedReceiptOwnsProject(t *testing.T) {
	t.Run("changed end boundary", func(t *testing.T) {
		f := newApplyTestFixture(t)
		stop := f.options()
		stop.hooks.afterPreparedReceipt = func() error { return errors.New("stop after prepared receipt") }
		if _, err := Run(stop); err == nil {
			t.Fatal("expected interruption")
		}
		beforeLedger := snapshotTree(t, f.projectRoot)
		beforeState := snapshotTree(t, f.projectData)

		f.packet.ToCursor = 3
		f.packet.NextCursor = evidence.CursorBoundary{Line: 3, SourceHash: testHashA}
		f.packet.Events = append(f.packet.Events, evidence.Item{
			ID: "ev-later", Timestamp: "2026-08-23T01:04:03Z", JSONLLine: 3,
			SourceHash: testHashA, Kind: "message", Role: "assistant", Summary: "Later boundary",
		})
		rewriteFixtureInputs(t, f)

		if _, err := Run(f.options()); !errors.Is(err, ErrPendingReceiptConflict) {
			t.Fatalf("err=%v", err)
		}
		if got := snapshotTree(t, f.projectRoot); got != beforeLedger {
			t.Fatalf("conflict changed ledger\nbefore:\n%s\nafter:\n%s", beforeLedger, got)
		}
		if got := snapshotTree(t, f.projectData); got != beforeState {
			t.Fatalf("conflict changed transaction state\nbefore:\n%s\nafter:\n%s", beforeState, got)
		}
	})

	t.Run("different session after partial ledger", func(t *testing.T) {
		f := newApplyTestFixture(t)
		stop := f.options()
		stop.hooks.afterFile = func(index int, _ string) error {
			if index == 0 {
				return errors.New("stop after first ledger file")
			}
			return nil
		}
		if _, err := Run(stop); err == nil {
			t.Fatal("expected interruption")
		}
		beforeLedger := snapshotTree(t, f.projectRoot)
		beforeState := snapshotTree(t, f.projectData)

		f.packet.SessionID = "session-other"
		rewriteFixtureInputs(t, f)
		if _, err := Run(f.options()); !errors.Is(err, ErrPendingReceiptConflict) {
			t.Fatalf("err=%v", err)
		}
		if got := snapshotTree(t, f.projectRoot); got != beforeLedger {
			t.Fatalf("conflict changed partial ledger\nbefore:\n%s\nafter:\n%s", beforeLedger, got)
		}
		if got := snapshotTree(t, f.projectData); got != beforeState {
			t.Fatalf("conflict changed transaction state\nbefore:\n%s\nafter:\n%s", beforeState, got)
		}
	})

	t.Run("different session while applied receipt lacks cursor CAS", func(t *testing.T) {
		f := newApplyTestFixture(t)
		stop := f.options()
		stop.hooks.afterAppliedReceipt = func() error { return errors.New("stop before cursor CAS") }
		if _, err := Run(stop); err == nil {
			t.Fatal("expected interruption")
		}
		f.packet.SessionID = "session-other"
		rewriteFixtureInputs(t, f)
		if _, err := Run(f.options()); !errors.Is(err, ErrPendingReceiptConflict) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestRunLaterCursorWithoutExactReceiptIsPureStalePreflight(t *testing.T) {
	f := newApplyTestFixture(t)
	if err := os.MkdirAll(f.projectData, 0o700); err != nil {
		t.Fatal(err)
	}
	advanced := cursor.Cursor{SessionID: testSessionID, LastLine: 3, LastHash: testHashA, UpdatedAt: f.now}
	if err := (cursor.Store{Root: f.projectData}).Commit(testSessionID, cursor.Cursor{}, advanced); err != nil {
		t.Fatal(err)
	}
	receipts := filepath.Join(f.projectData, "applied-proposals")
	if err := os.Mkdir(receipts, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(receipts, ".session-reviewer-"+strings.Repeat("a", 32))
	if err := os.WriteFile(orphan, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, f.dataDir)
	if got, err := Run(f.options()); !errors.Is(err, cursor.ErrStale) || got.CursorAdvanced || got.AlreadyApplied {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if after := snapshotTree(t, f.dataDir); after != before {
		t.Fatalf("later-cursor stale preflight mutated state\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestOutstandingReceiptOwnershipAllowsOnlyConclusiveAppliedReceipt(t *testing.T) {
	root := t.TempDir()
	store := cursor.Store{Root: root}
	next := evidence.CursorBoundary{Line: 2, SourceHash: testHashB}
	advanced := cursor.Cursor{SessionID: "session-other", LastLine: next.Line, LastHash: next.SourceHash, UpdatedAt: time.Now().UTC()}
	if err := store.Commit("session-other", cursor.Cursor{}, advanced); err != nil {
		t.Fatal(err)
	}
	applied := applyReceipt{State: receiptApplied, SessionID: "session-other", NextCursor: next}
	if err := rejectOutstandingProjectReceipts(store, []applyReceipt{applied}); err != nil {
		t.Fatalf("conclusive applied receipt blocked: %v", err)
	}
	prepared := applied
	prepared.State = receiptPrepared
	if err := rejectOutstandingProjectReceipts(store, []applyReceipt{prepared}); !errors.Is(err, ErrPendingReceiptConflict) {
		t.Fatalf("prepared receipt err=%v", err)
	}
	uncommitted := applied
	uncommitted.SessionID = "session-uncommitted"
	if err := rejectOutstandingProjectReceipts(store, []applyReceipt{uncommitted}); !errors.Is(err, ErrPendingReceiptConflict) {
		t.Fatalf("uncommitted applied receipt err=%v", err)
	}
}

func TestExactReceiptReadOnlyPreflightBoundsEveryDirectoryScan(t *testing.T) {
	for _, level := range []string{"projects", "project-data", "receipts"} {
		t.Run(level, func(t *testing.T) {
			dataDir := t.TempDir()
			projects := filepath.Join(dataDir, "projects")
			projectData := filepath.Join(projects, testProjectID)
			receipts := filepath.Join(projectData, "applied-proposals")
			if err := os.MkdirAll(receipts, 0o700); err != nil {
				t.Fatal(err)
			}
			var directory string
			switch level {
			case "projects":
				directory = projects
			case "project-data":
				directory = projectData
			case "receipts":
				directory = receipts
			}
			for index := 0; index < maxReceiptPreflightEntries+1; index++ {
				name := fmt.Sprintf("extra-%04d", index)
				if err := os.WriteFile(filepath.Join(directory, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotTree(t, dataDir)
			ctx := inputContext{Packet: evidence.Packet{ProjectID: testProjectID}, ProposalDigest: "sha256:" + testHashA}
			_, _, err = loadExactReceiptReadOnly(root, ctx)
			closeErr := root.Close()
			if err == nil || !strings.Contains(err.Error(), "entry count exceeds") {
				t.Fatalf("err=%v", err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if after := snapshotTree(t, dataDir); after != before {
				t.Fatalf("bounded preflight mutated state\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestRunAfterRenderFailureWritesNoReceiptOrLedger(t *testing.T) {
	f := newApplyTestFixture(t)
	before := hashLedger(t, f.projectRoot)
	opts := f.options()
	opts.hooks.afterRender = func() error { return errors.New("stop after render") }
	if _, err := Run(opts); err == nil {
		t.Fatal("expected injected failure")
	}
	if after := hashLedger(t, f.projectRoot); after != before {
		t.Fatal("render failure changed ledger")
	}
	if _, err := os.Stat(receiptPathForTest(t, f)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt exists: %v", err)
	}
}

func TestRunRejectsUnplannedLedgerDocumentAddedAfterRender(t *testing.T) {
	f := newApplyTestFixture(t)
	opts := f.options()
	injected := filepath.Join(f.projectRoot, "docs", "session-review", "open-loops", "decision-1.md")
	opts.hooks.afterRender = func() error {
		if err := os.MkdirAll(filepath.Dir(injected), 0o755); err != nil {
			return err
		}
		return os.WriteFile(injected, openLoopFixtureDocument("decision-1"), 0o644)
	}
	if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "ledger namespace has an intervening") {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(receiptPathForTest(t, f)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.projectRoot, "docs", "session-review", "current-state.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planned ledger write occurred: %v", err)
	}
	if _, err := os.Stat(injected); err != nil {
		t.Fatalf("unplanned user document was not preserved: %v", err)
	}
	assertCursorNotAdvanced(t, f)
}

func TestPreparedReceiptPinsWholeLedgerNamespaceAcrossRecovery(t *testing.T) {
	f := newApplyTestFixture(t)
	injected := filepath.Join(f.projectRoot, "docs", "session-review", "open-loops", "external-loop.md")
	opts := f.options()
	opts.hooks.afterPreparedReceipt = func() error {
		if err := os.MkdirAll(filepath.Dir(injected), 0o755); err != nil {
			return err
		}
		return os.WriteFile(injected, openLoopFixtureDocument("external-loop"), 0o644)
	}
	if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "ledger namespace has an intervening") {
		t.Fatalf("initial err=%v", err)
	}
	if _, err := os.Stat(receiptPathForTest(t, f)); err != nil {
		t.Fatalf("prepared receipt missing: %v", err)
	}
	if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "ledger namespace has an intervening") {
		t.Fatalf("recovery err=%v", err)
	}
	assertCursorNotAdvanced(t, f)
	if err := os.Remove(injected); err != nil {
		t.Fatal(err)
	}
	got, err := Run(f.options())
	if err != nil || !got.CursorAdvanced {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func openLoopFixtureDocument(id string) []byte {
	return []byte("---\nid: " + id + "\nentity_type: open_loop\nproject_id: " + testProjectID + "\nrevision: 1\ntitle: External loop\nstatus: open\ntags: []\nsource_sessions: []\nevidence: []\n---\n\n# External loop\n\n## Attempted paths\n")
}

func TestRunPartialRecoveryFailsClosedOnUserEdit(t *testing.T) {
	f := newApplyTestFixture(t)
	opts := f.options()
	opts.hooks.afterFile = func(index int, _ string) error {
		if index == 0 {
			return errors.New("stop after first file")
		}
		return nil
	}
	if _, err := Run(opts); err == nil {
		t.Fatal("interruption was not injected")
	}
	body, err := os.ReadFile(receiptPathForTest(t, f))
	if err != nil {
		t.Fatal(err)
	}
	var raw applyReceipt
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Files) < 2 {
		t.Fatalf("receipt files=%d", len(raw.Files))
	}
	target := raw.Files[1].RelativePath
	path := filepath.Join(f.projectRoot, filepath.FromSlash(target))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "intervening user edit") {
		t.Fatalf("err=%v", err)
	}
	c, err := (cursor.Store{Root: f.projectData}).Load(testSessionID)
	if err != nil || c != (cursor.Cursor{}) {
		t.Fatalf("cursor=%+v err=%v", c, err)
	}
}

func TestRunRejectsCorruptAndRedirectedReceipts(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		f := newApplyTestFixture(t)
		opts := f.options()
		opts.hooks.afterPreparedReceipt = func() error { return errors.New("stop") }
		if _, err := Run(opts); err == nil {
			t.Fatal("expected interruption")
		}
		path := receiptPathForTest(t, f)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body[len(body)/2] ^= 1
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(f.options()); err == nil {
			t.Fatal("accepted corrupt receipt")
		}
	})

	t.Run("symlink directory", func(t *testing.T) {
		f := newApplyTestFixture(t)
		if err := os.MkdirAll(f.projectData, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(f.projectData, "applied-proposals")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := Run(f.options()); err == nil {
			t.Fatal("accepted redirected receipt directory")
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Fatalf("outside entries=%v err=%v", entries, err)
		}
	})

	t.Run("case collision", func(t *testing.T) {
		f := newApplyTestFixture(t)
		opts := f.options()
		opts.hooks.afterPreparedReceipt = func() error { return errors.New("stop") }
		if _, err := Run(opts); err == nil {
			t.Fatal("expected interruption")
		}
		path := receiptPathForTest(t, f)
		upper := filepath.Join(filepath.Dir(path), strings.ToUpper(filepath.Base(path)))
		if upper == path {
			t.Skip("receipt name has no case variant")
		}
		if err := os.Rename(path, upper); err != nil {
			t.Skipf("case-only rename unavailable: %v", err)
		}
		if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "case-colliding") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestRunScansEveryReceiptBeforeCurrentProposal(t *testing.T) {
	badBodies := map[string][]byte{
		"malformed":     []byte("{"),
		"unknown field": []byte(`{"unknown":true}`),
		"duplicate key": []byte(`{"schema_version":1,"schema_version":1}`),
	}
	for name, badBody := range badBodies {
		t.Run(name, func(t *testing.T) {
			f := newApplyTestFixture(t)
			directory := filepath.Join(f.projectData, "applied-proposals")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			badName := strings.Repeat("c", 64) + ".json"
			if err := os.WriteFile(filepath.Join(directory, badName), badBody, 0o600); err != nil {
				t.Fatal(err)
			}
			before := hashLedger(t, f.projectRoot)
			if _, err := Run(f.options()); err == nil {
				t.Fatal("accepted invalid non-current receipt")
			}
			if after := hashLedger(t, f.projectRoot); after != before {
				t.Fatal("invalid receipt scan changed ledger")
			}
			assertCursorNotAdvanced(t, f)
		})
	}

	t.Run("nonregular", func(t *testing.T) {
		f := newApplyTestFixture(t)
		bad := filepath.Join(f.projectData, "applied-proposals", strings.Repeat("c", 64)+".json")
		if err := os.MkdirAll(bad, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "not regular") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("invalid digest name", func(t *testing.T) {
		f := newApplyTestFixture(t)
		directory := filepath.Join(f.projectData, "applied-proposals")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "not-a-digest.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "receipt name") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("case collision", func(t *testing.T) {
		f := newApplyTestFixture(t)
		opts := f.options()
		opts.hooks.afterPreparedReceipt = func() error { return errors.New("stop") }
		if _, err := Run(opts); err == nil {
			t.Fatal("expected interruption")
		}
		lower := receiptPathForTest(t, f)
		upper := filepath.Join(filepath.Dir(lower), strings.ToUpper(filepath.Base(lower)))
		if err := os.WriteFile(upper, []byte("{}"), 0o600); err != nil {
			t.Skipf("case-colliding filename unavailable: %v", err)
		}
		entries, err := os.ReadDir(filepath.Dir(lower))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) < 2 {
			t.Skip("filesystem is case-insensitive")
		}
		if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "case-colliding") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestRunBoundsReceiptDirectoryScan(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		f := newApplyTestFixture(t)
		directory := filepath.Join(f.projectData, "applied-proposals")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 4097; index++ {
			name := fmt.Sprintf("%064x.json", index+1)
			if err := os.WriteFile(filepath.Join(directory, name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "receipt count") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("aggregate size", func(t *testing.T) {
		f := newApplyTestFixture(t)
		directory := filepath.Join(f.projectData, "applied-proposals")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 5; index++ {
			name := fmt.Sprintf("%064x.json", index+1)
			path := filepath.Join(directory, name)
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, maxReceiptBytes); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "aggregate size") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestRunRejectsPreparedReceiptForDifferentSessionOrBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*inputContext)
	}{
		{name: "different session", mutate: func(ctx *inputContext) { ctx.Packet.SessionID = "session-other" }},
		{name: "different boundary", mutate: func(ctx *inputContext) {
			ctx.Packet.FromCursor = 2
			ctx.Packet.ToCursor = 3
			ctx.Packet.ExpectedCursor = evidence.CursorBoundary{Line: 1, SourceHash: testHashA}
			ctx.Packet.NextCursor = evidence.CursorBoundary{Line: 3, SourceHash: testHashA}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newApplyTestFixture(t)
			ctx := inputContext{Packet: f.packet, ProposalDigest: "sha256:" + strings.Repeat("c", 64), EvidenceFileDigest: "sha256:" + testHashA, EvidencePacketDigest: "sha256:" + testHashB}
			tc.mutate(&ctx)
			receipt, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: []ledger.PlannedFile{}}, digestLedgerSnapshot(nil))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(f.projectData, 0o700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(f.projectData)
			if err != nil {
				t.Fatal(err)
			}
			if err := saveReceipt(root, receipt); err != nil {
				_ = root.Close()
				t.Fatal(err)
			}
			if err := root.Close(); err != nil {
				t.Fatal(err)
			}
			beforeLedger := snapshotTree(t, f.projectRoot)
			beforeState := snapshotTree(t, f.projectData)
			if got, err := Run(f.options()); !errors.Is(err, ErrPendingReceiptConflict) || got.CursorAdvanced || got.AlreadyApplied {
				t.Fatalf("got=%+v err=%v", got, err)
			}
			if got := snapshotTree(t, f.projectRoot); got != beforeLedger {
				t.Fatal("pending receipt changed ledger")
			}
			if got := snapshotTree(t, f.projectData); got != beforeState {
				t.Fatal("pending receipt changed transaction state")
			}
		})
	}
}

func TestRunRejectsBoundedOrChangingInputsBeforeStateWrites(t *testing.T) {
	t.Run("oversized proposal", func(t *testing.T) {
		f := newApplyTestFixture(t)
		if err := os.WriteFile(f.proposalPath, bytes.Repeat([]byte(" "), maxInputBytes+1), 0o600); err != nil {
			t.Fatal(err)
		}
		beforeState := snapshotTree(t, f.projectData)
		beforeLedger := snapshotTree(t, f.projectRoot)
		if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("err=%v", err)
		}
		if afterState := snapshotTree(t, f.projectData); afterState != beforeState {
			t.Fatalf("rejected oversized input changed precreated project state bytes, hashes, modes, or mtimes\nbefore:\n%s\nafter:\n%s", beforeState, afterState)
		}
		if afterLedger := snapshotTree(t, f.projectRoot); afterLedger != beforeLedger {
			t.Fatalf("rejected oversized input changed ledger bytes, hashes, modes, or mtimes\nbefore:\n%s\nafter:\n%s", beforeLedger, afterLedger)
		}
	})

	t.Run("changes during read", func(t *testing.T) {
		f := newApplyTestFixture(t)
		opts := f.options()
		opts.hooks.duringInputRead = func(kind string) error {
			if kind == "proposal" {
				file, err := os.OpenFile(f.proposalPath, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					return err
				}
				_, writeErr := file.WriteString(" ")
				return errors.Join(writeErr, file.Close())
			}
			return nil
		}
		beforeState := snapshotTree(t, f.projectData)
		beforeLedger := snapshotTree(t, f.projectRoot)
		if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "changed while reading") {
			t.Fatalf("err=%v", err)
		}
		if afterState := snapshotTree(t, f.projectData); afterState != beforeState {
			t.Fatalf("rejected changing input changed precreated project state bytes, hashes, modes, or mtimes\nbefore:\n%s\nafter:\n%s", beforeState, afterState)
		}
		if afterLedger := snapshotTree(t, f.projectRoot); afterLedger != beforeLedger {
			t.Fatalf("rejected changing input changed ledger bytes, hashes, modes, or mtimes\nbefore:\n%s\nafter:\n%s", beforeLedger, afterLedger)
		}
	})
}

func TestRunRechecksTargetsAfterBeforeCASHook(t *testing.T) {
	f := newApplyTestFixture(t)
	opts := f.options()
	opts.hooks.beforeCAS = func() error {
		return os.WriteFile(filepath.Join(f.projectRoot, "docs", "session-review", "current-state.md"), []byte("user edit\n"), 0o644)
	}
	if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "does not match applied receipt") {
		t.Fatalf("err=%v", err)
	}
	c, err := (cursor.Store{Root: f.projectData}).Load(testSessionID)
	if err != nil || c != (cursor.Cursor{}) {
		t.Fatalf("cursor=%+v err=%v", c, err)
	}
}

func TestRunRechecksTargetsAfterCallerNow(t *testing.T) {
	f := newApplyTestFixture(t)
	opts := f.options()
	opts.Now = func() time.Time {
		if err := os.WriteFile(filepath.Join(f.projectRoot, "docs", "session-review", "current-state.md"), []byte("user edit from Now\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return f.now
	}
	if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "does not match applied receipt") {
		t.Fatalf("err=%v", err)
	}
	assertCursorNotAdvanced(t, f)
}

func TestRunFailsClosedWhenPinnedRootsAreReplaced(t *testing.T) {
	t.Run("project root after render", func(t *testing.T) {
		f := newApplyTestFixture(t)
		original := f.projectRoot + "-original"
		opts := f.options()
		opts.hooks.afterRender = func() error {
			return replaceDirectoryForTest(f.projectRoot, original)
		}
		if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("err=%v", err)
		}
		assertCursorNotAdvanced(t, f)
		if _, err := os.Stat(filepath.Join(f.projectRoot, "docs", "session-review", "current-state.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement project tree was written: %v", err)
		}
	})

	t.Run("data root after render", func(t *testing.T) {
		f := newApplyTestFixture(t)
		original := f.dataDir + "-original"
		before := snapshotTree(t, f.dataDir)
		renameDenied := false
		opts := f.options()
		opts.hooks.afterRender = func() error {
			if err := os.Rename(f.dataDir, original); err != nil {
				renameDenied = runtime.GOOS == "windows" && errors.Is(err, os.ErrPermission)
				return err
			}
			return os.Mkdir(f.dataDir, 0o700)
		}
		_, err := Run(opts)
		if err == nil || (!renameDenied && !strings.Contains(err.Error(), "identity")) {
			t.Fatalf("err=%v", err)
		}
		if renameDenied {
			if after := snapshotTree(t, f.dataDir); after != before {
				t.Fatalf("denied replacement mutated data root\nbefore:\n%s\nafter:\n%s", before, after)
			}
			return
		}
		entries, err := os.ReadDir(f.dataDir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("replacement data tree entries=%v err=%v", entries, err)
		}
		if _, err := os.Stat(filepath.Join(original, "projects", testProjectID, "cursors", testSessionID+".json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cursor advanced in pinned data tree: %v", err)
		}
	})

	t.Run("project data after prepared receipt", func(t *testing.T) {
		f := newApplyTestFixture(t)
		before := hashLedger(t, f.projectRoot)
		original := f.projectData + "-original"
		opts := f.options()
		opts.hooks.afterPreparedReceipt = func() error {
			return replaceDirectoryForTest(f.projectData, original)
		}
		if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("err=%v", err)
		}
		if after := hashLedger(t, f.projectRoot); after != before {
			t.Fatal("ledger changed after project-data replacement")
		}
		entries, err := os.ReadDir(f.projectData)
		if err != nil || len(entries) != 0 {
			t.Fatalf("replacement project-data entries=%v err=%v", entries, err)
		}
		if _, err := os.Stat(filepath.Join(original, "cursors", testSessionID+".json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cursor advanced in pinned project-data tree: %v", err)
		}
	})
}

func TestRunDoesNotDependOnLegacyApplyLockEntry(t *testing.T) {
	f := newApplyTestFixture(t)
	if err := os.MkdirAll(f.projectData, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(f.projectData, ".apply.lock")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := f.options()
	opts.hooks.afterPreparedReceipt = func() error {
		return os.Rename(legacy, legacy+".moved")
	}
	if got, err := Run(opts); err != nil || !got.CursorAdvanced {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("apply recreated legacy lock: %v", err)
	}
}

func TestRunCreatesNoFilesystemApplyLock(t *testing.T) {
	f := newApplyTestFixture(t)
	if _, err := Run(f.options()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.projectData, ".apply.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("filesystem apply lock exists: %v", err)
	}
}

func TestRunRepairsReceiptPrivacyModesUnderLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows exposes only a writable/read-only approximation, not POSIX privacy bits")
	}
	for _, tc := range []struct {
		name  string
		drift func(receiptPath string) error
	}{
		{name: "directory", drift: func(receiptPath string) error { return os.Chmod(filepath.Dir(receiptPath), 0o755) }},
		{name: "file", drift: func(receiptPath string) error { return os.Chmod(receiptPath, 0o644) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newApplyTestFixture(t)
			opts := f.options()
			opts.hooks.afterPreparedReceipt = func() error { return errors.New("stop after prepared") }
			if _, err := Run(opts); err == nil {
				t.Fatal("expected injected interruption")
			}
			path := receiptPathForTest(t, f)
			if err := tc.drift(path); err != nil {
				t.Fatal(err)
			}
			if _, err := Run(f.options()); err != nil {
				t.Fatal(err)
			}
			dirInfo, err := os.Stat(filepath.Dir(path))
			if err != nil || dirInfo.Mode().Perm() != 0o700 {
				t.Fatalf("receipt dir mode=%#o err=%v", dirInfo.Mode().Perm(), err)
			}
			fileInfo, err := os.Stat(path)
			if err != nil || fileInfo.Mode().Perm() != 0o600 {
				t.Fatalf("receipt file mode=%#o err=%v", fileInfo.Mode().Perm(), err)
			}
		})
	}
}

func TestNewPreparedReceiptRejectsAggregateEncodedSizeAndFileCount(t *testing.T) {
	ctx := inputContext{Packet: evidence.Packet{
		ProjectID: testProjectID, SessionID: testSessionID, FromCursor: 1, ToCursor: 1,
		ExpectedCursor: evidence.CursorBoundary{Line: 0}, NextCursor: evidence.CursorBoundary{Line: 1, SourceHash: testHashA},
	}, ProposalDigest: "sha256:" + testHashA, EvidenceFileDigest: "sha256:" + testHashA, EvidencePacketDigest: "sha256:" + testHashA}

	t.Run("aggregate encoded target data", func(t *testing.T) {
		data := bytes.Repeat([]byte("x"), 4<<20)
		files := make([]ledger.PlannedFile, 13)
		for i := range files {
			files[i] = ledger.PlannedFile{RelativePath: fmt.Sprintf("docs/session-review/decisions/decision-%02d.md", i), Data: data, Perm: 0o644}
		}
		if _, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: files}, digestLedgerSnapshot(nil)); err == nil || !strings.Contains(err.Error(), "encoded size") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("file count", func(t *testing.T) {
		files := make([]ledger.PlannedFile, 4097)
		for i := range files {
			files[i] = ledger.PlannedFile{RelativePath: fmt.Sprintf("docs/session-review/decisions/decision-%04d.md", i), Data: []byte("x"), Perm: 0o644}
		}
		if _, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: files}, digestLedgerSnapshot(nil)); err == nil || !strings.Contains(err.Error(), "file count") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestValidateLedgerTargetUsageRejectsUnrecoverableNewDocument(t *testing.T) {
	baseline := ledger.SnapshotUsage{
		Files:            []ledger.SnapshotFile{{RelativePath: "docs/session-review/project-overview.md", Size: 1}},
		DirectoryEntries: 10_000,
	}
	plan := ledger.WritePlan{Files: []ledger.PlannedFile{{
		RelativePath: "docs/session-review/decisions/new.md",
		Data:         []byte("new"),
		Perm:         0o644,
	}}}
	if err := validateLedgerTargetUsage(baseline, plan); err == nil || !strings.Contains(err.Error(), "cannot be recovered") {
		t.Fatalf("err=%v", err)
	}
}

func TestReceiptValidateRejectsTraversalPathsDirectly(t *testing.T) {
	ctx := inputContext{Packet: evidence.Packet{
		ProjectID: testProjectID, SessionID: testSessionID, FromCursor: 1, ToCursor: 1,
		ExpectedCursor: evidence.CursorBoundary{}, NextCursor: evidence.CursorBoundary{Line: 1, SourceHash: testHashA},
	}, ProposalDigest: "sha256:" + testHashA, EvidenceFileDigest: "sha256:" + testHashA, EvidencePacketDigest: "sha256:" + testHashA}
	base, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: []ledger.PlannedFile{{RelativePath: "docs/session-review/current-state.md", Data: []byte("x"), Perm: 0o644}}}, digestLedgerSnapshot(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"..", "../escape", "./../escape", "docs/../../escape", "docs/session-review/../../../escape"} {
		t.Run(strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			receipt := base
			receipt.Files = append([]receiptFile(nil), base.Files...)
			receipt.Files[0].RelativePath = path
			if err := receipt.validate(); err == nil || !strings.Contains(err.Error(), "invalid receipt file path") {
				t.Fatalf("path=%q err=%v", path, err)
			}
		})
	}
}

func TestReceiptValidateRejectsMalformedInputDigests(t *testing.T) {
	ctx := inputContext{Packet: evidence.Packet{
		ProjectID: testProjectID, SessionID: testSessionID, FromCursor: 1, ToCursor: 1,
		ExpectedCursor: evidence.CursorBoundary{}, NextCursor: evidence.CursorBoundary{Line: 1, SourceHash: testHashA},
	}, ProposalDigest: "sha256:" + testHashA, EvidenceFileDigest: "sha256:" + testHashA, EvidencePacketDigest: "sha256:" + testHashA}
	base, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: []ledger.PlannedFile{}}, digestLedgerSnapshot(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"proposal", "evidence file", "evidence packet"} {
		t.Run(field, func(t *testing.T) {
			receipt := base
			switch field {
			case "proposal":
				receipt.ProposalSHA256 = "sha256:short"
			case "evidence file":
				receipt.EvidenceFileSHA256 = "not-a-digest"
			case "evidence packet":
				receipt.EvidencePacketSHA256 = "sha256:" + strings.ToUpper(testHashA)
			}
			if err := receipt.validate(); err == nil || !strings.Contains(err.Error(), "invalid apply receipt identity") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestReceiptValidateRejectsMalformedRecoveryMetadata(t *testing.T) {
	ctx := inputContext{Packet: evidence.Packet{
		ProjectID: testProjectID, SessionID: testSessionID, FromCursor: 2, ToCursor: 2,
		ExpectedCursor: evidence.CursorBoundary{Line: 1, SourceHash: testHashA}, NextCursor: evidence.CursorBoundary{Line: 2, SourceHash: testHashB},
	}, ProposalDigest: "sha256:" + testHashA, EvidenceFileDigest: "sha256:" + testHashA, EvidencePacketDigest: "sha256:" + testHashA}
	base, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: []ledger.PlannedFile{{
		RelativePath: "docs/session-review/current-state.md", Data: []byte("new"), Perm: 0o644,
		ExpectedExists: true, ExpectedData: []byte("old"), ExpectedPerm: 0o644,
	}}}, digestLedgerSnapshot(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*applyReceipt)
	}{
		{name: "expected cursor hash", mutate: func(receipt *applyReceipt) { receipt.ExpectedCursor.SourceHash = strings.ToUpper(testHashA) }},
		{name: "next cursor hash", mutate: func(receipt *applyReceipt) { receipt.NextCursor.SourceHash = "" }},
		{name: "preimage hash", mutate: func(receipt *applyReceipt) { receipt.Files[0].PreimageSHA256 = "sha256:short" }},
		{name: "prepared changed files", mutate: func(receipt *applyReceipt) { receipt.ChangedFiles = []string{receipt.Files[0].RelativePath} }},
		{name: "applied missing changed files", mutate: func(receipt *applyReceipt) { receipt.State = receiptApplied }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := base
			receipt.Files = append([]receiptFile(nil), base.Files...)
			receipt.ChangedFiles = append([]string(nil), base.ChangedFiles...)
			tc.mutate(&receipt)
			if err := receipt.validate(); err == nil {
				t.Fatal("accepted malformed recovery metadata")
			}
		})
	}
}

func TestApplyMutexNamePolicyIsDeterministicAndNamespaced(t *testing.T) {
	first := windowsApplyMutexName("/trusted/data", testProjectID)
	if first != windowsApplyMutexName("/trusted/data", testProjectID) {
		t.Fatal("mutex name is not deterministic")
	}
	if !strings.HasPrefix(first, `Local\SessionReviewer.Apply.`) {
		t.Fatalf("mutex namespace=%q", first)
	}
	if first == windowsApplyMutexName("/trusted/data-other", testProjectID) || first == windowsApplyMutexName("/trusted/data", "project-other") {
		t.Fatal("mutex name does not bind data identity and project ID")
	}
}

func TestRunStaleCursorLeavesDataTreeByteAndMetadataExact(t *testing.T) {
	for _, existingUnsafe := range []bool{false, true} {
		name := "projects missing"
		if existingUnsafe {
			name = "existing unsafe modes"
		}
		t.Run(name, func(t *testing.T) {
			f := newApplyTestFixture(t)
			if err := os.RemoveAll(filepath.Join(f.dataDir, "projects")); err != nil {
				t.Fatal(err)
			}
			if existingUnsafe {
				cursorDir := filepath.Join(f.projectData, "cursors")
				if err := os.MkdirAll(cursorDir, 0o755); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{filepath.Join(f.dataDir, "projects"), f.projectData, cursorDir} {
					if err := os.Chmod(path, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			makeExpectedCursorStale(t, f)
			before := snapshotTree(t, f.dataDir)
			if _, err := Run(f.options()); !errors.Is(err, cursor.ErrStale) {
				t.Fatalf("err=%v", err)
			}
			if after := snapshotTree(t, f.dataDir); after != before {
				t.Fatalf("stale apply changed data tree\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestRunAcceptsLaterCursorOnlyWhileReceiptTargetsMatch(t *testing.T) {
	f := newApplyTestFixture(t)
	if _, err := Run(f.options()); err != nil {
		t.Fatal(err)
	}
	store := cursor.Store{Root: f.projectData}
	current, err := store.Load(testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	later := cursor.Cursor{SessionID: testSessionID, LastLine: 3, LastHash: testHashA, UpdatedAt: f.now.Add(time.Second)}
	if err := store.Commit(testSessionID, current, later); err != nil {
		t.Fatal(err)
	}
	got, err := Run(f.options())
	if err != nil || !got.AlreadyApplied {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if err := os.WriteFile(filepath.Join(f.projectRoot, "docs", "session-review", "current-state.md"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "does not match applied receipt") {
		t.Fatalf("err=%v", err)
	}
}

func TestFinishReceiptClampsClockRegression(t *testing.T) {
	f := newApplyTestFixture(t)
	projectData := t.TempDir()
	store := cursor.Store{Root: projectData}
	future := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	current := cursor.Cursor{SessionID: testSessionID, LastLine: 1, LastHash: testHashA, UpdatedAt: future}
	if err := store.Commit(testSessionID, cursor.Cursor{}, current); err != nil {
		t.Fatal(err)
	}
	receipt := applyReceipt{ProjectID: testProjectID, SessionID: testSessionID, FromCursor: 2, ToCursor: 2,
		ExpectedCursor: evidence.CursorBoundary{Line: 1, SourceHash: testHashA},
		NextCursor:     evidence.CursorBoundary{Line: 2, SourceHash: testHashB}, Files: []receiptFile{}, ChangedFiles: []string{},
		LedgerSnapshotSHA256: digestLedgerSnapshot(mustLedgerSnapshot(t, f.projectRoot))}
	got, err := finishReceipt(store, current, receipt, Options{ProjectRoot: f.projectRoot, Now: func() time.Time { return future.Add(-time.Hour) }}, nil, nil, nil)
	if err != nil || !got.CursorAdvanced {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	advanced, err := store.Load(testSessionID)
	if err != nil || !advanced.UpdatedAt.Equal(future) {
		t.Fatalf("cursor=%+v err=%v", advanced, err)
	}
}

func TestProjectApplyLockIsCrossProcess(t *testing.T) {
	if os.Getenv("SESSION_REVIEWER_APPLY_LOCK_HELPER") == "1" {
		projectRoot := os.Getenv("SESSION_REVIEWER_APPLY_LOCK_PROJECT")
		dataDir := os.Getenv("SESSION_REVIEWER_APPLY_LOCK_DATA")
		ready := os.Getenv("SESSION_REVIEWER_APPLY_LOCK_READY")
		release := os.Getenv("SESSION_REVIEWER_APPLY_LOCK_RELEASE")
		lock, err := acquireProjectApplyLock(projectRoot, dataDir, testProjectID)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer lock.Release()
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			os.Exit(3)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(release); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(4)
	}
	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	ready := filepath.Join(t.TempDir(), "ready")
	release := filepath.Join(t.TempDir(), "release")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestProjectApplyLockIsCrossProcess$")
	cmd.Env = append(os.Environ(),
		"SESSION_REVIEWER_APPLY_LOCK_HELPER=1",
		"SESSION_REVIEWER_APPLY_LOCK_PROJECT="+projectRoot,
		"SESSION_REVIEWER_APPLY_LOCK_DATA="+dataDir,
		"SESSION_REVIEWER_APPLY_LOCK_READY="+ready,
		"SESSION_REVIEWER_APPLY_LOCK_RELEASE="+release,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("helper did not acquire lock: %v", err)
	}
	started := time.Now()
	if lock, err := acquireProjectApplyLock(projectRoot, dataDir, testProjectID); err == nil {
		_ = lock.Release()
		t.Fatal("second process acquired live project lock")
	} else if time.Since(started) < applyLockTimeout {
		t.Fatalf("lock failed before bounded wait: %v", err)
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectApplyLockAllowsIndependentProjects(t *testing.T) {
	dataDir := t.TempDir()
	first, err := acquireProjectApplyLock(t.TempDir(), dataDir, "project-aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	started := time.Now()
	second, err := acquireProjectApplyLock(t.TempDir(), dataDir, "project-bbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("independent project lock blocked: %v", err)
	}
	defer second.Release()
	if elapsed := time.Since(started); elapsed >= applyLockTimeout {
		t.Fatalf("independent project lock waited %v", elapsed)
	}
}

func TestRunProjectRootReplacementCannotBypassProjectDataLock(t *testing.T) {
	f := newApplyTestFixture(t)
	ready := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	first := f.options()
	first.hooks.afterPreparedReceipt = func() error {
		close(ready)
		<-release
		return errors.New("release first transaction")
	}
	go func() {
		_, err := Run(first)
		firstDone <- err
	}()
	<-ready
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	original := f.projectRoot + "-original"
	if err := os.Rename(f.projectRoot, original); err != nil {
		t.Fatal(err)
	}
	if err := copyTreeForTest(original, f.projectRoot); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "locked by a live owner") {
		t.Fatalf("replacement entrant err=%v", err)
	}
	if elapsed := time.Since(started); elapsed < applyLockTimeout {
		t.Fatalf("replacement entrant failed before lock timeout: %v", elapsed)
	}
	close(release)
	if err := <-firstDone; err == nil {
		t.Fatal("first transaction unexpectedly succeeded after root replacement")
	}
}

func receiptPathForTest(t *testing.T, f *applyTestFixture) string {
	t.Helper()
	body, err := os.ReadFile(f.proposalPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return filepath.Join(f.projectData, "applied-proposals", hex.EncodeToString(digest[:])+".json")
}

func replaceDirectoryForTest(live, moved string) error {
	if err := os.Rename(live, moved); err != nil {
		return err
	}
	return os.Mkdir(live, 0o700)
}

func copyTreeForTest(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected non-regular fixture entry %s", relative)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, body, info.Mode().Perm())
	})
}

func makeExpectedCursorStale(t *testing.T, f *applyTestFixture) {
	t.Helper()
	f.packet.FromCursor = 2
	f.packet.ToCursor = 2
	f.packet.ExpectedCursor = evidence.CursorBoundary{Line: 1, SourceHash: testHashA}
	f.packet.NextCursor = evidence.CursorBoundary{Line: 2, SourceHash: testHashB}
	f.packet.Events = f.packet.Events[1:]
	evidenceBody, err := json.Marshal(f.packet)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.evidencePath, append(evidenceBody, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := evidence.Digest(f.packet)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(f.proposalPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := proposal.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	p.FromCursor = f.packet.FromCursor
	p.ToCursor = f.packet.ToCursor
	p.EvidencePacketSHA256 = digest
	body, err = json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.proposalPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func rewriteFixtureInputs(t *testing.T, f *applyTestFixture) {
	t.Helper()
	evidenceBody, err := json.Marshal(f.packet)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.evidencePath, append(evidenceBody, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := evidence.Digest(f.packet)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(f.proposalPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := proposal.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	p.ProjectID = f.packet.ProjectID
	p.SessionID = f.packet.SessionID
	p.FromCursor = f.packet.FromCursor
	p.ToCursor = f.packet.ToCursor
	p.EvidencePacketSHA256 = digest
	body, err = json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.proposalPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

type multiPacketFixture struct {
	projectRoot string
	dataDir     string
	sessions    string
	evidence    string
	proposal    string
	now         time.Time
}

func newMultiPacketFixture(t *testing.T) *multiPacketFixture {
	t.Helper()
	root := t.TempDir()
	f := &multiPacketFixture{
		projectRoot: filepath.Join(root, "project"),
		dataDir:     filepath.Join(root, "data"),
		sessions:    filepath.Join(root, "sessions"),
		evidence:    filepath.Join(root, "work", "evidence.json"),
		proposal:    filepath.Join(root, "work", "proposal.json"),
		now:         time.Date(2026, 8, 23, 5, 0, 0, 0, time.UTC),
	}
	vaultRoot := filepath.Join(root, "vault")
	for _, directory := range []string{f.projectRoot, vaultRoot, f.sessions, filepath.Dir(f.evidence)} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := project.Initialize(project.InitOptions{
		ProjectRoot: f.projectRoot,
		VaultRoot:   vaultRoot,
		DataDir:     f.dataDir,
		Now:         func() time.Time { return f.now },
		Random:      bytes.NewReader(bytes.Repeat([]byte{0x11}, 16)),
	}); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(f.sessions, "rollout.jsonl")
	records := []any{
		map[string]any{"timestamp": "2026-08-23T01:00:00Z", "type": "session_meta", "payload": map[string]any{"id": testSessionID, "cwd": f.projectRoot, "source": "test"}},
		multiPacketMessage("first-message", "user", "Choose durable ledger", "2026-08-23T01:02:03Z"),
		multiPacketToolResult("first-verify", "go test passed", "2026-08-23T01:03:03Z"),
		multiPacketMessage("second-message", "user", "Verify native build", "2026-08-23T01:04:03Z"),
		multiPacketToolResult("second-verify", "native build passed", "2026-08-23T01:05:03Z"),
	}
	var sessionBody bytes.Buffer
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		sessionBody.Write(line)
		sessionBody.WriteByte('\n')
	}
	if err := os.WriteFile(sessionPath, sessionBody.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func multiPacketMessage(id, role, body, timestamp string) map[string]any {
	return map[string]any{
		"timestamp": timestamp,
		"type":      "response_item",
		"payload": map[string]any{
			"type": "message", "id": id, "role": role,
			"content": []map[string]any{{"type": "input_text", "text": body}},
		},
	}
}

func multiPacketToolResult(id, output, timestamp string) map[string]any {
	return map[string]any{
		"timestamp": timestamp,
		"type":      "response_item",
		"payload":   map[string]any{"type": "custom_tool_call_output", "id": id, "output": output},
	}
}

func (f *multiPacketFixture) prepare(t *testing.T, fromStart bool) evidence.Packet {
	t.Helper()
	limits := evidence.DefaultLimits()
	limits.MaxEvents = 2
	mode := "checkpoint"
	if fromStart {
		mode = "review"
	}
	packet, err := preparepkg.Run(preparepkg.Options{
		Mode: mode, SessionsRoot: f.sessions, SessionID: testSessionID,
		CWD: f.projectRoot, DataDir: f.dataDir, Output: f.evidence,
		GOOS: runtime.GOOS, Now: f.now, AmbiguityWindow: time.Second, Limits: limits,
		FromStart: fromStart,
	})
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

func (f *multiPacketFixture) applyOptions(t *testing.T, packet evidence.Packet, fixtureName string) Options {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "proposals", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	p, err := proposal.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	p.ProjectID, p.SessionID = packet.ProjectID, packet.SessionID
	p.FromCursor, p.ToCursor = packet.FromCursor, packet.ToCursor
	p.EvidencePacketSHA256, err = evidence.Digest(packet)
	if err != nil {
		t.Fatal(err)
	}
	if packet.SessionUsage != nil {
		p.SessionReport.Accounting = &accounting.SessionAccounting{StartedAt: packet.SessionUsage.StartedAt, EndedAt: packet.SessionUsage.EndedAt, DurationMS: packet.SessionUsage.DurationMS, Models: []accounting.ModelAccounting{}, TotalTokens: packet.SessionUsage.TotalTokens, TotalCostUSD: 0}
	}
	if len(packet.Events) != 2 {
		t.Fatalf("packet events=%d want=2", len(packet.Events))
	}
	events := map[string]evidence.Item{
		"ev-message": packet.Events[0], "ev-verify": packet.Events[1],
		"ev-progress": packet.Events[0], "ev-native": packet.Events[1],
	}
	bindProposalEvidence(t, &p, events)
	if fixtureName == "valid-first.json" {
		loopEvidence := []ledger.EvidenceRef{evidenceRef(packet.Events[0], packet.SessionID)}
		p.OpenLoops = []proposal.OpenLoopChange{{Operation: "create", Entity: &ledger.OpenLoop{
			ID: "loop-1", ProjectID: packet.ProjectID, Title: "Verify native build", Status: "open", Revision: 1,
			Tags: []string{"verification"}, SourceSessions: []string{packet.SessionID}, Evidence: loopEvidence,
			Question: "Does the native build pass?", Attempts: []string{}, Blocker: "Native evidence pending",
			NextExperiment: "Run the native build", CompletionCriterion: "The native build passes",
		}}}
		p.TimelineEvents[0].OpenLoopIDs = []string{"loop-1"}
		p.SessionReport.OpenLoopsCreated = []string{"loop-1"}
		p.EvidenceLinks = append(p.EvidenceLinks, proposal.EvidenceLink{EntityID: "loop-1", EvidenceID: packet.Events[0].ID, Relation: "supports"})
	}
	body, err = json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.proposal, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return Options{
		ProposalPath: f.proposal, EvidencePath: f.evidence,
		ProjectRoot: f.projectRoot, DataDir: f.dataDir,
		Now: func() time.Time { return f.now },
	}
}

func bindProposalEvidence(t *testing.T, p *proposal.Proposal, events map[string]evidence.Item) {
	t.Helper()
	bind := func(refs []ledger.EvidenceRef) {
		for index := range refs {
			event, ok := events[refs[index].EvidenceID]
			if !ok {
				t.Fatalf("fixture references unknown evidence %q", refs[index].EvidenceID)
			}
			refs[index] = evidenceRef(event, p.SessionID)
		}
	}
	for index := range p.NewDecisions {
		bind(p.NewDecisions[index].Evidence)
	}
	for index := range p.UpdatedDecisions {
		if p.UpdatedDecisions[index].Evidence != nil {
			bind(*p.UpdatedDecisions[index].Evidence)
		}
	}
	for index := range p.OpenLoops {
		if p.OpenLoops[index].Entity != nil {
			bind(p.OpenLoops[index].Entity.Evidence)
		}
		if p.OpenLoops[index].Patch != nil && p.OpenLoops[index].Patch.Evidence != nil {
			bind(*p.OpenLoops[index].Patch.Evidence)
		}
	}
	for index := range p.TimelineEvents {
		bind(p.TimelineEvents[index].Evidence)
	}
	if p.CurrentStatePatch.Evidence != nil {
		bind(*p.CurrentStatePatch.Evidence)
	}
	for index := range p.SessionReport.Phases {
		bind(p.SessionReport.Phases[index].Evidence)
	}
	bind(p.SessionReport.Evidence)
	for index := range p.EvidenceLinks {
		event, ok := events[p.EvidenceLinks[index].EvidenceID]
		if !ok {
			t.Fatalf("fixture link references unknown evidence %q", p.EvidenceLinks[index].EvidenceID)
		}
		p.EvidenceLinks[index].EvidenceID = event.ID
	}
}

func evidenceRef(event evidence.Item, sessionID string) ledger.EvidenceRef {
	return ledger.EvidenceRef{
		EvidenceID: event.ID, SessionID: sessionID, JSONLLine: event.JSONLLine,
		SourceHash: event.SourceHash, Summary: event.Summary,
	}
}

func (f *multiPacketFixture) initializeGitBaseline(t *testing.T) {
	t.Helper()
	commands := [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.name=SessionReviewer Test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "baseline"},
	}
	for _, args := range commands {
		command := exec.Command("git", args...)
		command.Dir = f.projectRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
}

func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filepath.Base(path) == "maintenance.lock" && filepath.Base(filepath.Dir(path)) == "objects" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		modTime := info.ModTime().UnixNano()
		size := info.Size()
		if info.IsDir() {
			// Directory timestamps and allocation sizes can change when a
			// platform or Git creates and removes an internal transient lock.
			// Entry names and regular-file metadata below are stable evidence.
			modTime = 0
			size = 0
		}
		line := filepath.ToSlash(relative) + "|" + info.Mode().String() + "|" + strconv.FormatInt(modTime, 10) + "|" + strconv.FormatInt(size, 10)
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(body)
			line += "|" + hex.EncodeToString(digest[:])
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func pinnedApplyDirectoryInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func assertCursorNotAdvanced(t *testing.T, f *applyTestFixture) {
	t.Helper()
	c, err := (cursor.Store{Root: f.projectData}).Load(testSessionID)
	if err != nil || c != (cursor.Cursor{}) {
		t.Fatalf("cursor=%+v err=%v", c, err)
	}
}

func hashLedger(t *testing.T, root string) string {
	t.Helper()
	ledgerRoot := filepath.Join(root, "docs", "session-review")
	var names []string
	if err := filepath.WalkDir(ledgerRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			name, relErr := filepath.Rel(ledgerRoot, path)
			if relErr != nil {
				return relErr
			}
			names = append(names, name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(ledgerRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		h.Write([]byte(strings.ReplaceAll(name, string(filepath.Separator), "/")))
		h.Write([]byte{0})
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil))
}
