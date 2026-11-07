// Unlocking and locking a PC's entries in the browser, and describing them
// in plain words. Entry names, folders, skips and run details are locked
// with a key only this browser (after login) and the PC can make.
import { api, type BrowseResult, type EntryConfig, type EntryRecord, type PCDetail, type Retention, type Schedule } from "./api.ts";
import { forgetKeys, getAccountKey } from "./account.ts";
import { entryKey, labelBrowse, labelEntry, labelPCKey, labelRunDetail, LockedError, open, randomBytes, seal } from "./vault.ts";

const enc = new TextEncoder();
const dec = new TextDecoder();

export class NeedUnlock extends Error {
  constructor() {
    super("Type your password to unlock this PC's details.");
  }
}

/** A PC's own key. Throws NeedUnlock if the account key is gone, or doesn't
 * fit (one left over from another login): the page then asks for the
 * password instead of failing. */
export async function openPCKey(pc: Pick<PCDetail, "id" | "pcKey">): Promise<Uint8Array> {
  const account = getAccountKey();
  if (!account) throw new NeedUnlock();
  if (!pc.pcKey) throw new Error("This PC hasn't been unlocked on the PC yet.");
  try {
    return await open(account, pc.pcKey, labelPCKey(pc.id));
  } catch (e) {
    if (!(e instanceof LockedError)) throw e;
    forgetKeys();
    throw new NeedUnlock();
  }
}

/** The entry key of a PC; see openPCKey for what it throws. */
export async function pcEntryKey(pc: Pick<PCDetail, "id" | "pcKey">): Promise<Uint8Array> {
  return entryKey(await openPCKey(pc));
}

export interface Entry {
  record: EntryRecord;
  config: EntryConfig | null; // null if it couldn't be unlocked
}

export async function openEntries(key: Uint8Array, pcID: string, records: EntryRecord[]): Promise<Entry[]> {
  return Promise.all(
    records.map(async (record) => {
      try {
        const plain = await open(key, record.config, labelEntry(pcID, record.id));
        return { record, config: JSON.parse(dec.decode(plain)) as EntryConfig };
      } catch {
        return { record, config: null };
      }
    }),
  );
}

export function sealConfig(key: Uint8Array, pcID: string, entryID: string, cfg: EntryConfig): Promise<string> {
  return seal(key, enc.encode(JSON.stringify(cfg)), labelEntry(pcID, entryID));
}

export async function openDetail(key: Uint8Array, pcID: string, detail?: string): Promise<string> {
  if (!detail) return "";
  try {
    return dec.decode(await open(key, detail, labelRunDetail(pcID)));
  } catch {
    return "";
  }
}

export async function browse(key: Uint8Array, pcID: string, path: string): Promise<BrowseResult> {
  const label = labelBrowse(pcID);
  const req = await seal(key, enc.encode(JSON.stringify({ path })), label);
  const res = await api.browse(pcID, req);
  return JSON.parse(dec.decode(await open(key, res.data, label)));
}

const ID_ALPHABET = "abcdefghijklmnopqrstuvwxyz234567";

export function newEntryID(): string {
  return Array.from(randomBytes(16), (b) => ID_ALPHABET[b & 31]).join("");
}

export const DEFAULT_RETENTION: Retention = { last: 0, allDays: 2, dailyDays: 30, weeklyWeeks: 0, monthlyMonths: 24, yearlyYears: 0 };
export const DEFAULT_SCHEDULE: Schedule = { kind: "hourly", minute: 12 * 60, weekday: 0 };

// ---------- Schedules (same rules as internal/schedule) ----------

function atMinute(day: Date, minute: number): Date {
  return new Date(day.getFullYear(), day.getMonth(), day.getDate(), Math.floor(minute / 60), minute % 60);
}

function addDays(d: Date, n: number): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n, d.getHours(), d.getMinutes());
}

/** The most recent time the schedule asked for a run (null for hourly/manual). */
export function lastScheduled(s: Schedule, now = new Date()): Date | null {
  if (s.kind === "daily") {
    const t = atMinute(now, s.minute);
    return t > now ? addDays(t, -1) : t;
  }
  if (s.kind === "weekly") {
    const back = (now.getDay() - s.weekday + 7) % 7;
    let t = atMinute(addDays(now, -back), s.minute);
    if (t > now) t = addDays(t, -7);
    return t;
  }
  return null;
}

/** Is the entry behind its schedule (by more than an hour of slack)? */
export function isLate(s: Schedule, lastOk: string | null, createdAt: string, now = new Date()): boolean {
  const slack = 3600_000;
  if (s.kind === "manual") return false;
  const since = lastOk ? new Date(lastOk) : new Date(createdAt);
  if (s.kind === "hourly") return now.getTime() - since.getTime() > 2 * 3600_000 + slack;
  const due = lastScheduled(s, now)!;
  return since < due && now.getTime() - due.getTime() > slack;
}

