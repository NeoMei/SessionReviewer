# SessionReviewer Obsidian Project Evolution Browser Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Build an installable Obsidian Desktop plugin that discovers SessionReviewer v2 projects, renders the approved project-evolution browser, switches node details in place, exposes decisions and model accounting, edits allowed Markdown fields, and safely invokes fixed CLI conflict actions.

**Architecture:** Keep the plugin as a read/edit projection over 项目回顾.md, 项目历史.md, and the validated hidden ledger.json contract produced by the core plan. A repository layer discovers visible project-review Markdown, parses shared marker fixtures, verifies ledger/document hashes, and emits immutable BrowserModel snapshots; an ItemView renders those snapshots with plain TypeScript DOM code, while all writes use Vault.process with stale-hash checks and all state-changing CLI calls use execFile with shell disabled.

**Tech Stack:** Node.js 22, TypeScript 5.8.3, Obsidian API 1.13.1 with minAppVersion 1.8.7, esbuild 0.25.5 CJS/es2021 bundle, Vitest 4.1.10, jsdom 26.1.0, ESLint 9.39.4, plain CSS, npm lockfile v3.

## Global Constraints

- This plan starts only after the core v2 plan is complete and its real Project/Vault acceptance is green.
- The plugin is desktop-only because explicit CLI migration/conflict actions use Node child_process.
- The plugin never reads raw Codex Session files and never calls a model.
- Human content comes from the two Markdown documents; ledger.json is validated read-only machine data, not an editable duplicate source.
- Human edits write exact Markdown units through Obsidian Vault APIs; machine fields are never written by the plugin.
- The main desktop layout is a wide two-column evolution browser; below 860px it becomes a single column.
- Default evolution navigation shows the latest five nodes; full history is searchable and virtualized.
- Node selection updates the adjacent detail pane without opening another file.
- Top views are 项目演进, 关键决策, and 投入统计.
- Parse, schema, stale-hash, conflict, and migration failures keep the last valid snapshot visible with a specific actionable banner.
- CLI execution uses a verified executable path, execFile, shell=false, and an exact allowlist; Markdown can never supply executable names or arguments.
- macOS and Windows real Obsidian UI acceptance are release gates.

## Official Build References

- Obsidian official sample plugin package/build pattern: https://github.com/obsidianmd/obsidian-sample-plugin
- Obsidian API package and registerView/plugin structure: https://www.npmjs.com/package/obsidian
- Obsidian community release asset rule: https://github.com/obsidianmd/obsidian-releases

## File Structure

- Create obsidian-plugin/package.json and package-lock.json: reproducible npm scripts and dependency graph.
- Create obsidian-plugin/manifest.json and versions.json: plugin identity, desktop-only/minimum app version, release mapping.
- Create obsidian-plugin/tsconfig.json, esbuild.config.mjs, vitest.config.ts, eslint.config.mts: build/test/lint.
- Create obsidian-plugin/src/main.ts: plugin lifecycle, view registration, commands, settings.
- Create obsidian-plugin/src/contracts/review-v2.ts: exact TypeScript mirror of core v2 schema.
- Create obsidian-plugin/src/data/markdown.ts: shared marker grammar and human model parser.
- Create obsidian-plugin/src/data/ledger.ts: strict machine-ledger validator.
- Create obsidian-plugin/src/data/repository.ts: discovery, load, hash verification, watch, stale snapshot.
- Create obsidian-plugin/src/data/editor.ts: stale-safe allowed-field Markdown patches through Vault.process.
- Create obsidian-plugin/src/cli/runner.ts: verified execFile allowlist and JSON status/resolve adapters.
- Create obsidian-plugin/src/view/project-view.ts: ItemView state machine and lifecycle.
- Create obsidian-plugin/src/view/render-shell.ts: header, resume strip, tabs, loading/error/empty states.
- Create obsidian-plugin/src/view/render-evolution.ts: two-column node rail and adjacent detail.
- Create obsidian-plugin/src/view/render-decisions.ts: decision table and reverse navigation.
- Create obsidian-plugin/src/view/render-usage.ts: totals, model shares, expanded prices.
- Create obsidian-plugin/src/view/edit-modal.ts and conflict-modal.ts: explicit edits and three-candidate resolution.
- Create obsidian-plugin/styles.css: wide, compact, scrollable, responsive, accessible layout.
- Create obsidian-plugin/tests/: parser, repository, view, editing, CLI, accessibility, and packaging tests.
- Modify root .github/workflows/ci.yml and release.yml: Node gates and plugin release assets.
- Modify README.md: install, open, edit, sync, migration, Windows instructions.

---

### Task 1: Scaffold a Reproducible Official-Pattern Obsidian Plugin

