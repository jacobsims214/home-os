"use client";

/**
 * Guide reading view (#4570): the page a person opens with one hand while
 * standing in front of the boiler.
 *
 * The acceptance criterion is "follow a guide step by step on a phone", so the
 * page is built around three things and nothing else:
 *
 * 1. **Steps, not a wall of markdown.** `lib/guides/model.ts` splits the body
 *    at headings (or one step per item when the body is a bare numbered list),
 *    and each step is its own numbered card. A reader scrolls to the step they
 *    are on instead of losing their place in prose.
 * 2. **A visible place in the guide.** Each step has a "Mark done" toggle and
 *    the header carries "2 of 5 done", so someone interrupted mid-job can see
 *    where they stopped. The ticks are client-only state kept in localStorage
 *    per guide id: the guides API has no per-person progress column, and
 *    pretending a tick was saved server-side would be a lie about what happened.
 * 3. **Nothing pushes the page sideways.** Paragraphs cap at 68ch, long words
 *    break, and a code block (a Wi-Fi password, a serial, a shell command)
 *    scrolls inside itself (components/guides/GuideBody.tsx).
 *
 * Deliberately NOT `DetailPageLayout`: that frame is for entities the resources
 * API knows (files, notes, secrets). A guide has none of those, so the frame
 * would draw a Resources section that has nothing in it. The title bar below
 * keeps the shape that frame uses on a phone — a 44 px back button, sticky
 * under the shell header — without the tabs.
 *
 * Roles (#4689): reading a guide is granted to family, viewer and housesitter;
 * a sitter reading "reset the router" is the reason guides exist. This page is
 * read-only for everyone (editing is its own ticket), so there is no Edit
 * affordance to hide from anyone.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { Button, Skeleton } from "@mantine/core";
import { IconAlertTriangle, IconArrowLeft, IconBook, IconCheck } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { guideKeys } from "@/lib/query-keys";
import { GuideStepBody } from "@/components/guides/GuideBody";
import {
  formatGuideDate,
  guideStepCount,
  guideSubtitle,
  parseGuideSteps,
  type Guide,
} from "@/lib/guides/model";

const STROKE = 1.75;

/** The phone's back button: a 44 px target, like the detail frame's. */
const iconButton =
  "inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-token-sm text-ink-2 " +
  "transition-colors hover:bg-surface-2 hover:text-ink";

/**
 * Where the ticks are kept: localStorage, per guide and per browser. Every
 * access is wrapped in try/catch because Safari in private mode throws on
 * setItem, and a guide that refuses to open because it could not remember a
 * tick would be a worse bug than forgetting the tick.
 */
function storageKey(guideId: string): string {
  return "home-os:guide-steps:" + guideId;
}

function loadDone(guideId: string): Set<number> {
  try {
    const raw = window.localStorage.getItem(storageKey(guideId));
    if (!raw) return new Set();
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((n): n is number => typeof n === "number"));
  } catch {
    return new Set();
  }
}

function saveDone(guideId: string, done: Set<number>): void {
  try {
    window.localStorage.setItem(storageKey(guideId), JSON.stringify(Array.from(done)));
  } catch {
    /* Private mode or a full quota: the ticks are a convenience, not data. */
  }
}

function BackBar({ title }: { title: string }) {
  return (
    <div className="sticky top-[var(--app-shell-header-offset,0px)] z-10 -mx-4 -mt-4 mb-4 flex min-h-14 items-center gap-1 border-b border-line bg-bg px-2 md:static md:mx-0 md:mt-0 md:mb-6 md:border-0 md:bg-transparent md:px-0">
      <Link href="/dashboard/guides" aria-label="Back to Guides" className={`${iconButton} md:hidden`}>
        <IconArrowLeft size={22} stroke={STROKE} aria-hidden />
      </Link>
      <div className="min-w-0 flex-1">
        <Link
          href="/dashboard/guides"
          className="hidden items-center gap-1 text-sm text-ink-3 hover:text-accent md:inline-flex"
        >
          <IconArrowLeft size={16} stroke={STROKE} aria-hidden />
          Back to Guides
        </Link>
        {/* On a phone the guide's own h1 sits directly under this bar, so a
            second copy here would read as two titles stacked. It stays in the
            accessibility tree (sr-only) — it is the bar's only label — and is
            drawn from md up, where the bar is a breadcrumb line rather than a
            sticky header. */}
        <h2 className="sr-only truncate text-lg font-semibold leading-tight text-ink md:not-sr-only md:mt-1 md:text-2xl md:font-bold">
          {title}
        </h2>
      </div>
    </div>
  );
}

/** One step: its number, its heading, its body, and a tick. */
function StepCard({
  index,
  heading,
  done,
  onToggle,
  children,
}: {
  index: number;
  heading: string | null;
  done: boolean;
  onToggle: () => void;
  children: React.ReactNode;
}) {
  const label = heading ?? `Step ${index + 1}`;
  return (
    <li
      data-guide-step={index + 1}
      data-done={done ? "true" : "false"}
      className={`min-w-0 rounded-token-md border bg-surface p-4 shadow-token-sm ${
        done ? "border-line bg-surface-2" : "border-line"
      }`}
    >
      <div className="mb-2 flex items-start gap-3">
        <span
          aria-hidden
          className={`grid size-8 shrink-0 place-items-center rounded-full text-sm font-semibold tabular-nums ${
            done ? "bg-accent-fill text-white" : "bg-accent-soft text-accent"
          }`}
        >
          {done ? <IconCheck size={18} stroke={2.25} /> : index + 1}
        </span>
        <h3
          className={`min-w-0 flex-1 pt-1 text-[17px] font-semibold leading-snug [overflow-wrap:anywhere] ${
            done ? "text-ink-3" : "text-ink"
          }`}
        >
          {label}
        </h3>
      </div>

      <div className={done ? "opacity-70" : undefined}>{children}</div>

      {/* The tick is what makes a guide followable across an interruption:
          it is the reader's own place in the job, not a shared status. */}
      <Button
        variant={done ? "light" : "default"}
        size="sm"
        onClick={onToggle}
        aria-pressed={done}
        leftSection={<IconCheck size={16} stroke={STROKE} aria-hidden />}
        className="mt-3 max-md:min-h-11"
        data-guide-step-done={index + 1}
      >
        {done ? "Done" : "Mark done"}
      </Button>
    </li>
  );
}

