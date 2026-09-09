#!/usr/bin/env node
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtemp, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { pathToFileURL, fileURLToPath } from "node:url";
import { build } from "esbuild";

const here = dirname(fileURLToPath(import.meta.url));
const pluginRoot = resolve(here, "..");

function usage() {
  return "Usage: node scripts/check-v4-pane-layout.mjs --playwright-module <playwright/index.mjs> --browser-executable <browser>";
}

function parseArgs(argv) {
  const values = new Map();
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!name?.startsWith("--") || !value) throw new Error(usage());
    values.set(name.slice(2), value);
  }
  const playwrightModule = values.get("playwright-module");
  const browserExecutable = values.get("browser-executable");
  if (!playwrightModule || !browserExecutable || values.size !== 2) throw new Error(usage());
  return { playwrightModule: resolve(playwrightModule), browserExecutable: resolve(browserExecutable) };
}

const baseStyles = `
:root { --background-primary:#fff; --background-primary-alt:#f7f7f7; --background-secondary:#f5f4f1; --background-modifier-border:#dedbd4; --background-modifier-hover:#f0edfa; --background-modifier-form-field:#fff; --text-normal:#292820; --text-muted:#77736b; --text-faint:#969187; --text-accent:#705cc2; --interactive-accent:#705cc2; --interactive-accent-hover:#604caf; --interactive-normal:#f5f4f1; --interactive-hover:#eae7e1; --text-on-accent:#fff; --text-error:#b23a34; --input-height:30px; }
html, body { margin:0; min-width:0; } body { background:#eceae5; color:var(--text-normal); font:15px/1.55 -apple-system,BlinkMacSystemFont,sans-serif; padding:8px; } #pane { background:var(--background-primary); box-sizing:border-box; min-width:0; } button,input,select { color:inherit; font:inherit; } button { background:var(--interactive-normal); border:1px solid var(--background-modifier-border); border-radius:6px; padding:6px 12px; } input,select { background:var(--background-modifier-form-field); border:1px solid var(--background-modifier-border); border-radius:5px; padding:6px; } dt,dd { margin:0; }
`;

