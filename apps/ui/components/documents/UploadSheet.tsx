"use client";

/**
 * UploadSheet — filing a document from a phone (#4569).
 *
 * The mockup's flow (artifact #4520) is scan → reading → details → saved, and
 * the reason is the counter at a shop: a parent photographs the receipt and
 * wants it filed before they walk away. So the camera is the FIRST control,
 * not a link buried behind a file picker, and the whole flow fits one sheet.
 *
 * What the API actually does shapes the steps honestly (#4478):
 *   - The upload is ONE multipart POST carrying the file and the metadata; the
 *     record comes back with `ocr_status: "pending"` and the worker reads the
 *     text afterwards. There is no "reading found $64.19, dated Nov 12" answer
 *     to pre-fill from — the API does not return extracted fields, only the raw
 *     text — so the details step is NOT auto-filled with values nobody gave.
 *     It shows the file, asks the few fields the API stores, and says plainly
 *     that the text is read after filing.
 *   - After the POST the sheet polls GET /files/{id} until the OCR status is
 *     terminal, so "Filed" is only shown once the record really exists, and a
 *     reading problem is reported as reading, with the file still filed.
 *
 * Failure is a state of its own: the sheet never closes on an error and never
 * claims a file was saved when it wasn't. A write that never reached the server
 * says "nothing was saved" (lib/api.ts's offline rule), and each HTTP status
 * gets the sentence that helps (25 MB limit vs. role refused vs. server down).
 *
 * Phones: ResponsiveModal gives a bottom sheet with 44 px targets and 16 px
 * inputs (no iOS zoom) and keeps the actions above the keyboard (#4532).
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Group,
  Select,
  Stack,
  TagsInput,
  Text,
  Textarea,
  TextInput,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconCamera,
  IconCheck,
  IconFile,
  IconPhoto,
  IconRefresh,
  IconUpload,
  IconX,
} from "@tabler/icons-react";
import { useQueryClient } from "@tanstack/react-query";
import { ResponsiveModal, ResponsiveModalActions } from "@/components/ui";
import { fileKeys } from "@/lib/query-keys";
import { apiFetch } from "@/lib/api";
import type { FileRecord } from "@/lib/types/api";
import { CategoryIcon, categoryLabel } from "@/components/documents/categories";
import {
  CATEGORIES,
  CATEGORY_LABELS,
  categoryOf,
  documentCategoryLabel,
  isFiled,
  cleanTags,
  formatSize,
  textState,
  type Category,
} from "@/lib/documents/model";
import {
  uploadDocument,
  uploadFailureMessage,
  usePeople,
  type UploadMetadata,
} from "@/lib/documents/api";

const ICON_STROKE = 1.75;
const MAX_BYTES = 25 * 1024 * 1024;
/** OCR statuses the worker will not move away from (mcp document.go's set). */
const TERMINAL = new Set(["done", "failed", "skipped"]);
const POLL_MS = 2500;

type Step = "choose" | "details" | "uploading" | "reading" | "saved" | "failed";

interface Props {
  opened: boolean;
  onClose: () => void;
  /** Preselect the category (the hub's tile / a category view). */
  initialCategory?: Category | null;
}

/** "photo.jpg" → "photo" — a starting title a person can type over. */
export function titleFromFileName(name: string): string {
  const base = name.replace(/\.[a-z0-9]{1,8}$/i, "").replace(/[_-]+/g, " ").trim();
  return base.charAt(0).toUpperCase() + base.slice(1);
}

/** A person's own file size, or the reason it can't be filed. */
function rejectReason(file: File): string | null {
  if (file.size === 0) return "That file is empty, so there is nothing to file.";
  if (file.size > MAX_BYTES) return "That file is larger than the 25 MB limit. A photo or a PDF is usually far smaller.";
  return null;
}

