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
 */

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public body?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export interface ApiFetchOptions extends Omit<RequestInit, "body"> {
  /** JSON-serializable request body. Automatically sets Content-Type: application/json. */
  body?: unknown;
  /** Search params to append to the URL. */
  params?: Record<string, string | number | boolean | undefined>;
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

/**
 * Typed fetch wrapper for the Home OS API, routed through the BFF.
 * No Authorization header is sent: the BFF adds it from the session cookie.
 */
export async function apiFetch<T = unknown>(
  path: string,
  options: ApiFetchOptions = {},
): Promise<T> {
  const { body, params, headers: initHeaders, ...init } = options;

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

  const response = await fetch(url.toString(), {
    ...init,
    headers,
    credentials: "same-origin",
    body: serializedBody,
  });

  if (!response.ok) {
    let errorBody: unknown;
    try {
      errorBody = await response.json();
    } catch {
      // response may not be JSON
    }
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
