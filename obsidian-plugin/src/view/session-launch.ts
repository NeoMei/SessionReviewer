import type{SessionLaunchActions,SessionLaunchTarget}from"../cli/session-launch";
import{button,element}from"./dom";
export type SessionLaunchElement=HTMLElement&{dispose:()=>void};

export function renderSessionLaunch(target:SessionLaunchTarget,actions:SessionLaunchActions,submitted:()=>void):SessionLaunchElement{
	const root=element("section",{attrs:{"aria-label":"打开原 Session"}})as SessionLaunchElement;
	const open=button("打开原 Session",{"data-action":"prepare-session-open"});const status=element("p",{attrs:{role:"status"}});const controls=element("div");
	const configuration=element("details");configuration.append(element("summary",{text:"原 Session 启动器设置"}));const input=element("input",{attrs:{name:"session-launcher-executable","aria-label":`${target.provider} 可执行文件绝对路径`,placeholder:"可执行文件绝对路径"}});const verify=button("验证并保存启动器",{"data-action":"verify-session-launcher"});const info=element("p",{attrs:{role:"status"},text:"验证设置不会打开 Session，也不会发送消息。"});configuration.append(input,verify,info);
	root.append(open,status,controls,configuration);let disposed=false,busy=false,epoch=0;
	const setBusy=(value:boolean)=>{busy=value;open.disabled=value;verify.disabled=value};
	open.addEventListener("click",()=>{if(busy)return;controls.replaceChildren();status.textContent=`将在新的交互终端中恢复这条 ${target.provider} Session；不会自动发送消息。`;const confirm=button("确认打开原 Session",{"data-action":"confirm-session-open"});confirm.addEventListener("click",()=>{if(busy)return;const current=++epoch;setBusy(true);confirm.disabled=true;void actions.open(target).then(()=>{if(disposed||current!==epoch)return;setBusy(false);controls.replaceChildren();status.textContent="打开请求已提交；请在新终端中确认已恢复的会话。";submitted()}).catch(()=>{if(disposed||current!==epoch)return;setBusy(false);controls.replaceChildren();status.textContent="未能提交打开请求；请检查启动器设置并重试。";configuration.open=true})});controls.append(confirm)});
	verify.addEventListener("click",()=>{if(busy)return;const executable=input.value.trim();if(!executable.startsWith("/")){info.textContent="请输入绝对可执行文件路径。";return}const current=++epoch;setBusy(true);void actions.verify(target.provider,executable).then(value=>{if(disposed||current!==epoch)return;setBusy(false);input.value=value.executable;info.textContent=`启动器已验证 · ${value.version}`}).catch(()=>{if(disposed||current!==epoch)return;setBusy(false);info.textContent="启动器验证失败；未保存配置。"})});
	root.dispose=()=>{disposed=true;epoch++;root.replaceChildren()};return root;
}
