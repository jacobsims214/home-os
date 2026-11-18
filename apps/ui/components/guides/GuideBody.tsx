"use client";

/**
 * GuideBody (#4570): a guide's markdown, rendered for a phone.
 *
 * The reading experience IS the feature ("follow a guide step by step on a
 * phone"), so this component is deliberately plain and deliberately careful:
 *
 * - **Readable measure.** Paragraphs are 16 px at 1.6 line-height and capped
 *   at ~68 characters (`max-w-[68ch]`), because a 390 px phone is fine but a
 *   1440 px monitor is not: nothing full-bleeds a 200-character line.
 * - **No horizontal page scroll.** A fenced code block (a serial number, a
 *   Wi-Fi password, a shell command) renders as `overflow-x-auto
 *   whitespace-pre`, so IT scrolls and the page does not. Long words anywhere
 *   break with `[overflow-wrap:anywhere]`.
 * - **No `dangerouslySetInnerHTML`.** The body is a family's own text; it is
 *   parsed by lib/guides/model.ts into blocks and rendered as React elements,
 *   so a guide can never inject markup into the app. Inline `code` is styled
 *   as code, never executed or interpreted.
 * - **Links are checked.** Only http(s), mailto and tel links become anchors;
 *   anything else (`javascript:`, a relative path, a typo) is shown as its own
 *   text, and an external link opens in a new tab with `rel="noreferrer"`.
 *
 * Colour comes from the semantic tokens only (architecture/theme-tokens.md),
 * so light and dark both work with no per-page theme code.
 */

import type { ReactNode } from "react";
import type { GuideBlock, GuideStep } from "@/lib/guides/model";

/** A URL this app is willing to make clickable. */
function safeHref(url: string): string | null {
  const trimmed = url.trim();
  if (/^https?:\/\//i.test(trimmed) || /^mailto:/i.test(trimmed) || /^tel:/i.test(trimmed)) return trimmed;
  return null;
}

/**
 * Inline markdown → React nodes: `code`, **bold**, *italic*, [text](url).
 *
 * One pass, longest-marker-first, and no nesting beyond what a family writes
 * by hand. An unbalanced marker is left as the character it is — the text is
 * never lost, which matters more than being strict.
 */
export function renderInline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let rest = text;
  let key = 0;

  const pushText = (chunk: string) => {
    if (chunk) out.push(chunk);
  };

  while (rest.length > 0) {
    // Inline code first: its contents are literal, so **bold** inside a code
    // span must not be interpreted.
    const code = /^`([^`]+)`/.exec(rest);
    if (code) {
      out.push(
        <code
          key={`c${key++}`}
          className="rounded bg-surface-2 px-1 py-0.5 font-mono text-[0.9em] text-ink [overflow-wrap:anywhere]"
        >
          {code[1]}
        </code>,
      );
      rest = rest.slice(code[0].length);
      continue;
    }

    const bold = /^\*\*([^*]+)\*\*/.exec(rest) ?? /^__([^_]+)__/.exec(rest);
    if (bold) {
      out.push(
        <strong key={`b${key++}`} className="font-semibold text-ink">
          {bold[1]}
        </strong>,
      );
      rest = rest.slice(bold[0].length);
      continue;
    }

    const italic = /^\*([^*\s][^*]*)\*/.exec(rest) ?? /^_([^_\s][^_]*)_/.exec(rest);
    if (italic) {
      out.push(<em key={`i${key++}`}>{italic[1]}</em>);
      rest = rest.slice(italic[0].length);
      continue;
    }

    const link = /^\[([^\]]+)\]\(([^)\s]+)\)/.exec(rest);
    if (link) {
      const href = safeHref(link[2]);
      if (href) {
        const external = /^https?:\/\//i.test(href);
        out.push(
          <a
            key={`a${key++}`}
            href={href}
            className="font-medium text-accent underline underline-offset-2"
            {...(external ? { target: "_blank", rel: "noreferrer noopener" } : {})}
          >
            {link[1]}
          </a>,
        );
      } else {
        // Not a link we will follow: say what was written, don't swallow it.
        out.push(link[0]);
      }
      rest = rest.slice(link[0].length);
      continue;
    }

    // Nothing special at this position: take text up to the next marker.
    const next = rest.slice(1).search(/[`*[_]/);
    if (next === -1) {
      pushText(rest);
      rest = "";
    } else {
      pushText(rest.slice(0, next + 1));
      rest = rest.slice(next + 1);
    }
  }

  return out;
}

function Block({ block }: { block: GuideBlock }) {
  if (block.kind === "code") {
    return (
      <div className="my-3 max-w-full rounded-token-md border border-line bg-surface-2">
        {block.lang && (
          <span className="block border-b border-line px-3 py-1.5 font-mono text-[11px] uppercase tracking-wide text-ink-3">
            {block.lang}
          </span>
        )}
        {/* The ONE element allowed to scroll sideways: it contains its own
            width so a long serial or command never pushes the page wide. */}
        <pre
          data-guide-code
          className="m-0 overflow-x-auto px-3 py-2.5 text-[13px] leading-relaxed"
          tabIndex={0}
          aria-label={block.lang ? `${block.lang} block, scrolls sideways if long` : "Block, scrolls sideways if long"}
        >
          <code className="font-mono text-ink">{block.text}</code>
        </pre>
      </div>
    );
  }

  if (block.kind === "ul" || block.kind === "ol") {
    const List = block.kind === "ul" ? "ul" : "ol";
    return (
      <List
        className={`my-2 space-y-1.5 pl-5 text-[16px] leading-relaxed text-ink [overflow-wrap:anywhere] ${
          block.kind === "ul" ? "list-disc" : "list-decimal"
        }`}
      >
        {block.items.map((item, i) => (
          <li key={i} className="pl-0.5">
            {renderInline(item)}
          </li>
        ))}
      </List>
    );
  }

  // Plain paragraphs: newlines inside one paragraph are real line breaks in a
  // guide (an address, a code printed on a sticker), so they are kept.
  return (
    <p className="my-2 max-w-[68ch] whitespace-pre-line text-[16px] leading-relaxed text-ink [overflow-wrap:anywhere]">
      {renderInline(block.text)}
    </p>
  );
}

/**
 * One step's blocks. Steps are rendered by the page, which owns the step
 * numbering and the "done" state; this only knows how markdown looks.
 */
export function GuideStepBody({ step }: { step: GuideStep }) {
  return (
    <div className="min-w-0">
      {step.blocks.map((block, i) => (
        <Block key={i} block={block} />
      ))}
    </div>
  );
}

/** A whole body with no step splitting (the revision preview). */
export default function GuideBody({ steps }: { steps: GuideStep[] }) {
  return (
    <div className="min-w-0">
      {steps.map((step, i) => (
        <section key={i} className="min-w-0" aria-label={step.heading ?? undefined}>
          {step.heading && (
            <h3 className="mt-4 mb-1 text-[17px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">
              {renderInline(step.heading)}
            </h3>
          )}
          <GuideStepBody step={step} />
        </section>
      ))}
    </div>
  );
}
