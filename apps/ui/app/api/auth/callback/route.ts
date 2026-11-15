import { NextRequest, NextResponse } from "next/server";

const COOKIE_NAME = "home-os-token";
const COOKIE_MAX_AGE = 60 * 60 * 24;
// Set by app/api/auth/login/route.ts as "<state>.<code_verifier>".
const PKCE_COOKIE = "home-os-pkce";
const PKCE_COOKIE_PATH = "/api/auth";

// Redirect and always drop the one-time PKCE cookie.
function redirectClearingPkce(url: URL): NextResponse {
  const response = NextResponse.redirect(url);
  response.cookies.set(PKCE_COOKIE, "", { path: PKCE_COOKIE_PATH, maxAge: 0 });
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
    });

    if (!tokenResponse.ok) {
      const errorText = await tokenResponse.text();
      console.error("Token exchange failed:", errorText);
      return redirectClearingPkce(new URL("/login?error=token_exchange_failed", publicOrigin));
    }

    const tokens = await tokenResponse.json();
    const idToken = tokens.id_token;

    if (!idToken) {
      return redirectClearingPkce(new URL("/login?error=no_id_token", publicOrigin));
    }

    // Store the token in an httpOnly cookie (for server-side/BFF reads)
    const response = redirectClearingPkce(new URL("/dashboard", publicOrigin));
    response.cookies.set(COOKIE_NAME, idToken, {
      httpOnly: true,
      secure: process.env.NODE_ENV === "production",
      sameSite: "lax",
      path: "/",
      maxAge: COOKIE_MAX_AGE,
    });
    // Also set a non-httpOnly cookie so client-side JS (apiFetch) can read it
    response.cookies.set("home-os-token-client", idToken, {
      httpOnly: false,
      secure: process.env.NODE_ENV === "production",
      sameSite: "lax",
      path: "/",
      maxAge: COOKIE_MAX_AGE,
    });

    return response;
  } catch (error) {
    console.error("Callback error:", error);
    return redirectClearingPkce(new URL("/login?error=callback_error", publicOrigin));
  }
}