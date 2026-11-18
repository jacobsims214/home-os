"use client";

/**
 * One document (#4569): preview, what it is, its text and what it's linked to.
 *
 * Built to the approved documents mockup (#4520, "Document" and "Edit" states;
 * the mockup drew the detail as a sheet over the hub, and this page is the same
 * content at the same order — preview, read text, details, links — as a route
 * so a document can be linked, bookmarked and shared, and so the browser Back
 * button is the hub without a client-side stack to unwind).
 *
 * THE TEXT PROVENANCE NOTICE IS THE POINT OF THIS PAGE (#4941). A document's
 * body can be:
 *   - the text that was filed, byte for byte (`text_origin: "raw"`,
 *     `text_verified: true`) — then it IS the original wording;
 *   - a body that was changed or re-typed after filing (`raw` but
 *     `text_verified: false`) — the case the whole column exists for: a $35
 *     bill filed as $5 with the text re-typed to match, which was then reported
 *     as "verified";
 *   - text read from the image by OCR (`extracted`) — derived, may differ.
 * The wording comes from components/files/file-display.ts, the one place that
 * decides what a file's text may claim, and it is rendered HERE in full,
 * right above the text, not only as a tooltip on a chip. A person about to
 * trust a number must see which of the three they are looking at.
 *
 * PREVIEW follows what the browser can really do with the bytes the BFF
 * streams from GET /files/{id}/content: images and PDFs inline, plain text in
 * a <pre>, everything else a download. Bytes are fetched and shown from a blob
 * URL (the XSS-hardening pattern in architecture/file-module.md), never pointed
 * at directly. A preview that FAILS says so and keeps Download working; it never
 * renders a blank box that looks like an empty document.
 *
 * ROLE POLICY (#4689): a viewer reads this page in full — text, details, links,
 * download — but no Edit and no Delete. A housesitter or vendor gets the same
 * "not your section" panel the hub shows. Credential-linked files are 404 for a
 * viewer at the API, which this page renders as "not found" — exactly like a
 * missing id, because that is the rule.
 *
 * WHAT THE API DOESN'T HAVE: no reverse link lookup (links pointing AT this
 * file are not readable from it), no extracted fields (provider/amount), no
 * per-document version history. The page shows what exists and says what
 * doesn't in the words it uses, rather than inventing sections.
 */

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import {
  Button,
  Card,
  Group,
  Select,
  Skeleton,
  Stack,
  TagsInput,
  Text,
  Textarea,
  TextInput,
  Title,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconArrowLeft,
  IconDownload,
  IconExternalLink,
  IconLink,
  IconPencil,
  IconRefresh,
  IconTag,
  IconTrash,
} from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import { useNavRole } from "@/components/shared/use-nav-role";
import { canSeeFiles, canWriteFiles } from "@/lib/resource-access";
import FileTextMarker from "@/components/files/FileTextMarker";
import { bffPath } from "@/lib/api";
import { useDeleteDocument, useDocument, useDocumentLinks, usePeople, useUpdateDocument } from "@/lib/documents/api";
import {
  CATEGORIES,
  CATEGORY_SINGULAR,
  categoryOf,
  displayTitle,
  expiryOf,
  fileKind,
  formatDay,
  formatSize,
  textState,
  todayString,
  type Category,
} from "@/lib/documents/model";
import { attachmentOf, documentLinks, metadataPatch } from "@/lib/documents/entity-links";
import { CategoryIcon } from "@/components/documents/categories";
import type { FileRecord } from "@/lib/types/api";

const ICON_STROKE = NAV_ICON_STROKE;

