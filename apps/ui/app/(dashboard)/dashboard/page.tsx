"use client";

/**
 * Today — the screen a parent opens in the morning to run the day (#4549).
 *
 * Built to the approved mockup (artifact #76 v1, "Today (home) screen") inside
 * the approved shell (#75 v1); it replaces the old dashboard home (financial
 * summary cards), which is not what a parent opens first.
 *
 * WHAT IT SHOWS, AND WHY (all from the R1 war games, KB
 * discoveries/war-game-r1-*.md):
 *
 *  1. TODAY, IN TIME ORDER, MERGED ACROSS BOTH ADULTS. Cross-family visibility
 *     is proven working, so the day is one list, not one per adult. Who added
 *     what is on the row ("Amber via moltis-wg-amber" in Family activity)
 *     because attribution is what stops "did you book that?".
 *  2. BILLS DUE / OVERDUE. "Due" is a claim about today's date, so it is
 *     COMPUTED (lib/today/due.ts, the browser's copy of the one shared Go
 *     rule), never inferred from a calendar event. A PAID BILL NEVER APPEARS AS
 *     DUE: status comes from the bills endpoint's paid_date /
 *     paid_for_due_date. This was a real R1 failure — an agent said "Electric
 *     $180 due, still unpaid" about a bill paid that morning, because a stale
 *     Bills-calendar event looked like bill state. Bill mirror events are
 *     dropped from the agenda for exactly that reason (isBillMirrorEvent).
 *  3. WHAT'S FOR DINNER, from the meal plan, with the recipe's time/servings
 *     when the recipe is loaded and an HONEST allergy line.
 *  4. PEOPLE / CHORES: who is on the family's task lists, what is open, who
 *     owes what; allergies are named, because they are safety data.
 *  5. ANYTHING UNVERIFIED SAYS SO. If the allergy check could not run (the
 *     REST API does not return allergy_warnings/checks today, #4891), the
 *     dinner says "Not allergy-checked" — never a reassuring tick. Same for a
 *     section whose endpoint is missing: it says what is missing and why.
 *  6. EMPTY / DEGRADED STATES EXPLAIN WHY, never a blank panel: a new family
 *     sees what will appear here and what fills it.
 *
 * Each source loads and fails on its own: a failed read shows an inline error
 * card with Try again and NEVER renders as empty data (audit #74).
 *
 * GAPS, deliberately rendered rather than faked (see the completion comment):
 *  - "who is away" has no endpoint (no person-absence record exists), so the
 *    People panel says so instead of showing nobody away.
 *  - "Amber is cooking" / "you're driving" are not stored anywhere; the dinner
 *    and event rows show only what the API knows.
 *  - A recipe's missing ingredients for dinner has no endpoint either
 *    (meal-plans/grocery merges a week; it is not a per-dinner read), so the
 *    dinner card links to the grocery preview rather than inventing the list.
 *
 * Roles: a housesitter or viewer reads Today (a sitter needs the day, dinner
 * and lists); bills, upkeep money and the activity feed are hidden for a role
 * the API refuses anyway (KB decisions/2026-11-15-role-policy.md).
 */

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  Badge,
  Button,
  Card,
  Group,
  Skeleton,
  Stack,
  Text,
  ThemeIcon,
  Title,
  UnstyledButton,
} from "@mantine/core";
import {
  IconAlertTriangle,
  IconCalendar,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconListCheck,
  IconMapPin,
  IconPlus,
  IconRefresh,
  IconRepeat,
  IconStethoscope,
  IconToolsKitchen,
  IconUsers,
} from "@tabler/icons-react";
import { reportAllergies } from "@/lib/recipes/allergy";
import { useNavRole } from "@/components/shared/use-nav-role";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import ActivityFeed from "@/components/today/ActivityFeed";
import NewEventForm from "@/components/calendar/new-event-form";
import { canReadActivity, roleKnown } from "@/lib/resource-access";
import {
  billsAndUpkeepDue,
  buildAgenda,
  buildTomorrow,
  dinnerToday,
  formatDayHeading,
  formatDueLabel,
  householdToday,
  recipeFacts,
  relativeToNow,
  upNextRow,
  type AgendaRow,
  type DueRow,
  type PersonRow,
} from "@/lib/today/agenda";
import type { RecipeRow } from "@/lib/today/api";
import {
  currentHousehold,
  useActivity,
  useActivityRecordExistence,
  useAgenda,
  useBills,
  useListItemsFor,
  useLists,
  useMaintenanceTasks,
  useMe,
  usePeople,
  useRecipe,
  useTodayHousehold,
  useTodayMeals,
} from "@/lib/today/api";

/** Roles that may see money (bills, upkeep costs) — mirrors the API's policy. */
const MONEY_ROLES = new Set(["owner", "family_manager", "family_member", "viewer"]);
const FAMILY_ROLES = new Set(["owner", "family_manager", "family_member"]);

// ── page ─────────────────────────────────────────────────────

