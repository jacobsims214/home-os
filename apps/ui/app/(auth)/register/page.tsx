"use client";

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { apiFetch } from "@/lib/api";
import { useAuthStore, type User } from "@/stores/auth";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";

interface RegisterResponse {
  token: string;
}

export default function RegisterPage() {
  const router = useRouter();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});

  const registerMutation = useMutation({
    mutationFn: (body: {
      name: string;
      email: string;
      password: string;
    }) =>
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
  });

  const errorMessage =
    registerMutation.error instanceof Error
      ? registerMutation.error.message
      : registerMutation.error
        ? String((registerMutation.error as { message?: string }).message ?? "Registration failed")
        : null;

  function validate(): boolean {
    const next: Record<string, string> = {};
    if (!name.trim()) next.name = "Name is required";
    if (!email.trim()) next.email = "Email is required";
    if (!password) {
      next.password = "Password is required";
    } else if (password.length < 8) {
      next.password = "Password must be at least 8 characters";
    }
    setErrors(next);
    return Object.keys(next).length === 0;
  }

  return (
    <>
      <h1 className="text-xl font-semibold text-gray-900">Create account</h1>

      <form
        className="mt-6 space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!validate()) return;
          registerMutation.mutate({ name: name.trim(), email: email.trim(), password });
        }}
      >
        {errorMessage && (
          <div className="rounded-md bg-red-50 px-4 py-3 text-sm text-red-700">
            {errorMessage}
          </div>
        )}

        <Input
          label="Name"
          type="text"
          autoComplete="name"
          required
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={errors.name}
        />

        <Input
          label="Email"
          type="email"
          autoComplete="email"
          required
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          error={errors.email}
        />

        <Input
          label="Password"
          type="password"
          autoComplete="new-password"
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          error={errors.password}
        />

        <Button
          type="submit"
          loading={registerMutation.isPending}
          className="w-full"
        >
          Create account
        </Button>
      </form>

      <p className="mt-6 text-center text-sm text-gray-600">
        Already have an account?{" "}
        <Link
          href="/login"
          className="font-semibold text-indigo-600 hover:text-indigo-500"
        >
          Sign in
        </Link>
      </p>
    </>
  );
}
