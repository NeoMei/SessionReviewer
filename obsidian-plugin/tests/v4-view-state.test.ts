import { describe, expect, it } from "vitest";
import { normalizeV4ViewState, normalizeV4ViewStates } from "../src/state/v4-view-state";

describe("v4 project view state", () => {
  it("defaults to project evolution and rejects state from another project", () => {
    expect(normalizeV4ViewState(undefined, "project-a")).toEqual({
      projectId: "project-a", view: "evolution", selectedMilestoneId: null, selectedProblemId: null,
      sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: null }
    });
    expect(normalizeV4ViewState({ projectId: "project-b", view: "usage", selectedMilestoneId: "milestone-b", selectedProblemId: "problem-b" }, "project-a"))
      .toEqual({ projectId: "project-a", view: "evolution", selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: null } });
  });

  it("keeps only closed tabs and bounded identity strings", () => {
    expect(normalizeV4ViewState({ projectId: "project-a", view: "sessions", selectedMilestoneId: "milestone-a", selectedProblemId: "problem-a" }, "project-a"))
      .toEqual({ projectId: "project-a", view: "sessions", selectedMilestoneId: "milestone-a", selectedProblemId: "problem-a", sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: null } });
    expect(normalizeV4ViewState({ projectId: "project-a", view: "legacy", selectedMilestoneId: 4, selectedProblemId: "x".repeat(513) }, "project-a"))
      .toEqual({ projectId: "project-a", view: "evolution", selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: null } });
  });

  it("normalizes a dedicated per-project map without coercing a legacy v3 state", () => {
    expect(normalizeV4ViewStates({
      "project-a": { projectId: "project-a", view: "problems", selectedMilestoneId: "milestone-a", selectedProblemId: "problem-a" },
      "project-b": { projectId: "project-b", view: "usage", selectedMilestoneId: null, selectedProblemId: null },
      "../invalid": { projectId: "../invalid", view: "sessions" }
    })).toEqual({
      "project-a": { projectId: "project-a", view: "problems", selectedMilestoneId: "milestone-a", selectedProblemId: "problem-a", sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: null } },
      "project-b": { projectId: "project-b", view: "usage", selectedMilestoneId: null, selectedProblemId: null, sessionBrowser: { query: "", provider: null, processingState: null, sourceAvailability: null, dateFrom: null, dateTo: null, unknownDateOnly: false, page: 0, selected: null } }
    });
    expect(normalizeV4ViewStates({ projectId: "project-a", view: "usage", selectedEventId: "legacy-event" })).toEqual({});
  });
});
