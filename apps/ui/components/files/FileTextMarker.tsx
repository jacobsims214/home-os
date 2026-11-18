"use client";

/**
 * FileTextMarker — what a file's TEXT is, said honestly (#4564, #4941).
 *
 * Migration 072 added `files.text_origin`: `raw` is text stored exactly as it
 * was filed (the source), `extracted` is text the worker read from the bytes
 * (derived). The distinction exists because a $35 bill was filed as $5 with
 * the text re-typed to match, and the record was later reported as "verified".
 *
 * So a row must not present derived or changed text as the original document.
 * This component is the marker: a small, quiet chip whose label says which of
 * the three it is, and whose title (and the file view's sentence) says what
 * that means. It renders nothing when the file has no text — a file with no
 * text has nothing to claim either way.
 *
 * Wording lives in components/files/file-display.ts (tested by `npm test`), so
 * the row, the viewer and the tests cannot drift apart.
 */

import {
  describeFileText,
  textProvenance,
  textProvenanceLabel,
} from "@/components/files/file-display";
import type { FileRecord } from "@/lib/types/api";

const STYLES: Record<string, string> = {
  // The source itself: the calm, neutral one.
  source: "border-line bg-surface-2 text-ink-2",
  // Changed after filing: a warning, because a reader must not trust it.
  altered: "border-warn-line bg-warn-bg text-warn-ink",
  // Derived by OCR: informational, not alarming.
  derived: "border-line bg-surface text-ink-3",
};

export default function FileTextMarker({
  file,
  className = "",
}: {
  file: Pick<FileRecord, "text_origin" | "text_verified" | "extracted_text">;
  className?: string;
}) {
  const provenance = textProvenance(file);
  const label = textProvenanceLabel(provenance);
  if (!label) return null;

  const description = describeFileText(provenance);
  return (
    <span
      data-testid="file-text-marker"
      data-provenance={provenance}
      title={description}
      className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium leading-5 ${STYLES[provenance] ?? STYLES.derived} ${className}`}
    >
      {provenance === "altered" && (
        <svg className="h-3 w-3 shrink-0" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor" aria-hidden>
          <path strokeLinecap="round" strokeLinejoin="round" d="M12 9v3.75m0 3.75h.008v.008H12v-.008Z" />
        </svg>
      )}
      {label}
      {/* The tooltip is not available to a screen reader on a span, so the
          sentence is also the accessible description. */}
      <span className="sr-only">. {description}</span>
    </span>
  );
}
