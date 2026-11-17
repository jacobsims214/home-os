"use client";

/**
 * Family activity on Today (#4577), built to the approved Today mockup
 * (artifact #76 v1, section "Family activity" with an "All activity" link).
 *
 * ## Why this panel exists
 *
 * The R1 war game's worst attribution failure was an agent telling Jacob
 * "Nothing was deleted that I can see" about a deletion IT had performed: with
 * no change trail, neither a person nor an assistant could answer "what has the
 * other adult done?" or "did anything get deleted?". GET /activity now records
 * the person AND the agent (`actor`, `via_agent`, `agent.name`, and a
 * ready-made `description` such as `Jacob updated note "…" via agent
 * "moltis-wg-jacob"`), so every row here says WHO, HOW (which agent, when one
 * did it) and WHAT changed.
 *
 * ## What it deliberately is not
 *
 * A window, not the feed. Today draws the newest few changes and links to the
 * full history; the war game found that bloated default payloads slow screens
 * down and give a reader more to misread. The data hook asks for 8 rows
 * (ACTIVITY_TODAY_LIMIT) and this section shows 4 of them.
 *
 * ## Role policy (#4689)
 *
 * GET /activity needs ResActivity read: owner, family_manager, family_member
 * and viewer. A housesitter and a vendor answer 403, so the section is not
 * rendered for them at all — `canReadActivity` in lib/resource-access.ts is the
 * mirror of the API's table, and the mount site asks it before mounting this
 * component or firing the query. A housesitter's Today already hides bills and
 * documents for the same reason: a panel that can only fail is not drawn.
 *
 * ## A change whose record is gone
 *
 * The log outlives the rows it describes, so the row resolves what it can open
 * (lib/today/agenda.ts `activityTarget`):
 *
 *   - a deletion says "Deleted — nothing left to open" (the change WAS it);
 *   - a record that 404s now says "No longer there — deleted since", which is
 *     the answer R1 could not get;
 *   - a kind with no page yet (a list, a note, a credential) is text only, not
 *     a link to nowhere;
 *   - a probe that fails for any other reason changes nothing: an unverified
 *     record is never called deleted.
 */

import Link from "next/link";
import { Button, Card, Skeleton } from "@mantine/core";
import {
  IconAlertTriangle,
  IconChevronRight,
  IconHistory,
  IconRefresh,
  IconRobot,
} from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import type { ActivityRow, PersonRow } from "@/lib/today/agenda";
import { activityAvatar, activityTarget, activityWindow, attribution, feedParts } from "@/lib/today/agenda";
import type { useActivityRecordExistence } from "@/lib/today/api";

/** How many changes Today shows before "see all". */
export const ACTIVITY_TODAY_ROWS = 4;

type Existence = ReturnType<typeof useActivityRecordExistence>["exists"];

export interface ActivityFeedProps {
  entries: readonly ActivityRow[];
  /** The viewer's own user id: their own writes read "You". */
  viewerUserId: string | null;
  isLoading: boolean;
  error: Error | null;
  onRetry: () => void;
  /** Record id -> true (exists), false (404: gone), null/absent (unchecked). */
  exists?: Existence;
  /** The family's people, for the actor's avatar colour. */
  people?: readonly PersonRow[];
  /** Where "see all" goes. */
  href?: string;
}

