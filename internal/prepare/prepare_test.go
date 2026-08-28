package prepare

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/session"
)

func TestRunPreparesCurrentCheckpointWithoutAdvancingCursor(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	data := filepath.Join(root, "data")
	projectRoot := filepath.Join(root, "project")
	for _, dir := range []string{sessions, data, projectRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessionPath := filepath.Join(sessions, "rollout.jsonl")
	content := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"vscode"}}` + "\n" +
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"goal"}]}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 22, 10, 2, 0, 0, time.UTC)
	if err := os.Chtimes(sessionPath, now, now); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: projectRoot, VaultRoot: filepath.Join(root, "vault")}}}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "evidence.json")
	packet, err := Run(Options{Mode: "checkpoint", SessionsRoot: sessions, CWD: projectRoot, DataDir: data, Output: output, Now: now, AmbiguityWindow: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if packet.ProjectID != "p1" || packet.SessionID != "s1" || len(packet.Events) != 1 {
		t.Fatalf("packet=%+v", packet)
	}
	if packet.SchemaVersion != 2 || packet.ExpectedCursor != (evidence.CursorBoundary{}) || packet.NextCursor != (evidence.CursorBoundary{Line: 2, SourceHash: packet.Events[0].SourceHash}) {
		t.Fatalf("packet boundaries=%+v -> %+v", packet.ExpectedCursor, packet.NextCursor)
	}
	if _, err := os.Stat(filepath.Join(data, "projects", "p1", "cursors", "s1.json")); !os.IsNotExist(err) {
		t.Fatalf("cursor advanced during prepare: %v", err)
	}
	b, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var decoded evidence.Packet
	if err := json.Unmarshal(b, &decoded); err != nil || decoded.SessionID != "s1" {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}

type runFixture struct {
	root        string
	sessions    string
	data        string
	projectRoot string
	output      string
	now         time.Time
}

func newRunFixture(t *testing.T, body string) runFixture {
	t.Helper()
	root := t.TempDir()
	f := runFixture{
		root:        root,
		sessions:    filepath.Join(root, "sessions"),
		data:        filepath.Join(root, "data"),
		projectRoot: filepath.Join(root, "project"),
		output:      filepath.Join(root, "out", "evidence.json"),
		now:         time.Date(2026, 8, 22, 10, 2, 0, 0, time.UTC),
	}
	for _, dir := range []string{f.sessions, f.data, f.projectRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if body == "" {
		body = sessionBody(f.projectRoot,
			`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"goal"}]}}`)
	}
	f.writeSession(t, "s1.jsonl", body, f.now)
	if err := config.Save(filepath.Join(f.data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: f.projectRoot, VaultRoot: filepath.Join(root, "vault")}}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func sessionBody(projectRoot string, records ...string) string {
	lines := []string{`{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"vscode"}}`}
	lines = append(lines, records...)
	return strings.Join(lines, "\n") + "\n"
}

func (f runFixture) writeSession(t *testing.T, name, body string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(f.sessions, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f runFixture) options(mode string) Options {
	return Options{Mode: mode, SessionsRoot: f.sessions, CWD: f.projectRoot, DataDir: f.data, Output: f.output, Now: f.now, AmbiguityWindow: time.Second}
}

func (f runFixture) commitCursor(t *testing.T, line int) cursor.Cursor {
	t.Helper()
	root := filepath.Join(f.data, "projects", "p1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	current := cursor.Cursor{SessionID: "s1", LastLine: line, LastHash: f.sourceHash(t, line), UpdatedAt: f.now}
	if err := (cursor.Store{Root: root}).Commit("s1", cursor.Cursor{}, current); err != nil {
		t.Fatal(err)
	}
	return current
}

func (f runFixture) sourceHash(t *testing.T, line int) string {
	t.Helper()
	var hash string
	_, err := session.Stream(filepath.Join(f.sessions, "s1.jsonl"), session.DecodeOptions{FromLine: line}, func(record session.Record) error {
		if record.Line == line {
			hash = record.SourceHash
			return session.ErrStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, session.ErrStop) {
		t.Fatal(err)
	}
	if hash == "" {
		t.Fatalf("line %d has no valid record", line)
	}
	return hash
}

func (f runFixture) cursorBytes(t *testing.T) ([]byte, string) {
	t.Helper()
	path := filepath.Join(f.data, "projects", "p1", "cursors", "s1.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body, path
}

func (f runFixture) requireFailurePreservesOutputAndCursor(t *testing.T, opts Options, before []byte, cursorPath string, errorParts ...string) {
	t.Helper()
	packet, err := Run(opts)
	for _, part := range errorParts {
		if err == nil || !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q does not contain %q", err, part)
		}
	}
	if !reflect.DeepEqual(packet, evidence.Packet{}) {
		t.Fatalf("failure returned packet: %+v", packet)
	}
	if _, statErr := os.Stat(f.output); !os.IsNotExist(statErr) {
		t.Fatalf("output created: %v", statErr)
	}
	after, readErr := os.ReadFile(cursorPath)
	if readErr != nil || !bytes.Equal(before, after) {
		t.Fatalf("cursor changed: before=%q after=%q err=%v", before, after, readErr)
	}
}

// This catches reading records appended after the click-time boundary or
// reporting live-EOF HasMore instead of bounded HasMore.
func TestPrepareHonorsFrozenUpperBoundaryAndIgnoresActiveAppend(t *testing.T) {
	first := `{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"frozen"}]}}`
	f := newRunFixture(t, "")
	body := sessionBody(f.projectRoot, first)
	path := f.writeSession(t, "s1.jsonl", body, f.now)
	upper := evidence.CursorBoundary{Line: 2, SourceHash: f.sourceHash(t, 2)}
	appended := `{"timestamp":"2026-08-22T10:02:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"append-canary"}]}}` + "\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(appended); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	opts := f.options("review")
	opts.SessionID = "s1"
	opts.UpperBoundary = &upper
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if packet.NextCursor != upper || packet.ToCursor != upper.Line || packet.HasMore || len(packet.Events) != 1 || strings.Contains(packet.Events[0].Summary, "append-canary") {
		t.Fatalf("bounded packet = %#v", packet)
	}

	manual := f.options("review")
	manual.SessionID = "s1"
	manual.Output = filepath.Join(f.root, "manual.json")
	manualPacket, err := Run(manual)
	if err != nil {
		t.Fatal(err)
	}
	if manualPacket.ToCursor != 3 || len(manualPacket.Events) != 2 || manualPacket.HasMore {
		t.Fatalf("manual unbounded packet changed semantics: %#v", manualPacket)
	}
}

