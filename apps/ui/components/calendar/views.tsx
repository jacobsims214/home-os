"use client";

/**
 * The four views of the calendar (#4550). Agenda first: a parent asks "what's
 * on this week", not "draw me a month".
 *
 *  - AGENDA (the default on phones, and one of the choices everywhere): one
 *    section per day of the week, each a list of series cards. A quiet day says
 *    so in place, never by vanishing — a missing Wednesday is how a person
 *    misses that Wednesday is empty.
 *  - DAY / WEEK: a time grid. Blocks are placed by the wall clock the API sent
 *    (`local_time`), never by parsing an instant, so a block cannot drift.
 *  - MONTH: the six-week grid, its columns named by the API's day table.
 *
 * All four read the same `Series[]`, so a repeating event is one card in every
 * view and its occurrence chips are the same chips.
 */
import { useState } from "react";
import { IconAlertTriangle, IconCalendarOff, IconRefresh } from "@tabler/icons-react";
import {
  formatWallClock,
  monthDay,
  occurrencesOn,
  weekdayShort,
  type AgendaDay,
  type BillReading,
  type CalendarRow,
  type CalEvent,
  type DayInfo,
  type PersonRow,
  type Series,
} from "@/lib/calendar/model";
import SeriesCard, { SeriesContinuation, seriesColor } from "./series-card";

const ICON_STROKE = 1.75;

export interface ViewProps {
  days: readonly DayInfo[];
  agenda: readonly AgendaDay[];
  series: readonly Series[];
  calendars: readonly CalendarRow[];
  bills: readonly import("@/lib/calendar/model").BillRow[];
  people: readonly PersonRow[];
  today: string;
  /** local_date → the bill reading for a mirror event's entity id. */
  billFor: (ev: CalEvent) => BillReading | undefined;
  onOpen: (series: Series, occurrence: CalEvent) => void;
  onJumpToDate: (localDate: string) => void;
}

// ── agenda ──────────────────────────────────────────────────

export function AgendaView({ agenda, calendars, people, billFor, onOpen, onJumpToDate, today }: ViewProps) {
  const shown = agenda.filter((d) => d.isToday || d.localDate > today || d.series.length > 0);
  const list = shown.length > 0 ? shown : agenda.slice(0, 1);
  // The day each series gets its ONE card on: the first day of this window it
  // occurs. Later days carry a continuation row instead, so a Tue/Thu lesson is
  // one card plus one line — not two cards that look like two activities.
  const firstDay = new Map<string, string>();
  for (const day of agenda) {
    for (const s of day.series) {
      if (!firstDay.has(s.id)) firstDay.set(s.id, day.localDate);
    }
  }
  return (
    <div className="flex flex-col gap-5" data-testid="agenda-view">
      {list.map((day) => (
        <section key={day.localDate} aria-labelledby={`day-${day.localDate}`} data-testid="agenda-day" data-date={day.localDate}>
          <div className="mb-2 flex items-baseline justify-between gap-3">
            <h3 id={`day-${day.localDate}`} className="text-base font-semibold tracking-tight text-ink">
              <span className={day.isToday ? "text-accent" : ""}>{day.isToday ? "Today" : day.weekday}</span>
              <span className="ml-1.5 text-sm font-medium text-ink-3">{day.monthDay}</span>
            </h3>
            <span className="text-[13px] text-ink-3">
              {day.series.length === 0 ? "" : `${day.series.length} ${day.series.length === 1 ? "event" : "events"}`}
            </span>
          </div>
          {day.series.length === 0 ? (
            <div className="rounded-token-md border border-dashed border-line px-3.5 py-3 text-[13px] text-ink-3" data-testid="agenda-quiet">
              Nothing scheduled.
            </div>
          ) : (
            <div className="overflow-hidden rounded-token-md border border-line bg-surface">
              {sortWithinDay(day.series, day.localDate).map((s) => {
                const owner = firstDay.get(s.id) ?? day.localDate;
                const isOwner = owner === day.localDate;
                const occ = occurrencesOn(s, day.localDate)[0] ?? s.occurrences[0];
                return isOwner ? (
                  <SeriesCard
                    key={s.id}
                    series={s}
                    localDate={day.localDate}
                    calendars={calendars}
                    bill={occ ? billFor(occ) : undefined}
                    people={people}
                    past={day.localDate < today}
                    onOpen={onOpen}
                    onJumpToDate={onJumpToDate}
                  />
                ) : (
                  <SeriesContinuation
                    key={s.id}
                    series={s}
                    localDate={day.localDate}
                    calendars={calendars}
                    firstDate={owner}
                    past={day.localDate < today}
                    onOpen={onOpen}
                    onJumpToDate={onJumpToDate}
                  />
                );
              })}
            </div>
          )}
        </section>
      ))}
    </div>
  );
}

