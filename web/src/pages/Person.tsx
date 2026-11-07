import { useState } from "react";
import { api, type AdminPC } from "../api.ts";
import { bytes, when } from "../format.ts";
import { Link, navigate } from "../router.tsx";
import { Avatar, ConfirmModal, ErrorBox, nights, Tally, useLoad } from "../components/ui.tsx";
import { scheduleText } from "../entries.ts";
import { Cleanup } from "./PCDetail.tsx";

/** The admin's view of one person: their PCs and entries, to clean up.
 * Entry names and folders are locked with the person's own key, so the
 * entries are numbered here. */
export function PersonPage({ id }: { id: string }) {
  const { data, error, reload } = useLoad(() => api.person(id), [id]);
  const [removing, setRemoving] = useState(false);

  if (!data) return <div className="page">{error ? <ErrorBox error={error} /> : <div className="loading">Loading…</div>}</div>;
  const { person, storage, pcs } = data;
  const first = person.name.split(" ")[0];
  return (
    <div className="page">
      <div className="hero">
        <div className="hero-text">
          <div className="crumbs">
            <Link href="/people">People</Link>
            <span>/</span>
            <span>{person.name}</span>
          </div>
          <div className="person" style={{ marginTop: 10 }}>
            <Avatar name={person.name} c={0} big />
            <div>
              <h1 style={{ margin: 0 }}>{person.name}</h1>
              <p className="lede" style={{ margin: 0 }}>
                {person.email}, {person.admin ? "admin" : "member"}. {pcs.length} PC{pcs.length === 1 ? "" : "s"}, {bytes(storage.used)}
                {storage.limit > 0 ? ` of ${bytes(storage.limit)}` : ""} stored.
              </p>
            </div>
          </div>
        </div>
      </div>
      <ErrorBox error={error} />
      <p className="small" style={{ marginBottom: 18 }}>
        Entry names, folders and files are locked with {first}'s own key, so you see entries by number, with their schedules, times and sizes.
      </p>

      {pcs.length === 0 && (
        <div className="card empty">
          <p className="muted">{first} hasn't added a PC yet.</p>
        </div>
      )}
      {pcs.map((pc) => (
        <AdminPCCard key={pc.id} pc={pc} onChange={reload} />
      ))}

      {!person.admin && (
        <div className="card card-pad" style={{ marginTop: 24 }}>
          <div className="danger-zone">
            <div>
              <h2>Remove {person.name}</h2>
              <p className="small">Their account, PCs and all their backups go. They can't log in anymore.</p>
            </div>
            <button className="btn btn-secondary" onClick={() => setRemoving(true)}>
              Remove {first}
            </button>
          </div>
        </div>
      )}
      {removing && (
        <ConfirmModal
          title={`Remove ${person.name}?`}
          confirmText={person.name}
          onClose={() => setRemoving(false)}
          actions={[
            {
              label: `Remove ${first}`,
              danger: true,
              run: async () => {
                await api.removePerson(person.id);
                navigate("/people");
              },
            },
          ]}
        >
          <p>
            Their account and {pcs.length === 1 ? "PC are" : `${pcs.length} PCs are`} removed and every backup of theirs is deleted. This can't be undone from
            restash.
          </p>
          <p className="small">The backups sit in the server's trash for a while first, where you could still get them back by hand.</p>
        </ConfirmModal>
      )}
    </div>
  );
}

function AdminPCCard({ pc, onChange }: { pc: AdminPC; onChange: () => void }) {
  const [deleting, setDeleting] = useState<{ id: string; n: number } | null>(null);
  return (
    <div className="card card-pad" style={{ marginBottom: 18 }}>
      <div className="danger-zone">
        <div>
          <h2>{pc.name}</h2>
          <p className="small">
            {pc.os}, {pc.online ? "online now" : pc.lastSeen ? `last online ${when(pc.lastSeen).toLowerCase()}` : "never online"}. {bytes(pc.stored)}{" "}
            stored.
          </p>
        </div>
        <Tally marks={nights(pc.recent, pc.createdAt)} />
      </div>
      <div className="admin-entries">
        {pc.entries.length === 0 && <p className="muted small">No entries.</p>}
        {pc.entries.map((e, i) => (
          <div className="admin-entry" key={e.id}>
            <span className="cell-main">Entry {i + 1}</span>
            <span className="small truncate">{scheduleText(e.schedule)}</span>
            <span className="small col-when">{e.lastOk ? `Last backup ${when(e.lastOk.at).toLowerCase()}` : "Not backed up yet"}</span>
            <span className="small col-size">{e.lastOk ? bytes(e.lastOk.bytes) : ""}</span>
            <button className="link-button danger" onClick={() => setDeleting({ id: e.id, n: i + 1 })}>
              Delete
            </button>
          </div>
        ))}
      </div>
      {deleting && (
        <ConfirmModal
          title={`Delete entry ${deleting.n} on ${pc.name}?`}
          onClose={() => setDeleting(null)}
          actions={[
            {
              label: "Keep its backups",
              run: async () => {
                await api.deleteEntry(pc.id, deleting.id, false);
                onChange();
              },
            },
            {
              label: "Delete its backups too",
              danger: true,
              run: async () => {
                await api.deleteEntry(pc.id, deleting.id, true);
                onChange();
              },
            },
          ]}
        >
          <p>It stops backing up either way. The owner sees it gone from their PC's page.</p>
          <p className="small">Deleted backups are removed by {pc.name} the next time it's online; the server keeps them in its trash for a while after that.</p>
        </ConfirmModal>
      )}
      <Cleanup pc={pc} onChange={onChange} onRemoved={onChange} bare />
    </div>
  );
}
