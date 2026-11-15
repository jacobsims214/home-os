#!/usr/bin/env node
/**
 * E2E stub for Dex and the Go API, so Playwright can run the real UI (pages,
 * middleware, BFF, refresh) without the docker compose stack.
 *
 * Why a stub: compose has no Dex yet (#4619), the compose UI has no
 * API_INTERNAL_URL (#4624), and the OIDC callback hardcodes
 * http://home-os-dex:5556/dex/token. Point the UI at this server with
 *   API_INTERNAL_URL=http://127.0.0.1:<port>
 *   DEX_TOKEN_URL=http://127.0.0.1:<port>/dex/token
 *
 * Endpoints:
 *   POST /e2e/login        {email}  -> {id_token, refresh_token, expires_in}
 *                          Test-only stand-in for the Dex code exchange. Only
 *                          the seeded user is accepted.
 *   POST /dex/token        grant_type=refresh_token. Rotates the refresh token
 *                          the way Dex does (old one is single-use).
 *                          grant_type=authorization_code with code=e2e-code
 *                          and a code_verifier: the OIDC callback's exchange
 *                          (#4617). The redirect_uri it sent is recorded.
 *   GET  /e2e/stats        {dexRefreshes, apiRequests, apiUnauthorized,
 *                          codeExchanges, lastRedirectUri, apiWrites}
 *   *    /api/v1/*         401 unless the Bearer is a JWT minted here; fixed
 *                          seed data for the dashboard lists, [] otherwise.
 *                          An authorized POST/PUT/PATCH/DELETE is recorded in
 *                          apiWrites ("METHOD /path?query name", #4646) and
 *                          a POST answers 201 with its JSON body echoed back.
 *   GET/POST /api/v1/secrets/{key,setup,verify}
 *                          Master-key endpoints (#4445), per sign-in session:
 *                          each /e2e/login starts a session (sid claim, kept
 *                          across refreshes), so parallel tests don't share a
 *                          vault. Same statuses as the Go handler: /key 404 if
 *                          not set up; /setup 400 without a 16-128 byte
 *                          verifier, 409 if set up; /verify 400 without one,
 *                          401 on mismatch. A legacy row (seeded key_hash) is
 *                          upgraded on its first successful /verify. The stub
 *                          keeps the verifier as is; the API stores argon2id.
 *   POST /e2e/secrets/seed {sid, key_salt, key_hash}
 *                          Seed a pre-051 (legacy) master key for a session.
 *   GET  /e2e/secrets?sid= {set_up, legacy, upgraded, requests}: what the
 *                          secrets endpoints received (field names only).
 *   GET  /healthz          200 (Playwright webServer readiness probe)
 *
 * Plain node, no dependencies. Never use outside tests: the signing key is
 * random per process and nothing here is secure.
 */
import { createServer } from "node:http";
import { createHmac, randomBytes } from "node:crypto";

const PORT = Number(process.env.E2E_STUB_PORT || 3901);
const HOST = process.env.E2E_STUB_HOST || "127.0.0.1";
const TOKEN_TTL_S = 60 * 60;

const SEED_USER = {
  id: "00000000-0000-4000-8000-000000000001",
  household_id: "00000000-0000-4000-8000-0000000000aa",
  email: process.env.E2E_USER || "e2e@homeos.test",
  name: "E2E Tester",
};

const key = randomBytes(32);
// refresh token -> session id (sid). A sid lives for the whole sign-in.
const refreshTokens = new Map();
// sid -> { key_salt, key_version, verifier?, key_hash?, upgraded, requests[] }
const secretKeys = new Map();
const stats = { dexRefreshes: 0, apiRequests: 0, apiUnauthorized: 0, codeExchanges: 0, lastRedirectUri: null, apiWrites: [] };
// The only authorization code the stub redeems (tests hand it to the callback).
const E2E_CODE = "e2e-code";

const b64url = (v) => Buffer.from(v).toString("base64url");

function mintJwt(sid) {
  const now = Math.floor(Date.now() / 1000);
  const header = b64url(JSON.stringify({ alg: "HS256", typ: "JWT" }));
  const payload = b64url(
    JSON.stringify({
      iss: "e2e-stub",
      aud: "home-os-ui",
      sub: SEED_USER.id,
      email: SEED_USER.email,
      name: SEED_USER.name,
      household_id: SEED_USER.household_id,
      sid,
      iat: now,
      exp: now + TOKEN_TTL_S,
    }),
  );
  const sig = createHmac("sha256", key).update(`${header}.${payload}`).digest("base64url");
  return `${header}.${payload}.${sig}`;
}

