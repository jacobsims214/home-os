"use client";

/**
 * LinkedFinances (#4706): the loans and bills about one property or vehicle,
 * read-only, each linking to its own page, plus "Add loan" / "Add bill" forms
 * (ResponsiveModal) that create one with the subject's property_id or
 * vehicle_id already set.
 *
 * This replaces the old mortgage/tax/insurance/HOA and lender/loan/
 * registration/insurance inputs: those values now live in the loan and bills
 * (KB apis/property-vehicle-finance.md), so they are edited there. Adding
 * stays on this page; the list and the subject's derived fields refresh.
 */

import { useState, type FormEvent } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton, Stack } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconChevronRight, IconPlus } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { billKeys, loanKeys, propertyKeys } from "@/lib/query-keys";
import { ResponsiveModal, ResponsiveModalActions, Select, TextInput } from "@/components/ui";
import {
  BILL_CATEGORIES,
  BILL_PRESETS,
  FREQUENCIES,
  frequencyLabel,
  isLinkedTo,
  monthlyBillAmount,
  newBillBody,
  newLoanBody,
  type LinkedBill,
  type LinkedLoan,
  type Subject,
} from "./linked-finance-utils";

const usd = (v: unknown, digits = 0) => {
  const n = Number(v);
  if (v == null || v === "" || !Number.isFinite(n)) return null;
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", minimumFractionDigits: digits, maximumFractionDigits: digits });
};

/** Query keys, shared with the property page so one fetch serves both. */
export const linkedLoansKey = (s: Pick<Subject, "kind" | "id">) => [...loanKeys.all, s.kind, s.id] as const;
export const linkedBillsKey = (s: Pick<Subject, "kind" | "id">) => [...billKeys.all, s.kind, s.id] as const;

export function useLinkedFinances(subject: Pick<Subject, "kind" | "id">) {
  const s = { ...subject, name: "" };
  const loans = useQuery({
    queryKey: linkedLoansKey(subject),
    queryFn: () =>
      apiFetch<{ data: LinkedLoan[] }>("/api/v1/loans", { params: { [`${subject.kind}_id`]: subject.id } }).then((r) =>
        (r.data ?? []).filter((l) => isLinkedTo(l, s)),
      ),
    enabled: !!subject.id,
  });
  // GET /bills has no subject filter; filter here.
  const bills = useQuery({
    queryKey: linkedBillsKey(subject),
    queryFn: () => apiFetch<{ data: LinkedBill[] }>("/api/v1/bills").then((r) => (r.data ?? []).filter((b) => isLinkedTo(b, s))),
    enabled: !!subject.id,
  });
  return { loans, bills };
}

const ROW =
  "flex min-h-11 items-center gap-3 rounded-token-sm px-2 py-2 -mx-2 transition-colors hover:bg-surface-2 active:bg-surface-2";

function Row({ href, title, detail, amount, amountNote }: { href: string; title: string; detail: string; amount: string | null; amountNote?: string }) {
  return (
    <li>
      <Link href={href} className={ROW}>
        <span className="min-w-0 flex-1">
          <span className="block text-sm font-medium text-ink [overflow-wrap:anywhere]">{title}</span>
          {detail && <span className="block text-xs text-ink-3 [overflow-wrap:anywhere]">{detail}</span>}
        </span>
        {amount && (
          <span className="shrink-0 text-right">
            <span className="block text-sm font-medium tabular-nums text-ink">{amount}</span>
            {amountNote && <span className="block text-xs text-ink-3">{amountNote}</span>}
          </span>
        )}
        <IconChevronRight size={18} stroke={1.75} className="shrink-0 text-ink-3" aria-hidden />
      </Link>
    </li>
  );
}

function loanDetail(l: LinkedLoan): string {
  const rate = l.interest_rate != null && l.interest_rate !== "" ? `${Number(l.interest_rate)}%` : "";
  const term = l.term_months ? `${l.term_months} mo` : "";
  const pay = usd(l.monthly_payment, 2);
  return [l.lender, rate, term, pay && `${pay}/mo`].filter(Boolean).join(" · ");
}

function billDetail(b: LinkedBill): string {
  const due =
    (b.frequency || "monthly") === "monthly"
      ? b.due_day
        ? `due day ${b.due_day}`
        : ""
      : b.due_date
        ? `next due ${new Date(`${b.due_date.slice(0, 10)}T00:00:00`).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })}`
        : "";
  return [frequencyLabel(b.frequency), b.category, due].filter(Boolean).join(" · ");
}

/**
 * framed: its own card (the property page). Otherwise bare, for a section
 * that already has a "Loans & bills" title (the vehicle's EntityDetail); then
 * the heading is left out.
 */
