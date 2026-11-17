/**
 * Display helpers for a file row (#4564): size, date, and — the important one
 * — what the file's TEXT actually is.
 *
 * Why provenance is a display concern, not a footnote. Migration 072 (#4941)
 * added `files.text_origin`: `raw` means the body is the caller's own pasted
 * text stored unaltered (it is the SOURCE a recorded amount can be checked
 * against), `extracted` means the worker read it from the blob (it is DERIVED
 * and may differ from the document). The distinction exists because a $35 bill
 * was filed as $5 with the text re-typed to match: every artifact then said
 * $5 and the mistake was reported as "verified".
 *
 * So the UI must never present derived or altered text as the original
 * document. `textProvenance()` is the single place that decides what a row may
 * claim, and `describeFileText()` turns it into the sentence a person reads.
 * A file whose body changed after filing (text_verified false) must not say
 * "verbatim" — it says so plainly.
 *
 * Plain functions only (no JSX), so `npm test` covers the wording.
 */

import type { FileRecord } from "@/lib/types/api";

/** Human-readable byte size: 1.2 KB / 3.4 MB / 512 bytes. */
export function formatBytes(bytes: number | null | undefined): string {
  if (!bytes || bytes < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  const value = bytes / Math.pow(1024, i);
  const formatted = i === 0 ? value.toString() : value.toFixed(1);
  return `${formatted} ${units[i]}`;
}

/** Short locale-formatted date ("Jun 5, 2026"). Falls back to the raw ISO. */
export function formatDate(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });
}

/** "by Amber Sims" / "by a former member" / "" when unknown. */
export function uploaderLine(file: Pick<FileRecord, "uploaded_by_name">): string {
  const name = file.uploaded_by_name?.trim();
  if (!name) return "";
  return `by ${name}`;
}

/**
 * Where a file's text came from, as far as the UI is allowed to claim:
 * - "source":   text_origin raw AND the stored body still matches its
 *               fingerprint. It is the text that was filed, byte for byte.
 * - "altered":  text_origin raw but the body no longer matches its
 *               fingerprint: someone changed it after filing. Never call this
 *               the original.
 * - "derived":  text_origin extracted (or absent, which the column's default
 *               makes extracted): read from the bytes by OCR, may differ from
 *               the document.
 * - "none":     the file has no text at all.
 */
export type TextProvenance = "source" | "altered" | "derived" | "none";

export function textProvenance(
  file: Pick<FileRecord, "text_origin" | "text_verified" | "extracted_text">,
): TextProvenance {
  const hasText = typeof file.extracted_text === "string" && file.extracted_text.length > 0;
  // No text at all: nothing to claim either way, so no marker.
  if (!hasText) return "none";
  if (file.text_origin === "raw") {
    if (file.text_verified === true) return "source";
    // text_verified is false when the body changed after filing, and nil for a
    // raw body with no fingerprint. Without a matching fingerprint the row
    // cannot claim to be verbatim, so both are "altered".
    return "altered";
  }
  // "extracted", or absent (the column's default): derived, and it says so.
  return "derived";
}

/** The short marker a file row shows, or null when the file has no text. */
export function textProvenanceLabel(p: TextProvenance): string | null {
  switch (p) {
    case "source":
      return "Text as filed";
    case "altered":
      return "Text changed after filing";
    case "derived":
      return "Text read from file";
    default:
      return null;
  }
}

/**
 * The full sentence a person reads in the marker's tooltip or the file view.
 * It never claims more than the data supports: only a "source" body is called
 * the text that was filed, and a derived body says plainly that it was read by
 * OCR and may not match the document.
 */
export function describeFileText(p: TextProvenance): string {
  switch (p) {
    case "source":
      return "This text is exactly what was filed — it is the original wording, unaltered.";
    case "altered":
      return "This text no longer matches what was filed: it was changed or re-typed after upload. Treat the file itself as the original, not this text.";
    case "derived":
      return "This text was read from the file automatically (OCR). It is not the original document and may differ from it — check the file itself before relying on a number or a date.";
    default:
      return "This file has no extracted text.";
  }
}

/**
 * Whether the row may present its text as verbatim. Only a "source" body may;
 * every other case is a derived or altered copy and must be shown as such.
 */
export function isVerbatimText(p: TextProvenance): boolean {
  return p === "source";
}