function validJwt(jwt) {
  const [h, p, s] = jwt.split(".");
  if (!h || !p || !s) return false;
  if (createHmac("sha256", key).update(`${h}.${p}`).digest("base64url") !== s) return false;
  try {
    return JSON.parse(Buffer.from(p, "base64url").toString("utf8")).exp > Date.now() / 1000;
  } catch {
    return false;
  }
}

function jwtClaims(jwt) {
  try {
    return JSON.parse(Buffer.from(jwt.split(".")[1], "base64url").toString("utf8"));
  } catch {
    return {};
  }
}

function issueTokens(sid = `s-${randomBytes(8).toString("hex")}`) {
  const refresh = `e2e-r-${randomBytes(12).toString("hex")}`;
  refreshTokens.set(refresh, sid);
  return { access_token: "unused", token_type: "bearer", id_token: mintJwt(sid), refresh_token: refresh, expires_in: TOKEN_TTL_S };
}

/** Byte length of a base64 string, or 0 if it isn't one. */
function b64Len(v) {
  if (typeof v !== "string" || !/^[A-Za-z0-9+/]+={0,2}$/.test(v)) return 0;
  return Buffer.from(v, "base64").length;
}

/** /api/v1/secrets/{key,setup,verify}; null for any other secrets path. */
function secretsRoute(method, resource, sid, body) {
  const rec = secretKeys.get(sid);
  if (method === "GET" && resource === "secrets/key") {
    if (!rec) return [404, { error: { code: "NOT_FOUND", message: "master key not set up" } }];
    return [200, { data: { key_salt: rec.key_salt, key_version: rec.key_version } }];
  }
  if (method !== "POST" || (resource !== "secrets/setup" && resource !== "secrets/verify")) return null;
  const verifier = body.verifier || body.key_hash;
  const log = { endpoint: resource, fields: Object.keys(body).sort() };
  if (rec) rec.requests.push(log);
  if (!verifier) return [400, { error: { code: "BAD_REQUEST", message: "verifier is required" } }];
  if (!body.key_version) return [400, { error: { code: "BAD_REQUEST", message: "key_version is required" } }];
  if (resource === "secrets/setup") {
    const n = b64Len(verifier);
    if (n < 16 || n > 128) return [400, { error: { code: "BAD_REQUEST", message: "verifier must be 16 to 128 bytes" } }];
    if (!body.key_salt) return [400, { error: { code: "BAD_REQUEST", message: "key_salt is required" } }];
    if (rec) return [409, { error: { code: "CONFLICT", message: "master key already set up for this version" } }];
    secretKeys.set(sid, { key_salt: body.key_salt, key_version: body.key_version, verifier, upgraded: false, requests: [log] });
    return [201, { data: { id: `key-${sid}`, household_id: SEED_USER.household_id, key_salt: body.key_salt, key_version: body.key_version, created_at: new Date().toISOString() } }];
  }
  // Mirrors the API since #4446: 404 not set up, 422 wrong password (never
  // 401, which the BFF reads as an expired session).
  if (!rec) return [404, { error: { code: "NOT_FOUND", message: "master key not set up" } }];
  if (rec.verifier ? verifier !== rec.verifier : verifier !== rec.key_hash) {
    return [422, { error: { code: "INVALID_MASTER_KEY", message: "invalid master key" } }];
  }
  if (!rec.verifier) {
    rec.verifier = rec.key_hash; // legacy upgrade (the API stores argon2id of it)
    rec.key_hash = undefined;
    rec.upgraded = true;
  }
  return [200, { data: { verified: true } }];
}

// Realistic-length content so phone overflow checks see something wider than
// an empty state. Shapes follow the Go handlers the dashboard reads.
const now = new Date().toISOString();
const SEED = {
  properties: [{ id: "p1", household_id: SEED_USER.household_id, name: "Main house on Longwood Avenue", current_value: "485000", purchase_price: "410000" }],
  assets: [{ id: "a1", name: "Washer", current_value: "900" }],
  vehicles: [{ id: "v1", year: 2019, make: "Honda", model: "Odyssey EX-L", current_value: "24000" }],
  loans: [{ id: "l1", name: "Mortgage", remaining_balance: 312000, interest_rate: 3.25 }],
  bills: [
    { id: "b1", name: "Electricity (Duke Energy, residential time-of-use plan)", amount: "182.40", due_day: 12, category: "utilities" },
    { id: "b2", name: "Internet", amount: "70", due_day: 3, category: "utilities" },
  ],
  "maintenance/tasks": [
    { id: "m1", household_id: SEED_USER.household_id, schedule_id: null, property_id: "p1", asset_id: null, name: "Replace HVAC filter in upstairs hallway return", description: null, status: "pending", due_date: now, completed_at: null, cost: null, vendor_id: null, notes: null, created_at: now, updated_at: now },
  ],
};

