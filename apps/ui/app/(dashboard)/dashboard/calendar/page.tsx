"use client";

/**
 * Calendar (#4550) — agenda first, with recurrence, built to the approved
 * mockup (artifact #87 v1) inside the approved shell (artifact #75 v1), and
 * reading the same API Today (#4549) reads so the two screens always agree.
 *
 * WHAT THE R1 WAR GAMES CHANGED HERE (KB discoveries/war-game-r1-*.md):
 *
 *  1. AGENDA FIRST. A parent asks "what's on this week". So the default view is
 *     a week of days, each a list of what's on it; day/week/month are there for
 *     the times a grid is the right answer. On a phone the toolbar offers
 *     Agenda / Day / Month (the week grid is a tablet-and-up tool, per the
 *     mockup).
 *  2. ONE RECURRING EVENT IS ONE THING. Round one turned a weekly activity into
 *     9 and 35 rows. The API returns one row per occurrence (each carrying the
 *     series id and its recurrence_id); groupSeries() puts them back together, so
 *     a Tue/Thu lesson is one card with its dates as chips.
 *  3. THE DAY AND TIME COME FROM THE DATA. Every event carries local_date,
 *     weekday, local_time and end_local_* computed by the API in the
 *     household's zone (apps/api/internal/calendar/localday.go). Nothing on
 *     this page builds a weekday from a Date, so no browser zone can move an
 *     event — which is exactly how round one shifted an all-day event a day.
 *  4. ALL-DAY IS A DATE, NEVER AN INSTANT: it renders on its local_date as
 *     "All day" and never borrows a clock time.
 *  5. WHO ADDED WHAT, where the feed knows: the sheet prints "Added by Amber
 *     via moltis-wg-amber". Cross-family visibility is proven, and attribution
 *     is what stops "did you book that?".
 *  6. BILLS. Their mirrors sit on the Bills calendar, their amount and their
 *     PAID/DUE state come from GET /bills through the one shared bill rule, and
 *     an entry on the Bills calendar that is not linked to a bill says so
 *     instead of borrowing bill state.
 *  7. A FAILED READ IS NEVER AN EMPTY WEEK (audit #74): each source has its own
 *     error card with Try again, and the empty state explains what would fill
 *     it. Skeleton only in the content area.
 *
 * WHAT THIS PAGE CANNOT DO, said in the UI rather than faked: the REST API's
 * PATCH edits a series as a whole — there is no per-occurrence scope yet (KB
 * architecture/api-calendar-recurrence.md). The editor says "Saving changes
 * every date" before the person commits, and offers no "just this one" button
 * that could not keep its promise.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { SegmentedControl, Skeleton } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconChevronLeft, IconChevronRight, IconFilter, IconPlus, IconX } from "@tabler/icons-react";
import EventSheet from "@/components/calendar/event-sheet";
import EventEditor from "@/components/calendar/event-editor";
import { AgendaView, CalendarEmpty, CalendarError, CalendarSkeleton, DayGridView, MonthView, WeekGridView } from "@/components/calendar/views";
import {
  attributionMap,
  billReading,
  buildAgenda,
  emptyReason,
  eventBody,
  groupSeries,
  monthGrid,
  monthStart,
  daysFromEvents,
  missingDayTable,
  rangeFor,
  rangeTitle,
  seriesId,
  weekRangeLabel,
  type AgendaDay,
  type BillReading,
  type CalendarRow,
  type CalendarView,
  type CalEvent,
  type DayInfo,
  type EventFormValues,
  type Series,
} from "@/lib/calendar/model";
import { useCalendarAccess, useCalendarActivity, useCalendarEvents, useCalendarHousehold, useCalendars, useBills, useCreateEvent, useDeleteEvent, usePeople, useUpdateEvent } from "@/lib/calendar/api";
import { useHouseholdReadiness } from "@/components/shared/use-nav-role";
import { calendarReadsReady } from "@/lib/auth/session-readiness";
import { householdToday } from "@/lib/today/agenda";

const ICON_STROKE = 1.75;
/**
 * The view switch. Agenda is first because that is what a parent asks for
 * ("what's on this week"); Week is the grid a tablet or desktop can show, and
 * on a phone it stays out of the way (the mockup hides it there) — the agenda
 * covers the same week in a form a phone can read.
 */
const VIEW_OPTIONS: { value: string; label: string }[] = [
  { value: "agenda", label: "Agenda" },
  { value: "day", label: "Day" },
  { value: "week", label: "Week" },
  { value: "month", label: "Month" },
];

