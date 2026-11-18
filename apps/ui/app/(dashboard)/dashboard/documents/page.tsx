"use client";

/**
 * Documents (#4569) — the household's paperwork, filed and findable.
 *
 * Built to the approved documents mockup (artifact for #4520, drawn inside the
 * approved shell #75 v1; component vocabulary from People & House #89 and the
 * recipe pages #4567) on the API from #4478. Hierarchy, top to bottom:
 *
 *   1. a search box that goes to the ONE semantic search (/dashboard/search);
 *   2. expiring soon — the reason the hub exists (a policy that lapses is a
 *      real loss), ordered by date with the already-expired first;
 *   3. categories with real counts (a tile only for a category that holds
 *      something, and "other" only when documents are actually unfiled);
 *   4. recently added and the full list;
 *   5. the add control: a camera first, because a receipt is photographed at
 *      the counter.
 *
 * `?category=insurance` is the category view: the API's own filter (GET
 * /files?category=) rather than a filtered copy, with a breadcrumb back to the
 * hub. There is no separate route for it, so the browser Back button and a
 * shared link both work.
 *
 * THE SEARCH BOX IS A DOOR, NOT A SECOND SEARCH (#4561 owns search). What is
 * typed here goes to /dashboard/search?q=, which searches the Typesense index —
 * titles, notes, OCR text and metadata — by meaning. Nothing on this page
 * pretends to match on meaning: filtering the list already in hand is offered
 * as exactly that ("in this list"), and the hint under the box says where
 * meaning-search lives.
 *
 * WHAT A ROLE SEES (#4689 / decisions/2026-11-15-role-policy.md). Files read is
 * the family roles plus viewer; a viewer reads everything here but the add,
 * edit and delete controls are not drawn for them. A housesitter or vendor has
 * no Documents section at all (nav-config drops it, and the API answers 403), so
 * this page says so once, without firing a request the API would refuse.
 * Hiding is a courtesy: the API enforces.
 *
 * WHAT THE API DOESN'T HAVE, said plainly instead of faked:
 *  - no tag-management endpoint (tags live on a file and are edited with it),
 *    so the tags in use are shown and edited per document, never renamed in
 *    bulk;
 *  - no expiry filter (`GET /files` filters by category and person only), so
 *    Expiring soon is computed from the household's own list;
 *  - no extracted fields (provider, amount, "looks like") — the worker stores
 *    TEXT, not values, so nothing here invents a number from a scan.
 */

