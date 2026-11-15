/**
 * CSRF guard for cookie-authenticated, state-changing routes (#4646).
 *
 * Used by app/bff/[...path]/route.ts (every method except GET/HEAD) and
 * app/api/auth/logout/route.ts. The session lives in SameSite=Lax cookies,
 * which stop a cross-site form POST from carrying them but not a *same-site*
 * one: any sibling host under the same parent domain could otherwise POST to
 * /bff with the user's session. Browsers tell us where a request came from in
 * two headers, and this checks both:
 *
 *   Sec-Fetch-Site  when present, must be "same-origin" or "none" (typed in
 *                   the address bar / bookmark). "same-site" and "cross-site"
 *                   are refused.
 *   Origin          when present, must equal the public origin: the origin of
 *                   PUBLIC_URL when that is a valid http(s) URL (it is in the
 *                   helm chart), else the host the request was sent to
 *                   (first X-Forwarded-Host value, else Host). "null" and
 *                   malformed values are refused.
 *
 * A request with NEITHER header did not come from a current browser (every
 * browser sends Origin on a non-GET fetch or form POST, and Sec-Fetch-Site
 * since 2023). By default it is refused too, so nothing that strips both
 * headers can slip through. Pass `allowHeaderless: true` to accept it; logout
 * does, because a non-browser client has no browser session to forge and
 * logging out only drops cookies.
 *
 * No Next imports, so `node --test` can load this file directly.
 */

type HeaderSource = { headers: Headers };
type Env = Record<string, string | undefined>;

export type SameOriginOptions = {
  /** Accept a request that carries neither Origin nor Sec-Fetch-Site. Default false. */
  allowHeaderless?: boolean;
  /** Environment to read PUBLIC_URL from (tests). Default process.env. */
  env?: Env;
};

function firstValue(header: string | null): string {
  return (header ?? "").split(",")[0].trim();
}

/** PUBLIC_URL's origin, or null when unset or not an http(s) URL. */
function configuredOrigin(env: Env): string | null {
  const raw = env.PUBLIC_URL?.trim();
  if (!raw) return null;
  try {
    const url = new URL(raw);
    return url.protocol === "https:" || url.protocol === "http:" ? url.origin : null;
  } catch {
    return null;
  }
}

/** True unless the request came, or may have come, from another site. */
export function isSameOrigin(request: HeaderSource, opts: SameOriginOptions = {}): boolean {
  const fetchSite = request.headers.get("sec-fetch-site");
  const origin = request.headers.get("origin");

  if (fetchSite === null && origin === null) return opts.allowHeaderless === true;
  if (fetchSite !== null && fetchSite !== "same-origin" && fetchSite !== "none") return false;
  if (origin === null) return true; // Sec-Fetch-Site said same-origin / none

  let sent: URL;
  try {
    sent = new URL(origin);
  } catch {
    return false; // "null" (sandboxed iframe, data: URL, ...) or malformed
  }

  const expected = configuredOrigin(opts.env ?? process.env);
  if (expected !== null) return sent.origin === expected;

  // No PUBLIC_URL (local dev): compare hosts only. The scheme is not checked
  // because a proxy that terminates TLS may not send X-Forwarded-Proto.
  const host = firstValue(request.headers.get("x-forwarded-host")) || firstValue(request.headers.get("host"));
  return host !== "" && sent.host === host.toLowerCase();
}
