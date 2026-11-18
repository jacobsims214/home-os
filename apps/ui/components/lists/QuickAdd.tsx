"use client";

/**
 * The quick-add box (#4566), one line and Enter.
 *
 * A person types "2 lb ground beef" and presses Enter. The preview under the
 * box says what was understood BEFORE it is saved — "Ground beef · 2 lb · Meat
 * & seafood" — because that is the only moment they can tell the parser got it
 * right. It is a `<span>` in an `aria-live="polite"` region, not a toast: the
 * person is typing, and a toast would sit over the field they are typing in.
 *
 * The parse is `parseQuickAdd` in lib/lists/model.ts (tested). It never
 * invents a quantity: an unrecognised amount stays part of the name.
 *
 * The box is 48 px tall with 16 px text on phones: under 16 px, iOS zooms the
 * page when the field is focused, and this is the field a person focuses while
 * walking. The section is the guess; the item editor is where it can be fixed.
 */

import { useId, useState } from "react";
import { IconPlus } from "@tabler/icons-react";
import { parseQuickAdd } from "@/lib/lists/model";

const STROKE = 1.75;

export interface QuickAddProps {
  placeholder: string;
  /** Send one parsed line. Resolves false to keep the text for a retry. */
  onAdd: (parsed: ReturnType<typeof parseQuickAdd>) => Promise<boolean> | boolean;
  disabled?: boolean;
}

export default function QuickAdd({ placeholder, onAdd, disabled = false }: QuickAddProps) {
  const [value, setValue] = useState("");
  const [saving, setSaving] = useState(false);
  const inputId = useId();
  const parsed = parseQuickAdd(value);

  const submit = async () => {
    if (!value.trim() || saving) return;
    setSaving(true);
    try {
      const ok = await onAdd(parsed);
      // Keep the text when the write failed: a dropped request in a basement
      // aisle must not also lose what the person typed.
      if (ok !== false) setValue("");
    } finally {
      setSaving(false);
    }
  };

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <div className="flex items-center gap-2">
        <label htmlFor={inputId} className="sr-only">
          {placeholder}
        </label>
        <input
          id={inputId}
          type="text"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder={placeholder}
          autoComplete="off"
          enterKeyHint="done"
          disabled={disabled || saving}
          data-quick-add
          className="h-12 min-w-0 flex-1 rounded-token-md border border-line bg-surface px-3 text-[16px] text-ink placeholder:text-ink-3 focus:border-transparent focus:outline focus:outline-2 focus:outline-accent [focus:outline-offset:-1px] disabled:opacity-60"
        />
        <button
          type="submit"
          disabled={!value.trim() || disabled || saving}
          aria-label="Add item"
          className="grid size-12 shrink-0 place-items-center rounded-token-md bg-accent-fill text-white transition-opacity disabled:opacity-40"
        >
          <IconPlus size={22} stroke={2} />
        </button>
      </div>
      {/* What the parser understood, live, so a wrong amount is caught before
          it is saved. aria-live so a screen reader hears it too. */}
      <div aria-live="polite">
        {parsed.text && (
          <p className="mt-2 flex flex-wrap items-center gap-1.5 text-xs text-ink-3" data-quick-add-preview>
            <span>Adds</span>
            <span className="rounded-full bg-surface-2 px-2 py-0.5 font-medium text-ink-2">{parsed.text}</span>
            {parsed.quantity !== null && (
              <span className="rounded-full bg-surface-2 px-2 py-0.5 font-medium text-ink-2">
                {parsed.quantity}
                {parsed.unit ? ` ${parsed.unit}` : ""}
              </span>
            )}
            {parsed.section && (
              <span className="rounded-full bg-surface-2 px-2 py-0.5 font-medium text-ink-2">{parsed.section}</span>
            )}
          </p>
        )}
      </div>
    </form>
  );
}
