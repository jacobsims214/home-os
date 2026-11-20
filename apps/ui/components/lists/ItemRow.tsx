"use client";

/**
 * One list item (#4566).
 *
 * THE TAP TARGET IS THE POINT OF THIS TICKET. A parent is holding a phone in
 * one hand, pushing a cart with the other, in a store with one bar of signal.
 * So:
 *
 * - THE WHOLE ROW IS THE CHECK TARGET in store mode (a `<button role="checkbox">`
 *   at 64 px), and in the normal list the checkbox is a 48 px target with the
 *   text as a second, separate target for editing — never a 16 px square.
 * - The row's `aria-checked` is the truth, so a screen reader announces the
 *   tick even though the write is still in flight.
 * - A checked row is struck through AND dimmed: never colour alone
 *   (accessibility-and-responsive: state by more than one channel).
 * - The name of who checked it is only ever the server's `checked_by_name`.
 *   An optimistic tick has none, so the row says "Checked just now" instead of
 *   guessing that the person holding the phone did it. See optimistic.ts.
 *
 * Sizes are px literals in the mockup's scale (48/52/56/64) rather than
 * Tailwind's spacing scale, because a tap target is a measured minimum, not a
 * spacing decision: `min-h-16` is 64 px only because the scale is 4 px based,
 * and a future theme change must not shrink it.
 */

import { IconCheck, IconClock } from "@tabler/icons-react";
import { amountLabel, checkedLabel, dueLabel } from "@/lib/lists/model";
import { beginCheckTap, tapPointOf } from "@/lib/lists/tap-guard";
import type { ListItem } from "@/lib/lists/types";

const STROKE = 1.75;

export interface ItemRowProps {
  item: ListItem;
  /** The list's kind: decides whether a due date / assignee is shown. */
  kind: string;
  /**
   * The FAMILY's today, YYYY-MM-DD (#5316) — null until the household is known,
   * and then there is no due chip: a chip derived from the browser's clock is
   * the defect this consolidation removed.
   */
  today: string | null;
  /** The assignee's display name, when the list assigns people. */
  assigneeName?: string | null;
  /** This role may check items (family + housesitter; a viewer may not). */
  canCheck: boolean;
  /** This role may edit or remove items (family only). */
  canEdit: boolean;
  /** Store mode: the entire row is one big target, no edit affordance. */
  store?: boolean;
  onToggle: (item: ListItem) => void;
  onEdit?: (item: ListItem) => void;
}

/** The little tick circle, shared by both layouts so the two cannot drift. */
function CheckBox({ checked, size }: { checked: boolean; size: "sm" | "lg" }) {
  return (
    <span
      aria-hidden
      className={[
        "grid shrink-0 place-items-center rounded-full border-2 transition-colors",
        size === "lg" ? "size-[30px]" : "size-6",
        checked ? "border-accent-fill bg-accent-fill text-white" : "border-ink-3 text-transparent",
      ].join(" ")}
    >
      <IconCheck size={size === "lg" ? 20 : 16} stroke={2.75} />
    </span>
  );
}

