"use client";

/**
 * SecretEditor — modal form for creating and editing native Home OS secrets.
 *
 * Zero-knowledge flow (see architecture/secrets-manager-research.md):
 *  1. User picks a secret_type (login / note / api_key / card) and fills in
 *     type-specific fields plus a plaintext `name` (stored unencrypted so
 *     listings are searchable).
 *  2. On submit the type-specific fields are collected into a plain JSON
 *     object, JSON.stringified, and encrypted in the browser with the
 *     in-memory CryptoKey from the secrets store (AES-256-GCM).
 *  3. Only `{ encrypted_data, iv, key_version, name, secret_type,
 *     entity_type, entity_id }` is POSTed — the API never sees plaintext.
 *
 * Edit mode (`editSecretId` prop): when the modal opens it fetches the
 * existing secret via GET /api/v1/secrets/:id, decrypts the blob, parses the
 * JSON, and repopulates the form. On save the (possibly edited) fields are
 * re-encrypted with a fresh IV and PATCHed to /api/v1/secrets/:id.
 *
 * The component depends on apps/ui/lib/crypto.ts (encrypt/decrypt) and
 * apps/ui/stores/secrets.ts (cryptoKey + keyVersion). If the vault is
 * locked (no cryptoKey) it renders a locked notice instead of the form —
 * the parent SecretsSection is responsible for showing MasterPasswordPrompt.
 */
import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { encrypt, decrypt } from "@/lib/crypto";
import { useSecretsStore } from "@/stores/secrets";
import { secretKeys } from "@/lib/query-keys";
import Modal from "@/components/ui/Modal";
import Input from "@/components/ui/Input";
import Select from "@/components/ui/Select";
import Button from "@/components/ui/Button";
import type {
  Secret,
  SecretType,
  CreateSecretRequest,
  UpdateSecretRequest,
} from "@/lib/types/api";

// ─── Constants ────────────────────────────────────────────────

const SECRET_TYPE_OPTIONS: { value: SecretType; label: string }[] = [
  { value: "login", label: "Login" },
  { value: "note", label: "Secure Note" },
  { value: "api_key", label: "API Key" },
  { value: "card", label: "Credit Card" },
];

/**
 * Empty form state per secret type. The editor keeps a single object
 * containing every possible field; only the ones for the active type are
 * rendered and submitted. This keeps the type-switching logic simple
 * (no nested objects, no field migration when the user changes the type).
 */
function emptyFields(): SecretFormFields {
  return {
    name: "",
    // login
    username: "",
    password: "",
    url: "",
    // note
    content: "",
    // api_key
    key_name: "",
    key_value: "",
    // card
    cardholder: "",
    number: "",
    expiry: "",
    cvv: "",
  };
}

interface SecretFormFields {
  name: string;
  // login
  username: string;
  password: string;
  url: string;
  // note
  content: string;
  // api_key (key_value reuses `password`? no — distinct field)
  key_name: string;
  key_value: string;
  // card
  cardholder: string;
  number: string;
  expiry: string;
  cvv: string;
}

// ─── Props ────────────────────────────────────────────────────

interface SecretEditorProps {
  open: boolean;
  onClose: () => void;
  entityType: string;
  entityId: string;
  /** When provided, the editor opens in edit mode for this secret id. */
  editSecretId?: string | null;
}

// ─── Helpers ──────────────────────────────────────────────────

/**
 * Build the type-specific plaintext payload object from the form fields.
 * Only the fields relevant to the active secret_type are included — the
 * server never stores unrelated keys. This object is JSON.stringified and
 * encrypted before being sent.
 */
function buildPayload(
  type: SecretType,
  f: SecretFormFields,
): Record<string, string> {
  switch (type) {
    case "login":
      return {
        username: f.username,
        password: f.password,
        url: f.url,
      };
    case "note":
      return { content: f.content };
    case "api_key":
      return {
        key_name: f.key_name,
        key_value: f.key_value,
        url: f.url,
      };
    case "card":
      return {
        cardholder: f.cardholder,
        number: f.number,
        expiry: f.expiry,
        cvv: f.cvv,
      };
    default:
      return {};
  }
}

