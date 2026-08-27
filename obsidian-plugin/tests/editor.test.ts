import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { ReviewEditor } from "../src/data/editor";
import { sha256Text } from "../src/data/hash";
import { parseHistory } from "../src/data/markdown";
import type { VaultFile, VaultPort } from "../src/data/vault-port";

const fixture = (name: string): Promise<string> =>
  readFile(resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/review-v2", name), "utf8");

class EditVault implements VaultPort {
  constructor(public body: string) {}
  getMarkdownFiles(): VaultFile[] { return []; }
  getFrontmatter(): Record<string, unknown> | undefined { return undefined; }
  async read(): Promise<string> { return this.body; }
  async process(_path: string, transform: (current: string) => string): Promise<void> { this.body = transform(this.body); }
  onChange(): () => void { return () => undefined; }
}

describe("review editor", () => {
  it("writes one allowed event field and rejects stale or machine fields", async () => {
    const vault = new EditVault(await fixture("项目历史.valid.md"));
    const editor = new ReviewEditor(vault);
    const baseHash = sha256Text(vault.body);
    await editor.apply({ path: "项目历史.md", expectedSha256: baseHash, document: "history", unitId: "timeline-trust-chain", field: "event.next", value: "发布补丁版本" });
    expect(parseHistory(vault.body).events[0]?.next).toBe("发布补丁版本");
    await expect(editor.apply({ path: "项目历史.md", expectedSha256: baseHash, document: "history", unitId: "timeline-trust-chain", field: "event.next", value: "旧编辑" })).rejects.toThrow(/stale edit/);
    await expect(editor.apply({ path: "项目历史.md", expectedSha256: sha256Text(vault.body), document: "history", unitId: "timeline-trust-chain", field: "evidence" as never, value: "forged" })).rejects.toThrow(/field is read-only/);
  });

  it("rejects empty required fields before touching the Vault", async () => {
    const vault = new EditVault(await fixture("项目历史.valid.md"));
    const editor = new ReviewEditor(vault);
    const before = vault.body;
    await expect(editor.apply({ path: "项目历史.md", expectedSha256: sha256Text(before), document: "history", unitId: "timeline-trust-chain", field: "event.title", value: "   " })).rejects.toThrow(/cannot be empty/);
    expect(vault.body).toBe(before);
  });
});