// This catches stopping because a packet filled before the upper boundary but
// forgetting that more frozen evidence remains.
func TestPrepareHonorsFrozenUpperBoundaryHasMoreWithinFrozenRange(t *testing.T) {
	first := `{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"first"}]}}`
	second := `{"timestamp":"2026-08-22T10:02:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"second"}]}}`
	f := newRunFixture(t, "")
	f.writeSession(t, "s1.jsonl", sessionBody(f.projectRoot, first, second), f.now)
	upper := evidence.CursorBoundary{Line: 3, SourceHash: f.sourceHash(t, 3)}
	opts := f.options("review")
	opts.SessionID = "s1"
	opts.UpperBoundary = &upper
	opts.Limits = evidence.Limits{MaxEvents: 1, MaxSummaryRunes: 1200, MaxPacketRunes: 300000}
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !packet.HasMore || packet.NextCursor.Line != 2 || len(packet.Events) != 1 {
		t.Fatalf("bounded full packet = %#v", packet)
	}
}

// These cases catch trusting an invalid/missing/changed frozen record or an
// accepted cursor that has already crossed the frozen interval.
func TestPrepareHonorsFrozenUpperBoundaryRejectsInvalidAndDriftedSources(t *testing.T) {
	f := newRunFixture(t, "")
	for _, test := range []struct {
		name  string
		upper evidence.CursorBoundary
	}{
		{name: "zero", upper: evidence.CursorBoundary{}},
		{name: "invalid hash", upper: evidence.CursorBoundary{Line: 2, SourceHash: "bad"}},
		{name: "missing line", upper: evidence.CursorBoundary{Line: 3, SourceHash: strings.Repeat("a", 64)}},
		{name: "changed hash", upper: evidence.CursorBoundary{Line: 2, SourceHash: strings.Repeat("b", 64)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			local := test.upper
			opts := f.options("review")
			opts.SessionID = "s1"
			opts.Output = filepath.Join(f.root, "out", test.name+".json")
			opts.UpperBoundary = &local
			_, err := Run(opts)
			if test.name == "zero" || test.name == "invalid hash" {
				if err == nil || errors.Is(err, ErrCursorSourceDrift) {
					t.Fatalf("Run() validation error = %v", err)
				}
			} else if !errors.Is(err, ErrCursorSourceDrift) {
				t.Fatalf("Run() error = %v, want ErrCursorSourceDrift", err)
			}
			if _, statErr := os.Stat(opts.Output); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("drift wrote usable packet: %v", statErr)
			}
		})
	}
	for _, test := range []struct {
		name  string
		upper evidence.CursorBoundary
	}{
		{name: "cursor beyond", upper: evidence.CursorBoundary{Line: 1, SourceHash: strings.Repeat("a", 64)}},
		{name: "cursor equal different hash", upper: evidence.CursorBoundary{Line: 2, SourceHash: strings.Repeat("b", 64)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f2 := newRunFixture(t, "")
			f2.commitCursor(t, 2)
			local := test.upper
			opts := f2.options("review")
			opts.SessionID = "s1"
			opts.UpperBoundary = &local
			if _, err := Run(opts); !errors.Is(err, ErrCursorSourceDrift) {
				t.Fatalf("Run() error = %v, want ErrCursorSourceDrift", err)
			}
		})
	}
}

func TestRunExplicitSessionIgnoresUnrelatedCorruptCandidate(t *testing.T) {
	f := newRunFixture(t, "")
	const canary = "UNRELATED-CORRUPTION-CANARY"
	f.writeSession(t, "broken.jsonl", "{not-json-"+canary+"\n", f.now)
	opts := f.options("review")
	opts.SessionID = "s1"
	packet, err := Run(opts)
	if err != nil || packet.SessionID != "s1" {
		t.Fatalf("packet=%+v err=%v", packet, err)
	}
}

func TestRunExplicitSessionStreamsOrderedRolloutSegmentsAsOneTimeline(t *testing.T) {
	f := newRunFixture(t, "")
	second := `{"timestamp":"2026-08-22T12:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"` + filepath.ToSlash(f.projectRoot) + `","source":"vscode"}}` + "\n" +
		`{"timestamp":"2026-08-22T12:01:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"continued"}]}}` + "\n"
	f.writeSession(t, "s1-continuation.jsonl", second, f.now.Add(time.Minute))
	opts := f.options("review")
	opts.SessionID = "s1"
	opts.FromStart = true

	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(packet.Events) != 2 || packet.Events[0].Summary != "goal" || packet.Events[1].Summary != "continued" {
		t.Fatalf("events=%+v", packet.Events)
	}
	if packet.Events[0].JSONLLine != 2 || packet.Events[1].JSONLLine != 4 || packet.NextCursor.Line != 4 {
		t.Fatalf("boundaries=%+v events=%+v", packet.NextCursor, packet.Events)
	}
}

