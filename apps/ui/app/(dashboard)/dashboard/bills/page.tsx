"use client";

/**
 * Bills (#4551) — the list, built to the approved money mockup (artifact #102
 * v1) inside the approved shell (#75 v1), mobile-first.
 *
 * THE THREE THINGS THIS PAGE EXISTS TO GET RIGHT
 *
 *  1. A PAID BILL NEVER READS AS DUE. The reading comes from
 *     GET /api/v1/budget/due-soon (the API's own `status`, computed by the
 *     shared Go rule), joined to GET /api/v1/bills by id. Nothing on this page
 *     infers payment from `paid_date`, and nothing infers a due date from a
 *     calendar event: that is exactly how R1 got "Electric $180 due, still
 *     unpaid" about a bill paid that morning (KB discoveries/war-game-r1.md).
 *  2. THE WEEKDAY AND THE DATE ARE THE API'S. Every due line is
 *     `<weekday>, <Mon D>` from `due_weekday` / `due_date` (#4550's rule), plus
 *     the API's own `days_until_due` for "in 2 days". No weekday is built from
 *     a Date anywhere on the page, so no browser zone can move it.
 *  3. MONEY IS ABSENT, NOT ZERO. A role without money read gets no page at all
 *     (the API answers 403; the nav has no Money entry for them); a role with
 *     read and no write — a viewer — gets the full page with every write
 *     affordance disabled and a line saying who can change it.
 *
 * Each read fails on its own: a failed bills read shows an error card with Try
 * again and never renders as "no bills"; a failed due-soon read shows the
 * records with their stored schedule and says the next date is not reported.
 */

import { useMemo, useState } from "react";
import Link from "next/link";
import { Button, Card, Chip, Skeleton, Text, Title } from "@mantine/core";
import {
  IconAlertTriangle,
  IconCar,
  IconCheck,
  IconChevronRight,
  IconHome,
  IconLock,
  IconPlus,
  IconReceipt,
  IconRefresh,
} from "@tabler/icons-react";
import { notifications } from "@mantine/notifications";
import BillForm, { emptyBillForm } from "@/components/bills/BillForm";
import MoneyNoAccess from "@/components/bills/MoneyNoAccess";
import { NO_SUBJECTS, useBills, useBillsAccess, useCreateBill, useDueSoon, useSubjectNames } from "@/lib/bills/api";
import {
  dueCount,
  dueLabel,
  dayMonth,
  dueState,
  dueWhen,
  filterRows,
  formatMoney,
  frequencyChips,
  mergeRows,
  monthlyAmount,
  monthlyTotal,
  scheduleLabel,
  subjectLabel,
  type BillRow,
  type FrequencyFilter,
} from "@/lib/bills/model";

const ICON_STROKE = 1.75;