export default function DocumentDetailPage() {
  const params = useParams<{ id: string }>();
  const id = params?.id;
  const role = useNavRole();
  // Fails closed while the role loads (see the hub's note): no request a sitter
  // would only get a 403 for, and no hub drawn then withdrawn.
  const roleKnown = role != null && role !== "";
  const mayRead = roleKnown && canSeeFiles(role);
  const mayWrite = roleKnown && canWriteFiles(role);

  const docQ = useDocument(id, mayRead);
  const linksQ = useDocumentLinks(id, mayRead);
  const doc = docQ.data?.data ?? null;

  if (!roleKnown) return <DetailSkeleton />;
  if (!mayRead) return <BlockedPanel />;

  if (docQ.isLoading) return <DetailSkeleton />;

  if (docQ.isError) {
    const status = (docQ.error as { status?: number } | null)?.status;
    // A credential-linked file is a 404 for a viewer, byte-identical to a
    // missing one (#4775). Both say the same thing, because they ARE the same
    // thing to this person.
    return (
      <div className="mx-auto w-full max-w-4xl" data-document-missing>
        <BackLink />
        <Card radius="token-md" p="lg" withBorder mt="md" role={status === 404 ? undefined : "alert"}>
          <Title order={2} className="text-lg font-semibold text-ink">
            {status === 404 ? "This document isn't here" : "Couldn't load this document"}
          </Title>
          <Text size="sm" mt={6} style={{ color: "var(--ink-2)", lineHeight: 1.5 }}>
            {status === 404
              ? "It may have been deleted, or it belongs to a family you're not in."
              : "Home OS didn't answer. The document itself is safe — try again."}
          </Text>
          {status !== 404 && (
            <Button
              variant="default"
              mt="sm"
              leftSection={<IconRefresh size={16} stroke={ICON_STROKE} />}
              onClick={() => docQ.refetch()}
              className="max-md:min-h-11"
            >
              Try again
            </Button>
          )}
          <Button component={Link} href="/dashboard/documents" variant="subtle" mt="sm" ml="xs" className="max-md:min-h-11">
            All documents
          </Button>
        </Card>
      </div>
    );
  }

  if (!doc) {
    return (
      <div className="mx-auto w-full max-w-4xl" data-document-missing>
        <BackLink />
        <Text mt="md" size="sm" style={{ color: "var(--ink-2)" }}>
          This document isn&apos;t here.
        </Text>
      </div>
    );
  }

  return (
    <DocumentDetail
      file={doc}
      mayWrite={mayWrite}
      links={documentLinks(linksQ.data?.data?.other)}
      linksLoading={linksQ.isLoading}
    />
  );
}

// ─── The page ────────────────────────────────────────────────

