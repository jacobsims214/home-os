"use client";

/**
 * The grocery preview page (#4568), at /dashboard/meals/grocery.
 *
 * Manager decision on #4519 (#13046) is why this is a PAGE and not a dialog:
 * "a full preview page before writing, because the write goes into a shared
 * list another family member may be using in store mode right now. Must show
 * Add / On the list / Check amount per item, and the write must be idempotent
 * server-side."
 *
 * So: open the page, read every row, then confirm. `?from=&to=` come from the
 * week plan (the week the person was looking at); without them it falls back to
 * this week's dinners, so the URL works when typed.
 *
 * The preview is a GET that writes nothing. The confirm is a POST that is safe
 * to repeat: lists.Repo.MergeItems skips anything whose words already match an
 * open row and reopens a checked one, and the response reports what really
 * happened per row, so the page shows the TRUTH after the write rather than an
 * optimistic guess.
 */

import { useMemo, useState } from "react";
import { useSearchParams } from "next/navigation";
import dayjs from "dayjs";
import Link from "next/link";
import { Button, Card, Group, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle } from "@tabler/icons-react";
import GroceryPreview from "@/components/meals/GroceryPreview";
import { useApplyGrocery, useGroceryPreview } from "@/lib/recipes/api";
import { useNavRole } from "@/components/shared/use-nav-role";
import { addDays, weekStart } from "@/lib/recipes/week";
import type { GroceryResult } from "@/lib/recipes/types";

const ICON_STROKE = 1.75;
const WRITER_ROLES = new Set(["owner", "family_manager", "family_member"]);

export default function GroceryPage() {
  const role = useNavRole();
  const canWrite = role == null ? true : WRITER_ROLES.has(role);
  const params = useSearchParams();

  // The range comes from the week plan. Defaults: this week's dinners, worked
  // out in the browser so it matches the week the person just looked at.
  const range = useMemo(() => {
    const from = params?.get("from") ?? "";
    const to = params?.get("to") ?? "";
    if (/^\d{4}-\d{2}-\d{2}$/.test(from) && /^\d{4}-\d{2}-\d{2}$/.test(to)) return { from, to };
    const start = weekStart(dayjs().format("YYYY-MM-DD"));
    return { from: start, to: addDays(start, 6) };
  }, [params]);
  const slots = useMemo(() => ["dinner"], []);

  const previewQuery = useGroceryPreview({ ...range, slots });
  const apply = useApplyGrocery();
  const [applied, setApplied] = useState<GroceryResult | null>(null);

  const onConfirm = async () => {
    try {
      const res = await apply.mutateAsync({ ...range, slots });
      setApplied(res.data);
      const { add, already_on: already, check_amount: check } = res.data.counts;
      notifications.show({
        message:
          add > 0
            ? `Added ${add} ${add === 1 ? "item" : "items"} to ${res.data.list.name}` +
              (already > 0 ? ` · ${already} already there` : "") +
              (check > 0 ? ` · ${check} to check` : "")
            : `Nothing new — all ${already + check} items were already on the list`,
        color: add > 0 ? "teal" : "gray",
      });
    } catch {
      notifications.show({ message: "Could not add to the grocery list. Nothing was changed twice — try again.", color: "red" });
    }
  };

  const preview = previewQuery.data?.data ?? null;
  // A named list that is gone or archived answers 404/409: the page says so and
  // offers the default list rather than dumping a raw error.
  const listMissing = previewQuery.isError && isListProblem(previewQuery.error);
  const backHref = `/dashboard/meals?week=${range.from}`;

  return (
    <div className="mx-auto w-full max-w-3xl">
      {listMissing && (
        <Card radius="token-md" p="md" withBorder mb="md" role="alert" style={{ borderLeft: "4px solid var(--warn-line)", background: "var(--warn-bg)" }}>
          <Group gap={10} align="flex-start" wrap="nowrap">
            <IconAlertTriangle size={20} stroke={ICON_STROKE} style={{ color: "var(--warn-ink)", flex: "none", marginTop: 1 }} aria-hidden />
            <div>
              <Text fw={600} size="sm" style={{ color: "var(--warn-ink)" }}>
                That grocery list isn&apos;t available
              </Text>
              <Text size="sm" mt={2} style={{ color: "var(--warn-ink)" }}>
                It may have been archived or removed. Nothing was written.
              </Text>
              <Button component={Link} href="/dashboard/meals/grocery" variant="default" mt="sm" className="max-md:min-h-11">
                Use the default list
              </Button>
            </div>
          </Group>
        </Card>
      )}

      <GroceryPreview
        preview={preview}
        loading={previewQuery.isLoading}
        error={previewQuery.isError && !listMissing}
        onRetry={() => previewQuery.refetch()}
        onConfirm={onConfirm}
        confirming={apply.isPending}
        canWrite={canWrite}
        applied={applied}
      />

      <noscript>
        <Title order={3} size="h4" mt="lg">
          JavaScript is off
        </Title>
        <Text size="sm">
          The preview needs JavaScript to add the week&apos;s dinners. Nothing has been written; open{" "}
          <Link href={backHref}>the week plan</Link> to see what is planned.
        </Text>
      </noscript>
    </div>
  );
}

/** A 404/409 from the preview is about the list, not about the network. */
function isListProblem(error: unknown): boolean {
  const status = (error as { status?: number } | null)?.status;
  return status === 404 || status === 409;
}
