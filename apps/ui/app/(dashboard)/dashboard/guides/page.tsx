"use client";

/**
 * Guides (#4570): the family's how-tos and runbooks.
 *
 * Built to the guides mockup and the list-page pattern
 * (KB architecture/entity-list-pattern.md), mobile-first:
 *
 * - **Phone (< 768 px)**: stacked cards, each one a 44 px+ link with a
 *   chevron, the guide's summary and its step count. Nothing clips at 375 px;
 *   a long title wraps (`[overflow-wrap:anywhere]`) rather than pushing the
 *   row sideways.
 * - **>= 768 px**: the same cards in two columns — a guide is prose, so a
 *   table of it reads worse than cards at every width.
 * - **Search** narrows the list on the CLIENT. GET /guides has no `q` (its
 *   filters are subject and tag), and a family has tens of guides, so the box
 *   filters the already-loaded list instead of round-tripping per keystroke.
 *   It matches title, summary, tags and body, so "wifi" finds "Reset the
 *   router" when the body says wifi.
 *
 * NOT EntityList: its default `propertyFilter` silently empties any list whose
 * entity has no `property_id` (the Pets bug, #4556), and a guide's subject is
 * optional — most family guides are about nothing in particular. A generic
 * card also cannot carry the step count, which is what tells a reader whether
 * this is a two-tap fix or a Saturday job.
 *
 * Roles (#4689): guides read is family + viewer + housesitter. A house sitter
 * reading "reset the router" is the whole reason guides exist, so they see the
 * section (nav-config) and this page read-only: no Add, no Edit, no Delete.
 * The API refuses writes from them anyway; the UI just does not offer them.
 */

import { useMemo, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton, Text, Textarea, TextInput } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconBook, IconChevronRight, IconPlus, IconSearch } from "@tabler/icons-react";
import { usePathname } from "next/navigation";
import { apiFetch } from "@/lib/api";
import { guideKeys } from "@/lib/query-keys";
import LibrarySwitch from "@/components/kb/LibrarySwitch";
import { ResponsiveModal, ResponsiveModalActions } from "@/components/ui";
import { useNavRole } from "@/components/shared/use-nav-role";
import { guideStepCount, guideSubtitle, matchesSearch, stepCountLabel, type Guide } from "@/lib/guides/model";

const STROKE = 1.75;

/** Family roles may write guides (apps/api/internal/auth/roles.go ResGuides). */
const WRITER_ROLES = new Set(["owner", "family_manager", "family_member"]);

// A Next.js page may export only its component and Next's own fields, so the
// two pure helpers below live in lib/guides/model.ts (where node --test can
// reach them): stepCountLabel and matchesSearch.

function GuideCard({ guide, canWrite }: { guide: Guide; canWrite: boolean }) {
  const subtitle = guideSubtitle(guide);
  const steps = guideStepCount(guide.body);
  const tags = guide.tags ?? [];

  return (
    <Link
      href={`/dashboard/guides/${guide.id}`}
      data-guide-card={guide.id}
      className="block min-h-11 rounded-token-md border border-line bg-surface shadow-token-sm transition-colors hover:bg-surface-2 active:bg-surface-2"
    >
      <div className="flex items-start gap-3 p-4">
        <span
          aria-hidden
          className="grid size-11 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent"
        >
          <IconBook size={22} stroke={STROKE} />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">
            {guide.title}
          </h3>
          {subtitle && (
            <p className="mt-0.5 line-clamp-2 text-sm text-ink-3 [overflow-wrap:anywhere]">{subtitle}</p>
          )}
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            <span className="inline-flex items-center rounded-full bg-surface-2 px-2.5 py-1 text-xs font-medium text-ink-2">
              {stepCountLabel(steps)}
            </span>
            {tags.slice(0, 3).map((tag) => (
              <span
                key={tag}
                className="inline-flex items-center rounded-full bg-surface-2 px-2.5 py-1 text-xs font-medium text-ink-3"
              >
                {tag}
              </span>
            ))}
            {tags.length > 3 && (
              <span className="text-xs font-medium text-ink-3">+{tags.length - 3}</span>
            )}
          </div>
        </div>
        <IconChevronRight size={20} stroke={STROKE} className="mt-2.5 shrink-0 text-ink-3" aria-hidden />
      </div>
      {!canWrite && null}
    </Link>
  );
}

