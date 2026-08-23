# SessionReviewer Cross-Session History and Watcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build conservative cross-worktree project identity, accepted-entity history and resume recovery, a rebuildable pure-Go SQLite index, and an idempotent user-level macOS/Windows watcher that synchronizes and reminds without ever invoking a model or mutating Git.

**Architecture:** Extend the accepted ledger/sync layers through narrow read-only interfaces: project resolution and the index are deterministic caches over configuration, Git metadata, session metadata, and Markdown; history follows accepted `supersedes` links and accepted tags rather than synthesizing conclusions. A platform-neutral watcher consumes filesystem events through `fsnotify`, feeds the existing sync engine's hash/debounce/queue boundary, periodically reconciles missed events, indexes accepted state, detects cursor lag, and calls user-level notification/startup adapters; its dependency graph contains no proposal/apply/model or Git command package.

**Tech Stack:** Go 1.26; Go standard library; `modernc.org/sqlite v1.57.0` (pure Go, no C toolchain or CGO); `github.com/fsnotify/fsnotify v1.10.1` (kqueue on macOS and `ReadDirectoryChangesW` on Windows); existing TOML, ledger, recovery, cursor, atomic-file, and sync packages; `launchd`; Windows Task Scheduler and Windows Runtime toast notifications.

## Global Constraints

- Target macOS 13 or later on Apple Silicon and Intel.
- Target Windows 10 22H2 or later and Windows 11 on x86-64.
- Windows ARM binaries are not a first-release acceptance requirement.
- Installation, watcher registration, status, notification, and removal require no administrator privileges.
- Project association precedence is stored `project_id`, normalized Git remote identity, Git common-directory identity, repository root, configured path alias, then explicit confirmation.
- A Git worktree and its main checkout share one project history; unrelated projects with ambiguous identities are never merged automatically.
- Durable knowledge remains in project Markdown; `index.sqlite` is an optimization that must be quarantinable and fully rebuildable.
- The standalone `resume --ledger-only` and `history --ledger-only` commands render accepted entities only and never interpret pending evidence.
- The watcher may synchronize non-conflicting edits, drain the existing durable sync queue, refresh indexes, detect pending sessions, set `review_pending`, and remind the user.
- The watcher must never call a model, apply semantic proposals, close open loops, or run any Git command.
- The watcher never commits, stages, resets, checks out, pushes, pulls, rebases, or otherwise mutates Git.
- Raw session files stay local and are opened read-only; secrets must not enter SQLite, watcher state, queues, notifications, or logs.
- SQLite may store lowercase SHA-256 identities and accepted structural metadata, but never frontmatter/body narrative bytes, titles, evidence summaries, raw event text, or source-file content. Privacy tests distinguish an allowed hash from the forbidden bytes it hashes.
- Manual `sync`, `resume`, and `history` workflows remain complete when the watcher is disabled.

## Plan Set and Dependency Boundary

This plan consumes the preceding plans' exact contracts:

- `ledger.Load(projectRoot string) (ledger.State, error)` and the accepted `ledger.Decision`, `ledger.OpenLoop`, `ledger.SessionReport`, `ledger.CurrentState`, `ledger.TimelineEvent`, and `ledger.EvidenceRef` types;
- `recovery.ResumeLedgerOnly(projectRoot string) (recovery.ResumeCard, error)` and `recovery.HistoryLedgerOnly(projectRoot string) (recovery.HistoryView, error)`;
- `sync.NewEngine(sync.Options) (*sync.Engine, error)`, `Observe(sync.FileEvent)`, `Ready(time.Time) []string`, `Reconcile(context.Context, sync.ReconcileRequest)`, `DrainQueue(context.Context, int)`, and `Status(context.Context)`;
- the sync engine's durable `BaseStore`, content hashes, debounce decisions, retry queue, and `TriggerWatcher`/`TriggerPeriodic`/`TriggerQueue` constants.

It does not duplicate semantic proposal application, three-way merge, conflict notes, or queue persistence. If those upstream signatures differ at execution time, update this plan and the upstream plan in the same documentation commit before touching implementation.

## File Map

```text
go.mod                                      Pure-Go SQLite and fsnotify dependencies
go.sum                                      Dependency checksums
internal/config/config.go                   Stored Git identities, aliases, and explicit associations
internal/config/config_test.go              Backward-compatible TOML and ambiguity tests
internal/project/git_identity.go            Read-only Git metadata inspection and remote normalization
internal/project/git_identity_test.go       HTTPS/SSH/file remote and worktree cases
internal/project/resolve.go                 Precedence-based conservative project resolver
internal/project/resolve_test.go            Collision, alias, stored-ID, and confirmation tests
internal/session/context.go                 CWD segment extraction from visible session context records
internal/session/context_test.go            Project segmentation and read-only cross-project evidence cases
internal/index/schema.go                    SQLite schema and version
internal/index/lock.go                      Rooted per-index cross-process advisory lock
internal/index/lock_unix.go                 POSIX lock adapter
internal/index/lock_windows.go              Windows LockFileEx adapter
internal/index/store.go                     Rooted open, quick-check, quarantine, rebuild/swap, and queries
internal/index/store_test.go                Lock, privacy, identity-safe rebuild, corruption, and no-CGO tests
internal/cli/index.go                       Explicit cache status/rebuild commands
internal/history/build.go                   Supersession chains, accepted tag themes, and current entities
internal/history/build_test.go              Multi-session evolution, cycles, repeated entities, unresolved themes
internal/pending/scan.go                    Cursor-versus-session pending/idle detection
internal/pending/scan_test.go                New lines, active writes, missing sources, truncation, ambiguity
internal/repository/inspect.go               Read-only Git status used only by foreground resume
internal/repository/inspect_test.go          Branch/change drift and command allowlist tests
internal/recovery/resume.go                  Versioned recovery card with pending sessions and drift
internal/recovery/resume_test.go             Stop point, drift, and first-action rendering tests
internal/recovery/history.go                 Indexed ledger-only history entry point
internal/recovery/history_test.go            Accepted-only history rendering and index fallback tests
internal/watcher/types.go                    Clock, event source, notifier, pending scanner, and sync interfaces
internal/watcher/service.go                  Event loop, periodic reconciliation, queue drain, reminders
internal/watcher/service_test.go             Hash/debounce/restart/missed-event/cooldown tests
internal/watcher/state.go                    Atomic watcher state and reminder cooldowns
internal/watcher/state_test.go               Crash recovery, redaction, and idempotence tests
internal/watcher/fsnotify.go                 Recursive fsnotify adapter and rescan registration
internal/watcher/fsnotify_test.go             Native event adapter integration tests
internal/platform/notify/notify.go           Notification interface and safe message contract
internal/platform/notify/notify_darwin.go    `osascript` user notification adapter
internal/platform/notify/notify_windows.go   Windows Runtime toast adapter
internal/platform/notify/notify_test.go      Escaping, fallback, and canary tests
internal/platform/startup/startup.go         Install/status/uninstall interface and manifest
internal/platform/startup/launchd_darwin.go  User LaunchAgent adapter
internal/platform/startup/task_windows.go    User-logon Task Scheduler adapter
internal/platform/startup/startup_test.go    Exact commands, no-admin scope, idempotence, quoting
internal/cli/project.go                      Alias and explicit association commands
internal/cli/history.go                      Accepted ledger-only history command
internal/cli/resume.go                       Foreground repository-aware resume command
internal/cli/watch.go                        Run/install/status/uninstall watcher commands
internal/cli/run.go                          Route new commands and complete help
internal/cli/run_test.go                     CLI exit/output contract tests
cmd/session-reviewer/main.go                 Watcher subprocess entry remains the same binary
.github/workflows/ci.yml                     Native watcher and pure-Go build checks
README.md                                    History, recovery, watcher lifecycle, privacy, and recovery guide
```

---

### Task 1: Resolve One Project Across Remotes, Worktrees, Roots, and Aliases

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Create: `internal/project/git_identity.go`
- Create: `internal/project/git_identity_test.go`
- Create: `internal/project/resolve.go`
- Create: `internal/project/resolve_test.go`
- Create: `internal/session/context.go`
- Create: `internal/session/context_test.go`
- Create: `internal/cli/project.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: existing `config.Config`, `config.ProjectMapping{ID, Root, VaultRoot string}`, `session.StreamFile`, and initialized `project-overview.md` IDs.
- Produces: `project.GitInspector`, `project.GitIdentity`, `project.NormalizeRemote(string) (string, error)`, `project.Resolve(config.Config, project.ResolveInput) (project.Resolution, error)`, `session.SegmentContexts(*os.File) ([]session.ProjectSegment, error)`, `project alias add`, and `project associate-session`.

- [ ] **Step 1: Write failing configuration and resolution tests**

```go
// internal/project/resolve_test.go
package project

