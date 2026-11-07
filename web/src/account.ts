// Logging in, signing up and recovering: the steps that turn a password or
// recovery code into keys. The password and the unlocked account key never
// leave this page.
import { api, type User } from "./api.ts";
import {
  fromB64,
  LABEL_ACCOUNT_KEY,
  newCredentials,
  open,
  passwordKeys,
  randomBytes,
  recoveryKeys,
  seal,
  toB64,
  newRecoveryCode,
} from "./vault.ts";

// The unlocked account key. It is kept in this tab's session storage so a
// reload doesn't ask for the password again; closing the tab or logging out
// forgets it. It's stored with whose key it is: the login cookie is shared
// by all tabs, so another tab may have logged in as someone else since.
const STORE = "restash.accountKey";
let accountKey: Uint8Array | null = null;
let keyOwner = "";
try {
  const saved = JSON.parse(sessionStorage.getItem(STORE) ?? "null") as { user: string; secret: string } | null;
  if (saved?.user && saved.secret) {
    accountKey = fromB64(saved.secret);
    keyOwner = saved.user;
  }
} catch {
  /* storage blocked or an older format: ask for the password again */
}

function remember(secret: Uint8Array, user: string) {
  accountKey = secret;
  keyOwner = user;
  try {
    sessionStorage.setItem(STORE, JSON.stringify({ user, secret: toB64(secret) }));
  } catch {
    /* memory only */
  }
}

export function forgetKeys() {
  accountKey = null;
  keyOwner = "";
  try {
    sessionStorage.removeItem(STORE);
  } catch {
    /* nothing stored */
  }
}

/** Drops the cached key if it belongs to someone other than user. */
export function keepKeyFor(user: string) {
  if (accountKey && keyOwner !== user) forgetKeys();
}

export function getAccountKey(): Uint8Array | null {
  return accountKey;
}

/** Unlocks the account key again with the password (e.g. in a new tab). */
export async function unlock(password: string): Promise<void> {
  const me = await api.me();
  const k = await passwordKeys(password, fromB64(me.kdfSalt), me.kdfIterations);
  remember(await open(k.wrap, me.accountKey, LABEL_ACCOUNT_KEY), me.user.id);
}

export async function logIn(email: string, password: string): Promise<User> {
  const p = await api.params(email);
  const k = await passwordKeys(password, fromB64(p.kdfSalt), p.kdfIterations);
  const res = await api.login(email, toB64(k.auth));
  remember(await open(k.wrap, res.accountKey, LABEL_ACCOUNT_KEY), res.user.id);
  return res.user;
}

/** Everything needed to make an account, made before anything is sent. */
export interface PreparedSignup {
  recoveryCode: string;
  submit: () => Promise<User>;
}

export async function prepareSignup(invite: string, email: string, name: string, password: string): Promise<PreparedSignup> {
  const key = randomBytes(32);
  const code = newRecoveryCode();
  const creds = await newCredentials(password, key);
  const rk = await recoveryKeys(code);
  const recoveryAccountKey = await seal(rk.wrap, key, LABEL_ACCOUNT_KEY);
  return {
    recoveryCode: code,
    submit: async () => {
      const res = await api.signup({ invite, email, name, ...creds, recoveryAuth: toB64(rk.auth), recoveryAccountKey });
      remember(key, res.user.id);
      return res.user;
    },
  };
}

export async function recoverAccount(email: string, code: string, newPassword: string): Promise<User> {
  const rk = await recoveryKeys(code);
  const { recoveryAccountKey } = await api.recoverKey(email, toB64(rk.auth));
  const key = await open(rk.wrap, recoveryAccountKey, LABEL_ACCOUNT_KEY);
  const res = await api.recover(email, toB64(rk.auth), await newCredentials(newPassword, key));
  remember(key, res.user.id);
  return res.user;
}

export const MIN_PASSWORD = 10;
