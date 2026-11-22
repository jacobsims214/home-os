"use client";

/**
 * PaymentAccountsSection (#5506) — the accounts a family pays bills from, on the
 * Money page, mobile-first at 390 px.
 *
 * ## What this surface is for
 *
 * A bill can say "paid from Joint checking · 4821". This is where "Joint
 * checking" comes from, and where the bank login behind it lives.
 *
 *   name · institution · type · last-4     the masked identity, never more
 *   "Login stored"                         the credential fact (family only)
 *   Reveal                                  decrypts through the vault, in memory
 *
 * ## No field accepts an account number or a routing number
 *
 * The last-four field is the ONLY digit-bearing field on the form, and it is
 * refused the moment a longer number is pasted — before the request, so the
 * person is told what happened instead of being handed a failed save. There is
 * no field for a full number and none for a routing number: `payment_accounts`
 * has no column for either (075), and the UI must not pretend otherwise.
 *
 * `looksLikeFullNumber` warns on any OTHER field (notes, institution, name) too,
 * because the usual mistake is pasting the whole statement line into Notes. In
 * the last-four field it is a hard refusal; elsewhere it is a warning the person
 * can clear, since notes are free text.
 *
 * ## The credential never leaves the browser
 *
 * "Add login" opens the SAME SecretEditor the entity Resources tab uses: the
 * vault key is derived from the master password in memory and the payload is
 * encrypted with AES-GCM before POST /api/v1/secrets. What this component then
 * sends to attach it is the SECRET'S UUID (`PUT /payment-accounts/{id}/secret`).
 * "Reveal" opens the SAME SecretViewer, which decrypts with that in-memory key.
 *
 * Nothing here holds a plaintext in React state, writes one to storage, or puts
 * one in a request body — the ONE function that touches a plaintext is inside
 * SecretEditor/SecretViewer, which already own that responsibility.
 *
 * ## Role rules — reuse, never re-invent
 *
 * The API refuses the whole list to a role without money read, so this section
 * asks `useBillsAccess().canSee` for whether to draw at all (exactly as the
 * Money page does), and never inspects a role name itself. `login_stored` is
 * ABSENT for a role outside the credential policy and renders as nothing — not
 * as "no login" — so a viewer cannot tell the two apart.
 */

import { useState } from "react";
import { Badge, Card, Skeleton, Text } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconBuildingBank,
  IconKey,
  IconPlus,
  IconRefresh,
  IconTrash,
} from "@tabler/icons-react";
import {
  Button,
  ConfirmDialog,
  ResponsiveModal,
  ResponsiveModalActions,
  Select,
  TextInput,
} from "@/components/ui";
import MasterPasswordPrompt from "@/components/secrets/MasterPasswordPrompt";
import SecretEditor from "@/components/secrets/SecretEditor";
import SecretViewer from "@/components/secrets/SecretViewer";
import { probeNeedsSetup, useSecretsStore } from "@/stores/secrets";
import { useBillsAccess } from "@/lib/bills/api";
import {
  PAYMENT_ACCOUNT_TYPES,
  accountLabel,
  looksLikeFullNumber,
  useCreatePaymentAccount,
  useDeletePaymentAccount,
  useLinkAccountSecret,
  usePaymentAccounts,
  useUpdatePaymentAccount,
  validateLastFour,
  type PaymentAccount,
  type PaymentAccountInput,
  type PaymentAccountType,
} from "@/lib/payment-accounts/api";

/** entityType for a secret attached to a payment account. */
export const PAYMENT_ACCOUNT_ENTITY = "payment_account";

const EMPTY: PaymentAccountInput = {
  name: "",
  institution: "",
  account_type: "checking",
  last_four: "",
  notes: "",
};

function typeLabel(t: PaymentAccountType): string {
  return PAYMENT_ACCOUNT_TYPES.find((x) => x.value === t)?.label ?? t;
}