export default function CalendarPage() {
  const router = useRouter();
  const household = useCalendarHousehold();
  const timezone = household?.timezone ?? null;
  const { canWrite } = useCalendarAccess();

  /**
   * The precondition for every read on this page (#5307): the household the
   * person is in must be KNOWN before a request goes out. A fresh sign-in has
   * just cleared the household cookie (#4574), so a read fired before
   * /me/households answers is scoped to a guess — that is the "Couldn't load
   * the calendar" right after sign-in. Waiting here is reading the real
   * precondition, not a delay: the query starts the moment the answer lands.
   */
  const householdReady = useHouseholdReadiness();

  // Today in the HOUSEHOLD's zone, after mount: the server's clock is not the
  // family's, and a page that says "today" on the wrong day is worse than one
  // that paints a skeleton first.
  const [today, setToday] = useState<string | null>(null);
  const [view, setView] = useState<CalendarView>("agenda");
  const [anchor, setAnchor] = useState<string | null>(null);
  const [hidden, setHidden] = useState<Record<string, boolean>>({});
  const [sheet, setSheet] = useState<{ series: Series; occurrence: CalEvent } | null>(null);
  const [editing, setEditing] = useState<{ series: Series | null; occurrence: CalEvent | null } | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  // The week GRID is a wider-screen tool (the mockup hides it on phones): the
  // agenda covers the same week in a form a phone can read.
  const wideEnoughForWeek = useMediaQuery("(min-width: 48em)", false, { getInitialValueInEffect: false });

  useEffect(() => {
    setToday(householdToday(timezone));
    const id = setInterval(() => setToday(householdToday(timezone)), 60_000);
    return () => clearInterval(id);
  }, [timezone]);

  // The anchor follows today until the person navigates away from it.
  const anchorDate = anchor ?? today ?? "";

  const range = useMemo(
    () => (anchorDate ? rangeFor(view === "agenda" || view === "week" ? weekStartOf(anchorDate, view) : anchorDate, view) : null),
    [anchorDate, view],
  );

  // Reading is gated on `ready`, never on `today` alone: `today` comes from the
  // browser's clock and is set on mount, so it proves nothing about the session.
  const ready = calendarReadsReady(householdReady);
  const calendarsQuery = useCalendars(ready);
  const eventsQuery = useCalendarEvents(ready ? range?.start ?? null : null, ready ? range?.end ?? null : null);
  const billsQuery = useBills(ready);
  const peopleQuery = usePeople(ready);
  const activityQuery = useCalendarActivity(ready);

  const createEvent = useCreateEvent();
  const updateEvent = useUpdateEvent();
  const deleteEvent = useDeleteEvent();

  const calendars = useMemo(() => calendarsQuery.data?.data ?? [], [calendarsQuery.data]);
  const visibleCalendars = useMemo(
    () => calendars.filter((c) => !hidden[c.id]),
    [calendars, hidden],
  );
  const hiddenCount = calendars.filter((c) => hidden[c.id]).length;

  // The API answers one entry per day of the range, each with its weekday, so a
  // quiet day can be shown and a grid's columns named. An older API may not send
  // it; then the events' own local_date/weekday still group the agenda, and the
  // grid says why it can't draw its headers (it never invents a weekday).
  const dayTable = useMemo<DayInfo[]>(() => eventsQuery.data?.days ?? [], [eventsQuery.data]);
  const noDayTable = !eventsQuery.isLoading && missingDayTable(dayTable);
  const rawDays = useMemo<DayInfo[]>(() => {
    if (dayTable.length > 0) return dayTable;
    return daysFromEvents(eventsQuery.data?.data ?? []);
  }, [dayTable, eventsQuery.data]);

  const visibleIds = useMemo(() => new Set(visibleCalendars.map((c) => c.id)), [visibleCalendars]);
  const visibleEvents = useMemo(
    () => (eventsQuery.data?.data ?? []).filter((e) => visibleIds.has(e.calendar_id)),
    [eventsQuery.data, visibleIds],
  );
  const series = useMemo(() => groupSeries(visibleEvents), [visibleEvents]);
  const days = useMemo(() => {
    if (rawDays.length === 0) return [];
    if (view === "month") {
      // The API is asked for the month plus a week either side; the grid is the
      // six weeks that cover the month.
      return monthGrid(monthStart(anchorDate), rawDays);
    }
    if (view === "day") return rawDays.filter((d) => d.local_date === anchorDate).slice(0, 1);
    return rawDays.slice(0, 7);
  }, [rawDays, view, anchorDate]);

  const agenda: AgendaDay[] = useMemo(
    () => (today ? buildAgenda(visibleEvents, days, today) : []),
    [visibleEvents, days, today],
  );

  const bills = useMemo(() => billsQuery.data?.data ?? [], [billsQuery.data]);
  const billByEventId = useMemo(() => {
    const map = new Map<string, BillReading>();
    for (const ev of visibleEvents) {
      const cal = calendars.find((c) => c.id === ev.calendar_id);
      map.set(`${ev.id}@${ev.local_date ?? ""}`, billReading(ev, bills, today ?? "", { onBillsCalendar: cal?.type === "bills" }));
    }
    return map;
  }, [visibleEvents, bills, calendars, today]);

  const billFor = useCallback(
    (ev: CalEvent) => billByEventId.get(`${ev.id}@${ev.local_date ?? ""}`),
    [billByEventId],
  );

  const attribution = useMemo(
    () => attributionMap(activityQuery.data?.data ?? [], null),
    [activityQuery.data],
  );

  // ── navigation ────────────────────────────────────────────
  const shift = (direction: -1 | 1) => {
    if (!today) return;
    const base = anchorDate || today;
    if (view === "day") return setAnchor(addDays(base, direction));
    if (view === "month") return setAnchor(addMonths(monthStart(base), direction));
    setAnchor(addDays(base, 7 * direction));
  };
  const goToday = () => {
    setAnchor(null);
    setView((v) => v);
  };

  // ── writes ────────────────────────────────────────────────
  const handleSave = (values: EventFormValues, target: { calendarId: string; eventId: string | null }) => {
    setSaveError(null);
    const body = eventBody(values, timezone);
    const done = () => {
      setEditing(null);
      setSheet(null);
      notifications.show({ message: target.eventId ? "Event saved. Everyone in the family sees it." : "Event added. Everyone in the family sees it.", color: "cyan" });
    };
    const failed = (err: unknown) => setSaveError(err instanceof Error ? err.message : "That didn't save. Try again.");
    if (target.eventId) {
      // The series' own id: an occurrence and its siblings share it, and the
      // API's PATCH takes that id.
      updateEvent.mutate({ calendarId: target.calendarId, eventId: target.eventId, body }, { onSuccess: done, onError: failed });
      return;
    }
    // A new event belongs to the calendar in the form; event_type is a kind the
    // API accepts, not the calendar's type.
    createEvent.mutate(
      { calendarId: target.calendarId, body: { ...body, event_type: values.eventType || "family" } },
      { onSuccess: done, onError: failed },
    );
  };

  const handleDelete = () => {
    const target = sheet?.series.occurrences[0] ?? editing?.occurrence;
    if (!target) return;
    deleteEvent.mutate(
      { calendarId: target.calendar_id, eventId: seriesId(target) },
      {
        onSuccess: () => {
          setSheet(null);
          setEditing(null);
          notifications.show({ message: sheet?.series.repeating ? "Deleted every date in the series." : "Event deleted.", color: "cyan" });
        },
        onError: (err) => {
          const message = err instanceof Error ? err.message : "That didn't delete. Try again.";
          setSaveError(message);
          notifications.show({ message, color: "red" });
        },
      },
    );
  };

  // ── states ────────────────────────────────────────────────
  if (!today) {
    return (
      <div className="mx-auto w-full max-w-[1120px] px-4 pb-8 pt-4 md:px-6 md:pt-6" id="calendar-page">
        <CalendarShellSkeleton />
      </div>
    );
  }

  const rangeLabel = view === "week" ? weekRangeLabel(days) : days.length > 0 ? rangeTitle(days, view === "agenda" ? "week" : view) : "";
  // A disabled query is PENDING but not loading, so `!ready` has to count as
  // loading too: otherwise the gate would paint the empty state ("nothing this
  // week") for the instant before the household is known, which is the one
  // thing this page must never do (audit #74).
  const loading = !ready || eventsQuery.isLoading || calendarsQuery.isLoading;
  const error = (eventsQuery.error ?? calendarsQuery.error) as Error | null;
  const empty = !loading && !error && series.length === 0;
  const reason = empty
    ? emptyReason({
        hiddenCount,
        calendarCount: calendars.length,
        hasAnyEvents: (eventsQuery.data?.data ?? []).length > 0,
        rangeLabel: rangeLabel || "this week",
        canWrite,
      })
    : null;

  return (
    <div className="mx-auto flex w-full max-w-[1120px] flex-col gap-4 px-4 pb-8 pt-4 md:px-6 md:pt-6" id="calendar-page">
      {/* ── toolbar ──────────────────────────────────────────── */}
      <div className="flex flex-col gap-2.5 md:flex-row md:items-center md:gap-3">
        <div className="flex min-w-0 items-center gap-1">
          <button
            type="button"
            onClick={() => shift(-1)}
            className="grid h-11 w-11 flex-none place-items-center rounded-token-md text-ink-2 hover:bg-surface-2"
            aria-label={view === "day" ? "Previous day" : view === "month" ? "Previous month" : "Previous week"}
            data-testid="calendar-prev"
          >
            <IconChevronLeft size={20} stroke={ICON_STROKE} />
          </button>
          <button
            type="button"
            onClick={() => shift(1)}
            className="grid h-11 w-11 flex-none place-items-center rounded-token-md text-ink-2 hover:bg-surface-2"
            aria-label={view === "day" ? "Next day" : view === "month" ? "Next month" : "Next week"}
            data-testid="calendar-next"
          >
            <IconChevronRight size={20} stroke={ICON_STROKE} />
          </button>
          <div className="min-w-0 flex-1 px-1">
            <h2 className="truncate text-[18px] font-semibold leading-tight tracking-tight text-ink md:text-[20px]" data-testid="calendar-range">
              {loading ? <Skeleton height={20} width={150} /> : rangeLabel || "Calendar"}
            </h2>
          </div>
          <button
            type="button"
            onClick={goToday}
            className="h-9 flex-none rounded-token-sm border border-line bg-surface px-3 text-[13px] font-semibold text-ink hover:bg-surface-2"
            data-testid="calendar-today"
          >
            Today
          </button>
        </div>

        <div className="flex items-center gap-2 md:ml-auto">
          <SegmentedControl
            value={view}
            onChange={(value) => {
              setView(value as CalendarView);
              // Switching to the week view from the agenda should land on the
              // week that CONTAINS the anchor: the anchor is the day.
            }}
            data={wideEnoughForWeek ? VIEW_OPTIONS : VIEW_OPTIONS.filter((o) => o.value !== "week")}
            size="xs"
            radius="md"
            aria-label="Calendar view"
            data-testid="calendar-view"
          />
          {calendars.length > 1 && (
            <CalendarFilter
              calendars={calendars}
              hidden={hidden}
              onToggle={(id) => setHidden((prev) => ({ ...prev, [id]: !prev[id] }))}
              onShowAll={() => setHidden({})}
            />
          )}
          {canWrite && (
            <button
              type="button"
              onClick={() => setEditing({ series: null, occurrence: null })}
              className="hidden h-9 items-center gap-1.5 rounded-token-sm bg-accent-fill px-3.5 text-[13px] font-semibold text-white md:inline-flex"
              data-testid="calendar-new"
            >
              <IconPlus size={16} stroke={ICON_STROKE} aria-hidden />
              New event
            </button>
          )}
        </div>
      </div>

      {/* A filter that hides calendars says so: an empty week must never be a
          filter the person forgot. */}
      {hiddenCount > 0 && (
        <div className="flex items-center gap-2 rounded-token-sm bg-surface-2 py-1 pl-3 pr-1 text-[13px] text-ink-2" data-testid="calendar-filter-note">
          <IconFilter size={16} stroke={ICON_STROKE} className="flex-none text-ink-3" aria-hidden />
          <span className="flex-1">
            {calendars.filter((c) => hidden[c.id]).map((c) => c.name).join(" and ")} {hiddenCount === 1 ? "is" : "are"} hidden
          </span>
          <button type="button" onClick={() => setHidden({})} className="min-h-9 rounded-token-sm px-2 text-[13px] font-semibold text-accent">
            Show all
          </button>
        </div>
      )}

      {/* ── content ──────────────────────────────────────────── */}
      {error ? (
        <CalendarError
          what="Couldn't load the calendar"
          onRetry={() => {
            eventsQuery.refetch();
            calendarsQuery.refetch();
          }}
        />
      ) : loading ? (
        <CalendarSkeleton rows={view === "agenda" ? 4 : 3} />
      ) : noDayTable && view !== "agenda" ? (
        <CalendarEmpty
          title="This view needs the calendar's day list"
          body="Home OS answered with your events but not the list of days, so the grid's column headers can't be drawn without guessing a weekday. The agenda shows everything in the meantime."
          action={null}
          onNew={() => setEditing({ series: null, occurrence: null })}
          onShowAll={() => setHidden({})}
        />
      ) : empty && reason ? (
        <CalendarEmpty
          title={reason.title}
          body={reason.body}
          action={reason.action}
          onNew={() => setEditing({ series: null, occurrence: null })}
          onShowAll={() => setHidden({})}
        />
      ) : (
        <div className="md:grid md:grid-cols-[minmax(0,1fr)]">
          {view === "agenda" && (
            <AgendaView
              days={days}
              agenda={agenda}
              series={series}
              calendars={calendars}
              bills={bills}
              people={peopleQuery.data?.data ?? []}
              today={today}
              billFor={billFor}
              onOpen={(s, o) => setSheet({ series: s, occurrence: o })}
              onJumpToDate={(d) => {
                setAnchor(d);
                setView("day");
              }}
            />
          )}
          {view === "day" && (
            <DayGridView
              days={days}
              agenda={agenda}
              series={series}
              calendars={calendars}
              bills={bills}
              people={peopleQuery.data?.data ?? []}
              today={today}
              billFor={billFor}
              onOpen={(s, o) => setSheet({ series: s, occurrence: o })}
              onJumpToDate={(d) => setAnchor(d)}
            />
          )}
          {view === "week" && (
            <WeekGridView
              days={days}
              agenda={agenda}
              series={series}
              calendars={calendars}
              bills={bills}
              people={peopleQuery.data?.data ?? []}
              today={today}
              billFor={billFor}
              onOpen={(s, o) => setSheet({ series: s, occurrence: o })}
              onJumpToDate={(d) => setAnchor(d)}
            />
          )}
          {view === "month" && (
            <MonthView
              days={days}
              agenda={agenda}
              series={series}
              calendars={calendars}
              bills={bills}
              people={peopleQuery.data?.data ?? []}
              today={today}
              billFor={billFor}
              onOpen={(s, o) => setSheet({ series: s, occurrence: o })}
              onJumpToDate={(d) => {
                setAnchor(d);
                setView("day");
              }}
            />
          )}
        </div>
      )}

      {/* New event, as a floating action on a phone (the mockup's FAB). It
          stays away when the empty card is up: that card already carries the
          one action, and two Add buttons on top of each other is exactly the
          kind of duplication a careful screen avoids. */}
      {/* data-floating-action marks the FAB for the shared shell's bottom
          padding (#5408), so the last card scrolls clear of it instead of
          ending underneath. The geometry is --shell-fab-* in
          DashboardShell.module.css; this row only positions the button. */}
      {canWrite && !(empty && reason) && (
        <div
          data-floating-action=""
          className="pointer-events-none sticky bottom-[calc(72px+env(safe-area-inset-bottom))] z-30 flex h-0 justify-end md:hidden"
        >
          <button
            type="button"
            onClick={() => setEditing({ series: null, occurrence: null })}
            className="pointer-events-auto grid h-14 w-14 -translate-y-full place-items-center rounded-[16px] bg-accent-fill text-white shadow-token-lg"
            aria-label="New event"
            data-testid="calendar-fab"
          >
            <IconPlus size={26} stroke={2} />
          </button>
        </div>
      )}

      {/* ── overlays ─────────────────────────────────────────── */}
      <EventSheet
        opened={!!sheet}
        onClose={() => setSheet(null)}
        series={sheet?.series ?? null}
        occurrence={sheet?.occurrence ?? null}
        calendars={calendars}
        bills={bills}
        people={peopleQuery.data?.data ?? []}
        attribution={sheet ? attribution.get(sheet.occurrence.id) : undefined}
        today={today}
        canWrite={canWrite}
        onEdit={() => {
          if (!sheet) return;
          setEditing({ series: sheet.series, occurrence: sheet.occurrence });
          setSheet(null);
        }}
        onDelete={handleDelete}
        onOpenRecord={(href) => router.push(href)}
      />

      <EventEditor
        opened={!!editing}
        onClose={() => {
          setEditing(null);
          setSaveError(null);
        }}
        series={editing?.series ?? null}
        occurrence={editing?.occurrence ?? null}
        anchorDate={anchorDate || today}
        calendars={calendars}
        timezone={timezone}
        saving={createEvent.isPending || updateEvent.isPending}
        error={saveError}
        onSave={handleSave}
        onDelete={handleDelete}
        canWrite={canWrite}
      />
    </div>
  );
}

