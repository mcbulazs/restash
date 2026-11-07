// Log in, sign up (from an invite link) and recovery.
import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { api, type User } from "../api.ts";
import { logIn, MIN_PASSWORD, prepareSignup, recoverAccount, type PreparedSignup } from "../account.ts";
import { Link, navigate, query } from "../router.tsx";
import { ErrorBox, Field, Icon, Logo } from "../components/ui.tsx";

type OnUser = (u: User) => void;

const ART = [0.4, 0.55, 0.45, 0.6, 0.5, 0.7, 0.55, 0.65, 0.75, 0.6, 0.7, 0.8, 0.65, 0.75, 0.85, 0.7, 0.8, 0.9, 0.75, 0.85, 0.95, 0.8, 0.9, 1, 0.85, 0.95, 1, 0.9, 1, 1];

function Explain({ title, steps }: { title: string; steps: [string, string][] }) {
  return (
    <div className="split-right">
      <div className="explain">
        <div className="tally-art" aria-hidden="true">
          {ART.map((o, i) => (
            <i key={i} style={{ opacity: o, background: i % 9 === 4 ? "var(--cobalt-mid)" : undefined }} />
          ))}
        </div>
        <h2>{title}</h2>
        <ol>
          {steps.map(([a, b], i) => (
            <li key={i}>
              <span className="n">{i + 1}</span>
              <div>
                <strong>{a}</strong>
                <span>{b}</span>
              </div>
            </li>
          ))}
        </ol>
      </div>
    </div>
  );
}

const loginSteps: [string, string][] = [
  ["You type your password here", "It never leaves this page."],
  ["Your browser turns it into your key", "The key unlocks your backups and file names, right here on your screen."],
  ["Only locked data travels", "The server stores and sends encrypted pieces. It can't open them, and neither can its admin."],
];

function Split({ children, title, steps }: { children: ReactNode; title: string; steps: [string, string][] }) {
  return (
    <div className="split">
      <div className="split-left">
        <div className="split-form">{children}</div>
      </div>
      <Explain title={title} steps={steps} />
    </div>
  );
}

function useBusy() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, setError, run };
}

function afterLogin() {
  const next = query("next");
  navigate(next.startsWith("/") && !next.startsWith("//") ? next : "/", true);
}

export function LoginPage({ onUser }: { onUser: OnUser }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const { busy, error, run } = useBusy();
  const submit = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      onUser(await logIn(email, password));
      afterLogin();
    });
  };
  return (
    <Split title="What happens when you log in" steps={loginSteps}>
      <Logo href="/login" />
      <div className="head">
        <h1>Log in</h1>
        <p>Your password also unlocks your backups, so type it carefully.</p>
      </div>
      <form className="form" onSubmit={submit}>
        <Field label="Email" type="email" name="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} />
        <Field
          label="Password"
          type="password"
          name="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          icon={<Icon.lock />}
        />
        <ErrorBox error={error} />
        <button className="btn btn-primary btn-big btn-block" disabled={busy}>
          {busy ? "Unlocking…" : "Log in"}
        </button>
      </form>
      <div className="forgot">
        <h3>Forgot your password?</h3>
        <p>Use the recovery code you wrote down when you signed up. Without it, nobody can reset your password for you.</p>
        <Link href="/recover">Log in with recovery code</Link>
      </div>
    </Split>
  );
}

function checkPasswords(a: string, b: string) {
  if (a.length < MIN_PASSWORD) throw new Error(`Use at least ${MIN_PASSWORD} characters. A few unrelated words work well.`);
  if (a !== b) throw new Error("The two passwords don't match.");
}

export function SignupPage({ onUser }: { onUser: OnUser }) {
  const token = query("invite");
  const [invite, setInvite] = useState<{ email: string; admin: boolean } | null>(null);
  const [inviteError, setInviteError] = useState<unknown>(null);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [password2, setPassword2] = useState("");
  const [prepared, setPrepared] = useState<PreparedSignup | null>(null);
  const { busy, error, run } = useBusy();

  useEffect(() => {
    if (!token) {
      setInviteError(new Error("You need an invite link to make an account. Ask the person who runs this restash."));
      return;
    }
    api.invite(token).then(
      (inv) => {
        setInvite(inv);
        setEmail(inv.email);
      },
      setInviteError,
    );
  }, [token]);

  if (prepared) return <RecoveryCodeScreen email={email} prepared={prepared} onUser={onUser} />;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      checkPasswords(password, password2);
      setPrepared(await prepareSignup(token, email, name, password));
    });
  };

  return (
    <Split
      title="What happens when you sign up"
      steps={[
        ["Your password becomes your key", "Your browser turns it into a key that locks your backups. The password itself never leaves this page."],
        ["You get a recovery code", "It's the only other way in. Nobody can reset your password, not even the admin."],
        ["Then you add your PCs", "Each PC backs up with its own key, locked with yours."],
      ]}
    >
      <Logo href="/login" />
      <div className="head">
        <h1>{invite?.admin ? "Set up restash" : "Make your account"}</h1>
        <p>{invite?.admin ? "This first account runs the server. You can invite family and friends after." : "You've been invited to back up your PCs here."}</p>
      </div>
      {inviteError ? (
        <>
          <ErrorBox error={inviteError} />
          <p className="small">
            Already have an account? <Link href="/login">Log in</Link>
          </p>
        </>
      ) : (
        <form className="form" onSubmit={submit}>
          <Field label="Your name" name="name" autoComplete="name" required maxLength={60} value={name} onChange={(e) => setName(e.target.value)} hint="Shown to the admin and on your PCs." />
          <Field label="Email" type="email" name="email" autoComplete="username" required readOnly={!!invite?.email} value={email} onChange={(e) => setEmail(e.target.value)} />
          <Field
            label="Password"
            type="password"
            name="new-password"
            autoComplete="new-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            hint={`At least ${MIN_PASSWORD} characters. It unlocks your backups, so pick one you'll remember.`}
          />
          <Field label="Password again" type="password" name="new-password-2" autoComplete="new-password" required value={password2} onChange={(e) => setPassword2(e.target.value)} />
          <ErrorBox error={error} />
          <button className="btn btn-primary btn-big btn-block" disabled={busy || !invite}>
            {busy ? "Making your key…" : "Continue"}
          </button>
        </form>
      )}
    </Split>
  );
}

