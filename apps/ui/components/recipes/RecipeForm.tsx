"use client";

/**
 * RecipeForm (#4567): create and edit a recipe with STRUCTURED ingredients.
 *
 * Built to the approved mockup (#106 v3, "Import recipe" and "Recipe" states)
 * and the manager decisions recorded on #4519:
 *  - ingredients are structured rows ({quantity, unit, name, section}), because
 *    the meal→grocery merge (#4500) needs quantities to add up;
 *  - the paste box is the PRIMARY way in (a parent pastes from a website or an
 *    index card) and it parses the same way the recipe tools do, showing a
 *    preview of what will be saved before saving;
 *  - "From a link" is a SECONDARY row: source_url must be http(s) and is kept
 *    as a reference, never fetched here.
 *
 * Two API rules this form respects:
 *  - PUT /recipes/{id} REPLACES: omitted optional fields are cleared. So the
 *    form always sends every field, and clearing a box really does clear it.
 *  - `steps` is ONE text column, one step per line (KB recipe-tools.md), so the
 *    textarea is split and re-joined on the way in and out.
 *
 * Phones: ResponsiveModal gives 44 px inputs and 16 px text (no iOS zoom) and
 * keeps the actions above the keyboard (#4532). Every action is a 44 px target.
 */

import { useMemo, useState, type FormEvent } from "react";
import { useRouter } from "next/navigation";
import {
  ActionIcon,
  Badge,
  Button,
  Group,
  Input,
  SegmentedControl,
  Stack,
  TagsInput,
  Text,
  Textarea,
  TextInput,
  Tooltip,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconPlus, IconTrash, IconWand } from "@tabler/icons-react";
import { ResponsiveModal, ResponsiveModalActions } from "@/components/ui";
import {
  cleanIngredients,
  formatQuantity,
  parseIngredientLines,
  parseSteps,
  UNIT_OPTIONS,
  type Ingredient,
} from "@/lib/recipes/ingredients";
import { useCreateRecipe, useUpdateRecipe } from "@/lib/recipes/api";
import type { Recipe, RecipeInput } from "@/lib/recipes/types";

const ICON_STROKE = 1.75;

type Mode = "paste" | "rows";

interface Props {
  opened: boolean;
  onClose: () => void;
  /** The recipe to edit; absent means create. */
  recipe?: Recipe | null;
}

const EMPTY_ROW = (): Ingredient => ({ name: "", quantity: "", unit: "", section: "" });

/** The API's ingredient shape: note is not stored, so it is folded into the name. */
function toApiIngredients(rows: readonly Ingredient[]): Ingredient[] {
  return cleanIngredients(rows).map((ing) => {
    // The API has no `note` column (047: {name, quantity, unit, section}), so a
    // preparation note would be silently dropped. Keep it, in the name, rather
    // than lose "sifted" — the grocery merge matches on the name's words.
    const note = (rows.find((r) => r.name === ing.name)?.note ?? "").trim();
    if (!note) return ing;
    const merged: Ingredient = { ...ing, name: `${ing.name} (${note})` };
    delete (merged as { note?: unknown }).note;
    return merged;
  });
}

/** The form's own rows from a saved recipe (which has no `note`). */
function toRows(recipe: Recipe | null | undefined): Ingredient[] {
  if (!recipe || !Array.isArray(recipe.ingredients) || recipe.ingredients.length === 0) return [EMPTY_ROW()];
  return recipe.ingredients.map((ing) => ({
    name: ing.name ?? "",
    quantity: ing.quantity ?? "",
    unit: ing.unit ?? "",
    section: ing.section ?? "",
  }));
}

