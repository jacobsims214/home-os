"use client";

/**
 * A bill (#4551) — detail, edit, mark paid, delete, built to the approved
 * money mockup (artifact #102 v1) inside the approved shell (#75 v1), and
 * matching Today (#4549) and the calendar (#4550) for component vocabulary.
 *
 * THE READING THIS PAGE SHOWS, AND WHERE EACH PART COMES FROM
 *  - When it is due, what weekday that is, and whether it is PAID: the API's
 *    GET /api/v1/budget/due-soon row for this bill (`due_date`, `due_weekday`,
 *    `days_until_due`, `status`). Nothing here works out a weekday, and a paid
 *    bill is never rendered as due (the R1 failure, KB
 *    discoveries/war-game-r1.md).
 *  - The record itself (name, amount, frequency, due day/date, category,
 *    account, notes, what it is for, the last payment): GET /api/v1/bills/{id}.
 *  - "What the next due date becomes" when you mark it paid: the API's current
 *    due date plus one period (model.nextAfterPaying). After the write the page
 *    re-reads due-soon, so the date it then shows is the API's, not the hope.
 *
 * A bill that due-soon does not report (no usable schedule, or a paid once
 * bill) says the next date is not reported and shows the stored schedule. It
 * is never given an invented date and never called due.
 *
 * MARKING PAID cannot be completed by this build's API: there is no payment
 * route (see lib/bills/api.ts). The sheet writes the two payment columns
 * through the PUT and believes the response; when the API does not echo a
 * stored payment back, the page says exactly that instead of showing a tick.
 *
 * Roles: a viewer reads everything and every write control is disabled, with a
 * line saying who can change it (decisions/2026-11-15-role-policy.md). A
 * housesitter or vendor gets the no-access card, because the API answers 403.
 */

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { Button, Menu, Skeleton, Switch } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconArrowLeft,
  IconCheck,
  IconChevronDown,
  IconCreditCard,
  IconDotsVertical,
  IconLock,
  IconPencil,
  IconRefresh,
  IconTrash,
  IconAlertTriangle,
} from "@tabler/icons-react";
import BillForm, { billToForm } from "@/components/bills/BillForm";
import { AttentionBadge } from "@/components/bills/attention";
import { PayeeCard } from "@/components/bills/payee";
import MarkPaidSheet from "@/components/bills/MarkPaidSheet";
import MoneyNoAccess from "@/components/bills/MoneyNoAccess";
import EntityResources from "@/components/EntityResources";
import { useNavRole } from "@/components/shared/use-nav-role";
import { resourceAccess } from "@/lib/resource-access";
import {
  NO_SUBJECTS,
  useBill,
  useBillsAccess,
  useDeleteBill,
  useDueSoon,
  useMarkBillPaid,
  useSubjectNames,
  useUpdateBill,
} from "@/lib/bills/api";
import {
  accountLine,
  accountSummary,
  accountTypeLabel,
  attentionHelp,
  attentionNeedsAction,
  attentionOf,
  dueLabel,
  dueWhen,
  formatMoney,
  frequencyLabel,
  fullDate,
  mergeRows,
  nextAfterPaying,
  scheduleLabel,
  subjectLabel,
  type Bill,
} from "@/lib/bills/model";
import { useCurrentHousehold } from "@/components/shared/use-nav-role";
import { householdToday } from "@/lib/today/agenda";

const ICON_STROKE = 1.75;

