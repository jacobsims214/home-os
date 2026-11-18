"use client";

/**
 * One card per thing, with its dates (#4550).
 *
 * THE POINT OF THIS COMPONENT: round one of the R1 war game turned one weekly
 * activity into 35 rows, so a parent scrolling the week saw the same swim
 * practice nine times and could not tell it was one thing. A series here is ONE
 * card — the title, the rule and the count — and its dates are a strip of chips
 * inside it: Tue 17, Thu 19, Tue 24… Tapping a chip opens that day.
 *
 * Every date and time on the card is a string the API produced: `local_date`,
 * `weekday`, `local_time` (apps/api/internal/calendar/localday.go). Nothing
 * here builds a Date, so no browser zone can move an event to the wrong day.
 */
import { useState } from "react";
import { IconAlertTriangle, IconCheck, IconRepeat } from "@tabler/icons-react";
import {
  formatTimeRange,
  formatWallClock,
  monthDay,
  repeatSummary,
  type BillReading,
  type CalendarRow,
  type CalEvent,
  type Series,
} from "@/lib/calendar/model";

const ICON_STROKE = 1.75;

/** The colour an event draws in: its own, else its calendar's, else the accent. */
export function seriesColor(series: Series, calendars: readonly CalendarRow[]): string {
  const own = series.occurrences[0]?.color?.trim();
  if (own) return own;
  const cal = calendars.find((c) => c.id === series.calendarId);
  return cal?.color?.trim() || "var(--accent)";
}

export interface SeriesCardProps {
  series: Series;
  /** The day this instance of the card sits under (agenda/day views). */
  localDate?: string;
  calendars: readonly CalendarRow[];
  /** Bill state for a mirror event; undefined when bills did not load. */
  bill?: BillReading;
  /** The viewer's first name, for "You" in the who's-involved line. */
  viewerPersonId?: string | null;
  people?: readonly { id: string; display_name: string }[];
  /** "past" dims a card whose day is over. */
  past?: boolean;
  onOpen: (series: Series, occurrence: CalEvent) => void;
  onJumpToDate?: (localDate: string) => void;
}

/**
 * The time column: "All day", the start time, or — when the event crosses into
 * another hour — the start time with the range under it. Every part is the
 * API's own wall clock (`local_time` / `end_local_time`).
 */
function timeLabel(series: Series, occurrence: CalEvent | undefined): { top: string; sub: string } {
  if (series.allDay) return { top: "All day", sub: "" };
  const start = occurrence ? formatWallClock(occurrence.local_time) : "";
  const end = occurrence ? formatWallClock(occurrence.end_local_time) : "";
  const range = occurrence ? formatTimeRange(occurrence) : "";
  if (!start) return { top: range || "—", sub: "" };
  const crossesHour = !!end && start.slice(0, 2) !== end.slice(0, 2);
  return { top: start, sub: crossesHour ? range : "" };
}

