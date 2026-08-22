# SessionReviewer Foundation Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the deterministic foundation's identity, discovery, initialization, diagnostics, and Windows durability gaps before proposal application is built on top of it.

**Architecture:** Keep the Go CLI deterministic and dependency-light, but make discovery inputs explicit and typed: platform policy resolves roots and runtime session identifiers, session discovery records per-file health, and initialization separates a read-only preview from mutation. Failures cross the CLI boundary as stable safe codes, while internal causes remain available to tests; Windows replacement uses the operating system replacement primitive for an existing destination instead of describing a multi-rename sequence as atomic.

**Tech Stack:** Go 1.26, Go standard library, `github.com/pelletier/go-toml/v2 v2.4.3`, GitHub Actions on native macOS and Windows runners.

## Global Constraints

- Target macOS 13 or later on Apple Silicon and Intel.
- Target Windows 10 22H2 or later and Windows 11 on x86-64; Windows ARM is not a first-release acceptance requirement.
- Installation and runtime must not require administrator privileges.
- The standalone Go CLI must make no model or OpenAI API calls and must not claim to create semantic conclusions.
- No code path may automatically run `git commit`, `git push`, `git reset`, `git checkout`, `git switch`, `git restore`, or mutate the Git index, branch, refs, or worktree.
- Raw Codex session files remain local and are opened read-only.
- Hidden reasoning, system/developer messages, and encrypted or opaque compaction payloads remain excluded.
- Redaction happens before evidence, ledger, cursor, receipt, or diagnostic persistence.
- Explicit `--sessions-root` wins over environment discovery; environment discovery wins over Codex home; Codex home wins over the conventional user-profile path.
- An explicitly selected session must not be blocked by an unrelated corrupt JSONL file, but corruption in the selected candidate or any duplicate candidate for that ID fails closed.
- All persistent writes stay beneath an already validated project or machine-data root and use the platform replacement adapter.
- Preserve the existing evidence packet schema and prepare cursor semantics in this plan; proposal cursor-boundary additions belong to the ledger/Skill plan.
- Do not add synchronization, watcher, SQLite, Mermaid, proposal application, or semantic Skill behavior in this plan.

## File Structure

```text
internal/config/config.go                 Enforce globally unique configured project IDs
internal/config/config_test.go            Duplicate-ID corruption regression tests
internal/project/init.go                  Read-only initialization preview and write execution
internal/project/init_test.go             Preview/no-write and copied-overview identity tests
internal/cli/init.go                      Preview-first `init` contract and explicit `--write`
internal/platform/paths.go                Session-root and runtime-session precedence policy
internal/platform/paths_test.go           macOS/Windows precedence table tests
internal/session/locator.go               Per-candidate discovery health and targeted selection
internal/session/locator_test.go          Selected/unrelated/duplicate corruption matrix
internal/prepare/prepare.go               Consume targeted discovery without changing extraction
internal/prepare/prepare_test.go           End-to-end targeted-selection failure policy
internal/cli/diagnostic.go                Stable user-safe errors and recovery hints
internal/cli/run.go                       Complete root help and command routing
internal/cli/prepare.go                   Root/session discovery and actionable safe failures
internal/cli/run_test.go                  Help, precedence, identifiers, and non-leakage tests
internal/atomicfile/replace_windows.go     Native `ReplaceFileW`/`MoveFileExW` adapter
internal/atomicfile/replace_windows_logic.go Injectable Windows replacement state machine
internal/atomicfile/replace_windows_test.go Native replacement and interrupted-state tests
.github/workflows/ci.yml                  Native Windows replacement gate
README.md                                 Truthful preview, precedence, identifier, and durability docs
docs/superpowers/plans/2026-08-22-session-reviewer-foundation.md
                                          Reconciled completed checklist and verification record
```

---

### Task 1: Reject a Project ID Claimed by More Than One Root

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/project/init.go`
- Modify: `internal/project/init_test.go`

**Interfaces:**
- Consumes: `config.Config{Version int, Projects []config.ProjectMapping}` and an overview `project_id` read under the project root.
- Produces: `func (c Config) ValidateProjectIDs() error`; `func (c Config) ProjectByID(id string) (ProjectMapping, bool)`; initialization fails before writing when an ID belongs to another configured root.

- [ ] **Step 1: Write the failing config and copied-overview tests**

```go
// internal/config/config_test.go
func TestConfigRejectsDuplicateProjectIDAcrossRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	err := Save(path, Config{Version: 1, Projects: []ProjectMapping{
		{ID: "project-1111111111111111", Root: "/work/one", VaultRoot: "/vault/one"},
		{ID: "project-1111111111111111", Root: "/work/two", VaultRoot: "/vault/two"},
	}})
	if err == nil || !strings.Contains(err.Error(), "project ID is mapped more than once") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadRejectsDuplicateProjectIDAcrossRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "version = 1\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/one'\nvault_root = '/v1'\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/two'\nvault_root = '/v2'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil { t.Fatal(err) }
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "configuration state and recovery backup are invalid") {
		t.Fatalf("err=%v", err)
	}
}

