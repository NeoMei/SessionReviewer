import type { ConversationRequest, SessionEventRequest } from "../cli/runner";
import type { ConversationPageV1 } from "../contracts/conversation-page";
import type { MachineLedgerV4, ReviewPresentationV4, SessionEventPageV1, SessionIndexV1 } from "../contracts/review-v4";
import type { ProjectDescriptor } from "../data/repository";
import type { V4Tab, V4ViewState } from "../state/v4-view-state";
import { normalizeV4ViewState } from "../state/v4-view-state";
import { button, element } from "./dom";
import { presentStatus, summarizeRisk } from "./presentation";
import { renderScanRecords, type ScanRecordsElement } from "./render-scan-records";
import { renderV4Decisions } from "./render-v4-decisions";
import { renderV4Evolution, type V4EvolutionUiState } from "./render-v4-evolution";
import { renderV4Problems } from "./render-v4-problems";
import { renderV4Usage } from "./render-v4-usage";

export interface RenderV4ShellOptions {
  initialState?: unknown;
  saveState?: (state: V4ViewState) => void | Promise<void>;
  cliUnavailable?: boolean;
  loadSessionEvents?: (request: SessionEventRequest) => Promise<SessionEventPageV1>;
  loadConversation?: (request: ConversationRequest) => Promise<ConversationPageV1>;
  eventPageCache?: Map<string, SessionEventPageV1>;
  snapshotStatus?: string;
  trustDetail?: HTMLElement;
}

export type V4ShellElement = HTMLElement & { scanRecords?: ScanRecordsElement; dispose: () => void };

const TABS: ReadonlyArray<readonly [V4Tab, string]> = [
  ["evolution", "项目演进"], ["problems", "问题脉络"], ["decisions", "决策与约定"], ["sessions", "全部 Sessions"], ["usage", "用量"]
];

export function renderV4Shell(
  descriptor: ProjectDescriptor,
  presentation: ReviewPresentationV4,
  index: SessionIndexV1 | undefined,
  ledger: MachineLedgerV4,
  open: (path: string) => void,
  options: RenderV4ShellOptions = {}
): V4ShellElement {
  const root = element("div", { className: "sr-v4-shell" }) as unknown as V4ShellElement;
  let state = normalizeV4ViewState(options.initialState, descriptor.projectId);
  if (state.selectedMilestoneId !== null && !presentation.timeline.some((item) => item.id === state.selectedMilestoneId)) state.selectedMilestoneId = null;
  if (state.selectedProblemId !== null && !presentation.problem_nodes.some((item) => item.id === state.selectedProblemId)) state.selectedProblemId = null;
  let records: ScanRecordsElement | undefined;
  let disposed = false;
  const evolutionUi: V4EvolutionUiState = { fullHistory: false, page: 0 };

  const update = (patch: Partial<V4ViewState>, focus = false): void => {
    if (disposed) return;
    const focusedMilestoneId = root.ownerDocument.activeElement instanceof HTMLElement && root.contains(root.ownerDocument.activeElement)
      ? root.ownerDocument.activeElement.dataset.v4MilestoneId
      : undefined;
    const previous = state.view;
    const next = { ...state, ...patch, projectId: descriptor.projectId };
    if (sameState(state, next)) {
      if (focus) root.querySelector<HTMLButtonElement>(`[data-v4-tab="${state.view}"]`)?.focus();
      return;
    }
    state = next;
    if (state.view === "evolution" && state.selectedMilestoneId === null) state.selectedMilestoneId = presentation.timeline.at(-1)?.id ?? null;
    if (state.view === "problems" && state.selectedProblemId === null) state.selectedProblemId = presentation.problem_root_ids[0] ?? presentation.problem_nodes[0]?.id ?? null;
    if (previous === "sessions" && state.view !== "sessions") disposeRecords();
    draw();
    void options.saveState?.(state);
    if (focus) root.querySelector<HTMLButtonElement>(`[data-v4-tab="${state.view}"]`)?.focus();
    else if (focusedMilestoneId) [...root.querySelectorAll<HTMLButtonElement>("[data-v4-milestone-id]")]
      .find((control) => control.dataset.v4MilestoneId === focusedMilestoneId)?.focus();
  };
  const draw = (): void => {
    if (disposed) return;
    const tablist = renderTabs(state.view, (view, focus) => update({ view }, focus));
    const panel = renderPanel();
    root.replaceChildren(renderHeader(descriptor, presentation, index, open, options), tablist, panel);
  };
  const renderPanel = (): HTMLElement => {
    if (state.view === "problems") return renderV4Problems(presentation, state, update, () => open(`${descriptor.root}/项目回顾.md`));
    if (state.view === "decisions") return renderV4Decisions(presentation, () => open(`${descriptor.root}/项目回顾.md`));
    if (state.view === "usage") {
      const currentPrices = new Set(ledger.current_pricing_snapshot_ids);
      return renderV4Usage(ledger.accounting, ledger.pricing_snapshots.filter((price) => currentPrices.has(price.snapshot_id)));
    }
    if (state.view === "sessions") {
      const panel = element("section", { className: "sr-v4-sessions", attrs: { "data-v4-panel": "sessions", role: "tabpanel" } });
      if (!index) panel.append(element("p", { className: "sr-empty", text: "当前快照没有已验证的 Session 索引；需要重新扫描后才能建立完整清单。" }));
      else {
        records = renderScanRecords(index, options);
        root.scanRecords = records;
        panel.append(records);
      }
      return panel;
    }
    return renderV4Evolution(presentation, state, update, () => open(`${descriptor.root}/项目历史.md`), evolutionUi);
  };
  const disposeRecords = (): void => {
    records?.dispose();
    records = undefined;
    root.scanRecords = undefined;
  };
  root.dispose = () => {
    disposed = true;
    disposeRecords();
    root.replaceChildren();
  };
  draw();
  return root;
}

