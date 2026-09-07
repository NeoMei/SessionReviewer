import { WorkspaceLeaf } from "obsidian";
import { describe, expect, it, vi } from "vitest";
import { SyncStatusError, type CliRunner, type ConversationRequest, type SessionSummaryRequest } from "../src/cli/runner";
import type { ConversationPageV1 } from "../src/contracts/conversation-page";
import type { MachineLedgerV4, ReviewPresentationV4, SessionEventPageV1, SessionIndexEntryV1, SessionIndexV1, SessionSummaryV1 } from "../src/contracts/review-v4";
import type { Snapshot } from "../src/data/repository";
import { renderMarkdownV4View } from "../src/view/presentation";
import { ProjectEvolutionView } from "../src/view/project-view";
import { defaultViewState } from "../src/view/render-shell";
import { populatedSessionSummary } from "./fixtures/session-summary";
import { normalizeV4ViewState, type V4ViewState, type V4ViewStatePatch } from "../src/state/v4-view-state";

const VIEW_DIGEST = `sha256:${"1".repeat(64)}`;
const PROJECT_DIGEST = `sha256:${"2".repeat(64)}`;

const emptyPresentation: ReviewPresentationV4 = {
  schema_version: 4, minimum_reader_version: "0.4.0", minimum_writer_version: "0.4.0", project_id: "project-p", generation_id: "generation-1",
  project_view_digest: PROJECT_DIGEST, revision: 1,
  current_state: { goal: "goal", stage: "stage", status: "status", next_action: "next", last_verification: "" },
  timeline: [], decisions: [], risks: [], open_loops: [], problem_map_revision: 0, problem_root_ids: [], problem_nodes: [], chain_dependencies: [], human_patches: [], orphan_patches: [], generated_baselines: []
};

const emptyLedger: MachineLedgerV4 = {
  schema_version: 4, minimum_reader_version: "0.4.0", minimum_writer_version: "0.4.0", project_id: "project-p", generation_id: "generation-1",
  project_view_digest: PROJECT_DIGEST, accepted_revision: 1, review_sha256: "0".repeat(64), history_sha256: "0".repeat(64),
  accounting: { total_duration_ms: 0, total_tokens: 0, total_cost_usd: null, models: [] }, sessions: [], human_patches: [], orphan_patches: [], generated_baselines: [], pricing_snapshots: [], current_pricing_snapshot_ids: [],
  sync_hashes: { review_sha256: "0".repeat(64), history_sha256: "0".repeat(64), ledger_sha256: "0".repeat(64), session_index_digest: `sha256:${"0".repeat(64)}` }
};

function sessionFixture(overrides: Partial<SessionIndexEntryV1> = {}): SessionIndexEntryV1 {
  return {
    provider: "codex",
    session_id: "session-1",
    processing_state: "partial",
    state_reason_codes: ["unsupported_source_records"],
    source_availability: "available",
    source_terminal_state: null,
    started_at: "2026-09-06T16:00:00Z",
    ended_at: "2026-09-06T17:36:34.520Z",
    duration_ms: 5_794_520,
    warning_count: 1,
    record_count: 4_767,
    indexed_event_count: 2,
    coverage: { seen: 4_767, indexed: 2, collapsed: 0, unprojected: 0, undecodable: 4_765, truncated: 0 },
    fact_counts: { file_change: 0, command: 0, verification: 0, error: 0, artifact: 1 },
    session_view_digest: VIEW_DIGEST,
    usage_record_digest: null,
    summary_digest: null,
    last_seen_generation_id: "generation-1",
    last_successful_generation_id: "generation-1",
    ...overrides
  };
}

function indexFixture(sessions: SessionIndexEntryV1[] = [sessionFixture()]): SessionIndexV1 {
  const counts = {
    complete: sessions.filter((session) => session.processing_state === "complete").length,
    partial: sessions.filter((session) => session.processing_state === "partial").length,
    error: sessions.filter((session) => session.processing_state === "error").length,
    unprocessed: sessions.filter((session) => session.processing_state === "unprocessed").length
  };
  return {
    schema_version: 1,
    minimum_reader_version: "0.4.0",
    digest: `sha256:${"0".repeat(64)}`,
    project_id: "project-p",
    generation_id: "generation-1",
    project_view_digest: PROJECT_DIGEST,
    generated_at: "2026-09-07T00:00:00Z",
    sort_version: "started-at-desc-null-last-provider-session-v1",
    coverage: {
      total: sessions.length,
      ...counts,
      source_available: sessions.filter((session) => session.source_availability === "available").length,
      source_unavailable: sessions.filter((session) => session.source_availability === "unavailable").length,
      started_at_known: sessions.filter((session) => session.started_at !== null).length,
      ended_at_known: sessions.filter((session) => session.ended_at !== null).length,
      usage_known: sessions.filter((session) => session.usage_record_digest !== null).length
    },
    sessions
  };
}