// internal/project/init_test.go
func TestInitializeRejectsOverviewIDClaimedByAnotherRoot(t *testing.T) {
	first, second, firstVault, secondVault, data := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	wantID := "project-1111111111111111"
	writeTestOverview(t, second, wantID)
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: wantID, Root: first, VaultRoot: firstVault,
	}}}); err != nil { t.Fatal(err) }
	_, err := Initialize(InitOptions{ProjectRoot: second, VaultRoot: secondVault, DataDir: data})
	if err == nil || !strings.Contains(err.Error(), "already belongs to another project root") {
		t.Fatalf("err=%v", err)
	}
	if got, _ := config.Load(filepath.Join(data, "config.toml")); len(got.Projects) != 1 {
		t.Fatalf("projects=%+v", got.Projects)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/config ./internal/project -run 'Test(ConfigRejectsDuplicateProjectIDAcrossRoots|LoadRejectsDuplicateProjectIDAcrossRoots|InitializeRejectsOverviewIDClaimedByAnotherRoot)$' -count=1`

Expected: FAIL because duplicate IDs are currently accepted and an unmapped copied overview is appended.

- [ ] **Step 3: Add strict ID indexing and guard overview recovery**

```go
// internal/config/config.go
func (c Config) ValidateProjectIDs() error {
	seen := make(map[string]string, len(c.Projects))
	for _, project := range c.Projects {
		if project.ID == "" {
			return errors.New("configured project ID is empty")
		}
		if firstRoot, found := seen[project.ID]; found {
			return fmt.Errorf("project ID is mapped more than once: %q and %q", firstRoot, project.Root)
		}
		seen[project.ID] = project.Root
	}
	return nil
}

func (c Config) ProjectByID(id string) (ProjectMapping, bool) {
	for _, project := range c.Projects {
		if project.ID == id { return project, true }
	}
	return ProjectMapping{}, false
}

func validate(cfg Config) error {
	if cfg.Version != 1 { return errors.New("unsupported config version") }
	return cfg.ValidateProjectIDs()
}

// internal/project/init.go, immediately before appending an overview-only mapping
if overviewExists {
	if owner, claimed := cfg.ProjectByID(overviewID); claimed {
		return InitResult{}, fmt.Errorf("project ID %q already belongs to another project root %q", overviewID, owner.Root)
	}
	cfg.Projects = append(cfg.Projects, config.ProjectMapping{ID: overviewID, Root: root, VaultRoot: vault})
	// retain the existing guarded SaveRoot path and return value
}
```

- [ ] **Step 4: Run the package suites and verify GREEN**

Run: `gofmt -w internal/config/config.go internal/config/config_test.go internal/project/init.go internal/project/init_test.go && go test ./internal/config ./internal/project -count=1`

Expected: PASS; both load and save reject duplicate configured IDs, and initialization leaves the existing mapping unchanged.

- [ ] **Step 5: Commit the identity invariant**

```bash
git add internal/config/config.go internal/config/config_test.go internal/project/init.go internal/project/init_test.go
git commit -m "fix: reject duplicate project identities"
```

### Task 2: Make Initialization Preview-First and Side-Effect Free

**Files:**
- Modify: `internal/project/init.go`
- Modify: `internal/project/init_test.go`
- Modify: `internal/cli/init.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: existing `project.InitOptions`.
- Produces: `project.InitPreview{ProjectID, ProjectRoot, VaultRoot, LedgerRoot, ConfigPath, Action string}`; `project.PreviewInitialization(opts InitOptions) (InitPreview, error)`; `init` previews by default and writes only with `--write`.

- [ ] **Step 1: Write failing no-write and CLI contract tests**

```go
// internal/project/init_test.go
func TestPreviewInitializationDoesNotCreateDataOrLedger(t *testing.T) {
	base := t.TempDir()
	root, vault, data := filepath.Join(base, "project"), filepath.Join(base, "vault"), filepath.Join(base, "machine")
	for _, path := range []string{root, vault} { if err := os.Mkdir(path, 0o755); err != nil { t.Fatal(err) } }
	preview, err := PreviewInitialization(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data})
	if err != nil { t.Fatal(err) }
	if preview.Action != "create" || preview.LedgerRoot != filepath.Join(root, "docs", "session-review") { t.Fatalf("preview=%+v", preview) }
	for _, path := range []string{data, filepath.Join(root, "docs")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) { t.Fatalf("%s exists: %v", path, err) }
	}
}

// internal/cli/run_test.go
func TestRunInitPreviewsWithoutWritingUntilWriteFlag(t *testing.T) {
	projectRoot, vaultRoot, dataRoot := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "data")
	args := []string{"init", "--project", projectRoot, "--vault", vaultRoot, "--data-dir", dataRoot}
	var out, errOut bytes.Buffer
	if code := Run(args, &out, &errOut); code != 0 || !strings.Contains(out.String(), "action: create") { t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String()) }
	if _, err := os.Stat(filepath.Join(projectRoot, "docs")); !errors.Is(err, os.ErrNotExist) { t.Fatalf("preview wrote docs: %v", err) }
	out.Reset(); errOut.Reset()
	if code := Run(append(args, "--write"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "written: true") { t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String()) }
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/project ./internal/cli -run 'Test(PreviewInitializationDoesNotCreateDataOrLedger|RunInitPreviewsWithoutWritingUntilWriteFlag)$' -count=1`

Expected: FAIL because `PreviewInitialization` and `--write` do not exist and `init` mutates immediately.

- [ ] **Step 3: Add the preview value and route mutation explicitly**

```go
// internal/project/init.go
type InitPreview struct {
	ProjectID  string
	ProjectRoot string
	VaultRoot string
	LedgerRoot string
	ConfigPath string
	Action string
}

func PreviewInitialization(opts InitOptions) (InitPreview, error) {
	root, err := filepath.Abs(opts.ProjectRoot); if err != nil { return InitPreview{}, err }
	vault, err := filepath.Abs(opts.VaultRoot); if err != nil { return InitPreview{}, err }
	data, err := filepath.Abs(opts.DataDir); if err != nil { return InitPreview{}, err }
	projectDir, err := pathguard.Open(root); if err != nil { return InitPreview{}, fmt.Errorf("project root is a symlink or reparse point, or invalid: %w", err) }
	defer projectDir.Close()
	vaultDir, err := pathguard.Open(vault); if err != nil { return InitPreview{}, fmt.Errorf("vault root is a symlink or reparse point, or invalid: %w", err) }
	defer vaultDir.Close()
	if inside(opts.GOOS, root, vault) || inside(opts.GOOS, vault, root) || projectDir.ContainsIdentity(vaultDir.Info()) || vaultDir.ContainsIdentity(projectDir.Info()) {
		return InitPreview{}, fmt.Errorf("project and vault must not contain one another")
	}
	preview := InitPreview{ProjectRoot: root, VaultRoot: vault, LedgerRoot: filepath.Join(root, "docs", "session-review"), ConfigPath: filepath.Join(data, "config.toml"), Action: "create"}
	if cfg, loadErr := config.Load(preview.ConfigPath); loadErr == nil {
		if mapped, found := cfg.FindProject(opts.GOOS, root); found { preview.ProjectID, preview.Action = mapped.ID, "reuse" }
	} else if !errors.Is(loadErr, os.ErrNotExist) { return InitPreview{}, loadErr }
	return preview, nil
}

// internal/cli/init.go
write := flags.Bool("write", false, "perform the previewed writes")
// after resolving defaults and parsing flags:
options := project.InitOptions{ProjectRoot: *projectRoot, VaultRoot: *vaultRoot, DataDir: *dataRoot, GOOS: runtime.GOOS}
preview, err := project.PreviewInitialization(options)
if err != nil { return writeDiagnostic(stderr, "init", err) }
fmt.Fprintf(stdout, "action: %s\nproject_id: %s\nledger: %s\nconfig: %s\nwritten: false\n", preview.Action, preview.ProjectID, preview.LedgerRoot, preview.ConfigPath)
if !*write { return 0 }
result, err := project.Initialize(options)
if err != nil { return writeDiagnostic(stderr, "init", err) }
fmt.Fprintf(stdout, "project_id: %s\nledger: %s\nconfig: %s\nwritten: true\n", result.ProjectID, result.LedgerRoot, result.ConfigPath)
return 0
```

The implementation must share path/nesting validation helpers between preview and `Initialize`; the shown function is the complete observable contract, not permission to weaken the locked revalidation inside `Initialize`. `Initialize` must recompute and validate state under its existing transaction lock because the preview is advisory.

- [ ] **Step 4: Run init suites and verify GREEN**

Run: `gofmt -w internal/project/init.go internal/project/init_test.go internal/cli/init.go internal/cli/run_test.go && go test ./internal/project ./internal/cli -count=1`

Expected: PASS; preview creates no directory or file, `--write` retains locked idempotent initialization, and a preview/write race fails during write revalidation.

- [ ] **Step 5: Commit preview-first initialization**

```bash
git add internal/project/init.go internal/project/init_test.go internal/cli/init.go internal/cli/run_test.go
git commit -m "feat: preview initialization before writes"
```

### Task 3: Resolve Session Roots and Current Session Identifiers by Explicit Precedence

**Files:**
- Modify: `internal/platform/paths.go`
- Modify: `internal/platform/paths_test.go`
- Modify: `internal/cli/prepare.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: CLI flags plus `platform.Env{GOOS, Home, LocalAppData, SessionReviewerSessionsRoot, CodexHome, CodexThreadID, CodexSessionID}`.
- Produces: `platform.SessionRoot{Path, Source string}`; `platform.ResolveSessionsRoot(flagValue string, env Env) (SessionRoot, error)`; `platform.ResolveCurrentSessionID(flagValue string, env Env) (string, string, error)`.

- [ ] **Step 1: Write the precedence and identifier table tests**

```go
// internal/platform/paths_test.go
func TestResolveSessionsRootPrecedence(t *testing.T) {
	env := Env{GOOS: "darwin", Home: "/Users/me", SessionReviewerSessionsRoot: "/env/sessions", CodexHome: "/codex"}
	for _, test := range []struct{ flag, wantPath, wantSource string }{
		{"/flag/sessions", "/flag/sessions", "flag"},
		{"", "/env/sessions", "SESSION_REVIEWER_SESSIONS_ROOT"},
	} {
		got, err := ResolveSessionsRoot(test.flag, env); if err != nil { t.Fatal(err) }
		if got.Path != test.wantPath || got.Source != test.wantSource { t.Fatalf("got=%+v", got) }
	}
	env.SessionReviewerSessionsRoot = ""
	got, _ := ResolveSessionsRoot("", env); if got.Path != filepath.Join("/codex", "sessions") || got.Source != "CODEX_HOME" { t.Fatalf("got=%+v", got) }
	env.CodexHome = ""
	got, _ = ResolveSessionsRoot("", env); if got.Path != filepath.Join("/Users/me", ".codex", "sessions") || got.Source != "conventional" { t.Fatalf("got=%+v", got) }
}

func TestResolveCurrentSessionIDPrecedenceAndConflict(t *testing.T) {
	env := Env{CodexThreadID: "thread-1", CodexSessionID: "session-1"}
	if id, source, err := ResolveCurrentSessionID("explicit", env); err != nil || id != "explicit" || source != "flag" { t.Fatalf("id=%q source=%q err=%v", id, source, err) }
	if id, source, err := ResolveCurrentSessionID("", env); err != nil || id != "thread-1" || source != "CODEX_THREAD_ID" { t.Fatalf("id=%q source=%q err=%v", id, source, err) }
	env.CodexThreadID = ""
	if id, source, err := ResolveCurrentSessionID("", env); err != nil || id != "session-1" || source != "CODEX_SESSION_ID" { t.Fatalf("id=%q source=%q err=%v", id, source, err) }
}
```

- [ ] **Step 2: Run platform tests and verify RED**

Run: `go test ./internal/platform -run 'TestResolve(SessionsRootPrecedence|CurrentSessionIDPrecedenceAndConflict)$' -count=1`

Expected: FAIL because the typed discovery policy is absent.

- [ ] **Step 3: Implement the policy and wire `prepare` flags**

```go
// internal/platform/paths.go
type Env struct {
	GOOS string
	Home string
	LocalAppData string
	SessionReviewerSessionsRoot string
	CodexHome string
	CodexThreadID string
	CodexSessionID string
}

type SessionRoot struct { Path string; Source string }

func ResolveSessionsRoot(flagValue string, env Env) (SessionRoot, error) {
	candidates := []SessionRoot{
		{Path: flagValue, Source: "flag"},
		{Path: env.SessionReviewerSessionsRoot, Source: "SESSION_REVIEWER_SESSIONS_ROOT"},
	}
	if env.CodexHome != "" { candidates = append(candidates, SessionRoot{Path: filepath.Join(env.CodexHome, "sessions"), Source: "CODEX_HOME"}) }
	if env.Home != "" { candidates = append(candidates, SessionRoot{Path: filepath.Join(env.Home, ".codex", "sessions"), Source: "conventional"}) }
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.Path) != "" { return candidate, nil }
	}
	return SessionRoot{}, fmt.Errorf("cannot resolve Codex sessions root; use --sessions-root or set SESSION_REVIEWER_SESSIONS_ROOT")
}

