"use client";

/**
 * Loans list (#4552), mobile-first on the EntityList pattern
 * (architecture/entity-list-pattern.md) and matching the properties list
 * (#4553). The money facts the mockup asks for are rate, payment, due day and
 * balance.
 *
 * - Phone (< 768 px): stacked cards, each one link with a 44 px+ tap target;
 *   the money facts sit in a two-up strip under the name, so there is no fixed
 *   multi-column table and nothing clips.
 * - >= 768 px: a table in a horizontal scroller, first cell a real link.
 * - "Add loan" opens a ResponsiveModal (bottom sheet on phones) and POSTs
 *   /api/v1/loans. There is no /new route.
 *
 * A loan is about one property, vehicle or asset, or about the family itself
 * (#4704, domain model v2): the form sends the typed property_id / vehicle_id
 * / asset_id and never the pre-v2 entity_type / entity_id pair that migration
 * 056 dropped (KB architecture/linked-finances-ui.md).
 *
 * Money is not redacted on top of the API: GET /loans is granted to the same
 * roles as bills (auth.CanSeeMoney in roles.go) and the API answers 403 to
 * everyone else, so the redaction boundary stays the server's.
 */

import { useState, type FormEvent } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton, Stack, Table } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconChevronRight, IconCreditCard, IconPlus } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { loanKeys, propertyKeys } from "@/lib/query-keys";
import { ResponsiveModal, ResponsiveModalActions, Select, TextInput } from "@/components/ui";

interface Loan {
  id: string;
  name: string;
  property_id?: string | null;
  vehicle_id?: string | null;
  asset_id?: string | null;
  lender?: string | null;
  original_amount?: string | number | null;
  remaining_balance?: string | number | null;
  interest_rate?: string | number | null;
  term_months?: number | null;
  monthly_payment?: string | number | null;
  start_date?: string | null;
  due_day?: number | null;
  term_end?: string | null;
  account_number?: string | null;
  notes?: string | null;
}

interface Named {
  id: string;
  name?: string;
  year?: number | null;
  make?: string | null;
  model?: string | null;
}

const usd = (v: unknown, digits = 0): string | null => {
  if (v == null || v === "") return null;
  const n = Number(v);
  if (!Number.isFinite(n)) return null;
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", minimumFractionDigits: digits, maximumFractionDigits: digits });
};

const pct = (v: unknown): string | null => {
  if (v == null || v === "") return null;
  const n = Number(v);
  return Number.isFinite(n) ? `${n}%` : null;
};

/** A vehicle has no name: "2019 Honda Odyssey", as the vehicle pages call it. */
function entityName(e: Named): string {
  return e.name || [e.year, e.make, e.model].filter((x) => x != null && x !== "").join(" ") || "Untitled";
}

type SubjectOption = { value: string; label: string; group: string };

/** "property:<uuid>" / "vehicle:<uuid>" / "asset:<uuid>", or "" for family-level. */
function subjectValue(l: Loan): string {
  if (l.property_id) return `property:${l.property_id}`;
  if (l.vehicle_id) return `vehicle:${l.vehicle_id}`;
  if (l.asset_id) return `asset:${l.asset_id}`;
  return "";
}

function subjectLabel(value: string, options: SubjectOption[]): string {
  if (!value) return "Family";
  return options.find((o) => o.value === value)?.label ?? "Family";
}

/**
 * The subject part of a POST/PUT body: the typed FK the picker chose, the
 * other two cleared. An empty string clears a column on update (subject.Fields
 * documents that), so switching a loan from a property to a vehicle is one
 * change, not a stale second FK.
 */
function subjectBody(value: string): Record<string, string> {
  const [kind, id] = value.split(":");
  return {
    property_id: kind === "property" ? id : "",
    vehicle_id: kind === "vehicle" ? id : "",
    asset_id: kind === "asset" ? id : "",
  };
}

