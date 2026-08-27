import { type App, Modal } from "obsidian";
import { button, element } from "./dom";

export interface ConflictCandidate {
  id: string;
  unit: string;
  base: string;
  project: string;
  obsidian: string;
}

export type ConflictAction = "accept_project" | "accept_obsidian" | "manual_merge";

export function renderConflict(conflict: ConflictCandidate, resolve?: (action: ConflictAction, manual?: string) => void): HTMLElement {
  const root = element("div", { className: "session-reviewer-conflict" });
  root.append(element("h2", { text: "处理同一内容的两边修改" }), element("p", { text: `影响单元：${conflict.unit}` }));
  const candidates = element("div", { className: "sr-conflict-candidates" });
  for (const [label, value] of [["Base", conflict.base], ["Project", conflict.project], ["Obsidian", conflict.obsidian]]) {
    const card = element("section");
    card.append(element("h3", { text: label }), element("pre", { text: value }));
    candidates.append(card);
  }
  const manual = element("textarea", { attrs: { "aria-label": "手工合并内容", rows: "10" } });
  manual.value = conflict.obsidian;
  const actions = element("div", { className: "sr-modal-actions" });
  for (const [action, label] of [["accept_project", "使用 Project"], ["accept_obsidian", "使用 Obsidian"], ["manual_merge", "确认手工合并"]] as const) {
    const control = button(label, { "data-resolution-action": action });
    control.addEventListener("click", () => resolve?.(action, action === "manual_merge" ? manual.value : undefined));
    actions.append(control);
  }
  root.append(candidates, element("h3", { text: "手工合并" }), manual, actions);
  return root;
}

export class ConflictModal extends Modal {
  constructor(app: App, private readonly conflict: ConflictCandidate, private readonly resolve: (action: ConflictAction, manual?: string) => void) { super(app); }
  onOpen(): void { this.contentEl.replaceChildren(renderConflict(this.conflict, this.resolve)); }
  onClose(): void { this.contentEl.replaceChildren(); }
}