/** All-day first, then by the day's wall clock (strings the API produced). */
function sortWithinDay(series: readonly Series[], localDate: string): Series[] {
  return [...series].sort((a, b) => {
    const ao = occurrencesOn(a, localDate)[0];
    const bo = occurrencesOn(b, localDate)[0];
    const aAll = !!ao?.all_day;
    const bAll = !!bo?.all_day;
    if (aAll !== bAll) return aAll ? -1 : 1;
    return (ao?.local_time ?? "").localeCompare(bo?.local_time ?? "");
  });
}

// ── day / week time grid ────────────────────────────────────

/** The grid's hours: 7 AM to 10 PM covers what a family books, with an overflow note. */
const GRID_START_HOUR = 7;
const GRID_END_HOUR = 22;
const HOUR_PX = 52;

/** Minutes from the API's "HH:MM"; NaN-free by construction. */
function minutesOf(hhmm: string | null | undefined): number | null {
  const m = /^(\d{2}):(\d{2})$/.exec(hhmm ?? "");
  if (!m) return null;
  return Number(m[1]) * 60 + Number(m[2]);
}

interface Placed {
  series: Series;
  occurrence: CalEvent;
  startMin: number;
  endMin: number;
  column: number;
  columns: number;
}

/** Side-by-side placement for overlapping blocks (the mockup's layout()). */
function place(events: readonly { series: Series; occurrence: CalEvent }[]): Placed[] {
  const withTimes = events
    .map((e) => {
      const startMin = minutesOf(e.occurrence.local_time);
      const endMin = minutesOf(e.occurrence.end_local_time);
      return { ...e, startMin: startMin ?? 0, endMin: endMin && endMin > (startMin ?? 0) ? endMin : (startMin ?? 0) + 30 };
    })
    .sort((a, b) => a.startMin - b.startMin || b.endMin - a.endMin);
  const cols: number[] = [];
  const out: Placed[] = [];
  let clusterStart = 0;
  let maxEnd = -1;
  let cluster: typeof withTimes = [];
  const flush = () => {
    for (const item of cluster) {
      const placed = out.find((p) => p.occurrence === item.occurrence);
      if (placed) placed.columns = cols.length;
    }
    cols.length = 0;
  };
  for (const e of withTimes) {
    if (e.startMin >= maxEnd && cluster.length > 0) {
      flush();
      cluster = [];
      maxEnd = -1;
    }
    if (e.startMin < clusterStart) clusterStart = e.startMin;
    cluster.push(e);
    let c = cols.findIndex((end) => end <= e.startMin);
    if (c < 0) {
      c = cols.length;
      cols.push(e.endMin);
    } else cols[c] = e.endMin;
    maxEnd = Math.max(maxEnd, e.endMin);
    out.push({ ...e, column: c, columns: 1 });
  }
  flush();
  return out;
}

export function DayGridView(props: ViewProps) {
  const days = props.days.slice(0, 7);
  return <TimeGrid {...props} days={days} single={days.length === 1} />;
}