export default function SeriesCard({
  series,
  localDate,
  calendars,
  bill,
  people,
  past,
  onOpen,
  onJumpToDate,
}: SeriesCardProps) {
  const [expanded, setExpanded] = useState(false);
  const color = seriesColor(series, calendars);
  const here = series.occurrences.find((o) => (o.local_date ?? "") === localDate) ?? series.occurrences[0];
  const { top, sub } = timeLabel(series, here);
  const rule = repeatSummary(series);
  const count = series.occurrences.length;
  const people_ids = (here as CalEvent & { person_ids?: string[] }).person_ids ?? [];
  const involved = people_ids.map((id) => people?.find((p) => p.id === id)).filter(Boolean);
  // Three chips inline; the rest behind "N more", so a term's worth of dates
  // never turns the card into a wall.
  const shown = expanded ? series.occurrences : series.occurrences.slice(0, 3);
  const hidden = count - shown.length;

  return (
    <article
      className={[
        "group grid min-h-[60px] grid-cols-[58px_3px_minmax(0,1fr)] items-start gap-3 border-b border-line px-3.5 py-3 last:border-b-0",
        past ? "text-ink-3" : "",
      ].join(" ")}
      data-testid="series-card"
      data-series-id={series.id}
      data-repeating={series.repeating ? "true" : "false"}
      data-day={localDate}
    >
      <div className="flex flex-col gap-0.5 pt-0.5">
        <time className="text-[13px] font-medium leading-tight" style={{ color: past ? "var(--ink-3)" : "var(--ink-2)" }}>
          {top || "—"}
        </time>
        {sub && sub !== top && <span className="text-[11px] leading-tight text-ink-3">{sub}</span>}
      </div>
      <span className="min-h-[38px] self-stretch rounded-sm" style={{ background: color, opacity: past ? 0.4 : 1 }} aria-hidden />
      <div className="min-w-0">
        <div className="flex items-start gap-2">
          <button
            type="button"
            onClick={() => onOpen(series, here)}
            className="min-h-6 min-w-0 flex-1 text-left"
            data-testid="series-open"
          >
            <b className={["block truncate text-[15px] leading-snug", past ? "font-medium text-ink-2" : "font-semibold text-ink"].join(" ")}>
              {series.title}
            </b>
          </button>
          {series.repeating && count > 1 && (
            <span
              className="inline-flex flex-none items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-[11px] font-semibold text-ink-2"
              data-testid="series-count"
              aria-label={`${count} dates in view`}
            >
              <IconRepeat size={12} stroke={ICON_STROKE} aria-hidden />
              {count} dates
            </span>
          )}
        </div>

        {/* The rule, where it came from, and — for a bill — its real state. */}
        {rule && (
          <p className="mt-0.5 text-[13px] leading-snug text-ink-3" data-testid="series-rule">
            {rule}
          </p>
        )}
        {/* Bill state is read from the bills module, and it is NEVER "due" once
            paid; an entry nobody linked to a bill says what it is instead of
            borrowing bill state. */}
        {bill?.kind === "bill" && bill.state === "paid" && (
          <p className="mt-0.5 flex items-center gap-1 text-[13px] font-medium leading-snug text-ink-2" data-testid="bill-paid">
            <IconCheck size={13} stroke={ICON_STROKE} className="flex-none" aria-hidden />
            {bill.paidFor ? `Paid for ${monthDay(bill.paidFor)}` : "Paid"}
          </p>
        )}
        {bill?.kind === "bill" && bill.state === "due" && (
          <p className="mt-0.5 text-[13px] font-medium leading-snug text-ink-2" data-testid="bill-due">
            {bill.amount ?? "Due"}
            {bill.amount && " · "}
            {bill.daysUntil === 0
              ? "due today"
              : bill.daysUntil > 0
                ? `due in ${bill.daysUntil} ${bill.daysUntil === 1 ? "day" : "days"}`
                : `overdue by ${Math.abs(bill.daysUntil)} days`}
          </p>
        )}
        {bill?.kind === "unlinked" && (
          <p className="mt-0.5 flex items-start gap-1 text-[13px] leading-snug text-warn-ink" data-testid="bill-unlinked">
            <IconAlertTriangle size={13} stroke={ICON_STROKE} className="mt-0.5 flex-none" aria-hidden />
            On the Bills calendar, not linked to a bill — this is not a due date
          </p>
        )}

        {involved.length > 0 && (
          <p className="mt-0.5 truncate text-[13px] leading-snug text-ink-3" data-testid="series-people">
            {involved.map((p) => p!.display_name.split(/\s+/)[0]).join(", ")}
          </p>
        )}

        {/* The dates: one chip each, in the same card. */}
        {count > 1 && (
          <div className="mt-1.5 flex flex-wrap items-center gap-1" data-testid="series-occurrences">
            {shown.map((o) => {
              const isHere = (o.local_date ?? "") === localDate;
              return (
                <button
                  key={o.recurrence_id ?? `${o.local_date}-${o.local_time}`}
                  type="button"
                  onClick={() => (onJumpToDate ? onJumpToDate(o.local_date ?? "") : onOpen(series, o))}
                  className={[
                    "min-h-6 rounded-full border px-2 text-[11.5px] font-medium leading-5",
                    isHere
                      ? "border-accent bg-accent-soft text-accent"
                      : "border-line bg-surface text-ink-2 hover:bg-surface-2",
                  ].join(" ")}
                  aria-current={isHere ? "date" : undefined}
                  data-testid="occurrence-chip"
                  data-occurrence={o.recurrence_id ?? o.local_date}
                >
                  {(o.weekday ?? "").slice(0, 3)} {monthDay(o.local_date ?? "").split(" ").pop()}
                </button>
              );
            })}
            {hidden > 0 && (
              <button
                type="button"
                onClick={() => setExpanded(true)}
                className="min-h-6 rounded-full px-2 text-[11.5px] font-semibold text-accent"
                aria-expanded={false}
                data-testid="occurrences-more"
              >
                +{hidden} more
              </button>
            )}
            {expanded && count > 3 && (
              <button
                type="button"
                onClick={() => setExpanded(false)}
                className="min-h-6 rounded-full px-2 text-[11.5px] font-semibold text-accent"
                aria-expanded
              >
                Show less
              </button>
            )}
          </div>
        )}
      </div>
    </article>
  );
}

