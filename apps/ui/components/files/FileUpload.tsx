"use client";

/**
 * FileUpload — drag-drop / file-picker / mobile-camera upload widget.
 *
 * Appears on entity detail pages (property, vehicle, asset, ...). Uploads
 * files to POST /api/v1/files/upload as multipart/form-data together with
 * the entity_type + entity_id association, shows per-file upload progress,
 * then polls the file's OCR status until it reaches a terminal state.
 *
 * Why XMLHttpRequest instead of fetch: the fetch API has no upload-progress
 * event. XHR's `upload.onprogress` is the only standard way to surface byte
 * progress to the user during a multipart upload. The auth bearer token is
 * read directly from the auth store (same source as `apiFetch`) so this
 * stays consistent with the rest of the app's auth flow.
 *
 * Why a polling useQuery per uploaded file: the API returns the created File
 * record synchronously from POST /upload with `ocr_status: "pending"`. The
 * OCR worker transitions that status asynchronously. We poll
 * GET /api/v1/files/{id} with `refetchInterval` until the status is
 * terminal (done / failed / skipped), then stop. This mirrors how the rest
 * of the UI uses TanStack Query for server-state lifecycle.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@/stores/auth";
import { apiFetch, ApiError } from "@/lib/api";
import { fileKeys } from "@/lib/query-keys";
import type { FileRecord, FileOCRStatus } from "@/lib/types/api";

// ─── Constants ────────────────────────────────────────────────

const BASE_URL = process.env.NEXT_PUBLIC_API_URL ?? "";
const UPLOAD_PATH = "/api/v1/files/upload";
/** OCR statuses that the worker will not transition away from. */
const TERMINAL_OCR_STATUSES: FileOCRStatus[] = ["done", "failed", "skipped"];
/** Poll interval for OCR status. Modest — OCR is a background pipeline. */
const OCR_POLL_MS = 3000;

// ─── Types ────────────────────────────────────────────────────

interface FileUploadProps {
  entityType: string;
  entityId: string;
}

/**
 * Local upload slot — tracks one file from selection through upload
 * completion and OCR resolution. `fileId` is set once the upload returns
 * the created File record; before that the slot is identified by its
 * local `id` (a counter) so React keys stay stable across re-renders.
 */
interface UploadSlot {
  localId: number;
  file: File;
  fileName: string;
  /** 0–100 during upload; 100 once the XHR completes (even on error). */
  progress: number;
  /** Set when the upload XHR fails (network error or non-2xx response). */
  uploadError: string | null;
  /** Set once the server returns the created File record. */
  fileId: string | null;
  /** True while the slot is actively uploading. */
  uploading: boolean;
}

// ─── Upload helper (XHR for progress) ─────────────────────────

/**
 * Upload a single file to POST /api/v1/files/upload as multipart/form-data.
 * Resolves with the created File record on 2xx, rejects with an ApiError
 * on non-2xx or a network failure. `onProgress` receives 0–100.
 *
 * The token is read at call time (not capture time) so a freshly refreshed
 * token from the auth store is always used.
 */
function uploadFile(
  file: File,
  entityType: string,
  entityId: string,
  onProgress: (pct: number) => void,
  signal?: AbortSignal,
): Promise<FileRecord> {
  return new Promise((resolve, reject) => {
    const token = useAuthStore.getState().token;
    const form = new FormData();
    form.append("file", file, file.name);
    form.append("entity_type", entityType);
    form.append("entity_id", entityId);

    const xhr = new XMLHttpRequest();
    const url = `${BASE_URL}${UPLOAD_PATH}`;

    xhr.open("POST", url, true);
    if (token) {
      xhr.setRequestHeader("Authorization", `Bearer ${token}`);
    }
    // Do NOT set Content-Type — the browser sets multipart/form-data with
    // the correct boundary when given a FormData body. Setting it manually
    // would strip the boundary and break parsing on the server.

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) {
        onProgress(Math.round((event.loaded / event.total) * 100));
      }
    };

    // Set up abort-signal wiring BEFORE the XHR handlers so each handler
    // can remove the listener when it settles. The AbortSignal is owned by
    // the caller and may outlive this upload (e.g. reused across retries),
    // so we must remove the listener when the XHR settles (load / error /
    // abort) to avoid leaking the closure and the XHR it captures.
    const onAbort = () => xhr.abort();
    let removeAbortListener: () => void = () => {};
    if (signal) {
      if (signal.aborted) {
        xhr.abort();
        return;
      }
      signal.addEventListener("abort", onAbort);
      removeAbortListener = () =>
        signal!.removeEventListener("abort", onAbort);
    }

    xhr.onload = () => {
      removeAbortListener();
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          resolve(JSON.parse(xhr.responseText) as FileRecord);
        } catch {
          reject(
            new ApiError(
              xhr.status,
              "upload returned non-JSON response",
              xhr.responseText,
            ),
          );
        }
      } else {
        let body: unknown;
        try {
          body = JSON.parse(xhr.responseText);
        } catch {
          body = xhr.responseText;
        }
        reject(
          new ApiError(
            xhr.status,
            `Upload failed: ${xhr.status} ${xhr.statusText}`,
            body,
          ),
        );
      }
    };

    xhr.onerror = () => {
      removeAbortListener();
      reject(new ApiError(0, "Network error during upload"));
    };
    xhr.onabort = () => {
      removeAbortListener();
      reject(new ApiError(0, "Upload aborted"));
    };

    xhr.send(form);
  });
}

