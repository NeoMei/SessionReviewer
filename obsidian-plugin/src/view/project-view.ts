import { ItemView, Notice, type WorkspaceLeaf } from "obsidian";
import { VIEW_TYPE } from "../constants";
import type { BrowserModel, EditableField, ScanStatus } from "../contracts/review-v3";
import type { SessionEventPageV1 } from "../contracts/review-v4";
import { SyncStatusError, type CliRunner, type ProblemCandidate } from "../cli/runner";
import type { ReviewEditor } from "../data/editor";
import type { Diagnostic, MarkdownSnapshotReady, ProjectDescriptor, ProjectRepository, Snapshot, SnapshotReady } from "../data/repository";
import { ConflictModal, type ConflictAction } from "./conflict-modal";
import { ConfirmModal } from "./confirm-modal";
import { element } from "./dom";
import { EditModal } from "./edit-modal";
import { defaultViewState, renderReadyView, type SaveViewState, type ViewState } from "./render-shell";
import { renderScanJobBanner, renderStatusBanner, scanActionLabel } from "./status-banner";
import { renderMarkdownV4View } from "./presentation";
import type { ScanRecordsElement } from "./render-scan-records";
import { normalizeV4ViewState, normalizeV4ViewStates, type V4ViewState, type V4ViewStatePatch, type V4ViewStates } from "../state/v4-view-state";
import { filterSessions, normalizeSessionBrowserState } from "../state/session-browser-state";

export class ProjectEvolutionView extends ItemView {
  private disposeWatch?: () => void;
  private lastReady?: SnapshotReady;
  private lastMarkdownReady?: MarkdownSnapshotReady;
  private selected?: ProjectDescriptor;
  private projects: ProjectDescriptor[] = [];
  private currentState: ViewState;
  private announcement = "";
  private cliDiagnostic?: Diagnostic;
  private hiddenConflictIds: string[] = [];
  private scanStatus?: ScanStatus;
  private scanActionInFlight = false;
  private scanPollGeneration = 0;
  private scanPollTimer?: number;
  private refreshEpoch = 0;
  private closed = false;
  private settlingTimer?: number;
  private scanRecords?: ScanRecordsElement;
  private v4Browser?: HTMLElement & { dispose?: () => void };
  private eventPageCache = new Map<string, SessionEventPageV1>();
  private eventCacheGeneration = "";
  private pendingEventRecovery?: { projectId: string; provider: string; sessionId: string; ordinal: number; generationId: string; sessionViewDigest: string | null };
  private recoveredEventSelection?: { projectId: string; provider: string; sessionId: string };
  private eventRecoveryEpoch = 0;
  private problemCandidates: ProblemCandidate[] = [];
  private problemUnavailableReason?: string;

  constructor(
    leaf: WorkspaceLeaf,
    private readonly repository?: ProjectRepository,
    private readonly editor?: ReviewEditor,
    private readonly runner?: CliRunner,
    private readonly initialState: ViewState = defaultViewState(),
    private readonly saveState?: SaveViewState,
    initialV4States: unknown = {},
    private readonly saveV4State?: (state: V4ViewState) => void | Promise<void>,
    private readonly loadV4State?: (projectId: string) => V4ViewState | undefined
  ) {
    super(leaf);
    this.currentState = initialState;
    this.v4States = normalizeV4ViewStates(initialV4States);
  }

  private v4States: V4ViewStates;

  getViewType(): string {
    return VIEW_TYPE;
  }

  getDisplayText(): string {
    return "项目脉络";
  }

  async onOpen(): Promise<void> {
    this.closed = false;
    if (!this.repository) {
      this.contentEl.textContent = "正在加载项目…";
      return;
    }
    await this.openProjects();
  }

  async onClose(): Promise<void> {
    this.closed = true;
    this.refreshEpoch += 1;
    this.invalidateEventRecovery();
    this.recoveredEventSelection = undefined;
    this.stopSettlingRetry();
    this.stopScanPolling();
    this.disposeWatch?.();
    this.disposeWatch = undefined;
    this.disposeScanRecords();
    this.resetEventCache();
    this.contentEl.replaceChildren();
  }

