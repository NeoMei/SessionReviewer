import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { cp, mkdtemp, mkdir, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { afterEach, test } from "node:test";

import { expectedAssetNames, parseChecksums, publishVerifiedRelease } from "./publish-release.mjs";

const version = "0.4.3";
const commit = "1".repeat(40);
const payloadNames = [
  "session-reviewer_0.4.3_darwin_amd64.tar.gz",
  "session-reviewer_0.4.3_darwin_arm64.tar.gz",
  "session-reviewer_0.4.3_windows_amd64.zip",
  "session-reviewer-obsidian-0.4.3.zip",
  "main.js",
  "manifest.json",
  "styles.css"
];
const assetNames = [...payloadNames, "SHA256SUMS"];
const roots = [];

afterEach(async () => {
  await Promise.all(roots.splice(0).map((root) => rm(root, { recursive: true, force: true })));
});

test("expectedAssetNames returns the exact release set and rejects non-plain versions", () => {
  assert.deepEqual(expectedAssetNames(version), assetNames);
  for (const invalid of ["v0.4.3", "0.4", "0.4.3-beta.1", "01.4.3", "0.4.3/extra", ""])
    assert.throws(() => expectedAssetNames(invalid), /version/i, invalid);
});

test("parseChecksums accepts exactly seven unique safe payload records", () => {
  const text = payloadNames.map((name, index) => `${String(index).repeat(64)}  ${name}`).join("\n") + "\n";
  assert.deepEqual([...parseChecksums(text, payloadNames)], payloadNames.map((name, index) => [name, String(index).repeat(64)]));
});

test("parseChecksums rejects unsafe, unknown, missing, duplicate, and malformed records", () => {
  const digest = "a".repeat(64);
  const valid = payloadNames.map((name) => `${digest}  ${name}`);
  const cases = [
    [`${digest}  ../outside\n`, /unsafe/i],
    [`${digest}  /tmp/outside\n`, /unsafe/i],
    [[...valid.slice(0, -1), `${digest}  unknown.bin`].join("\n") + "\n", /unknown/i],
    [valid.slice(0, -1).join("\n") + "\n", /missing/i],
    [[...valid, valid[0]].join("\n") + "\n", /duplicate/i],
    [[...valid.slice(0, -1), `${"A".repeat(64)}  ${payloadNames.at(-1)}`].join("\n") + "\n", /malformed/i],
    [[...valid.slice(0, -1), `${"a".repeat(63)}  ${payloadNames.at(-1)}`].join("\n") + "\n", /malformed/i],
    [valid.join("\n") + "\ntrailing text\n", /malformed/i]
  ];
  for (const [text, pattern] of cases) assert.throws(() => parseChecksums(text, payloadNames), pattern);
});

test("publishVerifiedRelease promotes once only after byte, manifest, provenance, and repeated ref verification", async () => {
  const fixture = await releaseFixture({ releaseMode: "draft", draftAssets: [] });
  await publishVerifiedRelease(fixture.request, fixture.io);

  const edits = fixture.mutations("release edit");
  assert.equal(edits.length, 1);
  assert.deepEqual(edits[0].args.slice(-5), ["--draft=false", "--prerelease=false", "--latest", "--verify-tag", "--repo=NeoMei/SessionReviewer"]);
  const editIndex = fixture.calls.indexOf(edits[0]);
  assert.equal(fixture.calls.slice(0, editIndex).filter((call) => call.key === "attestation verify" && !call.args.includes("--help")).length, 6);
  for (const call of fixture.calls.slice(0, editIndex).filter((candidate) => candidate.key === "attestation verify" && !candidate.args.includes("--help"))) {
    assert.ok(call.args.includes("--repo") && call.args.includes("NeoMei/SessionReviewer"));
    assert.ok(call.args.includes("--source-digest") && call.args.includes(commit));
    assert.ok(call.args.includes("--source-ref") && call.args.includes("refs/tags/0.4.3"));
    assert.ok(call.args.includes("--signer-workflow") && call.args.includes("NeoMei/SessionReviewer/.github/workflows/ci.yml"));
  }
  assert.equal(fixture.calls.slice(0, editIndex).filter((call) => call.key === "git remote commit").length, 4);
  assert.match(fixture.summary.join("\n"), new RegExp(`HEAD.*${commit}`));
  assert.match(fixture.summary.join("\n"), new RegExp(`remote default branch tip.*${commit}`));
});

test("mainline identity failures never create, upload, or publish a release", async () => {
  const cases = [
    [{ defaultBranchError: typedError(500, "API unavailable") }, /default branch/i],
    [{ localTag: "2".repeat(40) }, /local tag/i],
    [{ branchTips: ["2".repeat(40)] }, /default branch tip/i],
    [{ remoteTags: ["2".repeat(40)] }, /remote tag/i],
    [{ branchTips: [commit, "2".repeat(40)] }, /before promotion.*default branch tip/i],
    [{ remoteTags: [commit, "2".repeat(40)] }, /before promotion.*remote tag/i]
  ];
  for (const [options, pattern] of cases) {
    const fixture = await releaseFixture(options);
    await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), pattern);
    if ((options.branchTips?.length ?? 0) > 1 || (options.remoteTags?.length ?? 0) > 1) {
      assert.equal(fixture.mutations("release edit").length, 0);
      assert.equal(fixture.state.release.isDraft, true);
    } else {
      assert.deepEqual(fixture.mutations(), []);
    }
    if (!options.defaultBranchError) {
      assert.match(fixture.summary.join("\n"), /HEAD.*[0-9a-f]{40}/);
      assert.match(fixture.summary.join("\n"), /remote tag.*[0-9a-f]{40}/);
    }
  }
});

