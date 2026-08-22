# SessionReviewer Deterministic Three-Way Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build deterministic, recoverable Base/Project/Vault synchronization that preserves human Markdown edits, exposes conflicts and explicit resolutions, and never invokes a semantic model or mutates Git.

**Architecture:** Each Markdown entity is parsed into a lossless frontmatter/body document whose editable units are merged against a durable per-entity base. A per-project locked sync engine scans stable entity identities, plans every entity independently, applies root-confined atomic writes through a crash journal and retry queue, and advances a base only after Project and Vault both verify the same accepted document. Conflict notes mirror both candidates to both sides while unrelated entities continue.

**Tech Stack:** Go 1.26; Go standard library including `os.Root`; `github.com/pelletier/go-toml/v2 v2.4.3`; `gopkg.in/yaml.v3 v3.0.1`; `golang.org/x/text v0.41.0`; existing `internal/atomicfile`, `internal/pathguard`, `internal/project`, `internal/config`, and `internal/redact` packages; GitHub Actions macOS and Windows runners.

## Global Constraints

- Approved design scope is sections 5.5, 8.8, 10.6, 11, and 13–16 of `docs/superpowers/specs/2026-08-22-session-reviewer-design.md`.
- Obsidian is a first-class editable view at `<Vault>/Projects/<stable-project-segment>/Session Review/`; the project ledger remains the durable repository copy.
- Synchronization is deterministic and performs no model, network, shell, Markdown execution, or semantic inference call.
- No automatic Git commit, push, reset, checkout, clean, restore, add, branch, tag, or rollback is permitted.
- `status`, `title`, `tags`, every recognized narrative section, every unknown frontmatter key, and every unknown body section are editable units.
- `id`, `entity_type`, `project_id`, `sync_status`, `sync_hash`, `base_hash`, `project_hash`, and `vault_hash` are machine-reserved; the domain field `status` remains editable.
- A missing file is not a deletion request. `status: archived` is the only automatic logical-deletion signal; physical deletion is never automatic.
- A merge base advances only after Project and Vault both contain and verify the accepted content.
- A malformed entity, invalid reserved-field edit, normalized-path collision, or suspected secret is preserved at its source and isolated from automatic writes.
- Raw candidate content never appears in CLI errors, queue metadata, transaction journals, or logs. Suspected secrets never enter merge bases or conflict notes.
- Sync never reads, repairs, creates, or advances accepted session cursors.
- `sync --dry-run` does not modify Project, Vault, merge bases, queue, journals, conflicts, config, or self-loop hashes.
- Target macOS 13+ on Intel and Apple Silicon, and Windows 10 22H2+/Windows 11 on x86-64 without administrator privileges.
- All writes are relative to already opened project, vault, or per-project data roots and use same-directory atomic replacement.

## Scope Boundary

This plan implements explicit synchronization, sync status, conflict resolution, retry/recovery primitives, and watcher-consumable event coalescing. It does not implement semantic proposal application, `resume`, `history`, filesystem watcher registration, `launchd`, Task Scheduler, notifications, packaging, or release publication. The later watcher calls the public `sync.Engine` interfaces defined here; it does not replace their locking, queue, hash, or reconciliation semantics.

## Approved-Design Requirement Routing

| Design requirement | This plan's implementation/acceptance |
|---|---|
| 5.5 editable Obsidian view | Tasks 1–3 persist a stable vault review root and lossless entity representation |
| 8.8 editable/reserved fields | Tasks 2, 4, and 5 preserve unknown units, merge editable units, and isolate reserved edits |
| 10.6 explicit sync and dry-run | Tasks 8–9 provide read-only planning plus deterministic application |
| 11.1 Base/Project/Vault matrix | Tasks 3–4 cover additions, edits, field/section changes, missing files, rename, archive, and conflicts |
| 11.2 conflict behavior | Task 5 mirrors candidates/actions; Task 8 keeps unrelated progress moving |
| 11.3 integrity | Tasks 6–8 cover locks, atomic rooted writes, hashes, debounce, durable retry, reconciliation, and recovery |
| 13 failure handling | Tasks 3, 5–8 isolate malformed input, queue unavailable Vault work, preserve dirty-worktree changes, and retry Windows locks; sync never touches SQLite, source sessions, models, or cursors |
| 14 implementation structure | File Map follows `internal/ledger`, `internal/sync`, `internal/platform`, and existing CLI boundaries without adding a C toolchain |
| 15 testing | Tasks 2–10 cover security canaries, full merge matrix, idempotence, crash recovery, cross-platform paths/locks/replacement, and native CI |
| 16 Scenario A | Acceptance proves repeated sync is incremental/no-op and cursor state is byte-identical; semantic checkpoint/timeline/diagram assertions belong to the ledger/Skill plan |
| 16 Scenario B | Acceptance proves an Obsidian decision/next-action edit reaches Project; semantic `resume` recovery-card assertions belong to the ledger/Skill plan |
| 16 Scenario C | No sync-specific semantic assertion; cross-session supersession/history belongs to the history plan |
| 16 Scenario D | Task 10 fully verifies visible/recoverable conflict and unrelated entity progress |
| 16 Scenario E | Task 10 and native CI verify Windows Unicode path, sync, resolution, atomic retry, and restart recovery; startup watcher, `resume`, `history`, and uninstall belong to later plans |

## File Map

```text
go.mod                                  Add YAML AST and Unicode normalization dependencies
go.sum                                  Record dependency checksums
internal/config/config.go               Persist stable vault review path and case policy
internal/config/config_test.go          Mapping migration and validation tests
internal/platform/pathkey.go            Unicode/case-aware relative-path identity
internal/platform/pathkey_test.go       NFC, macOS case modes, and Windows comparisons
internal/project/init.go                Create/backfill stable Obsidian mapping and entity identity
internal/project/init_test.go           Stable mapping, collision-safe segment, and re-init tests
internal/project/lock.go                 Export reusable per-project advisory lock
internal/project/lock_unix.go            POSIX flock adapter renamed for reusable lock
internal/project/lock_windows.go         Windows LockFileEx adapter renamed for reusable lock
internal/project/lock_test.go            live-owner, crash-release, and cross-process tests
internal/pathguard/tree.go               Root-confined directory creation, reads, and Markdown walk
internal/pathguard/tree_test.go          traversal, symlink, junction/reparse, and race tests
internal/ledger/document.go              YAML-node frontmatter parser, identity, editable/reserved units
internal/ledger/body.go                  Fence-aware Markdown section AST
internal/ledger/document_test.go         Round-trip and unknown-key/section preservation tests
internal/ledger/scan.go                  Stable ID/path inventory and normalized collision detection
internal/ledger/scan_test.go             duplicate ID, case, Unicode, malformed, and archive tests
internal/sync/types.go                   Public sync contracts, reports, operations, and enums
internal/sync/base_store.go              Atomic durable base records and last-written hashes
internal/sync/base_store_test.go         corruption, recovery, CAS, and private-state tests
internal/sync/merge.go                    Field/section three-way merge and archive matrix
internal/sync/merge_test.go               Exhaustive Base/Project/Vault table tests
internal/sync/conflict.go                 Mirrored conflict/repair notes and resolution validation
internal/sync/conflict_test.go            lossless candidates, stale resolve, and action tests
internal/sync/queue.go                    Content-free durable retry queue and exponential backoff
internal/sync/queue_test.go               ordering, dedupe, restart, bounds, and canary tests
internal/sync/events.go                   self-loop suppression and debounce/coalescing
internal/sync/events_test.go              rapid-save, repeated-event, case, and Unicode tests
internal/sync/writer.go                   rooted atomic writes with bounded transient retry
internal/sync/writer_test.go              Windows sharing violation and path escape tests
internal/sync/transaction.go              content-free per-entity crash journals
internal/sync/transaction_test.go         every interruption point and restart convergence
internal/sync/service.go                  locked scan/plan/apply/status/resolve/queue orchestration
internal/sync/service_test.go             dry-run, unavailable vault, and unrelated progress tests
internal/sync/acceptance_test.go          macOS/Windows semantic parity and end-to-end scenarios
internal/cli/sync.go                      `sync`, `sync status`, and `sync resolve` flags/output
internal/cli/run.go                       Route sync commands
internal/cli/run_test.go                  CLI exit, JSON status, dry-run, and safe-error tests
.github/workflows/ci.yml                  Run sync tests on macOS arm64/x64 and Windows x64
README.md                                 Commands, mapping, conflicts, archive, and recovery guidance
testdata/sync/base-decision.md            Sanitized base entity fixture
testdata/sync/project-decision.md         Independent Project edit fixture
testdata/sync/vault-decision.md           Independent Vault edit fixture
testdata/sync/conflicting-decision.md     Same-section conflict fixture
```

---

### Task 1: Persist a Stable, Unicode-Safe Obsidian Mapping

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/platform/pathkey.go`
- Create: `internal/platform/pathkey_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/project/init.go`
- Modify: `internal/project/init_test.go`

**Interfaces:**
- Consumes: an existing `config.ProjectMapping`, physical vault root, project basename, project ID, and target OS.
- Produces: `platform.PathKey(goos string, caseMode platform.CaseMode, relative string) (string, error)`, `project.DefaultVaultReviewPath(projectName, projectID string) (string, error)`, and mapping fields `VaultReviewPath string` plus `VaultCaseMode platform.CaseMode`.

- [ ] **Step 1: Write failing mapping and path-identity tests**

```go
func TestPathKeyNormalizesObsidianUnicodeAndConfiguredCase(t *testing.T) {
	composed, err := PathKey("darwin", platform.CaseInsensitive, "Decisions/Café.md")
	if err != nil { t.Fatal(err) }
	decomposed, err := PathKey("darwin", platform.CaseInsensitive, "decisions/Cafe\u0301.md")
	if err != nil { t.Fatal(err) }
	if composed != decomposed { t.Fatalf("keys differ: %q %q", composed, decomposed) }
	sensitive, _ := PathKey("darwin", platform.CaseSensitive, "decisions/CAFÉ.md")
	if sensitive == composed { t.Fatal("case-sensitive volume collapsed distinct names") }
}