function DocumentDetail({
  file,
  mayWrite,
  links,
  linksLoading,
}: {
  file: FileRecord;
  mayWrite: boolean;
  links: ReturnType<typeof documentLinks>;
  linksLoading: boolean;
}) {
  const router = useRouter();
  const today = todayString();
  const category = categoryOf(file);
  const expiry = expiryOf(file, today);
  const state = textState(file);
  const attachment = attachmentOf(file);
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const del = useDeleteDocument(file.id);

  const onDelete = async () => {
    try {
      await del.mutateAsync();
      notifications.show({ title: "Deleted", message: `${displayTitle(file)} and its file are gone.`, color: "gray" });
      router.push("/dashboard/documents");
    } catch {
      notifications.show({ title: "Couldn't delete it", message: "Nothing was deleted. Try again.", color: "red" });
      setConfirmDelete(false);
    }
  };

  return (
    <div className="mx-auto w-full max-w-4xl" data-document-detail data-document-id={file.id}>
      <BackLink />

      {/* Header: what this is, at a glance. */}
      <div className="mb-4 flex items-start gap-3.5">
        <span
          aria-hidden
          style={{
            width: 56,
            height: 56,
            flex: "none",
            display: "grid",
            placeItems: "center",
            borderRadius: 14,
            background: category === "other" ? "var(--surface-2)" : "var(--accent-soft)",
            color: category === "other" ? "var(--ink-2)" : "var(--accent)",
          }}
        >
          <CategoryIcon category={category} size={28} />
        </span>
        <div style={{ flex: 1, minWidth: 0 }}>
          <Title order={2} className="text-[22px] font-semibold text-ink" style={{ lineHeight: 1.2, overflowWrap: "anywhere" }} data-document-title>
            {displayTitle(file)}
          </Title>
          <Text size="sm" mt={2} style={{ color: "var(--ink-3)", overflowWrap: "anywhere" }}>
            {[fileKind(file.content_type, file.name), formatSize(file.size), file.uploaded_by_name ? `filed by ${file.uploaded_by_name}` : null]
              .filter(Boolean)
              .join(" · ")}
          </Text>
          <div className="mt-2 flex flex-wrap gap-1.5">
            <span className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[12px] font-semibold leading-5" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>
              <CategoryIcon category={category} size={13} />
              {CATEGORY_SINGULAR[category]}
            </span>
            {expiry.state !== "none" && (
              <span
                className="inline-flex items-center rounded-full px-2 py-0.5 text-[12px] font-semibold leading-5"
                style={
                  expiry.state === "expired"
                    ? { background: "var(--danger-soft, #fff5f5)", color: "var(--danger)" }
                    : expiry.state === "soon"
                      ? { background: "var(--warn-bg)", color: "var(--warn-ink)" }
                      : { background: "var(--surface-2)", color: "var(--ink-2)" }
                }
                data-expiry={expiry.state}
              >
                {expiry.label}
                {file.expires_on ? ` · ${formatDay(file.expires_on)}` : ""}
              </span>
            )}
          </div>
        </div>
      </div>

      {expiry.state === "expired" && (
        <div
          role="status"
          className="mb-4 flex items-start gap-2.5 rounded-token-md border px-3 py-2.5"
          style={{ background: "var(--danger-soft, #fff5f5)", borderColor: "var(--danger-line, #ffc9c9)", color: "var(--danger)" }}
        >
          <IconAlertTriangle size={18} stroke={ICON_STROKE} aria-hidden style={{ flex: "none", marginTop: 1 }} />
          <Text size="sm" style={{ lineHeight: 1.45 }}>
            <b>This one has expired.</b> The document is still here and downloadable — the date passed on{" "}
            {formatDay(file.expires_on)}.
          </Text>
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)] lg:items-start">
        <div className="grid gap-4">
          <PreviewSection file={file} />

          <section aria-labelledby="doc-text">
            <h3 id="doc-text" className="mb-2 flex items-center gap-2 text-[13px] font-semibold uppercase tracking-wider" style={{ color: "var(--ink-3)" }}>
              Read text
              <FileTextMarker file={file} />
            </h3>
            <TextSection file={file} state={state} />
          </section>
        </div>

        <div className="grid gap-4">
          <section aria-labelledby="doc-details">
            <div className="mb-2 flex items-baseline justify-between">
              <h3 id="doc-details" className="m-0 text-[13px] font-semibold uppercase tracking-wider" style={{ color: "var(--ink-3)" }}>
                Details
              </h3>
              {mayWrite && !editing && (
                <Button
                  size="compact-sm"
                  variant="subtle"
                  leftSection={<IconPencil size={14} stroke={ICON_STROKE} />}
                  onClick={() => setEditing(true)}
                  className="max-md:min-h-11"
                  data-document-edit
                >
                  Edit
                </Button>
              )}
            </div>
            {editing ? (
              <MetadataForm file={file} onDone={() => setEditing(false)} />
            ) : (
              <DetailsList file={file} attachment={attachment} />
            )}
          </section>

          <section aria-labelledby="doc-links">
            <h3 id="doc-links" className="mb-2 text-[13px] font-semibold uppercase tracking-wider" style={{ color: "var(--ink-3)" }}>
              Linked to
            </h3>
            <LinksSection links={links} loading={linksLoading} />
          </section>

          <section aria-label="File">
            <div className="flex flex-wrap gap-2">
              <DownloadButton file={file} />
              {mayWrite && !editing && (
                <Button
                  variant="default"
                  color="red"
                  leftSection={<IconTrash size={16} stroke={ICON_STROKE} />}
                  onClick={() => setConfirmDelete(true)}
                  className="max-md:min-h-11"
                  data-document-delete
                >
                  Delete
                </Button>
              )}
            </div>
            {confirmDelete && (
              <Card radius="token-md" p="md" withBorder mt="sm" role="alertdialog" aria-label="Delete this document" data-document-delete-confirm>
                <Text fw={600} size="sm">
                  Delete {displayTitle(file)}?
                </Text>
                <Text size="sm" mt={4} style={{ color: "var(--ink-2)", lineHeight: 1.45 }}>
                  The file and its text are removed for the whole family. This can&apos;t be undone.
                </Text>
                <Group gap={8} mt="sm">
                  <Button color="red" size="sm" loading={del.isPending} onClick={onDelete} className="max-md:min-h-11" data-document-delete-yes>
                    Delete it
                  </Button>
                  <Button variant="default" size="sm" onClick={() => setConfirmDelete(false)} disabled={del.isPending} className="max-md:min-h-11">
                    Keep it
                  </Button>
                </Group>
              </Card>
            )}
          </section>
        </div>
      </div>
    </div>
  );
}

