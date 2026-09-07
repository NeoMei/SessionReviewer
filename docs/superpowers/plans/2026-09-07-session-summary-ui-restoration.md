# Session Summary UI Restoration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Implements the accepted Session detail hierarchy, not a new design.

**Goal:** Show authenticated deterministic Session summaries ahead of the existing Q/A and execution drilldown in 全部 Sessions.

**Architecture:** Reuse `SessionSummaryV1` and its strict parser. Add a fixed-argv CLI reader, then an independently disposable summary component mounted by the existing Session browser. A selected Session's generation and view digest bind every response; source availability does not prevent retained summary inspection.

**Tech Stack:** Existing TypeScript, Obsidian DOM, Vitest; no new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§7.3,12.3,17.2,18.4. Backend prerequisite: authenticated `LoadSessionSummary` and `inspect session-summary` runtime repair passes task review.

## Global Constraints

- Ordinary scans and deterministic projection start zero Agent processes.
- Only validated `session-summary-v1` is rendered. Never synthesize reasons, intentions, verification or resolution from assistant prose.
- Preserve exact project/provider/Session/generation/SessionView bindings. Never pass arbitrary paths or invoke a shell.
- Each of five blocks shows total, shown and omitted counts; unknown data is never zero-filled. Summary text is already bounded/redacted server-side and rendered as text, never HTML.
- Source-unavailable Sessions retain summaries when accepted private facts exist. Missing CLI is a single clear recovery message, not an empty success.
- No transcripts or summaries written into Vault or plugin settings. Existing Q/A and event pagination, trust fail-closed behavior, state persistence and disposal remain intact.
- No cross-platform expansion, migrations, model calls, dependency/schema/version changes, production Vault install or release.

### Task 1: Wire retained authenticated summaries into Session details

**Files:**
- Modify `obsidian-plugin/src/cli/runner.ts`, `obsidian-plugin/tests/cli.test.ts`.
- Create `obsidian-plugin/src/view/render-session-summary.ts`, `obsidian-plugin/tests/session-summary.test.ts`.
- Modify `obsidian-plugin/src/view/render-scan-records.ts`, `render-v4-shell.ts`, `presentation.ts`, `project-view.ts`, `obsidian-plugin/styles.css`.
- Modify existing `render-scan-records.test.ts` and `view.test.ts` only to cover integration without weakening existing assertions.
- Reuse `obsidian-plugin/src/data/contracts-v4.ts:parseSessionSummaryV1` and existing JSON contract fixtures.

**Interfaces:**

```ts
export interface SessionSummaryRequest {
  projectId: string;
  provider: string;
  sessionId: string;
  expectedGenerationId: string;
  expectedSessionViewDigest: string;
}
// CliRunner method; identity validation happens before starting child process.
getSessionSummary(request: SessionSummaryRequest): Promise<SessionSummaryV1>;
export type SessionSummaryElement = HTMLElement & {
  updateIdentity(request: SessionSummaryRequest): void;
  dispose(): void;
};
export function renderSessionSummary(
  request: SessionSummaryRequest,
  load: (request: SessionSummaryRequest) => Promise<SessionSummaryV1>
): SessionSummaryElement;
```

- [ ] **Write RED runner tests.** Assert exact argv below and existing `shell:false`, timeout and byte cap. Wrong project/provider/Session/generation/view digest, malformed/duplicate JSON and unknown fields reject. Invalid request never starts a process. Use the existing `SessionSummaryV1` fixture, not a partial cast.

```ts
expect(argv).toEqual(['inspect', 'session-summary', '--project-id', request.projectId,
  '--provider', request.provider, '--session-id', request.sessionId,
  '--expected-generation-id', request.expectedGenerationId, '--json']);
```

- [ ] **Write RED component tests.** The five sections are exactly `阶段边界`, `关键操作`, `结果与验证`, `错误`, `遗留问题`; entries retain timestamps and expandable revision references. Forty total/eight omitted is visible. Empty block copy says no captured matching facts, not no real-world issues. Test loading/error/retry, wrong bound response rejection, malicious HTML displayed literally, A→B delayed A response rejection, dispose during load, and no duplicate reload on unchanged identity.

```ts
const view = renderSessionSummary(binding, load);
document.body.append(view);
await flushPromises();
expect(view.textContent).toContain('关键操作');
expect(view.textContent).toContain('已返回 32 / 共 40 · 未展示 8');
expect(view.querySelector('script')).toBeNull();
```

- [ ] **Write RED real-browser-host tests.** Mount actual v4 Session host and assert summary precedes Q/A which precedes execution facts. Select a retained `source_availability=unavailable` Session with a valid SessionView digest: summary loader runs, source-backed Q/A does not. No digest: show no retained summary. Tab departure, project refresh/switch and close dispose the summary. Event-page changes must not refetch unchanged summary. Missing CLI preserves full index and a single summary recovery message.
- [ ] **Run RED:** `npm --prefix obsidian-plugin test -- session-summary.test.ts cli.test.ts render-scan-records.test.ts` and record expected missing-reader/component failure before production changes.
- [ ] **Implement runner.** Validate safe IDs and digest using existing helpers. Invoke the exact argv through the existing bounded runner, parse strictly, compare every response identity, and return bounded localized typed errors (reuse existing inspection diagnostic policy). Do not expose arbitrary stderr.
- [ ] **Implement independent summary component.** Maintain request epoch plus complete binding key; ignore outdated results, preserve retry only for current binding, and invalidate on dispose. Keep the initial summary compact: five native, initially closed block disclosures with their titles and total/shown/omitted counters always visible; expanding one reveals its bounded items and does not expand the other blocks. Empty blocks visibly say no captured matching facts even while collapsed. Label server `shown` as returned summary entries, not as currently expanded DOM entries. Entries show text, timestamps and source IDs in native disclosures. Empty item text shows an honest no-excerpt fallback, never a blank paragraph; error entries expose their bounded typed code (or a known localized equivalent). Test empty excerpts, error codes, keyboard expansion and independent block state. Use a polite loading/error status. No model summaries or invented labels indicating resolved/passed without explicit facts.
- [ ] **Wire through v4 host.** Thread optional `loadSessionSummary` from ProjectEvolutionView to RenderV4ShellOptions/ScanRecordsOptions. Render before Q/A, after compact Session identity/coverage. Manage summary identity/lifecycle alongside conversation but independently: available accepted facts suffice even if source logs are gone. Keep existing event/Q/A requests and cache binding unchanged.
- [ ] **Verify GREEN:** focused command, then `npm --prefix obsidian-plugin run check`; run `git diff --check`. Exercise the rendered real component with valid fixture data on desktop/narrow widths, keyboard disclosure/retry and stale-response sequence; no network/real Vault needed for this bounded component check. Native product acceptance remains a separate gate.
- [ ] **Commit exact task files** with `feat(obsidian): show retained session summaries`; report RED/GREEN, binding/lifecycle evidence and remaining S07 filters/search requirements. Do not mark product complete.
