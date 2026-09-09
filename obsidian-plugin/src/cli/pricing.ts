import type { PricingSnapshotV1, PricingSupplementV1 } from "../contracts/review-v4";
export interface PricingCatalogListing {
 listing_id:string;provider:string;model:string;category:string;status:string;billing_host:null;billing_mode:null;region:null;
 input_per_mtok:number|null;cached_input_per_mtok:number|null;output_per_mtok:number|null;promo:boolean;promo_until:string|null;price_note:string|null;has_unstructured_condition:boolean;pricing_url:string;detail_url:string;last_updated:string;
}
export interface PricingCatalog {schema_version:1;minimum_reader_version:"0.4.0";status:string;retrieved_at:string;model_count:number;history_count:number;refresh_error:string;listings:PricingCatalogListing[];}
export interface PricingCatalogSelection {schema_version:1;minimum_reader_version:"0.4.0";project_id:string;provider:string;session_id:string;usage_record_digest:string;billing_host:string;billed_model_id:string;billing_mode:string;region:string|null;modelpricewatch_listing_id:string;supersedes_snapshot_id:string|null;}
export interface PricingActions {supplement?:(input:PricingSupplementV1)=>Promise<unknown>;catalog?:()=>Promise<PricingCatalog>;acceptCatalog?:(input:PricingCatalogSelection)=>Promise<unknown>;}
export function validatePricingResult(price:PricingSnapshotV1,input:PricingSupplementV1|PricingCatalogSelection):void {
 if(price.project_id!==input.project_id||price.provider!==input.provider||price.session_id!==input.session_id||price.usage_record_digest!==input.usage_record_digest||price.billed_model_id!==input.billed_model_id||price.billing_host!==input.billing_host||price.billing_mode!==input.billing_mode||price.region!==input.region)throw new Error("pricing response mismatch");
}
export function validateCatalog(value:unknown): PricingCatalog {
 if(!value||typeof value!=="object"||Array.isArray(value))throw new Error("invalid catalog");
 const row=value as Record<string,unknown>;
 if(Object.keys(row).sort().join(",")!=="history_count,listings,minimum_reader_version,model_count,refresh_error,retrieved_at,schema_version,status"||row.schema_version!==1||row.minimum_reader_version!=="0.4.0"||!["current","stale","expired"].includes(String(row.status))||typeof row.retrieved_at!=="string"||!Number.isFinite(Date.parse(row.retrieved_at))||typeof row.refresh_error!=="string"||!Number.isSafeInteger(row.model_count)||!Number.isSafeInteger(row.history_count)||Number(row.history_count)<0||!Array.isArray(row.listings)||row.listings.length!==row.model_count||row.listings.length>4096)throw new Error("invalid catalog");
 const ids=new Set<string>();
 for(const item of row.listings as unknown[]){
  if(!item||typeof item!=="object"||Array.isArray(item))throw new Error("invalid listing");const r=item as Record<string,unknown>;
  if(Object.keys(r).sort().join(",")!=="billing_host,billing_mode,cached_input_per_mtok,category,detail_url,has_unstructured_condition,input_per_mtok,last_updated,listing_id,model,output_per_mtok,price_note,pricing_url,promo,promo_until,provider,region,status")throw new Error("invalid listing");
  for(const key of ["listing_id","provider","model","category","status","last_updated","pricing_url","detail_url"])if(typeof r[key]!=="string"||!r[key]||r[key].length>4096)throw new Error("invalid listing");
  for(const key of ["price_note","promo_until"])if(r[key]!==null&&(typeof r[key]!=="string"||r[key].length>4096))throw new Error("invalid listing");
  for(const key of ["billing_host","billing_mode","region"])if(r[key]!==null)throw new Error("unverified route");
  for(const key of ["input_per_mtok","cached_input_per_mtok","output_per_mtok"])if(r[key]!==null&&(typeof r[key]!=="number"||!Number.isFinite(r[key])||r[key]<0))throw new Error("invalid rate");
  for(const key of ["pricing_url","detail_url"]){const url=new URL(r[key] as string);if(url.protocol!=="https:"||url.username||url.password)throw new Error("invalid URL");}
  if(typeof r.promo!=="boolean"||typeof r.has_unstructured_condition!=="boolean"||typeof r.listing_id!=="string"||!/^[a-z0-9][a-z0-9._:-]{0,127}$/.test(r.listing_id)||ids.has(r.listing_id))throw new Error("invalid listing");ids.add(r.listing_id);
 }return value as PricingCatalog;
}
