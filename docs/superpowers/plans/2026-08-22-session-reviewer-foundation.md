# SessionReviewer Cross-Platform Foundation Implementation Plan

> Historical plan note: evidence packet snippets below describe the original schema v1 milestone. The current executable contract is schema v2 with exact `expected_cursor` and `next_cursor` line/source-hash boundaries; old `to_cursor + 1` snippets are not the current protocol.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the deterministic macOS/Windows foundation that initializes a project, locates Codex sessions, streams and redacts JSONL records, emits bounded evidence packets, and preserves cursor state for later Skill-driven semantic workflows.

**Architecture:** A dependency-light Go CLI owns filesystem integrity and session parsing; semantic interpretation remains outside this milestone. Platform, configuration, session, redaction, evidence, and cursor packages expose narrow typed interfaces so the later ledger/Skill plan can consume evidence packets without revisiting raw-log handling.

**Tech Stack:** Go 1.26, Go standard library, `github.com/pelletier/go-toml/v2 v2.4.3`, GitHub Actions with `actions/checkout@v6` and `actions/setup-go@v7`.

## Global Constraints

- Target macOS 13 or later on Apple Silicon and Intel.
- Target Windows 10 22H2 or later and Windows 11 on x86-64.
- Windows ARM binaries are not a first-release acceptance requirement.
- Installation and runtime must not require administrator privileges.
- Raw Codex session files stay local and are opened read-only.
- Hidden reasoning, system messages, developer messages, and encrypted compaction payloads are never exported.
- Redaction happens before evidence packets or machine state are persisted.
- Semantic workflows require the Codex Skill; the standalone CLI must not invent conclusions.
- All writes use explicit project/data roots and atomic replacement.
- No automatic Git commit, push, reset, checkout, or rollback.
- The repository module path is `github.com/neomei/SessionReviewer`.

## Plan Set and Scope Boundary

The approved design contains five dependent, independently reviewable plans:

1. **This plan — deterministic foundation:** bootstrap, initialization, session discovery, streaming parser, redaction, evidence packets, cursors, and macOS/Windows CI.
2. **Ledger and Skill workflows:** proposal schemas, Markdown entities, Mermaid rendering, `review`, `checkpoint`, and `resume`.
3. **Bidirectional synchronization:** Obsidian mapping, three-way merge, conflicts, queues, dry-run, and atomic cross-platform writes.
4. **History and watcher:** project/worktree association, cross-session history, `launchd`, Task Scheduler, reconciliation, and reminders.
5. **Release hardening:** real-session acceptance, packaging, installation, recovery, uninstall, and security regression.

This plan ends with a working CLI that can produce safe evidence packets and initialize project identity. It deliberately does not generate semantic decisions or ledger updates.

## File Map

```text
go.mod                              Module, Go version, TOML dependency
go.sum                              Dependency checksums
.github/workflows/ci.yml            macOS and Windows test/build matrix
cmd/session-reviewer/main.go        Process entry point only
internal/cli/run.go                 Command routing and exit codes
internal/cli/run_test.go            CLI contract tests
internal/cli/prepare.go             Prepare flag parsing and safe output
internal/platform/paths.go          OS-specific default path and comparison policy
internal/platform/paths_test.go     macOS/Windows path policy tests
internal/config/config.go           Global TOML config types and atomic storage
internal/config/config_test.go      Config round-trip and corruption tests
internal/project/init.go            Project identity and ledger initialization
internal/project/init_test.go       Init idempotence and path safety tests
internal/cli/init.go                Init flag parsing and output contract
internal/session/record.go          Normalized JSONL record types
internal/session/stream.go          Bounded streaming decoder
internal/session/stream_test.go     Malformed/truncated/large-line tests
internal/session/locator.go         Session discovery and current-session resolution
internal/session/locator_test.go    Unicode, ambiguity, and explicit-ID tests
internal/redact/redact.go           Secret recognizers and bounded text output
internal/redact/redact_test.go      Canary non-disclosure tests
internal/evidence/types.go          Versioned evidence packet contract
internal/evidence/extract.go        Allowed-event extraction and exclusion policy
internal/evidence/extract_test.go   Role filtering, bounding, and provenance tests
internal/cursor/store.go            Atomic compare-and-swap cursor storage
internal/cursor/store_test.go       Durability and stale-update tests
internal/prepare/prepare.go         End-to-end evidence preparation orchestration
internal/prepare/prepare_test.go    Current/selected/checkpoint integration tests
internal/atomicfile/write.go        Shared same-directory atomic writer
internal/atomicfile/replace_posix.go POSIX replacement adapter
internal/atomicfile/replace_windows.go Windows replacement with rollback
internal/atomicfile/write_test.go   Replace and cleanup tests
testdata/sessions/minimal.jsonl     Small sanitized session fixture
README.md                           Foundation commands and scope
```

---

### Task 1: Bootstrap the CLI and Cross-Platform CI

**Files:**
- Create: `go.mod`
- Create: `cmd/session-reviewer/main.go`
- Create: `internal/cli/run.go`
- Create: `internal/cli/run_test.go`
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: process arguments and standard streams.
- Produces: `cli.Run(args []string, stdout, stderr io.Writer) int`; later tasks add subcommands through this router.

- [x] **Step 1: Write the failing CLI contract test**

```go
// internal/cli/run_test.go
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRequiresCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run(nil, &out, &errOut)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "Usage: session-reviewer") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"version"}, &out, &errOut)
	if code != 0 || strings.TrimSpace(out.String()) != "dev" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"unknown"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), `unknown command "unknown"`) {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}
```

- [x] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli -run 'TestRun' -v`

Expected: FAIL because `go.mod` and `Run` do not exist.

- [x] **Step 3: Add the minimal module and command router**

```go
// go.mod
module github.com/neomei/SessionReviewer

go 1.26
```

```go
// internal/cli/run.go
package cli

import (
	"fmt"
	"io"
)

var Version = "dev"

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: session-reviewer <command> [options]")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, Version)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}
```

```go
// cmd/session-reviewer/main.go
package main

