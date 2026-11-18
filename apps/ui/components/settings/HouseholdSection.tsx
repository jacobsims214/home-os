"use client";

/**
 * Settings -> Household (#4560): the family's name, its time zone, and how Home
 * OS looks on this device.
 *
 * Who may change what:
 * - name and time zone: owners and family managers only. PATCH
 *   /api/v1/households/{id} (#4820) grants this to exactly those two roles
 *   (api/household-settings.md, decisions/2026-11-15-role-policy.md): a wrong
 *   time zone changes what every date means for every member and every agent,
 *   so it sits with the roles that already manage members. Everyone else gets
 *   THE SAME VALUES read-only, with a line naming who can change them —
 *   rendering the form disabled would suggest they are one permission away.
 * - theme: everyone. It is a per-device choice (#4529), not family data, and it
 *   is the "colour" part of this section.
 *
 * The time zone is validated in components/settings/timezone.ts with the API's
 * own four rules BEFORE the request, so a bad value is refused inline and no
 * PATCH is sent (a bad zone that reaches the database silently shifts every
 * date the family sees). The API stays the authority: its 400 message is shown
 * verbatim under the field.
 */

import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, TextInput } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconCheck } from "@tabler/icons-react";
import { apiFetch, ApiError } from "@/lib/api";
import ColorSchemeControl from "@/components/ui/ColorSchemeControl";
import { useCurrentHousehold, useNavRole } from "@/components/shared/use-nav-role";
import { SectionHeader, SettingsCard } from "./SettingsFrame";
import TimezoneField from "./TimezoneField";
import { canManageHousehold, roleLabel } from "./settings-sections";
import { validateTimezone } from "./timezone";
import classes from "./settings.module.css";

/** The PATCH body's response: lowercase keys, RFC 3339 times (#4820). */
interface HouseholdResponse {
  data: { id: string; name: string; timezone: string; updated_at: string };
}

/** A rough "Central Time" style name, for the read-only row. */
function zoneHint(timezone: string): string {
  try {
    const long = new Intl.DateTimeFormat("en-US", {
      timeZone: timezone,
      timeZoneName: "long",
    })
      .formatToParts(new Date())
      .find((p) => p.type === "timeZoneName")?.value;
    return long ? `${long} · ${timezone}` : timezone;
  } catch {
    return timezone;
  }
}

