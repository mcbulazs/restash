import { useEffect, useState, type KeyboardEvent } from "react";
import { api, type BrowseResult, type EntryConfig, type PCDetail, type Retention, type Schedule, type ScheduleKind } from "../api.ts";
import { bytes } from "../format.ts";
import { Link, navigate } from "../router.tsx";
import { ErrorBox, Icon, UnlockCard } from "../components/ui.tsx";
import {
  browse,
  clock,
  DEFAULT_RETENTION,
  DEFAULT_SCHEDULE,
  NeedUnlock,
  newEntryID,
  openEntries,
  pcEntryKey,
  plainWords,
  sealConfig,
  shortPath,
  WEEKDAYS,
} from "../entries.ts";

interface Loaded {
  pc: PCDetail;
  key: Uint8Array;
  entryID: string;
  isNew: boolean;
  lastBytes: number | null;
}

const KINDS: { kind: ScheduleKind; label: string }[] = [
  { kind: "hourly", label: "Every hour" },
  { kind: "daily", label: "Every day" },
  { kind: "weekly", label: "Every week" },
  { kind: "manual", label: "Manually" },
];

const KIND_HELP: Record<ScheduleKind, string> = {
  hourly: "Only while the PC is on. A missed run happens as soon as it starts up again.",
  daily: "In the PC's own time. If it's off then, the backup runs as soon as it starts up again.",
  weekly: "In the PC's own time. If it's off then, the backup runs as soon as it starts up again.",
  manual: "Only when you click \"Back up now\" on the PC's page.",
};

