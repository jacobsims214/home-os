"use client";

/**
 * FileList — the files attached to one entity, mobile-first (#4564).
 *
 * Each row says what a person needs to judge a document: its name, size, who
 * uploaded it and when, and — the part #4941 made necessary — what its TEXT
 * is. A file whose text was re-typed after filing must not read as the
 * original document, so every row carries a FileTextMarker and the file view
 * repeats the sentence in full.
 *
 * Phone (390 px): the row is a two-line block, not a table; the name wraps
 * rather than truncating to "Duke Ene…"; every control is 44 px; the metadata
 * line wraps instead of scrolling sideways. A long list is cut at
 * COLLAPSED_ROWS behind "Show all".
 *
 * Role policy (#4689): `access` comes from resourceAccess(). "read" (a viewer)
 * shows the list and the download but no upload or delete; "none" renders
 * nothing. The API enforces; this only avoids drawing dead controls.
 */

import { useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError, bffPath } from "@/lib/api";
import type { FileRecord, FileOCRStatus } from "@/lib/types/api";
import { fileKeys } from "@/lib/query-keys";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import FileViewer, { fileContentUrl } from "@/components/files/FileViewer";
import FileTextMarker from "@/components/files/FileTextMarker";
import { formatBytes, formatDate, uploaderLine } from "@/components/files/file-display";
import type { Access } from "@/lib/resource-access";

// ─── Constants ────────────────────────────────────────────────

/** Rows shown before "Show all". */
export const COLLAPSED_ROWS = 6;

const TOUCH = "max-md:min-h-11";

// ─── Types ────────────────────────────────────────────────────

interface FilesResponse {
  data: FileRecord[];
}

// ─── Content-type icon ─────────────────────────────────────────

function ContentTypeIcon({ contentType }: { contentType: string }) {
  const ct = (contentType || "").toLowerCase();
  let path: React.ReactNode;

  if (ct.startsWith("image/")) {
    path = (
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="m2.25 15.75 5.159-5.159a2.25 2.25 0 0 1 3.182 0l5.159 5.159m-1.5-1.5 1.409-1.409a2.25 2.25 0 0 1 3.182 0l2.909 2.909m-18 3.75h16.5a1.5 1.5 0 0 0 1.5-1.5V6a1.5 1.5 0 0 0-1.5-1.5H3.75A1.5 1.5 0 0 0 2.25 6v12a1.5 1.5 0 0 0 1.5 1.5Zm10.5-11.25h.008v.008h-.008V8.25Zm.375 0a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Z"
      />
    );
  } else if (ct.startsWith("video/")) {
    path = (
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="m15.75 10.5 4.72-4.72a.75.75 0 0 1 1.28.53v11.38a.75.75 0 0 1-1.28.53l-4.72-4.72M4.5 18.75h9a2.25 2.25 0 0 0 2.25-2.25v-9a2.25 2.25 0 0 0-2.25-2.25h-9A2.25 2.25 0 0 0 2.25 7.5v9a2.25 2.25 0 0 0 2.25 2.25Z"
      />
    );
  } else if (ct.startsWith("audio/")) {
    path = (
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M19.114 5.636a9 9 0 0 1 0 12.728M16.463 8.288a5.25 5.25 0 0 1 0 7.424M6.75 8.25l4.72-4.72a.75.75 0 0 1 1.28.53v15.88a.75.75 0 0 1-1.28.53l-4.72-4.72H4.51c-.88 0-1.704-.507-1.938-1.354A9.01 9.01 0 0 1 2.25 12c0-.83.112-1.633.322-2.396C2.806 8.756 3.63 8.25 4.51 8.25H6.75Z"
      />
    );
  } else {
    path = (
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m0 12.75h7.5m-7.5 3H12M10.5 2.25H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"
      />
    );
  }

  return (
    <svg className="h-5 w-5 shrink-0 text-ink-3" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor" aria-hidden>
      {path}
    </svg>
  );
}

