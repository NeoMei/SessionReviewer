import { ItemView, type WorkspaceLeaf } from "obsidian";
import { VIEW_TYPE } from "../constants";

export class ProjectEvolutionView extends ItemView {
  constructor(leaf: WorkspaceLeaf) {
    super(leaf);
  }

  getViewType(): string {
    return VIEW_TYPE;
  }

  getDisplayText(): string {
    return "SessionReviewer 项目脉络";
  }

  async onOpen(): Promise<void> {
    this.contentEl.textContent = "正在加载项目…";
  }

  async onClose(): Promise<void> {
    this.contentEl.replaceChildren();
  }
}