export default function UploadSheet({ opened, onClose, initialCategory = null }: Props) {
  const queryClient = useQueryClient();
  const cameraRef = useRef<HTMLInputElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const previewUrlRef = useRef<string | null>(null);

  const [step, setStep] = useState<Step>("choose");
  const [file, setFile] = useState<File | null>(null);
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const [progress, setProgress] = useState(0);
  const [created, setCreated] = useState<FileRecord | null>(null);
  const [failure, setFailure] = useState<{ title: string; body: string; canRetry: boolean } | null>(null);
  const [isDragging, setDragging] = useState(false);
  // A photo the browser can't decode (a HEIC on a non-Apple browser, a file
  // whose type lies) must not become a broken-image icon: fall back to the
  // file glyph and keep the name and size readable.
  const [previewBroken, setPreviewBroken] = useState(false);

  // The metadata fields. Title starts from the file name, which is a starting
  // point and never presented as something the document said.
  const [title, setTitle] = useState("");
  const [category, setCategory] = useState<Category>(initialCategory ?? "other");
  const [documentDate, setDocumentDate] = useState("");
  const [expiresOn, setExpiresOn] = useState("");
  const [personId, setPersonId] = useState<string | null>(null);
  const [description, setDescription] = useState("");
  const [tags, setTags] = useState<string[]>([]);
  const [dateError, setDateError] = useState<string | null>(null);

  const peopleQ = usePeople(opened);
  const people = peopleQ.data?.data ?? [];

  // Reset on close so a second document starts clean (and so no stale file or
  // progress flashes when the sheet reopens).
  useEffect(() => {
    if (opened) return;
    setStep("choose");
    setFile(null);
    setProgress(0);
    setCreated(null);
    setFailure(null);
    setDateError(null);
    setTags([]);
    setDescription("");
    setTitle("");
    setDocumentDate("");
    setExpiresOn("");
    setPersonId(null);
    setCategory(initialCategory ?? "other");
    if (previewUrlRef.current) {
      URL.revokeObjectURL(previewUrlRef.current);
      previewUrlRef.current = null;
    }
    setPreviewUrl(null);
    setPreviewBroken(false);
  }, [opened, initialCategory]);

  // Object URLs are revoked when replaced and on unmount; never in a setState
  // updater (StrictMode double-invokes those).
  useEffect(() => {
    return () => {
      if (previewUrlRef.current) URL.revokeObjectURL(previewUrlRef.current);
    };
  }, []);

  const accept = useCallback((picked: File | null | undefined) => {
    if (!picked) return;
    const reason = rejectReason(picked);
    if (reason) {
      setFailure({ title: "Can't file this one", body: reason, canRetry: false });
      setStep("failed");
      return;
    }
    if (previewUrlRef.current) URL.revokeObjectURL(previewUrlRef.current);
    const url = picked.type.startsWith("image/") ? URL.createObjectURL(picked) : null;
    previewUrlRef.current = url;
    setPreviewBroken(false);
    setPreviewUrl(url);
    setFile(picked);
    setTitle(titleFromFileName(picked.name));
    setStep("details");
  }, []);

  const onInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    accept(e.target.files?.[0]);
    // Reset so picking the same file again re-fires change.
    e.target.value = "";
  };

  /**
   * What the API will be asked to store. A date pair the API would refuse
   * (expiry before the document date) is caught here with the same rule, so a
   * person sees it before a round trip; the API still enforces it.
   */
  const metadata = useMemo<UploadMetadata>(
    () => ({
      title: title.trim() || undefined,
      category,
      document_date: documentDate || undefined,
      expires_on: expiresOn || undefined,
      description: description.trim() || undefined,
      person_id: personId ?? undefined,
    }),
    [title, category, documentDate, expiresOn, description, personId],
  );

  const dateProblem = useMemo(() => {
    if (documentDate && expiresOn && expiresOn < documentDate) {
      return "The expiry is before the document's date — check one of the two.";
    }
    return null;
  }, [documentDate, expiresOn]);

  const startUpload = async () => {
    if (!file) return;
    if (dateProblem) {
      setDateError(dateProblem);
      return;
    }
    setDateError(null);
    setFailure(null);
    setStep("uploading");
    setProgress(0);
    try {
      const record = await uploadDocument(file, metadata, setProgress);
      setCreated(record);
      setStep("reading");
      // Tags are a second call: the upload form has no tags field and the API's
      // PATCH is where they live (name/tags keep the old semantics, #4478).
      const wanted = cleanTags(tags);
      if (wanted.length > 0) {
        try {
          await apiFetch<{ data: FileRecord }>(`/api/v1/files/${record.id}`, {
            method: "PATCH",
            body: { tags: wanted },
          });
        } catch {
          // The document IS filed; only its tags didn't stick. Say so on the
          // saved step rather than failing an upload that succeeded.
          notifications.show({
            title: "Filed, but the tags didn't stick",
            message: "The document is saved. You can add the tags again from its page.",
            color: "yellow",
          });
        }
      }
      void queryClient.invalidateQueries({ queryKey: fileKeys.lists() });
    } catch (err) {
      const status = (err as { status?: number })?.status ?? 0;
      const body = (err as { body?: unknown })?.body;
      const offline = !!(err as { offline?: boolean })?.offline;
      setFailure({
        title: offline ? "You're offline" : "The upload didn't go through",
        body: offline ? "Nothing was saved. Reconnect and try again — the file is still on your phone." : uploadFailureMessage(status, body),
        canRetry: true,
      });
      setStep("failed");
    }
  };

  return (
    <ResponsiveModal
      opened={opened}
      onClose={onClose}
      title={step === "saved" ? "Filed" : step === "reading" ? "Reading it now" : "Add a document"}
      size="md"
      closeOnBack
    >
      {/* The test id lives on the body, not the Modal root: Mantine's root is a
          wrapper that stays "hidden" while the dialog itself is on screen. */}
      <Stack gap="md" pb={8} data-testid="upload-sheet">
        {/* The two hidden pickers: the camera one is the phone's camera; the
            other is any file (a PDF from email, a photo already taken). */}
        <input
          ref={cameraRef}
          type="file"
          accept="image/*"
          capture="environment"
          hidden
          onChange={onInputChange}
          data-testid="upload-camera-input"
        />
        <input ref={fileRef} type="file" hidden onChange={onInputChange} data-testid="upload-file-input" />

        {step === "choose" && (
          <ChooseStep
            dragging={isDragging}
            onCamera={() => cameraRef.current?.click()}
            onPick={() => fileRef.current?.click()}
            onDragOver={(e) => {
              e.preventDefault();
              setDragging(true);
            }}
            onDragLeave={() => setDragging(false)}
            onDrop={(e) => {
              e.preventDefault();
              setDragging(false);
              accept(e.dataTransfer.files?.[0]);
            }}
          />
        )}

        {step === "details" && file && (
          <DetailsStep
            file={file}
            previewUrl={previewBroken ? null : previewUrl}
            onPreviewBroken={() => setPreviewBroken(true)}
            title={title}
            onTitle={setTitle}
            category={category}
            onCategory={setCategory}
            documentDate={documentDate}
            onDocumentDate={setDocumentDate}
            expiresOn={expiresOn}
            onExpiresOn={setExpiresOn}
            dateError={dateError}
            personId={personId}
            onPersonId={setPersonId}
            people={people}
            description={description}
            onDescription={setDescription}
            tags={tags}
            onTags={setTags}
            onReplace={() => {
              setFile(null);
              setStep("choose");
            }}
          />
        )}

        {step === "uploading" && file && (
          <div data-testid="upload-progress">
            <Text fw={600}>{file.name}</Text>
            <Text size="sm" c="dimmed" mt={4}>
              Sending it to Home OS…
            </Text>
            <div
              role="progressbar"
              aria-valuenow={progress}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-label="Upload progress"
              style={{ height: 8, borderRadius: 4, background: "var(--surface-2)", marginTop: 14, overflow: "hidden" }}
            >
              <div style={{ height: "100%", width: `${progress}%`, background: "var(--accent-fill)", transition: "width 150ms ease-out" }} />
            </div>
            <Text size="xs" c="dimmed" mt={6}>
              {progress}% sent
            </Text>
          </div>
        )}

        {step === "reading" && created && <ReadingStep file={created} onDone={() => setStep("saved")} />}

        {step === "saved" && created && <SavedStep file={created} />}

        {step === "failed" && failure && (
          <div role="alert" data-testid="upload-failure" className="rounded-token-md border border-warn-line px-3 py-3" style={{ background: "var(--warn-bg)", color: "var(--warn-ink)" }}>
            <Group gap={8} wrap="nowrap" align="flex-start">
              <IconAlertTriangle size={20} stroke={ICON_STROKE} aria-hidden style={{ flex: "none", marginTop: 2 }} />
              <div style={{ minWidth: 0 }}>
                <Text fw={600} size="sm">
                  {failure.title}
                </Text>
                <Text size="sm" mt={2} style={{ lineHeight: 1.45 }}>
                  {failure.body}
                </Text>
              </div>
            </Group>
            <Group gap={8} mt="sm">
              {failure.canRetry && file && (
                <Button
                  size="sm"
                  leftSection={<IconRefresh size={16} stroke={ICON_STROKE} />}
                  onClick={() => {
                    // Back to the details step with every field as it was: the
                    // file is still on the phone, so a retry is one tap and
                    // nothing has to be re-typed.
                    setFailure(null);
                    setStep("details");
                  }}
                  className="max-md:min-h-11"
                  data-testid="upload-retry"
                >
                  Try again
                </Button>
              )}
              <Button size="sm" variant="default" onClick={onClose} className="max-md:min-h-11">
                Close
              </Button>
            </Group>
          </div>
        )}
      </Stack>

      {step === "details" && (
        <ResponsiveModalActions>
          <Button variant="default" onClick={onClose} className="max-md:min-h-11">
            Cancel
          </Button>
          <Button onClick={startUpload} disabled={!file} className="max-md:min-h-11" data-testid="upload-submit">
            File it
          </Button>
        </ResponsiveModalActions>
      )}
      {step === "saved" && created && (
        <ResponsiveModalActions>
          <Button variant="default" onClick={() => setStep("choose")} className="max-md:min-h-11">
            Scan another
          </Button>
          <Button onClick={onClose} className="max-md:min-h-11" data-testid="upload-open">
            Done
          </Button>
        </ResponsiveModalActions>
      )}
    </ResponsiveModal>
  );
}

