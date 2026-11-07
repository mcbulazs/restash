// Formatting for times and sizes, in plain words.

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

const time = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
const monthDay = new Intl.DateTimeFormat("en", { month: "short", day: "numeric" });
const monthDayYear = new Intl.DateTimeFormat("en", { month: "short", day: "numeric", year: "numeric" });

export function startOfDay(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate());
}

/** Whole calendar days between two dates (b - a). */
export function daysBetween(a: Date, b: Date): number {
  return Math.round((startOfDay(b).getTime() - startOfDay(a).getTime()) / DAY);
}

/** "Oct 29", or "Oct 29, 2025" if not this year. */
export function shortDate(d: Date, now = new Date()): string {
  return (d.getFullYear() === now.getFullYear() ? monthDay : monthDayYear).format(d);
}

/** "12 minutes ago", "Today at 03:00", "Yesterday at 21:14", "9 days ago". */
export function when(iso: string, now = new Date()): string {
  const d = new Date(iso);
  const ago = now.getTime() - d.getTime();
  if (ago < MIN) return "Just now";
  if (ago < HOUR) {
    const m = Math.floor(ago / MIN);
    return `${m} minute${m === 1 ? "" : "s"} ago`;
  }
  const days = daysBetween(d, now);
  if (days === 0) return `Today at ${time.format(d)}`;
  if (days === 1) return `Yesterday at ${time.format(d)}`;
  if (days < 30) return `${days} days ago`;
  return shortDate(d, now);
}

/** Decimal units like the OS file managers: "186 GB". */
export function bytes(n: number): string {
  if (n < 1000) return n === 0 ? "0 GB" : `${n} B`;
  const units = ["kB", "MB", "GB", "TB", "PB"];
  let v = n;
  let i = -1;
  do {
    v /= 1000;
    i++;
  } while (v >= 1000 && i < units.length - 1);
  return `${v >= 100 ? Math.round(v) : v >= 10 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

export function minutesLeft(iso: string, now = new Date()): number {
  return Math.max(0, Math.round((new Date(iso).getTime() - now.getTime()) / MIN));
}

/** "about 3 minutes", for time left. */
export function timeLeft(seconds: number): string {
  if (seconds < 60) return "under a minute";
  const m = Math.round(seconds / 60);
  if (m < 60) return `about ${m} minute${m === 1 ? "" : "s"}`;
  const h = Math.floor(m / 60);
  const rest = m % 60;
  return `about ${h} hour${h === 1 ? "" : "s"}${rest >= 5 ? ` ${rest} min` : ""}`;
}

/** "60%", never 100% while it's still running. */
export function percentText(p: number): string {
  return `${Math.min(99, Math.floor(p * 100))}%`;
}