test("a non-404 release lookup failure is not treated as absence", async () => {
  const fixture = await releaseFixture({ releaseLookupError: typedError(503, "service unavailable") });
  await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), /inspect published release/i);
  assert.deepEqual(fixture.mutations(), []);
});

test("the release lookup uses the API so draft releases can be resumed", async () => {
  const fixture = await releaseFixture({ releaseMode: "draft", draftAssets: assetNames });
  await publishVerifiedRelease(fixture.request, fixture.io);
  const lookups = fixture.calls.filter((call) => call.file === "gh" && call.args[0] === "api");
  assert.ok(lookups.some((call) => call.args[1] === "repos/NeoMei/SessionReviewer/releases/tags/0.4.3"));
  assert.ok(lookups.some((call) => call.args[1] === "graphql"));
  assert.ok(lookups.some((call) => call.args[1] === "repos/NeoMei/SessionReviewer/releases/123"));
  assert.equal(fixture.mutations("release create").length, 0);
});

test("draft upload interruption remains resumable without replacing an existing asset", async () => {
  const fixture = await releaseFixture({ uploadFailures: 1 });
  await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), /upload interrupted/i);
  assert.equal(fixture.state.release.isDraft, true);
  assert.equal(fixture.mutations("release edit").length, 0);

  await publishVerifiedRelease(fixture.request, fixture.io);
  assert.equal(fixture.mutations("release create").length, 1);
  assert.equal(fixture.mutations("release edit").length, 1);
  assert.equal(fixture.calls.some((call) => call.args.includes("--clobber")), false);
});

test("drafts with unknown or conflicting assets require human intervention", async () => {
  for (const options of [
    { releaseMode: "draft", draftAssets: ["unexpected.bin"] },
    { releaseMode: "draft", draftAssets: ["main.js"], corruptRemote: "main.js" }
  ]) {
    const fixture = await releaseFixture(options);
    await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), /unknown asset|conflicting asset/i);
    assert.equal(fixture.mutations("release upload").length, 0);
    assert.equal(fixture.mutations("release edit").length, 0);
  }
});

