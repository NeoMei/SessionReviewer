# SessionReviewer Release Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the completed SessionReviewer engine and Skill into traceable `v0.1.0` source and binary distributions with truthful platform durability, idempotent no-admin installation, actionable CLI help, real private macOS/Windows acceptance receipts, security/performance/recovery gates, checksums, SBOMs, release CI, documentation, and a tested rollback path.

**Architecture:** Build metadata is injected from an exact commit for private candidates and from an exact `v0.1.0` tag/commit pair for public mode, while a Go release packager creates deterministic per-platform archives, a separate Skill archive, SPDX SBOMs, and one checksum manifest without relying on host archive quirks. Installation remains user-scoped and manifest-driven; private real-session E2E runs consume external ignored inputs and emit redacted receipts, while public release publication remains mechanically blocked until the repository owner explicitly authorizes and adds a license grant.

**Tech Stack:** Go 1.26; Go standard library archive/tar, archive/zip, gzip, SHA-256, JSON, and `debug/buildinfo`; existing macOS/Windows engine and Skill; POSIX shell; PowerShell 5.1+; GitHub Actions; `govulncheck` from `golang.org/x/vuln v1.7.0`; SPDX 2.3 JSON; native macOS 13 Intel/Apple Silicon and Windows 10 22H2/Windows 11 x86-64 acceptance hosts.

## Global Constraints

- Release target is `v0.1.0`; every release archive, receipt, SBOM, version response, checksum, tag, and GitHub Release must name exactly `0.1.0` and the same 40-character commit.
- Target macOS 13 or later on Apple Silicon and Intel.
- Target Windows 10 22H2 or later and Windows 11 on x86-64.
- Windows ARM is architecture-ready but is not a `v0.1.0` binary or acceptance target.
- Release binaries use `CGO_ENABLED=0` and require no local C toolchain, runtime, separate API key, or administrator privileges.
- Raw real sessions, proposals derived from them, private vault/project copies, and detailed receipts are never committed, embedded in archives, or uploaded as public artifacts.
- Public release publication is forbidden until the repository owner explicitly chooses a license and adds the resulting license text; this plan must not choose a license or copyright holder.
- Until that authorization exists, packages contain `LICENSE_STATUS.md` stating that no license grant is included and are restricted to private acceptance; CI must refuse the public-release job.
- The watcher never calls a model or Git and is not required for manual checkpoint/review/sync/resume/history workflows.
- No release is accepted from cross-compilation alone; native minimum-OS receipts are required for macOS and Windows.
- Windows replacement is described according to the current Go 1.26 rooted implementation: an existing destination uses one `os.Root.Rename` replacement operation, while an absent destination is published with a rooted hard link followed by temporary-name removal. Neither path is claimed to provide untested power-loss equivalence to POSIX directory `fsync`.
- Secrets must not appear in evidence, Markdown, SQLite, merge bases, durable queues, conflict notes, watcher state, logs, receipts, SBOMs, archives, or checksums filenames.
- Install, upgrade, rollback, and uninstall preserve project/vault Markdown and raw sessions; local machine state is removed only through an explicit purge flag.
- Release automation never tags, publishes, or uploads from a dirty tree.

## Legal Scope Boundary

The approved design does not identify a license, copyright holder, or distribution grant. Those are material legal decisions and cannot be inferred by implementation agents. This plan therefore adds an explicit status notice and a hard public-release gate; it completes private packaging and acceptance, but the final public GitHub Release step remains unavailable until the repository owner supplies an authorized `LICENSE` file. Adding MIT, Apache-2.0, GPL, a proprietary EULA, a holder name, or a copyright line is outside this plan.

## File Map

```text
internal/buildinfo/buildinfo.go             Injected version/commit/date and release validation
internal/buildinfo/buildinfo_test.go        Dev/release metadata tests
internal/cli/help.go                        Root and subcommand actionable help text
internal/cli/help_test.go                   Every command, recovery hint, and exit-code help contract
internal/cli/run.go                         `--help`, `help`, version JSON, and doctor routing
internal/cli/doctor.go                      Read-only install/config/startup/index diagnostics
internal/cli/doctor_test.go                 Safe actionable diagnostic output
cmd/session-reviewer/main.go                Build metadata remains linked through buildinfo package
LICENSE_STATUS.md                           Explicit no-license-grant status for private artifacts
docs/release/licensing.md                   Owner authorization gate and public-release refusal
internal/atomicfile/guarantee.go             Named/testable platform guarantees
internal/atomicfile/guarantee_test.go        Current publication-operation matrix
internal/atomicfile/replace_windows.go       Current rooted replacement entry point; behavior retained
internal/atomicfile/replace_windows_logic.go Current replace-or-link decision; behavior retained
docs/platform/windows-durability.md          Exact Windows guarantees and limitations
internal/install/manifest.go                  Installed files, rollback pair, hashes, version
internal/install/manifest_test.go             Source/archive/rollback/preservation invariants
scripts/install.sh                            Source first, then archive macOS install and upgrade
scripts/uninstall.sh                          Manifest-driven macOS uninstall/purge
scripts/install.ps1                           Source first, then archive Windows install and upgrade
scripts/uninstall.ps1                         Manifest-driven Windows uninstall/purge
cmd/verify-skill/main.go                      Skill frontmatter/script/package contract
skill/session-reviewer/SKILL.md               Packaged Codex workflow instructions
skill/session-reviewer/scripts/*              Relative-path POSIX/PowerShell workflow wrappers
cmd/release-packager/main.go                 Deterministic archive/checksum/SBOM command
cmd/release-packager/archive.go              Stable tar.gz/zip creation
cmd/release-packager/archive_test.go         Archive contents, modes, timestamps, traversal tests
cmd/release-packager/sbom.go                 SPDX 2.3 JSON module/build inventory
cmd/release-packager/sbom_test.go            SPDX validation and no-secret tests
scripts/build-release.sh                     Clean-tree multi-target release build
scripts/build-release.ps1                    Native Windows release build/verification
cmd/check-release-license/main.go            Hard public-release license gate
test/e2e/real/config.go                       External real-session/proposal input contract
test/e2e/real/runner.go                       Scenarios A-E orchestration
test/e2e/real/runner_test.go                  Synthetic harness unit tests
test/e2e/real/receipt.go                      Redacted signed-input-hash receipt schema
test/e2e/real/receipt_test.go                 Privacy and assertion completeness tests
scripts/run-real-e2e.sh                       macOS private E2E entry point
scripts/run-real-e2e.ps1                      Windows private E2E entry point
scripts/collect-platform-receipt.sh           macOS version/arch/binary evidence
scripts/collect-platform-receipt.ps1          Windows version/build/arch/binary evidence
test/release/security_test.go                 Persistence-wide canary and traversal regression
test/release/performance_test.go              Memory and latency budgets
test/release/recovery_test.go                 Crash, locked-file, corrupt-index, queue restart recovery
test/release/archive_test.go                  Installed/archive/Skill smoke tests
.gitignore                                    Private E2E inputs/receipts and `dist/`
.github/workflows/ci.yml                      Always-on release regressions
.github/workflows/release-native.yml          Native minimum-OS private acceptance dispatch
.github/workflows/release.yml                 Tag build, artifact/SBOM/checksum assembly, gated publish
docs/release/native-hosts.md                  Exact native host labels and evidence commands
docs/release/checklist.md                     `v0.1.0` gates and receipt manifest
docs/release/rollback.md                      Watcher stop, previous binary restore, dry-run, index rebuild
README.md                                     Install, help, recovery, uninstall, support, and license status
```

---

### Task 1: Inject Release Identity and Make Help/Diagnostics Actionable

**Files:**
- Create: `internal/buildinfo/buildinfo.go`
- Create: `internal/buildinfo/buildinfo_test.go`
- Create: `internal/cli/help.go`
- Create: `internal/cli/help_test.go`
- Create: `internal/cli/doctor.go`
- Create: `internal/cli/doctor_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`
- Modify: `cmd/session-reviewer/main.go`
- Create: `LICENSE_STATUS.md`
- Create: `docs/release/licensing.md`

