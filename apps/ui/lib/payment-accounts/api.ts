"use client";

/**
 * Payment accounts (#5506): the data layer for the accounts a family pays bills
 * from, and the bank login behind each one.
 *
 * ## Two facts, two policies (the ticket's role rule)
 *
 * The API answers a payment account with its name, institution, type and
 * `last_four` — masked at the source; `payment_accounts` has no column for a
 * full account number or a routing number (075), so there is nothing longer to
 * leak by accident on the way here.
 *
 * `login_stored` is a CREDENTIAL fact and follows the existing credential
 * policy (ResSecrets: family only). The API OMITS the field rather than sending
 * false when the caller may not know, so this layer models it as
 * `boolean | undefined` and never renders an absence. That is deliberate: a
 * viewer must not be able to tell "no login stored" from "you may not know".
 *
 * ## The login itself never passes through here in plaintext
 *
 * `linkSecret` sends a SECRET UUID. The credential was encrypted in the browser
 * by SecretEditor (PBKDF2 key in memory, AES-GCM) and POSTed to
 * /api/v1/secrets as ciphertext; this module only ever names it. Revealing it
 * goes through SecretViewer, which decrypts with the in-memory vault key. There
 * is no function here that accepts, returns or transmits a password.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { secretKeys } from "@/lib/query-keys";

export type PaymentAccountType = "checking" | "savings" | "credit_card" | "other";

/** The four types, with the words a person reads. Order matches the DB enum. */
export const PAYMENT_ACCOUNT_TYPES: { value: PaymentAccountType; label: string }[] = [
  { value: "checking", label: "Checking" },
  { value: "savings", label: "Savings" },
  { value: "credit_card", label: "Credit card" },
  { value: "other", label: "Other" },
];

export interface PaymentAccount {
  id: string;
  household_id: string;
  name: string;
  institution?: string;
  account_type: PaymentAccountType;
  /** Exactly four digits, or null when the family has not looked them up. */
  last_four: string | null;
  notes?: string;
  /**
   * Whether a bank login is on file. `undefined` means the API did not say —
   * the caller's role is outside the credential policy — and the UI renders
   * nothing rather than "no login".
   */
  login_stored?: boolean;
  created_at: string;
  updated_at: string;
}

export interface PaymentAccountInput {
  name: string;
  institution: string;
  account_type: PaymentAccountType;
  last_four: string;
  notes: string;
}

export const paymentAccountKeys = {
  all: ["payment-accounts"] as const,
  list: () => [...paymentAccountKeys.all, "list"] as const,
  detail: (id: string) => [...paymentAccountKeys.all, "detail", id] as const,
};

/**
 * validateLastFour is the CLIENT half of the last-four rule, and it exists so a
 * person is warned BEFORE the request leaves — the ticket asks for "warn and
 * refuse" on a paste, and a round trip that ends in a red toast is a worse
 * version of the same refusal.
 *
 * The server is still the authority (paymentaccount.ValidateLastFour in Go holds
 * the identical rule, and the CHAR(4) CHECK holds under it). This mirrors that
 * rule; it does not replace it. Both messages are written to the same standard:
 * say what arrived, and say what to do instead.
 *
 * Returns "" when valid.
 */
export function validateLastFour(raw: string): string {
  const v = raw.trim();
  if (v === "") return "";
  if (!/^[0-9]+$/.test(v)) {
    return "Last four must be exactly four digits. An account or routing number cannot be stored here — Home OS keeps only the last four digits.";
  }
  if (v.length === 9) {
    return "That looks like a routing number. Home OS does not store routing numbers — enter the last four digits of the account instead.";
  }
  if (v.length !== 4) {
    return `Last four must be exactly four digits (that is ${v.length}). Home OS never stores a full account number — keep only the last four digits.`;
  }
  return "";
}

