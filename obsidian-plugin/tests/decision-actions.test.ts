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
 expect(root.textContent).toContain("保存结果未确认");expect(root.textContent).not.toContain("/private/path");root.remove();
});
it("edits retain exact source links and require saved decision identity/revision", async()=>{
 const p=fixture();const d={id:"decision-one",kind:"decision" as const,occurred_at:"2026-09-01T00:00:00Z",title:"旧结论",rationale:"依据",impact:"范围",reevaluate_when:"触发条件",status:"active" as const,legacy_status_text:null,supersedes:[],milestone_ids:["milestone-one"],session_refs:[{provider:"codex",session_id:"session-one",turn_unit_ids:["turn-one"]}],provenance:"human_created" as const,pinned:false,revision:2};p.decisions=[d];let saved:unknown;
 const root=renderV4Decisions(p,()=>{},{save:async(input,prior)=>{saved={input,prior};}});document.body.append(root);
 const edit=root.querySelector<HTMLButtonElement>('[data-action="edit-decision"]');expect(edit).not.toBeNull();edit!.click();
 const form=root.querySelector<HTMLFormElement>("form")!;form.querySelector<HTMLInputElement>('[name="title"]')!.value="修订结论";
 form.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}));await tick();
 expect(saved).toMatchObject({input:{title:"修订结论",session_refs:d.session_refs,milestone_ids:d.milestone_ids},prior:{id:d.id,revision:d.revision}});root.remove();
});

it("saves manual decisions through stdin with review and entity revisions",async()=>{
 const {CliRunner}=await import("../src/cli/runner");let argv:readonly string[]=[];let body="";
 const runner=new CliRunner("/bin/sr",(_file,args,options,callback)=>{argv=args;expect(options.shell).toBe(false);return{stdin:{end(input:string){body=input;callback(null,JSON.stringify({schema_version:1,project_id:"project-p",review_sha256:"b".repeat(64),decisions:[]}),"");}}};});
 expect(typeof runner.saveDecision).toBe("function");
 const input={schema_version:1 as const,kind:"decision" as const,occurred_at:"2026-09-01T00:00:00Z",title:"新的结论",rationale:"依据",impact:"范围",reevaluate_when:"条件",status:"active" as const,supersedes:[],milestone_ids:[],session_refs:[],pinned:false};
 await runner.saveDecision("project-p","a".repeat(64),input,{id:"decision-one",revision:2});expect(JSON.parse(body)).toEqual(input);expect(argv).toEqual(["decisions","edit","--project-id","project-p","--expected-review-sha256","a".repeat(64),"--decision-id","decision-one","--expected-decision-revision","2","--json"]);
});

it("keeps a decision candidate private until a confirmation form is submitted",async()=>{
 const p=fixture();let action:unknown;
 const input={schema_version:1 as const,kind:"decision" as const,occurred_at:"2026-09-01T00:00:00Z",title:"候选建议",rationale:"依据",impact:"范围",reevaluate_when:"条件",status:"active" as const,supersedes:[],milestone_ids:[],session_refs:[],pinned:false};
 const candidate={id:"candidate-one",project_id:p.project_id,annotation_kind:"decision_candidate" as const,status:"pending" as const,text:JSON.stringify(input),generation_id:p.generation_id,schema_version:1 as const,analysis_profile:"v1",agent_run_id:"run-1",dependencies:[],revision:1,created_at:"2026-09-01T00:00:00Z",confirmed_entity_id:null};
 const root=renderV4Decisions(p,()=>{},{candidates:[candidate],transition:async(c,a,body)=>{action={c,a,body};}});document.body.append(root);
 const confirm=root.querySelector<HTMLButtonElement>('[data-action="confirm-decision-candidate"]');expect(confirm).not.toBeNull();confirm!.click();expect(action).toBeUndefined();
 const form=root.querySelector<HTMLFormElement>("form")!;form.querySelector<HTMLInputElement>('[name="title"]')!.value="人确认后的结论";form.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}));await tick();expect(action).toMatchObject({a:"confirm",body:{title:"人确认后的结论"}});root.remove();
});

it("lists empty private candidates and sends confirm payload with both preimages",async()=>{
 const {CliRunner}=await import("../src/cli/runner");let argv:readonly string[]=[];let body="";
 const runner=new CliRunner("/bin/sr",(_file,args,_options,callback)=>{argv=args;if(args[1]==="candidates"){callback(null,'{"schema_version":1,"project_id":"project-p"}',"");return;}return{stdin:{end(input:string){body=input;callback(null,'{"schema_version":1,"project_id":"project-p"}',"");}}};});
 expect(typeof runner.listDecisionCandidates).toBe("function");await expect(runner.listDecisionCandidates("project-p")).resolves.toEqual([]);
 await runner.transitionDecisionCandidate("project-p","a".repeat(64),{id:"candidate-one",revision:2},"ignore");expect(body).toBe("");expect(argv).toContain("--expected-revision");expect(argv).toContain("2");expect(argv).toContain("--expected-review-sha256");
});

it("only offers restore for ignored candidates, keeping not-decision terminal",async()=>{
 const {renderDecisionCandidates}=await import("../src/view/decision-candidates");
 const base={id:"candidate-one",project_id:"project-p",annotation_kind:"decision_candidate" as const,text:"{}",generation_id:"generation-1",schema_version:1 as const,analysis_profile:"v1",agent_run_id:"run-1",dependencies:[],revision:2,created_at:"2026-09-01T00:00:00Z",confirmed_entity_id:null};
 const root=renderDecisionCandidates([{...base,status:"not_decision"},{...base,id:"candidate-two",status:"ignored"}],[],async()=>{});
 expect(root.querySelectorAll('[data-action="restore-decision-candidate"]')).toHaveLength(1);
});

it("shows decision status, provenance, source sessions and replacement history",()=>{
 const p=fixture();
 const base={kind:"decision" as const,occurred_at:"2026-09-09T00:00:00Z",rationale:"依据",impact:"范围",reevaluate_when:"条件",legacy_status_text:null,milestone_ids:[],session_refs:[{provider:"claude",session_id:"session-one",turn_unit_ids:["turn-one"]}],pinned:false,revision:1};
 p.decisions=[{...base,id:"old",title:"旧方案",status:"superseded",supersedes:[],provenance:"human_created"},{...base,id:"new",title:"新方案",status:"active",supersedes:["old"],provenance:"ai_candidate_confirmed"}];
 const root=renderV4Decisions(p,()=>{});
 expect(root.textContent).toContain("来源：AI 候选经人工确认");
 expect(root.textContent).toContain("替代：旧方案");
 expect(root.textContent).toContain("claude / session-one");
 Array.from(root.querySelectorAll("button")).find(e=>e.textContent==="已替代 / 已归档")!.click();
 expect(root.textContent).toContain("状态：已替代");
 expect(root.textContent).toContain("已被替代：新方案");
 expect(root.textContent).toContain("来源：人工创建");
 root.dispose();
});
