# V4 native pane responsiveness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve accessible v4 content when Obsidian's pane is narrow inside a wide window.

**Architecture:** Use the existing v4 shell as a named inline-size query container. Apply the existing compact layout to its available width, including nested Session browsers, without changing DOM order or data behavior. Preserve legacy viewport rules outside v4.

**Tech Stack:** CSS container queries, existing TypeScript renderer and esbuild, opt-in Playwright layout regression using an externally supplied module and browser executable (no new dependencies).

**Spec:** docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md (§5 and §12.3); accepted problem-context-layout-v3 prototype.

## Global Constraints

- 左侧只显示真实问题句及其父子层级，不重复“项目演进、决策与约定、模型价格”等顶部栏目。
- 主体区域显示当前问题的父路径、直接子问题和有限相关问题；右侧显示所选问题关联的问答与执行因果链。
- Preserve five top tabs and actual question order; compact layout stacks tree, context, detail in that order. Do not hide content to pass overflow checks.
- All CSS changes scoped to v4. No data/authentication/selection behavior changes, dependency installation, paid model calls, real Vault writes, push or release.
- Native acceptance is a controller gate in the explicitly authorized temporary Vault; browser fixtures alone do not close it.

### Task 1: Make v4 layout respond to pane width

**Files:**
- Modify: `obsidian-plugin/styles.css`
- Create: `obsidian-plugin/scripts/check-v4-pane-layout.mjs` (real renderer/browser assertions and documented CLI arguments)
- Create: `obsidian-plugin/tests/fixtures/v4-pane-layout.ts` (synthetic ledger/index fixture wrapper using the actual renderer; only Obsidian runtime primitive shim; existing typed lint and TypeScript project apply)

**Interfaces:**
- Consumes `renderMarkdownV4View` from `obsidian-plugin/src/view/presentation.ts`, existing contract ledger/index JSON, and production CSS.
- Produces no new runtime API; CLI test takes `--playwright-module` and `--browser-executable` paths and exits nonzero on a layout failure. Browser context/server/temp build resources close in finally blocks.

- [ ] **Step 1: Implement a behavioral browser regression before CSS changes.** Bundle the actual renderer with esbuild, use the existing contract's public-valid state, and mount inside a fixed-width host at viewport 1280. Fixture must include a multi-level question tree, source identity, long unbroken question text, a history detail, and Session/usage panels. Assert at host widths 580 and 760 that tree/context/detail vertically stack and all their rectangles remain inside the shell; at host width 1200 assert three ordered columns. Resize the same page from wide to narrow and back, retaining selected tab/node. Also check 390 viewport with a fitting host. Assert the real nested Session browser stacks at narrow pane width and usage/header content does not escape it. Do not equate document scrollWidth alone with success: shell clipping currently conceals overflow.

```js
const tree = await page.locator('.sr-v4-problem-tree').boundingBox();
const context = await page.locator('.sr-v4-problem-context').boundingBox();
const detail = await page.locator('.sr-v4-problem-detail').boundingBox();
assert(context.y >= tree.y + tree.height - 1);
assert(detail.y >= context.y + context.height - 1);
assert(detail.x + detail.width <= shell.x + shell.width + 1);
```

- [ ] **Step 2: Run RED.** Run the new script with existing module `/Users/neomei/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs` and executable `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`. Record expected failing assertion on the wide-window/narrow-pane layout, not an environment/import error.

- [ ] **Step 3: Apply minimal scoped CSS.** Establish named containment and transfer v4 compact rules from viewport-only matching to pane matching. Scope nested shared component compact rules under v4; keep legacy rules. Set relevant grid/flex children min-width:0 and text wrapping only where actual content escapes. Existing tab bar may scroll horizontally; content panels may not silently clip.

```css
.sr-v4-shell { container: sr-v4-pane / inline-size; }
@container sr-v4-pane (max-width: 860px) {
  .sr-v4-evolution, .sr-v4-problems { grid-template-columns: minmax(0, 1fr); }
}
```

This snippet shows the boundary, not the entire patch: include existing header/support/model/pricing compact rules and v4-scoped nested scan/event/conversation grids and definition layout. Avoid duplicating the same v4 compact block in both query types.

- [ ] **Step 4: Run GREEN and regression.** Repeat the browser script, capture narrow/wide screenshots to a uniquely created temporary output folder, then `npm --prefix obsidian-plugin run check` once. Record exact command/results, screenshot paths and limitations in the task report. Self-review no global layout/data changes.
- [ ] **Step 5: Commit only the three listed production/test files.** Use `git add obsidian-plugin/styles.css obsidian-plugin/scripts/check-v4-pane-layout.mjs obsidian-plugin/tests/fixtures/v4-pane-layout.ts` then `git commit -m "fix(obsidian): adapt v4 layout to native pane width"`. Controller independently reviews and reinstalls the candidate into the authorized temporary Vault; full spec restoration remains open.
