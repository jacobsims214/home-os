"use client";

import { useEffect } from "react";
import { Card, Text, Loader } from "@mantine/core";

export default function LoginPage() {
  useEffect(() => {
    // Redirect to Dex OIDC authorize endpoint
    const params = new URLSearchParams({
      response_type: "code",
      client_id: "home-os-ui",
      redirect_uri: `${window.location.origin}/api/auth/callback`,
      scope: "openid email profile",
    });
    window.location.href = `/dex/auth?${params.toString()}`;
  }, []);

  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50 px-4">
      <Card className="w-full max-w-md" padding="xl">
        <div className="text-center">
          <Loader size="lg" className="mx-auto" />
          <Text size="lg" fw={500} mt="md">Redirecting to login...</Text>
          <Text size="sm" c="dimmed" mt="sm">
            You&apos;ll be redirected to the authentication page.
          </Text>
        </div>
      </Card>
    </div>
  );
}