**Interfaces:**
- Consumes: existing CLI commands and `cli.Run(args,stdout,stderr) int`.
- Produces: `buildinfo.Current() buildinfo.Info`, `buildinfo.ValidateRelease(buildinfo.Info) error`, `session-reviewer version [--json]`, `session-reviewer doctor [--json]`, complete root/subcommand help, and a hard documented no-license-grant status.

- [ ] **Step 1: Write failing metadata, help, and diagnostic tests**

```go
// internal/buildinfo/buildinfo_test.go
func TestValidateReleaseRequiresExactInjectedMetadata(t *testing.T) {
	good:=Info{Version:"0.1.0",Commit:strings.Repeat("a",40),BuiltAt:"2026-08-23T01:02:03Z",GoVersion:"go1.26.5"}
	if err:=ValidateRelease(good);err!=nil{t.Fatal(err)}
	for _,bad:=range []Info{{Version:"dev",Commit:good.Commit,BuiltAt:good.BuiltAt},{Version:"0.1.0",Commit:"short",BuiltAt:good.BuiltAt},{Version:"0.1.0",Commit:good.Commit,BuiltAt:"today"}} { if ValidateRelease(bad)==nil{t.Fatalf("accepted %#v",bad)} }
}
```

```go
// internal/cli/help_test.go
func TestRootHelpNamesEveryWorkflowAndRecoveryCommand(t *testing.T) {
	var out,errOut bytes.Buffer;code:=Run([]string{"--help"},&out,&errOut)
	if code!=0||errOut.Len()!=0{t.Fatalf("code=%d stderr=%q",code,errOut.String())}
	for _,want:=range []string{"init","prepare review","prepare checkpoint","apply","sync","resume --ledger-only","history --ledger-only","watch install","watch status","watch uninstall","doctor","version"}{if !strings.Contains(out.String(),want){t.Fatalf("missing %q",want)}}
}

func TestDoctorReportsSafeActionableFailures(t *testing.T) {
	result:=runDoctorFixture(t,brokenIndexAndMissingWatcher)
	for _,want:=range []string{"index: corrupt","action: session-reviewer index rebuild","watcher: not installed","action: session-reviewer watch install"}{if !strings.Contains(result,want){t.Fatalf("missing %q",want)}}
	if strings.Contains(result,"sk-canary-"){t.Fatal("doctor leaked secret")}
}
```

- [ ] **Step 2: Run focused tests and observe current incomplete help/version behavior**

Run:

```bash
go test ./internal/buildinfo ./internal/cli -run 'TestValidateRelease|TestRootHelp|TestDoctor' -v
```

Expected: FAIL because the buildinfo/help/doctor contracts do not exist and current root help is incomplete.

- [ ] **Step 3: Add exact build identity types and link variables**

```go
// internal/buildinfo/buildinfo.go
package buildinfo

var Version = "dev"
var Commit = "unknown"
var BuiltAt = "unknown"

type Info struct { Version,Commit,BuiltAt,GoVersion string `json:"-"` }
type JSONInfo struct { Version string `json:"version"`; Commit string `json:"commit"`; BuiltAt string `json:"built_at"`; GoVersion string `json:"go_version"` }

func Current() Info { return Info{Version:Version,Commit:Commit,BuiltAt:BuiltAt,GoVersion:runtime.Version()} }
func ValidateRelease(i Info) error {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(i.Version){return fmt.Errorf("release version must be semantic without v prefix")}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(i.Commit){return fmt.Errorf("release commit must be a full lowercase SHA")}
	if _,err:=time.Parse(time.RFC3339,i.BuiltAt);err!=nil{return fmt.Errorf("release build time: %w",err)}
	return nil
}
```

Remove `cli.Version`. `runVersion` renders `buildinfo.Current()` as one human line or canonical JSON. Release builds inject exactly:

```bash
-ldflags "-s -w -X github.com/neomei/SessionReviewer/internal/buildinfo.Version=0.1.0 -X github.com/neomei/SessionReviewer/internal/buildinfo.Commit=$COMMIT -X github.com/neomei/SessionReviewer/internal/buildinfo.BuiltAt=$BUILD_TIME"
```

- [ ] **Step 4: Implement complete help and read-only doctor**

`help.go` stores exact per-command synopsis, purpose, required flags, exit codes (`0` success, `1` operational failure, `2` usage error), examples, and the next recovery command. Unknown commands print the root synopsis plus concrete examples such as `session-reviewer help history` and `session-reviewer help watch`. `doctor` reads build identity, config, ledger, index quick-check, cursor directory, queue counts, startup status, and writable configured roots; it never reads session bodies, invokes a model, modifies state, or prints raw config values.

Create `LICENSE_STATUS.md` with this exact project status (not a license grant):

```markdown
# License Status

This repository does not currently include a license grant. Private build and acceptance artifacts created from it are not authorized for public redistribution by this notice. A public release must remain blocked until the repository owner explicitly selects and adds an authorized LICENSE file.
```

`docs/release/licensing.md` states that implementation agents must not select a license/holder, `cmd/check-release-license` is the mechanical gate added in Task 4, and public release resumes only after explicit owner authorization in a separate change.

- [ ] **Step 5: Run CLI and metadata regressions**

Run:

```bash
gofmt -w internal/buildinfo internal/cli cmd/session-reviewer
go test ./internal/buildinfo ./internal/cli -v
go run ./cmd/session-reviewer --help
go run ./cmd/session-reviewer version --json
go run ./cmd/session-reviewer doctor --json
```

Expected: tests pass; help exits 0; version JSON reports `dev/unknown` in a source run; doctor returns safe checks/actions and exit 1 only when a check is unhealthy.

- [ ] **Step 6: Commit build identity and release-status UX**

```bash
git add internal/buildinfo internal/cli cmd/session-reviewer/main.go LICENSE_STATUS.md docs/release/licensing.md
git commit -m "feat: add release identity and actionable diagnostics"
```

---

### Task 2: Define and Prove Windows Replacement Durability Without Overclaiming

**Files:**
- Create: `internal/atomicfile/guarantee.go`
- Create: `internal/atomicfile/guarantee_test.go`
- Modify: `internal/atomicfile/replace_windows_test.go`
- Modify: `internal/atomicfile/write_test.go`
- Create: `docs/platform/windows-durability.md`
- Do not modify: `internal/atomicfile/replace_windows.go`
- Do not modify: `internal/atomicfile/replace_windows_logic.go`

**Interfaces:**
- Consumes: existing `atomicfile.Write`, `WriteRoot`, `replaceWindowsFile`, rooted `Rename`/`Link`/`Remove`, temporary-file sync, and publication sync contracts.
- Produces: `atomicfile.GuaranteeFor(goos string) atomicfile.Guarantee`, classified Windows publication errors, and tested documentation of exactly what is guaranteed for existing versus absent destinations and what remains unproven for power loss.

- [ ] **Step 1: Write failing durability and crash-state matrix tests**

```go
func TestWindowsGuaranteeMatchesCurrentRootedPublication(t *testing.T) {
	g:=GuaranteeFor("windows")
	if !g.TempDataFlushed || !g.ExistingDestinationSingleReplace || !g.AbsentDestinationRaceSafeLink || !g.PublishedFileFlushed {t.Fatalf("%#v",g)}
	if g.UsesBackupProtocol || g.DirectoryEntryPowerLossGuaranteed {t.Fatalf("overclaim: %#v",g)}
}

func TestWindowsPublicationUsesCurrentExactOperations(t *testing.T) {
	present:=captureWindowsOps(t,true); if err:=replaceWindowsFile("tmp","state",present.ops);err!=nil{t.Fatal(err)}
	if diff:=cmp.Diff([]string{"exists:state","rename:tmp:state"},present.calls);diff!=""{t.Fatal(diff)}
	absent:=captureWindowsOps(t,false); if err:=replaceWindowsFile("tmp","state",absent.ops);err!=nil{t.Fatal(err)}
	if diff:=cmp.Diff([]string{"exists:state","link:tmp:state","remove:tmp"},absent.calls);diff!=""{t.Fatal(diff)}
}
```

