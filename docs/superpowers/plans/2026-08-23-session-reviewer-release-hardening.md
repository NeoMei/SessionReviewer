# SessionReviewer Release Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the completed SessionReviewer engine and Skill into traceable `v0.1.0` source and binary distributions with truthful platform durability, idempotent no-admin installation, actionable CLI help, real private macOS/Windows acceptance receipts, security/performance/recovery gates, checksums, SBOMs, release CI, documentation, and a tested rollback path.

**Architecture:** Build metadata is injected into one binary from an exact tag/commit, while a Go release packager creates deterministic per-platform archives, a separate Skill archive, SPDX SBOMs, and one checksum manifest without relying on host archive quirks. Installation remains user-scoped and manifest-driven; private real-session E2E runs consume external ignored inputs and emit redacted receipts, while public release publication remains mechanically blocked until the repository owner explicitly authorizes and adds a license grant.

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
- Windows replacement is described according to its tested guarantees; the multi-rename backup protocol must never be called atomic or power-loss equivalent to POSIX rename.
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
internal/atomicfile/durability.go            Named/testable platform guarantees
internal/atomicfile/durability_test.go       Crash-point and recovery matrix
internal/atomicfile/replace_windows.go       Retried recoverable backup protocol, accurately scoped
internal/atomicfile/replace_windows_logic.go Recovery decisions and failure classes
docs/platform/windows-durability.md          Exact Windows guarantees and limitations
cmd/release-packager/main.go                 Deterministic archive/checksum/SBOM command
cmd/release-packager/archive.go              Stable tar.gz/zip creation
cmd/release-packager/archive_test.go         Archive contents, modes, timestamps, traversal tests
cmd/release-packager/sbom.go                 SPDX 2.3 JSON module/build inventory
cmd/release-packager/sbom_test.go            SPDX validation and no-secret tests
scripts/build-release.sh                     Clean-tree multi-target release build
scripts/build-release.ps1                    Native Windows release build/verification
cmd/check-release-license/main.go            Hard public-release license gate
scripts/install.sh                            Source/archive macOS install and upgrade
scripts/uninstall.sh                          Manifest-driven macOS uninstall/purge
scripts/install.ps1                           Source/archive Windows install and upgrade
scripts/uninstall.ps1                         Manifest-driven Windows uninstall/purge
internal/install/manifest.go                  Installed files, previous binary, hashes, version
internal/install/manifest_test.go             Upgrade/rollback/preservation invariants
cmd/verify-skill/main.go                      Skill frontmatter/script/package contract
skill/session-reviewer/SKILL.md               Packaged Codex workflow instructions
skill/session-reviewer/scripts/*              Relative-path POSIX/PowerShell workflow wrappers
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

`docs/release/licensing.md` states that implementation agents must not select a license/holder, `cmd/check-release-license` is the mechanical gate added in Task 3, and public release resumes only after explicit owner authorization in a separate change.

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
- Create: `internal/atomicfile/durability.go`
- Create: `internal/atomicfile/durability_test.go`
- Modify: `internal/atomicfile/replace_windows.go`
- Modify: `internal/atomicfile/replace_windows_logic.go`
- Modify: `internal/atomicfile/replace_windows_test.go`
- Modify: `internal/atomicfile/write.go`
- Modify: `internal/atomicfile/write_test.go`
- Create: `docs/platform/windows-durability.md`

**Interfaces:**
- Consumes: existing `atomicfile.Write`, `WriteRoot`, `BackupPath`, and every config/cursor/base/queue/state loader.
- Produces: `atomicfile.GuaranteeFor(goos string) atomicfile.Guarantee`, `atomicfile.RecoverRoot(*os.Root,string) error`, classified Windows lock/recovery errors, and tested documentation of exactly what survives process crash versus power loss.

- [ ] **Step 1: Write failing durability and crash-state matrix tests**

```go
func TestWindowsGuaranteeIsCrashRecoverableButNotClaimedAtomic(t *testing.T) {
	g:=GuaranteeFor("windows")
	if !g.TempDataFlushed || !g.ProcessCrashRecoverable || !g.BackupPreservesPrevious {t.Fatalf("%#v",g)}
	if g.AtomicReplacement || g.DirectoryEntryPowerLossGuaranteed {t.Fatalf("overclaim: %#v",g)}
}

func TestRecoverRootCoversEveryInterruptedWindowsState(t *testing.T) {
	for _,tc:=range []struct{name string;destination,backup,temp bool;want string}{
		{"old only",true,false,false,"old"},
		{"backup only",false,true,false,"old"},
		{"new and backup",true,true,false,"new"},
		{"old and temp",true,false,true,"old"},
	}{t.Run(tc.name,func(t *testing.T){root:=buildCrashState(t,tc);if err:=RecoverRoot(root,"state.json");err!=nil{t.Fatal(err)};if got:=readState(t,root);got!=tc.want{t.Fatalf("got %q",got)}})}
}
```

- [ ] **Step 2: Run durability tests and expose the undocumented guarantee**

Run:

```bash
go test ./internal/atomicfile -run 'TestWindowsGuarantee|TestRecoverRoot' -v
```

Expected: FAIL because the named guarantee and general recovery entry point do not exist.

- [ ] **Step 3: Add the exact guarantee and reusable recovery API**

```go
type Guarantee struct {
	TempDataFlushed bool
	AtomicReplacement bool
	ProcessCrashRecoverable bool
	BackupPreservesPrevious bool
	DirectoryEntryPowerLossGuaranteed bool
}
func GuaranteeFor(goos string) Guarantee {
	if goos=="windows" { return Guarantee{TempDataFlushed:true,ProcessCrashRecoverable:true,BackupPreservesPrevious:true} }
	return Guarantee{TempDataFlushed:true,AtomicReplacement:true,ProcessCrashRecoverable:true,BackupPreservesPrevious:true}
}
func RecoverRoot(root *os.Root,name string) error { return recoverReplacement(root,name,BackupPath(name)) }
```

The Windows implementation retains the pinned `os.Root` multi-rename protocol because converting root-relative paths back to names for `ReplaceFileW` would weaken the path-swap boundary. It retries sharing/lock violations with delays `50ms, 100ms, 200ms, 400ms, 800ms`; after that, callers persist the existing sync queue item. It does not label the sequence atomic. A valid destination wins when destination and backup both exist; backup-only restores the prior version; a temp is never promoted after caller identity is lost.

- [ ] **Step 4: Apply recovery consistently to every durable loader**

Before reading config, cursor, merge base, queue item, watcher state, install manifest, apply receipt, or index swap marker, loaders call `RecoverRoot` under their existing process lock. Read-only commands may consult a valid backup but must not mutate recovery state. Add injected crash hooks after temp sync, after destination-to-backup, after temp-to-destination, and before backup removal; helper subprocess tests terminate at each hook and assert the next writer recovers deterministically.

`docs/platform/windows-durability.md` must explicitly say:

- the temporary file's data is flushed before installation;
- a process crash leaves a recoverable old or new complete file under tested states;
- open-file sharing violations retry and then queue without discarding either side;
- the sequence is not a single atomic replace and does not claim power-loss directory-entry durability;
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
go test .\internal\atomicfile -run 'TestRecoverRoot|TestWindowsNativeLockedDestination|TestCrashHelper' -count 20 -v
```

Expected: all tests pass; lock failures preserve old and queued new content; documentation never uses “atomic replacement” for the Windows multi-rename path.

- [ ] **Step 6: Commit accurately scoped durability**

```bash
git add internal/atomicfile docs/platform/windows-durability.md
git commit -m "fix: harden and document Windows file recovery"
```

---

### Task 3: Build Deterministic Archives, SBOMs, Checksums, and the License Gate

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
- Consumes: exact clean tag/commit/build time, built binaries, README, `LICENSE_STATUS.md`, install/uninstall scripts, and packaged Skill.
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
type PackageOptions struct { Version,Commit string; Epoch time.Time; Root,Dist string; Targets []Target }
type Entry struct { Source,Name string; Mode fs.FileMode }
func BuildArchives(PackageOptions)([]Artifact,error)
type Artifact struct { Name,Path,SHA256 string; Size int64 }
func WriteChecksums(path string,artifacts []Artifact)error
```

Tar/zip entry names use `/`, reject absolute/traversing/link/device entries, set directories `0755`, executables `0755`, documents `0644`, owner/group `0`, and every timestamp to the commit epoch. Gzip uses fixed name/comment and epoch. `SHA256SUMS` sorts filenames bytewise and writes `fmt.Sprintf("%s  %s\n", artifact.SHA256, filepath.Base(artifact.Path))`, where the hash validator requires exactly 64 lowercase hexadecimal characters.

`sbom.go` runs `go list -m -json all` and `go version -m artifact.BinaryPath`, writes SPDX 2.3 JSON with the application package, module packages, module versions/checksums, target OS/arch, build commit, and archive SHA. It rejects replacement paths outside the checkout and never includes environment variables or local absolute paths.

- [ ] **Step 4: Add exact clean-tree build scripts and hard public gate**

`scripts/build-release.sh` verifies `git diff --quiet`, `git diff --cached --quiet`, no untracked files except ignored `dist/`, exact tag `v0.1.0`, tag target equals `HEAD`, and Go `1.26.x`; it derives `COMMIT=$(git rev-parse HEAD)` and `BUILD_TIME=$(git show -s --format=%cI HEAD)`, builds all three targets with `CGO_ENABLED=0 -trimpath -buildvcs=true`, validates injected metadata by running native binaries where possible and `go version -m` otherwise, then invokes the packager.

`go run ./cmd/check-release-license --public` requires a root `LICENSE` regular file, rejects symlink/empty/status-notice content, and requires a separately committed `docs/release/license-authorization.json` with integer version `1`, exact string `authorized_by: repository-owner`, an RFC3339 `authorized_at`, and a 64-character lowercase `license_file_sha256` equal to the file hash. This plan does not create either file. `release.yml` calls this before any public upload; therefore current publication fails safely and private archives remain available for acceptance only.

- [ ] **Step 5: Build the immediate private `v0.1.0` package set twice**

Before the final tag exists, test with an isolated signed input tuple while keeping output private:

```bash
rm -rf dist
go run ./cmd/release-packager --version 0.1.0 --commit "$(git rev-parse HEAD)" --epoch "$(git show -s --format=%cI HEAD)" --input ./build --dist ./dist --private
find dist -maxdepth 1 -type f -print | LC_ALL=C sort
shasum -a 256 -c dist/SHA256SUMS
cp dist/SHA256SUMS /tmp/session-reviewer-first-SHA256SUMS
rm -rf dist
go run ./cmd/release-packager --version 0.1.0 --commit "$(git rev-parse HEAD)" --epoch "$(git show -s --format=%cI HEAD)" --input ./build --dist ./dist --private
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

### Task 4: Package the Skill and Add Source/Archive Install, Upgrade, Rollback, and Uninstall

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
- Create: `test/release/archive_test.go`

**Interfaces:**
- Consumes: source checkout or one verified release archive, `SHA256SUMS`, CLI watcher lifecycle, and existing Skill workflows.
- Produces: user-level binary/Skill install manifest, idempotent upgrade, recoverable previous binary, source-build install, archive install, uninstall with optional local-state purge, and a self-contained Skill archive.

- [ ] **Step 1: Write failing install and Skill package tests**

```go
func TestUpgradeKeepsPreviousBinaryAndUninstallPreservesKnowledge(t *testing.T) {
	f:=newInstallFixture(t);f.install("0.1.0-dev.1");f.install("0.1.0")
	m:=f.manifest();if m.Version!="0.1.0"||m.PreviousBinarySHA256==""{t.Fatalf("%#v",m)}
	f.uninstall(false)
	for _,path:=range []string{f.projectLedger,f.vaultReview,f.rawSessions}{if _,err:=os.Stat(path);err!=nil{t.Fatalf("preserved path %s: %v",path,err)}}
}

func TestSkillArchiveHasValidIdentityAndNoCheckoutPaths(t *testing.T) {
	archive:=buildSkillArchive(t);entries:=readZip(t,archive)
	body:=entries["session-reviewer/SKILL.md"]
	if !bytes.Contains(body,[]byte("name: session-reviewer")){t.Fatal("skill identity missing")}
	if bytes.Contains(body,[]byte("/Users/neomei/"))||bytes.Contains(body,[]byte(`C:\Users\`)){t.Fatal("checkout path leaked")}
	for _,script:=range []string{"prepare-workflow.sh","prepare-workflow.ps1","apply-proposal.sh","apply-proposal.ps1"}{if !hasEntrySuffix(entries,script){t.Fatalf("missing %s",script)}}
}
```

- [ ] **Step 2: Run install/archive tests before scripts exist**

Run:

```bash
go test ./internal/install ./test/release -run 'TestUpgrade|TestSkillArchive' -v
```

Expected: FAIL because the manifest, scripts, and verified archive contract do not exist.

- [ ] **Step 3: Define the manifest and exact install locations**

```go
type Manifest struct {
	Version int `json:"version"`
	ProductVersion,Commit,InstalledAt string
	BinaryPath,BinarySHA256,SkillPath,SkillSHA256 string
	PreviousBinaryPath,PreviousBinarySHA256 string
	StartupInstalled bool
}
func Load(root string)(Manifest,error)
func Save(root string,Manifest)error
func VerifyFiles(Manifest)error
```

macOS defaults: binary `$HOME/.local/bin/session-reviewer`, Skill `${CODEX_HOME:-$HOME/.codex}/skills/session-reviewer`, manifest `$HOME/.local/share/session-reviewer/install-manifest.json`. Windows defaults: binary `%LOCALAPPDATA%\SessionReviewer\bin\session-reviewer.exe`, Skill `%CODEX_HOME%\skills\session-reviewer` or `%USERPROFILE%\.codex\skills\session-reviewer`, manifest `%LOCALAPPDATA%\SessionReviewer\install-manifest.json`. Installers refuse system paths and never modify machine-level PATH.

- [ ] **Step 4: Implement exact source/archive/rollback/uninstall flows**

Source install commands:

```bash
./scripts/install.sh --source . --version 0.1.0 --install-watcher
```

```powershell
.\scripts\install.ps1 -Source . -Version 0.1.0 -InstallWatcher
```

Archive install verifies its filename entry in `SHA256SUMS`, extracts to a private temporary directory, runs `version --json`, verifies version/commit against archive metadata, stops the watcher if installed, and moves the existing verified binary to `filepath.Join(dataRoot,"previous","session-reviewer-"+currentBinarySHA256)`. It then atomically installs binary and Skill, writes the manifest last, and reinstalls/restarts the watcher. A failure restores the previous manifest/binary/Skill and watcher state.

`uninstall` first calls `watch uninstall`, removes only manifest-listed files whose current hashes match, reports locally modified Skill files rather than deleting them, and preserves data. `--purge-state`/`-PurgeState` additionally removes index, watcher state, queues, merge bases, cursors, and config after printing the exact roots; it still preserves project/vault Markdown and raw sessions. `--rollback` reinstalls the manifest's previous verified binary and corresponding Skill backup, then runs `doctor` and `sync --dry-run`.

- [ ] **Step 5: Verify Skill and both installer families in isolated homes**

Run:

```bash
go run ./cmd/verify-skill ./skill/session-reviewer
env HOME="$(mktemp -d)" CODEX_HOME="$(mktemp -d)" ./scripts/install.sh --source . --version 0.1.0
go test ./internal/install ./test/release -v
pwsh -NoProfile -File ./scripts/install.ps1 -Source . -Version 0.1.0 -WhatIf
git diff --check
```

Expected: Skill verification and isolated POSIX install pass; PowerShell shows only user-level paths; archive smoke covers Windows installation natively in Task 6.

- [ ] **Step 6: Commit self-contained installation and Skill packaging**

```bash
git add internal/install scripts/install.sh scripts/uninstall.sh scripts/install.ps1 scripts/uninstall.ps1 cmd/verify-skill skill/session-reviewer test/release/archive_test.go
git commit -m "feat: package and install SessionReviewer and Skill"
```

---

### Task 5: Build Non-Committed Real-Session E2E Scenarios A-E and Redacted Receipts

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

### Task 6: Collect Native Minimum-OS macOS and Windows Evidence

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

### Task 7: Add Release Security, Canary, Memory, Performance, and Recovery Regressions

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
	if metrics.PeakHeapBytes>128<<20{t.Fatalf("peak heap %d",metrics.PeakHeapBytes)}
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

Expected: FAIL until Tasks 1–6 expose every listed store to `exerciseEveryWorkflow`, keep streaming allocation below the stated bound, and wire every listed crash hook to recovery; after those exact contracts exist, PASS.

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

Keep generated large files in test temp directories. Metrics use `runtime.MemStats.TotalAlloc` deltas and wall time around isolated phases; native receipts record actual values. A CI timing breach fails and prints metrics, never event/entity content.

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

### Task 8: Add Gated Release CI, Final Documentation, and Rollback Drill

**Files:**
- Create: `.github/workflows/release.yml`
- Create: `docs/release/rollback.md`
- Modify: `docs/release/checklist.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: clean `v0.1.0` tag, candidate/native workflow receipts, deterministic artifacts/SBOMs/checksums, license authorization gate, and installer rollback.
- Produces: private candidate artifacts today and, only after separate explicit license authorization, an immutable GitHub Release with the exact verified asset set.

- [ ] **Step 1: Write failing release-manifest and rollback-drill tests**

```go
func TestReleaseManifestRequiresArtifactsSBOMChecksumsNativeReceiptsAndLicenseAuthorization(t *testing.T) {
	m:=completeManifest(t)
	if err:=ValidateReleaseManifest(m);err!=nil{t.Fatal(err)}
	m.NativeReceipts=m.NativeReceipts[:3];if err:=ValidateReleaseManifest(m);err==nil{t.Fatal("accepted missing native receipt")}
	m=completeManifest(t);m.LicenseAuthorized=false;if err:=ValidateReleaseManifest(m);err==nil{t.Fatal("accepted unlicensed public release")}
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

`release.yml` triggers on `v*` tags and manual candidate dispatch. Build jobs have `contents: read`, verify clean exact `v0.1.0`, run the full test/vet/race/vulnerability suite, build one target each, and upload binary/SBOM fragments. An assemble job downloads only same-run artifacts, validates build metadata and archive contents, creates deterministic archives/Skill/SBOMs/`SHA256SUMS`, and uploads a private `candidate-0.1.0` artifact.

A native-gate job downloads the four receipt bundles by explicitly supplied successful `release-native` run ID and validates commit plus artifact hashes. The final `publish` job has `contents: write`, depends on build/assemble/native-gate, runs `go run ./cmd/check-release-license --public`, rejects any pre-existing mismatched tag/release/asset, creates a non-draft non-prerelease GitHub Release, and uploads exactly:

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

Because this plan does not add `LICENSE`/authorization, `publish` currently stops before any GitHub write. Candidate build and private native acceptance remain usable.

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
git add .github/workflows/release.yml docs/release/checklist.md docs/release/rollback.md README.md
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
- clean working tree and exact tag/commit identity.

Public publication additionally requires an explicit repository-owner licensing decision in a separate authorized change that adds `LICENSE` and `docs/release/license-authorization.json`. Until then, do not create/push `v0.1.0`, do not create a public GitHub Release, and report: private release candidate technically verified; public distribution blocked by missing owner-authorized license grant.
