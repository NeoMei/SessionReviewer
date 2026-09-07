import type { SessionIndexEntryV1 } from "../contracts/review-v4";

export interface SessionBrowserState {
  query: string;
  provider: string | null;
  processingState: "complete" | "partial" | "error" | "unprocessed" | null;
  sourceAvailability: "available" | "unavailable" | null;
  dateFrom: string | null;
  dateTo: string | null;
  unknownDateOnly: boolean;
  page: number;
  selected: { provider: string; sessionId: string } | null;
}

export const DEFAULT_SESSION_BROWSER_STATE: Readonly<SessionBrowserState> = Object.freeze({
  query: "",
  provider: null,
  processingState: null,
  sourceAvailability: null,
  dateFrom: null,
  dateTo: null,
  unknownDateOnly: false,
  page: 0,
  selected: null
});

const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]*$/;
const PROCESSING_STATES = new Set<SessionBrowserState["processingState"]>(["complete", "partial", "error", "unprocessed"]);
const SOURCE_AVAILABILITIES = new Set<SessionBrowserState["sourceAvailability"]>(["available", "unavailable"]);
const encoder = new TextEncoder();

export function normalizeSessionBrowserState(value: unknown): SessionBrowserState {
  if (!record(value)) return { ...DEFAULT_SESSION_BROWSER_STATE };
  const query = boundedText(value.query, 256) ?? "";
  const provider = safeId(value.provider, 128);
  const processingState = typeof value.processingState === "string" && PROCESSING_STATES.has(value.processingState as SessionBrowserState["processingState"])
    ? value.processingState as Exclude<SessionBrowserState["processingState"], null>
    : null;
  const sourceAvailability = typeof value.sourceAvailability === "string" && SOURCE_AVAILABILITIES.has(value.sourceAvailability as SessionBrowserState["sourceAvailability"])
    ? value.sourceAvailability as Exclude<SessionBrowserState["sourceAvailability"], null>
    : null;
  const dateFromSupplied = value.dateFrom !== undefined && value.dateFrom !== null;
  const dateToSupplied = value.dateTo !== undefined && value.dateTo !== null;
  let dateFrom = calendarDate(value.dateFrom);
  let dateTo = calendarDate(value.dateTo);
  const unknownDateOnly = typeof value.unknownDateOnly === "boolean" ? value.unknownDateOnly : false;
  if (unknownDateOnly || (dateFromSupplied && dateFrom === null) || (dateToSupplied && dateTo === null) || (dateFrom !== null && dateTo !== null && dateFrom > dateTo)) {
    dateFrom = null;
    dateTo = null;
  }
  const page = Number.isInteger(value.page) && (value.page as number) >= 0 && (value.page as number) <= 2621 ? value.page as number : 0;
  const selected = record(value.selected)
    ? identity(value.selected.provider, value.selected.sessionId)
    : null;
  return { query, provider, processingState, sourceAvailability, dateFrom, dateTo, unknownDateOnly, page, selected };
}

export function filterSessions(sessions: SessionIndexEntryV1[], state: SessionBrowserState): SessionIndexEntryV1[] {
  const query = state.query.toLocaleLowerCase();
  return sessions.filter((session) => {
    if (state.provider !== null && session.provider !== state.provider) return false;
    if (state.processingState !== null && session.processing_state !== state.processingState) return false;
    if (state.sourceAvailability !== null && session.source_availability !== state.sourceAvailability) return false;
    if (query && !`${session.provider} ${session.session_id}`.toLocaleLowerCase().includes(query)) return false;
    if (state.unknownDateOnly) return session.started_at === null;
    if (state.dateFrom === null && state.dateTo === null) return true;
    if (session.started_at === null) return false;
    const localDate = displayedLocalDate(session.started_at);
    if (localDate === null) return false;
    return (state.dateFrom === null || localDate >= state.dateFrom) && (state.dateTo === null || localDate <= state.dateTo);
  });
}

export function validCalendarDate(value: string): boolean {
  return calendarDate(value) !== null;
}

function displayedLocalDate(value: string): string | null {
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return null;
  return `${date.getFullYear().toString().padStart(4, "0")}-${(date.getMonth() + 1).toString().padStart(2, "0")}-${date.getDate().toString().padStart(2, "0")}`;
}

function calendarDate(value: unknown): string | null {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return null;
  const [year, month, day] = value.split("-").map(Number);
  const date = new Date(year, month - 1, day);
  return date.getFullYear() === year && date.getMonth() === month - 1 && date.getDate() === day ? value : null;
}

function boundedText(value: unknown, maximum: number): string | null {
  return typeof value === "string" && encoder.encode(value).byteLength <= maximum ? value : null;
}

function safeId(value: unknown, maximum: number): string | null {
  return typeof value === "string" && encoder.encode(value).byteLength <= maximum && SAFE_ID.test(value) ? value : null;
}

function identity(provider: unknown, sessionId: unknown): SessionBrowserState["selected"] {
  const safeProvider = safeId(provider, 128);
  const safeSessionId = safeId(sessionId, 128);
  return safeProvider !== null && safeSessionId !== null ? { provider: safeProvider, sessionId: safeSessionId } : null;
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
