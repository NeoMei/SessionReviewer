import { type App, Modal, Notice } from "obsidian";
import type { EditableField } from "../contracts/review-v2";
import { button, element } from "./dom";

export class EditModal extends Modal {
  constructor(
    app: App,
    private readonly field: EditableField,
    private readonly save: (value: string | string[]) => Promise<void>
  ) {
    super(app);
  }

  onOpen(): void {
    this.contentEl.replaceChildren();
    this.contentEl.classList.add("session-reviewer-edit-modal");
    this.contentEl.append(element("h2", { text: `编辑${fieldLabel(this.field.field)}` }));
    const hint = element("p", { className: "sr-edit-hint", text: Array.isArray(this.field.value) ? "每行一项。保存后会标记为等待同步。" : "保存后会直接写回 Markdown，并标记为等待同步。" });
    const input = element("textarea", { attrs: { rows: "9", "aria-label": fieldLabel(this.field.field) } });
    input.value = Array.isArray(this.field.value) ? this.field.value.join("\n") : this.field.value;
    const error = element("p", { className: "sr-edit-error", attrs: { role: "alert" } });
    const actions = element("div", { className: "sr-modal-actions" });
    const cancel = button("取消", { "data-action": "cancel-edit" });
    const submit = button("保存", { "data-action": "save-edit" });
    cancel.addEventListener("click", () => this.close());
    const handleSubmit = async (): Promise<void> => {
      error.textContent = "";
      submit.disabled = true;
      try {
        const value = Array.isArray(this.field.value) ? input.value.split("\n").map((line) => line.trim()).filter(Boolean) : input.value;
        await this.save(value);
        new Notice("已保存，等待同步到代码目录");
        this.close();
      } catch (caught) {
        const message = caught instanceof Error ? caught.message : String(caught);
        error.textContent = message.includes("stale edit") ? "文件已变更，请重新加载后再编辑。" : message;
        submit.disabled = false;
      }
    };
    submit.addEventListener("click", () => { void handleSubmit(); });
    actions.append(cancel, submit);
    this.contentEl.append(hint, input, error, actions);
    input.focus();
  }

  onClose(): void {
    this.contentEl.replaceChildren();
  }
}

export function fieldLabel(field: EditableField["field"]): string {
  const labels: Record<EditableField["field"], string> = {
    goal: "项目目标", stage: "当前阶段", status: "当前状态", next_action: "下一步",
    "risk.title": "风险标题", "risk.status": "风险状态", "risk.detail": "风险详情",
    "decision.title": "决策标题", "decision.rationale": "决策原因", "decision.impact": "决策影响",
    "event.title": "节点标题", "event.meaning": "节点意义", "event.summary": "节点摘要", "event.why": "前因",
    "event.changes": "变更", "event.results": "结果与验证", "event.next": "下一步"
  };
  return labels[field];
}
