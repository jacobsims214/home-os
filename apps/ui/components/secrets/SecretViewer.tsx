"use client";

import { useEffect, useState } from "react";
import Modal from "@/components/ui/Modal";
import { apiFetch, ApiError } from "@/lib/api";
// Crypto utilities + secrets store are provided by the Frontend Crypto Layer
// story (apps/ui/lib/crypto.ts, apps/ui/stores/secrets.ts). The API surface
// used here matches the contract documented in the SecretViewer task:
//   - crypto.decrypt(cryptoKey, encrypted_data, iv) -> Promise<string>
//   - useSecretsStore Zustand store exposing `.cryptoKey` (CryptoKey | null)
import * as crypto from "@/lib/crypto";
import { useSecretsStore } from "@/stores/secrets";
import type { Secret, SecretType } from "@/lib/types/api";

// Re-export Secret as SecretRecord for internal use
type SecretRecord = Secret;

// ─── Types ─────────────────────────────────────────────────────

// SecretType and the GET /api/v1/secrets/:id response shape (Secret) are
// imported from @/lib/types/api. Duplicating them here risks silent drift
// from the Go model (apps/api/internal/secret/model.go).

/** Decrypted plaintext payloads, keyed by secret_type. The browser decrypts
 *  the blob and parses it as JSON; the field set is type-specific. */
interface LoginPayload {
  username?: string;
  password?: string;
  url?: string;
}
interface NotePayload {
  content?: string;
}
interface ApiKeyPayload {
  key_name?: string;
  key_value?: string;
  url?: string;
}
interface CardPayload {
  cardholder?: string;
  number?: string;
  expiry?: string;
  cvv?: string;
}

type DecryptedPayload =
  | ({ type: "login" } & LoginPayload)
  | ({ type: "note" } & NotePayload)
  | ({ type: "api_key" } & ApiKeyPayload)
  | ({ type: "card" } & CardPayload);

interface SecretViewerProps {
  secretId: string | null;
  open: boolean;
  onClose: () => void;
  /**
   * Called when the user clicks "Edit" on a loaded secret. The parent is
   * responsible for closing the viewer and opening SecretEditor with the
   * returned secret id in edit mode.
   */
  onEdit?: (secretId: string) => void;
}

// ─── Inline icons ──────────────────────────────────────────────

function CopyIcon({ className = "h-4 w-4" }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor" aria-hidden="true">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M15.75 17.25v3.375c0 .621-.504 1.125-1.125 1.125h-9.75a1.125 1.125 0 01-1.125-1.125V7.875c0-.621.504-1.125 1.125-1.125H6.75a9.06 9.06 0 011.5.243M19.5 8.25l-7.5 7.5m7.5-7.5l-7.5-7.5m7.5 7.5H21"
      />
    </svg>
  );
}

function EyeIcon({ className = "h-4 w-4" }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor" aria-hidden="true">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M2.036 12.322a1.012 1.012 0 010-.639C3.423 7.51 7.36 4.5 12 4.5c4.638 0 8.573 3.007 9.963 7.178.07.207.07.431 0 .639C20.577 16.49 16.64 19.5 12 19.5c-4.638 0-8.573-3.007-9.963-7.178z"
      />
      <path strokeLinecap="round" strokeLinejoin="round" d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
    </svg>
  );
}

function EyeOffIcon({ className = "h-4 w-4" }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor" aria-hidden="true">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M3.98 8.223A10.477 10.477 0 001.934 12C3.226 16.338 7.244 19.5 12 19.5c.993 0 1.953-.138 2.863-.395M6.228 6.228A10.45 10.45 0 0112 4.5c4.756 0 8.773 3.162 10.065 7.498.09.284.09.59 0 .874a10.497 10.497 0 01-1.934 3.617M6.228 6.228L3 3m3.228 3.228l3.65 3.65m7.894 7.894L21 21m-3.228-3.228l-3.65-3.65m0 0a3 3 0 10-4.243-4.243m4.243 4.243L9.88 9.88"
      />
    </svg>
  );
}

function PencilIcon({ className = "h-4 w-4" }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor" aria-hidden="true">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931zm0 0L19.5 7.125"
      />
    </svg>
  );
}

// ─── Field row with copy + optional reveal toggle ─────────────

interface FieldRowProps {
  label: string;
  value: string | undefined | null;
  /** When true, the value is masked until the user toggles reveal. */
  sensitive?: boolean;
  /** Render the value as a multi-line block instead of a single line. */
  multiline?: boolean;
}

/**
 * FieldRow renders a labeled secret field with a Copy button and, for
 * `sensitive` fields, a show/hide toggle. Empty values still render the label
 * so the user can see the field exists but is blank.
 */
