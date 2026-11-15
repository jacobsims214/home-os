import { NextRequest, NextResponse } from "next/server";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

// Session cookies. All are httpOnly: no token is ever readable by client JS.
// home-os-token is read by the BFF (app/bff/[...path]/route.ts) and middleware.
const COOKIE_NAME = "home-os-token";
// Dex refresh token (granted for the offline_access scope). Used by the BFF
// refresh flow; never sent to the Go API.
const REFRESH_COOKIE = "home-os-refresh";
// Removed in #4412: older builds set this JS-readable copy of the id token.
// We still expire it so browsers that have one drop it on next login.
const LEGACY_CLIENT_COOKIE = "home-os-token-client";
// Fallback when neither the id_token exp nor expires_in is usable (Dex default
// idTokens expiry is 24h).
const DEFAULT_TOKEN_MAX_AGE = 60 * 60 * 24;
// Dex does not report refresh-token lifetime. Its default config never expires
// them, so cap the cookie at 30 days; a rejected refresh just means re-login.
const REFRESH_MAX_AGE = 60 * 60 * 24 * 30;
// Set by app/api/auth/login/route.ts as "<state>.<code_verifier>".
const PKCE_COOKIE = "home-os-pkce";
const PKCE_COOKIE_PATH = "/api/auth";

const baseCookie = () => ({
  httpOnly: true,
  secure: process.env.NODE_ENV === "production",
  sameSite: "lax" as const,
  path: "/",
});

// Seconds until the JWT's exp claim, or null if it has none / can't be parsed.
// The signature is not checked here: this only sizes the cookie, the Go API
// validates the token on every request.
function secondsUntilExp(jwt: string): number | null {
  const payload = jwt.split(".")[1];
  if (!payload) return null;
  try {
    const claims = JSON.parse(Buffer.from(payload, "base64url").toString("utf8"));
    if (typeof claims.exp !== "number") return null;
    return Math.floor(claims.exp - Date.now() / 1000);
  } catch {
    return null;
  }
}

function tokenMaxAge(idToken: string, expiresIn: unknown): number {
  const fromExp = secondsUntilExp(idToken);
  if (fromExp !== null) return fromExp;
  if (typeof expiresIn === "number" && Number.isFinite(expiresIn)) return Math.floor(expiresIn);
  return DEFAULT_TOKEN_MAX_AGE;
}

// Redirect and always drop the one-time PKCE cookie.
function redirectClearingPkce(url: URL): NextResponse {
  const response = NextResponse.redirect(url);
  response.headers.set("Cache-Control", "no-store");
  response.cookies.set(PKCE_COOKIE, "", {
    ...baseCookie(),
    path: PKCE_COOKIE_PATH,
    maxAge: 0,
  });
  return response;
}

export async function GET(request: NextRequest) {
  const { searchParams } = new URL(request.url);
  const code = searchParams.get("code");
  const state = searchParams.get("state");

  // Build the public origin from Host headers (what the browser sees)
  // This MUST match the redirect_uri sent in the initial auth request
  const host = request.headers.get("x-forwarded-host") || request.headers.get("host") || "localhost:8000";
  const proto = request.headers.get("x-forwarded-proto") || "http";
  const publicOrigin = `${proto}://${host}`;

  if (!code) {
    return redirectClearingPkce(new URL("/login?error=no_code", publicOrigin));
  }

  // PKCE: the verifier must come from the httpOnly cookie set when this login
  // started, and the state must match. Without it we never call Dex.
  const pkce = request.cookies.get(PKCE_COOKIE)?.value ?? "";
  const sep = pkce.indexOf(".");
  const expectedState = sep > 0 ? pkce.slice(0, sep) : "";
  const codeVerifier = sep > 0 ? pkce.slice(sep + 1) : "";
  if (!codeVerifier) {
    return redirectClearingPkce(new URL("/login?error=missing_verifier", publicOrigin));
  }
  if (!state || state !== expectedState) {
    return redirectClearingPkce(new URL("/login?error=state_mismatch", publicOrigin));
  }

  try {
    const redirectUri = `${publicOrigin}/api/auth/callback`;

    // Exchange code for tokens via Dex
    const tokenResponse = await fetch("http://home-os-dex:5556/dex/token", {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        grant_type: "authorization_code",
        code,
        redirect_uri: redirectUri,
        client_id: "home-os-ui",
        code_verifier: codeVerifier,
      }),
      cache: "no-store",
    });

    if (!tokenResponse.ok) {
      // Dex error bodies are {error, error_description}; they carry no tokens.
      const errorText = await tokenResponse.text();
      console.error("Token exchange failed:", tokenResponse.status, errorText);
      return redirectClearingPkce(new URL("/login?error=token_exchange_failed", publicOrigin));
    }

    const tokens = await tokenResponse.json();
    const idToken: unknown = tokens.id_token;
    const refreshToken: unknown = tokens.refresh_token;

    if (typeof idToken !== "string" || !idToken) {
      return redirectClearingPkce(new URL("/login?error=no_id_token", publicOrigin));
    }

    const maxAge = tokenMaxAge(idToken, tokens.expires_in);
    if (maxAge <= 0) {
      return redirectClearingPkce(new URL("/login?error=token_expired", publicOrigin));
    }

    const response = redirectClearingPkce(new URL("/dashboard", publicOrigin));
    // Session token for the BFF; expires with the id token.
    response.cookies.set(COOKIE_NAME, idToken, { ...baseCookie(), maxAge });
    if (typeof refreshToken === "string" && refreshToken) {
      response.cookies.set(REFRESH_COOKIE, refreshToken, { ...baseCookie(), maxAge: REFRESH_MAX_AGE });
    } else {
      // No refresh token this time: drop any stale one from an earlier session.
      response.cookies.set(REFRESH_COOKIE, "", { ...baseCookie(), maxAge: 0 });
    }
    response.cookies.set(LEGACY_CLIENT_COOKIE, "", { ...baseCookie(), maxAge: 0 });

    return response;
  } catch (error) {
    console.error("Callback error:", error instanceof Error ? error.message : "unknown");
    return redirectClearingPkce(new URL("/login?error=callback_error", publicOrigin));
  }
}
