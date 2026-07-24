"use client";

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { notifications } from "@mantine/notifications";
import { apiFetch } from "@/lib/api";
import { useAuthStore, type User } from "@/stores/auth";
import Button from "@/components/ui/Button";
import Card from "@/components/ui/Card";
import PasswordInput from "@/components/ui/PasswordInput";
import TextInput from "@/components/ui/TextInput";

interface RegisterResponse {
  token: string;
}

export default function RegisterPage() {
  const router = useRouter();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  const registerMutation = useMutation({
    mutationFn: (body: { name: string; email: string; password: string }) =>
      apiFetch<RegisterResponse>("/api/v1/auth/register", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: async (data) => {
      try {
        const user = await apiFetch<User>("/api/v1/auth/me", {
          headers: { Authorization: `Bearer ${data.token}` },
        });
        useAuthStore.getState().setAuth(user, data.token);
        notifications.show({
          title: "Account created",
          message: "Welcome to Home OS!",
          color: "green",
        });
      } catch {
        useAuthStore.getState().setAuth(
          { id: "", email, name, avatar_url: null },
          data.token,
        );
      }

      // Set httpOnly cookie for middleware auth checks
      await fetch("/api/auth", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: data.token }),
      });

      router.push("/dashboard");
    },
    onError: (error: Error) => {
      notifications.show({
        title: "Registration failed",
        message: error.message || "Please try again",
        color: "red",
      });
    },
  });

  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50 px-4">
      <Card className="w-full max-w-md">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-gray-900">Create account</h1>
          <p className="mt-2 text-sm text-gray-600">
            Join us and start managing your home.
          </p>
        </div>

        <form
          className="mt-6 space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            registerMutation.mutate({ name: name.trim(), email: email.trim(), password });
          }}
        >
          <TextInput
            label="Name"
            type="text"
            autoComplete="name"
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="John Doe"
          />

          <TextInput
            label="Email"
            type="email"
            autoComplete="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@example.com"
          />

          <PasswordInput
            label="Password"
            autoComplete="new-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="••••••••"
          />

          <Button
            type="submit"
            loading={registerMutation.isPending}
            className="w-full"
            size="md"
          >
            Create account
          </Button>
        </form>

        <div className="mt-6 text-center text-sm text-gray-600">
          Already have an account?{" "}
          <Link
            href="/login"
            className="font-semibold text-indigo-600 hover:text-indigo-500"
          >
            Sign in
          </Link>
        </div>
      </Card>
    </div>
  );
}