func TestResolveUsesApprovedPrecedenceAndFailsClosedOnRemoteCollision(t *testing.T) {
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{
		{ID: "project-1111111111111111", Root: "/work/main", RemoteIdentities: []string{"github.com/acme/app"}, CommonDirs: []string{"/work/main/.git"}},
		{ID: "project-2222222222222222", Root: "/other/fork", RemoteIdentities: []string{"github.com/acme/app"}},
	}}
	got, err := Resolve(cfg, ResolveInput{StoredProjectID: "project-1111111111111111", Identity: GitIdentity{Root: "/work/wt", CommonDir: "/work/main/.git", Remotes: []string{"github.com/acme/app"}}})
	if err != nil || got.ProjectID != "project-1111111111111111" || got.MatchedBy != MatchStoredID { t.Fatalf("got %#v, %v", got, err) }
	_, err = Resolve(cfg, ResolveInput{Identity: GitIdentity{Root: "/new", Remotes: []string{"github.com/acme/app"}}})
	if !errors.Is(err, ErrAmbiguousProject) { t.Fatalf("error = %v", err) }
}

func TestNormalizeRemoteRemovesCredentialsAndUnifiesSSHHTTPS(t *testing.T) {
	for _, raw := range []string{
		"https://token@GitHub.com/Acme/App.git",
		"ssh://git@github.com/Acme/App.git",
		"git@github.com:Acme/App.git",
	} {
		got, err := NormalizeRemote(raw)
		if err != nil || got != "github.com/acme/app" { t.Fatalf("%q => %q, %v", raw, got, err) }
		if strings.Contains(got, "token") || strings.Contains(got, "git@") { t.Fatalf("credential leaked: %q", got) }
	}
}
```

```go
// internal/session/context_test.go
func TestSegmentContextsChangesOnlyOnVisibleWorkingDirectoryContext(t *testing.T) {
	file := writeSession(t, `{"type":"session_meta","payload":{"id":"s1","cwd":"/repo/main"}}
{"type":"response_item","payload":{"type":"custom_tool_call_output","output":"read /repo/other/file.go"}}
{"type":"turn_context","payload":{"cwd":"/repo/wt"}}
`)
	segments, err := SegmentContexts(file)
	if err != nil { t.Fatal(err) }
	want := []ProjectSegment{{SessionID:"s1", CWD:"/repo/main", FromLine:1, ToLine:2}, {SessionID:"s1", CWD:"/repo/wt", FromLine:3, ToLine:3}}
	if diff := cmp.Diff(want, segments); diff != "" { t.Fatal(diff) }
}
```

- [ ] **Step 2: Run the focused tests and verify the missing contracts**

Run:

```bash
go test ./internal/project ./internal/session -run 'TestResolve|TestNormalizeRemote|TestSegmentContexts' -v
```

Expected: FAIL because `RemoteIdentities`, `CommonDirs`, `Resolve`, `NormalizeRemote`, and `SegmentContexts` do not exist.

- [ ] **Step 3: Add the exact stored and runtime types**

```go
// The complete union already introduced by the sync plan. This task adds
// identity behavior/validation without dropping or redeclaring sync fields.
type SessionAssociation struct {
	SessionID string `toml:"session_id"`
	ProjectID string `toml:"project_id"`
}

type ProjectMapping struct {
	ID               string            `toml:"id"`
	Root             string            `toml:"root"`
	VaultRoot        string            `toml:"vault_root"`
	VaultReviewPath  string            `toml:"vault_review_path,omitempty"`
	VaultCaseMode    platform.CaseMode `toml:"vault_case_mode,omitempty"`
	RemoteIdentities []string          `toml:"remote_identities,omitempty"`
	CommonDirs       []string          `toml:"common_dirs,omitempty"`
	Aliases          []string          `toml:"aliases,omitempty"`
}

type Config struct {
	Version             int                  `toml:"version"`
	Projects            []ProjectMapping     `toml:"projects"`
	SessionAssociations []SessionAssociation `toml:"session_associations,omitempty"`
}
```

Do not replace the sync plan's `ProjectMapping`/`Config` declarations. Add validation for remote identities, common directories, aliases, and associations to the existing union. A round-trip fixture populates `VaultReviewPath`, `VaultCaseMode`, every identity slice, and `SessionAssociations`, performs `project alias add` and `project associate-session`, reloads TOML, and asserts every unrelated field is byte-for-byte equivalent after canonical re-encoding.

```go
// internal/project/resolve.go
package project

type MatchKind string
const (
	MatchStoredID MatchKind = "stored_project_id"
	MatchRemote MatchKind = "git_remote"
	MatchCommonDir MatchKind = "git_common_dir"
	MatchRoot MatchKind = "repository_root"
	MatchAlias MatchKind = "path_alias"
	MatchConfirmation MatchKind = "explicit_confirmation"
)

var ErrAmbiguousProject = errors.New("project identity is ambiguous")
var ErrProjectConfirmationRequired = errors.New("project identity requires explicit confirmation")

type GitIdentity struct { Root, CommonDir string; Remotes []string }
type ResolveInput struct { StoredProjectID, SessionID, ConfirmedProjectID, GOOS string; Identity GitIdentity }
type Resolution struct { ProjectID string; MatchedBy MatchKind; ConfirmationRequired bool }

func Resolve(cfg config.Config, in ResolveInput) (Resolution, error) {
	checks := []struct{ kind MatchKind; values func(config.ProjectMapping) []string; wanted []string }{
		{MatchRemote, func(p config.ProjectMapping) []string { return p.RemoteIdentities }, in.Identity.Remotes},
		{MatchCommonDir, func(p config.ProjectMapping) []string { return p.CommonDirs }, []string{in.Identity.CommonDir}},
		{MatchRoot, func(p config.ProjectMapping) []string { return []string{p.Root} }, []string{in.Identity.Root}},
		{MatchAlias, func(p config.ProjectMapping) []string { return p.Aliases }, []string{in.Identity.Root}},
	}
	if in.StoredProjectID != "" { return uniqueID(cfg, in.StoredProjectID, MatchStoredID) }
	if in.SessionID != "" {
		for _, a := range cfg.SessionAssociations { if a.SessionID == in.SessionID { return uniqueID(cfg, a.ProjectID, MatchConfirmation) } }
	}
	for _, check := range checks {
		ids := matchMappings(cfg.Projects, check.values, check.wanted, in.GOOS)
		if len(ids) == 1 { return Resolution{ProjectID: ids[0], MatchedBy: check.kind}, nil }
		if len(ids) > 1 { return Resolution{}, ErrAmbiguousProject }
	}
	if in.ConfirmedProjectID != "" { return uniqueID(cfg, in.ConfirmedProjectID, MatchConfirmation) }
	return Resolution{ConfirmationRequired:true}, ErrProjectConfirmationRequired
}

func uniqueID(cfg config.Config,id string,kind MatchKind)(Resolution,error){
	for _,p:=range cfg.Projects{if p.ID==id{return Resolution{ProjectID:id,MatchedBy:kind},nil}}
	return Resolution{},fmt.Errorf("configured project %q does not exist",id)
}
func matchMappings(projects []config.ProjectMapping,values func(config.ProjectMapping)[]string,wanted []string,goos string)[]string{
	seen:=map[string]bool{};var ids []string
	for _,p:=range projects{for _,have:=range values(p){for _,want:=range wanted{if have==""||want==""{continue};equal:=have==want;if filepath.IsAbs(have){equal=platform.NormalizePath(goos,have)==platform.NormalizePath(goos,want)};if equal&&!seen[p.ID]{seen[p.ID]=true;ids=append(ids,p.ID)}}}}
	sort.Strings(ids);return ids
}
```

`git_identity.go` must define this injectable, read-only command boundary and reject every argv except the three exact slices listed below:

```go
type CommandRunner interface { Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) }
type GitInspector struct { Runner CommandRunner }
func (g GitInspector) Inspect(ctx context.Context, cwd string) (GitIdentity, error)
```

The package-level allowlist is equality over argument count and each string, not joined text, prefix, substring, or shell parsing:

```go
var inspectGitArgv = [][]string{
	{"rev-parse", "--show-toplevel"},
	{"rev-parse", "--git-common-dir"},
	{"config", "--get-regexp", `^remote\..*\.url$`},
}
```

`Inspect` runs exactly those invocations; extra options, alternate regexes, positionals, and combined tokens fail before `Runner.Run`. It converts a relative common dir against the repository root, resolves physical directory identity on the current OS, sorts/deduplicates normalized remotes, and never prints raw remote URLs. Tests enumerate the exact three accepted vectors plus near misses such as `rev-parse --show-toplevel --exec-path`, `config --get-regexp .*`, and `status`.

- [ ] **Step 4: Implement context segmentation and explicit confirmation commands**

```go
// internal/session/context.go
type ProjectSegment struct { SessionID, ProjectID, CWD string; FromLine, ToLine int }

