export interface VaultFile {
  path: string;
  basename: string;
}

export interface VaultPort {
  getMarkdownFiles(): VaultFile[];
  getFrontmatter(path: string): Record<string, unknown> | undefined;
  read(path: string): Promise<string>;
  process(path: string, transform: (current: string) => string): Promise<void>;
  onChange(listener: (path: string) => void): () => void;
}

export class ObsidianVaultPort implements VaultPort {
  constructor(private readonly app: App) {}

  getMarkdownFiles(): VaultFile[] {
    return this.app.vault.getMarkdownFiles().map((file) => ({ path: file.path, basename: file.basename }));
  }

  getFrontmatter(path: string): Record<string, unknown> | undefined {
    const file = this.app.vault.getAbstractFileByPath(normalizePath(path));
    if (!file || !isFile(file)) return undefined;
    return this.app.metadataCache.getFileCache(file)?.frontmatter;
  }

  read(path: string): Promise<string> {
    return this.app.vault.adapter.read(normalizePath(path));
  }

  async process(path: string, transform: (current: string) => string): Promise<void> {
    const file = this.app.vault.getAbstractFileByPath(normalizePath(path));
    if (!file || !isFile(file)) throw new Error(`Vault file does not exist: ${path}`);
    await this.app.vault.process(file, transform);
  }

  onChange(listener: (path: string) => void): () => void {
    const refs: EventRef[] = [
      this.app.vault.on("create", (file) => listener(file.path)),
      this.app.vault.on("modify", (file) => listener(file.path)),
      this.app.vault.on("delete", (file) => listener(file.path)),
      this.app.vault.on("rename", (file, oldPath) => { listener(oldPath); listener(file.path); })
    ];
    return () => { for (const ref of refs) this.app.vault.offref(ref); };
  }
}

function isFile(value: unknown): value is TFile {
  return typeof value === "object" && value !== null && "extension" in value;
}
import { type App, type EventRef, normalizePath, type TFile } from "obsidian";
