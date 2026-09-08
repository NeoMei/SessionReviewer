# Session Navigation Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after retained-event access; preserve the reviewed local Session filter/state implementation.

**Goal:** Finish the accepted long-event navigation and CLI-recovery behavior rather than treating four paging buttons as complete navigation.

**Architecture:** Preserve bounded fixed-argv `--anchor` queries. Carry a closed typed inspect error through the CLI runner so the host can refresh the authenticated index and restore the same namespaced Session near the prior ordinal. CLI absence gets one installation/recovery link; local index browsing stays available.

**Tech Stack:** Existing TypeScript renderer, CliRunner, ProjectEvolutionView and Vitest. No new package or persistent data schema.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§7.3,12.3,18.4; the existing Session-index and all-Sessions plans. This is a bounded missing-flow reconciliation, not a new layout design.

## Global Constraints

- Namespaced identity is `(project_id, provider, session_id)`. A refresh must not substitute a different Session or reuse a stale cursor.
- Event page size remains25 in the current UI and CLI limit remains1..100. Ordinal is a positive safe integer; no decimals, exponent notation, negative values or silent out-of-range clamping of direct user input.
- Responses remain at most1MiB, fixed argv with `shell:false`; no arbitrary paths/executables or shell command strings from source or Markdown.
- Ordinary reading starts zero Agents. Retained private facts remain readable without raw source files.
- No schema migration, provider implementation, real-Vault mutation or release inside these tasks. No hidden/raw text may enter public errors.

### Task 1: Expose ordinal jump and recover stale event pages

**Files:** Modify `obsidian-plugin/src/cli/runner.ts`, `src/data/contracts-v4.ts`, `src/view/render-scan-records.ts`, `src/view/presentation.ts`, `src/view/render-v4-shell.ts`, `src/view/project-view.ts`; focused tests in `tests/cli.test.ts`, `tests/contracts-v4.test.ts` and `tests/render-scan-records.test.ts`; add `tests/session-event-recovery.test.ts`. Modify only the relevant navigation styles in `styles.css` if needed.

**Interfaces:** Add exported `SessionInspectError` with a closed code union: `stale_cursor | generation_mismatch | anchor_out_of_range | unavailable`. Its message is a fixed localized/public string, never CLI stderr. Existing event success parsing and identity checks remain unchanged. Add optional host callback `refreshSessionEvents(request: {provider: string; sessionId: string; ordinal: number}): Promise<void>` to the existing view options; source-backed conversation remains independent.

- [ ] RED runner tests return nonzero exit with exact bounded `{error:{code,message}}` JSON for stale_cursor, generation_mismatch and anchor_out_of_range. Assert the closed typed error survives, while malformed/duplicate/unknown JSON, unsafe code, arbitrary stderr/path and success-binding mismatch become generic unavailable without leaking text. Parse through existing strict JSON utilities, not permissive JSON.parse. The caller-supplied message is never displayed.
- [ ] RED successful-page binding regressions from the independent audit: a same-identity first response beginning at25 rather than0,100items for limit25, and a nonterminal25/50page with null next_cursor must all be rejected. Validate nonempty cursor topology, nonempty cursor strings, page item count against the request limit, first-request origin and inclusion of the requested anchor. Do not decode opaque cursors in the plugin. Preserve existing Go half-open ranges and legitimate final short pages.
- [ ] RED rendered navigation-continuity regressions: a next response skipping25..50 to50..75, an overlapping previous response, a first response not starting0, a last response not ending at total, or a response with changed total/coverage in the same immutable binding must not enter cache or become the displayed valid page. Carry the navigation direction and expected source-page boundary; validate successful transitions before caching. Cover valid adjacent/first/last/anchor transitions, retry and cache hits as well as rejection. Keep the last valid page readable with a fixed error rather than silently losing events.
- [ ] RED rendered tests use2438 events: enter ordinal1,1220 and2438; press Enter or the explicit 跳转 button; assert fixed authenticated request with `anchor` and no cursor, correct returned range, and selection of the requested event within its page. Zero events disables jump. Input0,2439,fractional,exponent,whitespace-only and unsafe integer makes no request and leaves the current valid page visible with an inline error.

```ts
expect(loadSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({
  provider: "codex", sessionId: "session-long", anchor: 1220, limit: 25
}));
expect(root.querySelector('[data-event-ordinal="1220"]')?.getAttribute("aria-selected")).toBe("true");
```

