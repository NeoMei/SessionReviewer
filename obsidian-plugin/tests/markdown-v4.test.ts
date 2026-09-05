import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { codeOf, parseMachineLedgerV4 } from "../src/data/contracts-v4";
import { markdownCodeOf, parseMarkdownV4, renderProblemTreeV4 } from "../src/data/markdown-v4";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/contracts/v4/markdown");
const read = (name: string): string => readFileSync(resolve(root, name), "utf8");

interface CorpusCase {
  name: string;
  review: string;
  history: string;
  ledger: string;
  expected_code: string;
  expected_document_code?: string;
  expected_markdown_code?: string;
  expected_fields: Record<string, string>;
}

const cases = (JSON.parse(read("cases.json")) as { cases: CorpusCase[] }).cases;

describe("v4 human Markdown codec", () => {
  it("keeps raw-document diagnostics distinct from ledger-backed draft diagnostics", () => {
    const draftOnly = cases.filter((entry) => entry.expected_markdown_code && !entry.expected_document_code);
    expect(draftOnly.map((entry) => entry.name)).toEqual([
      "draft-duplicate-baseline",
      "draft-invalid-baseline-hash",
      "draft-baseline-generation-mismatch",
      "draft-baseline-kind-mismatch",
      "draft-baseline-value-missing",
      "draft-baseline-values-present",
      "draft-duplicate-human-patch",
      "draft-orphan-patch-collision"
    ]);
  });

  it.each(cases)("matches the shared corpus for $name", (entry) => {
    let ledger;
    try {
      ledger = parseMachineLedgerV4(read(entry.ledger));
    } catch (error) {
      expect(codeOf(error)).toBe(entry.expected_code);
      return;
    }
    expect(entry.expected_code).toBe("");
    let result: ReturnType<typeof parseMarkdownV4> | undefined;
    let markdownError: unknown;
    try { result = parseMarkdownV4({ review: read(entry.review), history: read(entry.history) }, ledger); }
    catch (error) { markdownError = error; }
    expect(markdownCodeOf(markdownError)).toBe(entry.expected_markdown_code);
    if (entry.expected_markdown_code) return;
    expect(result).toBeDefined();
    const fields = Object.fromEntries(result!.fields.map((field) => [`${field.entity}/${field.name}`, field.value]));
    expect(fields).toMatchObject(entry.expected_fields);
  });

  it("does not promote a local edit to an accepted machine state", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    expect(pair.review.split("项目目标夹具")).toHaveLength(2);
    const before = structuredClone(ledger);

    const result = parseMarkdownV4({ ...pair, review: pair.review.replace("项目目标夹具", "人工编辑目标") }, ledger);

    expect(result.changedFields).toContainEqual({ entity: "project-overview", name: "goal" });
    expect(result.presentation.current_state.goal).toBe("人工编辑目标");
    expect(ledger).toEqual(before);
  });

  it("treats custom shell edits as pending without changing typed fields", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const result = parseMarkdownV4({ ...pair, review: `${pair.review}\n人工附注。\n` }, ledger);

    expect(result.changedFields).toEqual([]);
    expect(result.changedDocuments).toEqual(["review"]);
    expect(result.presentation.revision).toBe(ledger.accepted_revision + 1);
  });

  it("accepts composite and non-integer user YAML while rejecting nested merge keys", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const userYaml = "custom:\n  list: [one, null, 1.5]\n  map: {enabled: true}\n";
    const review = pair.review.replace("---\n# 项目回顾", `${userYaml}---\n# 项目回顾`);

    expect(() => parseMarkdownV4({ ...pair, review }, ledger)).not.toThrow();
    const nestedMerge = pair.review.replace("---\n# 项目回顾", "custom:\n  <<: {enabled: true}\n---\n# 项目回顾");
    expect(() => parseMarkdownV4({ ...pair, review: nestedMerge }, ledger)).toThrowError(expect.objectContaining({ code: "markdown_format_invalid" }));
  });

  it("rejects removal of a stable entity anchor as a structural edit", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const review = pair.review.replace('<a id="decision-x6465636973696f6e3a616c706861"></a>\n', "");
    expect(review).not.toBe(pair.review);

    expect(() => parseMarkdownV4({ ...pair, review }, ledger)).toThrowError(expect.objectContaining({ code: "markdown_structure_edit_requires_command" }));
  });

  it("scans a large legal history shell without per-entity rescans", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const custom = Array.from({ length: 20_000 }, (_, index) => `<a id="custom-${index}"></a>`).join("\n");

    const result = parseMarkdownV4({ ...pair, history: `${pair.history}\n${custom}\n` }, ledger);

    expect(result.changedDocuments).toEqual(["history"]);
    expect(result.changedFields).toEqual([]);
  });

  it("preserves verification while upgrading a nonempty human milestone conclusion", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const history = pair.history.replace("人工字段可编辑，结构仍需显式操作。", "人工确认的新结论");
    const beforeVerification = structuredClone(ledger.document_projection!.presentation_base.timeline[0].closed_loop.verification);

    const result = parseMarkdownV4({ ...pair, history }, ledger);

    expect(result.presentation.timeline[0].closed_loop.conclusion).toMatchObject({ kind: "human_confirmed", text: "人工确认的新结论", missing_reason: null });
    expect(result.presentation.timeline[0].closed_loop.verification).toEqual(beforeVerification);
  });

  it("rejects clearing a non-missing milestone conclusion", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const history = pair.history.replace("人工字段可编辑，结构仍需显式操作。", "");

    expect(() => parseMarkdownV4({ ...pair, history }, ledger)).toThrowError(expect.objectContaining({ code: "markdown_structure_edit_requires_command" }));
  });

  it("updates one valid existing patch without mutating the accepted ledger", () => {
    const ledger = parseMachineLedgerV4(read("ledger-existing-patch.json"));
    const before = structuredClone(ledger);

    const result = parseMarkdownV4({ review: read("review-existing-patch-draft.md"), history: read("history.md") }, ledger);

    expect(result.presentation.current_state.goal).toBe("再次人工编辑目标");
    expect(result.presentation.human_patches).toHaveLength(1);
    expect(result.presentation.human_patches[0]).toMatchObject({ entity_id: "project-overview", field: "goal", operation: "set", value: "再次人工编辑目标" });
    expect(result.presentation.generated_baselines).toEqual(before.generated_baselines);
    expect(ledger).toEqual(before);
  });

  it("removes an existing human patch when the field returns to its generated baseline", () => {
    const ledger = parseMachineLedgerV4(read("ledger-existing-patch.json"));

    const result = parseMarkdownV4({ review: read("review.md"), history: read("history.md") }, ledger);

    expect(result.presentation.current_state.goal).toBe("项目目标夹具");
    expect(result.presentation.human_patches).toEqual([]);
    expect(result.presentation.generated_baselines).toEqual(ledger.generated_baselines);
  });

  it("validates Go baseline hashes containing HTML and line-separator characters", () => {
    const ledger = parseMachineLedgerV4(read("ledger-special-baseline.json"));
    expect(ledger.generated_baselines[0].generated_hash).toBe("112173e9eb315eaffc9ce7b55ca9fc1a768be05486a426a2365c1354301476bd");

    const result = parseMarkdownV4({ review: read("review-special-baseline-draft.md"), history: read("history.md") }, ledger);

    expect(result.presentation.current_state.goal).toBe("再次特殊字符覆盖");
    expect(result.presentation.human_patches[0]).toMatchObject({ value: "再次特殊字符覆盖", base_generated_hash: "112173e9eb315eaffc9ce7b55ca9fc1a768be05486a426a2365c1354301476bd" });
  });

  it.each([
    ["ledger-duplicate-baseline.json", "review.md"],
    ["ledger-invalid-baseline-hash.json", "review.md"],
    ["ledger-baseline-generation-mismatch.json", "review.md"],
    ["ledger-baseline-kind-mismatch.json", "review.md"],
    ["ledger-baseline-value-missing.json", "review.md"],
    ["ledger-baseline-values-present.json", "review.md"],
    ["ledger-duplicate-human-patch.json", "review-existing-patch.md"],
    ["ledger-orphan-patch-collision.json", "review-existing-patch.md"]
  ])("does not reject malformed patch metadata for an untouched field in %s", (ledgerName, reviewName) => {
    const ledger = parseMachineLedgerV4(read(ledgerName));

    const result = parseMarkdownV4({ review: read(reviewName), history: read("history.md") }, ledger);

    expect(result.changedFields).toEqual([]);
  });

  it("renders a deep legal problem chain iteratively within the document bound", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const presentation = structuredClone(ledger.document_projection!.presentation_base);
    const template = presentation.problem_nodes[0];
    presentation.problem_nodes = Array.from({ length: 5_000 }, (_, index) => ({
      ...template,
      id: `deep-${index}`,
      primary_parent_id: index === 0 ? null : `deep-${index - 1}`
    }));

    const rendered = renderProblemTreeV4(presentation);

    expect(rendered.split("\n")).toHaveLength(5_000);
    expect(rendered).toContain("#problem-x646565702d34393939");
  });

  it("rejects a legal-depth problem chain once bounded rendering would exceed 64 MiB", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const presentation = structuredClone(ledger.document_projection!.presentation_base);
    const template = presentation.problem_nodes[0];
    presentation.problem_nodes = Array.from({ length: 9_000 }, (_, index) => ({
      ...template,
      id: `oversize-${index}`,
      primary_parent_id: index === 0 ? null : `oversize-${index - 1}`
    }));

    expect(() => renderProblemTreeV4(presentation)).toThrowError(expect.objectContaining({ code: "markdown_format_invalid" }));
  });
});