export default function TodayPage() {
  const role = useNavRole();
  const household = useTodayHousehold();
  const hh = currentHousehold(household.data?.data);
  const timezone = hh?.timezone ?? null;

  // Today's date in the HOUSEHOLD's zone, computed after mount: the server's
  // zone may differ from the browser's, so rendering it during SSR would
  // hydrate a different date than the one the family sees. Until it is known,
  // the sections show their loading state rather than a wrong day.
  const [now, setNow] = useState<Date | null>(null);
  useEffect(() => {
    setNow(new Date());
    // A parent leaves this page open; the "now" line and the greeting should
    // not go stale for the whole morning.
    const id = setInterval(() => setNow(new Date()), 60_000);
    return () => clearInterval(id);
  }, []);
  const today = now ? householdToday(timezone, now) : null;
  const nowMinutes = now
    ? (() => {
        const fmt = new Intl.DateTimeFormat("en-US", {
          timeZone: timezone ?? undefined,
          hour: "2-digit",
          minute: "2-digit",
          hour12: false,
        });
        const parts = fmt.formatToParts(now);
        const h = Number(parts.find((p) => p.type === "hour")?.value ?? "0") % 24;
        const m = Number(parts.find((p) => p.type === "minute")?.value ?? "0");
        return h * 60 + m;
      })()
    : null;

  const canSeeMoney = role == null || MONEY_ROLES.has(role);
  // ResCalendar write is owner / family_manager / family_member (apps/api
  // internal/auth/roles.go): a housesitter and a viewer may READ the day but not
  // add to it. The Calendar page hides its own New event control for them, and
  // Today does the same rather than offering a form the API would refuse.
  const canWriteEvents = role == null || FAMILY_ROLES.has(role);
  // "Add event" opens the calendar's own create form IN PLACE (#5302): the
  // bottom sheet on a phone, the centred dialog from 768 px. Nothing is read
  // until it opens (`useCalendars(opened && …)`), so the closed form is one
  // empty render, not a query on every visit to Today.
  const [addingEvent, setAddingEvent] = useState(false);

  // Family activity needs ResActivity read: owner, family_manager,
  // family_member, viewer. A housesitter and a vendor answer 403, so neither the
  // section nor its request happens for them — #4577, role policy #4689.
  //
  // This waits for the role instead of assuming it: unlike the other gates, a
  // wrong guess here is a refused request on every visit plus a panel that
  // appears and is then withdrawn. Today already reads the same
  // GET /me/households for the timezone under the same query key, so the role
  // arrives with the data the page cannot render without.
  const canSeeFeed = roleKnown(role) && canReadActivity(role);
  const isFamily = role == null || FAMILY_ROLES.has(role);

  const agenda = useAgenda(today);
  const bills = useBills(!!today && canSeeMoney);
  const tasks = useMaintenanceTasks(!!today && canSeeMoney);
  const meals = useTodayMeals(today);
  const people = usePeople(!!today);
  const lists = useLists(!!today);
  const activity = useActivity(!!today && canSeeFeed);
  // Does each change's record still exist? Only the rows Today shows are
  // probed, and only for kinds with a detail page: a 404 is the "it was
  // deleted since" answer, never a guess (#4577).
  const activityExists = useActivityRecordExistence(
    canSeeFeed ? activity.data?.data ?? [] : [],
    !!today && canSeeFeed,
  );
  const me = useMe();

  const dinner = useMemo(
    () => (meals.data?.data && today ? dinnerToday(meals.data.data, today) : null),
    [meals.data, today],
  );
  const recipe = useRecipe(dinner?.recipe_id ?? null);

  const agendaRows = useMemo(
    () => (agenda.data?.data && today ? buildAgenda(agenda.data.data, today, timezone, nowMinutes) : []),
    [agenda.data, today, timezone, nowMinutes],
  );
  const tomorrowRows = useMemo(
    () => (agenda.data?.data && today ? buildTomorrow(agenda.data.data, shiftDay(today), timezone) : []),
    [agenda.data, today, timezone],
  );
  const due = useMemo(
    () =>
      today
        ? billsAndUpkeepDue(today, bills.data?.data ?? [], tasks.data?.data ?? [])
        : { rows: [] as DueRow[], withoutDueDate: 0 },
    [today, bills.data, tasks.data],
  );

  // Nothing has rendered yet (before the first mount): a skeleton, not a blank.
  if (!now || !today) {
    return (
      <div className="mx-auto w-full max-w-[1120px] px-4 pb-8 pt-4 md:px-6 md:pt-6">
        <TodaySkeleton />
      </div>
    );
  }

  const viewerName = me.data?.name?.split(/\s+/)[0] ?? null;
  const summary = greetingSummary(agendaRows, due.rows, dinner?.title ?? null);

  return (
    <div className="mx-auto flex w-full max-w-[1120px] flex-col gap-6 px-4 pb-8 pt-4 md:px-6 md:pt-6">
      {/* ── Greeting + quick actions ─────────────────────────── */}
      <section aria-labelledby="today-greeting" className="flex flex-col gap-3.5 lg:flex-row lg:items-end lg:justify-between lg:gap-6">
        <div>
          <Title order={2} id="today-greeting" className="text-[22px] leading-tight tracking-tight text-ink lg:text-[26px]">
            {greeting(nowMinutes, viewerName)}
          </Title>
          <p className="mt-1 text-sm text-ink-2" data-testid="today-summary">
            {summary}
          </p>
        </div>
        <div
          role="group"
          aria-label="Quick actions"
          className="-mx-4 flex gap-2 overflow-x-auto px-4 pb-0.5 [scrollbar-width:none] lg:mx-0 lg:flex-wrap lg:px-0 [&::-webkit-scrollbar]:hidden"
        >
          {canWriteEvents && (
            <QuickAction
              onClick={() => setAddingEvent(true)}
              icon={<IconCalendar size={18} stroke={NAV_ICON_STROKE} />}
              label="Add event"
              testId="today-add-event"
            />
          )}
          <QuickAction href="/dashboard/lists" icon={<IconListCheck size={18} stroke={NAV_ICON_STROKE} />} label="Add to a list" />
          <QuickAction href="/dashboard/meals" icon={<IconToolsKitchen size={18} stroke={NAV_ICON_STROKE} />} label="Plan dinner" />
          {isFamily && <QuickAction href="/dashboard/members" icon={<IconUsers size={18} stroke={NAV_ICON_STROKE} />} label="Family" />}
        </div>
      </section>

      {/*
        ONE column on phones, TWO on desktop, and the phone order is the
        mockup's (artifact #76): agenda, needs-you, bills, dinner, tomorrow,
        activity. The two column wrappers are `contents` on phones, so their
        sections are laid out by the one flex column and `order-*` decides the
        sequence; from lg up each wrapper is a real column again.
      */}
      <div className="flex flex-col gap-6 lg:grid lg:grid-cols-[minmax(0,1.55fr)_minmax(0,1fr)] lg:items-start lg:gap-7">
        <div className="contents lg:flex lg:min-w-0 lg:flex-col lg:gap-6">
          <div className="order-1 lg:order-none">
            <AgendaSection
              rows={agendaRows}
              tomorrowRows={tomorrowRows}
              nowMinutes={nowMinutes}
              isLoading={agenda.isLoading}
              error={agenda.error as Error | null}
              onRetry={() => agenda.refetch()}
              canAddEvent={canWriteEvents}
              onAddEvent={() => setAddingEvent(true)}
            />
          </div>
          <div className="order-5 lg:order-none">
            <TomorrowSection
              rows={tomorrowRows}
              date={shiftDay(today)}
              isLoading={agenda.isLoading}
              error={agenda.error as Error | null}
              onRetry={() => agenda.refetch()}
            />
          </div>
        </div>

        <div className="contents lg:flex lg:min-w-0 lg:flex-col lg:gap-6">
          {canSeeFeed && (
            <div className="order-2 lg:order-none">
              <NeedsYouSection lists={lists} today={today} people={people.data?.data ?? []} viewerUserId={me.data?.id ?? null} />
            </div>
          )}
          {canSeeMoney && (
            <div className="order-3 lg:order-none">
              <DueSection
                rows={due.rows}
                withoutDueDate={due.withoutDueDate}
                today={today}
                isLoading={bills.isLoading || tasks.isLoading}
                error={(bills.error ?? tasks.error) as Error | null}
                onRetry={() => {
                  bills.refetch();
                  tasks.refetch();
                }}
              />
            </div>
          )}
          <div className="order-4 lg:order-none">
            <DinnerSection
              meal={dinner}
              today={today}
              recipe={recipe.data?.data ?? null}
              people={people.data?.data ?? []}
              peopleError={people.error as Error | null}
              isLoading={meals.isLoading}
              error={meals.error as Error | null}
              onRetry={() => meals.refetch()}
            />
          </div>
          {canSeeFeed && (
            <div className="order-6 lg:order-none">
              <ActivityFeed
                entries={activity.data?.data ?? []}
                viewerUserId={me.data?.id ?? null}
                isLoading={activity.isLoading}
                error={activity.error as Error | null}
                onRetry={() => activity.refetch()}
                exists={activityExists.exists}
                people={people.data?.data ?? []}
              />
            </div>
          )}
          <div className="order-7 lg:order-none">
            <PeopleSection
              people={people.data?.data ?? []}
              isLoading={people.isLoading}
              error={people.error as Error | null}
              onRetry={() => people.refetch()}
            />
          </div>
        </div>
      </div>

      {/*
        The calendar's own create form, opened IN PLACE by "Add event" (#5302):
        the bottom sheet on a phone, the centred dialog from 768 px — the wave's
        one create-modal idiom, not a second form. Today's date is the anchor,
        so a new event lands on the day the person is looking at.
      */}
      {canWriteEvents && (
        <NewEventForm
          opened={addingEvent}
          onClose={() => setAddingEvent(false)}
          anchorDate={today}
          timezone={timezone}
          canWrite={canWriteEvents}
          // The new event belongs on today's agenda as soon as the API accepts
          // it: refetch the day rather than leaving the page a stale copy.
          onCreated={() => agenda.refetch()}
        />
      )}
    </div>
  );
}

