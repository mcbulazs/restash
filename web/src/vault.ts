// Key handling in the browser. This mirrors internal/vault in Go step by step;
// both are checked against internal/vault/testdata/vectors.json.
//
// The password is stretched with PBKDF2 into a master key, which HKDF splits
// into an auth key (sent to the server to log in) and a wrap key (never
// leaves this page; it unlocks the account key).

const subtle = globalThis.crypto.subtle;
const enc = new TextEncoder();

export const ITERATIONS = 600_000;

export interface Keys {
  auth: Uint8Array;
  wrap: Uint8Array;
}

export function toB64(b: Uint8Array): string {
  let s = "";
  for (const c of b) s += String.fromCharCode(c);
  return btoa(s);
}

export function fromB64(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export function randomBytes(n: number): Uint8Array {
  return globalThis.crypto.getRandomValues(new Uint8Array(n));
}

async function hkdf(ikm: Uint8Array, info: string): Promise<Uint8Array> {
  const key = await subtle.importKey("raw", ikm as BufferSource, "HKDF", false, ["deriveBits"]);
  const bits = await subtle.deriveBits({ name: "HKDF", hash: "SHA-256", salt: new Uint8Array(), info: enc.encode(info) }, key, 256);
  return new Uint8Array(bits);
}

async function split(master: Uint8Array): Promise<Keys> {
  return { auth: await hkdf(master, "restash auth v1"), wrap: await hkdf(master, "restash wrap v1") };
}

export async function passwordKeys(password: string, salt: Uint8Array, iterations: number): Promise<Keys> {
  const key = await subtle.importKey("raw", enc.encode(password), "PBKDF2", false, ["deriveBits"]);
  const bits = await subtle.deriveBits({ name: "PBKDF2", hash: "SHA-256", salt: salt as BufferSource, iterations }, key, 256);
  return split(new Uint8Array(bits));
}

// Recovery codes: 24 characters of Crockford base32, shown in groups of 4.
const RECOVERY_ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

export function newRecoveryCode(): string {
  const groups: string[] = [];
  const b = randomBytes(24);
  for (let i = 0; i < 24; i += 4) {
    groups.push(Array.from(b.slice(i, i + 4), (c) => RECOVERY_ALPHABET[c & 31]).join(""));
  }
  return groups.join("-");
}

export function normalizeRecoveryCode(code: string): string {
  let out = "";
  for (let ch of code.toUpperCase()) {
    if (ch === " " || ch === "-" || ch === "\t") continue;
    if (ch === "O") ch = "0";
    if (ch === "I" || ch === "L") ch = "1";
    if (!RECOVERY_ALPHABET.includes(ch)) throw new Error("The recovery code has characters that don't belong in it.");
    out += ch;
  }
  if (out.length !== 24) throw new Error("The recovery code should be 24 characters.");
  return out;
}

export async function recoveryKeys(code: string): Promise<Keys> {
  const master = await hkdf(enc.encode(normalizeRecoveryCode(code)), "restash recovery v1");
  return split(master);
}

export const LABEL_ACCOUNT_KEY = "restash account key";
export const labelPCKey = (pcID: string) => "restash pc key " + pcID;
export const labelEntry = (pcID: string, entryID: string) => `restash entry ${pcID} ${entryID}`;
export const labelRunDetail = (pcID: string) => "restash run detail " + pcID;
export const labelBrowse = (pcID: string) => "restash browse " + pcID;

/** The key that locks a PC's entry settings and run details. */
export function entryKey(pcKey: Uint8Array): Promise<Uint8Array> {
  return hkdf(pcKey, "restash entries v1");
}

async function aes(key: Uint8Array): Promise<CryptoKey> {
  return subtle.importKey("raw", key as BufferSource, "AES-GCM", false, ["encrypt", "decrypt"]);
}

export async function seal(key: Uint8Array, plain: Uint8Array, label: string, nonce = randomBytes(12)): Promise<string> {
  const ct = await subtle.encrypt({ name: "AES-GCM", iv: nonce as BufferSource, additionalData: enc.encode(label) }, await aes(key), plain as BufferSource);
  const out = new Uint8Array(12 + ct.byteLength);
  out.set(nonce);
  out.set(new Uint8Array(ct), 12);
  return toB64(out);
}

export class LockedError extends Error {
  constructor() {
    super("Could not unlock: wrong password or damaged data.");
  }
}

export async function open(key: Uint8Array, sealed: string, label: string): Promise<Uint8Array> {
  let raw: Uint8Array;
  try {
    raw = fromB64(sealed);
  } catch {
    throw new LockedError();
  }
  if (raw.length < 28) throw new LockedError();
  try {
    const pt = await subtle.decrypt({ name: "AES-GCM", iv: raw.slice(0, 12), additionalData: enc.encode(label) }, await aes(key), raw.slice(12));
    return new Uint8Array(pt);
  } catch {
    throw new LockedError();
  }
}

/** New password-dependent fields for the server, locking accountKey. */
export async function newCredentials(password: string, accountKey: Uint8Array) {
  const salt = randomBytes(16);
  const k = await passwordKeys(password, salt, ITERATIONS);
  return {
    kdfSalt: toB64(salt),
    kdfIterations: ITERATIONS,
    auth: toB64(k.auth),
    accountKey: await seal(k.wrap, accountKey, LABEL_ACCOUNT_KEY),
  };
}