function BackLink() {
  return (
    <Link
      href="/dashboard/documents"
      className="mb-3 inline-flex items-center gap-1 py-2 text-[13px] font-semibold"
      style={{ color: "var(--accent)" }}
    >
      <IconArrowLeft size={16} stroke={ICON_STROKE} aria-hidden />
      Documents
    </Link>
  );
}

// ─── Preview ─────────────────────────────────────────────────

type PreviewKind = "pdf" | "image" | "text" | "other";

function previewKind(contentType: string | null | undefined): PreviewKind {
  const ct = (contentType ?? "").toLowerCase();
  if (ct === "application/pdf") return "pdf";
  if (ct.startsWith("image/")) return "image";
  if (ct.startsWith("text/") || ct === "application/json" || ct === "application/xml") return "text";
  return "other";
}

function PreviewSection({ file }: { file: FileRecord }) {
  const kind = previewKind(file.content_type);
  const [url, setUrl] = useState<string | null>(null);
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(kind !== "other");
  // A file whose bytes don't decode (a truncated scan, a HEIC) must say so
  // rather than showing a broken-image glyph that looks like an empty document.
  const [broken, setBroken] = useState(false);
  const urlRef = useRef<string | null>(null);

  const contentHref = bffPath(`/api/v1/files/${file.id}/content`);

  useEffect(() => {
    let cancelled = false;
    setError(null);
    setText(null);
    setBroken(false);
    if (kind === "other") {
      setLoading(false);
      return;
    }
    setLoading(true);
    (async () => {
      try {
        const res = await fetch(contentHref, { credentials: "same-origin" });
        if (!res.ok) throw new Error(String(res.status));
        if (kind === "text") {
          const body = await res.text();
          if (!cancelled) setText(body);
        } else {
          const blob = await res.blob();
          if (cancelled) return;
          const objectUrl = URL.createObjectURL(blob);
          urlRef.current = objectUrl;
          setUrl(objectUrl);
        }
      } catch {
        if (!cancelled) setError("Home OS couldn't load the preview.");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
      if (urlRef.current) {
        URL.revokeObjectURL(urlRef.current);
        urlRef.current = null;
      }
      setUrl(null);
    };
  }, [contentHref, kind]);

  return (
    <section aria-labelledby="doc-preview" data-document-preview={kind}>
      <h3 id="doc-preview" className="mb-2 text-[13px] font-semibold uppercase tracking-wider" style={{ color: "var(--ink-3)" }}>
        {kind === "other" ? "File" : "Preview"}
      </h3>
      {loading && <Skeleton height={kind === "image" ? 220 : 320} radius="md" />}
      {!loading && error && (
        <Card radius="token-md" p="md" withBorder role="alert" style={{ borderColor: "var(--warn-line)", background: "var(--warn-bg)", color: "var(--warn-ink)" }}>
          <Text fw={600} size="sm">
            {error}
          </Text>
          <Text size="sm" mt={2} style={{ lineHeight: 1.45 }}>
            The file itself is fine — download it to open it.
          </Text>
        </Card>
      )}
      {!loading && !error && kind === "pdf" && url && (
        <iframe src={url} title={file.name} className="w-full rounded-token-md border border-line bg-surface" style={{ height: 420 }} />
      )}
      {!loading && !error && !broken && kind === "image" && url && (
        <div className="flex items-center justify-center overflow-auto rounded-token-md border border-line" style={{ background: "var(--surface-2)", padding: 8, maxHeight: 420 }}>
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src={url}
            alt={displayTitle(file)}
            onError={() => setBroken(true)}
            style={{ maxWidth: "100%", maxHeight: 400, objectFit: "contain" }}
          />
        </div>
      )}
      {!loading && !error && broken && (
        <Card radius="token-md" p="md" withBorder style={{ background: "var(--surface-2)" }} data-document-preview-broken>
          <Text size="sm" style={{ color: "var(--ink-2)", lineHeight: 1.5 }}>
            The browser couldn&apos;t draw this image. The file is fine — download it to open it.
          </Text>
        </Card>
      )}
      {!loading && !error && kind === "text" && text !== null && (
        <pre className="max-h-[420px] overflow-auto whitespace-pre-wrap rounded-token-md border border-line p-4 text-xs" style={{ background: "var(--surface-2)", color: "var(--ink-2)", overflowWrap: "anywhere" }}>
          {text}
        </pre>
      )}
      {!loading && !error && kind === "other" && (
        <Card radius="token-md" p="md" withBorder style={{ background: "var(--surface-2)" }}>
          <Text size="sm" style={{ color: "var(--ink-2)", lineHeight: 1.5 }}>
            There&apos;s no preview for a {fileKind(file.content_type, file.name).toLowerCase()} in the browser. Download it to
            open it.
          </Text>
        </Card>
      )}
      <Text size="xs" mt={6} style={{ color: "var(--ink-3)" }}>
        {[formatSize(file.size), file.content_type || "unknown type", `added ${formatDay(file.created_at.slice(0, 10))}`]
          .filter(Boolean)
          .join(" · ")}
      </Text>
    </section>
  );
}

// ─── Read text ───────────────────────────────────────────────

/**
 * The text, with the sentence that says WHAT it is (#4941).
 *
 * `describeFileText` (components/files/file-display.ts) is the one place that
 * decides this, and the sentence is printed in full above the text: a derived
 * or re-typed body must not read as the original document, and a chip with a
 * tooltip is not enough when someone is about to trust a number.
 */
function TextSection({ file, state }: { file: FileRecord; state: ReturnType<typeof textState> }) {
  const hasText = typeof file.extracted_text === "string" && file.extracted_text.length > 0;
  const kindLabel =
    state.kind === "retrying" ? "Retrying" : state.kind === "failed" ? "Couldn't read it" : state.kind === "pending" ? "Reading it now" : state.kind === "none" ? "No text" : "Ready";

  return (
    <div className="grid gap-2">
      {/* The provenance notice, before the text it is about. */}
      {hasText && (
        <div data-testid="document-text-provenance" data-provenance={provenanceKey(file)}>
          <p
            className="m-0 rounded-token-sm border px-3 py-2 text-xs leading-relaxed"
            style={
              provenanceKey(file) === "altered"
                ? { borderColor: "var(--warn-line)", background: "var(--warn-bg)", color: "var(--warn-ink)" }
                : { borderColor: "var(--line)", background: "var(--surface-2)", color: "var(--ink-2)" }
            }
          >
            {describeProvenance(file)}
          </p>
        </div>
      )}

      {state.kind !== "done" && (
        <p className="m-0 text-[13px] leading-relaxed" style={{ color: state.kind === "failed" ? "var(--warn-ink)" : "var(--ink-2)" }} data-testid="document-text-state">
          <b>{kindLabel}.</b> {state.note}
          {state.error && state.kind === "failed" ? (
            <span style={{ display: "block", marginTop: 2, color: "var(--ink-3)" }}>Home OS said: {state.error}</span>
          ) : null}
        </p>
      )}

      {hasText && (
        <pre
          className="max-h-[320px] overflow-auto whitespace-pre-wrap rounded-token-md border border-line p-3.5 text-xs"
          style={{ background: "var(--surface-2)", color: "var(--ink-2)", overflowWrap: "anywhere", fontFamily: "var(--font-mono, ui-monospace, monospace)" }}
          data-testid="document-text"
        >
          {file.extracted_text}
        </pre>
      )}
    </div>
  );
}

/** The provenance key the page renders, from the shared display module. */
function provenanceKey(file: FileRecord) {
  const hasText = typeof file.extracted_text === "string" && file.extracted_text.length > 0;
  if (!hasText) return "none";
  if (file.text_origin === "raw") return file.text_verified === true ? "source" : "altered";
  return "derived";
}

function describeProvenance(file: FileRecord): string {
  switch (provenanceKey(file)) {
    case "source":
      return "This text is exactly what was filed — the original wording, unaltered.";
    case "altered":
      return "This text no longer matches what was filed: it was changed or re-typed after upload. Treat the file above as the original, not this text.";
    case "derived":
      return "This text was read from the file automatically (OCR). It is not the original document and may differ from it — check the file itself before relying on a number or a date.";
    default:
      return "This file has no extracted text.";
  }
}

// ─── Details ─────────────────────────────────────────────────

function DetailsList({ file, attachment }: { file: FileRecord; attachment: ReturnType<typeof attachmentOf> }) {
  const peopleQ = usePeople(true);
  const person = (peopleQ.data?.data ?? []).find((p) => p.id === file.person_id) ?? null;
  const rows: Array<[string, React.ReactNode]> = [
    ["Document date", file.document_date ? formatDay(file.document_date) : "Not set"],
    ["Expires", file.expires_on ? formatDay(file.expires_on) : "No expiry"],
    ["For", person ? person.display_name : file.person_id ? "A person who has left the family" : "Nobody in particular"],
    ["Filed", formatDay(file.created_at.slice(0, 10)) + (file.uploaded_by_name ? ` by ${file.uploaded_by_name}` : "")],
    [
      "Belongs to",
      attachment ? (
        attachment.href ? (
          <Link href={attachment.href} style={{ color: "var(--accent)", fontWeight: 500 }}>
            {attachment.noun} page
          </Link>
        ) : (
          attachment.noun
        )
      ) : (
        "The whole family"
      ),
    ],
    ["File name", <span key="n" style={{ fontFamily: "var(--font-mono, ui-monospace, monospace)", fontSize: 13 }}>{file.name}</span>],
  ];
  return (
    <div className="grid gap-0" data-document-details>
      <dl className="m-0">
        {rows.map(([label, value]) => (
          <div key={label} className="flex flex-col gap-0.5 border-b border-line py-2.5 last:border-b-0 md:grid md:grid-cols-[minmax(6rem,1fr)_minmax(0,2fr)] md:gap-6 md:items-baseline">
            <dt className="text-[13px]" style={{ color: "var(--ink-3)" }}>
              {label}
            </dt>
            <dd className="m-0 text-[15px]" style={{ overflowWrap: "anywhere" }}>
              {value}
            </dd>
          </div>
        ))}
      </dl>
      <div className="mt-3">
        <div className="mb-1.5 flex items-center gap-1.5 text-[13px]" style={{ color: "var(--ink-3)" }}>
          <IconTag size={14} stroke={ICON_STROKE} aria-hidden />
          Tags
        </div>
        {(file.tags ?? []).length > 0 ? (
          <div className="flex flex-wrap gap-1.5" data-document-tags>
            {(file.tags ?? []).map((t) => (
              <span key={t} className="rounded-full px-2 py-0.5 text-[12px] leading-5" style={{ background: "var(--surface-2)", color: "var(--ink-2)" }}>
                {t}
              </span>
            ))}
          </div>
        ) : (
          <Text size="sm" style={{ color: "var(--ink-3)" }}>
            No tags. Tags are words you&apos;d search for — an insurer, a room, a kid.
          </Text>
        )}
      </div>
      {file.description ? (
        <div className="mt-3">
          <div className="mb-1 text-[13px]" style={{ color: "var(--ink-3)" }}>
            Notes
          </div>
          <Text size="sm" style={{ color: "var(--ink)", lineHeight: 1.5, overflowWrap: "anywhere" }} data-document-description>
            {file.description}
          </Text>
        </div>
      ) : null}
    </div>
  );
}

// ─── Edit ────────────────────────────────────────────────────

function MetadataForm({ file, onDone }: { file: FileRecord; onDone: () => void }) {
  const update = useUpdateDocument(file.id);
  const peopleQ = usePeople(true);
  const [title, setTitle] = useState(file.title ?? "");
  const [category, setCategory] = useState<Category>(categoryOf(file));
  const [documentDate, setDocumentDate] = useState(file.document_date ?? "");
  const [expiresOn, setExpiresOn] = useState(file.expires_on ?? "");
  const [personId, setPersonId] = useState<string | null>(file.person_id ?? null);
  const [description, setDescription] = useState(file.description ?? "");
  const [tags, setTags] = useState<string[]>(file.tags ?? []);
  const [error, setError] = useState<string | null>(null);

  const dateProblem = documentDate && expiresOn && expiresOn < documentDate ? "The expiry is before the document's date — check one of the two." : null;

  const save = async () => {
    if (dateProblem) {
      setError(dateProblem);
      return;
    }
    const patch = metadataPatch(file, { title, category, documentDate, expiresOn, description, personId, tags });
    if (Object.keys(patch).length === 0) {
      onDone();
      return;
    }
    try {
      await update.mutateAsync(patch as Parameters<typeof update.mutateAsync>[0]);
      notifications.show({ title: "Saved", message: "The details are updated for the whole family.", color: "gray" });
      onDone();
    } catch (err) {
      const status = (err as { status?: number })?.status;
      const body = (err as { body?: { error?: { message?: string } } })?.body;
      setError(
        status === 400 && body?.error?.message
          ? body.error.message
          : "Home OS didn't save that. Nothing was changed — try again.",
      );
    }
  };

  return (
    <Card radius="token-md" p="md" withBorder data-document-edit-form>
      <Stack gap="sm">
        <TextInput
          label="Title"
          description={`Leave blank to show the file name: ${file.name}`}
          value={title}
          onChange={(e) => setTitle(e.currentTarget.value)}
          classNames={{ input: "max-md:min-h-11 max-md:text-base" }}
          data-document-edit-title
        />
        <div>
          <Text size="sm" fw={500} mb={6}>
            Category
          </Text>
          <div role="group" aria-label="Category" style={{ display: "grid", gridTemplateColumns: "repeat(3, minmax(0, 1fr))", gap: 6 }}>
            {CATEGORIES.map((c) => {
              const on = c === category;
              return (
                <button
                  key={c}
                  type="button"
                  aria-pressed={on}
                  onClick={() => setCategory(c)}
                  data-category={c}
                  style={{
                    minHeight: 44,
                    padding: "6px 4px",
                    borderRadius: "var(--r-sm)",
                    border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`,
                    background: on ? "var(--accent-soft)" : "var(--surface)",
                    color: on ? "var(--accent)" : "var(--ink-2)",
                    fontWeight: on ? 600 : 500,
                    fontSize: 12,
                    cursor: "pointer",
                  }}
                >
                  {CATEGORY_SINGULAR[c]}
                </button>
              );
            })}
          </div>
        </div>
        <Group grow align="flex-start" gap="sm">
          <TextInput
            type="date"
            label="Document date"
            value={documentDate}
            onChange={(e) => setDocumentDate(e.currentTarget.value)}
            error={error ?? undefined}
            classNames={{ input: "max-md:min-h-11 max-md:text-base" }}
            data-document-edit-date
          />
          <TextInput
            type="date"
            label="Expires"
            value={expiresOn}
            onChange={(e) => setExpiresOn(e.currentTarget.value)}
            classNames={{ input: "max-md:min-h-11 max-md:text-base" }}
            data-document-edit-expires
          />
        </Group>
        <Select
          label="For"
          placeholder="Nobody in particular"
          clearable
          searchable
          data={(peopleQ.data?.data ?? []).map((p) => ({ value: p.id, label: p.display_name }))}
          value={personId}
          onChange={setPersonId}
          data-document-edit-person
          nothingFoundMessage="No people in this family yet"
        />
        <TagsInput label="Tags" value={tags} onChange={setTags} data-document-edit-tags />
        <Textarea
          label="Notes"
          description="Searchable too, and shown above"
          autosize
          minRows={2}
          value={description}
          onChange={(e) => setDescription(e.currentTarget.value)}
          data-document-edit-description
        />
        {error && !dateProblem && (
          <Text size="sm" role="alert" style={{ color: "var(--danger)" }}>
            {error}
          </Text>
        )}
        <Group gap={8} justify="flex-end">
          <Button variant="default" onClick={onDone} disabled={update.isPending} className="max-md:min-h-11">
            Cancel
          </Button>
          <Button onClick={save} loading={update.isPending} className="max-md:min-h-11" data-document-edit-save>
            Save
          </Button>
        </Group>
      </Stack>
    </Card>
  );
}

// ─── Links ───────────────────────────────────────────────────

function LinksSection({ links, loading }: { links: ReturnType<typeof documentLinks>; loading: boolean }) {
  if (loading) return <Skeleton height={72} radius="md" />;
  if (links.length === 0) {
    return (
      <Card radius="token-md" p="md" withBorder style={{ background: "var(--surface-2)" }} data-document-links-empty>
        <Text size="sm" style={{ color: "var(--ink-2)", lineHeight: 1.5 }}>
          Nothing is linked to this document yet. Links are made from a bill, a vehicle, a person or a thing — the
          &ldquo;Linked to&rdquo; row on that record.
        </Text>
      </Card>
    );
  }
  return (
    <div className="overflow-hidden rounded-token-md border border-line bg-surface" data-document-links>
      {links.map((l) => {
        const body = (
          <>
            <span aria-hidden style={{ width: 32, height: 32, flex: "none", display: "grid", placeItems: "center", borderRadius: 8, background: "var(--surface-2)", color: "var(--ink-2)" }}>
              <IconLink size={16} stroke={ICON_STROKE} />
            </span>
            <span style={{ flex: 1, minWidth: 0 }}>
              <span style={{ display: "block", fontWeight: 500, fontSize: 14, overflowWrap: "anywhere" }}>{l.title}</span>
              <span style={{ display: "block", fontSize: 12, color: "var(--ink-3)" }}>{l.detail}</span>
            </span>
            {l.href && <IconExternalLink size={16} stroke={ICON_STROKE} aria-hidden style={{ color: "var(--ink-3)", flex: "none" }} />}
          </>
        );
        const style: React.CSSProperties = {
          display: "flex",
          alignItems: "center",
          gap: 12,
          minHeight: 52,
          padding: "8px 14px",
          borderBottom: "1px solid var(--line)",
          color: "inherit",
          textDecoration: "none",
        };
        return l.href && l.external ? (
          <a key={l.id} href={l.href} target="_blank" rel="noopener noreferrer" style={style} data-document-link={l.kind}>
            {body}
          </a>
        ) : l.href ? (
          <Link key={l.id} href={l.href} style={style} data-document-link={l.kind}>
            {body}
          </Link>
        ) : (
          <div key={l.id} style={style} data-document-link={l.kind}>
            {body}
          </div>
        );
      })}
    </div>
  );
}

// ─── Download ────────────────────────────────────────────────

function DownloadButton({ file }: { file: FileRecord }) {
  const [busy, setBusy] = useState(false);
  const href = bffPath(`/api/v1/files/${file.id}/content`);

  const download = async () => {
    setBusy(true);
    try {
      const res = await fetch(href, { credentials: "same-origin" });
      if (!res.ok) throw new Error(String(res.status));
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = file.name || "document";
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch {
      notifications.show({ title: "Couldn't download it", message: "The download didn't start. Try again.", color: "red" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Button
      variant="default"
      leftSection={<IconDownload size={16} stroke={ICON_STROKE} />}
      onClick={download}
      loading={busy}
      className="max-md:min-h-11"
      data-document-download
    >
      Download
    </Button>
  );
}

// ─── Shells ──────────────────────────────────────────────────

function DetailSkeleton() {
  return (
    <div className="mx-auto w-full max-w-4xl" aria-busy="true" aria-label="Loading document">
      <Skeleton height={16} width={110} mb="md" radius="sm" />
      <Group gap={14} mb="md" align="flex-start">
        <Skeleton height={56} width={56} radius="md" />
        <div style={{ flex: 1 }}>
          <Skeleton height={22} width="60%" radius="sm" />
          <Skeleton height={14} width="40%" mt={8} radius="sm" />
        </div>
      </Group>
      <Skeleton height={320} radius="md" mb="md" />
      <Skeleton height={140} radius="md" />
    </div>
  );
}

function BlockedPanel() {
  return (
    <div className="mx-auto w-full max-w-4xl" data-documents-blocked>
      <BackLink />
      <Card radius="token-md" p="lg" withBorder role="status" style={{ background: "var(--surface-2)" }}>
        <Text fw={600}>Documents is for the family</Text>
        <Text size="sm" mt={6} style={{ color: "var(--ink-2)", lineHeight: 1.5 }}>
          Your role in this family doesn&apos;t include household paperwork, so this document isn&apos;t shown.
        </Text>
        <Button component={Link} href="/dashboard" variant="default" mt="md" className="max-md:min-h-11">
          Back to Today
        </Button>
      </Card>
    </div>
  );
}
