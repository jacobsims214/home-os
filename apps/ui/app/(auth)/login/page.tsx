"use client";

import { useState, useEffect } from "react";
import { useMutation } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { apiFetch } from "@/lib/api";
import { useAuthStore, type User } from "@/stores/auth";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";

interface LoginResponse {
  token: string;
}

export default function LoginPage() {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  // Redirect to /dashboard if the auth cookie is present
  // (cookie is the source of truth for middleware, not Zustand)
  useEffect(() => {
    const cookie = document.cookie
      .split("; ")
      .find((c) => c.startsWith("home-os-token="));
    if (cookie) {
      router.push("/dashboard");
    }
  }, [router]);

  const loginMutation = useMutation({
    mutationFn: (body: { email: string; password: string }) =>
      apiFetch<LoginResponse>("/api/v1/auth/login", {
        method: "POST",
        body,
      }),
    onSuccess: async (data) => {
      // Store token in Zustand immediately so apiFetch has it
      useAuthStore.getState().setAuth(
        { id: "", email, name: "", avatar_url: null },
        data.token,
      );

      // Fetch user profile (response is NOT wrapped in { data: ... })
      try {
        const user = await apiFetch<User>("/api/v1/auth/me", {
          headers: { Authorization: `Bearer ${data.token}` },
        });
        useAuthStore.getState().setAuth(user, data.token);
      } catch {
        // Keep the minimal user info from above
      }

      // Set cookie for middleware auth checks
      await fetch("/api/auth", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: data.token }),
      });

      router.push("/dashboard");
    },
  });

  const errorMessage =
    loginMutation.error instanceof Error
      ? loginMutation.error.message
      : loginMutation.error
        ? String((loginMutation.error as { message?: string }).message ?? "Login failed")
        : null;

  return (
    <>
      <h1 className="text-xl font-semibold text-gray-900">Sign in</h1>

      <form
        className="mt-6 space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          loginMutation.mutate({ email, password });
        }}
      >
        {errorMessage && (
          <div className="rounded-md bg-red-50 px-4 py-3 text-sm text-red-700">
            {errorMessage}
          </div>
        )}

        <Input
          label="Email"
          type="email"
          autoComplete="email"
          required
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />

        <Input
          label="Password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />

        <Button
          type="submit"
          loading={loginMutation.isPending}
          className="w-full"
        >
          Sign in
        </Button>
      </form>

      <p className="mt-6 text-center text-sm text-gray-600">
        Don&apos;t have an account?{" "}
        <Link
          href="/register"
          className="font-semibold text-indigo-600 hover:text-indigo-500"
        >
          Sign up
        </Link>
      </p>
    </>
  );
}
