import { expect,it,vi } from "vitest";
import { renderDecisionExtraction } from "../src/view/decision-extraction";
const tick=()=>new Promise(done=>setTimeout(done,0));
it("does not call an Agent until explicit consent and uses the current revision to cancel",async()=>{
 let starts=0;let cancelled:unknown;
 const job={schema_version:1 as const,job_id:"decision-extract-one",project_id:"project-p",generation_id:"generation-1",state:"running" as const,revision:2,pid:100,dependency_digests:[],candidate_count:0,error_code:"",created_at:"2026-09-09T00:00:00Z",updated_at:"2026-09-09T00:00:00Z"};
 const root=renderDecisionExtraction({start:async()=>{starts++;return job;},status:async()=>job,cancel:async(id,revision)=>{cancelled={id,revision};return {...job,state:"cancelled",revision:3};}},()=>{});document.body.append(root);
 expect(starts).toBe(0);root.querySelector<HTMLButtonElement>('[data-action="prepare-extraction"]')!.click();expect(starts).toBe(0);
 root.querySelector<HTMLButtonElement>('[data-action="confirm-extraction"]')!.click();await tick();expect(starts).toBe(1);root.querySelector<HTMLButtonElement>('[data-action="cancel-extraction"]')!.click();await tick();expect(cancelled).toEqual({id:"decision-extract-one",revision:2});expect(root.textContent).toContain("已取消");root.dispose();root.remove();
});

it("uses fixed extraction argv and rejects a job from another project",async()=>{
 const {CliRunner}=await import("../src/cli/runner");let argv:readonly string[]=[];
 const job={schema_version:1,job_id:"decision-extract-one",project_id:"project-other",generation_id:"generation-1",state:"queued",revision:1,pid:0,dependency_digests:[],candidate_count:0,error_code:"",created_at:"2026-09-09T00:00:00Z",updated_at:"2026-09-09T00:00:00Z"};
 const runner=new CliRunner("/bin/sr",(_file,args,_options,callback)=>{argv=args;callback(null,JSON.stringify(job),"");});expect(typeof runner.startDecisionExtraction).toBe("function");await expect(runner.startDecisionExtraction("project-p","generation-1")).rejects.toThrow();expect(argv).toEqual(["decisions","extract","--project-id","project-p","--expected-generation-id","generation-1","--json"]);
});

it("mounts extraction in the decision tab and disposes polling when leaving",async()=>{
 const {renderV4Shell}=await import("../src/view/render-v4-shell");const {v4PresentationFixture,v4IndexFixture,v4LedgerFixture}=await import("./fixtures/v4-shell");
 const root=renderV4Shell({projectId:"project-p",root:"Projects/P",name:"P",format:"markdown-v4"},v4PresentationFixture(),v4IndexFixture(),v4LedgerFixture(),()=>{}, {initialState:{projectId:"project-p",view:"decisions"},decisionExtraction:{start:async()=>{throw new Error("not requested");},status:async()=>{throw new Error("no job");},cancel:async()=>{throw new Error("no job");}}});document.body.append(root);
 expect(root.querySelector('[data-action="prepare-extraction"]')).not.toBeNull();root.querySelector<HTMLButtonElement>('[data-v4-tab="sessions"]')!.click();expect(root.querySelector('[data-action="prepare-extraction"]')).toBeNull();root.dispose();root.remove();
});

it("loads persisted running job without starting another and ignores completed initial jobs",async()=>{
 let starts=0;let refreshed=0;const job={schema_version:1 as const,job_id:"decision-extract-old",project_id:"project-p",generation_id:"generation-1",state:"completed" as const,revision:4,pid:0,dependency_digests:[],candidate_count:2,error_code:"",created_at:"2026-09-09T00:00:00Z",updated_at:"2026-09-09T00:00:00Z"};
 const root=renderDecisionExtraction({start:async()=>{starts++;return job;},status:async()=>job,cancel:async()=>job,latest:async()=>job},()=>{refreshed++;});document.body.append(root);await tick();expect(root.textContent).toContain("候选 2");expect(starts).toBe(0);expect(refreshed).toBe(0);root.dispose();root.remove();
});

