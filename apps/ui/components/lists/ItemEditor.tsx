"use client";

/**
 * Add-with-details / edit one item (#4566).
 *
 * The quick-add box covers "2 lb ground beef" and Enter. This is where a person
 * sets the things the box cannot guess: the aisle, who is doing it, when it is
 * due, and the note ("85% lean"). It follows the mockup's "Add item" sheet.
 *
 * All fields are always sent, because PATCH is a partial by design but a form
 * that omits a cleared field would silently keep the old value: an empty aisle
 * or due date has to arrive as an explicit `null` to clear it. That is the same
 * trap the recipe form documents (`PUT` replaces) and the reason this form
 * builds the whole body every time.
 *
 * Performance note for the supermarket case: this sheet is NOT on the check-off
 * path. Ticking an item never opens a dialog; it is a one-tap write.
 */

import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { notifications } from "@mantine/notifications";
import { IconTrash } from "@tabler/icons-react";
import { ResponsiveModal, ResponsiveModalActions } from "@/components/ui";
import { apiFetch } from "@/lib/api";
import { listKeys } from "@/lib/query-keys";
import { AISLES } from "@/lib/lists/model";
import type { ListItem, PersonRow } from "@/lib/lists/types";

const STROKE = 1.75;
const FIELD =
  "h-11 w-full rounded-token-sm border border-line bg-surface px-3 text-[16px] text-ink placeholder:text-ink-3 focus:border-transparent focus:outline focus:outline-2 focus:outline-accent [focus:outline-offset:-1px]";

export interface ItemEditorProps {
  opened: boolean;
  /** Null: a new item. */
  item: ListItem | null;
  kind: string;
  listId: string;
  people: PersonRow[];
  onClose: () => void;
}