func TestRunExplicitSessionRejectsSelectedCorruptCandidateWithoutOutputOrCursorAdvance(t *testing.T) {
	f := newRunFixture(t, "")
	f.commitCursor(t, 1)
	before, cursorPath := f.cursorBytes(t)
	f.writeSession(t, "s1.jsonl", sessionBody(f.projectRoot,
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"goal"}]}}`,
		"{broken-selected-candidate"), f.now)
	opts := f.options("checkpoint")
	opts.SessionID = "s1"
	f.requireFailurePreservesOutputAndCursor(t, opts, before, cursorPath, "selected session candidate is corrupt")
}

func TestRunExplicitSessionScansPastConflictingMetadataWithoutPacketOutputOrCursorAdvance(t *testing.T) {
	f := newRunFixture(t, "")
	f.commitCursor(t, 1)
	before, cursorPath := f.cursorBytes(t)
	f.writeSession(t, "s1.jsonl", sessionBody(f.projectRoot,
		`{"timestamp":"2026-08-22T10:01:00Z","type":"session_meta","payload":{"id":"other","cwd":"`+filepath.ToSlash(f.projectRoot)+`"}}`,
		"{malformed-after-conflicting-metadata"), f.now)
	opts := f.options("checkpoint")
	opts.SessionID = "s1"
	f.requireFailurePreservesOutputAndCursor(t, opts, before, cursorPath, "selected session candidate is corrupt")
}

func TestRunExplicitSessionRejectsCorruptDuplicateWithoutOutputOrCursorAdvance(t *testing.T) {
	f := newRunFixture(t, "")
	f.commitCursor(t, 1)
	before, cursorPath := f.cursorBytes(t)
	f.writeSession(t, "duplicate.jsonl", sessionBody(f.projectRoot, "{broken-duplicate"), f.now)
	opts := f.options("checkpoint")
	opts.SessionID = "s1"
	f.requireFailurePreservesOutputAndCursor(t, opts, before, cursorPath, "duplicate session id")
}

func TestRunInferredSessionRejectsAnyCorruptCandidateWithoutOutputOrCursorAdvance(t *testing.T) {
	f := newRunFixture(t, "")
	f.commitCursor(t, 1)
	before, cursorPath := f.cursorBytes(t)
	const canary = "INFERRED-CORRUPTION-CANARY"
	f.writeSession(t, "broken.jsonl", "{not-json-"+canary+"\n", f.now)
	f.requireFailurePreservesOutputAndCursor(t, f.options("checkpoint"), before, cursorPath,
		"current-session discovery contains corrupt candidates", "select a session explicitly")
}

func replaceDataPath(t *testing.T, dataPath, decoyPath string) string {
	t.Helper()
	moved := dataPath + "-moved"
	if err := os.Rename(dataPath, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoyPath, dataPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return moved
}

func TestRunPinsDataRootBeforeConfigurationLoad(t *testing.T) {
	f := newRunFixture(t, "")
	decoy := filepath.Join(f.root, "decoy-data")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(decoy, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "decoy", Root: f.projectRoot}}}); err != nil {
		t.Fatal(err)
	}
	var moved string
	opts := f.options("review")
	opts.afterOpenDataDir = func() error { moved = replaceDataPath(t, f.data, decoy); return nil }
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if packet.ProjectID != "p1" {
		t.Fatalf("mixed replacement config: %+v", packet)
	}
	if _, err := os.Stat(filepath.Join(moved, "config.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestRunUsesSamePinnedDataSnapshotForConfigAndCursor(t *testing.T) {
	f := newRunFixture(t, sessionBody("PROJECT",
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"old"}]}}`,
		`{"timestamp":"2026-08-22T10:02:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"new"}]}}`))
	path := filepath.Join(f.sessions, "s1.jsonl")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.ReplaceAll(body, []byte("PROJECT"), []byte(filepath.ToSlash(f.projectRoot)))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, f.now, f.now); err != nil {
		t.Fatal(err)
	}
	f.commitCursor(t, 1)
	decoy := filepath.Join(f.root, "decoy-data")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(decoy, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: f.projectRoot}}}); err != nil {
		t.Fatal(err)
	}
	decoyProject := filepath.Join(decoy, "projects", "p1")
	if err := os.MkdirAll(decoyProject, 0o700); err != nil {
		t.Fatal(err)
	}
	decoyCursor := cursor.Cursor{SessionID: "s1", LastLine: 2, LastHash: f.sourceHash(t, 2), UpdatedAt: f.now}
	if err := (cursor.Store{Root: decoyProject}).Commit("s1", cursor.Cursor{}, decoyCursor); err != nil {
		t.Fatal(err)
	}
	opts := f.options("checkpoint")
	opts.afterLoadConfig = func() error {
		if err := os.Rename(f.data, f.data+"-moved"); err != nil {
			return err
		}
		return os.Rename(decoy, f.data)
	}
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if packet.FromCursor != 2 || len(packet.Events) != 2 || packet.Events[0].Summary != "old" {
		t.Fatalf("mixed config/cursor snapshot: %+v", packet)
	}
}

