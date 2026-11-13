import { useState, type FormEvent } from "react";
import { api, type Invite, type Person } from "../api.ts";
import { Link } from "../router.tsx";
import { bytes, daysBetween, shortDate } from "../format.ts";
import { Avatar, CopyButton, ErrorBox, Field, Icon, useLoad } from "../components/ui.tsx";

const SPACE_COLORS = ["var(--cobalt)", "var(--green)", "var(--ochre)", "var(--cobalt-mid)", "var(--rust)", "var(--quiet)"];

function Status({ p }: { p: Person }) {
  if (p.pcs === 0) return <span className="status-dot hollow status-quiet">No PCs yet</span>;
  if (p.limit > 0 && p.stored >= p.limit) return <span className="status-dot hollow status-warn">At its storage limit</span>;
  if (p.allSafe) return <span className="status-dot status-good">All backed up</span>;
  if (p.problem === "failed") return <span className="status-dot fail-text">A backup failed</span>;
  if (p.problem === "late") return <span className="status-dot hollow status-warn">A backup is late</span>;
  if (!p.oldestOk) return <span className="status-dot hollow status-warn">Not backed up yet</span>;
  const days = daysBetween(new Date(p.oldestOk), new Date());
  return <span className="status-dot hollow status-warn">Quiet for {days} day{days === 1 ? "" : "s"}</span>;
}

function InviteForm({ mail, onDone }: { mail: boolean; onDone: () => void }) {
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [made, setMade] = useState<Invite | null>(null);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    api.createInvite(email).then(
      (inv) => {
        setMade(inv);
        setBusy(false);
        onDone();
      },
      (err) => {
        setError(err);
        setBusy(false);
      },
    );
  };
  if (made)
    return (
      <div className="form">
        {made.emailed ? (
          <p>
            <strong>Invite sent to {made.email}.</strong> It works once, for 7 days. If it doesn't arrive, they can check their spam
            folder, or you can send them the link yourself:
          </p>
        ) : mail ? (
          <div className="notice">
            <Icon.warning />
            <span>
              Couldn't email {made.email}{made.emailError ? ` (${made.emailError})` : ""}. Send them this link yourself instead. It works once, for 7 days.
            </span>
          </div>
        ) : (
          <p>
            Send this link to <strong>{made.email}</strong>. It works once, for 7 days.
          </p>
        )}
        <div className="command">
          <code>{made.link}</code>
          <CopyButton text={made.link} className="" />
        </div>
      </div>
    );
  return (
    <form className="invite-form" onSubmit={submit}>
      <Field label="Their email" type="email" name="invite-email" required value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />
      <button className="btn btn-primary" disabled={busy}>
        {mail ? (busy ? "Sending…" : "Send invite") : busy ? "Making link…" : "Make invite link"}
      </button>
      <div style={{ flexBasis: "100%" }}>
        <ErrorBox error={error} />
      </div>
    </form>
  );
}

/** Emails a waiting invite again. */
function SendAgain({ token }: { token: string }) {
  const [state, setState] = useState<"idle" | "busy" | "sent" | "failed">("idle");
  const [error, setError] = useState("");
  const send = () => {
    setState("busy");
    api.sendInvite(token).then(
      () => setState("sent"),
      (err) => {
        setError(err instanceof Error ? err.message : String(err));
        setState("failed");
      },
    );
  };
  if (state === "sent") return <span className="small">Sent</span>;
  return (
    <button className={`btn btn-link${state === "failed" ? " fail-text" : ""}`} disabled={state === "busy"} onClick={send} title={error || undefined}>
      {state === "busy" ? "Sending…" : state === "failed" ? "Couldn't send. Try again" : "Send again"}
    </button>
  );
}

/** The admin sets how much one person's PCs may store together. */
function LimitEditor({ p, onDone }: { p: Person; onDone: () => void }) {
  const [gb, setGb] = useState(p.limit > 0 ? String(Math.round(p.limit / 1e8) / 10) : "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const save = (limit: number) => {
    setBusy(true);
    setError(null);
    api.setLimit(p.id, limit).then(onDone, (e) => {
      setError(e);
      setBusy(false);
    });
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const n = Number(gb.replace(",", "."));
    if (!Number.isFinite(n) || n <= 0) {
      setError(new Error("Type a size in GB, like 50."));
      return;
    }
    save(Math.round(n * 1e9));
  };
  const first = p.name.split(" ")[0];
  return (
    <div className="card card-pad">
      <h2>Storage limit for {p.name}</h2>
      <p className="muted" style={{ marginBottom: 18 }}>
        All of {first}'s PCs together. They use {bytes(p.stored)} now. Over the limit, their PCs stop making new backups and clean up old ones instead,
        until there's room again.
      </p>
      <form className="invite-form" onSubmit={submit}>
        <label className="limit-input">
          <input className="input" inputMode="decimal" aria-label="Limit in GB" value={gb} onChange={(e) => setGb(e.target.value)} autoFocus placeholder="50" />
          <span>GB</span>
        </label>
        <button className="btn btn-primary" disabled={busy}>
          Save
        </button>
        {p.limit > 0 && (
          <button type="button" className="btn btn-secondary" disabled={busy} onClick={() => save(0)}>
            Remove the limit
          </button>
        )}
        <button type="button" className="btn btn-link" onClick={onDone}>
          Cancel
        </button>
        <div style={{ flexBasis: "100%" }}>
          <ErrorBox error={error} />
        </div>
      </form>
    </div>
  );
}

