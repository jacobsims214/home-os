"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import { useSecretsStore } from "@/stores/secrets";
import Modal from "@/components/ui/Modal";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";
import ConfirmDialog from "@/components/ui/ConfirmDialog";

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
      return (
        <span className="inline-flex items-center gap-1 rounded-full bg-green-50 px-2.5 py-0.5 text-xs font-medium text-green-700">
          <span className="h-1.5 w-1.5 rounded-full bg-green-500" />
          Connected
        </span>
      );
    }
    if (integration.status === "error") {
      return (
        <span className="inline-flex items-center gap-1 rounded-full bg-red-50 px-2.5 py-0.5 text-xs font-medium text-red-700">
          <span className="h-1.5 w-1.5 rounded-full bg-red-500" />
          Error
        </span>
      );
    }
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-gray-50 px-2.5 py-0.5 text-xs font-medium text-gray-600">
        <span className="h-1.5 w-1.5 rounded-full bg-gray-400" />
        Not configured
      </span>
    );
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
        <div className="mt-2 rounded-md bg-purple-50 border border-purple-200 px-4 py-2">
          <p className="text-xs text-purple-700">
            Integrations are <strong>household-wide</strong> — one connection shared across all your properties.
          </p>
        </div>
      </div>

      {/* ── Loading state ───────────────────────────────────── */}

      {isLoading && (
        <div className="space-y-4">
          {[1, 2, 3, 4].map((i) => (
            <div
              key={i}
              className="animate-pulse rounded-lg border border-gray-200 bg-white p-6"
            >
              <div className="flex items-center gap-4">
                <div className="h-10 w-10 rounded-lg bg-gray-200" />
                <div className="flex-1 space-y-2">
                  <div className="h-4 w-32 rounded bg-gray-200" />
                  <div className="h-3 w-48 rounded bg-gray-200" />
                </div>
                <div className="h-6 w-24 rounded-full bg-gray-200" />
              </div>
            </div>
          ))}
        </div>
      )}

      {/* ── Error state ─────────────────────────────────────── */}

      {isError && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-4">
          <div className="flex items-start gap-3">
            <svg className="mt-0.5 h-5 w-5 shrink-0 text-red-500" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126zM12 15.75h.007v.008H12v-.008z" />
            </svg>
            <div className="flex-1">
              <h3 className="text-sm font-medium text-red-800">
                Failed to load integrations
              </h3>
              <p className="mt-1 text-sm text-red-700">
                {error instanceof Error ? error.message : "An unexpected error occurred."}
              </p>
              <button
                onClick={() => queryClient.invalidateQueries({ queryKey: ["integrations"] })}
                className="mt-3 text-sm font-medium text-red-800 underline hover:text-red-900"
              >
                Try again
              </button>
            </div>
          </div>
        </div>
      )}

      {/* ── Integration cards ───────────────────────────────── */}

      {!isLoading && !isError && (
        <div className="space-y-4">
          {integrationTypes.map((type) => {
            const integration = getIntegration(type);
            const meta = INTEGRATIONS[type];
            const isConnected = integration.status === "connected";

            return (
              <div
                key={type}
                className="rounded-lg border border-gray-200 bg-white shadow-sm"
              >
                <div className="p-6">
                  {/* Header row */}
                  <div className="flex items-start justify-between gap-4">
                    <div className="flex items-start gap-4">
                      <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-lg bg-indigo-50">
                        {meta.icon}
                      </div>
                      <div>
                        <div className="flex items-center gap-3">
                          <h3 className="text-base font-semibold text-gray-900">
                            {meta.name}
                          </h3>
                          {statusBadge(integration)}
                        </div>
                        <p className="mt-0.5 text-sm text-gray-500">
                          {meta.description}
                        </p>
                        {isConnected && (
                          <p className="mt-0.5 text-xs text-gray-400">
                            Last tested: {formatTimestamp(integration.last_health_check)}
                          </p>
                        )}
                      </div>
                    </div>
                  </div>

                  {/* Action buttons */}
                  <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-gray-100 pt-4">
                    {isConnected ? (
                      <>
                        <Button
                          onClick={() => handleTest(type)}
                          loading={testMutation.isPending && testType === type}
                          variant="secondary"
                          className="!border !border-gray-300 !bg-white !text-gray-700 hover:!bg-gray-50"
                        >
                          Test Connection
                        </Button>
                        <button
                          type="button"
                          onClick={() => setDisconnectType(type)}
                          className="inline-flex items-center justify-center rounded-md border border-red-300 bg-white px-4 py-2 text-sm font-semibold text-red-600 hover:bg-red-50"
                        >
                          Disconnect
                        </button>
                      </>
                    ) : (
                      <Button onClick={() => openConnectModal(type)}>
                        Connect
                      </Button>
                    )}
                  </div>

                  {/* Test result */}
                  {testResult && testType === type && (
                    <div
                      className={`mt-3 rounded-md px-3 py-2 text-sm ${
                        testResult.success
                          ? "bg-green-50 text-green-700"
                          : "bg-red-50 text-red-700"
                      }`}
                    >
                      <div className="flex items-center gap-2">
                        {testResult.success ? (
                          <svg className="h-4 w-4 shrink-0" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
                            <path strokeLinecap="round" strokeLinejoin="round" d="M4.5 12.75l6 6 9-13.5" />
                          </svg>
                        ) : (
                          <svg className="h-4 w-4 shrink-0" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
                            <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                          </svg>
                        )}
                        {testResult.message}
                      </div>
                    </div>
                  )}

                  {/* Error message */}
                  {integration.status === "error" && integration.error_message && (
                    <div className="mt-3 rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">
                      {integration.error_message}
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}

      {/* ── Calendar Sync Section ────────────────────────────── */}

      <div className="mt-8">
        <h2 className="text-xl font-bold text-gray-900">Calendar Sync</h2>
        <p className="mt-1 text-sm text-gray-500">
          Sync your Home OS calendars with Apple Calendar, iPhone, or any CalDAV-compatible app.
        </p>

        <div className="mt-4 rounded-lg border border-gray-200 bg-white shadow-sm">
          <div className="p-6">
            {/* Server info */}
            <div className="space-y-3">
              <div className="flex items-center justify-between rounded-md bg-gray-50 px-4 py-3">
                <span className="text-sm font-medium text-gray-700">Server</span>
                <span className="text-sm text-gray-900 font-mono">http://localhost:8081</span>
              </div>
              <div className="flex items-center justify-between rounded-md bg-gray-50 px-4 py-3">
                <span className="text-sm font-medium text-gray-700">Username</span>
                <span className="text-sm text-gray-900 font-mono">{user?.email ?? "..."}</span>
              </div>
              <div className="flex items-center justify-between rounded-md bg-gray-50 px-4 py-3">
                <span className="text-sm font-medium text-gray-700">Password</span>
                <span className="text-sm text-gray-900 font-mono">
                  {caldavPassword ? (
                    <span className="inline-flex items-center gap-2">
                      <span className="tracking-wider">{caldavPassword}</span>
                      <button
                        type="button"
                        onClick={handleCopyPassword}
                        className="inline-flex items-center gap-1 rounded-md bg-indigo-50 px-2 py-1 text-xs font-medium text-indigo-700 hover:bg-indigo-100"
                      >
                        {copied ? (
                          <>
                            <svg className="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
                              <path strokeLinecap="round" strokeLinejoin="round" d="M4.5 12.75l6 6 9-13.5" />
                            </svg>
                            Copied
                          </>
                        ) : (
                          <>
                            <svg className="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                              <path strokeLinecap="round" strokeLinejoin="round" d="M15.666 3.888A2.25 2.25 0 0013.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 01-.75.75H9a.75.75 0 01-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 01-2.25 2.25H6.75A2.25 2.25 0 014.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 011.927-.184" />
                            </svg>
                            Copy
                          </>
                        )}
                      </button>
                    </span>
                  ) : (
                    <span className="text-gray-400">Not generated</span>
                  )}
                </span>
              </div>
            </div>

            {/* Generate button */}
            <div className="mt-4 flex items-center gap-3">
              <Button
                onClick={() => caldavPasswordMutation.mutate()}
                loading={caldavPasswordMutation.isPending}
              >
                Generate App Password
              </Button>
              {caldavPassword && (
                <p className="text-xs text-amber-600">
                  This password will never be shown again. Copy it now.
                </p>
              )}
            </div>

            {/* Setup instructions */}
            <div className="mt-6 rounded-md bg-blue-50 border border-blue-200 px-4 py-3">
              <h4 className="text-sm font-semibold text-blue-800">
                Apple Calendar Setup
              </h4>
              <ol className="mt-2 list-decimal space-y-1 pl-5 text-sm text-blue-700">
                <li>Open <strong>Settings</strong> on your iPhone or Mac</li>
                <li>Go to <strong>Calendar</strong> → <strong>Accounts</strong> → <strong>Add Account</strong></li>
                <li>Select <strong>Other</strong> → <strong>Add CalDAV Account</strong></li>
                <li>Enter the Server, Username, and Password shown above</li>
                <li>Your Home OS calendars will sync automatically</li>
              </ol>
            </div>

            {/* Error state */}
            {caldavPasswordMutation.isError && (
              <div className="mt-4 rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">
                Failed to generate password. Please try again.
              </div>
            )}
          </div>
        </div>
      </div>

      {/* ── Connect modal ────────────────────────────────────── */}

      {connectType && (
        <Modal
          open={!!connectType}
          onClose={() => {
            setConnectType(null);
            resetForm();
          }}
          title={`Connect ${INTEGRATIONS[connectType].name}`}
          maxWidth="max-w-lg"
        >
          <form onSubmit={handleConnectSubmit} className="space-y-4">
            {getConfigFields(connectType).map((field) => {
              if (field.type === "toggle") {
                return (
                  <label
                    key={field.key}
                    className="flex items-center gap-3 text-sm font-medium text-gray-900"
                  >
                    <input
                      type="checkbox"
                      checked={formToggleValues[field.key] ?? false}
                      onChange={(e) => handleToggleChange(field.key, e.target.checked)}
                      className="h-4 w-4 rounded border-gray-300 text-indigo-600 focus:ring-indigo-600"
                    />
                    {field.label}
                  </label>
                );
              }
              return (
                <Input
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
              <p className="text-sm text-red-600">{formError}</p>
            )}

            <div className="flex justify-end gap-3 pt-2">
              <button
                type="button"
                onClick={() => {
                  setConnectType(null);
                  resetForm();
                }}
                className="inline-flex items-center justify-center rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-semibold text-gray-700 hover:bg-gray-50"
              >
                Cancel
              </button>
              <Button type="submit" loading={connectMutation.isPending}>
                Connect
              </Button>
            </div>
          </form>
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

      <div className="mt-8 rounded-lg border border-gray-200 bg-white p-6">
        <h2 className="text-lg font-semibold text-gray-900">Secrets Manager</h2>
        <p className="mt-1 text-sm text-gray-500">
          Store passwords, API keys, and credit cards securely. All secrets are encrypted in your browser with AES-256-GCM before being sent to the server.
        </p>

        <SecretsStatus />
      </div>
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
      <div className="mt-4">
        <div className="flex items-center gap-3">
          <span className="inline-flex items-center rounded-full bg-green-100 px-2.5 py-0.5 text-xs font-medium text-green-800">
            Unlocked
          </span>
          <p className="text-sm text-gray-500">Your secrets are decrypted in memory. Lock to clear the key.</p>
        </div>
        <div className="mt-3">
          <Button variant="secondary" onClick={() => lock()}>Lock Secrets</Button>
        </div>
      </div>
    );
  }

  return (
    <div className="mt-4">
      <div className="flex items-center gap-3">
        <span className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-600">
          {hasKey ? "Locked" : "Not Set Up"}
        </span>
        <p className="text-sm text-gray-500">
          {hasKey
            ? "Enter your master password to unlock and view your secrets."
            : "Set up a master password to start storing encrypted secrets."}
        </p>
      </div>
      <div className="mt-3">
        <Button onClick={handleOpen}>
          {hasKey ? "Unlock Secrets" : "Set Up Master Password"}
        </Button>
      </div>

      <Modal open={showPrompt} onClose={() => setShowPrompt(false)} title={mode === "setup" ? "Set Up Master Password" : "Unlock Secrets"}>
        <div className="space-y-4">
          <Input
            label="Master Password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Enter master password"
          />
          {mode === "setup" && (
            <Input
              label="Confirm Password"
              type="password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              placeholder="Re-enter master password"
            />
          )}
          {(localError || error) && (
            <p className="text-sm text-red-600">{localError || error}</p>
          )}
          <div className="flex justify-end gap-3">
            <Button variant="secondary" onClick={() => setShowPrompt(false)}>Cancel</Button>
            <Button loading={isProcessing} onClick={handleSubmit}>
              {mode === "setup" ? "Set Up" : "Unlock"}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
