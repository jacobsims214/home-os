"use client";

/**
 * Settings -> Phone calendar (#4560, rebuilt by #4583).
 *
 * This is the CalDAV section: the app password, the server address and user
 * name a phone's Calendar app asks for, and the setup steps per device. The
 * route has not moved since #4560 — links and the phone's Back path still work.
 *
 * The three things a person needs are on one screen, in the order the phone
 * asks for them: password (shown once; the server keeps only a bcrypt hash),
 * server, user name — then the steps, with the values to paste directly above
 * them.
 *
 * The Server field is NOT a constant here (#5289): it shows the `server_url`
 * the API returns with the app password, because only the API knows the
 * deployment's public origin (its PUBLIC_URL). Until the person generates a
 * password there is no response to read, so the field shows this page's own
 * origin + /dav/ — the same rule the API uses — and a placeholder when even
 * that is unknown (server render). See caldav-server.ts: there is no loopback
 * fallback, because rendering one is the bug #5289 fixed.
 *
 * The steps themselves live in caldav-setup.ts, not here: iPhone (iOS 18's
 * moved Settings path), Android (DAVx5 — Android has no CalDAV client of its
 * own) and Mac, plus the note on which calendars a role ends up seeing
 * (#4807: a housesitter or vendor sees fewer). Keeping them as data is what
 * lets `node --test` cover them, and the repo has no RTL/jsdom.
 */

import { useEffect, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Alert, Button, CopyButton, SegmentedControl, Stack, Text, TextInput } from "@mantine/core";
import { IconAlertTriangle, IconCheck, IconCopy, IconInfoCircle } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { useMe } from "@/lib/today/api";
import { SectionHeader, SettingsCard } from "./SettingsFrame";
import { caldavServerUrl } from "./caldav-server";
import {
  CALDAV_CALENDAR_NOTE,
  CALDAV_DEVICES,
  DEFAULT_CALDAV_DEVICE,
  caldavDevice,
  type CaldavDevice,
  type CaldavDeviceId,
} from "./caldav-setup";

/** What POST /api/v1/auth/caldav-password answers (handler.go caldavPasswordResponse). */
type CaldavPassword = {
  password: string;
  username: string;
  /** Public CalDAV endpoint, built from the API's PUBLIC_URL by the server. */
  server_url?: string;
};

/**
 * A read-only value with its own copy button. The person is holding the phone
 * they are setting up, so every value they have to retype is a value they will
 * mistype: a CalDAV host with one wrong character answers "Cannot Connect" and
 * says nothing about why.
 */
function CopyField({
  label,
  value,
  placeholder,
  copyLabel,
  dataAttr,
}: {
  label: string;
  value: string;
  placeholder?: string;
  copyLabel: string;
  dataAttr: string;
}) {
  return (
    <TextInput
      label={label}
      value={value}
      placeholder={placeholder}
      readOnly
      ff="monospace"
      data-settings-readonly={dataAttr}
      rightSection={
        <CopyButton value={value} timeout={2000}>
          {({ copied, copy }) => (
            <Button
              size="compact-sm"
              variant="subtle"
              onClick={copy}
              disabled={!value}
              aria-label={copied ? "Copied" : copyLabel}
              leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
            >
              {copied ? "Copied" : "Copy"}
            </Button>
          )}
        </CopyButton>
      }
      rightSectionWidth={96}
      rightSectionPointerEvents="all"
    />
  );
}