function LoanCard({ loan, subject }: { loan: Loan; subject: string }) {
  const balance = usd(loan.remaining_balance ?? loan.original_amount);
  const rate = pct(loan.interest_rate);
  const payment = usd(loan.monthly_payment, 2);
  const sub = [loan.lender, subject].filter(Boolean).join(" · ");
  const facts = [
    balance && { label: loan.remaining_balance != null && loan.remaining_balance !== "" ? "Balance" : "Amount", value: balance },
    rate && { label: "Rate", value: rate },
    payment && { label: "Payment", value: payment },
    loan.due_day ? { label: "Due", value: `Day ${loan.due_day}` } : null,
  ].filter(Boolean) as { label: string; value: string }[];

  return (
    <Link
      href={`/dashboard/loans/${loan.id}`}
      data-loan-card={loan.id}
      className="block min-h-11 rounded-token-md border border-line bg-surface shadow-token-sm transition-colors hover:bg-surface-2 active:bg-surface-2"
    >
      <div className="flex items-center gap-3 p-4">
        <span aria-hidden className="grid size-11 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent">
          <IconCreditCard size={22} stroke={1.75} />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">{loan.name}</h3>
          {sub && <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{sub}</p>}
        </div>
        <IconChevronRight size={20} stroke={1.75} className="shrink-0 text-ink-3" aria-hidden />
      </div>
      {facts.length > 0 && (
        <dl className="grid grid-cols-2 border-t border-line">
          {facts.map((fact, i) => (
            <div
              key={fact.label}
              className={`min-w-0 px-4 py-2.5 ${i % 2 === 1 ? "border-l border-line" : ""} ${i > 1 ? "border-t border-line" : ""}`}
            >
              <dt className="text-xs text-ink-3">{fact.label}</dt>
              <dd className="text-[15px] font-semibold tabular-nums text-ink [overflow-wrap:anywhere]">{fact.value}</dd>
            </div>
          ))}
        </dl>
      )}
    </Link>
  );
}

const EMPTY = {
  name: "",
  lender: "",
  subject: "",
  original_amount: "",
  remaining_balance: "",
  interest_rate: "",
  monthly_payment: "",
  due_day: "",
  term_months: "",
  start_date: "",
};