func SegmentContexts(file *os.File) ([]ProjectSegment, error) {
	var segments []ProjectSegment
	var sessionID, cwd string
	_, err := StreamFile(file, DecodeOptions{}, func(record Record) error {
		next := cwd
		switch record.Type {
		case "session_meta":
			var p struct{ ID, CWD string }; if err := json.Unmarshal(record.Payload, &p); err != nil { return err }; sessionID, next = p.ID, p.CWD
		case "turn_context":
			var p struct{ CWD string `json:"cwd"` }; if err := json.Unmarshal(record.Payload, &p); err != nil { return err }; if p.CWD != "" { next = p.CWD }
		}
		if len(segments)==0&&next=="" { return nil }
		if len(segments) == 0 || next != cwd {
			if len(segments) > 0 { segments[len(segments)-1].ToLine = record.Line-1 }
			segments = append(segments, ProjectSegment{SessionID:sessionID,CWD:next,FromLine:record.Line})
		}
		cwd = next; segments[len(segments)-1].ToLine = record.Line; return nil
	})
	return segments, err
}
```

The CLI writes aliases or associations only after loading the pinned config, validating the project ID, resolving the alias to an existing non-redirected directory, taking the existing config lock, deduplicating with target-OS path semantics, and atomically saving:

```bash
session-reviewer project alias add --project-id project-0123456789abcdef --path /work/renamed
session-reviewer project associate-session --project-id project-0123456789abcdef --session 01a02971-61d6-7251-bdcf-f999230f961d
```

- [ ] **Step 5: Run identity and CLI regression tests**

Run:

```bash
gofmt -w internal/config internal/project internal/session internal/cli
go test ./internal/config ./internal/project ./internal/session ./internal/cli -v
go test ./internal/project -run TestGitInspectorAllowsOnlyReadOnlyCommands -count=20
```

Expected: PASS; existing version-1 TOML loads unchanged; ambiguous remote matches return `ErrAmbiguousProject`; a tool merely reading another repository creates no segment; explicit association is idempotent.

- [ ] **Step 6: Commit the identity boundary**

```bash
git add internal/config internal/project internal/session/context.go internal/session/context_test.go internal/cli/project.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: resolve projects across sessions and worktrees"
```

---

### Task 2: Add a Rebuildable Pure-Go SQLite Index

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/index/schema.go`
- Create: `internal/index/lock.go`
- Create: `internal/index/lock_unix.go`
- Create: `internal/index/lock_windows.go`
- Create: `internal/index/store.go`
- Create: `internal/index/store_test.go`
- Create: `internal/cli/index.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: pinned per-user data root, `ledger.State`, discovered session metadata/segments, and accepted content hashes; never raw event text or narrative/content bytes.
- Produces: `index.Open(index.Options) (*index.Store, error)`, `(*Store).Rebuild(context.Context, index.Snapshot) error`, `(*Store).ReplaceProject(context.Context, index.ProjectSnapshot) error`, `(*Store).Refresh(context.Context,string) error`, `(*Store).PendingSessions(context.Context, string) ([]index.SessionRow, error)`, `(*Store).QuarantinedPath() string`, `(*Store).Close() error`, and `session-reviewer index status|rebuild`.

- [ ] **Step 1: Write failing rebuild and corruption tests**

```go
// internal/index/store_test.go
func TestIndexIsDisposableAndRebuildsFromAcceptedSnapshot(t *testing.T) {
	data := t.TempDir()
	path := filepath.Join(data, "index", "index.sqlite")
	store, err := Open(Options{DataRoot:data, Now:func() time.Time{return fixedTime}}); if err != nil { t.Fatal(err) }
	snapshot := Snapshot{Projects:[]ProjectSnapshot{{ProjectID:"project-0123456789abcdef", Entities:[]EntityRow{{ID:"decision-1",Kind:"decision",Status:"accepted",ContentHash:strings.Repeat("a",64),Tags:[]string{"watcher"}}}}}}
	if err := store.Rebuild(context.Background(), snapshot); err != nil { t.Fatal(err) }
	if err := store.Close(); err != nil { t.Fatal(err) }
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil { t.Fatal(err) }
	rebuilt, err := Open(Options{DataRoot:data, Rebuild:snapshot, Now:func() time.Time{return fixedTime}}); if err != nil { t.Fatal(err) }
	defer rebuilt.Close()
	if rebuilt.QuarantinedPath() != path+".corrupt-20260823T010203Z" { t.Fatalf("quarantine = %q", rebuilt.QuarantinedPath()) }
	rows, err := rebuilt.Entities(context.Background(), "project-0123456789abcdef"); if err != nil || len(rows) != 1 { t.Fatalf("rows=%#v err=%v",rows,err) }
}

func TestIndexNeverStoresNarrativeOrEvidenceSummary(t *testing.T) {
	// Public row types expose IDs, classifications, tags, cursors, and hashes only.
	for _, typ := range []reflect.Type{reflect.TypeOf(EntityRow{}), reflect.TypeOf(SessionRow{})} {
		for i:=0;i<typ.NumField();i++ { if strings.Contains(strings.ToLower(typ.Field(i).Name), "summary") || strings.Contains(strings.ToLower(typ.Field(i).Name), "content") { t.Fatalf("unsafe field %s",typ.Field(i).Name) } }
	}
	narrative := []byte("NARRATIVE-CANARY-do-not-store")
	evidenceSummary := []byte("EVIDENCE-SUMMARY-CANARY-do-not-store")
	allowedHash := sha256Hex(narrative)
	f := newIndexPrivacyFixture(t)
	f.rebuildAcceptedLedger(EntitySource{ID:"decision-1",Title:string(narrative),Body:string(narrative),EvidenceSummary:string(evidenceSummary),ContentHash:allowedHash})
	for _, path := range f.sqliteFamilyAndQuarantineFiles() {
		body := mustRead(t,path)
		if bytes.Contains(body,narrative) || bytes.Contains(body,evidenceSummary) { t.Fatalf("narrative bytes persisted in %s",filepath.Base(path)) }
	}
	rows, err := f.store.Entities(context.Background(), f.projectID); if err != nil { t.Fatal(err) }
	if len(rows)!=1 || rows[0].ContentHash!=allowedHash { t.Fatalf("hash metadata missing: %#v",rows) }
}

func TestIndexLockSerializesProcessesAndRebuildRejectsRootSwap(t *testing.T) {
	f:=newIndexFixture(t); owner:=startIndexLockHelper(t,f.dataRoot); defer owner.Kill()
	if _,err:=Open(Options{DataRoot:f.dataRoot,LockTimeout:100*time.Millisecond});!errors.Is(err,ErrIndexLocked){t.Fatalf("err=%v",err)}
	owner.KillAndWait()
	store,err:=Open(Options{DataRoot:f.dataRoot});if err!=nil{t.Fatal(err)};defer store.Close()
	f.swapIndexDirectoryForDecoy()
	if err:=store.Rebuild(context.Background(),f.snapshot());!errors.Is(err,ErrIndexIdentityChanged){t.Fatalf("err=%v",err)}
}
```

- [ ] **Step 2: Run the test and verify it fails before adding SQLite**

Run:

```bash
CGO_ENABLED=0 go test ./internal/index -run 'TestIndexIsDisposable|TestIndexNeverStores|TestIndexLock' -v
```

Expected: FAIL because `internal/index` and its types do not exist.

- [ ] **Step 3: Define the schema and strict cache-only row types**

```go
// internal/index/schema.go
const SchemaVersion = 1
const schema = `
CREATE TABLE meta (schema_version INTEGER NOT NULL);
CREATE TABLE projects (project_id TEXT PRIMARY KEY, root_hash TEXT NOT NULL, indexed_at TEXT NOT NULL);
CREATE TABLE entities (project_id TEXT NOT NULL, entity_id TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL, revision INTEGER NOT NULL, content_hash TEXT NOT NULL, PRIMARY KEY(project_id,entity_id));
CREATE TABLE entity_tags (project_id TEXT NOT NULL, entity_id TEXT NOT NULL, tag TEXT NOT NULL, PRIMARY KEY(project_id,entity_id,tag));
CREATE TABLE supersedes (project_id TEXT NOT NULL, entity_id TEXT NOT NULL, predecessor_id TEXT NOT NULL, PRIMARY KEY(project_id,entity_id,predecessor_id));
CREATE TABLE sessions (project_id TEXT NOT NULL, session_id TEXT NOT NULL, from_line INTEGER NOT NULL, to_line INTEGER NOT NULL, accepted_line INTEGER NOT NULL, source_hash TEXT NOT NULL, missing INTEGER NOT NULL, PRIMARY KEY(project_id,session_id,from_line));
INSERT INTO meta(schema_version) VALUES (1);`

