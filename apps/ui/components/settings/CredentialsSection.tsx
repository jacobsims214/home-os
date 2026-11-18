"use client";

/**
 * Settings -> Credentials (#4560): the secrets vault's entry point.
 *
 * MOVED, not rebuilt. This is the Secrets Manager panel that lived on the
 * one-page Settings screen, with the same lock lifecycle, the same copy and the
 * same requests (#4445): GET /secrets/key decides "Set Up" vs "Unlock", setup
 * POSTs {verifier, key_salt, key_version}, unlock POSTs {verifier, key_version},
 * and the key only ever lives in memory. The entity pages' Secrets tab keeps
 * using the store directly (components/secrets/SecretsSection.tsx), so there is
 * still exactly one implementation of the vault.
 *
 * Only the family roles see the section at all (settings-sections.ts, the API's
 * ResSecrets grant). A credential belongs to something — a property's Wi-Fi, a
 * bill's login — so the list itself stays on those records; this page is where a
 * person sets up, unlocks and locks the vault.
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Alert, Badge, Button, Group, Stack, Text, TextInput } from "@mantine/core";
import { IconCheck, IconKey, IconLock, IconLockOpen, IconX } from "@tabler/icons-react";
import Modal from "@/components/ui/Modal";
import { SectionHeader, SettingsCard } from "./SettingsFrame";
import { fetchKeyInfo, useSecretsStore } from "@/stores/secrets";

export default function CredentialsSection() {
  return (
    <>
      <SectionHeader
        title="Credentials"
        description="Family logins and codes, locked with the family master password. Home OS can't read them without it."
      />
      <SettingsCard
        title="Secrets Manager"
        description="Passwords, API keys and cards are encrypted in your browser with AES-256-GCM before they are sent to the server."
      >
        <SecretsStatus />
      </SettingsCard>
      <SettingsCard title="Where credentials live">
        <Alert
          color="blue"
          variant="light"
          icon={<IconKey size={16} />}
          title="Open a record to see its credentials"
        >
          A credential belongs to something — the Wi-Fi on a property, a login on a bill, a code on a
          vehicle. Open it from People &amp; House or Money and use its Secrets tab.
        </Alert>
      </SettingsCard>
    </>
  );
}

// ── Secrets status sub-component (moved from settings/page.tsx) ──────────────

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
          <Badge color="green" leftSection={<IconLockOpen size={14} />}>
            Unlocked
          </Badge>
          <Text size="sm" c="var(--ink-3)">
            Your secrets are decrypted in memory. Lock to clear the key.
          </Text>
        </Group>
        <Button variant="outline" onClick={() => lock()}>
          Lock Secrets
        </Button>
      </Stack>
    );
  }

  return (
    <Stack gap="md">
      <Group>
        <Badge color="gray" leftSection={<IconLock size={14} />}>
          {hasKey ? "Locked" : "Not Set Up"}
        </Badge>
        <Text size="sm" c="var(--ink-3)">
          {hasKey
            ? "Enter your master password to unlock and view your secrets."
            : "Set up a master password to start storing encrypted secrets."}
        </Text>
      </Group>
      <Button onClick={handleOpen}>{hasKey ? "Unlock Secrets" : "Set Up Master Password"}</Button>

      {/* The dialog the vault has always used, unchanged (#4560 is a move, not
          a redesign): the same labels, the same store calls, and the same
          Modal the other non-form prompts use. */}
      <Modal
        opened={showPrompt}
        onClose={() => setShowPrompt(false)}
        title={mode === "setup" ? "Set Up Master Password" : "Unlock Secrets"}
        size="lg"
      >
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
            <Button variant="subtle" onClick={() => setShowPrompt(false)}>
              Cancel
            </Button>
            <Button loading={isProcessing} onClick={handleSubmit}>
              {mode === "setup" ? "Set Up" : "Unlock"}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