  private async openProjects(): Promise<void> {
    const epoch = ++this.refreshEpoch;
    this.contentEl.replaceChildren(element("p", { className: "sr-loading", text: "正在发现项目…" }));
    const projects = await this.repository!.discover();
    if (this.closed || epoch !== this.refreshEpoch) return;
    this.projects = projects;
    if (projects.length === 0) {
      this.contentEl.replaceChildren(element("div", { className: "session-reviewer-browser" }, [
        this.projectPicker(projects),
        element("h1", { text: "还没有项目回顾" }),
        element("p", { text: "请先运行 SessionReviewer 扫描或同步，生成项目回顾、项目历史和机器账本。" })
      ]));
      return;
    }
    this.selected = projects.find((project) => project.projectId === this.initialState.projectId) ?? projects[0];
    this.watchSelectedProject();
    await this.refresh(projects);
  }

  private async refresh(projects: ProjectDescriptor[], options: { scanStatus?: boolean; settlingAttempt?: number; eventRecoveryEpoch?: number } = {}): Promise<void> {
    if (options.eventRecoveryEpoch === undefined && this.pendingEventRecovery) this.invalidateEventRecovery();
    if (!this.selected || this.closed) return;
    const selected = this.selected;
    const epoch = ++this.refreshEpoch;
    const current = () => !this.closed && epoch === this.refreshEpoch && selected === this.selected &&
      (options.eventRecoveryEpoch === undefined || options.eventRecoveryEpoch === this.eventRecoveryEpoch);
    this.stopSettlingRetry();
    this.stopScanPolling();
    const previous = selected.format === "markdown-v4" ? this.lastMarkdownReady : this.lastReady;
    const snapshot = await this.repository!.load(selected, previous);
    if (!current()) return;
    if (options.eventRecoveryEpoch !== undefined) {
      if (snapshot.kind !== "markdown-v4" || snapshot.state.kind !== "public_valid") throw new Error("event recovery refresh rejected");
      const recovery = this.pendingEventRecovery;
      const refreshed = recovery && snapshot.state.index.sessions.find((entry) => entry.provider === recovery.provider && entry.session_id === recovery.sessionId);
      if (recovery && refreshed && snapshot.state.index.generation_id === recovery.generationId && refreshed.session_view_digest === recovery.sessionViewDigest) {
        throw new Error("event recovery binding did not change");
      }
    }
    const cli = await this.readCliStatus(selected);
    if (!current()) return;
    if (selected.format === "markdown-v4" && this.runner && cli.diagnostic?.code !== "cli_unavailable") {
      try {
        const problems = await this.runner.listProblemCandidates(selected.projectId);
        if (!current()) return;
        this.problemCandidates = problems.candidates ?? [];
        this.problemUnavailableReason = undefined;
      } catch (error) {
        this.problemCandidates = [];
        this.problemUnavailableReason = error instanceof Error ? error.message : "无法读取问题候选。";
      }
    }
    const scanStatus = selected.format === "markdown-v4" ? undefined : options.scanStatus === false ? this.scanStatus : await this.readScanStatus(selected);
    if (!current()) return;
    // Publish one complete result set. Obsolete loads/status commands never
    // change even the cached last-valid snapshot or shared diagnostics.
    if (snapshot.kind === "ready") this.lastReady = snapshot;
    if (snapshot.kind === "markdown-v4" && snapshot.state.kind === "public_valid") this.lastMarkdownReady = { ...snapshot, state: snapshot.state };
    this.cliDiagnostic = cli.diagnostic;
    this.hiddenConflictIds = cli.hiddenConflictIds;
    this.scanStatus = scanStatus;
    this.renderSnapshot(snapshot, projects);
    this.scheduleScanPolling();
    const delays = [250, 750, 2000];
    const attempt = options.settlingAttempt ?? 0;
    const unsettled = cli.diagnostic?.code === "sync_status_failed" || snapshot.kind === "markdown-v4-stale" || (snapshot.kind === "markdown-v4" && (snapshot.state.kind === "invalid" || snapshot.state.kind === "unverified" || snapshot.state.kind === "read_failed"));
    if (selected.format === "markdown-v4" && unsettled && attempt < delays.length) {
      this.settlingTimer = window.setTimeout(() => {
        this.settlingTimer = undefined;
        if (current()) void this.refresh(projects, { settlingAttempt: attempt + 1 });
      }, delays[attempt]);
    }
  }