// ---------- Plain words ----------

export const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

export function clock(minute: number): string {
  return `${String(Math.floor(minute / 60)).padStart(2, "0")}:${String(minute % 60).padStart(2, "0")}`;
}

export function scheduleText(s: Schedule): string {
  switch (s.kind) {
    case "hourly":
      return "Every hour while the PC is on.";
    case "daily":
      return s.minute < 6 * 60 || s.minute >= 21 * 60 ? `Every night at ${clock(s.minute)}.` : `Every day at ${clock(s.minute)}.`;
    case "weekly":
      return `Every ${WEEKDAYS[s.weekday]} at ${clock(s.minute)}.`;
    default:
      return "Only when you ask.";
  }
}

/** Short form for the PC list. */
export function schedulesSummary(list: Schedule[]): string {
  if (list.length === 0) return "No entries yet";
  if (list.length === 1) return scheduleText(list[0]).replace(/\.$/, "");
  return `${list.length} entries`;
}

function span(n: number, unit: "day" | "week" | "month" | "year"): string {
  if (unit === "month" && n >= 12 && n % 12 === 0) return span(n / 12, "year");
  if (unit === "day" && n >= 14 && n % 7 === 0 && n < 60) return span(n / 7, "week");
  return `${n} ${unit}${n === 1 ? "" : "s"}`;
}

export function retentionText(r: Retention): string {
  const steps: string[] = [];
  if (r.allDays) steps.push(`every backup for ${span(r.allDays, "day")}`);
  if (r.dailyDays) steps.push(`one a day for ${span(r.dailyDays, "day")}`);
  if (r.weeklyWeeks) steps.push(`one a week for ${span(r.weeklyWeeks, "week")}`);
  if (r.monthlyMonths) steps.push(`one a month for ${span(r.monthlyMonths, "month")}`);
  if (r.yearlyYears) steps.push(`one a year for ${span(r.yearlyYears, "year")}`);
  let text = steps.length ? "Keeps " + steps.map((s, i) => (i > 0 ? "then " + s : s)).join(", ") : "";
  if (r.last) text = text ? `${text}, and always the last ${r.last}` : `Keeps the last ${r.last} backup${r.last === 1 ? "" : "s"}`;
  return text ? text + "." : "Keeps nothing.";
}

/** "You can go back to any hour of the last 2 days, any day of..." */
export function goBackText(r: Retention, s: Schedule): string {
  const parts: string[] = [];
  const unit = s.kind === "hourly" ? "hour" : "backup";
  if (r.allDays) parts.push(`any ${unit} of the last ${span(r.allDays, "day")}`);
  if (r.dailyDays) parts.push(`any day of the last ${r.dailyDays === 30 || r.dailyDays === 31 ? "month" : span(r.dailyDays, "day")}`);
  if (r.weeklyWeeks) parts.push(`any week of the last ${span(r.weeklyWeeks, "week")}`);
  if (r.monthlyMonths) parts.push(`any month of the last ${span(r.monthlyMonths, "month")}`);
  if (r.yearlyYears) parts.push(`any year of the last ${span(r.yearlyYears, "year")}`);
  if (r.last) parts.push(`the last ${r.last} backup${r.last === 1 ? "" : "s"}`);
  if (!parts.length) return "";
  const list = parts.length > 1 ? parts.slice(0, -1).join(", ") + ", and " + parts[parts.length - 1] : parts[0];
  return `You can go back to ${list}.`;
}

export function skipsText(skips: string[]): string {
  if (!skips.length) return "";
  const list = skips.length > 1 ? skips.slice(0, -1).join(", ") + " and " + skips[skips.length - 1] : skips[0];
  return `Skips ${list}.`;
}

export function plainWords(name: string, s: Schedule, r: Retention): string {
  const n = name.trim() || "This entry";
  const when =
    s.kind === "manual" ? "is backed up only when you ask" : `is backed up ${scheduleText(s).replace(/^Every/, "every").replace(/\.$/, "")}`;
  return `${n} ${when}. ${goBackText(r, s)}`.trim();
}

/** Shortens a long path from the start, keeping the end that names it. */
export function shortPath(p: string, max = 48): string {
  return p.length <= max ? p : "…" + p.slice(p.length - max + 1);
}

/** restic's messages as sentences: "x does not exist, skipping. All source..." */
export function detailText(detail: string, maxLines = 3): string {
  return detail
    .split("\n")
    .map((l) => l.trim().replace(/^Fatal:\s*/, ""))
    .filter(Boolean)
    .slice(0, maxLines)
    .map((l) => {
      const t = l.charAt(0).toUpperCase() + l.slice(1);
      return /[.!?…:]$/.test(t) ? t : t + ".";
    })
    .join(" ");
}