export default function BillPage() {
  const params = useParams<{ id: string }>();
  const id = typeof params?.id === "string" ? params.id : "";
  const access = useBillsAccess();
  const role = useNavRole();
  const household = useCurrentHousehold();
  const router = useRouter();

  const billQuery = useBill(access.canSee ? id : null);
  const dueSoon = useDueSoon(access.canSee);
  const subjects = useSubjectNames(access.canSee);
  const update = useUpdateBill(id);
  const remove = useDeleteBill();
  const markPaid = useMarkBillPaid();

  const [editing, setEditing] = useState(false);
  const [paying, setPaying] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(true);
  // The next due date the API reported AFTER a payment, so the page can say
  // what changed without inventing it.
  const [paidNote, setPaidNote] = useState<{ next: string | null; recorded: boolean } | null>(null);

  // The API's reading of this bill, joined the same way the list does it.
  const row = useMemo(() => {
    const bill = billQuery.data?.data;
    if (!bill) return null;
    return mergeRows([bill], dueSoon.data?.data)[0] ?? null;
  }, [billQuery.data, dueSoon.data]);

  // Today in the FAMILY's timezone, after mount (the server's clock is not the
  // family's), for the "paid on" default. Never derived during SSR.
  const [today, setToday] = useState<string | null>(null);
  useEffect(() => {
    setToday(householdToday(household?.timezone ?? null));
  }, [household?.timezone]);

  if (!access.known) {
    return (
      <div role="status" aria-busy="true" aria-label="Loading bill">
        <Skeleton height={28} width="60%" radius="sm" mb="md" />
        <Skeleton height={160} radius="md" />
      </div>
    );
  }
  if (!access.canSee) return <MoneyNoAccess role={access.role} />;

  const bill = billQuery.data?.data ?? null;
  const subject = bill ? subjectLabel(bill, (subjects.data ?? NO_SUBJECTS).names) : null;
  const accessResources = resourceAccess(role, "bill");

  if (billQuery.isError) {
    return (
      <div className="mx-auto w-full max-w-3xl">
        <BackLink />
        <div className="flex items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-3.5 text-warn-ink" role="alert">
          <IconAlertTriangle size={20} stroke={ICON_STROKE} className="mt-0.5 shrink-0" aria-hidden />
          <div>
            <b className="block font-semibold">This bill didn&apos;t load</b>
            <p className="mt-0.5 text-[13px]">It may have been deleted, or Home OS couldn&apos;t reach the server.</p>
            <Button variant="default" size="sm" mt="xs" onClick={() => billQuery.refetch()} leftSection={<IconRefresh size={16} stroke={ICON_STROKE} aria-hidden />}>
              Try again
            </Button>
          </div>
        </div>
      </div>
    );
  }

  if (billQuery.isLoading || !bill) {
    return (
      <div role="status" aria-busy="true" aria-label="Loading bill">
        <Skeleton height={28} width="60%" radius="sm" mb="md" />
        <Skeleton height={160} radius="md" />
      </div>
    );
  }

  const due = row?.due ?? null;
  const amount = formatMoney(bill.amount);
  const monthly = formatMoney(bill.frequency === "once" ? null : monthlyOf(bill));
  const isPaid = due?.status === "paid";
  const nextAfter = nextAfterPaying(due, bill.frequency);
  // The API's answer to "what do I have to do about this bill?", and the account
  // and payee it comes with. Nothing here re-derives the state from is_autopay:
  // a bill whose switch is on but whose account is missing is `needs_setup`, and
  // the API is the only thing that knows that.
  const attention = attentionOf(bill);
  const needsAction = attentionNeedsAction(attention);
  const account = bill.payment_account ?? null;
  const accountType = accountTypeLabel(account?.account_type);
  const autopayOn = bill.autopay_enabled === true || bill.is_autopay === true;
  const toggleAutopay = (next: boolean) => {
    // Turning autopay OFF while an account is linked would leave the bill in the
    // database's forbidden state (bills_autopay_account_check refuses an account
    // on a non-autopay bill), so the account goes with it. Turning it ON leaves
    // the account as it is; the API's answer comes back and the badge follows it.
    const body: Record<string, unknown> = { is_autopay: next };
    if (!next) body.autopay_account_id = null;
    update.mutate(body, {
      onSuccess: () => {
        notifications.show({
          message: next ? "Autopay turned on" : "Autopay turned off — you pay this one",
          color: "teal",
        });
      },
    });
  };

  const saveEdit = (body: Record<string, unknown>) => {
    update.mutate(body, {
      onSuccess: () => {
        setEditing(false);
        notifications.show({ message: "Bill saved", color: "teal" });
      },
    });
  };

  const submitPaid = ({ paidDate, forDueDate }: { paidDate: string; forDueDate: string | null }) => {
    setPaidNote(null);
    markPaid.mutate(
      { id: bill.id, paidDate, forDueDate },
      {
        onSuccess: async (result) => {
          setPaying(false);
          const refreshed = await dueSoon.refetch();
          const nextRow = refreshed.data ? mergeRows([bill], refreshed.data.data)[0] : null;
          setPaidNote({ next: nextRow?.due?.due_date ?? null, recorded: result.recorded });
          if (result.recorded) {
            notifications.show({ message: `${bill.name} marked paid`, color: "teal" });
          }
        },
        onError: (err: Error) => {
          notifications.show({ title: "Couldn't mark it paid", message: err.message, color: "red" });
        },
      },
    );
  };

  const deleteBill = () => {
    remove.mutate(bill.id, {
      onSuccess: () => {
        notifications.show({ message: "Bill deleted", color: "teal" });
        router.push("/dashboard/bills");
      },
      onError: (err: Error) => notifications.show({ title: "Couldn't delete it", message: err.message, color: "red" }),
    });
  };

  return (
    <div className="min-w-0" data-testid="bill-page">
      {/* Phone: sticky title bar with a back link and one actions menu. */}
      <div className="sticky top-[var(--app-shell-header-offset,0px)] z-10 -mx-4 -mt-4 mb-4 flex min-h-14 items-center gap-1 border-b border-line bg-bg px-2 md:static md:mx-0 md:mt-0 md:mb-4 md:items-end md:gap-4 md:border-0 md:bg-transparent md:px-0">
        <Link href="/dashboard/bills" aria-label="Back to Bills" className="inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-token-sm text-ink-2 hover:bg-surface-2 hover:text-ink md:hidden">
          <IconArrowLeft size={22} stroke={ICON_STROKE} aria-hidden />
        </Link>
        <div className="min-w-0 flex-1">
          <Link href="/dashboard/bills" className="hidden items-center gap-1 text-sm text-ink-3 hover:text-accent md:inline-flex">
            <IconArrowLeft size={16} stroke={ICON_STROKE} aria-hidden />
            Back to Bills
          </Link>
          <h2 className="truncate text-lg font-semibold leading-tight text-ink md:mt-1 md:text-2xl md:font-bold">{bill.name}</h2>
        </div>
        {access.canWrite ? (
          <>
            <div className="md:hidden">
              <Menu position="bottom-end" width={200} shadow="lg" radius="md" withinPortal>
                <Menu.Target>
                  <button type="button" className="inline-flex h-11 w-11 items-center justify-center rounded-token-sm text-ink-2" aria-label={`Actions for ${bill.name}`}>
                    <IconDotsVertical size={22} stroke={ICON_STROKE} aria-hidden />
                  </button>
                </Menu.Target>
                <Menu.Dropdown>
                  <Menu.Item onClick={() => setEditing(true)} className="min-h-11" leftSection={<IconPencil size={18} stroke={ICON_STROKE} aria-hidden />}>
                    Edit
                  </Menu.Item>
                  <Menu.Item onClick={deleteBill} className="min-h-11" style={{ color: "var(--danger)" }} leftSection={<IconTrash size={18} stroke={ICON_STROKE} aria-hidden />}>
                    Delete
                  </Menu.Item>
                </Menu.Dropdown>
              </Menu>
            </div>
            <div className="hidden shrink-0 items-center gap-2 md:flex">
              <Button variant="default" onClick={() => setEditing(true)} leftSection={<IconPencil size={16} stroke={ICON_STROKE} aria-hidden />}>
                Edit
              </Button>
              <Button color="red" variant="outline" onClick={deleteBill} leftSection={<IconTrash size={16} stroke={ICON_STROKE} aria-hidden />}>
                Delete
              </Button>
            </div>
          </>
        ) : (
          <span className="mr-2 shrink-0 text-[13px] text-ink-3">View only</span>
        )}
      </div>

      <div className="flex flex-col gap-3 md:gap-4 lg:grid lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)] lg:items-start">
        <div className="flex min-w-0 flex-col gap-3 md:gap-4">
          {/* The big fact: when it is next due, and whether it is paid. */}
          <section className="rounded-token-md bg-accent-soft p-4" aria-labelledby="next-due" data-testid="bill-next-due">
            {/* WHAT THE FAMILY HAS TO DO, from the API's attention field. On a
                phone this is the first thing in the card, because "do I need to
                do anything?" is the question the page exists to answer. */}
            <div className="mb-2 flex flex-wrap items-center gap-2" data-testid="bill-attention">
              <AttentionBadge bill={bill} />
              <span className="text-[13px] leading-snug text-ink-2">{attentionHelp(attention)}</span>
            </div>
            <span className="text-[13px] font-medium text-ink-2" id="next-due">
              {isPaid ? "Paid · next due" : due ? "Next due" : "Due date"}
            </span>
            <div className="mt-0.5 flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
              {due ? (
                <>
                  <b className="text-[28px] font-semibold leading-tight tracking-tight text-ink" data-testid="bill-due-date">
                    {dueLabel(due)}
                  </b>
                  <span className="text-[14px] text-ink-2" data-testid="bill-due-when">
                    {(due.due_date ?? "").slice(0, 4)}
                    {" · "}
                    {isPaid ? `next in ${due.days_until_due} days` : dueWhen(due.days_until_due)}
                  </span>
                </>
              ) : (
                <b className="text-[22px] font-semibold leading-tight text-ink" data-testid="bill-due-date">
                  Not reported
                </b>
              )}
            </div>
            <p className="mt-1 text-[14px] text-ink-2">
              {amount ? <b className="font-semibold text-ink">{amount}</b> : <span>No amount</span>}
              {amount ? ` · ${frequencyLabel(bill.frequency)}` : ""}
              {monthly ? ` · counts as ${monthly} a month in the budget` : ""}
            </p>
            {due && (
              <p className="mt-0.5 text-[13px] text-ink-3">{isPaid ? "This due date is covered by the last payment." : scheduleLabel(bill)}</p>
            )}
            {!due && (
              <p className="mt-0.5 max-w-prose text-[13px] text-ink-3">
                The API did not report a due date for this bill, so it can&apos;t appear in what&apos;s due. Its stored
                schedule is {scheduleLabel(bill).toLowerCase()}.
              </p>
            )}

            {access.canWrite ? (
              <div className="mt-3 flex flex-wrap gap-2">
                <Button
                  onClick={() => setPaying(true)}
                  leftSection={<IconCheck size={18} stroke={ICON_STROKE} aria-hidden />}
                  className="min-h-11 flex-1 md:flex-none"
                  variant="filled"
                  disabled={!due}
                >
                  Mark paid
                </Button>
                {/* The mockup's rule: from 768 px up the title bar already has
                    Edit, so this one is not drawn twice. The wrapper div carries
                    the breakpoint (DetailPageLayout does the same): a Tailwind
                    display utility on a Mantine button loses to Mantine's own
                    display rule. */}
                <div className="flex-1 md:hidden">
                  <Button
                    onClick={() => setEditing(true)}
                    variant="default"
                    leftSection={<IconPencil size={18} stroke={ICON_STROKE} aria-hidden />}
                    className="min-h-11 w-full"
                  >
                    Edit
                  </Button>
                </div>
              </div>
            ) : (
              <p className="mt-3 flex items-start gap-2 text-[13px] text-ink-2" data-testid="view-only-note">
                <IconLock size={16} stroke={ICON_STROKE} className="mt-0.5 shrink-0 text-ink-3" aria-hidden />
                <span>
                  <b className="font-semibold text-ink">View only.</b> A family member can mark this paid or change it.
                </span>
              </p>
            )}

            {paidNote && (
              <div className="mt-3 rounded-token-sm bg-surface px-3 py-2.5 text-[13px]" role="status" data-testid="paid-result">
                {paidNote.recorded ? (
                  <span className="text-ink-2">
                    Marked paid. {paidNote.next ? <>Next due <b className="font-semibold text-ink">{fullDate(paidNote.next)}</b>.</> : "This one-time bill has no next date."}
                  </span>
                ) : (
                  <span className="text-ink-2">
                    <b className="font-semibold text-ink">The payment wasn&apos;t stored.</b> Home OS sent the payment to the
                    API and it was accepted, but the API&apos;s answer does not carry a paid date back: the bills API has no
                    way to record a payment yet, so nothing was marked. This bill stays as it was.
                  </span>
                )}
              </div>
            )}
          </section>

          {/* Details */}
          <section className="rounded-token-md border border-line bg-surface md:p-6" aria-label="Details">
            <h3 className="md:hidden">
              <button type="button" aria-expanded={detailsOpen} onClick={() => setDetailsOpen((o) => !o)} className="flex min-h-12 w-full items-center gap-2 px-4 text-left">
                <span className="min-w-0 flex-1 text-sm font-semibold text-ink">Details</span>
                <IconChevronDown size={20} stroke={ICON_STROKE} aria-hidden className={`shrink-0 text-ink-2 transition-transform motion-reduce:transition-none ${detailsOpen ? "rotate-180" : ""}`} />
              </button>
            </h3>
            <dl className={`m-0 ${detailsOpen ? "" : "max-md:hidden"}`} data-testid="bill-fields">
              <Field label="Amount" value={amount ?? "No amount"} numeric />
              <Field label="How often" value={scheduleLabel(bill)} />
              {bill.frequency !== "once" && <Field label="Per month" value={monthly ?? "—"} numeric />}
              {bill.category && <Field label="Category" value={bill.category} />}
              {!bill.category && <Field label="Category" value="No category" />}
              <Field label="Auto-pay" value={autopayOn ? "On" : "Off"} />
              {subject && <Field label="For" value={`${subject.name} · ${subject.kind}`} />}
              {account && (
                <Field
                  label="Paid from"
                  value={
                    <span data-testid="bill-account">
                      {accountLine(account)}
                      {accountType ? <span className="text-ink-3"> · {accountType}</span> : null}
                      {account.institution ? <span className="block text-[13px] text-ink-3">{account.institution}</span> : null}
                    </span>
                  }
                />
              )}
              {!account && needsAction && (
                <Field label="Paid from" value={<span className="text-ink-3">No account set up</span>} />
              )}
              {bill.account_number && <Field label="Account number" value={bill.account_number} mono />}
              {bill.payment_url && (
                <Field
                  label="Pay online"
                  value={
                    <a href={bill.payment_url} target="_blank" rel="noreferrer noopener" className="break-words text-accent underline">
                      {bill.payment_url}
                    </a>
                  }
                />
              )}
              {bill.notes && <Field label="Notes" value={bill.notes} />}
            </dl>
            {/* Outside the <dl>: a <dl> may only hold dt/dd (and div wrappers
                around them), so this empty-state <div> inside it was a
                definition-list violation (#4588). */}
            {!bill.account_number && !bill.notes && !bill.payment_url && (
              <div className="px-4 py-3 text-[13px] text-ink-3 md:px-0" data-testid="bill-fields-empty">
                Nothing else recorded on this bill yet. {access.canWrite ? "Edit it to add an account number, a payment link or a note." : ""}
              </div>
            )}
          </section>
        </div>

        <div className="flex min-w-0 flex-col gap-3 md:gap-4">
          {/* ── Autopay: the switch, and the account it draws on ──────────────
              The switch is the WRITE (does this bill pay itself?); the badge in
              the hero is the ANSWER (does anything actually pay it?). They are
              not the same thing: a bill with autopay ON and no account on record
              answers "needs_setup", and the line under the switch says so. */}
          <section className="rounded-token-md border border-line bg-surface" aria-labelledby="autopay-heading" data-testid="bill-autopay">
            <div className="flex items-start justify-between gap-3 px-4 pb-1 pt-3">
              <div className="min-w-0">
                <h2 className="m-0 text-[15px] font-semibold text-ink" id="autopay-heading">
                  Pays itself
                </h2>
                <p className="m-0 mt-0.5 text-[13px] leading-snug text-ink-3">
                  {needsAction ? "A person has to pay this one." : "Paid automatically — nothing for you to do."}
                </p>
              </div>
              {access.canWrite ? (
                // Mantine's own Switch, not components/ui/Switch: its `label`
                // wraps the input and the words in one <label>, so the whole
                // 44 px row is the hit area (the ui/Switch wrapper puts the
                // words in a sibling span and only the ~20 px input toggles).
                <Switch
                  checked={autopayOn}
                  disabled={update.isPending}
                  onChange={(e) => toggleAutopay(e.currentTarget.checked)}
                  label={
                    <span className="text-[13px] font-medium text-ink-2" data-testid="autopay-label">
                      Autopay<span className="sr-only"> for {bill.name}</span>
                    </span>
                  }
                  classNames={{ root: "min-h-11 flex items-center gap-2 pt-1", label: "cursor-inherit" }}
                  data-testid="autopay-toggle"
                />
              ) : (
                <span className="shrink-0 pt-2 text-[13px] font-semibold text-ink-2">{autopayOn ? "On" : "Off"}</span>
              )}
            </div>
            <div className="px-4 pb-3 pt-1" data-testid="bill-autopay-account">
              {account ? (
                <p className="m-0 flex items-start gap-2 text-[13px] leading-snug text-ink-2">
                  <IconCreditCard size={15} stroke={ICON_STROKE} className="mt-0.5 shrink-0 text-ink-3" aria-hidden />
                  <span>
                    From <b className="font-semibold text-ink">{accountSummary(account)}</b>
                    {accountType ? <span className="text-ink-3"> · {accountType}</span> : null}
                  </span>
                </p>
              ) : (
                <p className="m-0 text-[13px] leading-snug text-ink-3">
                  {autopayOn
                    ? "Autopay is on, but no account is linked yet — nothing will actually pay it. Add the account in the bill's edit form."
                    : "No account on record. A person pays this one."}
                </p>
              )}
            </div>
          </section>

          {/* Who we pay, how often, how much, and how to reach them. */}
          <PayeeCard payee={bill.payee} cadence={scheduleLabel(bill)} amount={amount} frequency={frequencyLabel(bill.frequency)} />

          {/* The last payment, as stored */}
          <section className="rounded-token-md border border-line bg-surface" aria-label="Last payment">
            <h3 className="m-0 flex min-h-12 items-center px-4 text-[15px] font-semibold text-ink-2">Last payment</h3>
            {bill.paid_date ? (
              <div className="flex items-start gap-3 px-4 pb-3">
                <span className="grid h-9 w-9 flex-none place-items-center rounded-[10px] bg-[var(--ok-bg,#ebfbee)] text-[var(--ok-ink,#2b8a3e)]" aria-hidden>
                  <IconCheck size={18} stroke={2} />
                </span>
                <div className="min-w-0">
                  <b className="block text-[14px] font-medium text-ink" data-testid="bill-paid-date">
                    Paid {fullDate(bill.paid_date)}
                  </b>
                  <span className="block text-[13px] text-ink-3">
                    {bill.paid_for_due_date ? `For the ${fullDate(bill.paid_for_due_date)} due date` : "Coverage is worked out from the paid date"}
                  </span>
                </div>
              </div>
            ) : (
              <p className="px-4 pb-3 text-[13px] text-ink-3">No payment recorded yet.</p>
            )}
            <p className="m-0 px-4 pb-3 text-[12px] text-ink-3">
              Home OS keeps the latest payment only. It doesn&apos;t record amounts paid.
            </p>
          </section>

          {/* What happens next, in words. */}
          {due && !isPaid && access.canWrite && (
            <section className="rounded-token-md border border-line bg-surface px-4 py-3" aria-label="Marking it paid">
              <p className="m-0 text-[13px] text-ink-2">
                Marking it paid covers <b className="font-semibold text-ink">{dueLabel(due)}</b>
                {nextAfter ? (
                  <>
                    {" "}
                    and moves it to <b className="font-semibold text-ink">{fullDate(nextAfter)}</b>.
                  </>
                ) : (
                  <> and closes it: a one-time bill has no next date.</>
                )}
              </p>
            </section>
          )}

          {accessResources.any && (
            <section className="rounded-token-md border border-line bg-surface px-4 md:p-6" aria-label="Resources">
              <EntityResources entityType="bill" entityId={bill.id} role={role} />
            </section>
          )}
        </div>
      </div>

      {editing && (
        <BillForm
          opened={editing}
          onClose={() => setEditing(false)}
          title="Edit bill"
          initial={billToForm(bill)}
          subjects={subjects.data ?? NO_SUBJECTS}
          saving={update.isPending}
          error={update.error instanceof Error ? update.error.message : null}
          onSubmit={saveEdit}
          onDelete={deleteBill}
        />
      )}

      {paying && today && (
        <MarkPaidSheet
          opened={paying}
          onClose={() => setPaying(false)}
          bill={bill}
          due={due}
          today={today}
          saving={markPaid.isPending}
          onSubmit={submitPaid}
        />
      )}
    </div>
  );
}