function html(bundle) {
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>SessionReviewer v4 pane layout regression</title><link rel="stylesheet" href="/styles.css"><style>${baseStyles}</style></head><body><main id="pane"></main><script>${bundle}</script></body></html>`;
}

async function box(page, selector) {
  const result = await page.locator(selector).boundingBox();
  assert(result, `${selector} must have a rendered rectangle`);
  return result;
}

function inside(inner, outer, label) {
  assert(inner.x >= outer.x - 1, `${label} escapes the shell on the left`);
  assert(inner.x + inner.width <= outer.x + outer.width + 1, `${label} escapes the shell on the right`);
}

async function setHost(page, width) {
  await page.locator("#pane").evaluate((node, value) => { node.style.width = `${value}px`; }, width);
  await page.evaluate(() => new Promise((resolveFrame) => window.requestAnimationFrame(() => window.requestAnimationFrame(resolveFrame))));
}

async function selectDeepProblem(page) {
  await page.locator('[data-v4-tab="problems"]').click();
  await page.locator('[data-v4-problem-id="problem:deep"]').click();
  await assertSelection(page);
}

async function assertSelection(page) {
  assert.equal(await page.locator('[data-v4-tab="problems"]').getAttribute("aria-selected"), "true", "problem tab selection must survive resize");
  assert.equal(await page.locator('[data-v4-problem-id="problem:deep"]').getAttribute("aria-selected"), "true", "deep problem selection must survive resize");
}

async function assertProblemLayout(page, width, compact) {
  await setHost(page, width);
  const shell = await box(page, ".sr-v4-shell");
  const tree = await box(page, ".sr-v4-problem-tree");
  const context = await box(page, ".sr-v4-problem-context");
  const detail = await box(page, ".sr-v4-problem-detail");
  for (const [label, rectangle] of [["tree", tree], ["context", context], ["detail", detail]]) inside(rectangle, shell, label);
  if (compact) {
    assert(context.y >= tree.y + tree.height - 1, `context must stack below tree at host width ${width}`);
    assert(detail.y >= context.y + context.height - 1, `detail must stack below context at host width ${width}`);
  } else {
    assert(Math.abs(context.y - tree.y) <= 1 && Math.abs(detail.y - tree.y) <= 1, "wide layout must keep all problem columns on one row");
    assert(context.x >= tree.x + tree.width - 1, "context must be the second wide column");
    assert(detail.x >= context.x + context.width - 1, "detail must be the third wide column");
  }
  const controls = page.locator('.sr-v4-problem-tree button, .sr-v4-problem-context button, .sr-v4-problem-detail button');
  for (let index = 0; index < await controls.count(); index += 1) {
    const control = controls.nth(index);
    const rectangle = await control.boundingBox();
    if (!rectangle) {
      assert(await control.evaluate((node) => Boolean(node.closest("details:not([open])"))), "only collapsed form controls may be hidden");
      continue;
    }
    inside(rectangle, shell, `problem control ${index}`);
    assert.equal(await control.evaluate((node) => node.scrollWidth <= node.clientWidth + 1), true, `problem control ${index} text must wrap inside its box`);
  }
}

async function assertHeaderAndUsage(page, width) {
  await setHost(page, width);
  const shell = await box(page, ".sr-v4-shell");
  const header = await box(page, ".sr-v4-header");
  inside(header, shell, "v4 header");
  const headerParts = page.locator('.sr-v4-header .sr-v4-state-field, .sr-v4-header .sr-v4-support > *');
  for (let index = 0; index < await headerParts.count(); index += 1) inside(await headerParts.nth(index).boundingBox(), shell, `header item ${index}`);
  await page.locator('[data-v4-tab="usage"]').click();
  const usage = await box(page, ".sr-v4-usage");
  inside(usage, shell, "usage panel");
  for (const selector of [".sr-v4-usage-total", ".sr-v4-model-card", ".sr-v4-price-lines"]) inside(await box(page, selector), shell, selector);
}

async function assertNestedSessions(page, width) {
  await setHost(page, width);
  await page.locator('[data-v4-tab="sessions"]').click();
  await page.locator(".sr-conversation-browser").waitFor();
  await page.getByText("已保留问题树、上下文和证据顺序。", { exact: true }).waitFor();
  await page.locator(".sr-event-browser").waitFor();
  const shell = await box(page, ".sr-v4-shell");
  for (const [containerSelector, firstSelector, secondSelector] of [
    [".sr-scan-browser", ".sr-session-rail", ".sr-event-area"],
    [".sr-conversation-browser", ".sr-turn-list", ".sr-conversation-detail"],
    [".sr-event-browser", ".sr-event-list", ".sr-event-detail"]
  ]) {
    const container = await box(page, containerSelector);
    const first = await box(page, firstSelector);
    const second = await box(page, secondSelector);
    inside(container, shell, containerSelector);
    inside(first, shell, firstSelector);
    inside(second, shell, secondSelector);
    assert(second.y >= first.y + first.height - 1, `${containerSelector} must stack in a narrow v4 pane`);
  }
  const search = await box(page, '.sr-session-rail input[aria-label="搜索 Session"]');
  inside(search, await box(page, ".sr-session-rail"), "Session search control");
}

async function run() {
  const args = parseArgs(process.argv.slice(2));
  const [{ chromium }, css, result] = await Promise.all([
    import(/* webpackIgnore: true */ pathToFileURL(args.playwrightModule).href), // eslint-disable-line no-unsanitized/method -- explicit CLI path supplied by the caller
    readFile(resolve(pluginRoot, "styles.css"), "utf8"),
    build({ entryPoints: [resolve(pluginRoot, "tests/fixtures/v4-pane-layout.ts")], bundle: true, write: false, format: "iife", platform: "browser", target: "es2022", plugins: [{ name: "unused-node-import", setup(b) { b.onResolve({ filter: /^node:crypto$/ }, (args) => ({ path: args.path, external: true, sideEffects: false })); } }], footer: { js: "V4PaneFixture.mountV4PaneLayoutFixture(document.querySelector('#pane'));" }, globalName: "V4PaneFixture" })
  ]);
  const outputDir = await mkdtemp(resolve(tmpdir(), "session-reviewer-v4-pane-"));
  let server;
  let browser;
  let context;
  try {
    const pageHtml = html(new TextDecoder().decode(result.outputFiles[0].contents));
    server = createServer((request, response) => {
      if (request.url === "/styles.css") {
        response.setHeader("Content-Type", "text/css; charset=utf-8");
        response.end(css);
      } else {
        response.setHeader("Content-Type", "text/html; charset=utf-8");
        response.end(pageHtml);
      }
    });
    await new Promise((resolveListen, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", resolveListen);
    });
    const address = server.address();
    assert(address && typeof address !== "string", "fixture server must have a TCP address");
    browser = await chromium.launch({ executablePath: args.browserExecutable, headless: true });
    context = await browser.newContext({ viewport: { width: 1280, height: 1600 }, deviceScaleFactor: 1 });
    const page = await context.newPage();
    const runtimeErrors = [];
    page.on("pageerror", (error) => runtimeErrors.push(error.message));
    page.on("console", (message) => { if (message.type() === "error") runtimeErrors.push(message.text()); });
    await page.goto(`http://127.0.0.1:${address.port}/`, { waitUntil: "load" });
    assert.equal(await page.title(), "SessionReviewer v4 pane layout regression");
    assert.equal(await page.locator(".sr-v4-shell").count(), 1, "actual v4 renderer must mount a nonblank shell");
    assert.equal(await page.locator("vite-error-overlay, nextjs-portal, #webpack-dev-server-client-overlay").count(), 0, "fixture must not show a framework error overlay");
    assert.deepEqual(await page.locator('[role="tab"]').allTextContents(), ["项目演进", "问题脉络", "决策与约定", "全部 Sessions", "用量"]);
    assert.match(await page.locator(".sr-v4-milestone-detail").textContent(), /触发问题[\s\S]*codex\/session-1#turn-layout/, "history detail must retain its source identity");

    await setHost(page, 1200);
    await selectDeepProblem(page);
    await assertProblemLayout(page, 1200, false);
    await page.screenshot({ path: resolve(outputDir, "wide-1200.png"), fullPage: true });
    await assertProblemLayout(page, 580, true);
    await assertSelection(page);
    await page.screenshot({ path: resolve(outputDir, "narrow-580.png"), fullPage: true });
    await assertProblemLayout(page, 760, true);
    await assertProblemLayout(page, 1200, false);
    await assertSelection(page);
    await assertHeaderAndUsage(page, 580);
    await assertNestedSessions(page, 580);

    await page.setViewportSize({ width: 390, height: 1600 });
    await assertHeaderAndUsage(page, 374);
    await assertNestedSessions(page, 374);
    await page.screenshot({ path: resolve(outputDir, "mobile-390.png"), fullPage: true });
    assert.deepEqual(runtimeErrors, [], "fixture must not emit browser runtime errors");
    process.stdout.write(`PASS v4 pane layout regression\nScreenshots: ${outputDir}\n`);
  } finally {
    await context?.close();
    await browser?.close();
    if (server) await new Promise((resolveClose, reject) => server.close((error) => error ? reject(error) : resolveClose()));
  }
}

run().catch((error) => {
  console.error(error instanceof Error ? error.stack : error);
  process.exitCode = 1;
});
