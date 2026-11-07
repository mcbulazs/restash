import { useEffect, useRef, useState, type InputHTMLAttributes, type ReactNode } from "react";
import type { Run, User } from "../api.ts";
import { daysBetween, startOfDay } from "../format.ts";
import { Link, navigate, usePath } from "../router.tsx";
import { unlock } from "../account.ts";

// ---------- Icons (24px, stroked) ----------

function Svg({ children, size = 20 }: { children: ReactNode; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  );
}

export const Icon = {
  plus: () => (
    <Svg>
      <path d="M12 5v14M5 12h14" />
    </Svg>
  ),
  download: () => (
    <Svg>
      <path d="M12 4v11M7 10l5 5 5-5M5 20h14" />
    </Svg>
  ),
  lock: (p: { size?: number }) => (
    <Svg size={p.size}>
      <rect x="5" y="11" width="14" height="9" rx="2" />
      <path d="M8 11V8a4 4 0 0 1 8 0v3" />
    </Svg>
  ),
  shield: () => (
    <Svg>
      <path d="M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z" />
    </Svg>
  ),
  warning: () => (
    <Svg>
      <path d="M12 4l9 16H3z" />
      <path d="M12 10v4M12 17.5v.5" />
    </Svg>
  ),
  file: () => (
    <Svg size={18}>
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
      <path d="M14 3v5h5" />
    </Svg>
  ),
  clock: () => (
    <Svg>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </Svg>
  ),
  folder: () => (
    <Svg>
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
    </Svg>
  ),
  calendar: () => (
    <Svg>
      <rect x="4" y="5" width="16" height="15" rx="2" />
      <path d="M4 10h16M9 3v4M15 3v4" />
    </Svg>
  ),
  x: (p: { size?: number }) => (
    <Svg size={p.size}>
      <path d="M6 6l12 12M18 6L6 18" />
    </Svg>
  ),
  chevron: () => (
    <Svg>
      <path d="M9 6l6 6-6 6" />
    </Svg>
  ),
  up: () => (
    <Svg>
      <path d="M12 19V5M6 11l6-6 6 6" />
    </Svg>
  ),
  home: () => (
    <Svg>
      <path d="M4 11l8-7 8 7v9h-5v-6H9v6H4z" />
    </Svg>
  ),
  check: () => (
    <Svg>
      <path d="M5 12.5l4.5 4.5L19 7.5" />
    </Svg>
  ),
  key: () => (
    <Svg>
      <circle cx="8" cy="15" r="4" />
      <path d="M11 12l8-8M16 7l2 2M14 9l2 2" />
    </Svg>
  ),
  refresh: () => (
    <Svg>
      <path d="M4 12a8 8 0 1 0 2.5-5.8M4 4v5h5" />
    </Svg>
  ),
};

/** Asks for the password again when this tab has no unlocked key. */
export function UnlockCard({ onDone, what }: { onDone: () => void; what: string }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  return (
    <div className="card card-pad unlock">
      <h2>Unlock {what}</h2>
      <p className="muted" style={{ margin: "8px 0 18px" }}>
        Names and folders are encrypted. Type your password to open them here; it stays on this page.
      </p>
      <form
        className="invite-form"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError(null);
          try {
            await unlock(password);
            onDone();
          } catch (err) {
            setError(err);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Field label="Password" type="password" name="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} autoFocus />
        <button className="btn btn-primary" disabled={busy}>
          {busy ? "Unlocking…" : "Unlock"}
        </button>
        <div style={{ flexBasis: "100%" }}>
          <ErrorBox error={error} />
        </div>
      </form>
    </div>
  );
}

// ---------- Brand ----------

export function Logo({ href = "/" }: { href?: string }) {
  return (
    <Link href={href} className="logo" aria-label="restash home">
      <span className="logo-mark">
        <i />
        <i />
        <i />
      </span>
      <span className="logo-word">restash</span>
    </Link>
  );
}

/** Initial in a circle; c picks one of three washes. */
export function Avatar({ name, c = 0, big }: { name: string; c?: number; big?: boolean }) {
  return <span className={`avatar ${c % 3 ? "c" + (c % 3) : ""} ${big ? "big" : ""}`}>{name.trim().charAt(0).toUpperCase() || "?"}</span>;
}

// ---------- App shell ----------

export function TopBar({ user, onLogout }: { user: User; onLogout: () => void }) {
  const path = usePath();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);
  const nav = [{ href: "/", label: "Your PCs", active: path === "/" || path.startsWith("/add") || path.startsWith("/pcs/") }];
  nav.push({ href: "/activity", label: "Activity", active: path.startsWith("/activity") });
  if (user.admin) nav.push({ href: "/people", label: "People", active: path.startsWith("/people") });
  return (
    <header className="topbar">
      <Logo />
      <nav className="nav">
        {nav.map((n) => (
          <Link key={n.href} href={n.href} className={n.active ? "active" : ""}>
            {n.label}
          </Link>
        ))}
      </nav>
      <div className="spacer" />
      <div className="user-menu" ref={ref}>
        <button className="user-button" onClick={() => setOpen(!open)} aria-expanded={open}>
          <span className="name">{user.name}</span>
          <Avatar name={user.name} />
        </button>
        {open && (
          <div className="menu">
            <div className="menu-head">
              <div className="cell-main">{user.name}</div>
              <div className="small truncate">{user.email}</div>
            </div>
            <button onClick={onLogout}>Log out</button>
          </div>
        )}
      </div>
    </header>
  );
}