export default function PaymentAccountsSection() {
  const access = useBillsAccess();
  const list = usePaymentAccounts(access.canSee);
  const del = useDeletePaymentAccount();

  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<PaymentAccount | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<PaymentAccount | null>(null);

  // The vault: one flow, shared with every other credentials surface.
  const unlocked = useSecretsStore((s) => s.isUnlocked);
  const [unlockOpen, setUnlockOpen] = useState(false);
  const [unlockMode, setUnlockMode] = useState<"setup" | "unlock">("unlock");
  const [pendingAccount, setPendingAccount] = useState<PaymentAccount | null>(null);
  const [editorFor, setEditorFor] = useState<PaymentAccount | null>(null);
  const [viewerFor, setViewerFor] = useState<PaymentAccount | null>(null);

  const link = useLinkAccountSecret();

  // A role without money read is not shown this surface at all: the API would
  // refuse the list (403) and drawing an empty card would read as "you have no
  // accounts", which is a different and false statement.
  if (access.known && !access.canSee) return null;

  /**
   * openLogin is the gate for both adding and revealing. The vault must be
   * unlocked first — that is the only state in which a credential can be
   * encrypted or decrypted — and the prompt is the SAME MasterPasswordPrompt
   * the credentials tab uses, so there is one unlock UX, not two.
   */
  async function openLogin(account: PaymentAccount) {
    setPendingAccount(account);
    if (!unlocked) {
      // 404 on GET /secrets/key means this household has never set a master
      // password; the prompt then offers to create one instead of asking for a
      // password that cannot work.
      setUnlockMode((await probeNeedsSetup()) ? "setup" : "unlock");
      setUnlockOpen(true);
      return;
    }
    if (account.login_stored) setViewerFor(account);
    else setEditorFor(account);
  }

  /**
   * The prompt calls onCLOSE on success (it owns the store call), so the
   * continuation lives there. onClose is also the cancel path: a person who
   * gives up gets no editor and no viewer, which is the correct outcome.
   */
  function closePrompt() {
    setUnlockOpen(false);
    const account = pendingAccount;
    setPendingAccount(null);
    if (!account || !useSecretsStore.getState().isUnlocked) return;
    if (account.login_stored) setViewerFor(account);
    else setEditorFor(account);
  }

  /**
   * onSecretSaved attaches the credential the editor just encrypted. The
   * editor hands back the saved secret's id — the ciphertext's handle — and
   * this is the only thing the link call carries.
   */
  async function onSecretSaved(secretId: string) {
    const account = editorFor;
    setEditorFor(null);
    if (!account) return;
    try {
      await link.mutateAsync({ accountId: account.id, secretId });
      notifications.show({
        color: "green",
        title: "Login stored",
        message: `The bank login for ${accountLabel(account)} is saved in the vault.`,
      });
    } catch {
      notifications.show({
        color: "red",
        title: "Could not attach the login",
        message: "The credential was saved, but this account was not linked to it. Try again from the account.",
      });
    }
  }

  return (
    <section aria-labelledby="payment-accounts-heading" className="mt-6">
      <div className="mb-2 flex items-center justify-between gap-2">
        <h2 id="payment-accounts-heading" className="text-lg font-semibold">
          Payment accounts
        </h2>
        {access.canWrite && (
          <Button
            size="sm"
            leftSection={<IconPlus size={16} />}
            onClick={() => {
              setEditing(null);
              setFormOpen(true);
            }}
          >
            Add account
          </Button>
        )}
      </div>

      {list.isLoading && <Skeleton height={96} radius="md" />}

      {list.isError && (
        <Card padding="md" withBorder role="alert">
          <Text size="sm" fw={500}>
            Couldn&apos;t load your payment accounts
          </Text>
          <Button
            mt="sm"
            variant="light"
            size="sm"
            leftSection={<IconRefresh size={16} />}
            onClick={() => void list.refetch()}
          >
            Try again
          </Button>
        </Card>
      )}

      {list.data && list.data.length === 0 && (
        <Card padding="md" withBorder>
          <Text size="sm" c="dimmed">
            No payment accounts yet. Add the account a bill is paid from and Home OS
            keeps only its last four digits — never the full number.
          </Text>
        </Card>
      )}

      <ul className="flex flex-col gap-2">
        {(list.data ?? []).map((a) => (
          <li key={a.id}>
            <Card padding="md" withBorder>
              <div className="flex items-start gap-3">
                <IconBuildingBank size={22} aria-hidden className="mt-0.5 shrink-0" />
                <div className="min-w-0 flex-1">
                  {/* The masked identity, in one place: name · last-4. */}
                  <Text fw={600} className="[overflow-wrap:anywhere]">
                    {accountLabel(a)}
                  </Text>
                  <Text size="sm" c="dimmed" className="[overflow-wrap:anywhere]">
                    {[a.institution, typeLabel(a.account_type)].filter(Boolean).join(" · ")}
                  </Text>

                  {/*
                    The credential fact. `undefined` means the API withheld it
                    (a role outside the credential policy) and NOTHING renders:
                    "no login" and "not allowed to know" must not look alike.
                  */}
                  {a.login_stored !== undefined && (
                    <div className="mt-2">
                      {a.login_stored ? (
                        <Badge color="green" variant="light" leftSection={<IconKey	size={12} />}>
                          Login stored
                        </Badge>
                      ) : (
                        <Text size="xs" c="dimmed">
                          No bank login stored
                        </Text>
                      )}
                    </div>
                  )}

                  {access.canWrite && (
                    <div className="mt-3 flex flex-wrap gap-2">
                      {/* One 44 px target per action; wrapping, never clipped. */}
                      <Button size="sm" variant="light" leftSection={<IconKey size={16} />}
                        onClick={() => void openLogin(a)}>
                        {a.login_stored ? "Reveal login" : "Add login"}
                      </Button>
                      <Button size="sm" variant="default"
                        onClick={() => {
                          setEditing(a);
                          setFormOpen(true);
                        }}>
                        Edit
                      </Button>
                      <Button size="sm" variant="subtle" color="red" leftSection={<IconTrash size={16} />}
                        onClick={() => setConfirmDelete(a)}>
                        Delete
                      </Button>
                    </div>
                  )}
                </div>
              </div>
            </Card>
          </li>
        ))}
      </ul>

      <AccountForm
        open={formOpen}
        onClose={() => setFormOpen(false)}
        account={editing}
      />

      <ConfirmDialog
        open={confirmDelete !== null}
        onClose={() => setConfirmDelete(null)}
        title="Delete this account?"
        confirmLabel="Delete account"
        loading={del.isPending}
        message={
          confirmDelete
            ? `${accountLabel(confirmDelete)} will be removed. Bills that were paid from it stay, and simply stop naming an account.`
            : ""
        }
        onConfirm={async () => {
          const a = confirmDelete;
          if (!a) return;
          try {
            await del.mutateAsync(a.id);
            notifications.show({ color: "green", title: "Account deleted", message: accountLabel(a) });
          } catch {
            notifications.show({
              color: "red",
              title: "Couldn't delete the account",
              message: "Nothing was changed. Try again.",
            });
          }
          setConfirmDelete(null);
        }}
      />

      <MasterPasswordPrompt open={unlockOpen} mode={unlockMode} onClose={closePrompt} />

      {/* The SAME credential editor the Resources tab uses; entity_type names
          the account, so the secret lists under it too. */}
      {editorFor && (
        <SecretEditor
          open
          onClose={() => setEditorFor(null)}
          entityType={PAYMENT_ACCOUNT_ENTITY}
          entityId={editorFor.id}
          onSaved={onSecretSaved}
        />
      )}

      {viewerFor && (
        <SecretViewer
          open
          secretId={viewerFor.id}
          onClose={() => setViewerFor(null)}
          onEdit={() => {
            const a = viewerFor;
            setViewerFor(null);
            if (a) setEditorFor(a);
          }}
        />
      )}
    </section>
  );
}

