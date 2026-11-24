"use client";

import { Suspense, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useSearchParams } from "next/navigation";
import Link from "next/link";
import { notifications } from "@mantine/notifications";
import { apiFetch } from "@/lib/api";
import { Button, PasswordInput } from "@mantine/core";
import { useForm } from "@mantine/form";

/**
 * /reset-password, rebuilt to the approved settings/onboarding mockup
 * (artifact #103 v1, "standalone screens: auth" -> new password) at phone
 * width (#4562).
 *
 * WHAT DID NOT CHANGE: the token comes from `?token=`, the form validates on
 * the client (8 characters, both fields equal), and the only call is
 * POST /api/v1/auth/reset-password {token, password} through `apiFetch`, which
 * keeps sending it to the API's same-origin /api/v1 route rather than /bff
 * because there is no session yet (lib/api.ts PUBLIC_API_PATHS). Nothing here
 * touches Dex or the PKCE flow.
 *
 * WHAT DID: the presentation. Before, the page drew a centred min-h-screen
 * wrapper around a Mantine `Card` while the (auth) layout drew its own
 * centred wrapper and card too -- a card in a card at 390 px, which is the
 * defect the audit (#4509) and this ticket are about. The layout now owns the
 * frame (full-bleed page on a phone, card from 768 px), so this page draws
 * only its content, and the fields are 48 px tall with 16 px text so iOS does
 * not zoom the viewport when a field takes focus.
 *
 * The mockup's three states, in its order: no token (a dead link), the form,
 * and the confirmation. Each one keeps its own heading and one way forward.
 */

// The mockup's auth type scale (`.acard h1`, `.acard .sub`) and a 48 px
// primary action (`.btn.block`), used by every state on this page.
const H1 = "m-0 text-[24px] font-semibold leading-tight tracking-[-0.015em] text-ink";
const SUB = "mb-5 mt-1.5 text-[15px] leading-normal text-ink-2";
const ACTION = "min-h-12 text-[15px]";

function AlertIcon() {
  return (
    <span
      aria-hidden
      className="mb-4 grid size-14 place-items-center rounded-token-lg bg-warn-bg text-warn-ink"
    >
      <svg
        width={28}
        height={28}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={1.75}
        strokeLinecap="round"
        strokeLinejoin="round"
        focusable="false"
      >
        <path d="M12 9v4M12 17v.01" />
        <path d="M10.3 4.2 2.6 17.5A2 2 0 0 0 4.3 20.5h15.4a2 2 0 0 0 1.7-3L13.7 4.2a2 2 0 0 0-3.4 0z" />
      </svg>
    </span>
  );
}

function ResetPasswordForm() {
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

  // No token in the URL: a dead link, laid out like the mockup's dead invite.
  if (!token) {
    return (
      <div data-auth-page="reset-password" data-state="no-token">
        <AlertIcon />
        <h1 className={`${H1} text-danger`}>Invalid reset link</h1>
        <p className={SUB}>
          This password reset link is missing a token. Please check your email for the full
          link or request a new password reset.
        </p>
        <Button component={Link} href="/login" variant="default" size="md" fullWidth className={ACTION}>
          Back to sign in
        </Button>
      </div>
    );
  }

  // Success: one confirmation and one way on, as the mockup's done state.
  if (success) {
    return (
      <div data-auth-page="reset-password" data-state="done" role="status">
        <span
          aria-hidden
          className="mb-4 grid size-14 place-items-center rounded-token-lg bg-accent-soft text-accent"
        >
          <svg
            width={28}
            height={28}
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth={1.75}
            strokeLinecap="round"
            strokeLinejoin="round"
            focusable="false"
          >
            <path d="m5 12 5 5L20 7" />
          </svg>
        </span>
        <h1 className={H1}>Password reset successful</h1>
        <p className={SUB}>
          Your password has been changed. You can now sign in with your new password.
        </p>
        <Button component={Link} href="/login" size="md" fullWidth className={ACTION}>
          Sign in
        </Button>
      </div>
    );
  }

  return (
    <div data-auth-page="reset-password" data-state="form">
      <h1 className={H1}>Choose a new password</h1>
      <p className={SUB}>You&apos;ll be signed out on your other devices.</p>

      <form
        className="flex flex-col gap-4"
        onSubmit={form.onSubmit((values) => {
          resetMutation.mutate({
            token,
            password: values.password,
          });
        })}
      >
        <PasswordInput
          label="New password"
          description="At least 8 characters. A short phrase is easier to remember."
          autoComplete="new-password"
          required
          size="md"
          placeholder="••••••••"
          // The mockup puts the hint UNDER the field (`.fld` then `.help`),
          // where Mantine's default order draws it between label and input.
          inputWrapperOrder={["label", "input", "description", "error"]}
          // 16 px: iOS zooms the whole viewport on focus below that.
          styles={{ input: { height: 48, fontSize: 16 } }}
          {...form.getInputProps("password")}
        />

        <PasswordInput
          label="Type it again"
          autoComplete="new-password"
          required
          size="md"
          placeholder="••••••••"
          inputWrapperOrder={["label", "input", "description", "error"]}
          styles={{ input: { height: 48, fontSize: 16 } }}
          {...form.getInputProps("confirmPassword")}
        />

        <Button type="submit" loading={resetMutation.isPending} size="md" fullWidth className={ACTION}>
          Save password and sign in
        </Button>
      </form>

      <p className="mb-0 mt-5 text-center text-sm text-ink-2">
        Remember your password?{" "}
        <Link href="/login" className="font-semibold text-accent underline-offset-2 hover:underline">
          Sign in
        </Link>
      </p>
    </div>
  );
}

export default function ResetPasswordPage() {
  return (
    <Suspense
      fallback={
        <p role="status" className="m-0 text-sm text-ink-2">
          Loading...
        </p>
      }
    >
      <ResetPasswordForm />
    </Suspense>
  );
}
