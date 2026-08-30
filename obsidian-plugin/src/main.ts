import { Plugin, type WorkspaceLeaf } from "obsidian";
import { discoverRuntime, type DiscoveredRuntime, type RuntimeResolver } from "./cli/discovery";
import { VIEW_TYPE } from "./constants";
import { ProjectRepository } from "./data/repository";
import { ReviewEditor } from "./data/editor";
import { ObsidianVaultPort } from "./data/vault-port";
import { ProjectEvolutionView } from "./view/project-view";
import { defaultViewState, type ViewState } from "./view/render-shell";

export { VIEW_TYPE } from "./constants";

export default class SessionReviewerPlugin extends Plugin {
  private viewState: ViewState = defaultViewState();
  private runtime: DiscoveredRuntime | undefined;
  private runtimeResolver: RuntimeResolver = discoverRuntime;
  private legacyPaths: { cliPath?: string; codexPath?: string } = {};

  async onload(): Promise<void> {
    const stored = await this.loadData() as { viewState?: Partial<ViewState>; cliPath?: unknown; codexPath?: unknown } | null;
    this.viewState = { ...defaultViewState(), ...(stored?.viewState ?? {}) };
    this.legacyPaths = {
      ...(typeof stored?.cliPath === "string" ? { cliPath: stored.cliPath } : {}),
      ...(typeof stored?.codexPath === "string" ? { codexPath: stored.codexPath } : {})
    };
    this.runtime = await this.runtimeResolver({
      legacyCliPath: this.legacyPaths.cliPath,
      legacyAgentPath: this.legacyPaths.codexPath
    });
    if (this.runtime && (this.legacyPaths.cliPath || this.legacyPaths.codexPath)) {
      this.legacyPaths = {};
      await this.saveData({ viewState: this.viewState });
    }
    const vault = new ObsidianVaultPort(this.app);
    const repository = new ProjectRepository(vault);
    const editor = new ReviewEditor(vault);
    this.registerView(VIEW_TYPE, (leaf: WorkspaceLeaf) => new ProjectEvolutionView(leaf, repository, editor, this.runtime?.runner, this.viewState, async (viewState) => {
      this.viewState = viewState;
      await this.saveData({ viewState, ...this.legacyPaths });
    }, () => this.runtime?.agentExecutable ?? ""));
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
}
