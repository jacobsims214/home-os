/**
 * Where to send the browser after a successful login.
 *
 * Anyone can craft a /login?redirect=... link, so the value is untrusted. It
 * goes through the same rule as middleware.ts (safeRedirectPath: same-origin
 * path only, a leading // or /\ collapses to /, anything else becomes /) on
 * the login page, again in /api/auth/login, and once more in the callback,
 * which also checks the final URL stays on the public origin.
 *
 * No runtime Next imports: used by the client login page and by route handlers.
 */
import { safeRedirectPath } from "@/middleware";

/**
 * The in-app page the callback sends the browser to (#5304), carrying the real
 * destination as ?to=. It exists so the login ends on a document the app owns
 * instead of redirecting straight into the destination -- see completionUrl().
 */
export const AUTH_COMPLETE_PATH = "/auth/complete";

export const DEFAULT_RETURN_PATH = "/dashboard";
// Longer values are dropped rather than truncated (a cut path is a different page).
export const MAX_RETURN_PATH_LENGTH = 2048;

// ASCII control characters: browsers strip some (tab, CR, LF) while parsing,
// which can turn a harmless-looking path into a protocol-relative URL.
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/;

// Landing back on these after login would restart the login flow. "/auth" is
// the completion route (#5304): it is a step of the login, not a destination,
// so returning to it would bounce the browser through login twice.
const LOOP_PREFIXES = ["/login", "/api/auth", AUTH_COMPLETE_PATH];

/** The sanitised return path, or DEFAULT_RETURN_PATH when there is none worth using. */
export function returnPath(raw: string | null | undefined): string {
  if (!raw || raw.length > MAX_RETURN_PATH_LENGTH || CONTROL_CHARS.test(raw)) return DEFAULT_RETURN_PATH;
  const path = safeRedirectPath(raw);
  if (path === "/") return DEFAULT_RETURN_PATH;
  const pathname = path.split(/[?#]/)[0];
  if (LOOP_PREFIXES.some((p) => pathname === p || pathname.startsWith(p + "/"))) return DEFAULT_RETURN_PATH;
  return path;
}

/**
 * Absolute post-login URL on `origin`. Belt and braces on top of returnPath():
 * if the resolved URL is on any other origin, the default page is used.
 */
export function returnUrl(origin: string, raw: string | null | undefined): URL {
  const fallback = new URL(DEFAULT_RETURN_PATH, origin);
  try {
    const url = new URL(returnPath(raw), origin);
    return url.origin === fallback.origin ? url : fallback;
  } catch {
    return fallback;
  }
}

/**
 * The post-login URL the callback redirects to (#5304).
 *
 * WHY NOT STRAIGHT TO THE DESTINATION. A 307 chain leaves no history entry of
 * its own, so with `callback -> 307 -> destination` the entry directly behind
 * the destination is whatever document was last navigated to before it -- which
 * after a login is Dex's own login form (the form submit is the one navigation
 * that adds an entry; every redirect after it lands in that same entry). Back
 * from the destination therefore re-requests Dex's form with a state the submit
 * already spent, and Dex answers 400 "Requested resource does not exist": a
 * dead end, no way back into the app (#5304).
 *
 * `history.replaceState` cannot repair that: the callback is a redirect hop and
 * never a document, so no script of ours runs at that URL, and replaceState can
 * only rewrite the ACTIVE entry -- never the one behind it. Measured on
 * Chromium's Page.getNavigationHistory, both the replaceState-in-callback and
 * the replaceState-in-a-bounce-route variants leave the history stack identical
 * to the failing one.
 *
 * So the login ends on /auth/complete, a real app page with no code or state in
 * its URL, and that page PUSHES the destination (see components/auth/
 * AuthComplete.tsx). Back then lands on the app's own page, never on Dex. The
 * authorization code stays a redirect hop and never becomes a document URL.
 *
 * The destination is sanitised by returnUrl() exactly as before: same origin,
 * same LOOP_PREFIXES rules, so this adds no new way to leave the origin.
 */
export function completionUrl(origin: string, raw: string | null | undefined): URL {
  const destination = returnUrl(origin, raw);
  const complete = new URL(AUTH_COMPLETE_PATH, origin);
  complete.searchParams.set("to", destination.pathname + destination.search + destination.hash);
  return complete.origin === new URL(origin).origin ? complete : new URL(AUTH_COMPLETE_PATH, origin);
}
