import { describe, expect, it } from "vitest";
import type { ScanStatus } from "../src/contracts/review-v3";
import { scanActionLabel } from "../src/view/status-banner";

describe("Gate B Obsidian Plugin Acceptance", () => {
  it("verifies Schema 3 contracts and single 更新项目脉络 action", () => {
    expect(scanActionLabel(undefined)).toBe("更新项目脉络");
    const running: ScanStatus = {
      schema_version: 1,
      job_id: "scan-1",
      project_id: "project-1",
      state: "running",
      phase: "extracting",
      session_count: 3,
      indexed_count: 1,
      issue_count: 0
    };
    expect(scanActionLabel(running)).toBe("正在提取脉络");
  });
});