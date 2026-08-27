import type { BrowserModel, EditableField, EditableFieldName, ViewKind } from "../contracts/review-v2";
import { button, element } from "./dom";
import { renderDecisions } from "./render-decisions";
import { renderEvolution } from "./render-evolution";
import { renderUsage } from "./render-usage";

export interface ViewState {
  projectId: string | null;
  view: ViewKind;
  selectedEventId: string | null;
  fullHistory: boolean;
  historyQuery: string;
}

export type SaveViewState = (state: ViewState) => void | Promise<void>;

export function defaultViewState(model?: BrowserModel): ViewState {
  return {
    projectId: model?.review.projectId ?? null,
    view: "evolution",
    selectedEventId: model?.events[0]?.id ?? null,
    fullHistory: false,
    historyQuery: ""
  };
}

export type EditHandler = (field: EditableField) => void;

export function renderReadyView(model: BrowserModel, initial?: Partial<ViewState>, save?: SaveViewState, onEdit?: EditHandler): HTMLElement {
  const root = element("div", { className: "session-reviewer-browser" });
  let state: ViewState = { ...defaultViewState(model), ...initial, projectId: model.review.projectId };
  if (!model.events.some((event) => event.id === state.selectedEventId)) state.selectedEventId = model.events[0]?.id ?? null;
  const update = (patch: Partial<ViewState>): void => {
    state = { ...state, ...patch };
    draw();
    void save?.(state);
  };
  const draw = (): void => {
    root.replaceChildren(renderHeader(model, onEdit), renderResume(model, onEdit), renderRisks(model, onEdit), renderTabs(state, update), renderPanel(model, state, update, onEdit));
  };
  draw();
  return root;
}

function renderHeader(model: BrowserModel, onEdit?: EditHandler): HTMLElement {
  const header = element("header", { className: "sr-header" });
  const identity = element("div");
  identity.append(element("span", { className: "sr-eyebrow", text: "SESSIONREVIEWER · 项目回顾" }), element("h1", { text: model.review.name }), editableText(element("p", { className: "sr-goal", text: model.review.goal }), model, "project-overview", "goal", onEdit));
  const meta = element("div", { className: "sr-header-meta" });
  meta.append(editableText(element("span", { className: "sr-status", text: model.review.status }), model, "project-overview", "status", onEdit), element("span", { text: model.lastSuccessfulSync ? `最近同步 ${formatDateTime(model.lastSuccessfulSync)}` : "尚未同步" }));
  header.append(identity, meta);
  return header;
}

function renderResume(model: BrowserModel, onEdit?: EditHandler): HTMLElement {
  const resume = element("section", { className: "sr-resume", attrs: { "aria-label": "继续工作" } });
  resume.append(
    element("div", { attrs: { "data-role": "resume-stage" } }, [element("span", { text: "当前阶段" }), editableText(element("strong", { text: model.review.stage }), model, "project-overview", "stage", onEdit)]),
    element("div", { attrs: { "data-role": "resume-next" } }, [element("span", { text: "下一步" }), editableText(element("strong", { text: model.review.nextAction }), model, "project-overview", "next_action", onEdit)])
  );
  return resume;
}

function renderTabs(state: ViewState, update: (patch: Partial<ViewState>) => void): HTMLElement {
  const tabs = element("div", { className: "sr-tabs", attrs: { role: "tablist", "aria-label": "项目视图" } });
  for (const [view, label] of [["evolution", "演进"], ["decisions", "决策"], ["usage", "用量"]] as const) {
    const tab = button(label, { role: "tab", "data-view": view, "aria-selected": String(state.view === view), tabindex: state.view === view ? "0" : "-1" });
    tab.addEventListener("click", () => update({ view }));
    tabs.append(tab);
  }
  return tabs;
}

function renderPanel(model: BrowserModel, state: ViewState, update: (patch: Partial<ViewState>) => void, onEdit?: EditHandler): HTMLElement {
  if (state.view === "decisions") return renderDecisions(model, update, onEdit);
  if (state.view === "usage") return renderUsage(model);
  return renderEvolution(model, state, update, onEdit);
}

function renderRisks(model: BrowserModel, onEdit?: EditHandler): HTMLElement {
  if (model.review.risks.length === 0) return element("div", { className: "sr-risks-empty" });
  const details = element("details", { className: "sr-risks" });
  details.append(element("summary", { text: `风险与待办 · ${model.review.risks.length}` }));
  for (const risk of model.review.risks) {
    const item = element("div", { className: "sr-risk" });
    item.append(
      editableText(element("strong", { text: risk.title }), model, risk.id, "risk.title", onEdit),
      editableText(element("span", { text: risk.status }), model, risk.id, "risk.status", onEdit),
      editableText(element("p", { text: risk.detail }), model, risk.id, "risk.detail", onEdit)
    );
    details.append(item);
  }
  return details;
}

function editableText(node: HTMLElement, model: BrowserModel, unitId: string, fieldName: EditableFieldName, onEdit?: EditHandler): HTMLElement {
  if (!onEdit) return node;
  const field = model.review.fields.find((candidate) => candidate.unitId === unitId && candidate.field === fieldName);
  if (!field) return node;
  const wrapper = element("span", { className: "sr-editable" }, [node]);
  const action = button("编辑", { class: "sr-edit", "data-edit-field": fieldName, "aria-label": `编辑 ${fieldName}` });
  action.className = "sr-edit";
  action.addEventListener("click", () => onEdit(field));
  wrapper.append(action);
  return wrapper;
}

function formatDateTime(value: string): string {
  const parsed = new Date(value);
  return Number.isNaN(parsed.valueOf()) ? value : parsed.toLocaleString("zh-CN", { hour12: false });
}