- [ ] RED actual-host stale flow: show a middle event, return stale_cursor, refresh the repository's authenticated index once, preserve the same provider/Session, and issue a new anchor request against the new generation/view. If the new retained count is smaller, only this recovery path clamps the previous location to the nearest valid event; show that the index changed. If no events or the Session is absent, show explicit empty/unavailable state, never another Session's detail. Keep all local filters unchanged; if they now exclude this Session, explain that selection is no longer available rather than bypassing filters.
- [ ] RED races: project change, filter change, Session change, view disposal or a newer refresh invalidates the pending recovery. At most one automatic refresh/reanchor is allowed per user navigation; a second stale response offers explicit retry and never loops. User navigation resets this allowance. Rejected refresh leaves a readable error and the full local index. No old cursor crosses the generation boundary.
- [ ] Run RED `npm --prefix obsidian-plugin test -- cli.test.ts render-scan-records.test.ts session-event-recovery.test.ts` before production edits.
- [ ] Implement the closed error mapper only for event inspection first, using bounded stdout retained by `run`. Add a labeled numeric-text ordinal field and submit handler to the existing event navigation; validate exact decimal grammar and count before loading. Preserve request epochs/cache keys and select requested ordinal using returned range, not assumed page alignment.
- [ ] Wire the refresh callback through presentation/shell to the existing ProjectEvolutionView repository refresh. Carry the original namespaced selection/ordinal only as ephemeral host state, with a recovery epoch and one-attempt guard; do not persist cursors or error payloads into Vault or plugin preferences. Reuse existing refresh/disposal/state-patch behavior instead of introducing a second project loader.
- [ ] GREEN focused tests, full plugin check and diff check. Render actual host at1200px,580px pane and390px viewport; verify keyboard submit, labels, console health and no clipping. Commit exact files and obtain independent spec/quality review. Native combined acceptance remains required later.

### Task 2: Provide one actionable CLI recovery and accurate empty states

**Files:** Modify `obsidian-plugin/src/view/presentation.ts`, `render-v4-shell.ts`, `render-scan-records.ts`, `render-session-summary.ts` and `render-conversation.ts` only for common recovery wording; corresponding focused host tests. Add no executable setting or installer subprocess.

**Interfaces:** Keep runtime discovery unchanged. The v4 host owns exactly one visible link with label `安装或恢复 CLI`, pointing to the fixed repository documentation `https://github.com/NeoMei/SessionReviewer/blob/main/README.zh-CN.md#构建测试与用户级安装`, with normal safe external-link attributes. Detail sections may explain unavailable features but must not duplicate recovery actions. Direct component use without a host may show one local link; nested host use suppresses it via an explicit option.

- [ ] RED missing-CLI host test: the full154-Session index and local filters work, all private loaders are uncalled, and exactly one keyboard-focusable recovery link has the fixed target. It states installation/discovery and plugin reload, not that editing arbitrary executable paths in Markdown will fix it. No download, install, process execution or external navigation occurs without the user's click.
- [ ] RED empty-state distinction: truly empty valid index says `公开索引中没有 Session。`; nonempty index with zero matching rows says `没有符合当前筛选条件的 Session。` and keeps filter-clear available. Old detail is removed in both cases. Missing index retains its existing rescan guidance and is not represented as an empty index.

```ts
expect(root.querySelectorAll('[data-action="recover-cli"]')).toHaveLength(1);
expect(root.textContent).toContain("没有符合当前筛选条件的 Session。");
expect(root.querySelector('[aria-label="Session 覆盖"]')).toBeNull();
```

- [ ] Run focused RED, implement small shared wording/ownership changes, and rerun full plugin check. Browser keyboard activation must resolve the fixed documentation target, not a source-provided URL. Record native no-CLI recovery as pending until combined candidate acceptance. Commit exact files and independent review.

## Preflight

| Shared surface | Producer / consumer | Resolution |
|---|---|---|
| Task1 runner / renderer | typed bounded error -> stale refresh | only three recovery codes accepted; all other failures remain generic |
| Task1 renderer / host | selected namespaced Session and ordinal -> new authenticated index | preserve identity and filters; one retry; never carry old cursor |
| Task1 input / CLI | exact ordinal -> existing anchor | direct invalid input rejected; recovery clamp explicit |
| Task1 success parser / runner / renderer | bounded cursor topology -> request binding -> adjacent page transition | all three layers must validate; five identity fields alone do not prove complete navigation |
| Task1/Task2 renderer | navigation state -> accurate empty/recovery guidance | Task2 changes no paging, loader or cache authority |
| Task2 components / host | no-CLI diagnostics -> one recovery action | host owns action, components do not create duplicates |

Ruling: Use one fixed installation/documentation entry instead of restoring removed executable settings — current runtime intentionally discovers verified user installations — cost if wrong is a reversible recovery-link adjustment, not an expanded executable-input surface.

Ruling: Include successful event-page request and transition authentication in Task1 — the2026-09-08 independent read-only audit found that identity-matching pages can exceed the requested limit, skip ranges or hide later events, violating the accepted no-silent-truncation requirement — cost if wrong is a bounded parser/runner/UI change caught by RED/GREEN and independent review. This does not expand the wire schema or relax source provenance.
