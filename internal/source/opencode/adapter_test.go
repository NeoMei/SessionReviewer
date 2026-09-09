package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

func adapterFixture(t *testing.T) (*sql.DB, string, projectidentity.Binding, *sourcecatalog.Catalog) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "opencode.db")
	binding, err := projectidentity.Resolve(config.ProjectMapping{ID: "project-opencode", Root: root}, root, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY,directory TEXT NOT NULL,time_created INTEGER NOT NULL,parent_id TEXT,revert TEXT);
 CREATE TABLE message(id TEXT PRIMARY KEY,session_id TEXT NOT NULL,time_created INTEGER NOT NULL,data TEXT NOT NULL);
 CREATE TABLE part(id TEXT PRIMARY KEY,message_id TEXT NOT NULL,session_id TEXT NOT NULL,data TEXT NOT NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := sourcecatalog.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	return db, path, binding, cat
}
func insertSession(t *testing.T, db *sql.DB, id, dir string, messages []rawMessage) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO session(id,directory,time_created) VALUES(?,?,?)`, id, dir, 1789000000000); err != nil {
		t.Fatal(err)
	}
	for i, m := range messages {
		// IDs are native and global across all Sessions.
		mid := id + "_" + m.ID
		if _, err := db.Exec(`INSERT INTO message VALUES(?,?,?,?)`, mid, id, m.Created, string(m.Data)); err != nil {
			t.Fatal(err)
		}
		for j, p := range m.Parts {
			if _, err := db.Exec(`INSERT INTO part VALUES(?,?,?,?)`, fmt.Sprintf("prt_%s_%06d_%06d", id, i, j), mid, id, string(p.Data)); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestDiscoveryIncludesOver100AndChildSessionsOnlyForBoundProject(t *testing.T) {
	db, path, binding, cat := adapterFixture(t)
	for i := 0; i < 121; i++ {
		insertSession(t, db, fmt.Sprintf("ses_%03d", i), binding.CanonicalRoot, []rawMessage{messageFixture("msg_1", "user", "Question", ""), messageFixture("msg_2", "assistant", "Answer", "stop")})
	}
	db.Exec(`UPDATE session SET parent_id='ses_000' WHERE id='ses_120'`)
	insertSession(t, db, "ses_other", t.TempDir(), []rawMessage{messageFixture("msg_1", "user", "DO_NOT_READ_OTHER_PROJECT", "")})
	r := redact.Default()
	a, err := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: cat, Redactor: &r})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := a.Discover(context.Background())
	if err != nil || len(discovery.Candidates) != 121 || len(discovery.Issues) != 0 {
		t.Fatalf("found=%d issues=%v err=%v", len(discovery.Candidates), discovery.Issues, err)
	}
	for _, candidate := range discovery.Candidates {
		if candidate.InitialCWD != binding.CanonicalRoot || candidate.SessionID == "ses_other" {
			t.Fatal("project isolation lost")
		}
	}
	candidate := discovery.Candidates[120]
	boundary, err := a.Freeze(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	forged := boundary
	forged.SourceIdentity = "forged"
	if _, err := a.Decode(context.Background(), forged, func(memory.ObservationRevision) error { return nil }); err == nil {
		t.Fatal("forged boundary decoded")
	}
	report, err := a.Decode(context.Background(), boundary, func(memory.ObservationRevision) error { return nil })
	if err != nil || report.RecordCount == nil || *report.RecordCount != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	visible, coverage, err := a.(source.VisibleReader).ReadVisiblePrefix(context.Background(), report.ProposedSource)
	if err != nil || len(visible) != 2 || !coverage.Complete {
		t.Fatalf("visible=%v coverage=%v err=%v", visible, coverage, err)
	}
	if _, err := a.Freeze(context.Background(), candidate); err == nil {
		t.Fatal("candidate lease reused")
	}
}
func TestPublishedReadAuthenticatesHistoricalPrefixAndRejectsSourceDrift(t *testing.T) {
	db, path, binding, cat := adapterFixture(t)
	insertSession(t, db, "ses_one", binding.CanonicalRoot, []rawMessage{messageFixture("msg_1", "user", "Question", ""), messageFixture("msg_2", "assistant", "Answer", "stop")})
	r := redact.Default()
	a, _ := New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: cat, Redactor: &r})
	d, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := a.Freeze(context.Background(), d.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	report, err := a.Decode(context.Background(), b, func(memory.ObservationRevision) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	messages, _, err := ReadPublishedVisible(context.Background(), path, report.ProposedSource)
	if err != nil || len(messages) != 2 {
		t.Fatal("published read failed", err)
	}
	_, err = db.Exec(`UPDATE part SET data=? WHERE message_id='ses_one_msg_2'`, string(json.RawMessage(`{"type":"text","text":"CHANGED"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPublishedVisible(context.Background(), path, report.ProposedSource); err == nil {
		t.Fatal("changed historical prefix read as original")
	}
	if _, err := cat.ApplyBatch([]sourcecatalog.BatchMutation{{Relation: report.BoundaryRelation, ExpectedDigest: report.ExpectedCatalogDigest, Desired: report.ProposedSource}}); err != nil {
		t.Fatal(err)
	}
	a, _ = New(AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: cat, Redactor: &r})
	if _, err := a.Discover(context.Background()); err == nil {
		t.Fatal("source drift accepted during rescan")
	}
}
func TestExplicitMissingUnsupportedAndUnconfiguredSources(t *testing.T) {
	_, path, binding, cat := adapterFixture(t)
	r := redact.Default()
	for _, name := range []string{filepath.Join(t.TempDir(), "missing.db"), path} {
		if name == path {
			db, _ := sql.Open("sqlite", path)
			db.Exec(`ALTER TABLE part RENAME TO unsupported_part`)
			db.Close()
		}
		a, err := New(AdapterOptions{DatabasePath: name, Bindings: []projectidentity.Binding{binding}, Catalog: cat, Redactor: &r})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.Discover(context.Background()); err == nil {
			t.Fatal("configured invalid source did not fail")
		}
	}
	t.Setenv("SESSION_REVIEWER_OPENCODE_DB", "")
	resolved, err := DatabasePath()
	if err != nil || resolved != "" {
		t.Fatal("unconfigured path touched default", resolved, err)
	}
	t.Setenv("SESSION_REVIEWER_OPENCODE_DB", "relative.db")
	if _, err := DatabasePath(); err == nil {
		t.Fatal("relative source accepted")
	}
}