func TestDefaultVaultReviewPathIsStableAndCrossPlatformSafe(t *testing.T) {
	got, err := DefaultVaultReviewPath(`会话:审查. `, "project-2a2a2a2a2a2a2a2a")
	if err != nil { t.Fatal(err) }
	want := filepath.ToSlash(filepath.Join("Projects", "会话-审查--2a2a2a2a", "Session Review"))
	if got != want { t.Fatalf("got=%q want=%q", got, want) }
}

func TestInitializeBackfillsStableVaultMappingOnce(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version:1, Projects:[]config.ProjectMapping{{ID:id, Root:root, VaultRoot:vault}}}); err != nil { t.Fatal(err) }
	writeTestOverview(t, root, id)
	opts := InitOptions{ProjectRoot:root, VaultRoot:vault, DataDir:data, GOOS:runtime.GOOS, Random:errorReader{}}
	if _, err := Initialize(opts); err != nil { t.Fatal(err) }
	first, err := config.Load(filepath.Join(data, "config.toml")); if err != nil { t.Fatal(err) }
	if _, err := Initialize(opts); err != nil { t.Fatal(err) }
	second, err := config.Load(filepath.Join(data, "config.toml")); if err != nil { t.Fatal(err) }
	if len(first.Projects) != 1 || first.Projects[0].VaultReviewPath == "" || first.Projects[0].VaultCaseMode == "" { t.Fatalf("first=%+v", first) }
	if first.Projects[0] != second.Projects[0] { t.Fatalf("mapping changed: first=%+v second=%+v", first.Projects[0], second.Projects[0]) }
}
```

The third test fixture must create real temporary project/vault/data directories, seed `config.toml` through `config.Save`, and compare both saved mappings rather than mocking configuration.

- [ ] **Step 2: Run the focused tests and confirm the new contracts are absent**

Run: `go test ./internal/platform ./internal/config ./internal/project -run 'Test(PathKey|DefaultVaultReviewPath|InitializeBackfills)' -v`

Expected: FAIL with undefined `PathKey`, `CaseInsensitive`, `DefaultVaultReviewPath`, and mapping fields.

- [ ] **Step 3: Add exact mapping types and validation**

```go
// internal/platform/pathkey.go
type CaseMode string

const (
	CaseSensitive   CaseMode = "sensitive"
	CaseInsensitive CaseMode = "insensitive"
)

// internal/config/config.go
type ProjectMapping struct {
	ID              string   `toml:"id"`
	Root            string   `toml:"root"`
	VaultRoot       string   `toml:"vault_root"`
	VaultReviewPath string   `toml:"vault_review_path,omitempty"`
	VaultCaseMode   platform.CaseMode `toml:"vault_case_mode,omitempty"`
}
```

Keep configuration version `1` so existing files load. `validate` accepts both fields empty for migration, but requires both together; a non-empty review path must be slash-separated, relative, clean, below `Projects/`, end in `/Session Review`, contain no `.`/`..` component, NUL, Windows-reserved component, trailing dot/space, or absolute/UNC/device prefix. `VaultCaseMode` accepts only the two constants.

`PathKey` must validate the relative path first, convert separators to `/`, apply NFC with `norm.NFC.String`, and apply `cases.Fold().String` only for Windows or `CaseInsensitive`. It must not call `strings.ToLower`, because Unicode case folding is required.

`DefaultVaultReviewPath` must NFC-normalize and trim the project basename, replace control bytes and `<>:"/\\|?*` with one `-`, trim trailing dots/spaces, prefix Windows device names with `_`, cap the display portion at 64 Unicode code points without splitting a rune, fall back to `Project`, and append `--` plus the first eight hexadecimal project-ID characters. The suffix makes mapping stable even when the project directory is renamed.

- [ ] **Step 4: Backfill mapping and project overview identity under the existing init transaction**

Change `Initialize` so every successful new or existing mapping has the stable path and detected case mode before `config.SaveRoot`. Windows is always `CaseInsensitive`. On macOS, `detectCaseMode(vaultDir.Root)` creates two random names differing only by ASCII case, opens them through the pinned root, and removes both before return; an inconclusive probe fails initialization without saving config. Re-initialization must preserve a non-empty existing mapping rather than recomputing after a directory rename.

Within the same existing init advisory-lock transaction, create `<data>/projects/<project-id>/merge-bases`, `queue`, `transactions`, and `locks` with mode `0700`, plus a stable regular `locks/sync.lock` with mode `0600`. Validate every component through the pinned data root. This makes later `sync --dry-run` and `sync status` able to acquire the OS lock without creating any path. Re-initialization is idempotent and rejects a redirect/non-directory at any state component.

Render a newly created overview with exact reserved identity fields:

```yaml
---
id: project-overview
entity_type: project_overview
project_id: project-2a2a2a2a2a2a2a2a
sync_status: synced
created_at: 2026-08-23T00:00:00Z
---
```

Task 1 changes `overviewBody` so newly created files contain those fields using the existing deterministic formatter. Task 2 separately adds the AST-backed migration test and implementation for already-existing older overviews. No directory inside the vault review path is created in this task.

- [ ] **Step 5: Run mapping tests and commit**

Run: `go mod tidy && gofmt -w internal/platform internal/config internal/project && go test ./internal/platform ./internal/config ./internal/project -v`

Expected: PASS; existing version-1 mapping tests remain green; `go.mod` contains exact direct requirements `golang.org/x/text v0.41.0` and the existing TOML dependency.

```bash
git add go.mod go.sum internal/platform/pathkey.go internal/platform/pathkey_test.go internal/config/config.go internal/config/config_test.go internal/project/init.go internal/project/init_test.go
git commit -m "feat: persist stable Obsidian mapping"
```

---

### Task 2: Parse and Render Lossless Editable Markdown Entities

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/ledger/document.go`
- Create: `internal/ledger/body.go`
- Create: `internal/ledger/document_test.go`
- Modify: `internal/project/init.go`
- Modify: `internal/project/init_test.go`

**Interfaces:**
- Consumes: UTF-8 Markdown bytes with YAML frontmatter and a repository-relative slash path.
- Produces: `ledger.Parse(relativePath string, content []byte) (Document,error)`, `Document.Render() ([]byte,error)`, `Document.Identity() (Identity,error)`, `Document.Units() UnitSet`, `Document.WithUnits(UnitSet) (Document,error)`, `Document.WithSyncStatus(string) (Document,error)`, and `ledger.ContentHash([]byte) string`.

- [ ] **Step 1: Write failing AST preservation and validation tests**

```go
func TestDocumentPreservesUnknownFrontmatterAndBodySections(t *testing.T) {
	input := []byte("---\nid: decision-1\nentity_type: decision\nproject_id: project-1\ntitle: 'Keep quotes'\nplugin_key:\n  nested: true\n---\n\nPreamble.\n\n## Context\nHuman edit.\n\n## Plugin Section\n```query\n# not a heading\n```\n")
	doc, err := Parse("decisions/decision-1.md", input)
	if err != nil { t.Fatal(err) }
	units := doc.Units()
	units[UnitKey{Kind: UnitFrontmatter, Name: "status"}] = Unit{Present: true, Value: []byte("accepted\n")}
	updated, err := doc.WithUnits(units)
	if err != nil { t.Fatal(err) }
	out, err := updated.Render()
	if err != nil { t.Fatal(err) }
	for _, want := range []string{"plugin_key:", "nested: true", "## Plugin Section", "# not a heading", "'Keep quotes'"} {
		if !bytes.Contains(out, []byte(want)) { t.Fatalf("missing %q in %s", want, out) }
	}
}

