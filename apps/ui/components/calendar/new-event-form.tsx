"use client";

/**
 * New event, opened from anywhere that is not the Calendar page (#5302).
 *
 * There is ONE event form in the app: `EventEditor` (this file's only child),
 * which already renders through `ResponsiveModal` — a centred dialog from 768 px
 * up, the wave's bottom sheet on a phone, keyboard-safe, closes on Back. This
 * file adds no markup, no fields and no validation. It supplies only what the
 * form cannot fetch for itself: the calendars a person may write in, the
 * household's zone (the caller already has it), and the create mutation — the
 * same `eventBody()` the Calendar page's host uses, so the two cannot drift.
 *
 * WHY IT EXISTS: Today's "Add event" quick action was a LINK to
 * /dashboard/calendar, so a button that promised a form changed the page and
 * showed nothing (war game R2, gap G9: 0 inputs after the tap). The approved
 * Today mockup (artifact #76 v1) draws that chip as a `<button>`, and it is a
 * button in the product now: it opens this form in place.
 *
 * The Calendar page keeps its own host for the same component because it also
 * EDITS and DELETES a series (it owns the sheet/editor state and the PATCH and
 * DELETE mutations); that host is not duplicated here. The form itself — the
 * thing that must never be forked — is shared.
 */
import { useState } from "react";
import { notifications } from "@mantine/notifications";
import EventEditor from "@/components/calendar/event-editor";
import { useCalendars, useCreateEvent } from "@/lib/calendar/api";
import { eventBody, type EventFormValues } from "@/lib/calendar/model";

export interface NewEventFormProps {
  opened: boolean;
  onClose: () => void;
  /** The day a new event starts on: the household's date where the person is. */
  anchorDate: string;
  /** The household's zone, so a typed 4:00 PM stays 4:00 PM across DST. */
  timezone: string | null;
  /** May this role write events? The API enforces it; callers also gate the button. */
  canWrite: boolean;
  /** Called after the API accepts the event, so the caller can refetch its own view. */
  onCreated?: () => void;
}

export default function NewEventForm({ opened, onClose, anchorDate, timezone, canWrite, onCreated }: NewEventFormProps) {
  // Read while the form is open (and cached for 5 minutes by the query), so the
  // calendar select is filled by the time a person looks at it.
  const calendarsQuery = useCalendars(opened && canWrite);
  const createEvent = useCreateEvent();
  const [saveError, setSaveError] = useState<string | null>(null);

  const close = () => {
    setSaveError(null);
    onClose();
  };

  const handleSave = (values: EventFormValues, target: { calendarId: string; eventId: string | null }) => {
    setSaveError(null);
    // Create only. An edit belongs to the page that can PATCH a series, so a
    // target.eventId here would mean the wrong host mounted this form.
    const body = eventBody(values, timezone);
    createEvent.mutate(
      { calendarId: target.calendarId, body: { ...body, event_type: values.eventType || "family" } },
      {
        onSuccess: () => {
          close();
          notifications.show({ message: "Event added. Everyone in the family sees it.", color: "cyan" });
          onCreated?.();
        },
        onError: (err) => setSaveError(err instanceof Error ? err.message : "That didn't save. Try again."),
      },
    );
  };

  // A failed calendars read is NOT "you have no calendars": the form would sit
  // there with an empty, disabled select and look like a household with none.
  const calendarError = calendarsQuery.error
    ? "Your calendars couldn't be loaded just now, so the form can't pick one. Close this and try again."
    : null;

  return (
    <EventEditor
      opened={opened}
      onClose={close}
      series={null}
      occurrence={null}
      anchorDate={anchorDate}
      calendars={calendarsQuery.data?.data ?? []}
      timezone={timezone}
      saving={createEvent.isPending}
      error={saveError ?? calendarError}
      onSave={handleSave}
      // Delete exists only for an event that is already stored; this form makes one.
      onDelete={() => {}}
      canWrite={canWrite}
    />
  );
}