// ── small chrome ────────────────────────────────────────────

function CalendarShellSkeleton() {
  return (
    <div className="flex flex-col gap-4" role="status" aria-busy="true">
      <Skeleton height={40} radius="sm" />
      <Skeleton height={220} radius="md" />
      <Skeleton height={120} radius="md" />
    </div>
  );
}

/** Calendars, each with a colour and its kind; a checkbox hides one. */
function CalendarFilter({
  calendars,
  hidden,
  onToggle,
  onShowAll,
}: {
  calendars: readonly CalendarRow[];
  hidden: Record<string, boolean>;
  onToggle: (id: string) => void;
  onShowAll: () => void;
}) {
  const [open, setOpen] = useState(false);
  const hiddenCount = calendars.filter((c) => hidden[c.id]).length;
  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-label="Filter calendars"
        className="relative grid h-9 w-9 place-items-center rounded-token-md border border-line bg-surface text-ink-2 hover:bg-surface-2"
        data-testid="calendar-filter"
      >
        <IconFilter size={18} stroke={ICON_STROKE} />
        {hiddenCount > 0 && <span className="absolute right-1.5 top-1.5 h-2 w-2 rounded-full bg-accent" aria-hidden />}
      </button>
      {open && (
        <>
          <button type="button" className="fixed inset-0 z-30 cursor-default" aria-label="Close filter" onClick={() => setOpen(false)} />
          <div
            className="absolute right-0 z-40 mt-1 w-64 rounded-token-md border border-line bg-raised p-2 shadow-token-lg"
            role="dialog"
            aria-label="Show on calendar"
            data-testid="calendar-filter-panel"
          >
            {calendars.map((c) => (
              <label key={c.id} className="flex min-h-11 cursor-pointer items-center gap-2.5 rounded-token-sm px-2 hover:bg-surface-2">
                <input
                  type="checkbox"
                  checked={!hidden[c.id]}
                  onChange={() => onToggle(c.id)}
                  className="h-5 w-5 flex-none"
                  style={{ accentColor: c.color || "var(--accent)" }}
                />
                <span className="h-3 w-3 flex-none rounded-[4px]" style={{ background: c.color || "var(--accent)" }} aria-hidden />
                <span className="min-w-0 flex-1">
                  <b className="block truncate text-sm font-medium text-ink">{c.name}</b>
                  <span className="block truncate text-[12px] text-ink-3">{calendarKindLabel(c.type)}</span>
                </span>
              </label>
            ))}
            <button
              type="button"
              onClick={onShowAll}
              className="mt-1 flex min-h-10 w-full items-center gap-2 rounded-token-sm px-2 text-[13px] font-semibold text-accent hover:bg-surface-2"
            >
              <IconX size={16} stroke={ICON_STROKE} aria-hidden />
              Show all
            </button>
          </div>
        </>
      )}
    </div>
  );
}