- [ ] **Step 2: Run durability tests and expose the undocumented guarantee**

Run:

```bash
go test ./internal/atomicfile -run 'TestWindowsGuarantee|TestWindowsPublication' -v
```

Expected: FAIL because the named guarantee does not exist; the exact-operation test passes against the current implementation and prevents a stale backup protocol from being introduced by this task.

- [ ] **Step 3: Add the exact current-publication guarantee API**

```go
type Guarantee struct {
	TempDataFlushed bool
	ExistingDestinationSingleReplace bool
	AbsentDestinationRaceSafeLink bool
	PublishedFileFlushed bool
	DirectoryFlushAttempted bool
	UsesBackupProtocol bool
	DirectoryEntryPowerLossGuaranteed bool
}
func GuaranteeFor(goos string) Guarantee {
	if goos=="windows" { return Guarantee{TempDataFlushed:true,ExistingDestinationSingleReplace:true,AbsentDestinationRaceSafeLink:true,PublishedFileFlushed:true,DirectoryFlushAttempted:true} }
	return Guarantee{TempDataFlushed:true,ExistingDestinationSingleReplace:true,PublishedFileFlushed:true,DirectoryFlushAttempted:true,DirectoryEntryPowerLossGuaranteed:true}
}
```

Do not replace the current implementation. For an existing destination, `os.Root.Rename` uses Go 1.26's handle-relative Windows rename with replace-if-exists semantics; one namespace operation publishes the fully synced temporary. For an absent destination, `Link(temporary,destination)` fails rather than overwriting a racing creator, and successful publication removes only the temporary name. The writer then flushes the published file and attempts to flush the pinned directory. A sharing/lock failure leaves the previous destination intact and returns the wrapped error; sync callers may retry/queue at their existing higher-level boundary. No `.session-reviewer-backup` file or loader recovery protocol participates in these Windows writes.

- [ ] **Step 4: Characterize exact visibility, failure, and durability boundaries**

Add injected-operation tests before temporary sync, after temporary sync/before publication, on existing-destination rename failure, on absent-destination link failure, after link/before temporary-name removal, and on publication-sync failure. They assert: a pre-publication failure leaves the old destination or absence unchanged; a successful existing-destination call leaves only the new complete file; an absent-destination link creates the complete destination and a failed cleanup may leave only an extra same-inode temporary name; publication-sync failure reports uncertainty after complete new content is visible. Native concurrent-reader tests repeatedly replace differing checksummed payloads and accept only complete old/new payloads, never missing or torn bytes.

`docs/platform/windows-durability.md` must explicitly say:

- the temporary file's data is flushed before installation;
- an existing destination is published with one rooted replace-if-exists rename after temporary sync;
- an absent destination is published by a race-safe rooted hard link followed by temporary-name removal;
- process interruption at tested hooks leaves an old/new complete destination; link cleanup may leave an extra temporary hard-link name that a later failed-write cleanup removes;
- open-file sharing violations leave the old destination intact and return to the caller's retry/queue boundary;
- no backup multi-rename/recovery state exists in the current implementation;
- power-loss directory-entry durability is not claimed where Windows directory-handle flush is unsupported or fails;
- native Windows 10/11 crash/power-interruption receipts are the release evidence;
- POSIX uses same-directory rename and directory sync where supported.

- [ ] **Step 5: Run unit, subprocess-crash, race, and native Windows suites**

Run locally:

```bash
gofmt -w internal/atomicfile
go test ./internal/atomicfile -v
go test -race ./internal/atomicfile -count=10
GOOS=windows GOARCH=amd64 go test -c -o /tmp/atomicfile-windows.test.exe ./internal/atomicfile
```

Run on native Windows 10 22H2 and Windows 11:

```powershell
go test .\internal\atomicfile -run 'TestWindowsNativeHandleRenameNeverMakesDestinationMissing|TestWindowsNativeLockedDestination|TestWindowsPublication' -count 20 -v
```

Expected: all tests pass; existing-destination replacement is one rooted atomic namespace operation for observed process-level visibility, absent-destination publication is race-safe, lock failures preserve old content, and documentation makes no untested power-loss claim.

- [ ] **Step 6: Commit accurately scoped durability**

```bash
git add internal/atomicfile docs/platform/windows-durability.md
git commit -m "fix: harden and document Windows file recovery"
```

---

### Task 3: Define the Install Manifest, Source Install, and Skill Verifier

**Files:**
- Create: `internal/install/manifest.go`
- Create: `internal/install/manifest_test.go`
- Create: `scripts/install.sh`
- Create: `scripts/uninstall.sh`
- Create: `scripts/install.ps1`
- Create: `scripts/uninstall.ps1`
- Create: `cmd/verify-skill/main.go`
- Modify: `skill/session-reviewer/SKILL.md`
- Modify: `skill/session-reviewer/scripts/prepare-workflow.sh`
- Modify: `skill/session-reviewer/scripts/prepare-workflow.ps1`
- Modify: `skill/session-reviewer/scripts/apply-proposal.sh`
- Modify: `skill/session-reviewer/scripts/apply-proposal.ps1`
- Create: `test/release/source_install_test.go`

**Interfaces:**
- Consumes: a clean source checkout, Go 1.26.x, existing watcher lifecycle commands, and the current packaged Skill workflows.
- Produces: `install.LoadRoot(*os.Root)`, `install.SaveRoot(*os.Root,Manifest)`, `install.VerifyFiles(Manifest)`, user-scoped source install/uninstall on POSIX and Windows, and `cmd/verify-skill`; it does not consume or create release archives.

- [ ] **Step 1: Write failing manifest, source-install, and Skill verification tests**

```go
func TestSourceInstallWritesVerifiedManifestAndPreservesKnowledgeOnUninstall(t *testing.T) {
	f:=newSourceInstallFixture(t); f.install()
	m:=f.manifest()
	if m.SchemaVersion!=1 || m.InstallMode!="source" || m.BinarySHA256=="" || m.SkillTreeSHA256=="" {t.Fatalf("%#v",m)}
	f.uninstall(false)
	for _,path:=range []string{f.projectLedger,f.vaultReview,f.rawSessions}{if _,err:=os.Stat(path);err!=nil{t.Fatalf("preserved %s: %v",path,err)}}
}

func TestVerifySkillRejectsMissingWrapperAndCheckoutPath(t *testing.T) {
	f:=copySkillFixture(t); f.remove("scripts/apply-proposal.ps1")
	if err:=VerifySkill(f.root);err==nil{t.Fatal("accepted incomplete Skill")}
	f=copySkillFixture(t);f.append("SKILL.md",`/Users/neomei/private`)
	if err:=VerifySkill(f.root);err==nil{t.Fatal("accepted checkout path")}
}
```

- [ ] **Step 2: Run source-install tests before the contracts exist**

Run:

```bash
go test ./internal/install ./test/release -run 'TestSourceInstall|TestVerifySkill' -v
go test ./skill/session-reviewer/tests -count=1
```

Expected: FAIL because the manifest, source installers, and verifier do not exist; existing Skill package tests remain an independent compatibility gate.

- [ ] **Step 3: Define the rooted manifest and exact user locations**

```go
type Manifest struct {
	SchemaVersion int `json:"schema_version"`
	InstallMode string `json:"install_mode"` // source or archive
	ProductVersion,Commit,InstalledAt string
	BinaryPath,BinarySHA256,SkillPath,SkillTreeSHA256 string
	PreviousBinaryPath,PreviousBinarySHA256,PreviousSkillPath,PreviousSkillTreeSHA256 string
	StartupInstalled bool
}
func LoadRoot(root *os.Root)(Manifest,error)
func SaveRoot(root *os.Root,Manifest)error
func VerifyFiles(Manifest)error
```

