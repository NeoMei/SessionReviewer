import type { ProblemMapCandidateV1, ProblemNodeV4, ReviewPresentationV4 } from "../contracts/review-v4";
import type { V4ViewState } from "../state/v4-view-state";
import { button, element } from "./dom";
import { renderConversation, type ConversationElement, type ConversationLoader } from "./render-conversation";

export type ProblemCandidateV1 = ProblemMapCandidateV1["candidates"][number];
export interface ProblemActions {
  candidates?: ProblemCandidateV1[];
  unavailableReason?: string;
  createProblem?: (question: string) => Promise<void>;
  transitionCandidate?: (candidate: ProblemCandidateV1, action: "apply_root" | "apply_child" | "apply_sibling" | "merge" | "keep_pending" | "dismiss" | "restore", target?: string) => Promise<void>;
  setProblemState?: (problem: ProblemNodeV4, action: "resolve" | "reopen") => Promise<void>;
  editProblem?: (problem: ProblemNodeV4, fields: { question: string; currentConclusion: string; completionCriterion: string }) => Promise<void>;
  moveProblem?: (problem: ProblemNodeV4, newParentId: string) => Promise<void>;
  reorderProblems?: (parentId: string, orderedChildIds: string[]) => Promise<void>;
  loadConversation?: ConversationLoader;
  announce?: (message: string) => void;
}

export function renderV4Problems(presentation: ReviewPresentationV4, state: V4ViewState, update: (patch: Partial<V4ViewState>) => void, openReview: () => void, actions: ProblemActions = {}): HTMLElement {
  const section = element("section", { className: "sr-v4-problems", attrs: { "data-v4-panel": "problems", role: "tabpanel" } });
  const live = element("p", { className: "sr-sr-only", attrs: { "aria-live": "polite" } });
  const run = async (operation: (() => Promise<void>) | undefined, success: string): Promise<void> => {
    if (!operation) return;
    try { await operation(); live.textContent = success; actions.announce?.(success); }
    catch (error) { const message = error instanceof Error ? error.message : String(error); live.textContent = message; actions.announce?.(message); }
  };
  if (presentation.problem_nodes.length === 0) {
    section.append(element("p", { className: "sr-empty", text: "尚无已确认的正式问题。未归类候选不会自动进入问题树。" }));
    if (actions.createProblem) {
      const input = element("textarea", { attrs: { "data-v4-new-problem": "", "aria-label": "新问题原文", maxlength: "4096" } });
      const create = button("创建待确认问题", { "data-action": "create-problem-candidate" });
      create.addEventListener("click", () => { if (input.value.length > 0) void run(() => actions.createProblem!(input.value), "问题已加入待确认列表。") });
      section.append(element("div", { className: "sr-v4-problem-create" }, [input, create]));
    }
    section.append(renderCandidates(actions.candidates ?? [], undefined, actions, run), nativeAction(openReview), live);
    return section;
  }
  const nodes = new Map(presentation.problem_nodes.map((node) => [node.id, node]));
  const selected = nodes.get(state.selectedProblemId ?? "") ?? nodes.get(presentation.problem_root_ids[0] ?? "") ?? presentation.problem_nodes[0];
  const rail = element("aside", { className: "sr-v4-problem-tree", attrs: { "aria-label": "正式问题树", role: "tree" } });
  for (const rootId of presentation.problem_root_ids) appendNode(rootId, 0, nodes, presentation.problem_nodes, selected, update, rail, new Set());
  const children = presentation.problem_nodes.filter((candidate) => candidate.primary_parent_id === selected.id).sort(problemOrder);
  const path = ancestorPath(selected, nodes);
  const related = selected.related_node_ids.map((id) => nodes.get(id)).filter((node): node is ProblemNodeV4 => node !== undefined);
  const context = element("main", { className: "sr-v4-problem-context", attrs: { "aria-label": "当前问题与子问题" } }, [
    element("p", { className: "sr-v4-problem-path", text: path.map((node) => node.question).join(" › ") }),
    element("article", { className: "sr-v4-problem-current" }, [element("strong", { text: selected.question }), element("span", { text: workflowLabel(selected.workflow_state) })])
  ]);
  const childList = element("div", { className: "sr-v4-direct-children" });
  for (const child of children) { const control = button(child.question, { "data-v4-child-problem-id": child.id }); control.addEventListener("click", () => update({ selectedProblemId: child.id })); childList.append(control); }
  if (children.length === 0) childList.append(element("p", { className: "sr-empty", text: "当前问题没有已确认的直接子问题。" }));
  context.append(element("h3", { text: "直接子问题" }), childList);
  if (related.length > 0) context.append(element("p", { className: "sr-v4-related-problems", text: `相关问题：${related.map((node) => node.question).join("；")}` }));
  const detail = element("aside", { className: "sr-v4-problem-detail", attrs: { "aria-label": "问题证据与问答来源" } }, [
    element("span", { className: "sr-detail-kicker", text: workflowLabel(selected.workflow_state) }), element("h2", { text: selected.question }),
    definition("完成标准", selected.completion_criterion || "未填写"), definition("当前结论", selected.current_conclusion || "未填写"), definition("回答状态", answerLabel(selected.answer_state)),
    renderSources(selected, presentation, actions), renderEdit(selected, actions, run), renderMove(selected, presentation.problem_nodes, nodes, actions, run), renderReorder(selected, presentation.problem_nodes, actions, run), renderStateAction(selected, actions, run), nativeAction(openReview)
  ]);
  section.append(rail, context, detail, renderCandidates(actions.candidates ?? [], selected, actions, run), live);
  return section;
}

