import { NextRequest, NextResponse } from "next/server";
import { createHash, randomBytes } from "node:crypto";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

// Short-lived httpOnly cookie holding "<state>.<code_verifier>" between the
// authorize redirect and the Dex callback. Read and cleared by
// app/api/auth/callback/route.ts.
const PKCE_COOKIE = "home-os-pkce";
const PKCE_MAX_AGE = 60 * 10;

function base64url(buf: Buffer): string {
  return buf.toString("base64").replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// Starts the Dex authorization-code flow with PKCE (RFC 7636, S256).
export async function GET(request: NextRequest) {
  // Same public-origin logic as the callback: redirect_uri must match exactly.
  const host = request.headers.get("x-forwarded-host") || request.headers.get("host") || "localhost:8000";
  const proto = request.headers.get("x-forwarded-proto") || "http";
  const publicOrigin = `${proto}://${host}`;

  // 32 random bytes -> 43-char base64url verifier (RFC 7636 §4.1 allows 43-128).
  const verifier = base64url(randomBytes(32));
  const challenge = base64url(createHash("sha256").update(verifier).digest());
  const state = base64url(randomBytes(16));

  const params = new URLSearchParams({
    response_type: "code",
    client_id: "home-os-ui",
    redirect_uri: `${publicOrigin}/api/auth/callback`,
    scope: "openid email profile",
    state,
    code_challenge: challenge,
    code_challenge_method: "S256",
  });

  const response = NextResponse.redirect(new URL(`/dex/auth?${params.toString()}`, publicOrigin));
  response.headers.set("Cache-Control", "no-store");
  response.cookies.set(PKCE_COOKIE, `${state}.${verifier}`, {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "lax",
    path: "/api/auth",
    maxAge: PKCE_MAX_AGE,
  });
  return response;
}
