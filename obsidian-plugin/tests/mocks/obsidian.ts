export class WorkspaceLeaf {
  async setViewState(_state: unknown): Promise<void> {}
}

export class Plugin {
  app: {
    workspace: {
      getLeaf: (_kind: string) => WorkspaceLeaf;
      revealLeaf: (_leaf: WorkspaceLeaf) => void;
    };
  };

  constructor(app: Plugin["app"], _manifest: unknown) {
    this.app = app;
  }

  registerView(_type: string, _creator: (leaf: WorkspaceLeaf) => ItemView): void {}
  addCommand(_command: unknown): void {}
  addSettingTab(_tab: unknown): void {}
  registerEvent(_event: unknown): void {}
  async loadData(): Promise<unknown> { return null; }
  async saveData(_data: unknown): Promise<void> {}
}

export class ItemView {
  contentEl = document.createElement("div");
  app = {} as never;

  constructor(public leaf: WorkspaceLeaf) {}
}

export class Modal {
  contentEl = document.createElement("div");
  constructor(_app?: unknown) {}
  open(): void { (this as { onOpen?: () => void }).onOpen?.(); }
  close(): void { (this as { onClose?: () => void }).onClose?.(); }
}

export class Notice {
  constructor(_message: string) {}
}

export function normalizePath(path: string): string { return path.replaceAll("\\", "/"); }

export class PluginSettingTab {
  containerEl: HTMLElement & { empty: () => void; createEl: (tag: string, options?: { text?: string }) => HTMLElement };
  constructor(_app?: unknown, _plugin?: unknown) {
    const node = document.createElement("div");
    this.containerEl = Object.assign(node, {
      empty: () => node.replaceChildren(),
      createEl: (tag: string, options?: { text?: string }) => { const child = document.createElement(tag); child.textContent = options?.text ?? ""; node.append(child); return child; }
    });
  }
}

class TextControl {
  setPlaceholder(_value: string): this { return this; }
  setValue(_value: string): this { return this; }
  onChange(_callback: (value: string) => void): this { return this; }
}
class ButtonControl {
  setButtonText(_value: string): this { return this; }
  setCta(): this { return this; }
  onClick(_callback: () => void): this { return this; }
}
export class Setting {
  private readonly settingEl: HTMLElement;
  private readonly nameEl: HTMLElement;
  private readonly descEl: HTMLElement;

  constructor(container: HTMLElement) {
    this.settingEl = document.createElement("div");
    this.settingEl.className = "setting-item";
    this.nameEl = document.createElement("div");
    this.nameEl.className = "setting-item-name";
    this.descEl = document.createElement("div");
    this.descEl.className = "setting-item-description";
    this.settingEl.append(this.nameEl, this.descEl);
    container.append(this.settingEl);
  }

  setName(value: string): this { this.nameEl.textContent = value; return this; }
  setDesc(value: string): this { this.descEl.textContent = value; return this; }
  setHeading(): this { this.settingEl.classList.add("mod-heading"); return this; }
  addText(callback: (control: TextControl) => void): this { callback(new TextControl()); return this; }
  addButton(callback: (control: ButtonControl) => void): this { callback(new ButtonControl()); return this; }
}