func TestRunFromStartDoesNotTouchCursorAfterDataReplacement(t *testing.T) {
	f := newRunFixture(t, "")
	decoy := filepath.Join(f.root, "decoy-data")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(decoy, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: f.projectRoot}}}); err != nil {
		t.Fatal(err)
	}
	cursorDir := filepath.Join(decoy, "projects", "p1", "cursors")
	if err := os.MkdirAll(cursorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cursorDir, "s1.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := f.options("review")
	opts.FromStart = true
	opts.afterLoadConfig = func() error { replaceDataPath(t, f.data, decoy); return nil }
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cursorDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "s1.json" {
		t.Fatalf("from-start touched cursor state: %v", entries)
	}
}

func TestRunReleasesPinnedDataRoot(t *testing.T) {
	f := newRunFixture(t, "")
	if _, err := Run(f.options("review")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.data, f.data+"-moved"); err != nil {
		t.Fatalf("data root handle leaked after Run: %v", err)
	}
}

func TestRunRejectsCursorSourceHashMismatch(t *testing.T) {
	f := newRunFixture(t, "")
	f.commitCursor(t, 2)
	f.writeSession(t, "s1.jsonl", sessionBody(f.projectRoot,
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"changed"}]}}`), f.now)
	_, err := Run(f.options("checkpoint"))
	if !errors.Is(err, ErrCursorSourceDrift) {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(f.output); !os.IsNotExist(statErr) {
		t.Fatalf("output created: %v", statErr)
	}
}

func TestRunRejectsCursorBeyondTruncatedSession(t *testing.T) {
	f := newRunFixture(t, "")
	f.commitCursor(t, 2)
	f.writeSession(t, "s1.jsonl", sessionBody(f.projectRoot), f.now)
	_, err := Run(f.options("checkpoint"))
	if !errors.Is(err, ErrCursorSourceDrift) {
		t.Fatalf("err=%v", err)
	}
}

func TestRunRejectsMalformedCandidateBeforeCursorValidation(t *testing.T) {
	f := newRunFixture(t, "")
	f.commitCursor(t, 2)
	before, cursorPath := f.cursorBytes(t)
	f.writeSession(t, "s1.jsonl", sessionBody(f.projectRoot, "{"), f.now)
	f.requireFailurePreservesOutputAndCursor(t, f.options("checkpoint"), before, cursorPath,
		"current-session discovery contains corrupt candidates")
}

func TestRunReviewFromStartBypassesCursorSourceValidation(t *testing.T) {
	f := newRunFixture(t, "")
	f.commitCursor(t, 1)
	f.writeSession(t, "s1.jsonl", sessionBody(f.projectRoot,
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"changed"}]}}`), f.now)
	opts := f.options("review")
	opts.FromStart = true
	packet, err := Run(opts)
	if err != nil || packet.FromCursor != 1 {
		t.Fatalf("packet=%+v err=%v", packet, err)
	}
}

func TestRunCheckpointStartsAfterExistingCursorWithoutChangingIt(t *testing.T) {
	f := newRunFixture(t, sessionBody("PROJECT",
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"old"}]}}`,
		`{"timestamp":"2026-08-22T10:02:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"new"}]}}`))
	// Replace the placeholder only after the fixture has allocated its project path.
	path := filepath.Join(f.sessions, "s1.jsonl")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.ReplaceAll(body, []byte("PROJECT"), []byte(filepath.ToSlash(f.projectRoot)))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, f.now, f.now); err != nil {
		t.Fatal(err)
	}
	wantCursor := f.commitCursor(t, 2)
	cursorPath := filepath.Join(f.data, "projects", "p1", "cursors", "s1.json")
	before, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatal(err)
	}

	packet, err := Run(f.options("checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if packet.FromCursor != 3 || packet.ToCursor != 3 || len(packet.Events) != 1 || packet.Events[0].Summary != "new" {
		t.Fatalf("packet=%+v", packet)
	}
	if packet.ExpectedCursor != (evidence.CursorBoundary{Line: wantCursor.LastLine, SourceHash: wantCursor.LastHash}) || packet.NextCursor != (evidence.CursorBoundary{Line: 3, SourceHash: packet.Events[0].SourceHash}) {
		t.Fatalf("packet boundaries=%+v -> %+v", packet.ExpectedCursor, packet.NextCursor)
	}
	after, err := os.ReadFile(cursorPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("cursor changed: before=%q after=%q err=%v", before, after, err)
	}
	gotCursor, err := (cursor.Store{Root: filepath.Join(f.data, "projects", "p1")}).LoadReadOnly("s1")
	if err != nil || gotCursor != wantCursor {
		t.Fatalf("cursor=%+v err=%v", gotCursor, err)
	}
}

func TestRunReviewFromStartIgnoresExistingCursor(t *testing.T) {
	f := newRunFixture(t, "")
	f.commitCursor(t, 2)
	opts := f.options("review")
	opts.FromStart = true
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if packet.FromCursor != 1 || packet.ToCursor != 2 || len(packet.Events) != 1 {
		t.Fatalf("packet=%+v", packet)
	}
}

