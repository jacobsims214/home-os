"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import type { FileRecord, FileOCRStatus } from "@/lib/types/api";
import { fileKeys } from "@/lib/query-keys";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import FileViewer, { fileContentUrl } from "@/components/files/FileViewer";

// ─── Types ────────────────────────────────────────────────────

/**
 * Envelope returned by GET /api/v1/files. The canonical `FileRecord` is
 * imported from @/lib/types/api (mirrors apps/api/internal/file/model.go
 * `File`); only the list envelope is defined here.
 */
interface FilesResponse {
  data: FileRecord[];
}

// ─── Helpers ──────────────────────────────────────────────────

/** Human-readable byte size: 1.2 KB / 3.4 MB / 512 bytes. */
function formatBytes(bytes: number): string {
  if (!bytes || bytes < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.min(
    units.length - 1,
    Math.floor(Math.log(bytes) / Math.log(1024)),
  );
  const value = bytes / Math.pow(1024, i);
  // Bytes get no decimals; larger units get one decimal place.
  const formatted = i === 0 ? value.toString() : value.toFixed(1);
  return `${formatted} ${units[i]}`;
}

/** Short locale-formatted date (e.g. "Jun 5, 2026"). Falls back to the raw ISO. */
function formatDate(iso: string): string {
  try {
    return new Date(iso).toLocaleDateString("en-US", {
      month: "short",
      day: "numeric",
      year: "numeric",
    });
  } catch {
    return iso;
  }
}

// ─── Content-type icon ─────────────────────────────────────────

/**
 * Inline SVG icon chosen by content-type prefix. Kept inline (no icon library)
 * to match the rest of the dashboard. The icon is purely decorative — the
 * content type label is also rendered as text for accessibility.
 */
function ContentTypeIcon({ contentType }: { contentType: string }) {
  const ct = contentType.toLowerCase();
  let path: React.ReactNode;

  if (ct.startsWith("image/")) {
    // Image
    path = (
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="m2.25 15.75 5.159-5.159a2.25 2.25 0 0 1 3.182 0l5.159 5.159m-1.5-1.5 1.409-1.409a2.25 2.25 0 0 1 3.182 0l2.909 2.909m-18 3.75h16.5a1.5 1.5 0 0 0 1.5-1.5V6a1.5 1.5 0 0 0-1.5-1.5H3.75A1.5 1.5 0 0 0 2.25 6v12a1.5 1.5 0 0 0 1.5 1.5Zm10.5-11.25h.008v.008h-.008V8.25Zm.375 0a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Z"
      />
    );
  } else if (ct.startsWith("video/")) {
    // Video
    path = (
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="m15.75 10.5 4.72-4.72a.75.75 0 0 1 1.28.53v11.38a.75.75 0 0 1-1.28.53l-4.72-4.72M4.5 18.75h9a2.25 2.25 0 0 0 2.25-2.25v-9a2.25 2.25 0 0 0-2.25-2.25h-9A2.25 2.25 0 0 0 2.25 7.5v9a2.25 2.25 0 0 0 2.25 2.25Z"
      />
    );
  } else if (ct.startsWith("audio/")) {
    // Audio
    path = (
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M19.114 5.636a9 9 0 0 1 0 12.728M16.463 8.288a5.25 5.25 0 0 1 0 7.424M6.75 8.25l4.72-4.72a.75.75 0 0 1 1.28.53v15.88a.75.75 0 0 1-1.28.53l-4.72-4.72H4.51c-.88 0-1.704-.507-1.938-1.354A9.01 9.01 0 0 1 2.25 12c0-.83.112-1.633.322-2.396C2.806 8.756 3.63 8.25 4.51 8.25H6.75Z"
      />
    );
  } else if (ct === "application/pdf") {
    // PDF
    path = (
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m2.25 0H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"
      />
    );
  } else if (
    ct.startsWith("text/") ||
    ct === "application/json" ||
    ct === "application/xml"
  ) {
    // Text / document
    path = (
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m0 12.75h7.5m-7.5 3H12M10.5 2.25H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"
      />
    );
  } else {
    // Generic file
    path = (
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m0 12.75h7.5m-7.5 3H12M10.5 2.25H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"
      />
    );
  }

  return (
    <svg
      className="h-5 w-5 shrink-0 text-gray-400"
      fill="none"
      viewBox="0 0 24 24"
      strokeWidth={1.5}
      stroke="currentColor"
      aria-hidden="true"
    >
      {path}
    </svg>
  );
}

// ─── OCR status badge ─────────────────────────────────────────

/**
 * OCR status badge. The shared Badge component
 * (apps/ui/components/ui/Badge.tsx) supports pending/in_progress/done/skipped
 * but the file OCR lifecycle uses pending/processing/done/failed/skipped
 * (see OCRStatus* constants in apps/api/internal/file/model.go). Rendering a
 * local badge here avoids coupling two unrelated status enums and keeps this
 * component self-contained.
 */
const ocrStatusConfig: Record<
  FileOCRStatus,
  { label: string; className: string }
> = {
  pending: {
    label: "OCR Pending",
    className: "bg-yellow-100 text-yellow-800 border-yellow-300",
  },
  processing: {
    label: "OCR Processing",
    className: "bg-blue-100 text-blue-800 border-blue-300",
  },
  done: {
    label: "OCR Done",
    className: "bg-green-100 text-green-800 border-green-300",
  },
  failed: {
    label: "OCR Failed",
    className: "bg-red-100 text-red-800 border-red-300",
  },
  skipped: {
    label: "OCR Skipped",
    className: "bg-gray-100 text-gray-500 border-gray-300",
  },
};

function OCRStatusBadge({ status }: { status: FileOCRStatus }) {
  const config = ocrStatusConfig[status] ?? {
    label: status || "Unknown",
    className: "bg-gray-100 text-gray-500 border-gray-300",
  };
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium ${config.className}`}
    >
      {config.label}
    </span>
  );
}

// ─── Trash icon button ────────────────────────────────────────

function TrashIconButton({
  onClick,
  disabled,
  label,
}: {
  onClick: (e: React.MouseEvent) => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      title={label}
      className="inline-flex items-center justify-center rounded-md p-1.5 text-gray-400 hover:bg-red-50 hover:text-red-600 disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-gray-400 transition-colors"
    >
      <svg
        className="h-4 w-4"
        fill="none"
        viewBox="0 0 24 24"
        strokeWidth={1.5}
        stroke="currentColor"
        aria-hidden="true"
      >
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0"
        />
      </svg>
    </button>
  );
}

// ─── Component ────────────────────────────────────────────────

interface FileListProps {
  entityType: string;
  entityId: string;
}

/**
 * FileList renders the files attached to a given household entity
 * (entity_type + entity_id). It fetches via GET /api/v1/files?entity_type=...
 * &entity_id=... and renders each file's name, content-type icon, byte size,
 * upload date, and OCR status badge. Each row has a trash button that opens a
 * ConfirmDialog; confirming calls DELETE /api/v1/files/{id} and invalidates
 * the entity-scoped list query.
 *
 * The component mirrors the polymorphic-entity-attachment pattern established
 * by NotesSection (apps/ui/components/notes/NotesSection.tsx) and the
 * TanStack Query list/delete pattern described in
 * architecture/list-page-pattern.md.
 */
export default function FileList({ entityType, entityId }: FileListProps) {
  const queryClient = useQueryClient();
  const [deleteTarget, setDeleteTarget] = useState<FileRecord | null>(null);
  const [viewTarget, setViewTarget] = useState<FileRecord | null>(null);

  // Download a file by fetching with Bearer auth and triggering a blob download.
  const handleDownload = async (file: FileRecord) => {
    const token = useAuthStore.getState().token;
    const resp = await fetch(
      `${process.env.NEXT_PUBLIC_API_URL ?? ""}/api/v1/files/${file.id}/content`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
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

  const queryKey = fileKeys.listByEntity(entityType, entityId);

  const filesQ = useQuery({
    queryKey,
    queryFn: () =>
      apiFetch<FilesResponse>("/api/v1/files", {
        params: { entity_type: entityType, entity_id: entityId },
      }),
    enabled: !!entityType && !!entityId,
  });

  const files = filesQ.data?.data ?? [];

  const deleteMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/api/v1/files/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey });
      setDeleteTarget(null);
    },
    // Keep the dialog open on error so the user can retry or cancel.
    // The mutation error is surfaced via the confirm button's loading state.
    onError: (error) => {
      console.error("Failed to delete file:", error);
    },
  });

  const confirmDelete = () => {
    if (deleteTarget) {
      deleteMutation.mutate(deleteTarget.id);
    }
  };

  const deleteErrorMessage =
    deleteMutation.error instanceof ApiError
      ? deleteMutation.error.message
      : deleteMutation.error instanceof Error
        ? deleteMutation.error.message
        : undefined;

  return (
    <>
      <div className="space-y-2">
        {/* Loading */}
        {filesQ.isLoading && (
          <div className="space-y-2">
            {[1, 2].map((i) => (
              <div
                key={i}
                className="h-14 animate-pulse rounded-lg bg-gray-100"
              />
            ))}
          </div>
        )}

        {/* Error */}
        {filesQ.isError && (
          <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
            <p className="font-medium">Failed to load files.</p>
            <p className="mt-0.5 text-red-600">
              {filesQ.error instanceof Error
                ? filesQ.error.message
                : "Unknown error"}
            </p>
            <button
              type="button"
              onClick={() => filesQ.refetch()}
              className="mt-2 inline-flex items-center rounded-md border border-red-300 bg-white px-3 py-1.5 text-xs font-semibold text-red-700 hover:bg-red-50 transition-colors"
            >
              Retry
            </button>
          </div>
        )}

        {/* Empty */}
        {!filesQ.isLoading && !filesQ.isError && files.length === 0 && (
          <p className="text-sm text-gray-400 italic">No files attached.</p>
        )}

        {/* Populated */}
        {files.map((file) => (
          <div
            key={file.id}
            className="flex items-center gap-3 rounded-lg border border-gray-200 bg-white px-4 py-3 hover:border-gray-300 hover:bg-gray-50 transition-colors cursor-pointer"
            onClick={() => setViewTarget(file)}
          >
            <ContentTypeIcon contentType={file.content_type} />

            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium text-gray-900">
                {file.name || "Untitled file"}
              </p>
              <p className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-gray-500">
                <span>{formatBytes(file.size)}</span>
                <span aria-hidden="true">·</span>
                <span className="truncate">
                  {file.content_type || "application/octet-stream"}
                </span>
                <span aria-hidden="true">·</span>
                <span>{formatDate(file.created_at)}</span>
              </p>
            </div>

            <div className="flex shrink-0 items-center gap-2">
              <OCRStatusBadge status={file.ocr_status} />
              <a
                href={fileContentUrl(file.id)}
                onClick={(e) => {
                  e.stopPropagation();
                  e.preventDefault();
                  handleDownload(file);
                }}
                className="inline-flex items-center justify-center rounded-md p-1.5 text-gray-400 hover:bg-blue-50 hover:text-blue-600 transition-colors"
                aria-label={`Download ${file.name || "file"}`}
                title={`Download ${file.name || "file"}`}
              >
                <svg
                  className="h-4 w-4"
                  fill="none"
                  viewBox="0 0 24 24"
                  strokeWidth={1.5}
                  stroke="currentColor"
                  aria-hidden="true"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    d="M3 16.5v2.25A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75V16.5M16.5 12 12 16.5m0 0L7.5 12m4.5 4.5V3"
                  />
                </svg>
              </a>
              <TrashIconButton
                onClick={(e) => {
                  e.stopPropagation();
                  setDeleteTarget(file);
                }}
                disabled={deleteMutation.isPending}
                label={`Delete ${file.name || "file"}`}
              />
            </div>
          </div>
        ))}
      </div>

      {/* File viewer modal */}
      <FileViewer
        file={viewTarget}
        open={viewTarget !== null}
        onClose={() => setViewTarget(null)}
      />

      {/* Delete confirmation */}
      <ConfirmDialog
        open={deleteTarget !== null}
        onClose={() => {
          if (!deleteMutation.isPending) setDeleteTarget(null);
        }}
        onConfirm={confirmDelete}
        title="Delete file"
        message={
          deleteTarget
            ? `Are you sure you want to delete "${deleteTarget.name || "this file"}"? This action cannot be undone.`
            : ""
        }
        loading={deleteMutation.isPending}
      />

      {/* Delete error — rendered below the dialog so the user sees the
          failure even though the dialog stays open. */}
      {deleteErrorMessage && (
        <p
          role="alert"
          className="mt-2 text-xs text-red-600"
        >
          {deleteErrorMessage}
        </p>
      )}
    </>
  );
}
