#!/usr/bin/env node

import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { lstat, mkdtemp, readFile, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, isAbsolute, join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const VERSION_RE = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const SHA_RE = /^[0-9a-f]{64}$/;
const COMMIT_RE = /^[0-9a-f]{40}$/;
const ATTESTED_NAMES = ["main.js", "manifest.json", "styles.css"];
const RELEASE_JQ = "{tagName:.tag_name,isDraft:.draft,isPrerelease:.prerelease,targetCommitish:.target_commitish,assets:[.assets[]|{name,state,size}]}";
const DRAFT_RELEASE_QUERY = "query($owner:String!,$name:String!,$tagName:String!){repository(owner:$owner,name:$name){release(tagName:$tagName){databaseId isDraft}}}";

export function expectedAssetNames(version) {
  validateVersion(version);
  return [
    `session-reviewer_${version}_darwin_amd64.tar.gz`,
    `session-reviewer_${version}_darwin_arm64.tar.gz`,
    `session-reviewer_${version}_windows_amd64.zip`,
    `session-reviewer-obsidian-${version}.zip`,
    "main.js",
    "manifest.json",
    "styles.css",
    "SHA256SUMS"
  ];
}

export function parseChecksums(text, expectedPayloadNames) {
  if (typeof text !== "string") throw new Error("checksum manifest must be text");
  const expected = new Set(expectedPayloadNames);
  if (expected.size !== expectedPayloadNames.length) throw new Error("expected checksum names must be unique");
  const result = new Map();
  const lines = text.endsWith("\n") ? text.slice(0, -1).split("\n") : text.split("\n");
  for (const line of lines) {
    const match = /^([0-9a-f]{64})  (.+)$/.exec(line);
    if (!match) throw new Error(`malformed checksum line: ${line}`);
    const [, digest, name] = match;
    if (!isSafeAssetName(name)) throw new Error(`unsafe checksum filename: ${name}`);
    if (!SHA_RE.test(digest)) throw new Error(`malformed checksum digest for ${name}`);
    if (!expected.has(name)) throw new Error(`unknown checksum entry: ${name}`);
    if (result.has(name)) throw new Error(`duplicate checksum entry: ${name}`);
    result.set(name, digest);
  }
  const missing = expectedPayloadNames.filter((name) => !result.has(name));
  if (missing.length) throw new Error(`missing checksum entries: ${missing.join(", ")}`);
  return result;
}

export async function publishVerifiedRelease(request, providedIo = productionIo()) {
  const { repo, version, commit, dist } = validateRequest(request);
  const io = normalizeIo(providedIo);
  const allNames = expectedAssetNames(version);
  const payloadNames = allNames.slice(0, -1);
  const absoluteDist = resolve(dist);

  const local = await inspectAssetDirectory(absoluteDist, allNames, payloadNames, version);
  await verifyGhCapabilities(io);
  const ghVersion = (await io.exec("gh", ["version"])).trim().split("\n")[0];
  await io.summary(`gh: ${ghVersion}`);
  const defaultBranch = (await runChecked(io, "gh", ["repo", "view", repo, "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name"], "inspect repository default branch")).trim();
  if (!isSafeRefName(defaultBranch)) throw new Error("invalid repository default branch");

  await verifyRefs(io, { repo, version, commit, defaultBranch, phase: "initial" });

  let release = await inspectRelease(io, repo, version);
  if (release && !release.isDraft) {
    await verifyPublishedRelease(io, { repo, version, commit, defaultBranch, local, release });
    return;
  }

  if (!release) {
    await io.exec("gh", ["release", "create", version, "--draft", "--verify-tag", "--target", commit, "--generate-notes", "--title", version, `--repo=${repo}`], { cwd: absoluteDist });
    release = await requireDraft(io, repo, version);
  }

  assertReleaseMetadata(release, { version, commit, draft: true });
  assertUploadedAssetRecords(release.assets, allNames, { allowMissing: true });
  await ensureExistingDraftAssetsMatch(io, { repo, version, local, release });

  const existing = new Set(release.assets.map((asset) => asset.name));
  const missing = allNames.filter((name) => !existing.has(name));
  if (missing.length) {
    await io.exec("gh", ["release", "upload", version, ...missing.map((name) => join(absoluteDist, name)), `--repo=${repo}`], { cwd: absoluteDist });
  }

  release = await requireDraft(io, repo, version);
  assertUploadedAssetRecords(release.assets, allNames);
  await verifyDraftContents(io, { repo, version, commit, defaultBranch, local, release });
  await verifyRefs(io, { repo, version, commit, defaultBranch, phase: "before promotion" });
  const finalDraft = await requireDraft(io, repo, version);
  assertReleaseMetadata(finalDraft, { version, commit, draft: true });
  assertUploadedAssetRecords(finalDraft.assets, allNames);
  await verifyDraftContents(io, { repo, version, commit, defaultBranch, local, release: finalDraft });

  let promotionError;
  try {
    await io.exec("gh", ["release", "edit", version, "--draft=false", "--prerelease=false", "--latest", "--verify-tag", `--repo=${repo}`]);
  } catch (error) {
    promotionError = error;
  }

  const published = await inspectRelease(io, repo, version);
  if (!published || published.isDraft) {
    if (promotionError) throw promotionError;
    throw new Error("release promotion did not produce a published release");
  }
  await verifyPublishedRelease(io, { repo, version, commit, defaultBranch, local, release: published });
}

async function verifyPublishedRelease(io, context) {
  const { repo, version, commit, defaultBranch, local, release } = context;
  assertReleaseMetadata(release, { version, commit, draft: false });
  assertUploadedAssetRecords(release.assets, local.allNames);
  await verifyDraftContents(io, context);
  await verifyRefs(io, { repo, version, commit, defaultBranch, phase: "published verification" });
}

async function verifyDraftContents(io, { repo, version, commit, local }) {
  const downloadRoot = await mkdtemp(join(tmpdir(), "session-reviewer-release-verify-"));
  try {
    await io.exec("gh", ["release", "download", version, "--dir", downloadRoot, `--repo=${repo}`]);
    const remote = await inspectAssetDirectory(downloadRoot, local.allNames, local.payloadNames, version);
    if (!local.checksumBytes.equals(remote.checksumBytes)) throw new Error("remote SHA256SUMS differs from local release manifest");
    if (!semanticEqual(local.manifest, remote.manifest)) throw new Error("remote manifest differs semantically from local manifest");
    for (const name of ATTESTED_NAMES) {
      await io.exec("gh", [
        "attestation", "verify", join(downloadRoot, name),
        "--repo", repo,
        "--source-digest", commit,
        "--source-ref", `refs/tags/${version}`,
        "--signer-workflow", `${repo}/.github/workflows/ci.yml`
      ]);
    }
  } finally {
    await rm(downloadRoot, { recursive: true, force: true });
  }
}

async function ensureExistingDraftAssetsMatch(io, { repo, version, local, release }) {
  if (!release.assets.length) return;
  const root = await mkdtemp(join(tmpdir(), "session-reviewer-release-resume-"));
  try {
    for (const asset of release.assets) {
      await io.exec("gh", ["release", "download", version, "--pattern", asset.name, "--dir", root, `--repo=${repo}`]);
      await assertRegularFile(join(root, asset.name));
      const digest = await sha256(join(root, asset.name));
      if (digest !== local.digests.get(asset.name)) throw new Error(`conflicting asset requires human intervention: ${asset.name}`);
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

async function inspectAssetDirectory(directory, allNames, payloadNames, version) {
  const entries = await readdir(directory);
  const actual = [...entries].sort();
  const expected = [...allNames].sort();
  const unknown = actual.filter((name) => !allNames.includes(name));
  const missing = allNames.filter((name) => !entries.includes(name));
  if (unknown.length) throw new Error(`unknown asset files: ${unknown.join(", ")}`);
  if (missing.length) throw new Error(`missing asset files: ${missing.join(", ")}`);
  if (actual.length !== expected.length) throw new Error("release asset set is not exact");
  for (const name of allNames) await assertRegularFile(join(directory, name));

  const checksumBytes = await readFile(join(directory, "SHA256SUMS"));
  const checksums = parseChecksums(checksumBytes.toString("utf8"), payloadNames);
  const digests = new Map();
  for (const name of payloadNames) {
    const digest = await sha256(join(directory, name));
    if (digest !== checksums.get(name)) throw new Error(`checksum mismatch for ${name}`);
    digests.set(name, digest);
  }
  digests.set("SHA256SUMS", createHash("sha256").update(checksumBytes).digest("hex"));

  let manifest;
  try {
    manifest = JSON.parse(await readFile(join(directory, "manifest.json"), "utf8"));
  } catch (error) {
    throw new Error("manifest.json must contain valid JSON", { cause: error });
  }
  validateManifest(manifest, version);
  return { allNames, payloadNames, checksumBytes, checksums, digests, manifest };
}

async function verifyRefs(io, { repo, version, commit, defaultBranch, phase }) {
  const head = (await io.exec("git", ["rev-parse", "HEAD"])).trim();
  const localTag = (await io.exec("git", ["rev-parse", `refs/tags/${version}^{commit}`])).trim();
  await io.exec("git", ["fetch", "--no-tags", "origin", `refs/heads/${defaultBranch}`]);
  const remoteBranch = (await io.exec("git", ["rev-parse", "FETCH_HEAD^{commit}"])).trim();
  await io.exec("git", ["fetch", "--no-tags", "origin", `refs/tags/${version}`]);
  const remoteTag = (await io.exec("git", ["rev-parse", "FETCH_HEAD^{commit}"])).trim();
  await writeEvidence(io, { phase, head, localTag, remoteBranch, remoteTag, defaultBranch, repo, version });
  if (head !== commit) throw new Error(`${phase}: local HEAD does not equal expected commit`);
  if (localTag !== commit) throw new Error(`${phase}: local tag does not equal expected commit`);
  if (remoteBranch !== commit) throw new Error(`${phase}: remote default branch tip does not equal expected commit`);
  if (remoteTag !== commit) throw new Error(`${phase}: remote tag does not equal expected commit`);
  return { phase, head, localTag, remoteBranch, remoteTag, defaultBranch, repo, version };
}

async function verifyGhCapabilities(io) {
  const checks = [
    [["release", "create", "--help"], ["--draft", "--verify-tag", "--target"]],
    [["release", "edit", "--help"], ["--draft", "--prerelease", "--latest", "--verify-tag"]],
    [["attestation", "verify", "--help"], ["--repo", "--source-digest", "--source-ref", "--signer-workflow"]]
  ];
  for (const [args, flags] of checks) {
    const help = await io.exec("gh", args);
    for (const flag of flags) if (!help.includes(flag)) throw new Error(`required gh flag unavailable: ${args.slice(0, -1).join(" ")} ${flag}`);
  }
}

async function inspectRelease(io, repo, version) {
  try {
    return parseRelease(await io.exec("gh", ["api", `repos/${repo}/releases/tags/${version}`, "--jq", RELEASE_JQ]));
  } catch (error) {
    if (error?.statusCode !== 404) throw new Error("failed to inspect published release", { cause: error });
  }

  const [owner, name] = repo.split("/");
  let draftID;
  try {
    draftID = (await io.exec("gh", [
      "api", "graphql",
      "-f", `query=${DRAFT_RELEASE_QUERY}`,
      "-F", `owner=${owner}`,
      "-F", `name=${name}`,
      "-F", `tagName=${version}`,
      "--jq", ".data.repository.release | select(.isDraft == true) | .databaseId"
    ])).trim();
  } catch (error) {
    throw new Error("failed to inspect draft release", { cause: error });
  }
  if (!draftID) return null;
  if (!/^[1-9]\d*$/.test(draftID)) throw new Error("draft release lookup returned an invalid database id");
  try {
    return parseRelease(await io.exec("gh", ["api", `repos/${repo}/releases/${draftID}`, "--jq", RELEASE_JQ]));
  } catch (error) {
    throw new Error("failed to inspect draft release metadata", { cause: error });
  }
}

function parseRelease(text) {
  try {
    return JSON.parse(text);
  } catch (error) {
    throw new Error("release lookup returned invalid JSON", { cause: error });
  }
}

async function requireDraft(io, repo, version) {
  const release = await inspectRelease(io, repo, version);
  if (!release || !release.isDraft) throw new Error("expected a resumable draft release");
  return release;
}

function assertReleaseMetadata(release, { version, commit, draft }) {
  if (release.tagName !== version) throw new Error("release tag mismatch");
  if (release.isDraft !== draft) throw new Error(`release draft state mismatch for ${version}`);
  if (release.isPrerelease) throw new Error("release must not be a prerelease");
  if (release.targetCommitish !== commit) throw new Error("release target commit mismatch");
}

function assertUploadedAssetRecords(assets, expectedNames, { allowMissing = false } = {}) {
  if (!Array.isArray(assets)) throw new Error("release assets are missing");
  const seen = new Set();
  for (const asset of assets) {
    if (!asset || typeof asset.name !== "string" || !isSafeAssetName(asset.name)) throw new Error("unsafe release asset record");
    if (!expectedNames.includes(asset.name)) throw new Error(`unknown asset requires human intervention: ${asset.name}`);
    if (seen.has(asset.name)) throw new Error(`duplicate release asset: ${asset.name}`);
    if (asset.state !== "uploaded") throw new Error(`release asset has incomplete upload state: ${asset.name}`);
    seen.add(asset.name);
  }
  if (!allowMissing) {
    const missing = expectedNames.filter((name) => !seen.has(name));
    if (missing.length) throw new Error(`missing asset records: ${missing.join(", ")}`);
  }
}

function validateRequest(request) {
  if (!request || typeof request !== "object") throw new Error("release request is required");
  const { repo, version, commit, dist } = request;
  validateVersion(version);
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repo ?? "")) throw new Error("repository must be OWNER/REPO");
  if (!COMMIT_RE.test(commit ?? "")) throw new Error("commit must be a full lowercase SHA-1");
  if (typeof dist !== "string" || !dist) throw new Error("dist directory is required");
  return { repo, version, commit, dist };
}

function validateVersion(version) {
  if (!VERSION_RE.test(version ?? "")) throw new Error("version must be plain X.Y.Z");
}

function validateManifest(manifest, version) {
  if (!manifest || typeof manifest !== "object" || Array.isArray(manifest)) throw new Error("manifest must be an object");
  if (manifest.id !== "session-reviewer") throw new Error("manifest plugin id mismatch");
  if (manifest.version !== version) throw new Error("manifest version mismatch");
  if (typeof manifest.minAppVersion !== "string" || !manifest.minAppVersion.trim()) throw new Error("manifest minimum app version is required");
}

function isSafeAssetName(name) {
  return typeof name === "string" && name.length > 0 && basename(name) === name && !isAbsolute(name) && name !== "." && name !== ".." && !name.includes("/") && !name.includes("\\");
}

function isSafeRefName(name) {
  return typeof name === "string" &&
    /^[A-Za-z0-9._/-]+$/.test(name) &&
    !name.startsWith("/") &&
    !name.endsWith("/") &&
    !name.endsWith(".") &&
    !name.includes("..") &&
    !name.includes("//") &&
    !name.includes("@{") &&
    !name.includes(".lock/") &&
    !name.endsWith(".lock");
}

async function assertRegularFile(path) {
  const stat = await lstat(path);
  if (!stat.isFile() || stat.isSymbolicLink()) throw new Error(`release asset must be a regular file: ${path}`);
}

async function sha256(path) {
  return createHash("sha256").update(await readFile(path)).digest("hex");
}

function semanticEqual(left, right) {
  return stableJson(left) === stableJson(right);
}

function stableJson(value) {
  if (Array.isArray(value)) return `[${value.map(stableJson).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJson(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}

async function writeEvidence(io, refs) {
  await io.summary(`HEAD (${refs.phase}): ${refs.head}`);
  await io.summary(`local tag (${refs.phase}): ${refs.localTag}`);
  await io.summary(`remote default branch tip (${refs.defaultBranch}, ${refs.phase}): ${refs.remoteBranch}`);
  await io.summary(`remote tag (${refs.phase}): ${refs.remoteTag}`);
}

function normalizeIo(io) {
  if (!io || typeof io.exec !== "function") throw new Error("io.exec is required");
  return { ...io, summary: typeof io.summary === "function" ? io.summary : async () => {} };
}

async function runChecked(io, file, args, label) {
  try {
    return await io.exec(file, args);
  } catch (error) {
    throw new Error(`failed to ${label}`, { cause: error });
  }
}

function productionIo() {
  return {
    exec: (file, args, options) => execBounded(file, args, options),
    summary: async (line) => {
      if (!process.env.GITHUB_STEP_SUMMARY) return;
      const { appendFile } = await import("node:fs/promises");
      await appendFile(process.env.GITHUB_STEP_SUMMARY, `- ${line}\n`);
    }
  };
}

function execBounded(file, args, options = {}) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(file, args, { cwd: options.cwd, env: process.env, stdio: ["ignore", "pipe", "pipe"] });
    const limit = 1024 * 1024;
    let stdout = Buffer.alloc(0);
    let stderr = Buffer.alloc(0);
    let overflow = false;
    let settled = false;
    const settle = (callback, value) => {
      if (settled) return;
      settled = true;
      callback(value);
    };
    child.stdout.on("data", (chunk) => {
      stdout = Buffer.concat([stdout, chunk]);
      if (stdout.length > limit) { overflow = true; child.kill(); }
    });
    child.stderr.on("data", (chunk) => {
      stderr = Buffer.concat([stderr, chunk]);
      if (stderr.length > limit) { overflow = true; child.kill(); }
    });
    child.on("error", (error) => settle(rejectPromise, error));
    child.on("close", (code) => {
      if (overflow) return settle(rejectPromise, new Error(`${file} output exceeded ${limit} bytes`));
      if (code === 0) return settle(resolvePromise, stdout.toString("utf8"));
      const error = new Error(`${file} ${args.join(" ")} failed (${code}): ${stderr.toString("utf8").trim()}`);
      const match = /\bHTTP (\d{3})\b/.exec(stderr.toString("utf8"));
      if (match) error.statusCode = Number(match[1]);
      settle(rejectPromise, error);
    });
  });
}

async function main(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 2) {
    const flag = argv[index];
    const value = argv[index + 1];
    if (!["--repo", "--version", "--commit", "--dist"].includes(flag) || value === undefined) throw new Error("usage: publish-release.mjs --repo OWNER/REPO --version X.Y.Z --commit SHA --dist DIR");
    options[flag.slice(2)] = value;
  }
  if (Object.keys(options).length !== 4) throw new Error("usage: publish-release.mjs --repo OWNER/REPO --version X.Y.Z --commit SHA --dist DIR");
  await publishVerifiedRelease(options);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(`publish-release: ${error.message}`);
    process.exitCode = 1;
  });
}
