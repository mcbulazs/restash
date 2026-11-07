import { useEffect, useState, type ReactNode } from "react";
import { api, type ActivityItem, type NotifySettings } from "../api.ts";
import { bytes, daysBetween, shortDate, startOfDay } from "../format.ts";
import { Link } from "../router.tsx";
import { ErrorBox, Icon } from "../components/ui.tsx";
import { getAccountKey } from "../account.ts";
import { detailText, openDetail, openEntries, pcEntryKey } from "../entries.ts";
import { labelActivity, open } from "../vault.ts";

type Filter = "all" | "problems" | "restores" | "logins";

const FILTERS: { id: Filter; label: string }[] = [
  { id: "all", label: "Everything" },
  { id: "problems", label: "Problems" },
  { id: "restores", label: "Restores" },
  { id: "logins", label: "Logins" },
];

const KINDS: Record<Filter, ActivityItem["kind"][] | null> = {
  all: null,
  problems: ["failed", "late", "quiet"],
  restores: ["restore", "download"],
  logins: ["login"],
};

const time = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});
const longDay = new Intl.DateTimeFormat("en", {
  weekday: "long",
  month: "short",
  day: "numeric",
});

function dayLabel(d: Date): string {
  const ago = daysBetween(d, new Date());
  if (ago === 0) return "Today";
  if (ago === 1) return "Yesterday";
  return longDay.format(d);
}

/** Unlocked names for the viewer's own things: entry names by PC, run
 * details and download lists by item. */
interface Unlocked {
  entryNames: Map<string, string>; // pc + entry id → name
  texts: Map<ActivityItem, string>;
}

async function unlockItems(items: ActivityItem[]): Promise<Unlocked> {
  const out: Unlocked = { entryNames: new Map(), texts: new Map() };
  const account = getAccountKey();
  if (!account) return out;
  const ownPCs = [...new Set(items.filter((i) => i.mine && i.pcId && i.pcKey).map((i) => i.pcId!))];
  const keys = new Map<string, Uint8Array>();
  await Promise.all(
    ownPCs.map(async (id) => {
      try {
        const pc = await api.pc(id);
        const key = await pcEntryKey(pc);
        keys.set(id, key);
        for (const e of await openEntries(key, pc.id, pc.entries)) if (e.config) out.entryNames.set(id + e.record.id, e.config.name);
      } catch {
        /* show without names */
      }
    }),
  );
  const dec = new TextDecoder();
  await Promise.all(
    items.map(async (it) => {
      if (it.kind === "failed" && it.detail && it.pcId && keys.has(it.pcId))
        out.texts.set(it, detailText(await openDetail(keys.get(it.pcId)!, it.pcId, it.detail), 2));
      if (it.kind === "download" && it.secret) {
        try {
          out.texts.set(it, dec.decode(await open(account, it.secret, labelActivity())));
        } catch {
          /* leave it */
        }
      }
    }),
  );
  return out;
}

function list(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
}