**Files:**
- Create: obsidian-plugin/package.json
- Create: obsidian-plugin/package-lock.json
- Create: obsidian-plugin/.npmrc
- Create: obsidian-plugin/manifest.json
- Create: obsidian-plugin/versions.json
- Create: obsidian-plugin/tsconfig.json
- Create: obsidian-plugin/esbuild.config.mjs
- Create: obsidian-plugin/vitest.config.ts
- Create: obsidian-plugin/eslint.config.mts
- Create: obsidian-plugin/src/main.ts
- Create: obsidian-plugin/src/constants.ts
- Create: obsidian-plugin/src/view/project-view.ts
- Create: obsidian-plugin/tests/mocks/obsidian.ts
- Create: obsidian-plugin/tests/main.test.ts

**Interfaces:**
- Consumes: Obsidian Plugin, ItemView, WorkspaceLeaf.
- Produces: plugin ID session-reviewer, view type session-reviewer-project-evolution, command session-reviewer:open-project-evolution, and npm build/test/lint scripts.

- [ ] **Step 1: Write the failing lifecycle test**

~~~ts
import { describe, expect, it, vi } from "vitest";
import SessionReviewerPlugin, { VIEW_TYPE } from "../src/main";

describe("plugin lifecycle", () => {
  it("registers one desktop project-evolution view and open command", async () => {
    const registerView = vi.fn();
    const addCommand = vi.fn();
    const plugin = new SessionReviewerPlugin({ workspace: {} } as never, {} as never);
    Object.assign(plugin, { registerView, addCommand, addSettingTab: vi.fn(), registerEvent: vi.fn() });
    await plugin.onload();
    expect(VIEW_TYPE).toBe("session-reviewer-project-evolution");
    expect(registerView).toHaveBeenCalledOnce();
    expect(addCommand).toHaveBeenCalledWith(expect.objectContaining({ id: "open-project-evolution" }));
  });
});
~~~

- [ ] **Step 2: Create package metadata and run RED**

Use this exact package script contract:

~~~json
{
  "name": "session-reviewer-obsidian",
  "version": "0.2.0",
  "private": true,
  "type": "module",
  "scripts": {
    "build": "tsc --noEmit --skipLibCheck && node esbuild.config.mjs production",
    "test": "vitest run",
    "lint": "eslint .",
    "check": "npm run lint && npm run test && npm run build"
  },
  "devDependencies": {
    "@eslint/js": "9.39.4",
    "@types/node": "22.15.17",
    "esbuild": "0.25.5",
    "eslint": "9.39.4",
    "eslint-plugin-obsidianmd": "0.4.0",
    "jsdom": "26.1.0",
    "obsidian": "1.13.1",
    "typescript": "5.8.3",
    "typescript-eslint": "8.59.1",
    "vitest": "4.1.10"
  }
}
~~~

Run: cd obsidian-plugin && npm test -- --run tests/main.test.ts

Expected: FAIL because src/main.ts is absent.

- [ ] **Step 3: Implement the minimal plugin lifecycle**

~~~ts
import { Plugin, WorkspaceLeaf } from "obsidian";
import { VIEW_TYPE } from "./constants";
import { ProjectEvolutionView } from "./view/project-view";
export { VIEW_TYPE } from "./constants";

export default class SessionReviewerPlugin extends Plugin {
  async onload(): Promise<void> {
    this.registerView(VIEW_TYPE, (leaf: WorkspaceLeaf) => new ProjectEvolutionView(leaf));
    this.addCommand({
      id: "open-project-evolution",
      name: "打开项目脉络",
      callback: () => this.activateView(),
    });
  }

  async activateView(): Promise<void> {
    const leaf = this.app.workspace.getLeaf("tab");
    await leaf.setViewState({ type: VIEW_TYPE, active: true });
    this.app.workspace.revealLeaf(leaf);
  }
}
~~~

Create constants.ts with export const VIEW_TYPE = "session-reviewer-project-evolution". Create the initial view file so Task 1 typecheck/build is complete:

~~~ts
import { ItemView, WorkspaceLeaf } from "obsidian";
import { VIEW_TYPE } from "../constants";

export class ProjectEvolutionView extends ItemView {
  constructor(leaf: WorkspaceLeaf) { super(leaf); }
  getViewType(): string { return VIEW_TYPE; }
  getDisplayText(): string { return "SessionReviewer 项目脉络"; }
  async onOpen(): Promise<void> { this.contentEl.setText("正在加载项目…"); }
  async onClose(): Promise<void> { this.contentEl.empty(); }
}
~~~

Create manifest.json with id session-reviewer, name SessionReviewer, author NeoMei, authorUrl https://github.com/NeoMei, version 0.2.0, minAppVersion 1.8.7, isDesktopOnly true, and a concise English description. Use the official sample esbuild settings: entry src/main.ts, format cjs, target es2021, obsidian/electron/Node built-ins external, production minification, no production sourcemap.

