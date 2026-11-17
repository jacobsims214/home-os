"use client";

/**
 * Family activity — "see all" behind Today's panel (#4577).
 *
 * Today shows the newest four changes; this page is the rest of the trail, in
 * pages of 50, newest first. It exists because the panel's link has to go
 * somewhere that can actually show the history: the R1 war game needed "what
 * has the other adult done?" and "did anything get deleted?" answered, and four
 * rows is not an answer when the question is about last week.
 *
 * Every row carries the attribution the API records — the person, the agent it
 * came through (`via_agent` / `agent.name`), and the ready-made description — and
 * the same "gone" handling Today uses: a deletion says so, and a record that
 * 404s now says it was deleted since instead of opening a page that cannot show
 * it (lib/today/agenda.ts `activityTarget`).
 *
 * Role policy (#4689): only owner, family_manager, family_member and viewer may
 * read the feed. A housesitter or a vendor reaching this URL (a bookmark, a
 * back button) gets a plain explanation, not the panel: the API answers 403, and
 * the page says that rather than showing an error it cannot fix.
 */

import Link from "next/link";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Button, Card } from "@mantine/core";
import { IconAlertTriangle, IconArrowLeft, IconHistory, IconRefresh } from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import { useNavRole } from "@/components/shared/use-nav-role";
import { ActivityRowView } from "@/components/today/ActivityFeed";
import { canReadActivity, roleKnown } from "@/lib/resource-access";
import { ApiError, apiFetch } from "@/lib/api";
import type { ActivityRow } from "@/lib/today/agenda";
import { useActivityRecordExistence, useMe, usePeople } from "@/lib/today/api";

const PAGE_SIZE = 50;

interface ActivityPage {
  data: ActivityRow[];
  next_cursor: string | null;
}

export default function ActivityPage() {
  const role = useNavRole();
  // Three states, not two: the role is unknown (GET /me/households in flight),
  // known and allowed, or known and refused. Only the last one is the
  // "not your access" message — telling a viewer they may not read the feed
  // while the request is still in flight would be a wrong answer, not a slow one.
  const known = roleKnown(role);
  const canRead = !known ? false : canReadActivity(role);
  // The viewer's own id, so their own writes read "You" here exactly as they do
  // on Today: the two surfaces share ActivityRowView, so they must agree.
  const me = useMe();
  const viewerUserId = me.data?.id ?? null;
  const people = usePeople(canRead);

  const feed = useInfiniteQuery({
    queryKey: ["activity", "all"],
    queryFn: ({ pageParam }) =>
      apiFetch<ActivityPage>("/api/v1/activity", {
        params: { limit: PAGE_SIZE, cursor: pageParam ?? undefined },
      }),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled: canRead,
    staleTime: 30_000,
    retry: false,
  });

  const entries = (feed.data?.pages ?? []).flatMap((p) => p.data ?? []);
  const exists = useActivityRecordExistence(entries, canRead);
  const forbidden = feed.error instanceof ApiError && feed.error.status === 403;

  return (
    <div className="mx-auto flex w-full max-w-[760px] flex-col gap-4 px-4 pb-8 pt-4 md:px-6 md:pt-6">
      <div>
        <Link
          href="/dashboard"
          className="inline-flex min-h-11 items-center gap-1.5 text-[13px] font-semibold text-accent md:min-h-0"
        >
          <IconArrowLeft size={16} stroke={NAV_ICON_STROKE} aria-hidden />
          Today
        </Link>
        {/* The shell's header already prints "Family activity" (nav-config's
            ROUTE_TITLES), so this page does not repeat it: one h1 per screen. */}
        <p className="mt-1 text-sm text-ink-2">
          Every change in this family, newest first, with the person who made it and the assistant it came through.
        </p>
      </div>

      {known && (!canRead || forbidden) ? (
        <Card withBorder radius="md" padding="md" data-testid="activity-forbidden">
          <div className="flex items-start gap-3">
            <span className="grid h-9 w-9 flex-none place-items-center rounded-token-md bg-accent-soft text-accent" aria-hidden>
              <IconHistory size={20} stroke={NAV_ICON_STROKE} />
            </span>
            <div className="min-w-0">
              <p className="text-[15px] font-semibold text-ink">Activity isn&apos;t part of your access</p>
              <p className="mt-0.5 text-[13px] leading-snug text-ink-3">
                The family change history is readable by the family roles and a viewer. Your role can&apos;t read it, so
                there is nothing to show here — ask an owner if you need the history.
              </p>
            </div>
          </div>
        </Card>
      ) : !known || feed.isLoading ? (
        <Card withBorder radius="md" padding="md" aria-busy="true" data-testid="activity-loading">
          <div className="flex flex-col gap-3">
            {[0, 1, 2, 3].map((i) => (
              <div key={i} className="h-4 animate-pulse rounded-sm bg-line" />
            ))}
          </div>
        </Card>
      ) : feed.error ? (
        <Card
          withBorder
          radius="md"
          padding="sm"
          role="alert"
          className="border-l-4 border-l-[var(--danger)]"
          data-testid="activity-error"
        >
          <div className="flex items-start gap-3">
            <IconAlertTriangle size={22} stroke={NAV_ICON_STROKE} className="mt-0.5 flex-none text-[var(--danger)]" aria-hidden />
            <div className="min-w-0">
              <p className="text-[15px] font-semibold text-ink">The activity history didn&apos;t load</p>
              <p className="mt-0.5 text-[13px] text-ink-2">
                This is a failed read, not an empty history — nothing here says the family has done nothing.
              </p>
              <Button
                size="xs"
                variant="default"
                mt="xs"
                leftSection={<IconRefresh size={16} stroke={NAV_ICON_STROKE} />}
                onClick={() => feed.refetch()}
              >
                Try again
              </Button>
            </div>
          </div>
        </Card>
      ) : entries.length === 0 ? (
        <Card withBorder radius="md" padding={0}>
          <div className="flex items-start gap-3 p-4" data-testid="activity-empty">
            <span className="grid h-9 w-9 flex-none place-items-center rounded-token-md bg-accent-soft text-accent" aria-hidden>
              <IconHistory size={20} stroke={NAV_ICON_STROKE} />
            </span>
            <div className="min-w-0">
              <p className="text-[15px] font-semibold text-ink">No changes yet</p>
              <p className="mt-0.5 text-[13px] leading-snug text-ink-3">
                Every create, update and delete in this family lands here with who did it. It starts filling as soon as
                anyone — or their assistant — changes something.
              </p>
            </div>
          </div>
        </Card>
      ) : (
        <Card withBorder radius="md" padding={0} className="overflow-hidden">
          <ul className="m-0 list-none p-0">
            {entries.map((entry) => (
              <ActivityRowView
                key={entry.id}
                entry={entry}
                viewerUserId={viewerUserId}
                exists={exists.exists.get(entry.id)}
                people={people.data?.data ?? []}
              />
            ))}
          </ul>
        </Card>
      )}

      {canRead && !forbidden && feed.hasNextPage && (
        <Button
          variant="default"
          onClick={() => feed.fetchNextPage()}
          loading={feed.isFetchingNextPage}
          data-testid="activity-more"
        >
          Show older changes
        </Button>
      )}
    </div>
  );
}
