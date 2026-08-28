import { NextRequest, NextResponse } from "next/server";

/**
 * BFF session route handler — manages the httpOnly JWT cookie.
 *
 * POST   /api/auth  — store the JWT in an httpOnly, secure, same-site cookie.
 * DELETE /api/auth  — clear the session cookie.
 * GET    /api/auth  — return decoded user info from the cookie (for session restore on page load).
 *
 * The browser never directly holds the JWT. The BFF reads it from the cookie
 * and injects it as an Authorization header when proxying to the Go API.
 */

const COOKIE_NAME = "home-os-token";
const COOKIE_MAX_AGE = 60 * 60 * 24; // 24 hours — matches JWT expiry

function setTokenCookie(response: NextResponse, token: string) {
  response.cookies.set(COOKIE_NAME, token, {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "lax",
    path: "/",
    maxAge: COOKIE_MAX_AGE,
  });
}

function clearTokenCookie(response: NextResponse) {
  response.cookies.set(COOKIE_NAME, "", {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "lax",
    path: "/",
    maxAge: 0, // expire immediately
  });
}

export async function POST(request: NextRequest) {
  try {
    const body = await request.json();
    const { token } = body;

    if (!token || typeof token !== "string") {
      return NextResponse.json(
        { error: "Token is required" },
        { status: 400 },
      );
    }

    const response = NextResponse.json({ ok: true });
    setTokenCookie(response, token);
    return response;
  } catch {
    return NextResponse.json(
      { error: "Invalid request body" },
      { status: 400 },
    );
  }
}

export async function DELETE() {
  const response = NextResponse.json({ ok: true });
  clearTokenCookie(response);
  return response;
}

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