Configure Vitest with environment jsdom and an alias from obsidian to tests/mocks/obsidian.ts. The mock exports minimal Plugin, ItemView, WorkspaceLeaf, Modal, Notice, and DOM-backed contentEl behavior used by tests; production build still externalizes the real obsidian module.

- [ ] **Step 4: Install exactly and verify scaffold**

Run: cd obsidian-plugin && npm install --package-lock-only

Run: cd obsidian-plugin && npm run check

Expected: package-lock.json lockfileVersion 3; lint, one lifecycle test, typecheck, and bundle all PASS; main.js is created.

- [ ] **Step 5: Commit**

~~~bash
git add obsidian-plugin
git commit -m "feat: scaffold SessionReviewer Obsidian plugin"
~~~

### Task 2: Mirror and Validate the Core v2 Contract

**Files:**
- Create: obsidian-plugin/src/contracts/review-v2.ts
- Create: obsidian-plugin/src/data/ledger.ts
- Create: obsidian-plugin/src/data/markdown.ts
- Create: obsidian-plugin/src/data/hash.ts
- Create: obsidian-plugin/tests/contracts.test.ts
- Create: obsidian-plugin/tests/markdown.test.ts
- Modify: obsidian-plugin/vitest.config.ts

**Interfaces:**
- Consumes: schemas/review-ledger-v2.schema.json and testdata/review-v2 fixtures from the completed core plan.
- Produces: parseLedger, parseReview, parseHistory, sha256Text, BrowserSource and typed human models used by repository/editor/view tasks.

- [ ] **Step 1: Write cross-language fixture tests**

~~~ts
describe("review v2 shared fixtures", () => {
  it("accepts Go golden data and rejects duplicate event IDs", async () => {
    const ledger = parseLedger(await fixture("ledger.valid.json"));
    const review = parseReview(await fixture("项目回顾.valid.md"));
    const history = parseHistory(await fixture("项目历史.valid.md"));
    expect(ledger.schemaVersion).toBe(2);
    expect(review.projectId).toBe(ledger.projectId);
    expect(history.events[0].id).toBe("timeline-trust-chain");
    const invalid = await fixture("项目历史.invalid-duplicate-event.md");
    expect(() => parseHistory(invalid)).toThrow(/duplicate event identity/);
  });
});
~~~

- [ ] **Step 2: Run tests and verify RED**

Run: cd obsidian-plugin && npm test -- --run tests/contracts.test.ts tests/markdown.test.ts

Expected: FAIL because contract and parser modules do not exist.

- [ ] **Step 3: Define exact TypeScript models**

~~~ts
export type ViewKind = "evolution" | "decisions" | "usage";
export interface ReviewModel {
  projectId: string;
  revision: number;
  name: string;
  goal: string;
  stage: string;
  status: string;
  nextAction: string;
  lastVerification: string;
  risks: RiskModel[];
  decisions: DecisionModel[];
}
export interface HistoryEvent {
  id: string;
  occurredAt: string;
  kind: string;
  title: string;
  meaning: string;
  summary: string;
  why: string;
  changes: string[];
  results: string[];
  decisionIds: string[];
  next: string;
}
export interface MachineLedger {
  schemaVersion: 2;
  projectId: string;
  acceptedRevision: number;
  reviewSha256: string;
  historySha256: string;
  lastSuccessfulSync?: string;
  accounting: ProjectAccounting;
}
~~~

Represent every JSON number checked by the Go safe-integer contract as number and reject values above Number.MAX_SAFE_INTEGER. Reject unknown object keys, negative totals/costs, duplicate model names, missing decisions, invalid hashes, schema mismatch, and aggregate totals/shares that differ from model rows.

Implement sha256Text as a synchronous function using node:crypto createHash. The plugin is desktop-only, and synchronous hashing is required because Obsidian Vault.process callbacks return replacement text synchronously.

- [ ] **Step 4: Implement the same strict marker grammar**

Use line-by-line parsing with a fence-state machine. Recognize only exact session-reviewer:decision, risk, and event comments outside fences. Require matching closing markers, stable IDs, unique IDs, exact required headings, and 4 MiB Markdown / 16 MiB ledger limits. Return source ranges for every editable field:

~~~ts
export interface SourceRange { start: number; end: number }
export interface EditableField {
  document: "review" | "history";
  unitId: string;
  field: string;
  value: string | string[];
  range: SourceRange;
}
~~~

- [ ] **Step 5: Verify Go/TypeScript fixture parity**

Run: go test ./internal/reviewv2 -count=1

Run: cd obsidian-plugin && npm test -- --run tests/contracts.test.ts tests/markdown.test.ts

Expected: both commands PASS against the same testdata/review-v2 files.

- [ ] **Step 6: Commit**

~~~bash
git add obsidian-plugin/src/contracts obsidian-plugin/src/data obsidian-plugin/tests obsidian-plugin/vitest.config.ts
git commit -m "feat: parse review v2 in Obsidian"
~~~

