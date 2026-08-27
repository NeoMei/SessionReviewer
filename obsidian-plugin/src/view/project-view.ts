import { ItemView, type WorkspaceLeaf } from "obsidian";
import { VIEW_TYPE } from "../constants";
import type { BrowserModel, EditableField } from "../contracts/review-v2";
import type { ReviewEditor } from "../data/editor";
import type { ProjectDescriptor, ProjectRepository, Snapshot, SnapshotReady } from "../data/repository";
import { element } from "./dom";
import { EditModal } from "./edit-modal";
import { defaultViewState, renderReadyView, type SaveViewState, type ViewState } from "./render-shell";

export class ProjectEvolutionView extends ItemView {
  private disposeWatch?: () => void;
  private lastReady?: SnapshotReady;
  private selected?: ProjectDescriptor;
  private projects: ProjectDescriptor[] = [];
  private currentState: ViewState;
  private announcement = "";

  constructor(
    leaf: WorkspaceLeaf,
    private readonly repository?: ProjectRepository,
    private readonly editor?: ReviewEditor,
    private readonly initialState: ViewState = defaultViewState(),
    private readonly saveState?: SaveViewState
  ) {
    super(leaf);
    this.currentState = initialState;
  }

  getViewType(): string {
    return VIEW_TYPE;
  }

  getDisplayText(): string {
    return "SessionReviewer 项目脉络";
  }

  async onOpen(): Promise<void> {
    if (!this.repository) {
      this.contentEl.textContent = "正在加载项目…";
      return;
    }
    await this.openProjects();
  }

  async onClose(): Promise<void> {
    this.disposeWatch?.();
    this.contentEl.replaceChildren();
  }

  private async openProjects(): Promise<void> {
    this.contentEl.replaceChildren(element("p", { className: "sr-loading", text: "正在发现项目…" }));
    const projects = await this.repository!.discover();
    this.projects = projects;
    if (projects.length === 0) {
      this.contentEl.replaceChildren(element("div", { className: "session-reviewer-browser" }, [
        element("h1", { text: "还没有 v2 项目回顾" }),
        element("p", { text: "请先运行 SessionReviewer v2 迁移或同步，生成项目回顾、项目历史和机器账本。" })
      ]));
      return;
    }
    this.selected = projects.find((project) => project.projectId === this.initialState.projectId) ?? projects[0];
    await this.refresh(projects);
    this.disposeWatch = this.repository!.watch(this.selected, () => { void this.refresh(projects); });
  }

  private async refresh(projects: ProjectDescriptor[]): Promise<void> {
    if (!this.selected) return;
    const snapshot = await this.repository!.load(this.selected, this.lastReady);
    if (snapshot.kind === "ready") this.lastReady = snapshot;
    this.renderSnapshot(snapshot, projects);
  }

  private renderSnapshot(snapshot: Snapshot, projects: ProjectDescriptor[]): void {
    this.contentEl.replaceChildren();
    if (snapshot.kind === "empty" || snapshot.kind === "migration_required") {
      const diagnostic = snapshot.kind === "empty" ? snapshot.diagnostic : snapshot.diagnostic;
      this.contentEl.append(element("div", { className: "session-reviewer-browser" }, [
        element("h1", { text: "暂时无法打开项目回顾" }),
        element("p", { text: diagnostic?.message ?? "项目还没有可用快照。" })
      ]));
      return;
    }
    const model = snapshot.kind === "stale" ? snapshot.lastValid.model : snapshot.model;
    const browser = renderReadyView(model, { ...this.currentState, projectId: model.review.projectId }, (viewState) => {
      this.currentState = viewState;
      return this.saveState?.(viewState);
    }, this.editor ? (field) => this.openEditor(field, model) : undefined);
    if (projects.length > 1) browser.prepend(this.projectPicker(projects));
    if (snapshot.kind === "pending_edit" || snapshot.kind === "stale") {
      browser.prepend(element("div", {
        className: `sr-banner sr-banner-${snapshot.kind}`,
        text: snapshot.kind === "pending_edit" ? `等待同步：${snapshot.diagnostic.message}` : `显示上次可信内容：${snapshot.diagnostic.message}`,
        attrs: { role: "status" }
      }));
    }
    if (this.announcement) browser.prepend(element("div", { className: "sr-sr-only", text: this.announcement, attrs: { "aria-live": "polite" } }));
    this.contentEl.append(browser);
  }

  private openEditor(field: EditableField, model: BrowserModel): void {
    if (!this.editor || !this.repository) return;
    const path = field.document === "review" ? model.source.reviewPath : model.source.historyPath;
    const expectedSha256 = field.document === "review" ? model.source.reviewSha256 : model.source.historySha256;
    new EditModal(this.app, field, async (value) => {
      const result = await this.editor!.apply({ path, expectedSha256, document: field.document, unitId: field.unitId, field: field.field, value });
      this.repository!.ignoreSelfWrite(path, result.sha256);
      this.announcement = "已保存，等待同步到代码目录。";
      await this.refresh(this.projects);
    }).open();
  }

  private projectPicker(projects: ProjectDescriptor[]): HTMLElement {
    const wrapper = element("label", { className: "sr-project-picker", text: "项目 " });
    const select = element("select", { attrs: { "aria-label": "选择项目" } });
    for (const project of projects) {
      const option = element("option", { text: project.name, attrs: { value: project.projectId } });
      option.selected = project.projectId === this.selected?.projectId;
      select.append(option);
    }
    select.addEventListener("change", () => {
      const next = projects.find((project) => project.projectId === select.value);
      if (!next) return;
      this.disposeWatch?.();
      this.selected = next;
      this.lastReady = undefined;
      void this.refresh(projects).then(() => { this.disposeWatch = this.repository!.watch(next, () => { void this.refresh(projects); }); });
    });
    wrapper.append(select);
    return wrapper;
  }
}