func TestRunReviewFromStartDoesNotReadCorruptCursor(t *testing.T) {
	f := newRunFixture(t, "")
	cursors := filepath.Join(f.data, "projects", "p1", "cursors")
	if err := os.MkdirAll(cursors, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cursors, "s1.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := f.options("review")
	opts.FromStart = true
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if packet.FromCursor != 1 || len(packet.Events) != 1 {
		t.Fatalf("packet=%+v", packet)
	}
	if _, err := os.Stat(filepath.Join(cursors, ".s1.lock")); !os.IsNotExist(err) {
		t.Fatalf("from-start read cursor state: %v", err)
	}
}

func TestRunEmptyPacketStartsAtCursorPlusOne(t *testing.T) {
	f := newRunFixture(t, "")
	f.commitCursor(t, 2)
	packet, err := Run(f.options("checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	want := evidence.CursorBoundary{Line: 2, SourceHash: f.sourceHash(t, 2)}
	if packet.FromCursor != 3 || packet.ToCursor != 2 || packet.ExpectedCursor != want || packet.NextCursor != want || packet.HasMore || packet.Events == nil || len(packet.Events) != 0 {
		t.Fatalf("packet=%+v", packet)
	}
}

func TestRunPacketFullIsSuccessfulSegmentedOutput(t *testing.T) {
	f := newRunFixture(t, sessionBody("PROJECT",
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"first"}]}}`,
		`{"timestamp":"2026-08-22T10:02:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"second"}]}}`))
	path := filepath.Join(f.sessions, "s1.jsonl")
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.ReplaceAll(body, []byte("PROJECT"), []byte(filepath.ToSlash(f.projectRoot))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, f.now, f.now); err != nil {
		t.Fatal(err)
	}
	opts := f.options("review")
	opts.Limits = evidence.DefaultLimits()
	opts.Limits.MaxEvents = 1
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !packet.HasMore || packet.ToCursor != 2 || len(packet.Events) != 1 || packet.Events[0].Summary != "first" {
		t.Fatalf("packet=%+v", packet)
	}
	if packet.NextCursor != (evidence.CursorBoundary{Line: 2, SourceHash: packet.Events[0].SourceHash}) {
		t.Fatalf("full packet advanced past consumed boundary: %+v", packet.NextCursor)
	}
}

func TestRunPacketFullPreservesAcceptedUsageRedactionWarning(t *testing.T) {
	const modelCanary = "sk-canary-123456789012345678901234567890"
	f := newRunFixture(t, sessionBody("PROJECT",
		`{"timestamp":"2026-08-22T10:00:30Z","type":"turn_context","payload":{"cwd":"PROJECT","model":"`+modelCanary+`"}}`,
		`{"timestamp":"2026-08-22T10:00:40Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":15}}}}`,
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"accepted"}]}}`,
		`{"timestamp":"2026-08-22T10:02:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"rejected"}]}}`))
	path := filepath.Join(f.sessions, "s1.jsonl")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.ReplaceAll(body, []byte("PROJECT"), []byte(filepath.ToSlash(f.projectRoot))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, f.now, f.now); err != nil {
		t.Fatal(err)
	}
	opts := f.options("review")
	opts.Limits = evidence.DefaultLimits()
	opts.Limits.MaxEvents = 1
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), modelCanary) {
		t.Fatalf("model identifier leaked: %s", encoded)
	}
	if !containsString(packet.Warnings, "redacted:openai_key:1") {
		t.Fatalf("accepted usage redaction warning was lost on rollback: %+v", packet.Warnings)
	}
}

func TestRunRejectsMalformedCandidateWithoutLeakingItsContents(t *testing.T) {
	const canary = "MALFORMED-CANARY-DO-NOT-LEAK"
	f := newRunFixture(t, sessionBody("PROJECT",
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"d1","role":"developer","content":[{"type":"input_text","text":"DEVELOPER-CANARY"}]}}`,
		`{"broken":"`+canary,
		`{"timestamp":"2026-08-22T10:03:00Z","type":"response_item","payload":{"type":"future_unknown","content":"UNKNOWN-CANARY"}}`))
	path := filepath.Join(f.sessions, "s1.jsonl")
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.ReplaceAll(body, []byte("PROJECT"), []byte(filepath.ToSlash(f.projectRoot))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, f.now, f.now); err != nil {
		t.Fatal(err)
	}
	_, err := Run(f.options("review"))
	if err == nil || !strings.Contains(err.Error(), "current-session discovery contains corrupt candidates") {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "DEVELOPER-CANARY") || strings.Contains(err.Error(), "UNKNOWN-CANARY") {
		t.Fatalf("error leaked candidate contents: %v", err)
	}
	if _, statErr := os.Stat(f.output); !os.IsNotExist(statErr) {
		t.Fatalf("output created: %v", statErr)
	}
}

func TestRunClassifiesUnsupportedSelectedSessionRecordFormat(t *testing.T) {
	f := newRunFixture(t, sessionBody("PROJECT",
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"custom_tool_call_output","id":"tool-1","output":{"unexpected":true}}}`))
	path := filepath.Join(f.sessions, "s1.jsonl")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.ReplaceAll(body, []byte("PROJECT"), []byte(filepath.ToSlash(f.projectRoot))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, f.now, f.now); err != nil {
		t.Fatal(err)
	}
	_, err = Run(f.options("review"))
	if !errors.Is(err, ErrSessionFormatUnsupported) {
		t.Fatalf("error does not preserve unsupported-format sentinel: %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRunRejectsExplicitSessionFromDifferentProject(t *testing.T) {
	f := newRunFixture(t, "")
	other := filepath.Join(f.root, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"other-session","cwd":"` + filepath.ToSlash(other) + `"}}` + "\n"
	f.writeSession(t, "other.jsonl", body, f.now.Add(time.Minute))
	opts := f.options("review")
	opts.SessionID = "other-session"
	if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "different project") {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(f.output); !os.IsNotExist(err) {
		t.Fatalf("output created: %v", err)
	}
}

