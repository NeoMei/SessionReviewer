# Zero-token Gate C Migration and Real-project Acceptance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recoverably migrate existing schema-v2 projects to schema v3, preserve human and safe legacy presentation, prove complete zero-token behavior against the real 154-Session AgentWiki project, and produce a locally verified 0.3.0 release candidate without publishing it.

**Architecture:** Migration is an explicit journaled operation layered over the Gate-A private generation and Gate-B publication transaction. Synthetic fixtures prove every migration and recovery branch before a guarded acceptance harness touches a real Project/Vault. Real acceptance records only safe counts, IDs, hashes, and outcomes; it never copies raw Sessions into the repository. The final step installs backed-up candidate artifacts locally and verifies the visible Obsidian workflow.

**Tech Stack:** Go 1.26, existing schema-v2 parser/migration journal/sync primitives, Gate-A memory pipeline, Gate-B publication service, TypeScript 5.8, Obsidian 1.13, Vitest/jsdom, esbuild, macOS and Windows test runners.

**Spec:** `docs/superpowers/specs/2026-08-31-zero-token-session-memory-analysis-design.md`

## Global Constraints

- Gate A and Gate B must pass before any real-project command in this plan.
- Migration is additive, restartable, and compare-and-swap protected. It never rewrites a v2 source after its preimage changes.
- Existing human edits remain highest-precedence presentation. Safe legacy Agent semantics remain `human_approved` or `legacy_unverified`; they never become Observations, SessionView derived facts, or ProjectView facts.
- Preserve the accepted historical 13-Session AgentWiki material as presentation while independently scanning the full frozen 154-Session source set.
- The old Agent review job remains historical status only. Migration and scan never resume it and invoke no Agent/model/network path.
- The real acceptance root, Vault, data root, and plugin directory come only from explicit environment variables. Do not commit private absolute paths, source text, tool output, credentials, or Vault content.
- Before changing any real Project/Vault/plugin file, authenticate identity, snapshot exact target bytes/modes/hashes into a newly created private backup directory, and print the recovery path.
- If the live frozen source count is not 154, stop real acceptance with a coverage-drift report; do not weaken the expected count or publish a partial success.
- Obsidian acceptance covers only the concise scan-result presentation/editing workflow. Ignore unrelated plugin/UI behavior.
- Human edits made during extraction or publication are never overwritten; acceptance must deliberately test this once.
- Versioning in this gate prepares 0.3.0 candidate artifacts only. No Git push, tag, GitHub Release, Obsidian marketplace submission, or public package publication is authorized.
- Preserve unrelated worktree changes and stage only files named by the active task.

## Acceptance Environment Contract

The guarded real-project command requires:

```text
SESSION_REVIEWER_ACCEPTANCE_PROJECT_ROOT  authenticated AgentWiki checkout root
SESSION_REVIEWER_ACCEPTANCE_VAULT_ROOT    mapped Obsidian Vault root
SESSION_REVIEWER_ACCEPTANCE_DATA_ROOT     SessionReviewer private data root
SESSION_REVIEWER_ACCEPTANCE_PLUGIN_DIR    installed session-reviewer plugin directory
SESSION_REVIEWER_ACCEPTANCE_BACKUP_ROOT   existing private parent for a new timestamped backup
SESSION_REVIEWER_ACCEPTANCE_EXPECTED_SESSIONS=154
```

The harness rejects empty values, shell interpolation, symlinks/reparse points, a backup nested inside Project/Vault/data/plugin roots, mismatched authenticated project IDs, a dirty migration target not equal to the captured preimage, and any expected count other than `154` for this release acceptance.

---

### Task 1: Define a Recoverable v2-to-v3 Migration Contract

**Files:**
- Create: `internal/migrationv3/types.go`
- Create: `internal/migrationv3/plan.go`
- Create: `internal/migrationv3/plan_test.go`
- Create: `schemas/migration-v3-plan.schema.json`
- Modify: `internal/reviewv2/migrate.go`
- Modify: `internal/reviewv2/migrate_test.go`
- Modify: `internal/reviewv2/migration_journal.go`
- Modify: `internal/reviewv2/migration_journal_test.go`

**Interfaces:**

