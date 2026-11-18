"use client";

/**
 * Create / edit an event (#4550, mockup #87 v1).
 *
 * The form maps 1:1 to what the API takes (POST /calendars/{id}/events and the
 * partial PATCH): title, calendar, all-day switch, start/end date + time,
 * repeat, reminders, location, notes. Nothing is offered that the API would
 * refuse:
 *
 *  - REPEAT is a rule the API accepts (normalizeRRule): DAILY / WEEKLY (with
 *    BYDAY) / MONTHLY (on the day of the month, or the nth weekday) / YEARLY,
 *    ending never, on a date, or after N times.
 *  - REMINDERS are `minutes_before` values 0..40320, at most 10 (the API's cap,
 *    so the Add button stops at 10 instead of earning a 400).
 *  - THE SCOPE OF AN EDIT IS STATED, not offered. A repeating event is one
 *    stored event; its PATCH changes all of them. The banner says so before the
 *    person saves, because "Edit this one" is exactly the option the API cannot
 *    honour yet and a control that lies is worse than no control.
 *
 * Dates and times are typed by a person in the HOUSEHOLD's zone, which the page
 * sends as `timezone`, so a weekly 4:00 PM lesson stays at 4:00 PM across DST.
 * All-day events are sent as dates (midnight UTC), the form the API stores.
 */
import { useEffect, useMemo, useState } from "react";
import { Button, Switch, Textarea, TextInput, Select, NumberInput } from "@mantine/core";
import { IconAlertTriangle, IconPlus, IconTrash } from "@tabler/icons-react";
import ResponsiveModal, { ResponsiveModalActions } from "@/components/ui/ResponsiveModal";
import {
  MAX_REMINDERS,
  REMINDER_CHOICES,
  canWriteIn,
  formatUntil,
  recurrenceFromForm,
  ruleLabel,
  type CalendarRow,
  type CalEvent,
  type EventFormValues,
  type Series,
} from "@/lib/calendar/model";

const ICON_STROKE = 1.75;

export interface EventEditorProps {
  opened: boolean;
  onClose: () => void;
  /** The series being edited, or null when creating. */
  series: Series | null;
  /** The occurrence the person tapped (its date seeds the form). */
  occurrence: CalEvent | null;
  /** The day the person is looking at, for a new event. */
  anchorDate: string;
  calendars: readonly CalendarRow[];
  timezone: string | null;
  saving: boolean;
  error: string | null;
  onSave: (values: EventFormValues, target: { calendarId: string; eventId: string | null }) => void;
  onDelete: () => void;
  canWrite: boolean;
}

/** The form's starting values: the tapped occurrence, or a fresh 9–10 AM slot. */
export function initialValues(series: Series | null, occurrence: CalEvent | null, anchorDate: string): EventFormValues {
  if (series && occurrence) {
    const endDate = occurrence.end_local_date || occurrence.local_date || anchorDate;
    const endTime = occurrence.all_day ? "" : occurrence.end_local_time || occurrence.local_time || "";
    return {
      title: occurrence.title,
      calendarId: occurrence.calendar_id,
      allDay: !!occurrence.all_day,
      startDate: occurrence.local_date || anchorDate,
      startTime: occurrence.all_day ? "09:00" : occurrence.local_time || "09:00",
      endDate,
      endTime: occurrence.all_day ? "10:00" : endTime,
      repeat: occurrence.rrule ?? "",
      reminders: (occurrence.reminders ?? []).map((r) => r.minutes_before),
      location: occurrence.location ?? "",
      notes: occurrence.description ?? "",
      eventType: occurrence.event_type,
    };
  }
  return {
    title: "",
    calendarId: "",
    allDay: false,
    startDate: anchorDate,
    startTime: "09:00",
    endDate: anchorDate,
    endTime: "10:00",
    repeat: "",
    reminders: [15],
    location: "",
    notes: "",
    eventType: "family",
  };
}