export default function GuidePage() {
  const params = useParams<{ id: string }>();
  const guideId =
    typeof params?.id === "string" ? params.id : Array.isArray(params?.id) ? params.id[0] ?? "" : "";

  const { data, isLoading, isError, refetch, isRefetching } = useQuery({
    queryKey: guideKeys.detail(guideId),
    queryFn: () => apiFetch<{ data: Guide }>(`/api/v1/guides/${guideId}`),
    enabled: guideId !== "",
  });

  const guide = data?.data;
  const steps = useMemo(() => parseGuideSteps(guide?.body), [guide?.body]);
  const [done, setDone] = useState<Set<number>>(new Set());

  // The ticks are per guide id, and the id only exists after the client mounts
  // (a dynamic route), so they are read here rather than in useState's initial
  // value — which would also make the first render differ from the server's.
  useEffect(() => {
    if (!guideId) return;
    setDone(loadDone(guideId));
  }, [guideId]);

  const toggle = useCallback(
    (index: number) => {
      setDone((prev) => {
        const next = new Set(prev);
        if (next.has(index)) next.delete(index);
        else next.add(index);
        saveDone(guideId, next);
        return next;
      });
    },
    [guideId],
  );

  if (isLoading) {
    return (
      <div className="mx-auto w-full max-w-3xl" aria-busy="true" aria-label="Loading guide">
        <Skeleton height={20} width="60%" radius="sm" />
        <div className="mt-4 space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="space-y-2 rounded-token-md border border-line bg-surface p-4 shadow-token-sm">
              <Skeleton height={14} width="40%" radius="sm" />
              <Skeleton height={12} width="90%" radius="sm" />
              <Skeleton height={12} width="75%" radius="sm" />
            </div>
          ))}
        </div>
      </div>
    );
  }

  if (isError || !guide) {
    return (
      <div className="mx-auto w-full max-w-3xl">
        <BackBar title="Guide" />
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">
            Couldn&apos;t open this guide. It may have been deleted, or your connection dropped.
          </p>
          <Button variant="default" onClick={() => refetch()} loading={isRefetching} className="max-md:min-h-11">
            Try again
          </Button>
        </div>
      </div>
    );
  }

  const summary = guideSubtitle(guide);
  const total = steps.length > 0 ? steps.length : guideStepCount(guide.body);
  const doneCount = Array.from(done).filter((i) => i < total).length;
  const updated = formatGuideDate(guide.updated_at);
  const tags = guide.tags ?? [];
  const progress =
    total === 0
      ? "No steps yet"
      : doneCount > 0
        ? `${doneCount} of ${total} done`
        : `${total} step${total === 1 ? "" : "s"}`;

  return (
    <div className="mx-auto w-full max-w-3xl min-w-0" data-guide-detail={guide.id}>
      <BackBar title={guide.title} />

      {/* The head: what the guide is for, how long it is, when it last changed. */}
      <header className="mb-4 min-w-0 md:mb-6">
        <div className="mb-3 flex items-center gap-3.5">
          <span
            aria-hidden
            className="grid size-12 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent"
          >
            <IconBook size={24} stroke={STROKE} />
          </span>
          <div className="min-w-0 flex-1">
            <h1 className="text-xl font-semibold leading-tight text-ink [overflow-wrap:anywhere] md:text-2xl">
              {guide.title}
            </h1>
            {summary && (
              <p className="mt-0.5 max-w-[68ch] text-sm text-ink-3 [overflow-wrap:anywhere]">{summary}</p>
            )}
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          <span
            data-guide-progress
            className="inline-flex items-center rounded-full bg-surface-2 px-2.5 py-1 text-xs font-medium text-ink-2"
          >
            {progress}
          </span>
          {tags.map((tag) => (
            <span
              key={tag}
              className="inline-flex items-center rounded-full bg-surface-2 px-2.5 py-1 text-xs font-medium text-ink-3"
            >
              {tag}
            </span>
          ))}
          {updated && <span className="text-xs text-ink-3">Updated {updated}</span>}
        </div>
      </header>

      {total === 0 ? (
        <div className="rounded-token-md border-2 border-dashed border-line px-4 py-10 text-center">
          <p className="text-sm text-ink-3">
            This guide has no steps yet. Someone wrote the title but not the how-to.
          </p>
        </div>
      ) : (
        <ol aria-label="Steps" className="m-0 list-none space-y-3 p-0">
          {steps.map((step, i) => (
            <StepCard key={i} index={i} heading={step.heading} done={done.has(i)} onToggle={() => toggle(i)}>
              <GuideStepBody step={step} />
            </StepCard>
          ))}
        </ol>
      )}

      {total > 0 && doneCount === total && (
        <p
          data-guide-complete
          className="mt-4 rounded-token-md border border-line bg-accent-soft px-4 py-3 text-sm font-medium text-accent"
        >
          All {total} steps done. That&apos;s the whole guide.
        </p>
      )}
    </div>
  );
}