func ResolveCurrentSessionID(flagValue string, env Env) (string, string, error) {
	for _, candidate := range []struct{ value, source string }{{flagValue, "flag"}, {env.CodexThreadID, "CODEX_THREAD_ID"}, {env.CodexSessionID, "CODEX_SESSION_ID"}} {
		if strings.TrimSpace(candidate.value) != "" { return candidate.value, candidate.source, nil }
	}
	return "", "cwd-and-time", nil
}

func CurrentEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{GOOS: runtime.GOOS, Home: home, LocalAppData: os.Getenv("LOCALAPPDATA"), SessionReviewerSessionsRoot: os.Getenv("SESSION_REVIEWER_SESSIONS_ROOT"), CodexHome: os.Getenv("CODEX_HOME"), CodexThreadID: os.Getenv("CODEX_THREAD_ID"), CodexSessionID: os.Getenv("CODEX_SESSION_ID")}
}

// internal/cli/prepare.go
currentSessionID := flags.String("current-session-id", "", "current Codex thread/session ID; --session overrides it")
// after flag parsing:
env := platform.CurrentEnv()
root, err := platform.ResolveSessionsRoot(*sessionsRoot, env)
if err != nil { return writeDiagnostic(stderr, "prepare", err) }
*sessionsRoot = root.Path
if *sessionID == "" {
	resolvedID, _, err := platform.ResolveCurrentSessionID(*currentSessionID, env)
	if err != nil { return writeDiagnostic(stderr, "prepare", err) }
	*sessionID = resolvedID
}
```

- [ ] **Step 4: Verify CLI precedence without depending on the developer's environment**

Run: `gofmt -w internal/platform/paths.go internal/platform/paths_test.go internal/cli/prepare.go internal/cli/run_test.go && go test ./internal/platform ./internal/cli -count=1`

Expected: PASS; tests inject `platform.Env` through a package-level `currentEnv` function variable restored with `t.Cleanup`, and `--session` remains the strongest selection input.

- [ ] **Step 5: Commit discovery precedence**

```bash
git add internal/platform/paths.go internal/platform/paths_test.go internal/cli/prepare.go internal/cli/run_test.go
git commit -m "feat: resolve Codex session context explicitly"
```

### Task 4: Isolate Unrelated Corrupt Session Files While Failing Closed for the Selection

**Files:**
- Modify: `internal/session/locator.go`
- Modify: `internal/session/locator_test.go`
- Modify: `internal/prepare/prepare.go`
- Modify: `internal/prepare/prepare_test.go`

**Interfaces:**
- Consumes: a validated sessions root and optional selected session ID.
- Produces: `session.Discovery{Candidates []Candidate, Issues []DiscoveryIssue}`; `session.DiscoveryIssue{Path, SessionID string, Err error}`; `session.Discover(root string, selectedSessionID string) (Discovery, error)`; `session.ResolveDiscovery(Discovery, ResolveOptions) (Candidate, error)`.

- [ ] **Step 1: Write the discovery corruption matrix**

```go
// internal/session/locator_test.go
func TestDiscoverExplicitIDIgnoresUnrelatedCorruptFile(t *testing.T) {
	root := t.TempDir()
	writeCandidate(t, root, "selected.jsonl", "wanted", "/project")
	if err := os.WriteFile(filepath.Join(root, "broken.jsonl"), []byte("{not-json\n"), 0o600); err != nil { t.Fatal(err) }
	discovery, err := Discover(root, "wanted")
	if err != nil { t.Fatal(err) }
	got, err := ResolveDiscovery(discovery, ResolveOptions{SessionID: "wanted"})
	if err != nil || got.ID != "wanted" { t.Fatalf("got=%+v err=%v issues=%+v", got, err, discovery.Issues) }
}

