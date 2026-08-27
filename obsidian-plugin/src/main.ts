import { Plugin, type WorkspaceLeaf } from "obsidian";
import { VIEW_TYPE } from "./constants";
import { ProjectEvolutionView } from "./view/project-view";

export { VIEW_TYPE } from "./constants";

export default class SessionReviewerPlugin extends Plugin {
  async onload(): Promise<void> {
    this.registerView(VIEW_TYPE, (leaf: WorkspaceLeaf) => new ProjectEvolutionView(leaf));
    this.addCommand({
      id: "open-project-evolution",
      name: "打开项目脉络",
      callback: () => this.activateView()
    });
  }

  async activateView(): Promise<void> {
    const leaf = this.app.workspace.getLeaf("tab");
    await leaf.setViewState({ type: VIEW_TYPE, active: true });
    this.app.workspace.revealLeaf(leaf);
  }
}
