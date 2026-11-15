"use client";

import { useState } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import { fetchKeyInfo, useSecretsStore } from "@/stores/secrets";
import { Card, TextInput, Badge, Button, Group, Text, Stack, Alert } from "@mantine/core";
import { IconCheck, IconX, IconCopy, IconLock, IconLockOpen } from "@tabler/icons-react";
import Modal from "@/components/ui/Modal";

// External service integrations (Home Assistant) were removed with the
// integrations table (migration 056, #4601): Home OS keeps files, secrets and
// calendars natively.

// ── Component ──────────────────────────────────────────────────

export default function SettingsPage() {
  const user = useAuthStore((s) => s.user);

  // CalDAV password state
  const [caldavPassword, setCaldavPassword] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  // ── CalDAV password mutation ──────────────────────────────────

  const caldavPasswordMutation = useMutation({
    mutationFn: () =>
      apiFetch<{ password: string }>("/api/v1/auth/caldav-password", {
        method: "POST",
      }),
    onSuccess: (data) => {
      setCaldavPassword(data.password);
      setCopied(false);
    },
    onError: (e: unknown) => {
      console.error("CalDAV password generation failed", e);
    },
  });

  function handleCopyPassword() {
    if (caldavPassword) {
      navigator.clipboard.writeText(caldavPassword);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }

  // ── Render ───────────────────────────────────────────────────

  return (
    <div className="mx-auto max-w-3xl px-4 py-8 sm:px-6 lg:px-8">
      <div className="mb-8">
        <h1 className="text-2xl font-bold text-gray-900">Settings</h1>
        <p className="mt-1 text-sm text-gray-500">
          Calendar sync and your family&apos;s secrets.
        </p>
      </div>

      {/* ── Calendar Sync Section ────────────────────────────── */}

      <div>
        <h2 className="text-xl font-bold text-gray-900">Calendar Sync</h2>
        <p className="mt-1 text-sm text-gray-500">
          Sync your Home OS calendars with Apple Calendar, iPhone, or any CalDAV-compatible app.
        </p>

        <Card withBorder mt={16}>
          <Stack gap="md">
            {/* Server info */}
            <Stack gap={4}>
              <Group justify="space-between">
                <Text size="sm" fw={500}>Server</Text>
                <Text size="sm" ff="monospace">http://localhost:8081</Text>
              </Group>
              <Group justify="space-between">
                <Text size="sm" fw={500}>Username</Text>
                <Text size="sm" ff="monospace">{user?.email ?? "..."}</Text>
              </Group>
              <Group justify="space-between">
                <Text size="sm" fw={500}>Password</Text>
                <Group gap={8}>
                  <Text size="sm" ff="monospace">
                    {caldavPassword ? (
                      <span>{caldavPassword}</span>
                    ) : (
                      <span className="text-gray-400">Not generated</span>
                    )}
                  </Text>
                  {caldavPassword && (
                    <Button
                      size="xs"
                      variant="subtle"
                      onClick={handleCopyPassword}
                      leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
                    >
                      {copied ? "Copied" : "Copy"}
                    </Button>
                  )}
                </Group>
              </Group>
            </Stack>

            {/* Generate button */}
            <Group wrap="wrap">
              <Button
                onClick={() => caldavPasswordMutation.mutate()}
                loading={caldavPasswordMutation.isPending}
              >
                Generate App Password
              </Button>
              {caldavPassword && (
                <Text size="xs" c="orange">
                  This password will never be shown again. Copy it now.
                </Text>
              )}
            </Group>

            {/* Setup instructions */}
            <Card withBorder style={{ backgroundColor: "var(--mantine-color-blue-0)" }}>
              <Text fw={600} mb={8}>Apple Calendar Setup</Text>
              <Stack gap={4}>
                <Text>1. Open <strong>Settings</strong> on your iPhone or Mac</Text>
                <Text>2. Go to <strong>Calendar</strong> → <strong>Accounts</strong> → <strong>Add Account</strong></Text>
                <Text>3. Select <strong>Other</strong> → <strong>Add CalDAV Account</strong></Text>
                <Text>4. Enter the Server, Username, and Password shown above</Text>
                <Text>5. Your Home OS calendars will sync automatically</Text>
              </Stack>
            </Card>

            {/* Error state */}
            {caldavPasswordMutation.isError && (
              <Alert color="red" title="Error" icon={<IconX size={16} />}>
                Failed to generate password. Please try again.
              </Alert>
            )}
          </Stack>
        </Card>
      </div>

      {/* ── Secrets Manager Section ──────────────────────────── */}

      <Card withBorder mt={32}>
        <Stack gap="md">
          <Text fw={600}>Secrets Manager</Text>
          <Text size="sm" c="dimmed">
            Store passwords, API keys, and credit cards securely. All secrets are encrypted in your browser with AES-256-GCM before being sent to the server.
          </Text>

          <SecretsStatus />
        </Stack>
      </Card>
    </div>
  );
}

// ── Secrets status sub-component ───────────────────────────────

function SecretsStatus() {
  const { isUnlocked, setup, unlock, lock, isProcessing, error, clearError } = useSecretsStore();
  const [showPrompt, setShowPrompt] = useState(false);
  const [mode, setMode] = useState<"setup" | "unlock">("unlock");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [localError, setLocalError] = useState<string | null>(null);

  // Is a master password set up? fetchKeyInfo returns null only on a 404 from
  // GET /secrets/key; other errors leave keyInfo undefined, and we show the
  // unlock flow (whose errors surface) rather than offer a setup that fails.
  const { data: keyInfo, refetch } = useQuery({
    queryKey: ["secrets", "key-info"],
    queryFn: fetchKeyInfo,
  });

  const hasKey = keyInfo !== null;

  const handleOpen = () => {
    setLocalError(null);
    clearError();
    setMode(hasKey ? "unlock" : "setup");
    setPassword("");
    setConfirmPassword("");
    setShowPrompt(true);
  };

  const handleSubmit = async () => {
    setLocalError(null);
    clearError();

    if (!password) {
      setLocalError("Password is required");
      return;
    }

    if (mode === "setup") {
      if (password !== confirmPassword) {
        setLocalError("Passwords do not match");
        return;
      }
      if (password.length < 8) {
        setLocalError("Master password must be at least 8 characters");
        return;
      }
    }
    try {
      if (mode === "setup") await setup(password);
      else await unlock(password);
    } catch {
      // The store has set `error`, which the modal shows.
      return;
    }

    setShowPrompt(false);
    setPassword("");
    setConfirmPassword("");
    refetch();
  };

  if (isUnlocked) {
    return (
      <Stack gap="md">
        <Group>
          <Badge color="green" leftSection={<IconLockOpen size={14} />}>Unlocked</Badge>
          <Text size="sm" c="dimmed">Your secrets are decrypted in memory. Lock to clear the key.</Text>
        </Group>
        <Button variant="outline" onClick={() => lock()}>Lock Secrets</Button>
      </Stack>
    );
  }

  return (
    <Stack gap="md">
      <Group>
        <Badge color="gray" leftSection={<IconLock size={14} />}>
          {hasKey ? "Locked" : "Not Set Up"}
        </Badge>
        <Text size="sm" c="dimmed">
          {hasKey
            ? "Enter your master password to unlock and view your secrets."
            : "Set up a master password to start storing encrypted secrets."}
        </Text>
      </Group>
      <Button onClick={handleOpen}>
        {hasKey ? "Unlock Secrets" : "Set Up Master Password"}
      </Button>

        <Modal opened={showPrompt} onClose={() => setShowPrompt(false)} title={mode === "setup" ? "Set Up Master Password" : "Unlock Secrets"} size="lg">
        <Stack gap="md">
          <TextInput
            label="Master Password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Enter master password"
          />
          {mode === "setup" && (
            <TextInput
              label="Confirm Password"
              type="password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              placeholder="Re-enter master password"
            />
          )}
          {(localError || error) && (
            <Alert color="red" title="Error" icon={<IconX size={16} />}>
              {localError || error}
            </Alert>
          )}
          <Group justify="flex-end">
            <Button variant="subtle" onClick={() => setShowPrompt(false)}>Cancel</Button>
            <Button loading={isProcessing} onClick={handleSubmit}>
              {mode === "setup" ? "Set Up" : "Unlock"}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}