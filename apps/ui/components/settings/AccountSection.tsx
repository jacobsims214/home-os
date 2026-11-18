"use client";

/**
 * Settings -> Account (#4560): the sign-in email, and changing the password.
 *
 * POST /api/v1/auth/change-password (#4822) verifies the CURRENT password
 * against Dex before it writes anything, so this form asks for it and shows
 * what Dex answered:
 *   200 {"message":"password changed"}    -> the only success path. Nothing is
 *                                            claimed until this arrives.
 *   401 INVALID_CREDENTIALS                -> "current password is incorrect"
 *   400 (too short / too long)             -> the API's own sentence
 *   429                                    -> rate limited, with Retry-After
 *   502 / 503                              -> Dex could not verify or update it;
 *                                            the old password still works, and
 *                                            the copy says so instead of lying
 * Changing it revokes the caller's refresh tokens, so other devices are signed
 * out; the copy says that before the person commits.
 */

import { useState } from "react";
import { Alert, Button, PasswordInput, TextInput } from "@mantine/core";
import { IconAlertTriangle, IconCheck } from "@tabler/icons-react";
import { apiFetch, ApiError } from "@/lib/api";
import { useMe } from "@/lib/today/api";
import { SectionHeader, SettingsCard } from "./SettingsFrame";
import classes from "./settings.module.css";

/** The shared API rule (apps/api/internal/auth/password.go). */
const MIN_PASSWORD = 8;
const MAX_PASSWORD = 72;

type Outcome = { kind: "idle" } | { kind: "saved" } | { kind: "error"; message: string };

/** What to tell the person for a failed change, keyed by what Dex/API answered. */
function failureMessage(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return "Your password was not changed. Check your connection and try again.";
  }
  const body = error.body as { error?: { code?: unknown; message?: unknown } } | undefined;
  const apiMessage = typeof body?.error?.message === "string" ? body.error.message : null;
  const code = typeof body?.error?.code === "string" ? body.error.code : null;
  if (error.status === 401 && code === "UNAUTHORIZED") {
    // The BFF's own 401: the session really is gone (lib/api would normally
    // have navigated; this call opts out, so say it here).
    return "Your session ended. Sign in again and change your password from Settings.";
  }
  if (error.status === 401) {
    // The API's 401 on this route is the current-password check
    // (INVALID_CREDENTIALS). Nothing was written: the new password was never
    // set, so the old one still works.
    return "That current password is not right. Nothing was changed.";
  }
  if (error.status === 400 && apiMessage) return apiMessage;
  if (error.status === 429) return "Too many tries. Wait a minute and try again.";
  if (error.status === 502 || error.status === 503) {
    return "The sign-in service could not do it just now. Your old password still works — try again in a moment.";
  }
  return "Your password was not changed. Try again.";
}

export default function AccountSection() {
  // GET /api/v1/auth/me is the caller's own profile: the email, and the
  // name. The persisted auth store only holds what a page happened to put in
  // it — on a fresh device it is empty, which is why this section reads the
  // API rather than the store (it showed "…" with the store alone).
  const me = useMe();
  const email = me.data?.email;
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [outcome, setOutcome] = useState<Outcome>({ kind: "idle" });
  const [saving, setSaving] = useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setOutcome({ kind: "idle" });

    const errors: Record<string, string> = {};
    if (!currentPassword) errors.current = "Enter your current password";
    if (newPassword.length < MIN_PASSWORD) errors.next = `Use at least ${MIN_PASSWORD} characters`;
    else if (newPassword.length > MAX_PASSWORD) errors.next = `Use at most ${MAX_PASSWORD} characters`;
    else if (newPassword === currentPassword) errors.next = "Choose a password you have not used here";
    if (confirmPassword !== newPassword) errors.confirm = "These don't match yet";
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) return;

    setSaving(true);
    try {
      await apiFetch<{ message: string }>("/api/v1/auth/change-password", {
        method: "POST",
        body: { current_password: currentPassword, new_password: newPassword },
        // THIS endpoint answers 401 for a wrong CURRENT password
        // (INVALID_CREDENTIALS), not only for a dead session. Without this the
        // shared client would send the person to /login for a typo in one field
        // (lib/api.ts redirectOn401). The 401 is told apart by its code below.
        redirectOn401: false,
      });
      // 200 only: the API changed it and revoked the other sessions' refresh
      // tokens. Nothing below runs on any other status.
      setOutcome({ kind: "saved" });
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
    } catch (error) {
      setOutcome({ kind: "error", message: failureMessage(error) });
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <SectionHeader
        title="Account"
        description="The email you sign in with, and your password."
      />

      <SettingsCard title="Sign-in email">
        <TextInput
          label="Email"
          value={email ?? ""}
          placeholder={me.isError ? "Couldn't load your email" : "Loading…"}
          readOnly
          description="Only an instance admin can change this."
          data-settings-readonly="account-email"
        />
        {me.data?.name && (
          <TextInput mt={12} label="Name" value={me.data.name} readOnly data-settings-readonly="account-name" />
        )}
      </SettingsCard>

      <SettingsCard
        title="Password"
        description="Changing it signs you out on your other devices."
      >
        <form onSubmit={submit} noValidate>
          <div style={{ display: "grid", gap: 16, maxWidth: 420 }}>
            <PasswordInput
              label="Current password"
              value={currentPassword}
              onChange={(event) => {
                setCurrentPassword(event.currentTarget.value);
                setFieldErrors((e) => ({ ...e, current: "" }));
                setOutcome({ kind: "idle" });
              }}
              error={fieldErrors.current || undefined}
              autoComplete="current-password"
              data-settings-field="current-password"
              styles={{ input: { fontSize: 16 } }}
            />
            <PasswordInput
              label="New password"
              value={newPassword}
              onChange={(event) => {
                setNewPassword(event.currentTarget.value);
                setFieldErrors((e) => ({ ...e, next: "" }));
                setOutcome({ kind: "idle" });
              }}
              error={fieldErrors.next || undefined}
              description={`At least ${MIN_PASSWORD} characters. A short phrase is easier to remember.`}
              autoComplete="new-password"
              data-settings-field="new-password"
              styles={{ input: { fontSize: 16 } }}
            />
            <PasswordInput
              label="Type it again"
              value={confirmPassword}
              onChange={(event) => {
                setConfirmPassword(event.currentTarget.value);
                setFieldErrors((e) => ({ ...e, confirm: "" }));
                setOutcome({ kind: "idle" });
              }}
              error={fieldErrors.confirm || undefined}
              autoComplete="new-password"
              data-settings-field="confirm-password"
              styles={{ input: { fontSize: 16 } }}
            />
          </div>

          <div className={classes.formActions} style={{ marginTop: 16, alignItems: "center" }}>
            <Button type="submit" loading={saving}>
              Change password
            </Button>
          </div>

          {outcome.kind === "saved" && (
            <Alert
              mt={16}
              color="green"
              variant="light"
              icon={<IconCheck size={16} />}
              role="status"
              title="Password changed"
              data-settings-outcome="saved"
            >
              You&apos;re signed in here. Your other devices will need the new password.
            </Alert>
          )}
          {outcome.kind === "error" && (
            <Alert
              mt={16}
              color="red"
              variant="light"
              icon={<IconAlertTriangle size={16} />}
              role="alert"
              title="Password not changed"
              data-settings-outcome="error"
            >
              {outcome.message}
            </Alert>
          )}
        </form>
      </SettingsCard>
    </>
  );
}
