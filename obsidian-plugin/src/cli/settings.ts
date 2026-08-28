import { type App, Notice, PluginSettingTab, Setting } from "obsidian";
import type SessionReviewerPlugin from "../main";
import { CliRunner } from "./runner";

export class CliSettingsTab extends PluginSettingTab {
  constructor(
    app: App,
    plugin: SessionReviewerPlugin,
    private path: string,
    private readonly savePath: (path: string) => Promise<void>
  ) {
    super(app, plugin);
  }

  display(): void {
    this.containerEl.empty();
    new Setting(this.containerEl)
      .setName("SessionReviewer CLI")
      .setHeading();
    new Setting(this.containerEl)
      .setName("CLI 可执行文件")
      .setDesc("仅保存在 Obsidian 插件设置中。不会写入 Markdown 或机器账本。")
      .addText((text) => text
        .setPlaceholder("/usr/local/bin/session-reviewer")
        .setValue(this.path)
        .onChange((value) => { this.path = value.trim(); }))
      .addButton((control) => control.setButtonText("验证并保存").setCta().onClick(async () => {
        try {
          const verified = await new CliRunner(this.path).verifyExecutable();
          await this.savePath(this.path);
          new Notice(`已连接 SessionReviewer ${verified.version}`);
        } catch (error) {
          new Notice(error instanceof Error ? error.message : String(error));
        }
      }));
  }
}