// ── greeting ────────────────────────────────────────────────

function greeting(nowMinutes: number | null, firstName: string | null): string {
  const who = firstName ? `, ${firstName}` : "";
  if (nowMinutes === null) return `Today${who}`;
  if (nowMinutes < 12 * 60) return `Good morning${who}`;
  if (nowMinutes < 17 * 60) return `Good afternoon${who}`;
  return `Good evening${who}`;
}

/** "7 events today · 3 things need you · fajitas tonight" — what the day holds. */
function greetingSummary(rows: readonly AgendaRow[], dueRows: readonly DueRow[], dinnerTitle: string | null): string {
  const parts: string[] = [];
  const counted = rows.filter((r) => !r.allDay).length + rows.filter((r) => r.allDay).length;
  if (counted > 0) parts.push(`${counted} ${counted === 1 ? "event" : "events"} today`);
  else parts.push("Nothing on the calendar today");
  if (dueRows.length > 0) parts.push(`${dueRows.length} due in the next 2 weeks`);
  if (dinnerTitle) parts.push(`${dinnerTitle.toLowerCase()} tonight`);
  return parts.join(" · ");
}

// ── shared bits ─────────────────────────────────────────────

/**
 * A quick action: a chip that either opens a form here (`onClick`) or goes to
 * the page that owns the job (`href`). "Add event" is the first kind — a chip
 * that promised a form but only changed the page is exactly the R2 G9 failure
 * (#5302), so a chip that opens something now SAYS so with aria-haspopup.
 */
function QuickAction({
  href,
  onClick,
  icon,
  label,
  testId,
}: {
  href?: string;
  onClick?: () => void;
  icon: React.ReactNode;
  label: string;
  testId?: string;
}) {
  const shared = {
    className:
      "flex h-10 flex-none items-center gap-1.5 rounded-full border border-line bg-surface px-3.5 pl-3 text-sm font-medium text-ink hover:bg-surface-2 lg:h-9 lg:rounded-token-sm",
    "data-testid": testId,
  };
  const inner = (
    <>
      <span className="text-accent" aria-hidden>{icon}</span>
      {label}
    </>
  );
  if (onClick) {
    return (
      <UnstyledButton onClick={onClick} aria-haspopup="dialog" {...shared}>
        {inner}
      </UnstyledButton>
    );
  }
  return (
    <UnstyledButton component={Link} href={href ?? "#"} {...shared}>
      {inner}
    </UnstyledButton>
  );
}

function SectionHeading({ id, title, count, action }: { id: string; title: string; count?: string; action?: { href: string; label: string } }) {
  return (
    <div className="mb-2 flex items-baseline justify-between gap-3">
      <h3 id={id} className="text-base font-semibold tracking-tight text-ink">
        {title}
        {count ? <span className="ml-1 font-medium text-ink-3">{count}</span> : null}
      </h3>
      {action && (
        <Link href={action.href} className="py-2 text-[13px] font-semibold text-accent">
          {action.label}
        </Link>
      )}
    </div>
  );
}

/** A card that explains itself when it has nothing to show (never a blank panel). */
function CalmPanel({ icon, title, children, action }: { icon: React.ReactNode; title: string; children?: React.ReactNode; action?: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3 p-4">
      <ThemeIcon variant="light" color="cyan" size={36} radius="md" aria-hidden>
        {icon}
      </ThemeIcon>
      <div className="min-w-0">
        <p className="text-[15px] font-semibold text-ink">{title}</p>
        {children ? <div className="mt-0.5 text-[13px] text-ink-3">{children}</div> : null}
        {action ? <div className="mt-2.5">{action}</div> : null}
      </div>
    </div>
  );
}

/**
 * A loading state for a section: skeleton rows ONLY inside the content area, so
 * the shell and the page's shape are stable while a source is in flight.
 */
function SectionSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <Card withBorder radius="md" padding={0} data-testid="section-skeleton" aria-busy="true">
      <Stack gap={10} p="sm">
        {Array.from({ length: rows }).map((_, i) => (
          <Skeleton key={i} height={18} radius="sm" />
        ))}
      </Stack>
    </Card>
  );
}

/**
 * A source that failed. It says what is missing and that the rest of the page
 * is fine, with Try again — it must never look like "there is nothing today"
 * (audit #74's root cause).
 */
