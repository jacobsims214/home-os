"use client";

/**
 * The grocery merge PREVIEW (#4568), and the confirm that writes it.
 *
 * Manager decision on #4519 (#13046): a FULL PREVIEW PAGE before writing,
 * because the write goes into a shared list another family member may be
 * working through in store mode at that moment. So the page shows every merged
 * item with one of three states, and nothing is written until someone taps
 * Confirm:
 *
 *   Add             — the confirm will add it
 *   On the list     — an open row with the same words is already there; a
 *                     re-run adds nothing
 *   Check amount    — the list says a different amount than the plan needs.
 *                     Nothing is written for it: only a person knows whether
 *                     to buy more. The row shows BOTH amounts.
 *
 * The write is idempotent server-side (lists.Repo.MergeItems), so a second tap,
 * a retry after a dropped response, or the assistant adding the same week
 * cannot duplicate the list. The preview is computed by the same code the write
 * runs, so the two cannot disagree.
 *
 * It also says what it is NOT adding: meals with no recipe, recipes with no
 * ingredients, and — the honest part — that quantities are as each recipe is
 * written, because meal plans have no servings yet.
 */

import { useMemo } from "react";
import Link from "next/link";
import { Badge, Button, Card, Group, Skeleton, Stack, Text, Title } from "@mantine/core";
import { IconArrowLeft, IconShoppingCart, IconCheck, IconAlertTriangle, IconInfoCircle, IconListCheck } from "@tabler/icons-react";
import { formatQuantity } from "@/lib/recipes/ingredients";
import type { GroceryItem, GroceryResult } from "@/lib/recipes/types";

const ICON_STROKE = 1.75;

/** One item's amount, as a person would read the list: "6", "7 lb", "⅔ cup". */
export function amountText(item: Pick<GroceryItem, "quantity" | "unit">): string {
  const q = formatQuantity(item.quantity ?? null);
  const unit = (item.unit ?? "").trim();
  if (!q && !unit) return "";
  return [q, unit].filter(Boolean).join(" ");
}

const STATUS_LABEL: Record<GroceryItem["status"], string> = {
  add: "Add",
  already_on_list: "On the list",
  check_amount: "Check amount",
};

export interface GroceryPreviewProps {
  preview: GroceryResult | null;
  loading: boolean;
  error: boolean;
  onRetry: () => void;
  onConfirm: () => void;
  confirming: boolean;
  canWrite: boolean;
  /** The applied result, once the confirm returned. */
  applied: GroceryResult | null;
}

