import { createHash } from "node:crypto";
import { NextRequest, NextResponse } from "next/server";
import { DEX_UI_CLIENT_ID, dexTokenUrl } from "@/lib/auth/dex-config";
import { isSameOrigin } from "@/lib/auth/same-origin";

/**
 * BFF proxy: /bff/<path> → ${API_INTERNAL_URL}/api/v1/<path>
 *
 * The browser never sees the JWT. This handler reads it from the httpOnly
 * `home-os-token` cookie and attaches it as `Authorization: Bearer` on the
 * server-to-server call to the Go API. Request and response bodies are
 * streamed, so multipart uploads and file downloads pass through unbuffered.
 *
 * Session refresh (#4423): the id_token cookie expires at the token's exp, and
 * the httpOnly `home-os-refresh` cookie holds the Dex refresh token. When the
 * id_token is missing, about to expire, or rejected by the API (401), the BFF
 * redeems the refresh token at Dex, writes BOTH rotated cookies back (Dex
 * rotates refresh tokens on every use) and retries once. If Dex rejects the
 * refresh token, both cookies are cleared and the client gets 401.
 *
 * One refresh token per user per client (#4649): Dex keeps a single refresh
 * token per (user, connector, client) and deletes it when it issues a new one,
 * and every browser uses the one public client `home-os-ui`. Signing in on a
 * second device therefore revokes the first device's `home-os-refresh`. The
 * first device keeps working on its id_token until exp; then Dex answers
 * `400 invalid_request` ("already been claimed"), which is "rejected" here:
 * sessionEnded() clears the cookies and returns 401, and lib/api.ts sends the
 * page to /login once (#4620). With the cookies gone, nothing retries, so it
 * cannot loop. Dex v2.42.0 has no setting for per-device refresh tokens; the
 * lever is the id_token lifetime (Helm `dex.idTokenExpiry`, default 24h;
 * compose deploy/dex/config.yaml `expiry.idTokens`). Never obtain tokens for
 * `home-os-ui` outside the browser login, or it logs users' browsers out.
 *
 * CSRF (#4646): every method except GET/HEAD must come from the app's own
 * origin (lib/auth/same-origin.ts: Sec-Fetch-Site and Origin against
 * PUBLIC_URL). Anything else, including a write with neither header (not a
 * browser), gets 403 before any refresh or upstream call. The session cookies
 * alone authenticate, so without this a sibling host on the same parent
 * domain could write through the BFF.
 *
 * Env: API_INTERNAL_URL — base URL of the Go API reachable from the UI pod
 * (default http://home-os-api:8080). DEX_TOKEN_URL — Dex token endpoint
 * (default http://home-os-dex:5556/dex/token; resolved by
 * lib/auth/dex-config.ts, shared with the OIDC callback). Both read at
 * request time.
 */

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const COOKIE_NAME = "home-os-token";
const REFRESH_COOKIE = "home-os-refresh";
// Removed in #4412; expired here too so a dead session leaves nothing behind.
const LEGACY_CLIENT_COOKIE = "home-os-token-client";
const DEFAULT_API_URL = "http://home-os-api:8080";

// Same cookie sizing as app/api/auth/callback/route.ts.
const DEFAULT_TOKEN_MAX_AGE = 60 * 60 * 24;
const REFRESH_MAX_AGE = 60 * 60 * 24 * 30;
// Refresh this many seconds before exp so the token can't expire in flight.
const EXPIRY_SKEW_SECONDS = 30;
// How long a completed refresh is remembered, keyed by the refresh token it
// consumed, so requests still carrying the rotated-out token reuse its result.
const REFRESH_RESULT_TTL_MS = 60_000;
const DEX_TIMEOUT_MS = 10_000;
// Bodies up to this size are buffered so a request can be replayed after a
// 401 → refresh. Larger or chunked bodies stream and are not retried (the
// proactive exp check still refreshes before sending them).
const MAX_REPLAY_BODY_BYTES = 1024 * 1024;

// Hop-by-hop headers (RFC 9110 §7.6.1) never cross a proxy.
const HOP_BY_HOP = [
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
];

