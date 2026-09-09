import type { DecisionExtractionActions, DecisionExtractionJob } from "../cli/decision-jobs";
import { button, element } from "./dom";
export type DecisionExtractionElement = HTMLElement & { dispose: () => void };

export function renderDecisionExtraction(actions: DecisionExtractionActions, completed: () => void): DecisionExtractionElement {
  const root = element("section", { className: "sr-decision-extraction", attrs: { "aria-label": "提取决策建议" } }) as DecisionExtractionElement;
  const start = button("提取决策建议（调用 Agent）", { "data-action": "prepare-extraction" });
  start.className = "sr-decision-secondary";
  const status = element("p", { className: "sr-decision-extraction-status", attrs: { role: "status" } });
  const controls = element("div", { className: "sr-decision-actions" });
  root.append(element("div", { className: "sr-decision-extraction-heading" }, [element("div", {}, [element("h3", { text: "从会话中提取建议" }), element("p", { text: "手动触发 · 使用已配置的 Agent · 结果需逐条确认" })]), start]), status, controls);
  let disposed = false;
  let timer: number | undefined;
  let job: DecisionExtractionJob | undefined = actions.initialJob;
  let completedNotified = job?.state === "completed";
  let busy = false;
  let loading = !!actions.latest;
  let loadFailed = false;
  let configured = !actions.configuration;
  const terminal = () => job && ["completed", "failed", "cancelled"].includes(job.state);
  const updateStart = () => { start.disabled = busy || loading || loadFailed || !configured || !!job && !terminal(); };
  const draw = () => {
    if (disposed) return;
    if (timer) window.clearTimeout(timer);
    controls.replaceChildren();
    updateStart();
    if (!job) return;
    const labels = { queued: "排队中", running: "正在提取候选", completed: "提取完成", failed: "提取失败，未确认任何项目决策", cancelled: "已取消" };
    status.textContent = `${labels[job.state]} · 候选 ${job.candidate_count}${actions.currentGenerationId && job.generation_id !== actions.currentGenerationId ? " · 先前扫描的提取任务" : ""}`;
    if (terminal()) {
      if (job.state === "completed" && !completedNotified) { completedNotified = true; completed(); }
      return;
    }
    const cancel = button("取消提取", { "data-action": "cancel-extraction" });
    cancel.disabled = busy;
    cancel.addEventListener("click", () => {
      if (!job || busy) return;
      busy = true;
      if (timer) window.clearTimeout(timer);
      cancel.disabled = true;
      const current = job;
      void actions.cancel(current.job_id, current.revision).then(next => {
        if (disposed) return;
        job = next; busy = false; draw();
      }).catch(() => {
        if (disposed) return;
        busy = false; draw(); status.textContent = "取消结果未确认；请刷新任务状态检查。";
      });
    });
    controls.append(cancel);
    timer = window.setTimeout(() => {
      if (disposed || !job || terminal() || busy) return;
      const current = job;
      void actions.status(current.job_id).then(next => {
        if (disposed || job?.job_id !== current.job_id) return;
        if (next.revision >= job.revision) job = next;
        draw();
      }).catch(() => {
        if (disposed) return;
        status.textContent = "任务状态读取失败；刷新项目后可继续检查。";
      });
    }, 1000);
  };
  start.addEventListener("click", () => {
    if (start.disabled) return;
    controls.replaceChildren();
    status.textContent = "将调用已配置且验证过的 agent，消耗模型用量；结果只进入待确认候选，不会自动修改项目决策。";
    const confirm = button("确认调用 Agent 提取", { "data-action": "confirm-extraction" });
    confirm.addEventListener("click", () => {
      if (busy || !configured) return;
      busy = true; updateStart(); confirm.disabled = true;
      void actions.start().then(next => {
        if (disposed) return;
        job = next; busy = false; completedNotified = false; draw();
      }).catch(() => {
        if (disposed) return;
        busy = false; loadFailed = true; updateStart(); controls.replaceChildren();
        status.textContent = "未能确认提取任务；请刷新项目检查任务状态后重试。";
      });
    });
    controls.append(confirm);
  });
  if (actions.configuration) {
    const configuration = element("details", { className: "sr-decision-configuration" });
    configuration.append(element("summary", { text: "Agent 配置" }));
    const input = element("input", { attrs: { name: "agent-executable", "aria-label": "Codex 可执行文件绝对路径", placeholder: "Codex 可执行文件绝对路径" } });
    const save = button("验证并保存 Agent", { "data-action": "configure-agent" });
    const info = element("p", { attrs: { role: "status" }, text: "正在读取 Agent 配置…" });
    configuration.append(input, save, info); root.append(configuration);
    let configurationEpoch = 0;
    const configure = async (executable?: string) => {
      const epoch = ++configurationEpoch;
      save.disabled = true;
      try {
        const result = await actions.configuration!(executable);
        if (disposed || epoch !== configurationEpoch) return;
        configured = result.compatible;
        if (result.executable) input.value = result.executable;
        info.textContent = configured ? `Agent 已验证 · ${result.version ?? "版本未知"}` : "请配置支持受限建议模式的 Codex。验证配置不会开始提取。";
        configuration.open = !configured;
      } catch {
        if (disposed || epoch !== configurationEpoch) return;
        configured = false; info.textContent = "Agent 配置验证失败；请检查可执行文件路径。"; configuration.open = true;
      } finally {
        if (!disposed && epoch === configurationEpoch) { save.disabled = false; updateStart(); }
      }
    };
    save.addEventListener("click", () => { if (!save.disabled) void configure(input.value.trim()); });
    void configure();
  }
  root.dispose = () => { disposed = true; if (timer) window.clearTimeout(timer); root.replaceChildren(); };
  draw();
  if (actions.latest) {
    void actions.latest().then(value => {
      if (disposed) return;
      job = value; completedNotified = value?.state === "completed"; loading = false; draw();
    }).catch(() => {
      if (disposed) return;
      loading = false; loadFailed = true; updateStart(); status.textContent = "无法读取已有提取任务；请刷新项目后重试。";
    });
  }
  return root;
}