function renderCandidates(candidates: ProblemCandidateV1[], selected: ProblemNodeV4 | undefined, actions: ProblemActions, run: (op: (() => Promise<void>) | undefined, success: string) => Promise<void>): HTMLElement {
  const pending = candidates.filter((item) => item.status === "pending" || item.status === "kept_pending");
  const drawer = element("section", { className: "sr-v4-problem-candidates", attrs: { "aria-label": "待归类问题" } }, [element("h3", { text: `待归类问题 ${pending.length}` })]);
  if (actions.unavailableReason) drawer.append(element("p", { className: "sr-empty", text: actions.unavailableReason }));
  for (const candidate of candidates) {
    const card = element("article", { className: "sr-v4-problem-candidate", attrs: { "data-problem-candidate-id": candidate.candidate_id } }, [
      element("strong", { text: candidate.question }), element("span", { text: `建议：${relationLabel(candidate.recommended_relation)} · 置信度：${confidenceLabel(candidate.confidence)}` })
    ]);
    for (const ground of candidate.grounds) card.append(element("p", { text: ground.explanation }));
    if (candidate.status === "pending" || candidate.status === "kept_pending") {
      card.append(confirmingAction("确认为顶层问题", "确认：确认为顶层问题", "apply-root", () => void run(actions.transitionCandidate ? () => actions.transitionCandidate!(candidate, "apply_root", undefined) : undefined, "问题结构已发布。")));
      if (selected) for (const [label, action] of [["作为子问题", "apply_child"], ["作为同级问题", "apply_sibling"], ["合并到当前问题", "merge"]] as const) card.append(confirmingAction(label, `确认：${label}`, action, () => void run(actions.transitionCandidate ? () => actions.transitionCandidate!(candidate, action, selected.id) : undefined, "问题结构已发布。")));
      const keep = button("继续待归类", { "data-action": "keep-pending" }); keep.addEventListener("click", () => void run(actions.transitionCandidate ? () => actions.transitionCandidate!(candidate, "keep_pending") : undefined, "候选继续待归类。"));
      const dismiss = button("忽略", { "data-action": "dismiss-candidate" }); dismiss.addEventListener("click", () => void run(actions.transitionCandidate ? () => actions.transitionCandidate!(candidate, "dismiss") : undefined, "候选已忽略。"));
      card.append(keep, dismiss);
    } else if (candidate.status === "dismissed" || candidate.status === "stale") {
      const restore = button("恢复候选", { "data-action": "restore-candidate" }); restore.addEventListener("click", () => void run(actions.transitionCandidate ? () => actions.transitionCandidate!(candidate, "restore") : undefined, "候选已恢复。")); card.append(restore);
    }
    drawer.append(card);
  }
  if (candidates.length === 0 && !actions.unavailableReason) drawer.append(element("p", { className: "sr-empty", text: "当前没有待归类候选。普通扫描使用零 Token 确定性规则。" }));
  return drawer;
}