function ErrorPanel({ title, message, onRetry }: { title: string; message: string; onRetry: () => void }) {
  return (
    <Card
      withBorder
      radius="md"
      padding="sm"
      role="alert"
      data-testid="section-error"
      className="border-l-4 border-l-[var(--danger)]"
    >
      <div className="flex items-start gap-3">
        <IconAlertTriangle size={22} stroke={NAV_ICON_STROKE} className="mt-0.5 flex-none text-[var(--danger)]" aria-hidden />
        <div className="min-w-0">
          <p className="text-[15px] font-semibold text-ink">{title}</p>
          <p className="mt-0.5 text-[13px] text-ink-2">{message}</p>
          <Button size="xs" variant="default" mt="xs" leftSection={<IconRefresh size={16} stroke={NAV_ICON_STROKE} />} onClick={onRetry}>
            Try again
          </Button>
        </div>
      </div>
    </Card>
  );
}

/** A section with no endpoint yet: the honest gap, named. */
function GapNote({ children }: { children: React.ReactNode }) {
  return (
    <p className="mt-2 rounded-token-sm border border-dashed border-line px-2.5 py-2 text-[12px] leading-relaxed text-ink-3" data-testid="today-gap">
      {children}
    </p>
  );
}

// ── Today's agenda ──────────────────────────────────────────

function AgendaSection({
  rows,
  tomorrowRows,
  nowMinutes,
  isLoading,
  error,
  onRetry,
  canAddEvent,
  onAddEvent,
}: {
  rows: AgendaRow[];
  /** Tomorrow's rows, for the empty state's "tomorrow starts at ..." line only. */
  tomorrowRows: AgendaRow[];
  nowMinutes: number | null;
  isLoading: boolean;
  error: Error | null;
  onRetry: () => void;
  canAddEvent: boolean;
  onAddEvent: () => void;
}) {
  // "Show N more tonight": everything past the first five timed rows, as the
  // mockup has it. Recurring expansion means a busy day can be long.
  const [expanded, setExpanded] = useState(false);
  const allDay = rows.filter((r) => r.allDay);
  const timed = rows.filter((r) => !r.allDay);
  const next = upNextRow(rows);
  const visible = expanded ? timed : timed.slice(0, 5);
  const hidden = timed.length - visible.length;
  const nowLineIndex = nowMinutes === null ? -1 : visible.findIndex((r) => r.minutes !== null && r.minutes > nowMinutes);
  const allDayCount = allDay.length;

  return (
    <section aria-labelledby="h-today" data-testid="today-agenda">
      <SectionHeading
        id="h-today"
        title="Today"
        count={allDayCount + timed.length > 0 ? String(allDayCount + timed.length) : undefined}
        action={{ href: "/dashboard/calendar", label: "Calendar" }}
      />
      {error ? (
        <ErrorPanel
          title="The calendar didn't load"
          message="We couldn't reach it just now, so today's and tomorrow's events are missing. Everything else below is up to date."
          onRetry={onRetry}
        />
      ) : isLoading ? (
        <SectionSkeleton rows={4} />
      ) : rows.length === 0 ? (
        <Card withBorder radius="md" padding={0}>
          <CalmPanel
            icon={<IconCalendar size={20} stroke={NAV_ICON_STROKE} />}
            title="Nothing scheduled today"
            action={
              canAddEvent ? (
                <Button onClick={onAddEvent} size="xs" variant="default" leftSection={<IconPlus size={16} stroke={NAV_ICON_STROKE} />} data-testid="today-add-event-empty">
                  Add event
                </Button>
              ) : undefined
            }
          >
            {tomorrowRows.length > 0
              ? `Tomorrow starts at ${tomorrowRows.find((r) => !r.allDay)?.time ?? "an all-day event"} with ${tomorrowRows.find((r) => !r.allDay)?.event.title ?? tomorrowRows[0].event.title}.`
              : "Nothing is on the family calendar today or tomorrow. Events both adults add appear here, in time order."}
          </CalmPanel>
        </Card>
      ) : (
        <Card withBorder radius="md" padding={0} className="overflow-hidden">
          {allDay.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5 border-b border-line bg-surface-2 px-3.5 py-2.5" data-testid="today-allday">
              <span className="mr-0.5 self-center text-[12px] font-semibold uppercase tracking-wide text-ink-3">All day</span>
              {allDay.map((r) => (
                <span key={r.key} className="inline-flex min-h-7 items-center gap-1.5 rounded-full border border-line bg-surface px-2.5 text-[13px] font-medium text-ink">
                  <span className="h-2 w-2 flex-none rounded-full" style={{ background: r.event.color || "var(--accent)" }} aria-hidden />
                  {r.event.title}
                </span>
              ))}
            </div>
          )}
          {visible.map((row, i) => (
            <AgendaRowView key={`${row.key}-${row.minutes ?? "all"}`} row={row} isNext={next !== null && row.key === next.key} showNowLine={nowLineIndex === i && nowMinutes !== null} nowMinutes={nowMinutes} />
          ))}
          {nowLineIndex === -1 && nowMinutes !== null && timed.some((r) => r.happeningNow) && (
            <div className="mx-3.5 flex h-0 items-center" aria-hidden>
              <span className="flex-none rounded-full bg-badge px-1.5 text-[11px] font-semibold leading-5 text-white">{minutesLabel(nowMinutes)}</span>
              <span className="h-0.5 flex-1 bg-badge" />
            </div>
          )}
          {hidden > 0 && (
            <button
              type="button"
              onClick={() => setExpanded(true)}
              aria-expanded={false}
              className="flex min-h-11 w-full items-center justify-center gap-1.5 border-t border-line text-[13px] font-semibold text-accent"
            >
              Show {hidden} more later today
              <IconChevronDown size={16} stroke={NAV_ICON_STROKE} aria-hidden />
            </button>
          )}
          {expanded && timed.length > 5 && (
            <button
              type="button"
              onClick={() => setExpanded(false)}
              aria-expanded
              className="flex min-h-11 w-full items-center justify-center gap-1.5 border-t border-line text-[13px] font-semibold text-accent"
            >
              Show less
              <IconChevronDown size={16} stroke={NAV_ICON_STROKE} className="rotate-180" aria-hidden />
            </button>
          )}
        </Card>
      )}

    </section>
  );
}

/**
 * Tomorrow's peek. Its own section rather than part of Today's, so the phone
 * can put it where the mockup does (after dinner, before activity) while the
 * desktop column keeps it under the agenda.
 */
