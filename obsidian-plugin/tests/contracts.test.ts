import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { parseLedger } from "../src/data/ledger";

const fixture = (name: string): Promise<string> =>
  readFile(resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/review-v3", name), "utf8");

describe("review v2 machine contract", () => {
  it("accepts the Go golden ledger and exposes accounting", async () => {
    const ledger = parseLedger(await fixture("ledger.valid.json"));
    expect(ledger.schemaVersion).toBe(3);
	expect(ledger.minimumWriterVersion).toBe("0.3.0");
    expect(ledger.projectId).toBe("project-review-v2");
    expect(ledger.accounting.totalTokens).toBe(350);
    expect(ledger.sessions[0]?.accounting?.models[0]?.pricing?.source).toBe("https://example.com/pricing");
  });

  it("rejects duplicate identities, unknown keys, unsafe integers, and forged totals", async () => {
    const duplicate = await fixture("ledger.invalid-duplicate-id.json");
    expect(() => parseLedger(duplicate)).toThrow(/duplicate/i);

    const valid = JSON.parse(await fixture("ledger.valid.json")) as Record<string, unknown>;
    expect(() => parseLedger('{"schema_version":2,"schema_version":2}')).toThrow(/duplicate JSON object key/);
    expect(() => parseLedger(JSON.stringify({ ...valid, unexpected: true }))).toThrow(/unknown/i);

    const unsafe = structuredClone(valid) as { accepted_revision: number };
    unsafe.accepted_revision = Number.MAX_SAFE_INTEGER + 1;
    expect(() => parseLedger(JSON.stringify(unsafe))).toThrow(/safe integer/i);

    const forged = structuredClone(valid) as { accounting: { total_tokens: number } };
    forged.accounting.total_tokens += 1;
    expect(() => parseLedger(JSON.stringify(forged))).toThrow(/aggregate total/i);
  });

  it("accepts host token_count when reasoning exceeds visible output", async () => {
    const valid = JSON.parse(await fixture("ledger.valid.json")) as {
      sessions: Array<{ accounting: { models: Array<{ output_tokens: number; reasoning_output_tokens: number; total_tokens: number }> } }>;
    };
    const model = valid.sessions[0]?.accounting.models[0];
    if (!model) throw new Error("golden ledger is missing session model accounting");
    model.reasoning_output_tokens = model.output_tokens + 326;
    const ledger = parseLedger(JSON.stringify(valid));
    expect(ledger.sessions[0]?.accounting?.models[0]).toMatchObject({
      outputTokens: model.output_tokens,
      reasoningOutputTokens: model.reasoning_output_tokens,
      totalTokens: model.total_tokens
    });
  });

  it("accepts omitted scalar values and valueless patch operations from the Go wire format", async () => {
    const valid = JSON.parse(await fixture("ledger.valid.json")) as {
      generation_id: string;
      human_patches: Array<Record<string, unknown>>;
      generated_baselines: Array<Record<string, unknown>>;
    };
    valid.generated_baselines = [{
      generation_id: valid.generation_id,
      entity_id: "project-overview",
      field: "goal",
      kind: "scalar",
      generated_hash: "a".repeat(64)
    }];
    valid.human_patches = [{
      entity_id: "project-overview",
      field: "goal",
      operation: "restore_default",
      base_generated_hash: "b".repeat(64)
    }];

    const ledger = parseLedger(JSON.stringify(valid));

    expect(ledger.generatedBaselines[0]?.value).toBeUndefined();
    expect(ledger.humanPatches[0]?.value).toBeUndefined();
    expect(ledger.humanPatches[0]?.values).toBeUndefined();
  });

  it("preserves token usage and marks pricing incomplete for the host unknown-pricing sentinel", async () => {
	const valid = JSON.parse(await fixture("ledger.valid.json")) as {
		accounting: { total_cost_usd: number; models: Array<{ total_cost_usd: number; cost_share_pct: number }> };
		sessions: Array<{ accounting: { total_cost_usd: number; models: Array<{ pricing: Record<string, unknown>; cost_usd: number }> } }>;
	};
	valid.accounting.total_cost_usd = 0;
	for (const model of valid.accounting.models) {
		model.total_cost_usd = 0;
		model.cost_share_pct = 0;
	}
	for (const session of valid.sessions) {
		session.accounting.total_cost_usd = 0;
		for (const model of session.accounting.models) {
			model.pricing = {
				currency: "", input_per_million: 0, cached_input_per_million: 0,
				cache_write_input_per_million: 0, output_per_million: 0, source: "", as_of: ""
			};
			model.cost_usd = 0;
		}
	}

	const ledger = parseLedger(JSON.stringify(valid));
	expect(ledger.accounting.totalTokens).toBe(350);
	expect(ledger.accounting.pricingComplete).toBe(false);
	expect(ledger.sessions[0]?.accounting?.pricingComplete).toBe(false);
	expect(ledger.sessions[0]?.accounting?.models[0]?.pricing).toBeUndefined();
  });
});
