"use client";

/**
 * LinkedLogin (#5506) — "is a login stored, and how do I reach it?", for one
 * record, on a bill or on a vendor.
 *
 * ## Why this is one component and not two
 *
 * A bill's bank login (`payment_accounts.secret_id`) and a vendor's portal login
 * (`vendors.portal_secret_id`) are the same three facts and the same three
 * actions: a stored-or-not badge, a way to add one, and a way to reveal one
 * through the vault. Written twice they drift — and the drift would be in the
 * part that says whether a credential exists, which is exactly the thing the
 * role rule is about. The only difference is which LINK ROUTE attaches it, so
 * that is the one prop.
 *
 * ## The role rule, reused and not re-invented
 *
 * `stored` is `boolean | undefined` because the API OMITS the credential fact
 * for a role outside the credential policy (ResSecrets: family only) rather
 * than sending false. `undefined` renders NOTHING. That asymmetry is the point:
 * a viewer must not be able to tell "no login stored" from "you may not know".
 * This component never inspects a role name; it renders what the API was
 * willing to say.
 *
 * ## The plaintext never leaves the browser
 *
 * "Add" opens SecretEditor (PBKDF2 key in memory → AES-GCM → POST ciphertext)
 * and then sends the saved credential's UUID to the link route. "Reveal" opens
 * SecretViewer, which decrypts with the in-memory key. Neither this component
 * nor either call it makes carries a password.
 */

import { useState } from "react";
import { Badge, Text } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconEye, IconKey, IconPlus } from "@tabler/icons-react";
import { Button } from "@/components/ui";
import MasterPasswordPrompt from "@/components/secrets/MasterPasswordPrompt";
import SecretEditor from "@/components/secrets/SecretEditor";
import SecretViewer from "@/components/secrets/SecretViewer";
import { probeNeedsSetup, useSecretsStore } from "@/stores/secrets";
import type { MasterPasswordPromptMode } from "@/components/secrets/MasterPasswordPrompt";

export interface LinkedLoginProps {
  /**
   * Whether a credential is linked. `undefined` = the API withheld it, so
   * nothing about credentials is rendered at all.
   */
  stored: boolean | undefined;
  /** What the credential belongs to, for wording: "bank login", "portal login". */
  noun: string;
  /**
   * entityType for the secret when one is created here. Defaults to the record
   * kind so the credential also lists under that record's Resources tab.
   */
  entityType: string;
  entityId: string;
  /** Attaches (or detaches) the credential. Receives the secret's UUID. */
  onLink?: (secretId: string | null) => Promise<unknown>;
  /**
   * Whether the person may change it. The API decides for real (ResSecrets
   * write); this only avoids drawing a control that would 403.
   */
  canEdit?: boolean;
  /** The id of the already-linked secret, when the caller knows it. */
  secretId?: string | null;
}

export default function LinkedLogin({
  stored,
  noun,
  entityType,
  entityId,
  onLink,
  canEdit = false,
  secretId = null,
}: LinkedLoginProps) {
  const unlocked = useSecretsStore((s) => s.isUnlocked);
  const [promptOpen, setPromptOpen] = useState(false);
  const [mode, setMode] = useState<MasterPasswordPromptMode>("unlock");
  const [action, setAction] = useState<"add" | "reveal" | null>(null);
  const [editorOpen, setEditorOpen] = useState(false);
  const [viewerOpen, setViewerOpen] = useState(false);

  // The API withheld the credential fact: render nothing. Not "no login" —
  // that would answer a question this role is not allowed to ask.
  if (stored === undefined) return null;

  async function start(what: "add" | "reveal") {
    setAction(what);
    if (!unlocked) {
      setMode((await probeNeedsSetup()) ? "setup" : "unlock");
      setPromptOpen(true);
      return;
    }
    if (what === "add") setEditorOpen(true);
    else setViewerOpen(true);
  }

  /** The prompt closes on success AND on cancel; continue only if unlocked. */
  function onPromptClose() {
    setPromptOpen(false);
    if (!useSecretsStore.getState().isUnlocked) {
      setAction(null);
      return;
    }
    if (action === "add") setEditorOpen(true);
    else if (action === "reveal") setViewerOpen(true);
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      {stored ? (
        <Badge color="green" variant="light" leftSection={<IconKey size={12} />}>
          {noun} stored
        </Badge>
      ) : (
        <Text size="xs" c="dimmed">
          No {noun} stored
        </Text>
      )}

      {canEdit && !stored && (
        <Button size="sm" variant="light" leftSection={<IconPlus size={16} />} onClick={() => void start("add")}>
          Add {noun}
        </Button>
      )}
      {stored && (
        <Button size="sm" variant="light" leftSection={<IconEye size={16} />} onClick={() => void start("reveal")}>
          Reveal
        </Button>
      )}
      {canEdit && stored && onLink && (
        <Button
          size="sm"
          variant="subtle"
          color="red"
          onClick={async () => {
            try {
              await onLink(null);
              notifications.show({ color: "green", title: "Login unlinked", message: `This ${noun} is no longer linked.` });
            } catch {
              notifications.show({ color: "red", title: "Couldn't unlink", message: "Nothing was changed." });
            }
          }}
        >
          Unlink
        </Button>
      )}

      <MasterPasswordPrompt open={promptOpen} mode={mode} onClose={onPromptClose} />

      {editorOpen && (
        <SecretEditor
          open
          onClose={() => setEditorOpen(false)}
          entityType={entityType}
          entityId={entityId}
          onSaved={async (id) => {
            if (!onLink) return;
            try {
              await onLink(id);
              notifications.show({ color: "green", title: "Login stored", message: `The ${noun} is saved in the vault and linked.` });
            } catch {
              notifications.show({
                color: "red",
                title: "Saved, but not linked",
                message: `The credential is in your vault. Link it again from this page.`,
              });
            }
          }}
        />
      )}

      {viewerOpen && (secretId || stored) && (
        <SecretViewer
          open
          // Without the id we cannot fetch a specific blob; SecretViewer needs
          // one. A caller that shows this component for a linked credential
          // passes it, which is why the prop exists.
          secretId={secretId}
          onClose={() => setViewerOpen(false)}
        />
      )}
    </div>
  );
}
