import { useEffect, useRef, useState } from "react";
import { api, ApiError, type PCDetail, type RunDetail, type Running } from "../api.ts";
import { bytes, daysBetween, percentText, shortDate, startOfDay, timeLeft, when } from "../format.ts";
import { Link, navigate } from "../router.tsx";
import { ConfirmModal, ErrorBox, Icon, nights, Tally, UnlockCard } from "../components/ui.tsx";
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
  const [running, setRunning] = useState<Running[]>([]);
  const runningIds = useRef<string[]>([]);

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
          setRunning(pc.running ?? []);
          runningIds.current = (pc.running ?? []).map((r) => r.entry);
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

  // Live progress while the PC is online. When a backup finishes, reload
  // so it shows up under Recent backups.
  const online = data?.pc.online ?? false;
  useEffect(() => {
    if (!online) return;
    const t = setInterval(() => {
      api.running(id).then((r) => {
        const now = r.running.map((x) => x.entry);
        const finished = runningIds.current.some((e) => !now.includes(e));
        runningIds.current = now;
        setRunning(r.running);
        if (finished) setTick((n) => n + 1);
      }, () => {});
    }, 3000);
    return () => clearInterval(t);
  }, [id, online]);

  const runNow = async (entry = "", label = "everything") => {
    setNote(null);
    try {
      await api.runNow(id, entry);
      setNote({ ok: true, text: `Asked ${data?.pc.name ?? "the PC"} to back up ${label} now. You can follow it below.` });
      setTimeout(() => setTick((n) => n + 1), 3000);
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
  const runningOf = new Map(running.map((r) => [r.entry, r]));
  let summary: string;
  if (running.length > 0) {
    const r = running[0];
    const name = nameOf.get(r.entry) ?? "An entry";
    summary =
      r.totalBytes > 0
        ? `Backing up ${name}: ${percentText(r.percent)} done${r.eta > 0 ? `, ${timeLeft(r.eta)} left` : ""}.`
        : `Backing up ${name}: looking for changes.`;
    if (running.length > 1) summary += ` ${running.length - 1} more running.`;
  } else if (!pc.ready) summary = `${pc.name} is waiting for its password. Type it on the PC to finish setting it up.`;
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
              const live = runningOf.get(e.record.id);
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
                        {live ? (
                          <div className="small live-text">Backing up, {live.totalBytes > 0 ? percentText(live.percent) : "starting"}</div>
                        ) : (
                          <div className={`small ${failed ? "fail-text" : ""}`}>
                            {failed ? `Failed ${when(lastRun.at).toLowerCase()}` : e.record.lastOk ? `Last backup ${when(e.record.lastOk.at).toLowerCase()}` : "Not backed up yet"}
                          </div>
                        )}
                      </div>
                      {cfg && (
                        <Link href={`/pcs/${pc.id}/entries/${e.record.id}`} className="entry-edit">
                          Edit
                        </Link>
                      )}
                    </div>
                    {live && <LiveBackup r={live} />}
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

      <Cleanup pc={pc} names={nameOf} onChange={() => setTick((n) => n + 1)} />
    </div>
  );
}

/** Backups left by removed entries, removals waiting for the PC, and
 * removing the PC itself. Used by the owner here and by the admin. */
export function Cleanup({
  pc,
  names,
  onChange,
  onRemoved = () => navigate("/"),
  bare = false,
}: {
  pc: Pick<PCDetail, "id" | "name" | "online" | "leftovers" | "purges">;
  names?: Map<string, string>;
  onChange: () => void;
  onRemoved?: () => void;
  bare?: boolean; // inside another card
}) {
  const [asking, setAsking] = useState<"" | "pc" | string>("");
  const leftover = pc.leftovers.find((l) => l.entry === asking);
  const what = (entry: string) => (entry === "manual" ? "Backups made by hand" : (names?.get(entry) ?? "A removed entry"));
  return (
    <div className={bare ? "cleanup-bare" : "card card-pad"} style={{ marginTop: 24 }}>
      {pc.leftovers.length > 0 && (
        <>
          <h2>Backups of removed entries</h2>
          <p className="small" style={{ margin: "4px 0 10px" }}>
            They can still be restored, and take space until they're deleted.
          </p>
          {pc.leftovers.map((l) => (
            <div className="leftover" key={l.entry}>
              <div className="grow">
                <div className="cell-main">{what(l.entry)}</div>
                <div className="cell-sub">
                  {l.runs} backup{l.runs === 1 ? "" : "s"}, the last {when(l.last).toLowerCase()}
                  {l.bytes ? `, ${bytes(l.bytes)}` : ""}
                </div>
              </div>
              <button className="btn btn-secondary" onClick={() => setAsking(l.entry)}>
                Delete these backups
              </button>
            </div>
          ))}
        </>
      )}
      {pc.purges.length > 0 && (
        <p className="small" style={{ marginTop: pc.leftovers.length ? 14 : 0 }}>
          Deleting the backups of {pc.purges.length} removed entr{pc.purges.length === 1 ? "y" : "ies"}.{" "}
          {pc.online ? `${pc.name} is on it.` : `${pc.name} does it the next time it's online.`}
        </p>
      )}
      <div className="danger-zone" style={{ marginTop: pc.leftovers.length || pc.purges.length ? 20 : 0 }}>
        <div>
          <h2>Remove {pc.name}</h2>
          <p className="small">It stops backing up and all its backups are deleted.</p>
        </div>
        <button className="btn btn-secondary" onClick={() => setAsking("pc")}>
          Remove this PC
        </button>
      </div>
      {leftover && (
        <ConfirmModal
          title="Delete these backups?"
          onClose={() => setAsking("")}
          actions={[
            {
              label: "Delete backups",
              danger: true,
              run: async () => {
                await api.deleteBackups(pc.id, leftover.entry);
                onChange();
              },
            },
          ]}
        >
          <p>
            {what(leftover.entry)}: {leftover.runs} backup{leftover.runs === 1 ? "" : "s"}. They can't be restored afterwards.
          </p>
          <p className="small">{pc.name} deletes them the next time it's online; the server keeps them in its trash for a while after that.</p>
        </ConfirmModal>
      )}
      {asking === "pc" && (
        <ConfirmModal
          title={`Remove ${pc.name}?`}
          confirmText={pc.name}
          onClose={() => setAsking("")}
          actions={[
            {
              label: "Remove it",
              danger: true,
              run: async () => {
                await api.removePC(pc.id);
                onRemoved();
              },
            },
          ]}
        >
          <p>It's unlinked right away and stops backing up. All of its backups are deleted, so they can't be restored.</p>
          <p className="small">
            The server keeps them in its trash for a while, where the admin can still get them back. To use the PC with restash again, add it again from Add
            a PC.
          </p>
        </ConfirmModal>
      )}
    </div>
  );
}

/** A running backup: a bar and how far it is. */
function LiveBackup({ r }: { r: Running }) {
  const known = r.totalBytes > 0;
  return (
    <div className="progress-line live-backup" role="status">
      <div className="bar" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={known ? Math.floor(r.percent * 100) : undefined}>
        <i style={{ width: known ? `${Math.max(2, r.percent * 100)}%` : "100%" }} className={known ? "" : "indeterminate"} />
      </div>
      <div className="small">
        {known
          ? `${bytes(r.bytes)} of ${bytes(r.totalBytes)} checked, ${r.files.toLocaleString()} of ${r.totalFiles.toLocaleString()} files${r.eta > 0 ? `. ${timeLeft(r.eta)[0].toUpperCase()}${timeLeft(r.eta).slice(1)} left.` : "."}`
          : "Looking for changes…"}
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
