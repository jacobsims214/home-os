"use client";

import { useEffect, useState } from "react";
import { Button, Card, Text, Loader } from "@mantine/core";
import { DEFAULT_RETURN_PATH, returnPath } from "@/lib/auth/return-path";
import { hasSessionRemnants, teardownSession } from "@/lib/session-teardown";

// Server route that creates the PKCE verifier (httpOnly cookie) and redirects
// to Dex's authorize endpoint with code_challenge + S256.
const LOGIN_START = "/api/auth/login";

// /api/auth/login, carrying ?redirect= only once it passes the same rule as
// middleware.ts: anyone can craft a /login?redirect= link (#4617). The login
// route and the callback check it again before the browser is sent there.
function loginStartUrl(redirect: string | null): string {
  const path = returnPath(redirect);
  return path === DEFAULT_RETURN_PATH ? LOGIN_START : `${LOGIN_START}?redirect=${encodeURIComponent(path)}`;
}

export default function LoginPage() {
  const [error, setError] = useState<string | null>(null);
  const [startUrl, setStartUrl] = useState(LOGIN_START);

  useEffect(() => {
    let cancelled = false;
    const params = new URLSearchParams(window.location.search);
    const start = loginStartUrl(params.get("redirect"));
    setStartUrl(start);
    const err = params.get("error");
    if (err) {
      // Don't bounce straight back to Dex after a failed callback: that loops.
      setError(err);
    }

    // Session-expiry sweep (#4648). Every way a session ends lands here: the
    // middleware's 307 with no cookie, the dashboard layout's redirect, a /bff
    // 401 (lib/api.ts) and the logout route's 303. If this browser is still
    // holding a signed-in person's leftovers — cached /bff responses from a
    // worker built before #4648, or the persisted home-os-auth profile — drop
    // them before signing anyone in, so an expired session can never serve
    // cached authenticated data. Nothing to do on a first visit, which is why
    // this asks first.
    const sweep = hasSessionRemnants().then((remnants) => (remnants ? teardownSession() : undefined));

    void sweep
      .catch(() => undefined)
      .then(() => {
        // A failed sweep must never block the sign-in.
        if (!cancelled && !err) window.location.replace(start);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50 px-4">
      <Card className="w-full max-w-md" padding="xl">
        {error ? (
          <div className="text-center">
            <Text size="lg" fw={500}>Sign-in didn&apos;t complete</Text>
            <Text size="sm" c="dimmed" mt="sm">
              Something went wrong while signing you in ({error}). Please try again.
            </Text>
            <Button component="a" href={startUrl} mt="lg">
              Try again
            </Button>
          </div>
        ) : (
          <div className="text-center">
            <Loader size="lg" className="mx-auto" />
            <Text size="lg" fw={500} mt="md">Redirecting to login...</Text>
            <Text size="sm" c="dimmed" mt="sm">
              You&apos;ll be redirected to the authentication page.
            </Text>
          </div>
        )}
      </Card>
    </div>
  );
}