test("downloaded release shape and bytes are independently verified before promotion", async () => {
  const cases = [
    [{ omitAfterUpload: "styles.css" }, /missing asset/i],
    [{ extraAfterUpload: "surprise.bin" }, /unknown asset/i],
    [{ corruptAfterUpload: "main.js" }, /checksum mismatch/i]
  ];
  for (const [options, pattern] of cases) {
    const fixture = await releaseFixture(options);
    await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), pattern);
    assert.equal(fixture.mutations("release edit").length, 0);
  }
});

test("local and downloaded manifests require the release version, plugin id, minimum, and semantic equality", async () => {
  for (const manifest of [
    { id: "session-reviewer", version: "0.4.2", minAppVersion: "1.8.7" },
    { id: "another-plugin", version, minAppVersion: "1.8.7" },
    { id: "session-reviewer", version, minAppVersion: "" }
  ]) {
    const fixture = await releaseFixture({ manifest });
    await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), /manifest/i);
    assert.deepEqual(fixture.mutations(), []);
  }

  const fixture = await releaseFixture({ remoteManifestAfterDownload: { id: "session-reviewer", version, minAppVersion: "1.9.0" } });
  await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), /checksum mismatch|manifest/i);
  assert.equal(fixture.mutations("release edit").length, 0);
});

test("symlinks and nonregular files are rejected in local and downloaded asset sets", async () => {
  const local = await releaseFixture();
  await rm(join(local.dist, "styles.css"));
  await symlink(join(local.dist, "main.js"), join(local.dist, "styles.css"));
  await assert.rejects(publishVerifiedRelease(local.request, local.io), /regular file/i);
  assert.deepEqual(local.mutations(), []);

  const downloaded = await releaseFixture({ downloadedSymlink: "styles.css" });
  await assert.rejects(publishVerifiedRelease(downloaded.request, downloaded.io), /regular file/i);
  assert.equal(downloaded.mutations("release edit").length, 0);
});

test("attestation rejection and missing gh policy flags leave the release as a draft", async () => {
  for (const options of [{ attestationFailure: "manifest.json" }, { missingHelpFlag: "--source-ref" }]) {
    const fixture = await releaseFixture(options);
    await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), /attestation|required gh flag/i);
    assert.equal(fixture.mutations("release edit").length, 0);
  }
});

test("an existing exact published release is verified without mutation, while mismatches fail read-only", async () => {
  const exact = await releaseFixture({ releaseMode: "published", draftAssets: assetNames });
  await publishVerifiedRelease(exact.request, exact.io);
  assert.deepEqual(exact.mutations(), []);
  assert.equal(exact.calls.filter((call) => call.key === "attestation verify" && !call.args.includes("--help")).length, 3);

  const mismatch = await releaseFixture({ releaseMode: "published", draftAssets: assetNames, corruptRemote: "main.js" });
  await assert.rejects(publishVerifiedRelease(mismatch.request, mismatch.io), /checksum mismatch|conflicting asset/i);
  assert.deepEqual(mismatch.mutations(), []);
});

test("an uncertain promotion response is reconciled read-only", async () => {
  const fixture = await releaseFixture({ uncertainPromotion: true });
  await publishVerifiedRelease(fixture.request, fixture.io);
  assert.equal(fixture.mutations("release edit").length, 1);
  assert.equal(fixture.state.release.isDraft, false);
});

test("a draft mutation after verification is caught by the final metadata recheck", async () => {
  const fixture = await releaseFixture({ releaseMode: "draft", draftAssets: [], mutateAfterFinalRefs: "unknown-asset" });
  await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), /unknown asset/i);
  assert.equal(fixture.mutations("release edit").length, 0);
  assert.equal(fixture.state.release.isDraft, true);
});

test("a same-name asset replacement after verification is caught before promotion", async () => {
  const fixture = await releaseFixture({ releaseMode: "draft", draftAssets: [], mutateAfterFinalRefs: "replace-main" });
  await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), /checksum mismatch/i);
  assert.equal(fixture.mutations("release edit").length, 0);
  assert.equal(fixture.state.release.isDraft, true);
});