function FieldRow({ label, value, sensitive = false, multiline = false }: FieldRowProps) {
  const [revealed, setRevealed] = useState(false);
  const [copied, setCopied] = useState(false);

  const displayValue = value ?? "";
  const isEmpty = displayValue === "";
  // Masked rendering: dots with the same length budget so layout doesn't jump.
  const masked = "•".repeat(Math.min(displayValue.length, 24)) || "••••";
  const shown = sensitive ? (revealed ? displayValue : masked) : displayValue;

  async function handleCopy() {
    if (isEmpty) return;
    try {
      await navigator.clipboard.writeText(displayValue);
      setCopied(true);
      // Brief "Copied" confirmation, then revert. setTimeout keeps this
      // side-effect out of React render.
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard API can throw in non-secure contexts; the copied flag stays
      // false so the user sees the label did not flip to "Copied".
    }
  }

  return (
    <div className="flex flex-col gap-1 py-2">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-semibold uppercase tracking-wide text-gray-500">
          {label}
        </span>
        <div className="flex items-center gap-1">
          {sensitive && !isEmpty && (
            <button
              type="button"
              onClick={() => setRevealed((v) => !v)}
              className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-gray-600 hover:bg-gray-100 transition-colors"
              aria-pressed={revealed}
              aria-label={revealed ? `Hide ${label}` : `Show ${label}`}
              title={revealed ? "Hide" : "Show"}
            >
              {revealed ? <EyeOffIcon /> : <EyeIcon />}
              <span>{revealed ? "Hide" : "Show"}</span>
            </button>
          )}
          <button
            type="button"
            onClick={handleCopy}
            disabled={isEmpty}
            className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-gray-600 hover:bg-gray-100 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
            aria-label={`Copy ${label}`}
            title={copied ? "Copied!" : "Copy"}
          >
            <CopyIcon />
            <span>{copied ? "Copied" : "Copy"}</span>
          </button>
        </div>
      </div>
      {multiline ? (
        <pre className="mt-1 max-h-60 overflow-auto rounded-md border border-gray-200 bg-gray-50 p-3 text-sm text-gray-800 whitespace-pre-wrap break-words">
          {shown || <span className="text-gray-400 italic">empty</span>}
        </pre>
      ) : (
        <p
          className={`mt-1 break-all text-sm ${isEmpty ? "text-gray-400 italic" : "text-gray-900"}`}
        >
          {isEmpty ? "empty" : shown}
        </p>
      )}
    </div>
  );
}

// ─── Typed renderers per secret_type ──────────────────────────

function LoginFields(p: LoginPayload) {
  return (
    <div className="divide-y divide-gray-100">
      <FieldRow label="Username" value={p.username} />
      <FieldRow label="Password" value={p.password} sensitive />
      <FieldRow label="URL" value={p.url} />
    </div>
  );
}

function NoteFields(p: NotePayload) {
  return (
    <div className="divide-y divide-gray-100">
      <FieldRow label="Content" value={p.content} multiline />
    </div>
  );
}

function ApiKeyFields(p: ApiKeyPayload) {
  return (
    <div className="divide-y divide-gray-100">
      <FieldRow label="Key Name" value={p.key_name} />
      <FieldRow label="Key Value" value={p.key_value} sensitive />
      <FieldRow label="URL" value={p.url} />
    </div>
  );
}

function CardFields(p: CardPayload) {
  return (
    <div className="divide-y divide-gray-100">
      <FieldRow label="Cardholder" value={p.cardholder} />
      <FieldRow label="Number" value={p.number} sensitive />
      <FieldRow label="Expiry" value={p.expiry} />
      <FieldRow label="CVV" value={p.cvv} sensitive />
    </div>
  );
}

// ─── Main component ────────────────────────────────────────────

/**
 * SecretViewer — modal that fetches an encrypted secret, decrypts it in the
 * browser with the user's derived CryptoKey, and renders its fields by
 * secret_type.
 *
 * Why decrypt client-side: the core API only ever stores/returns the encrypted
 * blob + IV. The plaintext is only ever reconstructed in the browser using the
 * CryptoKey derived (via PBKDF2) from the user's master password. A wrong key
 * (or a locked vault) yields a GCM auth-tag mismatch which we surface as a
 * decryption-failed error.
 */