/** Icon, title and line for one item. */
function describe(it: ActivityItem, u: Unlocked): { icon: ReactNode; tone: string; title: ReactNode; sub: string } {
  const who = it.mine ? "You" : it.user;
  const entryName = (id?: string) => (id && it.pcId ? u.entryNames.get(it.pcId + id) : undefined);
  const info = it.info ?? {};
  switch (it.kind) {
    case "backups": {
      const names = (it.entries ?? []).map((e) => entryName(e) ?? (e === "manual" ? "By hand" : "an entry"));
      const n = it.mine ? (it.entries?.length ?? 0) : (it.count ?? 0);
      const title = it.mine ? `${it.pc} backed up ${n} entr${n === 1 ? "y" : "ies"}` : `${it.pc} backed up`;
      const added = (it.added ?? 0) > 0 ? `${bytes(it.added ?? 0)} new.` : "No changes.";
      const sub = it.mine ? `${list(names)}. ${added}` : `${added} Only ${it.user} can see which entries.`;
      return { icon: <Icon.check />, tone: "good", title, sub };
    }
    case "failed": {
      const name = entryName(it.entries?.[0]);
      return {
        icon: <Icon.warning />,
        tone: "bad",
        title: it.mine && name ? `${name} failed on ${it.pc}` : `A backup failed on ${it.pc}`,
        sub: it.mine ? u.texts.get(it) || "Open the PC to see what went wrong." : `Only ${it.user} can see the details.`,
      };
    }
    case "late": {
      const name = entryName(info.entry as string);
      const since = info.since ? new Date(info.since as string) : null;
      return {
        icon: <Icon.clock />,
        tone: "warn",
        title: it.mine && name ? `${name} on ${it.pc} is late` : `A backup on ${it.pc} is late`,
        sub: since ? `It was due ${shortDate(since)} at ${time.format(since)} and the PC was on.` : "",
      };
    }
    case "quiet":
      return {
        icon: <Icon.clock />,
        tone: "warn",
        title: `${it.pc} has been quiet for ${info.days ?? "a few"} days`,
        sub: "No backup in that time. It may be switched off or offline.",
      };
    case "recovered":
      return {
        icon: <Icon.check />,
        tone: "good",
        title: `${it.pc} is backing up again`,
        sub: "The problem went away.",
      };
    case "restore": {
      const state = info.state as string;
      const from = info.from ? ` from ${info.from}'s backup` : "";
      const sub =
        state === "done"
          ? `${info.files ?? 0} files, ${bytes(Number(info.bytes ?? 0))}${from}.`
          : state === "failed"
            ? `It failed${from}. Open the restore page to try again.`
            : `Started${from}.`;
      return {
        icon: <Icon.refresh />,
        tone: "plain",
        title: `${who} restored files to ${it.pc}`,
        sub,
      };
    }
    case "download": {
      const what = u.texts.get(it);
      const n = Number(info.items ?? 0);
      return {
        icon: <Icon.download />,
        tone: "plain",
        title: `${who} downloaded ${n === 1 ? "a file" : `${n} items`} in the browser`,
        sub: `${what ? what + " from " : "From "}${it.pc}, ${bytes(Number(info.bytes ?? 0))}, on ${info.browser ?? "a browser"}.`,
      };
    }
    case "login":
      return {
        icon: <Icon.key />,
        tone: "warn",
        title: it.mine ? "New login for you" : `New login for ${it.user}`,
        sub: `${info.browser ?? "A browser"}. If this wasn't ${it.mine ? "you" : it.user}, ${it.mine ? "change your" : "they should change their"} password.`,
      };
  }
}

export function ActivityPage() {
  const [data, setData] = useState<{
    items: ActivityItem[];
    weekBackups: number;
    weekProblems: number;
  } | null>(null);
  const [unlocked, setUnlocked] = useState<Unlocked>({
    entryNames: new Map(),
    texts: new Map(),
  });
  const [error, setError] = useState<unknown>(null);
  const [filter, setFilter] = useState<Filter>("all");

  useEffect(() => {
    api.activity().then(async (d) => {
      setData(d);
      setUnlocked(await unlockItems(d.items));
    }, setError);
  }, []);

  if (!data) return <div className="page">{error ? <ErrorBox error={error} /> : <div className="loading">Loading…</div>}</div>;

  const kinds = KINDS[filter];
  const items = data.items.filter((i) => !kinds || kinds.includes(i.kind));
  const groups: { label: string; items: ActivityItem[] }[] = [];
  for (const it of items) {
    const label = dayLabel(startOfDay(new Date(it.at)));
    if (groups.at(-1)?.label !== label) groups.push({ label, items: [] });
    groups.at(-1)!.items.push(it);
  }
  const p = data.weekProblems;

  return (
    <div className="page">
      <div className="hero">
        <div className="hero-text">
          <h1>
            This week: {data.weekBackups} backup
            {data.weekBackups === 1 ? "" : "s"}, {p === 0 ? "no problems" : `${p} problem${p === 1 ? "" : "s"}`}.
          </h1>
          <p className="lede">Everything that happened across your PCs, newest first.</p>
        </div>
        <div className="segmented" role="radiogroup" aria-label="Show">
          {FILTERS.map((f) => (
            <button
              key={f.id}
              type="button"
              role="radio"
              aria-checked={filter === f.id}
              className={filter === f.id ? "on" : ""}
              onClick={() => setFilter(f.id)}
            >
              {f.label}
            </button>
          ))}
        </div>
      </div>

      <div className="detail-grid">
        <div className="card card-pad feed">
          {groups.length === 0 && (
            <p className="muted">
              {filter === "all" ? "Nothing yet. Backups, restores and logins show up here." : "Nothing of this kind in the last 30 days."}
            </p>
          )}
          {groups.map((g) => (
            <div key={g.label} className="feed-group">
              <div className="small feed-day">{g.label}</div>
              {g.items.map((it, i) => {
                const d = describe(it, unlocked);
                const body = (
                  <>
                    <span className={`feed-icon ${d.tone}`}>{d.icon}</span>
                    <div className="feed-text">
                      <div className={`feed-title ${d.tone === "bad" ? "fail-text" : ""}`}>{d.title}</div>
                      {d.sub && <div className="small">{d.sub}</div>}
                    </div>
                    <span className="small feed-time">{time.format(new Date(it.at))}</span>
                  </>
                );
                return it.mine && it.pcId ? (
                  <Link key={i} href={`/pcs/${it.pcId}`} className="feed-item">
                    {body}
                  </Link>
                ) : (
                  <div key={i} className="feed-item">
                    {body}
                  </div>
                );
              })}
            </div>
          ))}
        </div>
        <NotifyCard />
      </div>
    </div>
  );
}

