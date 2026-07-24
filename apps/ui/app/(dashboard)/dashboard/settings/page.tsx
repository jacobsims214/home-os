"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import { useSecretsStore } from "@/stores/secrets";
import { Card, Switch, TextInput, Badge, Button, Group, Text, Stack, Alert, Paper, Anchor, Box } from "@mantine/core";
import { IconCheck, IconX, IconCopy, IconInfoCircle, IconLock, IconLockOpen } from "@tabler/icons-react";
import Modal from "@/components/ui/Modal";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import { showNotification, cleanNotifications } from "@mantine/notifications";

// ── Types ──────────────────────────────────────────────────────

type IntegrationType = "homeassistant";

interface Integration {
  type: IntegrationType;
  status: "connected" | "disconnected" | "error";
  last_health_check: string | null;
  last_sync: string | null;
  error_message: string | null;
}

interface TestResult {
  success: boolean;
  message: string;
}

// ── Integration metadata ───────────────────────────────────────

interface IntegrationMeta {
  name: string;
  description: string;
  icon: React.ReactNode;
}

const INTEGRATIONS: Record<IntegrationType, IntegrationMeta> = {
  homeassistant: {
    name: "Home Assistant",
    description: "Smart home automation",
    icon: (
      <svg className="h-8 w-8 text-indigo-500" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M2.25 12l8.954-8.955a1.126 1.126 0 011.591 0L21.75 12M4.5 9.75v10.125c0 .621.504 1.125 1.125 1.125H9.75v-4.875c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125V21h4.125c.621 0 1.125-.504 1.125-1.125V9.75M8.25 21h8.25" />
      </svg>
    ),
  },
};

// ── Config field definitions per integration type ──────────────

interface ConfigField {
  key: string;
  label: string;
  type: "text" | "password" | "toggle" | "select";
  placeholder?: string;
  defaultValue?: string;
  helpText?: string;
}

function getConfigFields(type: IntegrationType): ConfigField[] {
  switch (type) {
    case "homeassistant":
      return [
        { key: "base_url", label: "Base URL", type: "text", placeholder: "http://192.168.1.10:8123" },
        { key: "token", label: "Long-Lived Access Token", type: "password", placeholder: "From HA profile page" },
      ];
  }
}

// ── Component ──────────────────────────────────────────────────

