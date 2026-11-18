import AuthComplete from "@/components/auth/AuthComplete";
import { returnPath } from "@/lib/auth/return-path";

/**
 * /auth/complete — the last step of a login (#5304).
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
 * See components/auth/AuthComplete.tsx for the revisit behaviour (Back is never
 * trapped) and lib/auth/return-path.ts's completionUrl() for the callback side.
 *
 * `?to=` is sanitised here as well as in the callback, with the same rule the
 * login page and middleware use (lib/auth/return-path.ts): anyone can type this
 * URL, so it never carries another origin, never returns to /login or /auth,
 * and falls back to the dashboard when the value is missing or junk.
 */
export const metadata = {
  title: "Signing in — Home OS",
};

export default function AuthCompletePage({
  searchParams,
}: {
  searchParams?: { to?: string | string[] };
}) {
  const raw = typeof searchParams?.to === "string" ? searchParams.to : null;
  return <AuthComplete to={returnPath(raw)} />;
}