type EntityRow struct { ID, Kind, Status, ContentHash string; Revision int; Tags, Supersedes []string }
type SessionRow struct { SessionID string; FromLine, ToLine, AcceptedLine int; SourceHash string; Missing bool }
type ProjectSnapshot struct { ProjectID, RootHash string; IndexedAt time.Time; Entities []EntityRow; Sessions []SessionRow }
type Snapshot struct { Projects []ProjectSnapshot }
type SnapshotSource interface { Project(context.Context,string)(ProjectSnapshot,error) }
type Options struct { DataRoot string; ExpectedRoot fs.FileInfo; Rebuild Snapshot; Source SnapshotSource; Now func() time.Time; LockTimeout time.Duration }
```

`Open` validates and opens `DataRoot` as `os.Root`, confirms `ExpectedRoot` when supplied, ensures/opens an `index` directory with mode `0700`, and acquires one stable regular `index/index.lock` (`0600`) with a rooted cross-process advisory lock before inspecting SQLite. POSIX uses `flock`; Windows uses `LockFileEx`; the lock file is never deleted, a live owner times out with `ErrIndexLocked`, and process death releases it. The held lock covers quick-check, quarantine, rebuild, swap, and every write transaction; read queries use the live store whose owner still holds the lock. Helper-subprocess tests prove exclusion and crash release on both OS implementations.

`store.go` imports `_ "modernc.org/sqlite"`, opens root-relative `index/index.sqlite`, sets `PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `PRAGMA foreign_keys=ON`, `PRAGMA busy_timeout=5000`, and runs `PRAGMA quick_check`. It records the opened data/index directory identities and rechecks both before every quarantine or swap. Any open/schema/check failure closes the handle, lstat/open/stat-verifies the database and its `-wal`/`-shm` companions as regular entries beneath the pinned index root, and renames each verified entry to a collision-free root-relative basename `index.sqlite.corrupt-<UTC>[.<n>]`; a redirect, identity change, or case-collision fails closed without moving anything. It then creates a clean sibling database from the supplied snapshot. An empty snapshot creates an empty cache; it never infers accepted facts.

- [ ] **Step 4: Implement transactional replacement and deterministic queries**

```go
func (s *Store) ReplaceProject(ctx context.Context, p ProjectSnapshot) error {
	tx, err := s.db.BeginTx(ctx, nil); if err != nil { return err }
	defer tx.Rollback()
	for _, q := range []string{"DELETE FROM entity_tags WHERE project_id=?", "DELETE FROM supersedes WHERE project_id=?", "DELETE FROM entities WHERE project_id=?", "DELETE FROM sessions WHERE project_id=?", "DELETE FROM projects WHERE project_id=?"} {
		if _, err := tx.ExecContext(ctx,q,p.ProjectID); err != nil { return err }
	}
	if _, err := tx.ExecContext(ctx,"INSERT INTO projects(project_id,root_hash,indexed_at) VALUES(?,?,?)",p.ProjectID,p.RootHash,p.IndexedAt.UTC().Format(time.RFC3339Nano)); err != nil { return err }
	for _, e := range p.Entities {
		if _, err := tx.ExecContext(ctx,"INSERT INTO entities VALUES(?,?,?,?,?,?)",p.ProjectID,e.ID,e.Kind,e.Status,e.Revision,e.ContentHash); err != nil { return err }
		for _, tag := range sortedUnique(e.Tags) { if _,err:=tx.ExecContext(ctx,"INSERT INTO entity_tags VALUES(?,?,?)",p.ProjectID,e.ID,tag); err!=nil{return err} }
		for _, old := range sortedUnique(e.Supersedes) { if _,err:=tx.ExecContext(ctx,"INSERT INTO supersedes VALUES(?,?,?)",p.ProjectID,e.ID,old); err!=nil{return err} }
	}
	for _, r := range p.Sessions { if _,err:=tx.ExecContext(ctx,"INSERT INTO sessions VALUES(?,?,?,?,?,?,?)",p.ProjectID,r.SessionID,r.FromLine,r.ToLine,r.AcceptedLine,r.SourceHash,boolInt(r.Missing)); err!=nil{return err} }
	return tx.Commit()
}
func (s *Store) Refresh(ctx context.Context,projectID string)error{
	if s.source==nil{return fmt.Errorf("index snapshot source is required")}
	p,err:=s.source.Project(ctx,projectID);if err!=nil{return err};return s.ReplaceProject(ctx,p)
}
```

Every query orders by stable keys. Under the same per-index lock, `Rebuild` creates a unique sibling database through the pinned `os.Root`, populates it transactionally, checkpoints/closes it, reopens it by rooted name, verifies regular-file identity and `quick_check`, writes a content-free swap marker, and installs it with the current rooted `atomicfile` publication primitive. It then reopens/quick-checks the installed identity before clearing the marker. Startup uses the marker only to select and verify complete old/new candidates; it never follows or trusts an absolute path. A crash leaves either the previous usable cache or a fully built new cache. SQLite bytes are never a source for rendering ledger documents.

`internal/cli/index.go` implements `session-reviewer index status [--data-dir DIR] [--json]` and `session-reviewer index rebuild [--data-dir DIR] [--project ROOT|--all]`. Rebuild loads accepted Markdown and session metadata through `SnapshotSource`, writes a sibling database, quick-checks it, swaps it, and prints project/entity/session counts; it never reads narrative into SQLite, advances a cursor, or requires the watcher.

- [ ] **Step 5: Prove the dependency has no C-toolchain requirement**

Run:

```bash
go get modernc.org/sqlite@v1.57.0
gofmt -w internal/index
CGO_ENABLED=0 go test ./internal/index -v
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./cmd/session-reviewer
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/session-reviewer
go list -deps ./internal/index | rg 'mattn/go-sqlite3|C$' && exit 1 || true
go run ./cmd/session-reviewer index rebuild --project .
go run ./cmd/session-reviewer index status --json
```

Expected: tests and both builds pass with `CGO_ENABLED=0`; the dependency scan prints nothing.

- [ ] **Step 6: Commit the disposable index**

```bash
git add go.mod go.sum internal/index internal/cli/index.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: add rebuildable pure Go project index"
```

---

### Task 3: Build Pending Detection and Cross-Session Evolution History

