package inspect

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/source/opencode"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

func TestOpenCodePublishedConversationRequiresExplicitDatabase(t *testing.T) {
	t.Setenv("SESSION_REVIEWER_OPENCODE_DB", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_, _, err := readPublishedVisible(context.Background(), memory.SourceRecord{Provider: "opencode"})
	if !errors.Is(err, source.ErrProviderUnavailable) {
		t.Fatalf("unconfigured OpenCode reader should be unavailable without source access: %v", err)
	}
	t.Setenv("SESSION_REVIEWER_OPENCODE_DB", "relative/opencode.db")
	_, _, err = readPublishedVisible(context.Background(), memory.SourceRecord{Provider: "opencode"})
	if err == nil || errors.Is(err, source.ErrVisibleReaderUnsupported) {
		t.Fatalf("explicit invalid database did not reach path validation: %v", err)
	}
}

func TestOpenCodePublishedConversationAuthenticatesCanonicalSourceAndRoot(t *testing.T) {
	path, projectRoot, record := openCodeConversationFixture(t)
	t.Setenv("SESSION_REVIEWER_OPENCODE_DB", path)
	messages, coverage, err := readPublishedVisible(context.Background(), record)
	if err != nil || len(messages) != 2 || messages[0].Text != "Question" || messages[1].Text != "Answer" || !coverage.Complete {
		t.Fatalf("source-full conversation missing: messages=%+v coverage=%+v err=%v", messages, coverage, err)
	}
	request := ConversationRequest{Provider: "opencode", SessionID: "ses_inspectMixed", ProjectID: "project-opencode"}
	view := memory.SessionView{SourceIdentity: record.SourceIdentity}
	turns, _, loaded, unsupported, err := loadConversationSource(context.Background(), request, view, record, conversationchain.CurrentSegmentationRuleVersion)
	if err != nil || !loaded || unsupported || len(turns) != 1 || len(turns[0].Messages) != 2 || turns[0].Messages[1].SourceRef.RecordOrdinal != 2 {
		t.Fatalf("generic conversation source integration: turns=%+v loaded=%v unsupported=%v err=%v", turns, loaded, unsupported, err)
	}
	tampered := record
	tampered.FrozenBoundary.Location = memory.SourceLocation{Kind: memory.SourceLocationCanonical, Canonical: &memory.CanonicalSourceLocation{Record: 1}}
	if _, _, err := readPublishedVisible(context.Background(), tampered); err == nil {
		t.Fatal("changed canonical boundary authenticated")
	}
	if err := os.Rename(projectRoot, projectRoot+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(projectRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readPublishedVisible(context.Background(), record); err == nil {
		t.Fatal("replacement project root authenticated")
	}
}

func openCodeConversationFixture(t *testing.T) (string, string, memory.SourceRecord) {
	t.Helper()
	projectRoot := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(projectRoot, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "opencode.db")
	binding, err := projectidentity.Resolve(config.ProjectMapping{ID: "project-opencode", Root: projectRoot}, projectRoot, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY,directory TEXT NOT NULL,time_created INTEGER NOT NULL,parent_id TEXT,revert TEXT);
 CREATE TABLE message(id TEXT PRIMARY KEY,session_id TEXT NOT NULL,time_created INTEGER NOT NULL,data TEXT NOT NULL);
 CREATE TABLE part(id TEXT PRIMARY KEY,message_id TEXT NOT NULL,session_id TEXT NOT NULL,data TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session(id,directory,time_created) VALUES('ses_inspectMixed',?,1789000000000)`, projectRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO message VALUES('msg_1','ses_inspectMixed',1789000000000,'{"role":"user","time":{"created":1789000000000}}');
 INSERT INTO message VALUES('msg_2','ses_inspectMixed',1789000001000,'{"role":"assistant","finish":"stop","time":{"created":1789000001000,"completed":1789000002000},"modelID":"model","providerID":"provider","tokens":{"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}');
 INSERT INTO part VALUES('prt_1','msg_1','ses_inspectMixed','{"type":"text","text":"Question"}');
 INSERT INTO part VALUES('prt_2','msg_2','ses_inspectMixed','{"type":"text","text":"Answer"}');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	catalog, err := sourcecatalog.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	r := redact.Default()
	adapter, err := opencode.New(opencode.AdapterOptions{DatabasePath: path, Bindings: []projectidentity.Binding{binding}, Catalog: catalog, Redactor: &r})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := adapter.Discover(context.Background())
	if err != nil || len(discovery.Candidates) != 1 {
		t.Fatalf("discover synthetic source: %+v err=%v", discovery, err)
	}
	boundary, err := adapter.Freeze(context.Background(), discovery.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	report, err := adapter.Decode(context.Background(), boundary, func(memory.ObservationRevision) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return path, projectRoot, report.ProposedSource
}