export default function HouseholdSection() {
  const household = useCurrentHousehold();
  const role = useNavRole();
  const canManage = canManageHousehold(role);
  const queryClient = useQueryClient();

  const [name, setName] = useState("");
  const [timezone, setTimezone] = useState("");
  const [nameError, setNameError] = useState<string | null>(null);
  const [timezoneErr, setTimezoneErr] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // Seed the form from the household once it loads (and after a save, when the
  // query is refreshed with the API's own answer).
  useEffect(() => {
    if (!household) return;
    setName(household.name);
    setTimezone(household.timezone);
    // Depend on the values, not the object: useCurrentHousehold returns a new
    // object each render.
  }, [household?.id, household?.name, household?.timezone]);

  const mutation = useMutation({
    mutationFn: (body: { name?: string; timezone?: string }) =>
      apiFetch<HouseholdResponse>(`/api/v1/households/${household!.id}`, {
        method: "PATCH",
        body,
      }),
    onSuccess: (res) => {
      // Trust the API's answer over the form: refresh the family list the whole
      // app reads (header chip, sidebar card, this page).
      queryClient.invalidateQueries({ queryKey: ["me", "households"] });
      setName(res.data.name);
      setTimezone(res.data.timezone);
      setSaved(true);
      notifications.show({ message: "Household settings saved.", color: "green" });
    },
    onError: (error: unknown) => {
      const message =
        error instanceof ApiError && error.status === 403
          ? "Only an owner or a family manager can change the household."
          : error instanceof ApiError && error.status === 400
            ? // The API names the bad field ("timezone must be an IANA time
              // zone name…"), so the person can correct it without guessing.
              errorBodyMessage(error) ?? error.message
            : "That didn't save. Nothing was changed — check your connection and try again.";
      setTimezoneErr(message);
      notifications.show({ message, color: "red" });
    },
  });

  function submit(event: React.FormEvent) {
    event.preventDefault();
    setSaved(false);
    const trimmedName = name.trim();
    const nextNameError =
      trimmedName === ""
        ? "Enter a name for your household"
        : trimmedName.length > 255
          ? "Use at most 255 characters"
          : null;
    const tz = validateTimezone(timezone);
    setNameError(nextNameError);
    setTimezoneErr(tz.error);
    if (nextNameError || !tz.ok) return;

    // Only the fields that changed: the API refuses an empty body and treats an
    // absent field as "leave it alone".
    const body: { name?: string; timezone?: string } = {};
    if (trimmedName !== household?.name) body.name = trimmedName;
    if (tz.value !== household?.timezone) body.timezone = tz.value;
    if (Object.keys(body).length === 0) {
      setSaved(true);
      return;
    }
    mutation.mutate(body);
  }

  return (
    <>
      <SectionHeader
        title="Household"
        description="The family's name and time zone, and how Home OS looks on this device."
      />

      {canManage ? (
        <SettingsCard
          title="Family details"
          description="The name everyone sees, and the time zone that decides when reminders, bills and “today” roll over."
        >
          <form onSubmit={submit} noValidate>
            <div className={classes.formRow}>
              <TextInput
                label="Family name"
                value={name}
                onChange={(event) => {
                  setName(event.currentTarget.value);
                  setNameError(null);
                  setSaved(false);
                }}
                error={nameError ?? undefined}
                data-settings-field="household-name"
                autoComplete="organization"
              />
              <TimezoneField
                value={timezone}
                onChange={(value) => {
                  setTimezone(value);
                  setTimezoneErr(null);
                  setSaved(false);
                }}
                error={timezoneErr}
              />
            </div>
            <div className={classes.formActions} style={{ marginTop: 16, alignItems: "center" }}>
              <Button type="submit" loading={mutation.isPending}>
                Save household
              </Button>
              {saved && !mutation.isPending && (
                <span role="status" className={classes.indexHint}>
                  <IconCheck size={16} stroke={2} aria-hidden /> Saved
                </span>
              )}
            </div>
          </form>
        </SettingsCard>
      ) : (
        <SettingsCard
          title="Family details"
          description={`You're a ${roleLabel(role).toLowerCase()} here, so these are read-only. An owner or family manager can change them.`}
        >
          <div className={classes.readOnlyGrid}>
            <div>
              <div className={classes.readOnlyLabel}>Family name</div>
              <div className={classes.readOnlyValue} data-settings-readonly="household-name">
                {household?.name ?? "…"}
              </div>
            </div>
            <div>
              <div className={classes.readOnlyLabel}>Time zone</div>
              <div className={classes.readOnlyValue} data-settings-readonly="household-timezone">
                {household ? zoneHint(household.timezone) : "…"}
              </div>
            </div>
          </div>
          <Alert
            mt={16}
            color="blue"
            variant="light"
            icon={<IconAlertTriangle size={16} />}
            title="Changing the time zone changes every date the family sees"
          >
            Ask an owner or a family manager if it is wrong.
          </Alert>
        </SettingsCard>
      )}

      <SettingsCard
        title="Appearance"
        description="Saved on this device. System follows your phone or computer."
      >
        <ColorSchemeControl />
      </SettingsCard>
    </>
  );
}

/**
 * The API's own message for a 400: apierr wraps it as
 * {"error":{"code","message"}}. Falls back to null so the caller can use its
 * generic copy.
 */
function errorBodyMessage(error: ApiError): string | null {
  const body = error.body as { error?: { message?: unknown } } | undefined;
  const message = body?.error?.message;
  return typeof message === "string" && message.trim() !== "" ? message : null;
}
