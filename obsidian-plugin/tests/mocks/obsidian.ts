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
  value = "";
  onChangeHandler?: (value: string) => void;
  setPlaceholder(_value: string): this { return this; }
  setValue(value: string): this { this.value = value; return this; }
  onChange(callback: (value: string) => void): this { this.onChangeHandler = callback; return this; }
}
class ButtonControl {
  onClickHandler?: () => void;
  setButtonText(_value: string): this { return this; }
  setCta(): this { return this; }
  onClick(callback: () => void): this { this.onClickHandler = callback; return this; }
}
export class Setting {
  static instances: Setting[] = [];
  readonly textControls: TextControl[] = [];
  readonly buttonControls: ButtonControl[] = [];
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
    Setting.instances.push(this);
  }

  setName(value: string): this { this.nameEl.textContent = value; return this; }
  setDesc(value: string): this { this.descEl.textContent = value; return this; }
  setHeading(): this { this.settingEl.classList.add("mod-heading"); return this; }
  addText(callback: (control: TextControl) => void): this { const control = new TextControl(); this.textControls.push(control); callback(control); return this; }
  addButton(callback: (control: ButtonControl) => void): this { const control = new ButtonControl(); this.buttonControls.push(control); callback(control); return this; }
}
