# Release Verification Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish only the accepted mainline revision and independently verified official assets, preserving a resumable draft until verification succeeds.

**Architecture:** Keep current deterministic packagers and CI platform matrix. Add a small Node22 release orchestration script with explicit argv, pure metadata validators and an injectable process boundary tested without network or GitHub writes. CI runs the script only after platform gates and build provenance; immutable published releases are never overwritten.

**Tech Stack:** Existing Node22 built-ins/node:test, Git/GitHub CLI, GitHub Actions and existing Go release-packager tests; no new dependency or product runtime capability.

**Spec:** `docs/verification/2026-09-08-release-execution.md` release authorization/gates; product acceptance remains `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`.

## Global Constraints

- No push/tag/release/upload/install during implementation or automated local tests. Final remote writes occur only after all applicable S01–S12 gates.
- No product source, human Markdown, source adapters or Vault changes in this task. Existing platform checks remain; no new migration/platform expansion.
- Exact release set: three CLI archives, one plugin ZIP, standalone `main.js`, `manifest.json`, `styles.css`, and `SHA256SUMS`. Version is validated plain `X.Y.Z`, never inferred from an arbitrary filename or remote response.
- Preserve existing reproducible archive bytes and three-file attestation. Do not expand trusted inputs, expose tokens or invent completion from local assets alone.
- Audit correction: verified gh2.97.0 already stages asset-bearing releases as drafts until upload completes. There is no confirmed interrupted-upload/public-partial bug. This task adds repository-owned independent verification before that automatic promotion, plus a mainline requirement and compatibility metadata repair.
- Capture `gh version` in CI evidence; fail if required verify/edit flags are unavailable. Use fixed official repository identity from the job context, not asset metadata.

### Task 1: Enforce mainline and remote artifact verification before promotion

**Files:** Create `scripts/publish-release.mjs`, `scripts/publish-release.test.mjs`; modify `.github/workflows/ci.yml`, `cmd/release-packager/main_test.go` for wiring assertions only. Do not alter deterministic packager implementation or baselines.

**Interfaces:** Export `expectedAssetNames(version): string[]`, `parseChecksums(text, expectedPayloadNames): Map<string,string>`, and `publishVerifiedRelease({repo,version,commit,dist}, io): Promise<void>`. `io.exec(file,args,options)` returns bounded stdout or throws; production uses spawn/execFile with argument arrays and fixed cwd, tests a recording fake. All file reads/hashes use Node built-ins; temporary download directory comes from `mkdtemp` and is the only recursively cleaned directory. The script runs main only when invoked directly, so imports in tests are nonmutating.

- [ ] Write pure RED tests: exact eight names; semver rejection; checksums exactly seven unique payloads; reject traversal/absolute path, unknown or missing entries, duplicate lines, non-lowercase/non-64hex digest, trailing malformed text. Never invoke `sha256sum -c` on unchecked remote filenames.

```js
assert.throws(() => parseChecksums(`${'a'.repeat(64)}  ../outside\n`, expectedAssetNames('0.4.3').filter(n => n !== 'SHA256SUMS')));
assert.equal(expectedAssetNames('0.4.3').length, 8);
```

- [ ] Write orchestrator RED tests with recording fake Git/gh: unknown/default-branch lookup error, tag/head mismatch, non-mainline tag, remote ref changing before promotion, asset upload interruption, missing/extra/corrupt downloaded payload, mismatched manifest version/ID/minimum, attestation rejection and draft re-entry. Assert no public promotion in every failure case; successful case has exactly one final promotion after all checks. Published-existing exact result is read-only verified success; published-existing mismatch is an error with zero mutation.

```js
await assert.rejects(publishVerifiedRelease(validRequest, fakeWithCorruptDownloadedMain));
assert.equal(fakeWithCorruptDownloadedMain.calls.some(c => c.args.includes('--draft=false')), false);
```

