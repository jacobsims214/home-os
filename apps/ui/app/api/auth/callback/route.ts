import { NextRequest, NextResponse } from "next/server";

const COOKIE_NAME = "home-os-token";
const COOKIE_MAX_AGE = 60 * 60 * 24;

export async function GET(request: NextRequest) {
  const { searchParams } = new URL(request.url);
  const code = searchParams.get("code");

  if (!code) {
    return NextResponse.redirect(new URL("/login?error=no_code", publicOrigin));
  }

  try {
    // Build the redirect_uri from the Host header (what the browser sees)
    // This MUST match the redirect_uri sent in the initial auth request
    const host = request.headers.get("x-forwarded-host") || request.headers.get("host") || "localhost:8000";
    const proto = request.headers.get("x-forwarded-proto") || "http";
    const publicOrigin = `${proto}://${host}`;
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
      }),
    });

    if (!tokenResponse.ok) {
      const errorText = await tokenResponse.text();
      console.error("Token exchange failed:", errorText);
      return NextResponse.redirect(new URL("/login?error=token_exchange_failed", publicOrigin));
    }

    const tokens = await tokenResponse.json();
    const idToken = tokens.id_token;

    if (!idToken) {
      return NextResponse.redirect(new URL("/login?error=no_id_token", publicOrigin));
    }

    // Store the token in an httpOnly cookie
    const response = NextResponse.redirect(new URL("/dashboard", publicOrigin));
    response.cookies.set(COOKIE_NAME, idToken, {
      httpOnly: true,
      secure: process.env.NODE_ENV === "production",
      sameSite: "lax",
      path: "/",
      maxAge: COOKIE_MAX_AGE,
    });

    return response;
  } catch (error) {
    console.error("Callback error:", error);
    return NextResponse.redirect(new URL("/login?error=callback_error", publicOrigin));
  }
}