### Task 3: Discover Projects and Maintain Valid Snapshots

**Files:**
- Create: obsidian-plugin/src/data/repository.ts
- Create: obsidian-plugin/src/data/vault-port.ts
- Create: obsidian-plugin/src/state/store.ts
- Create: obsidian-plugin/tests/repository.test.ts
- Create: obsidian-plugin/tests/store.test.ts

**Interfaces:**
- Consumes: Obsidian Vault.getMarkdownFiles, cached frontmatter, Vault.adapter.read for the sibling hidden ledger, Task 2 parsers/hashes.
- Produces: ProjectRepository.discover, load, watch and BrowserStore immutable snapshots.

- [ ] **Step 1: Write failing discovery and last-valid-snapshot tests**

~~~ts
it("discovers project_review files and keeps last valid snapshot after corruption", async () => {
  const vault = fakeVault({
    "Projects/A/Session Review/项目回顾.md": reviewFixture(),
    "Projects/A/Session Review/项目历史.md": historyFixture(),
    "Projects/A/Session Review/.session-reviewer/ledger.json": ledgerFixture(),
  });
  const repo = new ProjectRepository(vault);
  const projects = await repo.discover();
  expect(projects).toEqual([{ projectId: "project-0123456789abcdef", root: "Projects/A/Session Review", name: "SessionReviewer" }]);
  const first = await repo.load(projects[0]);
  vault.write("Projects/A/Session Review/项目历史.md", "broken");
  const second = await repo.load(projects[0], first);
  expect(second.kind).toBe("stale");
  expect(second.lastValid).toEqual(first);
  expect(second.diagnostic.code).toBe("history_parse_failed");
});
~~~

- [ ] **Step 2: Run the repository tests and verify RED**

Run: cd obsidian-plugin && npm test -- --run tests/repository.test.ts tests/store.test.ts

Expected: FAIL because ProjectRepository and BrowserStore do not exist.

- [ ] **Step 3: Implement discovery without hidden-folder enumeration**

Scan Vault.getMarkdownFiles for exact basename 项目回顾.md. Read cached frontmatter and require entity_type project_review, schema_version 2, stable project_id, and a sibling 项目历史.md. Construct the hidden ledger path from the visible parent; do not recursively enumerate dot folders. Sort descriptors by human name then project ID.

- [ ] **Step 4: Implement verified loading**

~~~ts
export interface SnapshotReady { kind: "ready"; model: BrowserModel; loadedAt: number }
export type Snapshot =
  | SnapshotReady
  | { kind: "pending_edit"; model: BrowserModel; machine: MachineLedger; diagnostic: Diagnostic }
  | { kind: "migration_required"; descriptor: ProjectDescriptor }
  | { kind: "stale"; lastValid: SnapshotReady; diagnostic: Diagnostic }
  | { kind: "empty"; diagnostic?: Diagnostic };

export class ProjectRepository {
  constructor(private readonly vault: VaultPort) {}
  discover(): Promise<ProjectDescriptor[]>;
  load(project: ProjectDescriptor, previous?: SnapshotReady): Promise<Snapshot>;
  watch(project: ProjectDescriptor, refresh: () => void): () => void;
}
~~~

Load review and history first, hash exact UTF-8 bytes, parse ledger, require all three project IDs match, then join event decision IDs to canonical review decisions. Matching ledger hashes return ready. Valid human Markdown with newer hashes returns pending_edit: show the new human content immediately, retain the last validated machine accounting with an explicit “等待同步” label, and never call it current. Invalid ledger/schema/reference data returns stale with the last valid snapshot; it never produces a partially joined model.

- [ ] **Step 5: Implement bounded watch refresh**

Listen only to create/modify/rename/delete events whose normalized path equals one of the three project files. Debounce 150 ms, collapse bursts, cancel on view close, and ignore a self-write only when both path and resulting hash match the pending editor write.

- [ ] **Step 6: Run repository tests**

Run: cd obsidian-plugin && npm test -- --run tests/repository.test.ts tests/store.test.ts

Expected: PASS.

- [ ] **Step 7: Commit**

~~~bash
git add obsidian-plugin/src/data/repository.ts obsidian-plugin/src/data/vault-port.ts obsidian-plugin/src/state obsidian-plugin/tests
git commit -m "feat: load verified review projects"
~~~

### Task 4: Build the Interactive Evolution, Decision, and Usage Views

**Files:**
- Modify: obsidian-plugin/src/view/project-view.ts
- Create: obsidian-plugin/src/view/render-shell.ts
- Create: obsidian-plugin/src/view/render-evolution.ts
- Create: obsidian-plugin/src/view/render-decisions.ts
- Create: obsidian-plugin/src/view/render-usage.ts
- Create: obsidian-plugin/src/view/dom.ts
- Create: obsidian-plugin/styles.css
- Create: obsidian-plugin/tests/view.test.ts
- Modify: obsidian-plugin/src/main.ts