func TestReservedFieldEditIsReportedWithoutEchoingValue(t *testing.T) {
	base := mustParse(t, entity("decision-1", "project-1", "Base"))
	edited := mustParse(t, entity("decision-evil-secret-value", "project-1", "Edit"))
	err := edited.ValidateReserved(base.Identity())
	if !errors.Is(err, ErrReservedField) || strings.Contains(err.Error(), "evil") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func mustParse(t *testing.T, content []byte) Document {
	t.Helper()
	doc, err := Parse("decisions/entity.md", content)
	if err != nil { t.Fatal(err) }
	return doc
}

func entity(id, projectID, body string) []byte {
	return []byte(fmt.Sprintf("---\nid: %s\nentity_type: decision\nproject_id: %s\nstatus: accepted\nsync_status: synced\n---\n\n## Context\n%s\n", id, projectID, body))
}
```

- [ ] **Step 2: Run the focused tests to verify failure**

Run: `go test ./internal/ledger -run 'Test(DocumentPreserves|ReservedField)' -v`

Expected: FAIL because `internal/ledger` does not exist.

- [ ] **Step 3: Define the exact document and unit types**

```go
type Identity struct { ID, EntityType, ProjectID string }

type UnitKind string
const (
	UnitFrontmatter UnitKind = "frontmatter"
	UnitPreamble    UnitKind = "preamble"
	UnitSection     UnitKind = "section"
)

type UnitKey struct { Kind UnitKind; Name string }
type Unit struct { Present bool; Value []byte }
type UnitSet map[UnitKey]Unit

type Document struct {
	relativePath string
	raw          []byte
	frontmatter  *yaml.Node
	body         Body
	dirty        bool
}

var ReservedFields = map[string]struct{}{
	"id": {}, "entity_type": {}, "project_id": {}, "sync_status": {},
	"sync_hash": {}, "base_hash": {}, "project_hash": {}, "vault_hash": {},
}
```

`Parse` requires valid UTF-8, LF or CRLF input no larger than 4 MiB, exactly one YAML document enclosed by first-line `---` and a closing `---`, a mapping root, scalar unique keys, and valid identity scalars. It rejects aliases, merge keys, duplicate keys, tags outside YAML core tags, NUL, and executable/link directives as data only; it never evaluates Markdown, YAML tags, templates, links, or code fences.

`Body` stores the exact preamble and a sequence of heading sections. Its fence-aware scanner recognizes ATX headings and Setext headings only outside backtick/tilde fences, tracks the full normalized heading ancestry plus occurrence number as `UnitKey.Name`, and retains heading line, line endings, and content bytes. Duplicate heading names are legal because the occurrence number is stable within the base document. `WithUnits` preserves the base order, removes deleted units, and appends newly introduced frontmatter keys and sections in deterministic key order.

Frontmatter unit values are canonical YAML node encodings including style and comments; body units are exact bytes. `Render` returns the original bytes when `dirty == false`; changed documents use LF, preserve all unknown nodes/sections, preserve scalar quoting style where valid, and end with one newline. `ContentHash` is lowercase SHA-256 hex of rendered bytes.

- [ ] **Step 4: Migrate older overview identity through the parser**

Replace the string-line overview mutation in `project.Initialize` with `ledger.Parse`, adding these values only when the existing file has `project_id` but lacks the other keys:

```go
units[ledger.UnitKey{Kind: ledger.UnitFrontmatter, Name: "id"}] = ledger.Unit{Present: true, Value: []byte("project-overview\n")}
units[ledger.UnitKey{Kind: ledger.UnitFrontmatter, Name: "entity_type"}] = ledger.Unit{Present: true, Value: []byte("project_overview\n")}
units[ledger.UnitKey{Kind: ledger.UnitFrontmatter, Name: "sync_status"}] = ledger.Unit{Present: true, Value: []byte("synced\n")}
```

An existing conflicting identity fails closed; its bytes and config remain unchanged.

- [ ] **Step 5: Run tests and commit**

Run: `go mod tidy && gofmt -w internal/ledger internal/project && go test ./internal/ledger ./internal/project -v`

Expected: PASS, including byte-identical no-op round trip, CRLF input, multilingual headings, fenced pseudo-headings, duplicate keys rejection, reserved edit rejection, and older overview migration.

```bash
git add go.mod go.sum internal/ledger internal/project/init.go internal/project/init_test.go
git commit -m "feat: add lossless Markdown entity AST"
```

---

### Task 3: Scan Stable Entities and Persist Durable Merge Bases

**Files:**
- Create: `internal/pathguard/tree.go`
- Create: `internal/pathguard/tree_test.go`
- Create: `internal/ledger/scan.go`
- Create: `internal/ledger/scan_test.go`
- Create: `internal/sync/types.go`
- Create: `internal/sync/base_store.go`
- Create: `internal/sync/base_store_test.go`

**Interfaces:**
- Consumes: pinned project/vault roots, configured case mode, `docs/session-review` or vault review relative root, and per-project data root.
- Produces: `ledger.Scan(*pathguard.Directory, rootRelative, goos string, caseMode platform.CaseMode) Inventory`, `sync.BaseStore.Load/List/Commit`, and shared sync enums/types.

- [ ] **Step 1: Write failing scanner and base-store tests**

```go
func TestScanFindsStableIDsAndIsolatesNormalizedCollisions(t *testing.T) {
	sources := []SourceDocument{
		{RelativePath:"decisions/Café.md", Content:entity("decision-1","project-1","one")},
		{RelativePath:"decisions/Cafe\u0301.md", Content:entity("decision-2","project-1","two")},
	}
	got := BuildInventory(sources, "darwin", platform.CaseInsensitive)
	if len(got.ByID) != 0 || len(got.Issues) != 2 { t.Fatalf("inventory=%+v", got) }
	for _, issue := range got.Issues { if issue.Kind != IssuePathCollision { t.Fatalf("issue=%+v", issue) } }
}

func TestBaseStoreCommitUsesCASAndRecoversAtomicBackup(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data); if err != nil { t.Fatal(err) }
	defer root.Close()
	store := BaseStore{Root:root}
	first := BaseRecord{Version: 1, EntityID: "decision-1", RelativePath: "decisions/decision-1.md", ContentHash: hash("one"), Content: []byte("one"), SyncedAt: fixedTime}
	if err := store.Commit("", first); err != nil { t.Fatal(err) }
	if err := store.Commit("stale", first); !errors.Is(err, ErrStaleBase) { t.Fatalf("err=%v", err) }
	primary := filepath.Join(data, baseRecordPath("decision-1"))
	backup := atomicfile.BackupPath(primary)
	if err := os.Rename(primary, backup); err != nil { t.Fatal(err) }
	got, found, err := store.Load("decision-1")
	if err != nil || !found || got.ContentHash != first.ContentHash { t.Fatalf("got=%+v found=%v err=%v",got,found,err) }
	if _, err := os.Stat(primary); !errors.Is(err, os.ErrNotExist) { t.Fatalf("read repaired primary: %v",err) }
	if _, err := os.Stat(backup); err != nil { t.Fatalf("backup removed: %v",err) }
}