export function WeekGridView(props: ViewProps) {
  return <TimeGrid {...props} days={props.days.slice(0, 7)} single={false} />;
}

function TimeGrid({ days, series, calendars, billFor, onOpen, today, single }: ViewProps & { single: boolean }) {
  const allDayByDay = days.map((d) => series.filter((s) => occurrencesOn(s, d.local_date).some((o) => o.all_day)));
  const anyAllDay = allDayByDay.some((l) => l.length > 0);
  const untimed = new Set<string>();
  return (
    <div className="overflow-hidden rounded-token-md border border-line bg-surface" data-testid={single ? "day-grid" : "week-grid"}>
      <div
        className="grid border-b border-line"
        style={{ gridTemplateColumns: `56px repeat(${days.length}, minmax(0,1fr))` }}
      >
        <div />
        {days.map((d) => (
          <div
            key={d.local_date}
            className={[
              "border-l border-line px-1 py-2 text-center text-[12px] font-medium leading-tight",
              d.local_date === today ? "text-accent" : "text-ink-3",
            ].join(" ")}
            data-date={d.local_date}
          >
            {single ? (
              <>
                <b
                  className={[
                    "mx-auto grid h-8 w-8 place-items-center rounded-full text-[17px] font-semibold",
                    d.local_date === today ? "bg-accent-fill text-white" : "text-ink",
                  ].join(" ")}
                >
                  {Number(d.local_date.slice(8, 10))}
                </b>
                {d.weekday}
              </>
            ) : (
              <>
                {weekdayShort(d.weekday)}
                <b
                  className={[
                    "mx-auto mt-0.5 grid h-8 w-8 place-items-center rounded-full text-[17px] font-semibold",
                    d.local_date === today ? "bg-accent-fill text-white" : "text-ink",
                  ].join(" ")}
                >
                  {Number(d.local_date.slice(8, 10))}
                </b>
              </>
            )}
          </div>
        ))}
      </div>

      {anyAllDay && (
        <div className="grid border-b border-line bg-surface-2" style={{ gridTemplateColumns: `56px repeat(${days.length}, minmax(0,1fr))` }}>
          <div className="px-1.5 pt-1.5 text-right text-[11px] text-ink-3">All day</div>
          {allDayByDay.map((list, i) => (
            <div key={days[i].local_date} className="flex min-w-0 flex-col gap-1 border-l border-line p-1" data-testid="grid-allday">
              {list.map((s) => {
                const occ = occurrencesOn(s, days[i].local_date)[0];
                return (
                  <button
                    key={s.id}
                    type="button"
                    onClick={() => onOpen(s, occ)}
                    className="flex h-6 items-center gap-1 overflow-hidden rounded-[5px] px-1.5 text-left text-[11.5px] font-medium text-ink"
                    style={{ background: `color-mix(in srgb, ${seriesColor(s, calendars)} 16%, var(--surface))` }}
                    data-testid="grid-allday-chip"
                  >
                    <span className="h-1.5 w-1.5 flex-none rounded-full" style={{ background: seriesColor(s, calendars) }} aria-hidden />
                    <span className="truncate">
                      {s.title}
                      {(() => {
                        const b = billFor(occ);
                        if (b?.kind === "bill" && b.state === "due" && b.amount) return ` ${b.amount}`;
                        if (b?.kind === "bill" && b.state === "paid") return " · paid";
                        return "";
                      })()}
                    </span>
                  </button>
                );
              })}
            </div>
          ))}
        </div>
      )}

      <div className="grid" style={{ gridTemplateColumns: `56px repeat(${days.length}, minmax(0,1fr))` }}>
        <div className="relative" style={{ height: (GRID_END_HOUR - GRID_START_HOUR) * HOUR_PX }}>
          {Array.from({ length: GRID_END_HOUR - GRID_START_HOUR }).map((_, i) => (
            <span
              key={i}
              className="absolute right-1.5 -translate-y-1/2 text-[11px] whitespace-nowrap text-ink-3"
              style={{ top: i * HOUR_PX }}
            >
              {formatWallClock(`${String(GRID_START_HOUR + i).padStart(2, "0")}:00`)}
            </span>
          ))}
        </div>
        {days.map((d) => {
          const todayCls = d.local_date === today ? "bg-accent-soft/40" : "";
          const blocks = place(
            series.flatMap((s) =>
              occurrencesOn(s, d.local_date)
                .filter((o) => !o.all_day && minutesOf(o.local_time) !== null)
                .map((o) => {
                  if ((minutesOf(o.local_time) ?? 0) < GRID_START_HOUR * 60 || (minutesOf(o.local_time) ?? 0) >= GRID_END_HOUR * 60) untimed.add(s.id);
                  return { series: s, occurrence: o };
                }),
            ),
          );
          return (
            <div
              key={d.local_date}
              className={["relative border-l border-line", todayCls].join(" ")}
              style={{
                height: (GRID_END_HOUR - GRID_START_HOUR) * HOUR_PX,
                backgroundImage:
                  "repeating-linear-gradient(to bottom, transparent 0 " + (HOUR_PX - 1) + "px, var(--line) " + (HOUR_PX - 1) + "px " + HOUR_PX + "px)",
              }}
              data-date={d.local_date}
            >
              {blocks.map((b) => {
                const top = ((b.startMin - GRID_START_HOUR * 60) / 60) * HOUR_PX;
                const height = Math.max(22, ((b.endMin - b.startMin) / 60) * HOUR_PX - 2);
                const color = seriesColor(b.series, calendars);
                const short = b.endMin - b.startMin < 45;
                const width = 100 / b.columns;
                return (
                  <button
                    key={`${b.series.id}-${b.occurrence.recurrence_id ?? d.local_date}`}
                    type="button"
                    onClick={() => onOpen(b.series, b.occurrence)}
                    className={[
                      "absolute overflow-hidden rounded-[6px] border-l-[3px] px-1.5 py-0.5 text-left text-[12px] leading-tight text-ink",
                      short ? "flex items-center gap-1.5" : "",
                    ].join(" ")}
                    style={{
                      top,
                      height,
                      left: `calc(${b.column * width}% + 2px)`,
                      width: `calc(${width}% - 4px)`,
                      borderColor: color,
                      background: `color-mix(in srgb, ${color} 15%, var(--surface))`,
                    }}
                    data-testid="grid-block"
                    data-series-id={b.series.id}
                  >
                    <b className="block truncate font-semibold">{b.series.title}</b>
                    <span className="block truncate text-[11.5px] text-ink-2">
                      {short ? formatWallClock(b.occurrence.local_time) : `${formatWallClock(b.occurrence.local_time)} – ${formatWallClock(b.occurrence.end_local_time)}`}
                    </span>
                  </button>
                );
              })}
            </div>
          );
        })}
      </div>
      {untimed.size > 0 && (
        <p className="border-t border-line px-3.5 py-2 text-[12px] text-ink-3" data-testid="grid-outside-hours">
          Some events fall outside {formatWallClock("07:00")} – {formatWallClock("22:00")}. The agenda lists the whole day.
        </p>
      )}
    </div>
  );
}

