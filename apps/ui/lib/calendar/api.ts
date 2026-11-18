"use client";

/**
 * The calendar page's data layer (#4550). One query per source, each with its
 * own cache key, so one failing read shows one inline error card and leaves the
 * rest of the page up — the same rule as Today (#4549) and audit #74's fix: a
 * failed read must never render as empty data.
 *
 * The events query ALWAYS sends start and end: the API expands a recurring
 * series into occurrences only inside a requested range (KB
 * architecture/api-calendar-recurrence.md, and the manager's note on #4550).
 * Without them a series comes back once, as stored, and the agenda would show
 * one lesson instead of the term's.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { personKeys } from "@/lib/query-keys";
import { useCurrentHousehold, useNavRole } from "@/components/shared/use-nav-role";
import type { ActivityRow, BillRow, CalendarRow, CalEvent, DayInfo, PersonRow } from "./model";

export interface EventListResponse {
  data: CalEvent[];
  /** One entry per day of the requested range, with the API's weekday. */
  days?: DayInfo[];
}

export const calendarKeys = {
  all: ["calendar-page"] as const,
  calendars: () => [...calendarKeys.all, "calendars"] as const,
  events: (start: string, end: string) => [...calendarKeys.all, "events", start, end] as const,
  bills: () => [...calendarKeys.all, "bills"] as const,
  people: () => [...calendarKeys.all, "people"] as const,
  activity: () => [...calendarKeys.all, "activity"] as const,
};

/** The household this page is in (timezone, role), and the viewer's own id. */
export function useCalendarHousehold() {
  return useCurrentHousehold();
}

/** GET /api/v1/calendars — the calendars with their kind and colour. */
export function useCalendars(enabled = true) {
  return useQuery({
    queryKey: calendarKeys.calendars(),
    queryFn: () => apiFetch<{ data: CalendarRow[] }>("/api/v1/calendars"),
    enabled,
    staleTime: 5 * 60_000,
    retry: false,
  });
}

/**
 * GET /api/v1/calendars/events?start&end — the occurrences of the visible
 * range, plus the day table the grid's headers come from.
 */
export function useCalendarEvents(start: string | null, end: string | null) {
  return useQuery({
    queryKey: calendarKeys.events(start ?? "", end ?? ""),
    queryFn: () =>
      apiFetch<EventListResponse>("/api/v1/calendars/events", {
        // The API reads these as RFC 3339; a date at 00:00Z is the convention
        // its range filter expects (all-day events are stored that way).
        params: { start: `${start!}T00:00:00Z`, end: `${end!}T00:00:00Z` },
      }),
    enabled: !!start && !!end,
    staleTime: 60_000,
    retry: false,
  });
}

/**
 * GET /api/v1/bills — bill state. A bill's calendar mirror says when it falls
 * due and nothing about payment, so the paid/due reading comes from here.
 */
export function useBills(enabled = true) {
  return useQuery({
    queryKey: calendarKeys.bills(),
    queryFn: () => apiFetch<{ data: BillRow[] }>("/api/v1/bills"),
    enabled,
    staleTime: 60_000,
    retry: false,
  });
}

/** GET /api/v1/people — names and colours for the "who's involved" chips. */
export function usePeople(enabled = true) {
  return useQuery({
    queryKey: personKeys.list(),
    queryFn: () => apiFetch<{ data: PersonRow[] }>("/api/v1/people"),
    enabled,
    staleTime: 60_000,
    retry: false,
  });
}

/**
 * GET /api/v1/activity?entity_type=calendar_event — who added what.
 *
 * The feed is the only place attribution is recorded (the events table has no
 * author column), so the sheet shows an author only when the feed names one for
 * that event. A role the API refuses (housesitter) never asks: the calendar
 * still works, it just says no author.
 */
export function useCalendarActivity(enabled = true) {
  return useQuery({
    queryKey: calendarKeys.activity(),
    queryFn: () =>
      apiFetch<{ data: ActivityRow[] }>("/api/v1/activity", {
        params: { entity_type: "calendar_event", limit: 100 },
      }),
    enabled,
    staleTime: 30_000,
    retry: false,
  });
}

// ── writes ──────────────────────────────────────────────────

/** The body POST/PATCH take (an Event on the API side; PATCH is partial). */
export interface EventBody {
  title?: string;
  description?: string;
  location?: string;
  color?: string;
  event_type?: string;
  start?: string;
  end?: string;
  all_day?: boolean;
  /** An RRULE value; "" ends a recurrence. */
  rrule?: string;
  exdates?: string[];
  timezone?: string;
  reminders?: { minutes_before: number }[];
}

/** POST /api/v1/calendars/{id}/events. */
export function useCreateEvent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ calendarId, body }: { calendarId: string; body: EventBody }) =>
      apiFetch<{ data: CalEvent }>(`/api/v1/calendars/${calendarId}/events`, { method: "POST", body }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: calendarKeys.all });
      qc.invalidateQueries({ queryKey: ["calendars"] });
    },
  });
}

/**
 * PATCH /api/v1/calendars/{id}/events/{eventId}.
 *
 * The API's PATCH edits THE SERIES: a repeating event's occurrence and its
 * siblings share one stored event, and there is no scope parameter yet
 * (architecture/api-calendar-recurrence.md). The editor says so in words before
 * the person saves, rather than silently changing nine lessons when they meant
 * one — and it does not offer an option that would not work.
 */
export function useUpdateEvent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ calendarId, eventId, body }: { calendarId: string; eventId: string; body: EventBody }) =>
      apiFetch<{ data: CalEvent }>(`/api/v1/calendars/${calendarId}/events/${eventId}`, { method: "PATCH", body }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: calendarKeys.all });
      qc.invalidateQueries({ queryKey: ["calendars"] });
    },
  });
}

/** DELETE /api/v1/calendars/{id}/events/{eventId} — removes the whole series. */
export function useDeleteEvent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ calendarId, eventId }: { calendarId: string; eventId: string }) =>
      apiFetch<void>(`/api/v1/calendars/${calendarId}/events/${eventId}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: calendarKeys.all });
      qc.invalidateQueries({ queryKey: ["calendars"] });
    },
  });
}

/** May the signed-in role write events? Mirrors the API's calendar write grant. */
const WRITER_ROLES = new Set(["owner", "family_manager", "family_member"]);

export function useCalendarAccess() {
  const role = useNavRole();
  return {
    role,
    /** Unknown role shows the controls: the API refuses what it must. */
    canWrite: role == null ? true : WRITER_ROLES.has(role),
  };
}