func hash(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
```

- [ ] **Step 2: Run focused tests to verify failure**

Run: `go test ./internal/pathguard ./internal/ledger ./internal/sync -run 'Test(Scan|BaseStore)' -v`

Expected: FAIL with undefined tree, scanner, and base-store APIs.

- [ ] **Step 3: Add exact inventory and base contracts**

```go
type Entry struct {
	Identity     Identity
	RelativePath string
	PathKey      string
	Document     Document
	Content      []byte
	ContentHash  string
}
type IssueKind string
const (
	IssueMalformed      IssueKind = "malformed"
	IssueDuplicateID    IssueKind = "duplicate_id"
	IssuePathCollision  IssueKind = "path_collision"
	IssueReservedEdit   IssueKind = "reserved_edit"
	IssueSensitive      IssueKind = "sensitive_content"
)
type ScanIssue struct { Kind IssueKind; RelativePath, EntityID string; Err error }
type Inventory struct { ByID map[string]Entry; Issues []ScanIssue }
type SourceDocument struct { RelativePath string; Content []byte }
func BuildInventory([]SourceDocument, goos string, caseMode platform.CaseMode) Inventory

type Side string
const ( SideProject Side = "project"; SideVault Side = "vault" )

// Shared deterministic timestamp in internal/sync package tests.
var fixedTime = time.Date(2026,8,23,0,0,0,0,time.UTC)
var t0 = fixedTime

type BaseRecord struct {
	Version      int       `json:"version"`
	EntityID     string    `json:"entity_id"`
	RelativePath string    `json:"relative_path"`
	ContentHash  string    `json:"content_hash"`
	ProjectHash  string    `json:"project_hash"`
	VaultHash    string    `json:"vault_hash"`
	Content      []byte    `json:"content"`
	SyncedAt     time.Time `json:"synced_at"`
}
type BaseStore struct { Root *os.Root }
func (s BaseStore) Load(entityID string) (BaseRecord, bool, error)
func (s BaseStore) List() ([]BaseRecord, error)
func (s BaseStore) Commit(expectedContentHash string, next BaseRecord) error
func baseRecordPath(entityID string) string
```

The base filename is `merge-bases/<sha256(entity-id)>.json`; the record carries the ID and rejects hash/ID/path mismatches. State is mode `0600`; directories are `0700`. Load accepts a valid primary or atomic replacement backup, never repairs during read, rejects trailing JSON, files over 8 MiB, symlinks/reparse points, duplicate case-insensitive state names, and invalid UTF-8 content.

`pathguard` must expose `EnsureDirectory(relative string, perm fs.FileMode) error`, `ReadRegular(relative string, max int64) ([]byte,bool,error)`, and `WalkMarkdown(relative string, visit func(relative string, content []byte) error) error`. Each method is relative, rejects `..`, absolute, UNC, and device paths, pins every opened parent identity, rejects all redirects including Windows reparse points, never follows a Markdown symlink, and rechecks file identity after reading.

`ledger.Scan` skips `sync-conflicts/`, `.obsidian/`, non-Markdown files, atomic temporary/backup names, and hidden files. It sorts slash paths before parsing. Duplicate identity or normalized path key isolates every participant rather than selecting one.

- [ ] **Step 4: Run scanner/base security tests and commit**

Run: `gofmt -w internal/pathguard internal/ledger internal/sync && go test ./internal/pathguard ./internal/ledger ./internal/sync -run 'Test(Scan|BaseStore|Tree)' -v`

Expected: PASS, including symlink escape on macOS, reparse classification in Windows logic tests, concurrent stale CAS, corrupt primary/backup, Unicode collision, duplicate ID, and deterministic list order.

```bash
git add internal/pathguard/tree.go internal/pathguard/tree_test.go internal/ledger/scan.go internal/ledger/scan_test.go internal/sync/types.go internal/sync/base_store.go internal/sync/base_store_test.go
git commit -m "feat: inventory entities and persist merge bases"
```

---

### Task 4: Implement the Complete Field/Section and Archive Merge Matrix

**Files:**
- Create: `internal/sync/merge.go`
- Create: `internal/sync/merge_test.go`
- Create: `testdata/sync/base-decision.md`
- Create: `testdata/sync/project-decision.md`
- Create: `testdata/sync/vault-decision.md`
- Create: `testdata/sync/conflicting-decision.md`

**Interfaces:**
- Consumes: `MergeInput{Base *ledger.Document, Project Candidate, Vault Candidate}`.
- Produces: `Merge(MergeInput) MergeResult` with an accepted document or exact conflicting units, without filesystem writes.

- [ ] **Step 1: Encode the merge table as failing table-driven tests**

```go
type Candidate struct { Present bool; RelativePath string; Document ledger.Document; Hash string }
type MergeInput struct { EntityID string; Base *ledger.Document; Project, Vault Candidate }
type MergeKind string
const (
	MergeNoop         MergeKind = "noop"
	MergeWriteProject MergeKind = "write_project"
	MergeWriteVault   MergeKind = "write_vault"
	MergeWriteBoth    MergeKind = "write_both"
	MergeConflict     MergeKind = "conflict"
)
type UnitConflict struct { Key ledger.UnitKey; Base, Project, Vault ledger.Unit }
type MergeResult struct { Kind MergeKind; Accepted *ledger.Document; Conflicts []UnitConflict; Reason string }

func TestMergeUnitMatrix(t *testing.T) {
	cases := []struct{ name string; base, project, vault ledger.Unit; conflict bool; want ledger.Unit }{
		{"unchanged", present("b"), present("b"), present("b"), false, present("b")},
		{"project-only", present("b"), present("p"), present("b"), false, present("p")},
		{"vault-only", present("b"), present("b"), present("v"), false, present("v")},
		{"same-change", present("b"), present("x"), present("x"), false, present("x")},
		{"different-change", present("b"), present("p"), present("v"), true, ledger.Unit{}},
		{"project-delete", present("b"), absent(), present("b"), false, absent()},
		{"vault-delete", present("b"), present("b"), absent(), false, absent()},
		{"delete-vs-edit", present("b"), absent(), present("v"), true, ledger.Unit{}},
		{"both-delete", present("b"), absent(), absent(), false, absent()},
	}
	for _, tc := range cases { t.Run(tc.name, func(t *testing.T) {
		got, conflict := mergeUnit(tc.base, tc.project, tc.vault)
		if conflict != tc.conflict || !reflect.DeepEqual(got, tc.want) { t.Fatalf("got=%+v conflict=%v want=%+v conflict=%v",got,conflict,tc.want,tc.conflict) }
	}) }
}

func present(value string) ledger.Unit { return ledger.Unit{Present:true, Value:[]byte(value)} }
func absent() ledger.Unit { return ledger.Unit{} }
```

Add document-level tests for missing file recovery, first sync, disjoint unknown-key/unknown-section edits, same-section conflicts, title changes, file renames, domain status transitions, and archive-versus-edit.

- [ ] **Step 2: Run merge tests to verify failure**

Run: `go test ./internal/sync -run 'TestMerge' -v`

Expected: FAIL because `Merge` is undefined.

- [ ] **Step 3: Implement these exact entity rules**

| Base | Project | Vault | Result |
|---|---|---|---|
| absent | present | absent | copy Project to Vault, then establish base |
| absent | absent | present | copy Vault to Project, then establish base |
| absent | present | present and byte-equivalent | establish base without rewrite |
| absent | present | present and different | merge unique units; conflict every common different unit |
| present | missing | unchanged | restore Base to Project |
| present | unchanged | missing | restore Base to Vault |
| present | missing | modified | copy modified Vault candidate to Project; missing is not deletion |
| present | modified | missing | copy modified Project candidate to Vault; missing is not deletion |
| present | missing | missing | restore Base to both sides |
| present | changed | unchanged | apply Project to Vault |
| present | unchanged | changed | apply Vault to Project |
| present | changed | changed | merge by the unit matrix |

Before unit merge, both present candidates must validate the base `Identity`. A changed reserved field yields `MergeConflict` with reason `reserved_field`; it is never treated as a new entity. A newly discovered entity requires valid reserved identity and exact project ID.

Archive policy is exact: if one candidate changes domain `status` from a non-archived base to `archived` while the other candidate changes any other editable unit, return reason `archive_vs_modify`; if the other side is unchanged, propagate the archive; if both set archived, merge their remaining units normally. Missing files never set archived and archived files remain present on both sides.

Treat `RelativePath` as a separate merge unit. A one-sided rename propagates; a rename on one side plus content edits on the other merges; identical two-sided renames converge; different two-sided renames conflict. A case/NFC-only spelling change on a mapping whose `platform.PathKey` is unchanged keeps the Base spelling and is a no-op, preventing Obsidian normalization loops. On a case-sensitive mapping, a case change is a real rename. Rename targets must remain within the same ledger root, end in `.md`, avoid `sync-conflicts/`, and have no normalized collision. A rename is the only operation permitted to remove an old physical path: both new files and the new Base must verify first, then the writer removes an old file only when its pinned identity and pre-rename hash still match.

The accepted document is created from Base when present, applies merged units in deterministic key order, sets `sync_status: synced`, and renders once. Reserved hashes stay out of entity frontmatter; they live in `BaseRecord`.

The package-private primitive exercised by the table has the exact signature `func mergeUnit(base, project, vault ledger.Unit) (ledger.Unit, bool)`, where the Boolean is `true` only for a conflict.

- [ ] **Step 4: Run exhaustive merge tests and commit**

Run: `gofmt -w internal/sync && go test ./internal/sync -run 'TestMerge' -count=100`

Expected: PASS 100 repeated runs with identical result hashes; fixtures show no lost unknown key/section and no meaningless diff.

```bash
git add internal/sync/merge.go internal/sync/merge_test.go testdata/sync
git commit -m "feat: merge Base Project and Vault entities"
```

---

### Task 5: Create Mirrored Conflict Notes and Explicit Resolution Actions

**Files:**
- Create: `internal/sync/conflict.go`
- Create: `internal/sync/conflict_test.go`

**Interfaces:**
- Consumes: a conflicting merge result, source candidates, base, project ID, and resolution command.
- Produces: deterministic `ConflictRecord`, safe Markdown notes for both roots, `Resolution`, and a validated selected document.

- [ ] **Step 1: Write failing conflict and resolution tests**

```go
type ResolutionAction string
const (
	AcceptProject  ResolutionAction = "accept_project"
	AcceptObsidian ResolutionAction = "accept_obsidian"
	ManualMerge    ResolutionAction = "manual_merge"
)
type Resolution struct { ConflictID string; Action ResolutionAction; ManualFile string }

func TestConflictNotePreservesAllCandidatesAndActions(t *testing.T) {
	record := ConflictRecord{Version:1, ID:"conflict-1", EntityID:"decision-1", ProjectID:"project-1", Kind:ConflictUnits, RelativePath:"decisions/decision-1.md", BaseHash:hash("BASE"), ProjectHash:hash("PROJECT-EDIT"), VaultHash:hash("VAULT-EDIT"), Base:[]byte("BASE"), Project:[]byte("PROJECT-EDIT"), Vault:[]byte("VAULT-EDIT"), Suggested:[]byte("SUGGESTED"), CreatedAt:fixedTime}
	note, err := RenderConflict(record)
	if err != nil { t.Fatal(err) }
	for _, want := range [][]byte{[]byte(record.BaseHash), []byte(record.ProjectHash), []byte(record.VaultHash), []byte("accept_project"), []byte("accept_obsidian"), []byte("manual_merge"), []byte("PROJECT-EDIT"), []byte("VAULT-EDIT")} {
		if !bytes.Contains(note, want) { t.Fatalf("missing %q", want) }
	}
}

func TestResolveRejectsStaleConflictAndInvalidManualIdentity(t *testing.T) {
	record := ConflictRecord{Version:1, ID:"conflict-1", EntityID:"decision-1", ProjectID:"project-1", Kind:ConflictUnits, RelativePath:"decisions/decision-1.md", BaseHash:hash("BASE"), ProjectHash:hash("PROJECT"), VaultHash:hash("VAULT"), Base:entityBytes("decision-1","project-1","BASE"), Project:entityBytes("decision-1","project-1","PROJECT"), Vault:entityBytes("decision-1","project-1","VAULT"), CreatedAt:fixedTime}
	project := Candidate{Present:true, Hash:hash("PROJECT-CHANGED")}
	vault := Candidate{Present:true, Hash:record.VaultHash}
	if _, err := SelectResolution(record, Resolution{ConflictID:record.ID,Action:AcceptProject}, project, vault, nil); !errors.Is(err,ErrStaleConflict) { t.Fatalf("stale err=%v",err) }
	manual, err := ledger.Parse("decisions/decision-1.md", entityBytes("decision-1","project-other","MANUAL")); if err != nil { t.Fatal(err) }
	project.Hash = record.ProjectHash
	if _, err := SelectResolution(record, Resolution{ConflictID:record.ID,Action:ManualMerge,ManualFile:"manual.md"}, project, vault, &manual); !errors.Is(err,ledger.ErrReservedField) { t.Fatalf("identity err=%v",err) }
}

func entityBytes(id, projectID, body string) []byte {
	return []byte(fmt.Sprintf("---\nid: %s\nentity_type: decision\nproject_id: %s\nstatus: accepted\nsync_status: synced\n---\n\n## Context\n%s\n",id,projectID,body))
}
```

- [ ] **Step 2: Run focused tests to verify failure**

Run: `go test ./internal/sync -run 'Test(Conflict|Resolve)' -v`

Expected: FAIL with undefined conflict contracts.

- [ ] **Step 3: Define exact conflict persistence and note layout**

```go
type ConflictKind string
const (
	ConflictUnits       ConflictKind = "units"
	ConflictArchiveEdit ConflictKind = "archive_vs_modify"
	ConflictReserved    ConflictKind = "reserved_field"
	ConflictMalformed   ConflictKind = "malformed"
	ConflictCollision   ConflictKind = "path_collision"
)
type ConflictRecord struct {
	Version                         int
	ID, EntityID, ProjectID         string
	Kind                            ConflictKind
	RelativePath, BasePath, ProjectPath, VaultPath string
	BaseHash, ProjectHash, VaultHash string
	Base, Project, Vault, Suggested []byte
	CreatedAt                       time.Time
}
func SelectResolution(ConflictRecord, Resolution, liveProject, liveVault Candidate, manual *ledger.Document) (ledger.Document,error)
```

Conflict ID is `conflict-<UTC YYYYMMDDTHHMMSSZ>-<safe-entity-id>-<first12 sha256(base|project|vault)>`. Both sides receive the identical note at `sync-conflicts/<id>.md`. Its frontmatter contains only IDs, kind, source hashes, `resolution_status: open`, creation time, and exact copy-paste commands. Its body has Base, Project, Obsidian, and Suggested Merge sections inside a dynamically sized backtick fence longer than every candidate fence. No candidate is interpreted as Markdown.

Before note creation, run `redact.Default().Text` over each candidate. Any finding changes the outcome to a content-free `sensitive_content` issue; do not persist the candidate to a base, queue, journal, log, or conflict note and do not copy it to the other side.

Malformed, path-collision, reserved-edit, and sensitive-content isolation also emits a mirrored content-free repair note named `sync-conflicts/repair-<UTC timestamp>-<safe entity-or-path-hash>.md`. It contains project ID, safe base entity ID when known, source side, source byte hash, stable issue code, and exact `sync status` plus `manual_merge` recovery commands; it never embeds the malformed/sensitive bytes or raw absolute path. Repair-note mirroring uses the same crash journal as conflicts, and unrelated entities continue.

For `accept_project` and `accept_obsidian`, read the candidate embedded in both identical notes and require its recorded hash to equal the live source candidate. For `manual_merge`, open `--file` as a regular file confined below either pinned Project or Vault root, parse it, require the base identity, and reject suspected secrets. Any live hash drift returns `ErrStaleConflict` and writes nothing.

Resolution writes the selected document with `sync_status: synced` to both entity paths, commits its base, and atomically changes both notes to `resolution_status: resolved`, `resolution_action`, `resolved_hash`, and `resolved_at`. It retains notes as recoverable history. Resolving an already resolved note to the same hash is idempotent; a different action is rejected.

- [ ] **Step 4: Run conflict tests and commit**

Run: `gofmt -w internal/sync && go test ./internal/sync -run 'Test(Conflict|Resolve)' -v`

Expected: PASS; same-field and archive conflicts preserve both edits; stale/manual-invalid/secret cases write nothing and expose no canary in errors.

```bash
git add internal/sync/conflict.go internal/sync/conflict_test.go
git commit -m "feat: persist and resolve sync conflicts"
```

---

### Task 6: Add Reusable Project Locks, Rooted Retrying Writes, and Crash Journals

**Files:**
- Modify: `internal/project/lock.go`
- Modify: `internal/project/lock_unix.go`
- Modify: `internal/project/lock_windows.go`
- Create: `internal/project/lock_test.go`
- Create: `internal/sync/writer.go`
- Create: `internal/sync/writer_test.go`
- Create: `internal/sync/transaction.go`
- Create: `internal/sync/transaction_test.go`

**Interfaces:**
- Consumes: pinned roots, relative paths, bytes, retry policy, and per-project data root.
- Produces: `project.AcquireProjectLock`, `sync.RootedWriter.Write`, and content-free durable transaction journals.

- [ ] **Step 1: Write failing process-lock, retry, and interruption tests**

```go
func TestRootedWriterRetriesSharingViolationThenSucceeds(t *testing.T) {
	clock := &fakeClock{}
	attempts := 0
	w := RootedWriter{Retry: RetryPolicy{Initial: 10*time.Millisecond, Max: 40*time.Millisecond, InlineAttempts: 4, QueueAttempts: 8}, Sleep: clock.Sleep,
		write: func(Side,string,[]byte,fs.FileMode) error { attempts++; if attempts < 4 { return ErrSharingViolation }; return nil }}
	if err := w.Write(context.Background(), SideVault, "decisions/d1.md", []byte("safe"), 0o644); err != nil { t.Fatal(err) }
	if attempts != 4 || !reflect.DeepEqual(clock.Sleeps, []time.Duration{10*time.Millisecond,20*time.Millisecond,40*time.Millisecond}) { t.Fatalf("attempts=%d sleeps=%v", attempts, clock.Sleeps) }
}

type fakeClock struct { Sleeps []time.Duration }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error { c.Sleeps=append(c.Sleeps,d); return nil }

func TestTransactionJournalRoundTripsEveryStage(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data); if err != nil { t.Fatal(err) }
	defer root.Close()
	store := TransactionStore{Root:root}
	for _, stage := range []TransactionStage{TxnPlanned, TxnProjectWritten, TxnVaultWritten, TxnBaseCommitted} {
		t.Run(string(stage), func(t *testing.T) {
			want := Transaction{Version:1,Kind:TxnEntitySync,EntityID:"decision-1",DesiredHash:hash("accepted"),ExpectedBaseHash:hash("base"),Stage:stage,UpdatedAt:fixedTime}
			if err := store.Save(want); err != nil { t.Fatal(err) }
			got, found, err := store.Load("decision-1"); if err != nil || !found || got != want { t.Fatalf("got=%+v found=%v err=%v",got,found,err) }
			if err := store.Remove("decision-1"); err != nil { t.Fatal(err) }
		})
	}
}
```

- [ ] **Step 2: Run focused tests to verify failure**

Run: `go test ./internal/project ./internal/sync -run 'Test(RootedWriter|Transaction|ProjectLock)' -v`

Expected: FAIL because reusable sync locks, writers, and journals are undefined.

- [ ] **Step 3: Export the existing advisory-lock mechanism without changing semantics**

```go
type ProjectLock struct { file *os.File }
func AcquireProjectLock(root *os.Root, name string, timeout time.Duration) (*ProjectLock, error)
func (lock *ProjectLock) Release() error
```

Keep the current stable-file open, POSIX `flock`, Windows `LockFileEx`, 10 ms poll, live-owner timeout, crash-release, `0600` mode, symlink/reparse rejection, and post-open identity checks. `Initialize` calls the exported API for `config.toml.lock`; sync calls it for `locks/sync.lock`. Add a subprocess test proving two processes cannot enter one project sync and a killed owner releases the OS lock without deleting the lock file.

- [ ] **Step 4: Implement rooted writes, retry classification, and journal schema**

```go
type RetryPolicy struct { Initial, Max time.Duration; InlineAttempts, QueueAttempts int }
func DefaultRetryPolicy() RetryPolicy { return RetryPolicy{Initial:100*time.Millisecond, Max:2*time.Second, InlineAttempts:5, QueueAttempts:8} }

