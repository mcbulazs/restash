import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, type PC, type PCDetail, type RestoreJobStatus } from "../api.ts";
import { bytes, shortDate, when } from "../format.ts";
import { Link } from "../router.tsx";
import { ErrorBox, Icon, UnlockCard } from "../components/ui.tsx";
import { NeedUnlock, openEntries, pcEntryKey, shortPath } from "../entries.ts";
import { getAccountKey } from "../account.ts";
import { Backup, saveAs, type Node, type Snapshot } from "../restic.ts";
import { labelActivity, labelPCKey, labelRestore, open, pcPassword, seal } from "../vault.ts";
import { FolderPicker } from "./EditEntry.tsx";

const enc = new TextEncoder();
const dec = new TextDecoder();
const monthDay = new Intl.DateTimeFormat("en", { month: "short", day: "numeric" });

interface Source {
  pc: PCDetail;
  backup: Backup;
  password: string; // the PC's restic password
  snaps: Snapshot[];
  entryNames: Map<string, string>; // entry id → name
}

/** "Today at 12:41 · Documents" */
function snapLabel(s: Snapshot, names: Map<string, string>): string {
  const tags = (s.tags ?? []).filter((t) => t.startsWith("entry:")).map((t) => names.get(t.slice(6)) ?? (t === "entry:manual" ? "By hand" : "Removed entry"));
  return `${when(s.time)}${tags.length ? " · " + tags.join(", ") : ""}`;
}

/** Splits a path into clickable crumbs: [{label, path}] from root down. */
function crumbs(backup: Backup, snap: Snapshot, dir: string) {
  const out: { label: string; path: string }[] = [];
  let p = dir;
  while (p.length >= snap.root.length) {
    const label = p === snap.root ? shortPath(backup.original(snap.id, p) || "/", 40) : p.slice(p.lastIndexOf("/") + 1);
    out.unshift({ label, path: p });
    if (p === "/" || p === snap.root) break;
    p = p.slice(0, p.lastIndexOf("/")) || "/";
  }
  return out;
}