  private stopSettlingRetry(): void {
    if (this.settlingTimer !== undefined) window.clearTimeout(this.settlingTimer);
    this.settlingTimer = undefined;
  }

  private watchSelectedProject(): void {
    this.disposeWatch?.();
    const selected = this.selected;
    if (!selected || this.closed) return;
    this.disposeWatch = this.repository!.watch(selected, () => {
      if (!this.closed && this.selected === selected) void this.refresh(this.projects);
    });
  }

  private renderSnapshot(snapshot: Snapshot, projects: ProjectDescriptor[]): void {
    this.disposeScanRecords();
    this.contentEl.replaceChildren();
    if (snapshot.kind === "markdown-v4" || snapshot.kind === "markdown-v4-stale") {
      const current = snapshot.kind === "markdown-v4-stale" ? snapshot.lastValid : snapshot;
      const recovery = this.eventRecoveryFor(current);
      const generation = current.state.kind === "public_valid" ? `${current.descriptor.projectId}\0${current.state.index.generation_id}` : "";
      if (generation !== this.eventCacheGeneration) {
        this.eventPageCache.clear();
        this.eventCacheGeneration = generation;
      }
      const activeProblemState = snapshot.kind === "markdown-v4" && snapshot.state.kind === "public_valid" ? snapshot.state : undefined;
      const browser = renderMarkdownV4View(
        snapshot,
        (path) => { void this.app.workspace.openLinkText(path, "", false); },
        {
          cliUnavailable: this.cliDiagnostic?.code === "cli_unavailable" || !this.runner,
          loadSessionEvents: this.runner && this.cliDiagnostic?.code !== "cli_unavailable"
            ? (request) => this.runner!.getSessionEvents(request)
            : undefined,
          loadConversation: this.runner && this.cliDiagnostic?.code !== "cli_unavailable" && typeof this.runner.getConversation === "function"
            ? (request) => this.runner!.getConversation(request)
            : undefined,
          loadSessionSummary: this.runner && this.cliDiagnostic?.code !== "cli_unavailable" && typeof this.runner.getSessionSummary === "function"
            ? (request) => this.runner!.getSessionSummary(request)
            : undefined,
          eventPageCache: this.eventPageCache,
          refreshSessionEvents: (request) => this.refreshSessionEvents(request),
          cancelSessionEventRecovery: () => this.invalidateEventRecovery(),
          recoverySession: recovery?.selection,
          initialSessionEventOrdinal: recovery?.ordinal,
          recoveryAlreadyAttempted: recovery !== undefined,
          recoverySelectionUnavailable: recovery?.unavailable,
          initialState: this.v4StateForRender(current),
          saveStatePatch: (patch) => this.saveV4Patch(current.descriptor.projectId, patch),
          problemCandidates: this.problemCandidates,
          problemUnavailableReason: this.problemUnavailableReason,
          createProblem: this.runner && activeProblemState ? async (question) => {
            const p = activeProblemState.value.presentation;
            await this.runner!.createProblem({ projectId: current.descriptor.projectId, expectedProblemMapRevision: p.problem_map_revision, expectedReviewSHA256: activeProblemState.ledger.review_sha256, question });
            await this.refresh(this.projects);
          } : undefined,
          transitionCandidate: this.runner && activeProblemState ? async (candidate, action, targetProblemId) => {
            const p = activeProblemState.value.presentation;
            await this.runner!.transitionProblemCandidate({ projectId: current.descriptor.projectId, expectedProblemMapRevision: p.problem_map_revision, expectedReviewSHA256: activeProblemState.ledger.review_sha256, candidate, action, targetProblemId });
            await this.refresh(this.projects);
          } : undefined,
          setProblemState: this.runner && activeProblemState ? async (problem, action) => {
            const p = activeProblemState.value.presentation;
            await this.runner!.setProblemState({ projectId: current.descriptor.projectId, expectedProblemMapRevision: p.problem_map_revision, expectedReviewSHA256: activeProblemState.ledger.review_sha256, problem, action });
            await this.refresh(this.projects);
          } : undefined,
          editProblem: this.runner && activeProblemState ? async (problem, fields) => {
            const p = activeProblemState.value.presentation;
            await this.runner!.editProblem({ projectId: current.descriptor.projectId, expectedProblemMapRevision: p.problem_map_revision, expectedReviewSHA256: activeProblemState.ledger.review_sha256, problem, ...fields });
            await this.refresh(this.projects);
          } : undefined,
          moveProblem: this.runner && activeProblemState ? async (problem, newParentId) => {
            const p = activeProblemState.value.presentation;
            await this.runner!.moveProblem({ projectId: current.descriptor.projectId, expectedProblemMapRevision: p.problem_map_revision, expectedReviewSHA256: activeProblemState.ledger.review_sha256, problem, newParentId });
            await this.refresh(this.projects);
          } : undefined,
          reorderProblems: this.runner && activeProblemState ? async (parentId, orderedChildIds) => {
            const p = activeProblemState.value.presentation;
            await this.runner!.reorderProblemChildren({ projectId: current.descriptor.projectId, expectedProblemMapRevision: p.problem_map_revision, expectedReviewSHA256: activeProblemState.ledger.review_sha256, parentId, orderedChildIds });
            await this.refresh(this.projects);
          } : undefined
        }
      );
      this.v4Browser = browser;
      if (recovery) {
        this.pendingEventRecovery = undefined;
        if (recovery.ordinal !== undefined) {
          this.recoveredEventSelection = { projectId: current.descriptor.projectId, ...recovery.selection };
          browser.prepend(element("p", { className: "sr-event-recovery-status", text: "项目索引已更新；正在按新一代索引恢复原 Session 的事件位置。", attrs: { role: "status" } }));
        } else this.recoveredEventSelection = undefined;
      }
      this.scanRecords = browser.scanRecords;
      browser.prepend(this.projectPicker(projects));
      if (snapshot.kind === "markdown-v4-stale") browser.prepend(renderStatusBanner(snapshot.diagnostic));
      if (this.cliDiagnostic && this.cliDiagnostic.code !== "cli_unavailable") browser.prepend(renderStatusBanner(this.cliDiagnostic));
      const refresh = element("button", { text: "刷新同步状态", attrs: { type: "button", "data-action": "refresh-v4-status" } });
      refresh.addEventListener("click", () => { void this.refresh(this.projects); });
      browser.append(refresh);
      this.contentEl.append(browser);
      return;
    }
    if (snapshot.kind === "empty" || snapshot.kind === "migration_required") {
      const diagnostic = snapshot.diagnostic;
      this.contentEl.append(element("div", { className: "session-reviewer-browser" }, [
        this.projectPicker(projects),
        element("h1", { text: "暂时无法打开项目回顾" }),
        element("p", { text: diagnostic?.message ?? "项目还没有可用快照。" })
      ]));
      return;
    }
    const model = snapshot.kind === "stale" ? snapshot.lastValid.model : snapshot.model;
    const scanAction = this.runner
      ? {
          label: scanActionLabel(this.scanStatus),
          disabled: this.scanActionInFlight || (this.scanStatus !== undefined && (this.scanStatus.state === "queued" || this.scanStatus.state === "running")),
          onStart: () => this.startScan()
        }
      : undefined;
    const browser = renderReadyView(model, { ...this.currentState, projectId: model.review.projectId }, (viewState) => {
      this.currentState = viewState;
      return this.saveState?.(viewState);
    }, this.editor ? (field) => this.openEditor(field, model) : undefined, scanAction);
    browser.prepend(this.projectPicker(projects));
    if (snapshot.kind === "pending_edit" || snapshot.kind === "stale") browser.prepend(renderStatusBanner(snapshot.diagnostic, this.actionFor(snapshot.diagnostic)));
    if (this.cliDiagnostic) browser.prepend(renderStatusBanner(this.cliDiagnostic, this.actionFor(this.cliDiagnostic)));
    if (this.announcement) browser.prepend(element("div", { className: "sr-sr-only", text: this.announcement, attrs: { "aria-live": "polite" } }));
    if (this.scanStatus) {
      const banner = renderScanJobBanner(this.scanStatus);
      if (banner) browser.prepend(banner);
    }
    this.contentEl.append(browser);
  }