/**
 * The same series on a LATER day of the window: a compact row, not a second
 * card (#4550).
 *
 * A weekly lesson happens on Tuesday AND Thursday. Drawing the full card twice
 * is exactly what round one looked like from the parent's side — the same
 * activity repeated down the page. So the series gets ONE card, on the first day
 * it occurs here, and the other days carry this row: the time, the title, and a
 * link back to the card that owns all the dates.
 */
export function SeriesContinuation({
  series,
  localDate,
  calendars,
  firstDate,
  past,
  onOpen,
  onJumpToDate,
}: {
  series: Series;
  localDate: string;
  calendars: readonly CalendarRow[];
  firstDate: string;
  past?: boolean;
  onOpen: (series: Series, occurrence: CalEvent) => void;
  onJumpToDate?: (localDate: string) => void;
}) {
  const color = seriesColor(series, calendars);
  const occurrence = series.occurrences.find((o) => (o.local_date ?? "") === localDate) ?? series.occurrences[0];
  const { top } = timeLabel(series, occurrence);
  return (
    <div
      className="grid min-h-[56px] grid-cols-[58px_3px_minmax(0,1fr)] items-center gap-3 border-b border-line px-3.5 py-2.5 last:border-b-0"
      data-testid="series-continuation"
      data-series-id={series.id}
      data-day={localDate}
    >
      <time className="text-[13px] font-medium leading-tight text-ink-2">{top || "—"}</time>
      <span className="min-h-[30px] self-stretch rounded-sm" style={{ background: color, opacity: past ? 0.4 : 1 }} aria-hidden />
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <button type="button" onClick={() => onOpen(series, occurrence)} className="min-h-6 min-w-0 flex-1 text-left">
            <b className={["block truncate text-[14px] leading-snug", past ? "font-medium text-ink-2" : "font-semibold text-ink"].join(" ")}>
              {series.title}
            </b>
          </button>
          <IconRepeat size={14} stroke={1.75} className="flex-none text-ink-3" aria-label="Repeats" />
        </div>
        <button
          type="button"
          onClick={() => (onJumpToDate ? onJumpToDate(firstDate) : onOpen(series, series.occurrences[0]))}
          className="mt-0.5 block max-w-full truncate text-left text-[12.5px] text-ink-3 underline decoration-dotted underline-offset-2 hover:text-ink-2"
          data-testid="series-continuation-link"
        >
          Same event as {monthDay(firstDate)} · see dates
        </button>
      </div>
    </div>
  );
}
