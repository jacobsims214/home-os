"use client";

/**
 * PropertyAttention (#4578, the mockup's "Needs attention"): this property's
 * open upkeep that is past its due date. Each row names the task and the thing
 * it is about, and links to that thing's page when it has one; a whole-house
 * task (no asset) is shown without a link, because there is no task page.
 *
 * The overdue rule lives in property-tree.ts (`isOverdue`), which `node --test`
 * covers, so "5 days overdue" and the count on a room card cannot disagree.
 */

import Link from "next/link";
import { IconAlertTriangle, IconChevronRight } from "@tabler/icons-react";
import { overdueTasks, type TreeTask } from "./property-tree";
import type { Asset } from "@/lib/types/api";

const STROKE = 1.75;

function daysOverdue(dueDate: string, now: Date): number {
  const due = new Date(`${dueDate.slice(0, 10)}T00:00:00`);
  const today = new Date(`${now.toISOString().slice(0, 10)}T00:00:00`);
  return Math.max(1, Math.round((today.getTime() - due.getTime()) / 86_400_000));
}

interface PropertyAttentionProps {
  tasks: TreeTask[];
  things: Asset[];
  /** The property's name, for a task about the whole house. */
  propertyName: string;
  /** "View all upkeep" goes to the maintenance page. */
  now?: Date;
}

export default function PropertyAttention({ tasks, things, propertyName, now = new Date() }: PropertyAttentionProps) {
  const overdue = overdueTasks(tasks, now);
  if (overdue.length === 0) return null;
  const thingById = new Map(things.map((t) => [t.id, t]));

  return (
    <section aria-labelledby="property-attention" className="min-w-0">
      <div className="mb-2 flex items-baseline justify-between gap-3">
        <h2 id="property-attention" className="flex min-w-0 items-center gap-1.5 text-sm font-semibold text-warn-ink">
          <IconAlertTriangle size={16} stroke={2} aria-hidden />
          Needs attention
          <span className="font-medium text-ink-3">{overdue.length}</span>
        </h2>
        <Link
          href="/dashboard/maintenance"
          className="shrink-0 py-2 text-sm font-semibold text-accent hover:underline"
        >
          All upkeep
        </Link>
      </div>
      <ul
        data-attention-list
        className="divide-y divide-line rounded-token-md border border-line bg-surface shadow-token-sm"
      >
        {overdue.map((task) => {
          const thing = task.asset_id ? thingById.get(task.asset_id) : undefined;
          const days = task.due_date ? daysOverdue(task.due_date, now) : 0;
          const subject = [thing?.name, thing ? null : propertyName].filter(Boolean).join(" · ");
          const body = (
            <>
              <span className="min-w-0 flex-1">
                <span className="block text-sm font-medium text-ink [overflow-wrap:anywhere]">{task.name}</span>
                <span className="block text-xs text-ink-3 [overflow-wrap:anywhere]">{subject}</span>
              </span>
              <span className="shrink-0 text-right">
                <span className="block text-xs font-semibold text-warn-ink">
                  {days === 1 ? "1 day overdue" : `${days} days overdue`}
                </span>
              </span>
              {thing && <IconChevronRight size={18} stroke={STROKE} className="shrink-0 text-ink-3" aria-hidden />}
            </>
          );
          return (
            <li key={task.id}>
              {thing ? (
                <Link
                  href={`/dashboard/assets/${thing.id}`}
                  className="flex min-h-11 items-center gap-2 px-4 py-2.5 transition-colors hover:bg-surface-2 active:bg-surface-2"
                >
                  {body}
                </Link>
              ) : (
                <div className="flex min-h-11 items-center gap-2 px-4 py-2.5">{body}</div>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
