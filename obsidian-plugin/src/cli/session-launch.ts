export type SessionLaunchProvider="codex"|"claude"|"opencode";
export interface SessionLaunchTarget{project_id:string;provider:SessionLaunchProvider;session_id:string;generation_id:string;session_view_digest:string}
export interface SessionLaunchResponse extends SessionLaunchTarget{schema_version:1;state:"launch_requested"}
export interface SessionLauncherConfiguration{schema_version:1;provider:SessionLaunchProvider;version:string;executable:string;identity:{kind:"posix-dev-inode"|"windows-volume-file-id";volume:string;file:string};sha256:string}
export interface SessionLaunchActions{open:(target:SessionLaunchTarget)=>Promise<SessionLaunchResponse>;verify:(provider:SessionLaunchProvider,executable:string)=>Promise<SessionLauncherConfiguration>}

export function parseSessionLaunchResponse(value:unknown,target:SessionLaunchTarget):SessionLaunchResponse{
	const row=record(value,"invalid Session launch response");exactKeys(row,["schema_version","project_id","provider","session_id","generation_id","session_view_digest","state"]);
	if(row.schema_version!==1||row.state!=="launch_requested"||row.project_id!==target.project_id||row.provider!==target.provider||row.session_id!==target.session_id||row.generation_id!==target.generation_id||row.session_view_digest!==target.session_view_digest)throw new Error("invalid Session launch response");
	return row as unknown as SessionLaunchResponse;
}
export function parseSessionLauncherConfiguration(value:unknown,provider:SessionLaunchProvider):SessionLauncherConfiguration{
	const row=record(value,"invalid Session launcher configuration");exactKeys(row,["schema_version","provider","version","executable","identity","sha256"]);const identity=record(row.identity,"invalid Session launcher configuration");exactKeys(identity,["kind","volume","file"]);
	if(row.schema_version!==1||row.provider!==provider||typeof row.version!=="string"||row.version.length<1||row.version.length>256||typeof row.executable!=="string"||!row.executable.startsWith("/")||typeof row.sha256!=="string"||!/^[0-9a-f]{64}$/.test(row.sha256)||identity.kind!=="posix-dev-inode"&&identity.kind!=="windows-volume-file-id"||typeof identity.volume!=="string"||!/^(0|[1-9][0-9]*)$/.test(identity.volume)||typeof identity.file!=="string"||!/^(0|[1-9][0-9]*)$/.test(identity.file))throw new Error("invalid Session launcher configuration");
	return row as unknown as SessionLauncherConfiguration;
}
function record(value:unknown,message:string):Record<string,unknown>{if(!value||typeof value!=="object"||Array.isArray(value))throw new Error(message);return value as Record<string,unknown>}
function exactKeys(row:Record<string,unknown>,allowed:string[]):void{const keys=Object.keys(row);if(keys.length!==allowed.length||keys.some(key=>!allowed.includes(key)))throw new Error("unexpected Session launch field")}