/** A monthly cost for a non-once bill, from the model's spread. */
function monthlyOf(bill: Bill): string | null {
  if (bill.amount === null || bill.amount === undefined || bill.amount === "") return null;
  if ((bill.frequency ?? "monthly") === "once") return null;
  return monthlyFor(bill);
}

function monthlyFor(bill: Bill): string {
  const n = Number(bill.amount);
  if (!Number.isFinite(n)) return "0";
  const per =
    bill.frequency === "quarterly" ? n / 3 : bill.frequency === "semiannual" ? n / 6 : bill.frequency === "annual" ? n / 12 : n;
  return per.toFixed(2);
}

function Field({ label, value, numeric, mono }: { label: string; value: React.ReactNode; numeric?: boolean; mono?: boolean }) {
  return (
    <div className="border-b border-line px-4 py-2.5 last:border-b-0 md:grid md:grid-cols-[minmax(8rem,1fr)_minmax(0,2fr)] md:items-baseline md:gap-6 md:px-0" data-field-row={label}>
      <dt className="text-[13px] text-ink-3">{label}</dt>
      <dd className={`m-0 text-[15px] text-ink ${numeric ? "tabular-nums" : ""} ${mono ? "font-mono text-[13.5px]" : ""} [overflow-wrap:anywhere]`}>{value}</dd>
    </div>
  );
}

function BackLink() {
  return (
    <Link href="/dashboard/bills" className="mb-3 inline-flex items-center gap-1 text-sm text-accent">
      <IconArrowLeft size={16} stroke={ICON_STROKE} aria-hidden />
      Back to Bills
    </Link>
  );
}