type RootedWriter struct { Project, Vault *pathguard.Directory; Retry RetryPolicy; Sleep func(context.Context,time.Duration) error; write func(Side,string,[]byte,fs.FileMode) error }
func (w RootedWriter) Write(ctx context.Context, side Side, relative string, content []byte, perm fs.FileMode) error

type TransactionKind string
const (
	TxnEntitySync TransactionKind = "entity_sync"
	TxnConflictNote TransactionKind = "conflict_note"
	TxnResolution TransactionKind = "resolution"
)
type TransactionStage string
const (
	TxnPlanned        TransactionStage = "planned"
	TxnProjectWritten TransactionStage = "project_written"
	TxnVaultWritten   TransactionStage = "vault_written"
	TxnBaseCommitted  TransactionStage = "base_committed"
)
type Transaction struct { Version int; Kind TransactionKind; EntityID, DesiredHash, ExpectedBaseHash, FromPathKey, ToPathKey string; Stage TransactionStage; UpdatedAt time.Time }
type TransactionStore struct { Root *os.Root }
func (s TransactionStore) Save(Transaction) error
func (s TransactionStore) Load(entityID string) (Transaction,bool,error)
func (s TransactionStore) List() ([]Transaction,error)
func (s TransactionStore) Remove(entityID string) error
```

`RootedWriter` validates and opens each parent through its pinned root before every attempt, calls `atomicfile.WriteRoot`, and retries only Windows sharing/lock violations. Permission, path, redirect, disk-full, invalid-name, and context errors are not retried. The retry delay is `min(Initial*2^(attempt-1), Max)` and obeys context cancellation. A final transient failure is typed `ErrTransientWrite` for queueing.

Journals live at `transactions/<sha256(kind|entity-id)>.json`, mode `0600`, and contain no document bytes, raw path, title, error message, or candidate excerpt; rename recovery stores only normalized `FromPathKey` and `ToPathKey`. Create `planned` before the first side write; atomically advance after each verified side write and base commit; remove only after base and both side hashes agree and any verified old rename path is removed. The same staged protocol wraps mirrored conflict-note creation and resolution-note closure, so a crash cannot leave a permanently one-sided open/resolved note. Restart loads journals first and re-runs that entity from the still-durable Base/Project/Vault/conflict inputs. A corrupt journal is fatal for that project and is never overwritten.

- [ ] **Step 5: Run lock/write/crash tests and commit**

Run: `gofmt -w internal/project internal/sync && go test ./internal/project ./internal/sync -run 'Test(RootedWriter|Transaction|ProjectLock)' -v`

Expected: PASS; transient retry sequence is exact; every injected crash converges; path replacement cannot redirect a write; journals contain none of the canary candidate text.

```bash
git add internal/project/lock.go internal/project/lock_unix.go internal/project/lock_windows.go internal/project/lock_test.go internal/sync/writer.go internal/sync/writer_test.go internal/sync/transaction.go internal/sync/transaction_test.go
git commit -m "feat: harden sync writes and crash recovery"
```

---

### Task 7: Add a Content-Free Durable Queue and Watcher Event Gate

**Files:**
- Create: `internal/sync/queue.go`
- Create: `internal/sync/queue_test.go`
- Create: `internal/sync/events.go`
- Create: `internal/sync/events_test.go`

**Interfaces:**
- Consumes: transiently failed entity IDs, target sides, base hashes, filesystem event hashes, and time.
- Produces: `Queue.Enqueue/Ready/Ack/Reschedule`, `Engine.Observe`, and `Engine.Ready` contracts used by CLI reconciliation and the later watcher.

- [ ] **Step 1: Write failing queue restart and event-gate tests**

```go
func TestQueueIsDurableDeduplicatedAndContentFree(t *testing.T) {
	data := t.TempDir()
	root, err := os.OpenRoot(data); if err != nil { t.Fatal(err) }
	defer root.Close()
	q := Queue{Root:root,Retry:DefaultRetryPolicy(),Now:func()time.Time{return fixedTime}}
	item := QueueItem{Version:1, EntityID:"decision-1", Target:SideVault, ExpectedBaseHash:hash("base"), CreatedAt:fixedTime, UpdatedAt:fixedTime}
	first, err := q.Enqueue(item); if err != nil { t.Fatal(err) }
	second, err := q.Enqueue(item); if err != nil { t.Fatal(err) }
	if first.ID != second.ID { t.Fatalf("not deduped: %q %q", first.ID, second.ID) }
	b, err := os.ReadFile(filepath.Join(data,queueRecordPath(first.ID))); if err != nil { t.Fatal(err) }
	if bytes.Contains(b, []byte("CANARY-CONTENT")) || bytes.Contains(b, []byte("decisions/")) { t.Fatalf("queue leaked content/path: %s", b) }
}

