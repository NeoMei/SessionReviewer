import type { PricingSnapshotV1 } from "../contracts/review-v4";
import type { PricingActions, PricingCatalogSelection } from "../cli/pricing";
import { button, element } from "./dom";
export function pricingCatalogForm(price:PricingSnapshotV1,actions:PricingActions):HTMLElement {
 const replacing=!["pending","ambiguous"].includes(price.status);
 const root=element("div");const open=button(replacing?"查询并替换当前价格":"查询 ModelPriceWatch 价格",{"data-action":"query-catalog"});const feedback=element("p",{attrs:{role:"status"}});root.append(open,feedback);
 open.addEventListener("click",()=>{if(open.disabled||!actions.catalog||!actions.acceptCatalog)return;open.disabled=true;feedback.textContent="正在读取公开价格目录…";
  void actions.catalog().then(catalog=>{
   feedback.textContent=`目录查询时间：${catalog.retrieved_at} · ${catalog.status==="current"?"缓存有效":catalog.status==="stale"?"过期缓存估算":"目录已过期"}`;
   if(catalog.status==="expired"){feedback.textContent="价格目录已过期，暂不能用于确认价格；请稍后重试或人工补充。";open.disabled=false;return;}
   const form=element("form",{attrs:{"aria-label":"确认目录价格"}});form.append(element("p",{text:`为 ${price.provider} / ${price.session_id} 的 ${price.billed_model_id} 选择价格。请按实际服务商和调用模式核对，名称相似不会自动匹配。`}));
   const filter=element("input",{attrs:{"aria-label":"筛选价格目录",placeholder:"输入服务商或模型名称"}});const select=element("select",{attrs:{"aria-label":"ModelPriceWatch 价格条目",required:"true"}});
   const populate=()=>{const old=select.value;select.replaceChildren(element("option",{text:"请选择价格条目",attrs:{value:""}}));for(const row of catalog.listings.filter(row=>`${row.provider} ${row.model} ${row.listing_id}`.toLowerCase().includes(filter.value.toLowerCase())))select.append(element("option",{text:`${row.provider} / ${row.model} (${row.listing_id})`,attrs:{value:row.listing_id}}));select.value=[...select.options].some(o=>o.value===old)?old:"";};populate();filter.addEventListener("input",populate);
   const detail=element("div");select.addEventListener("change",()=>{detail.replaceChildren();const row=catalog.listings.find(r=>r.listing_id===select.value);if(!row)return;detail.append(element("p",{text:`每百万 Token：输入 ${row.input_per_mtok??"未知"} / 缓存 ${row.cached_input_per_mtok??"未知"} / 输出 ${row.output_per_mtok??"未知"} USD · ${row.price_note??"请核对官方适用条件"}`}),element("p",{text:row.promo?`促销截止：${row.promo_until??"未明确"}`:"非促销价格"}),element("p",{text:row.has_unstructured_condition?"存在尚未结构化的适用条件，确认后可能仍为待定。":""}),element("a",{text:"服务商价格依据",attrs:{href:row.pricing_url}}));});
   form.append(filter,select,detail);const inputs=new Map<string,HTMLInputElement>();
   for(const [name,label] of [["billing_host","实际计费主机（如 api.openai.com）"],["billing_mode","实际调用模式（如 api）"],["region","适用区域（无区域限制可留空）"]]){const input=element("input",{attrs:{name,"aria-label":label}});input.required=name!=="region";input.value=name==="billing_host"?(price.billing_host==="unknown"?"":price.billing_host):name==="billing_mode"?(price.billing_mode==="unknown"?"":price.billing_mode):price.region??"";inputs.set(name,input);form.append(element("label",{text:label},[input]));}
   const replacement=element("input",{attrs:{type:"checkbox","aria-label":"确认替换当前价格，旧快照保留"}});replacement.required=replacing;if(replacing)form.append(element("label",{text:"确认替换当前价格，旧快照保留"},[replacement]));
   const submit=button("确认路由并保存目录价格",{type:"submit"});const cancel=button("取消",{});cancel.addEventListener("click",()=>{form.remove();open.disabled=false;});form.append(submit,cancel);
   form.addEventListener("submit",event=>{event.preventDefault();if(submit.disabled||(replacing&&!replacement.checked)||!form.reportValidity()||!catalog.listings.some(r=>r.listing_id===select.value))return;
    const input:PricingCatalogSelection={schema_version:1,minimum_reader_version:"0.4.0",project_id:price.project_id,provider:price.provider,session_id:price.session_id,usage_record_digest:price.usage_record_digest,billed_model_id:price.billed_model_id,billing_host:inputs.get("billing_host")!.value.trim(),billing_mode:inputs.get("billing_mode")!.value.trim(),region:inputs.get("region")!.value.trim()||null,modelpricewatch_listing_id:select.value,supersedes_snapshot_id:price.snapshot_id};
    submit.disabled=true;cancel.disabled=true;feedback.textContent="正在核对历史价格并保存…";void actions.acceptCatalog!(input).then(()=>{feedback.textContent="已保存定价结果，正在重新读取；不适用的价格仍显示待定。";}).catch(()=>{feedback.textContent="保存结果未确认；请刷新账本检查，输入已保留。";submit.disabled=false;cancel.disabled=false;});
   });root.append(form);
  }).catch(()=>{feedback.textContent="价格目录暂不可用；已有费用与用量记录保留，可稍后重试或补充价格。";open.disabled=false;});
 });return root;
}