// ─── OCR status badge ─────────────────────────────────────────

function ocrBadgeClass(status: FileOCRStatus): string {
  switch (status) {
    case "done":
      return "bg-green-50 text-green-700 ring-green-600/20";
    case "failed":
      return "bg-red-50 text-red-700 ring-red-600/20";
    case "skipped":
      return "bg-gray-50 text-gray-600 ring-gray-500/20";
    case "processing":
      return "bg-blue-50 text-blue-700 ring-blue-600/20 animate-pulse";
    case "pending":
    default:
      return "bg-amber-50 text-amber-700 ring-amber-600/20";
  }
}

function ocrLabel(status: FileOCRStatus): string {
  switch (status) {
    case "pending":
      return "OCR pending";
    case "processing":
      return "OCR running";
    case "done":
      return "OCR done";
    case "failed":
      return "OCR failed";
    case "skipped":
      return "OCR skipped";
  }
}

// ─── Icons ────────────────────────────────────────────────────

function UploadIcon() {
  return (
    <svg
      className="h-6 w-6 text-gray-400"
      fill="none"
      viewBox="0 0 24 24"
      strokeWidth={1.5}
      stroke="currentColor"
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M3 16.5v2.25A2.25 2.25 0 005.25 21h13.5A2.25 2.25 0 0021 18.75V16.5M16.5 12L12 16.5m0 0L7.5 12m4.5 4.5V3"
      />
    </svg>
  );
}

function CameraIcon() {
  return (
    <svg
      className="h-5 w-5"
      fill="none"
      viewBox="0 0 24 24"
      strokeWidth={1.5}
      stroke="currentColor"
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M6.827 6.175A2.31 2.31 0 015.186 7.23c-.38.054-.757.112-1.134.174C2.999 7.58 2.25 8.504 2.25 9.602v9.296c0 1.05.85 1.901 1.9 1.901h15.7a1.9 1.9 0 001.9-1.9V9.601c0-1.098-.75-2.022-1.802-2.198a43.07 43.07 0 00-1.134-.174 2.31 2.31 0 01-1.64-1.055l-.823-1.316a2.19 2.19 0 00-1.736-1.039 48.63 48.63 0 00-6.332 0 2.19 2.19 0 00-1.736 1.039l-.823 1.316z"
      />
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M16.5 12.75a4.5 4.5 0 11-9 0 4.5 4.5 0 019 0z"
      />
    </svg>
  );
}

function SpinnerIcon() {
  return (
    <svg
      className="h-4 w-4 animate-spin"
      fill="none"
      viewBox="0 0 24 24"
    >
      <circle
        className="opacity-25"
        cx="12"
        cy="12"
        r="10"
        stroke="currentColor"
        strokeWidth="4"
      />
      <path
        className="opacity-75"
        fill="currentColor"
        d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
      />
    </svg>
  );
}

function fmtSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// ─── Sub-component: a single uploaded file row ────────────────

/**
 * Renders one uploaded file with its upload progress and, once uploaded,
 * its OCR status. Polls GET /api/v1/files/{id} via TanStack Query until
 * the OCR status is terminal.
 */