func TestRunRejectsNoSessionAndAmbiguousSession(t *testing.T) {
	f := newRunFixture(t, "")
	if err := os.Remove(filepath.Join(f.sessions, "s1.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(f.options("review")); err == nil || !strings.Contains(err.Error(), "no session") {
		t.Fatalf("no-session err=%v", err)
	}
	body := sessionBody(f.projectRoot, `{"timestamp":"2026-08-22T10:01:00Z","type":"event","payload":{}}`)
	f.writeSession(t, "one.jsonl", strings.Replace(body, `"id":"s1"`, `"id":"one"`, 1), f.now)
	f.writeSession(t, "two.jsonl", strings.Replace(body, `"id":"s1"`, `"id":"two"`, 1), f.now.Add(-time.Millisecond))
	if _, err := Run(f.options("review")); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous err=%v", err)
	} else {
		wrapped := fmt.Errorf("outer prepare context: %w", err)
		if !errors.Is(wrapped, ErrSessionAmbiguous) || !errors.Is(wrapped, session.ErrSessionAmbiguous) {
			t.Fatalf("nested ambiguity sentinels lost: %v", wrapped)
		}
	}
}

func TestRunStreamFailurePreservesExistingOutput(t *testing.T) {
	large := strings.Repeat("x", 2048)
	f := newRunFixture(t, sessionBody("PROJECT", `{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"`+large+`"}]}}`))
	path := filepath.Join(f.sessions, "s1.jsonl")
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.ReplaceAll(body, []byte("PROJECT"), []byte(filepath.ToSlash(f.projectRoot))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, f.now, f.now); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.output), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.output, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := f.options("review")
	opts.MaxRecordBytes = 512
	if _, err := Run(opts); err == nil {
		t.Fatal("expected stream error")
	}
	if body, err := os.ReadFile(f.output); err != nil || string(body) != "old" {
		t.Fatalf("output=%q err=%v", body, err)
	}
}

func TestRunWritesPrivateOutputAndBoundsFinalJSON(t *testing.T) {
	f := newRunFixture(t, "")
	packet, err := Run(f.options("review"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.output)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if info.Mode().Perm()&0o200 == 0 {
			t.Fatalf("mode=%#o, want writable", info.Mode().Perm())
		}
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%#o", info.Mode().Perm())
	}
	body, err := os.ReadFile(f.output)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := utf8.RuneCount(bytes.TrimSpace(body)), packetTextRunesForTest(t, packet); got != want {
		t.Fatalf("written runes=%d compact packet runes=%d", got, want)
	}
}

func packetTextRunesForTest(t *testing.T, packet evidence.Packet) int {
	t.Helper()
	body, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	return utf8.RuneCount(body)
}

