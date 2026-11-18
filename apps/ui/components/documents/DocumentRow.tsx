"use client";

/**
 * One document, as a row (#4569). Used by the hub's lists everywhere: expiring
 * soon, recently added, a category view and a search-adjacent list.
 *
 * What a row says, and why:
 *  - the title the API calls the document by (title, else the file name);
 *  - the facts a person needs to recognise it: kind, size, date, who filed it;
 *  - its category and, when it has one, how soon it expires — stated as a
 *    relative phrase AND checked against the calendar day, because "Expires in
 *    14 days" is the thing a person acts on;
 *  - what its TEXT is (`FileTextMarker`, shared with the entity pages). A
 *    re-typed or OCR-read body must never read as the original document
 *    (#4941): the marker carries the sentence, and the detail page repeats it.
 *
 * Phone (390 px, the primary width): one tap target of 64 px+, the title wraps
 * to two lines rather than truncating to nothing, the chips wrap, and the whole
 * row is the link (no chevron to aim at).
 */

import Link from "next/link";
import { Text } from "@mantine/core";
import { IconAlertTriangle, IconChevronRight, IconClock } from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import FileTextMarker from "@/components/files/FileTextMarker";
import { CategoryIcon, categoryLabel } from "@/components/documents/categories";
import {
  categoryOf,
  displayTitle,
  expiryOf,
  rowMeta,
  type Expiry,
} from "@/lib/documents/model";
import type { FileRecord } from "@/lib/types/api";

const ICON_STROKE = NAV_ICON_STROKE;

/** The relative expiry as a chip: warning for soon, danger for past. */
export function ExpiryChip({ expiry }: { expiry: Expiry }) {
  if (expiry.state === "none" || !expiry.label) return null;
  const soon = expiry.state === "soon";
  const expired = expiry.state === "expired";
  if (!soon && !expired) {
    // A far-off date is a fact, not a warning: keep it as quiet text.
    return (
      <span className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[12px] leading-5" style={{ background: "var(--surface-2)", color: "var(--ink-2)" }}>
        <IconClock size={13} stroke={ICON_STROKE} aria-hidden />
        {expiry.label}
      </span>
    );
  }
  const style = expired
    ? { background: "var(--danger-soft, #fff5f5)", color: "var(--danger)", boxShadow: "inset 0 0 0 1px var(--danger-line, #ffc9c9)" }
    : { background: "var(--warn-bg)", color: "var(--warn-ink)" };
  return (
    <span className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[12px] font-semibold leading-5" style={style} data-expiry={expiry.state}>
      {expired ? <IconAlertTriangle size={13} stroke={ICON_STROKE} aria-hidden /> : <IconClock size={13} stroke={ICON_STROKE} aria-hidden />}
      {expiry.label}
    </span>
  );
}

export default function DocumentRow({
  file,
  today,
}: {
  file: FileRecord;
  /** Injected in tests so the relative expiry is stable. */
  today?: string;
}) {
  const category = categoryOf(file);
  const expiry = expiryOf(file, today);
  const meta = rowMeta(file);
  return (
    <Link
      href={`/dashboard/documents/${file.id}`}
      data-document-row={file.id}
      data-category={category}
      style={{
        display: "flex",
        alignItems: "center",
        gap: 12,
        minHeight: 64,
        padding: "10px 10px 10px 14px",
        borderBottom: "1px solid var(--line)",
        color: "inherit",
        textDecoration: "none",
      }}
      className="hover:bg-surface-2"
    >
      <span
        aria-hidden
        style={{
          width: 36,
          height: 36,
          flex: "none",
          display: "grid",
          placeItems: "center",
          borderRadius: 10,
          background: category === "other" ? "var(--surface-2)" : "var(--accent-soft)",
          color: category === "other" ? "var(--ink-2)" : "var(--accent)",
        }}
      >
        <CategoryIcon category={category} size={18} />
      </span>
      <span style={{ flex: 1, minWidth: 0 }}>
        <Text fw={600} size="md" style={{ lineHeight: 1.3, overflowWrap: "anywhere", display: "block" }} data-document-title>
          {displayTitle(file)}
        </Text>
        <span style={{ display: "block", fontSize: 13, color: "var(--ink-3)", lineHeight: 1.4, overflowWrap: "anywhere" }}>{meta}</span>
        <span style={{ display: "flex", flexWrap: "wrap", gap: 4, marginTop: 5 }}>
          <span className="inline-flex items-center rounded-full px-2 py-0.5 text-[12px] font-medium leading-5" style={{ background: "var(--surface-2)", color: "var(--ink-2)" }}>
            {categoryLabel(category)}
          </span>
          <ExpiryChip expiry={expiry} />
          <FileTextMarker file={file} />
        </span>
      </span>
      <IconChevronRight size={18} stroke={ICON_STROKE} aria-hidden style={{ color: "var(--ink-3)", flex: "none" }} />
    </Link>
  );
}
