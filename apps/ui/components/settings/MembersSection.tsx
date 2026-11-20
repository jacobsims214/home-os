"use client";

/**
 * Settings -> Members (#4560): who can sign in to this family.
 *
 * This section is the way in, not a second implementation. The member list,
 * invites and role changes are #4565's page (/dashboard/members): it already
 * reads GET /api/v1/households/me/members and PATCHes roles, and rebuilding it
 * here would give the app two sources of truth about who is in the family — the
 * same reason the shell has one family switcher. So the section states the
 * role's own limits and links to the page. Roles that cannot read the member
 * list at all (vendor) do not see the section (settings-sections.ts).
 */

import { Alert, Badge } from "@mantine/core";
import { IconUsers } from "@tabler/icons-react";
import { useNavRole } from "@/components/shared/use-nav-role";
import { SectionHeader, SectionRow, SettingsCard } from "./SettingsFrame";
import { canManageHousehold, roleLabel } from "./settings-sections";
import classes from "./settings.module.css";

export default function MembersSection() {
  const role = useNavRole();
  const canManage = canManageHousehold(role);

  return (
    <>
      <SectionHeader
        title="Members"
        description="Who can sign in to this family hub and what each person can do. Kids and anyone without a login live in People & House."
      />

      <ul className={classes.indexList}>
        <li>
          <SectionRow
            href="/dashboard/members"
            icon={IconUsers}
            title="Members and invites"
            hint={canManage ? "See everyone, invite someone, change a role" : "See who's in the family"}
          />
        </li>
      </ul>

      <SettingsCard title="Your access here">
        <div className={classes.readOnlyGrid}>
          <div>
            <div className={classes.readOnlyLabel}>Your role</div>
            <div className={classes.readOnlyValue} data-settings-readonly="member-role">
              <Badge color="cyan" variant="light" radius="sm">
                {roleLabel(role)}
              </Badge>
            </div>
          </div>
        </div>
        <Alert mt={16} color="blue" variant="light" icon={<IconUsers size={16} />} title="Who can change roles">
          {canManage
            ? "You can invite people and change their role. Only an owner can make another owner."
            : "Only an owner or a family manager can invite people or change a role."}
        </Alert>
      </SettingsCard>
    </>
  );
}
