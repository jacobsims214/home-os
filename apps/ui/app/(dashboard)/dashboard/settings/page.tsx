"use client";

/**
 * Settings (#4560): the section index on a phone, and a redirect on a wide
 * screen.
 *
 * Settings used to be one long page (722 lines) doing calendar sync and the
 * secrets vault at once. It is now a set of section pages — Household, Members,
 * Credentials, Phone calendar, Account — each at /dashboard/settings/<section>,
 * which is how the rest of this app already works (every detail page is its own
 * route, so Back works and a link can be shared). Layout per the approved
 * settings mockup (artifact #103 v1):
 *
 * - Phone (< 768 px): this page is the INDEX. The person's name and family at
 *   the top, then one 64 px row per section with a one-line hint, filtered by
 *   role. Tapping one opens its page, which has a "Back to Settings" bar.
 * - >= 768 px: an index would be a pointless extra tap, so the section list is
 *   shown beside the pane on every section page and this page forwards to the
 *   first section the role may open.
 *
 * The forwarding is a client-side replace after mount, not a server redirect:
 * the role comes from GET /me/households, which only the browser has.
 */

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useCurrentHousehold, useNavRole } from "@/components/shared/use-nav-role";
import SettingsFrame from "@/components/settings/SettingsFrame";
import { SectionRow } from "@/components/settings/SettingsFrame";
import { firstSectionHref, roleLabel, settingsSectionsForRole } from "@/components/settings/settings-sections";
import { useMe } from "@/lib/today/api";
import classes from "@/components/settings/settings.module.css";

/** Matches the shell's phone breakpoint (Mantine sm = 48em). */
const WIDE_QUERY = "(min-width: 48em)";

export default function SettingsIndexPage() {
  const router = useRouter();
  const role = useNavRole();
  const household = useCurrentHousehold();
  const me = useMe();
  const sections = settingsSectionsForRole(role);

  useEffect(() => {
    if (typeof window === "undefined") return;
    if (!window.matchMedia(WIDE_QUERY).matches) return;
    // Wait for the role: forwarding before it lands would always open the
    // first section, which is right today but would silently skip a role's
    // own ordering the moment the table changes.
    if (role == null) return;
    const href = firstSectionHref(role);
    if (href) router.replace(href);
  }, [role, router]);

  return (
    <SettingsFrame section={null}>
      <header className={classes.paneHeader}>
        <div className={classes.indexPerson}>
          <div>
            <p className={classes.indexName} data-settings-readonly="account-name">
              {me.data?.name || "Your account"}
            </p>
            <p className={classes.indexMeta}>
              {me.data?.email ?? ""}
              {household ? ` · ${roleLabel(household.role)} in ${household.name}` : ""}
            </p>
          </div>
        </div>
        <p className={classes.paneDescription}>
          The family, your account, and the things Home OS connects to.
        </p>
      </header>

      <ul className={classes.indexList} data-testid="settings-index">
        {sections.map((section) => (
          <li key={section.id}>
            <SectionRow
              href={section.path}
              icon={section.icon}
              title={section.label}
              hint={section.hint}
              label={`${section.label} settings`}
            />
          </li>
        ))}
      </ul>
    </SettingsFrame>
  );
}
