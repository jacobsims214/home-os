"use client";

/**
 * Category icons for the Documents hub (#4569).
 *
 * The icons are the mockup's own vocabulary (#4520), drawn with Tabler at the
 * shell's 1.75 stroke so a category tile in Documents reads like a section row
 * in the shell. Kept in one place because four surfaces use them: the hub's
 * tiles, the row's leading icon, the detail header and the upload form's
 * category picker. A new category is one entry here, and `CATEGORIES` in
 * lib/documents/model.ts is the list the API accepts — a category the API
 * refuses is never offered.
 */

import type { ComponentType } from "react";
import {
  IconBook,
  IconCar,
  IconCertificate,
  IconFile,
  IconFileInvoice,
  IconHome,
  IconReceipt,
  IconScale,
  IconSchool,
  IconShieldCheck,
  IconStethoscope,
  type IconProps,
} from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import { CATEGORY_SINGULAR, categoryOf, type Category } from "@/lib/documents/model";
import type { FileRecord } from "@/lib/types/api";

const ICONS: Record<Category, ComponentType<IconProps>> = {
  receipt: IconReceipt,
  tax: IconFileInvoice,
  insurance: IconShieldCheck,
  medical: IconStethoscope,
  manual: IconBook,
  warranty: IconCertificate,
  school: IconSchool,
  legal: IconScale,
  vehicle: IconCar,
  home: IconHome,
  other: IconFile,
};

/** The icon for a category (unknown → the plain file, like "other"). */
export function CategoryIcon({ category, size = 20 }: { category: Category | string; size?: number }) {
  const Icon = ICONS[category as Category] ?? ICONS.other;
  return <Icon size={size} stroke={NAV_ICON_STROKE} aria-hidden />;
}

/** A document's leading icon: its category's. */
export function documentIcon(file: Pick<FileRecord, "category">, size = 20) {
  return <CategoryIcon category={categoryOf(file)} size={size} />;
}

/** The singular label for a category, for a chip on one document. */
export function categoryLabel(category: Category | string): string {
  return CATEGORY_SINGULAR[category as Category] ?? CATEGORY_SINGULAR.other;
}
