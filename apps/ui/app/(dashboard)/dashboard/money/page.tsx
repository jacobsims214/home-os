"use client";

/**
 * Money overview (#4571) — the Overview tab of the approved money mockup
 * (artifact #102 v1), inside the approved shell (#75 v1), mobile-first.
 *
 * THE MONEY RULES THIS PAGE EXISTS TO KEEP (KB api/budget-categories-obligations.md)
 *
 *  1. THE NUMBERS ARE THE API'S. Every figure rendered here is a field of
 *     GET /api/v1/budget/obligations or GET /api/v1/budget/due-soon, printed
 *     verbatim through formatMoney. The page never adds, averages or spreads
 *     them: totals.committed is the hero number even when the categories on
 *     screen would sum to something else, because the monthly spread rule
 *     (x1, /3, /6, /12, `once` only in its due month) lives in Go and has one
 *     home. The only arithmetic here is the width of a progress bar, taken
 *     from two API numbers and used for nothing but its width.
 *  2. MONEY IS ABSENT, NOT ZERO. A null amount renders "—" (and "No amount"
 *     on a due row), never "$0.00". `totals.uncounted` is the API's own count
 *     of records it listed but could not count; the page names them instead of
 *     quietly adding zero for each.
 *  3. NO CLIENT-SIDE PERMISSION CHECK ON MONEY. The API redacts amounts at the
 *     serialization boundary and refuses the whole route to a role without
 *     money read (403). This page renders what it is handed — it never hides a
 *     number because of a role, and never shows one it was not given. The one
 *     role read below (`useBillsAccess`) chooses between the page and the
 *     mockup's no-access card, exactly as the bills pages do.
 *  4. A FAILED READ IS NEVER AN EMPTY PAGE. Each read fails on its own with an
 *     error card and Try again; an empty household gets the mockup's setup
 *     card, which is a different thing from an error.
 *
 * The month stepper changes ?month (YYYY-MM). Until the person touches it the
 * request omits the parameter and the API answers for the household's own
 * current month (its timezone, not the browser's), which is why the label is
 * read back from the response rather than computed here.
 */

import { useMemo, useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Card, Skeleton, Text } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconBuildingBank,
  IconCalendar,
  IconChevronLeft,
  IconChevronRight,
  IconEdit,
  IconInfoCircle,
  IconPlus,
  IconReceipt,
  IconRefresh,
  IconWallet,
} from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { ResponsiveModal, ResponsiveModalActions, TextInput } from "@/components/ui";
import MoneyNoAccess from "@/components/bills/MoneyNoAccess";
import PaymentAccountsSection from "@/components/payment-accounts/PaymentAccountsSection";
import { useBillsAccess } from "@/lib/bills/api";
import { dueLabel, formatMoney, frequencyLabel, type DueSoon } from "@/lib/bills/model";

const STROKE = 1.75;

// ── the obligations payload (apps/api/internal/budget/handler.go) ──────────

/** One bill or loan inside a category. `monthly_amount` is null when it cannot be counted. */
interface ObligationItem {
  kind: "bill" | "loan" | string;
  id: string;
  name: string;
  frequency: string;
  amount: string | null;
  monthly_amount: string | null;
  due_day: number | null;
  due_date: string | null;
}

interface ObligationGroup {
  /** null on the "No category" group: there is nothing to edit. */
  budget_category_id: string | null;
  name: string;
  /** null when the family set no limit for this category. */
  budgeted: string | null;
  /** Always set: the API formats every sum. */
  committed: string;
  /** budgeted - committed, from the API; null when there is no budget. */
  remaining: string | null;
  items: ObligationItem[];
}

interface Obligations {
  /** YYYY-MM, the month the numbers cover. */
  month: string;
  categories: ObligationGroup[];
  uncategorized: ObligationGroup;
  totals: {
    budgeted: string;
    committed: string;
    bills: string;
    loan_payments: string;
    /** How many records were listed but not counted (no amount). */
    uncounted: number;
  };
}