export default function EventEditor({
  opened,
  onClose,
  series,
  occurrence,
  anchorDate,
  calendars,
  timezone,
  saving,
  error,
  onSave,
  onDelete,
  canWrite,
}: EventEditorProps) {
  const [v, setV] = useState<EventFormValues>(() => initialValues(series, occurrence, anchorDate));
  const [formError, setFormError] = useState<string | null>(null);

  // Re-seed whenever the sheet is opened for another event (or for a new one).
  useEffect(() => {
    if (!opened) return;
    setV(initialValues(series, occurrence, anchorDate));
    setFormError(null);
  }, [opened, series, occurrence, anchorDate]);

  const writable = useMemo(() => calendars.filter((c) => canWriteIn(c)), [calendars]);
  useEffect(() => {
    // A new event needs a calendar: the first one a person may write in.
    if (opened && !v.calendarId && writable.length > 0) setV((prev) => ({ ...prev, calendarId: writable[0].id }));
  }, [opened, v.calendarId, writable]);

  const set = <K extends keyof EventFormValues>(key: K, value: EventFormValues[K]) =>
    setV((prev) => ({ ...prev, [key]: value }));

  const repeatRule = recurrenceFromForm(v);
  const wasRepeating = !!occurrence?.rrule;
  const ruleChanged = wasRepeating && repeatRule !== (occurrence?.rrule ?? "");

  function submit() {
    setFormError(null);
    if (!v.title.trim()) {
      setFormError("Give the event a name — “Piano lesson”, “Dentist”.");
      return;
    }
    if (!v.calendarId) {
      setFormError("Pick a calendar.");
      return;
    }
    if (!v.allDay && v.endDate === v.startDate && v.endTime <= v.startTime) {
      setFormError("The end time needs to be after the start time.");
      return;
    }
    if (v.endDate < v.startDate) {
      setFormError("The end date can't be before the start date.");
      return;
    }
    onSave({ ...v, repeat: repeatRule }, { calendarId: v.calendarId, eventId: occurrence?.id ?? null });
  }

  return (
    <ResponsiveModal
      opened={opened}
      onClose={onClose}
      title={occurrence ? "Edit event" : "New event"}
      size="lg"
      closeOnBack
    >
      <form
        className="flex flex-col gap-5 pt-1"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
        data-testid="event-editor"
      >
        <TextInput
          label="What's happening?"
          placeholder="Piano lesson"
          value={v.title}
          onChange={(e) => set("title", e.currentTarget.value)}
          data-autofocus
          size="md"
          styles={{ input: { fontSize: 16 } }}
          data-testid="editor-title"
        />

        <Select
          label="Calendar"
          value={v.calendarId || null}
          onChange={(value) => set("calendarId", value ?? "")}
          data={writable.map((c) => ({ value: c.id, label: c.name }))}
          placeholder={writable.length ? "Pick a calendar" : "No calendar you can write in"}
          disabled={writable.length === 0}
          size="md"
          styles={{ input: { fontSize: 16 } }}
          description="Bills and Maintenance are filled by Money and People & House, so their dates are changed there."
          data-testid="editor-calendar"
        />

        {/* A repeating event's edit reaches every date: said before saving. */}
        {wasRepeating && (
          <div className="flex gap-2.5 rounded-token-sm bg-warn-bg p-3 text-[13px] text-warn-ink" data-testid="editor-scope-note">
            <IconAlertTriangle size={18} stroke={ICON_STROKE} className="mt-px flex-none" aria-hidden />
            <span>
              <b className="font-semibold">This event repeats.</b> Saving changes{" "}
              <b className="font-semibold">every date</b> in the series — Home OS can&apos;t change just one yet. To move a single
              lesson, add a one-off event on that date instead.
              {ruleChanged && (
                <>
                  {" "}
                  You&apos;ve changed how it repeats, which always applies to the whole series.
                </>
              )}
            </span>
          </div>
        )}

        <div className="rounded-token-md border border-line bg-surface p-3.5">
          <div className="flex items-center justify-between" data-testid="editor-allday">
            <span className="text-[15px] text-ink">All day</span>
            <Switch checked={v.allDay} onChange={(e) => set("allDay", e.currentTarget.checked)} aria-label="All day" />
          </div>
          <div className={`mt-3 grid grid-cols-[56px_minmax(0,1.3fr)_minmax(0,1fr)] items-center gap-2 ${v.allDay ? "grid-cols-[56px_minmax(0,1fr)]" : ""}`}>
            <span className="text-sm text-ink-2">Starts</span>
            <TextInput
              type="date"
              aria-label="Start date"
              value={v.startDate}
              onChange={(e) => {
                const value = e.currentTarget.value;
                setV((prev) => ({ ...prev, startDate: value, endDate: prev.endDate < value ? value : prev.endDate }));
              }}
              size="md"
              styles={{ input: { fontSize: 16 } }}
              data-testid="editor-start-date"
            />
            {!v.allDay && (
              <TextInput
                type="time"
                aria-label="Start time"
                value={v.startTime}
                onChange={(e) => set("startTime", e.currentTarget.value)}
                size="md"
                styles={{ input: { fontSize: 16 } }}
                data-testid="editor-start-time"
              />
            )}
          </div>
          <div className={`mt-2 grid grid-cols-[56px_minmax(0,1.3fr)_minmax(0,1fr)] items-center gap-2 ${v.allDay ? "grid-cols-[56px_minmax(0,1fr)]" : ""}`}>
            <span className="text-sm text-ink-2">Ends</span>
            <TextInput
              type="date"
              aria-label="End date"
              value={v.endDate}
              onChange={(e) => set("endDate", e.currentTarget.value)}
              size="md"
              styles={{ input: { fontSize: 16 } }}
              data-testid="editor-end-date"
            />
            {!v.allDay && (
              <TextInput
                type="time"
                aria-label="End time"
                value={v.endTime}
                onChange={(e) => set("endTime", e.currentTarget.value)}
                size="md"
                styles={{ input: { fontSize: 16 } }}
                data-testid="editor-end-time"
              />
            )}
          </div>
          {v.allDay && (
            <p className="mt-2 text-[12.5px] text-ink-3">
              All-day events have no clock time: they belong to the date itself, in the household&apos;s timezone.
            </p>
          )}
        </div>

        {/* Repeat */}
        <div className="rounded-token-md border border-line bg-surface p-3.5">
          <Select
            label="Repeat"
            value={v.repeat ? "custom" : "none"}
            onChange={(value) => {
              if (value === "none") return set("repeat", "");
              if (value && value !== "custom") return set("repeat", value);
            }}
            data={[
              { value: "none", label: "Doesn't repeat" },
              { value: "FREQ=DAILY", label: "Daily" },
              { value: "FREQ=WEEKLY", label: "Weekly" },
              { value: "FREQ=MONTHLY", label: "Monthly" },
              { value: "FREQ=YEARLY", label: "Yearly" },
              ...(repeatRule && !SIMPLE_RULES.includes(repeatRule)
                ? [{ value: "custom", label: ruleLabel(repeatRule) || "Custom rule" }]
                : []),
            ]}
            size="md"
            styles={{ input: { fontSize: 16 } }}
            data-testid="editor-repeat"
          />
          {repeatRule && (
            <div className="mt-3 flex flex-col gap-2.5">
              {/* The rule in words. Skipped when the select above already shows
                  exactly this sentence (a rule with days or an end date reads as
                  itself there), so the form never says the same thing twice. */}
              {ruleLabel(v.repeat) !== repeatSelectLabel(v.repeat) && (
                <p className="rounded-token-sm bg-accent-soft px-2.5 py-2 text-[13px] text-ink" data-testid="editor-repeat-summary">
                  {ruleLabel(v.repeat) || "Repeats"}
                </p>
              )}
              {v.repeat.startsWith("FREQ=WEEKLY") && <WeeklyDays value={v.repeat} onChange={(r) => set("repeat", r)} />}
              {v.repeat.startsWith("FREQ=MONTHLY") && <MonthlyKind value={v.repeat} onChange={(r) => set("repeat", r)} startDate={v.startDate} />}
              <EndsOn value={v.repeat} onChange={(r) => set("repeat", r)} startDate={v.startDate} />
            </div>
          )}
        </div>

        {/* Reminders */}
        <div className="rounded-token-md border border-line bg-surface p-3.5">
          <span className="mb-1.5 block text-[13px] font-semibold text-ink-2">Reminders</span>
          <div className="flex flex-col gap-2">
            {v.reminders.map((minutes, i) => (
              <div key={`${minutes}-${i}`} className="flex items-center gap-2">
                <Select
                  aria-label={`Reminder ${i + 1}`}
                  value={String(minutes)}
                  onChange={(value) => {
                    // A dismissed combobox answers null; that is not "At the
                    // start", it is "no change".
                    if (value === null) return;
                    const next = [...v.reminders];
                    next[i] = Number(value);
                    set("reminders", next);
                  }}
                  data={REMINDER_CHOICES.map((c) => ({ value: String(c.minutes), label: c.label }))}
                  className="flex-1"
                  size="md"
                  styles={{ input: { fontSize: 16 } }}
                  data-testid={`editor-reminder-${i}`}
                />
                <Button
                  variant="subtle"
                  color="gray"
                  size="sm"
                  aria-label="Remove reminder"
                  onClick={() => set("reminders", v.reminders.filter((_, j) => j !== i))}
                >
                  <IconTrash size={16} stroke={ICON_STROKE} />
                </Button>
              </div>
            ))}
          </div>
          {v.reminders.length < MAX_REMINDERS ? (
            <button
              type="button"
              onClick={() => set("reminders", [...v.reminders, 1440])}
              className="mt-2 inline-flex min-h-10 items-center gap-1.5 text-sm font-semibold text-accent"
              data-testid="editor-add-reminder"
            >
              <IconPlus size={16} stroke={ICON_STROKE} aria-hidden />
              Add reminder
            </button>
          ) : (
            <p className="mt-2 text-[12.5px] text-ink-3">{MAX_REMINDERS} is the most one event can have.</p>
          )}
          <p className="mt-1 text-[12.5px] text-ink-3">
            Phones that sync with Home OS alert at these times, even when Home OS isn&apos;t open.
          </p>
        </div>

        <TextInput
          label="Location"
          placeholder="Add a place or address"
          value={v.location}
          onChange={(e) => set("location", e.currentTarget.value)}
          size="md"
          styles={{ input: { fontSize: 16 } }}
        />
        <Textarea
          label="Notes"
          placeholder="Anything people should know"
          value={v.notes}
          onChange={(e) => set("notes", e.currentTarget.value)}
          minRows={2}
          size="md"
          styles={{ input: { fontSize: 16 } }}
        />

        {timezone && (
          <p className="text-[12.5px] text-ink-3" data-testid="editor-timezone">
            Times are in the household&apos;s timezone ({timezone.replace(/_/g, " ")}).
          </p>
        )}

        {(formError || error) && (
          <p className="rounded-token-sm bg-[var(--warn-bg)] px-3 py-2 text-[13px] text-[var(--warn-ink)]" role="alert" data-testid="editor-error">
            {formError || error}
          </p>
        )}

        {occurrence && canWrite && (
          <Button
            variant="subtle"
            color="red"
            size="sm"
            justify="flex-start"
            leftSection={<IconTrash size={16} stroke={ICON_STROKE} />}
            onClick={onDelete}
            data-testid="editor-delete"
          >
            Delete {wasRepeating ? "all dates" : "event"}
          </Button>
        )}

        <ResponsiveModalActions>
          <Button variant="default" size="sm" onClick={onClose} type="button">
            Cancel
          </Button>
          <Button size="sm" type="submit" loading={saving} disabled={!canWrite} data-testid="editor-save">
            {wasRepeating ? "Save all dates" : "Save"}
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

// ── repeat sub-forms ────────────────────────────────────────

/** The four rules the Repeat select offers as one-tap choices. */
const SIMPLE_RULES = ["FREQ=DAILY", "FREQ=WEEKLY", "FREQ=MONTHLY", "FREQ=YEARLY"];

/** What the Repeat select shows for a rule: its plain name, or the whole sentence. */
function repeatSelectLabel(rule: string): string {
  const plain: Record<string, string> = {
    "FREQ=DAILY": "Daily",
    "FREQ=WEEKLY": "Weekly",
    "FREQ=MONTHLY": "Monthly",
    "FREQ=YEARLY": "Yearly",
  };
  return plain[rule] ?? ruleLabel(rule);
}

const DAYS = [
  { code: "MO", label: "M", long: "Monday" },
  { code: "TU", label: "T", long: "Tuesday" },
  { code: "WE", label: "W", long: "Wednesday" },
  { code: "TH", label: "T", long: "Thursday" },
  { code: "FR", label: "F", long: "Friday" },
  { code: "SA", label: "S", long: "Saturday" },
  { code: "SU", label: "S", long: "Sunday" },
];

/** The set BYDAY codes in an RRULE. */
function byDay(rule: string): string[] {
  const m = /BYDAY=([A-Z,+-]+)/.exec(rule);
  return m ? m[1].split(",").filter(Boolean) : [];
}

/** Replaces BYDAY (keeping everything else) in a rule. */
function withByDay(rule: string, days: string[]): string {
  const parts = rule.split(";").filter((p) => !p.startsWith("BYDAY="));
  if (days.length) parts.push(`BYDAY=${days.join(",")}`);
  return parts.join(";");
}

function WeeklyDays({ value, onChange }: { value: string; onChange: (rule: string) => void }) {
  const selected = byDay(value);
  return (
    <div>
      <span className="mb-1.5 block text-[13px] font-semibold text-ink-2" id="weekly-days-label">
        On
      </span>
      <div className="flex gap-1.5" role="group" aria-labelledby="weekly-days-label">
        {DAYS.map((d) => {
          const on = selected.includes(d.code);
          return (
            <button
              key={d.code}
              type="button"
              aria-label={d.long}
              aria-pressed={on}
              onClick={() => {
                const next = on ? selected.filter((c) => c !== d.code) : [...selected, d.code];
                // Keep the RFC order so the rule stays stable as it is edited.
                next.sort((a, b) => DAYS.findIndex((x) => x.code === a) - DAYS.findIndex((x) => x.code === b));
                onChange(withByDay(value, next));
              }}
              className={[
                "grid h-10 w-10 flex-none place-items-center rounded-full border text-[13px] font-semibold",
                on ? "border-accent-fill bg-accent-fill text-white" : "border-line bg-surface text-ink-2",
              ].join(" ")}
              data-testid={`editor-day-${d.code}`}
            >
              {d.label}
            </button>
          );
        })}
      </div>
      {selected.length === 0 && <p className="mt-1 text-[12.5px] text-warn-ink">Pick at least one day, or this repeats daily.</p>}
    </div>
  );
}

/** Ordinals and weekdays the RRULE accepts, offered as choices — the person says which. */
const ORDINALS = [
  { value: "1", label: "first" },
  { value: "2", label: "second" },
  { value: "3", label: "third" },
  { value: "4", label: "fourth" },
  { value: "-1", label: "last" },
];

/**
 * "Day 18 of the month" or "The third Tuesday". Both are explicit choices: the
 * page never works out which weekday a date is, because that answer comes from
 * the API everywhere else and a second, quieter implementation here is exactly
 * how the two drift apart.
 */
function MonthlyKind({ value, onChange, startDate }: { value: string; onChange: (rule: string) => void; startDate: string }) {
  const ordinal = /BYDAY=([+-]?\d)([A-Z]{2})/.exec(value);
  const dayOfMonth = Number(startDate.slice(8, 10));
  const setNth = (n: string, weekday: string) => {
    const parts = value.split(";").filter((p) => !p.startsWith("BYDAY=") && !p.startsWith("BYMONTHDAY="));
    parts.push(`BYDAY=${n}${weekday}`);
    onChange(parts.join(";"));
  };
  return (
    <div className="flex flex-col gap-2.5">
      <div role="radiogroup" aria-label="Which day of the month" className="flex flex-col gap-1">
        <label className="flex min-h-10 items-center gap-2.5 text-sm text-ink">
          <input
            type="radio"
            name="monthly-kind"
            checked={!ordinal}
            onChange={() => {
              const parts = value.split(";").filter((p) => !p.startsWith("BYDAY=") && !p.startsWith("BYMONTHDAY="));
              parts.push(`BYMONTHDAY=${dayOfMonth}`);
              onChange(parts.join(";"));
            }}
            data-testid="editor-monthly-day"
          />
          Day {dayOfMonth} of the month
        </label>
        <label className="flex min-h-10 items-center gap-2.5 text-sm text-ink">
          <input
            type="radio"
            name="monthly-kind"
            checked={!!ordinal}
            onChange={() => setNth(ordinal?.[1] ?? "1", ordinal?.[2] ?? "MO")}
            data-testid="editor-monthly-weekday"
          />
          The
          <Select
            aria-label="Which week"
            value={ordinal?.[1] ?? "1"}
            onChange={(n) => setNth(n ?? "1", ordinal?.[2] ?? "MO")}
            data={ORDINALS}
            w={110}
            size="sm"
            styles={{ input: { fontSize: 16 } }}
            disabled={!ordinal}
          />
          <Select
            aria-label="Which weekday"
            value={ordinal?.[2] ?? "MO"}
            onChange={(w) => setNth(ordinal?.[1] ?? "1", w ?? "MO")}
            data={DAYS.map((d) => ({ value: d.code, label: d.long }))}
            w={140}
            size="sm"
            styles={{ input: { fontSize: 16 } }}
            disabled={!ordinal}
          />
        </label>
      </div>
    </div>
  );
}

function EndsOn({ value, onChange, startDate }: { value: string; onChange: (rule: string) => void; startDate: string }) {
  const until = /UNTIL=(\d{8})/.exec(value);
  const count = /COUNT=(\d+)/.exec(value);
  const kind = until ? "until" : count ? "count" : "never";
  const strip = (rule: string) => rule.split(";").filter((p) => !p.startsWith("UNTIL=") && !p.startsWith("COUNT=")).join(";");
  return (
    <div className="flex flex-col gap-1" role="radiogroup" aria-label="Ends">
      <span className="mb-0.5 text-[13px] font-semibold text-ink-2">Ends</span>
      <label className="flex min-h-10 items-center gap-2.5 text-sm text-ink">
        <input type="radio" name="ends" checked={kind === "never"} onChange={() => onChange(strip(value))} />
        Never
      </label>
      <label className="flex min-h-10 items-center gap-2.5 text-sm text-ink">
        <input
          type="radio"
          name="ends"
          checked={kind === "until"}
          onChange={() => onChange(`${strip(value)};UNTIL=${(until?.[1] ?? startDate.replace(/-/g, "") )}`)}
        />
        On
        <TextInput
          type="date"
          aria-label="Last date"
          value={until ? `${until[1].slice(0, 4)}-${until[1].slice(4, 6)}-${until[1].slice(6, 8)}` : startDate}
          onChange={(e) => onChange(`${strip(value)};UNTIL=${e.currentTarget.value.replace(/-/g, "")}`)}
          size="sm"
          styles={{ input: { fontSize: 16 } }}
          data-testid="editor-until"
        />
      </label>
      <label className="flex min-h-10 items-center gap-2.5 text-sm text-ink">
        <input
          type="radio"
          name="ends"
          checked={kind === "count"}
          onChange={() => onChange(`${strip(value)};COUNT=${count?.[1] ?? "10"}`)}
        />
        After
        <NumberInput
          aria-label="Number of times"
          value={count ? Number(count[1]) : 10}
          onChange={(n) => onChange(`${strip(value)};COUNT=${Math.max(1, Number(n) || 1)}`)}
          min={1}
          max={1000}
          w={80}
          size="sm"
          styles={{ input: { fontSize: 16 } }}
        />
        times
      </label>
      {until && <p className="text-[12px] text-ink-3">Last date: {formatUntil(until[1])}</p>}
    </div>
  );
}
