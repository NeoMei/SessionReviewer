import { type App, Notice, PluginSettingTab, Setting } from "obsidian";
import type SessionReviewerPlugin from "../main";
import { CliRunner } from "./runner";

type RunnerFactory = (executable: string) => Pick<CliRunner, "verifyExecutable" | "verifyAgent">;

const defaultRunnerFactory: RunnerFactory = (executable) => new CliRunner(executable);

export class CliSettingsTab extends PluginSettingTab {
  constructor(
    app: App,
    plugin: SessionReviewerPlugin,
    private cliPath: string,
    private readonly saveCliPath: (path: string) => Promise<void>,
    private codexPath: string,
    private readonly saveCodexPath: (path: string) => Promise<void>,
    private readonly createRunner: RunnerFactory = defaultRunnerFactory
  ) {
    super(app, plugin);
  }

  display(): void {
    this.containerEl.empty();
    new Setting(this.containerEl)
      .setName("CLI")
      .setHeading();
    new Setting(this.containerEl)
      .setName("CLI 可执行文件")
      .setDesc("仅保存在 Obsidian 插件设置中。不会写入 Markdown 或机器账本。")
      .addText((text) => text
        .setPlaceholder("/usr/local/bin/session-reviewer")
        .setValue(this.cliPath)
        .onChange((value) => { this.cliPath = value.trim(); }))
      .addButton((control) => control.setButtonText("验证并保存").setCta().onClick(async () => {
        try {
          const verified = await this.createRunner(this.cliPath).verifyExecutable();
          await this.saveCliPath(this.cliPath);
          new Notice(`已连接 SessionReviewer ${verified.version}`);
        } catch (error) {
          new Notice(error instanceof Error ? error.message : String(error));
        }
      }));
    new Setting(this.containerEl)
      .setName("Codex 可执行文件")
      .setDesc("“总结并同步”使用的 Codex 命令行。需为绝对路径，仅保存在插件设置中，验证通过后才会保存。")
      .addText((text) => text
        .setPlaceholder("/usr/local/bin/codex")
        .setValue(this.codexPath)
        .onChange((value) => { this.codexPath = value.trim(); }))
      .addButton((control) => control.setButtonText("验证并保存").setCta().onClick(async () => {
        try {
          const verified = await this.createRunner(this.codexPath).verifyAgent(this.codexPath);
          if (!verified.compatible) {
            new Notice("当前 Codex 版本暂不兼容自动总结。");
            return;
          }
          await this.saveCodexPath(this.codexPath);
          new Notice(verified.version ? `Codex ${verified.version} 已就绪` : "Codex 已就绪");
        } catch (error) {
          new Notice(error instanceof Error ? error.message : String(error));
        }
      }));
  }
}
