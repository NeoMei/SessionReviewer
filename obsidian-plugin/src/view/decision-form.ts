import type { DecisionV4 } from "../contracts/review-v4";
import { button, element } from "./dom";
export type DecisionInput = Pick<DecisionV4, "kind" | "occurred_at" | "title" | "rationale" | "impact" | "reevaluate_when" | "supersedes" | "milestone_ids" | "session_refs" | "pinned"> & { schema_version: 1; status: "active" | "archived" };
export type DecisionSave = (input: DecisionInput, prior?: Pick<DecisionV4, "id" | "revision">) => Promise<unknown>;

export function decisionForm(decisions: DecisionV4[], save: DecisionSave, close: () => void, prior?: DecisionV4): HTMLFormElement {
 const form=element("form",{className:"sr-decision-form",attrs:{"aria-label":prior ? "编辑决策与约定" : "新建决策与约定"}});
 const fields=new Map<string,HTMLInputElement|HTMLTextAreaElement>();
 const field=(key:string,label:string,value:string,multiline=false,required=false)=>{
  const input=multiline?element("textarea"):element("input");input.name=key;input.value=value;input.required=required;input.setAttribute("aria-label",label);fields.set(key,input);form.append(element("label",{className:multiline||key==="title"?"sr-decision-field-wide":"",text:label},[input]));
 };
 const select=(label:string,entries:string[][],value:string)=>{const input=element("select",{attrs:{"aria-label":label}});for(const [key,text] of entries) input.append(element("option",{text,attrs:{value:key}}));input.value=value;form.append(element("label",{text:label},[input]));return input;};
 const kind=select("类型",[["decision","决策"],["agreement","约定"]],prior?.kind??"decision");
 field("title","结论",prior?.title??"",false,true);
 field("occurred_at","确定时间（含时区）",prior?.occurred_at??new Date().toISOString(),false,true);
 field("rationale","理由",prior?.rationale??"",true);
 field("impact","影响范围",prior?.impact??"",true);
 field("reevaluate_when","重新评估条件",prior?.reevaluate_when??"",true);
 const status=select("状态",[["active","生效中"],["archived","已归档"]],prior?.status==="archived"?"archived":"active");
 const pinned=element("input",{attrs:{type:"checkbox","aria-label":"置顶"}});pinned.checked=prior?.pinned??false;form.append(element("label",{text:"置顶"},[pinned]));
 const replacements=element("fieldset",{},[element("legend",{text:"替代已有决策（保留历史）"})]);
 const supersedes=new Map<string,HTMLInputElement>();
 for(const d of decisions.filter(d=>d.id!==prior?.id)){const input=element("input",{attrs:{type:"checkbox","aria-label":`替代：${d.title}`}});input.checked=prior?.supersedes.includes(d.id)??false;supersedes.set(d.id,input);replacements.append(element("label",{text:d.title},[input]));}
 if(supersedes.size)form.append(replacements);
 const feedback=element("p",{attrs:{role:"status"}});const submit=button("确认并保存",{type:"submit"});const cancel=button("取消",{});cancel.addEventListener("click",close);submit.className="sr-decision-primary";cancel.className="sr-decision-secondary";feedback.className="sr-decision-feedback";form.append(element("div",{className:"sr-decision-actions"},[submit,cancel]),feedback);
 form.addEventListener("submit",event=>{event.preventDefault();if(submit.disabled||!form.reportValidity())return;
  const value=(key:string)=>fields.get(key)!.value.trim();
  if(!Number.isFinite(Date.parse(value("occurred_at")))||!/(Z|[+-]\d{2}:\d{2})$/.test(value("occurred_at"))){feedback.textContent="请填写包含时区的有效时间。";return;}
  const targets=[...supersedes].filter(([,input])=>input.checked).map(([id])=>id);
  if(status.value==="archived"&&targets.length){feedback.textContent="归档记录不能替代其他决策，请取消替代选择。";return;}
  const input:DecisionInput={schema_version:1,kind:kind.value as DecisionInput["kind"],occurred_at:value("occurred_at"),title:value("title"),rationale:value("rationale"),impact:value("impact"),reevaluate_when:value("reevaluate_when"),status:status.value as DecisionInput["status"],pinned:pinned.checked,supersedes:targets,milestone_ids:[...(prior?.milestone_ids??[])],session_refs:structuredClone(prior?.session_refs??[])};
  submit.disabled=true;cancel.disabled=true;feedback.textContent="正在保存…";
  void save(input,prior?{id:prior.id,revision:prior.revision}:undefined).then(()=>{feedback.textContent="已保存，正在重新读取。";}).catch(()=>{feedback.textContent="保存结果未确认；请刷新后核对最新内容，输入已保留。";submit.disabled=false;cancel.disabled=false;});
 });return form;
}