import (
	"os"

	"github.com/neomei/SessionReviewer/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
```

```yaml
# .github/workflows/ci.yml
name: ci

on:
  push:
  pull_request:

permissions:
  contents: read

jobs:
  test:
    strategy:
      fail-fast: false
      matrix:
        os: [macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache: true
      - run: go test ./...
      - run: go vet ./...
      - run: go build ./cmd/session-reviewer
```

- [x] **Step 4: Format and verify locally**

Run: `gofmt -w cmd/session-reviewer/main.go internal/cli/run.go internal/cli/run_test.go && go test ./... && go vet ./... && go build ./cmd/session-reviewer`

Expected: all commands exit 0; tests report `ok .../internal/cli`.

- [x] **Step 5: Commit the bootstrap**

```bash
git add go.mod .github/workflows/ci.yml cmd/session-reviewer/main.go internal/cli/run.go internal/cli/run_test.go
git commit -m "chore: bootstrap cross-platform CLI"
```

---

### Task 2: Add Platform Paths and Atomic File Replacement

**Files:**
- Create: `internal/platform/paths.go`
- Create: `internal/platform/paths_test.go`
- Create: `internal/atomicfile/write.go`
- Create: `internal/atomicfile/write_test.go`

**Interfaces:**
- Consumes: `platform.Env{GOOS, Home, LocalAppData}`, paths from local/session metadata, and a target filename.
- Produces: `platform.DataDir(Env) (string, error)`, `platform.CurrentEnv() Env`, `platform.NormalizePath(goos, value string) string`, and `atomicfile.Write(path string, data []byte, perm fs.FileMode) error`.

- [x] **Step 1: Write failing platform and atomic-write tests**

```go
// internal/platform/paths_test.go
package platform

import (
	"path/filepath"
	"testing"
)

func TestDataDirMacOS(t *testing.T) {
	got, err := DataDir(Env{GOOS: "darwin", Home: "/Users/mei"})
	if err != nil || got != filepath.Join("/Users/mei", ".local", "share", "session-reviewer") {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestDataDirWindows(t *testing.T) {
	got, err := DataDir(Env{GOOS: "windows", LocalAppData: `C:\Users\Mei\AppData\Local`})
	if err != nil || got != filepath.Join(`C:\Users\Mei\AppData\Local`, "SessionReviewer") {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestDataDirRejectsMissingBase(t *testing.T) {
	if _, err := DataDir(Env{GOOS: "windows"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizePathWindowsAcrossSlashAndCase(t *testing.T) {
	a := NormalizePath("windows", `C:\项目\Repo`)
	b := NormalizePath("windows", `c:/项目/repo`)
	if a != b { t.Fatalf("a=%q b=%q", a, b) }
}
```

```go
// internal/atomicfile/write_test.go
package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesFileAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "new" {
		t.Fatalf("content=%q err=%v", b, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("entries=%v", entries)
	}
}
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/platform ./internal/atomicfile -v`

Expected: FAIL because both packages are missing.

- [x] **Step 3: Implement explicit platform policy and same-directory replacement**

```go
// internal/platform/paths.go
package platform

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

type Env struct {
	GOOS         string
	Home         string
	LocalAppData string
}

func CurrentEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{GOOS: runtime.GOOS, Home: home, LocalAppData: os.Getenv("LOCALAPPDATA")}
}

func DataDir(env Env) (string, error) {
	switch env.GOOS {
	case "darwin":
		if env.Home == "" {
			return "", fmt.Errorf("resolve data directory: home directory is empty")
		}
		return filepath.Join(env.Home, ".local", "share", "session-reviewer"), nil
	case "windows":
		if env.LocalAppData == "" {
			return "", fmt.Errorf("resolve data directory: LOCALAPPDATA is empty")
		}
		return filepath.Join(env.LocalAppData, "SessionReviewer"), nil
	default:
		return "", fmt.Errorf("unsupported operating system %q", env.GOOS)
	}
}

func NormalizePath(goos, value string) string {
	if goos == "windows" {
		value = strings.ReplaceAll(value, `\`, "/")
		return strings.ToLower(path.Clean(value))
	}
	return filepath.Clean(value)
}
```

```go
// internal/atomicfile/write.go
package atomicfile

import (
	"io/fs"
	"os"
	"path/filepath"
)

func Write(path string, data []byte, perm fs.FileMode) (retErr error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-reviewer-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return replaceFile(tmpName, path)
}
```

```go
// internal/atomicfile/replace_posix.go
//go:build !windows

package atomicfile

import "os"

func replaceFile(temporary, destination string) error {
	return os.Rename(temporary, destination)
}
```

```go
// internal/atomicfile/replace_windows.go
//go:build windows

package atomicfile

import (
	"errors"
	"os"
)

func replaceFile(temporary, destination string) error {
	backup := destination + ".session-reviewer-backup"
	_ = os.Remove(backup)
	if err := os.Rename(destination, backup); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.Rename(temporary, destination)
		}
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Rename(backup, destination)
		return err
	}
	return os.Remove(backup)
}
```

```go
// internal/atomicfile/replace_windows_test.go
//go:build windows

package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsReplaceRemovesBackup(t *testing.T) {
	dir:=t.TempDir(); path:=filepath.Join(dir,"state.json")
	if err:=os.WriteFile(path,[]byte("old"),0o600); err!=nil { t.Fatal(err) }
	if err:=Write(path,[]byte("new"),0o600); err!=nil { t.Fatal(err) }
	if _,err:=os.Stat(path+".session-reviewer-backup"); !os.IsNotExist(err) { t.Fatalf("backup remains: %v",err) }
	entries,_:=os.ReadDir(dir); if len(entries)!=1 { t.Fatalf("entries=%v",entries) }
}
```

The existing replacement test is platform-neutral and therefore runs natively in the Windows CI matrix. Do not use shell commands for replacement.

- [x] **Step 4: Run tests and cross-compile**

Run: `gofmt -w internal/platform internal/atomicfile && go test ./internal/platform ./internal/atomicfile && GOOS=windows GOARCH=amd64 go build ./cmd/session-reviewer`

Expected: tests pass and `session-reviewer.exe` builds.

- [x] **Step 5: Commit the platform foundation**

```bash
git add internal/platform internal/atomicfile
git commit -m "feat: add cross-platform data paths and atomic writes"
```

---

### Task 3: Persist Global Configuration and Initialize a Project

**Files:**
- Modify: `go.mod`
- Create: `go.sum`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `internal/project/init.go`
- Create: `internal/project/init_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: `config.Config`, project root, vault root, data directory, target GOOS, clock, and random reader.
- Produces: `config.Load(path)`, `config.Save(path, cfg)`, `config.FindProject(goos, root)`, and `project.Initialize(InitOptions) (InitResult, error)`.

- [x] **Step 1: Add TOML and write failing initialization tests**

Run: `go get github.com/pelletier/go-toml/v2@v2.4.3`

```go
// internal/project/init_test.go
package project

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitializeCreatesStableProjectAndMapping(t *testing.T) {
	root := filepath.Join(t.TempDir(), "项目")
	vault := filepath.Join(t.TempDir(), "知识库")
	data := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil { t.Fatal(err) }
	if err := os.MkdirAll(vault, 0o755); err != nil { t.Fatal(err) }
	opts := InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data,
		Now: func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
	}
	first, err := Initialize(opts)
	if err != nil { t.Fatal(err) }
	second, err := Initialize(opts)
	if err != nil { t.Fatal(err) }
	if first.ProjectID != second.ProjectID { t.Fatalf("ids differ: %q %q", first.ProjectID, second.ProjectID) }
	b, err := os.ReadFile(filepath.Join(root, "docs", "session-review", "project-overview.md"))
	if err != nil || !strings.Contains(string(b), "project-2a2a2a2a2a2a2a2a") {
		t.Fatalf("overview=%q err=%v", b, err)
	}
}

func TestInitializeRejectsNestedVaultAndProject(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil { t.Fatal(err) }
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "must not contain") { t.Fatalf("err=%v", err) }
}

func TestInitializeRejectsSymlinkedRoot(t *testing.T) {
	realRoot:=t.TempDir(); linkedRoot:=filepath.Join(t.TempDir(),"linked")
	if err:=os.Symlink(realRoot,linkedRoot); err!=nil { t.Skipf("symlink unavailable: %v",err) }
	_,err:=Initialize(InitOptions{ProjectRoot:linkedRoot,VaultRoot:t.TempDir(),DataDir:t.TempDir()})
	if err==nil || !strings.Contains(err.Error(),"symlink or reparse point") { t.Fatalf("err=%v",err) }
}
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config ./internal/project ./internal/cli -v`

Expected: FAIL because configuration and initialization types are missing.

- [x] **Step 3: Implement config storage and idempotent initialization**

```go
// internal/config/config.go
package config

import (
	"errors"
	"os"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/pelletier/go-toml/v2"
)

type ProjectMapping struct {
	ID        string `toml:"id"`
	Root      string `toml:"root"`
	VaultRoot string `toml:"vault_root"`
}

type Config struct {
	Version  int              `toml:"version"`
	Projects []ProjectMapping `toml:"projects"`
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) { return Config{Version: 1}, nil }
	if err != nil { return Config{}, err }
	var cfg Config
	if err := toml.Unmarshal(b, &cfg); err != nil { return Config{}, err }
	if cfg.Version != 1 { return Config{}, errors.New("unsupported config version") }
	return cfg, nil
}

func Save(path string, cfg Config) error {
	b, err := toml.Marshal(cfg)
	if err != nil { return err }
	return atomicfile.Write(path, b, 0o600)
}

func (c Config) FindProject(goos, root string) (ProjectMapping, bool) {
	clean := platform.NormalizePath(goos, root)
	for _, p := range c.Projects {
		if platform.NormalizePath(goos, p.Root) == clean { return p, true }
	}
	return ProjectMapping{}, false
}
```

```go
// internal/project/init.go
package project

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/platform"
)

type InitOptions struct {
	ProjectRoot string
	VaultRoot   string
	DataDir     string
	GOOS        string
	Now         func() time.Time
	Random      io.Reader
}

type InitResult struct { ProjectID, LedgerRoot, ConfigPath string }

func Initialize(opts InitOptions) (InitResult, error) {
	root, err := filepath.Abs(opts.ProjectRoot); if err != nil { return InitResult{}, err }
	vault, err := filepath.Abs(opts.VaultRoot); if err != nil { return InitResult{}, err }
	if err:=rejectRedirectedRoot(opts.GOOS,root); err!=nil { return InitResult{},err }
	if err:=rejectRedirectedRoot(opts.GOOS,vault); err!=nil { return InitResult{},err }
	if inside(opts.GOOS, root, vault) || inside(opts.GOOS, vault, root) { return InitResult{}, fmt.Errorf("project and vault must not contain one another") }
	if opts.Now == nil { opts.Now = time.Now }
	if opts.Random == nil { opts.Random = rand.Reader }
	configPath := filepath.Join(opts.DataDir, "config.toml")
	cfg, err := config.Load(configPath); if err != nil { return InitResult{}, err }
	if existing, ok := cfg.FindProject(opts.GOOS, root); ok {
		return InitResult{ProjectID: existing.ID, LedgerRoot: filepath.Join(root, "docs", "session-review"), ConfigPath: configPath}, nil
	}
	raw := make([]byte, 8); if _, err := io.ReadFull(opts.Random, raw); err != nil { return InitResult{}, err }
	id := "project-" + hex.EncodeToString(raw)
	ledger := filepath.Join(root, "docs", "session-review")
	if err := os.MkdirAll(ledger, 0o755); err != nil { return InitResult{}, err }
	body := fmt.Sprintf("---\nproject_id: %s\ncreated_at: %s\n---\n\n# %s\n", id, opts.Now().UTC().Format(time.RFC3339), filepath.Base(root))
	if err := atomicfile.Write(filepath.Join(ledger, "project-overview.md"), []byte(body), 0o644); err != nil { return InitResult{}, err }
	cfg.Projects = append(cfg.Projects, config.ProjectMapping{ID: id, Root: root, VaultRoot: vault})
	if err := config.Save(configPath, cfg); err != nil { return InitResult{}, err }
	return InitResult{ProjectID: id, LedgerRoot: ledger, ConfigPath: configPath}, nil
}

func rejectRedirectedRoot(goos,root string) error {
	evaluated,err:=filepath.EvalSymlinks(root); if err!=nil { return err }
	if platform.NormalizePath(goos,evaluated)!=platform.NormalizePath(goos,root) { return fmt.Errorf("root %q is a symlink or reparse point",root) }
	return nil
}

func inside(goos, parent, child string) bool {
	parent = platform.NormalizePath(goos, parent)
	child = platform.NormalizePath(goos, child)
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

```go
// internal/cli/init.go
package cli

import (
	"flag"
	"fmt"
	"io"
	"runtime"

	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/project"
)

func runInit(args []string, stdout, stderr io.Writer) int {
	flags:=flag.NewFlagSet("init",flag.ContinueOnError); flags.SetOutput(stderr)
	projectRoot:=flags.String("project","","project root")
	vaultRoot:=flags.String("vault","","Obsidian vault root")
	dataRoot:=flags.String("data-dir","","machine data directory")
	if err:=flags.Parse(args); err!=nil { return 2 }
	if *projectRoot=="" || *vaultRoot=="" { fmt.Fprintln(stderr,"init requires --project and --vault"); return 2 }
	if *dataRoot=="" {
		resolved,err:=platform.DataDir(platform.CurrentEnv()); if err!=nil { fmt.Fprintln(stderr,err); return 1 }; *dataRoot=resolved
	}
	result,err:=project.Initialize(project.InitOptions{ProjectRoot:*projectRoot,VaultRoot:*vaultRoot,DataDir:*dataRoot,GOOS:runtime.GOOS})
	if err!=nil { fmt.Fprintln(stderr,err); return 1 }
	fmt.Fprintf(stdout,"project_id: %s\nledger: %s\nconfig: %s\n",result.ProjectID,result.LedgerRoot,result.ConfigPath)
	return 0
}
```

Add this case to the `switch args[0]` statement in `internal/cli/run.go` before `default`:

```go
	case "init":
		return runInit(args[1:],stdout,stderr)
```

Add `TestRunInitRequiresProjectAndVault` to `run_test.go`; call `Run([]string{"init"},...)` and assert exit code 2 plus `init requires --project and --vault`. Print only the project ID and created paths; never print environment contents.

- [x] **Step 4: Verify idempotence and CLI behavior**

Run: `gofmt -w internal/config internal/project internal/cli && go test ./internal/config ./internal/project ./internal/cli -v && go test ./...`

Expected: all tests pass; running init twice in a temporary project returns the same project ID and does not duplicate the TOML mapping.

- [x] **Step 5: Commit initialization**

```bash
git add go.mod go.sum internal/config internal/project internal/cli
git commit -m "feat: initialize project identity and vault mapping"
```

---

### Task 4: Stream and Normalize Codex JSONL Records

**Files:**
- Create: `internal/session/record.go`
- Create: `internal/session/stream.go`
- Create: `internal/session/stream_test.go`
- Create: `testdata/sessions/minimal.jsonl`

**Interfaces:**
- Consumes: a session file, `session.DecodeOptions`, and a visitor callback.
- Produces: `session.Stream(path, opts, visit) (DecodeSummary, error)` with line, offset, hash, timestamp, type, and raw payload provenance.

- [x] **Step 1: Create a sanitized fixture and failing decoder tests**

```jsonl
{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"session-1","cwd":"/work/项目","source":"vscode"}}
{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"msg-user","role":"user","content":[{"type":"input_text","text":"Continue the project"}]}}
{"timestamp":"2026-08-22T10:02:00Z","type":"response_item","payload":{"type":"reasoning","id":"reasoning-1","summary":[]}}
```

```go
// internal/session/stream_test.go
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamPreservesProvenanceAndWarnsOnMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	content := "{\"timestamp\":\"2026-08-22T10:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"s1\"}}\nnot-json\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil { t.Fatal(err) }
	var records []Record
	summary, err := Stream(path, DecodeOptions{MaxRecordBytes: 1 << 20}, func(r Record) error {
		records = append(records, r); return nil
	})
	if err != nil { t.Fatal(err) }
	if len(records) != 1 || records[0].Line != 1 || records[0].SourceHash == "" { t.Fatalf("records=%+v", records) }
	if summary.MalformedLines != 1 { t.Fatalf("summary=%+v", summary) }
}

func TestStreamRejectsOversizedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 1025)+"\n"), 0o600); err != nil { t.Fatal(err) }
	_, err := Stream(path, DecodeOptions{MaxRecordBytes: 1024}, func(Record) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "exceeds 1024 bytes") { t.Fatalf("err=%v", err) }
}
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/session -run 'TestStream' -v`

Expected: FAIL because `Record` and `Stream` are undefined.

- [x] **Step 3: Implement a bounded line reader**

```go
// internal/session/record.go
package session

import "encoding/json"

type Record struct {
	Line       int
	ByteOffset int64
	Timestamp  string
	Type       string
	Payload    json.RawMessage
	SourceHash string
}

type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type DecodeOptions struct { FromLine, MaxRecordBytes int }
type DecodeSummary struct { Lines, Records, MalformedLines int }
```

```go
// internal/session/stream.go
package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func Stream(path string, opts DecodeOptions, visit func(Record) error) (DecodeSummary, error) {
	if opts.MaxRecordBytes == 0 { opts.MaxRecordBytes = 64 << 20 }
	f, err := os.Open(path); if err != nil { return DecodeSummary{}, err }
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var summary DecodeSummary
	var offset int64
	for {
		line, readErr := r.ReadBytes('\n')
		if len(line) == 0 && readErr == io.EOF { return summary, nil }
		summary.Lines++
		start := offset; offset += int64(len(line))
		if len(line) > opts.MaxRecordBytes { return summary, fmt.Errorf("line %d exceeds %d bytes", summary.Lines, opts.MaxRecordBytes) }
		trimmed := bytes.TrimSpace(line)
		if summary.Lines >= opts.FromLine && len(trimmed) > 0 {
			var env envelope
			if err := json.Unmarshal(trimmed, &env); err != nil {
				summary.MalformedLines++
			} else {
				sum := sha256.Sum256(trimmed)
				record := Record{Line: summary.Lines, ByteOffset: start, Timestamp: env.Timestamp, Type: env.Type, Payload: env.Payload, SourceHash: hex.EncodeToString(sum[:])}
				if err := visit(record); err != nil { return summary, err }
				summary.Records++
			}
		}
		if readErr == io.EOF { return summary, nil }
		if readErr != nil { return summary, readErr }
	}
}
```

- [x] **Step 4: Verify fixture, malformed-line, and large-line behavior**

Run: `gofmt -w internal/session && go test ./internal/session -v && go test ./...`

Expected: valid lines are visited in order; malformed lines increment the summary; records above 64 MiB fail before JSON decoding.

- [x] **Step 5: Commit the streaming parser**

```bash
git add internal/session testdata/sessions/minimal.jsonl
git commit -m "feat: stream Codex session records"
```

---

### Task 5: Discover and Resolve Sessions Conservatively

**Files:**
- Create: `internal/session/locator.go`
- Create: `internal/session/locator_test.go`

**Interfaces:**
- Consumes: sessions root, current working directory, optional explicit session ID, current time, and OS path policy.
- Produces: `session.Discover(root) ([]Candidate, error)` and `session.Resolve(candidates, ResolveOptions) (Candidate, error)`.

- [x] **Step 1: Write failing discovery and ambiguity tests**

```go
// internal/session/locator_test.go
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveByExplicitID(t *testing.T) {
	candidates := []Candidate{{ID: "s1", Path: "one"}, {ID: "s2", Path: "two"}}
	got, err := Resolve(candidates, ResolveOptions{SessionID: "s2"})
	if err != nil || got.Path != "two" { t.Fatalf("got=%+v err=%v", got, err) }
}

func TestResolveCurrentRejectsAmbiguousSameProjectSessions(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	candidates := []Candidate{
		{ID: "s1", CWD: `/work/项目`, ModTime: now.Add(-time.Minute)},
		{ID: "s2", CWD: `/work/项目`, ModTime: now.Add(-2*time.Minute)},
	}
	_, err := Resolve(candidates, ResolveOptions{CWD: `/work/项目`, Now: now, AmbiguityWindow: 5 * time.Minute})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") { t.Fatalf("err=%v", err) }
}

func TestDiscoverReadsOnlyJSONLSessionMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "2026", "08", "22", "rollout.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { t.Fatal(err) }
	line := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"/work/项目","source":"vscode"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil { t.Fatal(err) }
	got, err := Discover(root)
	if err != nil || len(got) != 1 || got[0].ID != "s1" { t.Fatalf("got=%+v err=%v", got, err) }
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/session -run 'TestResolve|TestDiscover' -v`

Expected: FAIL because locator types are missing.

- [x] **Step 3: Implement metadata-only discovery and explicit ambiguity**

```go
// internal/session/locator.go
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/platform"
)

var ErrStop = errors.New("stop stream")

type Candidate struct { ID, Path, CWD, Source string; StartedAt, ModTime time.Time }
type ResolveOptions struct { SessionID, CWD, GOOS string; Now time.Time; AmbiguityWindow time.Duration }

func Discover(root string) ([]Candidate, error) {
	var out []Candidate
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil { return walkErr }
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") { return nil }
		var candidate Candidate
		_, err := Stream(path, DecodeOptions{MaxRecordBytes: 64 << 20}, func(record Record) error {
			if record.Type != "session_meta" { return nil }
			var meta struct { ID, CWD, Source string; Timestamp time.Time `json:"timestamp"` }
			if err := json.Unmarshal(record.Payload, &meta); err != nil { return err }
			candidate = Candidate{ID: meta.ID, Path: path, CWD: meta.CWD, Source: meta.Source, StartedAt: meta.Timestamp}
			return ErrStop
		})
		if err != nil && !errors.Is(err, ErrStop) { return err }
		if candidate.ID != "" { info, _ := entry.Info(); candidate.ModTime = info.ModTime(); out = append(out, candidate) }
		return nil
	})
	return out, err
}

func Resolve(candidates []Candidate, opts ResolveOptions) (Candidate, error) {
	if opts.SessionID != "" {
		for _, c := range candidates { if c.ID == opts.SessionID { return c, nil } }
		return Candidate{}, fmt.Errorf("session %q not found", opts.SessionID)
	}
	var matches []Candidate
	for _, c := range candidates { if platform.NormalizePath(opts.GOOS, c.CWD) == platform.NormalizePath(opts.GOOS, opts.CWD) { matches = append(matches, c) } }
	if len(matches) == 0 { return Candidate{}, fmt.Errorf("no session matches working directory %q", opts.CWD) }
	sort.Slice(matches, func(i, j int) bool { return matches[i].ModTime.After(matches[j].ModTime) })
	if len(matches) > 1 && matches[0].ModTime.Sub(matches[1].ModTime) < opts.AmbiguityWindow {
		return Candidate{}, fmt.Errorf("ambiguous current session: %s and %s", matches[0].ID, matches[1].ID)
	}
	return matches[0], nil
}

```

Add this exact test to `locator_test.go`; it verifies Windows metadata comparison even when the test host is macOS:

```go
func TestResolveNormalizesWindowsPaths(t *testing.T) {
	candidates:=[]Candidate{{ID:"s1",CWD:`C:\项目\Repo`,ModTime:time.Now()}}
	got,err:=Resolve(candidates,ResolveOptions{CWD:`c:/项目/repo`,GOOS:"windows",AmbiguityWindow:time.Minute})
	if err!=nil || got.ID!="s1" { t.Fatalf("got=%+v err=%v",got,err) }
}
```

- [x] **Step 4: Verify conservative resolution**

Run: `gofmt -w internal/session && go test ./internal/session -run 'TestResolve|TestDiscover|TestNormalize' -v`

Expected: explicit ID wins; one clear current candidate resolves; candidates inside the ambiguity window return an error containing both IDs.

- [x] **Step 5: Commit session discovery**

```bash
git add internal/session/locator.go internal/session/locator_test.go
git commit -m "feat: discover and resolve local sessions"
```

---

### Task 6: Redact Secrets Before Persistence

**Files:**
- Create: `internal/redact/redact.go`
- Create: `internal/redact/redact_test.go`

**Interfaces:**
- Consumes: arbitrary message or tool text.
- Produces: `redact.Default().Text(input string) Result`, where `Result.Text` is safe to persist and `Result.Findings` contains only rule names and counts, never raw matches.

- [x] **Step 1: Write canary non-disclosure tests**

```go
// internal/redact/redact_test.go
package redact

import (
	"strings"
	"testing"
)

func TestDefaultRedactsCredentialCanaries(t *testing.T) {
	canaries := []string{
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.canary.signature",
		"OPENAI_API_KEY=sk-canary-123456789012345678901234567890",
		"postgres://admin:canary-password@db.example/test",
		"-----BEGIN PRIVATE KEY-----\nCANARYPRIVATEKEY\n-----END PRIVATE KEY-----",
		"cookie: session=canary-cookie-value",
	}
	for _, input := range canaries {
		result := Default().Text(input)
		if strings.Contains(result.Text, "canary") { t.Fatalf("leak for %q: %q", input, result.Text) }
		if !strings.Contains(result.Text, "[REDACTED:") { t.Fatalf("not redacted: %q", result.Text) }
	}
}

func TestDefaultPreservesSessionAndItemIDs(t *testing.T) {
	input := "session 01a02971-61d6-7251-bdcf-f999230f961d item msg_01a02974-3c83-7390-acd8-cb0fd17b6eef"
	if got := Default().Text(input).Text; got != input { t.Fatalf("got=%q", got) }
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/redact -v`

Expected: FAIL because the package does not exist.

- [x] **Step 3: Implement ordered recognizers with non-secret findings**

```go
// internal/redact/redact.go
package redact

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

type Finding struct { Rule string `json:"rule"`; Count int `json:"count"` }
type Result struct { Text string `json:"text"`; Findings []Finding `json:"findings,omitempty"` }
type rule struct { name string; re *regexp.Regexp }
type Redactor struct { rules []rule }

var tokenCandidate = regexp.MustCompile(`[A-Za-z0-9+/=_-]{40,}`)

func Default() Redactor {
	return Redactor{rules: []rule{
		{"private_key", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)},
		{"bearer", regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]{12,}`)},
		{"openai_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
		{"connection_url", regexp.MustCompile(`(?i)\b(postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis)://[^\s/@:]+:[^\s/@]+@[^\s]+`)},
		{"named_secret", regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|auth(?:orization)?|cookie|password|secret)\b\s*[:=]\s*[^\s,;]+`)},
	}}
}

func (r Redactor) Text(input string) Result {
	text := input
	counts := map[string]int{}
	for _, rule := range r.rules {
		matches := rule.re.FindAllStringIndex(text, -1)
		if len(matches) == 0 { continue }
		counts[rule.name] += len(matches)
		text = rule.re.ReplaceAllString(text, "[REDACTED:"+strings.ToUpper(rule.name)+"]")
	}
	text = tokenCandidate.ReplaceAllStringFunc(text,func(value string) string {
		if isStableID(value) || entropy(value)<4.0 { return value }
		counts["high_entropy_token"]++
		return "[REDACTED:HIGH_ENTROPY_TOKEN]"
	})
	keys := make([]string, 0, len(counts)); for key := range counts { keys = append(keys, key) }; sort.Strings(keys)
	result := Result{Text: text}
	for _, key := range keys { result.Findings = append(result.Findings, Finding{Rule: key, Count: counts[key]}) }
	return result
}

func isStableID(value string) bool {
	for _,prefix:=range []string{"msg_","rs_","ctc_","ctco_","ev-"} { if strings.HasPrefix(value,prefix) { return true } }
	return false
}

func entropy(value string) float64 {
	counts:=map[rune]float64{}; runes:=[]rune(value)
	for _,r:=range runes { counts[r]++ }
	var result float64
	for _,count:=range counts { p:=count/float64(len(runes)); result-=p*math.Log2(p) }
	return result
}
```

Add these tests to `redact_test.go`:

```go
func TestDefaultRedactsHighEntropyCanary(t *testing.T) {
	input:="q7Vx2Pm9Lk4Nz8Rc1Ya6Wt3Hu5Jd0Sf7Bg2Ke9Ui"
	result:=Default().Text(input)
	if strings.Contains(result.Text,input) || len(result.Findings)!=1 || result.Findings[0].Rule!="high_entropy_token" { t.Fatalf("result=%+v",result) }
}

func TestDefaultPreservesStableItemID(t *testing.T) {
	input:="msg_01a029743c837390acd8cb0fd17b6eef00000000"
	if got:=Default().Text(input).Text; got!=input { t.Fatalf("got=%q",got) }
}
```

Entropy handling stays separate from format recognizers because its false-positive profile is different.

- [x] **Step 4: Run leak-focused tests**

Run: `gofmt -w internal/redact && go test ./internal/redact -v && go test ./...`

Expected: no assertion output includes raw canaries; all tests pass.

- [x] **Step 5: Commit redaction**

```bash
git add internal/redact
git commit -m "feat: redact secrets from persisted evidence"
```

---

### Task 7: Extract a Versioned, Bounded Evidence Packet

**Files:**
- Create: `internal/evidence/types.go`
- Create: `internal/evidence/extract.go`
- Create: `internal/evidence/extract_test.go`

**Interfaces:**
- Consumes: normalized `session.Record` values, a `redact.Redactor`, and explicit `evidence.Limits`.
- Produces: `evidence.Extractor.Add(record)`, `evidence.Extractor.Packet() Packet`, `evidence.ErrPacketFull`, and schema version 1 JSON with provenance and warnings.

- [x] **Step 1: Write failing inclusion, exclusion, and bounding tests**

```go
// internal/evidence/extract_test.go
package evidence

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/session"
)

func record(t *testing.T, line int, payload string) session.Record {
	t.Helper()
	return session.Record{Line: line, Timestamp: "2026-08-22T10:00:00Z", Type: "response_item", Payload: json.RawMessage(payload), SourceHash: strings.Repeat("a", 64)}
}

func TestExtractorIncludesUserAssistantAndToolEvidence(t *testing.T) {
	x := New("s1", "/work/project", 1, redact.Default(), DefaultLimits())
	inputs := []session.Record{
		record(t, 1, `{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"goal"}]}`),
		record(t, 2, `{"type":"message","id":"a1","role":"assistant","content":[{"type":"output_text","text":"result"}]}`),
		record(t, 3, `{"type":"custom_tool_call","id":"c1","name":"exec_command","input":"{\\"cmd\\":\\"go test ./...\\"}"}`),
	}
	for _, input := range inputs { if err := x.Add(input); err != nil { t.Fatal(err) } }
	if got := x.Packet(); len(got.Events) != 3 || got.ToCursor != 3 { t.Fatalf("packet=%+v", got) }
}

func TestExtractorExcludesDeveloperAndReasoning(t *testing.T) {
	x := New("s1", "/work/project", 1, redact.Default(), DefaultLimits())
	_ = x.Add(record(t, 1, `{"type":"message","id":"d1","role":"developer","content":[{"type":"input_text","text":"do not export"}]}`))
	_ = x.Add(record(t, 2, `{"type":"reasoning","id":"r1","summary":[]}`))
	if got := x.Packet(); len(got.Events) != 0 { t.Fatalf("events=%+v", got.Events) }
}

func TestExtractorBoundsAndRedactsToolOutput(t *testing.T) {
	x := New("s1", "/work/project", 1, redact.Default(), DefaultLimits())
	long := strings.Repeat("x", 3000) + " OPENAI_API_KEY=sk-canary-123456789012345678901234567890"
	payload, _ := json.Marshal(map[string]any{"type":"custom_tool_call_output", "id":"o1", "call_id":"c1", "output":long})
	if err := x.Add(session.Record{Line: 1, Type:"response_item", Payload:payload, SourceHash:strings.Repeat("b",64)}); err != nil { t.Fatal(err) }
	got := x.Packet().Events[0].Summary
	if len([]rune(got)) > 1200 || strings.Contains(got, "canary") { t.Fatalf("summary leaked or unbounded: %q", got) }
}

func TestExtractorStopsBeforePacketLimit(t *testing.T) {
	x := New("s1", "/work/project", 1, redact.Default(), Limits{MaxEvents:2, MaxSummaryRunes:100, MaxPacketRunes:200})
	for line:=1; line<=3; line++ {
		err := x.Add(record(t,line,`{"type":"message","id":"u","role":"user","content":[{"type":"input_text","text":"goal"}]}`))
		if line < 3 && err != nil { t.Fatal(err) }
		if line == 3 && !errors.Is(err,ErrPacketFull) { t.Fatalf("err=%v",err) }
	}
	packet := x.Packet()
	if len(packet.Events)!=2 || packet.ToCursor!=2 || !packet.HasMore { t.Fatalf("packet=%+v",packet) }
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/evidence -v`

Expected: FAIL because evidence types and extractor are missing.

- [x] **Step 3: Implement the stable packet contract and allowlist**

```go
// internal/evidence/types.go
package evidence

type Packet struct {
	SchemaVersion int            `json:"schema_version"`
	ProjectID     string         `json:"project_id,omitempty"`
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
	FromCursor    int            `json:"from_cursor"`
	ToCursor      int            `json:"to_cursor"`
	HasMore       bool           `json:"has_more"`
	Events        []Item         `json:"events"`
	Warnings      []string       `json:"warnings,omitempty"`
}

type Item struct {
	ID         string `json:"id"`
	ItemID     string `json:"item_id,omitempty"`
	Timestamp  string `json:"timestamp"`
	JSONLLine  int    `json:"jsonl_line"`
	SourceHash string `json:"source_hash"`
	Kind       string `json:"kind"`
	Role       string `json:"role,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Summary    string `json:"summary"`
}
```

```go
// internal/evidence/extract.go
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/session"
)

var ErrPacketFull = errors.New("evidence packet is full")

type Limits struct { MaxEvents, MaxSummaryRunes, MaxPacketRunes int }
func DefaultLimits() Limits { return Limits{MaxEvents:500, MaxSummaryRunes:1200, MaxPacketRunes:300000} }

type Extractor struct { packet Packet; redactor redact.Redactor; limits Limits; totalRunes int }

func New(sessionID, cwd string, from int, redactor redact.Redactor, limits Limits) *Extractor {
	return &Extractor{packet: Packet{SchemaVersion:1, SessionID:sessionID, CWD:cwd, FromCursor:from, ToCursor:from-1, Events:[]Item{}}, redactor:redactor, limits:limits}
}

func (x *Extractor) Add(record session.Record) error {
	if len(x.packet.Events) >= x.limits.MaxEvents { x.packet.HasMore=true; return ErrPacketFull }
	if record.Type == "turn_context" {
		var p struct{ CWD string `json:"cwd"` }; if json.Unmarshal(record.Payload, &p) == nil && p.CWD != "" && p.CWD != x.packet.CWD {
			if err:=x.append(record, "cwd_change", "", "", p.CWD, ""); err!=nil { return err }
		}; x.packet.ToCursor=record.Line; return nil
	}
	if record.Type != "response_item" { x.packet.ToCursor=record.Line; return nil }
	var header struct{ Type, ID, Role, Name, Input, Output string; Content []struct{ Type, Text string } }
	if err := json.Unmarshal(record.Payload, &header); err != nil { return err }
	switch header.Type {
	case "message":
		if header.Role != "user" && header.Role != "assistant" { x.packet.ToCursor=record.Line; return nil }
		var parts []string; for _, c := range header.Content { if c.Type == "input_text" || c.Type == "output_text" { parts = append(parts, c.Text) } }
		if err:=x.append(record, "message", header.ID, header.Role, strings.Join(parts, "\n"), ""); err!=nil { return err }
	case "custom_tool_call": if err:=x.append(record, "tool_call", header.ID, "", header.Input, header.Name); err!=nil { return err }
	case "custom_tool_call_output": if err:=x.append(record, "tool_result", header.ID, "", header.Output, ""); err!=nil { return err }
	}
	x.packet.ToCursor=record.Line
	return nil
}

func (x *Extractor) append(record session.Record, kind, itemID, role, text, tool string) error {
	redacted := x.redactor.Text(bound(text, x.limits.MaxSummaryRunes))
	safe := redacted.Text
	for _,finding:=range redacted.Findings { x.packet.Warnings=append(x.packet.Warnings,fmt.Sprintf("redacted:%s:%d",finding.Rule,finding.Count)) }
	if x.totalRunes+utf8.RuneCountInString(safe) > x.limits.MaxPacketRunes { x.packet.HasMore=true; return ErrPacketFull }
	hash := sha256.Sum256([]byte(x.packet.SessionID+":"+itemID+":"+record.SourceHash))
	x.packet.Events = append(x.packet.Events, Item{ID:"ev-"+hex.EncodeToString(hash[:6]), ItemID:itemID, Timestamp:record.Timestamp, JSONLLine:record.Line, SourceHash:record.SourceHash, Kind:kind, Role:role, ToolName:tool, Summary:safe})
	x.totalRunes += utf8.RuneCountInString(safe)
	return nil
}

func (x *Extractor) Packet() Packet { return x.packet }

func bound(value string, max int) string {
	if utf8.RuneCountInString(value) <= max { return value }
	runes := []rune(value); return string(runes[:max])+"…[TRUNCATED]"
}
```

Add this test to `extract_test.go`:

```go
func TestPacketJSONContainsNoSeededCanary(t *testing.T) {
	x:=New("s1","/work/project",1,redact.Default(),DefaultLimits())
	_ = x.Add(record(t,1,`{"type":"custom_tool_call_output","id":"o1","output":"OPENAI_API_KEY=sk-canary-123456789012345678901234567890"}`))
	b,err:=json.Marshal(x.Packet()); if err!=nil { t.Fatal(err) }
	if strings.Contains(string(b),"canary") { t.Fatalf("packet leaked canary: %s",b) }
	if !strings.Contains(string(b),"redacted:openai_key:1") { t.Fatalf("warning missing: %s",b) }
}
```

`ErrPacketFull` is a successful segmentation boundary: callers serialize the current packet with `has_more: true` and resume from `to_cursor + 1` only after the packet is semantically accepted.

- [x] **Step 4: Verify extraction policy**

Run: `gofmt -w internal/evidence && go test ./internal/evidence -v && go test ./...`

Expected: user, assistant, tool-call, tool-result, and CWD-change events are included; developer/reasoning records are absent; all summaries are bounded and redacted.

- [x] **Step 5: Commit the evidence contract**

```bash
git add internal/evidence
git commit -m "feat: emit redacted evidence packets"
```

---

### Task 8: Add Durable Compare-and-Swap Cursors

**Files:**
- Create: `internal/cursor/store.go`
- Create: `internal/cursor/store_test.go`

**Interfaces:**
- Consumes: data root, session ID, expected cursor, and next cursor.
- Produces: `cursor.Store.Load(sessionID)`, `cursor.Store.Commit(sessionID, expected, next)`, and `cursor.ErrStale`.

- [x] **Step 1: Write failing cursor durability tests**

```go
// internal/cursor/store_test.go
package cursor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreCommitAndReload(t *testing.T) {
	store := Store{Root:t.TempDir()}
	next := Cursor{SessionID:"s1", LastLine:42, LastHash:strings.Repeat("a",64), UpdatedAt:time.Date(2026,8,22,10,0,0,0,time.UTC)}
	if err := store.Commit("s1", Cursor{}, next); err != nil { t.Fatal(err) }
	got, err := store.Load("s1")
	if err != nil || got != next { t.Fatalf("got=%+v err=%v", got, err) }
}

func TestStoreRejectsStaleCommit(t *testing.T) {
	store := Store{Root:t.TempDir()}
	current := Cursor{SessionID:"s1", LastLine:10, LastHash:strings.Repeat("a",64)}
	if err := store.Commit("s1", Cursor{}, current); err != nil { t.Fatal(err) }
	err := store.Commit("s1", Cursor{}, Cursor{SessionID:"s1", LastLine:20})
	if !errors.Is(err, ErrStale) { t.Fatalf("err=%v", err) }
}

func TestStoreRejectsUnsafeSessionID(t *testing.T) {
	store:=Store{Root:t.TempDir()}
	if _,err:=store.Load("../escape"); err==nil { t.Fatal("expected invalid id error") }
}

func TestStoreRejectsDecreasingCursor(t *testing.T) {
	store:=Store{Root:t.TempDir()}; current:=Cursor{SessionID:"s1",LastLine:10}
	if err:=store.Commit("s1",Cursor{},current); err!=nil { t.Fatal(err) }
	if err:=store.Commit("s1",current,Cursor{SessionID:"s1",LastLine:9}); err==nil { t.Fatal("expected decreasing cursor error") }
}

func TestStoreReportsCorruptJSON(t *testing.T) {
	store:=Store{Root:t.TempDir()}; path:=filepath.Join(store.Root,"cursors","s1.json")
	if err:=os.MkdirAll(filepath.Dir(path),0o700); err!=nil { t.Fatal(err) }
	if err:=os.WriteFile(path,[]byte("{"),0o600); err!=nil { t.Fatal(err) }
	if _,err:=store.Load("s1"); err==nil { t.Fatal("expected JSON error") }
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cursor -v`

Expected: FAIL because `Store`, `Cursor`, and `ErrStale` are undefined.

- [x] **Step 3: Implement atomic compare-and-swap storage**

```go
// internal/cursor/store.go
package cursor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

var ErrStale = errors.New("stale cursor")
var safeID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Cursor struct { SessionID string `json:"session_id"`; LastLine int `json:"last_line"`; LastHash string `json:"last_hash"`; UpdatedAt time.Time `json:"updated_at"` }
type Store struct { Root string }

func (s Store) Load(sessionID string) (Cursor, error) {
	path, err := s.path(sessionID); if err != nil { return Cursor{}, err }
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) { return Cursor{}, nil }
	if err != nil { return Cursor{}, err }
	var cursor Cursor; if err := json.Unmarshal(b, &cursor); err != nil { return Cursor{}, err }
	return cursor, nil
}

func (s Store) Commit(sessionID string, expected, next Cursor) error {
	current, err := s.Load(sessionID); if err != nil { return err }
	if current != expected { return ErrStale }
	if next.SessionID != sessionID || next.LastLine < current.LastLine { return fmt.Errorf("invalid next cursor") }
	b, err := json.MarshalIndent(next, "", "  "); if err != nil { return err }
	path, _ := s.path(sessionID); return atomicfile.Write(path, append(b, '\n'), 0o600)
}

func (s Store) path(sessionID string) (string, error) {
	if !safeID.MatchString(sessionID) { return "", fmt.Errorf("invalid session id %q", sessionID) }
	return filepath.Join(s.Root, "cursors", sessionID+".json"), nil
}
```

Task 9's integration test checks the remaining invariant: merely preparing evidence never creates or advances a cursor file.

- [x] **Step 4: Verify stale and durability behavior**

Run: `gofmt -w internal/cursor && go test ./internal/cursor -v && go test ./...`

Expected: compare-and-swap accepts the exact current cursor and rejects stale writers with `ErrStale`.

- [x] **Step 5: Commit cursor storage**

```bash
git add internal/cursor
git commit -m "feat: persist session cursors safely"
```

---

### Task 9: Orchestrate `prepare review` and `prepare checkpoint`

**Files:**
- Create: `internal/prepare/prepare.go`
- Create: `internal/prepare/prepare_test.go`
- Create: `internal/cli/prepare.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: `prepare.Options{Mode, SessionsRoot, SessionID, CWD, DataDir, Output, FromStart, GOOS}`.
- Produces: `prepare.Run(options) (evidence.Packet, error)` and CLI commands that atomically write JSON evidence without advancing cursors.

- [x] **Step 1: Write a failing end-to-end prepare test**

```go
// internal/prepare/prepare_test.go
package prepare

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/evidence"
)

func TestRunPreparesCurrentCheckpointWithoutAdvancingCursor(t *testing.T) {
	root := t.TempDir(); sessions := filepath.Join(root,"sessions"); data := filepath.Join(root,"data"); projectRoot := filepath.Join(root,"project")
	for _, dir := range []string{sessions,data,projectRoot} { if err := os.MkdirAll(dir,0o755); err != nil { t.Fatal(err) } }
	sessionPath := filepath.Join(sessions,"rollout.jsonl")
	content := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"`+filepath.ToSlash(projectRoot)+`","source":"vscode"}}`+"\n"+
		`{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"goal"}]}}`+"\n"
	if err := os.WriteFile(sessionPath,[]byte(content),0o600); err != nil { t.Fatal(err) }
	if err := config.Save(filepath.Join(data,"config.toml"),config.Config{Version:1,Projects:[]config.ProjectMapping{{ID:"p1",Root:projectRoot,VaultRoot:filepath.Join(root,"vault")}}}); err != nil { t.Fatal(err) }
	output := filepath.Join(root,"evidence.json")
	packet, err := Run(Options{Mode:"checkpoint",SessionsRoot:sessions,CWD:projectRoot,DataDir:data,Output:output,Now:time.Date(2026,8,22,10,2,0,0,time.UTC),AmbiguityWindow:time.Second})
	if err != nil { t.Fatal(err) }
	if packet.ProjectID != "p1" || packet.SessionID != "s1" || len(packet.Events) != 1 { t.Fatalf("packet=%+v",packet) }
	if _, err := os.Stat(filepath.Join(data,"projects","p1","cursors","s1.json")); !os.IsNotExist(err) { t.Fatalf("cursor advanced during prepare: %v",err) }
	b, _ := os.ReadFile(output); var decoded evidence.Packet
	if err := json.Unmarshal(b,&decoded); err != nil || decoded.SessionID != "s1" { t.Fatalf("decoded=%+v err=%v",decoded,err) }
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/prepare ./internal/cli -v`

Expected: FAIL because the prepare orchestrator and command route do not exist.

- [x] **Step 3: Implement orchestration and command flags**

```go
// internal/prepare/prepare.go
package prepare

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/session"
)

type Options struct { Mode, SessionsRoot, SessionID, CWD, DataDir, Output, GOOS string; FromStart bool; Now time.Time; AmbiguityWindow time.Duration }

func Run(opts Options) (evidence.Packet, error) {
	if opts.Mode != "review" && opts.Mode != "checkpoint" { return evidence.Packet{}, fmt.Errorf("invalid prepare mode %q",opts.Mode) }
	candidates, err := session.Discover(opts.SessionsRoot); if err != nil { return evidence.Packet{},err }
	chosen, err := session.Resolve(candidates,session.ResolveOptions{SessionID:opts.SessionID,CWD:opts.CWD,GOOS:opts.GOOS,Now:opts.Now,AmbiguityWindow:opts.AmbiguityWindow}); if err != nil { return evidence.Packet{},err }
	cfg, err := config.Load(filepath.Join(opts.DataDir,"config.toml")); if err != nil { return evidence.Packet{},err }
	mapping, ok := cfg.FindProject(opts.GOOS,opts.CWD); if !ok { return evidence.Packet{},fmt.Errorf("project %q is not initialized",opts.CWD) }
	stored, err := (cursor.Store{Root:filepath.Join(opts.DataDir,"projects",mapping.ID)}).Load(chosen.ID); if err != nil { return evidence.Packet{},err }
	from := stored.LastLine+1; if opts.FromStart { from=1 }
	x := evidence.New(chosen.ID,chosen.CWD,from,redact.Default(),evidence.DefaultLimits())
	summary, err := session.Stream(chosen.Path,session.DecodeOptions{FromLine:from,MaxRecordBytes:64<<20},x.Add)
	if err != nil && !errors.Is(err,evidence.ErrPacketFull) { return evidence.Packet{},err }
	packet := x.Packet(); packet.ProjectID=mapping.ID
	if summary.MalformedLines>0 { packet.Warnings=append(packet.Warnings,fmt.Sprintf("malformed_jsonl_lines:%d",summary.MalformedLines)) }
	b, err := json.MarshalIndent(packet,"","  "); if err != nil { return evidence.Packet{},err }
	if err := atomicfile.Write(opts.Output,append(b,'\n'),0o600); err != nil { return evidence.Packet{},err }
	return packet,nil
}
```

```go
// internal/cli/prepare.go
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/prepare"
)

func runPrepare(args []string,stdout,stderr io.Writer) int {
	if len(args)==0 { fmt.Fprintln(stderr,"prepare requires review or checkpoint"); return 2 }
	mode:=args[0]; flags:=flag.NewFlagSet("prepare "+mode,flag.ContinueOnError); flags.SetOutput(stderr)
	sessionsRoot:=flags.String("sessions-root","","Codex sessions root")
	cwd:=flags.String("cwd","","project working directory")
	sessionID:=flags.String("session","","explicit session ID")
	dataRoot:=flags.String("data-dir","","machine data directory")
	output:=flags.String("output","","evidence output path")
	fromStart:=flags.Bool("from-start",false,"ignore accepted cursor")
	if err:=flags.Parse(args[1:]); err!=nil { return 2 }
	if mode!="review" && mode!="checkpoint" { fmt.Fprintf(stderr,"invalid prepare mode %q\n",mode); return 2 }
	if mode=="checkpoint" && *fromStart { fmt.Fprintln(stderr,"--from-start is valid only for review"); return 2 }
	if *output=="" { fmt.Fprintln(stderr,"prepare requires --output"); return 2 }
	if *cwd=="" { resolved,err:=os.Getwd(); if err!=nil { fmt.Fprintln(stderr,err); return 1 }; *cwd=resolved }
	if *sessionsRoot=="" { home,err:=os.UserHomeDir(); if err!=nil { fmt.Fprintln(stderr,err); return 1 }; *sessionsRoot=filepath.Join(home,".codex","sessions") }
	if *dataRoot=="" { resolved,err:=platform.DataDir(platform.CurrentEnv()); if err!=nil { fmt.Fprintln(stderr,err); return 1 }; *dataRoot=resolved }
	packet,err:=prepare.Run(prepare.Options{Mode:mode,SessionsRoot:*sessionsRoot,SessionID:*sessionID,CWD:*cwd,DataDir:*dataRoot,Output:*output,GOOS:runtime.GOOS,FromStart:*fromStart,Now:time.Now(),AmbiguityWindow:5*time.Minute})
	if err!=nil { fmt.Fprintln(stderr,err); return 1 }
	fmt.Fprintf(stdout,"prepared %d evidence events for session %s\noutput: %s\n",len(packet.Events),packet.SessionID,*output)
	return 0
}
```

Add this case to `internal/cli/run.go`:

```go
	case "prepare":
		return runPrepare(args[1:],stdout,stderr)
```

The command supports these flags:

```text
--sessions-root <path>   default <user-home>/.codex/sessions
--cwd <path>             default current directory
--session <id>           optional explicit selection
--data-dir <path>        default platform data directory
--output <path>          required
--from-start             review only
```

Add these validation tests to `run_test.go`; the package integration test above covers successful output with temporary roots:

```go
func TestRunPrepareRequiresMode(t *testing.T) {
	var out,errOut bytes.Buffer
	if code:=Run([]string{"prepare"},&out,&errOut); code!=2 || !strings.Contains(errOut.String(),"requires review or checkpoint") { t.Fatalf("code=%d stderr=%q",code,errOut.String()) }
}

func TestRunPrepareRequiresOutput(t *testing.T) {
	var out,errOut bytes.Buffer
	if code:=Run([]string{"prepare","review"},&out,&errOut); code!=2 || !strings.Contains(errOut.String(),"requires --output") { t.Fatalf("code=%d stderr=%q",code,errOut.String()) }
}

func TestRunCheckpointRejectsFromStart(t *testing.T) {
	var out,errOut bytes.Buffer
	code:=Run([]string{"prepare","checkpoint","--from-start","--output","evidence.json"},&out,&errOut)
	if code!=2 || !strings.Contains(errOut.String(),"valid only for review") { t.Fatalf("code=%d stderr=%q",code,errOut.String()) }
}
```

The CLI must not print event summaries to stdout or stderr.

- [x] **Step 4: Run full tests and both platform builds**

Run: `gofmt -w internal/prepare internal/cli && go test ./... && go vet ./... && go build ./cmd/session-reviewer && GOOS=windows GOARCH=amd64 go build -o /tmp/session-reviewer.exe ./cmd/session-reviewer`

Expected: all tests and vet pass; both binaries build; prepare writes evidence but no cursor.

- [x] **Step 5: Commit preparation workflows**

```bash
git add internal/prepare internal/cli
git commit -m "feat: prepare review and checkpoint evidence"
```

---

### Task 10: Add Scale, Security, Documentation, and Foundation Acceptance

**Files:**
- Create: `internal/prepare/acceptance_test.go`
- Modify: `README.md`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: all foundation packages and generated large fixtures.
- Produces: a release gate demonstrating bounded evidence, no canary leakage, idempotent prepare output, no cursor advance, and successful macOS/Windows builds.

- [x] **Step 1: Write failing foundation acceptance tests**

```go
// internal/prepare/acceptance_test.go
package prepare

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
)

