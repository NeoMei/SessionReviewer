import { Plugin, type WorkspaceLeaf } from "obsidian";
import { discoverRuntime, type DiscoveredRuntime, type RuntimeResolver } from "./cli/discovery";
import { VIEW_TYPE } from "./constants";
import { ProjectRepository } from "./data/repository";
import { ReviewEditor } from "./data/editor";
import { ObsidianVaultPort } from "./data/vault-port";
import { ProjectEvolutionView } from "./view/project-view";
import { defaultViewState, type ViewState } from "./view/render-shell";
import { normalizeV4ViewStates, type V4ViewStates } from "./state/v4-view-state";

export { VIEW_TYPE } from "./constants";

export default class SessionReviewerPlugin extends Plugin {
  private viewState: ViewState = defaultViewState();
  private v4ViewStates: V4ViewStates = {};
  private runtime: DiscoveredRuntime | undefined;
  private runtimeResolver: RuntimeResolver = discoverRuntime;
  private legacyPaths: { cliPath?: string } = {};
  private persistence = Promise.resolve();

  async onload(): Promise<void> {
    const stored = await this.loadData() as { viewState?: Partial<ViewState>; v4ViewStates?: unknown; cliPath?: unknown } | null;
    this.viewState = { ...defaultViewState(), ...(stored?.viewState ?? {}) };
    this.v4ViewStates = normalizeV4ViewStates(stored?.v4ViewStates);
    this.legacyPaths = {
      ...(typeof stored?.cliPath === "string" ? { cliPath: stored.cliPath } : {})
    };
    this.runtime = await this.runtimeResolver({
      legacyCliPath: this.legacyPaths.cliPath
    });
    if (this.runtime && this.legacyPaths.cliPath) {
      this.legacyPaths = {};
      await this.persist();
    }
    const vault = new ObsidianVaultPort(this.app);
    const repository = new ProjectRepository(vault);
    const editor = new ReviewEditor(vault);
    this.registerView(VIEW_TYPE, (leaf: WorkspaceLeaf) => new ProjectEvolutionView(leaf, repository, editor, this.runtime?.runner, this.viewState, async (viewState) => {
      this.viewState = viewState;
      await this.persist();
    }, this.v4ViewStates, async (v4ViewState) => {
      this.v4ViewStates = { ...this.v4ViewStates, [v4ViewState.projectId]: v4ViewState };
      await this.persist();
    }));
    this.addRibbonIcon("history", "打开项目脉络", () => void this.activateView());
    this.addCommand({
      id: "open-project-evolution",
      name: "打开项目脉络",
      callback: () => this.activateView()
    });
  }

  async activateView(): Promise<void> {
    const leaf = this.app.workspace.getLeaf("tab");
    await leaf.setViewState({ type: VIEW_TYPE, active: true });
    void this.app.workspace.revealLeaf(leaf);
  }

  private persist(): Promise<void> {
    const snapshot = { viewState: this.viewState, v4ViewStates: this.v4ViewStates, ...this.legacyPaths };
    const write = this.persistence.then(() => this.saveData(snapshot));
    this.persistence = write.catch(() => undefined);
    return write;
  }
}
