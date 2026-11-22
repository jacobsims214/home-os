/**
 * The attention badge (#5499): "nothing to do here" versus "you have to act".
 *
 * The badge renders the API's `attention` field and nothing else. It never
 * looks at `is_autopay`: a bill whose autopay switch is on but whose account is
 * missing comes back as `needs_setup`, and this badge must show "Needs setup",
 * not "Automatic" — that difference is the whole point of the field (#5498,
 * KB architecture/bill-attention-rule.md).
 *
 * Colour is the only cue at 390px that reads at a glance, so the tone is
 * carried by BOTH the tint and the icon: a family scanning the list should not
 * have to read a word, and a colour-blind one should not have to guess.
 */

import { IconAlertTriangle, IconBolt, IconClock, IconHelpCircle } from "@tabler/icons-react";
import { attentionLabel, attentionOf, attentionTone, type Bill } from "@/lib/bills/model";

const STROKE = 1.75;

const TONE_CLASSES: Record<string, string> = {
  auto: "border-[var(--line)] bg-surface-2 text-ink-2",
  action: "border-[var(--accent-fill)] bg-accent-soft text-[var(--accent-fill)]",
  warn: "border-warn-line bg-warn-bg text-warn-ink",
  danger: "border-[var(--danger)] bg-[var(--warn-bg)] text-[var(--danger)]",
  unknown: "border-line bg-surface-2 text-ink-3",
};

const TONE_ICONS: Record<string, typeof IconBolt> = {
  auto: IconBolt,
  action: IconAlertTriangle,
  warn: IconClock,
  danger: IconAlertTriangle,
  unknown: IconHelpCircle,
};

/**
 * The badge for one bill's attention state. `data-attention` carries the raw
 * API value so a test (and the reader) can see the field was passed through
 * rather than re-derived.
 */
export function AttentionBadge({ bill, className = "" }: { bill: Pick<Bill, "attention">; className?: string }) {
  const attention = attentionOf(bill);
  const tone = attentionTone(attention);
  const Icon = TONE_ICONS[tone] ?? IconHelpCircle;
  return (
    <span
      data-attention={attention ?? "unknown"}
      className={`inline-flex min-h-6 items-center gap-1 rounded-full border px-2 py-0.5 text-[12px] font-semibold leading-tight ${TONE_CLASSES[tone]} ${className}`}
    >
      <Icon size={13} stroke={STROKE} aria-hidden />
      {attentionLabel(attention)}
    </span>
  );
}
