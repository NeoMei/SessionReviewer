import { ItemView, Notice, type WorkspaceLeaf } from "obsidian";
import { VIEW_TYPE } from "../constants";
import { ACTIVE_REVIEW_STATES, type ReviewStatus } from "../cli/runner";
import type { BrowserModel, EditableField } from "../contracts/review-v2";
import type { CliRunner } from "../cli/runner";
import type { ReviewEditor } from "../data/editor";
import type { Diagnostic, ProjectDescriptor, ProjectRepository, Snapshot, SnapshotReady } from "../data/repository";
import { ConflictModal, type ConflictAction } from "./conflict-modal";
import { element } from "./dom";
import { EditModal } from "./edit-modal";
import { defaultViewState, renderReadyView, type SaveViewState, type ViewState } from "./render-shell";
import { renderReviewJobBanner, renderStatusBanner, reviewActionLabel, reviewFailureText } from "./status-banner";

export class ProjectEvolutionView extends ItemView {
  private disposeWatch?: () => void;
  private lastReady?: SnapshotReady;
  private selected?: ProjectDescriptor;
  private projects: ProjectDescriptor[] = [];
  private currentState: ViewState;
  private announcement = "";
  private cliDiagnostic?: Diagnostic;
  private hiddenConflictIds: string[] = [];
  private reviewStatus?: ReviewStatus;
  private reviewActionInFlight = false;
  private reviewPollGeneration = 0;
  private reviewPollTimer?: ReturnType<typeof setTimeout>;