**Interfaces:**
- Consumes: BrowserStore and BrowserModel from Task 3.
- Produces: ProjectEvolutionView, renderShell, renderEvolution, renderDecisions, renderUsage, persisted view state.

- [ ] **Step 1: Write failing interaction tests**

~~~ts
it("switches adjacent node detail and returns from a decision to its event", async () => {
  const view = renderReadyView(browserModelFixture());
  click(view, '[data-event-id="timeline-release"]');
  expect(text(view, '[data-role="detail-title"]')).toBe("v0.1.0 公开发布");
  expect(view.querySelector('[data-event-id="timeline-release"]')?.getAttribute("aria-selected")).toBe("true");

  click(view, '[data-view="decisions"]');
  click(view, '[data-action="open-related-event"][data-event-id="timeline-trust-chain"]');
  expect(view.querySelector('[data-view="evolution"]')?.getAttribute("aria-selected")).toBe("true");
  expect(text(view, '[data-role="detail-title"]')).toBe("信任链与 dry-run 边界修复");
});
~~~

- [ ] **Step 2: Run view tests and verify RED**

Run: cd obsidian-plugin && npm test -- --run tests/view.test.ts

Expected: FAIL because view renderers do not exist.

- [ ] **Step 3: Implement the approved page state**

~~~ts
export interface ViewState {
  projectId: string | null;
  view: ViewKind;
  selectedEventId: string | null;
  fullHistory: boolean;
  historyQuery: string;
}
~~~

Header shows project name/status/sync time. Resume strip shows stage and one next action. Tabs use role=tablist/tab/tabpanel. Evolution uses a 340px left rail and flexible right detail, latest five nodes by default, full history search on demand, and event selection by stable ID. Right detail renders meaning, summary, why, changes, results, linked decisions, and next. Decisions render date/conclusion/reason/impact plus related-event action. Usage renders duration, total tokens, cost, model count, Token share, cost share, per-million prices, source, and effective date.

- [ ] **Step 4: Implement compact wide CSS**

Use a maximum content width of 1180px, 340px/minmax(0,1fr) evolution columns, 12–16px panel gaps, natural page scrolling, no fixed viewport-height dashboard, and an 860px single-column container/media breakpoint. Use Obsidian CSS variables for colors and typography. Do not hard-code a light-only palette.

- [ ] **Step 5: Persist only navigation state**

Save projectId, view, selectedEventId, fullHistory, and query through plugin data/workspace state. Never cache human document content or ledger candidates in plugin settings. If a selected ID no longer exists, select the newest event.

- [ ] **Step 6: Run interaction and build tests**

Run: cd obsidian-plugin && npm test -- --run tests/view.test.ts

Run: cd obsidian-plugin && npm run build

Expected: PASS; main.js builds with no TypeScript error.

- [ ] **Step 7: Commit**

~~~bash
git add obsidian-plugin/src/view obsidian-plugin/src/main.ts obsidian-plugin/styles.css obsidian-plugin/tests/view.test.ts
git commit -m "feat: add interactive project evolution browser"
~~~

### Task 5: Add Safe Inline Editing Back to Markdown

**Files:**
- Create: obsidian-plugin/src/data/editor.ts
- Create: obsidian-plugin/src/view/edit-modal.ts
- Create: obsidian-plugin/tests/editor.test.ts
- Modify: obsidian-plugin/src/view/project-view.ts
- Modify: obsidian-plugin/src/view/render-shell.ts
- Modify: obsidian-plugin/src/view/render-evolution.ts
- Modify: obsidian-plugin/src/view/render-decisions.ts

**Interfaces:**
- Consumes: Task 2 EditableField ranges, Vault.process, Task 3 repository reload.
- Produces: ReviewEditor.apply and EditModal for the exact human-editable field allowlist.

- [ ] **Step 1: Write failing stale-safe edit tests**

~~~ts
it("writes one allowed event field and rejects stale or machine fields", async () => {
  const vault = fakeVaultWithHistory(historyFixture());
  const editor = new ReviewEditor(vault);
  const source = vault.read(historyPath);
  const baseHash = sha256Text(source);
  await editor.apply({
    path: historyPath,
    expectedSha256: baseHash,
    document: "history",
    unitId: "timeline-trust-chain",
    field: "event.next",
    value: "发布补丁版本",
  });
  expect(parseHistory(vault.read(historyPath)).events[0].next).toBe("发布补丁版本");
  await expect(editor.apply({
    path: historyPath,
    expectedSha256: baseHash,
    document: "history",
    unitId: "timeline-trust-chain",
    field: "event.next",
    value: "旧编辑",
  })).rejects.toThrow(/stale edit/);
  await expect(editor.apply({
    path: historyPath,
    expectedSha256: sha256Text(vault.read(historyPath)),
    document: "history",
    unitId: "timeline-trust-chain",
    field: "evidence",
    value: "forged",
  })).rejects.toThrow(/field is read-only/);
});
~~~

