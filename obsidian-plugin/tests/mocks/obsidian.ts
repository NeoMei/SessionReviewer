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
