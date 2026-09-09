import { SESSION_ID } from "../contracts/session-identity";
export interface SessionSearchRequest {
  projectId: string;
  expectedGenerationId: string;
  queryKind: "branch" | "file" | "error";
  query: string;
  limit: number;
  cursor?: string;
}
export interface SessionSearchPage {
  schema_version: 1;
  project_id: string;
  generation_id: string;
  total: number;
  items: Array<{ provider: string; session_id: string; match_kind: SessionSearchRequest["queryKind"] }>;
  next_cursor: string | null;
  previous_cursor: string | null;
}
export type SessionSearchLoader = (request: SessionSearchRequest) => Promise<SessionSearchPage>;
export function validateSearchPage(page: SessionSearchPage, request: SessionSearchRequest): void {
  if (!page || Object.keys(page).sort().join(",") !== "generation_id,items,next_cursor,previous_cursor,project_id,schema_version,total" || page.schema_version !== 1 || page.project_id !== request.projectId || page.generation_id !== request.expectedGenerationId || !Number.isSafeInteger(page.total) || page.total < 0 || !Array.isArray(page.items) || page.items.length > request.limit || page.items.length > page.total) throw new Error("search response mismatch");
  const seen = new Set<string>();
  for (const item of page.items) {
    if (!item || Object.keys(item).sort().join(",") !== "match_kind,provider,session_id" || item.match_kind !== request.queryKind || !safeId(item.provider) || (typeof item.session_id !== "string" || !SESSION_ID.test(item.session_id))) throw new Error("search result identity mismatch");
    const key = `${item.provider}\0${item.session_id}`;
    if (seen.has(key)) throw new Error("duplicate search result");
    seen.add(key);
  }
  for (const cursor of [page.next_cursor, page.previous_cursor]) if (cursor !== null && (typeof cursor !== "string" || cursor.length === 0 || new TextEncoder().encode(cursor).length > 4096)) throw new Error("invalid search cursor");
  if ((page.total === 0 && (page.items.length !== 0 || page.next_cursor !== null || page.previous_cursor !== null)) || (page.total > 0 && page.items.length === 0) || (!request.cursor && page.previous_cursor !== null)) throw new Error("invalid search topology");
}
function safeId(value: unknown): value is string { return typeof value === "string" && /^[a-z0-9][a-z0-9._:-]{0,127}$/.test(value); }
