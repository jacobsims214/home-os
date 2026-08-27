"use client";

import { Suspense, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import { notifications } from "@mantine/notifications";
import { apiFetch } from "@/lib/api";
import { Card, PasswordInput, Button, Text, Alert } from "@mantine/core";
import { useForm } from "@mantine/form";

function ResetPasswordForm() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const token = searchParams?.get("token") ?? "";
  const [success, setSuccess] = useState(false);

  const form = useForm({
    initialValues: {
      password: "",
      confirmPassword: "",
    },
    validate: {
      password: (value) =>
        value.length < 8 ? "Password must be at least 8 characters" : null,
      confirmPassword: (value, values) =>
        value !== values.password ? "Passwords do not match" : null,
    },
  });

  const resetMutation = useMutation({
    mutationFn: (body: { token: string; password: string }) =>
      apiFetch<{ message: string }>("/api/v1/auth/reset-password", {
        method: "POST",
        body,
      }),
    onSuccess: () => {
      setSuccess(true);
      notifications.show({
        title: "Password reset",
        message: "Your password has been reset successfully.",
        color: "green",
      });
    },
    onError: (error: Error) => {
      notifications.show({
        title: "Reset failed",
        message: error.message || "Invalid or expired reset token",
        color: "red",
      });
    },
  });

  // No token in the URL — show an error state
  if (!token) {
    return (
      <div className="text-center">
        <Text c="red" size="lg" fw={600}>
          Invalid reset link
        </Text>
        <Text c="dimmed" size="sm" mt="sm">
          This password reset link is missing a token. Please check your email
          for the full link or request a new password reset.
        </Text>
        <Button
          component={Link}
          href="/login"
          mt="lg"
          variant="outline"
          fullWidth
        >
          Back to sign in
        </Button>
      </div>
    );
  }

  // Success state — show confirmation
  if (success) {
    return (
      <div className="text-center">
        <Text size="lg" fw={600} c="green">
          Password reset successful
        </Text>
        <Text c="dimmed" size="sm" mt="sm">
          Your password has been changed. You can now sign in with your new
          password.
        </Text>
        <Button
          component={Link}
          href="/login"
          mt="lg"
          fullWidth
        >
          Sign in
        </Button>
      </div>
    );
  }

  return (
    <>
      <div className="text-center">
        <h1 className="text-2xl font-bold text-gray-900">Reset password</h1>
        <p className="mt-2 text-sm text-gray-600">
          Enter your new password below.
        </p>
      </div>

      <form
        className="mt-6 space-y-4"
        onSubmit={form.onSubmit((values) => {
          resetMutation.mutate({
            token,
            password: values.password,
          });
        })}
      >
        <PasswordInput
          label="New password"
          autoComplete="new-password"
          required
          placeholder="••••••••"
          {...form.getInputProps("password")}
        />

        <PasswordInput
          label="Confirm password"
          autoComplete="new-password"
          required
          placeholder="••••••••"
          {...form.getInputProps("confirmPassword")}
        />

        <Button
          type="submit"
          loading={resetMutation.isPending}
          className="w-full"
          size="md"
        >
          Reset password
        </Button>
      </form>

      <div className="mt-6 text-center text-sm text-gray-600">
        Remember your password?{" "}
        <Link
          href="/login"
          className="font-semibold text-indigo-600 hover:text-indigo-500"
        >
          Sign in
        </Link>
      </div>
    </>
  );
}

export default function ResetPasswordPage() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50 px-4">
      <Card className="w-full max-w-md">
        <Suspense
          fallback={
            <div className="text-center">
              <Text c="dimmed" size="sm">
                Loading...
              </Text>
            </div>
          }
        >
          <ResetPasswordForm />
        </Suspense>
      </Card>
    </div>
  );
}