export default function SecretViewer({
  secretId,
  open,
  onClose,
  onEdit,
}: SecretViewerProps) {
  const [secret, setSecret] = useState<SecretRecord | null>(null);
  const [payload, setPayload] = useState<DecryptedPayload | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Reset transient state when the modal closes or the target secret changes,
  // and trigger fetch + decrypt when it opens. We read cryptoKey from the store
  // via getState() inside the effect (mirrors the FileViewer/auth-token pattern)
  // so the async closure always sees the current key without re-subscribing.
  useEffect(() => {
    let cancelled = false;
    setPayload(null);
    setError(null);

    if (!open || !secretId) {
      setSecret(null);
      setLoading(false);
      return;
    }

    setLoading(true);
    (async () => {
      try {
        const resp = await apiFetch<{ data: Secret }>(
          `/api/v1/secrets/${encodeURIComponent(secretId)}`,
        );
        const rec = resp.data;
        if (cancelled) return;
        setSecret(rec);

        const cryptoKey = useSecretsStore.getState().cryptoKey;
        if (!cryptoKey) {
          throw new Error(
            "Vault is locked. Enter your master password to view this secret.",
          );
        }

        const plaintext = await crypto.decrypt(
          cryptoKey,
          rec.encrypted_data ?? "",
          rec.iv ?? "",
        );
        if (cancelled) return;

        // The decrypted plaintext is a JSON object whose fields depend on
        // secret_type. Parse defensively — a malformed blob is treated as a
        // decryption failure.
        let parsed: Record<string, unknown>;
        try {
          parsed = JSON.parse(plaintext) as Record<string, unknown>;
        } catch {
          throw new Error(
            "Decrypted secret is not valid JSON. The data may be corrupted.",
          );
        }

        const typed = toPayload(rec.secret_type, parsed);
        if (!typed) {
          throw new Error(`Unsupported secret type: ${rec.secret_type}`);
        }
        setPayload(typed);
      } catch (e) {
        if (cancelled) return;
        const msg =
          e instanceof ApiError
            ? e.message
            : e instanceof Error
              ? e.message
              : "Failed to view secret.";
        // A GCM auth-tag mismatch (wrong master password / key mismatch)
        // surfaces as a generic DOMException from crypto.subtle — make the
        // message actionable for the user.
        setError(
          /decrypt|operation|key/i.test(msg) && !/locked/i.test(msg)
            ? "Decryption failed — your master password may be incorrect."
            : msg,
        );
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [open, secretId]);

  const title = secret?.name ?? "View secret";

  return (
    <Modal open={open} onClose={onClose} title={title} maxWidth="max-w-lg">
      {error && (
        <div
          role="alert"
          className="mb-4 rounded-md bg-red-50 p-3 text-sm text-red-700"
        >
          {error}
        </div>
      )}

      {loading && (
        <div className="flex items-center justify-center py-16 text-sm text-gray-500">
          Decrypting…
        </div>
      )}

      {!loading && payload && (
        <div className="rounded-lg border border-gray-200 bg-white">
          <div className="px-4 py-2 border-b border-gray-100 flex items-center gap-2">
            <span className="inline-flex items-center rounded-full bg-indigo-50 px-2 py-0.5 text-xs font-medium text-indigo-700 capitalize">
              {payload.type.replace("_", " ")}
            </span>
            {secret?.created_at && (
              <span className="text-xs text-gray-400">
                added {new Date(secret.created_at).toLocaleDateString()}
              </span>
            )}
          </div>
          <div className="px-4">
            {payload.type === "login" && <LoginFields {...payload} />}
            {payload.type === "note" && <NoteFields {...payload} />}
            {payload.type === "api_key" && <ApiKeyFields {...payload} />}
            {payload.type === "card" && <CardFields {...payload} />}
          </div>
        </div>
      )}

      {!loading && !error && !payload && (
        <p className="py-8 text-center text-sm text-gray-400">
          No secret selected.
        </p>
      )}

      {/* Edit affordance — surfaces the existing edit-mode implementation in
          SecretEditor by handing control back to the parent with the secret
          id. Only rendered once a secret has loaded (so secret.id is known)
          and the vault is not in an error state. */}
      {!loading && !error && payload && secret && onEdit && (
        <div className="mt-4 flex justify-end border-t border-gray-100 pt-4">
          <button
            type="button"
            onClick={() => onEdit(secret.id)}
            className="inline-flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-semibold text-white hover:bg-indigo-500 transition-colors"
          >
            <PencilIcon />
            Edit
          </button>
        </div>
      )}
    </Modal>
  );
}

/**
 * Narrow a parsed plaintext object into a tagged payload matching the secret
 * type. Returns null for unknown types so the caller can surface an error
 * rather than rendering the wrong fields.
 */
function toPayload(
  type: SecretType,
  raw: Record<string, unknown>,
): DecryptedPayload | null {
  const str = (v: unknown): string | undefined =>
    typeof v === "string" && v !== "" ? v : undefined;

  switch (type) {
    case "login":
      return {
        type: "login",
        username: str(raw.username),
        password: str(raw.password),
        url: str(raw.url),
      };
    case "note":
      return {
        type: "note",
        content: str(raw.content),
      };
    case "api_key":
      return {
        type: "api_key",
        key_name: str(raw.key_name),
        key_value: str(raw.key_value),
        url: str(raw.url),
      };
    case "card":
      return {
        type: "card",
        cardholder: str(raw.cardholder),
        number: str(raw.number),
        expiry: str(raw.expiry),
        cvv: str(raw.cvv),
      };
    default:
      return null;
  }
}