**Files:**
- Create: `internal/pending/scan.go`
- Create: `internal/pending/scan_test.go`
- Create: `internal/history/build.go`
- Create: `internal/history/build_test.go`
- Modify: `internal/recovery/history.go`
- Modify: `internal/recovery/history_test.go`
- Create: `internal/cli/history.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: `session.Discover`, `session.SegmentContexts`, one existing per-project `cursor.Store.LoadReadOnly(sessionID)`, `ledger.State`, accepted `Decision.Tags`/`OpenLoop.Tags`, and `index.Store` as an optional cache.
- Produces: `pending.Scanner.Scan(context.Context, pending.Options) (pending.Report, error)`, `history.Build(ledger.State) (recovery.HistoryView, error)`, and `recovery.History(context.Context, recovery.HistoryOptions) (recovery.HistoryView, error)`.

- [ ] **Step 1: Write failing pending and evolution tests**

```go
// internal/pending/scan_test.go
func TestScanReportsOnlyUnacceptedProjectSegments(t *testing.T) {
	fixture := newFixture(t)
	fixture.session("s1", "/repo/main", 12)
	fixture.cursor("project-0123456789abcdef", "s1", 8)
	report, err := fixture.scanner().Scan(context.Background(), Options{ProjectID:"project-0123456789abcdef", IdleAfter:15*time.Minute, Now:fixedNow})
	if err != nil { t.Fatal(err) }
	if diff:=cmp.Diff([]PendingSession{{SessionID:"s1",FromLine:9,ToLine:12,Idle:true}},report.Sessions); diff!="" { t.Fatal(diff) }
}
```

```go
// internal/history/build_test.go
func TestBuildFollowsSupersedesAndGroupsOnlyAcceptedTags(t *testing.T) {
	state := ledger.State{ProjectID:"project-0123456789abcdef", Decisions:map[string]ledger.Decision{
		"decision-old":{ID:"decision-old",Status:"superseded",Title:"Poll files",Tags:[]string{"watcher"},SourceSessions:[]string{"s1"}},
		"decision-new":{ID:"decision-new",Status:"accepted",Title:"Native events plus reconciliation",Tags:[]string{"watcher"},Supersedes:[]string{"decision-old"},SourceSessions:[]string{"s2"}},
	}, OpenLoops:map[string]ledger.OpenLoop{"loop-1":{ID:"loop-1",Status:"open",Title:"Prove Windows restart",Tags:[]string{"watcher"},SourceSessions:[]string{"s2"}}}}
	view, err := Build(state); if err != nil { t.Fatal(err) }
	if diff:=cmp.Diff([][]string{{"decision-old","decision-new"}},view.SupersessionChains); diff!="" { t.Fatal(diff) }
	if diff:=cmp.Diff([]recovery.Theme{{Name:"watcher",DecisionIDs:[]string{"decision-new","decision-old"},OpenLoopIDs:[]string{"loop-1"}}},view.Themes); diff!="" { t.Fatal(diff) }
}
```

- [ ] **Step 2: Run tests to expose missing accepted-only behavior**

Run:

```bash
go test ./internal/pending ./internal/history ./internal/recovery -run 'TestScanReports|TestBuildFollows|TestHistory' -v
```

Expected: FAIL because the scanner, history builder, and extended view do not exist.

- [ ] **Step 3: Define pending and versioned history contracts**

```go
// internal/pending/scan.go
type PendingSession struct { SessionID, ProjectID string; FromLine, ToLine int; LastWrite time.Time; Idle, SourceMissing, SourceDrift bool }
type Report struct { ProjectID string; ReviewPending bool; Sessions []PendingSession; Ambiguous []string }
type Options struct { ProjectID string; IdleAfter time.Duration; Now time.Time }
type SessionSource interface { Candidates(context.Context) ([]session.Candidate,error); Segments(context.Context,session.Candidate)([]session.ProjectSegment,error); LastRecord(context.Context,session.Candidate)(session.Record,error) }
type CursorStore interface { LoadReadOnly(sessionID string)(cursor.Cursor,error) }
type CursorStores interface { ForProject(projectID string)(CursorStore,error) }
type Resolver interface { ResolveSegment(context.Context,session.ProjectSegment)(project.Resolution,error) }
type Scanner struct { Sessions SessionSource; Cursors CursorStores; Projects Resolver }
func (s Scanner) Scan(ctx context.Context, opts Options) (Report,error)
```

At scan start, `Scanner` calls `Cursors.ForProject(opts.ProjectID)` exactly once. The concrete factory resolves and identity-pins `<data>/projects/<project-id>` and returns the existing `cursor.Store{Root: projectDataPath, ExpectedRoot: projectDataInfo}`; it never treats the machine data root as a cursor store and never passes project ID as a session ID. The scanner calls that store's `LoadReadOnly(sessionID)` for resolved segments and never writes/repairs cursor state. It compares the last visible record line/hash to the accepted cursor. A missing source becomes `SourceMissing` only for previously indexed sessions; a shorter source or cursor hash mismatch becomes `SourceDrift`; an ambiguous identity goes in `Ambiguous` and is never assigned. `Idle` is true only when `Now-LastWrite >= IdleAfter`. Tests place same-named session cursors beneath two project stores and prove a scan reads only the requested project's value.

Extend the existing accepted view without replacing its earlier fields:

```go
// additions to internal/recovery/history.go
type HistoryView struct {
	ProjectID string
	Timeline []ledger.TimelineEvent
	Decisions []ledger.Decision
	OpenLoops []ledger.OpenLoop
	Sessions []ledger.SessionReport
	SupersessionChains [][]string
	Themes []Theme
}
type Theme struct { Name string; DecisionIDs, OpenLoopIDs []string }
type HistoryOptions struct { ProjectRoot, DataDir string; Index *index.Store }
func History(ctx context.Context, opts HistoryOptions) (HistoryView,error)
```

- [ ] **Step 4: Implement deterministic supersession and theme rules**

`history.Build` must reject a missing predecessor, a cross-project predecessor, and any cycle; produce each chain oldest-to-current; sort chains by first ID; keep superseded decisions in the chain; and list the terminal decision first in the normal `Decisions` section. Themes come only from normalized accepted `Tags` (`strings.ToLower(strings.TrimSpace(tag))`), never from title keyword inference. `OpenLoopIDs` contains only `open` or `blocked` loops, so any non-empty value is an unresolved theme; source sessions remain available through the referenced entities rather than duplicating IDs in `Theme`. Repeated entity IDs are rejected by `ledger.Load`; history never concatenates session summaries.

```go
func walk(id string, byID map[string]ledger.Decision, visiting, done map[string]bool) ([]string,error) {
	if visiting[id] { return nil, fmt.Errorf("supersedes cycle at %s",id) }
	if done[id] { return nil,nil }
	visiting[id]=true
	var chain []string
	for _, predecessor := range sortedUnique(byID[id].Supersedes) {
		if _,ok:=byID[predecessor]; !ok { return nil,fmt.Errorf("decision %s supersedes missing %s",id,predecessor) }
		prefix,err:=walk(predecessor,byID,visiting,done); if err!=nil{return nil,err}; chain=append(chain,prefix...)
	}
	visiting[id]=false; done[id]=true; return append(chain,id),nil
}
```

- [ ] **Step 5: Route ledger-only history with transparent index fallback**

`recovery.History` always loads Markdown first. It may use the index only to accelerate ordering/lookup after matching the project's root/content snapshot hash. A missing, corrupt, or stale cache triggers `ReplaceProject` and continues from Markdown. `HistoryLedgerOnly(projectRoot)` remains source-compatible and delegates with an empty data directory. CLI output is stable Markdown by default and JSON only with `--json`; it prints `pending sessions: N` as metadata but never processes them.

Run:

```bash
gofmt -w internal/pending internal/history internal/recovery internal/cli
go test ./internal/pending ./internal/history ./internal/recovery ./internal/cli -v
go test ./internal/history -run TestBuildFollowsSupersedesAndGroupsOnlyAcceptedTags -count=20
```

Expected: PASS and byte-stable history across 20 runs.

- [ ] **Step 6: Commit accepted cross-session history**

```bash
git add internal/pending internal/history internal/recovery/history.go internal/recovery/history_test.go internal/cli/history.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: render accepted cross-session project history"
```

---

### Task 4: Produce Foreground Resume Cards with Repository Drift

**Files:**
- Create: `internal/repository/inspect.go`
- Create: `internal/repository/inspect_test.go`
- Modify: `internal/recovery/resume.go`
- Modify: `internal/recovery/resume_test.go`
- Create: `internal/cli/resume.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: existing `recovery.ResumeCard`, `ledger.CurrentState`, accepted sessions, and `pending.Report`.
- Produces: `repository.Inspector.Inspect(context.Context,string) (repository.Snapshot,error)`, `recovery.BuildResume(ledger.State,pending.Report,repository.Snapshot) (recovery.ResumeCardV2,error)`, and `recovery.Resume(context.Context,recovery.ResumeOptions) (recovery.ResumeCardV2,error)`; the repository inspector is foreground-only and forbidden from the watcher dependency graph.

- [ ] **Step 1: Write failing recovery/drift tests**

```go
func TestResumeCardShowsStopPointPendingWorkAndObservedDrift(t *testing.T) {
	state := ledger.State{ProjectID:"project-0123456789abcdef",CurrentState:ledger.CurrentState{Goal:"Ship watcher",LastVerified:"unit tests pass",Branch:"main",UncommittedChanges:[]string{"old.txt"},NextAction:"run native E2E",FirstInspection:"docs/session-review/current-state.md"},Sessions:map[string]ledger.SessionReport{"s1":{SessionID:"s1",Phases:[]ledger.SessionPhase{{Title:"Stopped",Summary:"Task Scheduler not tested"}}}}}
	card,err:=BuildResume(state,pending.Report{ReviewPending:true,Sessions:[]pending.PendingSession{{SessionID:"s2",FromLine:5,ToLine:9}}},repository.Snapshot{Branch:"feature/watcher",Changes:[]string{"new.txt"}})
	if err!=nil{t.Fatal(err)}
	if diff:=cmp.Diff([]string{"branch changed: main -> feature/watcher","working tree removed: old.txt","working tree added: new.txt","session s2 has unreviewed lines 5-9"},card.Drift);diff!=""{t.Fatal(diff)}
	if card.StopPoint!="Task Scheduler not tested" || card.NextAction!="run native E2E"{t.Fatalf("%#v",card)}
}
```

- [ ] **Step 2: Run the recovery test and confirm the versioned API is absent**

Run:

```bash
go test ./internal/repository ./internal/recovery -run 'TestResumeCard|TestInspector' -v
```

Expected: FAIL because `repository.Snapshot`, `ResumeCardV2`, and `BuildResume` do not exist.

- [ ] **Step 3: Implement an allowlisted read-only foreground repository inspector**