export default function ItemEditor({ opened, item, kind, listId, people, onClose }: ItemEditorProps) {
  const queryClient = useQueryClient();
  const [text, setText] = useState("");
  const [quantity, setQuantity] = useState("");
  const [unit, setUnit] = useState("");
  const [section, setSection] = useState("");
  const [assignee, setAssignee] = useState("");
  const [dueDate, setDueDate] = useState("");

  // Reset the form whenever the sheet opens, so a previous item's values can
  // never leak into this one.
  useEffect(() => {
    if (!opened) return;
    setText(item?.text ?? "");
    setQuantity(item?.quantity != null ? String(item.quantity) : "");
    setUnit(item?.unit ?? "");
    setSection(item?.section ?? "");
    setAssignee(item?.assignee_person_id ?? "");
    setDueDate(item?.due_date ?? "");
  }, [opened, item]);

  const isGrocery = kind === "grocery" || kind === "shopping";
  const choresLike = kind === "chores" || kind === "todo";

  /** Every field, every time: an empty one has to clear, not be omitted. */
  const body = () => ({
    text: text.trim(),
    quantity: quantity.trim() ? Number(quantity) : null,
    unit: unit.trim() || null,
    section: section.trim() || null,
    assignee_person_id: assignee || null,
    due_date: dueDate || null,
  });

  const save = useMutation<{ data: ListItem | ListItem[] }>({
    mutationFn: async () =>
      item
        ? apiFetch<{ data: ListItem }>(`/api/v1/lists/${listId}/items/${item.id}`, { method: "PATCH", body: body() })
        : apiFetch<{ data: ListItem[] }>(`/api/v1/lists/${listId}/items`, { method: "POST", body: { items: [body()] } }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: listKeys.feed(listId) });
      void queryClient.invalidateQueries({ queryKey: listKeys.allRows() });
      notifications.show({ message: item ? "Item saved" : "Item added", color: "teal" });
      onClose();
    },
    onError: (err) => {
      notifications.show({ message: err instanceof Error ? err.message : "That didn't save", color: "red" });
    },
  });

  const remove = useMutation({
    mutationFn: () =>
      apiFetch<void>(`/api/v1/lists/${listId}/items/${item!.id}`, { method: "DELETE" }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: listKeys.feed(listId) });
      void queryClient.invalidateQueries({ queryKey: listKeys.allRows() });
      notifications.show({ message: "Item removed", color: "teal" });
      onClose();
    },
  });

  return (
    <ResponsiveModal opened={opened} onClose={onClose} title={item ? "Edit item" : "Add item"}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (text.trim()) save.mutate();
        }}
      >
        <div className="flex flex-col gap-4">
          <div>
            <label htmlFor="ie-text" className="text-[13px] font-semibold text-ink-2">
              Item
            </label>
            <input
              id="ie-text"
              value={text}
              onChange={(e) => setText(e.target.value)}
              data-autofocus
              required
              className={`mt-1.5 ${FIELD}`}
            />
          </div>

          <div className="grid grid-cols-2 gap-2.5">
            <div>
              <label htmlFor="ie-qty" className="text-[13px] font-semibold text-ink-2">
                Quantity
              </label>
              <input
                id="ie-qty"
                type="number"
                inputMode="decimal"
                step="any"
                min="0"
                value={quantity}
                onChange={(e) => setQuantity(e.target.value)}
                className={`mt-1.5 ${FIELD} tabular-nums`}
              />
            </div>
            <div>
              <label htmlFor="ie-unit" className="text-[13px] font-semibold text-ink-2">
                Unit
              </label>
              <input
                id="ie-unit"
                value={unit}
                onChange={(e) => setUnit(e.target.value)}
                placeholder="lb, oz, box"
                className={`mt-1.5 ${FIELD}`}
              />
            </div>
          </div>

          {isGrocery && (
            <div>
              <label htmlFor="ie-section" className="text-[13px] font-semibold text-ink-2">
                Aisle
              </label>
              <select
                id="ie-section"
                value={section}
                onChange={(e) => setSection(e.target.value)}
                className={`mt-1.5 ${FIELD}`}
              >
                <option value="">Other</option>
                {AISLES.map((aisle) => (
                  <option key={aisle} value={aisle}>
                    {aisle}
                  </option>
                ))}
                {/* An aisle this build doesn't know (from a phone, or a
                    hand-written section) stays selectable rather than being
                    silently rewritten to Other on save. */}
                {section && !(AISLES as readonly string[]).includes(section) && (
                  <option value={section}>{section}</option>
                )}
              </select>
            </div>
          )}

          {choresLike && (
            <>
              <div>
                <label htmlFor="ie-who" className="text-[13px] font-semibold text-ink-2">
                  Who&rsquo;s doing it
                </label>
                <select
                  id="ie-who"
                  value={assignee}
                  onChange={(e) => setAssignee(e.target.value)}
                  className={`mt-1.5 ${FIELD}`}
                >
                  <option value="">Anyone</option>
                  {people.map((person) => (
                    <option key={person.id} value={person.id}>
                      {person.display_name}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label htmlFor="ie-due" className="text-[13px] font-semibold text-ink-2">
                  Due
                </label>
                <input
                  id="ie-due"
                  type="date"
                  value={dueDate.slice(0, 10)}
                  onChange={(e) => setDueDate(e.target.value)}
                  className={`mt-1.5 max-w-[220px] ${FIELD}`}
                />
              </div>
            </>
          )}
        </div>

        <ResponsiveModalActions>
          {item && (
            <button
              type="button"
              onClick={() => remove.mutate()}
              disabled={remove.isPending}
              className="mr-auto inline-flex min-h-11 items-center gap-1.5 rounded-token-sm px-2 text-sm font-semibold text-danger disabled:opacity-50"
            >
              <IconTrash size={18} stroke={STROKE} aria-hidden />
              Remove
            </button>
          )}
          <button type="button" onClick={onClose} className="min-h-11 rounded-token-sm px-4 text-sm font-semibold text-ink-2">
            Cancel
          </button>
          <button
            type="submit"
            disabled={!text.trim() || save.isPending}
            className="min-h-11 rounded-token-sm bg-accent-fill px-4 text-sm font-semibold text-white disabled:opacity-50"
          >
            {item ? "Save" : "Add"}
          </button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}