const moneyKeys = {
  all: ["money-page"] as const,
  obligations: (month: string | null) => [...moneyKeys.all, "obligations", month ?? "current"] as const,
  dueSoon: (days: number) => [...moneyKeys.all, "due-soon", days] as const,
};

/** The 30-day window the mockup's overview card states. */
const DUE_SOON_DAYS = 30;

function useObligations(month: string | null, enabled: boolean) {
  return useQuery({
    queryKey: moneyKeys.obligations(month),
    queryFn: () =>
      apiFetch<{ data: Obligations }>("/api/v1/budget/obligations", {
        // No month → the API's own "this month" in the household's timezone.
        params: month ? { month } : undefined,
      }),
    enabled,
    staleTime: 30_000,
    retry: false,
  });
}

/** The overview window only. Summary depth lists only five rows, so ask for the items. */
function useDueSoon30(enabled: boolean) {
  return useQuery({
    queryKey: moneyKeys.dueSoon(DUE_SOON_DAYS),
    queryFn: () =>
      apiFetch<{ data: DueSoon }>("/api/v1/budget/due-soon", {
        params: { days: DUE_SOON_DAYS, detail: "items" },
      }),
    enabled,
    staleTime: 30_000,
    retry: false,
  });
}

// ── month helpers (a label and a step; no money, no dates) ──────────────────

const MONTH_NAMES = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
];

/** "2026-11" → "November 2026"; "" when the API did not say. */
function monthLabel(month: string | null | undefined): string {
  const m = /^(\d{4})-(\d{2})$/.exec(month ?? "");
  if (!m) return "";
  const index = Number(m[2]) - 1;
  if (index < 0 || index > 11) return "";
  return `${MONTH_NAMES[index]} ${m[1]}`;
}

/** Steps a YYYY-MM by whole months. Pure calendar arithmetic on a civil string. */
function stepMonth(month: string, delta: number): string {
  const m = /^(\d{4})-(\d{2})$/.exec(month);
  if (!m) return month;
  const total = Number(m[1]) * 12 + (Number(m[2]) - 1) + delta;
  const year = Math.floor(total / 12);
  const index = ((total % 12) + 12) % 12;
  return `${year}-${String(index + 1).padStart(2, "0")}`;
}

/** The bar width for two API amounts; presentational only (see the file header). */
function meterWidth(committed: string | null, budgeted: string | null): number {
  const c = Number(committed);
  const b = Number(budgeted);
  if (!Number.isFinite(c) || !Number.isFinite(b) || b <= 0) return 0;
  return Math.max(0, Math.min(100, Math.round((c / b) * 100)));
}

/** "—" for anything the API did not send. Never "$0.00". */
function moneyOrDash(value: string | null | undefined): string {
  return formatMoney(value) ?? "—";
}

/**
 * "left" in the legend, from the API's own budgeted and committed. The
 * difference is arithmetic on two API totals for a SUM only — it invents no
 * figure the API did not send, and it is never the hero number.
 */
function leftLabel(data: Obligations): string {
  const budgeted = Number(data.totals.budgeted);
  const committed = Number(data.totals.committed);
  if (!Number.isFinite(budgeted) || !Number.isFinite(committed)) return "—";
  const left = budgeted - committed;
  if (left < 0) return `${moneyOrDash(String(-left))} over`;
  return `${moneyOrDash(String(left))} left`;
}

/** How many categories the API reports as having nothing left. */
function overCount(groups: readonly ObligationGroup[]): number {
  return groups.filter((g) => g.remaining !== null && Number(g.remaining) < 0).length;
}

// ── the category row (mockup: .cat) ────────────────────────────────────────

