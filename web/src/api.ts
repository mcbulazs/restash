// Types mirror the Go API in internal/server.

export interface User {
  id: string;
  email: string;
  name: string;
  admin: boolean;
}

export interface Run {
  at: string;
  ok: boolean;
}

export type ScheduleKind = "hourly" | "daily" | "weekly" | "manual";

export interface Schedule {
  kind: ScheduleKind;
  minute: number; // minutes after midnight, PC time (daily, weekly)
  weekday: number; // 0 = Sunday (weekly)
}

export interface Retention {
  last: number;
  allDays: number;
  dailyDays: number;
  weeklyWeeks: number;
  monthlyMonths: number;
  yearlyYears: number;
}

/** The locked part of an entry. */
export interface EntryConfig {
  name: string;
  paths: string[];
  skips: string[];
  retention: Retention;
}

export interface RunDetail {
  entry: string;
  startedAt: string;
  at: string;
  ok: boolean;
  files: number;
  bytes: number;
  added: number;
  detail?: string; // locked
}

export interface EntryRecord {
  id: string;
  schedule: Schedule;
  config: string; // locked EntryConfig
  createdAt: string;
  updatedAt: string;
  lastOk: RunDetail | null;
}

export interface BrowseResult {
  path: string;
  parent: string;
  dirs: string[];
  home: string;
  sep: string;
  error?: string;
}

export interface PC {
  id: string;
  name: string;
  os: string;
  arch: string;
  agentVersion: string;
  ready: boolean;
  online: boolean;
  createdAt: string;
  lastSeen: string | null;
  lastBackup: string | null;
  lastOk: string | null;
  stored: number;
  recent: Run[];
  schedules: Schedule[];
}

export interface PCDetail extends PC {
  pcKey: string;
  timezone: string;
  entries: EntryRecord[];
  runs: RunDetail[];
}

export interface JoinCode {
  code: string;
  expiresAt: string;
  server: string;
}

export type JoinStatus =
  | { status: "waiting" | "expired" }
  | { status: "joined" | "ready"; pc: { id: string; name: string; os: string } };

export interface Person extends User {
  createdAt: string;
  pcs: number;
  stored: number;
  oldestOk: string | null;
  allSafe: boolean;
}

export interface Invite {
  token: string;
  email: string;
  link: string;
  createdAt: string;
  expiresAt: string;
}

export interface People {
  people: Person[];
  invites: Invite[];
  space: { total: number; used: number };
}

export interface Credentials {
  kdfSalt: string;
  kdfIterations: number;
  auth: string;
  accountKey: string;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "same-origin",
    });
  } catch {
    throw new ApiError(0, "Can't reach the server. Check your connection and try again.");
  }
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error || msg;
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, msg.charAt(0).toUpperCase() + msg.slice(1) + (/[.!?]$/.test(msg) ? "" : "."));
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}

export const api = {
  params: (email: string) => call<{ kdfSalt: string; kdfIterations: number }>("GET", "/api/auth/params?email=" + encodeURIComponent(email)),
  login: (email: string, auth: string) => call<{ user: User; accountKey: string }>("POST", "/api/auth/login", { email, auth }),
  logout: () => call<void>("POST", "/api/auth/logout"),
  me: () => call<{ user: User; accountKey: string; kdfSalt: string; kdfIterations: number }>("GET", "/api/me"),
  invite: (token: string) => call<{ email: string; admin: boolean; expiresAt: string }>("GET", "/api/auth/invite/" + encodeURIComponent(token)),
  signup: (body: { invite: string; email: string; name: string; recoveryAuth: string; recoveryAccountKey: string } & Credentials) =>
    call<{ user: User; accountKey: string }>("POST", "/api/auth/signup", body),
  recoverKey: (email: string, recoveryAuth: string) => call<{ recoveryAccountKey: string }>("POST", "/api/auth/recover/key", { email, recoveryAuth }),
  recover: (email: string, recoveryAuth: string, creds: Credentials) =>
    call<{ user: User; accountKey: string }>("POST", "/api/auth/recover", { email, recoveryAuth, new: creds }),

  pcs: () => call<PC[]>("GET", "/api/pcs"),
  createJoinCode: () => call<JoinCode>("POST", "/api/pcs/join"),
  joinStatus: (code: string) => call<JoinStatus>("GET", "/api/pcs/join/" + encodeURIComponent(code)),

  pc: (id: string) => call<PCDetail>("GET", "/api/pcs/" + encodeURIComponent(id)),
  createEntry: (pc: string, body: { id: string; schedule: Schedule; config: string }) => call<EntryRecord>("POST", `/api/pcs/${encodeURIComponent(pc)}/entries`, body),
  updateEntry: (pc: string, id: string, body: { schedule: Schedule; config: string }) =>
    call<void>("PUT", `/api/pcs/${encodeURIComponent(pc)}/entries/${encodeURIComponent(id)}`, body),
  deleteEntry: (pc: string, id: string) => call<void>("DELETE", `/api/pcs/${encodeURIComponent(pc)}/entries/${encodeURIComponent(id)}`),
  runNow: (pc: string, entry = "") => call<void>("POST", `/api/pcs/${encodeURIComponent(pc)}/run`, { entry }),
  browse: (pc: string, data: string) => call<{ data: string }>("POST", `/api/pcs/${encodeURIComponent(pc)}/browse`, { data }),

  people: () => call<People>("GET", "/api/people"),
  createInvite: (email: string) => call<Invite>("POST", "/api/invites", { email }),
  deleteInvite: (token: string) => call<void>("DELETE", "/api/invites/" + encodeURIComponent(token)),
};