function Toggle({ on, onChange, label, hint }: { on: boolean; onChange: (v: boolean) => void; label: string; hint: string }) {
  return (
    <label className="toggle-row">
      <span>
        <span className="toggle-label">{label}</span>
        <span className="small">{hint}</span>
      </span>
      <input type="checkbox" role="switch" className="switch" checked={on} onChange={(e) => onChange(e.target.checked)} />
    </label>
  );
}

function NotifyCard() {
  const [s, setS] = useState<NotifySettings | null>(null);
  const [server, setServer] = useState("");
  const [admin, setAdmin] = useState(false);
  const [topic, setTopic] = useState("");
  const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null);

  useEffect(() => {
    api.notify().then(
      (r) => {
        setS(r.settings);
        setTopic(r.settings.topic);
        setServer(r.server);
        setAdmin(r.admin);
      },
      () => {},
    );
  }, []);
  if (!s) return <div className="card card-pad" />;

  const save = async (next: NotifySettings) => {
    setNote(null);
    try {
      await api.setNotify(next);
      setS(next);
    } catch (e) {
      setNote({ ok: false, text: e instanceof Error ? e.message : String(e) });
    }
  };
  const any = admin ? " for any account" : "";

  if (!server)
    return (
      <div className="card card-pad notify">
        <h2>Alerts</h2>
        <p className="small" style={{ margin: "6px 0 0" }}>
          This server doesn't send alerts. Problems still show on Your PCs and in the list here.
        </p>
      </div>
    );

  return (
    <div className="card card-pad notify">
      <h2>Tell me when</h2>
      <p className="small" style={{ margin: "6px 0 16px" }}>
        Sent to your{" "}
        <a href="https://ntfy.sh" target="_blank" rel="noreferrer">
          ntfy
        </a>{" "}
        topic on {server.replace(/^https?:\/\//, "")}. Alerts name PCs, never files.
      </p>
      <Toggle on={s.fail} onChange={(v) => save({ ...s, fail: v })} label="A backup fails or runs late" hint={`Right away, once per problem${any}.`} />
      <Toggle on={s.quiet} onChange={(v) => save({ ...s, quiet: v })} label="A PC goes quiet" hint={`After 3 days without a backup${any}.`} />
      <Toggle on={s.login} onChange={(v) => save({ ...s, login: v })} label="Someone logs in somewhere new" hint={`A new browser or device${any}.`} />
      <Toggle on={s.weekly} onChange={(v) => save({ ...s, weekly: v })} label="A weekly summary" hint="Every Monday morning." />
      <form
        className="topic-form"
        onSubmit={async (e) => {
          e.preventDefault();
          await save({ ...s, topic: topic.trim() });
        }}
      >
        <label htmlFor="topic" className="small">
          Your ntfy topic (make it hard to guess, it's the password)
        </label>
        <div className="topic-row">
          <input id="topic" className="input mono" value={topic} onChange={(e) => setTopic(e.target.value)} placeholder="anna-backups-7f3k9q" />
          <button className="btn btn-secondary">Save</button>
        </div>
        {s.topic && (
          <button
            type="button"
            className="link-button"
            onClick={() =>
              api.testNotify().then(
                () => setNote({ ok: true, text: "Sent. Check your phone." }),
                (e) => setNote({ ok: false, text: e.message }),
              )
            }
          >
            Send a test
          </button>
        )}
      </form>
      {note &&
        (note.ok ? (
          <div className="note-ok" style={{ marginTop: 12 }}>
            {note.text}
          </div>
        ) : (
          <div style={{ marginTop: 12 }}>
            <ErrorBox error={new Error(note.text)} />
          </div>
        ))}
    </div>
  );
}
