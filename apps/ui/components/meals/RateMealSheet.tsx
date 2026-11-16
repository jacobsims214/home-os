"use client";

/**
 * "How was it?" — rate a planned meal, per person (#4568).
 *
 * The mockup's "Rate a meal" state (#106 v3), and the manager's roster rule:
 * one row per person, 1-5 plus an optional comment. Owners and family managers
 * rate for anyone because kids have no login; a family_member rates only as
 * their own person. The API enforces the rule (meals.Policy) — this sheet only
 * offers what the role can actually do, so nobody types four ratings and then
 * gets three 403s.
 *
 * Uses the recipe detail page's `useRateMeal` (PUT /meals/{id}/feedback/{personId}
 * upserts one row per person per meal), so there is one implementation of the
 * rating write, not two.
 */

import { useState } from "react";
import { Button, Divider, Drawer, Group, Rating, Stack, Text, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useRateMeal } from "@/lib/recipes/api";
import type { Meal, Person } from "@/lib/recipes/types";

export interface RateMealSheetProps {
  opened: boolean;
  onClose: () => void;
  meal: Meal | null;
  /** "Mon, November 16". */
  dayLabel: string;
  people: Person[];
  /** True for owner/family_manager: they may rate for kids. */
  canRateOthers: boolean;
  /** The signed-in person, when the role may only rate for themselves. */
  selfPersonId?: string | null;
}

export default function RateMealSheet({
  opened,
  onClose,
  meal,
  dayLabel,
  people,
  canRateOthers,
  selfPersonId,
}: RateMealSheetProps) {
  const rate = useRateMeal(meal?.id ?? "");
  const [values, setValues] = useState<Record<string, number>>({});
  const [comment, setComment] = useState("");
  const [error, setError] = useState<string | null>(null);

  // Who gets a row: everyone for a parent, only yourself for a family_member.
  const rows = canRateOthers ? people : people.filter((p) => p.id === selfPersonId);

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
      setValues({});
      setComment("");
      onClose();
    } catch {
      setError("Could not save the ratings. Try again in a moment.");
    }
  };

  return (
    <Drawer
      opened={opened}
      onClose={() => {
        setError(null);
        onClose();
      }}
      position="bottom"
      size="auto"
      title="How was it?"
      radius="token-lg"
      data-rate-sheet
      styles={{
        title: { fontWeight: 600, fontSize: 17 },
        body: { paddingBottom: "calc(16px + env(safe-area-inset-bottom))" },
      }}
    >
      {/* The marker goes on the visible body; Drawer's root is a hidden wrapper. */}
      <Stack gap="sm" data-rate-body>
        <Text size="sm" style={{ color: "var(--ink-3)", marginTop: 0 }}>
          {dayLabel}: <b style={{ color: "var(--ink)" }}>{meal?.title ?? "dinner"}</b>. Ratings help the family&apos;s
          suggestions pick better dinners — one rating per person, kids included.
        </Text>

        {rows.map((p) => (
          <Group key={p.id} justify="space-between" wrap="nowrap" gap="sm" style={{ minHeight: 44 }}>
            <div style={{ minWidth: 0 }}>
              <Text size="sm" fw={600} style={{ overflowWrap: "anywhere" }}>
                {p.display_name}
              </Text>
              {!p.has_login && (
                <Text size="xs" style={{ color: "var(--ink-3)" }}>
                  no login — rate for them
                </Text>
              )}
            </div>
            {/*
              Each star is its own accessible control: Mantine renders one radio
              per star and labels it from getSymbolLabel, so "4 stars for Nora"
              is what a screen reader (and the e2e) actually finds. Passing one
              aria-label on <Rating> would name none of them.
            */}
            <Rating
              value={values[p.id] ?? 0}
              onChange={(v) => {
                setError(null);
                setValues((prev) => ({ ...prev, [p.id]: v }));
              }}
              size="lg"
              getSymbolLabel={(v) => `${v} ${v === 1 ? "star" : "stars"} for ${p.display_name}`}
              data-rate-person={p.id}
            />
          </Group>
        ))}

        {!canRateOthers && (
          <Text size="xs" style={{ color: "var(--ink-3)" }}>
            You can rate as yourself; a parent can rate for everyone.
          </Text>
        )}

        <Textarea
          label="Comment (optional, applies to your rating)"
          value={comment}
          onChange={(e) => setComment(e.currentTarget.value)}
          autosize
          minRows={2}
          placeholder="More peppers next time"
          className="max-md:[&_input]:text-base"
        />

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
          <Button onClick={submit} loading={rate.isPending} disabled={!meal} className="max-md:min-h-11">
            Save ratings
          </Button>
        </Group>
      </Stack>
    </Drawer>
  );
}
