"use client";

/**
 * The event sheet (#4550, mockup #87 v1): one event, everything the API knows
 * about it, and what you can do next.
 *
 * What it says about a REPEATING event is the important part. The API expands a
 * series into occurrences for reading (so each date is its own fact) but its
 * PATCH edits the SERIES — there is no per-occurrence scope yet (KB
 * architecture/api-calendar-recurrence.md). Rather than offer a "just this one"
 * option that would silently change every lesson, the sheet states plainly that
 * Edit changes all of them, and the editor repeats that before saving.
 *
 * Bills and upkeep open their own record instead of an editor: their texts come
 * from another module, and a bill's mirror must never read as bill state.
 */
import { Button } from "@mantine/core";
import {
  IconBell,
  IconCalendarEvent,
  IconChevronRight,
  IconLock,
  IconMapPin,
  IconNote,
  IconRepeat,
  IconTrash,
  IconUsers,
} from "@tabler/icons-react";
import ResponsiveModal, { ResponsiveModalActions } from "@/components/ui/ResponsiveModal";
import {
  attributionLine,
  billReading,
  formatDayLabel,
  formatWallClock,
  formatWhen,
  isBillMirror,
  isUpkeepMirror,
  monthDay,
  readOnlyReason,
  remindersLine,
  repeatSummary,
  type Attribution,
  type BillRow,
  type CalendarRow,
  type CalEvent,
  type PersonRow,
  type Series,
} from "@/lib/calendar/model";

const ICON_STROKE = 1.75;

export interface EventSheetProps {
  opened: boolean;
  onClose: () => void;
  series: Series | null;
  occurrence: CalEvent | null;
  calendars: readonly CalendarRow[];
  bills: readonly BillRow[];
  people: readonly PersonRow[];
  attribution: Attribution | undefined;
  today: string;
  canWrite: boolean;
  onEdit: () => void;
  onDelete: () => void;
  onOpenRecord?: (href: string) => void;
}

