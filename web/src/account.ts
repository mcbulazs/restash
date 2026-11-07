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

// The unlocked account key, kept only in memory for this tab.
let accountKey: Uint8Array | null = null;

export function forgetKeys() {
  accountKey = null;
}

export function hasAccountKey() {
  return accountKey !== null;
}

export async function logIn(email: string, password: string): Promise<User> {
  const p = await api.params(email);
  const k = await passwordKeys(password, fromB64(p.kdfSalt), p.kdfIterations);
  const res = await api.login(email, toB64(k.auth));
  accountKey = await open(k.wrap, res.accountKey, LABEL_ACCOUNT_KEY);
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
      accountKey = key;
      return res.user;
    },
  };
}

export async function recoverAccount(email: string, code: string, newPassword: string): Promise<User> {
  const rk = await recoveryKeys(code);
  const { recoveryAccountKey } = await api.recoverKey(email, toB64(rk.auth));
  const key = await open(rk.wrap, recoveryAccountKey, LABEL_ACCOUNT_KEY);
  const res = await api.recover(email, toB64(rk.auth), await newCredentials(newPassword, key));
  accountKey = key;
  return res.user;
}

export const MIN_PASSWORD = 10;
