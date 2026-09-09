import { expect, it, vi } from "vitest";
import { parseSessionLaunchResponse, parseSessionLauncherConfiguration, type SessionLaunchResponse, type SessionLaunchTarget } from "../src/cli/session-launch";
import { renderSessionLaunch } from "../src/view/session-launch";

const target: SessionLaunchTarget = { project_id:"project-p",provider:"codex",session_id:"session-1",generation_id:"generation-1",session_view_digest:"sha256:"+"a".repeat(64) };
const response:SessionLaunchResponse = { schema_version:1,...target,state:"launch_requested" };
const tick=()=>new Promise(done=>setTimeout(done,0));

it("requires explicit click confirmation and submits only the bound Session identity",async()=>{
	const open=vi.fn().mockResolvedValue(response);
	const root=renderSessionLaunch(target,{open,verify:vi.fn()},()=>{});document.body.append(root);
	root.querySelector<HTMLButtonElement>('[data-action="prepare-session-open"]')!.click();expect(open).not.toHaveBeenCalled();expect(root.textContent).toContain("新的交互终端");expect(root.textContent).toContain("不会自动发送消息");
	root.querySelector<HTMLButtonElement>('[data-action="confirm-session-open"]')!.click();await tick();expect(open).toHaveBeenCalledWith(target);expect(root.textContent).toContain("已提交");expect(root.textContent).not.toContain("已恢复成功");
	root.dispose();root.remove();
});

it("verifies only the selected provider and an absolute executable",async()=>{
	const verify=vi.fn().mockResolvedValue({schema_version:1,provider:"codex",version:"fixture",executable:"/opt/codex",identity:{kind:"posix-dev-inode",volume:"1",file:"2"},sha256:"b".repeat(64)});
	const root=renderSessionLaunch(target,{open:vi.fn(),verify},()=>{});document.body.append(root);
	root.querySelector<HTMLDetailsElement>("details")!.open=true;const input=root.querySelector<HTMLInputElement>('[name="session-launcher-executable"]')!;input.value="/opt/codex";root.querySelector<HTMLButtonElement>('[data-action="verify-session-launcher"]')!.click();await tick();
	expect(verify).toHaveBeenCalledWith("codex","/opt/codex");expect(root.textContent).toContain("fixture");root.dispose();root.remove();
});

it("strictly validates launch and launcher identities",()=>{
	expect(parseSessionLaunchResponse(response,target)).toEqual(response);
	expect(()=>parseSessionLaunchResponse({...response,provider:"claude"},target)).toThrow();
	expect(()=>parseSessionLaunchResponse({...response,extra:true},target)).toThrow();
	const configuration={schema_version:1,provider:"codex",version:"fixture",executable:"/opt/codex",identity:{kind:"posix-dev-inode",volume:"1",file:"2"},sha256:"b".repeat(64)};
	expect(parseSessionLauncherConfiguration(configuration,"codex")).toEqual(configuration);
	expect(()=>parseSessionLauncherConfiguration({...configuration,provider:"claude"},"codex")).toThrow();
	expect(()=>parseSessionLauncherConfiguration({...configuration,sha256:"bad"},"codex")).toThrow();
});

it("ignores a late open result after disposal",async()=>{
	let resolve!:(value:typeof response)=>void;const open=()=>new Promise<typeof response>(done=>{resolve=done});let opened=0;
	const root=renderSessionLaunch(target,{open,verify:vi.fn()},()=>{opened++});root.querySelector<HTMLButtonElement>('[data-action="prepare-session-open"]')!.click();root.querySelector<HTMLButtonElement>('[data-action="confirm-session-open"]')!.click();root.dispose();resolve(response);await tick();expect(opened).toBe(0);expect(root.childElementCount).toBe(0);
});
