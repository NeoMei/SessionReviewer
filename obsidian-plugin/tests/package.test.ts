import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { mkdtemp, readFile, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it } from "vitest";

const roots: string[] = [];
const repository = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

afterEach(async () => {
  await Promise.all(roots.splice(0).map((root) => rm(root, { recursive: true, force: true })));
});

describe("Obsidian plugin package", () => {
  it("publishes marketplace metadata at the repository root", async () => {
    const rootManifest = resolve(repository, "manifest.json");
    const rootVersions = resolve(repository, "versions.json");
    expect(existsSync(rootManifest)).toBe(true);
    expect(existsSync(rootVersions)).toBe(true);
    if (!existsSync(rootManifest) || !existsSync(rootVersions)) return;
    const manifest = JSON.parse(await readFile(rootManifest, "utf8")) as { description: string };
    expect(manifest).toEqual(JSON.parse(await readFile(resolve(repository, "obsidian-plugin/manifest.json"), "utf8")));
    expect(manifest.description).not.toMatch(/\bObsidian\b/i);
    expect(JSON.parse(await readFile(rootVersions, "utf8"))).toEqual(JSON.parse(await readFile(resolve(repository, "obsidian-plugin/versions.json"), "utf8")));
  });

  it.skipIf(process.platform === "win32")("packages only installable assets with matching versions reproducibly", async () => {
    const first = await mkdtemp(join(tmpdir(), "sr-plugin-one-"));
    const second = await mkdtemp(join(tmpdir(), "sr-plugin-two-"));
    roots.push(first, second);
    const build = (dist: string): void => {
      execFileSync("bash", ["scripts/build-obsidian-plugin.sh", "0.2.8", dist], {
        cwd: repository,
        env: { ...process.env, SESSION_REVIEWER_PACKAGE_SKIP_CHECK: "1", SOURCE_DATE_EPOCH: "315532800" },
        stdio: "pipe"
      });
    };
    build(first);
    build(second);
    expect((await readdir(first)).sort()).toEqual(["SHA256SUMS", "main.js", "manifest.json", "session-reviewer-obsidian-0.2.8.zip", "styles.css"]);
    const archiveName = "session-reviewer-obsidian-0.2.8.zip";
    const firstArchive = join(first, archiveName);
    const entries = execFileSync("unzip", ["-Z1", firstArchive], { encoding: "utf8" }).trim().split("\n").sort();
    expect(entries).toEqual(["session-reviewer/main.js", "session-reviewer/manifest.json", "session-reviewer/styles.css"]);
    const manifest = JSON.parse(execFileSync("unzip", ["-p", firstArchive, "session-reviewer/manifest.json"], { encoding: "utf8" })) as Record<string, unknown>;
    expect(manifest).toMatchObject({ id: "session-reviewer", version: "0.2.8" });
    const mainJs = execFileSync("unzip", ["-p", firstArchive, "session-reviewer/main.js"], { encoding: "utf8" });
    expect(mainJs).not.toContain("sourceMappingURL=data:");
    expect(mainJs).toContain("E_APPLY_RECOVERY");
    expect(mainJs).toContain('"review","start"');
    expect(mainJs).not.toMatch(/\/Users\/|AppData|private_error/);
    expect(await readFile(firstArchive)).toEqual(await readFile(join(second, archiveName)));
    expect(await readFile(join(first, "SHA256SUMS"), "utf8")).toEqual(await readFile(join(second, "SHA256SUMS"), "utf8"));
  }, 30_000);
});
