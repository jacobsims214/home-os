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
 * Clear-Site-Data (#4648): expiring the cookies is not enough on a shared
 * device. The service worker used to cache `/bff/*` responses (authenticated
 * household data) and logout left them, and the zustand `home-os-auth` profile
 * stayed in localStorage, so the next person to pick up the tablet could be
 * served the previous person's data from Cache Storage. The 303 therefore
 * carries `Clear-Site-Data: "cache", "storage"`, which makes the browser drop
 * this origin's HTTP cache, Cache Storage, localStorage, sessionStorage,
 * IndexedDB and service worker registrations — without trusting any script of
 * ours to still be running. The app also purges from the page before it
 * submits (lib/session-teardown.ts), because Clear-Site-Data is only honoured
 * in a secure context: on a plain-http origin the page-side purge is the one
 * that does the work.
 *
 * Uses the standard Response (no next/server import) so `node --test` can load
 * this file (route.test.mjs, via lib/auth/node-ts-resolve.mjs for the "@/" import).
 */
import { isSameOrigin } from "@/lib/auth/same-origin";
import { CLEAR_SITE_DATA } from "@/lib/auth/clear-site-data";
import { HOUSEHOLD_COOKIE } from "@/lib/household-cookie";

export const dynamic = "force-dynamic";

// Path=/ cookies from the callback (see KB architecture/ui-session-cookies.md).
const SESSION_COOKIES = ["home-os-token", "home-os-refresh", "home-os-token-client"];
// The active household (#4574). Not a credential — an id the BFF forwards as
// X-Household-ID — but it belongs to the session, so signing out of one account
// must not leave the next one scoped to the previous account's family.
const HOUSEHOLD_COOKIES = [HOUSEHOLD_COOKIE];
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
  // "cache" is HTTP cache + Cache Storage + the back/forward entries; "storage"
  // is localStorage, sessionStorage, IndexedDB and the service worker
  // registrations. Both are quoted-string directives, exactly as MDN's grammar
  // requires. See lib/session-teardown.ts for what the page clears as well.
  headers.set("Clear-Site-Data", CLEAR_SITE_DATA);
  for (const name of [...SESSION_COOKIES, ...HOUSEHOLD_COOKIES]) {
    headers.append("Set-Cookie", expiredCookie(name, "/"));
  }
  headers.append("Set-Cookie", expiredCookie(PKCE_COOKIE, "/api/auth"));
  return new Response(null, { status: 303, headers });
}

export async function GET(): Promise<Response> {
  return new Response(null, { status: 405, headers: { Allow: "POST" } });
}
