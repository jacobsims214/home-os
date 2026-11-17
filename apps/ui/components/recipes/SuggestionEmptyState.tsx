"use client";

/**
 * SuggestionEmptyState (#4567): explains a short or empty meal_suggest result.
 *
 * WHY THIS EXISTS. meal_suggest hard-excludes any recipe matching an allergen
 * of someone eating, and it over-matches on purpose — a broad allergen like
 * "fish" legitimately rules out most of a recipe box (the manager's note on
 * this ticket: fish excludes 11 of 12 on the sample). So ZERO suggestions is
 * a CORRECT answer, not a bug. But a blank list reads as broken, and a parent
 * who thinks the app is broken goes back to guessing at dinner — which is
 * exactly how someone gets served an allergen.
 *
 * So the empty state names the cause: which person's rule excluded which
 * recipes, from the server's own `excluded` reasons, plus any `checks` (rules
 * that could not be applied at all). The logic lives in
 * lib/recipes/suggestions.ts so the meals page (#4568) and these pages share
 * one implementation.
 *
 * This component is presentational: it renders an EmptyExplanation and never
 * decides anything about safety.
 */

import { Badge, Card, Divider, Group, List, Stack, Text, ThemeIcon } from "@mantine/core";
import { IconInfoCircle, IconShieldCheck, IconSparkles } from "@tabler/icons-react";
import type { EmptyExplanation } from "@/lib/recipes/suggestions";
import { groupLabel } from "@/lib/recipes/suggestions";

const ICON_STROKE = 1.75;

export default function SuggestionEmptyState({ explanation }: { explanation: EmptyExplanation }) {
  const { headline, body, groups, excludedHidden, checks, kind } = explanation;
  // "no-recipes" is a first-run state, not a safety outcome: no shield.
  const Icon = kind === "no-recipes" || kind === "filtered" ? IconSparkles : IconShieldCheck;

  return (
    <Card
      radius="token-md"
      p="md"
      withBorder
      data-suggestion-empty={kind}
      style={{ background: "var(--surface)" }}
    >
      <Stack gap="sm">
        <Group gap="sm" align="flex-start" wrap="nowrap">
          <ThemeIcon size={40} radius="md" variant="light" color="teal" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>
            <Icon size={22} stroke={ICON_STROKE} aria-hidden />
          </ThemeIcon>
          <div style={{ flex: 1, minWidth: 0 }}>
            <Text fw={700} size="md" data-suggestion-headline>
              {headline}
            </Text>
            <Text size="sm" mt={4} style={{ color: "var(--ink-2)" }}>
              {body}
            </Text>
          </div>
        </Group>

        {groups.length > 0 && (
          <>
            <Divider />
            <div>
              <Text size="xs" fw={700} tt="uppercase" style={{ color: "var(--ink-3)", letterSpacing: ".04em" }} mb={6}>
                What ruled them out
              </Text>
              <Stack gap={8}>
                {groups.map((g) => (
                  <div key={`${g.person}-${g.kind}-${g.rule}`} data-exclusion-group>
                    <Group gap={6} wrap="wrap">
                      <Badge
                        size="sm"
                        variant="light"
                        color={g.kind === "diet" ? "grape" : "orange"}
                        style={g.kind === "diet" ? undefined : { background: "var(--warn-bg)", color: "var(--warn-ink)" }}
                      >
                        {g.kind === "diet" ? "Diet" : g.kind === "allergy" ? "Allergy" : "Rule"}
                      </Badge>
                      <Text size="sm" fw={600}>
                        {groupLabel(g)}
                      </Text>
                      <Text size="sm" style={{ color: "var(--ink-3)" }}>
                        excluded {g.recipes.length} {g.recipes.length === 1 ? "recipe" : "recipes"}
                      </Text>
                    </Group>
                    <Text size="xs" mt={2} style={{ color: "var(--ink-3)", overflowWrap: "anywhere" }}>
                      {g.recipes.join(", ")}
                      {excludedHidden > 0 && g === groups[groups.length - 1] && excludedHidden > 0
                        ? ` and ${excludedHidden} more`
                        : ""}
                    </Text>
                  </div>
                ))}
              </Stack>
            </div>
          </>
        )}

        {/*
          `checks`: rules that could not be applied at all. Verbatim, and above
          the fold — if Home OS could not read an allergy, saying "no ideas"
          without saying why would be the silent failure #4874 closed.
        */}
        {checks.length > 0 && (
          <>
            <Divider />
            <div data-suggestion-checks>
              <Group gap={6} align="center" mb={4}>
                <IconInfoCircle size={16} stroke={ICON_STROKE} style={{ color: "var(--danger)" }} aria-hidden />
                <Text size="xs" fw={700} tt="uppercase" style={{ color: "var(--danger)", letterSpacing: ".04em" }}>
                  Couldn&rsquo;t be applied
                </Text>
              </Group>
              <List size="sm" spacing={4} icon={<span aria-hidden />} style={{ color: "var(--danger)" }}>
                {checks.map((line, i) => (
                  <List.Item key={`${i}-${line}`}>{line}</List.Item>
                ))}
              </List>
            </div>
          </>
        )}
      </Stack>
    </Card>
  );
}
