"use client";

/**
 * Maintenance / upkeep (#4558) — the route entry.
 *
 * The page is the board: a grouped list on phones (Overdue / This week / Next
 * 30 days / Later / No due date, then Done), the kanban from 768px up. Built to
 * the approved people & house mockup (#89 v1), whose Upkeep screen replaced the
 * drag-only, one-column-on-a-phone board.
 *
 * WHO GETS IT (roles.go, ResMaintenance):
 *  - read: family roles, a viewer and a vendor (rolesFabric). A housesitter
 *    has no maintenance grant, so the page says so instead of drawing a list
 *    the API answers 403 for — the same rule the nav follows.
 *  - ActUpdateStatus (status, completed_at): family and vendor. That is the
 *    checkbox, the Start/Skip menu and a drag. A viewer sees every task and
 *    can change none, and the page says who can.
 *  - ActWrite (due_date and the rest): family only, so Snooze is hidden for a
 *    vendor — it PATCHes due_date and would be a 403.
 *
 * Maintenance is not a money surface: no amount is formatted, hidden or
 * redacted here. Costs belong to Money (see the board's own note).
 */

import { Badge, Button, Skeleton, Text, Title } from "@mantine/core";
import { IconLock, IconTool } from "@tabler/icons-react";
import MaintenanceKanban from "@/components/maintenance/MaintenanceKanban";
import { useNavRole } from "@/components/shared/use-nav-role";
import { canSeeEntity, roleKnown } from "@/lib/resource-access";

const ICON_STROKE = 1.75;
const FAMILY = ["owner", "family_manager", "family_member"];

export default function MaintenancePage() {
  const role = useNavRole();

  // The role is still loading: a skeleton, never an owner's controls flashing
  // at a viewer or a housesitter (#4574's rule for every role-gated page).
  if (!roleKnown(role)) {
    return (
      <div className="mx-auto w-full max-w-3xl" role="status" aria-busy="true" aria-label="Loading maintenance tasks">
        <Skeleton height={28} width={160} radius="sm" mb="md" />
        <Skeleton height={220} radius="md" />
      </div>
    );
  }

  // Only housesitters and any unknown role fall out here; the API enforces it.
  if (!canSeeEntity(role, "maintenance")) {
    return (
      <div className="mx-auto w-full max-w-3xl" data-testid="upkeep-no-access">
        <div className="flex items-start gap-3 rounded-token-md border border-line bg-surface p-4">
          <span className="grid h-9 w-9 flex-none place-items-center rounded-[10px] bg-surface-2 text-ink-2" aria-hidden>
            <IconLock size={18} stroke={ICON_STROKE} />
          </span>
          <div className="min-w-0">
            <Title order={3} className="text-lg font-semibold">
              Maintenance isn&apos;t available to you
            </Title>
            <p className="mt-1 max-w-prose text-sm text-ink-2">
              Your role in this family doesn&apos;t include the house&apos;s upkeep tasks. Ask an owner or a family
              manager if you need to see or do them.
            </p>
          </div>
        </div>
      </div>
    );
  }

  const canChangeStatus = FAMILY.includes(role) || role === "vendor";
  const canEdit = FAMILY.includes(role);

  return (
    <div className="mx-auto w-full" data-testid="maintenance-route">
      {!canChangeStatus && (
        <div
          className="mb-4 flex items-start gap-2.5 rounded-token-md border border-line bg-surface-2 px-3.5 py-3 text-sm text-ink-2"
          data-testid="view-only"
        >
          <IconLock size={18} stroke={ICON_STROKE} className="mt-0.5 shrink-0 text-ink-3" aria-hidden />
          <span>
            <b className="font-semibold text-ink">View only.</b> You can see every task and what is due. A family member
            can complete one or change it.
          </span>
        </div>
      )}
      <MaintenanceKanban canChangeStatus={canChangeStatus} canEdit={canEdit} />
    </div>
  );
}
