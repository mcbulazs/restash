import { api, type PC } from "../api.ts";
import { bytes, shortDate, when, daysBetween } from "../format.ts";
import { Link } from "../router.tsx";
import { ErrorBox, Icon, Legend, nights, Tally, useLoad } from "../components/ui.tsx";
import { useEffect } from "react";

const SAFE_MS = 48 * 3600_000;

export function isSafe(pc: PC, now = Date.now()): boolean {
  return !!pc.lastOk && now - new Date(pc.lastOk).getTime() < SAFE_MS;
}

/** Why a PC isn't safe, in one sentence. */
function trouble(pc: PC, now = new Date()): string {
  if (!pc.ready) return `${pc.name} is waiting for its password.`;
  if (!pc.lastOk) return `${pc.name} hasn't backed up yet.`;
  const days = daysBetween(new Date(pc.lastOk), now);
  let s = `${pc.name} hasn't backed up in ${days} day${days === 1 ? "" : "s"}.`;
  if (!pc.online && pc.lastSeen) s += ` It was last online on ${shortDate(new Date(pc.lastSeen), now)}.`;
  return s;
}

export function headline(pcs: PC[]): [string, string] {
  if (pcs.length === 0) return ["Let's back up your first PC.", "Add a PC and pick the folders that matter. It takes a few minutes."];
  const safe = pcs.filter((p) => isSafe(p));
  const unsafe = pcs.filter((p) => !isSafe(p));
  const latest = pcs
    .map((p) => p.lastOk)
    .filter((t): t is string => !!t)
    .sort()
    .pop();
  if (unsafe.length === 0) {
    const last = latest ? `Last backup ${when(latest).replace(/^[A-Z]/, (c) => c.toLowerCase())}.` : "";
    return [pcs.length === 1 ? "Your PC is safe." : `All ${pcs.length} of your PCs are safe.`, last];
  }
  const title =
    pcs.length === 1 ? "Your PC isn't backed up yet." : safe.length === 0 ? `None of your ${pcs.length} PCs are backed up.` : `${safe.length} of your ${pcs.length} PCs are safe.`;
  return [pcs.length === 1 && pcs[0].lastOk ? "Your PC needs a look." : title, trouble(unsafe[0])];
}

function statusLine(pc: PC): string {
  if (!pc.ready) return "Waiting for its password";
  if (pc.online) return "Online now";
  if (pc.lastSeen) {
    const seen = new Date(pc.lastSeen);
    const today = daysBetween(seen, new Date()) === 0;
    return `Offline since ${today ? seen.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false }) : shortDate(seen)}`;
  }
  return "Offline";
}

export function PCsPage() {
  const { data: pcs, error, reload } = useLoad(api.pcs);
  useEffect(() => {
    const t = setInterval(reload, 30_000);
    return () => clearInterval(t);
  }, []);

  if (!pcs) return <div className="page">{error ? <ErrorBox error={error} /> : <div className="loading">Loading…</div>}</div>;
  const [title, sub] = headline(pcs);
  return (
    <div className="page">
      <div className="hero">
        <div className="hero-text">
          <h1>{title}</h1>
          {sub && <p className="lede">{sub}</p>}
        </div>
        <Link href="/add" className="btn btn-primary">
          <Icon.plus /> Add a PC
        </Link>
      </div>
      <ErrorBox error={error} />
      {pcs.length === 0 ? (
        <div className="card empty">
          <h2>No PCs yet</h2>
          <p className="muted">Once a PC is added, its backups show up here, one bar per night.</p>
          <Link href="/add" className="btn btn-primary" style={{ marginTop: 8 }}>
            <Icon.plus /> Add your first PC
          </Link>
        </div>
      ) : (
        <>
          <div className="card">
            <div className="table-head pc-grid">
              <span>PC</span>
              <span className="col-tally">Last 30 days</span>
              <span>Last backup</span>
              <span className="col-stored">Stored</span>
            </div>
            {pcs.map((pc) => {
              const late = !isSafe(pc);
              return (
                <div className="row pc-grid" key={pc.id}>
                  <div>
                    <div className="cell-title">{pc.name}</div>
                    <div className="cell-sub">{pc.os}</div>
                  </div>
                  <div className="col-tally">
                    <Tally marks={nights(pc.recent, pc.createdAt)} />
                  </div>
                  <div>
                    <div className={`cell-main ${late && pc.lastOk ? "warn-text" : ""}`}>{pc.lastOk ? when(pc.lastOk) : "Not yet"}</div>
                    <div className="cell-sub">{statusLine(pc)}</div>
                  </div>
                  <div className="col-stored cell-main" style={{ fontWeight: 400 }}>
                    {pc.ready ? bytes(pc.stored) : "–"}
                  </div>
                </div>
              );
            })}
          </div>
          <div className="below-table">
            <Legend />
            <span className="lock-note">
              <Icon.lock /> Everything is encrypted on each PC. The server only sees names, times and sizes.
            </span>
          </div>
        </>
      )}
    </div>
  );
}
