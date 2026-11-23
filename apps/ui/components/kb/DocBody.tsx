"use client";

/**
 * DocBody (HOME-OS-5503): a reference doc's markdown, for a phone.
 *
 * IT DOES NOT PARSE ANYTHING. The parser is `parseGuideSteps`
 * (lib/guides/model.ts) and the inline renderer and block styles are
 * `components/guides/GuideBody.tsx` — the SAME code a guide reads with. A KB
 * that grew its own markdown renderer would be two homes for one rule, and the
 * second one would drift: a fenced block that scrolls inside itself in a guide
 * and not in a doc is the kind of difference nobody notices until a Wi-Fi
 * password widens the page.
 *
 * WHAT DIFFERS IS THE SHAPE, and it is the distinction the whole split is for:
 *
 * - A GUIDE is read step by step, so it is split into numbered cards with a
 *   "Mark done" tick. A DOC is LOOKED UP — "what is the router's password",
 *   "which night are the bins" — so it is one continuous page under its
 *   headings, with a jump list when it is long. Numbering a reference doc's
 *   sections into "Step 1 / Step 2" would tell a reader to follow it, which is
 *   the wrong instruction for a lookup.
 * - The prose keeps the guide's measures, because they were right: 16 px at
 *   1.6 line-height, 68ch maximum, `[overflow-wrap:anywhere]` on anything long,
 *   and a fenced block that scrolls INSIDE itself so the page never widens.
 *
 * Anchors: each heading gets an id from its own text (duplicates get "-2"),
 * computed ONCE for the jump list and the sections together, so the two can
 * never disagree about what is on the page.
 */

import { useMemo } from "react";
import { GuideStepBody, renderInline } from "@/components/guides/GuideBody";
import { parseGuideSteps, type GuideStep } from "@/lib/guides/model";

/** A URL fragment for a heading: "After hours" → "after-hours". */
export function headingId(text: string, taken: Set<string>): string {
  const base =
    String(text ?? "")
      .toLowerCase()
      .replace(/[`*_[\]()#]/g, "")
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "") || "section";
  let id = base;
  let n = 2;
  while (taken.has(id)) id = `${base}-${n++}`;
  taken.add(id);
  return id;
}

/** One heading on the page, with the id its section carries. */
export interface DocHeading {
  id: string;
  text: string;
}

export interface DocSection {
  heading: DocHeading | null;
  step: GuideStep;
}

/**
 * The page as sections, each heading paired with the id its section gets. One
 * pass, so the jump list and the rendered page cannot disagree.
 */
export function sectionsOf(body: string | null | undefined): DocSection[] {
  const taken = new Set<string>();
  return parseGuideSteps(body).map((step) => ({
    step,
    heading: step.heading ? { id: headingId(step.heading, taken), text: step.heading } : null,
  }));
}

export function DocBody({ body }: { body: string | null | undefined }) {
  const sections = useMemo(() => sectionsOf(body), [body]);
  const headings = sections.flatMap((s) => (s.heading ? [s.heading] : []));

  if (sections.length === 0) {
    return (
      <p className="rounded-token-md border-2 border-dashed border-line px-4 py-8 text-center text-sm text-ink-3">
        This doc is empty. Someone made its address but has not written what goes there yet.
      </p>
    );
  }

  return (
    <div className="min-w-0" data-kb-doc-body>
      {/* A lookup doc is usually short, but "what the babysitter needs" is not:
          a jump list appears only when there is something to jump between. */}
      {headings.length > 2 && (
        <nav aria-label="On this page" className="mb-4 rounded-token-md border border-line bg-surface-2 p-3">
          <p className="mb-1 text-xs font-semibold uppercase tracking-wide text-ink-3">On this page</p>
          <ul className="m-0 list-none space-y-1 p-0">
            {headings.map((h) => (
              <li key={h.id} className="min-w-0">
                <a
                  href={`#${h.id}`}
                  className="inline-flex min-h-11 items-center text-sm font-medium text-accent [overflow-wrap:anywhere]"
                >
                  {h.text}
                </a>
              </li>
            ))}
          </ul>
        </nav>
      )}

      {sections.map((section, i) => (
        <section key={section.heading?.id ?? `s${i}`} className="min-w-0" aria-label={section.heading?.text}>
          {section.heading && (
            <h2
              id={section.heading.id}
              // scroll-mt clears the sticky app header, so a jump does not land
              // with the heading underneath it.
              className="mb-1 mt-6 scroll-mt-20 text-[19px] font-semibold leading-snug text-ink [overflow-wrap:anywhere] first:mt-0"
            >
              {renderInline(section.heading.text)}
            </h2>
          )}
          <GuideStepBody step={section.step} />
        </section>
      ))}
    </div>
  );
}