function RecoveryCodeScreen({ email, prepared, onUser }: { email: string; prepared: PreparedSignup; onUser: OnUser }) {
  const [saved, setSaved] = useState(false);
  const [copied, setCopied] = useState(false);
  const { busy, error, run } = useBusy();
  const code = prepared.recoveryCode;
  const shown = code.replaceAll("-", " ");
  const text = `restash recovery code\n\nAccount: ${email}\nSite: ${location.origin}\nCode: ${code}\n\nIf you forget your password, this code is the only way back into your backups.\n`;

  const saveFile = () => {
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "restash-recovery-code.txt";
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className="center-screen">
      <div className="dialog">
        <div className="dialog-bar">restash setup</div>
        <div className="dialog-body">
          <div className="progress">
            <span className="on">Account</span>
            <span className="on now">Recovery code</span>
            <span>Your PCs</span>
          </div>
          <div>
            <h1 style={{ fontSize: 30 }}>Save your recovery code</h1>
            <p className="muted" style={{ marginTop: 12, maxWidth: 640 }}>
              If you ever forget your password, this code is the only way back into your backups. Nobody can reset it for you, not even the admin.
            </p>
          </div>
          <div className="code-box" aria-label="Recovery code">
            {shown}
          </div>
          <div className="btn-row">
            <button type="button" className="btn btn-secondary" onClick={() => window.print()}>
              Print it
            </button>
            <button type="button" className="btn btn-secondary" onClick={saveFile}>
              <Icon.download /> Save as a file
            </button>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => navigator.clipboard.writeText(code).then(() => setCopied(true), () => {})}
            >
              {copied ? "Copied" : "Copy"}
            </button>
          </div>
          <div className="notice">
            <Icon.warning />
            <span>Don't keep it only on this computer. If it dies, the code dies with it. Paper in a drawer works well.</span>
          </div>
          <ErrorBox error={error} />
          <div className="dialog-foot">
            <label className="check">
              <input type="checkbox" checked={saved} onChange={(e) => setSaved(e.target.checked)} />
              I've written it down or saved it somewhere other than this computer
            </label>
            <button
              className="btn btn-primary btn-big"
              disabled={!saved || busy}
              onClick={() =>
                run(async () => {
                  onUser(await prepared.submit());
                  navigate("/", true);
                })
              }
            >
              {busy ? "Making your account…" : "Continue"}
            </button>
          </div>
        </div>
      </div>
      <div className="print-only">
        <h2>restash recovery code</h2>
        <p>Account: {email}</p>
        <p>Site: {location.origin}</p>
        <p className="mono" style={{ fontSize: 24, margin: "24px 0" }}>
          {shown}
        </p>
        <p>If you forget your password, this code is the only way back into your backups.</p>
      </div>
    </div>
  );
}

export function RecoverPage({ onUser }: { onUser: OnUser }) {
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [password2, setPassword2] = useState("");
  const { busy, error, run } = useBusy();
  const submit = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      checkPasswords(password, password2);
      onUser(await recoverAccount(email, code, password));
      navigate("/", true);
    });
  };
  return (
    <Split
      title="What happens when you recover"
      steps={[
        ["Your recovery code unlocks your key", "Right here in your browser. The code itself never leaves this page."],
        ["You pick a new password", "Your key gets locked again with it. Your backups stay exactly as they were."],
        ["Other devices log out", "Log in again there with the new password. Your recovery code keeps working."],
      ]}
    >
      <Logo href="/login" />
      <div className="head">
        <h1>Recover your account</h1>
        <p>Use the recovery code you saved when you signed up, then pick a new password.</p>
      </div>
      <form className="form" onSubmit={submit}>
        <Field label="Email" type="email" name="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} />
        <Field
          label="Recovery code"
          name="recovery-code"
          autoComplete="off"
          spellCheck={false}
          required
          placeholder="XXXX XXXX XXXX XXXX XXXX XXXX"
          className="input mono"
          value={code}
          onChange={(e) => setCode(e.target.value)}
        />
        <Field label="New password" type="password" name="new-password" autoComplete="new-password" required value={password} onChange={(e) => setPassword(e.target.value)} hint={`At least ${MIN_PASSWORD} characters.`} />
        <Field label="New password again" type="password" name="new-password-2" autoComplete="new-password" required value={password2} onChange={(e) => setPassword2(e.target.value)} />
        <ErrorBox error={error} />
        <button className="btn btn-primary btn-big btn-block" disabled={busy}>
          {busy ? "Unlocking…" : "Set new password"}
        </button>
      </form>
      <p className="small">
        Remembered it? <Link href="/login">Log in</Link>
      </p>
    </Split>
  );
}
