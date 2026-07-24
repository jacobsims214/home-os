"use client";

import { useState, useEffect, type FormEvent } from "react";
import Modal from "@/components/ui/Modal";
import Input from "@/components/ui/Input";
import Button from "@/components/ui/Button";
import { useSecretsStore } from "@/stores/secrets";

/**
 * Which flow the prompt is in.
 *
 * - "setup"  — first-time master password creation (password + confirm)
 * - "unlock" — returning user re-entering their master password
 *
 * The parent decides which mode to render. The secrets store (sibling task
 * #1288) does not surface a "needs setup" flag, so SecretsSection must
 * determine that itself (e.g. by checking whether the verify endpoint
 * reports a configured key) and pass the right mode here.
 */
export type MasterPasswordPromptMode = "setup" | "unlock";

interface MasterPasswordPromptProps {
  open: boolean;
  onClose: () => void;
  /** Default "unlock". Pass "setup" for first-time master password creation. */
  mode?: MasterPasswordPromptMode;
}

/**
 * MasterPasswordPrompt — the gatekeeper for the secrets manager.
 *
 * The user must enter their master password to derive the AES-256-GCM
 * encryption key (PBKDF2, all client-side via Web Crypto API) before they
 * can view or create secrets. The key never leaves the browser.
 *
 * Two modes share one modal:
 *
 * **Setup mode** (first time): password + confirm-password fields, calls
 * `secretsStore.setup(password)`. Validates that the passwords match and
 * are non-empty before submitting.
 *
 * **Unlock mode** (returning user): single password field, calls
 * `secretsStore.unlock(password)`. Shows a "Lock" button to clear the
 * in-memory key. Wrong-password (401) errors are mapped to a friendly
 * message.
 */
export default function MasterPasswordPrompt({
  open,
  onClose,
  mode = "unlock",
}: MasterPasswordPromptProps) {
  const setup = useSecretsStore((s) => s.setup);
  const unlock = useSecretsStore((s) => s.unlock);
  const lock = useSecretsStore((s) => s.lock);
  // Single source of truth for loading + error UI state (see task #1324).
  // The store owns isProcessing (set by setup/unlock) and error (set on
  // failure by setup/unlock, or by setError for client-side validation).
  // There is NO local loading/error useState — that was a duplicate source
  // of truth that caused stale-value sync bugs.
  const isProcessing = useSecretsStore((s) => s.isProcessing);
  const error = useSecretsStore((s) => s.error);
  const clearError = useSecretsStore((s) => s.clearError);
  const setError = useSecretsStore((s) => s.setError);

  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");

  const isSetup = mode === "setup";

  // Reset all state every time the modal opens so we never show stale
  // errors or leftover password values from a previous attempt.
  useEffect(() => {
    if (open) {
      setPassword("");
      setConfirmPassword("");
      clearError();
    }
  }, [open, clearError]);

  function resetFields() {
    setPassword("");
    setConfirmPassword("");
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    clearError();

    if (!password) {
      setError("Please enter your master password.");
      return;
    }

    if (isSetup && password !== confirmPassword) {
      setError("Passwords do not match.");
      return;
    }

    try {
      if (isSetup) {
        await setup(password);
      } else {
        await unlock(password);
      }
      // Success — clear sensitive fields and close.
      resetFields();
      onClose();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      // The store already set its `error` field on failure, but we override
      // it here with a user-friendly message so the user sees actionable
      // copy rather than a raw status code. Both the store's set and this
      // setError write to the SAME field — single source of truth.
      if (/401|unauthorized|incorrect|invalid (master )?password/i.test(message)) {
        setError("Incorrect master password. Please try again.");
      } else {
        setError(message || "Something went wrong. Please try again.");
      }
    }
  }

  function handleLock() {
    lock();
    resetFields();
    onClose();
  }

  const title = isSetup
    ? "Set up your master password"
    : "Enter your master password";

  return (
    <Modal opened={open} onClose={onClose} title={title}>
      <form onSubmit={handleSubmit} className="space-y-4">
        <Input
          label="Master password"
          type="password"
          autoComplete={isSetup ? "new-password" : "current-password"}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          error={error ?? undefined}
          autoFocus
          disabled={isProcessing}
        />

        {isSetup && (
          <Input
            label="Confirm password"
            type="password"
            autoComplete="new-password"
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            disabled={isProcessing}
          />
        )}

        <div className="flex items-center justify-between gap-2 pt-1">
          {!isSetup && (
            <Button
              type="button"
              variant="secondary"
              onClick={handleLock}
              disabled={isProcessing}
              className="!border !border-gray-300 !bg-white !text-gray-700 hover:!bg-gray-50"
            >
              Lock
            </Button>
          )}
          <Button type="submit" loading={isProcessing} className="ml-auto">
            {isSetup ? "Set Up" : "Unlock"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
