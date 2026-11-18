// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
// Secrets setup/unlock request shapes and the setup probe (#4445).
import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { pbkdf2Sync, createHash } from "node:crypto";
import "../lib/auth/node-ts-resolve.mjs";

const { resetLoginRedirect } = await import("../lib/api.ts");
const { computeVerifier, deriveKey } = await import("../lib/crypto.ts");
const store = await import("./secrets.ts");
const { useSecretsStore, probeNeedsSetup, fetchKeyInfo, buildSetupRequest, buildVerifyRequest } = store;

const PASSWORD = "correct horse battery staple";
const SALT = Buffer.from("0123456789abcdef").toString("base64");

/**
 * The verifier every existing household has stored (pre-051 as key_hash,
 * post-051 under argon2id): base64(SHA-256(PBKDF2-SHA256(password, salt,
 * 100k, 32 bytes))). Computed independently with node:crypto.
 */
function legacyVerifier(password, saltB64) {
  const raw = pbkdf2Sync(password, Buffer.from(saltB64, "base64"), 100_000, 32, "sha256");
  return createHash("sha256").update(raw).digest("base64");
}

let assigned;
let calls;
let routes;

/** fetch stub: routes maps "METHOD /path" to [status, body]. */
function installFetch() {
  calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const u = new URL(url);
    const method = init.method ?? "GET";
    calls.push({ method, path: u.pathname, body: init.body === undefined ? undefined : JSON.parse(init.body) });
    const [status, body] = routes[`${method} ${u.pathname}`] ?? [500, { error: { code: "UNROUTED" } }];
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  };
}

beforeEach(() => {
  assigned = [];
  routes = {};
  resetLoginRedirect();
  globalThis.window = {
    location: { origin: "https://home.example", pathname: "/dashboard/settings", search: "", assign: (to) => assigned.push(to) },
  };
  installFetch();
  useSecretsStore.getState().lock();
});

test("verifier value is unchanged: matches the pre-051 key_hash computation", async () => {
  const key = await deriveKey(PASSWORD, SALT);
  assert.equal(await computeVerifier(key), legacyVerifier(PASSWORD, SALT));
});

test("the vault key is NON-EXTRACTABLE and its verifier is the stored value (#4575)", async () => {
  const { deriveKeyAndVerifier } = await import("../lib/crypto.ts");
  const { key, verifier } = await deriveKeyAndVerifier(PASSWORD, SALT);
  // Same verifier as the pre-051 key_hash, so no household is locked out.
  assert.equal(verifier, legacyVerifier(PASSWORD, SALT));
  // ...but the key itself cannot be exported out of WebCrypto.
  assert.equal(key.extractable, false);
  await assert.rejects(() => crypto.subtle.exportKey("raw", key));
  // It still encrypts/decrypts, which is all the vault needs it for.
  assert.deepEqual(key.usages.sort(), ["decrypt", "encrypt"]);
  const { encrypt, decrypt } = await import("../lib/crypto.ts");
  const { ciphertext, iv } = await encrypt(key, JSON.stringify({ a: "b" }));
  assert.equal(await decrypt(key, ciphertext, iv), JSON.stringify({ a: "b" }));
});

test("setup/unlock put a non-extractable key in the store", async () => {
  routes["GET /bff/secrets/key"] = [200, { data: { key_salt: SALT, key_version: 1 } }];
  routes["POST /bff/secrets/verify"] = [200, { data: { verified: true } }];
  await useSecretsStore.getState().unlock(PASSWORD);
  const stored = useSecretsStore.getState().cryptoKey;
  assert.ok(stored);
  assert.equal(stored.extractable, false);
});

test("request builders send `verifier`, never `key_hash`", () => {
  assert.deepEqual(buildSetupRequest("v", "s", 1), { verifier: "v", key_salt: "s", key_version: 1 });
  assert.deepEqual(buildVerifyRequest("v", 1), { verifier: "v", key_version: 1 });
});

test("setup POSTs {verifier, key_salt, key_version} through the BFF, no password or key", async () => {
  routes["POST /bff/secrets/setup"] = [201, { data: { id: "k1" } }];
  await useSecretsStore.getState().setup(PASSWORD);

  assert.equal(calls.length, 1);
  const [{ method, path, body }] = calls;
  assert.equal(method, "POST");
  assert.equal(path, "/bff/secrets/setup");
  assert.deepEqual(Object.keys(body).sort(), ["key_salt", "key_version", "verifier"]);
  assert.equal(body.key_version, 1);
  assert.equal(body.verifier, legacyVerifier(PASSWORD, body.key_salt));
  assert.equal(Buffer.from(body.verifier, "base64").length, 32); // API accepts 16-128 bytes
  assert.ok(!JSON.stringify(body).includes(PASSWORD));

  const state = useSecretsStore.getState();
  assert.equal(state.isUnlocked, true);
  // Nothing derived from the password is kept in state except the CryptoKey.
  const { cryptoKey, ...rest } = state;
  assert.ok(cryptoKey);
  const serialized = JSON.stringify(rest);
  assert.ok(!serialized.includes(PASSWORD));
  assert.ok(!serialized.includes(body.verifier));
});

