import { useEffect, useState } from "react";
import { api, type JoinCode, type JoinStatus } from "../api.ts";
import { minutesLeft } from "../format.ts";
import { Link } from "../router.tsx";
import { CopyButton, ErrorBox, Icon } from "../components/ui.tsx";

const RELEASES = "https://github.com/mcbulazs/restash/releases/latest/download/";

type OS = "windows" | "macos" | "linux";

interface Build {
  os: OS;
  label: string;
  file: string;
}

const builds: Record<OS, Build> = {
  windows: { os: "windows", label: "Windows", file: "restash-agent-windows-amd64.exe" },
  macos: { os: "macos", label: "macOS", file: "restash-agent-darwin-arm64" },
  linux: { os: "linux", label: "Linux", file: "restash-agent-linux-amd64" },
};

const otherBuilds: { label: string; file: string }[] = [
  { label: "Windows (ARM)", file: "restash-agent-windows-arm64.exe" },
  { label: "macOS (Intel)", file: "restash-agent-darwin-amd64" },
  { label: "Linux (ARM)", file: "restash-agent-linux-arm64" },
];

function detectOS(): OS {
  const p = (navigator.platform || navigator.userAgent).toLowerCase();
  if (p.includes("win")) return "windows";
  if (p.includes("mac")) return "macos";
  return "linux";
}

function command(b: Build, server: string, code: string): string {
  const join = `join -server ${server} ${code}`;
  if (b.os === "windows") return `.\\${b.file} ${join}`;
  return `chmod +x ${b.file} && ./${b.file} ${join}`;
}

export function AddPCPage() {
  const [os, setOS] = useState<OS>(detectOS);
  const [code, setCode] = useState<JoinCode | null>(null);
  const [status, setStatus] = useState<JoinStatus>({ status: "waiting" });
  const [error, setError] = useState<unknown>(null);
  const [, tick] = useState(0);

  const newCode = () => {
    setError(null);
    setStatus({ status: "waiting" });
    api.createJoinCode().then(setCode, setError);
  };
  useEffect(newCode, []);

  // Poll until the PC is ready or the code expires.
  useEffect(() => {
    if (!code || status.status === "ready" || status.status === "expired") return;
    const t = setInterval(() => {
      api.joinStatus(code.code).then(setStatus, () => {});
      tick((n) => n + 1);
    }, 2000);
    return () => clearInterval(t);
  }, [code, status.status]);

  const b = builds[os];
  const joined = status.status === "joined" || status.status === "ready";
  const pcName = "pc" in status ? status.pc.name : "";

  return (
    <div className="page">
      <div className="hero-text">
        <div className="crumbs">
          <Link href="/">Your PCs</Link>
          <span>/</span>
          <span>Add a PC</span>
        </div>
        <h1 style={{ marginTop: 6 }}>Add a PC</h1>
        <p className="lede">Do this on the PC you want to back up. It links itself to your account.</p>
      </div>
      <ErrorBox error={error} />
      <div className="add-grid">
        <div className="card steps">
          <div className={`step ${joined ? "done" : "current"}`}>
            <span className="step-num">1</span>
            <div className="step-body">
              <h2>Download restash</h2>
              <p className="muted">We detected {b.label}. restash runs from a terminal for now. It's one file with everything it needs inside.</p>
              <a className="btn btn-primary btn-big" href={RELEASES + b.file}>
                <Icon.download /> Download for {b.label}
              </a>
              <span className="file-note">
                <Icon.file /> {b.file}
              </span>
            </div>
          </div>
          <div className={`step ${joined ? "done" : ""}`}>
            <span className="step-num">2</span>
            <div className="step-body">
              <h2>Link it with this code</h2>
              <p className="muted">In a terminal, in the folder you downloaded it to, run:</p>
              {code ? (
                <>
                  <div className="command">
                    <code>{command(b, code.server, code.code)}</code>
                    <CopyButton text={command(b, code.server, code.code)} className="" />
                  </div>
                  <p className="small">
                    Your code is <span className="mono">{code.code}</span>. It works once and expires in {minutesLeft(code.expiresAt)} minutes.
                  </p>
                </>
              ) : (
                <p className="small">Getting a code…</p>
              )}
              {os === "windows" && (
                <div className="notice">
                  <Icon.shield />
                  <span>Windows may say it protected your PC. Click "More info", then "Run anyway". restash is open source and not signed by Microsoft yet.</span>
                </div>
              )}
              {os === "macos" && (
                <div className="notice">
                  <Icon.shield />
                  <span>
                    If macOS won't open it, go to System Settings → Privacy & Security and click "Open Anyway". restash is open source and not signed by Apple yet.
                  </span>
                </div>
              )}
            </div>
          </div>
          <div className={`step ${status.status === "ready" ? "done" : joined ? "current" : ""}`}>
            <span className="step-num">3</span>
            <div className="step-body">
              <h2>Type your password there</h2>
              <p className="muted">
                restash asks for your account password. It unlocks this PC's backups right on the PC and is never sent anywhere.
              </p>
            </div>
          </div>
        </div>

        <div className="side">
          <div className="card card-pad">
            {status.status === "ready" ? (
              <>
                <div className="waiting-title">
                  <span className="pulse done" />
                  <h2>{pcName} is ready</h2>
                </div>
                <p className="muted" style={{ marginTop: 14 }}>
                  It's linked and unlocked. Next, pick the folders to back up. Keep <span className="mono">restash-agent run</span> going on it so backups run on time.
                </p>
                <Link href={"pc" in status ? `/pcs/${status.pc.id}/entries/new` : "/"} className="btn btn-primary" style={{ marginTop: 18 }}>
                  Pick folders
                </Link>
              </>
            ) : status.status === "joined" ? (
              <>
                <div className="waiting-title">
                  <span className="pulse" />
                  <h2>{pcName} is linked</h2>
                </div>
                <p className="muted" style={{ marginTop: 14 }}>
                  Now type your password on {pcName} to unlock it. This page updates by itself.
                </p>
              </>
            ) : status.status === "expired" ? (
              <>
                <h2>This code expired</h2>
                <p className="muted" style={{ marginTop: 14 }}>
                  Codes work for an hour. Get a new one and use it in the command.
                </p>
                <button className="btn btn-primary" style={{ marginTop: 18 }} onClick={newCode}>
                  Get a new code
                </button>
              </>
            ) : (
              <>
                <div className="waiting-title">
                  <span className="pulse" />
                  <h2>Waiting for the new PC</h2>
                </div>
                <p className="muted" style={{ marginTop: 14 }}>
                  This page updates by itself once the PC checks in.
                  {code && ` The code works once and expires in ${minutesLeft(code.expiresAt)} minutes.`}
                </p>
              </>
            )}
          </div>
          <div className="card card-pad">
            <h3>Setting up a different kind of PC?</h3>
            <div className="os-links">
              {(Object.keys(builds) as OS[])
                .filter((o) => o !== os)
                .map((o) => (
                  <a key={o} href="#" onClick={(e) => (e.preventDefault(), setOS(o))}>
                    <Icon.download /> {builds[o].label}
                  </a>
                ))}
              {otherBuilds.map((o) => (
                <a key={o.file} href={RELEASES + o.file}>
                  <Icon.download /> {o.label}
                </a>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
