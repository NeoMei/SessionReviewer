import { Plugin, type WorkspaceLeaf } from "obsidian";
import { CliRunner } from "./cli/runner";
import { CliSettingsTab } from "./cli/settings";
import { VIEW_TYPE } from "./constants";
import { ProjectRepository } from "./data/repository";
import { ReviewEditor } from "./data/editor";
import { ObsidianVaultPort } from "./data/vault-port";
import { ProjectEvolutionView } from "./view/project-view";
import { defaultViewState, type ViewState } from "./view/render-shell";

export { VIEW_TYPE } from "./constants";

export default class SessionReviewerPlugin extends Plugin {
  private viewState: ViewState = defaultViewState();
  private cliPath = "";
  private codexPath = "";

  async onload(): Promise<void> {
    const stored = await this.loadData() as { viewState?: Partial<ViewState> } | null;
    this.viewState = { ...defaultViewState(), ...(stored?.viewState ?? {}) };
    this.cliPath = typeof (stored as { cliPath?: unknown } | null)?.cliPath === "string" ? (stored as { cliPath: string }).cliPath : "";
    this.codexPath = typeof (stored as { codexPath?: unknown } | null)?.codexPath === "string" ? (stored as { codexPath: string }).codexPath : "";
    const vault = new ObsidianVaultPort(this.app);
    const repository = new ProjectRepository(vault);
    const editor = new ReviewEditor(vault);
    this.registerView(VIEW_TYPE, (leaf: WorkspaceLeaf) => new ProjectEvolutionView(leaf, repository, editor, this.configuredRunner(), this.viewState, async (viewState) => {
      this.viewState = viewState;
      await this.saveData({ viewState, cliPath: this.cliPath, codexPath: this.codexPath });
    }, () => this.codexPath));
    this.addSettingTab(new CliSettingsTab(this.app, this, this.cliPath, async (cliPath) => {
      this.cliPath = cliPath;
      await this.saveData({ viewState: this.viewState, cliPath, codexPath: this.codexPath });
      this.app.workspace.detachLeavesOfType(VIEW_TYPE);
      await this.activateView();
    }, this.codexPath, async (codexPath) => {
      this.codexPath = codexPath;
      await this.saveData({ viewState: this.viewState, cliPath: this.cliPath, codexPath });
    }));
    this.addRibbonIcon("history", "SessionReviewer：打开项目脉络", () => void this.activateView());
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

  private configuredRunner(): CliRunner | undefined {
    if (!this.cliPath) return undefined;
    try { return new CliRunner(this.cliPath); } catch { return undefined; }
  }
}
