import { expect, it, vi } from "vitest";
import type { SessionLaunchTarget } from "../src/cli/session-launch";
import { CliRunner } from "../src/cli/runner";
import { renderScanRecords } from "../src/view/render-scan-records";
import { v4IndexFixture } from "./fixtures/v4-shell";
it("routes selected Session to fixed CLI arguments only after explicit confirmation",async()=>{
 const index=v4IndexFixture();const open=vi.fn(async (target: SessionLaunchTarget)=>({...target,schema_version:1 as const,state:"launch_requested" as const}));
 const root=renderScanRecords(index,{sessionLaunch:{open,verify:vi.fn()}});
 const first=root.querySelector<HTMLButtonElement>('[data-action="prepare-session-open"]');expect(first).not.toBeNull();expect(open).not.toHaveBeenCalled();first!.click();expect(open).not.toHaveBeenCalled();root.querySelector<HTMLButtonElement>('[data-action="confirm-session-open"]')!.click();await new Promise(done=>setTimeout(done,0));
 expect(open).toHaveBeenCalledWith(expect.objectContaining({project_id:index.project_id,generation_id:index.generation_id,provider:index.sessions[0].provider,session_id:index.sessions[0].session_id,session_view_digest:index.sessions[0].session_view_digest}));root.dispose();
});
it("uses fixed shell-free launch argv and rejects a foreign returned Session",async()=>{
 const target={project_id:"project-p",provider:"codex" as const,session_id:"session-one",generation_id:"generation-1",session_view_digest:`sha256:${"1".repeat(64)}`};let args:readonly string[]=[]; let foreign = false;
 const runner=new CliRunner("/sr",(_file,argv,options,callback)=>{args=argv;expect(options.shell).toBe(false);callback(null,JSON.stringify({...target,session_id:foreign?"other-session":target.session_id,schema_version:1,state:"launch_requested"}),"");});await runner.openNativeSession(target);expect(args).toEqual(["sessions","open","--project-id","project-p","--provider","codex","--session-id","session-one","--expected-generation-id","generation-1","--expected-session-view-digest",target.session_view_digest,"--json"]); foreign=true; await expect(runner.openNativeSession(target)).rejects.toThrow("invalid Session launch response");
});
