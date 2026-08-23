package apply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/project"
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
	got, err := finishReceipt(store, current, receipt, Options{ProjectRoot: t.TempDir(), Now: func() time.Time { return future.Add(-time.Hour) }}, nil)
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
