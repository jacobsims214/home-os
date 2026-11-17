"use client";

/**
 * "What's for dinner on <day>?" (#4568) — the suggestions sheet.
 *
 * Built to the approved mockup (#106 v3, the "What's for dinner?" state) and to
 * the manager decisions on #4519: unsafe recipes never appear here, one hard
 * confirm happens at the moment a recipe is ADDED, and a short or empty list is
 * EXPLAINED rather than blank.
 *
 * THE EMPTY STATE IS THE POINT. meal_suggest hard-excludes any recipe matching
 * an allergen of someone eating, and it over-matches on purpose: with a fish
 * allergy, 11 of 12 recipes on the sample household are excluded. A blank list
 * reads as broken; a parent who thinks the app is broken goes back to guessing,
 * which is how someone gets served an allergen. So this sheet renders
 * `SuggestionEmptyState` (built and tested in #4567) with the explanation
 * `explainSuggestions` produces from the server's own `excluded` reasons —
 * WHICH allergy excluded WHAT, biggest cause first, plus `excludedHidden` for
 * the reasons the server capped, plus any `checks` verbatim.
 *
 * WHERE THE DATA COMES FROM, HONESTLY. There is no REST endpoint for
 * meal_suggest today (the tool is MCP-only and the UI cannot call the MCP:
 * it refuses `home-os-ui` tokens by audience, #4692). This sheet therefore
 * renders `meal_suggest`'s EXACT result shape from `suggestions` when a caller
 * has it, and otherwise shows the family's recipe box with an explicit note
 * that suggestions are not ranked here yet. It never invents ranks, reasons or
 * exclusions — a fabricated "safe for everyone" is worse than no list.
 */

import Link from "next/link";
import { Badge, Button, Card, Divider, Drawer, Group, Skeleton, Stack, Text, Title } from "@mantine/core";
import { IconChevronRight, IconClock, IconInfoCircle, IconSparkles, IconPlus } from "@tabler/icons-react";
import SuggestionEmptyState from "@/components/recipes/SuggestionEmptyState";
import { explainSuggestions, groupLabel, type SuggestSummary } from "@/lib/recipes/suggestions";
import { reportAllergies } from "@/lib/recipes/allergy";
import { AllergyChip } from "@/components/recipes/AllergyBanner";
import type { Person, Recipe } from "@/lib/recipes/types";

const ICON_STROKE = 1.75;

export interface SuggestionSheetProps {
  opened: boolean;
  onClose: () => void;
  /** "Tuesday dinner", for the heading. */
  when: string;
  /**
   * meal_suggest's result, when the caller has one. Until a REST endpoint
   * exists this is null and the sheet shows the recipe box instead — see the
   * file header.
   */
  suggestions?: SuggestSummary | null;
  /** True while the suggestions request is in flight. */
  suggestionsLoading?: boolean;
  /** The recipe box, for the fallback list and for "plan this". */
  recipes: Recipe[];
  /**
   * The household's people, so each picker row can say whether it was checked
   * against their allergies. Passed through, never interpreted here: matching
   * is the server's (lib/recipes/allergy.ts reasons about the ANSWER only).
   */
  people: Person[] | null;
  recipesLoading?: boolean;
  recipesError?: boolean;
  onRetrySuggestions?: () => void;
  /** Plan a recipe for this date and slot. */
  onPlanRecipe: (recipe: Recipe) => void;
  planningId?: string | null;
  canWrite: boolean;
}