Load/save are strict, size-bounded, rooted, identity-checked, content-hash verified, mode `0600`, and reject trailing JSON, redirects, absolute manifest entries outside the selected user roots, or inconsistent previous/current pairs. macOS defaults are binary `$HOME/.local/bin/session-reviewer`, Skill `${CODEX_HOME:-$HOME/.codex}/skills/session-reviewer`, manifest `$HOME/.local/share/session-reviewer/install-manifest.json`. Windows defaults are binary `%LOCALAPPDATA%\SessionReviewer\bin\session-reviewer.exe`, Skill `%CODEX_HOME%\skills\session-reviewer` or `%USERPROFILE%\.codex\skills\session-reviewer`, manifest `%LOCALAPPDATA%\SessionReviewer\install-manifest.json`. Installers refuse system locations and never modify machine-level PATH.

- [ ] **Step 4: Implement source-only install, safe uninstall, and the Skill verifier**

Exact source commands are:

```bash
./scripts/install.sh --source . --version 0.1.0 --install-watcher
./scripts/uninstall.sh
```

```powershell
.\scripts\install.ps1 -Source . -Version 0.1.0 -InstallWatcher
.\scripts\uninstall.ps1
```

Source install verifies a clean checkout and Go `1.26.x`, runs `cmd/verify-skill`, builds the native binary with explicit version/commit/time metadata, verifies `version --json`, copies binary and Skill through rooted temporary files, installs the watcher only when requested, and writes the manifest last. Failure removes only newly created hash-matching files and restores the prior watcher state. Source uninstall stops the watcher, removes only manifest-listed files whose current hashes match, reports modified Skill files, preserves machine data/project/vault/raw sessions, and is idempotent. Archive/checksum/rollback flags are usage errors until Task 5.

`cmd/verify-skill` checks frontmatter identity, required references/wrappers, POSIX syntax, PowerShell parseability when available, schema byte identity, relative resource paths, no checkout/user paths, no forbidden model/Git/network capability, and the existing Skill package test contract.

- [ ] **Step 5: Verify in isolated user homes and commit**

Run:

```bash
go run ./cmd/verify-skill ./skill/session-reviewer
env HOME="$(mktemp -d)" CODEX_HOME="$(mktemp -d)" ./scripts/install.sh --source . --version 0.1.0
go test ./internal/install ./test/release ./skill/session-reviewer/tests -v
pwsh -NoProfile -File ./scripts/install.ps1 -Source . -Version 0.1.0 -WhatIf
git diff --check
```

Expected: verifier/tests and isolated POSIX source install/uninstall pass; PowerShell reports only user paths; no archive is needed or produced.

```bash
git add internal/install scripts/install.sh scripts/uninstall.sh scripts/install.ps1 scripts/uninstall.ps1 cmd/verify-skill skill/session-reviewer test/release/source_install_test.go
git commit -m "feat: add source installation and Skill verification"
```

---

### Task 4: Build Deterministic Archives, SBOMs, Checksums, and the License Gate

**Files:**
- Create: `cmd/release-packager/main.go`
- Create: `cmd/release-packager/archive.go`
- Create: `cmd/release-packager/archive_test.go`
- Create: `cmd/release-packager/sbom.go`
- Create: `cmd/release-packager/sbom_test.go`
- Create: `scripts/build-release.sh`
- Create: `scripts/build-release.ps1`
- Create: `cmd/check-release-license/main.go`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: a clean commit/build time, Task 3's source installers and verified Skill, README, and `LICENSE_STATUS.md`; public mode additionally consumes exact tag `v0.1.0` plus owner license authorization.
- Produces: deterministic `dist/session-reviewer_0.1.0_{darwin_amd64,darwin_arm64}.tar.gz`, `dist/session-reviewer_0.1.0_windows_amd64.zip`, `dist/session-reviewer-skill_0.1.0.zip`, one SPDX SBOM per binary plus Skill, and `dist/SHA256SUMS`.

- [ ] **Step 1: Write failing archive/SBOM/license-gate tests**

```go
func TestArchiveIsDeterministicSafeAndComplete(t *testing.T) {
	first:=buildArchive(t,fixedEpoch);second:=buildArchive(t,fixedEpoch)
	if !bytes.Equal(first,second){t.Fatal("same inputs produced different archive bytes")}
	entries:=readArchive(t,first)
	want:=[]string{"session-reviewer","README.md","LICENSE_STATUS.md","scripts/install.sh","scripts/uninstall.sh","skill/session-reviewer/SKILL.md"}
	if diff:=cmp.Diff(want,entryNames(entries));diff!=""{t.Fatal(diff)}
	for _,e:=range entries{if strings.HasPrefix(e.Name,"/")||strings.Contains(e.Name,"..")||e.Mode&0o022!=0{t.Fatalf("unsafe entry %#v",e)}}
}

func TestPublicLicenseGateFailsWithoutOwnerAuthorizedLicense(t *testing.T) {
	err:=checkReleaseLicense(t.TempDir())
	if err==nil||!strings.Contains(err.Error(),"owner-authorized LICENSE is required for public release"){t.Fatalf("error=%v",err)}
}

func TestSBOMNamesExactBuildAndEveryModuleWithoutSecrets(t *testing.T) {
	doc:=buildSBOM(t,"0.1.0",strings.Repeat("a",40))
	if doc.SPDXVersion!="SPDX-2.3"||doc.Name!="session-reviewer_0.1.0_windows_amd64"{t.Fatalf("%#v",doc)}
	if !containsPackage(doc.Packages,"modernc.org/sqlite")||!containsPackage(doc.Packages,"github.com/fsnotify/fsnotify"){t.Fatal("dependency missing")}
	if strings.Contains(marshal(doc),"canary"){t.Fatal("secret leaked")}
}
```

- [ ] **Step 2: Run packager tests and verify no distribution mechanism exists**

Run:

```bash
go test ./cmd/release-packager ./cmd/check-release-license -run 'TestArchive|TestPublicLicense|TestSBOM' -v
```

Expected: FAIL because the packager and gate do not exist.

- [ ] **Step 3: Implement stable archive and checksum contracts**

```go
type Target struct { GOOS,GOARCH,BinaryPath,ArchivePath string }
type PackageMode string
const (PackagePrivateCandidate PackageMode="private_candidate"; PackagePublicRelease PackageMode="public_release")
type PackageOptions struct { Mode PackageMode; Version,Commit string; Epoch time.Time; Root,Dist string; Targets []Target }
type Entry struct { Source,Name string; Mode fs.FileMode }
func BuildArchives(PackageOptions)([]Artifact,error)
type Artifact struct { Name,Path,SHA256 string; Size int64 }
func WriteChecksums(path string,artifacts []Artifact)error
```

Tar/zip entry names use `/`, reject absolute/traversing/link/device entries, set directories `0755`, executables `0755`, documents `0644`, owner/group `0`, and every timestamp to the commit epoch. Gzip uses fixed name/comment and epoch. `SHA256SUMS` sorts filenames bytewise and writes `fmt.Sprintf("%s  %s\n", artifact.SHA256, filepath.Base(artifact.Path))`, where the hash validator requires exactly 64 lowercase hexadecimal characters.

`sbom.go` runs `go list -m -json all` and `go version -m artifact.BinaryPath`, writes SPDX 2.3 JSON with the application package, module packages, module versions/checksums, target OS/arch, build commit, and archive SHA. It rejects replacement paths outside the checkout and never includes environment variables or local absolute paths.

- [ ] **Step 4: Add exact clean-tree build scripts and hard public gate**

Both scripts require exactly one mode. `--private --version 0.1.0` requires a clean tracked/index tree, no untracked files except ignored `dist/`, Go `1.26.x`, and a full lowercase `HEAD`; it explicitly does **not** require or create a tag and records package mode `private_candidate`. `--public --version 0.1.0` requires all private checks plus `git describe --tags --exact-match HEAD` equal to exactly `v0.1.0`, `git rev-list -n1 v0.1.0` equal to `HEAD`, no second exact tag, and a successful public license gate before packaging; it records `public_release`. Unknown/multiple modes are usage errors. Both derive `COMMIT=$(git rev-parse HEAD)` and `BUILD_TIME=$(git show -s --format=%cI HEAD)`, build all three targets with `CGO_ENABLED=0 -trimpath -buildvcs=true`, validate injected metadata by running native binaries where possible and `go version -m` otherwise, then invoke the packager. Tests prove private mode succeeds at an untagged clean commit and public mode rejects an untagged, wrong-tagged, or unauthorized commit.

