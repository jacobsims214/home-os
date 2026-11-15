/**
 * The one-time httpOnly cookie that carries a login from /api/auth/login to
 * the Dex callback: "<state>.<code_verifier>[.<base64url(return path)>]".
 *
 * state and verifier are base64url (no dots). The return path (#4617) rides in
 * the same cookie so it is bound to this login's state; it is re-validated in
 * the callback anyway. Cookies written before #4617 have no third part.
 */
export const PKCE_COOKIE = "home-os-pkce";
export const PKCE_COOKIE_PATH = "/api/auth";
export const PKCE_MAX_AGE = 60 * 10;

export type PkceCookie = { state: string; verifier: string; returnPath: string | null };

export function encodePkceCookie({ state, verifier, returnPath }: PkceCookie): string {
  const parts = [state, verifier];
  if (returnPath) parts.push(Buffer.from(returnPath, "utf8").toString("base64url"));
  return parts.join(".");
}

/** The parsed cookie, or null when state or verifier is missing. */
export function decodePkceCookie(value: string | undefined): PkceCookie | null {
  const [state = "", verifier = "", encodedReturn = ""] = (value ?? "").split(".");
  if (!state || !verifier) return null;
  let returnPath: string | null = null;
  if (encodedReturn) {
    try {
      returnPath = Buffer.from(encodedReturn, "base64url").toString("utf8") || null;
    } catch {
      returnPath = null;
    }
  }
  return { state, verifier, returnPath };
}