  private async readCliStatus(selected: ProjectDescriptor): Promise<{ diagnostic?: Diagnostic; hiddenConflictIds: string[] }> {
    let diagnostic: Diagnostic | undefined;
    let hiddenConflictIds: string[] = [];
    if (!this.runner) {
      return { diagnostic: { code: "cli_unavailable", message: "" }, hiddenConflictIds };
    }
    try {
      const status = await this.runner.status(selected.projectId);
      if (selected.format === "markdown-v4") {
        // This status observes private sync state but carries no acceptance
        // proof for the independently loaded public snapshot.
        if (status.conflicted > 0 || status.open_conflicts.length > 0 || status.hidden_conflict_ids.length > 0) diagnostic = { code: "content_conflict", message: "请在原生 Markdown 中比较并修改双方内容，然后重新查询同步状态。" };
        else if (status.machine_state === "blocked" || status.blocked > 0 || status.malformed > 0) diagnostic = { code: "sync_status_failed", message: "" };
        else if (status.pending_operations.length > 0 || status.machine_state === "pending") diagnostic = { code: "markdown_sync_pending", message: "" };
        return { diagnostic, hiddenConflictIds };
      }
      hiddenConflictIds = Array.isArray(status.hidden_conflict_ids) ? status.hidden_conflict_ids.filter((value): value is string => typeof value === "string") : [];
      if (hiddenConflictIds.length) diagnostic = { code: "content_conflict", message: `待处理冲突 ${hiddenConflictIds.length} 个。` };
      else if (status.migration === "required") diagnostic = { code: "migration_required", message: "" };
      else if (status.machine_state === "blocked") diagnostic = { code: "machine_ledger_modified", message: "" };
    } catch (error) {
      diagnostic = { code: error instanceof SyncStatusError && error.code === "cli_unavailable" ? "cli_unavailable" : "sync_status_failed", message: "" };
    }
    return { diagnostic, hiddenConflictIds };
  }