it("requires verified configuration and explicit generation consent separately",async()=>{
 let starts=0;let configured="";const job={schema_version:1 as const,job_id:"decision-extract-new",project_id:"project-p",generation_id:"generation-1",state:"running" as const,revision:2,pid:100,dependency_digests:[],candidate_count:0,error_code:"",created_at:"2026-09-09T00:00:00Z",updated_at:"2026-09-09T00:00:00Z"};
 const root=renderDecisionExtraction({start:async()=>{starts++;return job;},status:async()=>job,cancel:async()=>job,configuration:async(path)=>{if(path)configured=path;return{schema_version:1,kind:"codex",compatible:!!path,version:path?"1.0.0":undefined};}},()=>{});document.body.append(root);await tick();
 const start=root.querySelector<HTMLButtonElement>('[data-action="prepare-extraction"]')!;expect(start.disabled).toBe(true);
 const input=root.querySelector<HTMLInputElement>('[name="agent-executable"]');expect(input).not.toBeNull();input!.value="/Applications/Codex Tool/codex";
 root.querySelector<HTMLButtonElement>('[data-action="configure-agent"]')!.click();await tick();expect(configured).toBe("/Applications/Codex Tool/codex");expect(start.disabled).toBe(false);expect(starts).toBe(0);start.click();expect(starts).toBe(0);root.querySelector<HTMLButtonElement>('[data-action="confirm-extraction"]')!.click();await tick();expect(starts).toBe(1);root.dispose();root.remove();
});

it("reads a saved extraction, cancels its exact revision, and rejects injected executable flags",async()=>{
 const {CliRunner}=await import("../src/cli/runner");let calls=0;let argv:readonly string[]=[];const job={schema_version:1,job_id:"decision-extract-one",project_id:"project-p",generation_id:"generation-1",state:"cancelled",revision:3,pid:0,dependency_digests:[],candidate_count:0,error_code:"",created_at:"2026-09-09T00:00:00Z",updated_at:"2026-09-09T00:00:00Z"};
 const runner=new CliRunner("/bin/sr",(_file,args,options,callback)=>{calls++;argv=args;expect(options.shell).toBe(false);callback(null,JSON.stringify(job),"");});
 expect((await runner.getDecisionExtraction("project-p"))?.job_id).toBe("decision-extract-one");expect(argv).toEqual(["decisions","extract","status","--project-id","project-p","--json"]);
 expect((await runner.cancelDecisionExtraction("project-p","decision-extract-one",2)).state).toBe("cancelled");expect(argv).toEqual(["decisions","extract","cancel","--job-id","decision-extract-one","--expected-revision","2","--json"]);
 await expect(runner.decisionAgentConfiguration("--dangerous-flag")).rejects.toThrow();expect(calls).toBe(2);
});


it("stops polling a running task after its panel is disposed",async()=>{
 vi.useFakeTimers();let polls=0;const job={schema_version:1 as const,job_id:"decision-extract-running",project_id:"project-p",generation_id:"generation-1",state:"running" as const,revision:2,pid:100,dependency_digests:[],candidate_count:0,error_code:"",created_at:"2026-09-09T00:00:00Z",updated_at:"2026-09-09T00:00:00Z"};
 const root=renderDecisionExtraction({start:async()=>job,status:async()=>{polls++;return job;},cancel:async()=>job,initialJob:job},()=>{});
 try {await vi.advanceTimersByTimeAsync(1000);expect(polls).toBe(1);root.dispose();await vi.advanceTimersByTimeAsync(5000);expect(polls).toBe(1);}finally{root.dispose();vi.useRealTimers();}
});

it("retains a deduplicated prior-scan job and labels it without hiding cancellation",async()=>{
 const {CliRunner}=await import("../src/cli/runner");const job={schema_version:1 as const,job_id:"decision-extract-existing",project_id:"project-p",generation_id:"generation-1",state:"running" as const,revision:2,pid:100,dependency_digests:[],candidate_count:0,error_code:"",created_at:"2026-09-09T00:00:00Z",updated_at:"2026-09-09T00:00:00Z"};const runner=new CliRunner("/bin/sr",(_f,_a,_o,callback)=>callback(null,JSON.stringify(job),""));
 await expect(runner.startDecisionExtraction("project-p","generation-2")).resolves.toMatchObject({job_id:job.job_id,generation_id:"generation-1"});
 const root=renderDecisionExtraction({start:async()=>job,status:async()=>job,cancel:async()=>job,initialJob:job,currentGenerationId:"generation-2"},()=>{});expect(root.textContent).toContain("先前扫描");expect(root.querySelector('[data-action="cancel-extraction"]')).not.toBeNull();root.dispose();
});
