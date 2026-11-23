"use client";

/**
 * LibrarySwitch (HOME-OS-5503): the two halves of the Guides section.
 *
 * Guides (#4570) and the family reference (the KB) are both "a title, markdown
 * and tags, in the index". They are NOT the same record — a guide has revision
 * history and an optional property/asset/vehicle subject; a reference doc has a
 * path and is replaced outright — so they keep their own tables. But they are
 * the same JOB to a person ("where do I read the thing about the router"), and
 * two nav tiles for two markdown stores is how this project ended up with one
 * rule in two homes before.
 *
 * So: one nav section, one page family, and this switch. It is a pair of
 * LINKS, not tabs, because the two sides are two routes that each need their
 * own URL — a deep link to a doc must survive a reload and a share.
 *
 * The labels say what each side is for, in the same three words the empty
 * states and the More sheet's tile hint use: "step by step" and "look up".
 */

import Link from "next/link";
import { IconBook, IconBookmark } from "@tabler/icons-react";

const STROKE = 1.75;

const ITEMS = [
  {
    href: "/dashboard/guides",
    label: "Guides",
    hint: "Step by step",
    icon: IconBook,
    // The list and every single guide, but NOT the reference under it.
    matches: (p: string) => p === "/dashboard/guides" || /^\/dashboard\/guides\/(?!reference(\/|$))/.test(p),
  },
  {
    href: "/dashboard/guides/reference",
    label: "Reference",
    hint: "Look things up",
    icon: IconBookmark,
    matches: (p: string) => p === "/dashboard/guides/reference" || p.startsWith("/dashboard/guides/reference/"),
  },
] as const;

export default function LibrarySwitch({ pathname }: { pathname: string }) {
  return (
    <nav aria-label="Guides and reference" className="mb-4" data-kb-library-switch>
      <div className="flex gap-1 rounded-token-md border border-line bg-surface-2 p-1">
        {ITEMS.map((item) => {
          const active = item.matches(pathname);
          const Icon = item.icon;
          return (
            <Link
              key={item.href}
              href={item.href}
              aria-current={active ? "page" : undefined}
              data-kb-switch={item.label.toLowerCase()}
              className={`flex min-h-11 min-w-0 flex-1 flex-col items-center justify-center rounded-token-sm px-2 py-1 text-center transition-colors ${
                active ? "bg-surface text-ink shadow-token-sm" : "text-ink-2 hover:text-ink"
              }`}
            >
              <span className="inline-flex items-center gap-1.5 text-sm font-semibold">
                <Icon size={16} stroke={STROKE} aria-hidden />
                {item.label}
              </span>
              <span className="text-[11px] leading-tight text-ink-3">{item.hint}</span>
            </Link>
          );
        })}
      </div>
    </nav>
  );
}