// Request headers we never forward: the browser's cookies and any
// client-supplied Authorization stay here; host/length are recomputed.
const STRIP_REQUEST = new Set([
  ...HOP_BY_HOP,
  "host",
  "cookie",
  "authorization",
  "content-length",
  "accept-encoding",
  "expect", // undici rejects Expect: 100-continue (curl sends it for large uploads)
  "origin",
  "referer",
]);

// Response headers we never return: cookie management belongs to the UI.
const STRIP_RESPONSE = new Set([...HOP_BY_HOP, "set-cookie"]);

function apiBase(): string {
  return (process.env.API_INTERNAL_URL || DEFAULT_API_URL).replace(/\/+$/, "");
}

function errorResponse(status: number, code: string, message: string) {
  // Same envelope as the Go API's apierr package.
  return NextResponse.json(
    { error: { code, message } },
    { status, headers: { "Cache-Control": "no-store" } },
  );
}

// ---------------------------------------------------------------------------
// Dex refresh
// ---------------------------------------------------------------------------

type SessionTokens = {
  idToken: string;
  /** Epoch ms when the id_token cookie should expire. */
  expiresAt: number;
  /** New refresh token, or null if Dex didn't return one (keep the old cookie). */
  refreshToken: string | null;
};

type RefreshOutcome =
  | { ok: true; tokens: SessionTokens }
  // rejected: Dex refused the refresh token, so the session is over.
  // unavailable: Dex unreachable / 5xx. Transient, so don't log the user out.
  | { ok: false; reason: "rejected" | "unavailable" };

// Single-flight state, per server process. Keys are sha256(refresh token) so
// raw tokens are never used as map keys. Values never reach a log.
const inflightRefreshes = new Map<string, Promise<RefreshOutcome>>();
const recentRefreshes = new Map<string, { at: number; tokens: SessionTokens }>();

// exp claim in epoch seconds, or null. The signature is not checked: this only
// decides when to refresh and sizes the cookie; the Go API validates tokens.
function jwtExp(jwt: string): number | null {
  const payload = jwt.split(".")[1];
  if (!payload) return null;
  try {
    const claims = JSON.parse(Buffer.from(payload, "base64url").toString("utf8"));
    return typeof claims.exp === "number" ? claims.exp : null;
  } catch {
    return null;
  }
}

function expiresSoon(jwt: string): boolean {
  const exp = jwtExp(jwt);
  return exp !== null && exp - Date.now() / 1000 < EXPIRY_SKEW_SECONDS;
}

async function redeemRefreshToken(refreshToken: string): Promise<RefreshOutcome> {
  let res: Response;
  try {
    res = await fetch(dexTokenUrl(), {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        grant_type: "refresh_token",
        refresh_token: refreshToken,
        client_id: DEX_UI_CLIENT_ID,
      }),
      cache: "no-store",
      signal: AbortSignal.timeout(DEX_TIMEOUT_MS),
    });
  } catch (err) {
    console.error("[bff] refresh: Dex unreachable:", err instanceof Error ? err.name : "unknown");
    return { ok: false, reason: "unavailable" };
  }

  if (!res.ok) {
    // Dex error bodies are {error, error_description}; log only the code.
    let code = "";
    try {
      const body = await res.json();
      if (typeof body?.error === "string") code = body.error;
    } catch {
      /* not JSON */
    }
    console.warn("[bff] refresh rejected by Dex:", res.status, code);
    return { ok: false, reason: res.status >= 500 ? "unavailable" : "rejected" };
  }

  let tokens: Record<string, unknown>;
  try {
    tokens = await res.json();
  } catch {
    console.error("[bff] refresh: Dex returned a non-JSON body");
    return { ok: false, reason: "unavailable" };
  }

  const idToken = tokens.id_token;
  if (typeof idToken !== "string" || !idToken) {
    console.warn("[bff] refresh: Dex response had no id_token");
    return { ok: false, reason: "rejected" };
  }

  let maxAgeSeconds: number;
  const exp = jwtExp(idToken);
  if (exp !== null) maxAgeSeconds = Math.floor(exp - Date.now() / 1000);
  else if (typeof tokens.expires_in === "number" && Number.isFinite(tokens.expires_in))
    maxAgeSeconds = Math.floor(tokens.expires_in);
  else maxAgeSeconds = DEFAULT_TOKEN_MAX_AGE;
  if (maxAgeSeconds <= 0) {
    console.warn("[bff] refresh: Dex issued an already-expired id_token");
    return { ok: false, reason: "rejected" };
  }

  const newRefresh = tokens.refresh_token;
  return {
    ok: true,
    tokens: {
      idToken,
      expiresAt: Date.now() + maxAgeSeconds * 1000,
      refreshToken: typeof newRefresh === "string" && newRefresh ? newRefresh : null,
    },
  };
}

