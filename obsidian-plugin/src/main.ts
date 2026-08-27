import { Plugin, type WorkspaceLeaf } from "obsidian";
import { VIEW_TYPE } from "./constants";
import { ProjectRepository } from "./data/repository";
import { ObsidianVaultPort } from "./data/vault-port";
import { ProjectEvolutionView } from "./view/project-view";
import { defaultViewState, type ViewState } from "./view/render-shell";

export { VIEW_TYPE } from "./constants";

export default class SessionReviewerPlugin extends Plugin {
  private viewState: ViewState = defaultViewState();

  async onload(): Promise<void> {
    const stored = await this.loadData() as { viewState?: Partial<ViewState> } | null;
    this.viewState = { ...defaultViewState(), ...(stored?.viewState ?? {}) };
    const repository = new ProjectRepository(new ObsidianVaultPort(this.app));
    this.registerView(VIEW_TYPE, (leaf: WorkspaceLeaf) => new ProjectEvolutionView(leaf, repository, this.viewState, async (viewState) => {
      this.viewState = viewState;
      await this.saveData({ viewState });
    }));
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