export default function LinkedFinances({ subject, framed = false }: { subject: Subject; framed?: boolean }) {
  const { loans, bills } = useLinkedFinances(subject);
  const [adding, setAdding] = useState<"loan" | "bill" | null>(null);
  const loanRows = loans.data ?? [];
  const billRows = bills.data ?? [];
  const loading = loans.isLoading || bills.isLoading;
  const failed = loans.isError || bills.isError;

  const monthly =
    loanRows.reduce((sum, l) => sum + (Number(l.monthly_payment) || 0), 0) +
    billRows.reduce((sum, b) => sum + monthlyBillAmount(b), 0);

  const what = subject.kind === "property" ? "mortgage, property tax, insurance and HOA" : "loan, registration and insurance";

  const body = (
    <div data-linked-finances={subject.kind} className="min-w-0">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          {framed && <h2 className="text-sm font-semibold text-ink">Loans &amp; bills</h2>}
          <p className={`text-xs text-ink-3 ${framed ? "mt-0.5" : ""}`}>The {what} for this {subject.kind}. Open one to change it.</p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="default" size="sm" leftSection={<IconPlus size={16} />} onClick={() => setAdding("loan")} aria-haspopup="dialog" className="max-md:min-h-11">
            Add loan
          </Button>
          <Button variant="default" size="sm" leftSection={<IconPlus size={16} />} onClick={() => setAdding("bill")} aria-haspopup="dialog" className="max-md:min-h-11">
            Add bill
          </Button>
        </div>
      </div>

      {loading && (
        <Stack gap={6} mt="sm">
          <Skeleton height={44} radius="sm" />
          <Skeleton height={44} radius="sm" />
        </Stack>
      )}
      {failed && !loading && (
        <p role="alert" className="mt-3 text-sm text-danger">
          Couldn&apos;t load the loans and bills.
        </p>
      )}
      {!loading && !failed && loanRows.length === 0 && billRows.length === 0 && (
        <p className="mt-3 rounded-token-sm border border-dashed border-line px-3 py-4 text-center text-sm text-ink-3">
          No loans or bills linked to this {subject.kind} yet.
        </p>
      )}

      {!loading && loanRows.length > 0 && (
        <div className="mt-3">
          <h3 className="text-xs font-medium uppercase tracking-wide text-ink-3">Loans</h3>
          <ul className="mt-1 divide-y divide-line" aria-label="Loans">
            {loanRows.map((l) => (
              <Row
                key={l.id}
                href={`/dashboard/loans/${l.id}`}
                title={l.name}
                detail={loanDetail(l)}
                amount={usd(l.remaining_balance ?? l.original_amount)}
                amountNote={l.remaining_balance != null && l.remaining_balance !== "" ? "balance" : "amount"}
              />
            ))}
          </ul>
        </div>
      )}

      {!loading && billRows.length > 0 && (
        <div className="mt-3">
          <h3 className="text-xs font-medium uppercase tracking-wide text-ink-3">Bills</h3>
          <ul className="mt-1 divide-y divide-line" aria-label="Bills">
            {billRows.map((b) => (
              <Row key={b.id} href={`/dashboard/bills/${b.id}`} title={b.name} detail={billDetail(b)} amount={usd(b.amount, 2)} amountNote={frequencyLabel(b.frequency).toLowerCase()} />
            ))}
          </ul>
        </div>
      )}

      {!loading && monthly > 0 && (
        <div className="mt-2 flex justify-between border-t border-line pt-2 text-sm">
          <span className="text-ink-2">About per month</span>
          <span className="font-semibold tabular-nums text-ink">{usd(monthly, 2)}</span>
        </div>
      )}

      <AddLoanModal subject={subject} opened={adding === "loan"} onClose={() => setAdding(null)} />
      <AddBillModal subject={subject} opened={adding === "bill"} onClose={() => setAdding(null)} />
    </div>
  );

  if (!framed) return body;
  return <section className="rounded-token-md border border-line bg-surface p-5 shadow-token-sm">{body}</section>;
}

// ─── Add forms ────────────────────────────────────────────────

function useCreate(subject: Subject, kind: "loan" | "bill", onDone: () => void) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: Record<string, unknown>) => apiFetch<{ data: { id: string } }>(`/api/v1/${kind}s`, { method: "POST", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: kind === "loan" ? loanKeys.all : billKeys.all });
      queryClient.invalidateQueries({ queryKey: [kind] }); // EntityList's cache
      // The subject's derived finance fields (mortgage_amount, insurance_cost, ...).
      queryClient.invalidateQueries({ queryKey: subject.kind === "property" ? propertyKeys.detail(subject.id) : [subject.kind, subject.id] });
      notifications.show({ message: kind === "loan" ? "Loan added" : "Bill added", color: "teal" });
      onDone();
    },
  });
}

function useFormState(initial: () => Record<string, string>) {
  const [values, setValues] = useState<Record<string, string>>(initial);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const set = (name: string, value: string) => {
    setValues((prev) => ({ ...prev, [name]: value }));
    setErrors((prev) => {
      if (!prev[name]) return prev;
      const next = { ...prev };
      delete next[name];
      return next;
    });
  };
  return { values, setValues, errors, setErrors, set };
}

function ForLine({ subject }: { subject: Subject }) {
  return (
    <p className="text-sm text-ink-2">
      For <span className="font-medium text-ink">{subject.name}</span>
    </p>
  );
}