test("post-promotion reconciliation repeats full byte verification", async () => {
  const fixture = await releaseFixture({ corruptAfterPromotion: "main.js" });
  await assert.rejects(publishVerifiedRelease(fixture.request, fixture.io), /checksum mismatch/i);
  assert.equal(fixture.mutations("release edit").length, 1);
  assert.equal(fixture.state.release.isDraft, false);
});

async function releaseFixture(options = {}) {
  const root = await mkdtemp(join(tmpdir(), "sr-publish-test-"));
  roots.push(root);
  const dist = join(root, "dist");
  const remote = join(root, "remote");
  await mkdir(dist);
  await mkdir(remote);
  await writeReleaseFiles(dist, options.manifest);
  await cp(dist, remote, { recursive: true });
  if (options.corruptRemote) await writeFile(join(remote, options.corruptRemote), "conflicting bytes");

  const initialNames = options.releaseMode ? [...(options.draftAssets ?? assetNames)] : [];
  const state = {
    release: options.releaseMode ? releaseJson(options.releaseMode === "draft", initialNames) : null,
    branchTips: [...(options.branchTips ?? [commit, commit])],
    remoteTags: [...(options.remoteTags ?? [commit, commit])],
    fetchKind: "",
    uploadFailures: options.uploadFailures ?? 0,
    downloadRound: 0,
    remoteCommitCount: 0
  };
  const calls = [];
  const summary = [];

  const io = {
    summary: async (line) => summary.push(line),
    exec: async (file, args) => {
      const call = { file, args: [...args], key: commandKey(file, args) };
      calls.push(call);
      if (file === "gh" && args[0] === "version") return "gh version 2.97.0 (test)\n";
      if (file === "gh" && args.at(-1) === "--help") {
        const help = args.slice(0, -1).join(" ") === "release create"
          ? "--draft --verify-tag --target"
          : args.slice(0, -1).join(" ") === "release edit"
            ? "--draft --prerelease --latest --verify-tag"
            : "--source-digest --source-ref --signer-workflow --repo";
        return options.missingHelpFlag ? help.replace(options.missingHelpFlag, "") : help;
      }
      if (file === "gh" && args[0] === "repo" && args[1] === "view") {
        if (options.defaultBranchError) throw options.defaultBranchError;
        return `${options.defaultBranch ?? "main"}\n`;
      }
      if (file === "git" && args[0] === "rev-parse" && args[1] === "HEAD") return `${options.localHead ?? commit}\n`;
      if (file === "git" && args[0] === "rev-parse" && args[1]?.startsWith("refs/tags/")) return `${options.localTag ?? commit}\n`;
      if (file === "git" && args[0] === "fetch") {
        state.fetchKind = args.at(-1).startsWith("refs/heads/") ? "branch" : "tag";
        return "";
      }
      if (file === "git" && args[0] === "rev-parse" && args[1] === "FETCH_HEAD^{commit}") {
        call.key = "git remote commit";
        state.remoteCommitCount++;
        if (state.remoteCommitCount === 4 && options.mutateAfterFinalRefs === "unknown-asset")
          state.release.assets.push({ name: "surprise.bin", state: "uploaded", size: 1 });
        if (state.remoteCommitCount === 4 && options.mutateAfterFinalRefs === "replace-main")
          await writeFile(join(remote, "main.js"), "same-name replacement");
        const queue = state.fetchKind === "branch" ? state.branchTips : state.remoteTags;
        return `${queue.length > 1 ? queue.shift() : queue[0]}\n`;
      }
      if (file === "gh" && args[0] === "api") {
        if (args[1] === "graphql") return state.release?.isDraft ? "123\n" : "\n";
        if (args[1] === "repos/NeoMei/SessionReviewer/releases/123") {
          if (!state.release?.isDraft) throw typedError(404, "HTTP 404: draft release not found");
          return JSON.stringify(state.release);
        }
        if (options.releaseLookupError) throw options.releaseLookupError;
        if (!state.release || state.release.isDraft) throw typedError(404, "HTTP 404: published release not found");
        return JSON.stringify(state.release);
      }
      if (file === "gh" && args[0] === "release" && args[1] === "create") {
        state.release = releaseJson(true, []);
        return "";
      }
      if (file === "gh" && args[0] === "release" && args[1] === "upload") {
        const paths = args.slice(3).filter((arg) => !arg.startsWith("--"));
        const alreadyUploaded = [];
        for (const path of paths) {
          await cp(path, join(remote, basename(path)));
          alreadyUploaded.push(basename(path));
          if (state.uploadFailures > 0) {
            state.release.assets.push({ name: basename(path), state: "uploaded", size: (await readFile(path)).length });
            state.uploadFailures--;
            throw new Error("upload interrupted");
          }
        }
        state.release.assets.push(...alreadyUploaded.map((name) => ({ name, state: "uploaded", size: 1 })));
        if (options.omitAfterUpload) state.release.assets = state.release.assets.filter((asset) => asset.name !== options.omitAfterUpload);
        if (options.extraAfterUpload) state.release.assets.push({ name: options.extraAfterUpload, state: "uploaded", size: 1 });
        if (options.corruptAfterUpload) await writeFile(join(remote, options.corruptAfterUpload), "download corruption");
        return "";
      }
      if (file === "gh" && args[0] === "release" && args[1] === "download") {
        state.downloadRound++;
        const destination = args[args.indexOf("--dir") + 1];
        const patternIndex = args.indexOf("--pattern");
        const names = patternIndex >= 0 ? [args[patternIndex + 1]] : state.release.assets.map((asset) => asset.name);
        for (const name of names) {
          const target = join(destination, name);
          if (options.downloadedSymlink === name && patternIndex < 0) {
            await symlink(join(remote, "main.js"), target);
          } else {
            await cp(join(remote, name), target);
          }
        }
        if (options.remoteManifestAfterDownload && patternIndex < 0)
          await writeFile(join(destination, "manifest.json"), JSON.stringify(options.remoteManifestAfterDownload));
        return "";
      }
      if (file === "gh" && args[0] === "attestation" && args[1] === "verify") {
        if (basename(args[2]) === options.attestationFailure) throw new Error("attestation rejected");
        return "verified\n";
      }
      if (file === "gh" && args[0] === "release" && args[1] === "edit") {
        state.release.isDraft = false;
        state.release.isPrerelease = false;
        if (options.corruptAfterPromotion) await writeFile(join(remote, options.corruptAfterPromotion), "post-promotion corruption");
        if (options.uncertainPromotion) throw new Error("connection reset after request");
        return "";
      }
      throw new Error(`unexpected command: ${file} ${args.join(" ")}`);
    }
  };

  return {
    calls,
    dist,
    io,
    remote,
    request: { repo: "NeoMei/SessionReviewer", version, commit, dist },
    state,
    summary,
    mutations: (key) => calls.filter((call) => ["release create", "release upload", "release edit"].includes(call.key) && !call.args.includes("--help") && (!key || call.key === key))
  };
}

async function writeReleaseFiles(dist, manifest = { id: "session-reviewer", version, minAppVersion: "1.8.7", isDesktopOnly: true }) {
  for (const name of payloadNames) {
    const body = name === "manifest.json" ? JSON.stringify(manifest) : `payload:${name}\n`;
    await writeFile(join(dist, name), body);
  }
  const lines = [];
  for (const name of payloadNames) lines.push(`${createHash("sha256").update(await readFile(join(dist, name))).digest("hex")}  ${name}`);
  await writeFile(join(dist, "SHA256SUMS"), lines.join("\n") + "\n");
}

function releaseJson(isDraft, names) {
  return {
    tagName: version,
    isDraft,
    isPrerelease: false,
    targetCommitish: commit,
    assets: names.map((name) => ({ name, state: "uploaded", size: 1 }))
  };
}

function typedError(statusCode, message) {
  return Object.assign(new Error(message), { statusCode });
}

function commandKey(file, args) {
  if (file === "gh" && args[0] === "release") return `release ${args[1]}`;
  if (file === "gh" && args[0] === "attestation") return "attestation verify";
  return `${file} ${args.slice(0, 2).join(" ")}`;
}