test("unlock: GET /bff/secrets/key, then POST /bff/secrets/verify {verifier, key_version}", async () => {
  routes["GET /bff/secrets/key"] = [200, { data: { key_salt: SALT, key_version: 1 } }];
  routes["POST /bff/secrets/verify"] = [200, { data: { verified: true } }];
  await useSecretsStore.getState().unlock(PASSWORD);

  assert.deepEqual(
    calls.map((c) => `${c.method} ${c.path}`),
    ["GET /bff/secrets/key", "POST /bff/secrets/verify"],
  );
  assert.equal(calls[0].body, undefined);
  assert.deepEqual(calls[1].body, { verifier: legacyVerifier(PASSWORD, SALT), key_version: 1 });
  assert.equal(useSecretsStore.getState().isUnlocked, true);
});

test("unlock with a wrong password: 422 -> 'Incorrect master password.', no /login redirect", async () => {
  routes["GET /bff/secrets/key"] = [200, { data: { key_salt: SALT, key_version: 1 } }];
  routes["POST /bff/secrets/verify"] = [422, { error: { code: "INVALID_MASTER_KEY", message: "invalid master key" } }];
  await assert.rejects(useSecretsStore.getState().unlock("wrong"), /Incorrect master password\./);
  assert.deepEqual(assigned, []);
  // One /verify, no session re-check: a 422 never costs a token refresh.
  assert.deepEqual(calls.map((c) => c.path), ["/bff/secrets/key", "/bff/secrets/verify"]);
  assert.equal(useSecretsStore.getState().isUnlocked, false);
  assert.equal(useSecretsStore.getState().cryptoKey, null);
  assert.equal(useSecretsStore.getState().error, "Incorrect master password.");
});

test("unlock while locked out: 429 -> a wait message, no /login redirect", async () => {
  routes["GET /bff/secrets/key"] = [200, { data: { key_salt: SALT, key_version: 1 } }];
  routes["POST /bff/secrets/verify"] = [429, { error: { code: "TOO_MANY_ATTEMPTS", message: "too many wrong master passwords" } }];
  await assert.rejects(useSecretsStore.getState().unlock(PASSWORD), /Too many wrong master passwords/);
  assert.deepEqual(assigned, []);
  assert.equal(useSecretsStore.getState().isUnlocked, false);
});

test("unlock when the session has ended: /verify 401 sends the browser to /login", async () => {
  routes["GET /bff/secrets/key"] = [200, { data: { key_salt: SALT, key_version: 1 } }];
  routes["POST /bff/secrets/verify"] = [401, { error: { code: "UNAUTHORIZED", message: "session expired" } }];
  await assert.rejects(useSecretsStore.getState().unlock(PASSWORD), (e) => e.status === 401);
  assert.deepEqual(assigned, ["/login?redirect=%2Fdashboard%2Fsettings"]);
  assert.equal(useSecretsStore.getState().isUnlocked, false);
});

test("unlock when not set up: a clear error, no /verify call", async () => {
  routes["GET /bff/secrets/key"] = [404, { error: { code: "NOT_FOUND" } }];
  await assert.rejects(useSecretsStore.getState().unlock(PASSWORD), /not been set up/);
  assert.deepEqual(calls.map((c) => c.path), ["/bff/secrets/key"]);
});

test("probeNeedsSetup: GET /bff/secrets/key, 404 means setup is needed", async () => {
  routes["GET /bff/secrets/key"] = [404, { error: { code: "NOT_FOUND" } }];
  assert.equal(await probeNeedsSetup(), true);
  assert.deepEqual(calls.map((c) => `${c.method} ${c.path}`), ["GET /bff/secrets/key"]);
});

test("probeNeedsSetup: 200 means already set up", async () => {
  routes["GET /bff/secrets/key"] = [200, { data: { key_salt: SALT, key_version: 1 } }];
  assert.equal(await probeNeedsSetup(), false);
});

test("probeNeedsSetup: other errors (400, 500) fall back to unlock, never setup", async () => {
  for (const status of [400, 500, 502]) {
    routes["GET /bff/secrets/key"] = [status, { error: { code: "X" } }];
    assert.equal(await probeNeedsSetup(), false, `status ${status}`);
  }
  assert.ok(calls.every((c) => c.path === "/bff/secrets/key" && c.method === "GET"));
});

test("fetchKeyInfo: null on 404, throws on other errors", async () => {
  routes["GET /bff/secrets/key"] = [404, {}];
  assert.equal(await fetchKeyInfo(), null);
  routes["GET /bff/secrets/key"] = [500, {}];
  await assert.rejects(fetchKeyInfo(), (e) => e.status === 500);
});