import { Suspense, useMemo, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import {
  Button,
  Card,
  Group,
  Skeleton,
  Text,
  TextInput,
  Title,
} from "@mantine/core";
import {
  IconAlertTriangle,
  IconCamera,
  IconChevronRight,
  IconPlus,
  IconRefresh,
  IconSearch,
  IconSparkles,
} from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import { useNavRole } from "@/components/shared/use-nav-role";
import { canSeeFiles, canWriteFiles } from "@/lib/resource-access";
import { useDocuments } from "@/lib/documents/api";
import {
  CATEGORY_LABELS,
  CATEGORY_SINGULAR,
  categoryCounts,
  expiringSoon,
  filterDocuments,
  isCategory,
  populatedCategories,
  type Category,
} from "@/lib/documents/model";
import { useHouseholdToday } from "@/lib/today/api";
import { CategoryIcon } from "@/components/documents/categories";
import DocumentRow from "@/components/documents/DocumentRow";
import UploadSheet from "@/components/documents/UploadSheet";

const ICON_STROKE = NAV_ICON_STROKE;

/**
 * `useSearchParams` needs a Suspense boundary in the App Router, or the whole
 * route opts into dynamic rendering with a build-time warning. The shell is a
 * client component already; this boundary keeps the hub's first paint the
 * skeleton rather than a bailout.
 */
export default function DocumentsPage() {
  return (
    <Suspense fallback={<LoadingHub />}>
      <DocumentsHub />
    </Suspense>
  );
}

function DocumentsHub() {
  const role = useNavRole();
  const router = useRouter();
  const searchParams = useSearchParams();
  const categoryParam = searchParams.get("category");
  const categoryView: Category | null = isCategory(categoryParam) ? categoryParam : null;

  const [query, setQuery] = useState("");
  const [uploadOpen, setUploadOpen] = useState(false);
  const [uploadCategory, setUploadCategory] = useState<Category | null>(null);

  // The role gate is decided FIRST, and it fails CLOSED while the role is still
  // loading: a housesitter or vendor must never fire the list request that
  // answers 403 on every visit (#4564's rule). Unlike the nav — which shows
  // everything until the role lands, so an owner doesn't flicker — this page
  // has a request attached to the answer, so it waits for the answer. The wait
  // is one /me/households round trip the shell already makes.
  const roleKnown = role != null && role !== "";
  const mayRead = roleKnown && canSeeFiles(role);
  const mayWrite = roleKnown && canWriteFiles(role);

  // A category view narrows with the API's own filter, so it stays right as the
  // family's pile grows. The hub itself fetches the whole list, because the
  // counts and the expiring list are computed from it.
  const documentsQ = useDocuments(categoryView ? { category: categoryView } : {}, mayRead);
  const files = useMemo(() => documentsQ.data?.data ?? [], [documentsQ.data]);
  // "Expiring soon" and the counts are the FAMILY's day (#5316): a warranty
  // that expires today is not expired yet, and which of the two it is must not
  // depend on the browser's zone. Null until the household is known, so the
  // lists are empty rather than wrong.
  const today = useHouseholdToday();

  const expiring = useMemo(() => (today ? expiringSoon(files, today) : []), [files, today]);
  const cats = useMemo(() => (today ? populatedCategories(files, today) : []), [files, today]);
  const counts = useMemo(() => (today ? categoryCounts(files, today) : new Map()), [files, today]);
  const filtered = useMemo(() => filterDocuments(files, { q: query }), [files, query]);

  const openUpload = (category: Category | null = null) => {
    setUploadCategory(category);
    setUploadOpen(true);
  };

  const submitSearch = (e: React.FormEvent) => {
    e.preventDefault();
    const q = query.trim();
    if (!q) return;
    router.push(`/dashboard/search?q=${encodeURIComponent(q)}`);
  };

  // The role isn't known yet: wait rather than guess. Drawing the hub and then
  // taking it away is worse than one skeleton, and firing the list would mean a
  // 403 for every sitter who ever opens a bookmarked link.
  if (!roleKnown) {
    return (
      <div className="mx-auto w-full max-w-6xl">
        <LoadingHub />
      </div>
    );
  }

  // ── The role that cannot use this at all ──────────────────────────────────
  // Not an empty hub: a sitter or vendor has no Documents entry in the nav, so
  // reaching this URL directly (an old link, a bookmark) says so once.
  if (!mayRead) {
    return (
      <div className="mx-auto w-full max-w-6xl" data-documents-blocked>
        {/* No page heading: the shell header already reads "Documents" for this
            route (nav-config's pageTitle), and printing it twice is noise. */}
        <Card radius="token-md" p="lg" withBorder role="status" style={{ background: "var(--surface-2)" }}>
          <Text fw={600}>Documents is for the family</Text>
          <Text size="sm" mt={6} style={{ color: "var(--ink-2)", lineHeight: 1.5 }}>
            Your role in this family doesn&apos;t include household paperwork, so there&apos;s nothing to show here.
            Ask a family manager if you need a specific document.
          </Text>
          <Button component={Link} href="/dashboard" variant="default" mt="md" className="max-md:min-h-11">
            Back to Today
          </Button>
        </Card>
      </div>
    );
  }

  const isLoading = documentsQ.isLoading;
  const isError = documentsQ.isError;
  const isEmpty = !isLoading && !isError && files.length === 0;

  return (
    <div className="mx-auto w-full max-w-6xl" data-documents-page data-documents-view={categoryView ? "category" : "hub"}>
      {categoryView && (
        <nav aria-label="Breadcrumb" className="mb-3 flex items-center gap-1 text-[13px]" style={{ color: "var(--ink-3)" }}>
          <Link href="/dashboard/documents" style={{ color: "var(--accent)", fontWeight: 500, padding: "6px 2px" }}>
            Documents
          </Link>
          <IconChevronRight size={14} stroke={ICON_STROKE} aria-hidden />
          <span aria-current="page" style={{ color: "var(--ink-2)", fontWeight: 600 }}>
            {CATEGORY_LABELS[categoryView]}
          </span>
        </nav>
      )}

      {/* Search: a door into the real semantic search (#4561). */}
      <form role="search" onSubmit={submitSearch} className="mb-1" data-documents-search>
        <TextInput
          // type="search" is what makes it a search box to a screen reader (and
          // to the phone keyboard's search key), not a plain text field.
          type="search"
          value={query}
          onChange={(e) => setQuery(e.currentTarget.value)}
          // Short enough to fit a 390 px phone without clipping mid-word.
          placeholder={'Try "car insurance"'}
          aria-label="Search documents"
          size="lg"
          leftSection={<IconSearch size={20} stroke={ICON_STROKE} />}
          rightSection={
            <button
              type="submit"
              aria-label="Search everything"
              style={{
                width: 40,
                height: 40,
                display: "grid",
                placeItems: "center",
                border: 0,
                borderRadius: 10,
                background: "var(--accent-fill)",
                color: "#fff",
                cursor: "pointer",
              }}
            >
              <IconChevronRight size={20} stroke={ICON_STROKE} />
            </button>
          }
          rightSectionWidth={48}
          classNames={{ input: "max-md:min-h-11 max-md:text-base" }}
        />
      </form>
      <p className="mb-3 flex items-start gap-1.5 text-[13px] text-ink-3" style={{ lineHeight: 1.45 }}>
        <IconSparkles size={15} stroke={ICON_STROKE} aria-hidden style={{ flex: "none", marginTop: 2 }} />
        Searches by meaning across every document&apos;s text and details, not just names.
      </p>

      {query.trim() && !isLoading && !isError && (
        <Card radius="token-md" p="md" withBorder mb="md" data-documents-filter>
          <Group justify="space-between" align="baseline">
            <Text fw={600} size="sm">
              {filtered.length} in this list match &ldquo;{query.trim()}&rdquo;
            </Text>
            <Link href={`/dashboard/search?q=${encodeURIComponent(query.trim())}`} className="text-[13px] font-semibold" style={{ color: "var(--accent)" }}>
              Search everything
            </Link>
          </Group>
          <Text size="xs" mt={4} style={{ color: "var(--ink-3)", lineHeight: 1.45 }}>
            This is a plain word filter over the {files.length} documents already loaded here. Use Search everything
            to match by meaning, or to reach documents further down the list.
          </Text>
          {filtered.length === 0 && (
            <Text size="sm" mt={8} style={{ color: "var(--ink-2)" }}>
              Nothing in this list has those words.
            </Text>
          )}
          {filtered.length > 0 && (
            <div className="mt-2 overflow-hidden rounded-token-md border border-line">
              {filtered.slice(0, 8).map((f) => (
                <DocumentRow key={f.id} file={f} today={today} />
              ))}
            </div>
          )}
        </Card>
      )}

      {isLoading && <LoadingHub />}

      {isError && !isLoading && (
        <Card radius="token-md" p="lg" withBorder role="alert" style={{ background: "var(--warn-bg)", color: "var(--warn-ink)", borderColor: "var(--warn-line)" }} data-documents-error>
          <Group gap={10} wrap="nowrap" align="flex-start">
            <IconAlertTriangle size={20} stroke={ICON_STROKE} aria-hidden style={{ flex: "none", marginTop: 2 }} />
            <div>
              <Text fw={600}>Couldn&apos;t load documents</Text>
              <Text size="sm" mt={2} style={{ lineHeight: 1.45 }}>
                The list didn&apos;t load, so nothing below is missing — it just isn&apos;t here yet. Check your
                connection and try again.
              </Text>
              <Button
                variant="default"
                mt="sm"
                leftSection={<IconRefresh size={16} stroke={ICON_STROKE} />}
                onClick={() => documentsQ.refetch()}
                className="max-md:min-h-11"
              >
                Try again
              </Button>
            </div>
          </Group>
        </Card>
      )}

      {isEmpty && (
        <EmptyHub
          canWrite={mayWrite}
          categoryView={categoryView}
          onAdd={() => openUpload(categoryView)}
        />
      )}

      {!isLoading && !isError && !isEmpty && (
        <div className="grid gap-6">
          {/* ── Category view: one category, with the person filter the API has ── */}
          {categoryView && (
            <section aria-labelledby="docs-category">
              <div className="mb-3 flex items-center gap-3.5">
                <span
                  aria-hidden
                  style={{
                    width: 52,
                    height: 52,
                    flex: "none",
                    display: "grid",
                    placeItems: "center",
                    borderRadius: 14,
                    background: "var(--accent-soft)",
                    color: "var(--accent)",
                  }}
                >
                  <CategoryIcon category={categoryView} size={26} />
                </span>
                <div style={{ minWidth: 0 }}>
                  <Title order={2} id="docs-category" className="text-xl font-semibold text-ink">
                    {CATEGORY_LABELS[categoryView]}
                  </Title>
                  <Text size="sm" style={{ color: "var(--ink-3)" }}>
                    {files.length} {files.length === 1 ? "document" : "documents"}
                    {expiring.length > 0 ? ` · ${expiring.length} expiring soon` : ""}
                  </Text>
                </div>
              </div>
              <div className="overflow-hidden rounded-token-md border border-line bg-surface">
                {files.map((f) => (
                  <DocumentRow key={f.id} file={f} today={today} />
                ))}
              </div>
              {mayWrite && (
                <Button
                  variant="default"
                  mt="md"
                  onClick={() => openUpload(categoryView)}
                  leftSection={<IconPlus size={16} stroke={ICON_STROKE} />}
                  className="max-md:min-h-11"
                  data-documents-category-add
                >
                  Add to {CATEGORY_LABELS[categoryView]}
                </Button>
              )}
            </section>
          )}

          {/* ── The hub ── */}
          {!categoryView && (
            <>
              {/* Expiring soon: the reason the hub exists. */}
              {expiring.length > 0 && (
                <section aria-labelledby="docs-expiring" data-documents-expiring>
                  <SectionHeading id="docs-expiring" title="Expiring soon" count={expiring.length} />
                  <div className="overflow-hidden rounded-token-md border" style={{ borderColor: "var(--warn-line)", background: "var(--warn-bg)" }}>
                    {expiring.slice(0, 4).map((f) => (
                      <DocumentRow key={f.id} file={f} today={today} />
                    ))}
                  </div>
                </section>
              )}

              {/* Categories with something in them. */}
              <section aria-labelledby="docs-categories" data-documents-categories>
                <SectionHeading id="docs-categories" title="Categories" />
                <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3 lg:grid-cols-4">
                  {cats.map((c) => (
                    <Link
                      key={c.category}
                      href={`/dashboard/documents?category=${c.category}`}
                      data-category-tile={c.category}
                      className="flex min-h-16 items-center gap-3 rounded-token-md border border-line bg-surface px-3 py-3 hover:border-ink-3"
                      style={{ color: "inherit", textDecoration: "none" }}
                    >
                      <span
                        aria-hidden
                        style={{
                          width: 40,
                          height: 40,
                          flex: "none",
                          display: "grid",
                          placeItems: "center",
                          borderRadius: 11,
                          background: c.category === "other" ? "var(--surface-2)" : "var(--accent-soft)",
                          color: c.category === "other" ? "var(--ink-2)" : "var(--accent)",
                        }}
                      >
                        <CategoryIcon category={c.category} size={20} />
                      </span>
                      <span style={{ minWidth: 0 }}>
                        <span style={{ display: "block", fontWeight: 600, fontSize: 15, lineHeight: 1.25 }}>
                          {CATEGORY_LABELS[c.category]}
                        </span>
                        <span style={{ display: "block", fontSize: 12, color: "var(--ink-3)", lineHeight: 1.35 }}>
                          {c.count}
                          {c.expiring > 0 ? (
                            <span style={{ color: "var(--warn-ink)", fontWeight: 600 }}>
                              {" · "}
                              {c.expiring} expiring
                            </span>
                          ) : null}
                        </span>
                      </span>
                    </Link>
                  ))}
                </div>
                {counts.get("other")!.count > 0 && (
                  <Text size="xs" mt={8} style={{ color: "var(--ink-3)" }}>
                    &ldquo;Not filed yet&rdquo; are documents with no category — give one a category from its own page.
                  </Text>
                )}
              </section>

              {/* Recently added, and the whole list. */}
              <section aria-labelledby="docs-all" id="docs-all" data-documents-all>
                <SectionHeading id="docs-all" title="All documents" count={files.length} />
                <div className="overflow-hidden rounded-token-md border border-line bg-surface">
                  {files.map((f) => (
                    <DocumentRow key={f.id} file={f} today={today} />
                  ))}
                </div>
              </section>
            </>
          )}

          {/* The add control: camera first. */}
          {mayWrite && (
            <button
              type="button"
              onClick={() => openUpload(categoryView)}
              data-documents-add
              className="flex flex-col items-center justify-center gap-2 rounded-token-md border-2 border-dashed px-4 py-5 text-center"
              style={{ borderColor: "var(--line)", color: "var(--accent)", background: "transparent", cursor: "pointer" }}
            >
              <span className="flex items-center gap-2 text-[15px] font-semibold">
                <IconCamera size={22} stroke={ICON_STROKE} aria-hidden />
                Scan or upload a document
              </span>
              <span style={{ fontSize: 13, color: "var(--ink-3)", fontWeight: 400, lineHeight: 1.35 }}>
                Camera on your phone, or any PDF or photo — up to 25 MB. Text is read after you file it.
              </span>
            </button>
          )}

          {!mayWrite && (
            <Card radius="token-md" p="md" withBorder style={{ background: "var(--surface-2)" }} data-documents-viewer-note>
              <Text size="sm" style={{ color: "var(--ink-2)", lineHeight: 1.5 }}>
                <b style={{ color: "var(--ink)" }}>You&apos;re viewing, not filing.</b> Everything in Documents is
                shared with family viewers except files linked to a login or bank credential — those never appear
                here.
              </Text>
            </Card>
          )}
        </div>
      )}

      {mayWrite && (
        <UploadSheet opened={uploadOpen} onClose={() => setUploadOpen(false)} initialCategory={uploadCategory} />
      )}
    </div>
  );
}

// ─── Pieces ──────────────────────────────────────────────────

function SectionHeading({ id, title, count }: { id: string; title: string; count?: number }) {
  return (
    <div className="mb-2 flex items-baseline justify-between gap-3">
      <h3 id={id} className="m-0 text-base font-semibold" style={{ letterSpacing: "-.005em" }}>
        {title}
        {count != null && (
          <>
            {" "}
            <span className="font-medium" style={{ color: "var(--ink-3)" }}>
              {count}
            </span>
          </>
        )}
      </h3>
    </div>
  );
}

function LoadingHub() {
  return (
    <div aria-busy="true" aria-label="Loading documents" data-documents-loading>
      <Skeleton height={16} width={140} mb="sm" radius="sm" />
      <Skeleton height={200} radius="md" mb="lg" />
      <Skeleton height={16} width={110} mb="sm" radius="sm" />
      <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3 lg:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} height={64} radius="md" />
        ))}
      </div>
    </div>
  );
}