/**
 * Refresh the session, at most once per refresh token. Concurrent requests
 * with the same refresh cookie share one Dex call. Requests that arrive just
 * after it finished, still carrying the now rotated-out token, get the cached
 * result instead of a Dex rejection that would log the user out.
 */
function refreshSession(refreshToken: string): Promise<RefreshOutcome> {
  const key = createHash("sha256").update(refreshToken).digest("hex");
  const now = Date.now();

  recentRefreshes.forEach((v, k) => {
    if (now - v.at > REFRESH_RESULT_TTL_MS) recentRefreshes.delete(k);
  });
  const cached = recentRefreshes.get(key);
  if (cached && cached.tokens.expiresAt - now > EXPIRY_SKEW_SECONDS * 1000) {
    return Promise.resolve({ ok: true, tokens: cached.tokens });
  }

  const pending = inflightRefreshes.get(key);
  if (pending) return pending;

  const p = redeemRefreshToken(refreshToken)
    .then((outcome) => {
      if (outcome.ok) recentRefreshes.set(key, { at: Date.now(), tokens: outcome.tokens });
      return outcome;
    })
    .finally(() => inflightRefreshes.delete(key));
  inflightRefreshes.set(key, p);
  return p;
}

function setSessionCookies(response: NextResponse, tokens: SessionTokens) {
  const maxAge = Math.max(1, Math.floor((tokens.expiresAt - Date.now()) / 1000));
  response.cookies.set(COOKIE_NAME, tokens.idToken, { ...baseCookie(), maxAge });
  if (tokens.refreshToken) {
    response.cookies.set(REFRESH_COOKIE, tokens.refreshToken, {
      ...baseCookie(),
      maxAge: REFRESH_MAX_AGE,
    });
  }
}

// Refresh failed for good: clear the session so the client goes to /login.
function sessionEnded(): NextResponse {
  const response = errorResponse(401, "UNAUTHORIZED", "session expired");
  for (const name of [COOKIE_NAME, REFRESH_COOKIE, LEGACY_CLIENT_COOKIE]) {
    response.cookies.set(name, "", { ...baseCookie(), maxAge: 0 });
  }
  return response;
}

function baseCookie() {
  return {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "lax" as const,
    path: "/",
  };
}

// ---------------------------------------------------------------------------
// Proxy
// ---------------------------------------------------------------------------

