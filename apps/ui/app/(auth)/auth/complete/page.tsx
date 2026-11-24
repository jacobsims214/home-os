import AuthComplete from "@/components/auth/AuthComplete";
import { returnPath } from "@/lib/auth/return-path";
import { REFRESH_COOKIE, SESSION_COOKIE } from "@/middleware";
import { cookies } from "next/headers";

/**
 * /auth/complete — the last step of a login (#5304, #5407).
 *
 * WHY THIS PAGE EXISTS. Pressing Back after signing in used to dead-end on
 * Dex's "Bad Request": the browser's history entry directly behind the
 * destination was Dex's login form, whose one-time state the submit had already
 * spent. Measured with Chromium's Page.getNavigationHistory, the chain
 * `callback -> 307 -> destination` adds no entry of its own, so that form stays
 * immediately behind wherever the callback sends the browser. `replaceState`
 * cannot help either: the callback is a redirect hop and never a document, so
 * no script of ours runs there, and replaceState can only rewrite the ACTIVE
 * entry -- never the one behind it.
 *
 * So the callback redirects HERE instead: an app page whose URL carries no
 * authorization code and no state. It pushes the real destination as a new
 * history entry, so Back from the destination lands in the app.
 *
 * WHY IT ALSO CHECKS THE SESSION (#5407). #5304 made Back land on this page but
 * had it forward to the destination unconditionally -- and the destination's
 * own guard then answers 307 /login for anyone without a session, whose page
 * `location.replace()`s into /api/auth/login and on to Dex. So for a visitor
 * whose session did not survive (or who never completed a login at all, which
 * is exactly what happens when the login was throttled), "Back lands here"
 * still meant "and is immediately sent back into the Dex chain" -- the shape
 * changed, the dead end did not. Forwarding a signed-OUT visitor is never
 * right: it costs them their Back position and spends another request against
 * the Dex login rate limiter. They get the sign-in card instead, one click
 * from where they were going.
 *
 * `?to=` is sanitised here as well as in the callback, with the same rule the
 * login page and middleware use (lib/auth/return-path.ts): anyone can type this
 * URL, so it never carries another origin, never returns to /login or /auth,
 * and falls back to the dashboard when the value is missing or junk.
 */
export const metadata = {
  title: "Signing in — Home OS",
};

// The page's whole job depends on the request's cookies, so it must never be
// prerendered or cached: a cached "signed in" render would hand a signed-out
// visitor the forward it must not get.
export const dynamic = "force-dynamic";

export default function AuthCompletePage({
  searchParams,
}: {
  searchParams?: { to?: string | string[] };
}) {
  const raw = typeof searchParams?.to === "string" ? searchParams.to : null;
  // Same rule as middleware.ts and the dashboard layout: either session cookie
  // lets the browser in. With only the refresh cookie left, the first /bff call
  // renews the session silently (#4423) -- that IS the "restore the session"
  // answer for a Back arrival.
  const cookieStore = cookies();
  const signedIn =
    !!cookieStore.get(SESSION_COOKIE)?.value || !!cookieStore.get(REFRESH_COOKIE)?.value;

  return <AuthComplete to={returnPath(raw)} signedIn={signedIn} />;
}
