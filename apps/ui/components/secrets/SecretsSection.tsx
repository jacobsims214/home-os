"use client";

/**
 * SecretsSection — the native Home OS credentials UI for entity detail pages,
 * mobile-first (#4575, mockup artifact #103 "Credentials" pane).
 *
 * This replaces the old PasswordsSection (which linked external ciphers). Unlike
 * the link-based PasswordsSection, SecretsSection owns the full lifecycle of
 * native, zero-knowledge secrets: list, create (via SecretEditor), view
 * (via SecretViewer), and delete.
 *
 * Zero-knowledge model (see architecture/secrets-manager-research.md):
 * the API only ever stores AES-256-GCM ciphertext. The plaintext name and
 * secret_type are stored unencrypted so listings can be searched without
 * decryption; the secret data itself is encrypted/decrypted in the browser
 * with a CryptoKey derived (PBKDF2) from the user's master password. That key
 * lives only in memory (secretsStore) and is lost on page refresh.
 *
 * Lock state:
 *  - secretsStore.isUnlocked === false → render a "Locked" panel with an
 *    "Unlock Secrets" button that opens MasterPasswordPrompt. The list is not
 *    fetched while locked (the API returns metadata only, but there's nothing
 *    actionable the user can do — view/edit/create all require the key).
 *  - secretsStore.isUnlocked === true → fetch GET /api/v1/secrets and render
 *    the list with name, type icon, created date, and a per-row delete button.
 *
 * The list pattern (TanStack Query + delete mutation + ConfirmDialog) mirrors
 * apps/ui/components/files/FileList.tsx.
 *
 * ## Mobile-first (#4575)
 *  - One card per credential at every width, with two separate targets: the
 *    name area is a 44 px+ button that opens the viewer, and the share switch
 *    sits on its own line below (never nested inside the button), so a thumb
 *    cannot flip a switch when it meant to open a credential.
 *  - Cards rather than the mockup's table because this section lives in a
 *    ~370 px column beside an entity's details, not in the settings pane the
 *    mockup draws. The mockup's per-row content is kept.
 *  - Nothing clips at 375 px: long names wrap with `[overflow-wrap:anywhere]`
 *    and no row has a fixed width.
 *
 * ## "Shared with agents" (#4446, #4575)
 * Turning sharing on stores a SECOND copy of the credential that the server
 * (and only the server) can decrypt, so the family's MCP agents can read it.
 * Every agent read is written to the family activity feed.
 *
 *  - The switch is drawn only for an owner or family_manager, mirroring the
 *    API's role gate (`canManageAgentSharing`). For everyone else it is
 *    rendered DISABLED with the reason — presentation only; the API still
 *    answers 403 for them, and no role check here decides access.
 *  - Sharing OFF is the default and nothing in this component turns it on by
 *    itself: each PUT is one credential, after a click.
 *  - The plaintext never enters React state: `enableAgentSharing` decrypts it
 *    in memory and hands it to the API in one call (see agent-sharing.ts).
 *  - Each credential links to the activity feed filtered to it, which is where
 *    the agent's reads of that credential appear. The feed is the audit trail;
 *    this component shows presence and a way to get there, never the values.
 */

import { useState } from "react";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { secretKeys } from "@/lib/query-keys";
import type { SecretListItem, SecretListResponse, SecretType } from "@/lib/types/api";
import { probeNeedsSetup, useSecretsStore } from "@/stores/secrets";
import type { MasterPasswordPromptMode } from "@/components/secrets/MasterPasswordPrompt";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import MasterPasswordPrompt from "@/components/secrets/MasterPasswordPrompt";
import SecretEditor from "@/components/secrets/SecretEditor";
import SecretViewer from "@/components/secrets/SecretViewer";
// Mantine's Switch directly, not components/ui/Switch: that wrapper puts its
// label in a SIBLING span, so the only hit area is the ~20 px input and the
// words do not toggle anything. Mantine's own `label` prop wraps the input and
// the text in one <label>, which is the accessible, thumb-sized pattern.
import { Switch } from "@mantine/core";
import { useNavRole } from "@/components/shared/use-nav-role";
import {
  agentShareErrorMessage,
  canManageAgentSharing,
  disableAgentSharing,
  enableAgentSharing,
} from "@/components/secrets/agent-sharing";

// ─── Types ────────────────────────────────────────────────────

interface SecretsSectionProps {
  entityType: string;
  entityId: string;
}