```go
// internal/repository/inspect.go
type Snapshot struct { Branch, Head string; Changes []string }
type Runner interface { Run(context.Context,string,string,...string)([]byte,error) }
type Inspector struct { Runner Runner }
func (i Inspector) Inspect(ctx context.Context, root string) (Snapshot,error) {
	branch,err:=i.run(ctx,root,"symbolic-ref","--quiet","--short","HEAD"); if err!=nil { branch=[]byte("(detached)") }
	head,err:=i.run(ctx,root,"rev-parse","HEAD"); if err!=nil{return Snapshot{},err}
	status,err:=i.run(ctx,root,"status","--porcelain=v1","-z","--untracked-files=all"); if err!=nil{return Snapshot{},err}
	return Snapshot{Branch:strings.TrimSpace(string(branch)),Head:strings.TrimSpace(string(head)),Changes:parsePorcelainZ(status)},nil
}
func (i Inspector) run(ctx context.Context,root string,args ...string)([]byte,error){
	if !exactArgv(args,
		[]string{"symbolic-ref","--quiet","--short","HEAD"},
		[]string{"rev-parse","HEAD"},
		[]string{"status","--porcelain=v1","-z","--untracked-files=all"},
	) { return nil,fmt.Errorf("Git command is not read-only allowlisted") }
	return i.Runner.Run(ctx,root,"git",args...)
}
```

`exactArgv` requires equal length and equal string at every position; it performs no join, prefix, substring, shell, or regexp comparison. In particular, `symbolic-ref --quiet --short HEAD --delete`, `symbolic-ref refs/heads/main`, `status --porcelain=v2`, and every extra argument are rejected before the runner records a call. No other Git subcommand is representable through this package. Status paths pass through redaction before they enter a card. The test enumerates the three accepted vectors and a table of near-miss/mutating vectors rather than checking command verbs alone.

- [ ] **Step 4: Add the versioned recovery card and stable renderer**

```go
type ResumeCardV2 struct {
	Version int `json:"version"`
	ProjectID,Goal,StopPoint,LastVerified string
	Drift,Blockers,OpenQuestions,SourceSessions,PendingSessions []string
	NextAction,FirstInspection string
	ReviewPending bool
}
type ResumeOptions struct { ProjectRoot,DataDir string; Inspector repository.Inspector; Pending pending.Scanner }
func Resume(ctx context.Context,opts ResumeOptions)(ResumeCardV2,error)
```

The renderer order is goal, stop point, verified state, subsequent drift, blockers, open questions, first useful action, first inspection, and pending sessions. An unavailable Git executable creates one safe drift line (`repository state unavailable`) but does not block ledger recovery. Ambiguous project association blocks aggregation and returns the explicit confirmation command. `--ledger-only` may inspect read-only repository state, but it never prepares/applies pending evidence.

- [ ] **Step 5: Run recovery and architecture tests**

Run:

```bash
gofmt -w internal/repository internal/recovery internal/cli
go test ./internal/repository ./internal/recovery ./internal/cli -v
go test ./internal/repository -run TestInspectorRejectsEveryMutatingGitVerb -count=10
```

Expected: PASS; `resume --ledger-only` names pending sessions and drift without advancing cursors or changing Git status.

- [ ] **Step 6: Commit foreground resume recovery**

```bash
git add internal/repository internal/recovery/resume.go internal/recovery/resume_test.go internal/cli/resume.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: report resume state and repository drift"
```

---

### Task 5: Implement the Deterministic Watcher Loop and Durable Reminder State

**Files:**
- Create: `internal/watcher/types.go`
- Create: `internal/watcher/service.go`
- Create: `internal/watcher/service_test.go`
- Create: `internal/watcher/state.go`
- Create: `internal/watcher/state_test.go`

**Interfaces:**
- Consumes: `sync.Engine` event/hash/debounce/reconcile/queue contracts, `pending.Scanner`, `index.Store`, and a notifier interface.
- Produces: `watcher.New(watcher.Options) (*watcher.Service,error)`, `(*Service).Run(context.Context) error`, and an atomic `watcher.StateStore`; no model, proposal, apply, recovery-repository, or Git dependency.

- [ ] **Step 1: Write failing event, reconciliation, and reminder tests**

```go
func TestRunDebouncesEventsReconcilesMissesDrainsQueueAndRemindsOnce(t *testing.T) {
	clock:=newFakeClock(fixedNow)
	events:=newFakeEvents(sync.FileEvent{Side:sync.SideProject,RelativePath:"decisions/d1.md",ObservedHash:strings.Repeat("a",64),At:fixedNow})
	svc,_:=New(Options{Events:events,Sync:&fakeSync{observe:sync.EventReady,ready:[]string{"d1"}},Pending:&fakePending{report:pending.Report{ReviewPending:true,Sessions:[]pending.PendingSession{{SessionID:"s1",Idle:true}}}},Index:&fakeIndex{},Notifier:&fakeNotifier{},State:NewStateStore(t.TempDir()),Clock:clock,Debounce:750*time.Millisecond,ReconcileEvery:5*time.Minute,IdleAfter:15*time.Minute,ReminderCooldown:24*time.Hour,QueueBatch:32})
	ctx,cancel:=context.WithCancel(context.Background()); done:=make(chan error,1); go func(){done<-svc.Run(ctx)}()
	clock.Advance(750*time.Millisecond); clock.Advance(5*time.Minute); cancel(); if err:=<-done; err!=nil&&!errors.Is(err,context.Canceled){t.Fatal(err)}
	if svc.Sync.(*fakeSync).watcherReconciles!=1 || svc.Sync.(*fakeSync).periodicReconciles!=1 || svc.Sync.(*fakeSync).queueDrains==0 { t.Fatalf("sync calls %#v",svc.Sync) }
	if svc.Notifier.(*fakeNotifier).calls!=1 { t.Fatalf("notifications=%d",svc.Notifier.(*fakeNotifier).calls) }
}

func TestWatcherImportsContainNoSemanticOrGitPackages(t *testing.T) {
	for _,file:=range goFiles(t,".") { body:=string(mustRead(t,file)); for _,forbidden:=range []string{"internal/proposal","internal/apply","internal/repository","exec.Command(\"git\""} { if strings.Contains(body,forbidden){t.Fatalf("%s contains %s",file,forbidden)} } }
}
```

- [ ] **Step 2: Run tests to verify watcher contracts are absent**

Run:

```bash
go test ./internal/watcher -run 'TestRunDebounces|TestWatcherImports' -v
```

Expected: FAIL because `watcher.Options`, `Service`, and durable state do not exist.

- [ ] **Step 3: Define dependency-injected watcher-only interfaces**

```go
// internal/watcher/types.go
type EventSource interface { Events() <-chan sync.FileEvent; Errors() <-chan error; Rescan(context.Context) error; Close() error }
type Syncer interface {
	Observe(sync.FileEvent)(sync.EventDisposition,error)
	Ready(time.Time)[]string
	Reconcile(context.Context,sync.ReconcileRequest)(sync.Report,error)
	DrainQueue(context.Context,int)(sync.QueueReport,error)
}
type PendingScanner interface { Scan(context.Context,pending.Options)(pending.Report,error) }
type Indexer interface { Refresh(context.Context,string) error }
type Notifier interface { Notify(context.Context,notify.Message) error }
type Clock interface { Now() time.Time; NewTicker(time.Duration) Ticker; After(time.Duration)<-chan time.Time }
type Options struct { ProjectID string; Events EventSource; Sync Syncer; Pending PendingScanner; Index Indexer; Notifier Notifier; State *StateStore; Clock Clock; Debounce,ReconcileEvery,IdleAfter,ReminderCooldown time.Duration; QueueBatch int }
```

Defaults are exactly `750ms`, `5m`, `15m`, `24h`, and `32`. Validation rejects zero/negative durations, missing dependencies, or queue batches outside `1..1000`.

- [ ] **Step 4: Implement the loop using the sync engine's existing hash/debounce/queue boundary**

```go
func (s *Service) handleEvent(ctx context.Context,event sync.FileEvent) error {
	disposition,err:=s.Sync.Observe(event); if err!=nil{return err}
	if disposition!=sync.EventReady{return nil}
	ids:=s.Sync.Ready(s.Clock.Now()); if len(ids)==0{return nil}
	_,err=s.Sync.Reconcile(ctx,sync.ReconcileRequest{EntityIDs:ids,Trigger:sync.TriggerWatcher}); return err
}
func (s *Service) periodic(ctx context.Context) error {
	if err:=s.Events.Rescan(ctx);err!=nil{return err}
	if _,err:=s.Sync.Reconcile(ctx,sync.ReconcileRequest{Trigger:sync.TriggerPeriodic});err!=nil{return err}
	if _,err:=s.Sync.DrainQueue(ctx,s.QueueBatch);err!=nil{return err}
	if err:=s.Index.Refresh(ctx,s.ProjectID);err!=nil{return err}
	report,err:=s.Pending.Scan(ctx,pending.Options{ProjectID:s.ProjectID,IdleAfter:s.IdleAfter,Now:s.Clock.Now()});if err!=nil{return err}
	return s.remind(ctx,report)
}
```

