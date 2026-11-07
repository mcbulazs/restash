import { useEffect, useState } from "react";
import { api, type JoinCode, type JoinStatus } from "../api.ts";
import { minutesLeft } from "../format.ts";
import { Link } from "../router.tsx";
import { CopyButton, ErrorBox, Icon } from "../components/ui.tsx";

type OS = "windows" | "macos" | "linux";

interface Build {
  target: string; // as the server names it: <goos>-<goarch>
  os: OS;
  label: string;
}

const builds: Build[] = [
  { target: "windows-amd64", os: "windows", label: "Windows" },
  { target: "darwin-arm64", os: "macos", label: "macOS" },
  { target: "linux-amd64", os: "linux", label: "Linux" },
  { target: "windows-arm64", os: "windows", label: "Windows (ARM)" },
  { target: "darwin-amd64", os: "macos", label: "macOS (Intel)" },
  { target: "linux-arm64", os: "linux", label: "Linux (ARM)" },
];

function detect(): string {
  const p = (navigator.platform || navigator.userAgent).toLowerCase();
  if (p.includes("win")) return "windows-amd64";
  if (p.includes("mac")) return "darwin-arm64";
  return p.includes("aarch64") || p.includes("arm") ? "linux-arm64" : "linux-amd64";
}

/** Chrome can tell ARM from Intel; other browsers keep the guess. */
function detectArch(set: (t: string) => void, current: string) {
  const uad = (navigator as Navigator & { userAgentData?: { getHighEntropyValues(h: string[]): Promise<{ architecture?: string }> } }).userAgentData;
  uad?.getHighEntropyValues(["architecture"]).then(
    (v) => {
      const arm = v.architecture === "arm";
      const [os] = current.split("-");
      if (os === "darwin") set(arm ? "darwin-arm64" : "darwin-amd64");
      else set(`${os}-${arm ? "arm64" : "amd64"}`);
    },
    () => {},
  );
}

function fileName(b: Build, code: string): string {
  const base = `restash-setup-${code}`;
  return b.os === "windows" ? base + ".exe" : b.os === "macos" ? base + ".zip" : base;
}

export function AddPCPage() {
  const [target, setTarget] = useState<string>(detect);
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
  useEffect(() => detectArch(setTarget, detect()), []);

  // Poll until the PC is ready or the code expires.
  useEffect(() => {
    if (!code || status.status === "ready" || status.status === "expired") return;
    const t = setInterval(() => {
      api.joinStatus(code.code).then(setStatus, () => {});
      tick((n) => n + 1);
    }, 2000);
    return () => clearInterval(t);
  }, [code, status.status]);

  const b = builds.find((x) => x.target === target) ?? builds[0];
  const os = b.os;
  const file = code ? fileName(b, code.code) : "";
  const linuxCmd = code ? `chmod +x ~/Downloads/restash-setup-${code.code} && ~/Downloads/restash-setup-${code.code}` : "";
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
              <p className="muted">For {b.label}. One file with everything it needs; it's made for this account, so use it on the PC you're adding.</p>
              {code ? (
                <a className="btn btn-primary btn-big" href={`/api/pcs/join/${code.code}/download/${b.target}`} download={file}>
                  <Icon.download /> Download for {b.label}
                </a>
              ) : (
                <button className="btn btn-primary btn-big" disabled>
                  <Icon.download /> Getting it ready…
                </button>
              )}
              {code && (
                <span className="file-note">
                  <Icon.file /> {file}
                </span>
              )}
            </div>
          </div>
          <div className={`step ${joined ? "done" : ""}`}>
            <span className="step-num">2</span>
            <div className="step-body">
              <h2>Open it</h2>
              {os === "windows" && (
                <>
                  <p className="muted">Open the file you downloaded. When Windows asks whether restash may make changes, click Yes: it sets itself up to run in the background.</p>
                  <div className="notice">
                    <Icon.shield />
                    <span>Windows may say it protected your PC. Click "More info", then "Run anyway". restash is open source and not signed by Microsoft yet.</span>
                  </div>
                </>
              )}
              {os === "macos" && (
                <>
                  <p className="muted">Open the zip you downloaded, then open the restash-setup file inside it. A Terminal window opens and sets it up.</p>
                  <div className="notice">
                    <Icon.shield />
                    <span>
                      If macOS won't open it, go to System Settings → Privacy & Security and click "Open Anyway". restash is open source and not signed by Apple yet.
                    </span>
                  </div>
                </>
              )}
              {os === "linux" && (
                <>
                  <p className="muted">In a terminal, run:</p>
                  {code && (
                    <div className="command">
                      <code>{linuxCmd}</code>
                      <CopyButton text={linuxCmd} className="" />
                    </div>
                  )}
                </>
              )}
              {code && (
                <p className="small">
                  It's linked to this code: <span className="mono">{code.code}</span>. It works once and expires in {minutesLeft(code.expiresAt)} minutes.
                </p>
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
                  It's linked, unlocked and runs in the background. Next, pick the folders to back up.
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
                  Codes work for an hour. Get a new one and download restash again.
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
              {builds
                .filter((o) => o.target !== b.target)
                .map((o) => (
                  <a key={o.target} href="#" onClick={(e) => (e.preventDefault(), setTarget(o.target))}>
                    <Icon.download /> {o.label}
                  </a>
                ))}
            </div>
            {code && (
              <p className="small" style={{ marginTop: 14 }}>
                Already have restash-agent? Run <span className="mono">restash-agent setup -server {code.server} {code.code}</span>
              </p>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