function snapshot(index = indexFixture()): Extract<Snapshot, { kind: "markdown-v4" }> {
  return {
    kind: "markdown-v4",
    descriptor: { projectId: "project-p", root: "Projects/SessionReviewer", name: "SessionReviewer", format: "markdown-v4" },
    state: {
      kind: "public_valid",
      index,
      value: {
        presentation: { ...emptyPresentation, project_id: index.project_id }, changedFields: [], changedDocuments: [], fields: []
      },
      ledger: { ...emptyLedger, project_id: index.project_id }
    },
    loadedAt: 1
  };
}

function projectSnapshot(projectId: string, name: string, excerptDigest = VIEW_DIGEST): Extract<Snapshot, { kind: "markdown-v4" }> {
  const index = indexFixture([sessionFixture({ session_id: `session-${name.toLocaleLowerCase()}`, session_view_digest: excerptDigest })]);
  index.project_id = projectId;
  return {
    ...snapshot(index),
    descriptor: { projectId, root: `Projects/${name}`, name, format: "markdown-v4" }
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function eventPage(overrides: Partial<SessionEventPageV1> = {}): SessionEventPageV1 {
  return {
    schema_version: 1,
    minimum_reader_version: "0.4.0",
    project_id: "project-p",
    provider: "codex",
    session_id: "session-1",
    generation_id: "generation-1",
    session_view_digest: VIEW_DIGEST,
    total: 2,
    range_start: 0,
    range_end: 1,
    items: [{ kind: "message", excerpt: "用户问题示例\n  保留空白", revision_id: "revision-1", sequence: 10, occurred_at: "2026-09-06T16:00:00Z" }],
    previous_cursor: null,
    next_cursor: "next-token",
    first_cursor: "first-token",
    last_cursor: "last-token",
    coverage: { seen: 4_767, indexed: 2, collapsed: 0, unprojected: 0, undecodable: 4_765, truncated: 0 },
    ...overrides
  };
}

function conversationFor(request: { projectId?: string; sessionId?: string; expectedGenerationId?: string; turnUnitId?: string }, excerpt = "可见用户问题"): ConversationPageV1 {
  const sessionId = request.sessionId ?? "session-1";
  const selected = request.turnUnitId !== undefined;
  const user = {
    role: "user" as const, phase: null, revision_id: `sha256:${"4".repeat(64)}`,
    source_ref: { provider: "codex" as const, session_id: sessionId, source_identity: "source-1", record_ordinal: 1, source_hash: "5".repeat(64) },
    occurred_at: "2026-09-07T00:00:00Z", visible_excerpt: excerpt, truncated: false,
    text: selected ? excerpt : null, text_truncated: false
  };
  return {
    schema_version: 1, minimum_reader_version: "0.4.0", mode: selected ? "turn_messages" : "turn_index",
    project_id: request.projectId ?? "project-p", provider: "codex", session_id: sessionId,
    generation_id: request.expectedGenerationId ?? "generation-1", session_view_digest: VIEW_DIGEST,
    dependency_digest: `sha256:${"6".repeat(64)}`, redaction_version: "visible-redaction-v1", turn_unit_id: selected ? request.turnUnitId! : null,
    total: 1, range_start: 0, range_end: 1, first_cursor: "first", previous_cursor: null, next_cursor: null, last_cursor: "last",
    turn_units: [{ turn_unit_id: "turn-1", ordinal: 1, started_at: "2026-09-07T00:00:00Z", ended_at: null, user_message: { ...user, text: null }, answer_state: "no_answer", assistant_message_count: 0 }],
    messages: selected ? [user] : [],
    coverage: { source_records: 1, visible_messages: 1, captured_messages: 1, truncated_messages: 0, truncated_bodies: 0, context_messages: 0, orphan_messages: 0, oversized_records: 0, malformed_records: 0, complete: true }
  };
}

function summaryFor(request: SessionSummaryRequest, text = "已保留关键事实"): SessionSummaryV1 {
  const result = populatedSessionSummary({
    project_id: request.projectId,
    provider: request.provider,
    session_id: request.sessionId,
    generation_id: request.expectedGenerationId,
    session_view_digest: request.expectedSessionViewDigest
  });
  result.phase_boundaries.items[0].text = text;
  return result;
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("v4 scanned Session renderer", () => {
  it("mounts retained summary before Q/A and execution facts in the actual Sessions host", async () => {
    const loadSessionSummary = vi.fn((request: SessionSummaryRequest) => Promise.resolve(summaryFor(request)));
    const loadConversation = vi.fn((request: ConversationRequest) => Promise.resolve(conversationFor(request)));
    const loadSessionEvents = vi.fn().mockResolvedValue(eventPage());
    const root = renderMarkdownV4View(snapshot(), () => {}, { loadSessionSummary, loadConversation, loadSessionEvents });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    await settle();
    await settle();

    const area = root.querySelector('[class="sr-event-area"]')!;
    const children = [...area.children];
    expect(children.map((node) => node.getAttribute("aria-label"))).toEqual([
      "Session 覆盖", "Session 保留摘要", "问答记录", "已索引执行事实"
    ]);
    expect(root.textContent).toContain("已保留关键事实");
  });

  it("loads retained authenticated facts without raw sources and never starts source-backed Q/A", async () => {
    const retained = sessionFixture({ source_availability: "unavailable", indexed_event_count: 0 });
    const loadSessionSummary = vi.fn((request: SessionSummaryRequest) => Promise.resolve(summaryFor(request)));
    const loadConversation = vi.fn();
    const root = renderMarkdownV4View(snapshot(indexFixture([retained])), () => {}, { loadSessionSummary, loadConversation });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    await settle();

    expect(loadSessionSummary).toHaveBeenCalledTimes(1);
    expect(loadConversation).not.toHaveBeenCalled();
    expect(root.textContent).toContain("已保留关键事实");
    expect(root.textContent).toContain("该 Session 的问答来源不可用");
  });

  it("does not request retained summary without a SessionView digest and explains why none is shown", () => {
    const loadSessionSummary = vi.fn();
    const noDigest = sessionFixture({ session_view_digest: null });
    const root = renderMarkdownV4View(snapshot(indexFixture([noDigest])), () => {}, { loadSessionSummary });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();

    expect(loadSessionSummary).not.toHaveBeenCalled();
    expect(root.querySelectorAll(".sr-summary-unavailable")).toHaveLength(1);
    expect(root.textContent).toContain("当前 Session 没有可验证的摘要绑定；未显示保留摘要。");
  });

  it("keeps summary identity independent from event paging and disposes it on tab departure", async () => {
    const late = deferred<SessionSummaryV1>();
    const loadSessionSummary = vi.fn().mockReturnValue(late.promise);
    const loadSessionEvents = vi.fn().mockResolvedValue(eventPage());
    const root = renderMarkdownV4View(snapshot(), () => {}, { loadSessionSummary, loadSessionEvents });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    await settle();
    root.querySelector<HTMLButtonElement>('[data-action="next-event-page"]')!.click();
    await settle();
    expect(loadSessionSummary).toHaveBeenCalledTimes(1);

    root.querySelector<HTMLButtonElement>('[data-v4-tab="evolution"]')!.click();
    late.resolve(summaryFor({ projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1", expectedSessionViewDigest: VIEW_DIGEST }, "过期摘要"));
    await settle();
    expect(root.textContent).not.toContain("过期摘要");
  });

  it("keeps the full index usable with one summary recovery notice when CLI is missing", () => {
    const root = renderMarkdownV4View(snapshot(indexFixture([sessionFixture(), sessionFixture({ session_id: "session-2" })])), () => {}, { cliUnavailable: true });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();

    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(2);
    expect(root.querySelectorAll(".sr-summary-unavailable")).toHaveLength(1);
    expect(root.textContent?.match(/无法读取 Session 摘要/g)).toHaveLength(1);
    expect(root.textContent).toContain("完整 Session 清单仍可浏览");
    expect(root.textContent).not.toContain("已索引执行事实仍可阅读");
    root.querySelector<HTMLButtonElement>('[data-session-id="session-2"]')!.click();
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(2);
  });

  it("renders partial coverage and pages through literal indexed excerpts", async () => {
    const loadSessionEvents = vi.fn()
      .mockResolvedValueOnce(eventPage())
      .mockResolvedValueOnce(eventPage({
        range_start: 1,
        range_end: 2,
        items: [{ kind: "tool_result", excerpt: "后续执行结果", revision_id: "revision-2", sequence: 20, occurred_at: "2026-09-06T17:36:34.520Z" }],
        previous_cursor: "previous-token",
        next_cursor: null
      }));
    const root = renderMarkdownV4View(snapshot(), () => {}, { loadSessionEvents });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();

    expect(root.querySelector('[aria-label="扫描 Session"]')).not.toBeNull();
    expect(root.textContent).toContain("部分");
    expect(root.textContent).toContain("未解码 4,765");
    await settle();
    expect(root.textContent).toContain("用户问题示例\n  保留空白");
    root.querySelector<HTMLButtonElement>('[data-action="next-event-page"]')?.click();
    await settle();

    expect(root.textContent).toContain("后续执行结果");
    expect(root.textContent).not.toContain("用户问题示例");
    expect(loadSessionEvents).toHaveBeenNthCalledWith(2, expect.objectContaining({ cursor: "next-token", limit: 25 }));
  });

  it("retries a failed next page with the same cursor instead of returning to page one", async () => {
    const secondPage = eventPage({
      range_start: 1,
      range_end: 2,
      items: [{ kind: "tool_result", excerpt: "重试后的第二页", revision_id: "revision-2", sequence: 20, occurred_at: "2026-09-06T17:36:34.520Z" }],
      previous_cursor: "previous-token",
      next_cursor: null
    });
    const loadSessionEvents = vi.fn()
      .mockResolvedValueOnce(eventPage())
      .mockRejectedValueOnce(new Error("第二页暂时不可用"))
      .mockResolvedValueOnce(secondPage);
    const root = renderMarkdownV4View(snapshot(), () => {}, { loadSessionEvents });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    await settle();

    root.querySelector<HTMLButtonElement>('[data-action="next-event-page"]')?.click();
    await settle();
    expect(root.textContent).toContain("第二页暂时不可用");
    root.querySelector<HTMLButtonElement>('[data-action="retry-event-page"]')?.click();
    await settle();

    expect(root.textContent).toContain("重试后的第二页");
    expect(root.textContent).not.toContain("用户问题示例");
    expect(loadSessionEvents).toHaveBeenNthCalledWith(2, expect.objectContaining({ cursor: "next-token" }));
    expect(loadSessionEvents).toHaveBeenNthCalledWith(3, expect.objectContaining({ cursor: "next-token" }));
  });

  it("pages and searches a bounded Session list", () => {
    const sessions = Array.from({ length: 26 }, (_value, index) => sessionFixture({
      session_id: `session-${String(index + 1).padStart(2, "0")}`,
      processing_state: "complete",
      state_reason_codes: [],
      indexed_event_count: 0,
      coverage: { seen: 0, indexed: 0, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 },
      session_view_digest: null
    }));
    const root = renderMarkdownV4View(snapshot(indexFixture(sessions)), () => {}, { cliUnavailable: true });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();

    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(25);
    expect(root.textContent).not.toContain("session-26");
    root.querySelector<HTMLButtonElement>('[data-action="next-session-page"]')?.click();
    expect(root.textContent).toContain("session-26");
    const search = root.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.value = "session-03";
    search.dispatchEvent(new Event("input"));
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(1);
    expect(root.textContent).toContain("session-03");
  });

  function manySessions(): SessionIndexEntryV1[] {
    return Array.from({ length: 154 }, (_value, index) => sessionFixture({
      session_id: `session-${String(154 - index).padStart(3, "0")}`,
      processing_state: index === 1 ? "error" : "complete",
      source_availability: index === 2 ? "unavailable" : "available",
      state_reason_codes: [], indexed_event_count: 0,
      coverage: { seen: 0, indexed: 0, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 },
      session_view_digest: null
    }));
  }

  it("selects the first filtered row when a local query excludes the current detail", () => {
    const root = renderMarkdownV4View(snapshot(indexFixture(manySessions())), () => {}, { cliUnavailable: true });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    const search = root.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.value = "session-001";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(1);
    expect(root.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-001");
  });

  it("removes stale Session detail when a local query returns no rows", () => {
    const root = renderMarkdownV4View(snapshot(indexFixture(manySessions())), () => {}, { cliUnavailable: true });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    const search = root.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.value = "does-not-exist";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    expect(root.querySelector('[aria-label="Session 覆盖"]')).toBeNull();
    expect(root.textContent).toContain("Session 覆盖：共 154");
  });

  it("renders the current filtered range and global accepted count independently", () => {
    const root = renderMarkdownV4View(snapshot(indexFixture(manySessions())), () => {}, { cliUnavailable: true });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(25);
    expect(root.textContent).toContain("1–25 / 共154");
    expect(root.textContent).toContain("Session 覆盖：共 154");
  });

  it("uses first, previous, next and last navigation and selects a visible row after page changes", () => {
    const sessions = Array.from({ length: 154 }, (_value, index) => sessionFixture({
      session_id: `session-${String(154 - index).padStart(3, "0")}`,
      processing_state: "complete", state_reason_codes: [], indexed_event_count: 0,
      coverage: { seen: 0, indexed: 0, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 }, session_view_digest: null
    }));
    const root = renderMarkdownV4View(snapshot(indexFixture(sessions)), () => {}, { cliUnavailable: true });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();

    root.querySelector<HTMLButtonElement>('[data-action="last-session-page"]')!.click();
    expect(root.textContent).toContain("151–154 / 共154");
    expect(root.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-004");
    expect(root.textContent).toContain("session-001");
    root.querySelector<HTMLButtonElement>('[data-action="first-session-page"]')!.click();
    expect(root.textContent).toContain("1–25 / 共154");
    expect(root.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-154");
  });

  it("applies every local rail filter without CLI, reports invalid dates and clears filters", () => {
    const sessions = [
      sessionFixture({ provider: "codex", session_id: "known-complete", processing_state: "complete", state_reason_codes: [], started_at: "2026-09-07T16:30:00Z", indexed_event_count: 0, session_view_digest: null }),
      sessionFixture({ provider: "claude", session_id: "known-error", processing_state: "error", source_availability: "unavailable", state_reason_codes: ["source_unavailable"], started_at: "2026-09-06T00:00:00Z", indexed_event_count: 0, session_view_digest: null }),
      sessionFixture({ provider: "opencode", session_id: "unknown-partial", processing_state: "partial", started_at: null, indexed_event_count: 0, session_view_digest: null })
    ];
    const saveStatePatch = vi.fn<(patch: V4ViewStatePatch) => void>();
    const root = renderMarkdownV4View(snapshot(indexFixture(sessions)), () => {}, { cliUnavailable: true, saveStatePatch });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();

    const provider = root.querySelector<HTMLSelectElement>('[aria-label="筛选 Provider"]')!;
    provider.value = "claude";
    provider.dispatchEvent(new Event("change", { bubbles: true }));
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(1);
    expect(root.textContent).toContain("known-error");
    expect(saveStatePatch.mock.lastCall?.[0].sessionBrowser?.provider).toBe("claude");
    expect(saveStatePatch.mock.lastCall?.[0].sessionBrowser?.selected).toEqual({ provider: "claude", sessionId: "known-error" });

    root.querySelector<HTMLButtonElement>('[data-action="clear-session-filters"]')!.click();
    const unknown = root.querySelector<HTMLInputElement>('[aria-label="仅未知日期"]')!;
    unknown.checked = true;
    unknown.dispatchEvent(new Event("change", { bubbles: true }));
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(1);
    expect(root.textContent).toContain("unknown-partial");

    root.querySelector<HTMLButtonElement>('[data-action="clear-session-filters"]')!.click();
    const from = root.querySelector<HTMLInputElement>('[aria-label="起始日期"]')!;
    const to = root.querySelector<HTMLInputElement>('[aria-label="结束日期"]')!;
    from.value = "2026-09-08";
    from.dispatchEvent(new Event("change", { bubbles: true }));
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(1);
    to.value = "2026-09-07";
    to.dispatchEvent(new Event("change", { bubbles: true }));
    expect(root.querySelector('[role="status"]')?.textContent).toContain("日期范围无效");
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(1);

    root.querySelector<HTMLButtonElement>('[data-action="clear-session-filters"]')!.click();
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(3);
  });

  it("restores a namespaced same-ID selection on its filtered page after leaving Sessions", () => {
    const sessions = Array.from({ length: 60 }, (_value, index) => sessionFixture({
      provider: index === 35 ? "claude" : "codex",
      session_id: index === 0 || index === 35 ? "same-id" : `session-${String(index).padStart(3, "0")}`,
      processing_state: "complete", state_reason_codes: [], indexed_event_count: 0, session_view_digest: null
    }));
    const initialState = {
      projectId: "project-p", view: "sessions", selectedMilestoneId: null, selectedProblemId: null,
      sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: { provider: "claude", sessionId: "same-id" } }
    };
    const root = renderMarkdownV4View(snapshot(indexFixture(sessions)), () => {}, { cliUnavailable: true, initialState });
    expect(root.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("claude / same-id");
    expect(root.textContent).toContain("26–50 / 共60");

    root.querySelector<HTMLButtonElement>('[data-v4-tab="usage"]')!.click();
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    expect(root.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("claude / same-id");
    expect(root.textContent).toContain("26–50 / 共60");
  });

  it("rejects an oversized live query without applying or persisting it", () => {
    const saveStatePatch = vi.fn<(patch: V4ViewStatePatch) => void>();
    const root = renderMarkdownV4View(snapshot(indexFixture([sessionFixture(), sessionFixture({ session_id: "session-2" })])), () => {}, { cliUnavailable: true, saveStatePatch });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    const search = root.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    const callsBefore = saveStatePatch.mock.calls.length;
    search.value = "🙂".repeat(65);
    search.dispatchEvent(new Event("input", { bubbles: true }));
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(2);
    expect(root.querySelector('[role="status"]')?.textContent).toContain("256 个 UTF-8 字节");
    expect(saveStatePatch).toHaveBeenCalledTimes(callsBefore);
  });

  it("preserves the connected focused search input while typing and when an event request resolves", async () => {
    const pending = deferred<SessionEventPageV1>();
    const sessions = [sessionFixture(), sessionFixture({ session_id: "session-2" })];
    const root = renderMarkdownV4View(snapshot(indexFixture(sessions)), () => {}, { loadSessionEvents: () => pending.promise });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    document.body.append(root);
    const search = root.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.focus();

    search.value = "s";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    expect(search.isConnected).toBe(true);
    expect(document.activeElement).toBe(search);
    expect(root.querySelector('[aria-label="搜索 Session"]')).toBe(search);

    search.value = "session-2";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    expect(search.isConnected).toBe(true);
    expect(document.activeElement).toBe(search);
    expect(root.querySelectorAll("[data-session-id]")).toHaveLength(1);

    pending.resolve(eventPage());
    await settle();
    expect(search.isConnected).toBe(true);
    expect(document.activeElement).toBe(search);
    expect(root.querySelector('[aria-label="搜索 Session"]')).toBe(search);
    root.remove();
  });

  it("rejects pending Session detail responses after a filter becomes empty", async () => {
    const pendingEvent = deferred<SessionEventPageV1>();
    const pendingSummary = deferred<SessionSummaryV1>();
    const pendingConversation = deferred<ConversationPageV1>();
    const root = renderMarkdownV4View(snapshot(), () => {}, {
      loadSessionEvents: () => pendingEvent.promise,
      loadSessionSummary: () => pendingSummary.promise,
      loadConversation: () => pendingConversation.promise
    });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    const search = root.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.value = "no-match";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    expect(root.querySelector('[aria-label="Session 覆盖"]')).toBeNull();

    pendingEvent.resolve(eventPage({ items: [{ kind: "message", excerpt: "late event", revision_id: "revision-late", sequence: 1, occurred_at: "2026-09-08T00:00:00Z" }] }));
    pendingSummary.resolve(summaryFor({ projectId: "project-p", provider: "codex", sessionId: "session-1", expectedGenerationId: "generation-1", expectedSessionViewDigest: VIEW_DIGEST }, "late summary"));
    pendingConversation.resolve(conversationFor({}, "late conversation"));
    await settle();
    expect(root.textContent).not.toContain("late event");
    expect(root.textContent).not.toContain("late summary");
    expect(root.textContent).not.toContain("late conversation");
  });

  it("states unavailable sources, empty excerpts and absent responses without inventing roles", async () => {
    const loadSessionEvents = vi.fn().mockResolvedValue(eventPage({
      items: [{ kind: "artifact", excerpt: "", revision_id: "revision-1", sequence: 10, occurred_at: "2026-09-06T16:00:00Z" }]
    }));
    const root = renderMarkdownV4View(snapshot(), () => {}, { loadSessionEvents });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    await settle();

    expect(root.textContent).toContain("该索引事件没有可用摘录");
    expect(root.textContent).toContain("索引摘录（非完整对话）");
    expect(root.textContent).not.toContain("用户说");
    expect(root.textContent).not.toContain("助手回复");

    const unavailable = sessionFixture({
      source_availability: "unavailable",
      processing_state: "error",
      state_reason_codes: ["source_unavailable"],
      indexed_event_count: 0,
      coverage: { seen: 0, indexed: 0, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 },
      session_view_digest: null
    });
    const unavailableRoot = renderMarkdownV4View(snapshot(indexFixture([unavailable])), () => {}, { cliUnavailable: true });
    unavailableRoot.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    expect(unavailableRoot.textContent).toContain("来源不可用");
    expect(unavailableRoot.textContent).toContain("无法读取扫描记录：CLI 不可用");
  });

  it("loads visible Q/A for a zero-fact Session and keeps execution facts separate", async () => {
    const zeroFacts = sessionFixture({
      indexed_event_count: 0,
      coverage: { seen: 1, indexed: 0, collapsed: 1, unprojected: 0, undecodable: 0, truncated: 0 },
      fact_counts: { file_change: 0, command: 0, verification: 0, error: 0, artifact: 0 }
    });
    const loadSessionEvents = vi.fn();
    const loadConversation = vi.fn((request: ConversationRequest) => Promise.resolve(conversationFor(request)));
    const root = renderMarkdownV4View(snapshot(indexFixture([zeroFacts])), () => {}, { loadSessionEvents, loadConversation });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    await settle();
    await settle();

    expect(loadConversation).toHaveBeenCalledWith(expect.objectContaining({ sessionId: "session-1", limit: 20 }));
    expect(loadSessionEvents).not.toHaveBeenCalled();
    expect(root.textContent).toContain("可见用户问题");
    expect(root.textContent).toContain("这个 Session 没有可读的已索引事件");
    expect(root.querySelector('[aria-label="问答记录"]')).not.toBeNull();
    expect(root.querySelector('[aria-label="已索引执行事实"]')).not.toBeNull();
  });

  it("does not recreate or rerequest Q/A while event selection and Session search redraw", async () => {
    const loadSessionEvents = vi.fn().mockResolvedValue(eventPage());
    const loadConversation = vi.fn((request: ConversationRequest) => Promise.resolve(conversationFor(request)));
    const root = renderMarkdownV4View(snapshot(), () => {}, { loadSessionEvents, loadConversation });
    root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    document.body.append(root);
    await settle();
    await settle();
    const conversation = root.querySelector('[aria-label="问答记录"]');
    const calls = loadConversation.mock.calls.length;
    root.querySelector<HTMLButtonElement>("[data-event-ordinal]")?.click();
    const search = root.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.focus();
    search.value = "session";
    search.dispatchEvent(new Event("input"));

    expect(root.querySelector('[aria-label="问答记录"]')).toBe(conversation);
    expect(loadConversation).toHaveBeenCalledTimes(calls);
    expect(document.activeElement).toBe(search);
    root.remove();
  });
});

describe("v4 project and event lifecycle", () => {
  it("does not call any private loader when the actual host reports CLI unavailable", async () => {
    const project = { projectId: "project-p", root: "Projects/SessionReviewer", name: "SessionReviewer", format: "markdown-v4" as const };
    const repository = { discover: vi.fn().mockResolvedValue([project]), load: vi.fn().mockResolvedValue(projectSnapshot(project.projectId, "SessionReviewer")), watch: vi.fn().mockReturnValue(vi.fn()) };
    const runner = {
      status: vi.fn().mockRejectedValue(new SyncStatusError("cli_unavailable")),
      getSessionEvents: vi.fn(), getConversation: vi.fn(), getSessionSummary: vi.fn()
    };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, runner as unknown as CliRunner);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen();
    view.contentEl.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    await settle();
    expect(runner.getSessionEvents).not.toHaveBeenCalled();
    expect(runner.getConversation).not.toHaveBeenCalled();
    expect(runner.getSessionSummary).not.toHaveBeenCalled();
    expect(view.contentEl.querySelectorAll("[data-session-id]")).toHaveLength(1);
    await view.onClose();
  });

  it("restores independent Session browser state after project A to B to A", async () => {
    const projectA = { projectId: "project-a", root: "Projects/A", name: "A", format: "markdown-v4" as const };
    const projectB = { projectId: "project-b", root: "Projects/B", name: "B", format: "markdown-v4" as const };
    const repository = {
      discover: vi.fn().mockResolvedValue([projectA, projectB]),
      load: vi.fn(async (project: typeof projectA) => projectSnapshot(project.projectId, project.name)),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const states: Record<string, V4ViewState> = {};
    const save = (next: V4ViewState) => { states[next.projectId] = structuredClone(next); };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, undefined, defaultViewState(), undefined, states, save, (projectId) => states[projectId]);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });
    await view.onOpen();
    view.contentEl.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    let search = view.contentEl.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.value = "session-a";
    search.dispatchEvent(new Event("input", { bubbles: true }));

    let picker = view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')!;
    picker.value = "project-b";
    picker.dispatchEvent(new Event("change", { bubbles: true }));
    await settle();
    view.contentEl.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    search = view.contentEl.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')!;
    search.value = "session-b";
    search.dispatchEvent(new Event("input", { bubbles: true }));

    picker = view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')!;
    picker.value = "project-a";
    picker.dispatchEvent(new Event("change", { bubbles: true }));
    await settle();
    expect(view.contentEl.querySelector<HTMLInputElement>('[aria-label="搜索 Session"]')?.value).toBe("session-a");
    expect(states["project-b"].sessionBrowser?.query).toBe("session-b");
    await view.onClose();
  });
  it("restores a persisted project selection on reload", async () => {
    const projectA = { projectId: "project-a", root: "Projects/A", name: "A" };
    const projectB = { projectId: "project-b", root: "Projects/B", name: "B" };
    const repository = {
      discover: vi.fn().mockResolvedValue([projectA, projectB]),
      load: vi.fn(async (project: typeof projectA) => ({ kind: "migration_required", descriptor: project, diagnostic: { code: "migration_required", message: project.name } })),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, undefined, { ...defaultViewState(), projectId: projectB.projectId });

    await view.onOpen();

    expect(repository.load).toHaveBeenCalledWith(projectB, undefined);
    expect(view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')?.value).toBe(projectB.projectId);
    expect(view.contentEl.textContent).toContain("B");
    await view.onClose();
  });

  it("rediscovers newly added projects and preserves the current project on explicit refresh", async () => {
    const projectA = { projectId: "project-a", root: "Projects/A", name: "A" };
    const projectB = { projectId: "project-b", root: "Projects/B", name: "B" };
    const repository = {
      discover: vi.fn().mockResolvedValueOnce([projectA]).mockResolvedValueOnce([projectA, projectB]),
      load: vi.fn(async (project: typeof projectA) => ({ kind: "migration_required", descriptor: project, diagnostic: { code: "migration_required", message: project.name } })),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never);

    await view.onOpen();
    expect(view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')?.value).toBe(projectA.projectId);
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="refresh-projects"]')?.click();
    await settle();

    expect(repository.discover).toHaveBeenCalledTimes(2);
    expect(view.contentEl.querySelectorAll('[aria-label="选择项目"] option')).toHaveLength(2);
    expect(view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')?.value).toBe(projectA.projectId);
    await view.onClose();
  });

  it("keeps the selector available to escape a malformed selected project", async () => {
    const projectA = { projectId: "project-a", root: "Projects/A", name: "A", format: "markdown-v4" as const };
    const projectB = { projectId: "project-b", root: "Projects/B", name: "B" };
    const repository = {
      discover: vi.fn().mockResolvedValue([projectA, projectB]),
      load: vi.fn(async (project: typeof projectA) => project.projectId === projectA.projectId
        ? { kind: "markdown-v4", descriptor: projectA, state: { kind: "invalid", code: "wire_contract_invalid" }, loadedAt: 1 }
        : { kind: "migration_required", descriptor: projectB, diagnostic: { code: "migration_required", message: "B 可打开" } }),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never);

    await view.onOpen();
    expect(view.contentEl.textContent).toContain("wire_contract_invalid");
    const picker = view.contentEl.querySelector<HTMLSelectElement>('[aria-label="选择项目"]')!;
    picker.value = projectB.projectId;
    picker.dispatchEvent(new Event("change"));
    await settle();

    expect(view.contentEl.textContent).toContain("B 可打开");
    expect(view.contentEl.querySelector('[aria-label="选择项目"]')).not.toBeNull();
    await view.onClose();
  });

  it("keeps event pages cached for the same generation and resets them for a new generation", async () => {
    const project = { projectId: "project-p", root: "Projects/SessionReviewer", name: "SessionReviewer", format: "markdown-v4" as const };
    const first = projectSnapshot(project.projectId, "SessionReviewer");
    const second = structuredClone(first);
    if (second.state.kind !== "public_valid") throw new Error("expected public-valid snapshot");
    second.state.index.generation_id = "generation-2";
    second.state.index.sessions[0].last_seen_generation_id = "generation-2";
    second.state.index.sessions[0].last_successful_generation_id = "generation-2";
    const repository = {
      discover: vi.fn().mockResolvedValue([project]),
      load: vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(first).mockResolvedValueOnce(second),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const runner = {
      status: vi.fn().mockResolvedValue({}),
      getSessionEvents: vi.fn().mockImplementation((request: { expectedGenerationId: string }) => Promise.resolve(eventPage({ generation_id: request.expectedGenerationId, session_id: "session-sessionreviewer" })))
    };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, runner as unknown as CliRunner);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });

    await view.onOpen();
    view.contentEl.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();
    await settle();
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="refresh-v4-status"]')?.click();
    await settle();
    expect(runner.getSessionEvents).toHaveBeenCalledTimes(1);

    view.contentEl.querySelector<HTMLButtonElement>('[data-action="refresh-v4-status"]')?.click();
    await settle();
    expect(runner.getSessionEvents).toHaveBeenCalledTimes(2);
    expect(runner.getSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({ expectedGenerationId: "generation-2" }));
    await view.onClose();
  });

  it("retains a selected Session on its containing page after generation refresh and requests only the new binding", async () => {
    const project = { projectId: "project-p", root: "Projects/SessionReviewer", name: "SessionReviewer", format: "markdown-v4" as const };
    const sessions = Array.from({ length: 60 }, (_value, index) => sessionFixture({
      session_id: `session-${String(60 - index).padStart(3, "0")}`,
      processing_state: "complete", state_reason_codes: [], indexed_event_count: 1,
      coverage: { seen: 1, indexed: 1, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 }
    }));
    const first = snapshot(indexFixture(sessions));
    const second = structuredClone(first);
    if (second.state.kind !== "public_valid") throw new Error("expected public-valid snapshot");
    const newDigest = `sha256:${"9".repeat(64)}`;
    second.state.index.generation_id = "generation-2";
    second.state.index.sessions[35].session_view_digest = newDigest;
    second.state.index.sessions.forEach((entry) => {
      entry.last_seen_generation_id = "generation-2";
      entry.last_successful_generation_id = "generation-2";
    });
    const repository = {
      discover: vi.fn().mockResolvedValue([project]),
      load: vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(second),
      watch: vi.fn().mockReturnValue(vi.fn())
    };
    const runner = {
      status: vi.fn().mockResolvedValue({}),
      getSessionEvents: vi.fn((request: { sessionId: string; expectedGenerationId: string; expectedSessionViewDigest: string }) => Promise.resolve(eventPage({
        session_id: request.sessionId, generation_id: request.expectedGenerationId, session_view_digest: request.expectedSessionViewDigest
      }))),
      getSessionSummary: vi.fn((request: SessionSummaryRequest) => Promise.resolve(summaryFor(request)))
    };
    const initial = normalizeV4ViewState({
      projectId: "project-p", view: "sessions",
      sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: { provider: "codex", sessionId: "session-025" } }
    }, "project-p");
    const states = { "project-p": initial };
    const view = new ProjectEvolutionView(new WorkspaceLeaf(), repository as never, undefined, runner as unknown as CliRunner, defaultViewState(), undefined, states, undefined, () => states["project-p"]);
    Object.assign(view, { app: { workspace: { openLinkText: vi.fn() } } });

    await view.onOpen();
    await settle();
    expect(view.contentEl.textContent).toContain("26–50 / 共60");
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-025");
    view.contentEl.querySelector<HTMLButtonElement>('[data-action="refresh-v4-status"]')!.click();
    await settle();
    await settle();

    expect(view.contentEl.textContent).toContain("26–50 / 共60");
    expect(view.contentEl.querySelector('[aria-label="Session 覆盖"] h3')?.textContent).toBe("codex / session-025");
    expect(runner.getSessionEvents).toHaveBeenLastCalledWith(expect.objectContaining({
      sessionId: "session-025", expectedGenerationId: "generation-2", expectedSessionViewDigest: newDigest
    }));
    expect(runner.getSessionEvents.mock.lastCall?.[0]).not.toHaveProperty("cursor");
    expect(runner.getSessionSummary).toHaveBeenLastCalledWith(expect.objectContaining({
      sessionId: "session-025", expectedGenerationId: "generation-2", expectedSessionViewDigest: newDigest
    }));
    await view.onClose();
  });
});
