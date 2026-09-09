import type { TimelineEntryV4 } from "../contracts/review-v4";

interface ExactInstant {
  seconds: bigint;
  fraction: string;
}

const RFC3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:\d{2})$/;

export function orderV4Milestones(items: readonly TimelineEntryV4[]): TimelineEntryV4[] {
  return [...items].sort(compareMilestones);
}

export function defaultV4MilestoneId(items: readonly TimelineEntryV4[]): string | null {
  let selected: TimelineEntryV4 | undefined;
  let selectedInstant: ExactInstant | undefined;
  for (const item of items) {
    const instant = parseExactInstant(item.occurred_at);
    if (!instant) continue;
    const comparison = selectedInstant ? compareInstants(instant, selectedInstant) : 1;
    if (comparison > 0 || (comparison === 0 && compareText(item.id, selected!.id) > 0)) {
      selected = item;
      selectedInstant = instant;
    }
  }
  if (selected) return selected.id;
  return [...items].sort((left, right) => compareText(left.id, right.id)).at(-1)?.id ?? null;
}

function compareMilestones(left: TimelineEntryV4, right: TimelineEntryV4): number {
  const leftInstant = parseExactInstant(left.occurred_at);
  const rightInstant = parseExactInstant(right.occurred_at);
  if (leftInstant && rightInstant) return compareInstants(leftInstant, rightInstant) || compareText(left.id, right.id);
  if (leftInstant) return 1;
  if (rightInstant) return -1;
  return compareText(left.id, right.id);
}

function compareInstants(left: ExactInstant, right: ExactInstant): number {
  if (left.seconds !== right.seconds) return left.seconds < right.seconds ? -1 : 1;
  const width = Math.max(left.fraction.length, right.fraction.length);
  return compareText(left.fraction.padEnd(width, "0"), right.fraction.padEnd(width, "0"));
}

function compareText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

function parseExactInstant(value: string): ExactInstant | undefined {
  const match = RFC3339.exec(value);
  if (!match) return undefined;
  const [, yearText, monthText, dayText, hourText, minuteText, secondText, fraction = "", zone] = match;
  const year = Number(yearText);
  const month = Number(monthText);
  const day = Number(dayText);
  const hour = Number(hourText);
  const minute = Number(minuteText);
  const second = Number(secondText);
  if (month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month) || hour > 23 || minute > 59 || second > 59) return undefined;
  if (zone !== "Z" && (Number(zone.slice(1, 3)) > 23 || Number(zone.slice(4, 6)) > 59)) return undefined;
  const wholeSecond = Date.parse(`${yearText}-${monthText}-${dayText}T${hourText}:${minuteText}:${secondText}${zone}`);
  if (!Number.isFinite(wholeSecond)) return undefined;
  return { seconds: BigInt(wholeSecond) / 1000n, fraction };
}

function daysInMonth(year: number, month: number): number {
  if (month === 2) return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28;
  return [4, 6, 9, 11].includes(month) ? 30 : 31;
}