/** "New guide": a title is enough to start; the body is written on the page. */
function NewGuideModal({
  opened,
  onClose,
  onCreated,
}: {
  opened: boolean;
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const queryClient = useQueryClient();
  const [title, setTitle] = useState("");
  const [summary, setSummary] = useState("");
  const [error, setError] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Guide }>("/api/v1/guides", { method: "POST", body }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: guideKeys.all });
      notifications.show({ message: "Guide started", color: "teal" });
      const id = res?.data?.id;
      if (!id) return onClose();
      onCreated(id);
    },
    onError: (err: Error) => setError(err.message || "Couldn't save the guide. Try again."),
  });

  const close = () => {
    setTitle("");
    setSummary("");
    setError(null);
    create.reset();
    onClose();
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim()) {
      setError("A guide needs a name — what is it for? Like “Reset the router”.");
      return;
    }
    setError(null);
    // The API requires a body string (it defaults to ""), and a new guide opens
    // straight into its own page to write the steps.
    create.mutate({ title: title.trim(), summary: summary.trim(), body: "" });
  };

  return (
    <ResponsiveModal opened={opened} onClose={close} title="New guide">
      <form onSubmit={submit} noValidate>
        <div className="flex flex-col gap-3">
          <TextInput
            label="Name"
            required
            data-autofocus
            placeholder="Reset the router"
            value={title}
            onChange={(e) => setTitle(e.currentTarget.value)}
            classNames={{ input: "max-md:text-base" }}
          />
          <Textarea
            label="Summary"
            description="Optional. One line under the name in the list."
            placeholder="What this guide is for, in a sentence."
            minRows={2}
            value={summary}
            autosize
            onChange={(e) => setSummary(e.currentTarget.value)}
            classNames={{ input: "max-md:text-base" }}
          />
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
        </div>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Start writing
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

export default function GuidesPage() {
  const router = useRouter();
  const pathname = usePathname();
  const role = useNavRole();
  const canWrite = role == null || WRITER_ROLES.has(role);
  const [search, setSearch] = useState("");
  const [showNew, setShowNew] = useState(false);

  const { data, isLoading, isError, refetch, isRefetching } = useQuery({
    queryKey: guideKeys.list(),
    queryFn: () => apiFetch<{ data: Guide[] }>("/api/v1/guides"),
  });

  const guides = useMemo(() => data?.data ?? [], [data]);
  const shown = useMemo(() => guides.filter((g) => matchesSearch(g, search)), [guides, search]);
  const trimmed = search.trim();

  return (
    <div className="mx-auto w-full max-w-6xl">
      {/* Title and New share a row; the description sits under both, so the
          description never pushes the button off a 375 px screen. */}
      <div className="mb-4 md:mb-6">
        <div className="flex items-center justify-between gap-3">
          <h2 className="min-w-0 text-xl font-semibold text-ink md:text-2xl">
            Guides
            {!isLoading && !isError && guides.length > 0 && (
              <span className="ml-2 align-middle text-base font-medium text-ink-3">{guides.length}</span>
            )}
          </h2>
          {canWrite && (
            <Button
              onClick={() => setShowNew(true)}
              leftSection={<IconPlus size={16} />}
              aria-haspopup="dialog"
              className="shrink-0 max-md:min-h-11"
            >
              New guide
            </Button>
          )}
        </div>
        <p className="mt-1 text-sm text-ink-3">
          How things work around the house: the router, the water heater, the cabin in winter.
        </p>
      </div>

      {/* #5503: the section's other half. A guide is a procedure you FOLLOW;
          the reference is knowledge you LOOK UP. They share this section, the
          page family and the markdown renderer, and differ in what a reader
          does with them — which is what the switch says in three words. */}
      <LibrarySwitch pathname={pathname ?? "/dashboard/guides"} />

      {/* Search. A real searchbox (type="search"), 16 px on phones so iOS does
          not zoom on focus, and only drawn once there is something to find. */}
      {!isLoading && !isError && guides.length > 0 && (
        <div className="mb-3">
          <TextInput
            type="search"
            value={search}
            onChange={(e) => setSearch(e.currentTarget.value)}
            placeholder="Search guides"
            aria-label="Search guides"
            leftSection={<IconSearch size={16} stroke={STROKE} />}
            classNames={{ input: "max-md:min-h-11 max-md:text-base" }}
            data-guides-search
          />
        </div>
      )}

      {isLoading && (
        <div role="status" aria-busy="true" aria-label="Loading guides" className="space-y-2 md:grid md:grid-cols-2 md:gap-3 md:space-y-0">
          {[1, 2, 3, 4].map((i) => (
            <div key={i} className="space-y-2 rounded-token-md border border-line bg-surface p-4 shadow-token-sm">
              <Skeleton height={14} width="55%" radius="sm" />
              <Skeleton height={12} width="80%" radius="sm" />
            </div>
          ))}
        </div>
      )}

      {isError && (
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">Couldn&apos;t load guides. Check your connection and try again.</p>
          <Button variant="default" onClick={() => refetch()} loading={isRefetching} className="max-md:min-h-11">
            Try again
          </Button>
        </div>
      )}

      {!isLoading && !isError && guides.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <p className="text-sm text-ink-3">
            No guides yet. Write down how to reset the router, drain the water heater, or open the cabin,
            and it&apos;s here the next time someone needs it.
          </p>
          {canWrite && (
            <Button
              variant="light"
              onClick={() => setShowNew(true)}
              leftSection={<IconPlus size={16} />}
              className="max-md:min-h-11"
            >
              Write the first guide
            </Button>
          )}
        </div>
      )}

      {!isLoading && !isError && guides.length > 0 && shown.length === 0 && (
        <div className="rounded-token-md border border-dashed border-line px-4 py-10 text-center">
          <Text size="sm" className="text-ink-3">
            No guide matches “{trimmed}”.
          </Text>
          <Button variant="subtle" onClick={() => setSearch("")} className="mt-2 max-md:min-h-11">
            Clear search
          </Button>
        </div>
      )}

      {shown.length > 0 && (
        <ul aria-label="Guides" className="space-y-2 md:grid md:grid-cols-2 md:gap-3 md:space-y-0" data-guides-list>
          {shown.map((guide) => (
            <li key={guide.id} className="min-w-0">
              <GuideCard guide={guide} canWrite={canWrite} />
            </li>
          ))}
        </ul>
      )}

      <NewGuideModal
        opened={showNew}
        onClose={() => setShowNew(false)}
        onCreated={(id) => router.push(`/dashboard/guides/${id}?edit=1`)}
      />
    </div>
  );
}