function AddLoanModal({ subject, opened, onClose }: { subject: Subject; opened: boolean; onClose: () => void }) {
  const suggested = `${subject.name} ${subject.kind === "property" ? "mortgage" : "loan"}`.trim();
  const f = useFormState(() => ({ name: suggested }));
  const close = () => {
    f.setValues({ name: suggested });
    f.setErrors({});
    create.reset();
    onClose();
  };
  const create = useCreate(subject, "loan", close);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const r = newLoanBody(subject, f.values);
    if (!r.ok) return f.setErrors(r.errors);
    create.mutate(r.body);
  };
  const field = (name: string, label: string, extra: Record<string, unknown> = {}) => (
    <TextInput label={label} value={f.values[name] ?? ""} error={f.errors[name]} onChange={(e) => f.set(name, e.currentTarget.value)} {...extra} />
  );

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add loan">
      <form onSubmit={submit} noValidate>
        <Stack gap="sm">
          <ForLine subject={subject} />
          {field("name", "Name", { required: true, "data-autofocus": true })}
          {field("lender", "Lender")}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {field("original_amount", "Amount borrowed", { inputMode: "decimal", placeholder: "$" })}
            {field("remaining_balance", "Balance owed", { inputMode: "decimal", placeholder: "Same as amount" })}
            {field("interest_rate", "Rate (%)", { inputMode: "decimal" })}
            {field("term_months", "Term (months)", { inputMode: "numeric" })}
            {field("monthly_payment", "Monthly payment", { inputMode: "decimal", placeholder: "$" })}
            {field("start_date", "Start date", { type: "date" })}
          </div>
          {create.isError && (
            <p role="alert" className="text-sm text-danger">
              Couldn&apos;t add the loan: {create.error instanceof Error ? create.error.message : "try again"}
            </p>
          )}
        </Stack>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Add loan
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

function AddBillModal({ subject, opened, onClose }: { subject: Subject; opened: boolean; onClose: () => void }) {
  const initial = () => ({ frequency: "monthly" });
  const f = useFormState(initial);
  const close = () => {
    f.setValues(initial());
    f.setErrors({});
    create.reset();
    onClose();
  };
  const create = useCreate(subject, "bill", close);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const r = newBillBody(subject, f.values);
    if (!r.ok) return f.setErrors(r.errors);
    create.mutate(r.body);
  };
  const monthly = (f.values.frequency || "monthly") === "monthly";
  const categories = BILL_CATEGORIES[subject.kind];

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add bill">
      <form onSubmit={submit} noValidate>
        <Stack gap="sm">
          <ForLine subject={subject} />
          <div role="group" aria-label="Quick pick" className="flex flex-wrap gap-2">
            {BILL_PRESETS[subject.kind].map((p) => (
              <button
                key={p.label}
                type="button"
                onClick={() =>
                  f.setValues((prev) => ({ ...prev, name: `${subject.name} ${p.suffix}`.trim(), category: p.category, frequency: p.frequency }))
                }
                className="min-h-11 rounded-full border border-line bg-surface px-4 text-sm text-ink-2 transition-colors hover:bg-surface-2 hover:text-ink md:min-h-9"
              >
                {p.label}
              </button>
            ))}
          </div>
          <TextInput
            label="Name"
            required
            data-autofocus
            value={f.values.name ?? ""}
            error={f.errors.name}
            onChange={(e) => f.set("name", e.currentTarget.value)}
          />
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <TextInput
              label="Amount"
              inputMode="decimal"
              placeholder="$"
              value={f.values.amount ?? ""}
              error={f.errors.amount}
              onChange={(e) => f.set("amount", e.currentTarget.value)}
            />
            <Select
              label="How often"
              data={FREQUENCIES}
              value={f.values.frequency || "monthly"}
              error={f.errors.frequency}
              onChange={(v) => f.set("frequency", v ?? "monthly")}
              allowDeselect={false}
              comboboxProps={{ withinPortal: true }}
            />
            {monthly ? (
              <TextInput
                label="Due day of month"
                inputMode="numeric"
                value={f.values.due_day ?? ""}
                error={f.errors.due_day}
                onChange={(e) => f.set("due_day", e.currentTarget.value)}
              />
            ) : (
              <TextInput label="Next due date" type="date" value={f.values.due_date ?? ""} onChange={(e) => f.set("due_date", e.currentTarget.value)} />
            )}
            <Select
              label="Category"
              data={categories.includes(f.values.category ?? "") || !f.values.category ? categories : [...categories, f.values.category!]}
              value={f.values.category || null}
              onChange={(v) => f.set("category", v ?? "")}
              clearable
              comboboxProps={{ withinPortal: true }}
            />
          </div>
          {create.isError && (
            <p role="alert" className="text-sm text-danger">
              Couldn&apos;t add the bill: {create.error instanceof Error ? create.error.message : "try again"}
            </p>
          )}
        </Stack>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Add bill
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}
