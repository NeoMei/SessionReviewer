import { expect, it, vi } from "vitest";
import { parseDecisionCandidatePage } from "../src/data/contracts-v4";
import { renderDecisionEvidence } from "../src/view/decision-evidence";
import { parseConversationPageV1 } from "../src/data/conversation-page";
import { selectedConversationPage, visibleMessage, visibleTurn } from "./fixtures/conversation";
const digest=`sha256:${"2".repeat(64)}`;
const ref={provider:"codex",session_id:"session-1",session_view_digest:digest,turn_unit_id:"turn-1",revision_id:`sha256:${"4".repeat(64)}`};
it("rejects evidence that has no matching candidate or source dependency",()=>{
 expect(()=>parseDecisionCandidatePage(JSON.stringify({schema_version:1,project_id:"project-p",candidate_evidence:[{candidate_id:"foreign",evidence_refs:[ref],error_code:""}]}),"project-p")).toThrow();
});
it("reads only after clicking and displays the exact cited message",async()=>{
 const page=parseConversationPageV1(JSON.stringify(selectedConversationPage({session_view_digest:digest,turn_unit_id:"turn-1",turn_units:[visibleTurn({user_message:visibleMessage("user",{revision_id:`sha256:${"3".repeat(64)}`})})],messages:[visibleMessage("user",{revision_id:`sha256:${"3".repeat(64)}`,text:"如何恢复可见问答？"}),visibleMessage("assistant",{revision_id:`sha256:${"4".repeat(64)}`,text:"已脱敏支持事实",visible_excerpt:"已脱敏支持事实"})],total:2,range_end:2})));
 const load=vi.fn(async()=>page); const root=renderDecisionEvidence("project-p","generation-1",{candidate_id:"candidate-1",evidence_refs:[ref],error_code:""},load);
 expect(load).not.toHaveBeenCalled();root.querySelector<HTMLButtonElement>("button")!.click();await new Promise(r=>setTimeout(r,0));
 expect(load).toHaveBeenCalledWith(expect.objectContaining({provider:"codex",sessionId:"session-1",sessionViewDigest:digest,turnUnitId:"turn-1"}));
 expect(root.textContent).toContain("已脱敏支持事实");root.dispose();
});
it("does not substitute another revision and ignores a late response after disposal",async()=>{
 const load=vi.fn(async()=>parseConversationPageV1(JSON.stringify(selectedConversationPage({session_view_digest:digest,turn_unit_id:"turn-1",turn_units:[visibleTurn({user_message:visibleMessage("user",{revision_id:`sha256:${"3".repeat(64)}`})})],messages:[visibleMessage("user",{revision_id:`sha256:${"3".repeat(64)}`,text:"如何恢复可见问答？"}),visibleMessage("assistant",{revision_id:`sha256:${"5".repeat(64)}`,text:"错误来源正文"})],total:2,range_end:2,next_cursor:null}))));
 const root=renderDecisionEvidence("project-p","generation-1",{candidate_id:"candidate-1",evidence_refs:[ref],error_code:""},load);root.querySelector<HTMLButtonElement>("button")!.click();await new Promise(r=>setTimeout(r,0));expect(root.textContent).not.toContain("错误来源正文");expect(root.textContent).toContain("未能读取对应证据");root.dispose();
 let resolve!:(value:ReturnType<typeof parseConversationPageV1>)=>void;
 const late=renderDecisionEvidence("project-p","generation-1",{candidate_id:"candidate-1",evidence_refs:[ref],error_code:""},()=>new Promise(done=>{resolve=done}));late.querySelector<HTMLButtonElement>("button")!.click();late.dispose();resolve(parseConversationPageV1(JSON.stringify(selectedConversationPage())));await new Promise(r=>setTimeout(r,0));expect(late.childElementCount).toBe(0);
});
it("accepts exact evidence dependencies and rejects a changed digest",()=>{
 const candidate={id:"candidate-one",project_id:"project-p",annotation_kind:"decision_candidate",entity_id:"decision-one",field:"decision",status:"pending",text:"{}",generation_id:"generation-1",schema_version:1,analysis_profile:"v1",agent_run_id:"run-1",dependencies:[{kind:"session_view",revision_id:"view-one",digest},{kind:"source_turn",revision_id:ref.revision_id,digest}],revision:1,created_at:"2026-09-09T00:00:00Z",confirmed_entity_id:null};
 const page={schema_version:1,project_id:"project-p",candidates:[candidate],candidate_evidence:[{candidate_id:candidate.id,evidence_refs:[ref],error_code:""}]};
 expect(parseDecisionCandidatePage(JSON.stringify(page),"project-p").evidence[0].evidence_refs).toEqual([ref]);
 for(const error_code of [[],["candidate_stale"],null,0]) expect(()=>parseDecisionCandidatePage(JSON.stringify({...page,candidate_evidence:[{...page.candidate_evidence[0],error_code}]}),"project-p")).toThrow();
 page.candidate_evidence[0].evidence_refs=[{...ref,session_view_digest:`sha256:${"9".repeat(64)}`}];expect(()=>parseDecisionCandidatePage(JSON.stringify(page),"project-p")).toThrow();
});