async function proxy(
  request: NextRequest,
  { params }: { params: { path?: string[] } },
): Promise<Response> {
  // CSRF: refuse cross-site writes before touching Dex or the API.
  if (request.method !== "GET" && request.method !== "HEAD" && !isSameOrigin(request)) {
    return errorResponse(403, "FORBIDDEN", "cross-site request refused");
  }

  let token = request.cookies.get(COOKIE_NAME)?.value;
  const refreshCookie = request.cookies.get(REFRESH_COOKIE)?.value;
  let rotated: SessionTokens | null = null;

  // Proactive refresh: the token cookie is gone (the browser drops it at exp)
  // or about to expire, and we hold a refresh token.
  if ((!token || expiresSoon(token)) && refreshCookie) {
    const outcome = await refreshSession(refreshCookie);
    if (outcome.ok) {
      rotated = outcome.tokens;
      token = outcome.tokens.idToken;
    } else if (outcome.reason === "rejected") {
      return sessionEnded();
    } else if (!token) {
      return errorResponse(503, "AUTH_UNAVAILABLE", "auth provider unavailable");
    }
    // unavailable but the old token hasn't quite expired: try it anyway.
  }

  if (!token) {
    return errorResponse(401, "UNAUTHORIZED", "no session");
  }

  const segments = params.path ?? [];
  if (segments.some((s) => s === "" || s === "." || s === "..")) {
    return errorResponse(400, "BAD_REQUEST", "invalid path");
  }

  const base = apiBase();
  const target =
    `${base}/api/v1/${segments.map(encodeURIComponent).join("/")}` +
    request.nextUrl.search;

  const headers = new Headers();
  request.headers.forEach((value, key) => {
    if (!STRIP_REQUEST.has(key.toLowerCase())) headers.set(key, value);
  });
  // Ask for an uncompressed body: fetch would decode it but keep the
  // Content-Encoding header, which would corrupt the response.
  headers.set("accept-encoding", "identity");
  const fwdHost = request.headers.get("x-forwarded-host") ?? request.headers.get("host");
  if (fwdHost) headers.set("x-forwarded-host", fwdHost);

  const hasBody = request.method !== "GET" && request.method !== "HEAD";

  // Buffer small bodies so the request can be replayed after a refresh.
  let body: ArrayBuffer | ReadableStream<Uint8Array> | undefined;
  let replayable = true;
  if (hasBody && request.body) {
    const lenHeader = request.headers.get("content-length");
    const len = lenHeader === null ? NaN : Number(lenHeader);
    if (Number.isFinite(len) && len <= MAX_REPLAY_BODY_BYTES) {
      body = await request.arrayBuffer();
    } else {
      body = request.body;
      replayable = false;
    }
  }
  const streaming = body instanceof ReadableStream;

  const send = (bearer: string) => {
    headers.set("authorization", `Bearer ${bearer}`);
    return fetch(target, {
      method: request.method,
      headers,
      body,
      redirect: "manual",
      cache: "no-store",
      signal: request.signal,
      // Required by undici to stream a ReadableStream request body.
      ...(streaming ? { duplex: "half" } : {}),
    } as RequestInit);
  };

  let upstream: Response;
  try {
    upstream = await send(token);

    // Reactive refresh: the API rejected the token (revoked, clock skew, …).
    // Only once per request, and only if we didn't just refresh.
    if (upstream.status === 401 && replayable && refreshCookie && !rotated) {
      const outcome = await refreshSession(refreshCookie);
      if (outcome.ok) {
        rotated = outcome.tokens;
        await upstream.body?.cancel().catch(() => {});
        upstream = await send(outcome.tokens.idToken);
      } else if (outcome.reason === "rejected") {
        await upstream.body?.cancel().catch(() => {});
        return sessionEnded();
      }
      // unavailable: pass the API's 401 through unchanged.
    }
  } catch (err) {
    if (request.signal.aborted) {
      return new Response(null, { status: 499 });
    }
    console.error("[bff] upstream request failed:", target, err instanceof Error ? err.message : err);
    const res = errorResponse(502, "BAD_GATEWAY", "API unavailable");
    if (rotated) setSessionCookies(res, rotated);
    return res;
  }

  const resHeaders = new Headers();
  upstream.headers.forEach((value, key) => {
    if (!STRIP_RESPONSE.has(key.toLowerCase())) resHeaders.set(key, value);
  });
  // If the API compressed anyway, fetch has already decoded the body.
  if (resHeaders.has("content-encoding")) {
    resHeaders.delete("content-encoding");
    resHeaders.delete("content-length");
  }
  // Keep redirects inside the BFF rather than leaking the internal host.
  const location = resHeaders.get("location");
  if (location) {
    const apiPrefix = `${base}/api/v1`;
    if (location.startsWith(apiPrefix)) {
      resHeaders.set("location", `/bff${location.slice(apiPrefix.length)}`);
    } else if (location.startsWith("/api/v1")) {
      resHeaders.set("location", `/bff${location.slice("/api/v1".length)}`);
    }
  }
  if (!resHeaders.has("cache-control")) {
    resHeaders.set("cache-control", "no-store");
  }

  const nullBody =
    request.method === "HEAD" || [101, 204, 205, 304].includes(upstream.status);

  const response = new NextResponse(nullBody ? null : upstream.body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers: resHeaders,
  });
  // Write the rotated id_token AND refresh token back (Dex rotates both).
  if (rotated) setSessionCookies(response, rotated);
  return response;
}

export const GET = proxy;
export const HEAD = proxy;
export const POST = proxy;
export const PUT = proxy;
export const PATCH = proxy;
export const DELETE = proxy;