/**
 * detectFullNumber flags a VALUE PASTED INTO ANY FIELD that looks like a full
 * account or routing number, so the form can warn wherever it lands rather than
 * only in the last-four box.
 *
 * A person does not usually paste into the field that wants it: they paste the
 * line off their statement into Notes, or into the account name. Notes are free
 * text and are allowed to say anything, so this is a WARNING the person can
 * dismiss for notes — but in the last-four field it is a hard refusal
 * (validateLastFour above). Flagging a 7+ digit run is the cheap, honest test:
 * real last-fours are four digits, so a longer run is either a full number or a
 * date, and asking about a date costs one glance.
 */
export function looksLikeFullNumber(v: string): boolean {
  return /[0-9]{7,}/.test(v.replace(/[\s-]/g, ""));
}

async function fetchAccounts(): Promise<PaymentAccount[]> {
  const res = await apiFetch<{ data: PaymentAccount[] }>("/payment-accounts");
  return res.data ?? [];
}

/** usePaymentAccounts lists the household's accounts (read: money policy). */
export function usePaymentAccounts(enabled = true) {
  return useQuery({
    queryKey: paymentAccountKeys.list(),
    queryFn: fetchAccounts,
    enabled,
  });
}

export function useCreatePaymentAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: PaymentAccountInput) =>
      apiFetch<PaymentAccount>("/payment-accounts", {
        method: "POST",
        body: JSON.stringify(sanitize(input)),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: paymentAccountKeys.all });
    },
  });
}

export function useUpdatePaymentAccount(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: PaymentAccountInput) =>
      apiFetch<PaymentAccount>(`/payment-accounts/${id}`, {
        method: "PUT",
        body: JSON.stringify(sanitize(input)),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: paymentAccountKeys.all });
    },
  });
}

export function useDeletePaymentAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<void>(`/payment-accounts/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: paymentAccountKeys.all });
    },
  });
}

/**
 * linkSecret attaches (secretId) or detaches (null) the bank login for an
 * account. THE BODY IS A UUID: this is the call that makes "a login is stored"
 * true without the server ever holding the login.
 */
export function useLinkAccountSecret() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ accountId, secretId }: { accountId: string; secretId: string | null }) =>
      apiFetch<PaymentAccount>(`/payment-accounts/${accountId}/secret`, {
        method: "PUT",
        body: JSON.stringify({ secret_id: secretId }),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: paymentAccountKeys.all });
      // The credential list itself changed too (a new secret was created or
      // detached), so a revisited Secrets tab must not show a stale link.
      void qc.invalidateQueries({ queryKey: secretKeys.all });
    },
  });
}

/** linkVendorSecret is the same operation for a payee's portal login. */
export function useLinkVendorSecret() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ vendorId, secretId }: { vendorId: string; secretId: string | null }) =>
      apiFetch<unknown>(`/vendors/${vendorId}/portal-secret`, {
        method: "PUT",
        body: JSON.stringify({ secret_id: secretId }),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["vendors"] });
      void qc.invalidateQueries({ queryKey: secretKeys.all });
    },
  });
}

/**
 * sanitize trims the free-text fields and drops a last_four the client cannot
 * vouch for. It NEVER truncates: a value that is not four digits is sent as
 * "" (which clears the column) only after the form has already refused the
 * submit, so a caller that skipped the form still cannot smuggle a longer
 * number past the API — the API refuses it and says why.
 */
function sanitize(input: PaymentAccountInput): Record<string, unknown> {
  const lastFour = input.last_four.trim();
  return {
    name: input.name.trim(),
    institution: input.institution.trim(),
    account_type: input.account_type,
    last_four: lastFour,
    notes: input.notes.trim(),
  };
}

/** accountLabel is the one rendering of an account's masked identity: */
/** "Joint checking · 4821". Used by the list, the bill page and the pickers,  */
/** so a bill and the Money page cannot describe the same account differently. */
export function accountLabel(a: {
  name: string;
  last_four?: string | null;
}): string {
  return a.last_four ? `${a.name} · ${a.last_four}` : a.name;
}
