"use client";

import { useState, useMemo, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import FullCalendar from "@fullcalendar/react";
import dayGridPlugin from "@fullcalendar/daygrid";
import timeGridPlugin from "@fullcalendar/timegrid";
import { apiFetch } from "@/lib/api";
import Modal from "@/components/ui/Modal";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";
import Select from "@/components/ui/Select";

// ── Types ──────────────────────────────────────────────────────

interface Calendar {
  id: string;
  name: string;
  type: string;
  color: string;
  property_id: string | null;
}

interface CalEvent {
  id: string;
  calendar_id: string;
  event_type: string;
  title: string;
  description: string;
  start: string;
  end: string;
  all_day: boolean;
  location: string;
  color: string;
  entity_type: string;
  entity_id: string;
}

interface Property {
  id: string;
  name: string;
}

// ── Event type config ──────────────────────────────────────────

type EventTypeKey =
  | "family"
  | "appointment"
  | "maintenance"
  | "bill"
  | "vehicle"
  | "pet"
  | "custom";

const EVENT_TYPES: Record<
  EventTypeKey,
  { color: string; icon: React.ReactNode; label: string }
> = {
  family: {
    color: "#8b5cf6",
    icon: (
      <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M15 19.128a9.38 9.38 0 002.625.372 9.337 9.337 0 004.121-.952 4.125 4.125 0 00-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128v.106A12.318 12.318 0 018.624 21c-2.331 0-4.512-.645-6.374-1.766l-.001-.109a6.375 6.375 0 0111.964-3.07M12 6.375a3.375 3.375 0 11-6.75 0 3.375 3.375 0 016.75 0zm8.25 2.25a2.625 2.625 0 11-5.25 0 2.625 2.625 0 015.25 0z" />
      </svg>
    ),
    label: "Family",
  },
  appointment: {
    color: "#3b82f6",
    icon: (
      <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 6a3.75 3.75 0 11-7.5 0 3.75 3.75 0 017.5 0zM4.501 20.118a7.5 7.5 0 0114.998 0A17.933 17.933 0 0112 21.75c-2.676 0-5.216-.584-7.499-1.632z" />
      </svg>
    ),
    label: "Appointment",
  },
  maintenance: {
    color: "#f59e0b",
    icon: (
      <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M11.42 15.17l7.07-7.07a4.95 4.95 0 00-7-7l-7.07 7.07a4.95 4.95 0 007 7zm-2.83-2.83l-1.41-1.41" />
      </svg>
    ),
    label: "Maintenance",
  },
  bill: {
    color: "#f97316",
    icon: (
      <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M12 6v12m-3-2.818l.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
      </svg>
    ),
    label: "Bill",
  },
  vehicle: {
    color: "#6b7280",
    icon: (
      <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M8.25 18.75a1.5 1.5 0 01-3 0m3 0a1.5 1.5 0 00-3 0m3 0h6m-9 0H3.375a1.125 1.125 0 01-1.125-1.125V14.25m17.25 4.5a1.5 1.5 0 01-3 0m3 0a1.5 1.5 0 00-3 0m3 0h1.125c.621 0 1.129-.504 1.09-1.124a17.902 17.902 0 00-3.213-9.193 2.056 2.056 0 00-1.58-.86H14.25M16.5 18.75h-2.25m0-11.177v-.958c0-.568-.422-1.048-.987-1.106a48.554 48.554 0 00-10.026 0 1.106 1.106 0 00-.987 1.106v7.635m12-6.677v6.677m0 4.5v-4.5m0 0h-12" />
      </svg>
    ),
    label: "Vehicle",
  },
  pet: {
    color: "#ec4899",
    icon: (
      <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M15.182 15.182a4.5 4.5 0 01-6.364 0M21 12a9 9 0 11-18 0 9 9 0 0118 0zM9.75 9.75c0 .414-.168.75-.375.75S9 10.164 9 9.75 9.168 9 9.375 9s.375.336.375.75zm-.375 0h.008v.015h-.008V9.75zm5.625 0c0 .414-.168.75-.375.75s-.375-.336-.375-.75.168-.75.375-.75.375.336.375.75zm-.375 0h.008v.015h-.008V9.75z" />
      </svg>
    ),
    label: "Pet",
  },
  custom: {
    color: "#6366f1",
    icon: (
      <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M6.75 3v2.25M17.25 3v2.25M3 18.75V7.5a2.25 2.25 0 012.25-2.25h13.5A2.25 2.25 0 0121 7.5v11.25m-18 0A2.25 2.25 0 005.25 21h13.5A2.25 2.25 0 0021 18.75m-18 0v-7.5A2.25 2.25 0 015.25 9h13.5A2.25 2.25 0 0121 11.25v7.5" />
      </svg>
    ),
    label: "Custom",
  },
};

const TYPE_KEYS: EventTypeKey[] = [
  "family",
  "appointment",
  "maintenance",
  "bill",
  "vehicle",
  "pet",
  "custom",
];

// ── Helpers ─────────────────────────────────────────────────────

/** Format a local date + optional time to an RFC 3339 UTC string. */
function toRFC3339(dateStr: string, timeStr: string): string {
  if (!dateStr) return "";
  const full = timeStr ? `${dateStr}T${timeStr}:00` : `${dateStr}T00:00:00`;
  const d = new Date(full);
  return d.toISOString();
}

/** Extract just the date part (YYYY-MM-DD) from a UTC ISO string. */
function isoToDate(iso: string): string {
  try {
    const d = new Date(iso);
    return d.toISOString().slice(0, 10); // local-naive for input[type=date]
  } catch {
    return "";
  }
}

/** Extract HH:MM from a UTC ISO string. */
function isoToTime(iso: string): string {
  try {
    const d = new Date(iso);
    return `${String(d.getHours()).padStart(2, "0")}:${String(
      d.getMinutes()
    ).padStart(2, "0")}`;
  } catch {
    return "";
  }
}

/** Get a human-friendly name for a calendar given its properties. */
function calendarLabel(cal: Calendar, properties: Property[]): string {
  if (cal.property_id) {
    const prop = properties.find((p) => p.id === cal.property_id);
    return prop ? `${prop.name} — ${cal.name}` : cal.name;
  }
  return cal.name;
}

// ── Page component ─────────────────────────────────────────────

export default function CalendarPage() {
  const queryClient = useQueryClient();

  // Filters
  const [typeFilter, setTypeFilter] = useState<EventTypeKey | null>(null); // null = All
  const [propertyFilter, setPropertyFilter] = useState<string>(""); // "" = All

  // Modals
  const [showAddModal, setShowAddModal] = useState(false);
  const [selectedEvent, setSelectedEvent] = useState<CalEvent | null>(null);

  // Add form state
  const [formTitle, setFormTitle] = useState("");
  const [formType, setFormType] = useState<EventTypeKey>("custom");
  const [formCalendarId, setFormCalendarId] = useState("");
  const [formDate, setFormDate] = useState("");
  const [formStartTime, setFormStartTime] = useState("");
  const [formEndTime, setFormEndTime] = useState("");
  const [formAllDay, setFormAllDay] = useState(false);
  const [formLocation, setFormLocation] = useState("");
  const [formDescription, setFormDescription] = useState("");
  const [formError, setFormError] = useState("");

  // ── Queries ──────────────────────────────────────────────────

  const { data: calendars = [], isLoading: calsLoading } = useQuery({
    queryKey: ["calendars"],
    queryFn: () =>
      apiFetch<{ data: Calendar[] }>("/api/v1/calendars").then((r) => r.data),
    staleTime: 60_000,
  });

  const { data: propertiesData } = useQuery({
    queryKey: ["properties"],
    queryFn: () => apiFetch<{ data: Property[] }>("/api/v1/properties"),
    staleTime: 60_000,
  });
  const properties = propertiesData?.data ?? [];

  const { data: allEvents = [], isLoading: eventsLoading } = useQuery({
    queryKey: ["calendar-events", propertyFilter],
    queryFn: () => {
      const params: Record<string, string | undefined> = {};
      if (propertyFilter) params.property_id = propertyFilter;
      return apiFetch<{ data: CalEvent[] }>("/api/v1/calendars/events", {
        params,
      }).then((r) => r.data);
    },
    staleTime: 30_000,
  });

  // ── Mutations ────────────────────────────────────────────────

  const createMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: CalEvent }>(
        `/api/v1/calendars/${formCalendarId}/events`,
        { method: "POST", body }
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["calendar-events"] });
      setShowAddModal(false);
      resetAddForm();
    },
    onError: (e: unknown) => {
      setFormError(e instanceof Error ? e.message : "Failed to create event");
    },
  });

  const deleteMutation = useMutation({
    mutationFn: ({
      calendarId,
      eventId,
    }: {
      calendarId: string;
      eventId: string;
    }) =>
      apiFetch<void>(`/api/v1/calendars/${calendarId}/events/${eventId}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["calendar-events"] });
      setSelectedEvent(null);
    },
  });

  // ── Refs for default calendar ────────────────────────────────

  // Set default calendar when calendars load
  useEffect(() => {
    if (calendars.length > 0 && !formCalendarId) {
      setFormCalendarId(calendars[0].id);
    }
  }, [calendars, formCalendarId]);

  // ── Filtered events ──────────────────────────────────────────

  const filteredEvents = useMemo(() => {
    if (!typeFilter) return allEvents;
    return allEvents.filter((e) => e.event_type === typeFilter);
  }, [allEvents, typeFilter]);

  // ── FullCalendar events ──────────────────────────────────────

  const fcEvents = useMemo(() => {
    return filteredEvents.map((e) => {
      const et = EVENT_TYPES[e.event_type as EventTypeKey];
      const bgColor =
        e.color || et?.color || EVENT_TYPES.custom.color;
      return {
        id: e.id,
        title: e.title,
        start: e.start,
        end: e.end,
        allDay: e.all_day,
        backgroundColor: bgColor,
        borderColor: bgColor,
        extendedProps: e,
      };
    });
  }, [filteredEvents]);

  // ── Form helpers ─────────────────────────────────────────────

  function resetAddForm() {
    setFormTitle("");
    setFormType("custom");
    setFormDate("");
    setFormStartTime("");
    setFormEndTime("");
    setFormAllDay(false);
    setFormLocation("");
    setFormDescription("");
    setFormError("");
  }

  function openAddModal() {
    resetAddForm();
    setShowAddModal(true);
  }

  function handleAddSubmit(e: React.FormEvent) {
    e.preventDefault();
    setFormError("");

    if (!formTitle.trim()) {
      setFormError("Title is required");
      return;
    }
    if (!formCalendarId) {
      setFormError("Please select a calendar");
      return;
    }
    if (!formDate) {
      setFormError("Date is required");
      return;
    }

    const start = toRFC3339(formDate, formAllDay ? "" : formStartTime || "09:00");
    const end = formAllDay
      ? toRFC3339(formDate, "")
      : toRFC3339(formDate, formEndTime || formStartTime || "10:00");

    createMutation.mutate({
      title: formTitle.trim(),
      description: formDescription.trim() || undefined,
      start,
      end,
      all_day: formAllDay,
      location: formLocation.trim() || undefined,
      event_type: formType,
      color: EVENT_TYPES[formType].color,
    });
  }

  function handleDelete() {
    if (!selectedEvent) return;
    deleteMutation.mutate({
      calendarId: selectedEvent.calendar_id,
      eventId: selectedEvent.id,
    });
  }

  // ── Render ───────────────────────────────────────────────────

  return (
    <div className="flex h-full flex-col">
      {/* ── Filter bar ────────────────────────────────────────── */}
      <div className="flex flex-wrap items-center gap-3 border-b border-gray-200 bg-white px-4 py-3 sm:flex-nowrap">
        {/* Event type chips */}
        <div className="flex flex-wrap items-center gap-1.5">
          <button
            onClick={() => setTypeFilter(null)}
            className={`rounded-full px-3 py-1 text-xs font-medium transition-colors ${
              typeFilter === null
                ? "bg-indigo-600 text-white"
                : "bg-gray-100 text-gray-600 hover:bg-gray-200"
            }`}
          >
            All
          </button>
          {TYPE_KEYS.map((key) => {
            const et = EVENT_TYPES[key];
            return (
              <button
                key={key}
                onClick={() => setTypeFilter(key)}
                className={`inline-flex items-center gap-1 rounded-full px-3 py-1 text-xs font-medium transition-colors ${
                  typeFilter === key
                    ? "text-white"
                    : "bg-gray-100 text-gray-600 hover:bg-gray-200"
                }`}
                style={
                  typeFilter === key
                    ? { backgroundColor: et.color }
                    : undefined
                }
              >
                {et.icon} {et.label}
              </button>
            );
          })}
        </div>

        {/* Property dropdown + Add button */}
        <div className="ml-auto flex items-center gap-3">
          <select
            value={propertyFilter}
            onChange={(e) => setPropertyFilter(e.target.value)}
            className="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-700 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
          >
            <option value="">All Properties</option>
            {properties.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>

          <Button onClick={openAddModal} className="shrink-0">
            + Add Event
          </Button>
        </div>
      </div>

      {/* ── Calendar ──────────────────────────────────────────── */}
      <div className="flex-1 p-4" style={{ minHeight: "600px" }}>
        {eventsLoading ? (
          <div className="flex h-full items-center justify-center">
            <svg
              className="h-6 w-6 animate-spin text-indigo-600"
              fill="none"
              viewBox="0 0 24 24"
            >
              <circle
                className="opacity-25"
                cx="12"
                cy="12"
                r="10"
                stroke="currentColor"
                strokeWidth="4"
              />
              <path
                className="opacity-75"
                fill="currentColor"
                d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
              />
            </svg>
          </div>
        ) : (
          <FullCalendar
            plugins={[dayGridPlugin, timeGridPlugin]}
            initialView="dayGridMonth"
            headerToolbar={{
              left: "prev,next today",
              center: "title",
              right: "dayGridMonth,timeGridWeek,timeGridDay",
            }}
            events={fcEvents}
            eventClick={(info) => {
              setSelectedEvent(info.event.extendedProps as CalEvent);
            }}
            height="auto"
            contentHeight="auto"
            eventDisplay="block"
            dayMaxEvents={4}
            nowIndicator
            eventTimeFormat={{
              hour: "numeric",
              minute: "2-digit",
              meridiem: "short",
            }}
          />
        )}
      </div>

      {/* ── Add Event modal ───────────────────────────────────── */}
      <Modal
        opened={showAddModal}
        onClose={() => {
          setShowAddModal(false);
          resetAddForm();
        }}
        title="Add Event"
        size="lg"
      >
        <form onSubmit={handleAddSubmit} className="space-y-4">
          <Input
            label="Title"
            value={formTitle}
            onChange={(e) => setFormTitle(e.target.value)}
            placeholder="e.g. Dentist appointment"
            required
          />

          <Select
            label="Event Type"
            value={formType}
            onChange={(e) => {
              if (e) setFormType(e as EventTypeKey);
            }}
            data={TYPE_KEYS.map((k) => ({
              value: k,
              label: EVENT_TYPES[k].label,
            }))}
          />

          <Select
            label="Calendar"
            value={formCalendarId}
            onChange={(e) => e && setFormCalendarId(e)}
            placeholder={calsLoading ? "Loading..." : "Select a calendar"}
            data={calendars.map((c) => ({
              value: c.id,
              label: calendarLabel(c, properties),
            }))}
          />

          <Input
            label="Date"
            type="date"
            value={formDate}
            onChange={(e) => setFormDate(e.target.value)}
            required
          />

          {/* All-day toggle */}
          <label className="flex items-center gap-2 text-sm font-medium text-gray-900">
            <input
              type="checkbox"
              checked={formAllDay}
              onChange={(e) => setFormAllDay(e.target.checked)}
              className="h-4 w-4 rounded border-gray-300 text-indigo-600 focus:ring-indigo-600"
            />
            All Day
          </label>

          {!formAllDay && (
            <div className="grid grid-cols-2 gap-3">
              <Input
                label="Start Time"
                type="time"
                value={formStartTime}
                onChange={(e) => setFormStartTime(e.target.value)}
              />
              <Input
                label="End Time"
                type="time"
                value={formEndTime}
                onChange={(e) => setFormEndTime(e.target.value)}
              />
            </div>
          )}

          <Input
            label="Location"
            value={formLocation}
            onChange={(e) => setFormLocation(e.target.value)}
            placeholder="e.g. 123 Main St"
          />

          {/* Description — textarea (not available as a UI component, so use styled textarea) */}
          <div>
            <label
              htmlFor="event-description"
              className="block text-sm font-medium text-gray-900"
            >
              Description
            </label>
            <textarea
              id="event-description"
              value={formDescription}
              onChange={(e) => setFormDescription(e.target.value)}
              rows={3}
              placeholder="Event notes..."
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:outline-none focus:ring-2 focus:ring-inset focus:ring-indigo-600"
            />
          </div>

          {formError && (
            <p className="text-sm text-red-600">{formError}</p>
          )}

          <div className="flex justify-end gap-3 pt-2">
            <button
              type="button"
              onClick={() => {
                setShowAddModal(false);
                resetAddForm();
              }}
              className="inline-flex items-center justify-center rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-semibold text-gray-700 hover:bg-gray-50"
            >
              Cancel
            </button>
            <Button type="submit" loading={createMutation.isPending}>
              Add Event
            </Button>
          </div>
        </form>
      </Modal>

      {/* ── Detail / Delete modal ─────────────────────────────── */}
      {selectedEvent && (
        <Modal
          opened={!!selectedEvent}
          onClose={() => setSelectedEvent(null)}
          title="Event Details"
          size="md"
        >
          <div className="space-y-4">
            {/* Title */}
            <div>
              <p className="text-xs font-medium text-gray-500">Title</p>
              <p className="text-sm font-semibold text-gray-900">
                {selectedEvent.title}
              </p>
            </div>

            {/* Event Type */}
            <div>
              <p className="text-xs font-medium text-gray-500">Event Type</p>
              <p className="text-sm text-gray-700 capitalize">
                {(() => {
                  const et =
                    EVENT_TYPES[selectedEvent.event_type as EventTypeKey];
                  return et ? (
                    <span className="inline-flex items-center gap-1">{et.icon} {et.label}</span>
                  ) : selectedEvent.event_type;
                })()}
              </p>
            </div>

            {/* Date / Time */}
            <div>
              <p className="text-xs font-medium text-gray-500">Date</p>
              <p className="text-sm text-gray-700">
                {new Date(selectedEvent.start).toLocaleDateString("en-US", {
                  weekday: "long",
                  month: "long",
                  day: "numeric",
                  year: "numeric",
                })}
                {!selectedEvent.all_day && (
                  <>
                    {" "}
                    {new Date(selectedEvent.start).toLocaleTimeString(
                      "en-US",
                      { hour: "numeric", minute: "2-digit" }
                    )}
                    {" – "}
                    {new Date(selectedEvent.end).toLocaleTimeString("en-US", {
                      hour: "numeric",
                      minute: "2-digit",
                    })}
                  </>
                )}
              </p>
            </div>

            {/* Location */}
            {selectedEvent.location && (
              <div>
                <p className="text-xs font-medium text-gray-500">Location</p>
                <p className="text-sm text-gray-700">
                  {selectedEvent.location}
                </p>
              </div>
            )}

            {/* Description */}
            {selectedEvent.description && (
              <div>
                <p className="text-xs font-medium text-gray-500">
                  Description
                </p>
                <p className="text-sm text-gray-700 whitespace-pre-wrap">
                  {selectedEvent.description}
                </p>
              </div>
            )}

            {/* Calendar */}
            <div>
              <p className="text-xs font-medium text-gray-500">Calendar</p>
              <p className="text-sm text-gray-700">
                {(() => {
                  const cal = calendars.find(
                    (c) => c.id === selectedEvent.calendar_id
                  );
                  return cal ? calendarLabel(cal, properties) : "Unknown";
                })()}
              </p>
            </div>

            {/* Linked entity */}
            {selectedEvent.entity_type && (
              <div className="rounded-md bg-indigo-50 px-3 py-2 text-xs text-indigo-700">
                Linked to: {selectedEvent.entity_type}
              </div>
            )}

            {/* Delete button */}
            <div className="flex justify-end gap-3 pt-2 border-t border-gray-100">
              <button
                type="button"
                onClick={handleDelete}
                disabled={deleteMutation.isPending}
                className="inline-flex items-center justify-center rounded-md border border-red-300 bg-white px-4 py-2 text-sm font-semibold text-red-600 hover:bg-red-50 disabled:opacity-50"
              >
                {deleteMutation.isPending ? "Deleting..." : "Delete Event"}
              </button>
              <button
                type="button"
                onClick={() => setSelectedEvent(null)}
                className="inline-flex items-center justify-center rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-semibold text-gray-700 hover:bg-gray-50"
              >
                Close
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}
