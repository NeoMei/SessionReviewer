package reviewjob

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/prepare"
)

type freezeFixture struct {
	root, sessions, data, project, other string
	identity                             pathguard.IdentityToken
}

func newFreezeFixture(t *testing.T, associations ...config.SessionAssociation) freezeFixture {
	t.Helper()
	root := t.TempDir()
	f := freezeFixture{
		root:     root,
		sessions: filepath.Join(root, "sessions"),
		data:     filepath.Join(root, "data"),
		project:  filepath.Join(root, "project"),
		other:    filepath.Join(root, "other"),
	}
	for _, directory := range []string{f.sessions, f.data, f.project, f.other, filepath.Join(f.data, "projects", "project-1")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Save(filepath.Join(f.data, "config.toml"), config.Config{
		Version: 1,
		Projects: []config.ProjectMapping{
			{ID: "project-1", Root: f.project},
			{ID: "project-2", Root: f.other},
		},
		SessionAssociations: associations,
	}); err != nil {
		t.Fatal(err)
	}
	directory, err := pathguard.Open(f.project)
	if err != nil {
		t.Fatal(err)
	}
	f.identity, err = directory.PhysicalIdentity()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f freezeFixture) options() FreezeOptions {
	return FreezeOptions{
		SessionsRoot:    f.sessions,
		DataRoot:        f.data,
		ProjectID:       "project-1",
		ProjectIdentity: f.identity,
	}
}

func (f freezeFixture) write(t *testing.T, name, sessionID, cwd, started string, records ...string) string {
	t.Helper()
	meta := `{"timestamp":"` + started + `","type":"session_meta","payload":{"id":"` + sessionID + `","cwd":"` + filepath.ToSlash(cwd) + `","source":"vscode"}}`
	body := strings.Join(append([]string{meta}, records...), "\n") + "\n"
	path := filepath.Join(f.sessions, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func responseRecord(timestamp, id, text string) string {
	return `{"timestamp":"` + timestamp + `","type":"response_item","payload":{"type":"message","id":"` + id + `","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}}`
}

func recordHash(record string) string {
	sum := sha256.Sum256([]byte(record))
	return hex.EncodeToString(sum[:])
}

func (f freezeFixture) commit(t *testing.T, sessionID string, boundary evidence.CursorBoundary) {
	t.Helper()
	store := cursor.Store{Root: filepath.Join(f.data, "projects", "project-1")}
	if err := store.Commit(sessionID, cursor.Cursor{}, cursor.Cursor{
		SessionID: sessionID,
		LastLine:  boundary.Line,
		LastHash:  boundary.SourceHash,
		UpdatedAt: time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
}

// This catches freezing by path or file instead of resolving one logical
// session, sorting anything other than (StartedAt, SessionID), or retaining a
// session whose accepted cursor already equals its exact click-time upper.
func TestFreezePendingOrdersLogicalSessionsAndSkipsAcceptedUpper(t *testing.T) {
	f := newFreezeFixture(t,
		config.SessionAssociation{SessionID: "broken-other", ProjectID: "project-2"},
	)
	aRecord := responseRecord("2026-08-28T08:01:00Z", "a", "A")
	f.write(t, "a.jsonl", "session-a", f.project, "2026-08-28T08:00:00Z", aRecord)
	bFirst := responseRecord("2026-08-28T09:01:00Z", "b1", "B1")
	bSecond := responseRecord("2026-08-28T09:03:00Z", "b2", "B2")
	f.write(t, "b-1.jsonl", "session-b", f.project, "2026-08-28T09:00:00Z", bFirst)
	f.write(t, "b-2.jsonl", "session-b", f.project, "2026-08-28T09:02:00Z", bSecond)
	cRecord := responseRecord("2026-08-28T09:01:00Z", "c", "C")
	f.write(t, "c.jsonl", "session-c", f.project, "2026-08-28T09:00:00Z", cRecord)
	dRecord := responseRecord("2026-08-28T10:01:00Z", "d", "D")
	f.write(t, "accepted.jsonl", "session-d", f.project, "2026-08-28T10:00:00Z", dRecord)
	f.write(t, "unrelated.jsonl", "session-z", f.other, "2026-08-28T07:00:00Z", responseRecord("2026-08-28T07:01:00Z", "z", "Z"))
	if err := os.WriteFile(filepath.Join(f.sessions, "broken-other.jsonl"), []byte(`{"timestamp":"2026-08-28T07:00:00Z","type":"session_meta","payload":{"id":"broken-other"}}`+"\n{broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.commit(t, "session-d", evidence.CursorBoundary{Line: 2, SourceHash: recordHash(dRecord)})

	got, err := FreezePending(f.options())
	if err != nil {
		t.Fatal(err)
	}
	want := []FrozenSession{
		{SessionID: "session-a", StartedAt: time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC), Upper: evidence.CursorBoundary{Line: 2, SourceHash: recordHash(aRecord)}},
		{SessionID: "session-b", StartedAt: time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC), Upper: evidence.CursorBoundary{Line: 4, SourceHash: recordHash(bSecond)}},
		{SessionID: "session-c", StartedAt: time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC), Upper: evidence.CursorBoundary{Line: 2, SourceHash: recordHash(cRecord)}},
	}
	if len(got) != len(want) {
		t.Fatalf("FreezePending() = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("FreezePending()[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{f.root, "a.jsonl", "b-1.jsonl", "path"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("frozen record persisted path material %q: %s", forbidden, body)
		}
	}
}

// This catches taking a live EOF later during job execution instead of
// preserving the exact click-time line/hash pair. The next freeze must see the
// append because the accepted cursor still precedes it.
func TestFreezePendingKeepsActiveAppendForNextFreeze(t *testing.T) {
	f := newFreezeFixture(t)
	first := responseRecord("2026-08-28T08:01:00Z", "one", "first")
	path := f.write(t, "active.jsonl", "session-active", f.project, "2026-08-28T08:00:00Z", first)

	initial, err := FreezePending(f.options())
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial FreezePending() = %#v, %v", initial, err)
	}
	if initial[0].Upper != (evidence.CursorBoundary{Line: 2, SourceHash: recordHash(first)}) {
		t.Fatalf("initial upper = %#v", initial[0].Upper)
	}
	f.commit(t, "session-active", initial[0].Upper)
	appended := responseRecord("2026-08-28T08:02:00Z", "two", "second")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(appended + "\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if initial[0].Upper.Line != 2 || initial[0].Upper.SourceHash != recordHash(first) {
		t.Fatalf("stored frozen boundary changed after append: %#v", initial[0])
	}

	next, err := FreezePending(f.options())
	if err != nil || len(next) != 1 {
		t.Fatalf("next FreezePending() = %#v, %v", next, err)
	}
	if next[0].Upper != (evidence.CursorBoundary{Line: 3, SourceHash: recordHash(appended)}) {
		t.Fatalf("next upper = %#v", next[0].Upper)
	}
}

// These cases catch silently ignoring a corrupt source that configuration
// associates with the target project, and silently guessing ownership when a
// corrupt candidate cannot be classified at all.
func TestFreezePendingFailsClosedForRelevantOrUnclassifiableDiscoveryIssue(t *testing.T) {
	for _, test := range []struct {
		name         string
		associations []config.SessionAssociation
	}{
		{name: "associated target", associations: []config.SessionAssociation{{SessionID: "broken", ProjectID: "project-1"}}},
		{name: "unclassifiable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFreezeFixture(t, test.associations...)
			if err := os.WriteFile(filepath.Join(f.sessions, "broken.jsonl"), []byte(`{"timestamp":"2026-08-28T07:00:00Z","type":"session_meta","payload":{"id":"broken"}}`+"\n{broken\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := FreezePending(f.options()); err == nil {
				t.Fatal("FreezePending() ignored a discovery issue that could belong to the target project")
			}
		})
	}
}

// FrozenSession requires a real canonical start time. This catches silently
// substituting filesystem modification time (which is mutable) or allowing an
// unstable zero-time ordering.
func TestFreezePendingRejectsZeroStartedAtDeterministically(t *testing.T) {
	f := newFreezeFixture(t)
	f.write(t, "zero.jsonl", "session-zero", f.project, "", responseRecord("2026-08-28T08:01:00Z", "one", "first"))
	if _, err := FreezePending(f.options()); err == nil || !strings.Contains(err.Error(), "start time") {
		t.Fatalf("FreezePending() error = %v, want deterministic missing start-time rejection", err)
	}
}

// This catches authorizing a candidate by a normalized CWD string without
// proving that both it and the configured mapping name the same physical root.
func TestFreezePendingAuthenticatesConfiguredPhysicalProjectIdentity(t *testing.T) {
	f := newFreezeFixture(t)
	f.write(t, "target.jsonl", "session-target", f.project, "2026-08-28T08:00:00Z", responseRecord("2026-08-28T08:01:00Z", "one", "first"))
	opts := f.options()
	opts.ProjectIdentity.File = "0"
	if _, err := FreezePending(opts); err == nil {
		t.Fatal("FreezePending() accepted a stale configured project identity")
	}
}

// This catches treating a cursor beyond the frozen upper, or the same line
// with a different hash, as merely already accepted.
func TestFreezePendingRejectsCursorSourceDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		cursor evidence.CursorBoundary
	}{
		{name: "beyond upper", cursor: evidence.CursorBoundary{Line: 3, SourceHash: strings.Repeat("a", 64)}},
		{name: "same line different hash", cursor: evidence.CursorBoundary{Line: 2, SourceHash: strings.Repeat("b", 64)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFreezeFixture(t)
			record := responseRecord("2026-08-28T08:01:00Z", "one", "first")
			f.write(t, "target.jsonl", "session-target", f.project, "2026-08-28T08:00:00Z", record)
			f.commit(t, "session-target", test.cursor)
			_, err := FreezePending(f.options())
			if !errors.Is(err, prepare.ErrCursorSourceDrift) {
				t.Fatalf("FreezePending() error = %v, want ErrCursorSourceDrift", err)
			}
		})
	}
}
