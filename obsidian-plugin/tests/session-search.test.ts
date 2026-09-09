import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { parseSessionIndexV1 } from "../src/data/contracts-v4";
import { renderScanRecords } from "../src/view/render-scan-records";
import type { SessionSearchPage } from "../src/cli/session-search";
const tick = async () => { await new Promise((resolve) => setTimeout(resolve, 0)); };
const fixture = () => parseSessionIndexV1(readFileSync("tests/fixtures/v4/session-index-v1.valid.json", "utf8"));
describe("private Session search", () => {
 it("waits for explicit search and intersects authenticated results with public filters", async () => {
  const index=fixture();
  let calls=0;
  const root=renderScanRecords(index,{loadSessionSearch:async request=>{
   calls++;
   return {schema_version:1,project_id:index.project_id,generation_id:index.generation_id,total:1,items:[{provider:index.sessions[0].provider,session_id:index.sessions[0].session_id,match_kind:request.queryKind}],next_cursor:null,previous_cursor:null};
  }});
  document.body.append(root);
  expect(calls).toBe(0);
  root.querySelector<HTMLInputElement>('[aria-label="搜索分支、文件或错误"]')!.value="src/main.go";
  root.querySelector<HTMLButtonElement>('[data-action="private-session-search"]')!.click();
  await tick();
  expect(calls).toBe(1);
  expect(root.querySelectorAll('.sr-session-list button')).toHaveLength(1);
  root.dispose();
 });
 it("ignores results arriving after clear and disables private search without CLI",async()=>{
  const index=fixture();let resolve!: (page:SessionSearchPage)=>void;
  const root=renderScanRecords(index,{loadSessionSearch:()=>new Promise(done=>{resolve=done;})});
  document.body.append(root);
  root.querySelector<HTMLInputElement>('[aria-label="搜索分支、文件或错误"]')!.value="missing";
  root.querySelector<HTMLButtonElement>('[data-action="private-session-search"]')!.click();
  root.querySelector<HTMLButtonElement>('[data-action="clear-private-search"]')!.click();
  resolve({schema_version:1,project_id:index.project_id,generation_id:index.generation_id,total:0,items:[],next_cursor:null,previous_cursor:null});await tick();
  expect(root.querySelectorAll('.sr-session-list button').length).toBeGreaterThan(0);
  root.dispose();
  const offline=renderScanRecords(index,{cliUnavailable:true});
  expect(offline.querySelector<HTMLButtonElement>('[data-action="private-session-search"]')!.disabled).toBe(true);
  offline.dispose();
 });
});