```go
type LegacyClass string

const (
    LegacyHumanApproved LegacyClass = "human_approved"
    LegacyUnverified    LegacyClass = "legacy_unverified"
    LegacyRejected      LegacyClass = "rejected"
)

type Plan struct {
    SchemaVersion     int
    ProjectID         string
    SourceRevision    string
    PreparedGeneration string
    HumanPatches      []presentation.Patch
    LegacyItems       []LegacyItem
    RejectedItems     []RejectedLegacyItem
    PublicPreimages   []publication.Destination
}

func BuildPlan(ctx context.Context, in Input) (Plan, error)
func ValidatePlan(Plan) error
```

- [ ] **Step 1: Write failing strict-plan and no-mutation tests**

Feed v2 review/history/ledger/sync state/historical job fixtures containing human edits, accepted Agent narrative, unsafe links, malformed markers, duplicate IDs, unknown custom blocks, and mixed project IDs. Assert planning is deterministic and performs zero writes. Unknown or unsafe semantic material must be rejected with typed location/reason, never silently dropped or promoted.

Run: `go test ./internal/migrationv3 ./internal/reviewv2 -run 'Test(MigrationV3Plan|LegacyClassification)' -count=1`

Expected: FAIL because the migration-v3 plan does not exist.

- [ ] **Step 2: Implement explicit legacy classification**

Classify directly edited supported fields as `human_approved`. Classify safe existing Agent-authored decisions/narrative as `legacy_unverified` unless the v2 ledger explicitly records a valid human acceptance marker. Reject content that fails stable identity, link, size, UTF-8, redaction, or project-boundary validation. Store legacy presentation text only in the public render plan/human patch payload, never in ObservationStore.

- [ ] **Step 3: Freeze all migration dependencies**

Require an authenticated mapping, strict v2 read, current sync state, historical job snapshot, Gate-A prepared generation, public preimage hashes, and source revision. Canonically sort patches/items, validate schema `1`, and write no migration journal until `ValidatePlan` succeeds.

- [ ] **Step 4: Extend the migration journal without weakening v2 recovery**

Add a versioned v3 branch that references the Gate-B publication intent and exact v2 backup hashes. Existing v2 journal recovery remains byte-compatible. Unsupported journal versions fail closed and leave both migration and publication pointers untouched.

- [ ] **Step 5: Run GREEN and commit**

Run: `gofmt -w internal/migrationv3 internal/reviewv2 && go test ./internal/migrationv3 ./internal/reviewv2 -count=20`

Expected: PASS; planning the same fixture twice produces byte-identical canonical JSON.

```bash
git add internal/migrationv3 internal/reviewv2 schemas/migration-v3-plan.schema.json
git commit -m "feat: plan recoverable schema v3 migration"
```

---

### Task 2: Execute Migration Through the Publication Transaction