func TestLargeSessionEvidenceIsBoundedAndContainsNoCanary(t *testing.T) {
	dir := t.TempDir(); path := filepath.Join(dir,"large.jsonl")
	f, err := os.Create(path); if err != nil { t.Fatal(err) }
	w := bufio.NewWriter(f)
	meta := `{"timestamp":"2026-08-22T10:00:00Z","type":"session_meta","payload":{"id":"large","cwd":"/project"}}`+"\n"
	_, _ = w.WriteString(meta)
	for i:=0;i<25000;i++ {
		payload,_ := json.Marshal(map[string]any{"timestamp":"2026-08-22T10:01:00Z","type":"response_item","payload":map[string]any{"type":"custom_tool_call_output","id":"out","output":strings.Repeat("x",900)+" OPENAI_API_KEY=sk-canary-123456789012345678901234567890"}})
		_, _ = w.Write(append(payload,'\n'))
	}
	if err:=w.Flush(); err!=nil { t.Fatal(err) }; if err:=f.Close(); err!=nil { t.Fatal(err) }
	info,_:=os.Stat(path); if info.Size()<20<<20 { t.Fatalf("fixture too small: %d",info.Size()) }
	output := runPreparedFixture(t,path,"/project")
	if bytes.Contains(output,[]byte("canary")) { t.Fatal("redaction canary leaked") }
	if len(output)>40<<20 { t.Fatalf("evidence packet unexpectedly large: %d",len(output)) }
}

