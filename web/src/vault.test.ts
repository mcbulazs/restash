// Checks the browser crypto against the Go side's test vectors.
// Run with: npm test
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { entryKey, fromB64, labelPCKey, newRecoveryCode, open, passwordKeys, recoveryKeys, seal, toB64, LockedError } from "./vault.ts";

const v = JSON.parse(readFileSync(new URL("../../internal/vault/testdata/vectors.json", import.meta.url), "utf8"));

test("password keys match Go", async () => {
  const k = await passwordKeys(v.password, fromB64(v.salt), v.iterations);
  assert.equal(toB64(k.auth), v.auth);
  assert.equal(toB64(k.wrap), v.wrap);
});

test("recovery keys match Go", async () => {
  const k = await recoveryKeys(v.recovery);
  assert.equal(toB64(k.auth), v.recoveryAuth);
  assert.equal(toB64(k.wrap), v.recoveryWrap);
});

test("seal matches Go and opens", async () => {
  const key = fromB64(v.sealKey);
  const sealed = await seal(key, new TextEncoder().encode(v.sealPlain), v.sealLabel, fromB64(v.sealNonce));
  assert.equal(sealed, v.sealed);
  assert.equal(new TextDecoder().decode(await open(key, v.sealed, v.sealLabel)), v.sealPlain);
  await assert.rejects(open(key, v.sealed, labelPCKey("other")), LockedError);
});

test("new recovery codes are valid", async () => {
  const c = newRecoveryCode();
  assert.match(c, /^([0-9A-Z]{4}-){5}[0-9A-Z]{4}$/);
  await recoveryKeys(c.toLowerCase().replaceAll("-", " "));
});

test("entry key matches Go", async () => {
  assert.equal(toB64(await entryKey(fromB64(v.sealKey))), v.entryKey);
});
