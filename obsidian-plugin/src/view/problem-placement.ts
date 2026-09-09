import type { ProblemCandidateV1 } from "./render-v4-problems";
import type { ProblemPlacementActions, ProblemPlacementJob } from "../cli/problem-placement";
import { button, element } from "./dom";

export type ProblemPlacementElement = HTMLElement & { dispose: () => void };

export function renderProblemPlacement(candidate: ProblemCandidateV1, actions: ProblemPlacementActions, completed: () => void): ProblemPlacementElement {
  const root = element("section", { className: "sr-problem-placement", attrs: { "aria-label": "Agent 问题归类建议" } }) as ProblemPlacementElement;
  const start = button("请求 Agent 归类建议", { "data-action": "prepare-problem-placement" });
  const status = element("p", { attrs: { role: "status" } });
  const controls = element("div");
  root.append(start, status, controls);
  let disposed = false;
  let busy = false;
  let job: ProblemPlacementJob | undefined;
  let timer: number | undefined;
  let completedJob = "";
  let loading = !!actions.latest;
  const terminal = () => !!job && ["completed", "failed", "cancelled"].includes(job.state);
  const draw = (): void => {
    if (disposed) return;
    if (timer !== undefined) window.clearTimeout(timer);
    controls.replaceChildren();
    start.disabled = loading || busy || !!job && !terminal();
    if (!job) return;
    status.textContent = ({ queued: "Agent 任务排队中", running: "Agent 正在生成归类建议", cancel_requested: "正在取消 Agent 任务", completed: "Agent 建议已返回，仍需人工确认", failed: "Agent 建议失败，正式问题树未改变", cancelled: "Agent 建议已取消" } as const)[job.state];
    if (job.state === "completed" && completedJob !== job.job_id) { completedJob = job.job_id; completed(); }
    if (terminal()) return;
    if (job.can_cancel) {
      const cancel = button("取消 Agent 归类", { "data-action": "cancel-problem-placement" });
      cancel.disabled = busy;
      cancel.addEventListener("click", () => {
        if (!job || busy) return;
        busy = true; cancel.disabled = true;
        const current = job;
        void actions.cancel(current.job_id, current.revision).then((next) => { if (!disposed && job?.job_id === current.job_id) { job = next; busy = false; draw(); } }).catch(() => { if (!disposed) { busy = false; draw(); status.textContent = "取消结果未确认；刷新项目后可继续检查。"; } });
      });
      controls.append(cancel);
    }
    timer = window.setTimeout(() => {
      if (disposed || !job || terminal() || busy) return;
      const current = job;
      void actions.status(current.job_id).then((next) => { if (!disposed && job?.job_id === current.job_id) { if (next.revision >= job.revision) job = next; draw(); } }).catch(() => { if (!disposed) status.textContent = "任务状态读取失败；刷新项目后可重试。"; });
    }, 1000);
  };
  start.addEventListener("click", () => {
    if (start.disabled) return;
    status.textContent = "将调用已配置且验证过的 Agent，消耗模型用量；结果只更新待确认建议，不会修改正式问题树。";
    const confirm = button("确认调用 Agent 归类", { "data-action": "confirm-problem-placement" });
    confirm.addEventListener("click", () => {
      if (busy) return;
      busy = true; start.disabled = true; confirm.disabled = true;
      void actions.start(candidate).then((next) => { if (!disposed) { job = next; busy = false; draw(); } }).catch(() => { if (!disposed) { busy = false; start.disabled = false; controls.replaceChildren(); status.textContent = "未能确认 Agent 任务；请检查 Agent 配置或刷新后重试。"; } });
    });
    controls.replaceChildren(confirm);
  });
  root.dispose = () => { disposed = true; if (timer !== undefined) window.clearTimeout(timer); root.replaceChildren(); };
  draw();
  if (actions.latest) {
    void actions.latest(candidate).then((value) => { if (!disposed) { job = value; loading = false; if (value?.state === "completed") completedJob = value.job_id; draw(); } }).catch(() => { if (!disposed) { loading = false; busy = true; start.disabled = true; status.textContent = "无法读取已有 Agent 归类任务；请刷新项目后重试。"; } });
  }
  return root;
}
