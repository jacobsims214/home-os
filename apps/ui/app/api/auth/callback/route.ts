import { NextRequest, NextResponse } from "next/server";

const COOKIE_NAME = "home-os-token";
const COOKIE_MAX_AGE = 60 * 60 * 24;

export async function GET(request: NextRequest) {
  const { searchParams } = new URL(request.url);
  const code = searchParams.get("code");
  const state = searchParams.get("state");

  if (!code) {
    return NextResponse.redirect(new URL("/login?error=no_code", request.url));
  }

  try {
    // Exchange code for tokens via Dex
    const tokenResponse = await fetch("http://home-os-dex:5556/dex/token", {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        grant_type: "authorization_code",
        code,
        redirect_uri: `${request.nextUrl.origin}/api/auth/callback`,
        client_id: "home-os-ui",
      }),
    });

    if (!tokenResponse.ok) {
      const errorText = await tokenResponse.text();
      console.error("Token exchange failed:", errorText);
      return NextResponse.redirect(new URL("/login?error=token_exchange_failed", request.url));
    }

    const tokens = await tokenResponse.json();
    const idToken = tokens.id_token;

    if (!idToken) {
      return NextResponse.redirect(new URL("/login?error=no_id_token", request.url));
    }

    // Store the token in an httpOnly cookie
    const response = NextResponse.redirect(new URL("/dashboard", request.url));
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
    return NextResponse.redirect(new URL("/login?error=callback_error", request.url));
  }
}