func TestDiscoverExplicitIDRejectsSelectedCorruptCandidate(t *testing.T) {
	root := t.TempDir()
	body := `{"timestamp":"2026-08-22T00:00:00Z","type":"session_meta","payload":{"id":"wanted","cwd":"/project"}}` + "\n{broken\n"
	if err := os.WriteFile(filepath.Join(root, "selected.jsonl"), []byte(body), 0o600); err != nil { t.Fatal(err) }
	discovery, err := Discover(root, "wanted")
	if err != nil { t.Fatal(err) }
	_, err = ResolveDiscovery(discovery, ResolveOptions{SessionID: "wanted"})
	if err == nil || !strings.Contains(err.Error(), "selected session candidate is corrupt") { t.Fatalf("err=%v", err) }
}

func TestDiscoverExplicitIDRejectsCorruptDuplicateCandidate(t *testing.T) {
	root := t.TempDir()
	writeCandidate(t, root, "one.jsonl", "wanted", "/project")
	body := `{"timestamp":"2026-08-22T00:00:00Z","type":"session_meta","payload":{"id":"wanted","cwd":"/project"}}` + "\n{broken\n"
	if err := os.WriteFile(filepath.Join(root, "two.jsonl"), []byte(body), 0o600); err != nil { t.Fatal(err) }
	discovery, err := Discover(root, "wanted")
	if err != nil { t.Fatal(err) }
	_, err = ResolveDiscovery(discovery, ResolveOptions{SessionID: "wanted"})
	if err == nil || !strings.Contains(err.Error(), "duplicate session id") { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: Run locator tests and verify RED**

Run: `go test ./internal/session -run 'TestDiscoverExplicitID(IgnoresUnrelatedCorruptFile|RejectsSelectedCorruptCandidate|RejectsCorruptDuplicateCandidate)$' -count=1`

Expected: FAIL because one malformed candidate aborts the root walk and discovery stops reading after metadata.

- [ ] **Step 3: Return per-file health and resolve conservatively**

```go
// internal/session/locator.go
type DiscoveryIssue struct { Path string; SessionID string; Err error }
type Discovery struct { Candidates []Candidate; Issues []DiscoveryIssue }

func ResolveDiscovery(discovery Discovery, opts ResolveOptions) (Candidate, error) {
	if opts.SessionID == "" && len(discovery.Issues) > 0 {
		return Candidate{}, fmt.Errorf("current-session discovery contains corrupt candidates; select a session explicitly")
	}
	matches := make([]Candidate, 0, 1)
	for _, candidate := range discovery.Candidates { if candidate.ID == opts.SessionID { matches = append(matches, candidate) } }
	selectedIssues := 0
	for _, issue := range discovery.Issues { if issue.SessionID == opts.SessionID { selectedIssues++ } }
	if opts.SessionID != "" && len(matches)+selectedIssues > 1 {
		return Candidate{}, fmt.Errorf("duplicate session id %q includes a corrupt candidate", opts.SessionID)
	}
	if selectedIssues == 1 {
		return Candidate{}, fmt.Errorf("selected session candidate is corrupt")
	}
	return Resolve(discovery.Candidates, opts)
}
```

Change `Discover` to continue the walk after a candidate-local decode/stream failure, append a `DiscoveryIssue`, and retain `SessionID` once a syntactically valid `session_meta` was observed. For targeted discovery it must scan the selected candidate through EOF so a malformed later record marks it corrupt; it may still stop after metadata for unrelated candidates to keep discovery bounded. Root redirection, walk I/O errors, file replacement, symlink/reparse points, and inability to enumerate the root remain whole-operation errors.

```go
// internal/prepare/prepare.go
discovery, err := session.Discover(opts.SessionsRoot, opts.SessionID)
if err != nil { return evidence.Packet{}, fmt.Errorf("discover sessions: %w", err) }
chosen, err := session.ResolveDiscovery(discovery, session.ResolveOptions{
	SessionID: opts.SessionID, CWD: opts.CWD, GOOS: opts.GOOS,
	Now: opts.Now, AmbiguityWindow: opts.AmbiguityWindow, PathsEqual: pathsEqual,
})
if err != nil { return evidence.Packet{}, err }
```

- [ ] **Step 4: Verify the locator and prepare policy end to end**

Run: `gofmt -w internal/session/locator.go internal/session/locator_test.go internal/prepare/prepare.go internal/prepare/prepare_test.go && go test ./internal/session ./internal/prepare -count=1`

Expected: PASS; unrelated corruption is ignored only for a selected ID, current-session inference fails on any discovery issue, and selected/duplicate corruption produces no output and advances no cursor.

- [ ] **Step 5: Commit targeted discovery**

```bash
git add internal/session/locator.go internal/session/locator_test.go internal/prepare/prepare.go internal/prepare/prepare_test.go
git commit -m "fix: isolate unrelated corrupt sessions"
```

### Task 5: Provide Actionable Help and Safe CLI Diagnostics

**Files:**
- Create: `internal/cli/diagnostic.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/init.go`
- Modify: `internal/cli/prepare.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: internal errors that may include filesystem paths or source details.
- Produces: `cli.Diagnostic{Code, Message, Hint string}`; `writeDiagnostic(io.Writer, string, error) int`; `help`, `init --help`, and `prepare <mode> --help` return exit 0 and complete usage.

- [ ] **Step 1: Write failing help, recovery, and non-disclosure tests**

```go
// internal/cli/run_test.go
func TestRunHelpListsEveryFoundationCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"help"}, &out, &errOut); code != 0 { t.Fatalf("code=%d err=%q", code, errOut.String()) }
	for _, text := range []string{"init", "prepare review", "prepare checkpoint", "version", "--sessions-root", "--current-session-id"} {
		if !strings.Contains(out.String(), text) { t.Fatalf("help=%q missing %q", out.String(), text) }
	}
}

