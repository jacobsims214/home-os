/**
 * Typed API client for Home OS.
 *
 * All data fetching goes through apiFetch(), which:
 * 1. Sends requests same-origin to the Next.js BFF (`/bff/...`). The BFF reads
 *    the httpOnly `home-os-token` cookie and attaches the bearer token
 *    server-side, so client JS never sees or stores a token.
 * 2. Automatically parses JSON responses and throws ApiError on non-2xx status.
 *
 * Callers may pass either `/api/v1/<path>` (rewritten to `/bff/<path>`) or
 * `/bff/<path>` directly. Login and token exchange live on `/api/auth`, never
 * on /bff (see KB architecture/bff-proxy.md).
 * 3. On a 401 from /bff, sends the browser to /login?redirect=<current page>
 *    (redirectToLogin). The BFF has already tried a silent refresh before it
 *    answers 401, so the session is really gone.
 */

// Relative with the extension so `node --test` can load this file directly.
import { safeRedirectPath } from "../middleware.ts";
import { offlineReadMessage, offlineWriteMessage } from "./pwa/offline.js";

export class ApiError extends Error {
  // Explicit fields rather than constructor parameter properties, which
  // Node's type stripping (node --test) cannot run.
  status: number;
  body?: unknown;
  /**
   * True when the request never reached the server because the network was
   * gone (#4538). The caller must treat this as "nothing was saved": there is
   * no queue and no retry, so a UI that showed the write as pending would be
   * lying. Lets a form say so without matching on the message.
   */
  offline: boolean;

  constructor(status: number, message: string, body?: unknown, offline = false) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
    this.offline = offline;
  }
}

export interface ApiFetchOptions extends Omit<RequestInit, "body"> {
  /** JSON-serializable request body. Automatically sets Content-Type: application/json. */
  body?: unknown;
  /** Search params to append to the URL. */
  params?: Record<string, string | number | boolean | undefined>;
  /**
   * Set false when a 401 from this endpoint means something other than "the
   * session is gone", so it must not send the browser to /login. (POST
   * /secrets/verify used it until #4446; it now answers 422 for a wrong
   * master password.) Default true.
   */
  redirectOn401?: boolean;
}

const API_PREFIX = "/api/v1";
const BFF_PREFIX = "/bff";

/**
 * Unauthenticated API endpoints used before a session exists. The BFF answers
 * 401 when there is no session cookie, so these go straight to the API's
 * same-origin /api/v1 route. None of them return a token.
 */
const PUBLIC_API_PATHS = new Set([
  "/api/v1/auth/forgot-password",
  "/api/v1/auth/reset-password",
  // Invite signup, mode B (#4401, #4579): a person with no account yet has no
  // session, so the BFF would answer 401 before the API ever saw the token.
  // The route is registered outside RequireAuth for exactly this reason, and
  // it is rate limited per client IP (architecture/api-rate-limiting.md).
  "/api/v1/invites/accept",
]);

/**
 * Map an API path to the same-origin path the browser should call:
 * `/api/v1/x` becomes `/bff/x`; `/bff/x` and public pre-login paths are
 * returned unchanged.
 */
export function bffPath(path: string): string {
  const pathname = path.split("?")[0];
  if (PUBLIC_API_PATHS.has(pathname)) return path;
  if (path === API_PREFIX || path.startsWith(`${API_PREFIX}/`) || path.startsWith(`${API_PREFIX}?`)) {
    return `${BFF_PREFIX}${path.slice(API_PREFIX.length)}`;
  }
  return path;
}

// Set once a login redirect has started, so several requests failing with 401
// together navigate only once.
let loginRedirectStarted = false;

/** Test hook: forget that a login redirect started. */
export function resetLoginRedirect(): void {
  loginRedirectStarted = false;
}

function underLogin(pathname: string): boolean {
  return pathname === "/login" || pathname.startsWith("/login/");
}

/**
 * Sends the browser to /login?redirect=<current path+query>, at most once per
 * page. The return path goes through the same same-origin rule the middleware
 * uses (safeRedirectPath). No-op on the server and when already under /login,
 * so a 401 on the login page cannot loop. Returns true if it navigated.
 *
 * Only for a 401 from /bff: a 503 AUTH_UNAVAILABLE (Dex down) keeps the
 * session and must surface as a normal error instead.
 */
export function redirectToLogin(): boolean {
  if (typeof window === "undefined" || loginRedirectStarted) return false;
  const { pathname, search } = window.location;
  if (underLogin(pathname)) return false;
  loginRedirectStarted = true;
  window.location.assign(`/login?redirect=${encodeURIComponent(safeRedirectPath(pathname + search))}`);
  return true;
}

/** True for a same-origin BFF path (/bff or /bff/...). */
export function isBffPath(pathname: string): boolean {
  return pathname === BFF_PREFIX || pathname.startsWith(`${BFF_PREFIX}/`);
}

/**
 * Whether a request changes state. Anything that is not a GET or HEAD is a
 * write, so its failure is reported as "not saved" rather than "could not
 * load" (#4538).
 */
function isWriteMethod(method: string | undefined): boolean {
  const m = (method || "GET").toUpperCase();
  return m !== "GET" && m !== "HEAD";
}

/**
 * Typed fetch wrapper for the Home OS API, routed through the BFF.
 * No Authorization header is sent: the BFF adds it from the session cookie.
 *
 * Offline (#4538): a request whose network attempt fails throws an ApiError with
 * `status: 0` and `offline: true`, whose message says whether nothing was SAVED
 * (a write) or could not be LOADED (a read). It is never retried and never
 * queued — see lib/pwa/offline.js for why.
 */
export async function apiFetch<T = unknown>(
  path: string,
  options: ApiFetchOptions = {},
): Promise<T> {
  const { body, params, headers: initHeaders, redirectOn401 = true, ...init } = options;

  // Build URL
  const origin =
    typeof window !== "undefined" ? window.location.origin : "http://localhost:3000";
  const url = new URL(bffPath(path), origin);

  if (params) {
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined) {
        url.searchParams.set(key, String(value));
      }
    }
  }

  // Build headers
  const headers = new Headers(initHeaders);

  if (body !== undefined) {
    headers.set("Content-Type", "application/json");
  }

  const serializedBody =
    body !== undefined
      ? typeof body === "string"
        ? body
        : JSON.stringify(body)
      : undefined;

  let response: Response;
  try {
    response = await fetch(url.toString(), {
      ...init,
      headers,
      credentials: "same-origin",
      body: serializedBody,
    });
  } catch (error) {
    // The network is gone (or the request was blocked before it left). #4538:
    // this must be LOUD and specific, never a silent queue — a note that looks
    // saved but never reached the server is a data-loss trap between
    // co-parents. status 0 is the conventional "no response" status.
    //
    // Only a genuine network failure gets here: an HTTP error response is a
    // resolved promise and is handled below as before.
    const write = isWriteMethod(init.method);
    throw new ApiError(
      0,
      write ? offlineWriteMessage() : offlineReadMessage(),
      { error: { code: "OFFLINE" } },
      true,
    );
  }

  if (!response.ok) {
    let errorBody: unknown;
    try {
      errorBody = await response.json();
    } catch {
      // response may not be JSON
    }
    // Public pre-login paths go to /api/v1, not /bff, and may 401 legitimately.
    if (response.status === 401 && redirectOn401 && isBffPath(url.pathname)) redirectToLogin();
    throw new ApiError(
      response.status,
      `API ${response.status}: ${response.statusText}`,
      errorBody,
    );
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return response.json() as Promise<T>;
}