// ─── Choose ──────────────────────────────────────────────────

function ChooseStep({
  dragging,
  onCamera,
  onPick,
  onDragOver,
  onDragLeave,
  onDrop,
}: {
  dragging: boolean;
  onCamera: () => void;
  onPick: () => void;
  onDragOver: (e: React.DragEvent) => void;
  onDragLeave: () => void;
  onDrop: (e: React.DragEvent) => void;
}) {
  return (
    <div data-testid="upload-choose">
      <div
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
        style={{
          borderRadius: "var(--r-md)",
          border: `1.5px dashed ${dragging ? "var(--accent)" : "var(--line)"}`,
          background: dragging ? "var(--accent-soft)" : "var(--surface-2)",
          padding: "20px 16px",
          textAlign: "center",
        }}
      >
        <Text fw={600}>Take a photo, or pick a file</Text>
        <Text size="sm" c="dimmed" mt={4} style={{ lineHeight: 1.45 }}>
          A receipt, a school form, an insurance letter. Home OS reads the text so you can find it later.
        </Text>
        <Stack gap={8} mt="md">
          <Button
            onClick={onCamera}
            size="md"
            leftSection={<IconCamera size={20} stroke={ICON_STROKE} />}
            fullWidth
            className="max-md:min-h-11"
            data-testid="upload-camera"
          >
            Take a photo
          </Button>
          <Button
            onClick={onPick}
            size="md"
            variant="default"
            leftSection={<IconUpload size={20} stroke={ICON_STROKE} />}
            fullWidth
            className="max-md:min-h-11"
            data-testid="upload-pick"
          >
            Choose a file
          </Button>
        </Stack>
        <Text size="xs" c="dimmed" mt={10}>
          Photos and PDFs up to 25 MB. On a computer you can also drop a file here.
        </Text>
      </div>
    </div>
  );
}

