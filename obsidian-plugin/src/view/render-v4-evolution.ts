import type { ReviewPresentationV4, TimelineEntryV4 } from "../contracts/review-v4";
import type { V4ViewState } from "../state/v4-view-state";
import { button, element } from "./dom";
import { presentDateTime } from "./presentation";
import { defaultV4MilestoneId, orderV4Milestones } from "./v4-milestone-order";

export interface V4EvolutionUiState {
  fullHistory: boolean;
  page: number;
}

export function renderV4Evolution(
  presentation: ReviewPresentationV4,
  state: V4ViewState,
  update: (patch: Partial<V4ViewState>) => void,
  openHistory: () => void,
  ui: V4EvolutionUiState = { fullHistory: false, page: 0 }
): HTMLElement {
  const section = element("section", { className: "sr-v4-evolution", attrs: { "data-v4-panel": "evolution", role: "tabpanel" } });
  if (presentation.timeline.length === 0) {
    section.append(element("p", { className: "sr-empty", text: "尚无已接受的项目里程碑。扫描事实不会被伪装成项目演进。" }), openButton("查看原生项目历史", openHistory));
    return section;
  }
  const defaultMilestoneId = defaultV4MilestoneId(presentation.timeline);
  let selected = presentation.timeline.find((item) => item.id === state.selectedMilestoneId) ??
    presentation.timeline.find((item) => item.id === defaultMilestoneId) ?? presentation.timeline[0];
  const pageSize = 8;
  const rail = element("aside", { className: "sr-v4-timeline-rail" });
  const detailHost = element("div", { className: "sr-v4-milestone-host" });
  const draw = (): void => {
    const ordered = orderV4Milestones(presentation.timeline);
    const recent = ordered.slice(-5).reverse();
    const pageCount = Math.max(1, Math.ceil(ordered.length / pageSize));
    const safePage = Math.min(ui.page, pageCount - 1);
    ui.page = safePage;
    const shown = ui.fullHistory ? ordered.slice(safePage * pageSize, (safePage + 1) * pageSize) : recent;
    const header = element("div", { className: "sr-rail-header" }, [element("h2", { text: "项目演进" }), element("span", { text: `共 ${presentation.timeline.length.toLocaleString("en-US")} 条` })]);
    const mode = button(ui.fullHistory ? "返回近期" : "查看全部", { "data-v4-history-mode": ui.fullHistory ? "recent" : "full" });
    mode.addEventListener("click", () => { ui.fullHistory = !ui.fullHistory; ui.page = 0; draw(); });
    rail.replaceChildren(header, mode);
    for (const milestone of shown) {
      const node = button("", { "data-v4-milestone-id": milestone.id, "aria-selected": String(milestone.id === selected.id) });
      node.append(element("strong", { text: milestone.title }), element("span", { text: presentDateTime(milestone.occurred_at) }));
      node.addEventListener("click", () => { selected = milestone; update({ selectedMilestoneId: milestone.id }); });
      rail.append(node);
    }
    if (ui.fullHistory) rail.append(historyNavigation(safePage, pageCount, (next) => { ui.page = next; draw(); }));
    rail.append(openButton("查看原生项目历史", openHistory));
    detailHost.replaceChildren(renderMilestone(selected));
  };
  section.append(rail, detailHost);
  draw();
  return section;
}

function historyNavigation(page: number, pageCount: number, go: (page: number) => void): HTMLElement {
  const nav = element("nav", { className: "sr-v4-history-navigation", attrs: { "aria-label": "项目历史分页" } });
  const definitions = [["首页", "first", 0], ["上一页", "previous", Math.max(0, page - 1)], ["下一页", "next", Math.min(pageCount - 1, page + 1)], ["末页", "last", pageCount - 1]] as const;
  for (const [label, action, target] of definitions) {
    const control = button(label, { "data-v4-history-page": action });
    control.disabled = target === page;
    control.addEventListener("click", () => go(target));
    nav.append(control);
  }
  nav.append(element("span", { text: `${page + 1} / ${pageCount}` }));
  return nav;
}

