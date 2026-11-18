"use client";

/**
 * Loan detail (#4552), mobile-first, in the DetailPageLayout frame (#4531):
 * a sticky title bar with a 44 px back button and an actions menu on phones,
 * collapsible sections, and Resources. The money the mockup asks for — rate,
 * payment, due day, balance — is the first thing on the page, in a summary
 * strip that is 2 x 2 on a phone and one row from 768 px up (never a fixed
 * 2-column grid).
 *
 * A loan is about one property, vehicle or asset, or about the family itself
 * (#4704, domain model v2). The editor sets exactly one typed FK and clears
 * the other two; it never sends the pre-v2 entity_type / entity_id pair that
 * migration 056 dropped (KB architecture/linked-finances-ui.md), and the
 * response's derived legacy fields are ignored if they arrive.
 *
 * Money is not re-gated here: GET/PUT /loans are auth.CanSeeMoney's grant
 * (roles.go), so the API is the redaction boundary and a role outside it gets
 * 403 rather than a page full of blank amounts.
 */

import { useEffect, useState, type FormEvent } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Skeleton, Stack } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconExternalLink } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { loanKeys, propertyKeys } from "@/lib/query-keys";
import { Select, TextInput } from "@/components/ui";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import DetailPageLayout from "@/components/layout/DetailPageLayout";

// The response also carries the derived entity_type / entity_id; they are not
// in this shape on purpose, so nothing here can read them.
interface Loan {
  id: string;
  name: string;
  property_id?: string | null;
  vehicle_id?: string | null;
  asset_id?: string | null;
  lender?: string | null;
  original_amount?: string | null;
  remaining_balance?: string | null;
  interest_rate?: string | null;
  term_months?: number | null;
  monthly_payment?: string | null;
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

interface Option {
  value: string;
  label: string;
  group: string;
}

const usd = (v: unknown, digits = 0): string | null => {
  if (v == null || v === "") return null;
  const n = Number(v);
  if (!Number.isFinite(n)) return null;
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", minimumFractionDigits: digits, maximumFractionDigits: digits });
};

const pct = (v: unknown): string | null => (v == null || v === "" || !Number.isFinite(Number(v)) ? null : `${Number(v)}%`);

const fmtDate = (v: string | null | undefined): string | null => {
  if (!v) return null;
  const d = new Date(`${v.slice(0, 10)}T00:00:00`);
  return Number.isNaN(d.getTime()) ? v : d.toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });
};

const entityName = (e: Named): string =>
  e.name || [e.year, e.make, e.model].filter((x) => x != null && x !== "").join(" ") || "Untitled";

/** "property:<uuid>" / "vehicle:<uuid>" / "asset:<uuid>", or "" for family-level. */
function subjectValue(l: Loan): string {
  if (l.property_id) return `property:${l.property_id}`;
  if (l.vehicle_id) return `vehicle:${l.vehicle_id}`;
  if (l.asset_id) return `asset:${l.asset_id}`;
  return "";
}

/** The subject part of a PUT body: exactly one typed FK, the others cleared. */
function subjectBody(value: string): Record<string, string> {
  const [kind, id] = value.split(":");
  return {
    property_id: kind === "property" ? id : "",
    vehicle_id: kind === "vehicle" ? id : "",
    asset_id: kind === "asset" ? id : "",
  };
}