`go run ./cmd/check-release-license --public` requires a root `LICENSE` regular file, rejects symlink/empty/status-notice content, and requires a separately committed `docs/release/license-authorization.json` with integer version `1`, exact string `authorized_by: repository-owner`, an RFC3339 `authorized_at`, and a 64-character lowercase `license_file_sha256` equal to the file hash. This plan does not create either file. Private mode never calls this command and is successful without authorization; public mode calls it before producing public-mode artifacts or performing any upload.

- [ ] **Step 5: Build the immediate private `v0.1.0` package set twice**

Before the final tag exists, test with an isolated signed input tuple while keeping output private:

```bash
rm -rf dist
./scripts/build-release.sh --version 0.1.0 --private
find dist -maxdepth 1 -type f -print | LC_ALL=C sort
shasum -a 256 -c dist/SHA256SUMS
cp dist/SHA256SUMS /tmp/session-reviewer-first-SHA256SUMS
rm -rf dist
./scripts/build-release.sh --version 0.1.0 --private
diff -u /tmp/session-reviewer-first-SHA256SUMS dist/SHA256SUMS
go run ./cmd/check-release-license --public
```

Expected: archives/SBOMs/checksums are created; checksum verification succeeds; the two manifests match; the final command exits nonzero with `owner-authorized LICENSE is required for public release` and uploads nothing.

- [ ] **Step 6: Commit the reproducible private packager**

```bash
git add cmd/release-packager cmd/check-release-license scripts/build-release.sh scripts/build-release.ps1 .gitignore
git commit -m "build: package release archives and SBOMs"
```

---

### Task 5: Add Verified Archive Install, Upgrade, Rollback, and Purging Uninstall

**Files:**
- Modify: `internal/install/manifest.go`
- Modify: `internal/install/manifest_test.go`
- Modify: `scripts/install.sh`
- Modify: `scripts/uninstall.sh`
- Modify: `scripts/install.ps1`
- Modify: `scripts/uninstall.ps1`
- Create: `test/release/archive_test.go`

**Interfaces:**
- Consumes: Task 3's rooted manifest/source installer/verified Skill, Task 4's deterministic platform and Skill archives, `SHA256SUMS`, and CLI watcher lifecycle.
- Produces: verified archive installation, idempotent same-version reinstall, versioned upgrade with binary/Skill rollback pair, purge-aware uninstall, and native archive-install smoke; source installation remains compatible.

- [ ] **Step 1: Write failing archive upgrade/rollback tests**

```go
func TestUpgradeKeepsPreviousBinaryAndUninstallPreservesKnowledge(t *testing.T) {
	f:=newInstallFixture(t);f.install("0.1.0-dev.1");f.install("0.1.0")
	m:=f.manifest();if m.ProductVersion!="0.1.0"||m.InstallMode!="archive"||m.PreviousBinarySHA256==""||m.PreviousSkillTreeSHA256==""{t.Fatalf("%#v",m)}
	f.uninstall(false)
	for _,path:=range []string{f.projectLedger,f.vaultReview,f.rawSessions}{if _,err:=os.Stat(path);err!=nil{t.Fatalf("preserved path %s: %v",path,err)}}
}

func TestArchiveInstallRejectsChecksumOrMetadataMismatchBeforeStoppingWatcher(t *testing.T) {
	f:=newInstallFixture(t);f.tamperArchive()
	if err:=f.installArchive();err==nil{t.Fatal("accepted tampered archive")}
	if f.watcherStops()!=0||f.manifestExists(){t.Fatal("mutated install before verification")}
}
```

- [ ] **Step 2: Run archive tests before archive flags exist**

Run:

```bash
go test ./internal/install ./test/release -run 'TestUpgrade|TestArchiveInstall' -v
```

Expected: FAIL because Task 3's source installer rejects archive/rollback flags and the archive transaction is not implemented.

- [ ] **Step 3: Extend the manifest transaction without changing its schema**

Use Task 3's `PreviousBinaryPath`/`PreviousBinarySHA256` and `PreviousSkillPath`/`PreviousSkillTreeSHA256` as one inseparable rollback pair. An upgrade refuses a half-pair, verifies current hashes before backup, stores backups beneath rooted `previous/<current-manifest-hash>/`, and writes one new manifest last. Same version/commit/archive hashes are a no-op after verification. A different commit with the same version is rejected.

- [ ] **Step 4: Implement exact archive/rollback/purge flows**

Archive install requires both the platform archive and `session-reviewer-skill_0.1.0.zip`, verifies each exact filename entry in `SHA256SUMS` before extraction, extracts to a private directory with traversal/link/device rejection, runs `version --json`, verifies version/commit against packager metadata, and runs the Skill verifier. Only then may it stop an installed watcher, back up the verified binary/Skill pair, atomically install the new pair, write the manifest last, and reinstall/restart the prior watcher spec. A failure restores the previous manifest/binary/Skill and watcher state.

`uninstall` first calls `watch uninstall`, removes only manifest-listed files whose current hashes match, reports locally modified Skill files rather than deleting them, and preserves data. `--purge-state`/`-PurgeState` additionally removes index, watcher state, queues, merge bases, cursors, and config after printing the exact roots; it still preserves project/vault Markdown and raw sessions. `--rollback` reinstalls the manifest's previous verified binary and corresponding Skill backup, then runs `doctor` and `sync --dry-run`.

- [ ] **Step 5: Verify source compatibility and both archive installer families**

Run:

```bash
./scripts/build-release.sh --version 0.1.0 --private
env HOME="$(mktemp -d)" CODEX_HOME="$(mktemp -d)" ./scripts/install.sh --archive dist/session-reviewer_0.1.0_darwin_$(go env GOARCH).tar.gz --skill-archive dist/session-reviewer-skill_0.1.0.zip --checksums dist/SHA256SUMS
go test ./internal/install ./test/release -v
pwsh -NoProfile -File ./scripts/install.ps1 -Archive dist/session-reviewer_0.1.0_windows_amd64.zip -SkillArchive dist/session-reviewer-skill_0.1.0.zip -Checksums dist/SHA256SUMS -WhatIf
git diff --check
```

Expected: private archives build without a tag, isolated POSIX archive install/rollback/uninstall pass, Task 3 source-install tests remain green, and PowerShell shows only user-level paths; native Windows archive smoke is Task 7.

- [ ] **Step 6: Commit verified archive installation**

```bash
git add internal/install scripts/install.sh scripts/uninstall.sh scripts/install.ps1 scripts/uninstall.ps1 test/release/archive_test.go
git commit -m "feat: install and roll back verified release archives"
```

---

### Task 6: Build Non-Committed Real-Session E2E Scenarios A-E and Redacted Receipts

**Files:**
- Create: `test/e2e/real/config.go`
- Create: `test/e2e/real/runner.go`
- Create: `test/e2e/real/runner_test.go`
- Create: `test/e2e/real/receipt.go`
- Create: `test/e2e/real/receipt_test.go`
- Create: `scripts/run-real-e2e.sh`
- Create: `scripts/run-real-e2e.ps1`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: external real session A/B JSONL paths, external validated proposal A/B JSON paths produced through the Skill, a private temporary project/vault, and the exact release binary.
- Produces: `real.Run(context.Context,real.Config) (real.Receipt,error)` covering scenarios A-E and private redacted JSON receipts under `filepath.Join("artifacts","private-e2e",runtime.GOOS+"-"+runtime.GOARCH)`.

- [ ] **Step 1: Write failing harness privacy and scenario-completeness tests**