func TestEventGateSuppressesSelfLoopAndDebouncesRapidSaves(t *testing.T) {
	lookup := &fakeHashLookup{entities:map[string]string{"decisions/d1.md":"decision-1"}, hashes:map[string]string{"decision-1|project":"written-hash"}}
	gate := NewEventGate(200*time.Millisecond, lookup)
	if got, _ := gate.Observe(FileEvent{Side:SideProject, RelativePath:"decisions/d1.md", ObservedHash:"written-hash", At:t0}); got != EventIgnoredSelf { t.Fatalf("got=%s",got) }
	if got, _ := gate.Observe(FileEvent{Side:SideVault, RelativePath:"decisions/d1.md", ObservedHash:"edit-1", At:t0}); got != EventDebounced { t.Fatalf("got=%s",got) }
	gate.Observe(FileEvent{Side:SideVault, RelativePath:"decisions/d1.md", ObservedHash:"edit-2", At:t0.Add(100*time.Millisecond)})
	if ready := gate.Ready(t0.Add(299*time.Millisecond)); len(ready) != 0 { t.Fatalf("early=%v",ready) }
	if ready := gate.Ready(t0.Add(300*time.Millisecond)); !reflect.DeepEqual(ready, []string{"decision-1"}) { t.Fatalf("ready=%v",ready) }
}

type fakeHashLookup struct { entities, hashes map[string]string }
func (f *fakeHashLookup) EntityForPath(_ Side, path string) (string,bool,error) { value,ok:=f.entities[path]; return value,ok,nil }
func (f *fakeHashLookup) LastWrittenHash(entity string, side Side) (string,bool,error) { value,ok:=f.hashes[entity+"|"+string(side)]; return value,ok,nil }
```

- [ ] **Step 2: Run focused tests to verify failure**

Run: `go test ./internal/sync -run 'Test(Queue|EventGate)' -v`

Expected: FAIL with undefined queue and event APIs.

- [ ] **Step 3: Implement exact queue state and backoff**

```go
type QueueState string
const ( QueuePending QueueState = "pending"; QueueBlocked QueueState = "blocked" )
type QueueItem struct {
	Version int `json:"version"`; ID, EntityID string; Target Side
	ExpectedBaseHash string; Attempts int; NotBefore time.Time; State QueueState
	LastErrorClass string; CreatedAt, UpdatedAt time.Time
}
type Queue struct { Root *os.Root; Retry RetryPolicy; Now func() time.Time }
func (q Queue) Enqueue(QueueItem) (QueueItem,error)
func (q Queue) Ready(now time.Time, limit int) ([]QueueItem,error)
func (q Queue) Ack(id string) error
func (q Queue) Reschedule(id, errorClass string) (QueueItem,error)
func queueRecordPath(id string) string
```

The deterministic queue ID is the first 32 hex characters of SHA-256 over `entityID|target|expectedBaseHash`; one logical failure has one item across restarts. `Ready` sorts by `NotBefore`, `CreatedAt`, then ID. `Reschedule` increments attempts and uses the same exponential policy; after `QueueAttempts` it retains a visible `blocked` item. Queue files contain only the declared fields, use atomic CAS under the project lock, and reject unknown/trailing JSON or invalid IDs. `Ack` is idempotent.

- [ ] **Step 4: Implement self-loop and debounce interfaces**

```go
type EventDisposition string
const ( EventIgnoredSelf EventDisposition = "ignored_self"; EventDebounced EventDisposition = "debounced"; EventReady EventDisposition = "ready" )
type FileEvent struct { Side Side; RelativePath, ObservedHash string; At time.Time }
type HashLookup interface { EntityForPath(Side,string)(string,bool,error); LastWrittenHash(string,Side)(string,bool,error) }
type EventGate struct { /* mutex, window, lookup, pending by entity */ }
func NewEventGate(window time.Duration, hashes HashLookup) *EventGate
func (g *EventGate) Observe(FileEvent) (EventDisposition,error)
func (g *EventGate) Ready(now time.Time) []string
```

`Observe` normalizes the path with the mapping path key before lookup, ignores conflict notes and atomic temporary/backup names, suppresses an event only when its observed byte hash equals the last verified side hash in `BaseRecord`, and resets the entity deadline on every human hash. `Ready` returns sorted stable entity IDs and removes only ready entries. The in-memory debounce is intentionally reconstructible; periodic reconciliation covers process crashes and missed events.

- [ ] **Step 5: Run queue/event tests and commit**

Run: `gofmt -w internal/sync && go test ./internal/sync -run 'Test(Queue|EventGate)' -count=50`

Expected: PASS 50 runs; queue restart ordering is stable, backoff is bounded, blocked status remains visible, self writes do not loop, and rapid Unicode/case-equivalent saves coalesce once.

```bash
git add internal/sync/queue.go internal/sync/queue_test.go internal/sync/events.go internal/sync/events_test.go
git commit -m "feat: queue and coalesce sync work"
```

---

### Task 8: Orchestrate Locked Reconciliation, Recovery, and Unrelated Progress

**Files:**
- Create: `internal/sync/service.go`
- Create: `internal/sync/service_test.go`

**Interfaces:**
- Consumes: configured roots/project identity, full or selected reconciliation request, bases, queue, and conflict resolution.
- Produces: the final public `sync.Engine` API consumed by CLI and later watcher plans.

- [ ] **Step 1: Write failing service-level behavior tests**

```go
func TestReconcileConflictDoesNotBlockUnrelatedEntity(t *testing.T) {
	fx := newServiceFixture(t)
	fx.seedConflict("decision-1")
	fx.seedProjectOnlyEdit("decision-2")
	report, err := fx.engine.Reconcile(context.Background(), ReconcileRequest{Trigger:TriggerCLI})
	if err != nil { t.Fatal(err) }
	if len(report.Conflicts) != 1 || !fx.vaultContains("decision-2", "PROJECT-ONLY") { t.Fatalf("report=%+v",report) }
	if !fx.projectContains("decision-1", "PROJECT-CONFLICT") || !fx.vaultContains("decision-1", "VAULT-CONFLICT") { t.Fatal("candidate was overwritten") }
}

