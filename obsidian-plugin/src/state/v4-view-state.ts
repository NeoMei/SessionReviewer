export type V4Tab = "evolution" | "problems" | "decisions" | "sessions" | "usage";

export interface V4ViewState {
  projectId: string;
  view: V4Tab;
  selectedMilestoneId: string | null;
  selectedProblemId: string | null;
}

export type V4ViewStates = Record<string, V4ViewState>;
export type SaveV4ViewStates = (states: V4ViewStates) => void | Promise<void>;

const PROJECT_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$/;
const TABS = new Set<V4Tab>(["evolution", "problems", "decisions", "sessions", "usage"]);

export function normalizeV4ViewState(value: unknown, projectId: string): V4ViewState {
  const fallback: V4ViewState = {
    projectId,
    view: "evolution",
    selectedMilestoneId: null,
    selectedProblemId: null
  };
  if (!record(value) || value.projectId !== projectId) return fallback;
  return {
    projectId,
    view: typeof value.view === "string" && TABS.has(value.view as V4Tab) ? value.view as V4Tab : "evolution",
    selectedMilestoneId: identity(value.selectedMilestoneId),
    selectedProblemId: identity(value.selectedProblemId)
  };
}

export function normalizeV4ViewStates(value: unknown): V4ViewStates {
  if (!record(value)) return {};
  const result: V4ViewStates = {};
  for (const [projectId, state] of Object.entries(value).slice(0, 1024)) {
    if (!PROJECT_ID.test(projectId) || !record(state) || state.projectId !== projectId) continue;
    result[projectId] = normalizeV4ViewState(state, projectId);
  }
  return result;
}

function identity(value: unknown): string | null {
  return typeof value === "string" && value.length > 0 && value.length <= 512 ? value : null;
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