```go
func TestReceiptContainsEveryScenarioButNoPathsOrNarrative(t *testing.T) {
	r:=syntheticReceipt(t)
	if diff:=cmp.Diff([]string{"A","B","C","D","E"},scenarioNames(r));diff!=""{t.Fatal(diff)}
	body:=mustJSON(t,r)
	for _,forbidden:=range []string{t.TempDir(),"/Users/",`C:\Users\`,"sk-canary-","decision rationale text"}{if bytes.Contains(body,[]byte(forbidden)){t.Fatalf("receipt leaked %q",forbidden)}}
	for _,s:=range r.Scenarios{if !s.Passed||len(s.Assertions)==0{t.Fatalf("incomplete %#v",s)}}
}

func TestRealInputsMustBeExternalIgnoredRegularFiles(t *testing.T) {
	root:=newGitFixture(t);committed:=filepath.Join(root,"testdata","session.jsonl");writeFile(t,committed)
	err:=ValidateInputs(Config{Checkout:root,SessionA:committed})
	if err==nil||!strings.Contains(err.Error(),"real input is tracked or inside checkout"){t.Fatalf("error=%v",err)}
}
```

- [ ] **Step 2: Run harness tests before its contract exists**

Run:

```bash
go test ./test/e2e/real -run 'TestReceipt|TestRealInputs' -v
```

Expected: FAIL because `Config`, `Receipt`, validation, and scenario runners do not exist.

- [ ] **Step 3: Define external-input and receipt schemas**

```go
type Config struct {
	Binary,Checkout,SessionA,SessionB,ProposalA,ProposalB,OutputDir string
	GOOS,GOARCH string
	KeepWorkspace bool
}
type Assertion struct { Name string `json:"name"`; Passed bool `json:"passed"`; ObservedHash string `json:"observed_hash,omitempty"` }
type ScenarioReceipt struct { Name string `json:"name"`; Passed bool `json:"passed"`; DurationMS int64 `json:"duration_ms"`; Assertions []Assertion `json:"assertions"` }
type Receipt struct {
	SchemaVersion int `json:"schema_version"`
	ProductVersion,Commit,GOOS,GOARCH,OSVersion,BinarySHA256,StartedAt,FinishedAt string
	SessionInputHashes,ProposalInputHashes []string
	Scenarios []ScenarioReceipt
	GitBeforeHash,GitAfterHash string
	RawInputsCommitted bool
}
func Run(context.Context,Config)(Receipt,error)
```

Validation requires every input be an external absolute regular file, not under checkout/data/project/vault, not tracked by Git, and private to the current user where the OS exposes permissions. Receipts include SHA-256 input identity but no basename/path, raw text, entity narrative, environment, username, hostname, vault name, or notification body. `.gitignore` excludes `/artifacts/private-e2e/`, `/test/e2e/private-inputs/`, and `/dist/`.

- [ ] **Step 4: Implement the exact scenario assertions**

- Scenario A: apply proposal A for checkpoint 1, append/use later real session evidence and proposal B, assert second packet starts after accepted cursor, unchanged rerun has no diff, timeline/diagrams gain only new accepted entities.
- Scenario B: edit an accepted decision explanation and next action in the private vault, run sync then resume, assert project receives both edits and recovery card names stop point/next action.
- Scenario C: associate two real sessions, proposal B supersedes proposal A's decision, run history, assert original choice, discovered problem, replacement, current state, and unresolved themes appear as linked entities rather than concatenated summaries.
- Scenario D: edit the same narrative field differently in project/vault plus a separate non-conflicting entity, sync, assert both conflict versions and base hash exist, unrelated entity synchronizes, resolution closes the note and is idempotent.
- Scenario E: on Windows under a generated Unicode root, install watcher no-admin, execute checkpoint/edit/sync/resume/history/conflict resolution, restart watcher, lock a target to force queueing, unlock/drain, uninstall twice, and assert documents semantically match a normalized macOS receipt.

The runner snapshots Git porcelain and tracked content hashes before/after; generated ledger changes may be uncommitted, but `.git`, index, refs, HEAD, and staging index must remain unchanged.

- [ ] **Step 5: Run synthetic harness tests and document private invocation**

```bash
go test ./test/e2e/real -v
./scripts/run-real-e2e.sh \
  --binary ./dist/unpacked/session-reviewer \
  --session-a /private/e2e/session-a.jsonl \
  --session-b /private/e2e/session-b.jsonl \
  --proposal-a /private/e2e/proposal-a.json \
  --proposal-b /private/e2e/proposal-b.json \
  --output ./artifacts/private-e2e/darwin-arm64
git status --short --ignored | rg 'artifacts/private-e2e/.*!!'
```

Expected: harness unit tests pass; real run is performed only when the four external files exist; generated receipt passes privacy validation and appears ignored (`!!`), never staged.

- [ ] **Step 6: Commit the harness, never its private inputs or receipts**

```bash
git add test/e2e/real scripts/run-real-e2e.sh scripts/run-real-e2e.ps1 .gitignore
git commit -m "test: add private real session acceptance harness"
```

---

### Task 7: Collect Native Minimum-OS macOS and Windows Evidence

**Files:**
- Create: `scripts/collect-platform-receipt.sh`
- Create: `scripts/collect-platform-receipt.ps1`
- Create: `.github/workflows/release-native.yml`
- Create: `docs/release/native-hosts.md`
- Create: `docs/release/checklist.md`

**Interfaces:**
- Consumes: exact `v0.1.0` candidate archive/checksum, private real E2E inputs on controlled hosts, and scenario runner.
- Produces: four private native receipt bundles keyed by exact commit/binary hash and one aggregate gate: macOS 13 Intel, macOS 13 Apple Silicon, Windows 10 22H2 x64, Windows 11 x64.

- [ ] **Step 1: Write failing platform receipt validation tests**

```go
func TestNativeReceiptGateRequiresEveryMinimumOSAndSameBinarySet(t *testing.T) {
	receipts:=[]Receipt{darwin13AMD64(),darwin13ARM64(),windows10_22H2(),windows11()}
	if err:=ValidateNativeSet(receipts,"0.1.0",strings.Repeat("a",40),expectedArchiveHashes());err!=nil{t.Fatal(err)}
	receipts[2].OSVersion="Windows 10 21H2"
	if err:=ValidateNativeSet(receipts,"0.1.0",strings.Repeat("a",40),expectedArchiveHashes());err==nil{t.Fatal("accepted unsupported Windows")}
}
```

- [ ] **Step 2: Run native-set validator test before collector/workflow exist**

Run:

```bash
go test ./test/e2e/real -run TestNativeReceiptGate -v
```

Expected: FAIL because native receipt set validation does not exist.

- [ ] **Step 3: Implement exact platform collectors**

macOS collector records `sw_vers -productVersion`, `uname -m`, `sysctl -n kern.osversion`, release binary SHA-256, `version --json`, LaunchAgent lifecycle results, and scenario receipt hash. It rejects product versions below 13 and architecture outside `x86_64|arm64`.

Windows collector records `[Environment]::OSVersion.Version`, registry `DisplayVersion` and `CurrentBuild`, `$env:PROCESSOR_ARCHITECTURE`, binary SHA-256, `version --json`, limited Task Scheduler lifecycle, and scenario receipt hash. It accepts Windows 10 only when display version is `22H2` and build is at least `19045`, or Windows 11 build at least `22000`; it requires `AMD64`.

Both collectors write redacted JSON and `receipt.sha256`, verify the receipt privacy schema, and never include user/host/path/environment data.

- [ ] **Step 4: Add a self-hosted native workflow with exact labels**

```yaml
name: release-native
on: { workflow_dispatch: { inputs: { commit: { required: true }, candidate_run_id: { required: true } } } }
permissions: { contents: read, actions: read }
jobs:
  accept:
    strategy:
      fail-fast: false
      matrix:
        include:
          - { label: macos-13-intel-x64, os: darwin, arch: amd64 }
          - { label: macos-13-apple-silicon-arm64, os: darwin, arch: arm64 }
          - { label: windows-10-22h2-x64, os: windows, arch: amd64 }
          - { label: windows-11-x64, os: windows, arch: amd64 }
    runs-on: [self-hosted, session-reviewer-release, '${{ matrix.label }}']
```

Each job downloads the candidate by run ID, verifies `SHA256SUMS`, runs installer twice, doctor, scenarios A-E against host-local protected inputs, watcher restart, rollback, uninstall twice, collector, and uploads the redacted receipt bundle as a private GitHub Actions artifact retained 30 days. It never checks out or uploads the private input directory.

- [ ] **Step 5: Execute and validate all four native runs**

