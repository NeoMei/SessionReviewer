import type { PricingActions } from "../cli/pricing";
import type { SessionSearchLoader } from "../cli/session-search";
import type { ConversationRequest, SessionEventRequest, SessionSummaryRequest } from "../cli/runner";
import type { ConversationPageV1 } from "../contracts/conversation-page";
import type { MachineLedgerV4, ReviewPresentationV4, SessionEventPageV1, SessionIndexV1, SessionSummaryV1 } from "../contracts/review-v4";
import type { ProjectDescriptor } from "../data/repository";
import type { ProblemCandidateV1, ProblemActions } from "./render-v4-problems";
import type { V4Tab, V4ViewState, V4ViewStatePatch } from "../state/v4-view-state";
import { normalizeV4ViewState } from "../state/v4-view-state";
import { normalizeSessionBrowserState } from "../state/session-browser-state";
import { button, element } from "./dom";
import { presentStatus, summarizeRisk } from "./presentation";
import { renderScanRecords, type ScanRecordsElement } from "./render-scan-records";
import { renderV4Decisions } from "./render-v4-decisions";
import { renderV4Evolution, type V4EvolutionElement, type V4EvolutionUiState } from "./render-v4-evolution";
import { renderV4Problems } from "./render-v4-problems";
import { renderV4Usage } from "./render-v4-usage";
import { defaultV4MilestoneId } from "./v4-milestone-order";

export interface RenderV4ShellOptions {
  initialState?: unknown;
  saveState?: (state: V4ViewState) => void | Promise<void>;
  saveStatePatch?: (patch: V4ViewStatePatch) => void | Promise<void>;
  loadSessionSearch?: SessionSearchLoader;
    pricingActions?: PricingActions;
  cliUnavailable?: boolean;
  loadSessionEvents?: (request: SessionEventRequest) => Promise<SessionEventPageV1>;
  loadConversation?: (request: ConversationRequest) => Promise<ConversationPageV1>;
  loadSessionSummary?: (request: SessionSummaryRequest) => Promise<SessionSummaryV1>;
  eventPageCache?: Map<string, SessionEventPageV1>;
  refreshSessionEvents?: (request: { provider: string; sessionId: string; ordinal: number }) => Promise<void>;
  cancelSessionEventRecovery?: () => void;
  recoverySession?: { provider: string; sessionId: string };
  initialSessionEventOrdinal?: number;
  recoveryAlreadyAttempted?: boolean;
  recoverySelectionUnavailable?: string;
  snapshotStatus?: string;
  trustDetail?: HTMLElement;
  problemCandidates?: ProblemCandidateV1[];
  createProblem?: ProblemActions["createProblem"];
  transitionCandidate?: ProblemActions["transitionCandidate"];
  setProblemState?: ProblemActions["setProblemState"];
  editProblem?: ProblemActions["editProblem"];
  moveProblem?: ProblemActions["moveProblem"];
  reorderProblems?: ProblemActions["reorderProblems"];
  problemUnavailableReason?: string;
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
  let evolution: V4EvolutionElement | undefined;
  let disposed = false;
  let recoveryPending = options.recoverySession !== undefined;
  const evolutionUi: V4EvolutionUiState = { fullHistory: false, page: 0 };

  const persist = (patch: V4ViewStatePatch): void => {
    if (options.saveStatePatch) void options.saveStatePatch(patch);
    else void options.saveState?.(state);
  };