Errors are classified and stored without raw paths/content; a failed event is recovered by the next periodic reconciliation or existing durable sync queue. The watcher state contains only version, project ID, `review_pending`, last scan, last reminder, last safe error class, and aggregate counts. `atomicfile.Write` persists it after redaction. The notification body is exactly `"SessionReviewer found N idle session(s) with unreviewed events. Run: session-reviewer resume --ledger-only"`.

- [ ] **Step 5: Verify restart, crash, rapid-save, and canary behavior**

Run:

```bash
gofmt -w internal/watcher
go test ./internal/watcher -v
go test -race ./internal/watcher -count=5
go test ./internal/watcher -run 'TestRestart|TestRapid|TestCanary|TestMissed' -count=20
```

Expected: PASS; a restart reloads cooldown state; 100 rapid saves produce one reconcile; missed events are found within five minutes; no canary appears beneath the state root.

- [ ] **Step 6: Commit the deterministic watcher core**

```bash
git add internal/watcher
git commit -m "feat: add deterministic watcher reconciliation loop"
```

---

### Task 6: Add Native File Event and User Notification Adapters

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/watcher/fsnotify.go`
- Create: `internal/watcher/fsnotify_test.go`
- Create: `internal/platform/notify/notify.go`
- Create: `internal/platform/notify/notify_darwin.go`
- Create: `internal/platform/notify/notify_windows.go`
- Create: `internal/platform/notify/notify_other.go`
- Create: `internal/platform/notify/notify_test.go`

**Interfaces:**
- Consumes: project/vault review roots and existing `sync.FileEvent`/content hash contracts.
- Produces: `watcher.NewFSNotify(projectRoot,vaultRoot string,now func() time.Time) (watcher.EventSource,error)` and `notify.New(runtime.GOOS,notify.Runner) notify.Notifier`.

- [ ] **Step 1: Write failing native adapter tests**

```go
func TestFSNotifyRecursivelyMapsNativeWritesAndSuppressesUnchangedHash(t *testing.T) {
	project,vault:=t.TempDir(),t.TempDir(); source,err:=NewFSNotify(project,vault,time.Now);if err!=nil{t.Fatal(err)};defer source.Close()
	path:=filepath.Join(project,"decisions","d1.md");if err:=os.MkdirAll(filepath.Dir(path),0o755);err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(path,[]byte("one"),0o600);err!=nil{t.Fatal(err)}
	event:=waitEvent(t,source.Events());if event.Side!=sync.SideProject||event.RelativePath!="decisions/d1.md"||event.ObservedHash!=sha256Hex([]byte("one")){t.Fatalf("%#v",event)}
}
```

```go
func TestNotificationEscapesUntrustedTextAsData(t *testing.T) {
	runner:=&captureRunner{}; n:=newDarwin(runner)
	msg:=Message{Title:`x" & do shell script "touch /tmp/pwned`,Body:"Run: session-reviewer resume --ledger-only"}
	if err:=n.Notify(context.Background(),msg);err!=nil{t.Fatal(err)}
	if strings.Contains(strings.Join(runner.args," "),msg.Title){t.Fatal("untrusted title embedded in AppleScript source")}
}
```

- [ ] **Step 2: Run adapter tests before installing fsnotify**

Run:

```bash
go test ./internal/watcher ./internal/platform/notify -run 'TestFSNotify|TestNotification' -v
```

Expected: FAIL because the adapters do not exist.

- [ ] **Step 3: Implement the justified cross-platform native event adapter**

Use `github.com/fsnotify/fsnotify v1.10.1`; on macOS it maps to kqueue and on Windows to `ReadDirectoryChangesW`, avoiding a C toolchain while keeping native kernel notifications. `NewFSNotify` recursively registers existing directories under the two allowed review roots, adds newly created directories after pathguard validation, converts rename/remove into a rescan signal, hashes only stable regular Markdown files, ignores `.session-reviewer-*` temporaries and `*.session-reviewer-backup`, and emits relative forward-slash paths. Hash/read races queue a rescan rather than emitting partial content.

```go
type FSNotifySource struct { watcher *fsnotify.Watcher; project,vault *pathguard.Directory; events chan sync.FileEvent; errors chan error; now func()time.Time; cancel context.CancelFunc }
func NewFSNotify(projectRoot,vaultRoot string,now func()time.Time)(*FSNotifySource,error)
func (s *FSNotifySource) Events()<-chan sync.FileEvent
func (s *FSNotifySource) Errors()<-chan error
func (s *FSNotifySource) Rescan(context.Context)error
func (s *FSNotifySource) Close()error
```

- [ ] **Step 4: Implement safe native notifications with local-state fallback**

macOS invokes `/usr/bin/osascript` with a constant script and passes title/body as argv data:

```go
const appleScript = `on run argv
display notification (item 2 of argv) with title (item 1 of argv)
end run`
```

Windows writes a constant PowerShell program to `%LOCALAPPDATA%\SessionReviewer\notify-toast.ps1` and passes the base64-encoded UTF-8 title/body as `-TitleBase64` and `-BodyBase64` data arguments. The program XML-escapes decoded values with `SecurityElement.Escape`, creates a `Windows.UI.Notifications.ToastNotification`, and calls `ToastNotificationManager.CreateToastNotifier("SessionReviewer").Show($toast)`. Command failures return `notify.ErrUnavailable`; the watcher retains `review_pending` and cooldown state so `watch status` remains a complete fallback.

- [ ] **Step 5: Run native integration and cross-compilation tests**

Run:

```bash
go get github.com/fsnotify/fsnotify@v1.10.1
gofmt -w internal/watcher internal/platform/notify
go test ./internal/watcher ./internal/platform/notify -v
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go test ./internal/platform/notify ./internal/watcher
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test ./internal/platform/notify ./internal/watcher
```

Expected: PASS; native local test observes a real file write; both adapters compile with CGO disabled.

- [ ] **Step 6: Commit native adapters**

```bash
git add go.mod go.sum internal/watcher/fsnotify.go internal/watcher/fsnotify_test.go internal/platform/notify
git commit -m "feat: add native file events and review reminders"
```

---

### Task 7: Register Idempotent User-Level Startup on macOS and Windows

**Files:**
- Create: `internal/platform/startup/startup.go`
- Create: `internal/platform/startup/launchd_darwin.go`
- Create: `internal/platform/startup/task_windows.go`
- Create: `internal/platform/startup/startup_other.go`
- Create: `internal/platform/startup/startup_test.go`
- Create: `internal/cli/watch.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: current executable path, data directory, config, watcher service, and platform notification adapter.
- Produces: `startup.Manager.Install(context.Context,startup.Spec) (startup.Status,error)`, `Status(context.Context)`, `Uninstall(context.Context,bool)`, plus `watch run|install|status|uninstall` CLI commands.

- [ ] **Step 1: Write failing exact-command and idempotence tests**

```go
func TestLaunchdInstallIsUserScopedAndIdempotent(t *testing.T) {
	runner:=&fakeRunner{}; m:=newLaunchd(runner,"/Users/me/Library/LaunchAgents",501)
	spec:=Spec{Executable:"/Users/me/.local/bin/session-reviewer",DataDir:"/Users/me/.local/share/session-reviewer"}
	first,err:=m.Install(context.Background(),spec);if err!=nil{t.Fatal(err)}
	second,err:=m.Install(context.Background(),spec);if err!=nil{t.Fatal(err)}
	if first.Scope!="user"||second.Scope!="user"{t.Fatalf("%#v %#v",first,second)}
	assertNoArgs(t,runner.calls,"sudo","system/")
	assertContainsCall(t,runner.calls,"launchctl","bootstrap","gui/501")
}

func TestWindowsInstallUsesLimitedOnLogonTask(t *testing.T) {
	runner:=&fakeRunner{};m:=newTaskScheduler(runner)
	_,err:=m.Install(context.Background(),Spec{Executable:`C:\Users\Me\AppData\Local\SessionReviewer\bin\session-reviewer.exe`,DataDir:`C:\Users\Me\AppData\Local\SessionReviewer`});if err!=nil{t.Fatal(err)}
	assertExactCall(t,runner.calls,`schtasks.exe /Create /F /SC ONLOGON /RL LIMITED /TN SessionReviewer /TR "\"C:\Users\Me\AppData\Local\SessionReviewer\bin\session-reviewer.exe\" watch run --data-dir \"C:\Users\Me\AppData\Local\SessionReviewer\""`)
}
```

- [ ] **Step 2: Run startup tests and verify the adapters are absent**

Run:

```bash
go test ./internal/platform/startup ./internal/cli -run 'TestLaunchd|TestWindowsInstall|TestRunWatch' -v
```

Expected: FAIL because startup managers and watcher commands do not exist.

- [ ] **Step 3: Define a platform-neutral lifecycle contract and manifest**