async function readBody(req) {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  return Buffer.concat(chunks).toString("utf8");
}

function send(res, status, body) {
  res.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" });
  res.end(JSON.stringify(body));
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url ?? "/", `http://${req.headers.host}`);
  const path = url.pathname;

  if (path === "/healthz") return send(res, 200, { ok: true });
  if (path === "/e2e/stats") return send(res, 200, stats);
  if (path === "/e2e/secrets" && req.method === "GET") {
    const rec = secretKeys.get(url.searchParams.get("sid") ?? "");
    return send(res, 200, { set_up: !!rec, legacy: !!rec?.key_hash, upgraded: !!rec?.upgraded, requests: rec?.requests ?? [] });
  }
  if (path === "/e2e/secrets/seed" && req.method === "POST") {
    const { sid, key_salt, key_hash } = JSON.parse((await readBody(req)) || "{}");
    if (!sid || !key_salt || !key_hash) return send(res, 400, { error: "sid, key_salt and key_hash are required" });
    secretKeys.set(sid, { key_salt, key_version: 1, key_hash, upgraded: false, requests: [] });
    return send(res, 201, { ok: true });
  }

  if (path === "/e2e/login" && req.method === "POST") {
    let email = "";
    try {
      email = JSON.parse((await readBody(req)) || "{}").email ?? "";
    } catch {
      /* fall through to 401 */
    }
    if (email !== SEED_USER.email) return send(res, 401, { error: "access_denied" });
    return send(res, 200, issueTokens());
  }

  if (path === "/dex/token" && req.method === "POST") {
    const form = new URLSearchParams(await readBody(req));
    if (form.get("grant_type") === "authorization_code") {
      if (form.get("code") !== E2E_CODE || !form.get("code_verifier") || form.get("client_id") !== "home-os-ui") {
        return send(res, 400, { error: "invalid_grant" });
      }
      stats.codeExchanges += 1;
      stats.lastRedirectUri = form.get("redirect_uri");
      return send(res, 200, issueTokens());
    }
    const old = form.get("refresh_token") ?? "";
    if (form.get("grant_type") !== "refresh_token" || !refreshTokens.has(old)) {
      return send(res, 400, { error: "invalid_request" });
    }
    stats.dexRefreshes += 1;
    const sid = refreshTokens.get(old);
    refreshTokens.delete(old); // rotation: a refresh token is single-use
    return send(res, 200, issueTokens(sid));
  }

  if (path.startsWith("/api/v1/")) {
    stats.apiRequests += 1;
    const auth = req.headers.authorization ?? "";
    if (!auth.startsWith("Bearer ") || !validJwt(auth.slice(7))) {
      stats.apiUnauthorized += 1;
      return send(res, 401, { error: { code: "UNAUTHORIZED", message: "invalid token" } });
    }
    const resource = path.slice("/api/v1/".length).replace(/\/+$/, "");
    let body = {};
    if (req.method !== "GET" && req.method !== "HEAD") {
      try {
        body = JSON.parse((await readBody(req)) || "{}");
      } catch {
        /* not JSON */
      }
    }
    if (resource.startsWith("secrets/")) {
      const out = secretsRoute(req.method, resource, jwtClaims(auth.slice(7)).sid ?? "", body);
      if (out) return send(res, out[0], out[1]);
    }
    if (req.method !== "GET" && req.method !== "HEAD") {
      stats.apiWrites.push(`${req.method} ${path}${url.search}${body.name ? ` ${body.name}` : ""}`);
      if (req.method === "POST") return send(res, 201, { data: { id: `e2e-${stats.apiWrites.length}`, ...body } });
    }
    if (resource === "auth/me" || resource === "users/me") return send(res, 200, { data: SEED_USER });
    const property = SEED.properties.find((p) => resource === `properties/${p.id}`);
    if (property) return send(res, 200, { data: property });
    return send(res, 200, { data: SEED[resource] ?? [] });
  }

  send(res, 404, { error: { code: "NOT_FOUND", message: path } });
});

server.listen(PORT, HOST, () => {
  console.log(`e2e stub (Dex + API) on http://${HOST}:${PORT}`);
});
for (const sig of ["SIGINT", "SIGTERM"]) process.on(sig, () => server.close(() => process.exit(0)));