  const update = (patch: Partial<V4ViewState>, focus = false): void => {
    if (disposed) return;
    const focusedMilestoneId = root.ownerDocument.activeElement instanceof HTMLElement && root.contains(root.ownerDocument.activeElement)
      ? root.ownerDocument.activeElement.dataset.v4MilestoneId
      : undefined;
    const previous = state.view;
    const next = { ...state, ...patch, projectId: descriptor.projectId };
    const requestedMilestoneId = next.selectedMilestoneId;
    const requestedProblemId = next.selectedProblemId;
    if (sameState(state, next)) {
      if (focus) root.querySelector<HTMLButtonElement>(`[data-v4-tab="${state.view}"]`)?.focus();
      return;
    }
    state = next;
    const persisted: V4ViewStatePatch = { ...patch };
    if (state.view === "evolution" && state.selectedMilestoneId === null) state.selectedMilestoneId = defaultV4MilestoneId(presentation.timeline);
    if (state.view === "problems" && state.selectedProblemId === null) state.selectedProblemId = presentation.problem_root_ids[0] ?? presentation.problem_nodes[0]?.id ?? null;
    if (state.selectedMilestoneId !== requestedMilestoneId) persisted.selectedMilestoneId = state.selectedMilestoneId;
    if (state.selectedProblemId !== requestedProblemId) persisted.selectedProblemId = state.selectedProblemId;
    if (previous === "sessions" && state.view !== "sessions") disposeRecords();
    if (previous === "evolution" && state.view !== "evolution") disposeEvolution();
    draw();
    persist(persisted);
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
    if (state.view === "problems") return renderV4Problems(presentation, state, update, () => open(`${descriptor.root}/项目回顾.md`), {
      candidates: options.problemCandidates, unavailableReason: options.problemUnavailableReason, createProblem: options.createProblem,
      transitionCandidate: options.transitionCandidate, setProblemState: options.setProblemState, editProblem: options.editProblem, moveProblem: options.moveProblem, reorderProblems: options.reorderProblems,
      loadConversation: options.cliUnavailable ? undefined : options.loadConversation
    });
    if (state.view === "decisions") return renderV4Decisions(presentation, () => open(`${descriptor.root}/项目回顾.md`));
    if (state.view === "usage") {
      const currentPrices = new Set(ledger.current_pricing_snapshot_ids);
      return renderV4Usage(ledger.accounting, ledger.pricing_snapshots.filter((price) => currentPrices.has(price.snapshot_id)), options.cliUnavailable ? {} : options.pricingActions);
    }
    if (state.view === "sessions") {
      const panel = element("section", { className: "sr-v4-sessions", attrs: { "data-v4-panel": "sessions", role: "tabpanel" } });
      if (!index) panel.append(element("p", { className: "sr-empty", text: "当前快照没有已验证的 Session 索引；需要重新扫描后才能建立完整清单。" }));
      else {
        const privateLoaders = options.cliUnavailable ? {
          loadSessionSearch: undefined,
          loadSessionEvents: undefined,
          loadConversation: undefined,
          loadSessionSummary: undefined
        } : {
          loadSessionSearch: options.loadSessionSearch,
          loadSessionEvents: options.loadSessionEvents,
          loadConversation: options.loadConversation,
          loadSessionSummary: options.loadSessionSummary
        };
        const recovery = recoveryPending ? {
          recoverySession: options.recoverySession,
          initialSessionEventOrdinal: options.initialSessionEventOrdinal,
          recoveryAlreadyAttempted: options.recoveryAlreadyAttempted,
          recoverySelectionUnavailable: options.recoverySelectionUnavailable
        } : {
          recoverySession: undefined,
          initialSessionEventOrdinal: undefined,
          recoveryAlreadyAttempted: undefined,
          recoverySelectionUnavailable: undefined
        };
        records = renderScanRecords(index, {
          ...options,
          ...privateLoaders,
          ...recovery,
          initialState: state.sessionBrowser,
          onStateChange: (sessionBrowser) => {
            state = { ...state, sessionBrowser };
            persist({ sessionBrowser });
          }
        });
        if (recoveryPending && options.recoverySession && !options.recoverySelectionUnavailable) {
          state = { ...state, sessionBrowser: { ...normalizeSessionBrowserState(state.sessionBrowser), selected: options.recoverySession } };
        }
        recoveryPending = false;
        root.scanRecords = records;
        panel.append(records);
      }
      return panel;
    }
    disposeEvolution();
    evolution = renderV4Evolution(presentation, state, update, () => open(`${descriptor.root}/项目历史.md`), evolutionUi, {
      index,
      loadConversation: options.cliUnavailable ? undefined : options.loadConversation,
      cliUnavailable: options.cliUnavailable
    });
    return evolution;
  };
  const disposeRecords = (): void => {
    records?.dispose();
    records = undefined;
    root.scanRecords = undefined;
  };
  const disposeEvolution = (): void => {
    evolution?.dispose();
    evolution = undefined;
  };
  root.dispose = () => {
    disposed = true;
    disposeRecords();
    disposeEvolution();
    root.replaceChildren();
  };
  draw();
  return root;
}

function sameState(left: V4ViewState, right: V4ViewState): boolean {
  return left.projectId === right.projectId && left.view === right.view && left.selectedMilestoneId === right.selectedMilestoneId &&
    left.selectedProblemId === right.selectedProblemId && JSON.stringify(left.sessionBrowser) === JSON.stringify(right.sessionBrowser);
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
      stateField("goal", "项目目标", presentation.current_state.goal, () => open(`${descriptor.root}/项目回顾.md`)),
      stateField("stage", "当前阶段", presentation.current_state.stage, () => open(`${descriptor.root}/项目回顾.md`)),
      stateField("status", "当前状态", presentation.current_state.status, () => open(`${descriptor.root}/项目回顾.md`), status.label),
      stateField("next_action", "下一步", presentation.current_state.next_action, () => open(`${descriptor.root}/项目回顾.md`))
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

function stateField(key: string, label: string, value: string, open: () => void, shown = value): HTMLElement {
  const normalized = shown.trim();
  const isLong = normalized.length > 72 || normalized.split(/\r?\n/).length > 2;
  if (value.trim() && isLong) {
    const details = element("details", { className: "sr-v4-state-field sr-v4-state-expanded", attrs: { "data-v4-state-field": key } });
    const summary = element("summary", {}, [
      element("span", { text: label }),
      element("strong", { text: boundedPreview(normalized) }),
      element("em", { text: "展开全文" })
    ]);
    summary.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      details.open = !details.open;
    });
    details.append(summary, element("p", { className: "sr-v4-state-full", text: shown }));
    return details;
  }
  const wrapper = element("div", { className: "sr-v4-state-field", attrs: { "data-v4-state-field": key } }, [element("span", { text: label })]);
  if (value.trim()) wrapper.append(element("strong", { text: shown }));
  else {
    const action = button("未填写 · 打开 Markdown", { "data-v4-open": "review" });
    action.addEventListener("click", open);
    wrapper.append(action);
  }
  return wrapper;
}

function boundedPreview(value: string): string {
  const compact = value.replace(/\s+/g, " ");
  return compact.length > 72 ? `${compact.slice(0, 72).trimEnd()}…` : compact;
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
