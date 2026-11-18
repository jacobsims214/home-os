"use client";

/**
 * Settings -> Phone calendar (#4560).
 *
 * This is the CalDAV section, moved out of the old one-page Settings and left
 * working, NOT rebuilt. #4583 owns the real page (app password shown once, the
 * device-by-device setup steps, which calendars each role sees); it will replace
 * this body inside the same route, so nobody has to move a link or learn a new
 * path.
 *
 * What is here is what the page already did: the server and user name to type
 * into a CalDAV client, POST /api/v1/auth/caldav-password (which stores a bcrypt
 * hash of a fresh app password, #4822's neighbour), the copy button, and the
 * Apple Calendar steps. The app password is shown once — the server keeps only
 * the hash — so the copy-now warning is part of the UI, not decoration.
 *
 * The Server field is NOT a constant here (#5289): it shows the `server_url`
 * the API returns with the app password, because only the API knows the
 * deployment's public origin (its PUBLIC_URL). Until the person generates a
 * password there is no response to read, so the field shows this page's own
 * origin + /dav/ — the same rule the API uses — and a placeholder when even
 * that is unknown (server render). See caldav-server.ts: there is no loopback
 * fallback, because rendering one is the bug this ticket fixes.
 */

import { useEffect, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Alert, Button, CopyButton, Stack, Text, TextInput } from "@mantine/core";
import { IconAlertTriangle, IconCheck, IconCopy } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { useMe } from "@/lib/today/api";
import { SectionHeader, SettingsCard } from "./SettingsFrame";
import { caldavServerUrl } from "./caldav-server";

/** What POST /api/v1/auth/caldav-password answers (handler.go caldavPasswordResponse). */
type CaldavPassword = {
  password: string;
  username: string;
  /** Public CalDAV endpoint, built from the API's PUBLIC_URL by the server. */
  server_url?: string;
};

const STEPS = [
  "Open Settings on your iPhone or Mac.",
  "Go to Calendar → Accounts → Add Account.",
  "Choose Other → Add CalDAV Account.",
  "Enter the server, your email and the app password above.",
  "Leave Calendars on, turn Reminders off, then tap Save.",
];

export default function PhoneCalendarSection() {
  // The CalDAV user name is users.email, from the caller's own profile.
  const me = useMe();
  const [password, setPassword] = useState<string | null>(null);

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
          Generate app password
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
            <Alert color="orange" variant="light" icon={<IconAlertTriangle size={16} />}>
              Copy it now: it is shown once. If you lose it, generate a new one and add the account
              again.
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
        <div style={{ display: "grid", gap: 10 }}>
          <TextInput
            label="Server"
            value={server}
            placeholder="Generate an app password to see it"
            readOnly
            ff="monospace"
            data-settings-readonly="caldav-server"
          />
          <TextInput
            label="User name"
            value={generate.data?.username ?? me.data?.email ?? ""}
            placeholder={me.isError ? "Couldn't load your email" : "Loading…"}
            readOnly
            ff="monospace"
            data-settings-readonly="caldav-username"
          />
        </div>
        <ol style={{ margin: "16px 0 0", paddingLeft: 20, display: "grid", gap: 6 }}>
          {STEPS.map((step) => (
            <Text key={step} component="li" size="sm" c="var(--ink-2)">
              {step}
            </Text>
          ))}
        </ol>
      </SettingsCard>
    </>
  );
}