export default function BillsPage() {
  const access = useBillsAccess();
  const [filter, setFilter] = useState<FrequencyFilter>("all");
  const [adding, setAdding] = useState(false);

  const bills = useBills(access.canSee);
  const dueSoon = useDueSoon(access.canSee);
  const subjects = useSubjectNames(access.canSee);
  const create = useCreateBill();

  const rows = useMemo(() => mergeRows(bills.data?.data ?? [], dueSoon.data?.data), [bills.data, dueSoon.data]);
  const chips = useMemo(() => frequencyChips(rows.map((r) => r.bill)), [rows]);
  const shown = useMemo(() => filterRows(rows, filter), [rows, filter]);
  const due = dueCount(rows);
  const perMonth = formatMoney(monthlyTotal(rows.map((r) => r.bill)));

  // The role is still loading: a skeleton, never an owner's controls flashing
  // at a housesitter (#4574's rule for every role-gated page).
  if (!access.known) {
    return (
      <div className="mx-auto w-full max-w-3xl" aria-busy="true" aria-label="Loading bills">
        <Skeleton height={28} width={140} radius="sm" mb="md" />
        <Skeleton height={120} radius="md" />
      </div>
    );
  }

  if (!access.canSee) return <MoneyNoAccess role={access.role} />;

  const submitNew = (body: Record<string, unknown>) => {
    create.mutate(body, {
      onSuccess: () => {
        setAdding(false);
        notifications.show({ message: "Bill added", color: "teal" });
      },
    });
  };

  return (
    <div className="mx-auto w-full max-w-3xl" data-testid="bills-page">
      <div className="mb-4 flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <Title order={2} className="text-xl font-semibold leading-tight md:text-2xl">
            Bills
          </Title>
          <Text size="sm" c="dimmed" className="mt-0.5">
            {bills.isLoading
              ? "Loading…"
              : rows.length === 0
                ? "No bills yet"
                : // A total of nothing is not a total: an empty list says so instead of "$0.00".
                  `${rows.length} ${rows.length === 1 ? "bill" : "bills"}${perMonth ? ` · ${perMonth} a month in all` : ""}`}
          </Text>
        </div>
        {access.canWrite && (
          <Button
            onClick={() => {
              create.reset();
              setAdding(true);
            }}
            leftSection={<IconPlus size={18} stroke={ICON_STROKE} aria-hidden />}
            className="min-h-11 shrink-0"
          >
            Add
          </Button>
        )}
      </div>

      {!access.canWrite && (
        <div className="mb-4 flex items-start gap-2.5 rounded-token-md border border-line bg-surface-2 px-3.5 py-3 text-sm text-ink-2" data-testid="view-only">
          <IconLock size={18} stroke={ICON_STROKE} className="mt-0.5 shrink-0 text-ink-3" aria-hidden />
          <span>
            <b className="font-semibold text-ink">View only.</b> You can see every bill and what is due. A family member
            can mark one paid or change it.
          </span>
        </div>
      )}

      {bills.isError ? (
        <ErrorCard
          title="Bills didn't load"
          message="Home OS couldn't reach the server. Nothing was lost: your bills are saved. Check your connection and try again."
          onRetry={() => bills.refetch()}
        />
      ) : bills.isLoading ? (
        <Skeleton height={260} radius="md" />
      ) : rows.length === 0 ? (
        <EmptyState canWrite={access.canWrite} onAdd={() => setAdding(true)} />
      ) : (
        <>
          {chips.length > 2 && (
            <div className="mb-3 -mx-4 flex gap-2 overflow-x-auto px-4 pb-1 [scrollbar-width:none] md:mx-0 md:flex-wrap md:px-0" role="group" aria-label="How often">
              {chips.map((c) => (
                <Chip
                  key={c.value}
                  checked={filter === c.value}
                  onChange={() => setFilter(c.value)}
                  variant="outline"
                  className="min-h-11 shrink-0"
                  aria-label={`${c.label}, ${c.count} ${c.count === 1 ? "bill" : "bills"}`}
                >
                  {c.label} <span className="ml-1 text-ink-3">{c.count}</span>
                </Chip>
              ))}
            </div>
          )}

          <p className="mb-2 text-[13px] text-ink-3" data-testid="bills-context">
            {due > 0 ? `${due} due now` : "Nothing due right now"} · sorted by next due date
          </p>

          {/* Phones: the list's cards (the mockup's .elist-cards). */}
          <Card withBorder radius="md" padding={0} className="overflow-hidden md:hidden">
            {shown.map((row, i) => (
              <BillListItem key={row.bill.id} row={row} names={(subjects.data ?? NO_SUBJECTS).names} first={i === 0} />
            ))}
          </Card>

          {/* 768px and up: the mockup's table (.elist-tbl). The same rows, the
              same reading, with the columns a wide screen has room for. */}
          <div className="hidden md:block" data-testid="bills-table">
            <Card withBorder radius="md" padding={0} className="overflow-x-auto">
              <table className="w-full min-w-[36rem] border-collapse text-sm">
                <caption className="sr-only">Bills, next due, amount, how often, per month, budget category and what each is for</caption>
                <thead>
                  <tr className="bg-surface-2 text-left">
                    {["Bill", "Next due", "Amount", "How often", "Per month", "Budget", "For"].map((h, i) => (
                      <th
                        key={h}
                        scope="col"
                        className={[
                          "border-b border-line px-3.5 py-2.5 text-[12px] font-semibold uppercase tracking-[.04em] text-ink-3",
                          i === 2 || i === 4 ? "text-right" : "",
                        ].join(" ")}
                      >
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {shown.map((row) => (
                    <BillTableRow key={row.bill.id} row={row} names={(subjects.data ?? NO_SUBJECTS).names} />
                  ))}
                </tbody>
              </table>
            </Card>
          </div>

          {dueSoon.isError && (
            <div className="mt-3 rounded-token-md border border-warn-line bg-warn-bg px-3.5 py-3 text-[13px] text-warn-ink" role="status" data-testid="due-soon-gap">
              <b className="font-semibold">Due dates didn&apos;t load.</b> The bills below are shown with the schedule stored
              on each one; nothing here says whether it has been paid.{" "}
              <button type="button" className="underline" onClick={() => dueSoon.refetch()}>
                Try again
              </button>
            </div>
          )}
          {dueSoon.data?.data.coverage?.without_due_date ? (
            <p className="mt-3 text-[13px] text-ink-3">
              {dueSoon.data.data.coverage.without_due_date === 1 ? "1 bill has" : `${dueSoon.data.data.coverage.without_due_date} bills have`} no
              usable due date, so {dueSoon.data.data.coverage.without_due_date === 1 ? "it doesn't" : "they don't"} show a next
              date here. Give {dueSoon.data.data.coverage.without_due_date === 1 ? "it" : "them"} a due day or date to fix that.
            </p>
          ) : null}
        </>
      )}

      {adding && (
        <BillForm
          opened={adding}
          onClose={() => setAdding(false)}
          title="Add bill"
          initial={emptyBillForm()}
          subjects={subjects.data ?? NO_SUBJECTS}
          saving={create.isPending}
          error={create.error instanceof Error ? create.error.message : null}
          onSubmit={submitNew}
        />
      )}
    </div>
  );
}

// ── one row ─────────────────────────────────────────────────

function BillListItem({ row, names, first }: { row: BillRow; names: Parameters<typeof subjectLabel>[1]; first: boolean }) {
  const { bill, due } = row;
  const state = dueState(row);
  const amount = formatMoney(bill.amount);
  const subject = subjectLabel(bill, names);
  const paidFor = bill.paid_for_due_date ?? null;

  // The second line: what the API says, in the API's own words and dates.
  const line =
    state === "due" && due
      ? `Next ${dueLabel(due)} · ${dueWhen(due.days_until_due)}`
      : state === "paid" && due
        ? `Paid${paidFor ? ` ${dayMonth(paidFor)}` : ""} · next ${dueLabel(due)}`
        : `${scheduleLabel(bill)} · next date not reported`;

  return (
    <Link
      href={`/dashboard/bills/${bill.id}`}
      data-bill-row={bill.id}
      data-due-state={state}
      className={[
        "flex min-h-16 items-center gap-3 px-3.5 py-2.5 transition-colors hover:bg-surface-2 active:bg-surface-2",
        first ? "" : "border-t border-line",
      ].join(" ")}
    >
      <span className="grid h-9 w-9 flex-none place-items-center rounded-[10px] bg-surface-2 text-ink-2" aria-hidden>
        {bill.vehicle_id ? (
          <IconCar size={18} stroke={ICON_STROKE} />
        ) : bill.property_id ? (
          <IconHome size={18} stroke={ICON_STROKE} />
        ) : (
          <IconReceipt size={18} stroke={ICON_STROKE} />
        )}
      </span>
      <span className="min-w-0 flex-1">
        <b className="block truncate text-[15px] font-semibold leading-snug text-ink">{bill.name}</b>
        {/* Two lines, not one truncated: the API's weekday plus its date is the
            whole point of this line, and "next Tue, Dec…" says nothing. */}
        <span className="block text-[13px] leading-snug text-ink-3">
          {state === "paid" && <IconCheck size={13} stroke={2} className="mr-0.5 inline align-[-2px] text-[var(--ok-ink,#2b8a3e)]" aria-hidden />}
          {line}
        </span>
      </span>
      <span className="shrink-0 text-right">
        {amount ? (
          <b className="block text-[15px] font-semibold tabular-nums text-ink">{amount}</b>
        ) : (
          <span className="block text-[13px] text-ink-3">No amount</span>
        )}
        <span className="block text-[12px] text-ink-3">{subject ? subject.name : frequencyChipLabel(bill.frequency)}</span>
      </span>
      <IconChevronRight size={18} stroke={ICON_STROKE} className="shrink-0 text-ink-3" aria-hidden />
    </Link>
  );
}

/**
 * One bill as a table row (768px and up). Same reading as the card: the due
 * line, the amount and the "per month" spread all come from the model, and the
 * paid state is the API's, never inferred here.
 */
function BillTableRow({ row, names }: { row: BillRow; names: Parameters<typeof subjectLabel>[1] }) {
  const { bill, due } = row;
  const state = dueState(row);
  const amount = formatMoney(bill.amount);
  const perMonth = formatMoney((bill.frequency ?? "monthly") === "once" ? null : monthlyAmount(bill));
  const subject = subjectLabel(bill, names);
  const paidFor = bill.paid_for_due_date ?? null;

  return (
    <tr
      data-bill-table-row={bill.id}
      data-due-state={state}
      className="border-b border-line last:border-b-0 hover:bg-surface-2"
    >
      <td className="px-3.5 py-3">
        <Link href={`/dashboard/bills/${bill.id}`} className="inline-flex items-center gap-2.5 font-semibold text-ink">
          <span className="grid h-8 w-8 flex-none place-items-center rounded-lg bg-surface-2 text-ink-2" aria-hidden>
            {bill.vehicle_id ? (
              <IconCar size={16} stroke={ICON_STROKE} />
            ) : bill.property_id ? (
              <IconHome size={16} stroke={ICON_STROKE} />
            ) : (
              <IconReceipt size={16} stroke={ICON_STROKE} />
            )}
          </span>
          <span className="min-w-0">{bill.name}</span>
        </Link>
      </td>
      <td className="whitespace-nowrap px-3.5 py-3 text-ink-2">
        {state === "paid" && due ? (
          <>
            <span className="block">{dueLabel(due)}</span>
            <span className="block text-[12px] font-semibold text-[var(--ok-ink,#2b8a3e)]">
              Paid{paidFor ? ` ${dayMonth(paidFor)}` : ""}
            </span>
          </>
        ) : state === "due" && due ? (
          <>
            <span className="block">{dueLabel(due)}</span>
            <span className="block text-[12px] text-ink-3">{dueWhen(due.days_until_due)}</span>
          </>
        ) : (
          <span className="block text-ink-3">{scheduleLabel(bill)}</span>
        )}
      </td>
      <td className="whitespace-nowrap px-3.5 py-3 text-right tabular-nums">
        {amount ? <b className="font-semibold text-ink">{amount}</b> : <span className="text-ink-3">No amount</span>}
      </td>
      <td className="whitespace-nowrap px-3.5 py-3 text-ink-2">{scheduleLabel(bill)}</td>
      <td className="whitespace-nowrap px-3.5 py-3 text-right tabular-nums text-ink-2">{perMonth ?? <span className="text-ink-3">–</span>}</td>
      <td className="px-3.5 py-3 text-ink-2">{bill.category || <span className="text-ink-3">None</span>}</td>
      <td className="px-3.5 py-3 text-ink-2">{subject ? subject.name : <span className="text-ink-3">–</span>}</td>
    </tr>
  );
}

function frequencyChipLabel(frequency: string | null | undefined): string {
  return frequency === "once" ? "Once" : "";
}

// ── empty and error ─────────────────────────────────────────

function EmptyState({ canWrite, onAdd }: { canWrite: boolean; onAdd: () => void }) {
  return (
    <div className="rounded-token-md border border-line bg-surface p-5" data-testid="bills-empty">
      <Title order={3} className="text-lg font-semibold">
        No bills yet
      </Title>
      <p className="mt-1 max-w-prose text-sm text-ink-2">
        Add the bills the family pays — electric, insurance, property tax, subscriptions. Home OS works out what they
        come to each month, shows what&apos;s due next, and puts their due dates on the Bills calendar so phones remind
        you.
      </p>
      <p className="mt-2 max-w-prose text-sm text-ink-3">
        Each bill needs a name; an amount, how often it recurs (monthly, quarterly, every 6 months, yearly or once) and
        what it is for are optional. A mortgage or insurance you add on a house or vehicle page shows up here too.
      </p>
      {canWrite && (
        <Button onClick={onAdd} mt="md" leftSection={<IconPlus size={18} stroke={ICON_STROKE} aria-hidden />} className="min-h-11">
          Add a bill
        </Button>
      )}
    </div>
  );
}

function ErrorCard({ title, message, onRetry }: { title: string; message: string; onRetry: () => void }) {
  return (
    <div className="flex items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-3.5 text-warn-ink" role="alert">
      <IconAlertTriangle size={20} stroke={ICON_STROKE} className="mt-0.5 shrink-0" aria-hidden />
      <div>
        <b className="block font-semibold">{title}</b>
        <p className="mt-0.5 text-[13px] leading-snug">{message}</p>
        <Button
          variant="default"
          size="sm"
          mt="xs"
          onClick={onRetry}
          leftSection={<IconRefresh size={16} stroke={ICON_STROKE} aria-hidden />}
          className="max-md:min-h-11"
        >
          Try again
        </Button>
      </div>
    </div>
  );
}