// ─── Helpers ──────────────────────────────────────────────────

/** Short locale-formatted date (e.g. "Jun 5, 2026"). Falls back to the raw ISO. */
function formatDate(iso: string): string {
  try {
    return new Date(iso).toLocaleDateString("en-US", {
      month: "short",
      day: "numeric",
      year: "numeric",
      timeZone: "UTC",
    });
  } catch {
    return iso;
  }
}

// ─── Agent-sharing bits ───────────────────────────────────────

/**
 * The activity feed filtered to one credential (#4575).
 *
 * The API's `entity_id` filter already narrows the feed to one record
 * (apps/api/internal/activity/handler.go, parseFilter), and an agent read is
 * logged with entity_type 'secret' and this credential's id. So the audit
 * trail for one credential is a plain link — no new endpoint, no new query
 * shape, and the same page a person already reads.
 */
export function credentialActivityHref(secretId: string): string {
  return `/dashboard/activity?entity_type=secret&entity_id=${encodeURIComponent(secretId)}`;
}

/** Robot mark, so "shared with agents" reads as agents and not as a person. */
function AgentIcon({ className = "h-4 w-4" }: { className?: string }) {
  return (
    <svg
      className={className}
      fill="none"
      viewBox="0 0 24 24"
      strokeWidth={1.5}
      stroke="currentColor"
      aria-hidden="true"
    >
      <rect x="5" y="8" width="14" height="11" rx="3" />
      <path strokeLinecap="round" d="M12 4v4M12 4a1 1 0 1 1 0-.01M9 13v.5M15 13v.5M10 16h4" />
    </svg>
  );
}

/**
 * The "Shared with agents" switch for one credential.
 *
 * Presentational only: it renders what the API reported (`shared_with_agents`)
 * and calls the one handler it is given. It never decides access — that is
 * `canManageAgentSharing`, which mirrors the API's role gate for drawing the
 * control, and the API, which enforces it.
 *
 * Deliberately NOT wired to any default: an unchecked switch does nothing
 * until it is clicked, and a checked one means the server already holds a
 * copy. Turning it on requires the vault key, which is why the parent passes
 * a handler rather than this component doing the work.
 */
function AgentShareSwitch({
  secret,
  role,
  pending,
  onToggle,
}: {
  secret: SecretListItem;
  role: string | null;
  pending: boolean;
  onToggle: (secret: SecretListItem, next: boolean) => void;
}) {
  const allowed = canManageAgentSharing(role);
  const on = secret.shared_with_agents === true;
  const name = secret.name || "this credential";
  const disabled = !allowed || pending;
  const label = on ? "Shared with agents" : "Not shared with agents";
  // A disabled control must say WHY, not just be pale: the mockup's member view
  // keeps the switch visible with "Only Jacob or Amber can change sharing."
  const why = allowed ? undefined : "Only an owner or family manager can change sharing.";
  return (
    <div className="flex min-h-11 flex-col justify-center gap-0.5" data-testid={`agent-share-${secret.id}`}>
      {/* Mantine's `label` makes the SWITCH AND THE WORDS one <label>: the
          whole row is the hit area (min 44 px tall), so a thumb hits it on a
          phone, and the text is part of the accessible name. The sr-only suffix
          names the credential, so a screen reader hears "Shared with agents,
          Netflix" and not five identical switches (mockup artifact #103). */}
      <Switch
        checked={on}
        disabled={disabled}
        onChange={(e) => onToggle(secret, e.currentTarget.checked)}
        label={
          <>
            <span aria-hidden="true">{pending ? "Saving…" : label}</span>
            <span className="sr-only">{`, ${name}`}</span>
          </>
        }
        classNames={{ root: "min-h-11 flex items-center", label: "text-[13px] font-medium text-ink-2 cursor-inherit" }}
        {...(disabled ? { "data-disabled-reason": "role" } : {})}
      />
      {why && <p className="pl-[52px] text-xs leading-snug text-ink-3">{why}</p>}
      {allowed && on && secret.agent_shared_at && (
        <p className="pl-[52px] text-xs leading-snug text-ink-3">
          Since {formatDate(secret.agent_shared_at)}
        </p>
      )}
    </div>
  );
}