export function PeoplePage() {
  const { data, error, reload } = useLoad(api.people);
  const [inviting, setInviting] = useState(false);
  const [editing, setEditing] = useState("");

  if (!data) return <div className="page">{error ? <ErrorBox error={error} /> : <div className="loading">Loading…</div>}</div>;
  const n = data.people.length;
  const totalStored = data.people.reduce((s, p) => s + p.stored, 0);
  return (
    <div className="page">
      <div className="hero">
        <div className="hero-text">
          <h1>{n === 1 ? "Just you back up here, so far." : `${n} people back up here.`}</h1>
          <p className="lede">You can invite people and see their PCs' health. You can never open their files.</p>
        </div>
        <button className="btn btn-primary" onClick={() => setInviting(!inviting)}>
          <Icon.plus /> Invite someone
        </button>
      </div>
      <ErrorBox error={error} />

      {inviting && (
        <div className="card card-pad">
          <h2>Invite someone</h2>
          <p className="muted" style={{ marginBottom: 18 }}>
            {data.mail
              ? "restash emails them a link to make their account. You also get the link, in case the email doesn't arrive."
              : "restash doesn't send email here. You get a link to send them yourself."}
          </p>
          <InviteForm mail={data.mail} onDone={reload} />
        </div>
      )}

      <div className="card">
        <div className="table-head people-grid">
          <span>Person</span>
          <span className="col-role">Role</span>
          <span className="col-pcs">PCs</span>
          <span className="col-stored">Stored</span>
          <span>Status</span>
        </div>
        {data.people.map((p, i) => (
          <div className="row people-grid" key={p.id}>
            <div className="person">
              <Avatar name={p.name} c={i} big />
              <div>
                <Link href={`/people/${p.id}`} className="cell-title truncate person-link">
                  {p.name}
                </Link>
                <div className="cell-sub truncate">{p.email}</div>
                {/* On phones the Stored column is hidden; the limit lives here. */}
                <button className="link-button limit-link limit-link-narrow" onClick={() => setEditing(p.id)}>
                  {bytes(p.stored)} {p.limit > 0 ? `of ${bytes(p.limit)}` : ", no limit"}
                </button>
              </div>
            </div>
            <span className="col-role cell-main" style={{ fontWeight: 400 }}>
              {p.admin ? "Admin" : "Member"}
            </span>
            <span className="col-pcs cell-main" style={{ fontWeight: 400 }}>
              {p.pcs}
            </span>
            <span className="col-stored">
              <span className="cell-main" style={{ fontWeight: 400 }}>
                {bytes(p.stored)}
              </span>
              <button className="link-button limit-link" title="Change the storage limit" onClick={() => setEditing(p.id)}>
                {p.limit > 0 ? `of ${bytes(p.limit)}` : "No limit"}
              </button>
            </span>
            <Status p={p} />
          </div>
        ))}
      </div>

      {editing && data.people.some((p) => p.id === editing) && (
        <LimitEditor
          key={editing}
          p={data.people.find((p) => p.id === editing)!}
          onDone={() => {
            setEditing("");
            reload();
          }}
        />
      )}

      {data.invites.length > 0 && (
        <div className="card card-pad">
          <h2>Waiting to join</h2>
          <div style={{ marginTop: 8 }}>
            {data.invites.map((inv) => {
              const left = Math.max(0, daysBetween(new Date(), new Date(inv.expiresAt)));
              const ago = daysBetween(new Date(inv.createdAt), new Date());
              return (
                <div className="invite-row" key={inv.token}>
                  <span className="muted">
                    <Icon.clock />
                  </span>
                  <div className="grow">
                    <div className="cell-main truncate">{inv.email}</div>
                    <div className="cell-sub">
                      Invited {ago === 0 ? "today" : ago === 1 ? "yesterday" : `${ago} days ago`}. The link expires{" "}
                      {left <= 0 ? "today" : left === 1 ? "tomorrow" : `in ${left} days`}.
                    </div>
                  </div>
                  {data.mail && <SendAgain token={inv.token} />}
                  <CopyButton text={inv.link} label="Copy link" />
                  <button className="btn btn-link" onClick={() => api.deleteInvite(inv.token).then(reload)}>
                    Cancel invite
                  </button>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {data.space.total > 0 && (
        <div className="card card-pad">
          <h2>Server space</h2>
          <p className="muted">
            {bytes(data.space.used)} used of {bytes(data.space.total)}. Backups take {bytes(totalStored)} of that.
          </p>
          <div className="space-bar" role="img" aria-label={`${bytes(data.space.used)} of ${bytes(data.space.total)} used`}>
            {data.people.map((p, i) => (
              <i key={p.id} style={{ width: `${(100 * p.stored) / data.space.total}%`, background: SPACE_COLORS[i % SPACE_COLORS.length] }} />
            ))}
            <i style={{ width: `${(100 * Math.max(0, data.space.used - totalStored)) / data.space.total}%`, background: "var(--line)" }} title="Other files" />
          </div>
          <div className="legend">
            {data.people.map((p, i) => (
              <span key={p.id}>
                <i style={{ width: 8, height: 8, borderRadius: "50%", background: SPACE_COLORS[i % SPACE_COLORS.length] }} />
                {p.name}
              </span>
            ))}
            <span>
              <i style={{ width: 8, height: 8, borderRadius: "50%", background: "var(--line)" }} />
              Other files
            </span>
          </div>
          <p className="small" style={{ marginTop: 14 }}>
            Counted {shortDate(new Date())}. Sizes are of the encrypted backups.
          </p>
        </div>
      )}
    </div>
  );
}
