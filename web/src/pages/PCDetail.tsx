import { useEffect, useState } from "react";
import { api, ApiError, type PCDetail, type RunDetail } from "../api.ts";
import { bytes, daysBetween, shortDate, startOfDay, when } from "../format.ts";
import { Link, navigate } from "../router.tsx";
import { ErrorBox, Icon, nights, Tally, UnlockCard } from "../components/ui.tsx";
import { detailText, isLate, NeedUnlock, openDetail, openEntries, pcEntryKey, retentionText, scheduleText, shortPath, skipsText, type Entry } from "../entries.ts";

const time = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
const longDay = new Intl.DateTimeFormat("en", { weekday: "long", month: "short", day: "numeric" });

function dayLabel(d: Date): string {
  const ago = daysBetween(d, new Date());
  if (ago === 0) return "Today";
  if (ago === 1) return "Yesterday";
  return longDay.format(d);
}

interface Loaded {
  pc: PCDetail;
  entries: Entry[];
  details: Map<RunDetail, string>; // unlocked run details
}

export function PCDetailPage({ id }: { id: string }) {
  const [data, setData] = useState<Loaded | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [locked, setLocked] = useState(false);
  const [tick, setTick] = useState(0);
  const [showAll, setShowAll] = useState(false);
  const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const pc = await api.pc(id);
        if (!pc.ready) {
          alive && setData({ pc, entries: [], details: new Map() });
          return;
        }
        const key = await pcEntryKey(pc);
        const entries = await openEntries(key, pc.id, pc.entries);
        const details = new Map<RunDetail, string>();
        await Promise.all(pc.runs.filter((r) => r.detail).map(async (r) => details.set(r, await openDetail(key, pc.id, r.detail))));
        if (alive) {
          setData({ pc, entries, details });
          setLocked(false);
          setError(null);
        }
      } catch (e) {
        if (!alive) return;
        if (e instanceof NeedUnlock) setLocked(true);
        else setError(e);
      }
    })();
    return () => {
      alive = false;
    };
  }, [id, tick]);

  useEffect(() => {
    const t = setInterval(() => setTick((n) => n + 1), 15_000);
    return () => clearInterval(t);
  }, []);

  const runNow = async (entry = "", label = "everything") => {
    setNote(null);
    try {
      await api.runNow(id, entry);
      setNote({ ok: true, text: `Asked ${data?.pc.name ?? "the PC"} to back up ${label} now. It shows up below when it's done.` });
      setTimeout(() => setTick((n) => n + 1), 4000);
    } catch (e) {
      setNote({ ok: false, text: e instanceof Error ? e.message : String(e) });
    }
  };

  if (locked) {
    return (
      <div className="page">
        <Crumbs name="This PC" />
        <UnlockCard what="this PC" onDone={() => setTick((n) => n + 1)} />
      </div>
    );
  }
  if (!data) {
    if (error instanceof ApiError && error.status === 404) {
      return (
        <div className="page">
          <Crumbs name="Not found" />
          <ErrorBox error={new Error("There's no such PC in your account.")} />
        </div>
      );
    }
    return <div className="page">{error ? <ErrorBox error={error} /> : <div className="loading">Loading…</div>}</div>;
  }

  const { pc, entries, details } = data;
  const nameOf = new Map(entries.map((e) => [e.record.id, e.config?.name ?? "Locked entry"]));
  const late = entries.filter((e) => isLate(e.record.schedule, e.record.lastOk?.at ?? null, e.record.createdAt));
  const failing = entries.filter((e) => pc.runs.find((r) => r.entry === e.record.id)?.ok === false);
  const facts = [pc.os, pc.ready ? `${bytes(pc.stored)} stored` : ""].filter(Boolean).join(", ");
  let summary: string;
  if (!pc.ready) summary = `${pc.name} is waiting for its password. Type it on the PC to finish setting it up.`;
  else if (entries.length === 0) summary = `Nothing is backed up yet. Add an entry to pick folders. ${facts}.`;
  else if (failing.length > 0) {
    const e = failing[0];
    const r = pc.runs.find((x) => x.entry === e.record.id)!;
    summary = `${e.config?.name ?? "An entry"} failed ${when(r.at).toLowerCase()}.${failing.length > 1 ? ` ${failing.length - 1} more failed too.` : ""} The details are under Recent backups.`;
  } else if (late.length === 0) summary = `${entries.length === 1 ? "The entry is" : `All ${entries.length} entries are`} up to date. ${facts}.`;
  else {
    const e = late[0];
    const last = e.record.lastOk ? `Its last backup was ${when(e.record.lastOk.at).toLowerCase()}.` : "It hasn't backed up yet.";
    summary = `${e.config?.name ?? "An entry"} is behind. ${last}${pc.online ? "" : ` ${pc.name} is offline.`}`;
  }

  const runs = showAll ? pc.runs : pc.runs.slice(0, 8);
  const groups: { label: string; runs: RunDetail[] }[] = [];
  for (const r of runs) {
    const label = dayLabel(startOfDay(new Date(r.at)));
    if (groups.at(-1)?.label !== label) groups.push({ label, runs: [] });
    groups.at(-1)!.runs.push(r);
  }

  return (
    <div className="page">
      <div className="hero">
        <div className="hero-text">
          <Crumbs name={pc.name} />
          <h1 style={{ marginTop: 6 }}>{pc.name}</h1>
          <p className="lede">{summary}</p>
        </div>
        {pc.ready && (
          <div className="hero-actions">
            {entries.length > 0 && (
              <button className="btn btn-secondary btn-tall" onClick={() => runNow()}>
                Back up everything now
              </button>
            )}
            <Link href={`/pcs/${pc.id}/restore`} className="btn btn-primary">
              <Icon.refresh /> Restore files
            </Link>
          </div>
        )}
      </div>
      {note && (note.ok ? <div className="note-ok">{note.text}</div> : <ErrorBox error={new Error(note.text)} />)}
      <ErrorBox error={error} />

      <div className="detail-grid">
        <div className="card">
          <div className="card-head">
            <div>
              <h2>Entries</h2>
              <p className="small">Each entry has its own schedule and history.</p>
            </div>
            {pc.ready && (
              <Link href={`/pcs/${pc.id}/entries/new`} className="btn btn-secondary">
                <Icon.plus /> Add an entry
              </Link>
            )}
          </div>
          {entries.length === 0 ? (
            <div className="empty" style={{ paddingTop: 32 }}>
              <p className="muted">{pc.ready ? "No entries yet. An entry is a folder (or a few) with its own schedule." : "Entries can be added once the PC is unlocked."}</p>
            </div>
          ) : (
            entries.map((e) => {
              const runsOf = pc.runs.filter((r) => r.entry === e.record.id).reverse();
              const lastRun = pc.runs.find((r) => r.entry === e.record.id);
              const failed = lastRun && !lastRun.ok;
              const cfg = e.config;
              return (
                <div className="entry" key={e.record.id}>
                  <span className="icon-tile">
                    <Icon.folder />
                  </span>
                  <div className="entry-body">
                    <div className="entry-top">
                      <div style={{ minWidth: 0 }}>
                        <h3 className="entry-name">{cfg?.name ?? "Locked entry"}</h3>
                        <div className="cell-sub truncate">
                          {cfg ? cfg.paths.map((p) => shortPath(p)).join(", ") : "Couldn't unlock this entry."}
                          {e.record.lastOk ? `, ${bytes(e.record.lastOk.bytes)}` : ""}
                        </div>
                      </div>
                      <div className="entry-tally">
                        <Tally marks={nights(runsOf.map((r) => ({ at: r.at, ok: r.ok })), e.record.createdAt)} />
                        <div className={`small ${failed ? "fail-text" : ""}`}>
                          {failed ? `Failed ${when(lastRun.at).toLowerCase()}` : e.record.lastOk ? `Last backup ${when(e.record.lastOk.at).toLowerCase()}` : "Not backed up yet"}
                        </div>
                      </div>
                      {cfg && (
                        <Link href={`/pcs/${pc.id}/entries/${e.record.id}`} className="entry-edit">
                          Edit
                        </Link>
                      )}
                    </div>
                    {cfg && (
                      <ul className="facts">
                        <li>
                          <Icon.clock /> {scheduleText(e.record.schedule)}
                        </li>
                        <li>
                          <Icon.calendar /> {retentionText(cfg.retention)}
                        </li>
                        {cfg.skips.length > 0 && (
                          <li>
                            <Icon.x /> {skipsText(cfg.skips)}
                          </li>
                        )}
                      </ul>
                    )}
                  </div>
                </div>
              );
            })
          )}
          <div className="card-foot lock-note">
            <Icon.lock /> Only you can see entry names and folders. The server knows the schedules, so it can tell you when a backup is late.
          </div>
        </div>

        <div className="card card-pad">
          <h2>Recent backups</h2>
          {groups.length === 0 && <p className="muted" style={{ marginTop: 12 }}>No backups yet.</p>}
          {groups.map((g) => (
            <div key={g.label} className="run-group">
              <div className="small">{g.label}</div>
              {g.runs.map((r, i) => {
                const detail = details.get(r) ?? "";
                return (
                  <div className={`run ${r.ok ? "" : "failed"}`} key={i}>
                    <span className="run-dot" />
                    <div style={{ minWidth: 0 }}>
                      <div>
                        <span className="run-time">{time.format(new Date(r.at))}</span> <span className="muted">{nameOf.get(r.entry) ?? (r.entry === "manual" ? "By hand" : "Removed entry")}</span>
                      </div>
                      <div className="small run-detail">
                        {r.ok ? (r.added > 0 ? `${bytes(r.added)} new.` : "No changes.") : detailText(detail) || "It failed."}
                        {r.ok && detail ? ` ${detailText(detail, 1)}` : ""}
                      </div>
                      {!r.ok && nameOf.has(r.entry) && r === pc.runs.find((x) => x.entry === r.entry) && (
                        <button className="link-button" onClick={() => runNow(r.entry, nameOf.get(r.entry))}>
                          Run it now
                        </button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          ))}
          {pc.runs.length > 8 && (
            <button className="btn btn-secondary" style={{ marginTop: 18 }} onClick={() => setShowAll(!showAll)}>
              {showAll ? "Show fewer" : "Show all backups"}
            </button>
          )}
          {!pc.online && pc.lastSeen && (
            <p className="small" style={{ marginTop: 18 }}>
              {pc.name} is offline since {shortDate(new Date(pc.lastSeen))}. It catches up on missed backups when it's back.
            </p>
          )}
        </div>
      </div>
    </div>
  );
}

function Crumbs({ name }: { name: string }) {
  return (
    <div className="crumbs">
      <Link href="/">Your PCs</Link>
      <span>/</span>
      <span>{name}</span>
    </div>
  );
}

export function goToPC(id: string) {
  navigate(`/pcs/${id}`);
}