func TestDryRunHasNoFilesystemOrStateSideEffects(t *testing.T) {
	fx := newServiceFixture(t); fx.seedProjectOnlyEdit("decision-1")
	before := fx.snapshotAllBytesAndMetadata()
	report, err := fx.engine.Reconcile(context.Background(), ReconcileRequest{DryRun:true, Trigger:TriggerCLI})
	if err != nil { t.Fatal(err) }
	after := fx.snapshotAllBytesAndMetadata()
	if !reflect.DeepEqual(before, after) { t.Fatalf("dry-run mutation: before=%+v after=%+v", before, after) }
	if len(report.Operations) != 1 || report.Operations[0].Kind != OperationUpdateVault { t.Fatalf("report=%+v", report) }
}
```

`serviceFixture.snapshotAllBytesAndMetadata` returns a sorted slice of `{Path, Mode, Size, ModTime, SHA256}` for Project, Vault, and per-project data roots. `newServiceFixture` creates those physical roots, writes a valid `project-overview.md`, initializes the stable mapping, constructs `Engine`, and registers `t.Cleanup(engine.Close)`. Its `seedSynced`, `seedConflict`, `seedProjectOnlyEdit`, `seedVaultOnlyEdit`, `projectContains`, and `vaultContains` methods write/read complete decision entities through root-confined fixture helpers; none bypasses production sync writes after `Engine` construction.

- [ ] **Step 2: Run service tests to verify failure**

Run: `go test ./internal/sync -run 'Test(Reconcile|DryRun|VaultUnavailable|RestartRecovery)' -v`

Expected: FAIL because `Engine` and reports are undefined.

- [ ] **Step 3: Define the exact service API**

```go
type Trigger string
const ( TriggerCLI Trigger = "cli"; TriggerWatcher Trigger = "watcher"; TriggerPeriodic Trigger = "periodic"; TriggerQueue Trigger = "queue" )
type Options struct {
	ProjectRoot, VaultRoot, VaultReviewPath, DataRoot, ProjectID, GOOS string
	VaultCaseMode platform.CaseMode; Retry RetryPolicy; Debounce time.Duration; Now func() time.Time
}
type ReconcileRequest struct { DryRun bool; EntityIDs []string; Trigger Trigger }
type OperationKind string
const (
	OperationAddProject OperationKind = "add_project"; OperationAddVault OperationKind = "add_vault"
	OperationUpdateProject OperationKind = "update_project"; OperationUpdateVault OperationKind = "update_vault"
	OperationArchive OperationKind = "archive"; OperationRestore OperationKind = "restore"; OperationRename OperationKind = "rename"
	OperationConflict OperationKind = "conflict"; OperationQueue OperationKind = "queue"
)
type Operation struct { EntityID string; Kind OperationKind; Target Side; RelativePath, BeforeHash, AfterHash string }
type EntityError struct { EntityID, Code string }
type Report struct { ProjectID string; DryRun bool; Operations []Operation; Conflicts []string; Issues []ledger.ScanIssue; Errors []EntityError; QueueDepth int }
type Status struct { ProjectID string; InSync, Conflicted, Malformed, Queued, Blocked int; OpenConflicts []string; Pending []Operation }
type QueueReport struct { Attempted, Completed, Rescheduled, Blocked int }
type Engine struct { /* pinned roots, stores, lock root, writer, gate */ }
func NewEngine(Options) (*Engine,error)
func (e *Engine) Reconcile(context.Context,ReconcileRequest)(Report,error)
func (e *Engine) Status(context.Context)(Status,error)
func (e *Engine) Resolve(context.Context,Resolution)(Report,error)
func (e *Engine) DrainQueue(context.Context,int)(QueueReport,error)
func (e *Engine) Observe(FileEvent)(EventDisposition,error)
func (e *Engine) Ready(time.Time) []string
func (e *Engine) Close() error
```

- [ ] **Step 4: Implement the locked per-entity transaction order**

`NewEngine` pins Project and Data roots. It verifies configured project identity from `project-overview.md`. It attempts to pin Vault; unavailability is a typed state, not construction failure. It rejects project/vault nesting and redirect roots again. It does not create directories until a non-dry operation holds the lock.

`Reconcile` acquires `locks/sync.lock`, loads and validates every journal/base/queue record, scans both inventories, unions stable IDs from Base/Project/Vault, sorts IDs, optionally intersects `EntityIDs`, and processes each independently:

1. classify scan issues and keep malformed/colliding source bytes untouched;
2. load Base and validate present candidates against its reserved identity;
3. run the suspected-secret gate before any persistent derived content;
4. call pure `Merge` and append the planned operation;
5. for dry-run, stop for that entity without creating any state;
6. on conflict, atomically mirror the note, mark the original entity `sync_status: conflicted` on both sides while retaining its last non-conflicting body, and leave Base unchanged;
7. on accepted change, write a content-free `planned` journal, write/verify Project, advance journal, write/verify Vault, advance journal, commit Base with both last-written hashes, advance/remove journal;
8. on final transient vault write failure, enqueue the entity and leave Base unchanged;
9. record an entity-scoped safe error and continue to the next entity.

Only failure to establish the project lock, corrupt machine state, changed root identity, invalid project identity, or context cancellation aborts the whole run. A single merge conflict, malformed file, permission error, or unavailable Vault does not block unrelated entities. If Vault is unavailable, queue changed entity IDs without modifying Project candidates or Base.

`Status` performs the same read-only scan and merge planning under the lock but creates no files or queue entries. `DrainQueue` selects ready items, rejects an item when its expected base hash is stale by acknowledging it and scheduling a fresh entity reconciliation, and reschedules only typed transient failures. Startup recovery reconciles journals before normal queued/new work.

- [ ] **Step 5: Run service recovery tests and commit**

Run: `gofmt -w internal/sync && go test ./internal/sync -run 'Test(Reconcile|DryRun|VaultUnavailable|RestartRecovery|Status|DrainQueue)' -v`

Expected: PASS; one conflict and one malformed entity do not block a third entity; every crash stage recovers; dry-run snapshots are identical; missing files restore; Vault outage queues; repeated unchanged run has zero operations and zero byte changes.

```bash
git add internal/sync/service.go internal/sync/service_test.go
git commit -m "feat: orchestrate deterministic reconciliation"
```

---

### Task 9: Expose `sync`, `sync status`, and `sync resolve` CLI Commands

**Files:**
- Create: `internal/cli/sync.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: current working directory, config/data root, dry-run/status/resolve flags, and standard streams.
- Produces: stable human and JSON output, exit codes, and calls to `sync.Engine`; no semantic or Git command.

- [ ] **Step 1: Write failing CLI contract tests**

```go
func TestRunSyncDryRunPrintsPlanAndChangesNothing(t *testing.T) {
	fx := newCLISyncFixture(t)
	before := fx.snapshot()
	code, stdout, stderr := fx.run("sync", "--dry-run", "--cwd", fx.project, "--data-dir", fx.data)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "update_vault decision-1") { t.Fatalf("code=%d stdout=%q stderr=%q",code,stdout,stderr) }
	if !reflect.DeepEqual(before, fx.snapshot()) { t.Fatal("dry-run changed files") }
}

func TestRunSyncResolveRequiresExactAction(t *testing.T) {
	for _, action := range []string{"", "project", "delete", "ACCEPT_PROJECT"} {
		var out, errOut bytes.Buffer
		code := Run([]string{"sync","resolve","--conflict","conflict-1","--action",action}, &out, &errOut)
		if code != 2 || out.Len() != 0 { t.Fatalf("action=%q code=%d",action,code) }
	}
}
```

- [ ] **Step 2: Run CLI tests to verify failure**

Run: `go test ./internal/cli -run 'TestRunSync' -v`

Expected: FAIL because `sync` routes as unknown.

- [ ] **Step 3: Implement exact command grammar and safe output**

```text
session-reviewer sync [--dry-run] [--cwd <project>] [--data-dir <data>]
session-reviewer sync status [--json] [--cwd <project>] [--data-dir <data>]
session-reviewer sync resolve --conflict <id> --action accept_project|accept_obsidian [--cwd <project>] [--data-dir <data>]
session-reviewer sync resolve --conflict <id> --action manual_merge --file <path> [--cwd <project>] [--data-dir <data>]
```

All modes load the pinned config snapshot, resolve exactly one physical project mapping with the existing conservative mapping logic, require its stable vault mapping fields, and pass them to `sync.NewEngine`. `--dry-run` is valid only on the root sync command; `--json` only on status; `--file` is required only for `manual_merge`; positional extras are usage errors.

Exit codes are `0` for completed sync/dry-run/status/resolution including visible conflicts, `1` for runtime/root/state/lock failure or blocked requested resolution, and `2` for invalid flags. Human output is one sorted line per operation plus final counts. JSON status uses `json.Encoder` over `sync.Status` and ends in one newline. Errors print only stable codes and recovery guidance, never absolute roots, candidate content, raw OS errors, or entity narrative.

Add this route:

```go
case "sync":
	return runSync(args[1:], stdout, stderr)
```

- [ ] **Step 4: Run CLI tests and commit**

Run: `gofmt -w internal/cli && go test ./internal/cli -run 'TestRunSync' -v`

Expected: PASS for dry-run, normal sync, JSON status, each resolution action, stale conflict, unavailable vault, extra arguments, and canary-safe stderr.

```bash
git add internal/cli/sync.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: expose sync status and resolution CLI"
```

---

### Task 10: Verify Cross-Platform Security, Unicode, Crash, and End-to-End Acceptance

**Files:**
- Create: `internal/sync/acceptance_test.go`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the public CLI/engine contracts and sanitized fixtures.
- Produces: release-blocking automated evidence for deterministic sync semantics on supported macOS and Windows targets.

- [ ] **Step 1: Add failing end-to-end acceptance tests**

Start with this complete happy-path test using the concrete `serviceFixture` established in Task 8:

```go
func TestSyncAcceptanceObsidianEditReachesProject(t *testing.T) {
	fx := newServiceFixture(t)
	fx.seedVaultOnlyEdit("decision-1")
	first, err := fx.engine.Reconcile(context.Background(), ReconcileRequest{Trigger:TriggerCLI})
	if err != nil { t.Fatal(err) }
	if !fx.projectContains("decision-1", "VAULT-ONLY") || len(first.Conflicts) != 0 { t.Fatalf("first=%+v",first) }
	before := fx.snapshotAllBytesAndMetadata()
	second, err := fx.engine.Reconcile(context.Background(), ReconcileRequest{Trigger:TriggerCLI})
	if err != nil { t.Fatal(err) }
	if len(second.Operations) != 0 || !reflect.DeepEqual(before,fx.snapshotAllBytesAndMetadata()) { t.Fatalf("second=%+v",second) }
}
```

Add one test per remaining row; each row is an exact setup/action/assertion contract rather than a shared assertion shortcut:

