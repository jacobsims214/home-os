import { NextRequest, NextResponse } from "next/server";
import { createHash, randomBytes } from "node:crypto";
import { DEX_UI_CLIENT_ID, dexConfig } from "@/lib/auth/dex-config";
import { PKCE_COOKIE, PKCE_COOKIE_PATH, PKCE_MAX_AGE, encodePkceCookie } from "@/lib/auth/pkce-cookie";
import { DEFAULT_RETURN_PATH, returnPath } from "@/lib/auth/return-path";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

function base64url(buf: Buffer): string {
  return buf.toString("base64").replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// Starts the Dex authorization-code flow with PKCE (RFC 7636, S256).
// ?redirect=<path> is where the callback sends the browser afterwards; it is
// sanitised here and again in the callback (lib/auth/return-path.ts).
export async function GET(request: NextRequest) {
  // redirect_uri and the public origin come from DEX_UI_REDIRECT_URI /
  // PUBLIC_URL (lib/auth/dex-config.ts); the callback uses the same values, so
  // redirect_uri matches exactly in the code exchange.
  const { publicOrigin, redirectUri } = dexConfig(request);

  // 32 random bytes -> 43-char base64url verifier (RFC 7636 §4.1 allows 43-128).
  const verifier = base64url(randomBytes(32));
  const challenge = base64url(createHash("sha256").update(verifier).digest());
  const state = base64url(randomBytes(16));
  const target = returnPath(request.nextUrl.searchParams.get("redirect"));

  const params = new URLSearchParams({
    response_type: "code",
    client_id: DEX_UI_CLIENT_ID,
    redirect_uri: redirectUri,
    scope: "openid email profile offline_access",
    state,
    code_challenge: challenge,
    code_challenge_method: "S256",
  });

  const response = NextResponse.redirect(new URL(`/dex/auth?${params.toString()}`, publicOrigin));
  response.headers.set("Cache-Control", "no-store");
  response.cookies.set(
    PKCE_COOKIE,
    encodePkceCookie({ state, verifier, returnPath: target === DEFAULT_RETURN_PATH ? null : target }),
    {
      httpOnly: true,
      secure: process.env.NODE_ENV === "production",
      sameSite: "lax",
      path: PKCE_COOKIE_PATH,
      maxAge: PKCE_MAX_AGE,
    },
  );
  return response;
}