function TomorrowSection({
  rows,
  date,
  isLoading,
  error,
  onRetry,
}: {
  rows: AgendaRow[];
  date: string;
  isLoading: boolean;
  error: Error | null;
  onRetry: () => void;
}) {
  return (
    <section aria-labelledby="h-tom" data-testid="today-tomorrow">
      <SectionHeading id="h-tom" title="Tomorrow" count={`· ${formatDayHeading(date)}`} action={{ href: "/dashboard/calendar", label: "Week" }} />
      {error ? (
        <ErrorPanel title="Tomorrow didn't load" message="It comes back with the calendar, not on its own." onRetry={onRetry} />
      ) : isLoading ? (
        <SectionSkeleton rows={2} />
      ) : rows.length === 0 ? (
        <Card withBorder radius="md" padding={0}>
          <CalmPanel icon={<IconCalendar size={20} stroke={NAV_ICON_STROKE} />} title="Nothing scheduled tomorrow">
            As soon as someone adds an event, it shows here the night before.
          </CalmPanel>
        </Card>
      ) : (
        <Card withBorder radius="md" padding={0}>
          {rows.slice(0, 4).map((r) => (
            <div key={`tm-${r.key}`} className="flex min-h-12 items-center gap-2.5 border-b border-line px-3.5 py-2.5 last:border-b-0">
              <time className="w-16 flex-none text-[13px] font-medium text-ink-2">{r.allDay ? "All day" : r.time}</time>
              <span className="h-2 w-2 flex-none rounded-full" style={{ background: r.event.color || "var(--accent)" }} aria-hidden />
              <span className="min-w-0 flex-1 truncate text-sm font-medium text-ink">{r.event.title}</span>
              {r.recurrence && <IconRepeat size={14} stroke={NAV_ICON_STROKE} className="flex-none text-ink-3" aria-label={r.recurrence} />}
            </div>
          ))}
          {rows.length > 4 && <p className="border-t border-line px-3.5 py-2 text-[12px] text-ink-3">and {rows.length - 4} more</p>}
        </Card>
      )}
    </section>
  );
}

function minutesLabel(minutes: number): string {
  const h24 = Math.floor(minutes / 60) % 24;
  const m = String(minutes % 60).padStart(2, "0");
  return `${h24 % 12 === 0 ? 12 : h24 % 12}:${m}`;
}

function AgendaRowView({
  row,
  isNext,
  showNowLine,
  nowMinutes,
}: {
  row: AgendaRow;
  isNext: boolean;
  showNowLine: boolean;
  nowMinutes: number | null;
}) {
  const color = row.event.color || "var(--accent)";
  const location = row.event.location?.trim() ?? "";
  return (
    <>
      {showNowLine && nowMinutes !== null && (
        <div className="mx-3.5 flex h-0 items-center" aria-label={`Now, ${minutesLabel(nowMinutes)}`}>
          <span className="flex-none rounded-full bg-badge px-1.5 text-[11px] font-semibold leading-5 text-white">{minutesLabel(nowMinutes)}</span>
          <span className="h-0.5 flex-1 bg-badge" />
        </div>
      )}
      <div
        className={[
          "grid min-h-[60px] items-center gap-3 border-b border-line px-3.5 py-3 last:border-b-0",
          "grid-cols-[58px_3px_minmax(0,1fr)]",
          row.past ? "text-ink-3" : "",
        ].join(" ")}
        data-testid="agenda-row"
        data-past={row.past ? "true" : undefined}
        data-next={isNext ? "true" : undefined}
      >
        <time className="text-[13px] font-medium leading-tight" style={{ color: row.past ? "var(--ink-3)" : "var(--ink-2)" }}>
          {row.time}
        </time>
        <span className="min-h-[34px] self-stretch rounded-sm" style={{ background: color, opacity: row.past ? 0.4 : 1 }} aria-hidden />
        <div className="min-w-0">
          <b className={["block truncate text-[15px] leading-snug", row.past ? "font-medium text-ink-2" : "font-semibold text-ink"].join(" ")}>
            {row.event.title}
          </b>
          <div className="flex items-center gap-1.5 overflow-hidden text-[13px] leading-snug text-ink-3">
            {row.recurrence && (
              <span className="inline-flex flex-none items-center gap-0.5">
                <IconRepeat size={14} stroke={NAV_ICON_STROKE} aria-hidden />
                {row.recurrence}
              </span>
            )}
            {row.recurrence && (location || row.happeningNow) && <span aria-hidden>·</span>}
            {row.happeningNow && <span className="font-medium text-accent">happening now</span>}
            {location && <span className="truncate">{location}</span>}
          </div>
          {isNext && nowMinutes !== null && row.minutes !== null && (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <span className="rounded-full bg-accent-soft px-2 py-0.5 text-[12px] font-semibold text-accent">
                {relativeToNow(row.minutes, nowMinutes) || "now"}
              </span>
              {location && (
                <Button
                  component="a"
                  href={`https://maps.apple.com/?q=${encodeURIComponent(location)}`}
                  target="_blank"
                  rel="noreferrer"
                  size="xs"
                  variant="default"
                  leftSection={<IconMapPin size={16} stroke={NAV_ICON_STROKE} />}
                >
                  Directions
                </Button>
              )}
            </div>
          )}
        </div>
      </div>
    </>
  );
}

// ── Bills & upkeep ──────────────────────────────────────────

function DueSection({
  rows,
  withoutDueDate,
  today,
  isLoading,
  error,
  onRetry,
}: {
  rows: DueRow[];
  withoutDueDate: number;
  today: string;
  isLoading: boolean;
  error: Error | null;
  onRetry: () => void;
}) {
  return (
    <section aria-labelledby="h-due" data-testid="today-due">
      <SectionHeading id="h-due" title="Bills & upkeep" count="· next 2 weeks" action={{ href: "/dashboard/bills", label: "Money" }} />
      {error ? (
        <ErrorPanel
          title="Bills and upkeep didn't load"
          message="Check back in a minute, or open Money to see every bill. Nothing here is being reported as not due."
          onRetry={onRetry}
        />
      ) : isLoading ? (
        <SectionSkeleton rows={3} />
      ) : (
        <Card withBorder radius="md" padding={0} className="overflow-hidden">
          {rows.length === 0 ? (
            <CalmPanel icon={<IconCheck size={20} stroke={NAV_ICON_STROKE} />} title="Nothing due in the next 2 weeks">
              Every bill and upkeep task is either paid or further out. A paid bill never appears here.
            </CalmPanel>
          ) : (
            rows.map((r) => <DueRowView key={`${r.kind}-${r.id}`} row={r} today={today} />)
          )}
          {withoutDueDate > 0 && (
            <div className="border-t border-line bg-surface-2 px-3.5 py-2.5">
              <GapNote>
                {withoutDueDate === 1 ? "1 record" : `${withoutDueDate} records`} {withoutDueDate === 1 ? "has" : "have"} no due
                date, so {withoutDueDate === 1 ? "it can't" : "they can't"} appear here. Open Money to give{" "}
                {withoutDueDate === 1 ? "it" : "them"} one.
              </GapNote>
            </div>
          )}
        </Card>
      )}
    </section>
  );
}