| Test name | Setup and action | Required assertion |
|---|---|---|
| `TestSyncAcceptanceProjectEditReachesVault` | `seedProjectOnlyEdit("decision-1")`, then CLI-trigger reconcile | Vault contains `PROJECT-ONLY`, Base hash equals both side hashes, no conflict |
| `TestSyncAcceptanceDisjointEditsMergeAndRepeatIsNoop` | change frontmatter `tags` in Project and `## Context` in Vault | first run contains both edits on both sides; second run has zero operations and an identical byte/metadata snapshot |
| `TestSyncAcceptanceConflictKeepsBothAndUnrelatedProgresses` | `seedConflict("decision-1")` plus `seedProjectOnlyEdit("decision-2")` | candidate 1 bytes appear in mirrored note, entity 1 bodies become last Base with `sync_status: conflicted`, entity 2 reaches Vault |
| `TestSyncAcceptanceArchiveVersusEditRequiresResolution` | Project changes `status` to `archived`; Vault changes `## Rationale` | Base does not advance; both edits occur in mirrored conflict; `accept_project` resolves both to archived content |
| `TestSyncAcceptanceMissingFileIsRestoredNotDeleted` | remove the Vault file after establishing Base | sync recreates the exact file and does not report archive |
| `TestSyncAcceptanceCrashAtEveryStageRecoversFromDurableBase` | inject process-style failure after each `TransactionStage`, close engine, construct a new engine, reconcile | each restart converges to both accepted hashes, advances one Base, removes journal, loses no edit |
| `TestSyncAcceptanceUnavailableVaultQueuesAndLaterDrains` | rename the pinned vault mount away before sync, restore it, advance fake clock, call `DrainQueue` | first run retains Project and queues one content-free item; drain writes Vault, advances Base, and acknowledges item |
| `TestSyncAcceptanceDryRunIsCompletelyReadOnly` | seed Project edit and snapshot all roots | dry-run reports `OperationUpdateVault`; every path/mode/size/mtime/hash remains identical |
| `TestSyncAcceptanceCursorAndGitStateUntouched` | seed a valid accepted cursor and a dirty Git fixture, snapshot cursor bytes/metadata and `git status --porcelain=v1` | normal sync changes only ledger/vault/machine sync state; cursor snapshot and Git status output are byte-identical; a fake `git` executable on `PATH` records zero invocations |
| `TestSyncAcceptanceUnicodeAndCasePolicyMatchesConfiguredVolume` | run in-memory sensitive/insensitive inventories with NFC and decomposed names, then native Unicode temp-root sync | insensitive policy isolates collision; sensitive policy keeps distinct paths; native sync reaches the intended entity only |
| `TestSyncAcceptanceRedirectCannotEscapeEitherRoot` | replace each existing parent boundary in turn with symlink on POSIX and reparse adapter on Windows | sync returns safe issue and outside sentinel bytes/metadata remain unchanged |
| `TestSyncAcceptanceCanariesNeverPropagateToStateOrConflict` | edit one Vault source with all six canary classes and create a would-be Project conflict | source stays untouched; other side and every derived/state/output location contain none of the original canaries |
| `TestSyncAcceptancePerProjectCrossProcessLockSerializes` | helper process holds `locks/sync.lock`; second process runs sync; kill first; rerun | second times out without writes; after owner death the rerun succeeds using the same regular lock file |

The canary test uses a bearer token, cookie, database URL, private key, named secret, and high-entropy token; after a blocked sync it recursively reads Project, Vault excluding the original deliberately edited source file, merge-bases, queue, transactions, conflicts, stdout, and stderr and asserts no original canary occurs.

- [ ] **Step 2: Run acceptance tests and verify the suite exposes any remaining gaps**

Run: `go test ./internal/sync -run 'TestSyncAcceptance' -v`

Expected: FAIL until all injected platform adapters and recovery hooks satisfy the acceptance suite; fix only sync milestone code named in Tasks 1–9, then rerun.

- [ ] **Step 3: Extend the existing CI matrix without weakening foundation gates**

Keep all current jobs and add exact commands to every macOS and Windows test job:

```yaml
- name: Test deterministic synchronization
  run: go test ./internal/sync ./internal/ledger ./internal/pathguard ./internal/project ./internal/cli -count=1
- name: Test race safety
  run: go test -race ./internal/sync ./internal/project -count=1
```

The macOS matrix must include Intel x64 and Apple Silicon arm64 runners already required by the repository; the Windows matrix is x86-64. Platform-native tests must exercise actual advisory locks and atomic replacement. Pure Windows sharing-violation/reparse logic tests may run on all hosts, but they do not substitute for the Windows job.

- [ ] **Step 4: Run all local gates and both cross-builds**

Run:

```bash
go test ./... -count=1
go test -race ./internal/sync ./internal/project -count=1
go vet ./...
go build -o ./bin/session-reviewer ./cmd/session-reviewer
GOOS=windows GOARCH=amd64 go build -o ./bin/session-reviewer.exe ./cmd/session-reviewer
```

Expected: all tests and vet pass; both binaries build; no real local session or vault content is added to the repository. A release still requires observing both native CI jobs green; a local cross-build alone is not Windows acceptance.

- [ ] **Step 5: Commit acceptance gates**

```bash
git add internal/sync/acceptance_test.go .github/workflows/ci.yml
git commit -m "test: gate cross-platform sync acceptance"
```

---

### Task 11: Document User Semantics and Run the Final Determinism Gate

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: completed command behavior and failure codes.
- Produces: exact operator guidance for mapping, dry-run, status, archive, conflict resolution, queue recovery, and watcher-disabled use.

- [ ] **Step 1: Add documentation assertions to the acceptance suite**

```go
func TestSyncREADMEContainsRecoveryContracts(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md")); if err != nil { t.Fatal(err) }
	for _, want := range []string{"session-reviewer sync --dry-run", "session-reviewer sync status --json", "accept_project", "accept_obsidian", "manual_merge", "status: archived", "missing file is not deletion", "does not run Git", "does not call a model"} {
		if !bytes.Contains(b, []byte(want)) { t.Fatalf("README missing %q", want) }
	}
}
```

- [ ] **Step 2: Run the documentation test to verify failure**

Run: `go test ./internal/sync -run TestSyncREADMEContainsRecoveryContracts -v`

Expected: FAIL listing the first absent contract.

- [ ] **Step 3: Document the exact workflow**

Add sections that show:

```bash
session-reviewer sync --dry-run
session-reviewer sync
session-reviewer sync status --json
session-reviewer sync resolve --conflict <conflict-id> --action accept_project
session-reviewer sync resolve --conflict <conflict-id> --action accept_obsidian
session-reviewer sync resolve --conflict <conflict-id> --action manual_merge --file ./docs/session-review/sync-conflicts/manual-merge.md
```

State that the stable mapped directory is printed by `init`, title edits do not rename identity paths, unknown YAML keys/Markdown sections survive, `status: archived` is logical deletion, a missing file is restored rather than deleted, conflict notes remain resolved history, `status --json` exposes queued/blocked work, rerunning sync drains ready work, and the full manual workflow works with the watcher disabled. State explicitly that sync does not call a model and does not run Git commands.

- [ ] **Step 4: Run the final unchanged-input and repository gates**

Run:

```bash
gofmt -w internal cmd
go test ./... -count=1
go test -race ./internal/sync ./internal/project -count=1
go vet ./...
git diff --check
```

Then run the sanitized end-to-end fixture twice and compare snapshots:

```bash
go test ./internal/sync -run 'TestSyncAcceptance(DisjointEditsMergeAndRepeatIsNoop|ConflictKeepsBothAndUnrelatedProgresses|CrashAtEveryStageRecoversFromDurableBase)' -count=20
```

Expected: every command passes; twenty repetitions are stable; the second unchanged sync reports zero operations and does not change content hashes or modification times.

- [ ] **Step 5: Commit documentation**

```bash
git add README.md
git commit -m "docs: explain deterministic sync recovery"
```

## Final Acceptance Checklist

- [ ] Stable entity IDs and persisted vault mapping survive title and project-directory renames.
- [ ] NFC/decomposed Unicode and configured case behavior cannot select the wrong Obsidian file.
- [ ] Unknown frontmatter keys, comments, scalar styles, preamble, fenced content, and unknown sections survive a merge.
- [ ] Every editable field/section combination follows the documented unit matrix.
- [ ] Reserved identity/hash edits are isolated and reported without echoing unsafe values.
- [ ] Missing files are restored; only `status: archived` represents logical deletion; no physical deletion is inferred.
- [ ] Conflict notes exist on both sides, retain Base/Project/Obsidian candidates, offer all three actions, and remain as resolved history.
- [ ] One conflict, malformed entity, queue failure, or Vault outage does not block unrelated safe entities.
- [ ] Dry-run changes no file, directory, base, queue, journal, config, conflict, hash, or mtime.
- [ ] Per-project cross-process advisory locks serialize sync/status/resolve transactions.
- [ ] Every write is rooted, redirect-safe, atomic, retried only for transient sharing violations, and verified before base advancement.
- [ ] Journals recover every crash boundary and contain no content; queue entries are durable, deduplicated, bounded, and content-free.
- [ ] Self-written hashes suppress watcher loops; human rapid saves debounce into one entity reconcile; periodic full reconciliation remains possible.
- [ ] Suspected secrets are not copied to the other side or persisted in merge bases, queues, journals, conflicts, or output.
- [ ] `sync`, `sync --dry-run`, `sync status [--json]`, and every `sync resolve` action have deterministic output and safe exit codes.
- [ ] No code path invokes a semantic model, executes Markdown, accesses the network, or mutates Git.
- [ ] macOS Intel/Apple Silicon and Windows x64 native CI pass; local cross-builds alone are not claimed as Windows acceptance.