export default function SettingsPage() {
  const queryClient = useQueryClient();
  const user = useAuthStore((s) => s.user);

  // Modal state
  const [connectType, setConnectType] = useState<IntegrationType | null>(null);
  const [testResult, setTestResult] = useState<TestResult | null>(null);
  const [testType, setTestType] = useState<IntegrationType | null>(null);
  const [disconnectType, setDisconnectType] = useState<IntegrationType | null>(null);

  // CalDAV password state
  const [caldavPassword, setCaldavPassword] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  // ── Fetch integrations ───────────────────────────────────────

  const {
    data: integrations = [],
    isLoading,
    isError,
    error,
  } = useQuery({
    queryKey: ["integrations"],
    queryFn: () =>
      apiFetch<{ data: Integration[] }>("/api/v1/integrations").then((r) => r.data),
    staleTime: 30_000,
  });

  // ── Helper: find integration by type ─────────────────────────
  function getIntegration(type: IntegrationType): Integration {
    return (
      integrations.find((i) => i.type === type) ?? {
        type,
        status: "disconnected",
        last_health_check: null,
        last_sync: null,
        error_message: null,
      }
    );
  }

  // ── Connect mutation ─────────────────────────────────────────

  const connectMutation = useMutation({
    mutationFn: ({
      type,
      config,
    }: {
      type: IntegrationType;
      config: Record<string, unknown>;
    }) =>
      apiFetch<void>(`/api/v1/integrations/${type}/connect`, {
        method: "POST",
        body: { config },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
      setConnectType(null);
      setFormError("");
    },
    onError: (e: unknown) => {
      setFormError(e instanceof Error ? e.message : "Failed to connect");
    },
  });

  // ── Test mutation ────────────────────────────────────────────

  const testMutation = useMutation({
    mutationFn: (type: IntegrationType) =>
      apiFetch<TestResult>(`/api/v1/integrations/${type}/test`, {
        method: "POST",
      }),
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
      setTestResult(data);
    },
    onError: (e: unknown) => {
      setTestResult({
        success: false,
        message: e instanceof Error ? e.message : "Test failed",
      });
    },
  });

  // ── Disconnect mutation ──────────────────────────────────────

  const disconnectMutation = useMutation({
    mutationFn: (type: IntegrationType) =>
      apiFetch<void>(`/api/v1/integrations/${type}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
      setDisconnectType(null);
    },
    onError: (e: unknown) => {
      // Keep dialog open so user can retry
      console.error("Disconnect failed", e);
    },
  });

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

  // ── Connect form state ───────────────────────────────────────

  const [formValues, setFormValues] = useState<Record<string, string>>({});
  const [formToggleValues, setFormToggleValues] = useState<Record<string, boolean>>({});
  const [formError, setFormError] = useState("");

  function resetForm() {
    setFormValues({});
    setFormToggleValues({});
    setFormError("");
  }

  function openConnectModal(type: IntegrationType) {
    resetForm();
    setConnectType(type);
  }

  function handleFieldChange(key: string, value: string) {
    setFormValues((prev) => ({ ...prev, [key]: value }));
  }

  function handleToggleChange(key: string, checked: boolean) {
    setFormToggleValues((prev) => ({ ...prev, [key]: checked }));
  }

  function handleConnectSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!connectType) return;
    setFormError("");

    // Build config from form values
    const config: Record<string, unknown> = { ...formValues };

    // Add toggle values
    const fields = getConfigFields(connectType);
    for (const field of fields) {
      if (field.type === "toggle") {
        config[field.key] = formToggleValues[field.key] ?? false;
      } else if (field.defaultValue && !config[field.key]) {
        config[field.key] = field.defaultValue;
      }
    }

    connectMutation.mutate({ type: connectType, config });
  }

  function handleTest(type: IntegrationType) {
    setTestResult(null);
    setTestType(type);
    testMutation.mutate(type);
  }

  function handleDisconnect(type: IntegrationType) {
    disconnectMutation.mutate(type);
  }

  // ── Helpers ──────────────────────────────────────────────────

  function statusBadge(integration: Integration) {
    if (integration.status === "connected") {
      return <Badge color="green" leftSection={<span className="h-2 w-2 rounded-full bg-green-500" />}>Connected</Badge>;
    }
    if (integration.status === "error") {
      return <Badge color="red" leftSection={<span className="h-2 w-2 rounded-full bg-red-500" />}>Error</Badge>;
    }
    return <Badge color="gray" leftSection={<span className="h-2 w-2 rounded-full bg-gray-400" />}>Not configured</Badge>;
  }

  function formatTimestamp(ts: string | null): string {
    if (!ts) return "Never";
    try {
      const d = new Date(ts);
      const now = new Date();
      const diffMs = now.getTime() - d.getTime();
      const diffMin = Math.floor(diffMs / 60000);
      if (diffMin < 1) return "Just now";
      if (diffMin < 60) return `${diffMin} minute${diffMin === 1 ? "" : "s"} ago`;
      const diffHrs = Math.floor(diffMin / 60);
      if (diffHrs < 24) return `${diffHrs} hour${diffHrs === 1 ? "" : "s"} ago`;
      return d.toLocaleDateString("en-US", { month: "short", day: "numeric" });
    } catch {
      return "Unknown";
    }
  }

  // ── Render ───────────────────────────────────────────────────

  const integrationTypes: IntegrationType[] = [
    "homeassistant",
  ];

  return (
    <div className="mx-auto max-w-3xl px-4 py-8 sm:px-6 lg:px-8">
      <div className="mb-8">
        <h1 className="text-2xl font-bold text-gray-900">Settings</h1>
        <p className="mt-1 text-sm text-gray-500">
          Connect and manage your self-hosted services.
        </p>
        <Card withBorder mt={8}>
          <Text size="sm" c="dimmed">
            <IconInfoCircle size={16} style={{ display: "inline", marginRight: 4 }} />
            Integrations are <strong>household-wide</strong> — one connection shared across all your properties.
          </Text>
        </Card>
      </div>

      {/* ── Loading state ───────────────────────────────────── */}

      {isLoading && (
        <Stack>
          {[1, 2, 3, 4].map((i) => (
            <Card key={i} withBorder>
              <Group>
                <Box w={40} h={40} style={{ backgroundColor: "var(--mantine-color-gray-2)", borderRadius: 8 }} />
                <Stack gap={4} style={{ flex: 1 }}>
                  <Box h={16} style={{ backgroundColor: "var(--mantine-color-gray-2)", borderRadius: 8 }} />
                  <Box h={12} style={{ backgroundColor: "var(--mantine-color-gray-2)", borderRadius: 8 }} />
                </Stack>
                <Box w={96} h={24} style={{ backgroundColor: "var(--mantine-color-gray-2)", borderRadius: 24 }} />
              </Group>
            </Card>
          ))}
        </Stack>
      )}

      {/* ── Error state ─────────────────────────────────────── */}

      {isError && (
        <Alert color="red" title="Failed to load integrations" icon={<IconX size={16} />}>
          {error instanceof Error ? error.message : "An unexpected error occurred."}
          <Button
            variant="subtle"
            size="xs"
            mt={8}
            onClick={() => queryClient.invalidateQueries({ queryKey: ["integrations"] })}
          >
            Try again
          </Button>
        </Alert>
      )}

      {/* ── Integration cards ───────────────────────────────── */}

      {!isLoading && !isError && (
        <Stack>
          {integrationTypes.map((type) => {
            const integration = getIntegration(type);
            const meta = INTEGRATIONS[type];
            const isConnected = integration.status === "connected";

            return (
              <Card key={type} withBorder shadow="sm">
                <Stack gap="md">
                  {/* Header row */}
                  <Group>
                    <Box
                      w={48}
                      h={48}
                      style={{ backgroundColor: "var(--mantine-color-indigo-5)", display: "flex", alignItems: "center", justifyContent: "center", borderRadius: 8 }}
                    >
                      {meta.icon}
                    </Box>
                    <Stack gap={4} style={{ flex: 1 }}>
                      <Group>
                        <Text fw={600}>{meta.name}</Text>
                        {statusBadge(integration)}
                      </Group>
                      <Text size="sm" c="dimmed">
                        {meta.description}
                      </Text>
                      {isConnected && (
                        <Text size="xs" c="gray.5">
                          Last tested: {formatTimestamp(integration.last_health_check)}
                        </Text>
                      )}
                    </Stack>
                  </Group>

                   {/* Action buttons */}
                   <Group wrap="wrap" pt="md" style={{ borderTop: "1px solid #f1f1f1", paddingTop: 16 }}>
                    {isConnected ? (
                      <>
                        <Button
                          onClick={() => handleTest(type)}
                          loading={testMutation.isPending && testType === type}
                          variant="outline"
                        >
                          Test Connection
                        </Button>
                        <Button
                          color="red"
                          onClick={() => setDisconnectType(type)}
                          variant="light"
                        >
                          Disconnect
                        </Button>
                      </>
                    ) : (
                      <Button onClick={() => openConnectModal(type)}>Connect</Button>
                    )}
                  </Group>

                  {/* Test result */}
                  {testResult && testType === type && (
                    <Alert
                      color={testResult.success ? "green" : "red"}
                      title={testResult.success ? "Success" : "Error"}
                      icon={testResult.success ? <IconCheck size={16} /> : <IconX size={16} />}
                    >
                      {testResult.message}
                    </Alert>
                  )}

                  {/* Error message */}
                  {integration.status === "error" && integration.error_message && (
                    <Alert color="red" title="Connection Error" icon={<IconX size={16} />}>
                      {integration.error_message}
                    </Alert>
                  )}
                </Stack>
              </Card>
            );
          })}
        </Stack>
      )}

      {/* ── Calendar Sync Section ────────────────────────────── */}

      <div className="mt-8">
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

      {/* ── Connect modal ────────────────────────────────────── */}

      {connectType && (
        <Modal
          opened={!!connectType}
          onClose={() => {
            setConnectType(null);
            resetForm();
          }}
          title={`Connect ${INTEGRATIONS[connectType].name}`}
          size="lg"
        >
          <Stack gap="md">
            <form onSubmit={handleConnectSubmit} style={{ display: "flex", flexDirection: "column", gap: 16 }}>
              {getConfigFields(connectType).map((field) => {
                if (field.type === "toggle") {
                  return (
                    <Switch
                      key={field.key}
                      label={field.label}
                      checked={formToggleValues[field.key] ?? false}
                      onChange={(e) => handleToggleChange(field.key, e.target.checked)}
                    />
                  );
                }
                return (
                  <TextInput
                    key={field.key}
                    label={field.label}
                    type={field.type}
                    placeholder={field.placeholder}
                    value={formValues[field.key] ?? ""}
                    onChange={(e) => handleFieldChange(field.key, e.target.value)}
                  />
                );
              })}

              {formError && (
                <Alert color="red" title="Error" icon={<IconX size={16} />}>
                  {formError}
                </Alert>
              )}

              <Group justify="flex-end" mt="md">
                <Button
                  variant="subtle"
                  onClick={() => {
                    setConnectType(null);
                    resetForm();
                  }}
                >
                  Cancel
                </Button>
                <Button type="submit" loading={connectMutation.isPending}>
                  Connect
                </Button>
              </Group>
            </form>
          </Stack>
        </Modal>
      )}

      {/* ── Disconnect confirmation ──────────────────────────── */}

      <ConfirmDialog
        open={!!disconnectType}
        onClose={() => setDisconnectType(null)}
        onConfirm={() => {
          if (disconnectType) handleDisconnect(disconnectType);
        }}
        title="Disconnect Integration"
        message={`Are you sure you want to disconnect ${disconnectType ? INTEGRATIONS[disconnectType].name : "this integration"}? The configuration will be removed.`}
        confirmLabel="Disconnect"
        loading={disconnectMutation.isPending}
      />

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

  // Check if secrets key exists
  const { data: keyInfo, refetch } = useQuery({
    queryKey: ["secrets", "key-info"],
    queryFn: async () => {
      try {
        const resp = await apiFetch<{ data: { key_salt: string; key_version: number } | null }>("/api/v1/secrets/key");
        return resp.data;
      } catch {
        return null;
      }
    },
  });

  const hasKey = keyInfo != null;

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
      await setup(password);
    } else {
      await unlock(password);
    }

    if (!useSecretsStore.getState().error) {
      setShowPrompt(false);
      setPassword("");
      setConfirmPassword("");
      refetch();
    }
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
