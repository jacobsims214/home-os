"use client";

/**
 * Meals (#4568): the week plan, dinner suggestions, rating a meal, and
 * "make the grocery list from this week".
 *
 * Built to the approved mockup (artifact #106 v3, the "Week plan" state) inside
 * the approved shell (#75 v1), plus the manager decisions recorded on #4519:
 *
 *  - ALLERGY SURFACING (#13039): a planned meal gets a warn banner and an
 *    inline flag — never a modal mid-cook. The one hard confirm is at the
 *    moment an unsafe recipe is ADDED, and that lives on the recipe pages and
 *    the suggestion sheet. Here, an already-planned meal is only ever flagged.
 *  - GROCERY (#13046): a full preview page before writing, with Add / On the
 *    list / Check amount per item, and an idempotent write. The preview is
 *    /dashboard/meals/grocery (components/meals/GroceryPreview).
 *
 * WHAT THIS PAGE CAN SAY ABOUT ALLERGIES TODAY, HONESTLY. The REST API does not
 * return allergy_warnings or checks yet (#4891 is the follow-up ticket), and the
 * UI cannot call the MCP (it refuses home-os-ui tokens by audience, #4692). So
 * with a family allergy saved, reportAllergies answers `not-checked` and every
 * planned meal reads "Not allergy-checked". That is the truth, and it is far
 * better than a blank flag that reads as "safe". The moment the API sends the
 * fields, nothing here changes: the report already understands every shape.
 *
 * The same honesty applies to suggestions: meal_suggest is MCP-only today, so
 * the sheet shows the recipe box and says rankings are not available here yet
 * rather than inventing them. SuggestionEmptyState is wired to the real
 * meal_suggest result shape and is proven by the e2e spec, so it lights up the
 * day an API endpoint exists.
 *
 * Roles: viewer and housesitter read the plan (a sitter cooks from it), and
 * allergies are safety data they must see. Writing the plan, rating for others
 * and adding to Groceries are gated.
 */

import { useMemo, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useHouseholdToday } from "@/lib/today/api";
import {
  Badge,
  Button,
  Card,
  Group,
  Skeleton,
  Stack,
  Text,
  Title,
  UnstyledButton,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconChevronLeft,
  IconChevronRight,
  IconClock,
  IconPlus,
  IconShoppingCart,
  IconSparkles,
  IconStar,
  IconTrash,
} from "@tabler/icons-react";
import { useClearMeal, useMealSuggestions, usePeople, usePlanMeal, useRecipes, useMealPlans } from "@/lib/recipes/api";
import { reportAllergies } from "@/lib/recipes/allergy";
import { useNavRole } from "@/components/shared/use-nav-role";
import SuggestionSheet from "@/components/meals/SuggestionSheet";
import PlannedMealWarning, { MealAllergyFlag } from "@/components/meals/PlannedMealWarning";
import RateMealSheet from "@/components/meals/RateMealSheet";
import {
  addDays,
  addWeek,
  buildWeek,
  dinnerRating,
  firstEmptyDay,
  firstUpcomingDay,
  mealAllergyPayload,
  plannedDinners,
  weekHeading,
  weekLabel,
  weekStart,
  type WeekDay,
} from "@/lib/recipes/week";
import type { Meal, Person, Recipe } from "@/lib/recipes/types";

const ICON_STROKE = 1.75;
/** Family roles that may change the plan (mirrors the API's meals policy). */
const WRITER_ROLES = new Set(["owner", "family_manager", "family_member"]);
/** Only these may rate for someone else: kids have no login. */
const RATER_OTHERS = new Set(["owner", "family_manager"]);