export default function SuggestionSheet({
  opened,
  onClose,
  when,
  suggestions,
  suggestionsLoading = false,
  recipes,
  people,
  recipesLoading = false,
  recipesError = false,
  onRetrySuggestions,
  onPlanRecipe,
  planningId,
  canWrite,
}: SuggestionSheetProps) {
  // meal_suggest returns at most 5 by default; the count is what tells "few"
  // apart from "none".
  const explanation = suggestions ? explainSuggestions(suggestions, 5) : null;
  const ranked = Array.isArray(suggestions?.suggestions) ? suggestions!.suggestions! : [];

  return (
    <Drawer
      opened={opened}
      onClose={onClose}
      position="bottom"
      size="92%"
      title={`What's for dinner on ${when}?`}
      radius="token-lg"
      data-suggestion-sheet
      styles={{
        title: { fontWeight: 600, fontSize: 17 },
        body: { paddingBottom: "calc(16px + env(safe-area-inset-bottom))" },
      }}
    >
      {/*
        The marker goes on the body, not on Drawer's root: Mantine renders the
        root as a hidden wrapper, so a test (or an a11y check) asking "is the
        sheet open" must look at the content that is actually visible.
      */}
      <Stack gap="md" data-suggestion-body>
        {/*
          The ranked list, when something actually ranked it. Each row carries
          meal_suggest's own one-line `reason`, verbatim: the reason is the
          server's explanation ("Rated 4.5/5 by Nora; not had in 4 weeks"), and
          rewording it here would make the page disagree with the assistant.
        */}
        {ranked.length > 0 && (
          <Card radius="token-md" p={0} withBorder style={{ background: "var(--surface)", overflow: "hidden" }}>
            <Stack gap={0} data-suggestion-list>
              {ranked.map((raw, i) => {
                const s = raw as {
                  recipe_id?: string;
                  name?: string;
                  reason?: string;
                  total_minutes?: number | null;
                  tags?: string[];
                };
                const recipe = recipes.find((r) => r.id === s.recipe_id);
                return (
                  <div
                    key={s.recipe_id ?? i}
                    data-suggestion-row={i}
                    style={{
                      display: "grid",
                      gridTemplateColumns: "minmax(0,1fr) auto",
                      gap: "4px 12px",
                      alignItems: "center",
                      padding: "12px 14px",
                      borderBottom: i === ranked.length - 1 ? "none" : "1px solid var(--line)",
                    }}
                  >
                    <div style={{ minWidth: 0 }}>
                      <Text fw={600} style={{ lineHeight: 1.3, overflowWrap: "anywhere" }} data-suggestion-name>
                        {s.name ?? "A recipe"}
                      </Text>
                      {s.reason && (
                        <Group gap={6} mt={2} wrap="nowrap" align="flex-start">
                          <IconSparkles size={14} stroke={ICON_STROKE} style={{ color: "var(--accent)", flex: "none", marginTop: 3 }} aria-hidden />
                          <Text size="sm" style={{ color: "var(--ink-2)", overflowWrap: "anywhere" }}>
                            {s.reason}
                          </Text>
                        </Group>
                      )}
                      <Group gap={8} mt={2} wrap="wrap">
                        {typeof s.total_minutes === "number" && (
                          <Group gap={4} wrap="nowrap">
                            <IconClock size={13} stroke={ICON_STROKE} style={{ color: "var(--ink-3)" }} aria-hidden />
                            <Text size="xs" style={{ color: "var(--ink-3)" }}>
                              {s.total_minutes} min
                            </Text>
                          </Group>
                        )}
                        {(s.tags ?? []).slice(0, 2).map((t) => (
                          <Badge key={t} size="sm" variant="light" color="gray" style={{ textTransform: "none" }}>
                            {t}
                          </Badge>
                        ))}
                      </Group>
                    </div>
                    {canWrite && recipe && (
                      <Button
                        size="compact-md"
                        onClick={() => onPlanRecipe(recipe)}
                        loading={planningId === recipe.id}
                        className="max-md:min-h-11"
                        data-suggestion-plan
                      >
                        Plan this
                      </Button>
                    )}
                  </div>
                );
              })}
            </Stack>
          </Card>
        )}

        {/*
          THE EXPLAINED EMPTY STATE. #4567 built SuggestionEmptyState and its KB
          doc asks this sheet to use it rather than render a blank list. It
          names the person + rule + which recipes each rule excluded, biggest
          cause first, plus the reasons the server capped (`excludedHidden`) and
          any `checks` verbatim.

          It appears when there is nothing to offer. When there IS something,
          the exclusions go into the collapsed "Skipped for safety" group below
          instead — the manager decision on #4519 (#13039) asks for collapsed,
          because a parent who can see tonight's options does not need the full
          explanation in the way of them, but must be able to open it.
        */}
        {explanation && ranked.length === 0 && <SuggestionEmptyState explanation={explanation} />}

        {/*
          Collapsed "Skipped for safety · N" (the approved mockup, #106 v3, and
          manager decision #13039). A <details> element, so it works without JS
          and is keyboard-operable; the summary is a 44 px target.
        */}
        {explanation && ranked.length > 0 && explanation.excludedTotal > 0 && (
          <details data-skipped-for-safety style={{ marginTop: 4 }}>
            <summary
              style={{
                display: "flex",
                alignItems: "center",
                gap: 8,
                minHeight: 44,
                fontSize: 13,
                fontWeight: 600,
                color: "var(--warn-ink)",
                cursor: "pointer",
                listStyle: "none",
              }}
            >
              <IconChevronRight
                size={16}
                stroke={ICON_STROKE}
                className="safety-caret"
                style={{ transition: "transform .15s" }}
                aria-hidden
              />
              Skipped for safety · {explanation.excludedTotal}
              {explanation.excludedHidden > 0 ? ` (${explanation.excludedHidden} not listed)` : ""}
            </summary>
            <Stack gap={6} mt={6}>
              {explanation.groups.map((g) => (
                <div
                  key={`${g.person}-${g.kind}-${g.rule}`}
                  data-exclusion-group
                  style={{
                    padding: "8px 12px",
                    border: "1px solid var(--warn-line)",
                    borderRadius: "var(--r-sm)",
                    background: "var(--warn-bg)",
                    color: "var(--warn-ink)",
                    fontSize: 13,
                  }}
                >
                  <Text size="sm" fw={700} style={{ color: "var(--warn-ink)" }}>
                    {groupLabel(g)}
                  </Text>
                  <Text size="xs" mt={2} style={{ color: "var(--warn-ink)", overflowWrap: "anywhere" }}>
                    {g.recipes.join(", ")}
                  </Text>
                </div>
              ))}
              {explanation.checks.map((c, i) => (
                <Text key={`${i}-${c}`} size="xs" style={{ color: "var(--danger)", overflowWrap: "anywhere" }} data-exclusion-check>
                  {c}
                </Text>
              ))}
            </Stack>
          </details>
        )}

        {/*
          No meal_suggest result at all. This is today's reality: there is no
          REST endpoint for it yet. Say so plainly and fall back to the recipe
          box, which is a real, useful list — never a fabricated ranking.
        */}
        {!suggestions && (
          <>
            {suggestionsLoading ? (
              <Stack gap="xs">
                {[0, 1, 2].map((i) => (
                  <Skeleton key={i} height={64} radius="md" />
                ))}
              </Stack>
            ) : (
              <Card radius="token-md" p="md" withBorder style={{ background: "var(--surface-2)" }} data-suggestions-unranked>
                <Group gap={10} align="flex-start" wrap="nowrap">
                  <IconInfoCircle size={20} stroke={ICON_STROKE} style={{ color: "var(--accent)", flex: "none", marginTop: 1 }} aria-hidden />
                  <div>
                    <Text fw={600} size="sm">
                      Suggestions aren&apos;t ranked here yet
                    </Text>
                    <Text size="sm" mt={2} style={{ color: "var(--ink-2)" }}>
                      Home OS can rank dinners by ratings, recent repeats and allergies, but that ranking only runs
                      through the assistant today. Until the app can ask it directly, pick from your recipes — the
                      allergy flag on each one says whether it has been checked.
                    </Text>
                  </div>
                </Group>
              </Card>
            )}

            <Divider label="Your recipes" labelPosition="left" />

            {recipesLoading && (
              <Stack gap="xs">
                {[0, 1, 2].map((i) => (
                  <Skeleton key={i} height={56} radius="md" />
                ))}
              </Stack>
            )}

            {recipesError && !recipesLoading && (
              <Card radius="token-md" p="md" withBorder role="alert" style={{ borderLeft: "4px solid var(--danger)" }}>
                <Text fw={600} size="sm">
                  Your recipes didn&apos;t load
                </Text>
                <Text size="sm" mt={2} style={{ color: "var(--ink-2)" }}>
                  Nothing was lost — this is only the list of ideas for tonight.
                </Text>
                {onRetrySuggestions && (
                  <Button variant="default" mt="sm" onClick={onRetrySuggestions} className="max-md:min-h-11">
                    Try again
                  </Button>
                )}
              </Card>
            )}

            {!recipesLoading && !recipesError && recipes.length === 0 && (
              <div className="flex flex-col items-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-8 text-center">
                <Text size="sm" style={{ color: "var(--ink-3)" }}>
                  No recipes yet. Add a few dinners your family cooks and there is something to pick from.
                </Text>
                {canWrite && (
                  <Button
                    component={Link}
                    href="/dashboard/recipes"
                    variant="light"
                    leftSection={<IconPlus size={16} stroke={ICON_STROKE} />}
                    className="max-md:min-h-11"
                  >
                    Add a recipe
                  </Button>
                )}
              </div>
            )}

            {!recipesLoading && !recipesError && recipes.length > 0 && (
              <Card radius="token-md" p={0} withBorder style={{ background: "var(--surface)", overflow: "hidden" }}>
                <Stack gap={0} data-recipe-picker>
                  {recipes.slice(0, 12).map((r, i) => (
                    <Group
                      key={r.id}
                      gap="sm"
                      wrap="nowrap"
                      align="center"
                      style={{ padding: "10px 14px", borderBottom: i === Math.min(recipes.length, 12) - 1 ? "none" : "1px solid var(--line)" }}
                    >
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <Text fw={600} size="sm" style={{ overflowWrap: "anywhere", lineHeight: 1.3 }} data-recipe-picker-title>
                          {r.title}
                        </Text>
                        <Text size="xs" style={{ color: "var(--ink-3)" }}>
                          {(r.ingredients?.length ?? 0)} ingredient{(r.ingredients?.length ?? 0) === 1 ? "" : "s"}
                          {r.servings != null ? ` · serves ${r.servings}` : ""}
                        </Text>
                        {/*
                          The same allergy flag the recipe rows carry. It matters
                          most here: this is the moment a recipe is ADDED, and
                          picking blind is how an allergen gets planned. Today the
                          API sends no allergy data, so this reads "Not
                          allergy-checked" — honest, and not a blank space.
                        */}
                        <PickerAllergyFlag recipe={r} people={people} />
                      </div>
                      {canWrite ? (
                        <Button
                          variant="light"
                          size="compact-md"
                          onClick={() => onPlanRecipe(r)}
                          loading={planningId === r.id}
                          className="max-md:min-h-11"
                          data-recipe-picker-plan
                        >
                          Plan
                        </Button>
                      ) : (
                        <IconChevronRight size={18} stroke={ICON_STROKE} style={{ color: "var(--ink-3)" }} aria-hidden />
                      )}
                    </Group>
                  ))}
                </Stack>
              </Card>
            )}

            <Button
              component={Link}
              href="/dashboard/recipes"
              variant="subtle"
              rightSection={<IconChevronRight size={16} stroke={ICON_STROKE} />}
              className="max-md:min-h-11"
            >
              All recipes
            </Button>
          </>
        )}

        {canWrite && (
          <Button
            component={Link}
            href="/dashboard/recipes"
            variant="default"
            leftSection={<IconPlus size={16} stroke={ICON_STROKE} />}
            className="max-md:min-h-11"
          >
            Add a recipe
          </Button>
        )}
      </Stack>
    </Drawer>
  );
}

/** The inline allergy flag on a picker row. */
function PickerAllergyFlag({ recipe, people }: { recipe: Recipe; people: Person[] | null }) {
  const report = reportAllergies(recipe, people);
  if (report.state === "none-saved" || report.state === "checked-clear") return null;
  return (
    <div style={{ marginTop: 4 }} data-recipe-picker-flag={report.state}>
      <AllergyChip report={report} />
    </div>
  );
}

/** Re-exported so the page and its tests can name the type without an import chain. */
export type { SuggestSummary };