**Files:**
- Create: `internal/migrationv3/service.go`
- Create: `internal/migrationv3/service_test.go`
- Create: `internal/migrationv3/recovery_test.go`
- Modify: `internal/publication/service.go`
- Modify: `internal/publication/service_test.go`
- Create: `internal/cli/migrate.go`
- Create: `internal/cli/migrate_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**

```text
session-reviewer migrate-v3 plan --project-id <id> [--data-dir <path>] --json
session-reviewer migrate-v3 apply --project-id <id> --plan-digest <sha256:...> [--data-dir <path>] --json
session-reviewer migrate-v3 status --project-id <id> [--data-dir <path>] --json
```

```go
func (s *Service) Plan(ctx context.Context, projectID string) (PlanResult, error)
func (s *Service) Apply(ctx context.Context, projectID string, planDigest memory.Digest) (publication.Result, error)
func (s *Service) Recover(ctx context.Context, projectID string) (RecoveryResult, error)
```

- [ ] **Step 1: Write failing apply/recovery tests**

Cover clean v2 migration, no-v2 fresh project, v2 preimage changed after planning, human edit during Gate-A scan, publication conflict, crash at every migration/publication stage, repeated `apply`, and old-writer access after migration. Assert a terminal outcome is either untouched v2 or complete verified v3; never a mixed public triplet.

Run: `go test ./internal/migrationv3 ./internal/publication ./internal/cli -run 'Test(MigrationV3Apply|MigrateV3Command)' -count=1`

Expected: FAIL because only planning exists.

- [ ] **Step 2: Compose migration with context update and publication**

Acquire the project migration lock, revalidate the plan digest/preimages, record the migration intent, render v3 from ProjectView plus human/legacy material, and call `publication.Publish`. Mark migration committed only after both Project and Vault verify v3 and the private published pointer switches. Keep v2 backup payloads reachable from the committed migration record.

- [ ] **Step 3: Make retry and rollback exact**

If source/public bytes are unchanged, resume from the last durable stage. If a human changed a destination, stop with `E_MIGRATION_PREIMAGE_CHANGED` and retain the prepared generation. CAS rollback only bytes still equal to migration desired images. Re-running `apply` on a committed plan returns the recorded result with zero writes.

- [ ] **Step 4: Enforce post-migration downgrade protection**

Run every legacy mutation entrypoint against the migrated fixture and require `ErrWriterUpgradeRequired` before file creation, truncation, rename, or sync-state mutation. Permit explicit supported v2 read-only inspection only.

- [ ] **Step 5: Run GREEN and commit**

Run: `gofmt -w internal/migrationv3 internal/publication internal/cli && go test ./internal/migrationv3 ./internal/publication ./internal/cli -count=20 && go test -race ./internal/migrationv3 ./internal/publication`

Expected: PASS with no race and no mixed schema state.

```bash
git add internal/migrationv3 internal/publication internal/cli/migrate.go internal/cli/migrate_test.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: migrate project presentation to schema v3"
```

---

### Task 3: Build the Full Synthetic Migration and Scan Matrix

**Files:**
- Create: `test/zerotoken/gate_c_test.go`
- Create: `test/zerotoken/fixtures/migration-v2/README.md`
- Create: `test/zerotoken/fixtures/migration-v2/project-review.md`
- Create: `test/zerotoken/fixtures/migration-v2/project-history.md`
- Create: `test/zerotoken/fixtures/migration-v2/ledger.json`
- Create: `test/zerotoken/fixtures/migration-v2/sync-state.json`
- Create: `test/zerotoken/fixtures/migration-v2/historical-job.json`
- Create: `test/zerotoken/fixtures/cross-platform/README.md`
- Modify: `internal/platform/paths_test.go`
- Modify: `internal/projectidentity/resolve_test.go`

- [ ] **Step 1: Create a generated 154-Session fixture without real content**

Generate deterministic minimal Codex event streams during tests: 151 indexed Sessions plus one unsupported, one malformed/unreadable, and one ambiguous cross-project case. Include a shared Session, source append, missing source after first publish, path move, authenticated worktree alias, and conflicting alias. Fixtures contain synthetic placeholders only.

- [ ] **Step 2: Encode the legacy 13-Session preservation case**

Seed the v2 documents with 13 accepted legacy presentation entities and human edits. After migration and the 154-Session scan, assert all safe legacy/human entities remain visible with their classifications, none appears in ObservationStore, and coverage counts derive solely from the frozen 154 source records.

- [ ] **Step 3: Test every required incremental/recovery path**

Assert unchanged rescan writes no versions/files; append changes only one source successor, dependent SessionView, and ProjectView; missing source retains prior facts with unavailable state; shared usage has one SourceCatalog row and no global double count; path move preserves project ID; conflicting aliases do not merge; malformed source does not block later Sessions; and every publication crash converges safely.

- [ ] **Step 4: Reconcile all coverage equations**

For every generation require:

```text
frozen = indexed + unsupported + missing + unreadable + ambiguous
indexed Project-affined SessionViews = ProjectView indexed count
associated usage = sum of unique associated source IDs
global usage = sum of globally unique source IDs
```

Reject a duplicate source ID, unaccounted frozen source, cross-project observation leak, or issue classified as indexed.

- [ ] **Step 5: Run cross-platform and repeated determinism checks**

Run: `go test ./test/zerotoken -run TestGateC -count=50 && go test ./internal/platform ./internal/projectidentity -count=20`

Expected: PASS on macOS and Windows CI with equal semantic IDs/digests after platform path normalization.

- [ ] **Step 6: Commit**

```bash
git add test/zerotoken internal/platform/paths_test.go internal/projectidentity/resolve_test.go
git commit -m "test: cover zero-token migration matrix"
```

---

### Task 4: Add a Guarded Real-project Acceptance Harness

**Files:**
- Create: `cmd/session-reviewer-acceptance/main.go`
- Create: `internal/acceptance/config.go`
- Create: `internal/acceptance/config_test.go`
- Create: `internal/acceptance/backup.go`
- Create: `internal/acceptance/backup_test.go`
- Create: `internal/acceptance/run.go`
- Create: `internal/acceptance/run_test.go`
- Create: `schemas/acceptance-report-v1.schema.json`
- Create: `docs/acceptance/zero-token-0.3.0.md`

**Interfaces:**

```text
go run ./cmd/session-reviewer-acceptance preflight --json
go run ./cmd/session-reviewer-acceptance backup --json
go run ./cmd/session-reviewer-acceptance run --backup-id <id> --json
go run ./cmd/session-reviewer-acceptance verify --run-id <id> --json
go run ./cmd/session-reviewer-acceptance restore --backup-id <id> --json
```

- [ ] **Step 1: Write failing environment and target-safety tests**

Reject absent/non-absolute roots, symlinks, nested or overlapping roots, world-readable backup parent, plugin ID mismatch, unauthenticated Project/Vault mapping, source count drift, and backup/restore target mismatch. Require explicit `SESSION_REVIEWER_ACCEPTANCE_EXPECTED_SESSIONS=154`.

Run: `go test ./internal/acceptance -run 'Test(Config|Backup|Restore)' -count=1`

Expected: FAIL because the harness does not exist.

- [ ] **Step 2: Implement content-minimizing backup and report records**

Back up only files the plan may mutate: current public review/history/ledger/sync metadata, private SessionReviewer project store, and installed plugin assets/settings. Record path relative to its authenticated root, mode, SHA-256, existence, and backup object hash. Never copy raw provider Session files. Create backup/report directories as `0700` and objects as `0600`.

- [ ] **Step 3: Implement preflight and phased run**

Preflight builds/tests the candidate, authenticates identity, freezes SourceCatalog boundaries, and prints planned targets without writes. `run` requires a valid backup ID, executes migration, full scan, publish, and verification as separate durable phases. Reports include safe counts, terminal-state totals, generation/digests, write counts, token count, issue codes, and timings—no excerpts or command arguments from Sessions.

- [ ] **Step 4: Make restore safe and explicit**

Restore only a selected backup whose target bytes still match the acceptance run's recorded postimage; otherwise stop with a per-file CAS conflict. Restore modes and absence atomically, then verify all backup hashes. Never delete or replace raw source Sessions.

- [ ] **Step 5: Run GREEN and commit**

Run: `gofmt -w cmd/session-reviewer-acceptance internal/acceptance && go test ./internal/acceptance ./cmd/session-reviewer-acceptance -count=20`

Expected: PASS using temporary roots; source-fixture bytes are identical before/after backup, run, and restore.

```bash
git add cmd/session-reviewer-acceptance internal/acceptance schemas/acceptance-report-v1.schema.json docs/acceptance/zero-token-0.3.0.md
git commit -m "test: add guarded real-project acceptance"
```

---

### Task 5: Prove the Real AgentWiki 154-Session Workflow

**Files:**
- Modify: `docs/acceptance/zero-token-0.3.0.md`
- Create locally, do not commit: `$SESSION_REVIEWER_ACCEPTANCE_BACKUP_ROOT/<backup-id>/`
- Create locally, do not commit: `$SESSION_REVIEWER_ACCEPTANCE_DATA_ROOT/acceptance/<run-id>.json`

- [ ] **Step 1: Run preflight and inspect the immutable target summary**

Run:

```bash
go run ./cmd/session-reviewer-acceptance preflight --json
```

Expected: authenticated project/Vault/plugin identities, exactly 154 frozen Sessions, zero writes, zero Agent/model/network operations, and no private absolute path in the committed report template.

- [ ] **Step 2: Create and verify the private backup**

Run:

```bash
go run ./cmd/session-reviewer-acceptance backup --json
```

Expected: a new backup ID/path, all target hashes verified, raw Session copy count `0`, and printed restore command. Stop if any target changes before the next step and create a fresh backup.

- [ ] **Step 3: Migrate and scan all frozen Sessions**

Run the exact `run --backup-id` command returned by backup.

Expected: terminal `completed` or `completed_with_issues`; `session_count=154`; all 154 reconcile across terminal states; no Agent process/API/network request; `review_run_tokens=0`; v3 Project/Vault generation and hashes agree; the historical 13-Session material remains visible as human/legacy presentation.

- [ ] **Step 4: Run unchanged and append-only acceptance**

First repeat the scan without source changes and require zero new Observations/views/public writes. Then append one synthetic, reversible acceptance marker through the provider's supported local fixture hook—not by editing a real Session—and run the isolated append fixture mode. Require only one successor source boundary and exact dependents to rebuild. Remove the fixture hook and prove real source bytes never changed.

- [ ] **Step 5: Exercise failure isolation against private mirrors**

Use harness-created private mirrors of one source boundary to test malformed, missing, shared, conflicting alias, path move, and crash stages. Do not corrupt or rename real source Sessions. Require the same results as Task 3 and verify the live 154-source generation remains published afterward.

- [ ] **Step 6: Test a concurrent human edit**

Pause the harness after extraction, make a reversible supported human edit in the mapped presentation, then continue. Require `E_HUMAN_CONCURRENT_EDIT`, no overwrite, and a retained prepared generation. Re-plan/retry and require the human edit to remain highest in both Project and Vault.

- [ ] **Step 7: Record only safe evidence**

Fill `docs/acceptance/zero-token-0.3.0.md` with command versions, counts, terminal-state reconciliation, generation/digests, zero-token proof, preserved legacy/human checks, write counts, backup ID, and pass/fail. Use `<private-root>` placeholders instead of local paths and include no Session text.

```bash
git add docs/acceptance/zero-token-0.3.0.md
git commit -m "test: accept 154-session zero-token scan"
```

---

### Task 6: Prepare and Install the 0.3.0 Local Candidate

**Files:**
- Modify: `internal/buildinfo/buildinfo.go`
- Modify: `internal/buildinfo/buildinfo_test.go`
- Modify: `obsidian-plugin/manifest.json`
- Modify: `obsidian-plugin/package.json`
- Modify: `obsidian-plugin/package-lock.json`
- Modify: `obsidian-plugin/versions.json`
- Modify: `README.md`
- Create: `obsidian-plugin/README.md`
- Create: `CHANGELOG.md`
- Create: `docs/release-checklist.md`
- Create: `internal/releasecandidate/package.go`
- Create: `internal/releasecandidate/package_test.go`
- Create: `scripts/build-release-candidate.sh`

- [ ] **Step 1: Write failing version-coherence and packaging tests**

Require CLI/plugin/package/versions schema metadata to report 0.3.0 and public schema 3. Require candidate assets `session-reviewer`, `main.js`, `manifest.json`, `styles.css`, checksums, and build provenance; reject dirty generated assets, extra files, wrong executable architecture, absolute local paths, or a manifest/package mismatch.

Run: `go test ./internal/buildinfo ./internal/releasecandidate -run 'Test(Version030|ReleaseCandidate)' -count=1`

Expected: FAIL until metadata and packager are updated.

- [ ] **Step 2: Update user-facing documentation**

Document `更新项目脉络`, whole-project zero-token scan, terminal coverage states, human edit precedence, associated/shared usage, migration backup/recovery, schema-3 upgrade requirement, and the fact that Obsidian is only a concise presentation/editor. Remove instructions for the two obsolete Agent settings without describing private store internals as plugin features.

- [ ] **Step 3: Build reproducible candidate artifacts**

Build the Go CLI for the local platform and Windows, run the plugin production build, copy only allowlisted assets into a fresh candidate directory, generate SHA-256 checksums, and rebuild once to compare hashes after excluding signed timestamp/provenance fields. The script refuses to publish, tag, push, or write into an installed plugin directory.

- [ ] **Step 4: Back up and install locally**

Use the acceptance backup mechanism to capture the existing CLI/plugin assets. Install the candidate CLI and the three Obsidian plugin assets with atomic replacement, preserving the plugin data file unless its schema migration is explicitly tested. Verify installed bytes equal candidate checksums, then reload Obsidian.

- [ ] **Step 5: Run real visible Obsidian acceptance**

In the installed plugin verify: existing project context opens; only `更新项目脉络` is primary; no Agent settings are required; clicking updates all 154 Sessions and shows the exact terminal status; review/history/evolution/status/usage remain concise; full-width model cards and pricing links render; associated/shared labels are correct; human set/suppress/restore survives a rescan; and unrelated Obsidian content is ignored.

Record pass/fail and installed asset hashes in the safe acceptance document. If any check fails, restore the backed-up plugin/CLI and return to the owning implementation task; do not proceed to release readiness.

- [ ] **Step 6: Run GREEN and commit**

Run:

```bash
go test ./internal/buildinfo ./internal/releasecandidate -count=20
./scripts/build-release-candidate.sh
go test ./... -count=1
cd obsidian-plugin && npm ci && npm test -- --run && npm run check && npm run build
```

Expected: PASS; candidate and installed checksums match; acceptance document records the visible workflow result.

```bash
git add internal/buildinfo internal/releasecandidate obsidian-plugin/manifest.json obsidian-plugin/package.json obsidian-plugin/package-lock.json obsidian-plugin/versions.json README.md obsidian-plugin/README.md CHANGELOG.md docs/release-checklist.md docs/acceptance/zero-token-0.3.0.md scripts/build-release-candidate.sh
git commit -m "chore: prepare zero-token 0.3.0 candidate"
```

---

### Task 7: Complete the Release-readiness Audit Without Publishing

**Files:**
- Modify: `docs/acceptance/zero-token-0.3.0.md`
- Modify: `docs/release-checklist.md`

- [ ] **Step 1: Run the complete repository verification**

Run:

```bash
go test ./... -count=1
go test -race ./internal/memory ./internal/memorystore ./internal/source/... ./internal/sessionview ./internal/projectview ./internal/publication ./internal/contextupdate ./internal/scanjob ./internal/migrationv3
go vet ./...
cd obsidian-plugin && npm ci && npm test -- --run && npm run check && npm run build
cd .. && go test ./test/zerotoken -count=20
git diff --check
```

Expected: all commands PASS with no race, vet, type, lint, bundle, or whitespace errors.

- [ ] **Step 2: Audit architecture boundaries mechanically**

Run:

```bash
rg -n 'internal/agent|agentExecutable|总结并同步' internal/scan internal/contextupdate internal/scanjob obsidian-plugin/src
rg -n 'full_transcript|raw_tool_output' internal/memory internal/memorystore schemas
rg -n 'TB[D]|TO[DO]|implement la[t]er|GATE_[ABC]_CONTINU[E]' docs/superpowers/plans/2026-08-31-zero-token-*.md
```

Expected: all three searches return no prohibited production/plan matches. If fixtures need a prohibited literal to assert rejection, isolate it in a clearly named test and narrow the command to production files.

- [ ] **Step 3: Re-run the installed unchanged scan**

From Obsidian click `更新项目脉络` once more with no source or human changes. Require 154 reconciled Sessions, zero model tokens, unchanged generation dependency digest, zero new immutable versions, zero public writes, and unchanged human-visible bytes.

- [ ] **Step 4: Compare all release surfaces without publishing**

Verify local branch commit, candidate version/checksums, installed CLI version, installed plugin manifest/assets, acceptance backup/run IDs, and release checklist agree. Inspect remote/tag/marketplace state read-only and record that 0.3.0 is not yet public. Do not create or modify any remote state.

- [ ] **Step 5: Commit the readiness record**

```bash
git add docs/acceptance/zero-token-0.3.0.md docs/release-checklist.md
git commit -m "docs: record zero-token release readiness"
git status --short
```

Expected: clean worktree. Gate C is complete only when the acceptance record proves migration preservation, exact 154-Session reconciliation, zero review-run tokens, safe recovery, unchanged-rescan zero writes, and installed Obsidian behavior. Public release still requires a separate explicit user instruction.
