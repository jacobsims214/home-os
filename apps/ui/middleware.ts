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
  "/invite", // invite accept page
  "/onboarding",
  "/api/auth", // OIDC login start + callback, session restore/logout
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

  const loginUrl = new URL("/login", request.url);
  loginUrl.searchParams.set("redirect", pathname + search);
  // A plain Response (not NextResponse) keeps this file free of runtime Next imports.
  return new Response(null, { status: 307, headers: { Location: loginUrl.toString() } });
}

export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};