function UploadedFileRow({ slot }: { slot: UploadSlot }) {
  const queryClient = useQueryClient();

  // Poll OCR status once the server has returned a file id. Stops
  // automatically when refetchInterval returns false (terminal status).
  const ocrQ = useQuery({
    queryKey: slot.fileId ? fileKeys.detail(slot.fileId) : ["files", "pending"],
    queryFn: () =>
      apiFetch<FileRecord>(`/api/v1/files/${slot.fileId}`),
    enabled: !!slot.fileId,
    // Only poll while the OCR status is non-terminal. Once terminal, stop.
    refetchInterval: (query) => {
      const status = query.state.data?.ocr_status;
      if (status && TERMINAL_OCR_STATUSES.includes(status)) {
        return false;
      }
      return OCR_POLL_MS;
    },
  });

  const ocrStatus: FileOCRStatus =
    slot.fileId ? (ocrQ.data?.ocr_status ?? "pending") : "pending";

  // After OCR reaches a terminal state, invalidate the entity's file list
  // so any sibling FileList component refetches with the final record.
  // Runs once per terminal transition. Side effect lives in useEffect so
  // React controls its timing (and so StrictMode doesn't double-fire).
  const wasTerminalRef = useRef(false);
  useEffect(() => {
    if (
      slot.fileId &&
      TERMINAL_OCR_STATUSES.includes(ocrStatus) &&
      !wasTerminalRef.current
    ) {
      wasTerminalRef.current = true;
      void queryClient.invalidateQueries({ queryKey: fileKeys.lists() });
    }
  }, [slot.fileId, ocrStatus, queryClient]);

  return (
    <li className="rounded-lg border border-gray-200 bg-white px-3 py-2.5">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium text-gray-900">
            {slot.fileName}
          </p>
          <p className="text-xs text-gray-400">{fmtSize(slot.file.size)}</p>
        </div>

        {slot.uploadError ? (
          <span className="inline-flex items-center rounded-md bg-red-50 px-2 py-1 text-xs font-medium text-red-700 ring-1 ring-inset ring-red-600/20">
            Upload failed
          </span>
        ) : slot.uploading ? (
          <span className="inline-flex items-center gap-1 text-xs font-medium text-indigo-600">
            <SpinnerIcon />
            {slot.progress}%
          </span>
        ) : slot.fileId ? (
          <span
            className={`inline-flex items-center rounded-md px-2 py-1 text-xs font-medium ring-1 ring-inset ${ocrBadgeClass(
              ocrStatus,
            )}`}
          >
            {ocrLabel(ocrStatus)}
          </span>
        ) : null}
      </div>

      {/* Upload progress bar */}
      {slot.uploading && !slot.uploadError && (
        <div className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-gray-100">
          <div
            className="h-full bg-indigo-600 transition-[width] duration-150 ease-out"
            style={{ width: `${slot.progress}%` }}
          />
        </div>
      )}

      {slot.uploadError && (
        <p className="mt-1 text-xs text-red-600">{slot.uploadError}</p>
      )}

      {slot.fileId && ocrStatus === "failed" && ocrQ.data?.ocr_error && (
        <p className="mt-1 text-xs text-red-600">
          OCR error: {ocrQ.data.ocr_error}
        </p>
      )}
    </li>
  );
}

// ─── Main component ───────────────────────────────────────────