export default function MealsPage() {
  const role = useNavRole();
  const canWrite = role == null ? true : WRITER_ROLES.has(role);
  const canRateOthers = role == null ? true : RATER_OTHERS.has(role);

  // `?week=YYYY-MM-DD` (any date in the week) comes from the recipe page's
  // "Plan it", so a recipe planned from there lands on a visible week.
  const params = useSearchParams();
  const requestedWeek = params?.get("week") ?? "";

  // Today is the FAMILY's day (#5316), in the household's time zone, after
  // mount: the server's clock (and its zone) is not the family's, and a browser
  // on UTC at 22:30 in New York is already on tomorrow. It is `null` until the
  // household is known, so nothing below calls a day past, or asks anyone to
  // plan one, on a guess — and no row is highlighted "today" on the wrong day.
  // `householdToday` is the same answer Today, Calendar, Pets, People and Bills
  // use, so no two pages can disagree about what day it is.
  //
  // The panel's week: a `?week=` link wins, otherwise the family's week. With
  // no day yet there is no week to draw (`start` is null) — the alternative,
  // a browser-clock placeholder, is the defect this consolidation removed.
  const today = useHouseholdToday();
  const [weekOffset, setWeekOffset] = useState(0);
  const start = useMemo(() => {
    const anchor = requestedWeek || today;
    if (!anchor) return null;
    return addWeek(weekStart(anchor), weekOffset);
  }, [requestedWeek, today, weekOffset]);

  const range = useMemo(() => (start ? { from: start, to: addDays(start, 6) } : null), [start]);
  const mealsQuery = useMealPlans(range ?? { from: "", to: "" }, !!range);
  const peopleQuery = usePeople();
  const recipesQuery = useRecipes();

  // Memoised on the query payloads, not on the `?? []` fallbacks: a fresh empty
  // array every render would rebuild the week (and the recipe index) on every
  // parent render, which is what the eslint hook warns about.
  const meals = useMemo(() => mealsQuery.data?.data ?? [], [mealsQuery.data]);
  const people = peopleQuery.data?.data ?? null;
  const recipes = useMemo(() => recipesQuery.data?.data ?? [], [recipesQuery.data]);
  const recipesById = useMemo(() => new Map(recipes.map((r) => [r.id, r])), [recipes]);

  const days = useMemo(
    () => (start && today ? buildWeek(start, meals, today) : []),
    [start, meals, today],
  );
  const planned = plannedDinners(days);
  // Only a day that is today or later may be called open (#5305).
  const nextEmpty = today ? firstEmptyDay(days, today) : null;
  // Some day of this week to open the suggestion sheet on when no dinner is
  // open: the first that hasn't gone, never `days[0]` (a gone Monday).
  const upcomingDay = today ? firstUpcomingDay(days, today) : null;
  // When the whole visible week is in the past (someone browsed back with ‹),
  // "What's for dinner?" still means TONIGHT, so the sheet opens on today
  // rather than on the week's Monday. Its own row, built from the same data, so
  // the sheet's heading and the write both name a real, still-cookable day.
  const todayRow = useMemo(
    () => (today ? buildWeek(weekStart(today), meals, today).find((d) => d.date === today) ?? null : null),
    [today, meals],
  );
  const suggestionDay = nextEmpty ?? upcomingDay ?? todayRow ?? days[0];
  // Nothing in this week can still be cooked, so the page must not offer to:
  // no nag, and no "Plan a dinner" instruction on a week that is over.
  const weekIsPast = days.length > 0 && days.every((d) => d.isPast);

  const plan = usePlanMeal();
  const clear = useClearMeal();

  const [suggestFor, setSuggestFor] = useState<WeekDay | null>(null);

  // meal_suggest for the night the sheet is open on. The REST endpoint does not
  // exist yet, so this answers 404 and the sheet says "not ranked here yet" and
  // falls back to the recipe box (lib/recipes/api.ts explains why it asks).
  const suggestionsQuery = useMealSuggestions(suggestFor ? { date: suggestFor.date, slot: "dinner" } : null);
  const [rateMeal, setRateMeal] = useState<{ meal: Meal; day: WeekDay } | null>(null);
  const [planningId, setPlanningId] = useState<string | null>(null);

  // The current person, for a family_member who may only rate as themselves.
  // The people list is ordered as the API returns it; matching by the
  // signed-in user is not possible from this page (the roster has no email),
  // so a member rating is offered only when there is exactly one login person
  // to rate for. Anything else would be guessing whose row is whose.
  const selfPersonId = useMemo(() => {
    const logins = (people ?? []).filter((p: Person) => p.has_login);
    return logins.length === 1 ? logins[0].id : null;
  }, [people]);

  const planRecipe = async (recipe: Recipe, day: WeekDay) => {
    if (!canWrite) return;
    setPlanningId(recipe.id);
    try {
      await plan.mutateAsync({ date: day.date, slot: "dinner", recipeId: recipe.id });
      notifications.show({ message: `${recipe.title} planned for ${day.dow} ${day.dayNum} dinner.`, color: "teal" });
      setSuggestFor(null);
    } catch {
      notifications.show({ message: "Could not plan that dinner. Try again in a moment.", color: "red" });
    } finally {
      setPlanningId(null);
    }
  };

  const clearDinner = async (day: WeekDay) => {
    if (!canWrite || !day.dinner) return;
    try {
      await clear.mutateAsync({ date: day.date, slot: "dinner" });
      notifications.show({ message: `${day.dow}'s dinner cleared.`, color: "teal" });
    } catch {
      notifications.show({ message: "Could not clear that dinner.", color: "red" });
    }
  };

  const isLoading = mealsQuery.isLoading;
  const isError = mealsQuery.isError;

  // The warning block: any planned dinner whose allergies cannot be verified
  // (or that carries a real match, once the API sends one).
  const warnings = days
    .map((day) => {
      if (!day.dinner) return null;
      const report = reportAllergies(mealAllergyPayload(day.dinner, recipesById), people);
      if (report.state === "none-saved" || report.state === "checked-clear") return null;
      return { day, report };
    })
    .filter((w): w is { day: WeekDay; report: ReturnType<typeof reportAllergies> } => w !== null);

  return (
    <div className="mx-auto w-full max-w-6xl">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3 md:mb-6">
        <div className="min-w-0">
          <Title order={2} className="text-xl font-semibold text-ink md:text-2xl">
            Meals
          </Title>
          <Text size="sm" mt={4} style={{ color: "var(--ink-3)" }}>
            The week&apos;s dinners in one place. What&apos;s missing goes to Groceries in one tap.
          </Text>
        </div>
        <Group gap={8}>
          <Button
            component={Link}
            href="/dashboard/recipes"
            variant="default"
            className="max-md:min-h-11"
            data-meals-recipes
          >
            Recipes
          </Button>
          {canWrite && (
            <Button
              onClick={() => setSuggestFor(suggestionDay)}
              leftSection={<IconSparkles size={16} stroke={ICON_STROKE} />}
              className="max-md:min-h-11"
              data-meals-suggest
            >
              What&apos;s for dinner?
            </Button>
          )}
        </Group>
      </div>

      {/* Week navigation: arrows plus the range, like the mockup. */}
      <Group gap={4} mb="xs" wrap="nowrap" align="center" data-meals-weeknav>
        <Button
          variant="subtle"
          size="compact-md"
          onClick={() => setWeekOffset((o) => o - 1)}
          aria-label="Previous week"
          className="max-md:min-h-11 max-md:min-w-11"
          px={8}
        >
          <IconChevronLeft size={20} stroke={ICON_STROKE} aria-hidden />
        </Button>
        <div style={{ flex: 1, minWidth: 0 }}>
          <Text fw={600} size="lg" style={{ lineHeight: 1.2 }} data-meals-weekday>
            {start && today ? weekHeading(start, today) : "This week"}
          </Text>
          <Text size="xs" style={{ color: "var(--ink-3)" }} data-meals-weekrange>
            {start ? weekLabel(start) : "\u2014"} \u00b7 {planned} {planned === 1 ? "dinner" : "dinners"} planned
          </Text>
        </div>
        <Button
          variant="subtle"
          size="compact-md"
          onClick={() => setWeekOffset((o) => o + 1)}
          aria-label="Next week"
          className="max-md:min-h-11 max-md:min-w-11"
          px={8}
        >
          <IconChevronRight size={20} stroke={ICON_STROKE} aria-hidden />
        </Button>
      </Group>

      {isLoading && (
        <Card radius="token-md" p={0} withBorder style={{ background: "var(--surface)", overflow: "hidden" }} aria-busy="true">
          <Stack gap={0}>
            {[0, 1, 2, 3, 4, 5, 6].map((i) => (
              <div key={i} style={{ display: "flex", gap: 12, padding: "10px 12px", borderBottom: i === 6 ? "none" : "1px solid var(--line)" }}>
                <Skeleton height={40} width={40} radius="sm" />
                <Skeleton height={52} radius="md" style={{ flex: 1 }} />
              </div>
            ))}
          </Stack>
        </Card>
      )}

      {isError && !isLoading && (
        <Card radius="token-md" p="md" withBorder role="alert" style={{ borderLeft: "4px solid var(--danger)" }}>
          <Text fw={600}>The meal plan didn&apos;t load</Text>
          <Text size="sm" mt={4} style={{ color: "var(--ink-2)" }}>
            Home OS didn&apos;t answer. Your plan and recipes are safe — nothing was lost.
          </Text>
          <Button variant="default" mt="sm" onClick={() => mealsQuery.refetch()} className="max-md:min-h-11">
            Try again
          </Button>
        </Card>
      )}

      {!isLoading && !isError && (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1.5fr)_minmax(0,1fr)]">
          <div style={{ minWidth: 0 }}>
            <Card
              radius="token-md"
              p={0}
              withBorder
              style={{ background: "var(--surface)", overflow: "hidden" }}
              data-meals-week
            >
              <div role="list" aria-label="Dinners this week">
                {days.map((day, i) => (
                  <DayRow
                    key={day.date}
                    day={day}
                    people={people}
                    recipesById={recipesById}
                    canWrite={canWrite}
                    onSuggest={() => setSuggestFor(day)}
                    onRate={() => day.dinner && setRateMeal({ meal: day.dinner, day })}
                    onClear={() => clearDinner(day)}
                    isLast={i === days.length - 1}
                  />
                ))}
              </div>
            </Card>

            <Card radius="token-md" p="sm" withBorder mt="md" style={{ background: "var(--surface-2)" }}>
              <Group gap={10} align="flex-start" wrap="nowrap">
                <IconSparkles size={18} stroke={ICON_STROKE} style={{ color: "var(--accent)", flex: "none", marginTop: 2 }} aria-hidden />
                <Text size="sm" style={{ color: "var(--ink-2)" }}>
                  Ask the assistant: <em>what&apos;s for dinner tomorrow</em>, <em>plan tacos on Thursday</em> or{" "}
                  <em>put this week&apos;s dinners on the grocery list</em>.
                </Text>
              </Group>
            </Card>
          </div>

          <div style={{ minWidth: 0, display: "flex", flexDirection: "column", gap: 16 }}>
            {/*
              The allergy block for meals that are already planned: a banner,
              never a modal (manager decision #13039). With no allergy data on
              the REST API yet this reads "Not allergy-checked", which is the
              truthful answer — and the reason it is not a blank space.
            */}
            {warnings.length > 0 && (
              <section aria-label="Allergy warnings for planned meals" data-meals-allergy>
                <PlannedMealWarning
                  report={warnings[0].report}
                  mealTitle={warnings[0].day.dinner?.title ?? ""}
                  dayLabel={`${warnings[0].day.dow} ${warnings[0].day.dayNum}`}
                />
                {warnings.length > 1 && (
                  <Text size="xs" style={{ color: "var(--ink-3)" }}>
                    {warnings.length} of {planned} planned dinners have something to check.
                  </Text>
                )}
              </section>
            )}

            {/* Groceries: the way into the preview page. */}
            <section aria-label="Groceries">
              <Card radius="token-md" p="md" withBorder style={{ background: "var(--surface)" }} data-meals-grocery>
                <Group gap={12} align="center" wrap="nowrap">
                  <div
                    aria-hidden
                    style={{
                      width: 40,
                      height: 40,
                      flex: "none",
                      borderRadius: "var(--r-md)",
                      display: "grid",
                      placeItems: "center",
                      background: "var(--accent-soft)",
                      color: "var(--accent)",
                    }}
                  >
                    <IconShoppingCart size={20} stroke={ICON_STROKE} />
                  </div>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <Text fw={600} size="sm">
                      Get ready for the week
                    </Text>
                    <Text size="xs" style={{ color: "var(--ink-3)" }}>
                      {planned === 0
                        ? weekIsPast
                          ? "No dinner was planned this week."
                          : "Plan a dinner and its ingredients can go on Groceries."
                        : `Merge ${planned} planned ${planned === 1 ? "dinner" : "dinners"} into Groceries.`}
                    </Text>
                  </div>
                </Group>
                <Button
                  component={Link}
                  href={range ? `/dashboard/meals/grocery?from=${range.from}&to=${range.to}` : "/dashboard/meals/grocery"}
                  variant="default"
                  fullWidth
                  mt="sm"
                  leftSection={<IconPlus size={16} stroke={ICON_STROKE} />}
                  className="max-md:min-h-11"
                  data-meals-grocery-go
                >
                  {canWrite ? "Add to Groceries" : "See what would be added"}
                </Button>
              </Card>
            </section>

            {nextEmpty && canWrite && (
              <section aria-label="Next empty dinner">
                <Card radius="token-md" p="md" withBorder style={{ background: "var(--surface)" }}>
                  <Text fw={600} size="sm">
                    {nextEmpty.dow} is open
                  </Text>
                  <Text size="xs" mb="xs" style={{ color: "var(--ink-3)" }}>
                    {nextEmpty.long} has no dinner planned.
                  </Text>
                  <Button
                    fullWidth
                    variant="light"
                    onClick={() => setSuggestFor(nextEmpty)}
                    leftSection={<IconSparkles size={16} stroke={ICON_STROKE} />}
                    className="max-md:min-h-11"
                  >
                    Plan {nextEmpty.dow}&apos;s dinner
                  </Button>
                </Card>
              </section>
            )}
          </div>
        </div>
      )}

      <SuggestionSheet
        opened={suggestFor !== null}
        onClose={() => setSuggestFor(null)}
        when={suggestFor ? `${suggestFor.dow} ${suggestFor.long}` : ""}
        // meal_suggest's result when the API has an endpoint for it; null when
        // it does not (404 maps to null), which is today. The sheet then shows
        // the recipe box and says rankings are not available here — it never
        // invents ranks, reasons or exclusions.
        suggestions={suggestionsQuery.data?.data ?? null}
        suggestionsLoading={suggestionsQuery.isLoading}
        people={people}
        recipes={recipes}
        recipesLoading={recipesQuery.isLoading}
        recipesError={recipesQuery.isError}
        onRetrySuggestions={() => {
          void suggestionsQuery.refetch();
          void recipesQuery.refetch();
        }}
        onPlanRecipe={(r) => suggestFor && planRecipe(r, suggestFor)}
        planningId={planningId}
        canWrite={canWrite}
      />

      <RateMealSheet
        opened={rateMeal !== null}
        onClose={() => setRateMeal(null)}
        meal={rateMeal?.meal ?? null}
        dayLabel={rateMeal ? `${rateMeal.day.dow}, ${rateMeal.day.long}` : ""}
        people={people ?? []}
        canRateOthers={canRateOthers}
        selfPersonId={canRateOthers ? null : selfPersonId}
      />
    </div>
  );
}