export function RestorePage({ id }: { id: string }) {
  const [src, setSrc] = useState<Source | null>(null);
  const [locked, setLocked] = useState(false);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [tick, setTick] = useState(0);
  const [status, setStatus] = useState("Unlocking the backup…");

  const [snapID, setSnapID] = useState("");
  const [dir, setDir] = useState("");
  const [nodes, setNodes] = useState<Node[] | null>(null);
  const [listError, setListError] = useState<unknown>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [sizes, setSizes] = useState<Map<string, { bytes: number; done: boolean }>>(new Map());
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<string[] | null>(null);

  const [pcs, setPCs] = useState<PC[]>([]);
  const [target, setTarget] = useState("");
  const [mode, setMode] = useState<"originals" | "folder">("originals");
  const [folder, setFolder] = useState("");
  const [picker, setPicker] = useState<{ pc: PCDetail; key: Uint8Array } | null>(null);
  const [busy, setBusy] = useState("");
  const [actionError, setActionError] = useState<unknown>(null);
  const [job, setJob] = useState<{ pc: PCDetail; key: Uint8Array; status: RestoreJobStatus; detail: string } | null>(null);
  const [download, setDownload] = useState<{ done: number; total: number } | null>(null);

  // Open the PC's backup in this page.
  useEffect(() => {
    let backup: Backup | null = null;
    (async () => {
      try {
        const pc = await api.pc(id);
        const account = getAccountKey();
        if (!account) throw new NeedUnlock();
        if (!pc.pcKey) throw new Error(`${pc.name} hasn't been set up on the PC yet, so it has no backups.`);
        const pcKey = await open(account, pc.pcKey, labelPCKey(pc.id));
        const password = pcPassword(pcKey);
        const ek = await pcEntryKey(pc);
        const entries = await openEntries(ek, pc.id, pc.entries);
        const entryNames = new Map(entries.filter((e) => e.config).map((e) => [e.record.id, e.config!.name]));
        setStatus("Loading the backup reader…");
        backup = await Backup.open(pc.id, password);
        setStatus("Reading the list of backups…");
        const snaps = await backup.snapshots();
        setSrc({ pc, backup, password, snaps, entryNames });
        if (snaps.length) {
          setSnapID(snaps[0].id);
          setDir(snaps[0].root);
        }
        setLocked(false);
      } catch (e) {
        if (e instanceof NeedUnlock) setLocked(true);
        else setLoadError(e);
      }
    })();
    api.pcs().then((list) => {
      const ready = list.filter((p) => p.ready);
      setPCs(ready);
      const self = ready.find((p) => p.id === id);
      setTarget(self?.online ? self.id : (ready.find((p) => p.online)?.id ?? self?.id ?? ""));
    }, () => {});
    return () => backup?.close();
  }, [id, tick]);

  const snap = src?.snaps.find((s) => s.id === snapID) ?? null;

  // List the current folder.
  useEffect(() => {
    if (!src || !snap || !dir) return;
    let alive = true;
    setNodes(null);
    setListError(null);
    setSelected(new Set());
    src.backup.list(snap.id, dir).then(
      async (list) => {
        if (!alive) return;
        setNodes(list);
        // Folder sizes need their whole contents read, so work them out
        // one by one in the background, giving up on very big ones.
        for (const n of list.filter((n) => n.type === "dir")) {
          if (!alive) return;
          try {
            const s = await src.backup.size(snap.id, [n.path], 300);
            if (alive) setSizes((m) => new Map(m).set(snap.id + n.path, { bytes: s.bytes, done: s.done }));
          } catch {
            /* leave it blank */
          }
        }
      },
      (e) => alive && setListError(e),
    );
    return () => {
      alive = false;
    };
  }, [src, snapID, dir]);

  // Search, a moment after typing stops.
  const searchSeq = useRef(0);
  useEffect(() => {
    if (!src || !snap) return;
    const q = query.trim();
    if (q.length < 2) {
      setResults(null);
      return;
    }
    const seq = ++searchSeq.current;
    const t = setTimeout(() => {
      src.backup.search(snap.id, snap.root, q, 100).then(
        (r) => seq === searchSeq.current && setResults(r),
        () => seq === searchSeq.current && setResults([]),
      );
    }, 350);
    return () => clearTimeout(t);
  }, [query, src, snapID]);

  const picked = useMemo(() => (nodes ?? []).filter((n) => selected.has(n.name)), [nodes, selected]);
  const pickedBytes = picked.reduce((sum, n) => sum + (n.type === "dir" ? (sizes.get(snapID + n.path)?.bytes ?? 0) : (n.size ?? 0)), 0);
  const sizesKnown = picked.every((n) => n.type !== "dir" || sizes.get(snapID + n.path)?.done);

  if (locked) {
    return (
      <div className="page">
        <UnlockCard what="this backup" onDone={() => setTick((n) => n + 1)} />
      </div>
    );
  }
  if (!src) {
    if (loadError instanceof ApiError && loadError.status === 404) return <div className="page"><ErrorBox error={new Error("There's no such PC in your account.")} /></div>;
    return <div className="page">{loadError ? <ErrorBox error={loadError} /> : <div className="loading">{status}</div>}</div>;
  }

  const { pc, backup } = src;
  const targetPC = pcs.find((p) => p.id === target);
  const samePC = target === pc.id;
  const effectiveMode = samePC ? mode : "folder";
  const label = "restored " + monthDay.format(new Date());

  const toggle = (name: string) => {
    const next = new Set(selected);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    setSelected(next);
  };

  const goTo = (path: string, select?: string) => {
    setQuery("");
    setResults(null);
    setDir(path);
    if (select) setTimeout(() => setSelected(new Set([select])), 0);
  };

  const doDownload = async () => {
    if (!snap || picked.length === 0) return;
    setActionError(null);
    const single = picked.length === 1 && picked[0].type === "file";
    const name = single ? picked[0].name : `${pc.name} ${shortDate(new Date(snap.time))}.zip`;
    let sink;
    try {
      sink = await saveAs(name, single ? "application/octet-stream" : "application/zip");
    } catch {
      return; // the save dialog was cancelled
    }
    setBusy("download");
    setDownload({ done: 0, total: sizesKnown ? pickedBytes : 0 });
    try {
      if (single) {
        let done = 0;
        await backup.file(snap.id, picked[0].path, async (c) => {
          await sink.write(c);
          done += c.length;
          setDownload({ done, total: picked[0].size ?? 0 });
        });
      } else {
        await backup.zip(snap.id, dir, picked.map((n) => n.path), sink.write, (n) => setDownload((d) => ({ done: n, total: d?.total ?? 0 })));
      }
      await sink.close();
      // Note it in the activity feed; what was downloaded stays locked.
      const account = getAccountKey();
      if (account) {
        const what = enc.encode(picked.map((n) => n.name).join(", "));
        const secret = await seal(account, what, labelActivity());
        api.downloaded(pc.id, picked.length, pickedBytes, secret).catch(() => {});
      }
    } catch (e) {
      await sink.abort();
      setActionError(e);
    } finally {
      setBusy("");
      setDownload(null);
    }
  };

  const doRestore = async () => {
    if (!snap || !targetPC || picked.length === 0) return;
    setActionError(null);
    if (effectiveMode === "folder" && !folder) {
      setActionError(new Error(`Pick a folder on ${targetPC.name} first.`));
      return;
    }
    setBusy("restore");
    try {
      const tpc = await api.pc(targetPC.id);
      const tkey = await pcEntryKey(tpc);
      const jobData = {
        sourcePc: pc.id,
        sourceKey: samePC ? "" : src.password,
        snapshot: snap.id,
        dir,
        names: picked.map((n) => n.name),
        originalDir: backup.original(snap.id, dir),
        mode: effectiveMode,
        target: effectiveMode === "folder" ? folder : "",
        label,
      };
      const sealed = await seal(tkey, enc.encode(JSON.stringify(jobData)), labelRestore(tpc.id));
      const status = await api.restore(tpc.id, sealed, pc.id === tpc.id ? "" : pc.name);
      setJob({ pc: tpc, key: tkey, status, detail: "" });
    } catch (e) {
      setActionError(e);
    } finally {
      setBusy("");
    }
  };

  return (
    <div className="page">
      <div className="hero-text">
        <div className="crumbs">
          <Link href="/">Your PCs</Link>
          <span>/</span>
          <Link href={`/pcs/${pc.id}`}>{pc.name}</Link>
          <span>/</span>
          <span>Restore</span>
        </div>
        <h1 style={{ marginTop: 6 }}>Restore files</h1>
        <p className="lede">Pick a backup, tick what you want back, and choose where it goes.</p>
      </div>

      {src.snaps.length === 0 ? (
        <div className="card empty">
          <h2>No backups yet</h2>
          <p className="muted">{pc.name} hasn't made a backup yet, so there's nothing to restore.</p>
        </div>
      ) : (
        <>
          <div className="restore-controls">
            <div className="field">
              <label htmlFor="snap">From the backup of</label>
              <select
                id="snap"
                className="input select-wide"
                value={snapID}
                onChange={(e) => {
                  const s = src.snaps.find((x) => x.id === e.target.value)!;
                  setSnapID(s.id);
                  setDir(s.root);
                }}
              >
                {src.snaps.map((s) => (
                  <option key={s.id} value={s.id}>
                    {snapLabel(s, src.entryNames)}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="find">Find a file</label>
              <div className="input-wrap">
                <input id="find" className="input search" placeholder="Name, e.g. thesis.docx" value={query} onChange={(e) => setQuery(e.target.value)} />
              </div>
            </div>
          </div>

          <div className="card">
            <div className="files-head">
              <div className="file-crumbs">
                {results ? (
                  <span>
                    {results.length === 100 ? "First 100 matches" : `${results.length} match${results.length === 1 ? "" : "es"}`} for “{query.trim()}”
                  </span>
                ) : (
                  snap &&
                  crumbs(backup, snap, dir).map((c, i, all) =>
                    i === all.length - 1 ? (
                      <strong key={c.path}>{c.label}</strong>
                    ) : (
                      <span key={c.path}>
                        <button className="crumb" onClick={() => goTo(c.path)}>
                          {c.label}
                        </button>
                        <span className="sep">/</span>
                      </span>
                    ),
                  )
                )}
              </div>
              <span className="lock-note">
                <Icon.lock /> Unlocked in this browser
              </span>
            </div>

            {results ? (
              <div>
                {results.length === 0 && <p className="muted" style={{ padding: 24 }}>Nothing with that name in this backup.</p>}
                {results.map((p) => {
                  const parent = p.slice(0, p.lastIndexOf("/")) || "/";
                  const name = p.slice(p.lastIndexOf("/") + 1);
                  return (
                    <button key={p} className="file-row result" onClick={() => goTo(parent, name)}>
                      <span />
                      <span className="file-name">
                        <Icon.file /> <span className="truncate">{name}</span>
                      </span>
                      <span className="small truncate result-path" title={backup.original(snapID, parent)}>
                        {shortPath(backup.original(snapID, parent), 60)}
                      </span>
                    </button>
                  );
                })}
              </div>
            ) : listError ? (
              <div style={{ padding: 24 }}>
                <ErrorBox error={listError} />
              </div>
            ) : !nodes ? (
              <div className="loading" style={{ padding: 48 }}>
                Opening the folder…
              </div>
            ) : (
              <div role="table" aria-label="Files">
                <div className="file-row file-head" role="row">
                  <span>
                    <input
                      type="checkbox"
                      aria-label="Select all"
                      checked={nodes.length > 0 && selected.size === nodes.filter((n) => n.type === "dir" || n.type === "file").length}
                      onChange={(e) => setSelected(e.target.checked ? new Set(nodes.filter((n) => n.type === "dir" || n.type === "file").map((n) => n.name)) : new Set())}
                    />
                  </span>
                  <span>Name</span>
                  <span className="col-size">Size</span>
                  <span className="col-changed">Changed</span>
                </div>
                {nodes.length === 0 && <p className="muted" style={{ padding: 24 }}>This folder is empty.</p>}
                {nodes.map((n) => {
                  const restorable = n.type === "dir" || n.type === "file";
                  const size = n.type === "dir" ? sizes.get(snapID + n.path) : null;
                  return (
                    <div className={`file-row ${selected.has(n.name) ? "on" : ""}`} role="row" key={n.name}>
                      <span>
                        <input type="checkbox" aria-label={`Select ${n.name}`} disabled={!restorable} checked={selected.has(n.name)} onChange={() => toggle(n.name)} />
                      </span>
                      {n.type === "dir" ? (
                        <button className="file-name dir" onClick={() => goTo(n.path)}>
                          <Icon.folder /> <span className="truncate">{n.name}</span>
                        </button>
                      ) : (
                        <span className="file-name" onClick={() => restorable && toggle(n.name)}>
                          <Icon.file /> <span className="truncate">{n.name}</span>
                          {n.type === "symlink" && <span className="small"> (link)</span>}
                        </span>
                      )}
                      <span className="col-size">{n.type === "file" ? bytes(n.size ?? 0) : size ? (size.done ? bytes(size.bytes) : `${bytes(size.bytes)}+`) : ""}</span>
                      <span className="col-changed">{shortDate(new Date(n.mtime))}</span>
                    </div>
                  );
                })}
              </div>
            )}
          </div>

          <div className="card restore-bar">
            <div className="restore-count">
              <strong>
                {picked.length} item{picked.length === 1 ? "" : "s"} selected
              </strong>
              <div className="small">{picked.length ? (sizesKnown ? bytes(pickedBytes) : `${bytes(pickedBytes)} or more`) : "Tick files or folders above"}</div>
            </div>
            <div className="field restore-target">
              <label htmlFor="target">Restore to</label>
              <select id="target" className="input select-wide" value={target} onChange={(e) => setTarget(e.target.value)}>
                {pcs.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                    {p.id === pc.id ? " (this PC)" : ""}
                    {p.online ? "" : " – offline"}
                  </option>
                ))}
              </select>
            </div>
            <div className="restore-modes" role="radiogroup">
              <label className={`radio ${samePC ? "" : "disabled"}`}>
                <input type="radio" name="mode" checked={effectiveMode === "originals"} disabled={!samePC} onChange={() => setMode("originals")} />
                <span>
                  <strong>Next to the originals</strong>
                  <span className="small">{samePC ? `Copies get “(${label})” added to their name` : `Only for restoring onto ${pc.name} itself`}</span>
                </span>
              </label>
              <label className="radio">
                <input type="radio" name="mode" checked={effectiveMode === "folder"} onChange={() => setMode("folder")} />
                <span>
                  <strong>Into a folder I choose</strong>
                  <span className="small">
                    {effectiveMode === "folder" && folder ? (
                      <>
                        <span className="mono" title={folder}>
                          {shortPath(folder, 44)}
                        </span>{" "}
                        <button className="link-button" onClick={() => openPicker()}>
                          Change
                        </button>
                      </>
                    ) : (
                      <>
                        Nothing gets overwritten either way.{" "}
                        {effectiveMode === "folder" && (
                          <button className="link-button" onClick={() => openPicker()}>
                            Pick a folder
                          </button>
                        )}
                      </>
                    )}
                  </span>
                </span>
              </label>
            </div>
            <div className="restore-actions">
              <div className="download-col">
                <button className="btn btn-secondary btn-big" disabled={!picked.length || !!busy} onClick={doDownload}>
                  <Icon.download /> {busy === "download" ? "Downloading…" : "Download as .zip"}
                </button>
                <span className="small">Works in any browser, no app needed</span>
              </div>
              <button className="btn btn-primary btn-big" disabled={!picked.length || !!busy || !targetPC?.online} onClick={doRestore}>
                <Icon.refresh /> {busy === "restore" ? "Sending…" : `Restore ${picked.length || ""} item${picked.length === 1 ? "" : "s"}`.replace("  ", " ")}
              </button>
            </div>
            {targetPC && !targetPC.online && <p className="small restore-note">{targetPC.name} is offline. Restoring to it works once it's back; downloading works now.</p>}
            {download && (
              <div className="restore-note">
                <Progress done={download.done} total={download.total} label="Downloading" />
              </div>
            )}
            {job && <JobStatus job={job} setJob={setJob} />}
            <div className="restore-note">
              <ErrorBox error={actionError} />
            </div>
          </div>
        </>
      )}

      {picker && (
        <FolderPicker
          pc={picker.pc}
          entryKey={picker.key}
          onClose={() => setPicker(null)}
          onPick={(p) => {
            setFolder(p);
            setPicker(null);
          }}
        />
      )}
    </div>
  );

  async function openPicker() {
    if (!targetPC) return;
    try {
      const tpc = await api.pc(targetPC.id);
      setPicker({ pc: tpc, key: await pcEntryKey(tpc) });
    } catch (e) {
      setActionError(e);
    }
  }
}

function Progress({ done, total, label }: { done: number; total: number; label: string }) {
  const pct = total > 0 ? Math.min(100, (100 * done) / total) : null;
  return (
    <div className="progress-line">
      <div className="small">
        {label}: {bytes(done)}
        {total > 0 ? ` of ${bytes(total)}` : ""}
      </div>
      <div className="bar">
        <i style={{ width: pct === null ? "100%" : `${pct}%` }} className={pct === null ? "indeterminate" : ""} />
      </div>
    </div>
  );
}

type Job = { pc: PCDetail; key: Uint8Array; status: RestoreJobStatus; detail: string };

/** Follows a restore on the target PC until it ends. */
function JobStatus({ job, setJob }: { job: Job; setJob: (j: Job) => void }) {
  useEffect(() => {
    if (job.status.state === "done" || job.status.state === "failed") return;
    const t = setTimeout(async () => {
      try {
        const status = await api.restoreStatus(job.pc.id, job.status.id);
        let detail = job.detail;
        if (status.detail) {
          try {
            detail = dec.decode(await open(job.key, status.detail, labelRestore(job.pc.id)));
          } catch {
            /* keep what we had */
          }
        }
        setJob({ ...job, status, detail });
      } catch {
        setJob({ ...job });
      }
    }, 1500);
    return () => clearTimeout(t);
  }, [job]);

  const s = job.status;
  if (s.state === "done") return <div className="note-ok restore-note">Done. {job.detail}</div>;
  if (s.state === "failed")
    return (
      <div className="restore-note">
        <ErrorBox error={new Error(`The restore on ${job.pc.name} failed. ${job.detail}`)} />
      </div>
    );
  return (
    <div className="restore-note">
      <Progress done={s.bytes} total={s.percent > 0 ? s.bytes / s.percent : 0} label={s.state === "sent" ? `Waiting for ${job.pc.name}` : `Restoring on ${job.pc.name}`} />
    </div>
  );
}