export default function ItemRow({
  item,
  kind,
  today,
  assigneeName,
  canCheck,
  canEdit,
  store = false,
  onToggle,
  onEdit,
}: ItemRowProps) {
  const amount = amountLabel(item);
  const choresLike = kind === "chores" || kind === "todo";
  const due = choresLike && item.due_date && !item.checked && today ? dueLabel(item.due_date, today) : "";
  const overdue = due.startsWith("Overdue");

  // What sits under the name: who it is for, and the check's who/when.
  const meta: string[] = [];
  if (item.checked) meta.push(checkedLabel(item));
  if (choresLike && assigneeName && !item.checked) meta.push(assigneeName);

  /**
   * One tap on the check-off (#5406).
   *
   * The row is guarded against a DOUBLE tap on the spot it just vacated: the
   * check-off removes the row instantly (the optimistic paint, #4566), so the
   * row below slides up under the finger and a second tap ~120 ms later would
   * check off a different item — silently marking an unbought item as bought.
   * `beginCheckTap` compares this tap with the last one on the list: same spot,
   * inside 500 ms, DIFFERENT item = the row moved, not a new intent. It is
   * called for both layouts and in store mode, because the guard belongs to the
   * row, not to a screen.
   */
  const toggle = (event: React.MouseEvent<HTMLButtonElement>) => {
    if (!canCheck) return;
    if (beginCheckTap(tapPointOf(item.list_id, item.id, event))) return;
    onToggle(item);
  };

  if (store) {
    return (
      // `data-item` is on the <li> in BOTH layouts, never on the control
      // itself: one place to find a row, and a locator like
      // `[data-item="x"] [role=checkbox]` means the same thing in each.
      <li data-item={item.id}>
        <button
          type="button"
          role="checkbox"
          aria-checked={item.checked}
          aria-label={`${item.checked ? "Uncheck" : "Check off"} ${item.text}`}
          disabled={!canCheck}
          onClick={toggle}
          className={[
            "flex min-h-16 w-full items-center gap-3.5 border-b border-line px-3.5 py-2 text-left transition-colors last:border-b-0",
            item.checked ? "bg-surface-2" : "bg-surface active:bg-surface-2",
            !canCheck ? "cursor-default" : "",
          ].join(" ")}
        >
          <CheckBox checked={item.checked} size="lg" />
          <span className="min-w-0 flex-1">
            <span
              className={[
                "block text-[18px] leading-tight [overflow-wrap:anywhere]",
                item.checked ? "text-ink-3 line-through decoration-1" : "font-medium text-ink",
              ].join(" ")}
            >
              {item.text}
            </span>
            {meta.length > 0 && <span className="mt-0.5 block text-[13px] text-ink-3">{meta.join(" · ")}</span>}
          </span>
          {amount && (
            <span
              className={[
                "shrink-0 rounded-token-sm px-2.5 py-1 text-[15px] font-semibold tabular-nums",
                item.checked ? "text-ink-3" : "bg-surface-2 text-ink",
              ].join(" ")}
            >
              {amount}
            </span>
          )}
        </button>
      </li>
    );
  }

  return (
    <li
      data-item={item.id}
      className="flex min-h-14 items-center gap-1 border-b border-line pr-3 last:border-b-0"
    >
      <button
        type="button"
        role="checkbox"
        aria-checked={item.checked}
        aria-label={`${item.checked ? "Uncheck" : "Check off"} ${item.text}`}
        disabled={!canCheck}
        onClick={toggle}
        className={[
          "grid size-12 shrink-0 place-items-center rounded-token-md",
          canCheck ? "hover:bg-surface-2" : "cursor-default opacity-45",
        ].join(" ")}
      >
        <CheckBox checked={item.checked} size="sm" />
      </button>
      {canEdit && onEdit ? (
        <button
          type="button"
          onClick={() => onEdit(item)}
          aria-label={`Edit ${item.text}`}
          className="min-h-[52px] min-w-0 flex-1 rounded-token-sm py-1.5 pr-1 text-left hover:bg-surface-2"
        >
          <ItemBody item={item} due={due} overdue={overdue} meta={meta} />
        </button>
      ) : (
        <div className="min-h-[52px] min-w-0 flex-1 py-1.5 pr-1">
          <ItemBody item={item} due={due} overdue={overdue} meta={meta} />
        </div>
      )}
      {amount && (
        <span
          className={[
            "shrink-0 pl-2 text-sm font-semibold tabular-nums",
            item.checked ? "text-ink-3" : "text-ink-2",
          ].join(" ")}
        >
          {amount}
        </span>
      )}
    </li>
  );
}

function ItemBody({
  item,
  due,
  overdue,
  meta,
}: {
  item: ListItem;
  due: string;
  overdue: boolean;
  meta: string[];
}) {
  return (
    <>
      <span
        className={[
          "block text-[15px] leading-snug [overflow-wrap:anywhere]",
          item.checked ? "text-ink-3 line-through decoration-1" : "font-medium text-ink",
        ].join(" ")}
      >
        {item.text}
      </span>
      {(meta.length > 0 || due) && (
        <span className="mt-0.5 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[12.5px] leading-tight text-ink-3">
          {due && (
            <span
              className={[
                "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-semibold",
                overdue ? "bg-warn-bg text-warn-ink" : "bg-surface-2 text-ink-2",
              ].join(" ")}
            >
              <IconClock size={13} stroke={STROKE} aria-hidden />
              {due}
            </span>
          )}
          {meta.map((line, i) => (
            <span key={i} className="[overflow-wrap:anywhere]">
              {i > 0 && <span aria-hidden> · </span>}
              {line}
            </span>
          ))}
        </span>
      )}
    </>
  );
}