function EmptyHub({
  canWrite,
  categoryView,
  onAdd,
}: {
  canWrite: boolean;
  categoryView: Category | null;
  onAdd: () => void;
}) {
  if (categoryView) {
    return (
      <div data-documents-empty data-documents-empty-category>
        <Title order={3} mt="md" className="text-xl font-semibold text-ink">
          Nothing in {CATEGORY_LABELS[categoryView]} yet
        </Title>
        <Text size="sm" mt={6} style={{ color: "var(--ink-2)", lineHeight: 1.55, maxWidth: 560 }}>
          A document filed as {CATEGORY_SINGULAR[categoryView].toLowerCase()} shows up here. File one with the button
          below, or change a document&apos;s category from its own page.
        </Text>
        {canWrite && (
          <Button mt="md" onClick={onAdd} leftSection={<IconPlus size={16} stroke={ICON_STROKE} />} className="max-md:min-h-11">
            Add to {CATEGORY_LABELS[categoryView]}
          </Button>
        )}
      </div>
    );
  }
  return (
    <div data-documents-empty>
      <Title order={3} mt="md" className="text-xl font-semibold text-ink">
        No documents yet
      </Title>
      <Text size="sm" mt={6} style={{ color: "var(--ink-2)", lineHeight: 1.55, maxWidth: 620 }}>
        Snap a bill, a school form or the washer&apos;s warranty card, and Home OS files it, reads its text so you
        can search it later, and warns you before anything expires. Everything you file appears here, grouped by
        category.
      </Text>
      <div className="mt-4 overflow-hidden rounded-token-md border border-line bg-surface" style={{ maxWidth: 620 }}>
        <div className="flex flex-wrap items-start gap-3.5 border-b border-line px-4 py-3.5 sm:flex-nowrap sm:items-center">
          <span aria-hidden style={{ width: 40, height: 40, flex: "none", display: "grid", placeItems: "center", borderRadius: 10, background: "var(--accent-soft)", color: "var(--accent)" }}>
            <IconCamera size={20} stroke={ICON_STROKE} />
          </span>
          <div className="min-w-0 flex-1">
            <Text fw={600} size="sm">
              Start with what&apos;s on the counter
            </Text>
            <Text size="xs" style={{ color: "var(--ink-3)" }}>
              The insurance renewal, a school form, the receipt you&apos;ll want at tax time
            </Text>
          </div>
          {canWrite && (
            // The wrapper takes the full row on a phone (Mantine's Button sets
            // its own width, so the width class goes on the wrapper), so the
            // copy is never squeezed to three words a line.
            <div className="w-full sm:w-auto" style={{ flex: "none" }}>
              <Button
                size="sm"
                onClick={onAdd}
                leftSection={<IconPlus size={16} stroke={ICON_STROKE} />}
                className="max-md:min-h-11"
                fullWidth
                data-documents-empty-add
              >
                Add
              </Button>
            </div>
          )}
        </div>
        <div className="flex items-center gap-3.5 px-4 py-3.5">
          <span aria-hidden style={{ width: 40, height: 40, flex: "none", display: "grid", placeItems: "center", borderRadius: 10, background: "var(--surface-2)", color: "var(--ink-2)" }}>
            <IconSearch size={20} stroke={ICON_STROKE} />
          </span>
          <div style={{ flex: 1, minWidth: 0 }}>
            <Text fw={600} size="sm">
              Then find it by meaning
            </Text>
            <Text size="xs" style={{ color: "var(--ink-3)" }}>
              &ldquo;car insurance&rdquo; finds a policy titled Odyssey, because the text is searchable too
            </Text>
          </div>
        </div>
      </div>
      {!canWrite && (
        <Text size="xs" mt={10} style={{ color: "var(--ink-3)" }}>
          You&apos;re a viewer in this family, so filing is done by the family — everything they file appears here.
        </Text>
      )}
    </div>
  );
}