function calendarKindLabel(type: string): string {
  switch (type) {
    case "family":
      return "The family's own events";
    case "bills":
      return "Due dates from Money";
    case "maintenance":
      return "Upkeep from People & House";
    case "vehicles":
      return "Vehicle dates";
    case "properties":
      return "This property";
    default:
      return "Shared calendar";
  }
}

// ── date arithmetic on the DIGITS the API sent ───────────────

/** The week's first day: the anchor itself (the agenda starts on the day you are). */
function weekStartOf(anchor: string, view: CalendarView): string {
  return view === "week" ? anchor : anchor;
}

function addDays(date: string, n: number): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  if (!m) return date;
  const d = new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]) + n));
  return d.toISOString().slice(0, 10);
}

function addMonths(date: string, n: number): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  if (!m) return date;
  const y = Number(m[1]);
  const mo = Number(m[2]) - 1 + n;
  const target = new Date(Date.UTC(y, mo, 1));
  // Clamp the day to the target month's length (Jan 31 + 1 month → Feb 28).
  const lastDay = new Date(Date.UTC(target.getUTCFullYear(), target.getUTCMonth() + 1, 0)).getUTCDate();
  const day = Math.min(Number(m[3]), lastDay);
  return `${target.getUTCFullYear()}-${String(target.getUTCMonth() + 1).padStart(2, "0")}-${String(day).padStart(2, "0")}`;
}