/** The link from one credential to its own slice of the family activity feed. */
function CredentialActivityLink({ secretId, name }: { secretId: string; name: string }) {
  return (
    <Link
      href={credentialActivityHref(secretId)}
      className="inline-flex min-h-11 items-center gap-1.5 text-[13px] font-semibold text-accent"
      aria-label={`Agent activity for ${name || "this credential"}`}
    >
      <AgentIcon />
      Agent activity
    </Link>
  );
}

// ─── Secret-type icon ──────────────────────────────────────────

/**
 * Inline SVG icon chosen by secret_type. Kept inline (no icon library) to
 * match FileList's ContentTypeIcon pattern. The icon is decorative — the
 * type label is also rendered as text for accessibility.
 *
 * Paths are Heroicons v2 outline (24px), licensed MIT.
 */
function SecretTypeIcon({ secretType }: { secretType: SecretType }) {
  let path: React.ReactNode;

  switch (secretType) {
    case "login":
      // KeyIcon — a credential with a password
      path = (
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          d="M15.75 5.25C17.4069 5.25 18.75 6.59315 18.75 8.25M21.75 8.25C21.75 11.5637 19.0637 14.25 15.75 14.25C15.3993 14.25 15.0555 14.2199 14.7213 14.1622C14.1583 14.0649 13.562 14.188 13.158 14.592L10.5 17.25H8.25V19.5H6V21.75H2.25V18.932C2.25 18.3352 2.48705 17.7629 2.90901 17.341L9.408 10.842C9.81202 10.438 9.93512 9.84172 9.83785 9.2787C9.7801 8.94446 9.75 8.60074 9.75 8.25C9.75 4.93629 12.4363 2.25 15.75 2.25C19.0637 2.25 21.75 4.93629 21.75 8.25Z"
        />
      );
      break;
    case "note":
      // DocumentTextIcon — a free-text secure note
      path = (
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          d="M19.5 14.25V11.625C19.5 9.76104 17.989 8.25 16.125 8.25H14.625C14.0037 8.25 13.5 7.74632 13.5 7.125V5.625C13.5 3.76104 11.989 2.25 10.125 2.25H8.25M8.25 15H15.75M8.25 18H12M10.5 2.25H5.625C5.00368 2.25 4.5 2.75368 4.5 3.375V20.625C4.5 21.2463 5.00368 21.75 5.625 21.75H18.375C18.9963 21.75 19.5 21.2463 19.5 20.625V11.25C19.5 6.27944 15.4706 2.25 10.5 2.25Z"
        />
      );
      break;
    case "api_key":
      // CodeBracketIcon — a machine credential / token
      path = (
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          d="M17.25 6.75L22.5 12L17.25 17.25M6.75 17.25L1.5 12L6.75 6.75M14.25 3.75L9.75 20.25"
        />
      );
      break;
    case "card":
      // CreditCardIcon — a payment card
      path = (
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          d="M2.25 8.25H21.75M2.25 9H21.75M5.25 14.25H11.25M5.25 16.5H8.25M4.5 19.5H19.5C20.7426 19.5 21.75 18.4926 21.75 17.25V6.75C21.75 5.50736 20.7426 4.5 19.5 4.5H4.5C3.25736 4.5 2.25 5.50736 2.25 6.75V17.25C2.25 18.4926 3.25736 19.5 4.5 19.5Z"
        />
      );
      break;
    default:
      // Fallback — generic key shape
      path = (
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          d="M15.75 5.25C17.4069 5.25 18.75 6.59315 18.75 8.25M21.75 8.25C21.75 11.5637 19.0637 14.25 15.75 14.25C15.3993 14.25 15.0555 14.2199 14.7213 14.1622C14.1583 14.0649 13.562 14.188 13.158 14.592L10.5 17.25H8.25V19.5H6V21.75H2.25V18.932C2.25 18.3352 2.48705 17.7629 2.90901 17.341L9.408 10.842C9.81202 10.438 9.93512 9.84172 9.83785 9.2787C9.7801 8.94446 9.75 8.60074 9.75 8.25C9.75 4.93629 12.4363 2.25 15.75 2.25C19.0637 2.25 21.75 4.93629 21.75 8.25Z"
        />
      );
  }

  return (
    <svg
      className="h-5 w-5 shrink-0 text-gray-400"
      fill="none"
      viewBox="0 0 24 24"
      strokeWidth={1.5}
      stroke="currentColor"
      aria-hidden="true"
    >
      {path}
    </svg>
  );
}

