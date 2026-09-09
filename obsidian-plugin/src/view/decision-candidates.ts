import type { AgentAnnotationEntryV1, DecisionV4 } from "../contracts/review-v4";
import { parseStrictWireDocument } from "../data/contracts-v4";
import { decisionForm, type DecisionInput } from "./decision-form";
import { button, element } from "./dom";
export type DecisionCandidateAction="confirm"|"ignore"|"not_decision"|"restore";
export type DecisionTransition=(candidate:AgentAnnotationEntryV1,action:DecisionCandidateAction,input?:DecisionInput)=>Promise<unknown>;
export function renderDecisionCandidates(candidates:AgentAnnotationEntryV1[],decisions:DecisionV4[],transition?:DecisionTransition):HTMLElement {
 const root=element("section",{attrs:{"aria-label":"决策候选"}},[element("h3",{text:"待确认建议"}),element("p",{text:"建议不会自动成为项目决策；确认前可修改内容。"})]);
 const pending=candidates.filter(c=>c.status==="pending");const history=element("details",{},[element("summary",{text:`历史候选 · ${candidates.length-pending.length}`})]);
 if(!pending.length)root.append(element("p",{text:"暂无待确认建议。"}));
 for(const candidate of candidates){
  let input:DecisionInput|undefined;
  try{input=parseStrictWireDocument(candidate.text,"decision proposal",row=>{
   if(row.schema_version!==1||!["decision","agreement"].includes(String(row.kind))||typeof row.title!=="string"||typeof row.rationale!=="string"||typeof row.impact!=="string"||typeof row.reevaluate_when!=="string"||typeof row.occurred_at!=="string"||!Array.isArray(row.supersedes)||!Array.isArray(row.milestone_ids)||!Array.isArray(row.session_refs)||typeof row.pinned!=="boolean"||!["active","archived"].includes(String(row.status)))throw new Error("invalid proposal");return row as unknown as DecisionInput;
  });}catch{input=undefined;}
  const labels={pending:"待确认",confirmed:"已确认",ignored:"已忽略",not_decision:"非决策",stale:"来源已变化"};
  const card=element("article",{className:"sr-card"},[element("strong",{text:input?.title??"候选内容无法读取"}),element("p",{text:labels[candidate.status]}),element("p",{text:input?.rationale??"请重新提取建议。"})]);
  const feedback=element("p",{attrs:{role:"status"}});const editor=element("div");
  const action=(key:DecisionCandidateAction,label:string)=>{const control=button(label,{"data-action":`${key}-decision-candidate`});control.disabled=!transition||(key==="confirm"&&!input);control.addEventListener("click",()=>{
   if(!transition||control.disabled)return;
   if(key==="confirm"&&input){if(editor.childElementCount)return;const draft:DecisionV4={...input,id:candidate.entity_id??candidate.id,status:input.status,legacy_status_text:null,provenance:"ai_candidate_confirmed",revision:1};editor.append(decisionForm(decisions,body=>transition(candidate,"confirm",body),()=>editor.replaceChildren(),draft));return;}
   control.disabled=true;void transition(candidate,key).then(()=>{feedback.textContent="状态已保存，正在重新读取。";}).catch(()=>{feedback.textContent="保存结果未确认；请刷新后检查。";control.disabled=false;});
  });card.append(control);};
  if(candidate.status==="pending"){action("confirm","确认并编辑");action("ignore","忽略");action("not_decision","这不是决策");}
  if(candidate.status==="ignored"||candidate.status==="not_decision")action("restore","恢复待确认");
  card.append(editor,feedback);(candidate.status==="pending"?root:history).append(card);
 }if(history.childElementCount>1)root.append(history);return root;
}