function sameState(left: V4ViewState, right: V4ViewState): boolean {
  return left.projectId === right.projectId && left.view === right.view && left.selectedMilestoneId === right.selectedMilestoneId && left.selectedProblemId === right.selectedProblemId;
}

function renderTabs(selected: V4Tab, select: (view: V4Tab, focus: boolean) => void): HTMLElement {
  const tablist = element("div", { className: "sr-v4-tabs", attrs: { role: "tablist", "aria-label": "项目视图" } });
  for (const [view, label] of TABS) {
    const tab = button(label, { role: "tab", "data-v4-tab": view, "aria-selected": String(view === selected), tabindex: view === selected ? "0" : "-1" });
    tab.addEventListener("click", () => select(view, false));
    tab.addEventListener("keydown", (event) => {
      const current = TABS.findIndex(([candidate]) => candidate === view);
      let next = current;
      if (event.key === "ArrowRight") next = (current + 1) % TABS.length;
      else if (event.key === "ArrowLeft") next = (current - 1 + TABS.length) % TABS.length;
      else if (event.key === "Home") next = 0;
      else if (event.key === "End") next = TABS.length - 1;
      else return;
      event.preventDefault();
      select(TABS[next][0], true);
    });
    tablist.append(tab);
  }
  return tablist;
}

function renderHeader(descriptor: ProjectDescriptor, presentation: ReviewPresentationV4, index: SessionIndexV1 | undefined, open: (path: string) => void, options: RenderV4ShellOptions): HTMLElement {
  const status = presentStatus(presentation.current_state.status);
  const header = element("header", { className: "sr-v4-header" }, [
    element("div", { className: "sr-v4-title" }, [element("h1", { text: descriptor.name }), element("span", { className: "sr-v4-status", text: options.snapshotStatus ?? "只读" }), renderCoverage(index)]),
    element("div", { className: "sr-v4-state-grid" }, [
      stateField("项目目标", presentation.current_state.goal, () => open(`${descriptor.root}/项目回顾.md`)),
      stateField("当前阶段", presentation.current_state.stage, () => open(`${descriptor.root}/项目回顾.md`)),
      stateField("当前状态", presentation.current_state.status, () => open(`${descriptor.root}/项目回顾.md`), status.label),
      stateField("下一步", presentation.current_state.next_action, () => open(`${descriptor.root}/项目回顾.md`))
    ]),
    element("div", { className: "sr-v4-support" }, [
      renderAttention(presentation),
      renderPinnedDecisions(presentation),
      options.trustDetail ?? renderProvenance(descriptor, presentation)
    ])
  ]);
  return header;
}

function renderCoverage(index: SessionIndexV1 | undefined): HTMLElement {
  if (!index) return element("span", { className: "sr-v4-coverage", text: "Session 索引未经当前验证" });
  const value = index.coverage;
  return element("span", { className: "sr-v4-coverage", text: `Sessions ${value.total} · 完整 ${value.complete} · 部分 ${value.partial} · 异常 ${value.error} · 未处理 ${value.unprocessed} · 来源不可用 ${value.source_unavailable}` });
}

function stateField(label: string, value: string, open: () => void, shown = value): HTMLElement {
  const wrapper = element("div", { className: "sr-v4-state-field" }, [element("span", { text: label })]);
  if (value.trim()) wrapper.append(element("strong", { text: shown }));
  else {
    const action = button("未填写 · 打开 Markdown", { "data-v4-open": "review" });
    action.addEventListener("click", open);
    wrapper.append(action);
  }
  return wrapper;
}

function renderAttention(presentation: ReviewPresentationV4): HTMLElement {
  const details = element("details", { className: "sr-v4-attention" });
  details.append(element("summary", { text: `风险与待办 · ${presentation.risks.length + presentation.open_loops.length}` }));
  const list = element("ul");
  for (const risk of presentation.risks) list.append(element("li", { text: `${risk.title}：${summarizeRisk(risk.detail) || "未填写"}` }));
  for (const loop of presentation.open_loops) list.append(element("li", { text: `${loop.title}：${loop.question || "未填写"}` }));
  if (list.childElementCount === 0) list.append(element("li", { text: "尚无已记录的风险或未决项。" }));
  details.append(list);
  return details;
}

function renderPinnedDecisions(presentation: ReviewPresentationV4): HTMLElement {
  const decisions = [...presentation.decisions]
    .filter((decision) => decision.status === "active")
    .sort((left, right) => Number(right.pinned) - Number(left.pinned) || right.occurred_at.localeCompare(left.occurred_at) || left.id.localeCompare(right.id))
    .slice(0, 3);
  const wrapper = element("aside", { className: "sr-v4-active-decisions" }, [element("strong", { text: `当前生效决策 · ${decisions.length}` })]);
  for (const decision of decisions) wrapper.append(element("span", { text: decision.title }));
  if (decisions.length === 0) wrapper.append(element("span", { className: "sr-empty", text: "尚无已确认的生效决策。" }));
  return wrapper;
}

function renderProvenance(descriptor: ProjectDescriptor, presentation: ReviewPresentationV4): HTMLElement {
  const details = element("details", { className: "sr-v4-provenance" });
  details.append(element("summary", { text: "来源与只读说明" }), element("p", { text: "公开文件校验不等于私有接受证明；结构写入仍需受信 CLI/CAS。" }), element("p", { text: `project_id ${descriptor.projectId} · generation ${presentation.generation_id} · revision ${presentation.revision}` }));
  return details;
}
