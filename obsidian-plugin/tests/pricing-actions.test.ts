import { expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { parseMachineLedgerV4 } from "../src/data/contracts-v4";
import { renderV4Usage } from "../src/view/render-v4-usage";
const tick=()=>new Promise(done=>setTimeout(done,0));
it("queries only on click and confirms a catalog entry with user supplied route instead of model-name guessing",async()=>{
 const ledger=parseMachineLedgerV4(readFileSync("tests/fixtures/v4/machine-ledger-v4.valid.json","utf8"));const price=ledger.pricing_snapshots[0];ledger.accounting.models=[{model:price.billed_model_id,total_tokens:15,total_cost_usd:null}];let calls=0;let accepted:unknown;
 const root=renderV4Usage(ledger.accounting,[price],{catalog:async()=>{calls++;return {schema_version:1,minimum_reader_version:"0.4.0",status:"current",retrieved_at:"2026-09-09T00:00:00Z",model_count:1,history_count:1,refresh_error:"",listings:[{listing_id:"vendor-model",provider:"Vendor",model:"Model",category:"text",status:"active",billing_host:null,billing_mode:null,region:null,input_per_mtok:1,cached_input_per_mtok:null,output_per_mtok:2,promo:false,promo_until:null,price_note:null,has_unstructured_condition:false,pricing_url:"https://example.com/pricing",detail_url:"https://modelpricewatch.com/model",last_updated:"2026-09-01"}]};},acceptCatalog:async input=>{accepted=input;}});
 document.body.append(root);expect(calls).toBe(0);
 const open=root.querySelector<HTMLButtonElement>('[data-action="query-catalog"]');expect(open).not.toBeNull();open!.click();await tick();expect(calls).toBe(1);expect(accepted).toBeUndefined();
 const form=root.querySelector<HTMLFormElement>('[aria-label="确认目录价格"]')!;
 const select=form.querySelector<HTMLSelectElement>('select')!;select.value="vendor-model";select.dispatchEvent(new Event("change"));
 form.querySelector<HTMLInputElement>('[name="billing_host"]')!.value="api.vendor.test";form.querySelector<HTMLInputElement>('[name="billing_mode"]')!.value="api";
 form.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}));await tick();expect(accepted).toMatchObject({modelpricewatch_listing_id:"vendor-model",billing_host:"api.vendor.test",billing_mode:"api",billed_model_id:price.billed_model_id,supersedes_snapshot_id:price.snapshot_id});expect(accepted).not.toHaveProperty("rates");root.remove();
});

it("rejects a malformed catalog and only publishes a supplement through bounded stdin with ledger preimage", async()=>{
 const {CliRunner}=await import("../src/cli/runner");
 const ledger=parseMachineLedgerV4(readFileSync("tests/fixtures/v4/machine-ledger-v4.valid.json","utf8"));const price=ledger.pricing_snapshots[0];
 const input={schema_version:1 as const,minimum_reader_version:"0.4.0" as const,project_id:price.project_id,provider:price.provider,session_id:price.session_id,usage_record_digest:price.usage_record_digest,billing_host:price.billing_host,billed_model_id:price.billed_model_id,billing_mode:price.billing_mode,billing_rule_version:price.billing_rule_version,region:price.region,effective_from:"2026-01-01T00:00:00Z",effective_until:null,rates:price.rates,source_url:price.source_url!,detail_url:price.detail_url,audit_reason:"已核对",supersedes_snapshot_id:price.snapshot_id};
 let received="";let argv:readonly string[]=[];
 const runner=new CliRunner("/bin/sr",(_file,args,options,callback)=>{argv=args;expect(options.shell).toBe(false);return {stdin:{end(body:string){received=body;callback(null,JSON.stringify(price),"");}}};});
 expect(typeof runner.supplementPricing).toBe("function");
 await runner.supplementPricing(input,"a".repeat(64));expect(JSON.parse(received)).toEqual(input);expect(argv).toContain("--expected-ledger-sha256");expect(argv).toContain("a".repeat(64));expect(argv).not.toContain("已核对");
 const broken=new CliRunner("/bin/sr",(_file,_args,_options,callback)=>callback(null,'{"schema_version":1,"listings":[]}',""));await expect(broken.getPricingCatalog()).rejects.toThrow("价格目录");
});