```go
type Spec struct { Executable,DataDir string }
type Status struct { Installed,Running bool; Scope,DefinitionPath,LastErrorClass string }
type Manager interface { Install(context.Context,Spec)(Status,error); Status(context.Context)(Status,error); Uninstall(context.Context,bool)(Status,error) }
type Manifest struct { Version int `json:"version"`; Executable string `json:"executable"`; DataDir string `json:"data_dir"`; DefinitionPath string `json:"definition_path"`; InstalledAt string `json:"installed_at"` }
```

The manifest is private (`0600`) and written atomically after successful native registration. Reinstall compares the exact executable/data-dir spec; an exact match is a no-op/kickstart, while a changed spec replaces the user definition. `Uninstall(false)` removes only startup registration and watcher manifest/state; `Uninstall(true)` additionally removes index and per-project queue/merge bases only after CLI `--purge-state` confirmation. It never deletes project/vault Markdown.

- [ ] **Step 4: Implement exact user launch mechanisms**

macOS writes `~/Library/LaunchAgents/com.neomei.session-reviewer.plist` with `Label`, exact `ProgramArguments` (`watch run --data-dir ...`), `RunAtLoad=true`, `KeepAlive=false`, and private stdout/stderr log paths. The adapter converts `os.Getuid()` to decimal text (for UID 501, domain `gui/501`) and passes absolute paths directly to `launchctl bootstrap gui/501 /Users/me/Library/LaunchAgents/com.neomei.session-reviewer.plist` and `launchctl kickstart -k gui/501/com.neomei.session-reviewer`; it does not depend on shell expansion. Already-loaded exit status is treated as idempotent only after `launchctl print gui/501/com.neomei.session-reviewer` confirms the same label. Uninstall runs `launchctl bootout gui/501/com.neomei.session-reviewer` and removes the plist.

Windows runs `schtasks.exe /Create /F /SC ONLOGON /RL LIMITED /TN SessionReviewer /TR "\"%LOCALAPPDATA%\SessionReviewer\bin\session-reviewer.exe\" watch run --data-dir \"%LOCALAPPDATA%\SessionReviewer\""`, verifies with `/Query /TN SessionReviewer /FO CSV /V`, and deletes with `/Delete /F /TN SessionReviewer`. It rejects executable/data paths containing control characters or quotes that cannot be represented losslessly; status never treats localized display text as the authority—the exit code and exact task name are authoritative.

- [ ] **Step 5: Wire lifecycle CLI and prove idempotence**

```text
session-reviewer watch run [--data-dir DIR] [--once]
session-reviewer watch install [--data-dir DIR]
session-reviewer watch status [--data-dir DIR] [--json]
session-reviewer watch uninstall [--data-dir DIR] [--purge-state]
```

`install`, `status`, and `uninstall` print installed/running/review-pending/queue counts and the exact next recovery command. `watch run` accepts no semantic/proposal flags. Run:

```bash
gofmt -w internal/platform/startup internal/cli
go test ./internal/platform/startup ./internal/cli -v
go test ./internal/platform/startup -run 'TestLaunchdInstallIsUserScopedAndIdempotent|TestWindowsInstallUsesLimitedOnLogonTask' -count=20
```

Expected: PASS; install twice and uninstall twice both exit 0; no command includes `sudo`, `/RL HIGHEST`, or a system startup location.

- [ ] **Step 6: Commit user-level watcher lifecycle**

```bash
git add internal/platform/startup internal/cli/watch.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: manage user level watcher startup"
```

---

### Task 8: Gate History and Watcher Security, Recovery, Performance, and Documentation

**Files:**
- Create: `internal/watcher/acceptance_test.go`
- Create: `internal/history/acceptance_test.go`
- Modify: `.github/workflows/ci.yml`
- Modify: `README.md`

**Interfaces:**
- Consumes: all history/watcher packages and prior sync/ledger acceptance fixtures.
- Produces: a fresh cross-platform gate proving worktree identity, accepted-only history, bounded index/watcher memory, missed-event recovery, queue durability, no canary leakage, no Git/model calls, and manual-operation parity.

- [ ] **Step 1: Write full acceptance tests**

```go
func TestWatcherAcceptanceNeverLeaksCanaryOrMutatesGitAndRecoversMissedEvents(t *testing.T) {
	fixture:=newAcceptanceFixture(t,"OPENAI_API_KEY=sk-canary-123456789012345678901234567890")
	before:=fixture.gitSnapshot()
	fixture.disableEvents(); fixture.editProjectDecision(); fixture.advance(5*time.Minute); fixture.restartWatcher()
	if !fixture.vaultHasRedactedEdit(){t.Fatal("periodic reconciliation did not recover missed event")}
	if after:=fixture.gitSnapshot();after!=before{t.Fatalf("watcher mutated Git: before=%q after=%q",before,after)}
	fixture.assertCanaryAbsent(fixture.dataRoot,fixture.projectRoot,fixture.vaultRoot)
}

func TestHistoryAcceptanceShowsEvolutionRatherThanSessionConcatenation(t *testing.T) {
	view:=buildTwoSessionSupersessionFixture(t)
	markdown:=view.Markdown()
	for _,want:=range []string{"Original choice","Discovered problem","Replacement","Current state","Unresolved themes"}{if !strings.Contains(markdown,want){t.Fatalf("missing %q",want)}}
	if strings.Contains(markdown,"Session 1 summary\nSession 2 summary"){t.Fatal("history concatenated independent summaries")}
}
```

- [ ] **Step 2: Run acceptance tests before expanding CI**

Run:

```bash
go test ./internal/history ./internal/watcher -run Acceptance -v
```

Expected: PASS only after Tasks 1–7; failures name the violated invariant without printing session content.

- [ ] **Step 3: Add cross-platform CI gates**

Add these exact steps to every existing macOS/Windows matrix entry:

```yaml
      - run: go test ./internal/history ./internal/watcher ./internal/platform/startup -count=2
      - run: go test -race ./internal/history ./internal/watcher
      - run: go test ./internal/history ./internal/watcher -run Acceptance -v
      - run: go test ./internal/index -run TestIndexIsDisposableAndRebuildsFromAcceptedSnapshot -count=2
      - run: go list -deps ./internal/watcher
        shell: bash
      - run: CGO_ENABLED=0 go build ./cmd/session-reviewer
        shell: bash
```

An architecture test parses the dependency list and fails if it contains `internal/apply`, `internal/proposal`, `internal/repository`, any HTTP/OpenAI client, or a Git runner. A 100,000-event synthetic metadata scan must remain below 96 MiB peak heap and complete within 30 seconds on each CI runner; native release hardware records stricter evidence later in the release-hardening plan.

- [ ] **Step 4: Document exact workflows and failure recovery**

Update `README.md` with runnable sections for:

```bash
session-reviewer history --ledger-only --project .
session-reviewer resume --ledger-only --project .
session-reviewer watch install
session-reviewer watch status --json
session-reviewer watch uninstall
session-reviewer watch uninstall --purge-state
```

Document the 750 ms debounce, five-minute reconciliation, 15-minute idle threshold, 24-hour reminder cooldown, cache quarantine name, alias/association commands, LaunchAgent/task names, and that watcher disablement does not impair manual commands. State explicitly that `index.sqlite` is disposable, watcher notifications contain counts plus a command only, the watcher never calls a model/Git, and `--purge-state` preserves project/vault Markdown.

- [ ] **Step 5: Run the complete history/watcher completion gate**

Run:

```bash
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build ./cmd/session-reviewer
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /tmp/session-reviewer-darwin-amd64 ./cmd/session-reviewer
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /tmp/session-reviewer-darwin-arm64 ./cmd/session-reviewer
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/session-reviewer-windows-amd64.exe ./cmd/session-reviewer
git diff --check
git status --short
```

Expected: tests, race tests, vet, and builds exit 0; `git diff --check` is silent; status lists only the intended Task 8 files before commit.

- [ ] **Step 6: Commit the accepted history/watcher milestone**

```bash
git add internal/history/acceptance_test.go internal/watcher/acceptance_test.go .github/workflows/ci.yml README.md
git commit -m "test: accept cross-session history and watcher"
```

## History and Watcher Completion Gate

Before release hardening begins, preserve command output for a fresh run of:

```bash
go test ./...
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build ./cmd/session-reviewer
session-reviewer history --ledger-only --project .
session-reviewer resume --ledger-only --project .
session-reviewer watch install
session-reviewer watch install
session-reviewer watch status --json
session-reviewer watch uninstall
session-reviewer watch uninstall
git status --short
```

Acceptance requires one project history across a main checkout and worktree, ambiguity on colliding remote-only identities, ordered supersession chains across at least two accepted sessions, unresolved accepted-tag themes, a recovery card with drift/pending/first action, cache rebuild after intentional corruption, missed-event recovery by periodic reconciliation, durable retry after a locked vault, one reminder per cooldown, no model/Git invocation, and idempotent no-admin lifecycle on both supported operating systems.