export default function PhoneCalendarSection() {
  // The user name is the member's email. The password response carries it (the
  // CalDAV Basic auth matches on it), and /auth/me answers it too, so the field
  // is filled before the password exists rather than after.
  const me = useMe();
  const [password, setPassword] = useState<string | null>(null);
  const [deviceId, setDeviceId] = useState<CaldavDeviceId>(DEFAULT_CALDAV_DEVICE);
  const device: CaldavDevice = caldavDevice(deviceId);

  const generate = useMutation({
    mutationFn: () => apiFetch<CaldavPassword>("/api/v1/auth/caldav-password", { method: "POST" }),
    onSuccess: (data) => setPassword(data.password),
  });

  // The server the person types into their phone. The API's answer wins; until
  // there is one, this deployment's own /dav/ (caldav-server.ts, #5289).
  //
  // The origin is read in an effect, not during render: this component is
  // server-rendered too, and reading window there would make the first client
  // render disagree with the HTML (a hydration error over an input's value).
  // One extra render, and the field simply shows its placeholder until then.
  const [origin, setOrigin] = useState<string | undefined>(undefined);
  useEffect(() => setOrigin(window.location.origin), []);
  const server = caldavServerUrl(generate.data?.server_url, origin);
  const username = generate.data?.username ?? me.data?.email ?? "";
  // Only say "couldn't load" when the email is really unavailable: while the
  // password request is in flight the field is about to fill itself in.
  const usernamePlaceholder = username
    ? undefined
    : me.isError && !generate.isPending
      ? "Couldn't load your email — reload the page"
      : "Loading…";

  return (
    <>
      <SectionHeader
        title="Phone calendar"
        description="See the family calendars in your phone's own Calendar app. Works with iPhone, iPad, Mac and any CalDAV client."
      />

      <SettingsCard title="1. App password">
        <Text size="sm" c="var(--ink-2)" mb={12}>
          Your phone uses its own password, not your Home OS one. You can replace it any time without
          changing your real password. Generating it also tells this page the exact server address to
          type into your phone — it is the address for this house, not a generic one.
        </Text>
        <Button onClick={() => generate.mutate()} loading={generate.isPending}>
          {password ? "Generate a new app password" : "Generate app password"}
        </Button>
        {password && (
          <Stack gap={8} mt={16}>
            <TextInput
              label="App password"
              value={password}
              readOnly
              ff="monospace"
              data-settings-readonly="caldav-password"
              rightSection={
                <CopyButton value={password} timeout={2000}>
                  {({ copied, copy }) => (
                    <Button
                      size="compact-sm"
                      variant="subtle"
                      onClick={copy}
                      aria-label={copied ? "Copied" : "Copy the app password"}
                      leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
                    >
                      {copied ? "Copied" : "Copy"}
                    </Button>
                  )}
                </CopyButton>
              }
              rightSectionWidth={96}
              rightSectionPointerEvents="all"
            />
            <Alert
              color="orange"
              variant="light"
              icon={<IconAlertTriangle size={16} />}
              data-testid="caldav-password-warning"
            >
              Copy it now: it is shown once. If you lose it, generate a new one — which also breaks
              the account on any device that already has the old one.
            </Alert>
          </Stack>
        )}
        {generate.isError && (
          <Alert mt={16} color="red" variant="light" icon={<IconAlertTriangle size={16} />}>
            That didn&apos;t work. Nothing was changed — try again.
          </Alert>
        )}
      </SettingsCard>

      <SettingsCard title="2. Add the account on your phone">
        <div style={{ display: "grid", gap: 12 }}>
          <CopyField
            label="Server"
            value={server}
            placeholder="Generate an app password to see it"
            copyLabel="Copy the server address"
            dataAttr="caldav-server"
          />
          <CopyField
            label="User name"
            value={username}
            placeholder={usernamePlaceholder}
            copyLabel="Copy the user name"
            dataAttr="caldav-username"
          />
        </div>

        <SegmentedControl
          mt={20}
          fullWidth
          value={device.id}
          onChange={(value) => setDeviceId(value as CaldavDeviceId)}
          data={CALDAV_DEVICES.map((d) => ({ value: d.id, label: d.label }))}
          aria-label="Which device are you setting up?"
        />

        <div data-testid={`caldav-steps-${device.id}`} style={{ marginTop: 16 }}>
          <Text fw={600} size="sm">
            {device.app}
          </Text>
          <ol style={{ margin: "10px 0 0", paddingLeft: 20, display: "grid", gap: 8 }}>
            {device.steps.map((step) => (
              <Text key={step} component="li" size="sm" c="var(--ink-2)">
                {step}
              </Text>
            ))}
          </ol>
          <Alert mt={12} color="blue" variant="light" icon={<IconInfoCircle size={16} />}>
            {device.note}
          </Alert>
        </div>

        <Text mt={16} size="xs" c="var(--ink-3)" data-testid="caldav-calendar-note">
          {CALDAV_CALENDAR_NOTE}
        </Text>
      </SettingsCard>
    </>
  );
}