export function EditEntryPage({ pcID, entryID }: { pcID: string; entryID: string }) {
  const isNew = entryID === "new";
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [locked, setLocked] = useState(false);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [tick, setTick] = useState(0);

  const [name, setName] = useState("");
  const [paths, setPaths] = useState<string[]>([]);
  const [skips, setSkips] = useState<string[]>([]);
  const [skipDraft, setSkipDraft] = useState("");
  const [sched, setSched] = useState<Schedule>(DEFAULT_SCHEDULE);
  const [ret, setRet] = useState<Retention>(DEFAULT_RETENTION);
  const [more, setMore] = useState(false);
  const [picking, setPicking] = useState(false);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    (async () => {
      try {
        const pc = await api.pc(pcID);
        const key = await pcEntryKey(pc);
        let id = entryID;
        let lastBytes: number | null = null;
        if (isNew) {
          id = newEntryID();
        } else {
          const rec = pc.entries.find((e) => e.id === entryID);
          if (!rec) throw new Error("This entry doesn't exist anymore.");
          const [e] = await openEntries(key, pc.id, [rec]);
          if (!e.config) throw new Error("This entry's settings couldn't be unlocked.");
          setName(e.config.name);
          setPaths(e.config.paths);
          setSkips(e.config.skips);
          setSched(rec.schedule);
          setRet(e.config.retention);
          setMore(!!(e.config.retention.last || e.config.retention.weeklyWeeks || e.config.retention.yearlyYears));
          lastBytes = rec.lastOk?.bytes ?? null;
        }
        setLoaded({ pc, key, entryID: id, isNew, lastBytes });
        setLocked(false);
      } catch (e) {
        if (e instanceof NeedUnlock) setLocked(true);
        else setLoadError(e);
      }
    })();
  }, [pcID, entryID, tick]);

  if (locked) {
    return (
      <div className="page">
        <UnlockCard what="this entry" onDone={() => setTick((n) => n + 1)} />
      </div>
    );
  }
  if (!loaded) return <div className="page">{loadError ? <ErrorBox error={loadError} /> : <div className="loading">Loading…</div>}</div>;

  const { pc } = loaded;
  const back = `/pcs/${pc.id}`;
  const title = isNew ? "Add an entry" : `Edit ${name || "entry"}`;

  const addPath = (p: string) => {
    const v = p.trim();
    if (v && !paths.includes(v)) setPaths([...paths, v]);
  };
  const addSkip = () => {
    const v = skipDraft.trim();
    if (v && !skips.includes(v)) setSkips([...skips, v]);
    setSkipDraft("");
  };
  const onSkipKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      addSkip();
    } else if (e.key === "Backspace" && !skipDraft && skips.length) {
      setSkips(skips.slice(0, -1));
    }
  };

  const save = async () => {
    setError(null);
    const cfg: EntryConfig = { name: name.trim(), paths, skips, retention: ret };
    if (!cfg.name) return setError(new Error("Give the entry a name, like Documents or Photos."));
    if (!cfg.paths.length) return setError(new Error("Add at least one folder."));
    const r = cfg.retention;
    if (!(r.last || r.allDays || r.dailyDays || r.weeklyWeeks || r.monthlyMonths || r.yearlyYears))
      return setError(new Error("Keep at least something: set one of the numbers under What it keeps."));
    setBusy(true);
    try {
      const config = await sealConfig(loaded.key, pc.id, loaded.entryID, cfg);
      if (isNew) await api.createEntry(pc.id, { id: loaded.entryID, schedule: sched, config });
      else await api.updateEntry(pc.id, loaded.entryID, { schedule: sched, config });
      navigate(back);
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!confirm(`Delete "${name}"? It stops backing up. Backups already made stay on the server and can still be restored.`)) return;
    setBusy(true);
    try {
      await api.deleteEntry(pc.id, loaded.entryID);
      navigate(back);
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };

  const num = (field: keyof Retention, label: string, unit: string) => (
    <div className="keep-row" key={field}>
      <label htmlFor={field}>{label}</label>
      <input
        id={field}
        className="input num"
        type="number"
        min={0}
        max={9999}
        value={ret[field] || ""}
        placeholder="0"
        onChange={(e) => setRet({ ...ret, [field]: Math.max(0, Math.min(9999, parseInt(e.target.value || "0", 10) || 0)) })}
      />
      <span className="muted">{unit}</span>
    </div>
  );

  const localTZ = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const tzNote = pc.timezone && pc.timezone !== localTZ ? ` (${pc.name}'s time, ${pc.timezone})` : "";

  return (
    <div className="page">
      <div className="hero-text">
        <div className="crumbs">
          <Link href="/">Your PCs</Link>
          <span>/</span>
          <Link href={back}>{pc.name}</Link>
          <span>/</span>
          <span>{isNew ? "New entry" : name || "Entry"}</span>
        </div>
        <h1 style={{ marginTop: 6 }}>{title}</h1>
        <p className="lede">{pc.online ? `Changes reach ${pc.name} right away.` : `Changes reach ${pc.name} the next time it's online.`}</p>
      </div>

      <div className="add-grid">
        <div>
          <div className="card form-card">
            <section>
              <div className="field" style={{ maxWidth: 420 }}>
                <label htmlFor="entry-name">Name</label>
                <input id="entry-name" className="input" value={name} maxLength={80} placeholder="Documents" onChange={(e) => setName(e.target.value)} />
              </div>
            </section>

            <section>
              <h3>Folders</h3>
              <p className="small">Everything inside these folders is backed up.</p>
              <div className="path-list">
                {paths.map((p) => (
                  <div className="path-row" key={p}>
                    <Icon.folder />
                    <span className="truncate" title={p}>
                      {shortPath(p, 70)}
                    </span>
                    <button type="button" className="icon-button" aria-label={`Remove ${p}`} onClick={() => setPaths(paths.filter((x) => x !== p))}>
                      <Icon.x />
                    </button>
                  </div>
                ))}
              </div>
              <div className="path-add">
                <button type="button" className="link-button with-icon" onClick={() => setPicking(true)}>
                  <Icon.plus /> Add a folder
                </button>
                <span className="small">or type a path:</span>
                <input
                  className="input mono small-input"
                  placeholder={pc.os.startsWith("Windows") ? "C:\\Users\\you\\Documents" : "/home/you/Documents"}
                  value={typed}
                  onChange={(e) => setTyped(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      addPath(typed);
                      setTyped("");
                    }
                  }}
                />
                {typed && (
                  <button
                    type="button"
                    className="btn btn-secondary"
                    onClick={() => {
                      addPath(typed);
                      setTyped("");
                    }}
                  >
                    Add
                  </button>
                )}
              </div>
            </section>

            <section>
              <h3>Skip</h3>
              <p className="small">Files and folders matching these are left out.</p>
              <div className="chips">
                {skips.map((s) => (
                  <span className="chip" key={s}>
                    {s}
                    <button type="button" aria-label={`Remove ${s}`} onClick={() => setSkips(skips.filter((x) => x !== s))}>
                      <Icon.x size={16} />
                    </button>
                  </span>
                ))}
                <input className="input chip-input" placeholder="Add a pattern" value={skipDraft} onChange={(e) => setSkipDraft(e.target.value)} onKeyDown={onSkipKey} onBlur={addSkip} />
              </div>
              <p className="small" style={{ marginTop: 8 }}>
                Like <span className="mono">node_modules</span>, <span className="mono">*.tmp</span> or <span className="mono">Thumbs.db</span>. Press Enter after each.
              </p>
            </section>

            <section>
              <h3>When it runs</h3>
              <div className="segmented" role="radiogroup" aria-label="When it runs">
                {KINDS.map((k) => (
                  <button
                    key={k.kind}
                    type="button"
                    role="radio"
                    aria-checked={sched.kind === k.kind}
                    className={sched.kind === k.kind ? "on" : ""}
                    onClick={() => setSched({ ...sched, kind: k.kind })}
                  >
                    {k.label}
                  </button>
                ))}
              </div>
              {(sched.kind === "daily" || sched.kind === "weekly") && (
                <div className="when-row">
                  {sched.kind === "weekly" && (
                    <>
                      <span className="muted">On</span>
                      <select className="input select" value={sched.weekday} onChange={(e) => setSched({ ...sched, weekday: +e.target.value })}>
                        {WEEKDAYS.map((d, i) => (
                          <option key={d} value={i}>
                            {d}
                          </option>
                        ))}
                      </select>
                    </>
                  )}
                  <span className="muted">at</span>
                  <input
                    className="input time"
                    type="time"
                    value={clock(sched.minute)}
                    onChange={(e) => {
                      const [h, m] = e.target.value.split(":").map(Number);
                      if (!isNaN(h) && !isNaN(m)) setSched({ ...sched, minute: h * 60 + m });
                    }}
                  />
                  {tzNote && <span className="small">{tzNote}</span>}
                </div>
              )}
              <p className="small" style={{ marginTop: 10 }}>
                {KIND_HELP[sched.kind]}
              </p>
            </section>

            <section>
              <h3>What it keeps</h3>
              <p className="small">Older backups are thinned out step by step. Deleted ones sit in the server trash for 30 days.</p>
              <div className="keep">
                {num("allDays", "Every backup for", "days")}
                {num("dailyDays", "Then one a day for", "days")}
                {more && num("weeklyWeeks", "Then one a week for", "weeks")}
                {num("monthlyMonths", "Then one a month for", "months")}
                {more && num("yearlyYears", "Then one a year for", "years")}
                {more && num("last", "And always the last", "backups")}
              </div>
              {!more && (
                <button type="button" className="link-button" style={{ marginTop: 12 }} onClick={() => setMore(true)}>
                  More options
                </button>
              )}
            </section>
          </div>
          <ErrorBox error={error} />
          <div className="form-actions">
            {!isNew && (
              <button type="button" className="link-button danger" onClick={remove} disabled={busy}>
                Delete this entry
              </button>
            )}
            <div className="spacer" />
            <Link href={back} className="btn btn-secondary btn-tall">
              Cancel
            </Link>
            <button type="button" className="btn btn-primary" onClick={save} disabled={busy}>
              {busy ? "Saving…" : isNew ? "Add entry" : "Save changes"}
            </button>
          </div>
        </div>

        <div className="side">
          <div className="card card-pad">
            <h2>In plain words</h2>
            <p style={{ marginTop: 14 }}>{plainWords(name, sched, ret)}</p>
            {loaded.lastBytes !== null && (
              <div className="plain-size">
                <div className="small">Size of the last backup</div>
                <div className="big-number">{bytes(loaded.lastBytes)}</div>
                <div className="small">Kept versions only add what changed between them.</div>
              </div>
            )}
          </div>
        </div>
      </div>

      {picking && (
        <FolderPicker
          pc={pc}
          entryKey={loaded.key}
          onClose={() => setPicking(false)}
          onPick={(p) => {
            addPath(p);
            setPicking(false);
          }}
        />
      )}
    </div>
  );
}