function renderMilestone(milestone: TimelineEntryV4): HTMLElement {
  const detail = element("article", { className: "sr-v4-milestone-detail" }, [
    element("span", { className: "sr-detail-kicker", text: milestoneKindLabel(milestone.kind) }),
    element("h2", { text: milestone.title }),
    element("p", { text: milestone.summary })
  ]);
  const definitions = [
    { key: "trigger_question", label: "触发问题", machineLabel: "触发", rawStatus: milestone.closed_loop.trigger_question.state, status: segmentStateLabel(milestone.closed_loop.trigger_question.state), segment: milestone.closed_loop.trigger_question },
    { key: "conclusion", label: "Agent 结论", machineLabel: "结论", rawStatus: milestone.closed_loop.conclusion.kind, status: conclusionKindLabel(milestone.closed_loop.conclusion.kind), segment: milestone.closed_loop.conclusion },
    { key: "execution", label: "执行与变更", machineLabel: "执行", rawStatus: milestone.closed_loop.execution.state, status: segmentStateLabel(milestone.closed_loop.execution.state), segment: milestone.closed_loop.execution },
    { key: "verification", label: "结果与验证", machineLabel: "验证", rawStatus: milestone.closed_loop.verification.state, status: segmentStateLabel(milestone.closed_loop.verification.state), segment: milestone.closed_loop.verification },
    { key: "impact_and_follow_up", label: "对项目的影响与后续", machineLabel: "影响/后续", rawStatus: milestone.closed_loop.impact_and_follow_up.state, status: segmentStateLabel(milestone.closed_loop.impact_and_follow_up.state), segment: milestone.closed_loop.impact_and_follow_up }
  ];
  const list = element("dl", { className: "sr-v4-closed-loop" });
  for (const { key, label, machineLabel, rawStatus, status, segment } of definitions) {
    const value = element("dd");
    value.append(element("span", { className: "sr-v4-segment-status", text: status }));
    value.append(element("p", { className: "sr-v4-segment-text", text: segment.text || missingLabel(segment.missing_reason) }));
    const provenance = element("details", { className: "sr-v4-segment-provenance" }, [
      element("summary", { text: "状态与来源" }),
      element("p", { text: `${machineLabel}状态：${status}` }),
      element("p", { text: `${machineLabel}状态：${rawStatus}` })
    ]);
    if (segment.text.trim()) provenance.append(element("p", { text: `${machineLabel}文本：${segment.text}` }));
    if (segment.missing_reason) provenance.append(
      element("p", { text: `${machineLabel}缺失原因：${missingLabel(segment.missing_reason)}` }),
      element("p", { text: `${machineLabel}缺失原因：${segment.missing_reason}` })
    );
    for (const ref of segment.source_turn_refs) {
      provenance.append(element("p", { text: `${machineLabel}引用：${ref.provider}/${ref.session_id}#${ref.turn_unit_id}` }));
      if (ref.session_view_digest !== undefined) provenance.append(element("p", { text: `快照视图：${ref.session_view_digest}` }));
    }
    value.append(provenance);
    list.append(element("div", { className: "sr-definition", attrs: { "data-v4-segment": key } }, [element("dt", { text: label }), value]));
  }
  detail.append(renderClosureCoverage(milestone), list);
  return detail;
}

function renderClosureCoverage(milestone: TimelineEntryV4): HTMLElement {
  const coverage = milestone.closed_loop.coverage;
  const disclosure = element("div", { className: "sr-v4-closure-coverage" }, [
    element("p", { text: `闭环覆盖：来源 ${coverage.source_turns} · 已捕获 ${coverage.captured_turns} · 截断 ${coverage.truncated_turns} · 来源不可用 ${coverage.source_unavailable_turns}` })
  ]);
  const warnings: string[] = [];
  if (coverage.source_turns !== coverage.captured_turns) warnings.push("部分捕获");
  if (coverage.truncated_turns > 0) warnings.push("回答可能不完整");
  if (coverage.source_unavailable_turns > 0) warnings.push("来源不可用");
  if (warnings.length > 0) disclosure.append(element("p", { className: "sr-v4-closure-warnings", text: warnings.join(" · ") }));
  return disclosure;
}

function milestoneKindLabel(kind: string): string {
  return ({
    milestone: "已接受里程碑",
    machine_verification: "机器验证",
    machine_commit: "提交记录",
    machine_release: "发布记录",
    machine_deployment: "部署记录",
    machine_version: "版本记录"
  } as Record<string, string>)[kind] ?? "其他里程碑";
}

function conclusionKindLabel(kind: string): string {
  return ({
    visible_answer_excerpt: "原回答摘录",
    human_confirmed: "人工确认",
    ai_candidate_confirmed: "AI 整理 · 已确认",
    missing: "未捕获 Agent 回答"
  } as Record<string, string>)[kind] ?? "未捕获 Agent 回答";
}

function segmentStateLabel(state: string): string {
  return ({ present: "已捕获", partial: "部分捕获", missing: "缺少证据" } as Record<string, string>)[state] ?? "缺少证据";
}

function missingLabel(reason: string | null): string {
  return ({
    not_captured: "未捕获",
    no_visible_answer: "未捕获 Agent 回答",
    no_execution_evidence: "未发现执行证据",
    not_verified: "待验证",
    source_unavailable: "来源不可用",
    partial_coverage: "部分捕获"
  } as Record<string, string>)[reason ?? ""] ?? "未捕获";
}

function openButton(label: string, open: () => void): HTMLButtonElement {
  const control = button(label, { "data-v4-open": "history" });
  control.addEventListener("click", open);
  return control;
}
