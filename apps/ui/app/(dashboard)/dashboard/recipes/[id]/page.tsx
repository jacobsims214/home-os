"use client";

/**
 * Recipe detail (#4567): one recipe, with cook mode, ratings and the allergy
 * surface.
 *
 * Built to the approved mockup (#106 v3, "Recipe" state): back link, title,
 * meta row (time, servings, rating, tags), the action row (Cook mode, Plan it,
 * Edit), the allergy banner, ingredients grouped by store section, steps, next
 * planned, recent ratings, and the delete affordance.
 *
 * THE ALLERGY BANNER IS THE POINT OF THIS PAGE. It renders three things the
 * server may send, and refuses to blur them:
 *   1. `allergy_warnings` — a real match: who, which allergen, what matched.
 *   2. `checks` — what could NOT be checked, verbatim. #4874 made an
 *      unreadable allergy string land here instead of silently matching
 *      nothing; hiding or collapsing it would put that silent failure back.
 *   3. nothing at all — in which case, if the family has allergies saved, the
 *      page says "Not allergy-checked" rather than letting an empty banner
 *      read as "safe". See lib/recipes/allergy.ts.
 *
 * Manager decision (#4519 #13039/#13046): the banner never blocks. A planned
 * meal stays planned; the one hard confirm is at the moment a recipe is ADDED
 * to the plan, which is #4568's week view — `needsAddConfirm` and
 * `addConfirmCopy` in lib/recipes/allergy.ts are exported for it, and the
 * "Plan it" action here uses them.
 *
 * Roles: viewer and housesitter read (allergies included — safety data);
 * only family roles get Cook mode's siblings "Plan it" / "Edit" / "Delete".
 */