/**
 * AccountForm is the add/edit sheet.
 *
 * THE LAST-FOUR RULE IS ENFORCED HERE FIRST, and the message says what arrived.
 * A paste of a full number is caught on change (so the person sees the warning
 * while the paste is still on their mind) AND on submit (so a form filled by
 * autofill or a keyboard cannot skip it). The save button is disabled while the
 * value is invalid: there is no path from "pasted 123456789012" to a request.
 */
function AccountForm({
  open,
  onClose,
  account,
}: {
  open: boolean;
  onClose: () => void;
  account: PaymentAccount | null;
}) {
  const create = useCreatePaymentAccount();
  const update = useUpdatePaymentAccount(account?.id ?? "");
  const [form, setForm] = useState<PaymentAccountInput>(EMPTY);
  const [error, setError] = useState("");

  // Reset the fields each time the sheet opens, so an edit never shows the
  // previous account's name (the sheet is one instance for both modes).
  const key = account?.id ?? "new";
  const [seeded, setSeeded] = useState("");
  if (open && seeded !== key) {
    setSeeded(key);
    setForm(
      account
        ? {
            name: account.name,
            institution: account.institution ?? "",
            account_type: account.account_type,
            last_four: account.last_four ?? "",
            notes: account.notes ?? "",
          }
        : EMPTY,
    );
    setError("");
  }

  const lastFourError = validateLastFour(form.last_four);
  // A full number in a field that is not the last-four box (usually Notes,
  // where a person pastes the statement line) is a warning, not a refusal.
  const strayNumber =
    !lastFourError &&
    (looksLikeFullNumber(form.notes) || looksLikeFullNumber(form.institution) || looksLikeFullNumber(form.name));

  const saving = create.isPending || update.isPending;
  const canSave = form.name.trim() !== "" && lastFourError === "" && !saving;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (lastFourError) {
      setError(lastFourError);
      return;
    }
    if (!canSave) return;
    try {
      if (account) await update.mutateAsync(form);
      else await create.mutateAsync(form);
      notifications.show({
        color: "green",
        title: account ? "Account saved" : "Account added",
        message: `${form.name.trim()} is ready to be a bill's payment account.`,
      });
      onClose();
    } catch (err) {
      // The API's message is the authority (it holds the same last-four rule and
      // the DB CHECK under it), so show what it said rather than a generic line.
      setError(err instanceof Error && err.message ? err.message : "Couldn't save the account. Try again.");
    }
  }

  return (
    <ResponsiveModal
      opened={open}
      onClose={onClose}
      title={account ? "Edit payment account" : "Add payment account"}
    >
      <form onSubmit={submit}>
        <TextInput
          label="Name"
          description="What your family calls it — “Joint checking”."
          value={form.name}
          onChange={(e) => setForm({ ...form, name: e.currentTarget.value })}
          required
          data-autofocus
        />
        <TextInput
          label="Institution"
          placeholder="First National"
          mt="sm"
          value={form.institution}
          onChange={(e) => setForm({ ...form, institution: e.currentTarget.value })}
        />
        <Select
          label="Type"
          mt="sm"
          value={form.account_type}
          onChange={(v: string | null) =>
            setForm({ ...form, account_type: (v as PaymentAccountType) ?? "checking" })
          }
          data={PAYMENT_ACCOUNT_TYPES.map((t) => ({ value: t.value, label: t.label }))}
          allowDeselect={false}
        />
        {/*
          The ONLY digit field on the form. inputMode numeric is the phone
          keyboard; maxLength 8 (not 4) so a paste is SEEN and refused with a
          message rather than silently clipped by the browser, which would hide
          the mistake and store the wrong four digits.
        */}
        <TextInput
          label="Last four digits"
          description="Only the last four. Home OS never stores a full account number or a routing number."
          placeholder="4821"
          mt="sm"
          inputMode="numeric"
          autoComplete="off"
          maxLength={8}
          value={form.last_four}
          onChange={(e) => setForm({ ...form, last_four: e.currentTarget.value })}
          error={lastFourError || undefined}
          aria-describedby={lastFourError ? "last-four-error" : undefined}
        />
        <TextInput
          label="Notes"
          mt="sm"
          value={form.notes}
          onChange={(e) => setForm({ ...form, notes: e.currentTarget.value })}
        />

        {strayNumber && (
          <Card mt="sm" padding="sm" withBorder role="status">
            <div className="flex items-start gap-2">
              <IconAlertTriangle size={18} aria-hidden className="mt-0.5 shrink-0" />
              <Text size="sm">
                That looks like a full account or routing number. Home OS will keep it
                in this free-text field as you typed it, but it will never be usable as
                an account number — only the last four digits are ever stored as digits.
              </Text>
            </div>
          </Card>
        )}

        {error && (
          <Text mt="sm" size="sm" c="red" id="last-four-error" role="alert">
            {error}
          </Text>
        )}

        <ResponsiveModalActions>
          <Button variant="default" onClick={onClose} type="button">
            Cancel
          </Button>
          <Button type="submit" disabled={!canSave} loading={saving}>
            {account ? "Save" : "Add account"}
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}