func TestPrepareSameInputIsByteStable(t *testing.T) {
	fixture := filepath.Join("..","..","testdata","sessions","minimal.jsonl")
	first := runPreparedFixture(t,fixture,"/work/项目")
	second := runPreparedFixture(t,fixture,"/work/项目")
	if !bytes.Equal(first,second) { t.Fatal("unchanged input produced different evidence") }
}

func runPreparedFixture(t *testing.T, sourcePath, cwd string) []byte {
	t.Helper()
	root:=t.TempDir(); sessions:=filepath.Join(root,"sessions"); data:=filepath.Join(root,"data")
	if err:=os.MkdirAll(sessions,0o755); err!=nil { t.Fatal(err) }
	source,err:=os.ReadFile(sourcePath); if err!=nil { t.Fatal(err) }
	copyPath:=filepath.Join(sessions,"rollout.jsonl")
	if err:=os.WriteFile(copyPath,source,0o400); err!=nil { t.Fatal(err) }
	cfg:=config.Config{Version:1,Projects:[]config.ProjectMapping{{ID:"project-test",Root:cwd,VaultRoot:filepath.Join(root,"vault")}}}
	if err:=config.Save(filepath.Join(data,"config.toml"),cfg); err!=nil { t.Fatal(err) }
	outputPath:=filepath.Join(root,"evidence.json")
	_,err=Run(Options{Mode:"checkpoint",SessionsRoot:sessions,CWD:cwd,DataDir:data,Output:outputPath,GOOS:"darwin",Now:time.Date(2026,8,22,10,2,0,0,time.UTC),AmbiguityWindow:time.Second})
	if err!=nil { t.Fatal(err) }
	output,err:=os.ReadFile(outputPath); if err!=nil { t.Fatal(err) }
	return output
}
```

- [x] **Step 2: Run acceptance tests before final documentation**

Run: `go test ./internal/prepare -run '^(TestFoundationLargeSessionReachesBoundedPacketAfterStreamingPast20MiB|TestFoundationPrepareFromStartIsByteStableAndCursorSideEffectFree)$' -count=1 -v`

Expected: exactly these two tests run and PASS only after all earlier tasks are correctly integrated. The >20 MB test remains an always-on release gate.

- [x] **Step 3: Write the foundation README**

````markdown
# SessionReviewer

SessionReviewer turns local Codex session logs into redacted evidence packets for a Skill-driven project history. The foundation CLI does not generate semantic conclusions by itself.

## Requirements

- macOS 13+ on Intel or Apple Silicon, or Windows 10 22H2+/Windows 11 x64
- Go 1.26 when building from source
- No administrator privileges or separate OpenAI API key

## Build and test

```bash
go test ./...
go vet ./...
go build ./cmd/session-reviewer
```

## Initialize

```bash
session-reviewer init --project . --vault /absolute/path/to/vault
```

## Prepare evidence

```bash
session-reviewer prepare checkpoint --sessions-root /absolute/path/to/.codex/sessions --output evidence.json
session-reviewer prepare review --session <session-id> --sessions-root /absolute/path/to/.codex/sessions --output evidence.json --from-start
```

Preparing evidence never advances the accepted cursor. A later Skill-driven apply step validates semantic changes and commits the cursor.

Raw sessions remain local. Evidence excludes developer/system messages and hidden reasoning, bounds tool output, and redacts likely secrets before persistence.
````

- [x] **Step 4: Strengthen CI and run final verification**

Add these commands after the normal test step in `.github/workflows/ci.yml`:

```yaml
      - run: go test -race ./...
      - run: go test ./internal/prepare -run '^TestFoundationPrepareFromStartIsByteStableAndCursorSideEffectFree$' -count=2