func TestRunPrepareNotFoundGivesSafeSelectionHint(t *testing.T) {
	base := t.TempDir()
	sessions := filepath.Join(base, "customer-secret-sessions")
	projectRoot := filepath.Join(base, "project")
	dataRoot := filepath.Join(base, "data")
	for _, path := range []string{sessions, projectRoot, dataRoot} { if err := os.MkdirAll(path, 0o700); err != nil { t.Fatal(err) } }
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "project-1111111111111111", Root: projectRoot}}}); err != nil { t.Fatal(err) }
	var out, errOut bytes.Buffer
	code := Run([]string{"prepare", "review", "--session", "missing", "--sessions-root", sessions, "--cwd", projectRoot, "--data-dir", dataRoot, "--output", filepath.Join(t.TempDir(), "packet.json")}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "E_SESSION_NOT_FOUND") || !strings.Contains(errOut.String(), "check --session") || strings.Contains(errOut.String(), sessions) {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}
```

- [ ] **Step 2: Run CLI tests and verify RED**

Run: `go test ./internal/cli -run 'TestRun(HelpListsEveryFoundationCommand|PrepareNotFoundGivesSafeSelectionHint)$' -count=1`

Expected: FAIL because help is incomplete and most prepare failures collapse to `prepare failed`.

- [ ] **Step 3: Add a closed diagnostic mapping and full help text**

```go
// internal/cli/diagnostic.go
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/neomei/SessionReviewer/internal/prepare"
)