/** Decorative lock icon used in the header and the locked-state panel. */
function LockIcon({ className = "h-5 w-5 text-gray-400" }: { className?: string }) {
  return (
    <svg
      className={className}
      fill="none"
      viewBox="0 0 24 24"
      strokeWidth={1.5}
      stroke="currentColor"
      aria-hidden="true"
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M16.5 10.5V6.75C16.5 4.26472 14.4853 2.25 12 2.25C9.51472 2.25 7.5 4.26472 7.5 6.75V10.5M6.75 21.75H17.25C18.4926 21.75 19.5 20.7426 19.5 19.5V12.75C19.5 11.5074 18.4926 10.5 17.25 10.5H6.75C5.50736 10.5 4.5 11.5074 4.5 12.75V19.5C4.5 20.7426 5.50736 21.75 6.75 21.75Z"
      />
    </svg>
  );
}

/** Small "plus" icon for the Add Secret button. */
function PlusIcon() {
  return (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      strokeWidth={1.5}
      stroke="currentColor"
      aria-hidden="true"
    >
      <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
    </svg>
  );
}

/** Trash icon button used on each row to delete a secret. */
function TrashIconButton({
  onClick,
  disabled,
  label,
}: {
  onClick: (e: React.MouseEvent) => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      title={label}
      className="inline-flex items-center justify-center rounded-md p-1.5 text-gray-400 hover:bg-red-50 hover:text-red-600 disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-gray-400 transition-colors"
    >
      <svg
        className="h-4 w-4"
        fill="none"
        viewBox="0 0 24 24"
        strokeWidth={1.5}
        stroke="currentColor"
        aria-hidden="true"
      >
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0"
        />
      </svg>
    </button>
  );
}

// ─── Rows ─────────────────────────────────────────────────────

/**
 * One credential on a phone: the name area is the button (the whole row is a
 * 44 px target) and the share switch sits on its own line below, so a thumb
 * never opens the viewer when it meant to flip the switch or vice versa.
 *
 * The switch gets a row of its own rather than sitting to the right of the
 * name: at 375 px a switch plus a long credential name leaves the name about
 * eight characters wide.
 */
function CredentialCard({
  secret,
  role,
  sharingId,
  onOpen,
  onToggleShare,
  onDelete,
  deleting,
}: {
  secret: SecretListItem;
  role: string | null;
  sharingId: string | null;
  onOpen: () => void;
  onToggleShare: (secret: SecretListItem, next: boolean) => void;
  onDelete: () => void;
  deleting: boolean;
}) {
  const name = secret.name || "Untitled secret";
  return (
    <li
      className="rounded-token-md border border-line bg-surface"
      data-testid={`secret-card-${secret.id}`}
    >
      <div className="flex items-stretch gap-1 p-1">
        <button
          type="button"
          onClick={onOpen}
          className="flex min-h-11 min-w-0 flex-1 items-center gap-3 rounded-token-md px-3 py-2 text-left transition-colors hover:bg-surface-2 active:bg-surface-2"
        >
          <SecretTypeIcon secretType={secret.secret_type} />
          <span className="min-w-0 flex-1">
            <span className="block text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">
              {name}
            </span>
            <span className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-ink-3">
              <span className="capitalize">{secret.secret_type.replace("_", " ")}</span>
              <span aria-hidden="true">·</span>
              <span>added {formatDate(secret.created_at)}</span>
            </span>
          </span>
        </button>
        <div className="flex shrink-0 items-center">
          <TrashIconButton
            onClick={(e) => {
              e.stopPropagation();
              onDelete();
            }}
            disabled={deleting}
            label={`Delete ${name}`}
          />
        </div>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-t border-line px-3 py-1.5">
        <AgentShareSwitch
          secret={secret}
          role={role}
          pending={sharingId === secret.id}
          onToggle={onToggleShare}
        />
        <CredentialActivityLink secretId={secret.id} name={name} />
      </div>
    </li>
  );
}

// ─── Component ────────────────────────────────────────────────

/**
 * SecretsSection renders the native credentials attached to a given household
 * entity (entity_type + entity_id). When the vault is locked it shows an
 * unlock prompt; when unlocked it fetches the entity-scoped secret list and
 * lets the user view, add, and delete secrets.
 */
