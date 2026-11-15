/**
 * POST /api/auth/logout — end the browser session.
 *
 * Expires every session cookie set by app/api/auth/callback/route.ts and
 * 303-redirects to /login, so a plain <form method="post"> submit lands on the
 * login page as a fresh GET.
 *
 * POST only: a GET (link, prefetch, <img>) must never log anyone out, so GET
 * answers 405. Cross-site POSTs are refused with 403 by checking Origin /
 * Sec-Fetch-Site (lib/auth/same-origin.ts, shared with the BFF since #4646),
 * which every current browser sends on a form POST.
 *
 * Dex is not called: it has no RP-initiated logout (end_session_endpoint) and
 * no RFC 7009 revocation endpoint for public clients, so ending the session
 * means dropping the cookies. The id token stays valid at the Go API until its
 * exp, but nothing in the browser holds it any more.
 *
 * Uses the standard Response (no next/server import) so `node --test` can load
 * this file (route.test.mjs, via lib/auth/node-ts-resolve.mjs for the "@/" import).
 */
import { isSameOrigin } from "@/lib/auth/same-origin";

export const dynamic = "force-dynamic";

// Path=/ cookies from the callback (see KB architecture/ui-session-cookies.md).
const SESSION_COOKIES = ["home-os-token", "home-os-refresh", "home-os-token-client"];
// The one-time PKCE cookie lives on /api/auth; drop it too in case a login was
// abandoned half-way.
const PKCE_COOKIE = "home-os-pkce";

function expiredCookie(name: string, path: string): string {
  const parts = [
    `${name}=`,
    `Path=${path}`,
    "Max-Age=0",
    "Expires=Thu, 01 Jan 1970 00:00:00 GMT",
    "HttpOnly",
    "SameSite=Lax",
  ];
  if (process.env.NODE_ENV === "production") parts.push("Secure");
  return parts.join("; ");
}

export async function POST(request: Request): Promise<Response> {
  // Headerless (non-browser) requests may log out: there is no browser session
  // to forge, and logout only drops cookies.
  if (!isSameOrigin(request, { allowHeaderless: true })) {
    return Response.json(
      { error: { code: "FORBIDDEN", message: "cross-site logout refused" } },
      { status: 403, headers: { "Cache-Control": "no-store" } },
    );
  }

  const headers = new Headers({ Location: "/login", "Cache-Control": "no-store" });
  for (const name of SESSION_COOKIES) headers.append("Set-Cookie", expiredCookie(name, "/"));
  headers.append("Set-Cookie", expiredCookie(PKCE_COOKIE, "/api/auth"));
  return new Response(null, { status: 303, headers });
}

export async function GET(): Promise<Response> {
  return new Response(null, { status: 405, headers: { Allow: "POST" } });
}