Run after candidate artifacts exist:

```bash
CANDIDATE_RUN_ID="$(gh run list --workflow release.yml --branch "$(git branch --show-current)" --status success --limit 1 --json databaseId --jq '.[0].databaseId')"
gh workflow run release-native.yml -f commit="$(git rev-parse HEAD)" -f candidate_run_id="$CANDIDATE_RUN_ID"
NATIVE_RUN_ID="$(gh run list --workflow release-native.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch "$NATIVE_RUN_ID" --exit-status
gh run download "$NATIVE_RUN_ID" -p 'native-receipt-*' -D artifacts/private-e2e/native-receipts
go run ./cmd/release-packager validate-native --receipts artifacts/private-e2e/native-receipts --version 0.1.0 --commit "$(git rev-parse HEAD)" --checksums dist/SHA256SUMS
```

Expected: both computed run IDs are non-empty decimal values; workflow and validator exit 0; all receipts name supported minimum OS/arch, exact commit, matching binary hashes, and scenarios A-E pass.

- [ ] **Step 6: Commit native evidence automation and release checklist**

```bash
git add scripts/collect-platform-receipt.sh scripts/collect-platform-receipt.ps1 .github/workflows/release-native.yml docs/release/native-hosts.md docs/release/checklist.md test/e2e/real
git commit -m "test: gate release on native platform receipts"
```

---

### Task 8: Add Release Security, Canary, Memory, Performance, and Recovery Regressions

**Files:**
- Create: `test/release/security_test.go`
- Create: `test/release/performance_test.go`
- Create: `test/release/recovery_test.go`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: all persistence stores, large synthetic sessions, rapid watcher events, sync conflicts/queues, corrupt index, locked Windows files, and built archives.
- Produces: always-on release regression package plus native performance fields in receipts.

- [ ] **Step 1: Write failing whole-product canary and resource-budget tests**

```go
func TestReleaseCanaryAbsentFromEveryPersistenceAndArtifact(t *testing.T) {
	canaries:=[]string{"sk-canary-123456789012345678901234567890","postgres://canary:secret@db/app","-----BEGIN PRIVATE KEY-----\ncanary"}
	f:=exerciseEveryWorkflow(t,canaries)
	for _,root:=range []string{f.project,f.vault,f.data,f.logs,f.receipts,f.dist}{scanRegularFiles(t,root,func(path string,body []byte){for _,c:=range canaries{if bytes.Contains(body,[]byte(c)){t.Fatalf("canary in %s",safeClass(path))}}})}
}

func TestReleaseMemoryAndLatencyBudgets(t *testing.T) {
	metrics:=runSyntheticReleaseScale(t,Scale{SessionBytes:100<<20,Entities:10_000,FileEvents:100_000,SyncEntities:1_000})
	if metrics.PeakLiveHeapBytes>128<<20{t.Fatalf("peak live heap %d",metrics.PeakLiveHeapBytes)}
	if metrics.Parse>30*time.Second||metrics.IndexRebuild>10*time.Second||metrics.History>5*time.Second||metrics.WatcherStorm>30*time.Second||metrics.Sync>15*time.Second{t.Fatalf("budgets %#v",metrics)}
}
```

- [ ] **Step 2: Write crash/recovery regressions and run the new package**

```go
func TestReleaseRecoversEveryDurableBoundary(t *testing.T) {
	for _,boundary:=range []string{"apply_after_render","apply_after_files","cursor_before_commit","sync_after_project","sync_after_vault","base_before_commit","queue_after_reschedule","index_before_swap","watcher_before_state"}{
		t.Run(boundary,func(t *testing.T){f:=crashAt(t,boundary);f.restart();f.assertOldOrNewComplete();f.assertNoLostEdit();f.assertIdempotentRetry();f.assertCanariesAbsent()})
	}
}
```

Run:

```bash
go test ./test/release -run 'TestReleaseCanary|TestReleaseMemory|TestReleaseRecovers' -v
```

Expected: FAIL until Tasks 1–7 expose every listed store to `exerciseEveryWorkflow`, keep streaming live heap below the stated bound, and wire every listed crash hook to recovery; after those exact contracts exist, PASS.

- [ ] **Step 3: Add fuzz/race/vulnerability gates with exact budgets**

Add CI steps:

```yaml
      - run: go test ./test/release -run TestReleaseCanaryAbsentFromEveryPersistenceAndArtifact -count=2
      - run: go test ./test/release -run TestReleaseMemoryAndLatencyBudgets -v
      - run: go test -race ./...
      - run: go test ./internal/session -fuzz FuzzStreamReader -fuzztime 30s
      - run: go test ./internal/ledger -fuzz FuzzParseEntity -fuzztime 30s
      - run: go test ./internal/sync -fuzz FuzzThreeWayMerge -fuzztime 30s
      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
```

Keep generated large files in test temp directories. `PeakLiveHeapBytes` is **not** derived from cumulative `runtime.MemStats.TotalAlloc`. A sampler records `/memory/classes/heap/objects:bytes` through `runtime/metrics` before the phase and every 10 ms until the worker exits, and reports the maximum baseline-adjusted live-object bytes; the worker must run long enough to produce at least two samples or the measurement fails. Separately record `TotalAllocatedBytes` as the before/after `runtime.MemStats.TotalAlloc` delta for allocation-churn diagnosis, with no peak label and no substitution for the 128 MiB live-heap gate. Wall time surrounds the same isolated phase; native receipts record both named values. A CI timing/memory breach fails and prints metrics, never event/entity content.

- [ ] **Step 4: Prove manual parity and watcher security invariants**

Add a release test that disables/uninstalls the watcher, then completes checkpoint/apply, sync, resume, history, conflict resolution, index rebuild, install rollback, and uninstall. Static dependency tests fail if watcher imports proposal/apply/repository-Git/model/network packages. A fake command runner records every watcher child process; allowed executables are only platform notification/startup commands, never `git`, `curl`, `powershell` scripts containing network access, or model clients.

- [ ] **Step 5: Run the complete security/recovery gate repeatedly**

Run:

```bash
gofmt -w test/release
go test ./test/release -v -count=3
go test -race ./...
go test ./internal/session -fuzz FuzzStreamReader -fuzztime 30s
go test ./internal/ledger -fuzz FuzzParseEntity -fuzztime 30s
go test ./internal/sync -fuzz FuzzThreeWayMerge -fuzztime 30s
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
```

Expected: every command exits 0; no known reachable vulnerability is reported; all canaries are absent; resource budgets pass three times.

- [ ] **Step 6: Commit release regressions**

```bash
git add test/release .github/workflows/ci.yml
git commit -m "test: add release security and recovery gates"
```

---

### Task 9: Add Gated Release CI, Final Documentation, and Rollback Drill

**Files:**
- Create: `.github/workflows/release.yml`
- Create: `test/release/release_manifest_test.go`
- Create: `docs/release/rollback.md`
- Modify: `docs/release/checklist.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: either a clean untagged/manual private-candidate commit or a clean exact `v0.1.0` public tag, candidate/native workflow receipts, deterministic artifacts/SBOMs/checksums, license authorization gate, and installer rollback.
- Produces: private candidate artifacts today and, only after separate explicit license authorization, an immutable GitHub Release with the exact verified asset set.

- [ ] **Step 1: Write failing release-manifest and rollback-drill tests**

```go
func TestReleaseManifestSeparatesPrivateCandidateAndPublicExactTag(t *testing.T) {
	private:=completeManifest(t);private.Mode="private_candidate";private.ExactTag="";private.LicenseAuthorized=false
	if err:=ValidateReleaseManifest(private);err!=nil{t.Fatalf("private candidate failed: %v",err)}
	private.NativeReceipts=private.NativeReceipts[:3];if err:=ValidateReleaseManifest(private);err==nil{t.Fatal("accepted missing native receipt")}
	public:=completeManifest(t);public.Mode="public_release";public.ExactTag="v0.1.0";public.LicenseAuthorized=false
	if err:=ValidateReleaseManifest(public);err==nil{t.Fatal("accepted unlicensed public release")}
	public.LicenseAuthorized=true;public.ExactTag="v0.1.0-rc.1";if err:=ValidateReleaseManifest(public);err==nil{t.Fatal("accepted wrong public tag")}
}

