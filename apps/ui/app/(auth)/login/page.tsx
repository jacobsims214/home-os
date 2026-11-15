"use client";

import { useEffect, useState } from "react";
import { Button, Card, Text, Loader } from "@mantine/core";

// Server route that creates the PKCE verifier (httpOnly cookie) and redirects
// to Dex's authorize endpoint with code_challenge + S256.
const LOGIN_START = "/api/auth/login";

export default function LoginPage() {
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const err = new URLSearchParams(window.location.search).get("error");
    if (err) {
      // Don't bounce straight back to Dex after a failed callback: that loops.
      setError(err);
      return;
    }
    window.location.replace(LOGIN_START);
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
            <Button component="a" href={LOGIN_START} mt="lg">
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
