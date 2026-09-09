import { expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { parseReviewPresentationV4 } from "../src/data/contracts-v4";
import { renderV4Decisions } from "../src/view/render-v4-decisions";
const tick = () => new Promise((done) => setTimeout(done, 0));
const fixture = () => parseReviewPresentationV4(readFileSync("tests/fixtures/v4/review-presentation-v4.valid.json", "utf8"));
it("creates only after explicit submission and preserves human input on failed publication", async () => {
 const p=fixture(); p.decisions=[]; const inputs:unknown[]=[];
 const root=renderV4Decisions(p,()=>{}, {save:async(input)=>{inputs.push(input);throw new Error("/private/path");}});
 document.body.append(root);
 const create=root.querySelector<HTMLButtonElement>('[data-action="create-decision"]');
 expect(create).not.toBeNull();create!.click();expect(inputs).toHaveLength(0);
 const form=root.querySelector<HTMLFormElement>("form")!;
 form.querySelector<HTMLInputElement>('[name="title"]')!.value="保持人工结论";
 form.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}));await tick();
 expect(inputs).toHaveLength(1);expect(inputs[0]).toMatchObject({schema_version:1,title:"保持人工结论",kind:"decision",status:"active",session_refs:[]});
 expect(form.querySelector<HTMLInputElement>('[name="title"]')!.value).toBe("保持人工结论");
 expect(root.textContent).toContain("未保存");expect(root.textContent).not.toContain("/private/path");root.remove();
});
it("edits retain exact source links and require saved decision identity/revision", async()=>{
 const p=fixture();const d={id:"decision-one",kind:"decision" as const,occurred_at:"2026-09-01T00:00:00Z",title:"旧结论",rationale:"依据",impact:"范围",reevaluate_when:"触发条件",status:"active" as const,legacy_status_text:null,supersedes:[],milestone_ids:["milestone-one"],session_refs:[{provider:"codex",session_id:"session-one",turn_unit_ids:["turn-one"]}],provenance:"human_created" as const,pinned:false,revision:2};p.decisions=[d];let saved:unknown;
 const root=renderV4Decisions(p,()=>{},{save:async(input,prior)=>{saved={input,prior};}});document.body.append(root);
 const edit=root.querySelector<HTMLButtonElement>('[data-action="edit-decision"]');expect(edit).not.toBeNull();edit!.click();
 const form=root.querySelector<HTMLFormElement>("form")!;form.querySelector<HTMLInputElement>('[name="title"]')!.value="修订结论";
 form.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}));await tick();
 expect(saved).toMatchObject({input:{title:"修订结论",session_refs:d.session_refs,milestone_ids:d.milestone_ids},prior:{id:d.id,revision:d.revision}});root.remove();
});
