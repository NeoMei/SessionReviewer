import { describe, expect, it } from "vitest";
import { renderConflict } from "../src/view/conflict-modal";
import { reviewFailureText, statusPresentation } from "../src/view/status-banner";

describe("actionable recovery UI", () => {
  it("maps failures to distinct human actions", () => {
    expect(statusPresentation("migration_required").action).toBe("先预览迁移");
    expect(statusPresentation("machine_ledger_modified").action).toBe("修复机器账本");
    expect(statusPresentation("history_parse_failed").action).toBe("打开项目历史");
    expect(statusPresentation("sync_not_run").title).toContain("等待同步");
  });

  it("explains unsafe prompt input and session segment conflicts separately", () => {
    expect(reviewFailureText("E_PROPOSAL_UNSAFE_INPUT", "fallback")).toContain("敏感信息");
    expect(reviewFailureText("E_SESSION_SEGMENT_CONFLICT", "fallback")).toContain("重复或冲突");
  });

  it("renders conflict candidates as escaped text with explicit actions", () => {
    const root = renderConflict({
      id: "conflict-project-overview-a", unit: "project-overview/goal", base: "<b>base</b>", project: "<script>project</script>", obsidian: "vault"
    });
    expect(root.querySelector("script")).toBeNull();
    expect(root.textContent).toContain("<script>project</script>");
    expect(root.querySelectorAll('[data-resolution-action]').length).toBe(3);
  });
});