func TestPrivateWorkflowSucceedsWithoutLicenseAndSkipsPublish(t *testing.T) {
	d:=DecideWorkflow(WorkflowInput{Event:"workflow_dispatch",Version:"0.1.0",Commit:strings.Repeat("a",40),LicenseAuthorized:false})
	if d.Mode!="private_candidate"||!d.Build||!d.Assemble||!d.NativeGate||d.Publish||d.Conclusion!="success"{t.Fatalf("%#v",d)}
	for _,tag:=range []string{"", "v0.1", "v0.1.0-rc.1", "v0.1.0^{}"} {
		if got:=DecideWorkflow(WorkflowInput{Event:"push_tag",Tag:tag,Version:"0.1.0",Commit:strings.Repeat("a",40),LicenseAuthorized:true});got.Publish{t.Fatalf("tag %q published",tag)}
	}
}

func TestRollbackDrillRestoresPreviousVersionWithoutKnowledgeLoss(t *testing.T) {
	f:=installedReleaseFixture(t,"0.1.0-rc.1");before:=f.knowledgeHashes();f.upgrade("0.1.0");f.rollback()
	if got:=f.version();got!="0.1.0-rc.1"{t.Fatalf("version=%s",got)}
	if diff:=cmp.Diff(before,f.knowledgeHashes());diff!=""{t.Fatal(diff)}
	f.syncDryRun();f.rebuildIndex();f.doctorHealthy()
}
```

- [ ] **Step 2: Run final-manifest tests before workflow/documentation exist**

Run:

```bash
go test ./test/release -run 'TestReleaseManifest|TestRollbackDrill' -v
```

Expected: FAIL because aggregate manifest validation and complete rollback drill do not exist.

- [ ] **Step 3: Add build/assemble/publish jobs with least privilege**

`release.yml` has two mutually exclusive modes. Manual `workflow_dispatch` is `private_candidate`: it checks out the supplied commit, requires clean commit identity and version `0.1.0` but no tag/license, runs Task 4's `--private` build, and must conclude **success** with the publish job skipped. A `push` of tag `v0.1.0` is `public_release`: it requires the ref name exactly `v0.1.0`, tag target exactly `HEAD`, no other exact tag, and Task 4's `--public` preflight. Build jobs have `contents: read`, run the full test/vet/race/vulnerability suite, build one target each, and upload binary/SBOM fragments. An assemble job downloads only same-run artifacts, validates mode/build metadata and archive contents, creates deterministic archives/Skill/SBOMs/`SHA256SUMS`, and uploads a private `candidate-0.1.0` artifact in either mode.

A native-gate job downloads the four receipt bundles by explicitly supplied successful `release-native` run ID and validates commit plus artifact hashes. The final `publish` job has `contents: write`, depends on build/assemble/native-gate, and has the exact condition `mode == 'public_release'`; private mode therefore skips the job without evaluating a missing-license command and the workflow remains green. In public mode the job reruns `go run ./cmd/check-release-license --public`, rejects any pre-existing mismatched tag/release/asset, creates a non-draft non-prerelease GitHub Release, and uploads exactly:

```text
session-reviewer_0.1.0_darwin_amd64.tar.gz
session-reviewer_0.1.0_darwin_arm64.tar.gz
session-reviewer_0.1.0_windows_amd64.zip
session-reviewer-skill_0.1.0.zip
session-reviewer_0.1.0_darwin_amd64.spdx.json
session-reviewer_0.1.0_darwin_arm64.spdx.json
session-reviewer_0.1.0_windows_amd64.spdx.json
session-reviewer-skill_0.1.0.spdx.json
SHA256SUMS
LICENSE
```

Because this plan does not add `LICENSE`/authorization, a public-tag run fails safely before any GitHub write. A manual private-candidate run builds, gates, uploads its private workflow artifact, skips `publish`, and succeeds.

- [ ] **Step 4: Document install, support, recovery, uninstall, and rollback exactly**

README must show checksum verification before extraction, archive/source installation on both OSes, Skill location, PATH behavior, all help/doctor/watcher commands, no-admin scope, supported OS floor, raw-session/privacy boundary, watcher-disabled manual parity, Windows durability wording, uninstall preservation, and current license/publication status.

`docs/release/rollback.md` executes:

```bash
session-reviewer watch uninstall
./scripts/install.sh --rollback
session-reviewer version --json
session-reviewer doctor --json
session-reviewer sync --dry-run
session-reviewer index rebuild
session-reviewer watch install
```

and Windows equivalents with `uninstall.ps1`/`install.ps1 -Rollback`. If binary startup fails, copy the manifest-verified previous binary from the manifest path, verify its SHA, invoke it with absolute path, leave watcher disabled, run sync dry-run, and preserve all local state for diagnosis. Rollback never reverses Markdown automatically.

- [ ] **Step 5: Perform the private final release rehearsal**

Run from a clean exact candidate commit before creating a public tag:

```bash
go test ./...
go test -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
./scripts/build-release.sh --version 0.1.0 --private
shasum -a 256 -c dist/SHA256SUMS
go test ./test/release -v -count=3
go run ./cmd/verify-skill ./skill/session-reviewer
go run ./cmd/release-packager validate-native --receipts artifacts/private-e2e/native-receipts --version 0.1.0 --commit "$(git rev-parse HEAD)" --checksums dist/SHA256SUMS
./scripts/install.sh --archive dist/session-reviewer_0.1.0_darwin_$(go env GOARCH).tar.gz --checksums dist/SHA256SUMS
session-reviewer doctor --json
./scripts/install.sh --rollback
./scripts/uninstall.sh
go run ./cmd/check-release-license --public
git diff --check
git status --short
```

Expected: every technical gate before the license check passes; the license check intentionally exits nonzero and performs no external write; diff check is silent; status is clean because `dist/`, private inputs, and receipts are ignored.

- [ ] **Step 6: Commit release automation and documentation**

```bash
git add .github/workflows/release.yml test/release/release_manifest_test.go docs/release/checklist.md docs/release/rollback.md README.md
git commit -m "build: gate SessionReviewer release publication"
```

## `v0.1.0` Completion and Publication Gate

Technical hardening is complete only when a fresh private rehearsal proves:

```bash
go test ./...
go test -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
./scripts/build-release.sh --version 0.1.0 --private
shasum -a 256 -c dist/SHA256SUMS
go test ./test/release -v -count=3
go run ./cmd/verify-skill ./skill/session-reviewer
go run ./cmd/release-packager validate-native --receipts artifacts/private-e2e/native-receipts --version 0.1.0 --commit "$(git rev-parse HEAD)" --checksums dist/SHA256SUMS
git status --short
```

Required evidence:

- deterministic archive hashes from two builds;
- version/commit/date embedded in all binaries;
- SPDX SBOM and checksum coverage for every binary and Skill artifact;
- source and archive install, upgrade, rollback, watcher restart, uninstall twice, and purge-preservation tests;
- non-committed real-session scenarios A-E on macOS and Windows;
- native macOS 13 Intel/ARM, Windows 10 22H2 x64, and Windows 11 x64 receipts for the exact candidate hashes;
- Windows lock/crash receipts with the accurately scoped recovery guarantee;
- canary absence across every persistence/artifact surface;
- 100 MiB session, 10,000-entity index/history, 100,000-event watcher, and 1,000-entity sync budgets;
- corrupt SQLite quarantine/rebuild, interrupted apply/sync, durable queue restart, and previous-version rollback;
- complete Skill archive and actionable help/doctor/docs;
- clean working tree and exact commit identity for a private candidate; public mode additionally requires exact `v0.1.0` tag/commit identity.

Public publication additionally requires an explicit repository-owner licensing decision in a separate authorized change that adds `LICENSE` and `docs/release/license-authorization.json`. Until then, do not create/push `v0.1.0`, do not create a public GitHub Release, and report: private release candidate technically verified; public distribution blocked by missing owner-authorized license grant.