function subjectHref(value: string): string | null {
  const [kind, id] = value.split(":");
  if (!id || !["property", "vehicle", "asset"].includes(kind)) return null;
  return `/dashboard/${kind === "property" ? "properties" : `${kind}s`}/${id}`;
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 px-4 py-3">
      <dt className="text-xs text-ink-3">{label}</dt>
      <dd className="text-[17px] font-semibold tabular-nums text-ink [overflow-wrap:anywhere]">{value}</dd>
    </div>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1 py-2.5 md:grid md:grid-cols-[minmax(7rem,1fr)_minmax(0,2fr)] md:gap-x-6">
      <dt className="text-sm font-medium text-ink-3">{label}</dt>
      <dd className="m-0 whitespace-pre-line text-sm text-ink [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

const FIELD =
  "flex flex-col gap-3 rounded-token-md border border-line bg-surface p-4 shadow-token-sm md:block md:border-0 md:bg-transparent md:p-0 md:shadow-none";

export default function LoanDetailPage() {
  const params = useParams<{ id: string }>();
  const id = params.id;
  const router = useRouter();
  const queryClient = useQueryClient();

  const [isEditing, setIsEditing] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});

  const loanQuery = useQuery({
    queryKey: loanKeys.detail(id),
    queryFn: () => apiFetch<{ data: Loan }>(`/api/v1/loans/${id}`),
  });
  const propertiesQuery = useQuery({ queryKey: propertyKeys.lists(), queryFn: () => apiFetch<{ data: Named[] }>("/api/v1/properties") });
  const vehiclesQuery = useQuery({ queryKey: ["vehicles", "list"], queryFn: () => apiFetch<{ data: Named[] }>("/api/v1/vehicles") });
  const assetsQuery = useQuery({ queryKey: ["assets", "list"], queryFn: () => apiFetch<{ data: Named[] }>("/api/v1/assets") });

  const loan = loanQuery.data?.data ?? null;
  const options: Option[] = [
    ...(propertiesQuery.data?.data ?? []).map((p) => ({ value: `property:${p.id}`, label: entityName(p), group: "Properties" })),
    ...(vehiclesQuery.data?.data ?? []).map((v) => ({ value: `vehicle:${v.id}`, label: entityName(v), group: "Vehicles" })),
    ...(assetsQuery.data?.data ?? []).map((a) => ({ value: `asset:${a.id}`, label: entityName(a), group: "Assets" })),
  ];
  const subjectOptions = [{ group: "Family", items: [{ value: "family", label: "The whole family" }] }, ...Array.from(new Set(options.map((o) => o.group))).map((group) => ({
    group,
    items: options.filter((o) => o.group === group).map(({ value, label }) => ({ value, label })),
  }))];
  const subjectName = (value: string) =>
    !value ? "Family" : options.find((o) => o.value === value)?.label ?? "Family";

  // Seed the form from the record when it arrives, and again on each entry to
  // edit mode, so a cancelled edit never leaves half a change behind.
  useEffect(() => {
    if (!loan) return;
    setValues({
      name: loan.name ?? "",
      lender: loan.lender ?? "",
      subject: subjectValue(loan),
      original_amount: loan.original_amount ?? "",
      remaining_balance: loan.remaining_balance ?? "",
      interest_rate: loan.interest_rate ?? "",
      term_months: loan.term_months != null ? String(loan.term_months) : "",
      monthly_payment: loan.monthly_payment ?? "",
      due_day: loan.due_day != null ? String(loan.due_day) : "",
      start_date: loan.start_date?.slice(0, 10) ?? "",
      account_number: loan.account_number ?? "",
      notes: loan.notes ?? "",
    });
    setErrors({});
  }, [loan, isEditing]);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: loanKeys.all });
    queryClient.invalidateQueries({ queryKey: ["loans"] });
    // A loan feeds the property's and vehicle's derived money fields (#4704).
    queryClient.invalidateQueries({ queryKey: propertyKeys.all });
    queryClient.invalidateQueries({ queryKey: ["vehicles"] });
    queryClient.invalidateQueries({ queryKey: ["assets"] });
  };

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Loan }>(`/api/v1/loans/${id}`, { method: "PUT", body }),
    onSuccess: () => {
      invalidate();
      setIsEditing(false);
      notifications.show({ message: "Loan saved", color: "teal" });
    },
    onError: (e: Error) => notifications.show({ title: "Save failed", message: e.message, color: "red" }),
  });

  const remove = useMutation({
    mutationFn: () => apiFetch<void>(`/api/v1/loans/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      invalidate();
      router.push("/dashboard/loans");
    },
    onError: (e: Error) => notifications.show({ title: "Delete failed", message: e.message, color: "red" }),
  });

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
    const money = (key: string) => {
      const raw = (values[key] ?? "").replace(/[$,\s]/g, "");
      if (raw === "") return undefined;
      if (!/^\d+(\.\d+)?$/.test(raw)) {
        errs[key] = "Enter a number";
        return undefined;
      }
      return raw;
    };
    const whole = (key: string, min: number, max: number) => {
      const raw = (values[key] ?? "").trim();
      if (raw === "") return undefined;
      const n = Number(raw);
      if (!Number.isInteger(n) || n < min || n > max) {
        errs[key] = `Whole number ${min}–${max}`;
        return undefined;
      }
      return n;
    };
    const rate = money("interest_rate");
    if (rate !== undefined && Number(rate) >= 100) errs.interest_rate = "Under 100";
    const term = whole("term_months", 1, 1200);
    const dueDay = whole("due_day", 1, 31);
    if (Object.keys(errs).length) return setErrors(errs);

    // PUT replaces the loan, so every editable field is sent; null clears a
    // money/date value the person emptied.
    const body: Record<string, unknown> = {
      name,
      ...subjectBody(values.subject),
      lender: values.lender.trim() || null,
      original_amount: money("original_amount") ?? null,
      remaining_balance: money("remaining_balance") ?? null,
      interest_rate: rate ?? null,
      monthly_payment: money("monthly_payment") ?? null,
      start_date: values.start_date || null,
      notes: values.notes.trim() || null,
      // The 044 fields are only changed when sent; null clears one.
      due_day: dueDay ?? null,
      term_months: term ?? null,
      account_number: values.account_number.trim() || null,
    };
    save.mutate(body);
  };

  const title = loan?.name ?? "Loan";
  const subject = loan ? subjectValue(loan) : "";
  const href = subjectHref(subject);

  const view = loan && (
    <div className="flex flex-col gap-5">
      {(() => {
        const balance = usd(loan.remaining_balance ?? loan.original_amount);
        const rate = pct(loan.interest_rate);
        const payment = usd(loan.monthly_payment, 2);
        const due = loan.due_day ? `Day ${loan.due_day}` : null;
        const stats = [
          balance && { label: loan.remaining_balance != null && loan.remaining_balance !== "" ? "Balance" : "Amount", value: balance },
          rate && { label: "Rate", value: rate },
          payment && { label: "Monthly payment", value: payment },
          due && { label: "Due", value: due },
        ].filter(Boolean) as { label: string; value: string }[];
        if (stats.length === 0) return null;
        return (
          <dl data-loan-summary className="grid grid-cols-2 divide-x divide-y divide-line overflow-hidden rounded-token-md border border-line bg-surface-2/40 md:grid-cols-4 md:divide-y-0">
            {stats.map((s) => (
              <Stat key={s.label} label={s.label} value={s.value} />
            ))}
          </dl>
        );
      })()}

      <div>
        <h3 className="text-sm font-semibold text-ink-2">Loan</h3>
        <dl className="m-0 divide-y divide-line">
          <Row label="Lender">{loan.lender || "—"}</Row>
          <Row label="Amount borrowed">{usd(loan.original_amount, 2) ?? "—"}</Row>
          <Row label="Balance owed">{usd(loan.remaining_balance, 2) ?? "—"}</Row>
          {loan.account_number && <Row label="Account number">{loan.account_number}</Row>}
        </dl>
      </div>

      <div>
        <h3 className="text-sm font-semibold text-ink-2">Terms</h3>
        <dl className="m-0 divide-y divide-line">
          <Row label="Rate">{pct(loan.interest_rate) ?? "—"}</Row>
          <Row label="Term">{loan.term_months ? `${loan.term_months} months` : "—"}</Row>
          <Row label="Monthly payment">{usd(loan.monthly_payment, 2) ?? "—"}</Row>
          <Row label="Due day">{loan.due_day ? `Day ${loan.due_day} of the month` : "—"}</Row>
          <Row label="Start date">{fmtDate(loan.start_date) ?? "—"}</Row>
          <Row label="Payoff date">{fmtDate(loan.term_end) ?? "—"}</Row>
        </dl>
      </div>

      <div>
        <h3 className="text-sm font-semibold text-ink-2">About</h3>
        <dl className="m-0 divide-y divide-line">
          <Row label="Applies to">
            {href ? (
              <Link href={href} className="inline-flex min-h-6 items-center gap-1 text-accent hover:underline">
                {subjectName(subject)}
                <IconExternalLink size={14} stroke={1.75} aria-hidden />
              </Link>
            ) : (
              "The whole family"
            )}
          </Row>
          <Row label="Notes">{loan.notes || "—"}</Row>
        </dl>
      </div>
    </div>
  );

  const edit = (
    <form id="loan-edit" onSubmit={submit} noValidate>
      <Stack gap="md">
        <div className={FIELD}>
          <TextInput label="Name" required value={values.name ?? ""} error={errors.name} onChange={(e) => set("name", e.currentTarget.value)} />
          <TextInput label="Lender" value={values.lender ?? ""} onChange={(e) => set("lender", e.currentTarget.value)} />
          <Select
            label="About"
            data={subjectOptions}
            value={values.subject || "family"}
            onChange={(v) => set("subject", v === "family" ? "" : v ?? "")}
            allowDeselect={false}
            searchable={options.length > 8}
            comboboxProps={{ withinPortal: true }}
          />
        </div>
        <div className={FIELD}>
          <TextInput label="Amount borrowed" inputMode="decimal" placeholder="$" value={values.original_amount ?? ""} error={errors.original_amount} onChange={(e) => set("original_amount", e.currentTarget.value)} />
          <TextInput label="Balance owed" inputMode="decimal" placeholder="$" value={values.remaining_balance ?? ""} error={errors.remaining_balance} onChange={(e) => set("remaining_balance", e.currentTarget.value)} />
          <TextInput label="Account number" value={values.account_number ?? ""} onChange={(e) => set("account_number", e.currentTarget.value)} />
        </div>
        <div className={FIELD}>
          <TextInput label="Rate (%)" inputMode="decimal" value={values.interest_rate ?? ""} error={errors.interest_rate} onChange={(e) => set("interest_rate", e.currentTarget.value)} />
          <TextInput label="Monthly payment" inputMode="decimal" placeholder="$" value={values.monthly_payment ?? ""} error={errors.monthly_payment} onChange={(e) => set("monthly_payment", e.currentTarget.value)} />
          <TextInput label="Due day of month" inputMode="numeric" placeholder="1–31" value={values.due_day ?? ""} error={errors.due_day} onChange={(e) => set("due_day", e.currentTarget.value)} />
          <TextInput label="Term (months)" inputMode="numeric" value={values.term_months ?? ""} error={errors.term_months} onChange={(e) => set("term_months", e.currentTarget.value)} />
          <TextInput label="Start date" type="date" value={values.start_date ?? ""} onChange={(e) => set("start_date", e.currentTarget.value)} />
        </div>
        <div className={FIELD}>
          <TextInput label="Notes" value={values.notes ?? ""} onChange={(e) => set("notes", e.currentTarget.value)} />
        </div>
        {save.isError && (
          <p role="alert" className="text-sm text-danger">
            Couldn&apos;t save the loan: {save.error instanceof Error ? save.error.message : "try again"}
          </p>
        )}
        {/* The form's Save lives in the title bar as a submit for this form. */}
        <button type="submit" className="sr-only" tabIndex={-1} aria-hidden />
      </Stack>
    </form>
  );

  return (
    <DetailPageLayout
      entityType="loan"
      entityId={id}
      title={isEditing ? `Edit ${title}` : title}
      isEditing={isEditing}
      onEdit={() => setIsEditing(true)}
      onDelete={() => setShowDelete(true)}
      onCancel={() => setIsEditing(false)}
      onSave={() => {
        const form = document.getElementById("loan-edit") as HTMLFormElement | null;
        form?.requestSubmit();
      }}
      isSaving={save.isPending}
    >
      {loanQuery.isLoading && (
        <Stack aria-busy="true" aria-label="Loading loan">
          <Skeleton height={72} radius="sm" />
          <Skeleton height={140} radius="sm" />
        </Stack>
      )}
      {loanQuery.isError && (
        <Alert color="red" title="Couldn't load this loan">
          {loanQuery.error instanceof Error ? loanQuery.error.message : "Try again in a moment."}
          <Button variant="default" size="sm" mt="sm" onClick={() => loanQuery.refetch()} loading={loanQuery.isRefetching}>
            Try again
          </Button>
        </Alert>
      )}
      {!loanQuery.isLoading && !loanQuery.isError && !loan && <p className="text-sm text-ink-3">No loan found with that id.</p>}
      {loan && (isEditing ? edit : view)}

      <ConfirmDialog
        open={showDelete}
        onClose={() => setShowDelete(false)}
        onConfirm={() => remove.mutate()}
        title="Delete loan"
        message={`Delete ${title}? This cannot be undone.`}
        loading={remove.isPending}
      />
    </DetailPageLayout>
  );
}