// ─── Details ─────────────────────────────────────────────────

function DetailsStep(props: {
  file: File;
  previewUrl: string | null;
  onPreviewBroken: () => void;
  title: string;
  onTitle: (v: string) => void;
  category: Category;
  onCategory: (v: Category) => void;
  documentDate: string;
  onDocumentDate: (v: string) => void;
  expiresOn: string;
  onExpiresOn: (v: string) => void;
  dateError: string | null;
  personId: string | null;
  onPersonId: (v: string | null) => void;
  people: Array<{ id: string; display_name: string }>;
  description: string;
  onDescription: (v: string) => void;
  tags: string[];
  onTags: (v: string[]) => void;
  onReplace: () => void;
}) {
  const {
    file,
    previewUrl,
    onPreviewBroken,
    title,
    onTitle,
    category,
    onCategory,
    documentDate,
    onDocumentDate,
    expiresOn,
    onExpiresOn,
    dateError,
    personId,
    onPersonId,
    people,
    description,
    onDescription,
    tags,
    onTags,
    onReplace,
  } = props;
  return (
    <Stack gap="md" data-testid="upload-details">
      <Group gap={12} wrap="nowrap" align="flex-start">
        <div
          style={{
            width: 56,
            height: 56,
            flex: "none",
            borderRadius: "var(--r-md)",
            background: previewUrl ? "transparent" : "var(--surface-2)",
            border: "1px solid var(--line)",
            overflow: "hidden",
            display: "grid",
            placeItems: "center",
          }}
        >
          {previewUrl ? (
            // eslint-disable-next-line @next/next/no-img-element
            <img
              src={previewUrl}
              alt=""
              onError={onPreviewBroken}
              style={{ width: "100%", height: "100%", objectFit: "cover" }}
            />
          ) : (
            <IconFile size={24} stroke={ICON_STROKE} aria-hidden />
          )}
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <Text fw={600} size="sm" style={{ overflowWrap: "anywhere" }}>
            {file.name}
          </Text>
          <Text size="xs" c="dimmed">
            {formatSize(file.size)} · the name is a starting point, not something the document said
          </Text>
        </div>
        <Button size="xs" variant="subtle" onClick={onReplace} leftSection={<IconX size={14} stroke={ICON_STROKE} />}>
          Change
        </Button>
      </Group>

      <TextInput
        label="Title"
        description="Leave it as is, or say what it really is"
        value={title}
        onChange={(e) => onTitle(e.currentTarget.value)}
        data-testid="upload-title"
      />

      <div>
        <Text size="sm" fw={500} mb={6}>
          Category
        </Text>
        <div role="group" aria-label="Category" style={{ display: "grid", gridTemplateColumns: "repeat(3, minmax(0, 1fr))", gap: 8 }}>
          {CATEGORIES.map((c) => {
            const on = c === category;
            return (
              <button
                key={c}
                type="button"
                aria-pressed={on}
                onClick={() => onCategory(c)}
                data-category={c}
                style={{
                  display: "flex",
                  flexDirection: "column",
                  alignItems: "center",
                  gap: 4,
                  minHeight: 60,
                  padding: "8px 4px",
                  borderRadius: "var(--r-md)",
                  border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`,
                  background: on ? "var(--accent-soft)" : "var(--surface)",
                  color: on ? "var(--accent)" : "var(--ink-2)",
                  fontWeight: on ? 600 : 500,
                  fontSize: 12,
                  cursor: "pointer",
                  boxShadow: on ? "inset 0 0 0 1px var(--accent)" : undefined,
                }}
              >
                <CategoryIcon category={c} size={18} />
                {categoryLabel(c)}
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
          onChange={(e) => onDocumentDate(e.currentTarget.value)}
          error={dateError ?? undefined}
          data-testid="upload-document-date"
        />
        <TextInput
          type="date"
          label="Expires"
          description="Shows under Expiring soon"
          value={expiresOn}
          onChange={(e) => onExpiresOn(e.currentTarget.value)}
          data-testid="upload-expires"
        />
      </Group>

      <Select
        label="For"
        placeholder="Nobody in particular"
        clearable
        searchable
        data={people.map((p) => ({ value: p.id, label: p.display_name }))}
        value={personId}
        onChange={onPersonId}
        data-testid="upload-person"
        nothingFoundMessage={people.length === 0 ? "No people in this family yet" : "No match"}
      />

      <Textarea
        label="Notes"
        description="Anything to remember when it comes up again"
        autosize
        minRows={2}
        value={description}
        onChange={(e) => onDescription(e.currentTarget.value)}
        data-testid="upload-description"
      />

      <TagsInput
        label="Tags"
        description="Optional — a word you'd search for, like an insurer or a room"
        value={tags}
        onChange={onTags}
        data-testid="upload-tags"
      />

      <Text size="xs" c="dimmed">
        Home OS reads the text after you file it, so it can be found by meaning. It takes a few seconds and you can
        close this once it&apos;s filed.
      </Text>
    </Stack>
  );
}

// ─── Reading ─────────────────────────────────────────────────

function ReadingStep({ file, onDone }: { file: FileRecord; onDone: () => void }) {
  const [current, setCurrent] = useState<FileRecord>(file);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let tries = 0;

    const poll = async () => {
      tries += 1;
      try {
        const res = await apiFetch<{ data: FileRecord }>(`/api/v1/files/${file.id}`);
        if (cancelled) return;
        setCurrent(res.data);
        if (TERMINAL.has(res.data.ocr_status)) {
          onDone();
          return;
        }
      } catch {
        // A failed poll is not a failed upload: the document is filed. Keep
        // trying for the bounded window, then move on and let the detail page
        // report the reading state.
      }
      // Bounded: the mockup's 30 s client window. Past it the document is
      // filed and the sheet moves on rather than spinning forever.
      if (tries * POLL_MS >= 30_000) {
        if (!cancelled) onDone();
        return;
      }
      timer = setTimeout(poll, POLL_MS);
    };
    timer = setTimeout(poll, POLL_MS);
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [file.id, onDone]);

  const state = textState(current);
  return (
    <div data-testid="upload-reading" style={{ padding: "18px 0 8px", textAlign: "center" }}>
      <div
        role="status"
        aria-label="Reading the document"
        style={{
          width: 40,
          height: 40,
          margin: "0 auto 14px",
          borderRadius: "50%",
          border: "4px solid var(--accent-soft)",
          borderTopColor: "var(--accent)",
          animation: "docs-spin 1s linear infinite",
        }}
      />
      <style>{`@keyframes docs-spin { to { transform: rotate(360deg); } } @media (prefers-reduced-motion: reduce) { [role="status"] { animation: none !important; } }`}</style>
      <Text fw={600}>Reading it now…</Text>
      <Text size="sm" c="dimmed" mt={4}>
        {state.note}
      </Text>
      <Text size="xs" c="dimmed" mt={10}>
        It&apos;s already filed — the text is what makes it findable later.
      </Text>
    </div>
  );
}

// ─── Saved ───────────────────────────────────────────────────

function SavedStep({ file }: { file: FileRecord }) {
  const category = categoryOf(file);
  return (
    <Stack gap="md" data-testid="upload-saved">
      <div style={{ textAlign: "center", padding: "12px 0 4px" }}>
        <div
          aria-hidden
          style={{
            width: 64,
            height: 64,
            margin: "0 auto 12px",
            borderRadius: "50%",
            display: "grid",
            placeItems: "center",
            background: "var(--ok-bg, #ebfbee)",
            color: "var(--ok-ink, #2b8a3e)",
          }}
        >
          <IconCheck size={32} stroke={2} />
        </div>
        <Text fw={600} size="lg">
          {isFiled(file) ? `Filed under ${CATEGORY_LABELS[category]}` : "Filed, with no category yet"}
        </Text>
        <Text size="sm" c="dimmed" mt={6} style={{ maxWidth: 340, margin: "6px auto 0" }}>
          {file.title || file.name} is searchable now. You&apos;ll find it under Documents and, if it has an expiry
          date, under Expiring soon.
        </Text>
      </div>
      <div style={{ display: "flex", alignItems: "center", gap: 12, padding: "10px 12px", borderRadius: "var(--r-md)", border: "1px solid var(--line)" }}>
        <span style={{ display: "grid", placeItems: "center", width: 36, height: 36, borderRadius: 10, background: "var(--accent-soft)", color: "var(--accent)", flex: "none" }}>
          <CategoryIcon category={category} size={18} />
        </span>
        <div style={{ minWidth: 0 }}>
          <Text fw={600} size="sm" style={{ overflowWrap: "anywhere" }}>
            {file.title || file.name}
          </Text>
          <Text size="xs" c="dimmed">
            {documentCategoryLabel(file)}
            {file.expires_on ? ` · expires ${file.expires_on}` : ""}
          </Text>
        </div>
      </div>
      {/* No buttons here: the sheet's pinned action row owns them, so the same
          two controls never appear twice. */}
    </Stack>
  );
}

/** The file icon used in the choose step's empty preview (kept for tests). */
export const UploadFileIcon = IconFile;
export const UploadPhotoIcon = IconPhoto;
