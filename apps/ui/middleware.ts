import type { NextRequest } from "next/server";

/**
 * Route guard for page navigations.
 *
 * Default-deny: every path needs a session cookie unless it is listed below.
 * This is a UX guard only — the data is protected by the BFF (/bff/*), which
 * reads the same httpOnly cookie and answers 401 JSON on its own.
 *
 * Only a type is imported from next/server, so this file has no runtime
 * dependency on Next and `node --test` can load it directly (middleware.test.mjs).
 */

// httpOnly cookies set by app/api/auth/callback (#4412). home-os-token holds
// the id token and expires with it; home-os-refresh holds the Dex refresh token
// (30d). Either one lets the page load: with only the refresh token left, the
// BFF refresh flow renews the session on the first /bff call (or answers 401,
// which sends the client to /login). Bouncing on the id token alone would log
// people out every day despite a valid refresh token.
export const SESSION_COOKIE = "home-os-token";
export const REFRESH_COOKIE = "home-os-refresh";

// Pages reachable without a session. A prefix matches itself and any subpath
// ("/invite" matches "/invite/abc"), never a longer sibling ("/invitex").
export const PUBLIC_PREFIXES = [
  "/login",
  "/register",
  "/reset-password",
  "/invites", // invite accept page: /invites/accept?token=… (#4579)
  "/invite", // the flat /invite/<token> spelling listed since #4413; no page uses it yet
  "/onboarding",
  "/api/auth", // OIDC login start + callback, logout
];

// Paths the guard never redirects:
// - /bff/* checks the session itself and must answer 401 JSON, not an HTML
//   redirect to /login (fetch would follow it and parse the login page).
// - Next internals and the PWA files next-pwa writes to public/.
const PASSTHROUGH_PREFIXES = ["/bff", "/_next", "/icons"];

export type RouteAccess = "public" | "passthrough" | "protected";

function underPrefix(pathname: string, prefix: string): boolean {
  return pathname === prefix || pathname.startsWith(prefix + "/");
}

// Root-level static files from public/: /sw.js, /workbox-<hash>.js,
// /manifest.json, /favicon.ico. Deliberately root only, so /dashboard/a.b
// stays protected.
function isRootStaticFile(pathname: string): boolean {
  return /^\/[^/]+\.[A-Za-z0-9]+$/.test(pathname);
}

export function routeAccess(pathname: string): RouteAccess {
  if (PASSTHROUGH_PREFIXES.some((p) => underPrefix(pathname, p)) || isRootStaticFile(pathname)) {
    return "passthrough";
  }
  if (PUBLIC_PREFIXES.some((p) => underPrefix(pathname, p))) {
    return "public";
  }
  return "protected";
}

export function middleware(request: NextRequest): Response | undefined {
  const { pathname, search } = request.nextUrl;

  if (routeAccess(pathname) !== "protected") return undefined;
  if (request.cookies.get(SESSION_COOKIE)?.value || request.cookies.get(REFRESH_COOKIE)?.value) {
    return undefined;
  }

  // A plain Response (not NextResponse) keeps this file free of runtime Next imports.
  return new Response(null, {
    status: 307,
    headers: { Location: loginLocation(publicOrigin(request), pathname + search) },
  });
}

/**
 * Makes a return path safe to hand back to the app after login: a same-origin
 * path starting with exactly one "/". A leading run of slashes or backslashes
 * ("//evil.example", "/\\evil.example") would be read by browsers as a
 * protocol-relative URL to another host, so it collapses to a single "/".
 * Anything that is not a path at all falls back to "/".
 */
export function safeRedirectPath(path: string): string {
  if (!path.startsWith("/") && !path.startsWith("\\")) return "/";
  return path.replace(/^[/\\]+/, "/");
}

// hostname[:port] or [ipv6][:port]; anything else in Host is ignored.
const HOST_RE = /^(?:[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*|\[[0-9A-Fa-f:.]+\])(?::\d{1,5})?$/;

function firstValue(header: string | null): string {
  return (header ?? "").split(",")[0].trim();
}

/**
 * The origin the browser used, for the /login redirect.
 *
 * Not request.url: in the standalone Next server that carries the bind address
 * (HOSTNAME:PORT, the pod name in Kubernetes), so behind Envoy a Location built
 * from it is unreachable (#4621). A relative Location would avoid the question,
 * but Next 14.2's middleware adapter parses Location with `new URL()` and no
 * base, so a relative one fails with "Invalid URL" (500).
 *
 * Order: PUBLIC_URL (set by the helm chart; not spoofable), then
 * X-Forwarded-Host / Host with X-Forwarded-Proto (same as the /api/auth
 * routes), then request.nextUrl as a last resort.
 */
export function publicOrigin(request: Pick<NextRequest, "headers" | "nextUrl">): string {
  const configured = process.env.PUBLIC_URL;
  if (configured) {
    try {
      const url = new URL(configured);
      if (url.protocol === "https:" || url.protocol === "http:") return url.origin;
    } catch {
      // Malformed PUBLIC_URL: fall through to the request headers.
    }
  }

  const host = firstValue(request.headers.get("x-forwarded-host")) || firstValue(request.headers.get("host"));
  if (HOST_RE.test(host)) {
    const fwdProto = firstValue(request.headers.get("x-forwarded-proto")).toLowerCase();
    const proto = fwdProto === "https" || fwdProto === "http" ? fwdProto : request.nextUrl.protocol.replace(/:$/, "");
    return `${proto}://${host}`;
  }
  return request.nextUrl.origin;
}

/** The /login redirect target on `origin`, with a sanitised return path. */
export function loginLocation(origin: string, returnPath: string): string {
  return `${origin}/login?redirect=${encodeURIComponent(safeRedirectPath(returnPath))}`;
}

export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};
