"use client";

/**
 * A list kind's icon and tile (#4566).
 *
 * The mockup (artifact #88 v1) gives each kind its own hue, reusing the
 * calendar's four colours. Those four tokens do not exist in this build
 * (`app/globals.css` has no --cal-*), and inventing four hues in a page would
 * fail the token rule the theme test enforces (app/theme-tokens.test.mjs:
 * "shared components use tokens, not hard-coded light colours").
 *
 * So a kind is told apart by its ICON, in the one accent colour, which is what
 * the rest of the app does (vehicles, pets, assets all use
 * `bg-accent-soft text-accent`). That is a deliberate difference from the
 * mockup, recorded in the completion summary: it keeps one accent (visual
 * craft: "mostly neutrals, one accent") instead of shipping four unvalidated
 * hues, and the label is always next to the icon anyway, so nothing depends on
 * telling the hues apart — which colour alone never should.
 */

import {
  IconBackpack,
  IconBrush,
  IconChecklist,
  IconList,
  IconShoppingBag,
  IconShoppingCart,
  type Icon,
} from "@tabler/icons-react";
import { kindLabel } from "@/lib/lists/model";
import type { ListKind } from "@/lib/lists/types";

const STROKE = 1.75;

const KIND_ICONS: Record<ListKind, Icon> = {
  grocery: IconShoppingCart,
  packing: IconBackpack,
  chores: IconBrush,
  todo: IconChecklist,
  shopping: IconShoppingBag,
  other: IconList,
};

/** The kind's icon; an unknown kind gets the generic list icon. */
export function kindIcon(kind: string): Icon {
  return KIND_ICONS[kind as ListKind] ?? IconList;
}

/** The kind tile: an icon on the accent-soft square, sized for a card or a header. */
export function KindTile({ kind, small = false }: { kind: string; small?: boolean }) {
  const Glyph = kindIcon(kind);
  return (
    <span
      aria-hidden
      data-list-kind={kind}
      className={
        small
          ? "grid size-8 shrink-0 place-items-center rounded-token-sm bg-accent-soft text-accent"
          : "grid size-10 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent"
      }
    >
      <Glyph size={small ? 18 : 21} stroke={STROKE} />
    </span>
  );
}

/** The kind's label. Exported through the component so a page never repeats the table. */
export { kindLabel };