type Diagnostic struct { Code, Message, Hint string }

func writeDiagnostic(w io.Writer, action string, err error) int {
	d := Diagnostic{Code: "E_" + strings.ToUpper(action) + "_FAILED", Message: action + " failed", Hint: "run session-reviewer help and retry with explicit paths"}
	switch {
	case errors.Is(err, prepare.ErrCursorSourceDrift):
		d = Diagnostic{"E_CURSOR_DRIFT", "accepted session source changed", "run prepare review --from-start; this does not repair the cursor"}
	case errors.Is(err, prepare.ErrSessionNotFound):
		d = Diagnostic{"E_SESSION_NOT_FOUND", "selected session was not found", "check --session and --sessions-root, or omit --session to use current-session discovery"}
	case errors.Is(err, prepare.ErrSessionAmbiguous):
		d = Diagnostic{"E_SESSION_AMBIGUOUS", "current session is ambiguous", "pass --session or --current-session-id explicitly"}
	case errors.Is(err, prepare.ErrProjectNotInitialized):
		d = Diagnostic{"E_PROJECT_NOT_INITIALIZED", "project is not initialized", "run session-reviewer init to preview, then repeat with --write"}
	case errors.Is(err, prepare.ErrUnsafeOutput):
		d = Diagnostic{"E_OUTPUT_UNSAFE", "evidence output path is unsafe", "choose a regular file under the project and outside session/data roots"}
	}
	fmt.Fprintf(w, "%s: %s\nrecovery: %s\n", d.Code, d.Message, d.Hint)
	return 1
}
```

Add and wrap the five exported sentinels in `internal/prepare/prepare.go` at their existing decision points; never include `err.Error()` in the CLI output. Keep detailed wrapped causes inside package tests. Add one constant root help string in `internal/cli/run.go`, route `help`, `-h`, and `--help` to stdout/0, and set a custom `FlagSet.Usage` for each subcommand.

- [ ] **Step 4: Run CLI and privacy regression suites**

Run: `gofmt -w internal/cli/diagnostic.go internal/cli/run.go internal/cli/init.go internal/cli/prepare.go internal/cli/run_test.go internal/prepare/prepare.go && go test ./internal/cli ./internal/prepare -count=1`

Expected: PASS; every known recovery state has a code and safe next action, unknown errors remain non-specific, and seeded session/path canaries never appear on stdout or stderr.

- [ ] **Step 5: Commit diagnostics**

```bash
git add internal/cli/diagnostic.go internal/cli/run.go internal/cli/init.go internal/cli/prepare.go internal/cli/run_test.go internal/prepare/prepare.go
git commit -m "feat: add safe actionable CLI diagnostics"
```

### Task 6: Make the Windows Replacement Claim Match the Implementation

**Files:**
- Modify: `internal/atomicfile/replace_windows.go`
- Modify: `internal/atomicfile/replace_windows_logic.go`
- Modify: `internal/atomicfile/replace_windows_test.go`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: same-directory temporary and destination paths from `atomicfile.WriteRoot`.
- Produces: existing `replaceFile` and `replaceRootFile` signatures; existing destinations use native `ReplaceFileW`, absent destinations use `MoveFileExW(MOVEFILE_WRITE_THROUGH)`, and failures leave the old destination readable.

- [ ] **Step 1: Write native Windows durability tests**

```go
// internal/atomicfile/replace_windows_test.go
func TestWindowsExistingDestinationUsesNativeReplace(t *testing.T) {
	called := false
	ops := windowsFileOps{
		stat: os.Stat,
		replaceExisting: func(destination, temporary string) error { called = true; return os.Rename(temporary, destination) },
		moveNew: func(temporary, destination string) error { return errors.New("moveNew must not be used") },
	}
	dir := t.TempDir(); destination := filepath.Join(dir, "state.json"); temporary := filepath.Join(dir, "state.tmp")
	os.WriteFile(destination, []byte("old"), 0o600); os.WriteFile(temporary, []byte("new"), 0o600)
	if err := replaceWindowsFile(temporary, destination, ops); err != nil { t.Fatal(err) }
	if !called { t.Fatal("ReplaceFileW adapter was not selected") }
}