func TestRunRejectsSymlinkOutputAndParentBeforeCursorLock(t *testing.T) {
	for _, test := range []struct {
		name       string
		linkParent bool
	}{
		{name: "target"},
		{name: "parent", linkParent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRunFixture(t, "")
			projectData := filepath.Join(f.data, "projects", "p1")
			if err := os.MkdirAll(filepath.Join(projectData, "cursors"), 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(f.root, "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			protected := filepath.Join(outside, "protected.json")
			if err := os.WriteFile(protected, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.linkParent {
				parent := filepath.Join(f.root, "linked-parent")
				if err := os.Symlink(outside, parent); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				f.output = filepath.Join(parent, "protected.json")
			} else {
				if err := os.MkdirAll(filepath.Dir(f.output), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(protected, f.output); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if _, err := Run(f.options("review")); err == nil {
				t.Fatal("expected unsafe output error")
			}
			if body, err := os.ReadFile(protected); err != nil || string(body) != "old" {
				t.Fatalf("protected output=%q err=%v", body, err)
			}
			if _, err := os.Stat(filepath.Join(projectData, "cursors", ".s1.lock")); !os.IsNotExist(err) {
				t.Fatalf("cursor lock created before output validation: %v", err)
			}
		})
	}
}

func TestRunNeverWritesInsideRawSessionsOrDataDirectory(t *testing.T) {
	for _, location := range []string{"sessions", "data"} {
		t.Run(location, func(t *testing.T) {
			f := newRunFixture(t, "")
			if location == "sessions" {
				f.output = filepath.Join(f.sessions, "evidence.json")
			} else {
				f.output = filepath.Join(f.data, "evidence.json")
			}
			if _, err := Run(f.options("review")); err == nil {
				t.Fatal("expected protected-root output error")
			}
			if _, err := os.Stat(f.output); !os.IsNotExist(err) {
				t.Fatalf("protected file created: %v", err)
			}
		})
	}
}

func TestRunReadOnlyCursorFallbackDoesNotRepairState(t *testing.T) {
	f := newRunFixture(t, "")
	cursors := filepath.Join(f.data, "projects", "p1", "cursors")
	if err := os.MkdirAll(cursors, 0o700); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(cursors, "s1.json")
	backup := primary + ".session-reviewer-backup"
	if err := os.WriteFile(primary, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := cursor.Cursor{SessionID: "s1", LastLine: 1, LastHash: f.sourceHash(t, 1), UpdatedAt: f.now}
	body, _ := json.Marshal(state)
	if err := os.WriteFile(backup, body, 0o600); err != nil {
		t.Fatal(err)
	}
	packet, err := Run(f.options("checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if packet.FromCursor != 2 || len(packet.Events) != 1 {
		t.Fatalf("packet=%+v", packet)
	}
	if got, _ := os.ReadFile(primary); string(got) != "{" {
		t.Fatalf("primary repaired: %q", got)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup changed: %v", err)
	}
}

func TestRunRejectsConfigurationWithDuplicateProjectRoot(t *testing.T) {
	f := newRunFixture(t, "")
	body := fmt.Sprintf("version = 1\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = %q\nvault_root = %q\n\n[[projects]]\nid = 'project-2222222222222222'\nroot = %q\nvault_root = %q\n",
		filepath.ToSlash(f.projectRoot), filepath.ToSlash(t.TempDir()), filepath.ToSlash(f.projectRoot), filepath.ToSlash(t.TempDir()))
	if err := os.WriteFile(filepath.Join(f.data, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(f.options("review")); err == nil || !strings.Contains(err.Error(), "configuration") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunResolvesRelativeWorkingDirectoryToConfiguredProject(t *testing.T) {
	f := newRunFixture(t, "")
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory := filepath.Dir(f.projectRoot)
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWorkingDirectory) })
	relative, err := filepath.Rel(workingDirectory, f.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	opts := f.options("review")
	opts.CWD = relative
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if packet.ProjectID != "p1" || packet.SessionID != "s1" {
		t.Fatalf("packet=%+v", packet)
	}
}

func TestRunRejectsInvalidDecodeLimitBeforeCursorLock(t *testing.T) {
	f := newRunFixture(t, "")
	cursors := filepath.Join(f.data, "projects", "p1", "cursors")
	if err := os.MkdirAll(cursors, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := f.options("review")
	opts.MaxRecordBytes = -1
	if _, err := Run(opts); err == nil {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cursors, ".s1.lock")); !os.IsNotExist(err) {
		t.Fatalf("cursor lock created for invalid option: %v", err)
	}
}

func TestRunDoesNotCreateCursorStateWhenProjectDataIsAbsent(t *testing.T) {
	f := newRunFixture(t, "")
	if _, err := Run(f.options("review")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.data, "projects")); !os.IsNotExist(err) {
		t.Fatalf("prepare created project cursor state: %v", err)
	}
}

func TestRunLeavesRawSessionBytesAndMetadataUnchanged(t *testing.T) {
	f := newRunFixture(t, "")
	path := filepath.Join(f.sessions, "s1.jsonl")
	beforeBody, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(f.options("checkpoint")); err != nil {
		t.Fatal(err)
	}
	afterBody, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBody, afterBody) || beforeInfo.Mode() != afterInfo.Mode() || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatalf("session changed: bytes=%t mode=%v/%v mtime=%v/%v", bytes.Equal(beforeBody, afterBody), beforeInfo.Mode(), afterInfo.Mode(), beforeInfo.ModTime(), afterInfo.ModTime())
	}
}

func TestRunRejectsCursorIncrementOverflow(t *testing.T) {
	f := newRunFixture(t, "")
	cursors := filepath.Join(f.data, "projects", "p1", "cursors")
	if err := os.MkdirAll(cursors, 0o700); err != nil {
		t.Fatal(err)
	}
	state := cursor.Cursor{SessionID: "s1", LastLine: int(^uint(0) >> 1), LastHash: strings.Repeat("a", 64), UpdatedAt: f.now}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cursors, "s1.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(f.options("checkpoint")); err == nil {
		t.Fatal("expected cursor overflow error")
	}
	if _, err := os.Stat(f.output); !os.IsNotExist(err) {
		t.Fatalf("output created for overflowing cursor: %v", err)
	}
}

func TestRunRejectsSessionRootWithSymlinkAncestor(t *testing.T) {
	f := newRunFixture(t, "")
	aliasRoot := filepath.Join(f.root, "alias-root")
	if err := os.Symlink(f.root, aliasRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	opts := f.options("review")
	opts.SessionsRoot = filepath.Join(aliasRoot, "sessions")
	if _, err := Run(opts); err == nil {
		t.Fatal("expected session-root ancestor symlink rejection")
	}
	if _, err := os.Stat(f.output); !os.IsNotExist(err) {
		t.Fatalf("output created: %v", err)
	}
}

func TestRunRejectsSessionReplacementBeforeOpen(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newRunFixture(t, "")
			sessionPath := filepath.Join(f.sessions, "s1.jsonl")
			originalPath := filepath.Join(f.root, "discovered.jsonl")
			replacementPath := filepath.Join(f.root, "replacement.jsonl")
			replacement := sessionBody(f.projectRoot,
				`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"replacement"}]}}`)
			if err := os.WriteFile(replacementPath, []byte(replacement), 0o600); err != nil {
				t.Fatal(err)
			}
			opts := f.options("review")
			opts.beforeOpenSession = func() error {
				if err := os.Rename(sessionPath, originalPath); err != nil {
					return err
				}
				switch kind {
				case "regular":
					return os.WriteFile(sessionPath, []byte(replacement), 0o600)
				case "symlink":
					return os.Symlink(replacementPath, sessionPath)
				case "hardlink":
					return os.Link(replacementPath, sessionPath)
				default:
					panic("unknown replacement kind")
				}
			}
			if _, err := Run(opts); err == nil {
				t.Fatal("expected changed session identity rejection")
			}
			if _, err := os.Stat(f.output); !os.IsNotExist(err) {
				t.Fatalf("output created: %v", err)
			}
		})
	}
}

func TestRunStreamsPinnedSessionAfterPathReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit replacing an open file in this test")
	}
	f := newRunFixture(t, "")
	sessionPath := filepath.Join(f.sessions, "s1.jsonl")
	originalPath := filepath.Join(f.root, "opened.jsonl")
	replacement := sessionBody(f.projectRoot,
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"replacement"}]}}`)
	opts := f.options("review")
	hookCalled := false
	opts.afterOpenSession = func() error {
		hookCalled = true
		if err := os.Rename(sessionPath, originalPath); err != nil {
			return err
		}
		return os.WriteFile(sessionPath, []byte(replacement), 0o600)
	}
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !hookCalled {
		t.Fatal("after-open hook was not called")
	}
	if len(packet.Events) != 1 || packet.Events[0].Summary != "goal" {
		t.Fatalf("streamed replacement instead of opened file: %+v", packet)
	}
}

func TestRunExplicitSessionAcceptsPhysicalDarwinCWDAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin physical identity behavior")
	}
	f := newRunFixture(t, "")
	alias, ok := caseAliasPath(t, f.projectRoot)
	if !ok {
		t.Skip("test filesystem is case-sensitive")
	}
	opts := f.options("review")
	opts.GOOS = "darwin"
	opts.CWD = alias
	opts.SessionID = "s1"
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if packet.ProjectID != "p1" || packet.SessionID != "s1" {
		t.Fatalf("packet=%+v", packet)
	}
}

func TestRunMatchesConfiguredProjectByPhysicalDarwinIdentity(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin physical identity behavior")
	}
	f := newRunFixture(t, "")
	alias, ok := caseAliasPath(t, f.projectRoot)
	if !ok {
		t.Skip("test filesystem is case-sensitive")
	}
	if err := config.Save(filepath.Join(f.data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "p1", Root: alias}}}); err != nil {
		t.Fatal(err)
	}
	opts := f.options("review")
	opts.GOOS = "darwin"
	packet, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if packet.ProjectID != "p1" {
		t.Fatalf("packet=%+v", packet)
	}
}

func TestFindConfiguredProjectSkipsMissingMappingsInAnyOrder(t *testing.T) {
	current := t.TempDir()
	stale := filepath.Join(t.TempDir(), "deleted")
	valid := config.ProjectMapping{ID: "valid", Root: current}
	missing := config.ProjectMapping{ID: "stale", Root: stale}
	for _, test := range []struct {
		name     string
		projects []config.ProjectMapping
	}{
		{name: "stale-first", projects: []config.ProjectMapping{missing, valid}},
		{name: "stale-last", projects: []config.ProjectMapping{valid, missing}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mapping, err := findConfiguredProject(config.Config{Version: 1, Projects: test.projects}, runtime.GOOS, current)
			if err != nil || mapping.ID != "valid" {
				t.Fatalf("mapping=%+v err=%v", mapping, err)
			}
		})
	}
}

func TestRunReportsNotInitializedWhenAllMappingsAreMissing(t *testing.T) {
	f := newRunFixture(t, "")
	stale := filepath.Join(f.root, "deleted-project")
	if err := config.Save(filepath.Join(f.data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "stale", Root: stale}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(f.options("review")); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("err=%v", err)
	}
}

func TestFindConfiguredProjectKeepsPhysicalAliasAmbiguityWithStaleMapping(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin physical identity behavior")
	}
	current := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(current, 0o700); err != nil {
		t.Fatal(err)
	}
	alias, ok := caseAliasPath(t, current)
	if !ok {
		t.Skip("test filesystem is case-sensitive")
	}
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{
		{ID: "stale", Root: filepath.Join(t.TempDir(), "deleted")},
		{ID: "one", Root: current},
		{ID: "two", Root: alias},
	}}
	if _, err := findConfiguredProject(cfg, "darwin", current); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err=%v", err)
	}
}

func TestFindConfiguredProjectFailsClosedForUnsafeMapping(t *testing.T) {
	current := t.TempDir()
	link := filepath.Join(t.TempDir(), "mapped-link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{
		{ID: "unsafe", Root: link},
		{ID: "valid", Root: current},
	}}
	if _, err := findConfiguredProject(cfg, runtime.GOOS, current); err == nil || !strings.Contains(err.Error(), "symlink or reparse point") {
		t.Fatalf("err=%v", err)
	}
}

func TestFindConfiguredProjectDoesNotSkipMalformedOrMissingCurrentRoot(t *testing.T) {
	current := t.TempDir()
	for _, test := range []struct {
		name string
		cfg  config.Config
		cwd  string
	}{
		{name: "malformed-mapping", cfg: config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "bad", Root: "bad\x00path"}, {ID: "valid", Root: current}}}, cwd: current},
		{name: "missing-current", cfg: config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "stale", Root: filepath.Join(t.TempDir(), "deleted")}}}, cwd: filepath.Join(t.TempDir(), "missing-current")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := findConfiguredProject(test.cfg, runtime.GOOS, test.cwd); err == nil {
				t.Fatal("expected mapping validation error")
			}
		})
	}
}

func TestFindConfiguredProjectPreservesWindowsNormalization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lexical Windows simulation is exercised only on non-Windows hosts")
	}
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{
		{ID: "stale", Root: `D:\Deleted`},
		{ID: "valid", Root: `c:/projects/sessionreviewer`},
	}}
	mapping, err := findConfiguredProject(cfg, "windows", `C:\Projects\SessionReviewer`)
	if err != nil || mapping.ID != "valid" {
		t.Fatalf("mapping=%+v err=%v", mapping, err)
	}
}

func TestFindConfiguredProjectPhysicallyValidatesUnrelatedEntries(t *testing.T) {
	current := t.TempDir()
	nonDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(nonDirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "unsafe", Root: nonDirectory}, {ID: "valid", Root: current}}}
	if _, err := findConfiguredProject(cfg, runtime.GOOS, current); err == nil {
		t.Fatal("unsafe unrelated mapping was ignored")
	}
}