// ─── OCR status badge ─────────────────────────────────────────

const ocrStatusConfig: Record<FileOCRStatus, { label: string; className: string }> = {
  pending: { label: "Reading…", className: "border-line bg-surface-2 text-ink-3" },
  processing: { label: "Reading…", className: "border-line bg-surface-2 text-ink-3" },
  done: { label: "Text ready", className: "border-line bg-surface-2 text-ink-3" },
  failed: { label: "No text", className: "border-warn-line bg-warn-bg text-warn-ink" },
  skipped: { label: "No text", className: "border-line bg-surface-2 text-ink-3" },
};

function OCRStatusBadge({ status }: { status: FileOCRStatus }) {
  const config = ocrStatusConfig[status] ?? {
    label: status || "Unknown",
    className: "border-line bg-surface-2 text-ink-3",
  };
  return (
    <span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[11px] font-medium leading-5 ${config.className}`}>
      {config.label}
    </span>
  );
}

// ─── Component ────────────────────────────────────────────────

interface FileListProps {
  entityType: string;
  entityId: string;
  /** "write" shows delete; "read" is a read-only list; "none" renders nothing. */
  access?: Access;
}

export default function FileList({ entityType, entityId, access = "write" }: FileListProps) {
  const queryClient = useQueryClient();
  const [deleteTarget, setDeleteTarget] = useState<FileRecord | null>(null);
  const [viewTarget, setViewTarget] = useState<FileRecord | null>(null);
  const [showAll, setShowAll] = useState(false);

  const canWrite = access === "write";
  const queryKey = fileKeys.listByEntity(entityType, entityId);

  const filesQ = useQuery({
    queryKey,
    queryFn: () =>
      apiFetch<FilesResponse>("/api/v1/files", {
        params: { entity_type: entityType, entity_id: entityId },
      }),
    enabled: access !== "none" && !!entityType && !!entityId,
  });

  const files = useMemo(() => filesQ.data?.data ?? [], [filesQ.data]);
  const shown = showAll ? files : files.slice(0, COLLAPSED_ROWS);

  const deleteMutation = useMutation({
    mutationFn: (id: string) => apiFetch(`/api/v1/files/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey });
      setDeleteTarget(null);
    },
    onError: (error) => {
      console.error("Failed to delete file:", error);
    },
  });

  const handleDownload = async (file: FileRecord) => {
    const resp = await fetch(bffPath(`/api/v1/files/${file.id}/content`), {
      credentials: "same-origin",
    });
    if (!resp.ok) throw new Error("Download failed");
    const blob = await resp.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = file.name || "download";
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  };

  if (access === "none") return null;

  const deleteErrorMessage =
    deleteMutation.error instanceof ApiError
      ? deleteMutation.error.message
      : deleteMutation.error instanceof Error
        ? deleteMutation.error.message
        : undefined;

  return (
    <>
      <div className="space-y-2" data-testid="file-list">
        {filesQ.isLoading && (
          <div className="space-y-2">
            {[1, 2].map((i) => (
              <div key={i} className="h-16 animate-pulse rounded-token-md bg-surface-2" />
            ))}
          </div>
        )}

        {filesQ.isError && (
          <div className="rounded-token-md border border-danger bg-surface px-4 py-3 text-sm">
            <p className="font-medium text-danger">Could not load files.</p>
            <p className="mt-0.5 text-ink-2">
              {filesQ.error instanceof Error ? filesQ.error.message : "Unknown error"}
            </p>
            <button
              type="button"
              onClick={() => filesQ.refetch()}
              className={`mt-2 inline-flex items-center rounded-token-sm border border-line bg-surface px-3 py-1.5 text-xs font-semibold text-ink hover:bg-surface-2 ${TOUCH}`}
            >
              Try again
            </button>
          </div>
        )}

        {!filesQ.isLoading && !filesQ.isError && files.length === 0 && (
          <p className="text-sm text-ink-3">
            {canWrite ? "No files yet. Add a photo of a receipt, a form or a manual." : "No files yet."}
          </p>
        )}

        {shown.map((file) => (
          <div
            key={file.id}
            data-testid="file-row"
            className="rounded-token-md border border-line bg-surface px-3 py-3"
          >
            <div className="flex items-start gap-3">
              <ContentTypeIcon contentType={file.content_type} />
              <div className="min-w-0 flex-1">
                <button
                  type="button"
                  onClick={() => setViewTarget(file)}
                  className="block w-full text-left"
                >
                  <p className="break-words text-sm font-medium text-ink" data-file-name>
                    {file.name || "Untitled file"}
                  </p>
                </button>
                {/* Metadata wraps rather than scrolling sideways at 390 px. */}
                <p className="mt-0.5 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-ink-3">
                  <span>{formatBytes(file.size)}</span>
                  <span aria-hidden>·</span>
                  <span>{formatDate(file.created_at)}</span>
                  {uploaderLine(file) && (
                    <>
                      <span aria-hidden>·</span>
                      <span data-file-uploader>{uploaderLine(file)}</span>
                    </>
                  )}
                </p>
                <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                  <FileTextMarker file={file} />
                  <OCRStatusBadge status={file.ocr_status} />
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-1">
                <a
                  href={fileContentUrl(file.id)}
                  onClick={(e) => {
                    e.stopPropagation();
                    e.preventDefault();
                    void handleDownload(file);
                  }}
                  className="inline-flex h-11 w-11 items-center justify-center rounded-token-sm text-ink-3 hover:bg-surface-2 hover:text-ink"
                  aria-label={`Download ${file.name || "file"}`}
                  title={`Download ${file.name || "file"}`}
                >
                  <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor" aria-hidden>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M3 16.5v2.25A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75V16.5M16.5 12 12 16.5m0 0L7.5 12m4.5 4.5V3" />
                  </svg>
                </a>
                {canWrite && (
                  <button
                    type="button"
                    onClick={() => setDeleteTarget(file)}
                    disabled={deleteMutation.isPending}
                    aria-label={`Delete ${file.name || "file"}`}
                    title={`Delete ${file.name || "file"}`}
                    // A disabled button must not light up: without the
                    // disabled:hover overrides it still showed the danger
                    // hover while the mutation was in flight.
                    className="inline-flex h-11 w-11 items-center justify-center rounded-token-sm text-ink-3 hover:bg-surface-2 hover:text-danger disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-ink-3"
                  >
                    <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor" aria-hidden>
                      <path strokeLinecap="round" strokeLinejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
                    </svg>
                  </button>
                )}
              </div>
            </div>
          </div>
        ))}
      </div>

      {files.length > COLLAPSED_ROWS && (
        <button
          type="button"
          onClick={() => setShowAll((v) => !v)}
          className={`mt-2 inline-flex w-full items-center justify-center rounded-token-sm border border-line bg-surface px-3 py-2 text-sm font-medium text-ink-2 hover:bg-surface-2 ${TOUCH}`}
        >
          {showAll ? "Show fewer" : `Show all ${files.length} files`}
        </button>
      )}

      <FileViewer file={viewTarget} open={viewTarget !== null} onClose={() => setViewTarget(null)} />

      <ConfirmDialog
        open={deleteTarget !== null}
        onClose={() => {
          if (!deleteMutation.isPending) setDeleteTarget(null);
        }}
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
        title="Delete file"
        message={
          deleteTarget
            ? `Are you sure you want to delete "${deleteTarget.name || "this file"}"? This action cannot be undone.`
            : ""
        }
        loading={deleteMutation.isPending}
      />

      {deleteErrorMessage && (
        <p role="alert" className="mt-2 text-xs text-danger">
          {deleteErrorMessage}
        </p>
      )}
    </>
  );
}