function CategoryRow({
  group,
  canWrite,
  onEdit,
}: {
  group: ObligationGroup;
  canWrite: boolean;
  onEdit: (group: ObligationGroup) => void;
}) {
  const budgeted = formatMoney(group.budgeted);
  const committed = formatMoney(group.committed);
  const remaining = group.remaining === null ? null : Number(group.remaining);
  // Only the API's `remaining` decides "over": the page does not subtract.
  const over = remaining !== null && remaining < 0;
  const pct = meterWidth(group.committed, group.budgeted);
  const items = group.items.length;

  return (
    <div
      data-money-category={group.budget_category_id ?? "none"}
      data-over={over ? "true" : "false"}
      className="border-b border-line px-4 py-3 last:border-b-0"
    >
      <div className="flex items-baseline gap-2">
        <b className="min-w-0 flex-1 text-[15px] font-semibold text-ink [overflow-wrap:anywhere]">{group.name}</b>
        {remaining === null ? (
          <span className="shrink-0 text-sm font-medium text-ink-3">No budget</span>
        ) : over ? (
          <span className="inline-flex shrink-0 items-center gap-1 text-sm font-semibold text-danger">
            <IconAlertTriangle size={15} stroke={2} aria-hidden />
            Over by {moneyOrDash(String(-remaining))}
          </span>
        ) : (
          <span className="shrink-0 text-sm font-medium tabular-nums text-ink-2">
            {moneyOrDash(String(remaining))} left
          </span>
        )}
      </div>

      {budgeted && (
        <div
          className="mt-2 h-1.5 overflow-hidden rounded-full bg-line"
          role="img"
          aria-label={`${pct}% of ${budgeted} committed`}
        >
          <i className={`block h-full rounded-full ${over ? "bg-danger" : "bg-accent"}`} style={{ width: `${pct}%` }} />
        </div>
      )}

      <div className="mt-1.5 flex flex-wrap justify-between gap-x-2 text-[13px] text-ink-3">
        <span className="min-w-0 truncate">
          {committed ?? "—"} committed
          {items > 0 && ` · ${items} ${items === 1 ? "item" : "items"}`}
        </span>
        <span className="tabular-nums">{budgeted ? `of ${budgeted}` : "no limit"}</span>
      </div>

      {canWrite && group.budget_category_id && (
        <Button
          variant="subtle"
          size="compact-sm"
          leftSection={<IconEdit size={15} />}
          onClick={() => onEdit(group)}
          className="mt-2 max-md:min-h-11"
          /* The visible words first: WCAG 2.5.3 wants the text a person reads
             inside the accessible name (axe label-content-name-mismatch). */
          aria-label={`Edit budget, ${group.name}`}
        >
          Edit budget
        </Button>
      )}
    </div>
  );
}

// ── the category editor (mockup: Edit category sheet) ──────────────────────

