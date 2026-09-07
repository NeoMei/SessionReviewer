import { describe, expect, it, vi } from "vitest";
import type { SessionSummaryRequest } from "../src/cli/runner";
import { renderSessionSummary } from "../src/view/render-session-summary";
import { populatedSessionSummary, sessionSummaryFixture } from "./fixtures/session-summary";

const DIGEST = `sha256:${"1".repeat(64)}`;
const binding: SessionSummaryRequest = {
  projectId: "project-p", provider: "opencode", sessionId: "session-1",
  expectedGenerationId: "generation-1", expectedSessionViewDigest: DIGEST
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

async function flushPromises(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("retained Session summary", () => {
  it("renders five compact blocks with exact returned counts and literal authenticated entries", async () => {
    const view = renderSessionSummary(binding, async () => populatedSessionSummary());
    document.body.append(view);
    await flushPromises();

    expect([...view.querySelectorAll("details.sr-summary-block > summary")].map((node) => node.textContent)).toEqual([
      "阶段边界 · 已返回 1 / 共 1 · 未展示 0", "关键操作 · 已返回 32 / 共 40 · 未展示 8", "结果与验证 · 已返回 1 / 共 1 · 未展示 0",
      "错误 · 已返回 0 / 共 0 · 未展示 0 · 没有捕获到匹配事实", "遗留问题 · 已返回 0 / 共 0 · 未展示 0 · 没有捕获到匹配事实"
    ]);
    expect([...view.querySelectorAll<HTMLDetailsElement>("details.sr-summary-block")].every((node) => !node.open)).toBe(true);
    expect(view.textContent).toContain("<script>window.pwned = true</script>");
    expect(view.querySelector("script")).toBeNull();
    expect(view.textContent).toContain("没有捕获到匹配事实");
    expect(view.querySelector(".sr-summary-item-meta")?.textContent).toBe("2026-09-07T00:01:00Z · 序列 1");
    view.remove();
  });

  it("expands blocks and revision sources independently with native keyboard disclosure", async () => {
    const view = renderSessionSummary(binding, async () => populatedSessionSummary());
    document.body.append(view);
    await flushPromises();
    const blocks = view.querySelectorAll<HTMLDetailsElement>("details.sr-summary-block");
    const first = blocks[0];
    const second = blocks[1];
    first.querySelector("summary")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(first.open).toBe(true);
    expect(second.open).toBe(false);
    const source = first.querySelector<HTMLDetailsElement>("details.sr-summary-source")!;
    source.querySelector("summary")!.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
    expect(source.open).toBe(true);
    expect(source.textContent).toContain("revision-1");
    expect(source.textContent).toContain("source-1");
    view.remove();
  });

  it("shows an honest empty-excerpt fallback and the retained typed error code", async () => {
    const summary = populatedSessionSummary();
    summary.key_operations.items[0].text = "";
    summary.errors = {
      total: 1, shown: 1, omitted: 0,
      coverage: { seen: 1, indexed: 1, collapsed: 0, unprojected: 0, undecodable: 0, truncated: 0 },
      items: [{ ...summary.key_operations.items[0], revision_id: "revision-error", source_revision_ids: ["source-error"], code: "command_failed" }]
    };
    const view = renderSessionSummary(binding, async () => summary);
    await flushPromises();

    expect(view.textContent).toContain("（该摘要条目没有可用摘录）");
    expect(view.textContent).toContain("错误代码：command_failed");
  });

  it("shows polite loading, bounded error and retry for the current binding", async () => {
    const pending = deferred<ReturnType<typeof sessionSummaryFixture>>();
    const load = vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValueOnce(sessionSummaryFixture());
    const view = renderSessionSummary(binding, load);
    expect(view.querySelector('[aria-live="polite"]')?.textContent).toContain("正在读取");
    pending.reject(new Error("/private/path secret stderr"));
    await flushPromises();
    expect(view.textContent).toContain("无法读取 Session 摘要");
    expect(view.textContent).not.toContain("/private/path");
    view.querySelector<HTMLButtonElement>('[data-action="retry-session-summary"]')!.click();
    await flushPromises();
    expect(load).toHaveBeenCalledTimes(2);
    expect(view.textContent).toContain("阶段边界");
  });

  it("rejects wrong bindings, delayed A responses, disposed responses and duplicate unchanged identity loads", async () => {
    const a = deferred<ReturnType<typeof sessionSummaryFixture>>();
    const load = vi.fn((request: SessionSummaryRequest) => request.sessionId === "session-1"
      ? a.promise
      : Promise.resolve(populatedSessionSummary({ session_id: request.sessionId })));
    const view = renderSessionSummary(binding, load);
    view.updateIdentity(binding);
    expect(load).toHaveBeenCalledTimes(1);
    view.updateIdentity({ ...binding, sessionId: "session-2" });
    await flushPromises();
    expect(view.textContent).toContain("关键操作");
    const stale = populatedSessionSummary();
    stale.phase_boundaries.items[0].text = "A 过期摘要";
    a.resolve(stale);
    await flushPromises();
    expect(view.textContent).not.toContain("A 过期摘要");

    const wrong = renderSessionSummary(binding, async () => populatedSessionSummary({ project_id: "project-other" }));
    await flushPromises();
    expect(wrong.textContent).toContain("无法读取 Session 摘要");
    const disposing = deferred<ReturnType<typeof sessionSummaryFixture>>();
    const duringLoad = renderSessionSummary(binding, () => disposing.promise);
    duringLoad.dispose();
    disposing.resolve(populatedSessionSummary());
    await flushPromises();
    expect(duringLoad.childElementCount).toBe(0);
  });
});
