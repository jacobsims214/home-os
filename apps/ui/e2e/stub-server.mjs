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
 *                          codeExchanges, lastRedirectUri, apiWrites,
 *                          householdHeaders}
 *   POST /e2e/households   {sid, households: [{id, name, role, timezone?, bills?}]}
 *                          Multi-household sign-in (#4574), per session like
 *                          the secrets vault: /me/households lists exactly these
 *                          (current = the X-Household-ID the UI sent, else the
 *                          first), /households/me answers the active one and
 *                          /bills returns the active household's rows. Without
 *                          it, every session keeps the single seed household.
 *                          householdHeaders in /e2e/stats is what the API saw:
 *                          one {sid, path, household} per API request, so a spec
 *                          can prove the BFF scoped a request to a family.
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
 *   POST /e2e/secrets/records {sid, records: [{id, name, secret_type,
 *                                              shared_with_agents?}]}
 *                          Seed listed credentials for a session (#4575), so a
 *                          spec can assert the list, the share switch and the
 *                          per-credential activity link without a real vault.
 *                          GET /api/v1/secrets answers these (filtered by
 *                          entity_type/entity_id when the query has them) and
 *                          GET /api/v1/secrets/:id answers one with a blob.
 *   PUT/DELETE /api/v1/secrets/:id/agent-share
 *                          Mirror agentshare.go's role gate: owner and
 *                          family_manager only (403 otherwise), 404 for an
 *                          unknown id, 503 without a key configured. A PUT
 *                          records "PUT agent-share" in apiWrites and sets the
 *                          flag; a DELETE clears it. The value is never kept.
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

/**
 * What the stub's POST /auth/caldav-password reports as the CalDAV server
 * (#5289). A public URL, not this machine's: the UI must display this value
 * verbatim, so a spec fails if a localhost literal ever comes back. Override
 * with E2E_CALDAV_SERVER_URL to assert the UI follows the API rather than a
 * constant.
 */
const CALDAV_SERVER_URL = process.env.E2E_CALDAV_SERVER_URL || "https://home.example/dav/";

const key = randomBytes(32);
// refresh token -> session id (sid). A sid lives for the whole sign-in.
const refreshTokens = new Map();
// sid -> { key_salt, key_version, verifier?, key_hash?, upgraded, requests[] }
const secretKeys = new Map();
/** Credentials per session for the list/view UI (#4575): see /e2e/secrets/records. */
const secretRecords = new Map();
const stats = { dexRefreshes: 0, apiRequests: 0, apiUnauthorized: 0, codeExchanges: 0, lastRedirectUri: null, apiWrites: [], householdHeaders: [] };
// sid -> [{ id, name, role, timezone, bills }]. Empty: the single seed family.
const sessionHouseholds = new Map();
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
  // sid is not part of the real Dex response: it is here so a spec can seed a
  // per-session household list (#4574) with POST /e2e/households.
  return { access_token: "unused", token_type: "bearer", id_token: mintJwt(sid), refresh_token: refresh, expires_in: TOKEN_TTL_S, sid };
}

/** Byte length of a base64 string, or 0 if it isn't one. */
function b64Len(v) {
  if (typeof v !== "string" || !/^[A-Za-z0-9+/]+={0,2}$/.test(v)) return 0;
  return Buffer.from(v, "base64").length;
}

/**
 * The documented shape of a listed credential (#4446): metadata only, plus the
 * sharing flag. encrypted_data/iv appear ONLY on GET /secrets/:id, exactly as
 * the Go handler does it, and agent_ciphertext never appears at all.
 */
function secretListItem(rec, withBlob = false) {
  const item = {
    id: rec.id,
    household_id: rec.household_id,
    entity_type: rec.entity_type,
    entity_id: rec.entity_id,
    name: rec.name,
    secret_type: rec.secret_type,
    key_version: 1,
    created_at: rec.created_at ?? new Date().toISOString(),
    updated_at: rec.created_at ?? new Date().toISOString(),
    shared_with_agents: rec.shared_with_agents === true,
  };
  if (item.shared_with_agents) item.agent_shared_at = rec.agent_shared_at;
  if (withBlob) {
    item.encrypted_data = rec.encrypted_data ?? "";
    item.iv = rec.iv ?? "";
  }
  return item;
}

/**
 * What the API lets one role do with "shared with agents" (#4446's
 * CanShareWithAgents): owner and family_manager only.
 */
const SHARE_ROLES = new Set(["owner", "family_manager"]);

/**
 * /api/v1/secrets/{key,setup,verify} and, since #4575, the list, one record
 * and the agent-share endpoints. null for any other secrets path.
 *
 * `ctx` carries the request's query string and the caller's role, because the
 * list is filtered and sharing is role-gated. The role comes from the stub's
 * seeded households: the first household is the active one, which is what
 * /me/households resolves to without an X-Household-ID.
 */
function secretsRoute(method, resource, sid, body, ctx = {}) {
  const rec = secretKeys.get(sid);
  if (method === "GET" && resource === "secrets/key") {
    if (!rec) return [404, { error: { code: "NOT_FOUND", message: "master key not set up" } }];
    return [200, { data: { key_salt: rec.key_salt, key_version: rec.key_version } }];
  }
  // ── Per-credential agent sharing (#4575) ────────────────────────────────
  // Mirrors apps/api/internal/secret/agentshare.go: role gate first (403),
  // then the id (404), then the server key (503 on PUT). The value is dropped
  // immediately: this stub records only that a share happened.
  const shareMatch = /^secrets\/([^/]+)\/agent-share$/.exec(resource);
  if (shareMatch && (method === "PUT" || method === "DELETE")) {
    if (!SHARE_ROLES.has(ctx.role)) {
      return [403, { error: { code: "FORBIDDEN", message: "only an owner or family manager can change agent sharing" } }];
    }
    const rows = secretRecords.get(sid) ?? [];
    const row = rows.find((r) => r.id === shareMatch[1]);
    if (!row) return [404, { error: { code: "NOT_FOUND", message: "secret not found" } }];
    if (method === "PUT") {
      if (!rec) return [503, { error: { code: "AGENT_SHARING_DISABLED", message: "sharing credentials with agents is not configured on this server" } }];
      if (!body || typeof body.value !== "object" || body.value === null) {
        return [400, { error: { code: "BAD_REQUEST", message: "value is required" } }];
      }
      row.shared_with_agents = true;
      row.agent_shared_at = new Date().toISOString();
      row.agent_uses = row.agent_uses ?? 0;
    } else {
      row.shared_with_agents = false;
      row.agent_shared_at = undefined;
    }
    return [200, { data: secretListItem(row) }];
  }

  // ── List and one record (#4575) ─────────────────────────────────────────
  // GET /secrets is filtered by entity_type/entity_id when the query carries
  // them (the credentials section always scopes to a record).
  if (method === "GET" && (resource === "secrets" || resource.startsWith("secrets/"))) {
    const rows = secretRecords.get(sid) ?? [];
    if (resource === "secrets") {
      const params = new URLSearchParams(ctx.search ?? "");
      const entityType = params.get("entity_type");
      const entityId = params.get("entity_id");
      const list = rows.filter(
        (r) =>
          (!entityType || r.entity_type === entityType) && (!entityId || r.entity_id === entityId),
      );
      return [200, { data: list.map((r) => secretListItem(r)) }];
    }
    const id = resource.slice("secrets/".length);
    const row = rows.find((r) => r.id === id);
    if (!row) return [404, { error: { code: "NOT_FOUND", message: "secret not found" } }];
    return [200, { data: secretListItem(row, true) }];
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
  // Credentials for the list/view UI (#4575), per sign-in session like the
  // vault itself. The blob is a placeholder: no spec decrypts it, because the
  // specs that matter here are about the list, the share switch and the link.
  if (path === "/e2e/secrets/records" && req.method === "POST") {
    const { sid, records } = JSON.parse((await readBody(req)) || "{}");
    secretRecords.set(
      sid,
      (records ?? []).map((r) => ({
        household_id: SEED_USER.household_id,
        secret_type: "login",
        created_at: new Date().toISOString(),
        encrypted_data: "",
        iv: "",
        ...r,
      })),
    );
    return send(res, 201, { ok: true });
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

  if (path === "/e2e/households" && req.method === "POST") {
    const { sid, households } = JSON.parse((await readBody(req)) || "{}");
    if (!sid || !Array.isArray(households) || households.length === 0) {
      return send(res, 400, { error: "sid and a non-empty households list are required" });
    }
    sessionHouseholds.set(sid, households);
    return send(res, 201, { ok: true });
  }

  if (path.startsWith("/api/v1/")) {
    stats.apiRequests += 1;
    const auth = req.headers.authorization ?? "";
    if (!auth.startsWith("Bearer ") || !validJwt(auth.slice(7))) {
      stats.apiUnauthorized += 1;
      return send(res, 401, { error: { code: "UNAUTHORIZED", message: "invalid token" } });
    }
    const resource = path.slice("/api/v1/".length).replace(/\/+$/, "");
    const claims = jwtClaims(auth.slice(7));
    // #4574: what the Go API would resolve the request to. The BFF sets this
    // from the home-os-household cookie; a browser-supplied header never
    // reaches here, which is the point of the assertion. Keyed by the session's
    // sid so parallel specs (one stub process) can read only their own.
    const headerHousehold = req.headers["x-household-id"] ?? null;
    stats.householdHeaders.push({ sid: claims.sid ?? null, path: resource, household: headerHousehold });
    const households = sessionHouseholds.get(claims.sid ?? "");
    const activeHousehold =
      households?.find((h) => h.id === headerHousehold) ?? households?.[0] ?? null;
    let body = {};
    if (req.method !== "GET" && req.method !== "HEAD") {
      try {
        body = JSON.parse((await readBody(req)) || "{}");
      } catch {
        /* not JSON */
      }
    }
    if (resource === "secrets" || resource.startsWith("secrets/")) {
      // The active household's role is what the API reads from the claims; the
      // stub derives it the same way /me/households does (#4575's share gate).
      const out = secretsRoute(
        req.method,
        resource,
        jwtClaims(auth.slice(7)).sid ?? "",
        body,
        { search: url.search, role: activeHousehold?.role ?? "owner" },
      );
      if (out) return send(res, out[0], out[1]);
    }
    // POST /auth/caldav-password (#5289). Mirrors the API's caldavPasswordResponse
    // (apps/api/internal/auth/handler.go): the plaintext is returned once, and
    // server_url is built from the API's PUBLIC_URL. The stub answers a fixed
    // public URL on purpose — it is what the UI must show verbatim instead of a
    // localhost literal, and a spec asserts exactly that.
    if (resource === "auth/caldav-password" && req.method === "POST") {
      return send(res, 200, {
        password: "e2eapppassword0123456789abcdefghij",
        username: SEED_USER.email,
        server_url: CALDAV_SERVER_URL,
      });
    }
    if (req.method !== "GET" && req.method !== "HEAD") {
      stats.apiWrites.push(`${req.method} ${path}${url.search}${body.name ? ` ${body.name}` : ""}`);
      if (req.method === "POST") return send(res, 201, { data: { id: `e2e-${stats.apiWrites.length}`, ...body } });
    }
    if (resource === "auth/me" || resource === "users/me") return send(res, 200, { data: SEED_USER });
    if (households) {
      // Mirrors GET /api/v1/me/households: role per household, is_default on
      // the first, current on the one X-Household-ID resolved to.
      if (resource === "me/households") {
        return send(res, 200, {
          data: households.map((h, i) => ({
            id: h.id,
            name: h.name,
            timezone: h.timezone ?? "UTC",
            role: h.role,
            joined_at: "2026-01-01T00:00:00Z",
            is_default: i === 0,
            current: h.id === activeHousehold?.id,
          })),
        });
      }
      if (resource === "households/me" && activeHousehold) {
        return send(res, 200, {
          data: { id: activeHousehold.id, name: activeHousehold.name, timezone: activeHousehold.timezone ?? "UTC" },
        });
      }
      // Lists and records the spec seeds per household: only the active
      // family's rows exist here. Another family's bill id is a 404, exactly as
      // the Go API answers for a row that is not in the request's household.
      if (activeHousehold && (resource === "bills" || resource.startsWith("bills/"))) {
        const bills = activeHousehold.bills ?? [];
        if (resource === "bills") return send(res, 200, { data: bills });
        const id = resource.slice("bills/".length);
        const bill = bills.find((b) => b.id === id);
        if (!bill) return send(res, 404, { error: { code: "NOT_FOUND", message: "bill not found" } });
        return send(res, 200, { data: bill });
      }
    }
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