/** One day of the week: the date, the dinner, and what can be done with it. */
function DayRow({
  day,
  people,
  recipesById,
  canWrite,
  onSuggest,
  onRate,
  onClear,
  isLast,
}: {
  day: WeekDay;
  people: Person[] | null;
  recipesById: ReadonlyMap<string, Recipe>;
  canWrite: boolean;
  onSuggest: () => void;
  onRate: () => void;
  onClear: () => void;
  isLast: boolean;
}) {
  const dinner = day.dinner;
  const report = reportAllergies(mealAllergyPayload(dinner, recipesById), people);
  const rating = dinnerRating(dinner);
  const recipe = dinner?.recipe_id ? recipesById.get(dinner.recipe_id) : undefined;
  const minutes = recipe ? (recipe.prep_minutes ?? 0) + (recipe.cook_minutes ?? 0) : 0;

  return (
    <div
      role="listitem"
      data-meal-day={day.date}
      data-meal-today={day.isToday ? "true" : undefined}
      data-meal-past={day.isPast ? "true" : undefined}
      style={{
        display: "grid",
        gridTemplateColumns: "44px minmax(0,1fr)",
        gap: 12,
        padding: "10px 12px",
        borderBottom: isLast ? "none" : "1px solid var(--line)",
        background: day.isToday ? "var(--surface-2)" : "transparent",
        // A gone day is history: it stays readable (it is what the family ate)
        // but stops competing with the days that can still be planned. It used
        // to be dimmed with opacity 0.72, which dropped its 12 px labels to
        // 3.05:1 and 3.93:1 on white — under WCAG AA's 4.5:1 (#4588). The
        // de-emphasis is now the token colours the row already carries
        // (--ink-2 / --ink-3), which pass on their own.
      }}
    >
      <div style={{ paddingTop: 4 }} aria-label={day.isToday ? `Today, ${day.long}` : day.long}>
        <Text
          size="xs"
          fw={600}
          style={{ color: day.isToday ? "var(--accent)" : "var(--ink-2)", lineHeight: 1.2 }}
          data-meal-dow
        >
          {day.dow}
        </Text>
        <Text
          fw={600}
          style={{
            fontSize: 22,
            letterSpacing: "-.02em",
            lineHeight: 1.1,
            color: day.isToday ? "#fff" : "var(--ink)",
            background: day.isToday ? "var(--accent-fill)" : "transparent",
            display: "inline-grid",
            placeItems: "center",
            width: day.isToday ? 34 : "auto",
            height: day.isToday ? 34 : "auto",
            borderRadius: day.isToday ? "50%" : 0,
          }}
        >
          {day.dayNum}
        </Text>
      </div>

      <div style={{ minWidth: 0 }}>
        {dinner ? (
          <div
            data-meal-planned={dinner.id}
            style={{
              border: day.isToday ? "1px solid var(--accent)" : "1px solid var(--line)",
              borderLeft: day.isToday ? "4px solid var(--accent)" : "1px solid var(--line)",
              borderRadius: "var(--r-md)",
              background: day.isToday ? "var(--surface)" : "var(--surface-2)",
              padding: "8px 10px",
              display: "flex",
              flexDirection: "column",
              gap: 3,
            }}
          >
            <Text fw={600} style={{ fontSize: 15, lineHeight: 1.3, overflowWrap: "anywhere" }} data-meal-title>
              {dinner.title}
            </Text>

            <Group gap={8} wrap="wrap">
              {minutes > 0 && (
                <Group gap={4} wrap="nowrap">
                  <IconClock size={13} stroke={ICON_STROKE} style={{ color: "var(--ink-3)" }} aria-hidden />
                  <Text size="xs" style={{ color: "var(--ink-3)" }}>
                    {minutes} min
                  </Text>
                </Group>
              )}
              {rating && (
                <Group gap={3} wrap="nowrap">
                  <IconStar size={13} stroke={ICON_STROKE} style={{ color: "var(--accent)", fill: "var(--accent)" }} aria-hidden />
                  <Text size="xs" style={{ color: "var(--ink-3)" }}>
                    {rating.average.toFixed(1)} · {rating.count} {rating.count === 1 ? "rating" : "ratings"}
                  </Text>
                </Group>
              )}
              {dinner.recipe_id ? (
                <Badge
                  component={Link}
                  href={`/dashboard/recipes/${dinner.recipe_id}`}
                  size="sm"
                  variant="light"
                  color="gray"
                  style={{ textTransform: "none", textDecoration: "none" }}
                >
                  Recipe
                </Badge>
              ) : (
                <Text size="xs" style={{ color: "var(--ink-3)" }}>
                  Free text — no ingredients to add
                </Text>
              )}
            </Group>

            {/* The inline flag: never a modal, never silent (decision #13039). */}
            <MealAllergyFlag report={report} />

            {dinner.notes && (
              <Text size="xs" style={{ color: "var(--ink-3)", overflowWrap: "anywhere" }}>
                {dinner.notes}
              </Text>
            )}

            <Group gap={4} mt={2} wrap="wrap">
              <Button variant="subtle" size="compact-xs" onClick={onRate} className="max-md:min-h-11" data-meal-rate>
                Rate it
              </Button>
              {/* Rating is an after-the-fact act and stays on a gone day; swapping
                  or clearing a dinner already eaten is not an offer to make. */}
              {canWrite && !day.isPast && (
                <>
                  <Button variant="subtle" size="compact-xs" onClick={onSuggest} className="max-md:min-h-11" data-meal-swap>
                    Swap
                  </Button>
                  <Button
                    variant="subtle"
                    size="compact-xs"
                    color="red"
                    onClick={onClear}
                    leftSection={<IconTrash size={14} stroke={ICON_STROKE} />}
                    className="max-md:min-h-11"
                    data-meal-clear
                    aria-label={`Clear ${day.dow}'s dinner`}
                  >
                    Clear
                  </Button>
                </>
              )}
            </Group>
          </div>
        ) : day.isPast ? (
          /*
            A past day with no dinner is SETTLED, not open (#5305, R2 G8): the
            old row offered "Plan dinner" for Monday on Wednesday, and the side
            panel led with "Mon is open — November 16 has no dinner planned".
            Nobody can cook a dinner for a day that has gone, so this says what
            happened and offers nothing. It is not a disabled button: a dead
            control still reads as an action that should work.
          */
          <div
            data-meal-settled={day.date}
            style={{
              minHeight: 52,
              display: "flex",
              alignItems: "center",
              padding: "0 12px",
              borderRadius: "var(--r-md)",
              border: "1px solid var(--line)",
              background: "transparent",
            }}
          >
            <Text size="xs" style={{ color: "var(--ink-3)" }}>
              No dinner planned
            </Text>
          </div>
        ) : (
          <UnstyledButton
            onClick={canWrite ? onSuggest : undefined}
            disabled={!canWrite}
            data-meal-empty={day.date}
            aria-label={canWrite ? `Plan dinner for ${day.long}` : `${day.long} has no dinner planned`}
            style={{
              width: "100%",
              minHeight: 52,
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              gap: 6,
              border: "1.5px dashed var(--line)",
              borderRadius: "var(--r-md)",
              fontSize: 13,
              fontWeight: 500,
              color: "var(--ink-3)",
              background: "transparent",
              cursor: canWrite ? "pointer" : "default",
            }}
          >
            {canWrite && <IconPlus size={16} stroke={ICON_STROKE} aria-hidden />}
            {canWrite ? "Plan dinner" : "Nothing planned"}
          </UnstyledButton>
        )}

        {day.otherSlots.length > 0 && (
          <Text size="xs" mt={4} style={{ color: "var(--ink-3)" }} data-meal-otherslots>
            {day.otherSlots.length} more {day.otherSlots.length === 1 ? "meal" : "meals"} today:{" "}
            {day.otherSlots.map((m) => m.title).join(", ")}
          </Text>
        )}
      </div>
    </div>
  );
}
