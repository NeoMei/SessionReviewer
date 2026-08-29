import { type App, Notice, PluginSettingTab, Setting, type SettingDefinitionItem } from "obsidian";
import type SessionReviewerPlugin from "../main";
import { CliRunner } from "./runner";

type RunnerFactory = (executable: string) => Pick<CliRunner, "verifyExecutable" | "verifyAgent">;

const defaultRunnerFactory: RunnerFactory = (executable) => new CliRunner(executable);

const TEXT = {
  heading: "CLI",
  cliName: "CLI 可执行文件",
  cliDesc: "仅保存在 Obsidian 插件设置中。不会写入 Markdown 或机器账本。",
  cliPlaceholder: "/usr/local/bin/session-reviewer",
  cliMissingPath: "请先填写 CLI 可执行文件的绝对路径。",
  codexName: "Codex 可执行文件",
  codexDesc: "“总结并同步”使用的命令行。需为绝对路径，仅保存在插件设置中，验证通过后才会保存。",
  codexPlaceholder: "/usr/local/bin/codex",
  codexMissingCli: "尚未配置 CLI，请先在上方验证并保存。",
  codexMissingPath: "请先填写可执行文件的绝对路径。",
  codexIncompatible: "当前版本暂不兼容自动总结。"
} as const;

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

  getSettingDefinitions(): SettingDefinitionItem[] {
    return [
      { name: TEXT.heading, render: (setting) => { setting.setName(TEXT.heading).setHeading(); } },
      { name: TEXT.cliName, desc: TEXT.cliDesc, render: (setting) => { this.configureCliSetting(setting); } },
      { name: TEXT.codexName, desc: TEXT.codexDesc, render: (setting) => { this.configureCodexSetting(setting); } }
    ];
  }

  display(): void {
    this.containerEl.empty();
    new Setting(this.containerEl).setName(TEXT.heading).setHeading();
    this.configureCliSetting(new Setting(this.containerEl));
    this.configureCodexSetting(new Setting(this.containerEl));
  }

  private configureCliSetting(setting: Setting): void {
    setting
      .setName(TEXT.cliName)
      .setDesc(TEXT.cliDesc)
      .addText((text) => text
        .setPlaceholder(TEXT.cliPlaceholder)
        .setValue(this.cliPath)
        .onChange((value) => { this.cliPath = value.trim(); }))
      .addButton((control) => control.setButtonText("验证并保存").setCta().onClick(async () => {
        try {
          if (!this.cliPath) {
            new Notice(TEXT.cliMissingPath);
            return;
          }
          const verified = await this.createRunner(this.cliPath).verifyExecutable();
          await this.saveCliPath(this.cliPath);
          new Notice("已连接 SessionReviewer " + verified.version);
        } catch (error) {
          new Notice(error instanceof Error ? error.message : String(error));
        }
      }));
  }

  private configureCodexSetting(setting: Setting): void {
    setting
      .setName(TEXT.codexName)
      .setDesc(TEXT.codexDesc)
      .addText((text) => text
        .setPlaceholder(TEXT.codexPlaceholder)
        .setValue(this.codexPath)
        .onChange((value) => { this.codexPath = value.trim(); }))
      .addButton((control) => control.setButtonText("验证并保存").setCta().onClick(async () => {
        try {
          if (!this.cliPath) {
            new Notice(TEXT.codexMissingCli);
            return;
          }
          if (!this.codexPath) {
            new Notice(TEXT.codexMissingPath);
            return;
          }
          const verified = await this.createRunner(this.cliPath).verifyAgent(this.codexPath);
          if (!verified.compatible) {
            new Notice(TEXT.codexIncompatible);
            return;
          }
          await this.saveCodexPath(this.codexPath);
          new Notice(verified.version ? "Codex " + verified.version + " 已就绪" : "Codex 已就绪");
        } catch (error) {
          new Notice(error instanceof Error ? error.message : String(error));
        }
      }));
  }
}