import { useMemo, useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import {
  Badge,
  Button,
  Card,
  Divider,
  Group,
  Modal,
  Rating,
  Skeleton,
  Stack,
  Text,
  TextInput,
  Textarea,
  Title,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconChevronLeft,
  IconClock,
  IconFlame,
  IconPencil,
  IconTrash,
  IconUsers,
} from "@tabler/icons-react";
import { useDeleteRecipe, useMealPlans, usePeople, useRateMeal, useRecipe } from "@/lib/recipes/api";
import { addConfirmCopy, needsAddConfirm, reportAllergies } from "@/lib/recipes/allergy";
import { formatQuantity, groupBySection, parseSteps } from "@/lib/recipes/ingredients";
import AllergyBanner from "@/components/recipes/AllergyBanner";
import CookMode from "@/components/recipes/CookMode";
import RecipeForm from "@/components/recipes/RecipeForm";
import { useNavRole } from "@/components/shared/use-nav-role";
import dayjs from "dayjs";
import { useHouseholdToday } from "@/lib/today/api";
import { addDays } from "@/lib/recipes/week";

const ICON_STROKE = 1.75;
const WRITER_ROLES = new Set(["owner", "family_manager", "family_member"]);
const RATER_OTHERS = new Set(["owner", "family_manager"]);

export default function RecipeDetailPage() {
  const params = useParams<{ id: string }>();
  const id = params?.id;
  const router = useRouter();
  const role = useNavRole();
  const canWrite = role == null ? true : WRITER_ROLES.has(role);
  const canRateOthers = role == null ? true : RATER_OTHERS.has(role);

  const recipeQuery = useRecipe(id);
  const peopleQuery = usePeople();
  const recipe = recipeQuery.data?.data ?? null;
  const people = peopleQuery.data?.data ?? null;

  // "Next planned" and ratings need a range. Today..+60d is enough to answer
  // "when is this on the plan" without pulling a year of history.
  //
  // `from` is the FAMILY's today (#5316): a page opened at 22:30 in New York
  // on a browser reading UTC would otherwise ask from tomorrow, and "next
  // planned" would silently skip tonight.
  const today = useHouseholdToday();
  // A stable placeholder while the household loads: `enabled` is what keeps
  // the request from going out, not a null range (useMealPlans' key needs one).
  const range = useMemo(
    () => (today ? { from: today, to: addDays(today, 60) } : { from: "", to: "" }),
    [today],
  );
  const mealsQuery = useMealPlans(range, !!recipe && !!today);
  const plannedMeals = (mealsQuery.data?.data ?? []).filter((m) => m.recipe_id === recipe?.id);
  const nextPlanned = plannedMeals.sort((a, b) => a.date.localeCompare(b.date))[0] ?? null;
  const lastPlanned = [...plannedMeals].sort((a, b) => b.date.localeCompare(a.date))[0] ?? null;

  const report = useMemo(() => reportAllergies(recipe, people), [recipe, people]);

  const [cookOpen, setCookOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [rateOpen, setRateOpen] = useState(false);
  const [addConfirm, setAddConfirm] = useState<{ title: string; body: string } | null>(null);
  const deleteRecipe = useDeleteRecipe();

  const ingredients = recipe?.ingredients ?? [];
  const sections = useMemo(() => groupBySection(ingredients), [ingredients]);
  const steps = useMemo(() => parseSteps(recipe?.steps ?? ""), [recipe?.steps]);

  if (recipeQuery.isLoading) {
    return (
      <div className="mx-auto w-full max-w-6xl">
        <Skeleton height={28} width="55%" mb="sm" radius="sm" />
        <Skeleton height={18} width="35%" mb="lg" radius="sm" />
        <Skeleton height={96} radius="md" mb="md" />
        <Skeleton height={240} radius="md" />
      </div>
    );
  }

  if (recipeQuery.isError || !recipe) {
    return (
      <div className="mx-auto w-full max-w-6xl">
        <Button
          component={Link}
          href="/dashboard/recipes"
          variant="subtle"
          leftSection={<IconChevronLeft size={18} stroke={ICON_STROKE} />}
          className="max-md:min-h-11"
        >
          All recipes
        </Button>
        <Card radius="token-md" p="md" withBorder role="alert" style={{ borderLeft: "4px solid var(--danger)" }}>
          <Text fw={600}>That recipe isn&apos;t here</Text>
          <Text size="sm" mt={4} style={{ color: "var(--ink-2)" }}>
            It may have been deleted, or it belongs to another family.
          </Text>
        </Card>
      </div>
    );
  }

  const totalMinutes = (recipe.prep_minutes ?? 0) + (recipe.cook_minutes ?? 0);

  /** "Plan it": the ONE place an unsafe recipe gets a hard confirm. */
  const onPlanIt = () => {
    if (!needsAddConfirm(report)) {
      notifications.show({
        message: `${recipe.title} is safe for everyone — pick a night on the meal plan.`,
        color: "teal",
      });
      router.push("/dashboard/meals");
      return;
    }
    setAddConfirm(addConfirmCopy(report, recipe.title));
  };

  const onDelete = () => {
    deleteRecipe.mutate(recipe.id, {
      onSuccess: () => {
        notifications.show({ message: "Recipe deleted. Planned meals keep their title.", color: "teal" });
        router.push("/dashboard/recipes");
      },
      onError: () => notifications.show({ message: "Could not delete that recipe.", color: "red" }),
    });
  };

  return (
    <div className="mx-auto w-full max-w-6xl">
      <Button
        component={Link}
        href="/dashboard/recipes"
        variant="subtle"
        leftSection={<IconChevronLeft size={18} stroke={ICON_STROKE} />}
        className="max-md:min-h-11"
        style={{ marginLeft: -8 }}
      >
        All recipes
      </Button>

      <div style={{ display: "flex", flexDirection: "column", gap: 10, margin: "4px 0 16px" }}>
        <Title order={2} style={{ fontSize: 24, fontWeight: 600, lineHeight: 1.2, overflowWrap: "anywhere" }} data-recipe-heading>
          {recipe.title}
        </Title>
        <Group gap={12} wrap="wrap">
          {totalMinutes > 0 && (
            <Group gap={5} wrap="nowrap">
              <IconClock size={15} stroke={ICON_STROKE} style={{ color: "var(--ink-3)" }} aria-hidden />
              <Text size="sm" style={{ color: "var(--ink-2)" }}>
                {recipe.prep_minutes ? `${recipe.prep_minutes} min prep` : ""}
                {recipe.prep_minutes && recipe.cook_minutes ? " · " : ""}
                {recipe.cook_minutes ? `${recipe.cook_minutes} min cook` : ""}
              </Text>
            </Group>
          )}
          {recipe.servings != null && (
            <Group gap={5} wrap="nowrap">
              <IconUsers size={15} stroke={ICON_STROKE} style={{ color: "var(--ink-3)" }} aria-hidden />
              <Text size="sm" style={{ color: "var(--ink-2)" }}>
                Serves {recipe.servings}
              </Text>
            </Group>
          )}
          {recipe.tags?.map((t) => (
            <Badge key={t} size="sm" variant="light" color="gray" style={{ textTransform: "none" }}>
              {t}
            </Badge>
          ))}
        </Group>

        {/*
          Cook mode is READ-ONLY — it marks steps done in local state and writes
          nothing — so every reader gets it, housesitter and viewer included.
          That is deliberate: a sitter is the one cooking dinner, and the mockup's
          housesitter state says they see the plan and the allergy warnings.
          Only Plan it / Edit / Delete are writes.
        */}
        <Group gap={8} wrap="wrap">
          <Button
            onClick={() => setCookOpen(true)}
            leftSection={<IconFlame size={16} stroke={ICON_STROKE} />}
            disabled={steps.length === 0}
            title={steps.length === 0 ? "Add the steps first" : undefined}
            className="max-md:min-h-11"
            data-recipe-cook
          >
            Cook mode
          </Button>
          {canWrite && (
            <>
            <Button
              variant="default"
              onClick={onPlanIt}
              className="max-md:min-h-11"
              data-recipe-plan
            >
              Plan it
            </Button>
            <Button
              variant="default"
              onClick={() => setEditOpen(true)}
              leftSection={<IconPencil size={16} stroke={ICON_STROKE} />}
              className="max-md:min-h-11"
              data-recipe-edit
            >
              Edit
            </Button>
            <Button
              variant="subtle"
              color="red"
              onClick={() => setDeleteOpen(true)}
              leftSection={<IconTrash size={16} stroke={ICON_STROKE} />}
              className="max-md:min-h-11"
            >
              Delete
            </Button>
            </>
          )}
        </Group>
      </div>

      {/* The allergy surface. Always rendered when it has anything to say. */}
      <AllergyBanner report={report} />

      {recipe.description && (
        <Text size="sm" mb="md" style={{ color: "var(--ink-2)", whiteSpace: "pre-wrap" }}>
          {recipe.description}
        </Text>
      )}

      <div className="grid grid-cols-1 gap-6 md:grid-cols-[minmax(0,1.5fr)_minmax(0,1fr)]">
        <div style={{ minWidth: 0 }}>
          <section aria-labelledby="ingredients-h" style={{ marginBottom: 24 }}>
            <Group justify="space-between" align="baseline" mb="xs">
              <Text id="ingredients-h" fw={600} size="md" component="h3">
                Ingredients <Text component="span" size="sm" style={{ color: "var(--ink-3)" }}>{ingredients.length}</Text>
              </Text>
            </Group>
            <Card radius="token-md" p={0} withBorder style={{ background: "var(--surface)", overflow: "hidden" }}>
              {ingredients.length === 0 ? (
                <div style={{ padding: 16 }}>
                  <Group gap={8} align="flex-start" wrap="nowrap">
                    <IconAlertTriangle size={18} stroke={ICON_STROKE} style={{ color: "var(--danger)", flex: "none", marginTop: 2 }} aria-hidden />
                    <Text size="sm" style={{ color: "var(--ink-2)" }}>
                      No ingredients saved. Without them this recipe can&apos;t be checked for allergies, so it is left
                      out of suggestions while anyone in the family has one.
                    </Text>
                  </Group>
                </div>
              ) : (
                <ul style={{ listStyle: "none", margin: 0, padding: 0 }} data-ingredient-list>
                  {sections.map((group) => (
                    <li key={group.section} style={{ listStyle: "none" }}>
                      {sections.length > 1 && (
                        <div
                          style={{
                            padding: "12px 14px 6px",
                            fontSize: 12,
                            fontWeight: 700,
                            letterSpacing: ".07em",
                            textTransform: "uppercase",
                            color: "var(--ink-3)",
                            background: "var(--surface-2)",
                            borderBottom: "1px solid var(--line)",
                          }}
                        >
                          {group.section}
                        </div>
                      )}
                      <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                        {group.items.map((ing, i) => (
                          <li
                            key={`${group.section}-${i}-${ing.name}`}
                            style={{
                              display: "grid",
                              gridTemplateColumns: "76px minmax(0,1fr)",
                              gap: 10,
                              alignItems: "baseline",
                              minHeight: 44,
                              padding: "9px 14px",
                              borderBottom: "1px solid var(--line)",
                              fontSize: 14.5,
                            }}
                          >
                            <span style={{ fontWeight: 700, color: "var(--ink-2)" }}>{formatQuantity(ing.quantity)}</span>
                            <span style={{ overflowWrap: "anywhere" }}>
                              {ing.unit ? `${ing.unit} ` : ""}
                              {ing.name}
                            </span>
                          </li>
                        ))}
                      </ul>
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          </section>

          <section aria-labelledby="steps-h">
            <Group justify="space-between" align="baseline" mb="xs">
              <Text id="steps-h" fw={600} size="md" component="h3">
                Steps <Text component="span" size="sm" style={{ color: "var(--ink-3)" }}>{steps.length}</Text>
              </Text>
            </Group>
            <Card radius="token-md" p={0} withBorder style={{ background: "var(--surface)", overflow: "hidden" }}>
              {steps.length === 0 ? (
                <div style={{ padding: 16 }}>
                  <Text size="sm" style={{ color: "var(--ink-3)" }}>
                    No steps yet.{" "}
                    {canWrite && (
                      <Button variant="subtle" size="compact-sm" onClick={() => setEditOpen(true)}>
                        Add them
                      </Button>
                    )}
                  </Text>
                </div>
              ) : (
                <ol style={{ listStyle: "none", margin: 0, padding: 0 }} data-step-list>
                  {steps.map((step, i) => (
                    <li
                      key={i}
                      style={{
                        display: "grid",
                        gridTemplateColumns: "30px minmax(0,1fr)",
                        gap: 12,
                        padding: "12px 14px",
                        borderBottom: i === steps.length - 1 ? 0 : "1px solid var(--line)",
                      }}
                    >
                      <span
                        aria-hidden
                        style={{
                          display: "grid",
                          placeItems: "center",
                          width: 26,
                          height: 26,
                          borderRadius: "50%",
                          background: "var(--surface-2)",
                          color: "var(--ink-2)",
                          fontSize: 13,
                          fontWeight: 700,
                        }}
                      >
                        {i + 1}
                      </span>
                      <Text size="sm" style={{ lineHeight: 1.55 }}>
                        {step}
                      </Text>
                    </li>
                  ))}
                </ol>
              )}
            </Card>
          </section>
        </div>

        <div style={{ minWidth: 0 }}>
          <Stack gap="lg">
            <section aria-labelledby="planned-h">
              <Text id="planned-h" fw={600} size="md" component="h3" mb="xs">
                On the plan
              </Text>
              <Card radius="token-md" p="md" withBorder style={{ background: "var(--surface)" }}>
                {nextPlanned ? (
                  <>
                    <Text fw={600} size="sm">
                      {dayjs(nextPlanned.date).format("ddd, MMM D")} · {nextPlanned.slot}
                    </Text>
                    <Text size="xs" mt={2} style={{ color: "var(--ink-3)" }}>
                      Next planned
                    </Text>
                  </>
                ) : lastPlanned ? (
                  <>
                    <Text fw={600} size="sm">
                      Last made {dayjs(lastPlanned.date).format("MMM D")}
                    </Text>
                    <Text size="xs" mt={2} style={{ color: "var(--ink-3)" }}>
                      Not on the plan in the next 60 days
                    </Text>
                  </>
                ) : (
                  <Text size="sm" style={{ color: "var(--ink-3)" }}>
                    Not planned yet.
                  </Text>
                )}
              </Card>
            </section>

            <section aria-labelledby="ratings-h">
              <Group justify="space-between" align="baseline" mb="xs">
                <Text id="ratings-h" fw={600} size="md" component="h3">
                  Recent ratings
                </Text>
                {canWrite && nextPlanned && (
                  <Button variant="subtle" size="compact-sm" onClick={() => setRateOpen(true)} className="max-md:min-h-11">
                    Rate it
                  </Button>
                )}
              </Group>
              <Card radius="token-md" p="md" withBorder style={{ background: "var(--surface)" }}>
                {plannedMeals.some((m) => m.feedback?.length > 0) ? (
                  <Stack gap="sm">
                    {plannedMeals
                      .flatMap((m) => m.feedback.map((f) => ({ ...f, date: m.date })))
                      .filter((f) => typeof f.rating === "number")
                      .slice(0, 5)
                      .map((f) => {
                        const person = people?.find((p) => p.id === f.person_id);
                        return (
                          <div key={f.id}>
                            <Group gap={8} wrap="wrap">
                              <Text size="sm" fw={600}>
                                {person?.display_name ?? "Someone"}
                              </Text>
                              <Rating value={f.rating ?? 0} readOnly size="sm" />
                              <Text size="xs" style={{ color: "var(--ink-3)" }}>
                                {dayjs(f.date).format("MMM D")}
                              </Text>
                            </Group>
                            {f.comment && (
                              <Text size="sm" mt={2} style={{ color: "var(--ink-2)" }}>
                                &ldquo;{f.comment}&rdquo;
                              </Text>
                            )}
                          </div>
                        );
                      })}
                  </Stack>
                ) : (
                  <Text size="sm" style={{ color: "var(--ink-3)" }}>
                    Nobody has rated this one yet. Ratings are how suggestions learn what the family likes.
                  </Text>
                )}
              </Card>
            </section>

            {recipe.source_url && (
              <Text size="xs" style={{ color: "var(--ink-3)", overflowWrap: "anywhere" }}>
                Source:{" "}
                <a href={recipe.source_url} target="_blank" rel="noopener noreferrer" style={{ color: "var(--accent)" }}>
                  {recipe.source_url}
                </a>
              </Text>
            )}
          </Stack>
        </div>
      </div>

      <CookMode
        opened={cookOpen}
        onClose={() => setCookOpen(false)}
        title={recipe.title}
        ingredients={ingredients}
        steps={recipe.steps}
      />

      {editOpen && <RecipeForm opened={editOpen} onClose={() => setEditOpen(false)} recipe={recipe} />}

      {/* The ONE hard confirm: adding an unsafe or unverified recipe to the plan. */}
      <Modal
        opened={!!addConfirm}
        onClose={() => setAddConfirm(null)}
        title={addConfirm?.title}
        centered
        data-add-confirm
      >
        <Group gap={10} align="flex-start" wrap="nowrap" mb="md">
          <IconAlertTriangle size={20} stroke={ICON_STROKE} style={{ color: "var(--danger)", flex: "none", marginTop: 2 }} aria-hidden />
          <Text size="sm" style={{ color: "var(--ink-2)" }}>
            {addConfirm?.body}
          </Text>
        </Group>
        <Group justify="flex-end" gap={8}>
          <Button variant="default" onClick={() => setAddConfirm(null)} className="max-md:min-h-11">
            Don&apos;t plan it
          </Button>
          <Button
            color="red"
            onClick={() => {
              setAddConfirm(null);
              router.push("/dashboard/meals");
            }}
            className="max-md:min-h-11"
            data-add-confirm-yes
          >
            I understand, plan it
          </Button>
        </Group>
      </Modal>

      <Modal opened={deleteOpen} onClose={() => setDeleteOpen(false)} title="Delete this recipe?" centered>
        <Text size="sm" style={{ color: "var(--ink-2)" }}>
          Planned meals keep their title, and the ratings people gave stay. This can&apos;t be undone.
        </Text>
        <Group justify="flex-end" mt="lg" gap={8}>
          <Button variant="default" onClick={() => setDeleteOpen(false)} className="max-md:min-h-11">
            Cancel
          </Button>
          <Button color="red" onClick={onDelete} loading={deleteRecipe.isPending} className="max-md:min-h-11">
            Delete recipe
          </Button>
        </Group>
      </Modal>

      {rateOpen && nextPlanned && (
        <RateMealModal
          mealId={nextPlanned.id}
          people={people ?? []}
          canRateOthers={canRateOthers}
          onClose={() => setRateOpen(false)}
        />
      )}
    </div>
  );
}

/**
 * Rate a meal (#106 v3 "Rate a meal"): one row per person, 1-5 plus an
 * optional comment. Owners and managers rate for anyone (kids have no login);
 * a family_member rates only as their own person — the API enforces it, and
 * this only offers what the role can actually do.
 */
function RateMealModal({
  mealId,
  people,
  canRateOthers,
  onClose,
}: {
  mealId: string;
  people: Array<{ id: string; display_name: string; user_id?: string | null }>;
  canRateOthers: boolean;
  onClose: () => void;
}) {
  const rate = useRateMeal(mealId);
  const [values, setValues] = useState<Record<string, number>>({});
  const [comment, setComment] = useState("");
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    const entries = Object.entries(values).filter(([, v]) => v > 0);
    if (entries.length === 0) {
      setError("Pick a star rating for at least one person.");
      return;
    }
    try {
      for (const [personId, rating] of entries) {
        await rate.mutateAsync({ personId, rating, comment: comment || undefined });
      }
      notifications.show({ message: "Ratings saved — thanks!", color: "teal" });
      onClose();
    } catch {
      setError("Could not save the ratings. Try again in a moment.");
    }
  };

  return (
    <Modal opened onClose={onClose} title="How was it?" centered>
      <Stack gap="sm">
        {people.map((p) => (
          <Group key={p.id} justify="space-between" wrap="nowrap" gap="sm">
            <Text size="sm" fw={600} style={{ overflowWrap: "anywhere" }}>
              {p.display_name}
            </Text>
            <Rating
              value={values[p.id] ?? 0}
              onChange={(v) => {
                setError(null);
                setValues((prev) => ({ ...prev, [p.id]: v }));
              }}
              size="lg"
              aria-label={`Rating for ${p.display_name}`}
            />
          </Group>
        ))}
        <Textarea
          label="Comment (optional)"
          value={comment}
          onChange={(e) => setComment(e.currentTarget.value)}
          autosize
          minRows={2}
          placeholder="More peppers next time"
        />
        {!canRateOthers && (
          <Text size="xs" style={{ color: "var(--ink-3)" }}>
            You can rate as yourself; a parent can rate for everyone.
          </Text>
        )}
        {error && (
          <Text size="sm" role="alert" style={{ color: "var(--danger)" }}>
            {error}
          </Text>
        )}
        <Divider />
        <Group justify="flex-end" gap={8}>
          <Button variant="default" onClick={onClose} className="max-md:min-h-11">
            Cancel
          </Button>
          <Button onClick={submit} loading={rate.isPending} className="max-md:min-h-11">
            Save ratings
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