export default function SecretsSection({ entityType, entityId }: SecretsSectionProps) {
  const queryClient = useQueryClient();
  // Subscribe to isUnlocked only — avoids re-rendering the list when the
  // cryptoKey changes but the lock state doesn't.
  const isUnlocked = useSecretsStore((s) => s.isUnlocked);
  // The signed-in role decides whether the share switch is drawn ENABLED. It is
  // presentation only: the API's own gate (403) is what actually decides.
  const role = useNavRole();

  // ── Modal / selection state ───────────────────────────────
  const [showUnlock, setShowUnlock] = useState(false);
  const [showEditor, setShowEditor] = useState(false);
  /**
   * Secret id being edited (opens SecretEditor in edit mode). null = editor
   * closed or in create mode. When the user clicks "Edit" in SecretViewer we
   * close the viewer and set this id; the editor's `open` prop is derived
   * from `showEditor || editSecretId !== null`.
   */
  const [editSecretId, setEditSecretId] = useState<string | null>(null);
  /** Secret being viewed (opens SecretViewer). null = viewer closed. */
  const [viewTarget, setViewTarget] = useState<string | null>(null);
  /** Secret pending deletion confirmation. null = dialog closed. */
  const [deleteTarget, setDeleteTarget] = useState<SecretListItem | null>(null);
  /** Credential whose agent-sharing switch is mid-flight (null = idle). */
  const [sharingId, setSharingId] = useState<string | null>(null);
  /** Last agent-sharing failure, shown under the list in plain words. */
  const [shareError, setShareError] = useState<string | null>(null);

  // ── Setup-vs-unlock mode detection ────────────────────────
  // The secrets store does NOT surface a "needs setup" flag — `isUnlocked`
  // only reflects whether a key is loaded in this session. To decide whether
  // to render MasterPasswordPrompt in "setup" (first-time) or "unlock"
  // (returning user) mode, we probe GET /api/v1/secrets/key once on the
  // first unlock click (404 = not set up). See `probeNeedsSetup` in
  // stores/secrets.ts and the contract in
  // architecture/secrets-ui.md ("Why the parent picks the mode").
  //
  // `needsSetup` is tri-state: null = not yet probed, true = no master
  // password configured, false = already configured. `promptMode` is the
  // derived value passed to <MasterPasswordPrompt mode={...} />. It defaults
  // to "unlock" so a probe failure (network/500) lands in the existing
  // unlock error path rather than silently offering setup.
  const [needsSetup, setNeedsSetup] = useState<boolean | null>(null);
  const [promptMode, setPromptMode] = useState<MasterPasswordPromptMode>("unlock");

  /**
   * Open the master-password prompt, picking setup vs unlock mode by probing
   * GET `/api/v1/secrets/key`. The probe is cached in `needsSetup` so we only
   * hit the API once per mount (a successful setup or unlock flips
   * `isUnlocked` and the prompt closes; on the next click `isUnlocked` is
   * already true and the locked panel is gone entirely).
   */
  async function handleUnlockClick() {
    let setupNeeded = needsSetup;
    if (setupNeeded === null) {
      setupNeeded = await probeNeedsSetup();
      setNeedsSetup(setupNeeded);
    }
    setPromptMode(setupNeeded ? "setup" : "unlock");
    setShowUnlock(true);
  }

  // ── List query (entity-scoped) ────────────────────────────
  // Only fetched when unlocked — while locked there is nothing actionable and
  // the API metadata is useless without the key to decrypt on view.
  const queryKey = secretKeys.listByEntity(entityType, entityId);

  const secretsQ = useQuery({
    queryKey,
    queryFn: () =>
      apiFetch<SecretListResponse>("/api/v1/secrets", {
        params: { entity_type: entityType, entity_id: entityId },
      }),
    enabled: !!entityType && !!entityId && isUnlocked,
  });

  const secrets = secretsQ.data?.data ?? [];

  // ── Delete mutation ────────────────────────────────────────
  // DELETE /api/v1/secrets/:id then invalidate the entity-scoped list so the
  // row disappears immediately. The dialog stays open on error so the user
  // can retry or cancel (mirrors FileList's delete pattern).
  const deleteMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/api/v1/secrets/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey });
      setDeleteTarget(null);
    },
    onError: (error) => {
      console.error("Failed to delete secret:", error);
    },
  });

  function confirmDelete() {
    if (deleteTarget) {
      deleteMutation.mutate(deleteTarget.id);
    }
  }

  // ── Agent sharing (#4575) ──────────────────────────────────
  // One credential per click: PUT sends the decrypted value once (the server
  // seals its own copy), DELETE drops the server copy. Nothing here changes a
  // default — a credential starts unshared and stays that way unless a person
  // with the right role flips this switch.
  //
  // The plaintext is kept INSIDE enableAgentSharing(), never in state: what
  // this component holds before and after is only the API's boolean.
  const shareMutation = useMutation({
    mutationFn: async ({ secret, next }: { secret: SecretListItem; next: boolean }) => {
      if (next) {
        const cryptoKey = useSecretsStore.getState().cryptoKey;
        if (!cryptoKey) {
          throw new Error("Unlock the vault before sharing a credential with agents.");
        }
        await enableAgentSharing(secret.id, cryptoKey);
      } else {
        await disableAgentSharing(secret.id);
      }
    },
    onMutate: ({ secret }) => {
      setShareError(null);
      setSharingId(secret.id);
    },
    onSuccess: () => {
      // The list carries shared_with_agents, so refetch it for the truth.
      void queryClient.invalidateQueries({ queryKey });
    },
    onError: (err) => {
      setShareError(agentShareErrorMessage(err));
    },
    onSettled: () => setSharingId(null),
  });

  function handleToggleShare(secret: SecretListItem, next: boolean) {
    shareMutation.mutate({ secret, next });
  }

  const deleteErrorMessage =
    deleteMutation.error instanceof ApiError
      ? deleteMutation.error.message
      : deleteMutation.error instanceof Error
        ? deleteMutation.error.message
        : undefined;

  return (
    <section className="mt-8">
      {/* ── Header ─────────────────────────────────────────── */}
      <div className="mb-4 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <LockIcon className="h-5 w-5 text-amber-500" />
          <h2 className="text-lg font-semibold text-gray-900">Secrets</h2>
        </div>
        {isUnlocked && (
          <button
            type="button"
            onClick={() => setShowEditor(true)}
            className="inline-flex items-center gap-1 rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
          >
            <PlusIcon />
            Add Secret
          </button>
        )}
      </div>

      {/* ── Locked state ───────────────────────────────────── */}
      {!isUnlocked && (
        <div className="flex flex-col items-center justify-center rounded-lg border border-dashed border-gray-300 bg-gray-50 px-4 py-10 text-center">
          <LockIcon className="h-8 w-8 text-gray-400" />
          <p className="mt-3 text-sm font-medium text-gray-700">
            {needsSetup === true
              ? "Set up your secrets vault"
              : "Your secrets vault is locked"}
          </p>
          <p className="mt-1 max-w-sm text-xs text-gray-500">
            {needsSetup === true
              ? "Create a master password to encrypt the secrets you store for this item. You'll need it every time you unlock the vault."
              : "Enter your master password to unlock and view, add, or manage the secrets for this item."}
          </p>
          <button
            type="button"
            onClick={handleUnlockClick}
            className="mt-4 inline-flex items-center gap-1.5 rounded-md bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 transition-colors"
          >
            <LockIcon className="h-4 w-4 text-white" />
            {needsSetup === true ? "Set Up Secrets" : "Unlock Secrets"}
          </button>
        </div>
      )}

      {/* ── Unlocked: list ─────────────────────────────────── */}
      {isUnlocked && (
        <div className="space-y-2">
          {/* Loading */}
          {secretsQ.isLoading && (
            <div className="space-y-2">
              {[1, 2].map((i) => (
                <div
                  key={i}
                  className="h-14 animate-pulse rounded-lg bg-gray-100"
                />
              ))}
            </div>
          )}

          {/* Error */}
          {secretsQ.isError && (
            <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
              <p className="font-medium">Failed to load secrets.</p>
              <p className="mt-0.5 text-red-600">
                {secretsQ.error instanceof Error
                  ? secretsQ.error.message
                  : "Unknown error"}
              </p>
              <button
                type="button"
                onClick={() => secretsQ.refetch()}
                className="mt-2 inline-flex items-center rounded-md border border-red-300 bg-white px-3 py-1.5 text-xs font-semibold text-red-700 hover:bg-red-50 transition-colors"
              >
                Retry
              </button>
            </div>
          )}

          {/* Empty */}
          {!secretsQ.isLoading && !secretsQ.isError && secrets.length === 0 && (
            <p className="text-sm text-gray-400 italic">No secrets added.</p>
          )}

          {/* The sharing explainer, once there is something to share. It states
              the policy, not a control: sharing is per credential and off
              unless someone turns it on. */}
          {!secretsQ.isLoading && !secretsQ.isError && secrets.length > 0 && (
            <p className="flex items-start gap-2 rounded-token-md bg-accent-soft px-3 py-2 text-[13px] leading-snug text-ink-2">
              <AgentIcon className="mt-0.5 h-4 w-4 shrink-0 text-accent" />
              <span>
                <strong className="font-semibold text-ink">Shared with agents</strong> lets the
                family&apos;s assistants use one login when you ask them to. Every use shows in
                Activity. Leave banking and email off.
                {!canManageAgentSharing(role) && " Only an owner or family manager can change sharing."}
              </span>
            </p>
          )}

          {/* Populated. Cards at every width, deliberately: this section renders
              in a ~370 px column beside an entity's own details (see
              DetailPageLayout), not in the mockup's full-width settings pane, so
              the mockup's credentials TABLE does not fit — at 768 px its three
              columns wrap to one word per line. The mockup's row content is kept;
              its layout is not. One presentation also means one place to be
              right about the switch's target size. */}
          {secrets.length > 0 && (
            <ul
              className="m-0 list-none space-y-2 p-0"
              data-testid="secrets-cards"
            >
              {secrets.map((secret) => (
                <CredentialCard
                  key={secret.id}
                  secret={secret}
                  role={role}
                  sharingId={sharingId}
                  onOpen={() => setViewTarget(secret.id)}
                  onToggleShare={handleToggleShare}
                  onDelete={() => setDeleteTarget(secret)}
                  deleting={deleteMutation.isPending}
                />
              ))}
            </ul>
          )}

          {/* Sharing failure — plain words, below the list, announced. */}
          {shareError && (
            <p role="alert" className="rounded-token-md bg-warn-bg px-3 py-2 text-[13px] text-warn-ink">
              {shareError}
            </p>
          )}
        </div>
      )}

      {/* ── Master password prompt (setup OR unlock flow) ─────
          SecretsSection decides the mode by probing GET /api/v1/secrets/key
          on the first unlock click — 404 → setup (first-time master password
          creation), 200 → unlock (returning user). See `handleUnlockClick`
          and architecture/secrets-ui.md "Why the parent picks the mode". */}
      <MasterPasswordPrompt
        open={showUnlock}
        onClose={() => setShowUnlock(false)}
        mode={promptMode}
      />

      {/* ── Secret editor (create + edit modes) ────────────────
           SecretEditor handles its own list invalidation on success and
           calls onClose. We pass entityType/entityId so it POSTs the secret
           against this entity.

           Edit mode: when `editSecretId` is set (set by SecretViewer's
           onEdit callback), the editor opens in edit mode — it fetches,
           decrypts, and repopulates the form, then PATCHes on save. The
           `open` prop is derived so either create (showEditor) or edit
           (editSecretId !== null) opens the modal. onClose clears both so
           neither lingers and reopens stale. */}
      <SecretEditor
        open={showEditor || editSecretId !== null}
        onClose={() => {
          setShowEditor(false);
          setEditSecretId(null);
        }}
        entityType={entityType}
        entityId={entityId}
        editSecretId={editSecretId}
      />

      {/* ── Secret viewer (decrypt + display) ──────────────────
           onEdit hands control to SecretEditor: closing the viewer and
           opening the editor with the secret id in edit mode. */}
      <SecretViewer
        secretId={viewTarget}
        open={viewTarget !== null}
        onClose={() => setViewTarget(null)}
        onEdit={(id) => {
          setViewTarget(null);
          setEditSecretId(id);
        }}
      />

      {/* ── Delete confirmation ─────────────────────────────── */}
      <ConfirmDialog
        open={deleteTarget !== null}
        onClose={() => {
          if (!deleteMutation.isPending) setDeleteTarget(null);
        }}
        onConfirm={confirmDelete}
        title="Delete secret"
        message={
          deleteTarget
            ? `Are you sure you want to delete "${deleteTarget.name || "this secret"}"? This action cannot be undone.`
            : ""
        }
        loading={deleteMutation.isPending}
      />

      {/* Delete error — surfaced below the dialog so the user sees the
          failure even though the dialog stays open. */}
      {deleteErrorMessage && (
        <p role="alert" className="mt-2 text-xs text-red-600">
          {deleteErrorMessage}
        </p>
      )}
    </section>
  );
}