/**
 * Populate form fields from a decrypted payload object. Unknown keys are
 * ignored; missing keys default to empty string. This is the inverse of
 * `buildPayload` and is what makes edit-mode repopulation work.
 */
function fieldsFromPayload(
  type: SecretType,
  payload: Record<string, string>,
): Partial<SecretFormFields> {
  switch (type) {
    case "login":
      return {
        username: payload.username ?? "",
        password: payload.password ?? "",
        url: payload.url ?? "",
      };
    case "note":
      return { content: payload.content ?? "" };
    case "api_key":
      return {
        key_name: payload.key_name ?? "",
        key_value: payload.key_value ?? "",
        url: payload.url ?? "",
      };
    case "card":
      return {
        cardholder: payload.cardholder ?? "",
        number: payload.number ?? "",
        expiry: payload.expiry ?? "",
        cvv: payload.cvv ?? "",
      };
    default:
      return {};
  }
}

// ─── Component ────────────────────────────────────────────────

export default function SecretEditor({
  open,
  onClose,
  entityType,
  entityId,
  editSecretId = null,
}: SecretEditorProps) {
  const queryClient = useQueryClient();
  const cryptoKey = useSecretsStore((s) => s.cryptoKey);
  const keyVersion = useSecretsStore((s) => s.keyVersion);
  const isUnlocked = useSecretsStore((s) => s.isUnlocked);

  const isEditMode = !!editSecretId;

  const [secretType, setSecretType] = useState<SecretType>("login");
  const [fields, setFields] = useState<SecretFormFields>(emptyFields);
  /** Form-level validation error message (rendered inline). */
  const [formError, setFormError] = useState<string | null>(null);

  // ── Edit-mode fetch + decrypt ──────────────────────────────
  // When editSecretId is provided and the modal opens, fetch the existing
  // secret, decrypt its blob, parse the JSON, and populate the form. The
  // query is disabled until both the modal is open AND the vault is
  // unlocked (without cryptoKey, decryption cannot succeed).
  const editQuery = useQuery({
    queryKey: secretKeys.detail(editSecretId ?? ""),
    queryFn: () => apiFetch<{ data: Secret }>(`/api/v1/secrets/${editSecretId}`).then(r => r.data),
    enabled: open && isEditMode && !!editSecretId && isUnlocked,
  });

  // Populate the form once the secret has been fetched and decrypted.
  // Runs as an effect so React controls timing; the `loadedId` guard
  // prevents re-populating (and clobbering user edits) if the query
  // refetches the same record.
  const [loadedId, setLoadedId] = useState<string | null>(null);
  useEffect(() => {
    if (!editQuery.data) return;
    const secret = editQuery.data;
    if (loadedId === secret.id) return;

    // Decrypt requires the in-memory cryptoKey. If the vault is locked
    // the form stays empty and the locked notice is shown instead.
    if (!cryptoKey) {
      setLoadedId(secret.id);
      return;
    }

    let cancelled = false;
    (async () => {
      try {
        const plaintext = await decrypt(
          cryptoKey,
          secret.encrypted_data ?? "",
          secret.iv ?? "",
        );
        if (cancelled) return;
        const payload = JSON.parse(plaintext) as Record<string, string>;
        setSecretType(secret.secret_type);
        setFields({
          ...emptyFields(),
          name: secret.name,
          ...fieldsFromPayload(secret.secret_type, payload),
        });
        setFormError(null);
        setLoadedId(secret.id);
      } catch {
        if (cancelled) return;
        setFormError(
          "Failed to decrypt this secret. Your master password may be incorrect or the data is corrupted.",
        );
        setLoadedId(secret.id);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [editQuery.data, cryptoKey, loadedId]);

  // Reset form state whenever the modal closes or switches between
  // create/edit. keyed by open + editSecretId so reopening the same
  // edit modal re-fetches.
  useEffect(() => {
    if (!open) {
      setSecretType("login");
      setFields(emptyFields());
      setFormError(null);
      setLoadedId(null);
    }
  }, [open]);

  // ── Create mutation ────────────────────────────────────────
  const createMutation = useMutation({
    mutationFn: async (req: CreateSecretRequest) =>
      apiFetch<{ data: Secret }>("/api/v1/secrets", { method: "POST", body: req }).then(r => r.data),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: secretKeys.lists() });
      if (entityType && entityId) {
        void queryClient.invalidateQueries({
          queryKey: secretKeys.listByEntity(entityType, entityId),
        });
      }
      onClose();
    },
    onError: (err) => {
      setFormError(
        err instanceof ApiError ? err.message : "Failed to create secret.",
      );
    },
  });

  // ── Update mutation ────────────────────────────────────────
  const updateMutation = useMutation({
    mutationFn: async (req: UpdateSecretRequest) => {
      if (!editSecretId) throw new Error("Missing secret id");
      return apiFetch<{ data: Secret }>(`/api/v1/secrets/${editSecretId}`, {
        method: "PATCH",
        body: req,
      });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: secretKeys.lists() });
      void queryClient.invalidateQueries({
        queryKey: secretKeys.detail(editSecretId ?? ""),
      });
      if (entityType && entityId) {
        void queryClient.invalidateQueries({
          queryKey: secretKeys.listByEntity(entityType, entityId),
        });
      }
      onClose();
    },
    onError: (err) => {
      setFormError(
        err instanceof ApiError ? err.message : "Failed to update secret.",
      );
    },
  });

  // ── Submit ─────────────────────────────────────────────────
  async function handleSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);

    if (!cryptoKey) {
      setFormError(
        "Your secrets vault is locked. Unlock it with your master password first.",
      );
      return;
    }

    if (!fields.name.trim()) {
      setFormError("A name is required.");
      return;
    }

    const payload = buildPayload(secretType, fields);
    const jsonString = JSON.stringify(payload);

    try {
      const { ciphertext, iv } = await encrypt(cryptoKey, jsonString);

      if (isEditMode && editSecretId) {
        const req: UpdateSecretRequest = {
          encrypted_data: ciphertext,
          iv,
          key_version: keyVersion,
          name: fields.name.trim(),
          secret_type: secretType,
        };
        updateMutation.mutate(req);
      } else {
        const req: CreateSecretRequest = {
          encrypted_data: ciphertext,
          iv,
          key_version: keyVersion,
          name: fields.name.trim(),
          secret_type: secretType,
          entity_type: entityType,
          entity_id: entityId,
        };
        createMutation.mutate(req);
      }
    } catch {
      setFormError("Encryption failed. Please try again.");
    }
  }

  // ── Field update helper ────────────────────────────────────
  function setField<K extends keyof SecretFormFields>(
    key: K,
    value: SecretFormFields[K],
  ) {
    setFields((prev) => ({ ...prev, [key]: value }));
  }

  const submitting = createMutation.isPending || updateMutation.isPending;
  const loadingEdit = isEditMode && editQuery.isLoading && !loadedId;

  // Memoize the title so it doesn't flicker between create/edit.
  const title = useMemo(
    () => (isEditMode ? "Edit Secret" : "Add Secret"),
    [isEditMode],
  );

  return (
    <Modal opened={open} onClose={onClose} title={title} size="lg">
      {/* Locked notice — vault must be unlocked before creating/editing. */}
      {!isUnlocked ? (
        <div className="rounded-md bg-amber-50 p-4 text-sm text-amber-800">
          Your secrets vault is locked. Enter your master password to unlock
          it before {isEditMode ? "editing" : "creating"} a secret.
        </div>
      ) : loadingEdit ? (
        <div className="space-y-3">
          <div className="h-4 w-1/3 animate-pulse rounded bg-gray-200" />
          <div className="h-10 w-full animate-pulse rounded bg-gray-100" />
          <div className="h-10 w-full animate-pulse rounded bg-gray-100" />
          <div className="h-10 w-full animate-pulse rounded bg-gray-100" />
        </div>
      ) : (
        <form onSubmit={handleSubmit} className="space-y-4">
          {/* Secret type selector */}
          <Select
            label="Type"
            value={secretType}
            onChange={(value) =>
              setSecretType(value as SecretType)
            }
            data={SECRET_TYPE_OPTIONS}
          />

          {/* Name — plaintext, stored unencrypted for searchability. */}
          <Input
            label="Name"
            value={fields.name}
            onChange={(e) => setField("name", e.target.value)}
            placeholder="e.g. GitHub Login"
            required
            autoFocus
          />

          {/* Type-specific fields */}
          {secretType === "login" && (
            <>
              <Input
                label="Username"
                value={fields.username}
                onChange={(e) => setField("username", e.target.value)}
                placeholder="username or email"
                autoComplete="off"
              />
              <Input
                label="Password"
                type="password"
                value={fields.password}
                onChange={(e) => setField("password", e.target.value)}
                autoComplete="new-password"
              />
              <Input
                label="URL"
                type="text"
                value={fields.url}
                onChange={(e) => setField("url", e.target.value)}
                placeholder="https://example.com"
              />
            </>
          )}

          {secretType === "note" && (
            <div>
              <label
                htmlFor="secret-content"
                className="block text-sm font-medium text-gray-900"
              >
                Content
              </label>
              <textarea
                id="secret-content"
                value={fields.content}
                onChange={(e) => setField("content", e.target.value)}
                rows={6}
                className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:outline-none focus:ring-2 focus:ring-inset focus:ring-indigo-600"
                placeholder="Write your secure note here..."
              />
            </div>
          )}

          {secretType === "api_key" && (
            <>
              <Input
                label="Key Name"
                value={fields.key_name}
                onChange={(e) => setField("key_name", e.target.value)}
                placeholder="e.g. OPENAI_API_KEY"
                autoComplete="off"
              />
              <Input
                label="Key Value"
                type="password"
                value={fields.key_value}
                onChange={(e) => setField("key_value", e.target.value)}
                autoComplete="off"
              />
              <Input
                label="URL"
                type="text"
                value={fields.url}
                onChange={(e) => setField("url", e.target.value)}
                placeholder="https://platform.example.com"
              />
            </>
          )}

          {secretType === "card" && (
            <>
              <Input
                label="Cardholder Name"
                value={fields.cardholder}
                onChange={(e) => setField("cardholder", e.target.value)}
                placeholder="Name on card"
                autoComplete="cc-name"
              />
              <Input
                label="Card Number"
                value={fields.number}
                onChange={(e) => setField("number", e.target.value)}
                placeholder="0000 0000 0000 0000"
                inputMode="numeric"
                autoComplete="cc-number"
              />
              <div className="grid grid-cols-2 gap-3">
                <Input
                  label="Expiry"
                  value={fields.expiry}
                  onChange={(e) => setField("expiry", e.target.value)}
                  placeholder="MM/YY"
                  autoComplete="cc-exp"
                />
                <Input
                  label="CVV"
                  type="password"
                  value={fields.cvv}
                  onChange={(e) => setField("cvv", e.target.value)}
                  placeholder="123"
                  inputMode="numeric"
                  autoComplete="cc-csc"
                />
              </div>
            </>
          )}

          {formError && (
            <p
              role="alert"
              className="rounded-md bg-red-50 px-3 py-2 text-sm text-red-700"
            >
              {formError}
            </p>
          )}

          <div className="flex justify-end gap-2 pt-2">
            <Button
              type="button"
              variant="secondary"
              onClick={onClose}
              disabled={submitting}
              className="border border-gray-300 bg-white text-gray-700 hover:bg-gray-50"
            >
              Cancel
            </Button>
            <Button type="submit" loading={submitting}>
              {isEditMode ? "Save Changes" : "Create Secret"}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}