export default function ActivityFeed({
  entries,
  viewerUserId,
  isLoading,
  error,
  onRetry,
  exists,
  people = [],
  href = "/dashboard/activity",
}: ActivityFeedProps) {
  const { rows, hidden } = activityWindow(entries, ACTIVITY_TODAY_ROWS);

  return (
    <section aria-labelledby="h-act" data-testid="today-activity">
      <div className="mb-2 flex items-baseline justify-between gap-3">
        <h3 id="h-act" className="text-base font-semibold tracking-tight text-ink">
          Family activity
        </h3>
        <Link href={href} className="py-2 text-[13px] font-semibold text-accent" data-testid="activity-see-all">
          See all
        </Link>
      </div>

      {error ? (
        <Card
          withBorder
          radius="md"
          padding="sm"
          role="alert"
          data-testid="activity-error"
          className="border-l-4 border-l-[var(--danger)]"
        >
          <div className="flex items-start gap-3">
            <IconAlertTriangle size={22} stroke={NAV_ICON_STROKE} className="mt-0.5 flex-none text-[var(--danger)]" aria-hidden />
            <div className="min-w-0">
              <p className="text-[15px] font-semibold text-ink">The family activity didn&apos;t load</p>
              <p className="mt-0.5 text-[13px] text-ink-2">
                Who changed what is unknown right now, not empty — nothing here is being reported as unchanged. The
                rest of Today is unaffected.
              </p>
              <Button
                size="xs"
                variant="default"
                mt="xs"
                leftSection={<IconRefresh size={16} stroke={NAV_ICON_STROKE} />}
                onClick={onRetry}
              >
                Try again
              </Button>
            </div>
          </div>
        </Card>
      ) : isLoading ? (
        <Card withBorder radius="md" padding={0} data-testid="activity-skeleton" aria-busy="true">
          <div className="flex flex-col gap-2.5 p-4">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} height={16} radius="sm" />
            ))}
          </div>
        </Card>
      ) : rows.length === 0 ? (
        <Card withBorder radius="md" padding={0}>
          <div className="flex items-start gap-3 p-4" data-testid="activity-empty">
            <span
              className="grid h-9 w-9 flex-none place-items-center rounded-token-md bg-accent-soft text-accent"
              aria-hidden
            >
              <IconHistory size={20} stroke={NAV_ICON_STROKE} />
            </span>
            <div className="min-w-0">
              <p className="text-[15px] font-semibold text-ink">No changes yet</p>
              <p className="mt-0.5 text-[13px] leading-snug text-ink-3">
                When either adult — or the assistant working for them — adds an event, pays a bill or ticks off a list
                item, it shows here with their name and the assistant&apos;s. That is how you answer &ldquo;did you do
                that?&rdquo; and &ldquo;did anything get deleted?&rdquo; without asking.
              </p>
            </div>
          </div>
        </Card>
      ) : (
        <Card withBorder radius="md" padding={0} className="overflow-hidden">
          <ul className="m-0 list-none p-0">
            {rows.map((entry) => (
              <ActivityRowView
                key={entry.id}
                entry={entry}
                viewerUserId={viewerUserId}
                exists={exists?.get(entry.id)}
                people={people}
              />
            ))}
          </ul>
          {hidden > 0 && (
            <Link
              href={href}
              className="flex min-h-11 items-center justify-center gap-1.5 border-t border-line bg-surface-2 text-[13px] font-semibold text-accent"
              data-testid="activity-more"
            >
              {/* No number: `hidden` counts only the rows this page fetched, so
                  it proves there IS more, not how much. A count here would be a
                  claim about the whole feed that this window cannot see. */}
              See all activity
              <IconChevronRight size={16} stroke={NAV_ICON_STROKE} aria-hidden />
            </Link>
          )}
        </Card>
      )}
    </section>
  );
}

/**
 * One change: who, what, how (agent), when — and where it opens.
 *
 * The whole row is the link when the record can be opened, so the tap target is
 * the row and not a 14 px phrase on it. When it cannot (a deletion, a record
 * that is gone, a kind with no page) the row is a plain `li` that says why: a
 * link to a page that cannot show the record is a dead control.
 */
export function ActivityRowView({
  entry,
  viewerUserId,
  exists,
  people = [],
}: {
  entry: ActivityRow;
  viewerUserId: string | null;
  exists: boolean | null | undefined;
  people?: readonly PersonRow[];
}) {
  const parts = feedParts(entry, viewerUserId);
  const target = activityTarget(entry, exists);
  const when = attribution(entry, viewerUserId).when;
  const avatar = activityAvatar(entry, viewerUserId, people);

  const body = (
    <>
      <span
        className={[
          "grid h-8 w-8 flex-none place-items-center rounded-full text-[12px] font-semibold",
          avatar.color ? "text-white" : "bg-accent-soft text-accent",
        ].join(" ")}
        style={avatar.color ? { background: `var(--mantine-color-${avatar.color}-6)` } : undefined}
        aria-hidden
      >
        {avatar.label}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-[14px] leading-snug text-ink-2">
          <b className="font-semibold text-ink">{parts.who}</b> {parts.text}
        </span>
        <span className="mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[12px] text-ink-3">
          {parts.viaAgent && (
            <span
              className="inline-flex items-center gap-1 rounded-full bg-surface-2 px-1.5 py-0.5 font-medium text-ink-2"
              data-testid="activity-agent"
              title={`This change was made through the ${parts.agentName} agent`}
            >
              <IconRobot size={13} stroke={NAV_ICON_STROKE} aria-hidden />
              via {parts.agentName}
            </span>
          )}
          {parts.viaAgent && <span aria-hidden>·</span>}
          <time dateTime={entry.created_at}>{when}</time>
          {target.note && (
            <>
              <span aria-hidden>·</span>
              <span
                className={
                  target.kind === "deleted" || target.kind === "gone"
                    ? "font-medium text-[var(--warn-ink)]"
                    : "text-ink-3"
                }
                data-testid={`activity-state-${target.kind}`}
              >
                {target.note}
              </span>
            </>
          )}
        </span>
      </span>
      {target.href && (
        <IconChevronRight size={16} stroke={NAV_ICON_STROKE} className="mt-1 flex-none text-ink-3" aria-hidden />
      )}
    </>
  );

  const shell = "flex gap-3 border-b border-line px-3.5 py-3 last:border-b-0";

  return (
    <li data-testid="activity-row" data-target={target.kind}>
      {target.href ? (
        <Link href={target.href} className={`${shell} min-h-14 hover:bg-surface-2`} data-testid="activity-link">
          {body}
        </Link>
      ) : (
        <div className={shell}>{body}</div>
      )}
    </li>
  );
}