export default function RecipeForm({ opened, onClose, recipe }: Props) {
  const router = useRouter();
  const isEdit = !!recipe;
  const create = useCreateRecipe();
  const update = useUpdateRecipe(recipe?.id ?? "");
  const saving = create.isPending || update.isPending;

  const [title, setTitle] = useState(recipe?.title ?? "");
  const [description, setDescription] = useState(recipe?.description ?? "");
  const [servings, setServings] = useState(recipe?.servings != null ? String(recipe.servings) : "");
  const [prepMinutes, setPrepMinutes] = useState(recipe?.prep_minutes != null ? String(recipe.prep_minutes) : "");
  const [cookMinutes, setCookMinutes] = useState(recipe?.cook_minutes != null ? String(recipe.cook_minutes) : "");
  const [tags, setTags] = useState<string[]>(recipe?.tags ?? []);
  const [sourceUrl, setSourceUrl] = useState(recipe?.source_url ?? "");
  const [stepsText, setStepsText] = useState(recipe?.steps ?? "");
  const [mode, setMode] = useState<Mode>("rows");
  const [rows, setRows] = useState<Ingredient[]>(() => toRows(recipe));
  const [paste, setPaste] = useState("");
  const [error, setError] = useState<string | null>(null);

  /** The rows that will actually be saved: pasted ones while in paste mode. */
  const effectiveRows = useMemo(
    () => (mode === "paste" ? parseIngredientLines(paste) : rows),
    [mode, paste, rows],
  );
  const cleaned = useMemo(() => cleanIngredients(effectiveRows), [effectiveRows]);
  const stepList = useMemo(() => parseSteps(stepsText), [stepsText]);

  const setRow = (index: number, patch: Partial<Ingredient>) => {
    setRows((prev) => prev.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  };
  const addRow = () => setRows((prev) => [...prev, EMPTY_ROW()]);
  const removeRow = (index: number) =>
    setRows((prev) => (prev.length === 1 ? [EMPTY_ROW()] : prev.filter((_, i) => i !== index)));

  /** "Use parsed rows" copies the paste preview into the editor for tweaking. */
  const adoptParsed = () => {
    const parsed = parseIngredientLines(paste);
    if (parsed.length === 0) return;
    setRows(parsed.map((p) => ({ ...p, quantity: p.quantity ?? "", unit: p.unit ?? "", section: p.section ?? "" })));
    setMode("rows");
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (!title.trim()) {
      setError("Give the recipe a name.");
      return;
    }
    if (cleaned.length === 0) {
      // Not a hard block on the server, but an ingredient-less recipe can't be
      // allergy-checked, and the server excludes it while anyone has an allergy
      // (#4803, noIngredientsLine). Say so here rather than let it surprise them.
      setError("Add at least one ingredient — without ingredients the recipe can't be checked for allergies.");
      return;
    }
    const body: RecipeInput = {
      title: title.trim(),
      description: description.trim() || null,
      servings: servings.trim() === "" ? null : Number(servings),
      prep_minutes: prepMinutes.trim() === "" ? null : Number(prepMinutes),
      cook_minutes: cookMinutes.trim() === "" ? null : Number(cookMinutes),
      ingredients: toApiIngredients(effectiveRows),
      steps: stepList.length > 0 ? stepList.join("\n") : null,
      tags,
      source_url: sourceUrl.trim() || null,
    };
    const onSuccess = (saved: Recipe) => {
      notifications.show({ message: isEdit ? "Recipe saved" : `Recipe added: ${saved.title}`, color: "teal" });
      onClose();
      // Navigate without closing first (ResponsiveModal #4532): replace swaps
      // the dialog's history entry, so Back from the recipe returns to the list.
      if (!isEdit) router.replace(`/dashboard/recipes/${saved.id}`);
    };
    const onError = (err: unknown) => {
      setError(err instanceof Error ? err.message : "Could not save the recipe.");
    };
    if (isEdit) update.mutate(body, { onSuccess: (res) => onSuccess(res.data), onError });
    else create.mutate(body, { onSuccess: (res) => onSuccess(res.data), onError });
  };

  const invalidUrl = sourceUrl.trim() !== "" && !/^https?:\/\//i.test(sourceUrl.trim());

  return (
    <ResponsiveModal opened={opened} onClose={onClose} title={isEdit ? "Edit recipe" : "Add a recipe"} size="lg">
      <form onSubmit={submit} noValidate>
        <Stack gap="md">
          <TextInput
            label="Name"
            required
            value={title}
            onChange={(e) => setTitle(e.currentTarget.value)}
            placeholder="Sheet-pan chicken fajitas"
            data-autofocus
            error={error && !title.trim() ? " " : undefined}
          />

          <Group grow align="flex-start" gap="sm">
            <TextInput
              label="Serves"
              inputMode="numeric"
              value={servings}
              onChange={(e) => setServings(e.currentTarget.value.replace(/[^0-9]/g, ""))}
              placeholder="5"
            />
            <TextInput
              label="Prep (min)"
              inputMode="numeric"
              value={prepMinutes}
              onChange={(e) => setPrepMinutes(e.currentTarget.value.replace(/[^0-9]/g, ""))}
              placeholder="10"
            />
            <TextInput
              label="Cook (min)"
              inputMode="numeric"
              value={cookMinutes}
              onChange={(e) => setCookMinutes(e.currentTarget.value.replace(/[^0-9]/g, ""))}
              placeholder="25"
            />
          </Group>

          <div>
            <Input.Label mb={6}>Ingredients</Input.Label>
            <SegmentedControl
              fullWidth
              size="sm"
              value={mode}
              onChange={(v) => setMode(v as Mode)}
              data={[
                { label: "Type them in", value: "rows" },
                { label: "Paste a list", value: "paste" },
              ]}
              mb="xs"
            />
            <Text size="xs" style={{ color: "var(--ink-3)" }} mb="xs">
              Structured so the grocery list can add amounts together. Quantity, unit and item are kept apart.
            </Text>

            {mode === "paste" ? (
              <Stack gap="xs">
                <Textarea
                  value={paste}
                  onChange={(e) => setPaste(e.currentTarget.value)}
                  autosize
                  minRows={5}
                  maxRows={12}
                  spellCheck={false}
                  placeholder={"2 cups flour, sifted\n1½ lb chicken thighs\n3 cloves garlic"}
                  styles={{ input: { fontFamily: "var(--font-mono, ui-monospace, monospace)", fontSize: 13 } }}
                  aria-label="Paste the ingredients, one per line"
                />
                <Group gap="xs" justify="space-between" wrap="wrap">
                  <Text size="xs" style={{ color: "var(--ink-3)" }}>
                    One ingredient per line. &ldquo;2 cups flour, sifted&rdquo; becomes 2 · cup · flour.
                  </Text>
                  {cleaned.length > 0 && (
                    <Button
                      size="compact-sm"
                      variant="light"
                      leftSection={<IconWand size={14} stroke={ICON_STROKE} />}
                      onClick={adoptParsed}
                      className="max-md:min-h-11"
                    >
                      Use these {cleaned.length} rows
                    </Button>
                  )}
                </Group>
                {cleaned.length > 0 && (
                  <div data-import-preview style={{ background: "var(--surface-2)", borderRadius: 8, padding: 12 }}>
                    <Text size="xs" fw={700} tt="uppercase" style={{ color: "var(--ink-3)", letterSpacing: ".04em" }} mb={6}>
                      Will save {cleaned.length} ingredient{cleaned.length === 1 ? "" : "s"}
                    </Text>
                    <Stack gap={2}>
                      {cleaned.map((ing, i) => (
                        <Text key={`${i}-${ing.name}`} size="sm" style={{ color: "var(--ink-2)", overflowWrap: "anywhere" }}>
                          {formatQuantity(ing.quantity)} {ing.unit ?? ""} <strong>{ing.name}</strong>
                        </Text>
                      ))}
                    </Stack>
                  </div>
                )}
              </Stack>
            ) : (
              <Stack gap="xs">
                {rows.map((row, i) => (
                  <Group key={i} gap={6} align="flex-start" wrap="nowrap" data-ingredient-row>
                    <TextInput
                      aria-label={`Quantity for ingredient ${i + 1}`}
                      placeholder="2"
                      value={row.quantity == null ? "" : String(row.quantity)}
                      onChange={(e) => {
                        const v = e.currentTarget.value;
                        const n = Number(v);
                        setRow(i, { quantity: v === "" ? "" : Number.isFinite(n) && v.trim() !== "" ? n : v });
                      }}
                      w={64}
                      style={{ flex: "0 0 auto" }}
                    />
                    <TextInput
                      aria-label={`Unit for ingredient ${i + 1}`}
                      placeholder="cup"
                      value={row.unit ?? ""}
                      onChange={(e) => setRow(i, { unit: e.currentTarget.value })}
                      list="recipe-units"
                      w={72}
                      style={{ flex: "0 0 auto" }}
                    />
                    <TextInput
                      aria-label={`Ingredient ${i + 1}`}
                      placeholder="flour, sifted"
                      value={row.name}
                      onChange={(e) => setRow(i, { name: e.currentTarget.value })}
                      style={{ flex: 1, minWidth: 0 }}
                    />
                    <Tooltip label="Remove" withArrow>
                      <ActionIcon
                        variant="subtle"
                        color="gray"
                        size="lg"
                        aria-label={`Remove ingredient ${i + 1}`}
                        onClick={() => removeRow(i)}
                      >
                        <IconTrash size={18} stroke={ICON_STROKE} />
                      </ActionIcon>
                    </Tooltip>
                  </Group>
                ))}
                <datalist id="recipe-units">
                  {UNIT_OPTIONS.map((u) => (
                    <option key={u} value={u} />
                  ))}
                </datalist>
                <Button
                  variant="subtle"
                  size="compact-sm"
                  leftSection={<IconPlus size={16} stroke={ICON_STROKE} />}
                  onClick={addRow}
                  className="max-md:min-h-11"
                  style={{ alignSelf: "flex-start" }}
                >
                  Add ingredient
                </Button>
              </Stack>
            )}
          </div>

          <div>
            <Textarea
              label="Steps"
              description="One step per line. Numbers are stripped — the order is the number."
              value={stepsText}
              onChange={(e) => setStepsText(e.currentTarget.value)}
              autosize
              minRows={4}
              maxRows={16}
              placeholder={"Heat the oven to 425°F.\nToss everything on a sheet pan.\nRoast 30 min, stirring once."}
            />
            {stepList.length > 0 && (
              <Text size="xs" mt={4} style={{ color: "var(--ink-3)" }} data-step-count>
                {stepList.length} step{stepList.length === 1 ? "" : "s"}
              </Text>
            )}
          </div>

          <TagsInput
            label="Tags"
            description="Weeknight, kid favorites, freezer…"
            value={tags}
            onChange={setTags}
            placeholder="Press Enter to add"
          />

          <TextInput
            label="From a link"
            description="Optional. Kept as the recipe's source; nothing is fetched."
            value={sourceUrl}
            onChange={(e) => setSourceUrl(e.currentTarget.value)}
            placeholder="https://…"
            inputMode="url"
            error={invalidUrl ? "Must start with http:// or https://" : undefined}
          />

          <Textarea
            label="Notes"
            value={description}
            onChange={(e) => setDescription(e.currentTarget.value)}
            autosize
            minRows={2}
            placeholder="Anything worth remembering — the variation the kids like, the pan that works."
          />

          {error && (
            <Group gap={8} align="flex-start" wrap="nowrap" role="alert" data-form-error>
              <IconAlertTriangle size={18} stroke={ICON_STROKE} style={{ color: "var(--danger)", flex: "none", marginTop: 2 }} aria-hidden />
              <Text size="sm" style={{ color: "var(--danger)" }}>
                {error}
              </Text>
            </Group>
          )}

          {cleaned.length === 0 && (
            <Badge color="orange" variant="light" leftSection={<IconAlertTriangle size={12} stroke={ICON_STROKE} />}>
              No ingredients yet — the recipe can&rsquo;t be allergy-checked
            </Badge>
          )}
        </Stack>

        <ResponsiveModalActions>
          <Button variant="default" onClick={onClose} disabled={saving} className="max-md:min-h-11">
            Cancel
          </Button>
          <Button type="submit" loading={saving} className="max-md:min-h-11">
            {isEdit ? "Save recipe" : "Add recipe"}
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}
