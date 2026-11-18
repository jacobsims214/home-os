"use client";

/**
 * One Settings section (#4560): /dashboard/settings/household, .../members,
 * .../credentials, .../phone-calendar, .../account.
 *
 * A sub-route rather than tabs or modals on one page, because that is the app's
 * own pattern (artifact #103 says the same): Back works, a section can be
 * linked, and each phone screen is one section instead of a 700 px scroll.
 *
 * The segment is read with useParams rather than a server `params` prop so the
 * page stays a single client component: the section list and the role both come
 * from client queries, and one file serves every section.
 *
 * What this file owns is the ROLE GATE, and it is the one place in the app that
 * fails CLOSED for an unknown role (nav-config and resource-access hide what a
 * role cannot use, but draw everything while the role is still unknown):
 *
 *   - a gated section (Members, Credentials) renders nothing until the role is
 *     known. Rendering it "meanwhile" would mount the credentials vault and fire
 *     GET /secrets/key for someone the API will then refuse — a probe for a role
 *     that has no business probing.
 *   - if the role cannot be determined at all (GET /me/households failed), the
 *     gated section says so instead of guessing. The API would answer 403; the
 *     page says the same thing without the round trip.
 *   - Household and Account have no gate: every role reads its own household and
 *     changes its own password.
 *
 * A segment that is not a section gets a "no such section" panel with a link
 * back, not a redirect: a bookmark to a section that was removed should say what
 * happened, and a redirect that races the navigation it started is worse.
 */

import Link from "next/link";
import { useParams } from "next/navigation";
import { Button, Skeleton, Stack } from "@mantine/core";
import SettingsFrame, { SectionHeader } from "@/components/settings/SettingsFrame";
import {
  SETTINGS_BASE,
  canSeeSettingsSection,
  findSettingsSection,
} from "@/components/settings/settings-sections";
import { useMyHouseholds, useNavRole } from "@/components/shared/use-nav-role";
import HouseholdSection from "@/components/settings/HouseholdSection";
import MembersSection from "@/components/settings/MembersSection";
import CredentialsSection from "@/components/settings/CredentialsSection";
import PhoneCalendarSection from "@/components/settings/PhoneCalendarSection";
import AgentTokensSection from "@/components/settings/AgentTokens";
import AccountSection from "@/components/settings/AccountSection";

const SECTION_BODY = {
  household: HouseholdSection,
  members: MembersSection,
  credentials: CredentialsSection,
  "phone-calendar": PhoneCalendarSection,
  "agent-tokens": AgentTokensSection,
  account: AccountSection,
} as const;

/** The panel a section that cannot be opened shows. Says why, and what next. */
function NoAccess({ title, description, reason }: { title: string; description: string; reason: string }) {
  return (
    <>
      <SectionHeader title={title} description={description} />
      <Stack gap="md" align="flex-start" data-testid="settings-section-forbidden">
        <p style={{ margin: 0, color: "var(--ink-2)", fontSize: 14, lineHeight: 1.5 }}>{reason}</p>
        <Button component={Link} href={SETTINGS_BASE} variant="default">
          Back to Settings
        </Button>
      </Stack>
    </>
  );
}

export default function SettingsSectionPage() {
  const params = useParams<{ section: string }>();
  const role = useNavRole();
  const households = useMyHouseholds();
  const sectionId = typeof params?.section === "string" ? params.section : "";
  const section = findSettingsSection(sectionId);

  if (!section) {
    return (
      <SettingsFrame section={null}>
        <SectionHeader title="Settings" description="That section doesn't exist." />
        <Stack gap="md" align="flex-start" data-testid="settings-section-missing">
          <p style={{ margin: 0, color: "var(--ink-2)", fontSize: 14, lineHeight: 1.5 }}>
            This link names a settings section Home OS doesn&apos;t have. Nothing was changed.
          </p>
          <Button component={Link} href={SETTINGS_BASE} variant="default">
            Back to Settings
          </Button>
        </Stack>
      </SettingsFrame>
    );
  }

  const gated = section.roles != null;
  if (gated && households.isLoading) {
    return (
      <SettingsFrame section={section}>
        <Skeleton height={28} width={180} radius="sm" mb="md" />
        <Skeleton height={120} radius="md" />
      </SettingsFrame>
    );
  }
  if (gated && role == null) {
    const noFamily = (households.data?.data?.length ?? 0) === 0;
    return (
      <SettingsFrame section={section}>
        <NoAccess
          title={section.label}
          description={section.hint}
          reason={
            noFamily
              ? "You're not in a family hub yet, so there is none of this to show. Join one with an invite link first."
              : "Home OS couldn't tell what your role in this family is just now, so this section stays closed. Check your connection and try again."
          }
        />
      </SettingsFrame>
    );
  }
  if (!canSeeSettingsSection(section, role)) {
    return (
      <SettingsFrame section={section}>
        <NoAccess
          title={section.label}
          description={section.hint}
          reason="Your role in this family can't open this section. An owner or a family manager can change your role if you need it."
        />
      </SettingsFrame>
    );
  }

  const Body = SECTION_BODY[section.id];
  return (
    <SettingsFrame section={section}>
      <Body />
    </SettingsFrame>
  );
}