function DueRowView({ row, today }: { row: DueRow; today: string }) {
  const late = row.daysUntil < 0;
  const soon = !late && row.daysUntil <= 3;
  const label = late
    ? `${Math.abs(row.daysUntil)} ${Math.abs(row.daysUntil) === 1 ? "day" : "days"} late`
    : row.daysUntil === 0
      ? "Due today"
      : `Due ${formatDueLabel(row.dueDate, today)}`;
  return (
    <div className="flex min-h-14 items-center gap-3 border-b border-line px-3.5 py-2.5 last:border-b-0" data-testid="due-row" data-late={late ? "true" : undefined}>
      <ThemeIcon variant="light" color={late ? "red" : "gray"} size={36} radius="md" aria-hidden>
        {row.kind === "bill" ? <IconStethoscope size={18} stroke={NAV_ICON_STROKE} /> : <IconToolsKitchen size={18} stroke={NAV_ICON_STROKE} />}
      </ThemeIcon>
      <div className="min-w-0 flex-1">
        <b className="block truncate text-[15px] font-medium leading-snug text-ink">{row.name}</b>
        <span className="block truncate text-[13px] leading-snug text-ink-3">{row.detail}</span>
      </div>
      <div className="flex-none text-right leading-tight">
        {row.amount && <b className="block text-[15px] font-semibold text-ink">{row.amount}</b>}
        <span
          className={[
            "mt-0.5 inline-block whitespace-nowrap rounded-[10px] px-2 py-0.5 text-[12px] font-semibold",
            late ? "text-[var(--danger)] shadow-[inset_0_0_0_1px_currentColor]" : soon ? "bg-warn-bg text-warn-ink shadow-[inset_0_0_0_1px_var(--warn-line)]" : "bg-surface-2 text-ink-2",
          ].join(" ")}
        >
          {label}
        </span>
      </div>
    </div>
  );
}

// ── Dinner ──────────────────────────────────────────────────

function DinnerSection({
  meal,
  today,
  recipe,
  people,
  peopleError,
  isLoading,
  error,
  onRetry,
}: {
  meal: { id: string; title: string; recipe_id?: string | null; notes?: string | null } | null;
  today: string;
  recipe: RecipeRow | null;
  people: PersonRow[];
  peopleError: Error | null;
  isLoading: boolean;
  error: Error | null;
  onRetry: () => void;
}) {
  // The allergy state, read the Recipes pages' way (#4567): the REST API does
  // not return allergy_warnings/checks today, so with a family allergy saved
  // this says "Not allergy-checked" — never a tick.
  const report = useMemo(
    () =>
      reportAllergies(
        recipe ? { allergy_warnings: recipe.allergy_warnings, checks: recipe.checks } : null,
        peopleError ? null : people,
      ),
    [recipe, people, peopleError],
  );
  const facts = recipeFacts(recipe);

  return (
    <section aria-labelledby="h-dinner" data-testid="today-dinner">
      <SectionHeading id="h-dinner" title="Tonight's dinner" action={{ href: "/dashboard/meals", label: "This week" }} />
      {error ? (
        <ErrorPanel title="The meal plan didn't load" message="Tonight's dinner is unknown right now, not missing. Try again in a moment." onRetry={onRetry} />
      ) : isLoading ? (
        <SectionSkeleton rows={2} />
      ) : !meal ? (
        <Card withBorder radius="md" padding={0}>
          <CalmPanel
            icon={<IconToolsKitchen size={20} stroke={NAV_ICON_STROKE} />}
            title="No dinner planned yet"
            action={
              <Button component={Link} href="/dashboard/meals" size="xs" leftSection={<IconPlus size={16} stroke={NAV_ICON_STROKE} />}>
                Plan dinner
              </Button>
            }
          >
            Pick a family favorite or plan the whole week — missing ingredients go straight to Groceries.
          </CalmPanel>
        </Card>
      ) : (
        <Card withBorder radius="md" padding={0} className="overflow-hidden">
          <div className="flex items-start gap-3.5 p-3.5">
            <ThemeIcon variant="light" color="cyan" size={56} radius="md" aria-hidden>
              <IconToolsKitchen size={26} stroke={NAV_ICON_STROKE} />
            </ThemeIcon>
            <div className="min-w-0 flex-1">
              <b className="block text-[17px] font-semibold leading-snug text-ink">{meal.title}</b>
              <p className="mt-0.5 text-[13px] text-ink-2">
                {[facts, meal.recipe_id ? "linked to a recipe" : "no recipe, so no ingredients to add"].filter(Boolean).join(" · ")}
              </p>
              {meal.notes && <p className="mt-1 text-[13px] text-ink-3">{meal.notes}</p>}
            </div>
          </div>
          <AllergyLine report={report} peopleError={!!peopleError} />
          <div className="flex flex-wrap items-center justify-between gap-2 border-t border-line px-3.5 py-2.5 text-[13px] text-ink-2">
            <span className="min-w-0 flex-1">Ingredients for the week can go straight to Groceries.</span>
            <Button component={Link} href="/dashboard/meals/grocery" size="xs" variant="default" className="flex-none">
              Grocery list
            </Button>
          </div>
        </Card>
      )}
    </section>
  );
}

/**
 * The dinner's allergy line. "Not allergy-checked" when nobody could check —
 * an unverifiable meal must never read as safe (the manager's note on #4519:
 * with a fish allergy, most recipes are excluded, and a reassuring tick on an
 * unchecked meal is worse than no tick at all).
 */
