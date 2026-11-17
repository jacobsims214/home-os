"use client";

/**
 * Recipes (#4567): the family's recipe box.
 *
 * Built to the approved mockup (#106 v3, "Recipes" state): a search box, tag
 * chips, and a list of recipe rows with time, times made and the allergy flag.
 * The long-title case is deliberate — a grandma's dictated recipe name has to
 * wrap cleanly rather than push the row sideways.
 *
 * Phones: the list is cards (one 44 px+ tap target each) and nothing clips;
 * from 768 px it stays cards but wider, and the page is capped at 1120 px like
 * the rest of the shell. The switch is CSS-only, so there is no SSR mismatch.
 *
 * The allergy flag per row comes from lib/recipes/allergy.ts: a row shows a
 * warning when the server warned, and "not allergy-checked" when the family
 * has allergies saved but the payload carried no allergy data. A row never
 * silently looks safe.
 *
 * Roles: viewer and housesitter see this read-only (no Add button, no Edit) —
 * allergies are safety data and stay visible to them. vendor has no Meals
 * section at all (nav-config), and the API refuses them anyway.
 */

import { useMemo, useState } from "react";
import Link from "next/link";
import { Badge, Button, Card, Chip, Group, Skeleton, Stack, Text, TextInput, Title } from "@mantine/core";
import { IconChevronRight, IconClock, IconPlus, IconSearch, IconToolsKitchen } from "@tabler/icons-react";
import { usePeople, useRecipes } from "@/lib/recipes/api";
import { reportAllergies } from "@/lib/recipes/allergy";
import { AllergyChip } from "@/components/recipes/AllergyBanner";
import RecipeForm from "@/components/recipes/RecipeForm";
import { useNavRole } from "@/components/shared/use-nav-role";
import type { Person, Recipe } from "@/lib/recipes/types";

const ICON_STROKE = 1.75;

/** Family roles that may write recipes (mirrors the API's meals policy). */
const WRITER_ROLES = new Set(["owner", "family_manager", "family_member"]);

function totalMinutes(recipe: Recipe): number | null {
  const prep = recipe.prep_minutes ?? 0;
  const cook = recipe.cook_minutes ?? 0;
  const total = prep + cook;
  return total > 0 ? total : null;
}

function RecipeRow({ recipe, people }: { recipe: Recipe; people: Person[] | null }) {
  // The real people list, so the row's flag can name who and what — never a
  // stand-in, because a fabricated person would print a made-up allergy.
  const report = useMemo(() => reportAllergies(recipe, people), [recipe, people]);
  const minutes = totalMinutes(recipe);
  return (
    <Card
      component={Link}
      href={`/dashboard/recipes/${recipe.id}`}
      data-recipe-card={recipe.id}
      radius="token-md"
      p="md"
      withBorder
      style={{ display: "block", minHeight: 44, background: "var(--surface)", textDecoration: "none" }}
    >
      <Group gap="sm" wrap="nowrap" align="flex-start">
        <div
          aria-hidden
          style={{
            display: "grid",
            placeItems: "center",
            width: 44,
            height: 44,
            flex: "none",
            borderRadius: "var(--r-md)",
            background: "var(--accent-soft)",
            color: "var(--accent)",
          }}
        >
          <IconToolsKitchen size={22} stroke={ICON_STROKE} />
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <Text fw={600} size="md" style={{ overflowWrap: "anywhere", lineHeight: 1.3 }} data-recipe-title>
            {recipe.title}
          </Text>
          <Group gap={10} mt={4} wrap="wrap">
            {minutes !== null && (
              <Group gap={4} wrap="nowrap">
                <IconClock size={13} stroke={ICON_STROKE} style={{ color: "var(--ink-3)" }} aria-hidden />
                <Text size="xs" style={{ color: "var(--ink-3)" }}>
                  {minutes} min
                </Text>
              </Group>
            )}
            {recipe.servings != null && (
              <Text size="xs" style={{ color: "var(--ink-3)" }}>
                Serves {recipe.servings}
              </Text>
            )}
            <Text size="xs" style={{ color: "var(--ink-3)" }}>
              {recipe.ingredients?.length ?? 0} ingredient{(recipe.ingredients?.length ?? 0) === 1 ? "" : "s"}
            </Text>
          </Group>
          {(report.state !== "none-saved" && report.state !== "checked-clear") && (
            <div style={{ marginTop: 6 }}>
              <AllergyChip report={report} />
            </div>
          )}
          {recipe.tags?.length > 0 && (
            <Group gap={6} mt={6} wrap="wrap">
              {recipe.tags.slice(0, 4).map((t) => (
                <Badge key={t} size="sm" variant="light" color="gray" style={{ textTransform: "none" }}>
                  {t}
                </Badge>
              ))}
            </Group>
          )}
        </div>
        <IconChevronRight size={20} stroke={ICON_STROKE} style={{ color: "var(--ink-3)", flex: "none" }} aria-hidden />
      </Group>
    </Card>
  );
}

