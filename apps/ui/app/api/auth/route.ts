import { NextRequest, NextResponse } from "next/server";

/**
 * Session restore route handler.
 *
 * GET /api/auth — return decoded user info from the httpOnly session cookie.
 *
 * This route never creates or clears a session. Sessions are created only by
 * the Dex callback (app/api/auth/callback) and ended by POST /api/auth/logout,
 * which clears both home-os-token and home-os-refresh.
 */

const COOKIE_NAME = "home-os-token";

export async function GET(request: NextRequest) {
  const token = request.cookies.get(COOKIE_NAME)?.value;

  if (!token) {
    return NextResponse.json({ user: null }, { status: 401 });
  }

  try {
    const [, payloadB64] = token.split(".");
    if (!payloadB64) {
      return NextResponse.json({ user: null }, { status: 401 });
    }

    const payload = JSON.parse(
      Buffer.from(payloadB64, "base64url").toString("utf-8"),
    );

    // Handle both hand-rolled JWT (user_id, household_id) and Dex OIDC (sub, email)
    return NextResponse.json({
      user: {
        id: payload.user_id || payload.sub || payload.email,
        householdId: payload.household_id,
        role: payload.role,
        email: payload.email,
        name: payload.name,
      },
    });
  } catch {
    return NextResponse.json({ user: null }, { status: 401 });
  }
}