function AllergyLine({ report, peopleError }: { report: ReturnType<typeof reportAllergies>; peopleError: boolean }) {
  if (peopleError) {
    return (
      <div className="flex items-start gap-2 border-t border-line px-3.5 py-2.5 text-[13px] text-ink-2" data-testid="dinner-allergy" data-state="unknown">
        <IconAlertTriangle size={16} stroke={NAV_ICON_STROKE} className="mt-0.5 flex-none text-[var(--warn-ink)]" aria-hidden />
        <span>
          Couldn&apos;t load the family&apos;s allergies, so dinner is <b className="font-semibold text-ink">not allergy-checked</b>.
        </span>
      </div>
    );
  }
  const checked = report.state === "checked-clear" || report.state === "warnings" || report.state === "warnings-and-unchecked";
  const warn = report.state === "warnings" || report.state === "warnings-and-unchecked" || report.state === "unchecked";
  return (
    <div
      className={[
        "flex items-start gap-2 border-t border-line px-3.5 py-2.5 text-[13px]",
        warn ? "bg-warn-bg text-warn-ink" : "text-ink-2",
      ].join(" ")}
      data-testid="dinner-allergy"
      data-state={report.state}
    >
      {checked && !warn ? (
        <IconCheck size={16} stroke={NAV_ICON_STROKE} className="mt-0.5 flex-none text-accent" aria-hidden />
      ) : (
        <IconAlertTriangle size={16} stroke={NAV_ICON_STROKE} className="mt-0.5 flex-none" aria-hidden />
      )}
      <span className="min-w-0">
        <b className="block font-semibold">{report.headline}</b>
        {report.detail ? <span className="block text-[12px] leading-snug opacity-90">{report.detail}</span> : null}
      </span>
    </div>
  );
}

// ── people ──────────────────────────────────────────────────

function PeopleSection({
  people,
  isLoading,
  error,
  onRetry,
}: {
  people: PersonRow[];
  isLoading: boolean;
  error: Error | null;
  onRetry: () => void;
}) {
  const withAllergies = people.filter((p) => (p.allergies ?? []).length > 0);
  return (
    <section aria-labelledby="h-people" data-testid="today-people">
      <SectionHeading id="h-people" title="People" action={{ href: "/dashboard/members", label: "Manage" }} />
      {error ? (
        <ErrorPanel title="The family didn't load" message="We couldn't read who is in this household just now." onRetry={onRetry} />
      ) : isLoading ? (
        <SectionSkeleton rows={2} />
      ) : people.length === 0 ? (
        <Card withBorder radius="md" padding={0}>
          <CalmPanel icon={<IconUsers size={20} stroke={NAV_ICON_STROKE} />} title="No one added yet">
            People and their allergies live here. Invite another adult and their events and lists appear on Today too.
          </CalmPanel>
        </Card>
      ) : (
        <Card withBorder radius="md" padding={0} className="overflow-hidden">
          {people.map((p) => (
            <div key={p.id} className="flex min-h-14 items-center gap-3 border-b border-line px-3.5 py-2.5 last:border-b-0">
              <span
                className="grid h-8 w-8 flex-none place-items-center rounded-full text-[13px] font-semibold text-white"
                style={{ background: `var(--mantine-color-${p.color || "cyan"}-6)` }}
                aria-hidden
              >
                {initials(p.display_name)}
              </span>
              <div className="min-w-0 flex-1">
                <b className="block truncate text-[15px] font-semibold leading-snug text-ink">{p.display_name}</b>
                <span className="block truncate text-[13px] leading-snug text-ink-3">
                  {(p.allergies ?? []).length > 0 ? `Allergic to ${(p.allergies ?? []).join(", ")}` : p.relationship || "No allergies saved"}
                </span>
              </div>
              {(p.allergies ?? []).length > 0 && (
                <Badge variant="light" color="red" size="sm" leftSection={<IconAlertTriangle size={12} stroke={NAV_ICON_STROKE} />} className="flex-none">
                  Allergy
                </Badge>
              )}
            </div>
          ))}
          <div className="border-t border-line bg-surface-2 px-3.5 py-2.5">
            <GapNote data-testid="today-gap-away">
              Who is away isn&apos;t shown: Home OS has no record of a person being away, so today it can&apos;t say that anyone is
              {withAllergies.length === 0 ? "." : ` (allergies above are saved, so allergy checks have something to work from).`}
            </GapNote>
          </div>
        </Card>
      )}
    </section>
  );
}

function initials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((p) => p[0]?.toUpperCase() ?? "")
    .join("");
}

// ── needs you (chores / open lists) ─────────────────────────

/**
 * "Needs you": the open list items ASSIGNED TO THE VIEWER, across every list
 * with something open — plus what they ticked off today (struck through, kept
 * until midnight), which is the mockup's own behaviour and the reason a parent
 * can tell "did the kids do their chores?".
 *
 * READ-ONLY: ticking an item is a write (POST /lists/{id}/items/check) and
 * Today has no write path yet, so no row draws a checkbox. The list is one tap
 * away, where it can be ticked.
 *
 * When nothing is assigned to the viewer this says exactly that — "Nothing on
 * any list is assigned to you" — rather than showing the family's items under a
 * "Needs you" heading, which would be a lie about who owes what.
 */