func TestWindowsNativeReplaceFailurePreservesDestination(t *testing.T) {
	dir := t.TempDir(); destination := filepath.Join(dir, "state.json"); temporary := filepath.Join(dir, "state.tmp")
	os.WriteFile(destination, []byte("old"), 0o600); os.WriteFile(temporary, []byte("new"), 0o600)
	ops := windowsFileOps{stat: os.Stat, replaceExisting: func(string, string) error { return syscall.ERROR_SHARING_VIOLATION }, moveNew: func(string, string) error { return nil }}
	if err := replaceWindowsFile(temporary, destination, ops); err == nil { t.Fatal("expected sharing violation") }
	if got, _ := os.ReadFile(destination); string(got) != "old" { t.Fatalf("destination=%q", got) }
}
```

- [ ] **Step 2: Run on the native Windows CI runner and verify RED**

Run locally for compile coverage: `GOOS=windows GOARCH=amd64 go test -c -o /tmp/atomicfile-windows.test.exe ./internal/atomicfile`

Run in Windows CI: `go test ./internal/atomicfile -run '^TestWindows' -count=20`

Expected: compile/test FAIL because the injected native operations do not exist; current code exposes a destination-missing interval between renames.

- [ ] **Step 3: Call the Windows replacement APIs directly**

```go
// internal/atomicfile/replace_windows_logic.go
type windowsFileOps struct {
	stat func(string) (fs.FileInfo, error)
	replaceExisting func(destination, temporary string) error
	moveNew func(temporary, destination string) error
}

func replaceWindowsFile(temporary, destination string, ops windowsFileOps) error {
	_, err := ops.stat(destination)
	switch {
	case err == nil:
		if err := ops.replaceExisting(destination, temporary); err != nil { return fmt.Errorf("replace existing destination: %w", err) }
		return nil
	case errors.Is(err, os.ErrNotExist):
		if err := ops.moveNew(temporary, destination); err != nil { return fmt.Errorf("install new destination: %w", err) }
		return nil
	default:
		return fmt.Errorf("inspect replacement destination: %w", err)
	}
}

// internal/atomicfile/replace_windows.go
//go:build windows
package atomicfile

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const movefileWriteThrough = 0x00000008
var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var replaceFileW = kernel32.NewProc("ReplaceFileW")
var moveFileExW = kernel32.NewProc("MoveFileExW")

func nativeReplace(destination, temporary string) error {
	dst, err := syscall.UTF16PtrFromString(destination); if err != nil { return err }
	tmp, err := syscall.UTF16PtrFromString(temporary); if err != nil { return err }
	ok, _, callErr := replaceFileW.Call(uintptr(unsafe.Pointer(dst)), uintptr(unsafe.Pointer(tmp)), 0, 0, 0, 0)
	if ok == 0 { return callErr }
	return nil
}

func nativeMoveNew(temporary, destination string) error {
	tmp, err := syscall.UTF16PtrFromString(temporary); if err != nil { return err }
	dst, err := syscall.UTF16PtrFromString(destination); if err != nil { return err }
	ok, _, callErr := moveFileExW.Call(uintptr(unsafe.Pointer(tmp)), uintptr(unsafe.Pointer(dst)), movefileWriteThrough)
	if ok == 0 { return callErr }
	return nil
}

func replaceFile(temporary, destination string) error { return replaceWindowsFile(temporary, destination, windowsFileOps{os.Stat, nativeReplace, nativeMoveNew}) }
func replaceRootFile(root *os.Root, temporary, destination string) error {
	return replaceWindowsFile(temporary, destination, windowsFileOps{root.Stat, func(dst, tmp string) error { return nativeReplace(filepath.Join(root.Name(), dst), filepath.Join(root.Name(), tmp)) }, func(tmp, dst string) error { return nativeMoveNew(filepath.Join(root.Name(), tmp), filepath.Join(root.Name(), dst)) }})
}
```

Remove the backup/rollback claim and logic from the Windows adapter. `ReplaceFileW` is the one-step replacement for an existing destination; the write contract is atomic visibility, not a claim that directory metadata is crash-durable on every filesystem. Keep read-side support for `.session-reviewer-backup` only as migration recovery for files written by older binaries until the release-hardening plan removes it deliberately.

- [ ] **Step 4: Verify native Windows behavior and cross-platform builds**

Run: `gofmt -w internal/atomicfile/replace_windows.go internal/atomicfile/replace_windows_logic.go internal/atomicfile/replace_windows_test.go && go test ./internal/atomicfile -count=1 && GOOS=windows GOARCH=amd64 go test -c -o /tmp/atomicfile-windows.test.exe ./internal/atomicfile && GOOS=windows GOARCH=amd64 go build -o /tmp/session-reviewer.exe ./cmd/session-reviewer`

Expected: POSIX logic tests PASS and both Windows artifacts compile. Native CI additionally passes `go test ./internal/atomicfile -run '^TestWindows' -count=20` with no missing destination, leftover backup, or lost old content after injected failure.

- [ ] **Step 5: Commit truthful Windows replacement**

```bash
git add internal/atomicfile/replace_windows.go internal/atomicfile/replace_windows_logic.go internal/atomicfile/replace_windows_test.go .github/workflows/ci.yml
git commit -m "fix: use native Windows file replacement"
```

### Task 7: Reconcile Foundation Progress, Documentation, and the Final Gate

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-08-22-session-reviewer-foundation.md`
- Modify: `.github/workflows/ci.yml`
- Modify: `internal/prepare/acceptance_test.go`

