import type { ProblemNodeV4, ReviewPresentationV4 } from "../contracts/review-v4";
import type { V4ViewState } from "../state/v4-view-state";
import { button, element } from "./dom";

export function renderV4Problems(presentation: ReviewPresentationV4, state: V4ViewState, update: (patch: Partial<V4ViewState>) => void, openReview: () => void): HTMLElement {
  const section = element("section", { className: "sr-v4-problems", attrs: { "data-v4-panel": "problems", role: "tabpanel" } });
  if (presentation.problem_nodes.length === 0) {
    section.append(element("p", { className: "sr-empty", text: "尚无已确认的正式问题。未归类候选不会自动进入问题树。" }), nativeAction(openReview));
    return section;
  }
  const nodes = new Map(presentation.problem_nodes.map((node) => [node.id, node]));
  const selected = nodes.get(state.selectedProblemId ?? "") ?? nodes.get(presentation.problem_root_ids[0] ?? "") ?? presentation.problem_nodes[0];
  const rail = element("aside", { className: "sr-v4-problem-tree", attrs: { "aria-label": "正式问题树" } });
  for (const rootId of presentation.problem_root_ids) appendNode(rootId, 0, nodes, presentation.problem_nodes, selected, update, rail, new Set());
  const children = presentation.problem_nodes
    .filter((candidate) => candidate.primary_parent_id === selected.id)
    .sort((left, right) => left.sibling_order - right.sibling_order || left.id.localeCompare(right.id));
  const path = ancestorPath(selected, nodes);
  const related = selected.related_node_ids.map((id) => nodes.get(id)).filter((node): node is ProblemNodeV4 => node !== undefined);
  const context = element("main", { className: "sr-v4-problem-context", attrs: { "aria-label": "当前问题与子问题" } }, [
    element("p", { className: "sr-v4-problem-path", text: path.map((node) => node.question).join(" › ") }),
    element("article", { className: "sr-v4-problem-current" }, [element("strong", { text: selected.question }), element("span", { text: workflowLabel(selected.workflow_state) })])
  ]);
  const childList = element("div", { className: "sr-v4-direct-children" });
  for (const child of children) {
    const control = button(child.question, { "data-v4-child-problem-id": child.id });
    control.addEventListener("click", () => update({ selectedProblemId: child.id }));
    childList.append(control);
  }
  if (children.length === 0) childList.append(element("p", { className: "sr-empty", text: "当前问题没有已确认的直接子问题。" }));
  context.append(element("h3", { text: "直接子问题" }), childList);
  if (related.length > 0) context.append(element("p", { className: "sr-v4-related-problems", text: `相关问题：${related.map((node) => node.question).join("；")}` }));
  const detail = element("aside", { className: "sr-v4-problem-detail", attrs: { "aria-label": "问题证据与问答来源" } }, [
    element("span", { className: "sr-detail-kicker", text: workflowLabel(selected.workflow_state) }),
    element("h2", { text: selected.question }),
    definition("完成标准", selected.completion_criterion || "未填写"),
    definition("当前结论", selected.current_conclusion || "未填写"),
    definition("回答状态", answerLabel(selected.answer_state)),
    renderSources(selected),
    nativeAction(openReview)
  ]);
  section.append(rail, context, detail);
  return section;
}

function ancestorPath(selected: ProblemNodeV4, nodes: Map<string, ProblemNodeV4>): ProblemNodeV4[] {
  const path = [selected];
  const seen = new Set([selected.id]);
  let parentId = selected.primary_parent_id;
  while (parentId !== null) {
    const parent = nodes.get(parentId);
    if (!parent || seen.has(parent.id)) break;
    seen.add(parent.id);
    path.unshift(parent);
    parentId = parent.primary_parent_id;
  }
  return path;
}

function renderSources(node: ProblemNodeV4): HTMLElement {
  const sources = element("div", { className: "sr-v4-problem-sources" }, [element("strong", { text: "关联问答来源" })]);
  if (node.source_turn_refs.length === 0) sources.append(element("p", { className: "sr-empty", text: "当前正式节点没有已绑定的可见问答来源。" }));
  for (const ref of node.source_turn_refs) sources.append(element("p", { text: `${ref.provider} / ${ref.session_id} # ${ref.turn_unit_id}` }));
  return sources;
}

function appendNode(id: string, depth: number, map: Map<string, ProblemNodeV4>, all: ProblemNodeV4[], selected: ProblemNodeV4, update: (patch: Partial<V4ViewState>) => void, parent: HTMLElement, seen: Set<string>): void {
  const node = map.get(id);
  if (!node || seen.has(id)) return;
  seen.add(id);
  const item = button(node.question, { "data-v4-problem-id": node.id, "aria-selected": String(node.id === selected.id) });
  item.style.setProperty("--sr-problem-depth", String(depth));
  item.addEventListener("click", () => update({ selectedProblemId: node.id }));
  parent.append(item);
  for (const child of all.filter((candidate) => candidate.primary_parent_id === node.id).sort((left, right) => left.sibling_order - right.sibling_order || left.id.localeCompare(right.id))) {
    appendNode(child.id, depth + 1, map, all, selected, update, parent, seen);
  }
}

function definition(label: string, value: string): HTMLElement {
  return element("div", { className: "sr-definition" }, [element("strong", { text: label }), element("p", { text: value })]);
}

function nativeAction(open: () => void): HTMLButtonElement {
  const control = button("在原生 Markdown 中查看或编辑", { "data-v4-open": "review" });
  control.addEventListener("click", open);
  return control;
}

function workflowLabel(value: ProblemNodeV4["workflow_state"]): string {
  return { not_started: "未开始", in_progress: "进行中", paused: "已暂停", resolved: "已解决" }[value];
}

function answerLabel(value: ProblemNodeV4["answer_state"]): string {
  return { no_answer: "尚无 Agent 回答", answered_unverified: "已有回答，待验证", execution_verified: "已有执行验证" }[value];
}
