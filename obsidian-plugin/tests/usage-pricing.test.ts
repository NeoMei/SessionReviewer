import { describe, expect, it } from "vitest";
import { renderV4Usage } from "../src/view/render-v4-usage";
import { readFileSync } from "node:fs";
import { sha256Text } from "../src/data/hash";
import { loadMarkdownSnapshot } from "../src/data/repository-v4";
import { parseMachineLedgerV4, parsePricingSnapshotV1 } from "../src/data/contracts-v4";

function fixture() {
  const ledger = parseMachineLedgerV4(readFileSync("tests/fixtures/v4/machine-ledger-v4.valid.json", "utf8"));
  ledger.accounting.models = [{ model: ledger.pricing_snapshots[0].billed_model_id, total_tokens: 15, total_cost_usd: null }];
  return ledger;
}

describe("pricing evidence in usage cards", () => {
  it("shows dated stale catalog evidence, missing dimensions and source attribution", () => {
    const ledger = fixture();
    const price = ledger.pricing_snapshots[0];
    price.status = "stale_estimate";
    price.retrieved_at = "2026-09-01T00:00:00Z";
    price.source_last_updated = "2026-08-31";
    price.missing_billing_dimensions = ["cache_write_input"];
    price.audit_reason = "catalog_expired";
    const root = renderV4Usage(ledger.accounting, [price]);
    expect(root.textContent).toContain("过期缓存估算");
    expect(root.textContent).toContain("目录查询时间");
    expect(root.textContent).toContain("2026");
    expect(root.textContent).toContain("价格来源更新");
    expect(root.textContent).toContain("缺少价格：缓存写入");
    expect(root.querySelector('a[href="https://modelpricewatch.com/"]')?.textContent).toContain("ModelPriceWatch");
  });

  it("labels an unknown route and missing total without pretending a zero bill", () => {
    const ledger = fixture();
    const price = ledger.pricing_snapshots[0];
    price.status = "pending";
    price.billing_host = "unknown";
    price.audit_reason = "observed_billing_route_missing";
    price.total_cost_usd = null;
    ledger.accounting.total_cost_usd = null;
    const root = renderV4Usage(ledger.accounting, [price]);
    expect(root.textContent).toContain("计费服务商未知");
    expect(root.textContent).toContain("尚未记录实际计费路由");
    expect(root.textContent).toContain("总费用待定");
    expect(root.textContent).not.toContain("unknown · pending");
  });
});

it("requires an explicit supplement form confirmation and retains it on failed publication", async () => {
  const ledger = fixture();
  const price = ledger.pricing_snapshots[0];
  let submitted: unknown;
  const root = renderV4Usage(ledger.accounting, [price], {
    supplement: async (input) => { submitted = input; throw new Error("private path must stay hidden"); }
  });
  document.body.append(root);
  expect(submitted).toBeUndefined();
  root.querySelector<HTMLButtonElement>('[data-action="supplement-price"]')!.click();
  const form = root.querySelector<HTMLFormElement>('form')!;
  const set = (key: string, value: string) => { form.querySelector<HTMLInputElement>(`[name="${key}"]`)!.value = value; };
  set("effective_from", "2026-01-01T00:00:00Z");
  set("audit_reason", "已核对官方来源与实际调用路由");
  form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(submitted).toMatchObject({ session_id: price.session_id, billed_model_id: price.billed_model_id, supersedes_snapshot_id: price.snapshot_id, rates: price.rates });
  expect(submitted).not.toHaveProperty("total_cost_usd");
  expect(root.textContent).toContain("保存结果未确认");
  expect(root.textContent).not.toContain("private path");
  expect(form.querySelector<HTMLInputElement>('[name="audit_reason"]')!.value).toContain("已核对");
  root.remove();
});

it("keeps the exact serialized ledger hash for compare-and-swap writes", () => {
  const read = (name: string) => readFileSync(`../testdata/contracts/v4/markdown/${name}`, "utf8");
  const input = { review: read("review.md"), history: read("history.md"), ledger: read("ledger.json"), index: read("index.json") };
  const state = loadMarkdownSnapshot(input);
  expect(state.kind).toBe("public_valid");
  if (state.kind !== "public_valid") throw new Error("invalid fixture");
  expect(state.ledgerSHA256).toBe(sha256Text(input.ledger));
});

it("reads complete pricing when only zero-quantity dimensions have unknown rates",()=>{
 const ledger=fixture();const price=ledger.pricing_snapshots[0];
 price.billable_quantities.cache_write_input=0;price.billable_quantities.reasoning_output=0;
 price.rates.cache_write_input=null;price.rates.reasoning_output=null;
 price.line_costs_usd.cache_write_input=null;price.line_costs_usd.reasoning_output=null;
 price.missing_billing_dimensions=[];price.pricing_complete=true;
 const dimensions=["input","cached_input","output"] as const;
 for(const d of dimensions){price.rates[d]=1;price.line_costs_usd[d]=price.billable_quantities[d]/1e6;}
 price.known_subtotal_usd=dimensions.reduce((sum,d)=>sum+price.line_costs_usd[d]!,0);price.total_cost_usd=price.known_subtotal_usd;
 expect(()=>parsePricingSnapshotV1(JSON.stringify(price))).not.toThrow();
});