export default function EventSheet({
  opened,
  onClose,
  series,
  occurrence,
  calendars,
  bills,
  people,
  attribution,
  today,
  canWrite,
  onEdit,
  onDelete,
  onOpenRecord,
}: EventSheetProps) {
  const ev = occurrence ?? series?.occurrences[0] ?? null;
  // Nothing to show: draw nothing. (Rendering a placeholder here left its text
  // visible under the next dialog while the sheet animated out.)
  if (!series || !ev) return null;
  const cal = calendars.find((c) => c.id === ev.calendar_id);
  const color = ev.color?.trim() || cal?.color?.trim() || "var(--accent)";
  const ro = readOnlyReason(ev, cal);
  const repeat = repeatSummary(ev);
  const bill = billReading(ev, bills, today, { onBillsCalendar: cal?.type === "bills" });
  const author = attributionLine(attribution);
  const involved = people.slice(0, 0); // The API does not return attendees yet; see the note below.

  return (
    <ResponsiveModal opened={opened} onClose={onClose} title={cal?.name ?? "Event"}>
      <div className="flex flex-col gap-4 pt-1" data-testid="event-sheet">
        <div className="flex gap-3.5">
          <span className="w-1 flex-none rounded-sm" style={{ background: color }} aria-hidden />
          <div className="min-w-0">
            <h2 className="text-[21px] font-semibold leading-tight tracking-tight text-ink" data-testid="sheet-title">
              {ev.title}
              {bill.kind === "bill" && bill.state === "due" && bill.amount ? ` · ${bill.amount}` : ""}
            </h2>
            <p className="mt-1 text-[15px] text-ink" data-testid="sheet-when">
              {formatWhen(ev)}
            </p>
            {repeat && (
              <p className="mt-0.5 flex items-center gap-1.5 text-sm text-ink-2" data-testid="sheet-repeat">
                <IconRepeat size={16} stroke={ICON_STROKE} className="flex-none text-ink-3" aria-hidden />
                {repeat}
              </p>
            )}
            {series.repeating && series.occurrences.length > 1 && (
              <p className="mt-0.5 text-[13px] text-ink-3" data-testid="sheet-series-note">
                One event with {series.occurrences.length} dates in view. Changing it changes every date.
              </p>
            )}
          </div>
        </div>

        {/* Bill state, read from the bills module — never from the mirror. */}
        {bill.kind === "bill" && (
          <div
            className="rounded-token-sm bg-surface-2 px-3 py-2.5 text-sm"
            data-testid="sheet-bill"
            data-bill-state={bill.state}
          >
            {bill.state === "paid" ? (
              <>
                <b className="font-semibold text-ink">Paid</b>
                <span className="text-ink-2">
                  {bill.paidFor ? ` — the payment covers ${monthDay(bill.paidFor)}.` : "."} The next one shows on its own date.
                </span>
              </>
            ) : (
              <>
                <b className="font-semibold text-ink">
                  {bill.dueDate ? `Due ${monthDay(bill.dueDate)}` : "Due"}
                  {bill.amount ? ` · ${bill.amount}` : ""}
                </b>
                <span className="text-ink-2">
                  {" "}
                  · {bill.autopay ? "autopay is on" : "not on autopay"}
                </span>
              </>
            )}
            <p className="mt-1 text-[12.5px] text-ink-3">
              This date comes from the bill. Change the amount or the date in Money and it updates here and on every phone.
            </p>
          </div>
        )}
        {bill.kind === "unlinked" && (
          <div className="rounded-token-sm bg-warn-bg px-3 py-2.5 text-sm text-warn-ink" data-testid="sheet-bill-unlinked">
            <b className="font-semibold">This sits on the Bills calendar but isn&apos;t linked to a bill.</b> It is a calendar entry,
            not a due date: nothing on this screen says what is owed or whether it was paid. Add the bill in Money to track it.
          </div>
        )}

        <dl className="border-t border-line">
          {ev.location && (
            <Row icon={<IconMapPin size={18} stroke={ICON_STROKE} />} label="Where" value={ev.location} />
          )}
          {/* The when line above already carries the day and the clock; this
              row appears only for an event that reaches into another day, so
              "4:00 – 4:45 PM" is never printed twice. */}
          {ev.end_local_date && ev.end_local_date !== ev.local_date && (
            <Row
              icon={<IconCalendarEvent size={18} stroke={ICON_STROKE} />}
              label="Ends"
              value={`${formatDayLabel(ev.end_local_date, "")}${ev.all_day ? "" : ` · ${formatWallClock(ev.end_local_time)}`}`}
            />
          )}
          <Row
            icon={<IconBell size={18} stroke={ICON_STROKE} />}
            label="Reminders"
            value={bill.kind === "bill" ? "3 days before, at 9:00 AM (set for every bill)" : remindersLine(ev.reminders)}
          />
          {involved.length > 0 && (
            <Row icon={<IconUsers size={18} stroke={ICON_STROKE} />} label="Who's involved" value={involved.map((p) => p.display_name).join(", ")} />
          )}
          {ev.description && <Row icon={<IconNote size={18} stroke={ICON_STROKE} />} label="Notes" value={ev.description} multiline />}
        </dl>

        {/* Who added it — only when the feed names someone. */}
        {author && (
          <p className="text-[13px] text-ink-3" data-testid="sheet-attribution">
            {author}
          </p>
        )}

        {ro && (
          <div className="flex gap-2.5 rounded-token-sm bg-surface-2 p-3 text-[13px] text-ink-2" data-testid="sheet-readonly">
            <IconLock size={18} stroke={ICON_STROKE} className="mt-px flex-none text-ink-3" aria-hidden />
            <span>{ro}</span>
          </div>
        )}
      </div>

      <ResponsiveModalActions>
        {canWrite && !ro && (
          <Button
            variant="subtle"
            color="red"
            size="sm"
            leftSection={<IconTrash size={16} stroke={ICON_STROKE} />}
            onClick={onDelete}
            data-testid="sheet-delete"
          >
            Delete
          </Button>
        )}
        <span className="flex-1" />
        {bill.kind === "bill" && bill.state !== "paid" && onOpenRecord && (
          <Button
            variant="default"
            size="sm"
            rightSection={<IconChevronRight size={16} stroke={ICON_STROKE} />}
            onClick={() => onOpenRecord("/dashboard/bills")}
          >
            Open in Money
          </Button>
        )}
        {isUpkeepMirror(ev) && onOpenRecord && (
          <Button
            variant="default"
            size="sm"
            rightSection={<IconChevronRight size={16} stroke={ICON_STROKE} />}
            onClick={() => onOpenRecord("/dashboard/maintenance")}
          >
            Open task
          </Button>
        )}
        {canWrite && !ro && !isBillMirror(ev) && !isUpkeepMirror(ev) && (
          <Button size="sm" onClick={onEdit} data-testid="sheet-edit">
            Edit{series.repeating ? " all dates" : ""}
          </Button>
        )}
      </ResponsiveModalActions>
    </ResponsiveModal>
  );
}

function Row({ icon, label, value, multiline }: { icon: React.ReactNode; label: string; value: string; multiline?: boolean }) {
  return (
    <div className="flex items-start gap-3.5 border-b border-line py-3 text-sm">
      <span className="mt-px flex-none text-ink-3" aria-hidden>
        {icon}
      </span>
      <div className="min-w-0 flex-1">
        <dt className="text-[13px] text-ink-3">{label}</dt>
        <dd className={multiline ? "mt-0.5 whitespace-pre-line text-ink" : "mt-0.5 truncate text-ink"}>{value}</dd>
      </div>
    </div>
  );
}