function FolderPicker({ pc, entryKey, onClose, onPick }: { pc: PCDetail; entryKey: Uint8Array; onClose: () => void; onPick: (path: string) => void }) {
  const [res, setRes] = useState<BrowseResult | null>(null);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<unknown>(null);

  const go = async (path: string) => {
    setBusy(true);
    setError(null);
    try {
      setRes(await browse(entryKey, pc.id, path));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    // Start in the home folder, where most people's files are.
    (async () => {
      setBusy(true);
      try {
        const top = await browse(entryKey, pc.id, "");
        setRes(top.home ? await browse(entryKey, pc.id, top.home) : top);
      } catch (e) {
        setError(e);
      } finally {
        setBusy(false);
      }
    })();
  }, []);

  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const base = (p: string) => p.split(/[\\/]/).filter(Boolean).pop() || p;

  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-modal="true" aria-label={`Folders on ${pc.name}`}>
        <div className="modal-head">
          <div style={{ minWidth: 0 }}>
            <h2>Pick a folder on {pc.name}</h2>
            <div className="small mono truncate">{res?.path || "Top level"}</div>
          </div>
          <button type="button" className="icon-button" aria-label="Close" onClick={onClose}>
            <Icon.x />
          </button>
        </div>
        <div className="picker-tools">
          <button type="button" className="btn btn-secondary" disabled={busy || !res?.path} onClick={() => go(res?.parent ?? "")}>
            <Icon.up /> Up
          </button>
          {res?.home && (
            <button type="button" className="btn btn-secondary" disabled={busy} onClick={() => go(res.home)}>
              <Icon.home /> Home
            </button>
          )}
          <button type="button" className="btn btn-secondary" disabled={busy} onClick={() => go("")}>
            {pc.os.startsWith("Windows") ? "Drives" : "Top"}
          </button>
        </div>
        <div className="picker-list">
          {error ? (
            <div style={{ padding: 16 }}>
              <ErrorBox error={error} />
            </div>
          ) : busy && !res ? (
            <div className="loading" style={{ padding: 32 }}>
              Asking {pc.name}…
            </div>
          ) : res?.error ? (
            <div style={{ padding: 16 }}>
              <ErrorBox error={new Error(`${pc.name} couldn't open this folder: ${res.error}`)} />
            </div>
          ) : res && res.dirs.length === 0 ? (
            <p className="muted" style={{ padding: 16 }}>
              No folders inside.
            </p>
          ) : (
            res?.dirs.map((d) => (
              <button type="button" key={d} className="picker-item" onClick={() => go(d)} disabled={busy}>
                <Icon.folder />
                <span className="truncate">{base(d)}</span>
                <Icon.chevron />
              </button>
            ))
          )}
        </div>
        <div className="modal-foot">
          <span className="small">The list is encrypted on {pc.name} and opened here.</span>
          <div className="spacer" />
          <button type="button" className="btn btn-secondary" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn btn-primary" disabled={!res?.path || busy} onClick={() => res && onPick(res.path)}>
            Choose this folder
          </button>
        </div>
      </div>
    </div>
  );
}
