"use client";

/**
 * The allergy surface for an ALREADY-PLANNED meal (#4568).
 *
 * Manager decision on #4519 (#13039/#13046): a meal that is already on the
 * plan gets a WARN BANNER and an INLINE FLAG — never a blocking modal. Someone
 * may be cooking it right now, and a dialog in the middle of dinner is
 * hostile. The one hard confirm lives at the moment an unsafe recipe is ADDED
 * (components/recipes/RecipeForm's add-to-plan path and this page's suggestion
 * sheet), not here.
 *
 * WHAT IT CAN SAY TODAY, HONESTLY. The REST API does not return
 * `allergy_warnings` or `checks` (#4891 is the follow-up), and the UI cannot
 * call the MCP (it refuses `home-os-ui` tokens by audience, #4692). So with a
 * family allergy saved, every planned meal comes out `not-checked`, and this
 * component says exactly that. It does NOT invent a warning from ingredient
 * names — that would be a second allergen matcher, which KB mcp/recipe-tools.md
 * forbids because two lists are how the warnings and meal_suggest drifted apart
 * before #4803.
 *
 * It reuses AllergyBanner, so when the API starts sending the fields the page
 * needs no change: reportAllergies already understands every shape.
 */

import { Group, Text } from "@mantine/core";
import { IconAlertTriangle, IconShieldExclamation } from "@tabler/icons-react";
import AllergyBanner, { AllergyChip } from "@/components/recipes/AllergyBanner";
import type { AllergyReport } from "@/lib/recipes/allergy";

const ICON_STROKE = 1.75;

/** The banner itself, for the week's warning block. */
export default function PlannedMealWarning({
  report,
  mealTitle,
  dayLabel,
}: {
  report: AllergyReport;
  mealTitle: string;
  dayLabel: string;
}) {
  // Nothing to say: no allergies saved, or the server verified it clean.
  if (report.state === "none-saved" || report.state === "checked-clear") return null;
  return (
    <div data-meal-warning={report.state}>
      <AllergyBanner report={report} />
      <Text size="xs" mb="sm" style={{ color: "var(--ink-3)", marginTop: -8 }}>
        {dayLabel}: {mealTitle}. Tap the meal to swap it or pick a suggestion instead.
      </Text>
    </div>
  );
}

/**
 * The inline flag on a week row: short, one line, and it says "couldn't check"
 * rather than nothing when that is the truth. Reuses AllergyChip so a row here
 * and a row on the Recipes page say the same words.
 */
export function MealAllergyFlag({ report }: { report: AllergyReport }) {
  if (report.state === "none-saved" || report.state === "checked-clear") return null;
  const isWarn = report.warnings.length > 0;
  return (
    <Group gap={6} wrap="nowrap" align="center" data-meal-flag={report.state}>
      <IconAlertTriangle
        size={13}
        stroke={ICON_STROKE}
        style={{ color: isWarn ? "var(--danger)" : "var(--warn-ink)", flex: "none" }}
        aria-hidden
      />
      <AllergyChip report={report} />
    </Group>
  );
}

/** Re-exported for the page's types. */
export type { AllergyReport };
export { IconShieldExclamation };