  private actionFor(diagnostic: Diagnostic): (() => void) | undefined {
    if (diagnostic.code === "stale_snapshot") return () => { void this.refresh(this.projects); };
    if ((diagnostic.code === "history_parse_failed" || diagnostic.code === "review_parse_failed") && this.selected) {
      const file = diagnostic.code === "history_parse_failed" ? `${this.selected.root}/项目历史.md` : `${this.selected.root}/项目回顾.md`;
      return () => { void this.app.workspace.openLinkText(file, "", false); };
    }
    if (!this.runner || !this.selected) return undefined;
    if (this.selected.format === "markdown-v4") return undefined;
    if (diagnostic.code === "migration_required") return () => { void this.runCliAction(() => this.runner!.migrationDryRun(this.selected!.projectId), "迁移预览已完成。"); };
    if (diagnostic.code === "machine_ledger_modified") return () => { void this.runCliAction(() => this.runner!.repairMachineLedger(this.selected!.projectId), "机器账本已修复。"); };
    if (diagnostic.code === "content_conflict") return () => { void this.openConflict(); };
    if (diagnostic.code === "sync_not_run") return () => { void this.runCliAction(() => this.runner!.syncProject(this.selected!.projectId), "已同步到代码目录。"); };
    return undefined;
  }

  private async runCliAction(action: () => Promise<unknown>, success: string): Promise<void> {
    try {
      await action();
      this.announcement = success;
    } catch (error) {
      this.announcement = error instanceof Error ? error.message : String(error);
      new Notice(this.announcement);
    }
    await this.refresh(this.projects);
  }

  private async readScanStatus(selected: ProjectDescriptor): Promise<ScanStatus | undefined> {
    if (!this.runner) return undefined;
    try {
      return await this.runner.getScanStatus(selected.projectId);
    } catch {
      // 状态读取失败时保持既有状态
    }
  }

  private startScan(): void {
    if (!this.runner || !this.selected || this.scanActionInFlight) return;
    if (this.scanStatus && (this.scanStatus.state === "queued" || this.scanStatus.state === "running")) return;
    const projectId = this.selected.projectId;
    void this.runScanAction(() => this.runner!.startScan(projectId), "项目脉络扫描已开始。");
  }