function NeedsYouSection({
  lists,
  today,
  people,
  viewerUserId,
}: {
  lists: ReturnType<typeof useLists>;
  today: string;
  people: PersonRow[];
  viewerUserId: string | null | undefined;
}) {
  const rows = (lists.data?.data ?? []).filter((l) => !l.archived && !l.is_template);
  const withItems = rows.filter((l) => l.open_count > 0);
  const familyOpen = rows.reduce((n, l) => n + l.open_count, 0);

  // The viewer's own person record: people carry user_id for anyone with a login.
  const myPerson = viewerUserId ? people.find((p) => p.user_id === viewerUserId) ?? null : null;
  const nameOf = (personId: string | null | undefined) => {
    if (!personId) return null;
    const p = people.find((x) => x.id === personId);
    return p ? p.display_name.split(/\s+/)[0] : null;
  };

  // Four lists is plenty for one panel, and keeps this from fanning out on a
  // family that has dozens of lists.
  const items = useListItemsFor(withItems.slice(0, 4).map((l) => l.id));

  const byList = new Map(withItems.map((l) => [l.id, l.name]));
  // An item belongs to exactly one list, but the panel fans out over several
  // reads — dedupe by id so a row can never appear twice.
  const seen = new Set<string>();
  const mine = items.items.filter((i) => {
    if (myPerson === null || i.assignee_person_id !== myPerson.id || seen.has(i.id)) return false;
    seen.add(i.id);
    return true;
  });
  const open = mine.filter((i) => !i.checked).slice(0, 4);
  const doneToday = mine.filter((i) => i.checked && i.checked_at && i.checked_at.slice(0, 10) === today).slice(0, 2);

  return (
    <section aria-labelledby="h-mine" data-testid="today-needs-you">
      <SectionHeading
        id="h-mine"
        title="Needs you"
        count={open.length > 0 ? String(open.length) : undefined}
        action={{ href: "/dashboard/lists", label: "Lists" }}
      />
      {lists.error ? (
        <ErrorPanel
          title="The lists didn't load"
          message="Chores and list items are unknown right now, not done. Try again in a moment."
          onRetry={() => lists.refetch()}
        />
      ) : lists.isLoading ? (
        <SectionSkeleton rows={3} />
      ) : (
        <Card withBorder radius="md" padding={0} className="overflow-hidden">
          {rows.length === 0 ? (
            <CalmPanel icon={<IconListCheck size={20} stroke={NAV_ICON_STROKE} />} title="No lists yet">
              Chores, groceries and errands live in shared lists. Anything assigned to you shows here each morning.
            </CalmPanel>
          ) : items.error ? (
            <div className="p-3.5">
              <p className="text-[13px] text-ink-2">
                The family has {familyOpen} open {familyOpen === 1 ? "item" : "items"}, but the list items didn&apos;t load — so this
                can&apos;t say what is yours.
              </p>
              <Button size="xs" variant="default" mt="xs" onClick={() => items.refetch()}>
                Try again
              </Button>
            </div>
          ) : items.isLoading ? (
            <div className="p-3.5">
              <Skeleton height={16} radius="sm" />
            </div>
          ) : open.length === 0 && doneToday.length === 0 ? (
            <CalmPanel icon={<IconCheck size={20} stroke={NAV_ICON_STROKE} />} title="You're all caught up">
              {myPerson
                ? "Nothing on any list is assigned to you."
                : "Nothing here is assigned to your login. Chores for someone without a login (a child) are on their list, not this panel."}
              {familyOpen > 0 ? ` The family still has ${familyOpen} open ${familyOpen === 1 ? "item" : "items"}.` : ""}
            </CalmPanel>
          ) : (
            <div data-testid="needs-you-items">
              {open.map((item) => (
                <NeedsYouRow
                  key={item.id}
                  item={item}
                  listName={byList.get(item.list_id) ?? "List"}
                  today={today}
                  assigneeName={null}
                />
              ))}
              {doneToday.map((item) => (
                <div
                  key={item.id}
                  className="flex min-h-14 items-center gap-3 border-b border-line px-3.5 py-2.5 last:border-b-0"
                  data-testid="needs-you-done"
                >
                  <span className="grid h-5 w-5 flex-none place-items-center rounded-[6px] bg-accent-fill text-white" aria-hidden>
                    <IconCheck size={13} strokeWidth={3} />
                  </span>
                  <div className="min-w-0 flex-1">
                    <b className="block text-[15px] font-medium leading-snug text-ink-3 line-through">{item.text}</b>
                    <span className="block truncate text-[13px] leading-snug text-ink-3">
                      Done today{item.checked_by_name ? ` by ${item.checked_by_name.split(/\s+/)[0]}` : ""}
                    </span>
                  </div>
                </div>
              ))}
            </div>
          )}
          {rows.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5 border-t border-line bg-surface-2 px-3.5 py-2.5 text-[13px] text-ink-2">
              {rows.slice(0, 4).map((l) => (
                <Link
                  key={l.id}
                  href="/dashboard/lists"
                  className="inline-flex min-h-8 items-center gap-1.5 rounded-full border border-line bg-surface px-2.5 font-medium text-ink"
                >
                  {l.name}
                  <span className="font-medium text-ink-3">{l.open_count}</span>
                </Link>
              ))}
            </div>
          )}
        </Card>
      )}
      <GapNote>
        Chores are list items with an assignee. Home OS has no separate chore-completion record, so &quot;done today&quot; is a list item
        ticked off today.
      </GapNote>
    </section>
  );
}

/**
 * One open item. READ-ONLY here on purpose: ticking an item is a write
 * (POST /lists/{id}/items/check), and Today has no write path yet — so this
 * row draws no checkbox at all. A box that looks tappable and does nothing is
 * worse than no box: the whole list is one tap away, where it can be ticked.
 */
function NeedsYouRow({
  item,
  listName,
  today,
  assigneeName,
}: {
  item: { id: string; text: string; due_date?: string | null };
  listName: string;
  today: string;
  /** Who else is on it; null for the viewer's own rows. */
  assigneeName: string | null;
}) {
  const late = item.due_date ? (parseCivilSafe(item.due_date) ?? 0) - (parseCivilSafe(today) ?? 0) < 0 : false;
  return (
    <div className="flex min-h-14 items-center gap-3 border-b border-line px-3.5 py-2.5 last:border-b-0" data-testid="needs-you-row">
      <div className="min-w-0 flex-1">
        <b className="block text-[15px] font-medium leading-snug text-ink">{item.text}</b>
        <span className="block truncate text-[13px] leading-snug text-ink-3">
          {assigneeName ? `${listName} · ${assigneeName}` : listName}
        </span>
      </div>
      {item.due_date && (
        <span
          className={[
            "flex-none whitespace-nowrap rounded-[10px] px-2 py-0.5 text-[12px] font-semibold",
            late ? "text-[var(--danger)] shadow-[inset_0_0_0_1px_currentColor]" : "bg-surface-2 text-ink-2",
          ].join(" ")}
        >
          {late ? "Overdue" : formatDueLabel(item.due_date.slice(0, 10), today)}
        </span>
      )}
    </div>
  );
}

function parseCivilSafe(d: string): number | null {
  const n = Date.parse(`${d.slice(0, 10)}T00:00:00Z`);
  return Number.isNaN(n) ? null : Math.round(n / 86_400_000);
}

// ── the first paint ─────────────────────────────────────────

function TodaySkeleton() {
  return (
    <div className="flex flex-col gap-6" aria-busy="true" aria-label="Loading Today" data-testid="today-skeleton">
      <div className="flex flex-col gap-3.5 lg:flex-row lg:items-end lg:justify-between">
        <div>
          <Skeleton width={230} height={26} radius="sm" />
          <Skeleton width={290} height={14} radius="sm" mt={10} />
        </div>
        <div className="flex gap-2">
          <Skeleton width={116} height={38} radius="xl" />
          <Skeleton width={128} height={38} radius="xl" />
        </div>
      </div>
      <div className="flex flex-col gap-6 lg:grid lg:grid-cols-[minmax(0,1.55fr)_minmax(0,1fr)] lg:items-start lg:gap-7">
        <SectionSkeleton rows={4} />
        <div className="flex flex-col gap-6">
          <SectionSkeleton rows={2} />
          <SectionSkeleton rows={2} />
        </div>
      </div>
    </div>
  );
}

/** `date` plus one day, YYYY-MM-DD, without pulling in a date library. */
function shiftDay(date: string): string {
  const n = Date.parse(`${date}T00:00:00Z`);
  if (Number.isNaN(n)) return date;
  return new Date(n + 86_400_000).toISOString().slice(0, 10);
}