function confirmingAction(label: string, confirmation: string, key: string, confirm: () => void): HTMLElement { const wrapper = element("span", { className: "sr-confirming-action" }); const start = button(label, { "data-action": key }); start.addEventListener("click", () => { const yes = button(confirmation, { "data-action": `confirm-${key}` }); yes.addEventListener("click", confirm); wrapper.replaceChildren(yes) }); wrapper.append(start); return wrapper; }
function renderEdit(node: ProblemNodeV4, actions: ProblemActions, run: (op: (() => Promise<void>) | undefined, success: string) => Promise<void>): HTMLElement {
  const form = element("div", { className: "sr-v4-problem-edit" }, [element("h3", { text: "编辑人工字段" })]);
  const question = element("textarea", { attrs: { "data-problem-edit-question": "", "aria-label": "问题原文", maxlength: "4096" } }); question.value = node.question;
  const conclusion = element("textarea", { attrs: { "data-problem-edit-conclusion": "", "aria-label": "当前结论", maxlength: "16384" } }); conclusion.value = node.current_conclusion;
  const criterion = element("textarea", { attrs: { "data-problem-edit-criterion": "", "aria-label": "完成标准", maxlength: "16384" } }); criterion.value = node.completion_criterion;
  const start = button("保存人工字段", { "data-action": "edit-problem" });
  start.addEventListener("click", () => { const confirm = button("确认：保存问题、结论和完成标准", { "data-action": "confirm-edit-problem" }); confirm.addEventListener("click", () => void run(actions.editProblem ? () => actions.editProblem!(node, { question: question.value, currentConclusion: conclusion.value, completionCriterion: criterion.value }) : undefined, "问题字段已发布。")); start.replaceWith(confirm); });
  form.append(question, conclusion, criterion, start); return form;
}
function renderMove(node: ProblemNodeV4, all: ProblemNodeV4[], nodes: Map<string, ProblemNodeV4>, actions: ProblemActions, run: (op: (() => Promise<void>) | undefined, success: string) => Promise<void>): HTMLElement {
  const box = element("div", { className: "sr-v4-problem-move" }, [element("h3", { text: "移动问题树" })]);
  const descendants = new Set(descendantIDs(node.id, all));
  const select = element("select", { attrs: { "data-problem-move-parent": "", "aria-label": "新父问题" } });
  const root = element("option", { text: "顶层", attrs: { value: "root" } }); root.selected = node.primary_parent_id === null; select.append(root);
  for (const candidate of all.filter((candidate) => candidate.id !== node.id && !descendants.has(candidate.id)).sort(problemOrder)) { const option = element("option", { text: candidate.question, attrs: { value: candidate.id } }); option.selected = node.primary_parent_id === candidate.id; select.append(option); }
  const preview = element("p", { className: "sr-v4-move-preview", attrs: { "data-problem-move-preview": "" } });
  const start = button("预览移动", { "data-action": "move-problem" });
  start.addEventListener("click", () => {
    const parent = select.value; const oldPath = ancestorPath(node, nodes).map((item) => item.question).join(" › "); const parentNode = nodes.get(parent); const newPath = parent === "root" ? node.question : `${ancestorPath(parentNode!, nodes).map((item) => item.question).join(" › ")} › ${node.question}`;
    preview.textContent = `原路径：${oldPath}；新路径：${newPath}；受影响子树：${[node.id, ...descendants].join("、")}`;
    const confirm = button("确认移动该子树", { "data-action": "confirm-move-problem" }); confirm.addEventListener("click", () => void run(actions.moveProblem ? () => actions.moveProblem!(node, parent) : undefined, "问题子树已发布。")); start.replaceWith(confirm);
  });
  box.append(select, preview, start); return box;
}
function renderReorder(node: ProblemNodeV4, all: ProblemNodeV4[], actions: ProblemActions, run: (op: (() => Promise<void>) | undefined, success: string) => Promise<void>): HTMLElement {
  const parentId = node.primary_parent_id ?? "root"; const siblings = all.filter((item) => (item.primary_parent_id ?? "root") === parentId).sort(problemOrder); const index = siblings.findIndex((item) => item.id === node.id);
  const box = element("div", { className: "sr-v4-problem-reorder" });
  const control = (label: string, delta: number): HTMLButtonElement => { const item = button(label, { "data-action": delta < 0 ? "move-problem-up" : "move-problem-down" }); item.disabled = index + delta < 0 || index + delta >= siblings.length; item.addEventListener("click", () => { const order = siblings.map((value) => value.id); [order[index], order[index + delta]] = [order[index + delta], order[index]]; void run(actions.reorderProblems ? () => actions.reorderProblems!(parentId, order) : undefined, "同级问题顺序已发布。"); }); return item; };
  box.append(control("上移", -1), control("下移", 1)); return box;
}
function descendantIDs(id: string, all: ProblemNodeV4[]): string[] { const result: string[] = []; const visit = (parent: string): void => { for (const node of all.filter((item) => item.primary_parent_id === parent)) { result.push(node.id); visit(node.id); } }; visit(id); return result; }
function renderStateAction(node: ProblemNodeV4, actions: ProblemActions, run: (op: (() => Promise<void>) | undefined, success: string) => Promise<void>): HTMLElement { const action = node.workflow_state === "resolved" ? "reopen" as const : "resolve" as const; return confirmingAction(action === "resolve" ? "标记为已解决" : "重新打开", action === "resolve" ? "确认：标记为已解决" : "确认：重新打开", `${action}-problem`, () => void run(actions.setProblemState ? () => actions.setProblemState!(node, action) : undefined, action === "resolve" ? "问题已标记为解决。" : "问题已重新打开。")); }
function ancestorPath(selected: ProblemNodeV4, nodes: Map<string, ProblemNodeV4>): ProblemNodeV4[] { const path = [selected]; const seen = new Set([selected.id]); let parentId = selected.primary_parent_id; while (parentId !== null) { const parent = nodes.get(parentId); if (!parent || seen.has(parent.id)) break; seen.add(parent.id); path.unshift(parent); parentId = parent.primary_parent_id; } return path; }
function renderSources(node: ProblemNodeV4, presentation: ReviewPresentationV4, actions: ProblemActions): HTMLElement {
  const sources = element("div", { className: "sr-v4-problem-sources" }, [element("strong", { text: "关联问答来源" })]); let viewer: ConversationElement | undefined;
  const body = element("div", { className: "sr-v4-problem-source-answer" });
  if (node.source_turn_refs.length === 0) sources.append(element("p", { className: "sr-empty", text: "当前正式节点没有已绑定的可见问答来源。" }));
  for (const ref of node.source_turn_refs) {
    const open = button(`${ref.provider} / ${ref.session_id} # ${ref.turn_unit_id}`, { "data-action": "open-problem-source" });
    open.disabled = !actions.loadConversation || !ref.session_view_digest;
    open.addEventListener("click", () => {
      if (!actions.loadConversation || !ref.session_view_digest) return;
      viewer?.dispose(); viewer = renderConversation({ projectId: presentation.project_id, provider: ref.provider, sessionId: ref.session_id, expectedGenerationId: presentation.generation_id, expectedSessionViewDigest: ref.session_view_digest, sessionViewDigest: ref.session_view_digest }, actions.loadConversation, { turnUnitId: ref.turn_unit_id });
      body.replaceChildren(viewer);
    }); sources.append(open);
  }
  sources.append(body); return sources;
}
function appendNode(id: string, depth: number, map: Map<string, ProblemNodeV4>, all: ProblemNodeV4[], selected: ProblemNodeV4, update: (patch: Partial<V4ViewState>) => void, parent: HTMLElement, seen: Set<string>): void { const node = map.get(id); if (!node || seen.has(id)) return; seen.add(id); const item = button(node.question, { role: "treeitem", "data-v4-problem-id": node.id, "aria-selected": String(node.id === selected.id), "aria-level": String(depth + 1) }); item.style.setProperty("--sr-problem-depth", String(depth)); item.addEventListener("click", () => update({ selectedProblemId: node.id })); parent.append(item); for (const child of all.filter((candidate) => candidate.primary_parent_id === node.id).sort(problemOrder)) appendNode(child.id, depth + 1, map, all, selected, update, parent, seen); }
function problemOrder(left: ProblemNodeV4, right: ProblemNodeV4): number { return left.sibling_order - right.sibling_order || left.id.localeCompare(right.id); }
function definition(label: string, value: string): HTMLElement { return element("div", { className: "sr-definition" }, [element("strong", { text: label }), element("p", { text: value })]); }
function nativeAction(open: () => void): HTMLButtonElement { const control = button("在原生 Markdown 中查看或编辑", { "data-v4-open": "review" }); control.addEventListener("click", open); return control; }
function workflowLabel(value: ProblemNodeV4["workflow_state"]): string { return { not_started: "未开始", in_progress: "进行中", paused: "已暂停", resolved: "已解决" }[value]; }
function answerLabel(value: ProblemNodeV4["answer_state"]): string { return { no_answer: "尚无 Agent 回答", answered_unverified: "已有回答，待验证", execution_verified: "已有执行验证" }[value]; }
function relationLabel(value: ProblemCandidateV1["recommended_relation"]): string { return { child: "子问题", sibling: "同级问题", merge: "合并", keep_pending: "继续待归类" }[value]; }
function confidenceLabel(value: ProblemCandidateV1["confidence"]): string { return { high: "高", medium: "中", low: "低" }[value]; }
