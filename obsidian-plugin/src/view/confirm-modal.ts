import { type App, Modal } from "obsidian";
import { button, element } from "./dom";

export class ConfirmModal extends Modal {
  constructor(
    app: App,
    private readonly message: string,
    private readonly onConfirm: () => void
  ) {
    super(app);
  }

  onOpen(): void {
    this.contentEl.replaceChildren();
    this.contentEl.classList.add("session-reviewer-confirm-modal");
    this.contentEl.append(element("p", { className: "sr-confirm-message", text: this.message }));
    const actions = element("div", { className: "sr-modal-actions" });
    const cancel = button("取消", { "data-action": "cancel-confirm" });
    const confirm = button("确认", { "data-action": "confirm" });
    cancel.addEventListener("click", () => this.close());
    confirm.addEventListener("click", () => {
      this.close();
      this.onConfirm();
    });
    actions.append(cancel, confirm);
    this.contentEl.append(actions);
  }

  onClose(): void {
    this.contentEl.replaceChildren();
  }
}
