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

export const DEFAULT_RETURN_PATH = "/dashboard";
// Longer values are dropped rather than truncated (a cut path is a different page).
export const MAX_RETURN_PATH_LENGTH = 2048;

// ASCII control characters: browsers strip some (tab, CR, LF) while parsing,
// which can turn a harmless-looking path into a protocol-relative URL.
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/;

// Landing back on these after login would restart the login flow.
const LOOP_PREFIXES = ["/login", "/api/auth"];

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
