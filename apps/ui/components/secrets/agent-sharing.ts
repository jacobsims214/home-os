"use client";

/**
 * Agent credential sharing helpers (#4575) — the browser half of #4446.
 *
 * Policy (decisions/2026-11-15-agent-credential-access.md): a credential is
 * NOT shared with agents unless an owner or family_manager explicitly turns
 * sharing on for that one credential. Nothing here changes that: this module
 * only wires the two calls the API already documents, and mirrors the API's
 * role gate so the UI does not draw a control that answers 403.
 *
 * The two calls (apps/api/internal/secret/agentshare.go):
 *   PUT    /api/v1/secrets/{id}/agent-share   {value: {...}}  owner/manager
 *   DELETE /api/v1/secrets/{id}/agent-share                   owner/manager
 *
 * Why the browser must send the value: the server cannot read the vault blob
 * (it is encrypted with the household master password), so turning sharing on
 * means decrypting here, in memory, and sending the plaintext once over TLS.
 * The API seals its own copy and returns the value nowhere.
 *
 * SECURITY RULES this file follows, please keep them:
 *  - The plaintext is never logged, never put in React state, never written
 *    to storage, and never rendered. It lives in a local variable for the
 *    duration of one request.
 *  - There is no "share everything" path and no default flip: every call is
 *    for one explicit credential id.
 *  - Disabling the control for other roles is presentation only. The API is
 *    the enforcement point and still answers 403 for them.
 */

import { apiFetch } from "@/lib/api";
import { decrypt } from "@/lib/crypto";
import type { Secret } from "@/lib/types/api";

/** The roles the API lets share or unshare a credential (agentshare.go). */
export function canManageAgentSharing(role: string | null | undefined): boolean {
  return role === "owner" || role === "family_manager";
}

/**
 * Turn sharing on for one credential: fetch the blob, decrypt it in memory,
 * and hand the plaintext to the API once. Returns nothing — the caller only
 * needs success/failure, because the value must never come back into the UI.
 *
 * Errors propagate: 403 (not owner/manager), 404 (unknown or another
 * household), 503 AGENT_SHARING_DISABLED (the server has no
 * AGENT_CREDENTIALS_KEY), or a decrypt failure (wrong key).
 */
export async function enableAgentSharing(
  secretId: string,
  cryptoKey: CryptoKey,
): Promise<void> {
  const { data: secret } = await apiFetch<{ data: Secret }>(
    `/api/v1/secrets/${encodeURIComponent(secretId)}`,
  );
  const plaintext = await decrypt(
    cryptoKey,
    secret.encrypted_data ?? "",
    secret.iv ?? "",
  );
  // Parsed fresh and never stored anywhere: this object is the payload the
  // server seals, and the only copy lives until this function returns.
  const value = JSON.parse(plaintext) as Record<string, string>;
  await apiFetch(`/api/v1/secrets/${encodeURIComponent(secretId)}/agent-share`, {
    method: "PUT",
    body: { value },
  });
}

/**
 * Turn sharing off for one credential. The server deletes its copy in the
 * same request, and the call is idempotent, so this is safe to retry.
 */
export async function disableAgentSharing(secretId: string): Promise<void> {
  await apiFetch(`/api/v1/secrets/${encodeURIComponent(secretId)}/agent-share`, {
    method: "DELETE",
  });
}

/**
 * A user-facing sentence for a failed share/unshare. The 503 has its own
 * wording because it is a server-setup fact, not something the person did
 * wrong, and 403 explains who can do it.
 */
export function agentShareErrorMessage(err: unknown): string {
  const apiError = err as
    | { status?: number; message?: string; body?: { error?: { code?: string } } }
    | null
    | undefined;
  const status = apiError?.status;
  // apiFetch puts the API's error object on `body`, so the code is nested.
  const code = apiError?.body?.error?.code;
  if (status === 503 || code === "AGENT_SHARING_DISABLED") {
    return "Sharing with agents is turned off on this server. Ask whoever runs Home OS to configure it.";
  }
  if (status === 403) {
    return "Only an owner or family manager can change agent sharing.";
  }
  if (status === 404) {
    return "That credential no longer exists. Reload the list.";
  }
  // ApiError's message is "API 400: Bad Request" — useless to a person — so
  // only a real, specific message is shown; otherwise a plain sentence.
  const message = apiError?.message;
  if (!message || /^API \d+/.test(message)) {
    return "Could not change agent sharing. Try again.";
  }
  return message;
}
