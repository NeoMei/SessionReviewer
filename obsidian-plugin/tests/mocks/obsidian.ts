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
}

export class ItemView {
  contentEl = document.createElement("div");

  constructor(public leaf: WorkspaceLeaf) {}
}

export class Modal {
  contentEl = document.createElement("div");
}

export class Notice {
  constructor(_message: string) {}
}