export default function FileUpload({
  entityType,
  entityId,
}: FileUploadProps) {
  const queryClient = useQueryClient();
  const [slots, setSlots] = useState<UploadSlot[]>([]);
  const [isDragging, setIsDragging] = useState(false);
  const localIdRef = useRef(0);

  // Two hidden inputs: one for the regular file picker, one for the mobile
  // camera (capture="environment"). Refs let us click them from buttons.
  const fileInputRef = useRef<HTMLInputElement>(null);
  const cameraInputRef = useRef<HTMLInputElement>(null);

  const uploadMutation = useMutation({
    mutationFn: ({
      file,
      slotLocalId,
    }: {
      file: File;
      slotLocalId: number;
    }) =>
      uploadFile(
        file,
        entityType,
        entityId,
        (pct) => {
          // Update this slot's progress in place. Using the localId (not
          // array index) keeps the update correct even if a later slot
          // finishes first and reorders the array.
          setSlots((prev) =>
            prev.map((s) =>
              s.localId === slotLocalId ? { ...s, progress: pct } : s,
            ),
          );
        },
      ),
    onMutate: ({ slotLocalId }) => {
      setSlots((prev) =>
        prev.map((s) =>
          s.localId === slotLocalId
            ? { ...s, uploading: true, progress: 0 }
            : s,
        ),
      );
    },
    onSuccess: (record, { slotLocalId }) => {
      setSlots((prev) =>
        prev.map((s) =>
          s.localId === slotLocalId
            ? {
                ...s,
                uploading: false,
                progress: 100,
                fileId: record.id,
                uploadError: null,
              }
            : s,
        ),
      );
      // Invalidate any file list queries so sibling lists refetch.
      void queryClient.invalidateQueries({ queryKey: fileKeys.lists() });
    },
    onError: (err, { slotLocalId }) => {
      const message =
        err instanceof ApiError ? err.message : "Upload failed";
      setSlots((prev) =>
        prev.map((s) =>
          s.localId === slotLocalId
            ? { ...s, uploading: false, uploadError: message }
            : s,
        ),
      );
    },
  });

  const startUploads = useCallback(
    (fileList: FileList | File[]) => {
      const files = Array.from(fileList);
      if (files.length === 0) return;
      if (!entityType || !entityId) return;

      // Create a slot per file, then dispatch each as its own mutation.
      // Each upload tracks its own progress independently.
      const newSlots: UploadSlot[] = files.map((file) => ({
        localId: ++localIdRef.current,
        file,
        fileName: file.name,
        progress: 0,
        uploadError: null,
        fileId: null,
        uploading: false,
      }));
      setSlots((prev) => [...prev, ...newSlots]);

      for (let i = 0; i < files.length; i++) {
        uploadMutation.mutate({
          file: files[i],
          slotLocalId: newSlots[i].localId,
        });
      }
    },
    [entityType, entityId, uploadMutation],
  );

  const handleFileInputChange = (
    e: React.ChangeEvent<HTMLInputElement>,
  ) => {
    if (e.target.files) {
      startUploads(e.target.files);
    }
    // Reset so selecting the same file again re-fires onChange.
    e.target.value = "";
  };

  const handleDrop = (e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setIsDragging(false);
    if (e.dataTransfer.files?.length) {
      startUploads(e.dataTransfer.files);
    }
  };

  const handleDragOver = (e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    if (!isDragging) setIsDragging(true);
  };

  const handleDragLeave = (e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setIsDragging(false);
  };

  // When the entity changes (user navigates to a different detail page),
  // clear any in-flight slot list so stale uploads don't bleed across.
  // The mutations themselves are fire-and-forget; the server will still
  // associate them with the entity id passed at dispatch time.
  const entityKey = `${entityType}:${entityId}`;
  const lastEntityKeyRef = useRef(entityKey);
  if (lastEntityKeyRef.current !== entityKey) {
    lastEntityKeyRef.current = entityKey;
    setSlots([]);
  }

  const disabled = !entityType || !entityId;

  return (
    <>
      {/* Drop zone */}
      <div
        onDrop={handleDrop}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        role="button"
        tabIndex={0}
        onClick={() => !disabled && fileInputRef.current?.click()}
        onKeyDown={(e) => {
          if (
            !disabled &&
            (e.key === "Enter" || e.key === " ") &&
            fileInputRef.current
          ) {
            e.preventDefault();
            fileInputRef.current.click();
          }
        }}
        className={`flex cursor-pointer flex-col items-center justify-center rounded-lg border-2 border-dashed px-6 py-8 text-center transition-colors ${
          isDragging
            ? "border-indigo-500 bg-indigo-50/50"
            : "border-gray-300 bg-gray-50/50 hover:border-indigo-400 hover:bg-indigo-50/30"
        } ${disabled ? "cursor-not-allowed opacity-50" : ""}`}
        aria-label="Drop files here or click to upload"
      >
        <UploadIcon />
        <p className="mt-2 text-sm font-medium text-gray-700">
          Drop files here, or{" "}
          <span className="text-indigo-600">browse</span>
        </p>
        <p className="mt-0.5 text-xs text-gray-400">
          Images, PDFs, documents — up to 25 MB each
        </p>
      </div>

      {/* Hidden inputs */}
      <input
        ref={fileInputRef}
        type="file"
        multiple
        className="hidden"
        onChange={handleFileInputChange}
        aria-hidden="true"
      />
      <input
        ref={cameraInputRef}
        type="file"
        accept="image/*"
        capture="environment"
        className="hidden"
        onChange={handleFileInputChange}
        aria-hidden="true"
      />

      {/* Action buttons */}
      <div className="mt-3 flex flex-wrap gap-2">
        <button
          type="button"
          onClick={() => fileInputRef.current?.click()}
          disabled={disabled}
          className="inline-flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-semibold text-white hover:bg-indigo-500 disabled:bg-gray-300 transition-colors"
        >
          <UploadIcon />
          Upload File
        </button>
        <button
          type="button"
          onClick={() => cameraInputRef.current?.click()}
          disabled={disabled}
          className="inline-flex items-center gap-1.5 rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:bg-gray-100 disabled:text-gray-400 transition-colors"
          // The camera button is only useful on touch devices that have a
          // camera; on desktop it falls back to a regular file picker.
          title="Take a photo with your camera"
        >
          <CameraIcon />
          Take Photo
        </button>
      </div>

      {/* Uploaded files list */}
      {slots.length > 0 && (
        <ul className="mt-4 space-y-2">
          {slots.map((slot) => (
            <UploadedFileRow key={slot.localId} slot={slot} />
          ))}
        </ul>
      )}
    </>
  );
}