  private async runScanAction(action: () => Promise<ScanStatus>, success: string): Promise<void> {
    if (this.scanActionInFlight) return;
    this.scanActionInFlight = true;
    this.renderSnapshot(this.lastReady ?? emptyReviewSnapshot(), this.projects);
    try {
      this.scanStatus = await action();
      this.announcement = success;
    } catch (error) {
      this.announcement = error instanceof Error ? error.message : String(error);
      new Notice(this.announcement);
    }
    this.scanActionInFlight = false;
    await this.refresh(this.projects, { scanStatus: false });
  }

  private scheduleScanPolling(): void {
    this.stopScanPolling();
    if (!this.scanStatus || (this.scanStatus.state !== "queued" && this.scanStatus.state !== "running")) return;
    void this.pollScanJob(this.scanPollGeneration, 0);
  }

  private stopScanPolling(): void {
    this.scanPollGeneration += 1;
    if (this.scanPollTimer !== undefined) {
      window.clearTimeout(this.scanPollTimer);
      this.scanPollTimer = undefined;
    }
  }

  private async pollScanJob(generation: number, round: number): Promise<void> {
    const delay = round === 0 ? 1000 : round === 1 ? 2000 : 5000;
    await new Promise<void>((resolve) => {
      this.scanPollTimer = window.setTimeout(resolve, delay);
    });
    this.scanPollTimer = undefined;
    if (generation !== this.scanPollGeneration) return;
    const previous = this.scanStatus;
    const selected = this.selected;
    if (!selected || this.closed) return;
    const current = await this.readScanStatus(selected);
    if (generation !== this.scanPollGeneration || this.closed || selected !== this.selected) return;
    this.scanStatus = current;
    if (!current) {
      void this.pollScanJob(generation, round + 1);
      return;
    }
    if (current.state !== "queued" && current.state !== "running") {
      if (previous && (previous.state === "queued" || previous.state === "running")) {
        this.announcement = scanTerminalAnnouncement(current);
      }
      await this.refresh(this.projects, { scanStatus: false });
      return;
    }
    this.renderSnapshot(this.lastReady ?? emptyReviewSnapshot(), this.projects);
    void this.pollScanJob(generation, round + 1);
  }