function AddLoanModal({ opened, onClose, options }: { opened: boolean; onClose: () => void; options: SubjectOption[] }) {
  const queryClient = useQueryClient();
  const [values, setValues] = useState<Record<string, string>>(EMPTY);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) => apiFetch<{ data: { id: string } }>("/api/v1/loans", { method: "POST", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: loanKeys.all });
      // EntityList's and LinkedFinances' caches, and the property/vehicle a
      // loan feeds derived money fields into (#4704).
      queryClient.invalidateQueries({ queryKey: ["loans"] });
      queryClient.invalidateQueries({ queryKey: propertyKeys.all });
      notifications.show({ message: "Loan added", color: "teal" });
      close();
    },
  });

  const close = () => {
    setValues(EMPTY);
    setErrors({});
    create.reset();
    onClose();
  };

  const set = (name: string, value: string) => {
    setValues((prev) => ({ ...prev, [name]: value }));
    if (errors[name]) {
      setErrors((prev) => {
        const next = { ...prev };
        delete next[name];
        return next;
      });
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    const name = values.name.trim();
    if (!name) errs.name = "Enter a name";
    // Money goes as NUMERIC strings (the API keeps them as strings); the term
    // and the due day are whole numbers.
    const money = (key: string) => {
      const raw = values[key].replace(/[$,\s]/g, "");
      if (raw === "") return undefined;
      if (!/^\d+(\.\d+)?$/.test(raw)) {
        errs[key] = "Enter a number";
        return undefined;
      }
      return raw;
    };
    const whole = (key: string, min: number, max: number) => {
      const raw = values[key].trim();
      if (raw === "") return undefined;
      const n = Number(raw);
      if (!Number.isInteger(n) || n < min || n > max) {
        errs[key] = `Whole number ${min}–${max}`;
        return undefined;
      }
      return n;
    };
    const original = money("original_amount");
    const remaining = money("remaining_balance");
    const monthly = money("monthly_payment");
    const rate = money("interest_rate");
    if (rate !== undefined && Number(rate) >= 100) errs.interest_rate = "Under 100";
    const term = whole("term_months", 1, 1200);
    const dueDay = whole("due_day", 1, 31);
    if (Object.keys(errs).length) return setErrors(errs);

    const body: Record<string, unknown> = { name, ...subjectBody(values.subject) };
    if (values.lender.trim()) body.lender = values.lender.trim();
    if (original !== undefined) body.original_amount = original;
    if (remaining !== undefined || original !== undefined) body.remaining_balance = remaining ?? original;
    if (rate !== undefined) body.interest_rate = rate;
    if (monthly !== undefined) body.monthly_payment = monthly;
    if (term !== undefined) body.term_months = term;
    if (dueDay !== undefined) body.due_day = dueDay;
    if (values.start_date) body.start_date = values.start_date;
    create.mutate(body);
  };

  const field = (name: string, label: string, extra: Record<string, unknown> = {}) => (
    <TextInput
      label={label}
      value={values[name]}
      error={errors[name]}
      onChange={(e) => set(name, e.currentTarget.value)}
      {...extra}
    />
  );
  const grouped = Array.from(new Set(options.map((o) => o.group))).map((group) => ({
    group,
    items: options.filter((o) => o.group === group).map(({ value, label }) => ({ value, label })),
  }));

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add loan">
      <form onSubmit={submit} noValidate>
        <Stack gap="sm">
          {field("name", "Name", { required: true, "data-autofocus": true, placeholder: "e.g. First Mortgage" })}
          {field("lender", "Lender", { placeholder: "e.g. Wells Fargo" })}
          <Select
            label="About"
            data={[{ group: "Family", items: [{ value: "family", label: "The whole family" }] }, ...grouped]}
            value={values.subject || "family"}
            onChange={(v) => set("subject", v === "family" ? "" : v ?? "")}
            allowDeselect={false}
            searchable={options.length > 8}
            comboboxProps={{ withinPortal: true }}
          />
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {field("original_amount", "Amount borrowed", { inputMode: "decimal", placeholder: "$" })}
            {field("remaining_balance", "Balance owed", { inputMode: "decimal", placeholder: "Same as amount" })}
            {field("interest_rate", "Rate (%)", { inputMode: "decimal" })}
            {field("monthly_payment", "Monthly payment", { inputMode: "decimal", placeholder: "$" })}
            {field("due_day", "Due day of month", { inputMode: "numeric", placeholder: "1–31" })}
            {field("term_months", "Term (months)", { inputMode: "numeric" })}
          </div>
          {field("start_date", "Start date", { type: "date" })}
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

export default function LoansPage() {
  const [showAdd, setShowAdd] = useState(false);

  const loansQuery = useQuery({
    queryKey: loanKeys.lists(),
    queryFn: () => apiFetch<{ data: Loan[] }>("/api/v1/loans"),
  });
  const propertiesQuery = useQuery({
    queryKey: propertyKeys.lists(),
    queryFn: () => apiFetch<{ data: Named[] }>("/api/v1/properties"),
  });
  const vehiclesQuery = useQuery({
    queryKey: ["vehicles", "list"],
    queryFn: () => apiFetch<{ data: Named[] }>("/api/v1/vehicles"),
  });
  const assetsQuery = useQuery({
    queryKey: ["assets", "list"],
    queryFn: () => apiFetch<{ data: Named[] }>("/api/v1/assets"),
  });

  const loans = loansQuery.data?.data ?? [];
  const options: SubjectOption[] = [
    ...(propertiesQuery.data?.data ?? []).map((p) => ({ value: `property:${p.id}`, label: entityName(p), group: "Properties" })),
    ...(vehiclesQuery.data?.data ?? []).map((v) => ({ value: `vehicle:${v.id}`, label: entityName(v), group: "Vehicles" })),
    ...(assetsQuery.data?.data ?? []).map((a) => ({ value: `asset:${a.id}`, label: entityName(a), group: "Assets" })),
  ];
  const subjectOf = (l: Loan) => subjectLabel(subjectValue(l), options);

  const total = loans.reduce((sum, l) => sum + (Number(l.remaining_balance ?? l.original_amount) || 0), 0);

  return (
    <div className="mx-auto w-full max-w-6xl">
      <div className="mb-4 md:mb-6">
        <div className="flex items-center justify-between gap-3">
          <h2 className="min-w-0 text-xl font-semibold text-ink md:text-2xl">Loans</h2>
          <Button onClick={() => setShowAdd(true)} leftSection={<IconPlus size={16} />} aria-haspopup="dialog" className="max-md:min-h-11">
            Add loan
          </Button>
        </div>
        <p className="mt-1 text-sm text-ink-3">Mortgages, vehicle loans and other debts</p>
      </div>

      {!loansQuery.isLoading && !loansQuery.isError && loans.length > 0 && (
        <div className="mb-4 flex items-center gap-4 rounded-token-md border border-line bg-surface p-4 shadow-token-sm md:mb-6 md:w-fit md:pr-8">
          <div className="min-w-0">
            <p className="text-xs text-ink-3">Balance owed</p>
            <p className="text-2xl font-semibold tabular-nums text-ink [overflow-wrap:anywhere]">{usd(total)}</p>
          </div>
          <div className="h-10 w-px bg-line" aria-hidden />
          <div>
            <p className="text-xs text-ink-3">Loans</p>
            <p className="text-lg font-semibold tabular-nums text-ink-2">{loans.length}</p>
          </div>
        </div>
      )}

      {loansQuery.isLoading && (
        <div aria-busy="true" aria-label="Loading loans" className="space-y-2">
          {[1, 2, 3].map((i) => (
            <div key={i} className="space-y-2 rounded-token-md border border-line bg-surface p-4 shadow-token-sm">
              <Skeleton height={14} width="55%" radius="sm" />
              <Skeleton height={12} width="80%" radius="sm" />
            </div>
          ))}
        </div>
      )}

      {loansQuery.isError && (
        <div role="alert" className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center">
          <IconAlertTriangle size={20} stroke={1.75} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">Couldn&apos;t load loans. Check your connection and try again.</p>
          <Button variant="default" onClick={() => loansQuery.refetch()} loading={loansQuery.isRefetching} className="max-md:min-h-11">
            Try again
          </Button>
        </div>
      )}

      {!loansQuery.isLoading && !loansQuery.isError && loans.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <p className="text-sm text-ink-3">No loans yet. Add a mortgage, a vehicle loan or any other debt.</p>
          <Button variant="light" onClick={() => setShowAdd(true)} leftSection={<IconPlus size={16} />} className="max-md:min-h-11">
            Add loan
          </Button>
        </div>
      )}

      {!loansQuery.isLoading && !loansQuery.isError && loans.length > 0 && (
        <>
          <ul aria-label="Loans" className="space-y-2 md:hidden">
            {loans.map((loan) => (
              <li key={loan.id} className="min-w-0">
                <LoanCard loan={loan} subject={subjectOf(loan)} />
              </li>
            ))}
          </ul>

          <div data-loan-table className="overflow-x-auto rounded-token-md border border-line bg-surface shadow-token-sm max-md:hidden">
            <Table highlightOnHover verticalSpacing="sm" horizontalSpacing="md" className="min-w-[44rem]">
              <Table.Thead>
                <Table.Tr>
                  {["Name", "About", "Balance", "Rate", "Payment", "Due"].map((label, i) => (
                    <Table.Th
                      key={label}
                      scope="col"
                      className={`whitespace-nowrap text-xs font-semibold uppercase tracking-wide text-ink-3 ${i >= 2 ? "text-right" : "text-left"}`}
                    >
                      {label}
                    </Table.Th>
                  ))}
                  <Table.Th className="w-10">
                    <span className="sr-only">Open</span>
                  </Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {loans.map((loan) => (
                  <Table.Tr key={loan.id}>
                    <Table.Td className="min-w-[12rem] max-w-[22rem] [overflow-wrap:anywhere]">
                      <Link href={`/dashboard/loans/${loan.id}`} className="font-medium text-ink hover:underline">
                        {loan.name}
                      </Link>
                      {loan.lender && <span className="block text-xs text-ink-3 [overflow-wrap:anywhere]">{loan.lender}</span>}
                    </Table.Td>
                    <Table.Td className="whitespace-nowrap text-ink-2">{subjectOf(loan)}</Table.Td>
                    <Table.Td className="whitespace-nowrap text-right tabular-nums text-ink">
                      {usd(loan.remaining_balance ?? loan.original_amount) ?? "—"}
                    </Table.Td>
                    <Table.Td className="whitespace-nowrap text-right tabular-nums text-ink-2">{pct(loan.interest_rate) ?? "—"}</Table.Td>
                    <Table.Td className="whitespace-nowrap text-right tabular-nums text-ink-2">{usd(loan.monthly_payment, 2) ?? "—"}</Table.Td>
                    <Table.Td className="whitespace-nowrap text-right tabular-nums text-ink-2">{loan.due_day ? `Day ${loan.due_day}` : "—"}</Table.Td>
                    <Table.Td className="text-ink-3">
                      <IconChevronRight size={18} stroke={1.75} aria-hidden />
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </div>
        </>
      )}

      <AddLoanModal opened={showAdd} onClose={() => setShowAdd(false)} options={options} />
    </div>
  );
}
