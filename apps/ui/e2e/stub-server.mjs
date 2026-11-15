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
const refreshTokens = new Set();
const stats = { dexRefreshes: 0, apiRequests: 0, apiUnauthorized: 0, codeExchanges: 0, lastRedirectUri: null, apiWrites: [] };
// The only authorization code the stub redeems (tests hand it to the callback).
const E2E_CODE = "e2e-code";

const b64url = (v) => Buffer.from(v).toString("base64url");

function mintJwt() {
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

function issueTokens() {
  const refresh = `e2e-r-${randomBytes(12).toString("hex")}`;
  refreshTokens.add(refresh);
  return { access_token: "unused", token_type: "bearer", id_token: mintJwt(), refresh_token: refresh, expires_in: TOKEN_TTL_S };
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
    refreshTokens.delete(old); // rotation: a refresh token is single-use
    return send(res, 200, issueTokens());
  }

  if (path.startsWith("/api/v1/")) {
    stats.apiRequests += 1;
    const auth = req.headers.authorization ?? "";
    if (!auth.startsWith("Bearer ") || !validJwt(auth.slice(7))) {
      stats.apiUnauthorized += 1;
      return send(res, 401, { error: { code: "UNAUTHORIZED", message: "invalid token" } });
    }
    const resource = path.slice("/api/v1/".length).replace(/\/+$/, "");
    if (req.method !== "GET" && req.method !== "HEAD") {
      let body = {};
      try {
        body = JSON.parse((await readBody(req)) || "{}");
      } catch {
        /* not JSON */
      }
      stats.apiWrites.push(`${req.method} ${path}${url.search}${body.name ? ` ${body.name}` : ""}`);
      if (req.method === "POST") return send(res, 201, { data: { id: `e2e-${stats.apiWrites.length}`, ...body } });
    }
    if (resource === "auth/me" || resource === "users/me") return send(res, 200, { data: SEED_USER });
    return send(res, 200, { data: SEED[resource] ?? [] });
  }

  send(res, 404, { error: { code: "NOT_FOUND", message: path } });
});

server.listen(PORT, HOST, () => {
  console.log(`e2e stub (Dex + API) on http://${HOST}:${PORT}`);
});
for (const sig of ["SIGINT", "SIGTERM"]) process.on(sig, () => server.close(() => process.exit(0)));