  constructor(
    leaf: WorkspaceLeaf,
    private readonly repository?: ProjectRepository,
    private readonly editor?: ReviewEditor,
    private readonly runner?: CliRunner,
    private readonly initialState: ViewState = defaultViewState(),
    private readonly saveState?: SaveViewState,
    private readonly agentExecutable: string | (() => string) = ""
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
    this.stopReviewPolling();
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

  private async refresh(projects: ProjectDescriptor[], options: { reviewStatus?: boolean } = {}): Promise<void> {
    if (!this.selected) return;
    const snapshot = await this.repository!.load(this.selected, this.lastReady);
    if (snapshot.kind === "ready") this.lastReady = snapshot;
    await this.refreshCliStatus();
    if (options.reviewStatus !== false) await this.refreshReviewStatus();
    this.renderSnapshot(snapshot, projects);
    this.scheduleReviewPolling();
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
    const review = this.runner
      ? {
          label: reviewActionLabel(this.reviewStatus),
          disabled: this.reviewActionInFlight || (this.reviewStatus !== undefined && ACTIVE_REVIEW_STATES.includes(this.reviewStatus.state)),
          onStart: () => this.startReview()
        }
      : undefined;
    const browser = renderReadyView(model, { ...this.currentState, projectId: model.review.projectId }, (viewState) => {
      this.currentState = viewState;
      return this.saveState?.(viewState);
    }, this.editor ? (field) => this.openEditor(field, model) : undefined, review);
    if (projects.length > 1) browser.prepend(this.projectPicker(projects));
    if (snapshot.kind === "pending_edit" || snapshot.kind === "stale") browser.prepend(renderStatusBanner(snapshot.diagnostic, this.actionFor(snapshot.diagnostic)));
    if (this.cliDiagnostic) browser.prepend(renderStatusBanner(this.cliDiagnostic, this.actionFor(this.cliDiagnostic)));
    if (this.announcement) browser.prepend(element("div", { className: "sr-sr-only", text: this.announcement, attrs: { "aria-live": "polite" } }));
    if (this.reviewStatus) {
      const banner = renderReviewJobBanner(this.reviewStatus, {
        onCancel: this.reviewStatus.canCancel ? () => this.cancelReview() : undefined,
        onRetry: this.reviewStatus.canRetry ? () => this.retryReview() : undefined,
        onSyncOnly: this.reviewStatus.canSyncOnly ? () => this.syncOnlyChanges() : undefined
      });
      if (banner) browser.prepend(banner);
    }
    this.contentEl.append(browser);
  }

  private async refreshCliStatus(): Promise<void> {
    this.cliDiagnostic = undefined;
    this.hiddenConflictIds = [];
    if (!this.selected) return;
    if (!this.runner) {
      this.cliDiagnostic = { code: "cli_unavailable", message: "" };
      return;
    }
    try {
      const status = await this.runner.status(this.selected.projectId);
      this.hiddenConflictIds = Array.isArray(status.hidden_conflict_ids) ? status.hidden_conflict_ids.filter((value): value is string => typeof value === "string") : [];
      if (this.hiddenConflictIds.length) this.cliDiagnostic = { code: "content_conflict", message: `待处理冲突 ${this.hiddenConflictIds.length} 个。` };
      else if (status.migration === "required") this.cliDiagnostic = { code: "migration_required", message: "" };
      else if (status.machine_state === "blocked") this.cliDiagnostic = { code: "machine_ledger_modified", message: "" };
    } catch (error) {
      this.cliDiagnostic = { code: "cli_unavailable", message: error instanceof Error ? error.message : String(error) };
    }
  }

  private actionFor(diagnostic: Diagnostic): (() => void) | undefined {
    if (diagnostic.code === "stale_snapshot") return () => { void this.refresh(this.projects); };
    if ((diagnostic.code === "history_parse_failed" || diagnostic.code === "review_parse_failed") && this.selected) {
      const file = diagnostic.code === "history_parse_failed" ? `${this.selected.root}/项目历史.md` : `${this.selected.root}/项目回顾.md`;
      return () => { void this.app.workspace.openLinkText(file, "", false); };
    }
    if (!this.runner || !this.selected) return undefined;
    if (diagnostic.code === "migration_required") return () => { void this.runCliAction(() => this.runner!.migrationDryRun(this.selected!.projectId), "迁移预览已完成。"); };
    if (diagnostic.code === "machine_ledger_modified") return () => { void this.runCliAction(() => this.runner!.repairMachineLedger(this.selected!.projectId), "机器账本已修复。"); };
    if (diagnostic.code === "content_conflict") return () => { void this.openConflict(); };
    if (diagnostic.code === "sync_not_run") return () => { void this.runCliAction(() => this.runner!.status(this.selected!.projectId), "同步状态已刷新。"); };
    return undefined;
  }

  private async runCliAction(action: () => Promise<unknown>, success: string): Promise<void> {
    try {
      await action();
      this.announcement = success;
    } catch (error) {
      this.announcement = error instanceof Error ? error.message : String(error);
    }
    await this.refresh(this.projects);
  }

  private agentPath(): string {
    return typeof this.agentExecutable === "function" ? this.agentExecutable() : this.agentExecutable;
  }

  private async refreshReviewStatus(): Promise<void> {
    this.reviewStatus = undefined;
    if (!this.runner || !this.selected) return;
    try {
      this.reviewStatus = await this.runner.reviewStatus(this.selected.projectId);
    } catch {
      // 状态读取失败时隐藏横幅，保持既有浏览能力。
    }
  }

  private startReview(): void {
    if (!this.runner || !this.selected || this.reviewActionInFlight) return;
    if (this.reviewStatus && ACTIVE_REVIEW_STATES.includes(this.reviewStatus.state)) return;
    const projectId = this.selected.projectId;
    const agentExecutable = this.agentPath();
    if (!agentExecutable) {
      const message = reviewFailureText("E_AGENT_UNCONFIGURED", "尚未配置 Codex。");
      this.announcement = message;
      new Notice(message);
      this.renderSnapshot(this.lastReady ?? emptyReviewSnapshot(), this.projects);
      return;
    }
    void this.runReviewAction(() => this.runner!.startReview(projectId, agentExecutable), "自动总结已开始。");
  }

  private cancelReview(): void {
    const jobId = this.reviewStatus?.jobId;
    if (!this.runner || !jobId) return;
    void this.runReviewAction(() => this.runner!.cancelReview(jobId), "已请求取消自动总结。");
  }

  private retryReview(): void {
    const status = this.reviewStatus;
    const jobId = status?.jobId;
    if (!this.runner || !jobId || status.retryExpectedAttempt === undefined || status.retryExpectedRevision === undefined) return;
    const attempt = status.retryExpectedAttempt;
    const revision = status.retryExpectedRevision;
    void this.runReviewAction(() => this.runner!.retryReview(jobId, this.agentPath(), attempt, revision), "已重新开始自动总结。");
  }

  private syncOnlyChanges(): void {
    if (!this.runner || !this.selected) return;
    void this.runCliAction(() => this.runner!.syncProject(this.selected!.projectId), "已有修改同步完成。");
  }

  private async runReviewAction(action: () => Promise<ReviewStatus>, success: string): Promise<void> {
    if (this.reviewActionInFlight) return;
    this.reviewActionInFlight = true;
    this.renderSnapshot(this.lastReady ?? emptyReviewSnapshot(), this.projects);
    try {
      this.reviewStatus = await action();
      this.announcement = success;
    } catch (error) {
      this.announcement = error instanceof Error ? error.message : String(error);
      new Notice(this.announcement);
    }
    this.reviewActionInFlight = false;
    await this.refresh(this.projects, { reviewStatus: false });
  }

  private scheduleReviewPolling(): void {
    this.stopReviewPolling();
    if (!this.reviewStatus || !ACTIVE_REVIEW_STATES.includes(this.reviewStatus.state)) return;
    void this.pollReviewJob(this.reviewPollGeneration, 0);
  }

  private stopReviewPolling(): void {
    this.reviewPollGeneration += 1;
    if (this.reviewPollTimer !== undefined) {
      clearTimeout(this.reviewPollTimer);
      this.reviewPollTimer = undefined;
    }
  }

  private async pollReviewJob(generation: number, round: number): Promise<void> {
    const delay = round === 0 ? 1000 : round === 1 ? 2000 : 5000;
    await new Promise<void>((resolve) => {
      this.reviewPollTimer = setTimeout(resolve, delay);
    });
    this.reviewPollTimer = undefined;
    if (generation !== this.reviewPollGeneration) return;
    const previous = this.reviewStatus;
    await this.refreshReviewStatus();
    if (generation !== this.reviewPollGeneration) return;
    const current = this.reviewStatus;
    if (!current) {
      void this.pollReviewJob(generation, round + 1);
      return;
    }
    if (!ACTIVE_REVIEW_STATES.includes(current.state)) {
      if (previous && ACTIVE_REVIEW_STATES.includes(previous.state)) this.announcement = reviewTerminalAnnouncement(current);
      await this.refresh(this.projects, { reviewStatus: false });
      return;
    }
    this.renderSnapshot(this.lastReady ?? emptyReviewSnapshot(), this.projects);
    void this.pollReviewJob(generation, round + 1);
  }

  private async openConflict(): Promise<void> {
    if (!this.repository || !this.runner || !this.selected || !this.hiddenConflictIds[0]) return;
    try {
      const conflict = await this.repository.loadConflict(this.selected, this.hiddenConflictIds[0]);
      const modal = new ConflictModal(this.app, conflict, (action, manual) => {
        if (!window.confirm("确认用选定内容解决这个冲突？")) return;
        modal.close();
        void this.resolveConflict(conflict.id, action, manual);
      });
      modal.open();
    } catch (error) {
      this.announcement = error instanceof Error ? error.message : String(error);
      this.renderSnapshot(this.lastReady ?? { kind: "empty", diagnostic: { code: "stale_snapshot", message: this.announcement } }, this.projects);
    }
  }

  private async resolveConflict(conflictId: string, action: ConflictAction, manual?: string): Promise<void> {
    if (!this.runner || !this.selected) return;
    await this.runCliAction(
      () => action === "manual_merge" ? this.runner!.manualMerge(this.selected!.projectId, conflictId, manual ?? "") : this.runner!.resolve(this.selected!.projectId, conflictId, action),
      "冲突已解决。"
    );
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
      this.stopReviewPolling();
      this.reviewStatus = undefined;
      this.disposeWatch?.();
      this.selected = next;
      this.lastReady = undefined;
      void this.refresh(projects).then(() => { this.disposeWatch = this.repository!.watch(next, () => { void this.refresh(projects); }); });
    });
    wrapper.append(select);
    return wrapper;
  }
}

function emptyReviewSnapshot(): Snapshot {
  return { kind: "empty", diagnostic: { code: "stale_snapshot", message: "" } };
}

function reviewTerminalAnnouncement(status: ReviewStatus): string {
  if (status.state === "failed") return reviewFailureText(status.errorCode, "自动总结失败，可重试。");
  if (status.state === "cancelled") return reviewFailureText(status.errorCode, "自动总结已取消。");
  if (status.state === "completed") return "自动总结完成。";
  return "";
}