- [ ] **Step 2: Run editor tests and verify RED**

Run: cd obsidian-plugin && npm test -- --run tests/editor.test.ts

Expected: FAIL because ReviewEditor does not exist.

- [ ] **Step 3: Implement the exact edit request**

~~~ts
export interface EditRequest {
  path: string;
  expectedSha256: string;
  document: "review" | "history";
  unitId: string;
  field: EditableFieldName;
  value: string | string[];
}

export class ReviewEditor {
  constructor(private readonly vault: VaultPort) {}
  async apply(request: EditRequest): Promise<{ sha256: string }> {
    let resultHash = "";
    await this.vault.process(request.path, current => {
      if (sha256Text(current) !== request.expectedSha256) throw new Error("stale edit");
      const next = patchAllowedField(current, request);
      parseByDocument(request.document, next);
      resultHash = sha256Text(next);
      return next;
    });
    return { sha256: resultHash };
  }
}
~~~

The allowlist must exactly match the core plan. The modal shows the current value, field-specific label, cancel/save, and an explicit “file changed; reload before editing” state. Empty required title/goal/status and values beyond configured rune limits are rejected before Vault.process.

- [ ] **Step 4: Wire edits to selected content**

Add edit actions to goal/stage/status/next/risk/decision/event sections. On success, announce through an aria-live region, reload from Vault, keep the current tab/node, and display “等待同步到代码目录” until ledger hashes/status catch up. Never edit accounting or sync badges.

- [ ] **Step 5: Run edit and view tests**

Run: cd obsidian-plugin && npm test -- --run tests/editor.test.ts tests/view.test.ts

Expected: PASS.

- [ ] **Step 6: Commit**

~~~bash
git add obsidian-plugin/src/data/editor.ts obsidian-plugin/src/view obsidian-plugin/tests
git commit -m "feat: edit review markdown from Obsidian"
~~~

### Task 6: Add Actionable Errors, Hidden Conflicts, and Allowlisted CLI Actions

**Files:**
- Create: obsidian-plugin/src/cli/runner.ts
- Create: obsidian-plugin/src/cli/settings.ts
- Create: obsidian-plugin/src/view/conflict-modal.ts
- Create: obsidian-plugin/src/view/status-banner.ts
- Create: obsidian-plugin/tests/cli.test.ts
- Create: obsidian-plugin/tests/conflict.test.ts
- Modify: obsidian-plugin/src/main.ts
- Modify: obsidian-plugin/src/data/repository.ts
- Modify: obsidian-plugin/src/view/project-view.ts

**Interfaces:**
- Consumes: core CLI sync status --json, sync resolve, sync repair-machine-ledger and hidden conflict JSON contract.
- Produces: CliRunner.status, resolve, repairMachineLedger and migrationDryRun plus specific actionable banners.

- [ ] **Step 1: Write failing command-injection and stale-conflict tests**

~~~ts
it("uses execFile without shell and rejects non-allowlisted arguments", async () => {
  const execFile = vi.fn((_file, _args, options, callback) => callback(null, '{"project_id":"project-1"}', ""));
  const runner = new CliRunner("/usr/local/bin/session-reviewer", execFile as never);
  await runner.status("project-0123456789abcdef");
  expect(execFile).toHaveBeenCalledWith(
    "/usr/local/bin/session-reviewer",
    ["sync", "status", "--json", "--project-id", "project-0123456789abcdef"],
    expect.objectContaining({ shell: false, windowsHide: true }),
    expect.any(Function),
  );
  await expect(runner.run(["sync", "--cwd", "x", "&&", "open", "/tmp"])).rejects.toThrow(/command is not allowed/);
});
~~~

- [ ] **Step 2: Run CLI/conflict tests and verify RED**

Run: cd obsidian-plugin && npm test -- --run tests/cli.test.ts tests/conflict.test.ts

Expected: FAIL because CliRunner and conflict UI do not exist.

- [ ] **Step 3: Implement executable verification and fixed commands**

Accept a configured absolute executable path only. On settings save, run version --json with execFile, shell=false, 10-second timeout, maxBuffer 1 MiB, and require a semantic version plus compatible review schema version. Store the path in plugin data; never read it from Markdown.

Expose only:

~~~ts
type AllowedAction =
  | { kind: "status"; projectId: string }
  | { kind: "migrationDryRun"; projectId: string }
  | { kind: "resolve"; projectId: string; conflictId: string; action: "accept_project" | "accept_obsidian" }
  | { kind: "manualMerge"; projectId: string; conflictId: string; file: string }
  | { kind: "repairMachineLedger"; projectId: string };