export default function GroceryPreview({
  preview,
  loading,
  error,
  onRetry,
  onConfirm,
  confirming,
  canWrite,
  applied,
}: GroceryPreviewProps) {
  const shown = applied ?? preview;

  // Group by section, in the server's walking order (grocery.SectionOrder).
  // The server already sorted the items; grouping here only inserts headings.
  const grouped = useMemo(() => {
    if (!shown) return [];
    const order = shown.sections.length > 0 ? shown.sections : Array.from(new Set(shown.items.map((i) => i.section)));
    return order
      .map((section) => ({ section, items: shown.items.filter((i) => i.section === section) }))
      .filter((g) => g.items.length > 0);
  }, [shown]);

  const addCount = shown?.counts.add ?? 0;
  const alreadyCount = shown?.counts.already_on ?? 0;
  const checkCount = shown?.counts.check_amount ?? 0;

  return (
    <div className="mx-auto w-full max-w-3xl" data-grocery-preview>
      <Button
        component={Link}
        href="/dashboard/meals"
        variant="subtle"
        leftSection={<IconArrowLeft size={18} stroke={ICON_STROKE} />}
        className="max-md:min-h-11"
        style={{ marginLeft: -8 }}
        mb="xs"
      >
        Week plan
      </Button>

      <Title order={2} className="text-xl font-semibold text-ink md:text-2xl" mb={4}>
        {applied ? "Added to Groceries" : "Add this week's dinners to Groceries"}
      </Title>
      <Text size="sm" style={{ color: "var(--ink-3)" }} mb="md">
        {shown
          ? `${shown.recipe_meals} ${shown.recipe_meals === 1 ? "dinner" : "dinners"} with recipes, ${shown.from} to ${shown.to}.`
          : `The week's dinners, merged into one grocery list.`}
      </Text>

      {loading && !shown && (
        <Stack gap="sm">
          <Skeleton height={64} radius="md" />
          {[0, 1, 2, 3, 4].map((i) => (
            <Skeleton key={i} height={56} radius="md" />
          ))}
        </Stack>
      )}

      {error && !shown && (
        <Card radius="token-md" p="md" withBorder role="alert" style={{ borderLeft: "4px solid var(--danger)" }}>
          <Text fw={600}>The grocery preview didn&apos;t load</Text>
          <Text size="sm" mt={4} style={{ color: "var(--ink-2)" }}>
            Nothing was written — your list is untouched. Home OS didn&apos;t answer.
          </Text>
          <Button variant="default" mt="sm" onClick={onRetry} className="max-md:min-h-11">
            Try again
          </Button>
        </Card>
      )}

      {shown && (
        <Stack gap="md">
          {/* The summary strip: what the confirm will do, in numbers. */}
          <Card
            radius="token-md"
            p="md"
            withBorder
            data-grocery-summary
            style={{ background: applied ? "var(--accent-soft)" : "var(--surface)" }}
          >
            <Group gap={10} align="flex-start" wrap="nowrap">
              {applied ? (
                <IconCheck size={20} stroke={ICON_STROKE} style={{ color: "var(--accent)", flex: "none", marginTop: 1 }} aria-hidden />
              ) : (
                <IconShoppingCart size={20} stroke={ICON_STROKE} style={{ color: "var(--accent)", flex: "none", marginTop: 1 }} aria-hidden />
              )}
              <div style={{ flex: 1, minWidth: 0 }}>
                <Text fw={600} size="sm" data-grocery-headline>
                  {applied
                    ? addCount > 0
                      ? `Added ${addCount} ${addCount === 1 ? "item" : "items"} to ${shown.list.name}`
                      : `Nothing new — all ${alreadyCount + checkCount} items were already on ${shown.list.name}`
                    : shown.list.exists
                      ? `Preview for ${shown.list.name} — nothing has been written yet`
                      : "Preview — Home OS will start a Groceries list for this"}
                </Text>
                <Group gap={8} mt={6} wrap="wrap">
                  <Badge size="sm" variant="light" color="teal" style={{ background: "var(--accent-soft)", color: "var(--accent)" }} data-count-add>
                    {addCount} to add
                  </Badge>
                  <Badge size="sm" variant="light" color="gray" data-count-already>
                    {alreadyCount} already on the list
                  </Badge>
                  {checkCount > 0 && (
                    <Badge
                      size="sm"
                      variant="light"
                      color="orange"
                      style={{ background: "var(--warn-bg)", color: "var(--warn-ink)" }}
                      data-count-check
                    >
                      {checkCount} check the amount
                    </Badge>
                  )}
                  {shown.counts.reopened > 0 && (
                    <Badge size="sm" variant="light" color="gray" data-count-reopened>
                      {shown.counts.reopened} back on the list
                    </Badge>
                  )}
                </Group>
                {!applied && (
                  <Text size="xs" mt={8} style={{ color: "var(--ink-3)" }}>
                    {shown.list.exists
                      ? "Someone in the family may be shopping from this list right now. Nothing changes until you confirm, and confirming twice changes nothing twice."
                      : "The first confirm starts the list, so it is ready when the family gets to the store."}
                  </Text>
                )}
              </div>
            </Group>
          </Card>

          {/* The items, by aisle. */}
          {grouped.length > 0 ? (
            <Card radius="token-md" p={0} withBorder style={{ background: "var(--surface)", overflow: "hidden" }}>
              {grouped.map((g, gi) => (
                <div key={g.section}>
                  <div
                    style={{
                      padding: "12px 14px 6px",
                      fontSize: 12,
                      fontWeight: 700,
                      letterSpacing: ".07em",
                      textTransform: "uppercase",
                      color: "var(--ink-2)",
                      background: "var(--surface-2)",
                      borderTop: gi === 0 ? "none" : "1px solid var(--line)",
                    }}
                    data-grocery-section={g.section}
                  >
                    {g.section}
                  </div>
                  <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                    {g.items.map((it, i) => (
                      <li
                        key={`${it.text}-${i}`}
                        data-grocery-item={it.status}
                        style={{
                          display: "grid",
                          gridTemplateColumns: "minmax(0,1fr) auto",
                          gap: 8,
                          alignItems: "baseline",
                          padding: "10px 14px",
                          borderTop: i === 0 ? "none" : "1px solid var(--line)",
                          minHeight: 44,
                        }}
                      >
                        <div style={{ minWidth: 0 }}>
                          <Group gap={6} wrap="wrap" align="baseline">
                            <Text size="sm" fw={500} style={{ overflowWrap: "anywhere" }}>
                              {it.text}
                            </Text>
                            <span
                              data-grocery-status={it.status}
                              style={{
                                display: "inline-flex",
                                alignItems: "center",
                                gap: 4,
                                fontSize: 12,
                                fontWeight: 600,
                                padding: "2px 8px",
                                borderRadius: 10,
                                background:
                                  it.status === "add"
                                    ? "var(--accent-soft)"
                                    : it.status === "check_amount"
                                      ? "var(--warn-bg)"
                                      : "var(--surface-2)",
                                color:
                                  it.status === "add"
                                    ? "var(--accent)"
                                    : it.status === "check_amount"
                                      ? "var(--warn-ink)"
                                      : "var(--ink-3)",
                                border: it.status === "check_amount" ? "1px solid var(--warn-line)" : "none",
                              }}
                            >
                              {it.status === "check_amount" && <IconAlertTriangle size={12} stroke={ICON_STROKE} aria-hidden />}
                              {it.status === "already_on_list" && <IconListCheck size={12} stroke={ICON_STROKE} aria-hidden />}
                              {STATUS_LABEL[it.status]}
                            </span>
                          </Group>
                          {/* Why it is here: which dinners asked for it. */}
                          {it.meals.length > 0 && (
                            <Text size="xs" mt={1} style={{ color: "var(--ink-3)", overflowWrap: "anywhere" }}>
                              {it.meals.join(" · ")}
                            </Text>
                          )}
                          {it.note && (
                            <Text size="xs" mt={1} style={{ color: "var(--ink-3)", overflowWrap: "anywhere" }}>
                              {it.note}
                            </Text>
                          )}
                          {it.status === "check_amount" && (
                            <Text size="xs" mt={2} style={{ color: "var(--warn-ink)", overflowWrap: "anywhere" }} data-check-amount>
                              The list says{" "}
                              <b>{amountText({ quantity: it.existing_quantity ?? null, unit: it.existing_unit ?? null }) || "a different amount"}</b>
                              ; this week needs <b>{amountText(it) || "more"}</b>. Only you know which to buy, so nothing
                              was changed.
                            </Text>
                          )}
                        </div>
                        <Text size="sm" fw={700} style={{ color: "var(--ink-2)", whiteSpace: "nowrap" }}>
                          {amountText(it)}
                        </Text>
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
            </Card>
          ) : (
            <Card radius="token-md" p="md" withBorder style={{ background: "var(--surface)" }} data-grocery-nothing>
              <Text fw={600} size="sm">
                Nothing to add from this week
              </Text>
              <Text size="sm" mt={2} style={{ color: "var(--ink-2)" }}>
                None of the planned dinners has a recipe with ingredients saved. Link a recipe to a dinner and its
                ingredients appear here.
              </Text>
              <Button component={Link} href="/dashboard/meals" variant="default" mt="sm" className="max-md:min-h-11">
                Back to the week plan
              </Button>
            </Card>
          )}

          {/* What was left out, and why — a short list must explain itself. */}
          {shown.skipped.length > 0 && (
            <div data-grocery-skipped>
              <Text size="xs" fw={700} tt="uppercase" style={{ color: "var(--ink-3)", letterSpacing: ".04em" }} mb={6}>
                Not added
              </Text>
              <Stack gap={6}>
                {shown.skipped.map((s) => (
                  <Group
                    key={`${s.date}-${s.slot}`}
                    gap={10}
                    align="flex-start"
                    wrap="nowrap"
                    style={{
                      padding: "10px 14px",
                      border: "1px dashed var(--line)",
                      borderRadius: "var(--r-sm)",
                      background: "var(--surface)",
                    }}
                  >
                    <IconInfoCircle size={16} stroke={ICON_STROKE} style={{ color: "var(--ink-3)", flex: "none", marginTop: 2 }} aria-hidden />
                    <Text size="sm" style={{ color: "var(--ink-2)", overflowWrap: "anywhere" }}>
                      <b>{s.title}</b> ({s.date} {s.slot}) — {s.reason}.
                    </Text>
                  </Group>
                ))}
              </Stack>
            </div>
          )}

          {shown.notes.map((n) => (
            <Text key={n} size="xs" style={{ color: "var(--ink-3)" }}>
              {n}
            </Text>
          ))}

          {/* The confirm. One tap writes; a second writes nothing new. */}
          {canWrite && grouped.length > 0 && (
            <Group gap={8} justify="flex-end" wrap="wrap">
              {applied ? (
                <>
                  <Button component={Link} href="/dashboard/lists" variant="default" className="max-md:min-h-11">
                    Open the list
                  </Button>
                  <Button component={Link} href="/dashboard/meals" className="max-md:min-h-11">
                    Back to the week plan
                  </Button>
                </>
              ) : (
                <>
                  <Button component={Link} href="/dashboard/meals" variant="default" className="max-md:min-h-11">
                    Cancel
                  </Button>
                  <Button
                    onClick={onConfirm}
                    loading={confirming}
                    disabled={addCount === 0}
                    leftSection={<IconShoppingCart size={16} stroke={ICON_STROKE} />}
                    className="max-md:min-h-11"
                    data-grocery-confirm
                  >
                    {addCount === 0
                      ? "Nothing new to add"
                      : `Add ${addCount} ${addCount === 1 ? "item" : "items"}${shown.list.exists ? "" : " and start the list"}`}
                  </Button>
                </>
              )}
            </Group>
          )}

          {/* A read-only role sees the preview and the reason it cannot act. */}
          {!canWrite && (
            <Text size="sm" style={{ color: "var(--ink-3)" }} data-grocery-readonly>
              Adding to the family&apos;s grocery list is for the family. You can see exactly what it would add — and the
              list itself, with its allergy notes, is on Lists.
            </Text>
          )}

          {/* The applied result says plainly that a re-run is harmless. */}
          {applied && addCount === 0 && (
            <Text size="xs" style={{ color: "var(--ink-3)" }} data-grocery-nothing-new>
              Nothing new was added: everything on this week&apos;s list was already there. That is what makes pressing
              the button twice safe.
            </Text>
          )}
        </Stack>
      )}
    </div>
  );
}