**Interfaces:**
- Consumes: all hardened CLI contracts from Tasks 1-6.
- Produces: documentation whose claims match observed behavior; a reconciled original-plan checklist with commit evidence; one acceptance gate for root precedence, selected corruption, preview-only init, identifier routing, and safe diagnostics.

- [ ] **Step 1: Add a failing hardening acceptance test**

```go
// internal/prepare/acceptance_test.go
func TestFoundationHardeningSelectedSessionIsIndependentAndIncremental(t *testing.T) {
	fixture := newFoundationFixture(t)
	fixture.writeSession(t, messageRecord("u1", "user", "first accepted packet"))
	if err := os.WriteFile(filepath.Join(fixture.sessions, "unrelated-corrupt.jsonl"), []byte("{broken\n"), 0o600); err != nil { t.Fatal(err) }
	opts := fixture.options("review")
	first, err := Run(opts); if err != nil { t.Fatal(err) }
	if first.SessionID != foundationSessionID || first.FromCursor != 1 { t.Fatalf("packet=%+v", first) }
	if _, err := os.Stat(filepath.Join(fixture.data, "projects", foundationProjectID, "cursors", foundationSessionID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepare advanced cursor: %v", err)
	}
}
```

- [ ] **Step 2: Run the acceptance test and verify RED before documentation changes**

Run: `go test ./internal/prepare -run '^TestFoundationHardeningSelectedSessionIsIndependentAndIncremental$' -count=1`

Expected: FAIL on the unrelated corrupt file until Task 4 is present; after Tasks 1-6 it passes.

- [ ] **Step 3: Reconcile the original foundation plan and README with exact status**

Add this status block immediately before `## Foundation Completion Gate` in `docs/superpowers/plans/2026-08-22-session-reviewer-foundation.md`, then change only steps backed by the listed commits from `- [ ]` to `- [x]`:

```markdown
## Implementation Status (reconciled 2026-08-23)

The deterministic foundation is implemented through `ddee5c7`, including follow-up durability and acceptance repairs. Tasks 1-10 are complete in the current repository. The fresh verification commands below remain the authority for the current checkout; checked boxes record implementation history, not a permanent claim that later changes cannot regress behavior.

Evidence commits: `bbb9ea7`, `59996c5`, `15172dd`, `4d172a0`, `385582a`, `2a12b0e`, `6d3f8d1`, `ddee5c7`.
```

Update `README.md` so initialization shows a preview command followed by the same command with `--write`; document the exact root precedence and `--current-session-id`/environment fallback; distinguish selected candidate corruption from unrelated corruption; state that Windows existing-file replacement uses `ReplaceFileW`, new-file installation uses `MoveFileExW`, and full native end-to-end release acceptance is still pending. Retain the explicit statements that the CLI has no model calls and performs no Git mutation.

- [ ] **Step 4: Run the complete fresh gate on the current checkout**

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/session-reviewer
GOOS=windows GOARCH=amd64 go test -c -o /tmp/session-reviewer-windows-tests.exe ./internal/atomicfile
GOOS=windows GOARCH=amd64 go build -o /tmp/session-reviewer.exe ./cmd/session-reviewer
git diff --check
```

Expected: every command exits 0. Then inspect `git status --short`: only the files named in this plan are modified before the commit. Native Windows CI must pass before this task is considered complete; cross-compilation alone is not Windows runtime evidence.

- [ ] **Step 5: Commit documentation and acceptance reconciliation**

```bash
git add README.md docs/superpowers/plans/2026-08-22-session-reviewer-foundation.md .github/workflows/ci.yml internal/prepare/acceptance_test.go
git commit -m "docs: reconcile hardened foundation status"
```

## Foundation Hardening Completion Gate

The next plan may begin only after a fresh run proves:

- no two configured roots can claim one `project_id`;
- `init` preview writes nothing, and `init --write` revalidates under the transaction lock;
- session-root precedence is flag, `SESSION_REVIEWER_SESSIONS_ROOT`, `CODEX_HOME/sessions`, then `<home>/.codex/sessions`;
- current-session ID precedence is `--session`, `--current-session-id`, `CODEX_THREAD_ID`, `CODEX_SESSION_ID`, then cwd/time inference;
- explicit session selection ignores unrelated corrupt JSONL while selected and duplicate corrupt candidates fail closed;
- every CLI error exposes a stable code and recovery action without leaking source content or sensitive paths;
- native Windows tests exercise `ReplaceFileW` behavior, while docs do not overstate crash durability or full end-to-end acceptance;
- the original foundation checklist and README reflect the code and fresh verification evidence;
- the Go CLI contains no model client and no automatic Git mutation.

Plan complete and saved to `docs/superpowers/plans/2026-08-23-session-reviewer-foundation-hardening.md`. Two execution options:

1. **Subagent-Driven (recommended)** - dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** - execute tasks in this session using executing-plans, batch execution with checkpoints.
