"use client";

/**
 * The time zone field for Settings -> Household (#4560).
 *
 * A text input plus a PICKER IN A DIALOG, not an inline dropdown:
 *
 * - The list is ~430 IANA names. An inline suggestion list anchored under the
 *   input covers whatever is beneath it, and at 390 px it covered the form's
 *   Save button: a tap meant for Save landed on a zone instead. That is a
 *   control that silently changes the family's time zone when someone meant to
 *   save, so the list cannot be an overlay on the form.
 * - The dialog is the app's own (ResponsiveModal): a bottom sheet below 768 px
 *   and a centred modal above, with its own scrolling body and pinned actions.
 * - Typing stays possible, because the browser's tzdata and the server's are not
 *   the same build: a zone the local list is missing can still be entered, and
 *   validation (timezone.ts, the API's own rules) decides whether it is real.
 *
 * The field itself is a plain input with the value as text, so the form submits
 * exactly what the person sees.
 */

import { useEffect, useMemo, useState } from "react";
import { Button, ScrollArea, Stack, TextInput } from "@mantine/core";
import { IconWorld } from "@tabler/icons-react";
import ResponsiveModal, { ResponsiveModalActions } from "@/components/ui/ResponsiveModal";
import { timezoneOptions } from "./timezone";
import classes from "./settings.module.css";

interface TimezoneFieldProps {
  value: string;
  onChange: (value: string) => void;
  error?: string | null;
}

/** A short "Central Standard Time" line for the row, or "" when unknown. */
function longZoneName(timezone: string): string {
  try {
    return (
      new Intl.DateTimeFormat("en-US", { timeZone: timezone, timeZoneName: "long" })
        .formatToParts(new Date())
        .find((p) => p.type === "timeZoneName")?.value ?? ""
    );
  } catch {
    return "";
  }
}

export default function TimezoneField({ value, onChange, error }: TimezoneFieldProps) {
  const [opened, setOpened] = useState(false);
  const [query, setQuery] = useState("");
  const [zones, setZones] = useState<string[]>([]);

  // Built after mount: the server's tzdata and the phone's differ, so a list
  // computed during render is a hydration mismatch waiting to happen.
  useEffect(() => setZones(timezoneOptions()), []);

  const matches = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return zones;
    // Both "chicago" and "america/chi" find America/Chicago.
    return zones.filter((z) => z.toLowerCase().includes(q));
  }, [zones, query]);

  function pick(zone: string) {
    onChange(zone);
    setOpened(false);
    setQuery("");
  }

  const long = longZoneName(value);

  return (
    <>
      <div>
        <TextInput
          label="Time zone"
          description="Sets when “today”, reminders and bill due dates roll over for the whole family."
          value={value}
          onChange={(event) => onChange(event.currentTarget.value)}
          error={error ?? undefined}
          data-settings-field="household-timezone"
          spellCheck={false}
          autoCapitalize="none"
          autoCorrect="off"
          // 16 px on phones: anything smaller makes iOS zoom the page on focus.
          styles={{ input: { fontSize: 16, fontFamily: "var(--mantine-font-family-monospace)" } }}
        />
        <div className={classes.zoneHintRow}>
          <Button
            variant="subtle"
            size="compact-sm"
            leftSection={<IconWorld size={16} />}
            onClick={() => setOpened(true)}
            data-settings-action="pick-timezone"
          >
            Pick from the list
          </Button>
          {long && <span className={classes.zoneHint}>{long}</span>}
        </div>
      </div>

      <ResponsiveModal
        opened={opened}
        onClose={() => setOpened(false)}
        title="Choose a time zone"
        size="md"
      >
        <Stack gap="md">
          <TextInput
            label="Search"
            placeholder="America/Chicago, Chicago, Eastern…"
            value={query}
            onChange={(event) => setQuery(event.currentTarget.value)}
            autoFocus
            styles={{ input: { fontSize: 16 } }}
          />
          <ScrollArea.Autosize mah={340} type="auto">
            <ul className={classes.zoneList} role="listbox" aria-label="Time zones">
              {matches.map((zone) => (
                <li key={zone}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={zone === value}
                    className={classes.zoneOption}
                    onClick={() => pick(zone)}
                  >
                    <span className={classes.zoneName}>{zone}</span>
                    <span className={classes.zoneLong}>{longZoneName(zone)}</span>
                  </button>
                </li>
              ))}
              {matches.length === 0 && (
                <li className={classes.zoneOption}>No zone matches that. Type it in the field instead.</li>
              )}
            </ul>
          </ScrollArea.Autosize>
          <ResponsiveModalActions>
            <Button variant="default" onClick={() => setOpened(false)}>
              Cancel
            </Button>
          </ResponsiveModalActions>
        </Stack>
      </ResponsiveModal>
    </>
  );
}