// ── month ───────────────────────────────────────────────────

/**
 * Month, in two shapes (mockup #87 v1).
 *
 *  - PHONE: a compact month (day numbers, a dot per calendar with something on
 *    that day) and, under it, the selected day's list. A 7-column grid of event
 *    titles at 390 px truncates every title to "4pm P", which reads as noise;
 *    the mockup's phone month is the month for orientation plus one real day.
 *  - TABLET AND DESKTOP: the six-week grid, with each cell's chips.
 *
 * Both read the same series, so tapping a day lands on the same events.
 */
export function MonthView(props: ViewProps) {
  return (
    <>
      <div className="md:hidden">
        <PhoneMonth {...props} />
      </div>
      <div className="hidden md:block">
        <MonthGrid {...props} />
      </div>
    </>
  );
}

const WEEKDAY_INITIALS = ["S", "M", "T", "W", "T", "F", "S"];

/** The phone's month: a compact month, then the selected day. */
function PhoneMonth({ days, series, calendars, billFor, onOpen, today, people }: ViewProps) {
  // Picking a day in the compact month shows that day BELOW, in place: the
  // month is for orientation, and switching views under the person's finger
  // is not what tapping a date means here.
  const todayInGrid = days.find((d) => d.local_date === today)?.local_date ?? null;
  const [picked, setPicked] = useState<string | null>(null);
  const selected = picked ?? todayInGrid ?? days[0]?.local_date ?? today;
  const selectedDay = days.find((d) => d.local_date === selected);
  const daySeries = series
    .filter((s) => occurrencesOn(s, selected).length > 0)
    .sort((a, b) => {
      const ao = occurrencesOn(a, selected)[0];
      const bo = occurrencesOn(b, selected)[0];
      if (!!ao?.all_day !== !!bo?.all_day) return ao?.all_day ? -1 : 1;
      return (ao?.local_time ?? "").localeCompare(bo?.local_time ?? "");
    });
  return (
    <div className="flex flex-col gap-5" data-testid="month-view">
      <div className="rounded-token-md border border-line bg-surface p-2" data-testid="phone-month">
        <div className="grid grid-cols-7 text-center">
          {WEEKDAY_INITIALS.map((initial, i) => (
            <span key={i} className="py-1 text-[11px] font-semibold text-ink-3">
              {initial}
            </span>
          ))}
        </div>
        <div className="grid grid-cols-7">
          {days.map((d) => {
            const dots = series
              .filter((s) => occurrencesOn(s, d.local_date).length > 0)
              .map((s) => seriesColor(s, calendars))
              .filter((c, i, all) => all.indexOf(c) === i)
              .slice(0, 3);
            const isSelected = d.local_date === selected;
            return (
              <button
                key={d.local_date}
                type="button"
                onClick={() => setPicked(d.local_date)}
                aria-pressed={isSelected}
                aria-label={`${d.weekday} ${monthDay(d.local_date)}`}
                className="flex h-10 flex-col items-center justify-center gap-0.5 rounded-[10px] text-[13px] text-ink"
                data-date={d.local_date}
                data-testid="phone-month-day"
              >
                <span
                  className={[
                    "grid h-7 w-7 place-items-center rounded-full",
                    d.local_date === today ? "font-semibold text-accent ring-1 ring-accent" : "",
                    isSelected ? "bg-accent-fill text-white ring-0" : "",
                  ].join(" ")}
                >
                  {Number(d.local_date.slice(8, 10))}
                </span>
                <span className="flex h-1.5 gap-0.5">
                  {dots.map((c, i) => (
                    <i key={i} className="h-1.5 w-1.5 rounded-full" style={{ background: c }} aria-hidden />
                  ))}
                </span>
              </button>
            );
          })}
        </div>
      </div>

      <section aria-labelledby="month-day-heading">
        <div className="mb-2 flex items-baseline justify-between gap-3">
          <h3 id="month-day-heading" className="text-base font-semibold tracking-tight text-ink">
            <span className={selected === today ? "text-accent" : ""}>{selected === today ? "Today" : selectedDay?.weekday ?? ""}</span>
            <span className="ml-1.5 text-sm font-medium text-ink-3">{monthDay(selected)}</span>
          </h3>
          <span className="text-[13px] text-ink-3">{daySeries.length === 0 ? "" : `${daySeries.length} ${daySeries.length === 1 ? "event" : "events"}`}</span>
        </div>
        {daySeries.length === 0 ? (
          <div className="rounded-token-md border border-dashed border-line px-3.5 py-3 text-[13px] text-ink-3" data-testid="month-quiet">
            Nothing scheduled. Pick another day above, or switch to Agenda for the week.
          </div>
        ) : (
          <div className="overflow-hidden rounded-token-md border border-line bg-surface">
            {daySeries.map((s) => (
              <SeriesCard
                key={s.id}
                series={s}
                localDate={selected}
                calendars={calendars}
                bill={seriesOccurrence(s, selected) ? billFor(seriesOccurrence(s, selected)!) : undefined}
                people={people}
                past={selected < today}
                onOpen={onOpen}
              />
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

function seriesOccurrence(s: Series, localDate: string): CalEvent | undefined {
  return occurrencesOn(s, localDate)[0];
}

/** Tablet and desktop: the six-week grid. */
function MonthGrid({ days, series, calendars, today, billFor, onOpen, onJumpToDate }: ViewProps) {
  const weeks: DayInfo[][] = [];
  for (let i = 0; i < days.length; i += 7) weeks.push(days.slice(i, i + 7));
  const monthPrefix = (days[Math.floor(days.length / 2)]?.local_date ?? today).slice(0, 7);
  return (
    <div className="overflow-hidden rounded-token-md border border-line bg-surface" data-testid="month-grid">
      <div className="grid grid-cols-7 border-b border-line">
        {(weeks[0] ?? days.slice(0, 7)).map((d) => (
          <span key={d.local_date} className="px-2 py-2 text-[11px] font-semibold uppercase tracking-wide text-ink-3">
            {weekdayShort(d.weekday)}
          </span>
        ))}
      </div>
      <div className="grid grid-cols-7">
        {weeks.flat().map((d) => {
          const inMonth = d.local_date.slice(0, 7) === monthPrefix;
          const daySeries = series.filter((s) => occurrencesOn(s, d.local_date).length > 0);
          const cap = daySeries.length > 4 ? 3 : 4;
          return (
            <div
              key={d.local_date}
              className={[
                "min-h-[104px] min-w-0 border-b border-r border-line p-1 md:min-h-[116px]",
                inMonth ? "" : "bg-surface-2",
                d.local_date === today ? "bg-accent-soft/40" : "",
              ].join(" ")}
              data-date={d.local_date}
              data-testid="month-cell"
            >
              <button
                type="button"
                onClick={() => onJumpToDate(d.local_date)}
                className={[
                  "mb-0.5 grid h-6 w-6 place-items-center rounded-full text-[13px] font-semibold",
                  d.local_date === today ? "bg-accent-fill text-white" : inMonth ? "text-ink" : "font-medium text-ink-3",
                ].join(" ")}
                aria-label={`${d.weekday} ${monthDay(d.local_date)}`}
              >
                {Number(d.local_date.slice(8, 10))}
              </button>
              <div className="flex flex-col gap-px">
                {daySeries.slice(0, cap).map((s) => {
                  const occ = occurrencesOn(s, d.local_date)[0];
                  const color = seriesColor(s, calendars);
                  const b = billFor(occ);
                  return (
                    <button
                      key={s.id}
                      type="button"
                      onClick={() => onOpen(s, occ)}
                      className={[
                        "flex h-[22px] min-w-0 items-center gap-1 overflow-hidden rounded-[4px] px-1 text-left text-[12px] text-ink hover:bg-surface-2",
                        occ?.all_day ? "font-medium" : "",
                      ].join(" ")}
                      style={occ?.all_day ? { background: `color-mix(in srgb, ${color} 16%, var(--surface))` } : undefined}
                      data-testid="month-chip"
                      data-series-id={s.id}
                    >
                      {occ?.all_day ? (
                        <span className="h-1.5 w-1.5 flex-none rounded-full" style={{ background: color }} aria-hidden />
                      ) : (
                        <time className="flex-none text-[11px] text-ink-3">{formatWallClock(occ?.local_time).replace(":00", "").replace(" ", "").toLowerCase()}</time>
                      )}
                      <span className="truncate">{s.title}</span>
                      {b?.kind === "bill" && b.state === "paid" && (
                        <span className="flex-none text-[10px] font-semibold text-ink-3" aria-label="paid">
                          paid
                        </span>
                      )}
                    </button>
                  );
                })}
                {daySeries.length > cap && (
                  <button
                    type="button"
                    onClick={() => onJumpToDate(d.local_date)}
                    className="min-h-[22px] self-start px-1 text-[12px] font-semibold text-accent"
                  >
                    {daySeries.length - cap} more
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

// ── states ──────────────────────────────────────────────────

/** A read that failed. Never an empty grid (audit #74). */
export function CalendarError({ what, onRetry }: { what: string; onRetry: () => void }) {
  return (
    <div
      className="flex items-start gap-3 rounded-token-md border border-line border-l-4 border-l-[var(--danger)] bg-surface p-3.5"
      role="alert"
      data-testid="calendar-error"
    >
      <IconAlertTriangle size={22} stroke={ICON_STROKE} className="mt-0.5 flex-none text-[var(--danger)]" aria-hidden />
      <div className="min-w-0">
        <p className="text-[15px] font-semibold text-ink">{what}</p>
        <p className="mt-0.5 text-[13px] text-ink-2">
          Home OS didn&apos;t answer, so nothing is shown rather than an empty week. Your events are safe — phones that sync with Home OS
          still have them. Check your connection, then try again.
        </p>
        <button
          type="button"
          onClick={onRetry}
          className="mt-2 inline-flex h-9 items-center gap-1.5 rounded-token-sm border border-line bg-surface px-3 text-[13px] font-semibold text-ink hover:bg-surface-2"
        >
          <IconRefresh size={16} stroke={ICON_STROKE} aria-hidden />
          Try again
        </button>
      </div>
    </div>
  );
}

/** Skeleton rows, only in the content area (the shell and controls stay up). */
export function CalendarSkeleton({ rows = 4 }: { rows?: number }) {
  return (
    <div className="overflow-hidden rounded-token-md border border-line bg-surface" role="status" aria-busy="true" data-testid="calendar-skeleton">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="flex items-center gap-3 border-b border-line px-3.5 py-3.5 last:border-b-0">
          <span className="h-3.5 w-12 rounded bg-line" />
          <span className="h-8 w-[3px] rounded bg-line" />
          <span className="flex-1">
            <span className="block h-3.5 w-[55%] rounded bg-line" />
            <span className="mt-1.5 block h-3 w-[35%] rounded bg-line" />
          </span>
        </div>
      ))}
    </div>
  );
}

/** The empty state: what is missing and what fills it, never a blank grid. */
export function CalendarEmpty({
  title,
  body,
  action,
  onNew,
  onShowAll,
}: {
  title: string;
  body: string;
  action: "new" | "show-all" | null;
  onNew?: () => void;
  onShowAll?: () => void;
}) {
  return (
    <div className="flex items-start gap-3 rounded-token-md border border-line bg-surface p-5" data-testid="calendar-empty">
      <span className="grid h-12 w-12 flex-none place-items-center rounded-[14px] bg-accent-soft text-accent" aria-hidden>
        <IconCalendarOff size={24} stroke={ICON_STROKE} />
      </span>
      <div className="min-w-0">
        <p className="text-[17px] font-semibold text-ink">{title}</p>
        <p className="mt-1 max-w-[46ch] text-sm text-ink-2">{body}</p>
        <div className="mt-3 flex flex-wrap gap-2">
          {action === "new" && onNew && (
            <button
              type="button"
              onClick={onNew}
              className="inline-flex h-10 items-center rounded-token-sm bg-accent-fill px-3.5 text-sm font-semibold text-white"
            >
              Add an event
            </button>
          )}
          {action === "show-all" && onShowAll && (
            <button
              type="button"
              onClick={onShowAll}
              className="inline-flex h-10 items-center rounded-token-sm border border-line bg-surface px-3.5 text-sm font-semibold text-ink"
            >
              Show all calendars
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