~~~

Validate project ID, conflict ID, and manual file; construct fixed argv arrays using --project-id so the CLI resolves its own trusted local Project/Vault mapping. Never store or expose the code repository absolute path in Markdown or ledger.json. For manual merge, create a 0600 temporary file from the explicit modal result, pass only that internally generated path, and delete it in finally.

- [ ] **Step 4: Implement distinct banners and conflict modal**

Map migration_required, content_conflict, machine_ledger_modified, history_parse_failed, review_parse_failed, stale_snapshot, sync_not_run, and cli_unavailable to different title, explanation, and action. Conflict modal shows Base/Project/Obsidian candidates as escaped preformatted text, the affected semantic unit, and explicit resolution confirmation. Re-run status before resolve; if hashes/ID changed, refuse and refresh.

- [ ] **Step 5: Verify last-valid snapshot behavior**

Corrupt each input independently and assert the browser keeps the prior valid content, marks it stale with timestamp, never renders corrupt candidates into normal detail, and restores ready state after the file is repaired.

- [ ] **Step 6: Run security and UI tests**

Run: cd obsidian-plugin && npm test -- --run tests/cli.test.ts tests/conflict.test.ts tests/repository.test.ts

Expected: PASS.

- [ ] **Step 7: Commit**

~~~bash
git add obsidian-plugin/src/cli obsidian-plugin/src/view obsidian-plugin/src/data/repository.ts obsidian-plugin/src/main.ts obsidian-plugin/tests
git commit -m "feat: resolve review sync issues safely"
~~~

### Task 7: Complete Accessibility, Large-History, and Responsive Behavior

**Files:**
- Create: obsidian-plugin/src/view/virtual-list.ts
- Create: obsidian-plugin/tests/accessibility.test.ts
- Create: obsidian-plugin/tests/large-history.test.ts
- Modify: obsidian-plugin/src/view/render-evolution.ts
- Modify: obsidian-plugin/src/view/render-shell.ts
- Modify: obsidian-plugin/styles.css

**Interfaces:**
- Consumes: Task 4 renderers and ViewState.
- Produces: keyboard-complete tabs/listbox, bounded rendering for 20,000 events, narrow-width layout.

- [ ] **Step 1: Write failing keyboard and large-list tests**

~~~ts
it("navigates tabs and event nodes by keyboard without rendering all history", () => {
  const model = browserModelWithEvents(20_000);
  const root = renderReadyView(model, { fullHistory: true });
  expect(root.querySelectorAll('[role="option"]').length).toBeLessThanOrEqual(80);
  focus(root, '[data-event-id="event-00000"]');
  keydown(root, "ArrowDown");
  expect(document.activeElement?.getAttribute("data-event-id")).toBe("event-00001");
  keydown(root, "Enter");
  expect(text(root, '[data-role="detail-title"]')).toBe("Event 1");
});
~~~

- [ ] **Step 2: Run tests and verify RED**

Run: cd obsidian-plugin && npm test -- --run tests/accessibility.test.ts tests/large-history.test.ts

Expected: FAIL because all events render and keyboard roving focus is absent.

- [ ] **Step 3: Implement bounded full-history rendering**

Use a fixed row-height virtual window with 20 overscan rows and a hard maximum of 80 DOM options. Filtering occurs on normalized lower-case title/kind/date text and preserves selected ID when present. Latest-five mode renders exactly min(5,eventCount) nodes.

- [ ] **Step 4: Implement keyboard and focus behavior**

Tabs support Left/Right/Home/End and aria-selected. Timeline uses listbox/option with roving tabindex, Up/Down/Home/End/Enter. After a decision reverse-jump, focus the selected event and update the detail heading through aria-live without moving focus into the detail pane. Modals trap focus and restore it on close.

- [ ] **Step 5: Verify responsive semantics**

At widths below 860px, CSS changes to one column without hiding actions, truncating titles, or introducing horizontal page overflow. Long hashes/prices only appear in expandable machine details and wrap with overflow-wrap:anywhere.

- [ ] **Step 6: Run complete plugin checks**

Run: cd obsidian-plugin && npm run check

Expected: lint, all Vitest suites, typecheck, and production bundle PASS.

- [ ] **Step 7: Commit**

~~~bash
git add obsidian-plugin/src/view obsidian-plugin/styles.css obsidian-plugin/tests
git commit -m "feat: harden project browser interaction"
~~~

### Task 8: CI, Release Assets, and Real macOS/Windows Obsidian Acceptance

**Files:**
- Modify: .github/workflows/ci.yml
- Modify: .github/workflows/release.yml
- Modify: README.md
- Create: obsidian-plugin/tests/package.test.ts
- Create: scripts/build-obsidian-plugin.sh
- Create: scripts/build-obsidian-plugin.ps1
- Create: docs/release/acceptance-obsidian-evolution-browser.md

