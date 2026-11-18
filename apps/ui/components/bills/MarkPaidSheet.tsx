"use client";

/**
 * Mark paid (#4551, mockup artifact #102's "Mark paid" sheet).
 *
 * WHAT IT ASKS, AND WHAT IT PROMISES:
 *  - WHICH DUE DATE the payment is for: the current one (the default) or the
 *    next one (paying a period ahead, which the MCP's bill_pay allows and
 *    nothing more — a typo cannot mark a year paid).
 *  - PAID ON: today in the family's timezone by default, never a future date
 *    (bill_pay's rule). It is a date input, so the browser's own picker does
 *    the typing.
 *  - WHAT HAPPENS NEXT: the next due date the bill moves to, computed from the
 *    API's current due date plus one period (lib/bills/model.ts
 *    nextAfterPaying). It says the amount is not recorded, because Home OS
 *    keeps the latest payment only — no transactions.
 *
 * The sheet does NOT claim the payment happened. It hands the dates to the
 * page's mutation and shows what the API answered: a recorded payment, or the
 * honest note that this API did not store one (see lib/bills/api.ts,
 * markBillPaid). A tick on a payment that was not stored is the R1 failure in
 * a different costume.
 */

import { useEffect, useMemo, useState } from "react";
import { Button, Stack, Text } from "@mantine/core";
import { ResponsiveModal, ResponsiveModalActions } from "@/components/ui";
import { dueLabel, dueWhen, formatMoney, fullDate, nextAfterPaying, type Bill, type DueItem } from "@/lib/bills/model";

export default function MarkPaidSheet({
  opened,
  onClose,
  bill,
  due,
  today,
  saving,
  onSubmit,
}: {
  opened: boolean;
  onClose: () => void;
  bill: Bill;
  /** The API's current reading of this bill, or null when it reported none. */
  due: DueItem | null;
  /** Today in the family's timezone, YYYY-MM-DD. */
  today: string;
  saving?: boolean;
  onSubmit: (args: { paidDate: string; forDueDate: string | null }) => void;
}) {
  const current = due?.due_date ?? null;
  const next = nextAfterPaying(due, bill.frequency);
  // The two dates the MCP's bill_pay accepts: the current due date, or one
  // period ahead. A bill with no reported due date offers only "no date".
  const choices = useMemo(() => {
    const out: { value: string; label: string; hint: string }[] = [];
    if (current) out.push({ value: current, label: fullDate(current), hint: "The next one" });
    if (next) out.push({ value: next, label: fullDate(next), hint: "Paying ahead" });
    return out;
  }, [current, next]);

  const [forDueDate, setForDueDate] = useState<string>(current ?? "");
  const [paidDate, setPaidDate] = useState<string>(today);

  useEffect(() => {
    if (opened) {
      setForDueDate(current ?? "");
      setPaidDate(today);
    }
  }, [opened, current, today]);

  // What the bill moves to: the due date after the one being paid.
  const afterThis = useMemo(() => {
    if (!forDueDate) return next;
    if (forDueDate === current) return next;
    return nextAfterPaying({ due_date: forDueDate }, bill.frequency);
  }, [forDueDate, current, next, bill.frequency]);

  const amount = formatMoney(bill.amount);

  return (
    <ResponsiveModal opened={opened} onClose={onClose} title="Mark paid" size="md">
      <Stack gap="md">
        <div className="rounded-token-md bg-surface-2 px-3.5 py-3">
          <b className="block text-[15px] font-semibold text-ink">{bill.name}</b>
          <span className="block text-[13px] text-ink-3">
            {amount ? `${amount}` : "No amount"} · {due ? `${dueLabel(due)} · ${dueWhen(due.days_until_due)}` : "no due date reported"}
          </span>
        </div>

        <div>
          <span className="mb-1.5 block text-[13px] font-semibold text-ink-2" id="mp-which">
            Which due date is this for?
          </span>
          <div role="radiogroup" aria-labelledby="mp-which" className="grid gap-2">
            {choices.length === 0 && (
              <Text size="sm" c="dimmed">
                The API did not report a due date for this bill, so there is no date to mark paid. Give it a due day or
                date first.
              </Text>
            )}
            {choices.map((c) => {
              const checked = forDueDate === c.value;
              return (
                <button
                  key={c.value}
                  type="button"
                  role="radio"
                  aria-checked={checked}
                  onClick={() => setForDueDate(c.value)}
                  className={[
                    "flex min-h-14 w-full items-center gap-3 rounded-token-md border px-3 py-2 text-left",
                    checked ? "border-accent bg-accent-soft" : "border-line bg-surface",
                  ].join(" ")}
                >
                  <span
                    aria-hidden
                    className={[
                      "grid h-5 w-5 flex-none place-items-center rounded-full border-2",
                      checked ? "border-accent-fill" : "border-ink-3",
                    ].join(" ")}
                  >
                    {checked && <span className="h-2.5 w-2.5 rounded-full bg-accent-fill" />}
                  </span>
                  <span className="min-w-0">
                    <b className="block text-[15px] font-semibold text-ink">{c.label}</b>
                    <span className="block text-[13px] text-ink-3">{c.hint}</span>
                  </span>
                </button>
              );
            })}
          </div>
        </div>

        <div>
          <label htmlFor="mp-on" className="mb-1.5 block text-[13px] font-semibold text-ink-2">
            Paid on
          </label>
          <input
            id="mp-on"
            type="date"
            value={paidDate}
            max={today}
            onChange={(e) => setPaidDate(e.currentTarget.value)}
            className="h-11 w-full rounded-token-md border border-line bg-surface px-3 text-base text-ink md:h-10 md:text-sm"
          />
        </div>

        <Text size="sm" c="dimmed" component="p">
          Home OS notes that it is paid, not how much.{" "}
          {afterThis ? (
            <>
              The reminder stops and <b>{bill.name}</b> moves to <b>{fullDate(afterThis)}</b>.
            </>
          ) : (
            <>This is a one-time bill, so it has no next date: paying it closes it.</>
          )}
        </Text>
      </Stack>

      <ResponsiveModalActions>
        <Button variant="default" onClick={onClose} className="max-md:min-h-11">
          Cancel
        </Button>
        <Button
          onClick={() => onSubmit({ paidDate, forDueDate: forDueDate || null })}
          loading={saving}
          disabled={!paidDate}
          className="max-md:min-h-11"
        >
          Mark paid
        </Button>
      </ResponsiveModalActions>
    </ResponsiveModal>
  );
}