export default function RecipesPage() {
  const role = useNavRole();
  const canWrite = role == null ? true : WRITER_ROLES.has(role);
  const [search, setSearch] = useState("");
  const [tag, setTag] = useState<string | null>(null);
  const [addOpen, setAddOpen] = useState(false);

  const recipesQuery = useRecipes({ q: search, tag: tag ?? undefined });
  const peopleQuery = usePeople();
  const recipes = recipesQuery.data?.data ?? [];
  // `null` while the people read is pending or failed: reportAllergies treats
  // that as "unknown", never as "nobody has allergies".
  const people = peopleQuery.data?.data ?? null;

  // Tag chips come from what is actually saved, so a chip never filters to
  // nothing. Sorted by frequency, then name.
  const tags = useMemo(() => {
    const counts = new Map<string, number>();
    for (const r of recipes) for (const t of r.tags ?? []) counts.set(t, (counts.get(t) ?? 0) + 1);
    return Array.from(counts.entries())
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .slice(0, 8)
      .map(([t]) => t);
  }, [recipes]);

  const isError = recipesQuery.isError;
  const isLoading = recipesQuery.isLoading;

  return (
    <div className="mx-auto w-full max-w-6xl">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3 md:mb-6">
        <div className="min-w-0">
          <Title order={2} className="text-xl font-semibold text-ink md:text-2xl">
            Recipes
          </Title>
          <Text size="sm" mt={4} style={{ color: "var(--ink-3)" }}>
            The dinners your family actually cooks. Each one shows whether it was checked against the family&apos;s
            allergies — never assume it was.
          </Text>
        </div>
        {canWrite && (
          <Button
            onClick={() => setAddOpen(true)}
            leftSection={<IconPlus size={16} stroke={ICON_STROKE} />}
            aria-haspopup="dialog"
            className="max-md:min-h-11"
            data-recipes-add
          >
            Add recipe
          </Button>
        )}
      </div>

      <TextInput
        value={search}
        onChange={(e) => setSearch(e.currentTarget.value)}
        placeholder="Search by name or ingredient"
        aria-label="Search recipes"
        leftSection={<IconSearch size={16} stroke={ICON_STROKE} />}
        mb={tags.length > 0 ? "xs" : "md"}
        className="max-md:[&_input]:min-h-11 max-md:[&_input]:text-base"
        data-recipes-search
      />

      {tags.length > 0 && (
        <Group gap={6} mb="md" wrap="wrap" role="toolbar" aria-label="Filter by tag">
          <Chip checked={tag === null} onChange={() => setTag(null)} size="md">
            All
          </Chip>
          {tags.map((t) => (
            <Chip key={t} checked={tag === t} onChange={() => setTag(tag === t ? null : t)} size="md">
              {t}
            </Chip>
          ))}
        </Group>
      )}

      {isLoading && (
        <Stack gap="xs">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} height={88} radius="md" />
          ))}
        </Stack>
      )}

      {isError && !isLoading && (
        <Card radius="token-md" p="md" withBorder style={{ borderLeft: "4px solid var(--danger)" }} role="alert">
          <Text fw={600}>The recipes didn&apos;t load</Text>
          <Text size="sm" mt={4} style={{ color: "var(--ink-2)" }}>
            Home OS didn&apos;t answer. Your recipes are safe — nothing was lost.
          </Text>
          <Button variant="default" mt="sm" onClick={() => recipesQuery.refetch()} className="max-md:min-h-11">
            Try again
          </Button>
        </Card>
      )}

      {!isLoading && !isError && recipes.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <Text size="sm" style={{ color: "var(--ink-3)" }}>
            {search.trim() || tag
              ? "No recipes match that search."
              : "No recipes yet. Add the dinners you actually cook and suggestions start from those."}
          </Text>
          {canWrite && (
            <Button
              variant="light"
              onClick={() => setAddOpen(true)}
              leftSection={<IconPlus size={16} stroke={ICON_STROKE} />}
              className="max-md:min-h-11"
            >
              Add recipe
            </Button>
          )}
        </div>
      )}

      {!isLoading && !isError && recipes.length > 0 && (
        <Stack gap="xs" data-recipe-list>
          {recipes.map((recipe) => (
            <RecipeRow key={recipe.id} recipe={recipe} people={people} />
          ))}
        </Stack>
      )}

      {addOpen && <RecipeForm opened={addOpen} onClose={() => setAddOpen(false)} />}
    </div>
  );
}