  private async openConflict(): Promise<void> {
    if (!this.repository || !this.runner || !this.selected || !this.hiddenConflictIds[0]) return;
    try {
      const conflict = await this.repository.loadConflict(this.selected, this.hiddenConflictIds[0]);
      const modal = new ConflictModal(this.app, conflict, (action, manual) => {
        new ConfirmModal(this.app, "确认用选定内容解决这个冲突？", () => {
          modal.close();
          void this.resolveConflict(conflict.id, action, manual);
        }).open();
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
    const wrapper = element("div", { className: "sr-project-picker" });
    wrapper.append(element("span", { text: "项目" }));
    const select = element("select", { attrs: { "aria-label": "选择项目" } });
    for (const project of projects) {
      const option = element("option", { text: `${project.name} · ${project.projectId}`, attrs: { value: project.projectId } });
      option.selected = project.projectId === this.selected?.projectId;
      select.append(option);
    }
    select.disabled = projects.length === 0;
    select.addEventListener("change", () => {
      const next = projects.find((project) => project.projectId === select.value);
      if (!next) return;
      this.stopScanPolling();
      this.scanStatus = undefined;
      this.selected = next;
      this.invalidateEventRecovery();
      this.recoveredEventSelection = undefined;
      this.currentState = { ...this.currentState, projectId: next.projectId };
      void this.saveState?.(this.currentState);
      if (next.format === "markdown-v4") {
        const current = this.loadV4State?.(next.projectId);
        if (current) this.v4States = { ...this.v4States, [next.projectId]: current };
        else if (!(next.projectId in this.v4States)) {
          this.v4States = { ...this.v4States, [next.projectId]: normalizeV4ViewState(undefined, next.projectId) };
          void this.saveV4State?.(this.v4States[next.projectId]);
        }
      }
      this.lastReady = undefined;
      this.lastMarkdownReady = undefined;
      this.disposeScanRecords();
      this.resetEventCache();
      this.watchSelectedProject();
      void this.refresh(projects);
    });
    const refresh = element("button", { text: "刷新项目", attrs: { type: "button", "data-action": "refresh-projects" } });
    refresh.addEventListener("click", () => { void this.rediscoverProjects(); });
    wrapper.append(select, refresh);
    return wrapper;
  }

  private async rediscoverProjects(): Promise<void> {
    if (!this.repository || this.closed) return;
    const previous = this.selected;
    const epoch = ++this.refreshEpoch;
    const projects = await this.repository.discover();
    if (this.closed || epoch !== this.refreshEpoch) return;
    this.projects = projects;
    if (projects.length === 0) {
      this.selected = undefined;
      this.lastReady = undefined;
      this.lastMarkdownReady = undefined;
      this.disposeWatch?.();
      this.disposeWatch = undefined;
      this.disposeScanRecords();
      this.resetEventCache();
      this.contentEl.replaceChildren(element("div", { className: "session-reviewer-browser" }, [
        this.projectPicker(projects),
        element("h1", { text: "还没有项目回顾" }),
        element("p", { text: "请先运行 SessionReviewer 扫描或同步，生成项目回顾、项目历史和机器账本。" })
      ]));
      return;
    }
    const next = projects.find((project) => project.projectId === previous?.projectId) ?? projects[0];
    const descriptorChanged = previous?.root !== next.root || previous?.format !== next.format;
    this.selected = next;
    if (descriptorChanged) {
      this.lastReady = undefined;
      this.lastMarkdownReady = undefined;
      this.resetEventCache();
    }
    this.currentState = { ...this.currentState, projectId: next.projectId };
    void this.saveState?.(this.currentState);
    this.watchSelectedProject();
    await this.refresh(projects);
  }

  private disposeScanRecords(): void {
    const browser = this.v4Browser;
    const records = this.scanRecords;
    this.v4Browser = undefined;
    this.scanRecords = undefined;
    if (browser) browser.dispose?.();
    else records?.dispose();
  }

  private currentV4State(projectId: string): V4ViewState | undefined {
    const current = this.loadV4State?.(projectId);
    if (current) this.v4States = { ...this.v4States, [projectId]: current };
    return current ?? this.v4States[projectId];
  }

  private saveV4Patch(projectId: string, patch: V4ViewStatePatch): void | Promise<void> {
    if (patch.sessionBrowser !== undefined) {
      if (this.pendingEventRecovery) this.invalidateEventRecovery();
      this.recoveredEventSelection = undefined;
    }
    const latest = normalizeV4ViewState(this.currentV4State(projectId), projectId);
    const next = normalizeV4ViewState({ ...latest, ...patch, projectId }, projectId);
    this.v4States = { ...this.v4States, [projectId]: next };
    return this.saveV4State?.(next);
  }

  private resetEventCache(): void {
    this.eventPageCache.clear();
    this.eventCacheGeneration = "";
  }

  private async refreshSessionEvents(request: { provider: string; sessionId: string; ordinal: number }): Promise<void> {
    const selected = this.selected;
    if (!selected || this.closed) return;
    const state = normalizeV4ViewState(this.currentV4State(selected.projectId), selected.projectId);
    const browserState = normalizeSessionBrowserState(state.sessionBrowser);
    const persisted = browserState.selected;
    const recovered = this.recoveredEventSelection?.projectId === selected.projectId ? this.recoveredEventSelection : undefined;
    const currentIndex = this.lastMarkdownReady?.descriptor.projectId === selected.projectId ? this.lastMarkdownReady.state.index : undefined;
    let currentSession = currentIndex?.sessions.find((entry) => entry.provider === request.provider && entry.session_id === request.sessionId);
    if (persisted === null) {
      if (recovered) {
        if (recovered.provider !== request.provider || recovered.sessionId !== request.sessionId) return;
      } else {
        const visible = currentIndex ? filterSessions(currentIndex.sessions, browserState)[0] : undefined;
        if (visible?.provider !== request.provider || visible.session_id !== request.sessionId) return;
        currentSession = visible;
      }
    } else if (persisted.provider !== request.provider || persisted.sessionId !== request.sessionId) return;
    if (!currentIndex || !currentSession) return;
    const recoveryEpoch = ++this.eventRecoveryEpoch;
    this.pendingEventRecovery = { projectId: selected.projectId, ...request, generationId: currentIndex.generation_id, sessionViewDigest: currentSession.session_view_digest };
    try {
      await this.refresh(this.projects, { eventRecoveryEpoch: recoveryEpoch });
    } catch (error) {
      if (recoveryEpoch === this.eventRecoveryEpoch) this.pendingEventRecovery = undefined;
      throw error;
    }
  }

  private eventRecoveryFor(snapshot: Extract<Snapshot, { kind: "markdown-v4" }>): {
    selection: { provider: string; sessionId: string };
    ordinal?: number;
    unavailable?: string;
  } | undefined {
    const recovery = this.pendingEventRecovery;
    if (!recovery || recovery.projectId !== snapshot.descriptor.projectId || snapshot.state.kind !== "public_valid") return undefined;
    const selection = { provider: recovery.provider, sessionId: recovery.sessionId };
    const state = normalizeV4ViewState(this.currentV4State(snapshot.descriptor.projectId), snapshot.descriptor.projectId);
    const browserState = normalizeSessionBrowserState(state.sessionBrowser);
    if (browserState.selected !== null &&
      (browserState.selected.provider !== recovery.provider || browserState.selected.sessionId !== recovery.sessionId)) {
      this.invalidateEventRecovery();
      this.recoveredEventSelection = undefined;
      return undefined;
    }
    const session = snapshot.state.index.sessions.find((entry) => entry.provider === recovery.provider && entry.session_id === recovery.sessionId);
    if (!session) return { selection, unavailable: "项目索引已更新；原 Session 已不在已验证索引中，未显示其他 Session 详情。" };
    if (!filterSessions(snapshot.state.index.sessions, browserState).includes(session)) {
      return { selection, unavailable: "项目索引已更新；原 Session 已不符合当前本地筛选，未绕过筛选显示详情。" };
    }
    if (session.indexed_event_count === 0 || session.session_view_digest === null) {
      return { selection, unavailable: "项目索引已更新；原 Session 当前没有可读的已索引事件。" };
    }
    return { selection, ordinal: Math.min(recovery.ordinal, session.indexed_event_count) };
  }

  private v4StateForRender(snapshot: Extract<Snapshot, { kind: "markdown-v4" }>): V4ViewState | undefined {
    const state = normalizeV4ViewState(this.currentV4State(snapshot.descriptor.projectId), snapshot.descriptor.projectId);
    const recovered = this.recoveredEventSelection?.projectId === snapshot.descriptor.projectId ? this.recoveredEventSelection : undefined;
    if (!recovered || snapshot.state.kind !== "public_valid") return state;
    const browserState = normalizeSessionBrowserState(state.sessionBrowser);
    if (browserState.selected !== null) {
      this.recoveredEventSelection = undefined;
      return state;
    }
    const session = snapshot.state.index.sessions.find((entry) => entry.provider === recovered.provider && entry.session_id === recovered.sessionId);
    if (!session || !filterSessions(snapshot.state.index.sessions, browserState).includes(session)) {
      this.recoveredEventSelection = undefined;
      return state;
    }
    return { ...state, sessionBrowser: { ...browserState, selected: { provider: recovered.provider, sessionId: recovered.sessionId } } };
  }

  private invalidateEventRecovery(): void {
    this.eventRecoveryEpoch += 1;
    this.pendingEventRecovery = undefined;
  }
}

function emptyReviewSnapshot(): Snapshot {
  return { kind: "empty", diagnostic: { code: "stale_snapshot", message: "" } };
}

function scanTerminalAnnouncement(status: ScanStatus): string {
  if (status.state === "completed_with_issues") {
    return `项目脉络已更新 · ${status.session_count} 个 Session · ${status.indexed_count} 已索引 · ${status.issue_count} 需检查`;
  }
  if (status.state === "completed") {
    return `项目脉络已更新 · ${status.session_count} 个 Session`;
  }
  if (status.state === "failed") {
    return status.error_message ? `更新失败：${status.error_message}` : status.error_code ? `更新失败：${status.error_code}` : "更新失败，可重试。";
  }
  return "";
}