- [ ] Run `node --test scripts/publish-release.test.mjs` RED. Implement strict input validation and explicit branch/tag guard: query repository default branch, validate Git ref, fetch that branch and tag, require tag peeled commit=expected commit=HEAD=remote default-branch tip. Repeat remote tip/tag identity check immediately before promotion; record all exact SHAs in job summary. A later main advance causes a safe gate failure, not release of another commit.
- [ ] Validate local asset set and checksums first. Inspect existing release by exact tag; only a typed404 is absent (not any network/auth error). If absent create draft with explicit `--draft --verify-tag --target <commit>` and no assets; if an existing draft is eligible, resume it. Never use `--clobber`: download existing expected draft assets and compare to current local digests, upload only missing matching names; unknown/conflicting assets fail for human intervention. Serialize the workflow by repository/tag with `cancel-in-progress:false`.
- [ ] Query exact remote names and uploaded state; download into a new task temp directory via fixed `gh release download` arguments. Parse remote checksums and require its bytes to equal the locally built checksum manifest, then independently hash each downloaded payload and compare both lists. Parse standalone manifest and compare all fields semantically to locally built manifest, including version, ID and minimum. Exact plugin ZIP hash plus unchanged reproducible packaging tests binds its known entry set. Reject symlinks/nonregular files in both input and downloaded sets.
- [ ] Verify each downloaded standalone plugin file with `gh attestation verify`: fixed repo, `--source-digest <commit>`, `--source-ref refs/tags/<version>`, `--signer-workflow <repo>/.github/workflows/ci.yml`. If required flags are unavailable or verification fails, keep draft. After final ref/metadata recheck, use `gh release edit <version> --draft=false --prerelease=false --latest --verify-tag`; read back release non-draft/non-prerelease and exact tag/assets. An uncertain promotion response triggers read-only reconciliation, never rewriting public assets.
- [ ] Wire publish job to the new script after current build/attest steps; add node tests to existing CI check sequence. Keep Go workflow assertions for wrapper invocation and add script/ordering checks, but use executable node tests for behavior. Run focused node and `go test ./cmd/release-packager -count=1`, existing plugin package test and diff check. Obtain independent spec/quality review. Do not actually invoke the production publishing entry point locally.

### Task 2: Restore released compatibility entries without rewriting release history

**Files:** Modify only `versions.json`, `obsidian-plugin/versions.json`, `obsidian-plugin/tests/package.test.ts`.

**Interfaces:** Existing Obsidian compatibility map JSON, unchanged root/plugin equality and current version checks. Both tagged0.3.0/0.3.1 manifests were read and declare `minAppVersion:"1.8.7"`.

- [ ] Add RED assertion using explicit known released-version fixture, independent of tag availability in shallow/source archive test environments. Require both0.3.0and0.3.1 mappings, exact verified minimum, preserve every pre-existing mapping and equality.

```ts
expect(versions).toMatchObject({ '0.3.0': '1.8.7', '0.3.1': '1.8.7' });
```

- [ ] Run `npm --prefix obsidian-plugin test -- package.test.ts` RED; add exactly the two missing map entries in semantic order, no version bump yet. Rerun full plugin check and focused release-packager tests; inspect exact diff and obtain independent review. This closes metadata completeness only, not BRAT upgrade acceptance.

## Preflight and references

Mainline guard intentionally chooses exact default-branch tip, stricter than ancestry, matching final release execution queue. A later tip change is a safe release pause; no tag force-update is permitted. Existing public-release mismatches require explicit human direction, not asset replacement. Resumable draft accepts only byte-identical existing assets, so a rebuild drift cannot be silently mixed into one release.

Official references verified2026-09-08: [gh2.97.0 draft/upload/publish implementation](https://github.com/cli/cli/blob/v2.97.0/pkg/cmd/release/create/create.go#L461), [draft promotion](https://cli.github.com/manual/gh_release_edit), [attestation identity enforcement](https://cli.github.com/manual/gh_attestation_verify). Upstream source describes tool behavior; no remote failure was injected during preflight.

Self-review: task1 covers tag/source identity, exact remote payloads, failure/retry/public immutability and explicit verified promotion; task2 covers the independently confirmed map omission. Native BRAT and CLI install, release tag CI, actual official downloads, and product acceptance remain final controller gates, not marked complete by these tests.
