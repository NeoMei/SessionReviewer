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
