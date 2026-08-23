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

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
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
			receipt, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: []ledger.PlannedFile{}})
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

func TestRunIgnoresValidReceiptForDifferentSessionOrBoundary(t *testing.T) {
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
			receipt, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: []ledger.PlannedFile{}})
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
			if got, err := Run(f.options()); err != nil || !got.CursorAdvanced {
				t.Fatalf("got=%+v err=%v", got, err)
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
		if _, err := Run(f.options()); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("err=%v", err)
		}
		if _, err := os.Stat(f.projectData); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("project data written: %v", err)
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
		if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "changed while reading") {
			t.Fatalf("err=%v", err)
		}
		if _, err := os.Stat(f.projectData); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("project data written: %v", err)
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
		opts := f.options()
		opts.hooks.afterRender = func() error {
			if err := os.Rename(f.dataDir, original); err != nil {
				return err
			}
			return os.Mkdir(f.dataDir, 0o700)
		}
		if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("err=%v", err)
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
		if _, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: files}); err == nil || !strings.Contains(err.Error(), "encoded size") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("file count", func(t *testing.T) {
		files := make([]ledger.PlannedFile, 4097)
		for i := range files {
			files[i] = ledger.PlannedFile{RelativePath: fmt.Sprintf("docs/session-review/decisions/decision-%04d.md", i), Data: []byte("x"), Perm: 0o644}
		}
		if _, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: files}); err == nil || !strings.Contains(err.Error(), "file count") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestReceiptValidateRejectsTraversalPathsDirectly(t *testing.T) {
	ctx := inputContext{Packet: evidence.Packet{
		ProjectID: testProjectID, SessionID: testSessionID, FromCursor: 1, ToCursor: 1,
		ExpectedCursor: evidence.CursorBoundary{}, NextCursor: evidence.CursorBoundary{Line: 1, SourceHash: testHashA},
	}, ProposalDigest: "sha256:" + testHashA, EvidenceFileDigest: "sha256:" + testHashA, EvidencePacketDigest: "sha256:" + testHashA}
	base, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: []ledger.PlannedFile{{RelativePath: "docs/session-review/current-state.md", Data: []byte("x"), Perm: 0o644}}})
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
	base, err := newPreparedReceipt(ctx, ledger.WritePlan{Files: []ledger.PlannedFile{}})
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
	}}})
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
	projectData := t.TempDir()
	store := cursor.Store{Root: projectData}
	future := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	current := cursor.Cursor{SessionID: testSessionID, LastLine: 1, LastHash: testHashA, UpdatedAt: future}
	if err := store.Commit(testSessionID, cursor.Cursor{}, current); err != nil {
		t.Fatal(err)
	}
	receipt := applyReceipt{ProjectID: testProjectID, SessionID: testSessionID, FromCursor: 2, ToCursor: 2,
		ExpectedCursor: evidence.CursorBoundary{Line: 1, SourceHash: testHashA},
		NextCursor:     evidence.CursorBoundary{Line: 2, SourceHash: testHashB}, Files: []receiptFile{}, ChangedFiles: []string{}}
	got, err := finishReceipt(store, current, receipt, Options{ProjectRoot: t.TempDir(), Now: func() time.Time { return future.Add(-time.Hour) }}, nil, nil, nil)
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
		dataDir := os.Getenv("SESSION_REVIEWER_APPLY_LOCK_DATA")
		ready := os.Getenv("SESSION_REVIEWER_APPLY_LOCK_READY")
		release := os.Getenv("SESSION_REVIEWER_APPLY_LOCK_RELEASE")
		lock, err := acquireProjectApplyLock(dataDir, testProjectID)
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
	ready := filepath.Join(t.TempDir(), "ready")
	release := filepath.Join(t.TempDir(), "release")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestProjectApplyLockIsCrossProcess$")
	cmd.Env = append(os.Environ(),
		"SESSION_REVIEWER_APPLY_LOCK_HELPER=1",
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
	if lock, err := acquireProjectApplyLock(dataDir, testProjectID); err == nil {
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

func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		line := filepath.ToSlash(relative) + "|" + info.Mode().String() + "|" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + "|" + strconv.FormatInt(info.Size(), 10)
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