**Interfaces:**
- Consumes: complete plugin and core v2 release assets.
- Produces: reproducible main.js/manifest.json/styles.css release bundle and current real-UI acceptance evidence.

- [ ] **Step 1: Write failing package-content test**

~~~ts
it("packages only the installable plugin assets with matching versions", async () => {
  const archive = await buildPluginArchive("0.2.0");
  expect(archive.entries.sort()).toEqual([
    "session-reviewer/main.js",
    "session-reviewer/manifest.json",
    "session-reviewer/styles.css",
  ]);
  expect(archive.manifest.version).toBe("0.2.0");
  expect(archive.manifest.id).toBe("session-reviewer");
  expect(archive.mainJs).not.toContain("sourceMappingURL=data:");
});
~~~

- [ ] **Step 2: Run package test and verify RED**

Run: cd obsidian-plugin && npm test -- --run tests/package.test.ts

Expected: FAIL because packaging scripts do not exist.

- [ ] **Step 3: Add cross-platform reproducible packaging**

Both shell and PowerShell scripts run npm ci, npm run check, normalize archive mtimes from SOURCE_DATE_EPOCH, include exactly main.js/manifest.json/styles.css under session-reviewer/, and emit SHA256SUMS. Package tests build twice and compare bytes and hashes.

- [ ] **Step 4: Extend CI and tag release**

CI adds Node 22 npm ci/check/package jobs on macOS and Windows. Release verifies package version equals the Git tag version and manifest/versions mapping, then uploads session-reviewer-obsidian-<version>.zip plus checksum alongside CLI archives. A future Community Plugins submission must use a GitHub Release tag identical to manifest version; it is not silently performed by this implementation.

- [ ] **Step 5: Update installation and operation docs**

Document manual installation into <Vault>/.obsidian/plugins/session-reviewer/, enabling Community Plugins, opening “SessionReviewer: 打开项目脉络”, configuring/verifying the CLI path on macOS and Windows, editing fields, syncing back to code, migration-required state, conflict actions, and machine-ledger repair.

- [ ] **Step 6: Run all automated gates**

Run: go test ./...

Run: go test -race ./... -skip '^TestFoundationLargeSessionReachesBoundedPacketAfterStreamingPast20MiB$'

Run: go vet ./...

Run: cd obsidian-plugin && npm ci && npm run check

Run: scripts/build-obsidian-plugin.sh 0.2.0

Run on Windows PowerShell: .\scripts\build-obsidian-plugin.ps1 -Version 0.2.0

Expected: every command exits 0; both package implementations produce the same logical entries and verified checksums.

- [ ] **Step 7: Perform real macOS UI acceptance**

In a clean test Vault and the real configured project:

1. Install the built three-file plugin package and enable it.
2. Open the project browser and verify header/resume strip/tabs.
3. Click at least five timeline nodes and compare every detail field to 项目历史.md.
4. Switch decisions/usage and verify reverse-jump plus total duration, tokens, cost, model shares, prices, source, and date.
5. Edit a node in the page, sync, and verify the code-side Markdown change.
6. Edit a different code-side unit, sync, and verify live page refresh.
7. Create a same-unit conflict, compare three candidates, resolve, and require repeat status/dry-run convergence.
8. Resize wide, 860px, and narrow panes; exercise keyboard-only navigation.

- [ ] **Step 8: Perform real Windows UI acceptance**

Repeat install, open, five-node switching, one page edit, one code-side edit, one conflict resolution, CLI-path validation, status, and repeat dry-run on Windows x64. Record Obsidian version, plugin version, CLI version, relative Vault mapping, sanitized hashes/counts, and visible results without private absolute paths.

- [ ] **Step 9: Commit**

~~~bash
git add .github/workflows README.md obsidian-plugin/tests/package.test.ts scripts/build-obsidian-plugin.sh scripts/build-obsidian-plugin.ps1 docs/release/acceptance-obsidian-evolution-browser.md
git commit -m "feat: package and verify Obsidian project browser"
~~~

## Final Plugin Verification

- [ ] Run git diff --check for the complete plugin implementation range.
- [ ] Run npm ci/check/package twice from a clean obsidian-plugin directory.
- [ ] Run full Go tests/race/vet because plugin fixtures and release workflows share the core contract.
- [ ] Inspect the production main.js for embedded sourcemaps, absolute local paths, dynamic eval, shell command construction, and raw candidate logging.
- [ ] Confirm the release archive contains exactly main.js, manifest.json, and styles.css under the plugin ID directory.
- [ ] Repeat macOS and Windows real-UI acceptance after the final review fix.
- [ ] Confirm Project/Vault repeat status and dry-run are fully converged and no unrelated working-tree changes entered the release commit.