// ---------- Tally: the last 30 nights ----------

export type Night = "ok" | "failed" | "missed" | "none";

/** One mark per day for the last `days` days, oldest first. Days before the
 * PC existed, and today if it hasn't backed up yet, count as "none". */
export function nights(runs: Run[], createdAt: string, days = 30, now = new Date()): Night[] {
  const today = startOfDay(now);
  const byDay = new Map<number, Night>();
  for (const r of runs) {
    const ago = daysBetween(new Date(r.at), today);
    if (ago < 0 || ago >= days) continue;
    if (r.ok) byDay.set(ago, "ok");
    else if (byDay.get(ago) !== "ok") byDay.set(ago, "failed");
  }
  const born = daysBetween(new Date(createdAt), today);
  const out: Night[] = [];
  for (let ago = days - 1; ago >= 0; ago--) {
    const got = byDay.get(ago);
    if (got) out.push(got);
    else if (ago > born || ago === 0) out.push("none");
    else out.push("missed");
  }
  return out;
}

const nightLabel: Record<Night, string> = { ok: "backed up", failed: "failed", missed: "missed", none: "nothing yet" };

export function Tally({ marks }: { marks: Night[] }) {
  const summary = `${marks.filter((m) => m === "ok").length} of the last ${marks.length} days backed up`;
  return (
    <div className="tally" role="img" aria-label={summary} title={summary}>
      {marks.map((m, i) => (
        <i key={i} className={m} title={nightLabel[m]} />
      ))}
    </div>
  );
}

export function Legend() {
  return (
    <div className="legend">
      <span>
        <i style={{ background: "var(--cobalt)" }} />
        Backed up
      </span>
      <span>
        <i style={{ boxShadow: "inset 0 0 0 1.25px var(--ochre)" }} />
        Missed
      </span>
      <span>
        <i style={{ background: "var(--rust)" }} />
        Failed
      </span>
    </div>
  );
}

// ---------- Forms ----------

export function Field({ label, hint, icon, ...input }: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: ReactNode; icon?: ReactNode }) {
  const id = input.id ?? input.name ?? label.toLowerCase().replace(/\W+/g, "-");
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <div className="input-wrap">
        <input id={id} className="input" {...input} />
        {icon && <span className="input-icon">{icon}</span>}
      </div>
      {hint && <div className="hint">{hint}</div>}
    </div>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  const msg = error instanceof Error ? error.message : String(error);
  return (
    <div className="error" role="alert">
      {msg}
    </div>
  );
}

export function CopyButton({ text, className = "btn btn-secondary", label = "Copy" }: { text: string; className?: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className={className}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        } catch {
          /* clipboard blocked; the text is on screen to copy by hand */
        }
      }}
    >
      {done ? "Copied" : label}
    </button>
  );
}

/** Loads data on mount and on demand; keeps the last good value. */
export function useLoad<T>(load: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let alive = true;
    load().then(
      (d) => alive && (setData(d), setError(null)),
      (e) => alive && setError(e),
    );
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);
  return { data, error, reload: () => setTick((t) => t + 1) };
}

export function goHome() {
  navigate("/");
}

export interface ConfirmAction {
  label: string;
  danger?: boolean;
  run: () => Promise<void>;
}

/** Asks before something that can't simply be undone. With confirmText,
 * that text has to be typed before the actions work. */
export function ConfirmModal({
  title,
  children,
  actions,
  confirmText,
  onClose,
}: {
  title: string;
  children: ReactNode;
  actions: ConfirmAction[];
  confirmText?: string;
  onClose: () => void;
}) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => e.key === "Escape" && !busy && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [busy]);
  const ready = !confirmText || typed.trim() === confirmText;
  const go = async (a: ConfirmAction) => {
    setBusy(true);
    setError(null);
    try {
      await a.run();
      onClose();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && !busy && onClose()}>
      <div className="modal confirm-modal" role="dialog" aria-modal="true" aria-label={title}>
        <div className="modal-head">
          <h2>{title}</h2>
          <button type="button" className="icon-button" aria-label="Close" onClick={onClose} disabled={busy}>
            <Icon.x />
          </button>
        </div>
        <div className="confirm-body">
          {children}
          {confirmText && (
            <label className="confirm-type">
              <span className="small">
                Type <strong>{confirmText}</strong> to confirm
              </span>
              <input className="input" value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus autoComplete="off" />
            </label>
          )}
          <ErrorBox error={error} />
        </div>
        <div className="modal-foot confirm-foot">
          <button type="button" className="btn btn-link" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <div className="spacer" />
          {actions.map((a) => (
            <button key={a.label} type="button" className={`btn ${a.danger ? "btn-danger" : "btn-secondary"}`} disabled={busy || !ready} onClick={() => go(a)}>
              {a.label}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
