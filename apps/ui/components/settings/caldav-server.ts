/**
 * The CalDAV server address the Phone-calendar page tells a person to type
 * into Apple Calendar (#5289).
 *
 * It used to be a module constant: the local-dev port from docker-compose,
 * rendered on the deployed UI, so a real household typed an address their
 * phone can never reach. The API already knows the
 * right answer: POST /api/v1/auth/caldav-password answers `server_url`, built
 * from the API's PUBLIC_URL (`caldavServerURL` in
 * apps/api/internal/auth/handler.go). That value is the authority.
 *
 * While there is no response yet — the section asks for the app password
 * BEFORE showing the server, so this is a short window — the page shows
 * `/dav/` on its own origin. That is the same rule the API applies (the
 * calendar service is routed under /dav/ on the public origin by envoy in the
 * helm chart and by Caddy in docker-compose), so the page and the API agree,
 * and nothing here has to know a hostname or a port.
 *
 * There is deliberately NO loopback fallback: a deployment must never render
 * one, and the old literal is how this bug happened. With no API value and no
 * browser origin (server render), the answer is an empty string — the field
 * shows a placeholder rather than a wrong address.
 *
 * Plain data and pure functions (no JSX, no browser-only API at module load),
 * so `npm test` (node --test) covers it in caldav-server.test.mjs.
 */

/** Where the calendar service is mounted on the public origin. */
export const CALDAV_PATH = "/dav/";

/**
 * `/dav/` on this page's own origin, or null for an origin a browser can still
 * parse but that is not a web page (file:, an extension) — it has no useful
 * /dav/ to point at.
 */
function sameOriginCaldav(origin: string): string | null {
  let url: URL;
  try {
    url = new URL(origin);
  } catch {
    return null;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return null;
  return `${url.origin}${CALDAV_PATH}`;
}

/**
 * The server address to show: the API's `server_url` when it sent one,
 * otherwise this origin's /dav/, otherwise "" (nothing to show yet).
 *
 * @param reported  `server_url` from POST /api/v1/auth/caldav-password
 * @param origin    `window.location.origin`, once the browser is mounted
 */
export function caldavServerUrl(reported?: string | null, origin?: string | null): string {
  const fromApi = (reported ?? "").trim();
  if (fromApi) return fromApi;
  return sameOriginCaldav((origin ?? "").trim()) ?? "";
}
