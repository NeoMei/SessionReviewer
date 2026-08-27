import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { parseLedger } from "../src/data/ledger";

const fixture = (name: string): Promise<string> =>
  readFile(resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/review-v2", name), "utf8");

describe("review v2 machine contract", () => {
  it("accepts the Go golden ledger and exposes accounting", async () => {
    const ledger = parseLedger(await fixture("ledger.valid.json"));
    expect(ledger.schemaVersion).toBe(2);
    expect(ledger.projectId).toBe("project-review-v2");
    expect(ledger.accounting.totalTokens).toBe(350);
    expect(ledger.sessions[0]?.accounting?.models[0]?.pricing.source).toBe("https://example.com/pricing");
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
});