function CategoryModal({
  opened,
  group,
  onClose,
}: {
  opened: boolean;
  /** null = a new category. */
  group: ObligationGroup | null;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const editing = !!group?.budget_category_id;
  const [name, setName] = useState(group?.name === "No category" ? "" : (group?.name ?? ""));
  const [budget, setBudget] = useState(group?.budgeted ?? "");
  const [error, setError] = useState<string | null>(null);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: moneyKeys.all });
    queryClient.invalidateQueries({ queryKey: ["bills-page"] });
  };

  const upsert = useMutation({
    mutationFn: (body: { name: string; monthly_amount: string | null }) => {
      const id = group?.budget_category_id;
      if (!id) return apiFetch<{ data: unknown }>("/api/v1/budget/categories", { method: "POST", body });
      // PUT is partial: send both fields the sheet owns, null clears the limit.
      return apiFetch<{ data: unknown }>(`/api/v1/budget/categories/${id}`, { method: "PUT", body });
    },
    onSuccess: () => {
      invalidate();
      notifications.show({ message: editing ? "Budget updated" : "Category added", color: "teal" });
      onClose();
    },
    onError: (err: Error) => setError(err.message || "Couldn't save the budget. Try again."),
  });

  const remove = useMutation({
    mutationFn: () => apiFetch<void>(`/api/v1/budget/categories/${group?.budget_category_id}`, { method: "DELETE" }),
    onSuccess: () => {
      invalidate();
      notifications.show({ message: "Category deleted", color: "teal" });
      onClose();
    },
    onError: (err: Error) => setError(err.message || "Couldn't delete the category. Try again."),
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setError("A category needs a name, like Utilities.");
      return;
    }
    const amount = budget.trim().replace(/^\$/, "").replace(/,/g, "");
    if (amount !== "" && !/^[0-9]{1,10}(\.[0-9]{1,2})?$/.test(amount)) {
      setError("Give the monthly budget as an amount like 450 or 450.00, or leave it empty.");
      return;
    }
    setError(null);
    // Empty means "no limit": the API stores null, not 0.
    upsert.mutate({ name: trimmed, monthly_amount: amount === "" ? null : amount });
  };

  return (
    <ResponsiveModal opened={opened} onClose={onClose} title={editing ? (group?.name ?? "Edit budget") : "Add category"}>
      <form onSubmit={submit} noValidate>
        <div className="flex flex-col gap-3">
          <TextInput
            label="Name"
            required
            data-autofocus
            placeholder="Utilities"
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <TextInput
            label="Monthly budget"
            placeholder="450.00"
            inputMode="decimal"
            description="Leave it empty to track the category without a limit."
            value={budget}
            onChange={(e) => setBudget(e.currentTarget.value)}
          />
          {group && group.items.length > 0 && (
            <div className="rounded-token-md border border-line">
              <p className="border-b border-line bg-surface-2 px-3 py-2 text-[13px] font-medium text-ink-2">
                In this category · {moneyOrDash(group.committed)} a month
              </p>
              <ul>
                {group.items.map((item) => (
                  <li
                    key={`${item.kind}-${item.id}`}
                    className="flex items-center gap-3 border-b border-line px-3 py-2.5 last:border-b-0"
                  >
                    <span className="min-w-0 flex-1">
                      <b className="block truncate text-sm font-medium text-ink">{item.name}</b>
                      <span className="block text-[12px] text-ink-3">{frequencyLabel(item.frequency)}</span>
                    </span>
                    <span className="shrink-0 text-sm tabular-nums text-ink-2">{moneyOrDash(item.monthly_amount)}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
          {editing && (
            <Button
              variant="subtle"
              color="red"
              type="button"
              onClick={() => remove.mutate()}
              loading={remove.isPending}
              className="self-start max-md:min-h-11"
            >
              Delete category
            </Button>
          )}
        </div>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={upsert.isPending}>
            Save
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

// ── the page ───────────────────────────────────────────────────────────────

export default function MoneyPage() {
  const access = useBillsAccess();
  // null = "the current month", decided by the API in the household's timezone.
  const [month, setMonth] = useState<string | null>(null);
  const [editing, setEditing] = useState<ObligationGroup | null>(null);
  const [modalOpen, setModalOpen] = useState(false);

  const obligations = useObligations(month, access.canSee);
  const dueSoon = useDueSoon30(access.canSee);

  const data = obligations.data?.data ?? null;
  const due = dueSoon.data?.data ?? null;

  // The month the numbers actually cover: the response's, so a person who has
  // not touched the stepper sees the household's current month, not the
  // browser's.
  const shownMonth = data?.month ?? month;
  const label = monthLabel(shownMonth);

  const groups = data?.categories ?? [];
  const uncategorized = data?.uncategorized ?? null;

  // The API's own count of records it listed but could not count: the page
  // names them rather than adding a silent zero for each.
  const uncounted = useMemo(() => {
    if (!data) return [] as ObligationItem[];
    return [...data.categories, data.uncategorized]
      .flatMap((g) => g.items)
      .filter((item) => item.monthly_amount === null);
  }, [data]);

  const dueItems = due?.items ?? [];
  const hasNothing = !!data && groups.length === 0 && (!uncategorized || uncategorized.items.length === 0);

  const openNew = () => {
    setEditing(null);
    setModalOpen(true);
  };
  const openEdit = (group: ObligationGroup) => {
    setEditing(group);
    setModalOpen(true);
  };

  // The role is still loading: a skeleton, never an owner's controls flashing
  // at a housesitter (#4574's rule for every role-gated page).
  if (!access.known) {
    return (
      <div className="mx-auto w-full max-w-5xl" role="status" aria-busy="true" aria-label="Loading money">
        <Skeleton height={28} width={140} radius="sm" mb="md" />
        <Skeleton height={180} radius="md" />
      </div>
    );
  }

  // A housesitter or vendor: the API answers 403 on every money route, so the
  // page says what is true instead of rendering an empty budget.
  if (!access.canSee) return <MoneyNoAccess role={access.role} />;

  return (
    <div className="mx-auto w-full max-w-5xl" data-testid="money-page">
      <div className="mb-4 flex items-center justify-between gap-3 md:mb-6">
        <h2 className="min-w-0 text-xl font-semibold text-ink md:text-2xl">Money</h2>
        <div
          className="flex shrink-0 items-center rounded-token-sm border border-line bg-surface"
          role="group"
          aria-label="Month"
        >
          <Button
            variant="subtle"
            size="compact-md"
            onClick={() => shownMonth && setMonth(stepMonth(shownMonth, -1))}
            disabled={!shownMonth}
            aria-label={shownMonth ? `Previous month, ${monthLabel(stepMonth(shownMonth, -1))}` : "Previous month"}
            /* 44 px, set on the element: Mantine's compact-md height (30 px)
               wins over the size-11 utility, and this is the control a thumb
               hits most on this page (#4588, target-size). */
            style={{ width: 44, height: 44 }}
            className="px-0"
          >
            <IconChevronLeft size={18} stroke={STROKE} aria-hidden />
          </Button>
          <b
            className="min-w-[7.5rem] text-center text-sm font-semibold tabular-nums text-ink"
            data-testid="money-month"
          >
            {label || "—"}
          </b>
          <Button
            variant="subtle"
            size="compact-md"
            onClick={() => shownMonth && setMonth(stepMonth(shownMonth, 1))}
            disabled={!shownMonth}
            aria-label={shownMonth ? `Next month, ${monthLabel(stepMonth(shownMonth, 1))}` : "Next month"}
            style={{ width: 44, height: 44 }}
            className="px-0"
          >
            <IconChevronRight size={18} stroke={STROKE} aria-hidden />
          </Button>
        </div>
      </div>

      {obligations.isLoading && (
        <div role="status" aria-busy="true" aria-label="Loading this month's numbers" className="mb-6 space-y-3">
          <Skeleton height={180} radius="md" />
          <Skeleton height={92} radius="md" />
        </div>
      )}

      {obligations.isError && (
        <div
          role="alert"
          className="mb-6 flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
          data-testid="money-obligations-error"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" />
          <p className="flex-1 text-sm">
            Couldn&apos;t load this month&apos;s numbers. Your bills, loans and budgets are saved — this is only the
            reading.
          </p>
          <Button
            variant="default"
            onClick={() => obligations.refetch()}
            loading={obligations.isRefetching}
            leftSection={<IconRefresh size={16} />}
            className="max-md:min-h-11"
          >
            Try again
          </Button>
        </div>
      )}

      {hasNothing && (
        <div className="mb-6 flex flex-col items-start gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-10">
          <h3 className="text-lg font-semibold text-ink">Nothing in Money yet</h3>
          <p className="max-w-xl text-sm text-ink-2">
            Add your bills and loans once. Home OS works out what you owe each month and what&apos;s due next.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button component={Link} href="/dashboard/bills" variant="light" className="max-md:min-h-11">
              Add a bill
            </Button>
            <Button component={Link} href="/dashboard/loans" variant="light" className="max-md:min-h-11">
              Add a loan
            </Button>
            {access.canWrite && (
              <Button variant="light" onClick={openNew} className="max-md:min-h-11">
                Set monthly budgets
              </Button>
            )}
          </div>
        </div>
      )}

      {data && !hasNothing && (
        <>
          <div className="mb-6 grid gap-3 md:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
            <Card withBorder radius="md" padding="md" data-testid="money-hero">
              <div className="flex items-center gap-1.5 text-[13px] font-medium text-ink-2">
                <IconWallet size={16} stroke={STROKE} aria-hidden />
                Committed monthly
              </div>
              <div className="mt-1 flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
                <b
                  className="text-[34px] font-semibold leading-tight tracking-tight tabular-nums text-ink"
                  data-testid="money-committed"
                >
                  {moneyOrDash(data.totals.committed)}
                </b>
                <span className="text-[15px] text-ink-2" data-testid="money-budgeted">
                  of {moneyOrDash(data.totals.budgeted)} budgeted
                </span>
              </div>

              <div
                className="mt-2 h-2.5 overflow-hidden rounded-full bg-line"
                role="img"
                aria-label={`${meterWidth(data.totals.committed, data.totals.budgeted)}% of the monthly budget is committed`}
              >
                <i
                  className={`block h-full rounded-full ${
                    Number(data.totals.committed) > Number(data.totals.budgeted) ? "bg-danger" : "bg-accent"
                  }`}
                  style={{ width: `${meterWidth(data.totals.committed, data.totals.budgeted)}%` }}
                  data-testid="money-meter"
                />
              </div>

              <div className="mt-2.5 flex flex-wrap gap-x-4 gap-y-1 text-[13px] text-ink-2">
                <span data-testid="money-left">
                  <b className="font-semibold text-ink">{leftLabel(data)}</b>
                </span>
                {overCount(groups) > 0 && (
                  <span className="font-semibold text-danger" data-testid="money-over-count">
                    {overCount(groups)} {overCount(groups) === 1 ? "category" : "categories"} over
                  </span>
                )}
              </div>

              <div className="-mx-4 -mb-4 mt-4 grid grid-cols-2 border-t border-line">
                <Link
                  href="/dashboard/bills"
                  className="px-4 py-3 hover:bg-surface-2"
                  data-testid="money-split-bills"
                >
                  <span className="block text-[12px] text-ink-3">Bills</span>
                  <b className="block text-[18px] font-semibold leading-tight tabular-nums text-ink">
                    {moneyOrDash(data.totals.bills)}
                  </b>
                  <small className="text-[12px] text-ink-3">spread per month</small>
                </Link>
                <Link
                  href="/dashboard/loans"
                  className="border-l border-line px-4 py-3 hover:bg-surface-2"
                  data-testid="money-split-loans"
                >
                  <span className="block text-[12px] text-ink-3">Loan payments</span>
                  <b className="block text-[18px] font-semibold leading-tight tabular-nums text-ink">
                    {moneyOrDash(data.totals.loan_payments)}
                  </b>
                  <small className="text-[12px] text-ink-3">per month</small>
                </Link>
              </div>
            </Card>

            <Card withBorder radius="md" padding="md" data-testid="money-due-soon">
              <span className="text-[13px] font-medium text-ink-2">
                Due in the next {due?.days ?? DUE_SOON_DAYS} days
              </span>
              {dueSoon.isLoading && <Skeleton height={30} width="60%" radius="sm" mt="xs" />}
              {dueSoon.isError && (
                <p className="mt-1 text-sm text-ink-3">
                  The next-due reading didn&apos;t load. Your bills and loans are unaffected.
                </p>
              )}
              {due && (
                <>
                  <div className="mt-1 flex flex-wrap items-baseline gap-x-2.5">
                    <b
                      className="text-[26px] font-semibold tracking-tight tabular-nums text-ink"
                      data-testid="money-due-total"
                    >
                      {moneyOrDash(due.total)}
                    </b>
                    <span className="text-sm text-ink-2" data-testid="money-due-count">
                      {(due.bill_count ?? 0) + (due.loan_count ?? 0)} payments
                    </span>
                  </div>
                  {due.from && due.to && (
                    <p className="mt-2 flex items-start gap-1.5 text-[13px] text-ink-3">
                      <IconCalendar size={16} stroke={STROKE} className="mt-0.5 shrink-0" aria-hidden />
                      <span>
                        {dueLabel({ due_date: due.from, due_weekday: "" })} –{" "}
                        {dueLabel({ due_date: due.to, due_weekday: "" })}
                      </span>
                    </p>
                  )}
                </>
              )}
            </Card>
          </div>

          {/* min-w-0 on both items (#4588): a grid item's default min-width is
              auto, i.e. its min-content, and at 320 px the min-content was
              310 px inside a 288 px column — a 6 px sideways scroll on the
              whole page (WCAG 1.4.10). */}
          <div className="grid gap-6 md:grid-cols-[minmax(0,3fr)_minmax(0,2fr)] md:items-start">
            <section aria-labelledby="money-due-heading" className="min-w-0">
              <div className="mb-2 flex items-baseline justify-between gap-3">
                <h3 id="money-due-heading" className="text-base font-semibold text-ink">
                  Due soon
                </h3>
                <Link href="/dashboard/bills" className="min-h-11 py-2 text-[13px] font-semibold text-accent">
                  All bills
                </Link>
              </div>

              {dueSoon.isError ? (
                <div className="rounded-token-md border border-warn-line bg-warn-bg p-4 text-sm text-warn-ink">
                  Couldn&apos;t load what&apos;s due.{" "}
                  <Button variant="subtle" size="compact-sm" onClick={() => dueSoon.refetch()}>
                    Try again
                  </Button>
                </div>
              ) : dueItems.length === 0 ? (
                <Card withBorder radius="md" padding="md">
                  <Text size="sm" c="dimmed">
                    Nothing falls due in the next {due?.days ?? DUE_SOON_DAYS} days.
                  </Text>
                </Card>
              ) : (
                <Card withBorder radius="md" padding={0}>
                  <ul data-testid="money-due-list">
                    {dueItems.map((item) => (
                      <li
                        key={`${item.source}-${item.id}`}
                        data-money-due={`${item.source}-${item.id}`}
                        className="border-b border-line last:border-b-0"
                      >
                        <Link
                          href={
                            item.source === "loan" ? `/dashboard/loans/${item.id}` : `/dashboard/bills/${item.id}`
                          }
                          className="flex min-h-16 items-center gap-3 px-4 py-2.5 hover:bg-surface-2"
                        >
                          <span
                            aria-hidden
                            className="grid size-9 shrink-0 place-items-center rounded-token-md bg-surface-2 text-ink-2"
                          >
                            {item.source === "loan" ? (
                              <IconBuildingBank size={18} stroke={STROKE} />
                            ) : (
                              <IconReceipt size={18} stroke={STROKE} />
                            )}
                          </span>
                          <span className="w-10 shrink-0 text-center leading-tight">
                            <small className="block text-[11px] font-semibold uppercase tracking-wide text-ink-3">
                              {item.due_weekday}
                            </small>
                            <b className="block text-[17px] font-semibold tabular-nums text-ink">
                              {Number((item.due_date ?? "").slice(8, 10)) || "—"}
                            </b>
                          </span>
                          <span className="min-w-0 flex-1">
                            <b className="block truncate text-[15px] font-semibold text-ink">{item.name}</b>
                            <span className="block truncate text-[13px] text-ink-3">
                              {item.status === "paid" ? "Paid · next " : ""}
                              {item.source === "loan" ? "loan payment" : "bill"}
                              {item.days_until_due === 0
                                ? " · due today"
                                : item.days_until_due > 0
                                  ? ` · in ${item.days_until_due} days`
                                  : ` · ${-item.days_until_due} days late`}
                            </span>
                          </span>
                          {item.amount === null ? (
                            <span className="shrink-0 text-[13px] font-medium text-ink-3" data-money-no-amount>
                              No amount
                            </span>
                          ) : (
                            <span className="shrink-0 text-[15px] font-semibold tabular-nums text-ink">
                              {formatMoney(item.amount)}
                            </span>
                          )}
                        </Link>
                      </li>
                    ))}
                  </ul>
                </Card>
              )}

              <p className="mt-2.5 text-[13px] leading-relaxed text-ink-3">
                Loan payments aren&apos;t ticked off: Home OS keeps the loan&apos;s balance, not each payment.
              </p>
            </section>

            <section aria-labelledby="money-budget-heading" className="min-w-0">
              <div className="mb-2 flex items-baseline justify-between gap-3">
                <h3 id="money-budget-heading" className="text-base font-semibold text-ink">
                  Budget
                  <span className="ml-1.5 font-medium text-ink-3">{label.split(" ")[0] || ""}</span>
                </h3>
                {access.canWrite && (
                  <Button
                    variant="subtle"
                    size="compact-sm"
                    leftSection={<IconPlus size={15} />}
                    onClick={openNew}
                    className="max-md:min-h-11"
                    data-testid="money-add-category"
                  >
                    Category
                  </Button>
                )}
              </div>

              {groups.length === 0 && !uncategorized?.items.length ? (
                <Card withBorder radius="md" padding="md">
                  <Text size="sm" c="dimmed">
                    No budget categories yet. Set one for housing, utilities or anything else you plan per month.
                  </Text>
                  {access.canWrite && (
                    <Button variant="light" mt="sm" onClick={openNew} className="max-md:min-h-11">
                      Set monthly budgets
                    </Button>
                  )}
                </Card>
              ) : (
                <Card withBorder radius="md" padding={0} data-testid="money-categories">
                  {groups.map((group) => (
                    <CategoryRow
                      key={group.budget_category_id ?? group.name}
                      group={group}
                      canWrite={access.canWrite}
                      onEdit={openEdit}
                    />
                  ))}
                  {uncategorized && uncategorized.items.length > 0 && (
                    <div
                      data-money-category="none"
                      className="flex items-baseline gap-2 border-t border-line bg-surface-2 px-4 py-3"
                    >
                      <b className="min-w-0 flex-1 text-[15px] font-semibold text-ink">{uncategorized.name}</b>
                      <span className="shrink-0 text-[13px] text-ink-3">
                        {uncategorized.items.length} {uncategorized.items.length === 1 ? "record" : "records"}
                      </span>
                    </div>
                  )}
                </Card>
              )}

              {uncounted.length > 0 && (
                <p className="mt-2.5 flex items-start gap-1.5 text-[13px] text-ink-3" data-testid="money-uncounted">
                  <IconInfoCircle size={16} stroke={STROKE} className="mt-0.5 shrink-0" aria-hidden />
                  <span>
                    <b className="font-semibold text-ink-2">{uncounted.map((item) => item.name).join(", ")}</b>{" "}
                    {uncounted.length === 1 ? "has" : "have"} no amount yet, so{" "}
                    {uncounted.length === 1 ? "it isn't" : "they aren't"} counted in the total.
                  </span>
                </p>
              )}

              <p className="mt-2.5 text-[13px] leading-relaxed text-ink-3">
                <b className="font-semibold text-ink-2">Budgeted</b> is what you plan per month.{" "}
                <b className="font-semibold text-ink-2">Committed</b> is what the category&apos;s bills and loan
                payments cost per month, spread out: a yearly bill counts a twelfth of its amount.
              </p>
            </section>
          </div>
        </>
      )}

      {/*
        Payment accounts (#5506): the accounts a bill is paid FROM, with the
        bank login behind each one. Placed on Money rather than in Settings
        because it is a money fact a person comes here to check — "which
        account does the mortgage come out of?" — and because the bill pages
        link back to it. The section asks the API's money policy itself and
        renders nothing for a role that would be refused.
      */}
      <PaymentAccountsSection />

      {modalOpen && (
        <CategoryModal
          opened={modalOpen}
          group={editing}
          onClose={() => {
            setModalOpen(false);
            setEditing(null);
          }}
        />
      )}
    </div>
  );
}