```

Run locally:

```bash
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/session-reviewer
GOOS=darwin GOARCH=amd64 go build -o /tmp/session-reviewer-darwin-amd64 ./cmd/session-reviewer
GOOS=windows GOARCH=amd64 go build -o /tmp/session-reviewer-windows-amd64.exe ./cmd/session-reviewer
git diff --check
```

Expected: every command exits 0; the two cross-platform binaries exist; `git diff --check` prints nothing.

- [x] **Step 5: Commit the accepted foundation**

```bash
git add README.md .github/workflows/ci.yml internal/prepare/acceptance_test.go
git commit -m "test: verify foundation scale and security"
```

## Implementation Status (reconciled 2026-08-23)

The deterministic foundation and its follow-up hardening are implemented through `05766c4`. Tasks 1-10 in this original plan are complete in the current repository. Checked boxes record the implementation history of each task; the fresh verification commands below remain the authority for the current checkout, and the historical illustrative code snippets are not a substitute for the current source contracts.

Original foundation evidence:

| Task | Implementation and repair commits |
| --- | --- |
| 1 | `47594ee` |
| 2 | `a74b5fd`, `b102e39` |
| 3 | `6855d88`, `ef9c488` |
| 4 | `bfe5a5b` |
| 5 | `e98cac6`, `134b63c` |
| 6 | `360dd10`, `def61f0`, `e11b7fa`, `8ba1370`, `9257b41`, `d36dff8`, `2dc5422`, `1c79bae` |
| 7 | `d6f2141`, `36690de` |
| 8 | `82ebb12`, `bbb9ea7` |
| 9 | `59996c5`, `15172dd`, `4d172a0` |
| 10 and final review | `385582a`, `2a12b0e`, `6d3f8d1`, `ddee5c7` |

Foundation hardening evidence:

| Hardening task | Implementation and repair commits |
| --- | --- |
| 1: globally unique project IDs | `fded734` |
| 2: preview-only initialization and locked revalidation | `9b534b1`, `621ec00` |
| 3: session-root and current-session-ID precedence | `260ec0d` |
| 4: selected-session corruption isolation | `635bd48`, `9f9cec8` |
| 5: safe actionable diagnostics | `9909b2a`, `906e883` |
| 6: rooted Windows replacement and no-clobber installation | `3acc549`, `d991228`, `05766c4` |

The final Task 6 contract supersedes the earlier path-based Windows adapter design: an existing destination uses handle-relative `os.Root.Rename`; an absent destination uses rooted `Link` followed by `Remove`, with no replacing fallback. This records atomic visibility and no-clobber behavior, not universal filesystem crash durability. The current local host can cross-compile the Windows implementation and tests but cannot supply a native Windows runtime receipt; that receipt remains pending for the current commit, as does full Windows end-to-end release acceptance.

The standalone Go CLI makes no model or OpenAI API calls and performs no automatic Git mutation. Initialization previews without writing unless `--write` is present; session-root precedence is flag, `SESSION_REVIEWER_SESSIONS_ROOT`, `CODEX_HOME/sessions`, then the conventional home path; current-session-ID precedence is `--session`, `--current-session-id`, `CODEX_THREAD_ID`, `CODEX_SESSION_ID`, then conservative cwd/time inference. A selected ID ignores unrelated corrupt JSONL, while corruption in the selected or duplicate candidate set fails closed. Operational `init`/`prepare` failures routed through the diagnostic mapper cross the process boundary as stable codes with recovery actions and without source content or sensitive paths; syntax and usage errors remain plain usage text with exit code 2.

## Foundation Completion Gate

Before starting the ledger/Skill plan, verify all of the following in one fresh run:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/session-reviewer
GOOS=windows GOARCH=amd64 go build -o /tmp/session-reviewer.exe ./cmd/session-reviewer
git status --short
```

Expected:

- every test, race test, vet, and build passes;
- the Windows binary cross-compiles;
- `git status --short` is empty;
- a >20 MB synthetic session produces bounded, redacted evidence;
- developer messages and reasoning records are absent from evidence;
- repeated prepare runs are byte-stable;
- prepare never advances a cursor;
- project initialization is idempotent and rejects nested project/vault roots.

Only after this gate passes should the next plan define proposal application, Markdown ledger entities, Mermaid rendering, and the Codex Skill.
