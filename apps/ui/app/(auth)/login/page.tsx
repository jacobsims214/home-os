"use client";

import { useState, useEffect } from "react";
import { useMutation } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { notifications } from "@mantine/notifications";
import { apiFetch } from "@/lib/api";
import { useAuthStore, type User } from "@/stores/auth";
import { Card, TextInput, PasswordInput, Button } from "@mantine/core";
import { useForm } from "@mantine/form";

interface LoginResponse {
  token: string;
}

export default function LoginPage() {
  const router = useRouter();

  const form = useForm({
    initialValues: {
      email: "",
      password: "",
    },
    validate: {
      email: (value) => (/^\S+@\S+$/.test(value) ? null : "Invalid email"),
      password: (value) => (value.length < 1 ? "Password is required" : null),
    },
  });

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
        { id: "", email: form.values.email, name: "", avatar_url: null },
        data.token,
      );

      // Fetch user profile (response is NOT wrapped in { data: ... })
      try {
        const user = await apiFetch<User>("/api/v1/auth/me", {
          headers: { Authorization: `Bearer ${data.token}` },
        });
        useAuthStore.getState().setAuth(user, data.token);
        notifications.show({
          title: "Success",
          message: "Welcome back!",
          color: "green",
        });
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
    onError: (error: Error) => {
      notifications.show({
        title: "Login failed",
        message: error.message || "Invalid credentials",
        color: "red",
      });
    },
  });

  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50 px-4">
      <Card className="w-full max-w-md">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-gray-900">Sign in</h1>
          <p className="mt-2 text-sm text-gray-600">
            Welcome back! Please enter your details.
          </p>
        </div>

        <form
          className="mt-6 space-y-4"
          onSubmit={form.onSubmit((values) => {
            loginMutation.mutate(values);
          })}
        >
          <TextInput
            label="Email"
            type="email"
            autoComplete="email"
            required
            placeholder="you@example.com"
            {...form.getInputProps("email")}
          />

          <PasswordInput
            label="Password"
            autoComplete="current-password"
            required
            placeholder="••••••••"
            {...form.getInputProps("password")}
          />

          <Button
            type="submit"
            loading={loginMutation.isPending}
            className="w-full"
            size="md"
          >
            Sign in
          </Button>
        </form>

        <div className="mt-6 text-center text-sm text-gray-600">
          Don&apos;t have an account?{" "}
          <Link
            href="/register"
            className="font-semibold text-indigo-600 hover:text-indigo-500"
          >
            Sign up
          </Link>
        </div>
      </Card>
    </div>
  );
}
