"use client";

import { useEffect, useRef, useState } from "react";
import Modal from "@/components/ui/Modal";
import { useAuthStore } from "@/stores/auth";
import type { FileRecord } from "@/lib/types/api";

interface FileViewerProps {
  /** The file metadata to preview. Null/undefined while closed is fine. */
  file: FileRecord | null;
  open: boolean;
  onClose: () => void;
}

const BASE_URL = process.env.NEXT_PUBLIC_API_URL ?? "";

/**
 * Build the absolute content URL for a file.
 *
 * The API serves raw bytes at GET /api/v1/files/:id/content. Bearer-token auth
 * means a plain `<img src>` / `<iframe src>` would 401 (there is no cookie
 * auth), so callers must fetch with the Authorization header and turn the
 * response into a blob URL — see FileViewer.
 */
export function fileContentUrl(fileId: string): string {
  const origin =
    typeof window !== "undefined" ? window.location.origin : "http://localhost:3000";
  return new URL(`${BASE_URL}/api/v1/files/${fileId}/content`, origin).toString();
}

/** Fetch a file's content as a blob, attaching the bearer auth token. */
async function fetchContentBlob(fileId: string): Promise<Blob> {
  const token = useAuthStore.getState().token;
  const res = await fetch(fileContentUrl(fileId), {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  });
  if (!res.ok) {
    throw new Error(`Failed to load file content: ${res.status} ${res.statusText}`);
  }
  return res.blob();
}

/** Fetch a file's content as text, attaching the bearer auth token. */
async function fetchContentText(fileId: string): Promise<string> {
  const token = useAuthStore.getState().token;
  const res = await fetch(fileContentUrl(fileId), {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  });
  if (!res.ok) {
    throw new Error(`Failed to load file content: ${res.status} ${res.statusText}`);
  }
  return res.text();
}

/** Classify a content type into a preview strategy. */
type PreviewKind = "pdf" | "image" | "text" | "other";

function classifyContentType(contentType: string): PreviewKind {
  const ct = contentType.toLowerCase();
  if (ct === "application/pdf") return "pdf";
  if (ct.startsWith("image/")) return "image";
  // Treat plain-text-ish types as text preview.
  if (
    ct.startsWith("text/") ||
    ct === "application/json" ||
    ct === "application/xml" ||
    ct === "application/javascript" ||
    ct === "application/x-yaml"
  ) {
    return "text";
  }
  return "other";
}

/**
 * FileViewer — modal overlay that shows an inline preview of a single file.
 *
 * Preview strategy by content type:
 * - PDF: iframe rendered from a blob object URL (fetched with bearer auth).
 * - Image: img rendered from a blob object URL.
 * - Text: fetched as text and shown in a <pre>.
 * - Other: a "Download" button that fetches the blob and triggers a download.
 *
 * Why blob URLs instead of pointing `<img>`/`<iframe>` directly at
 * `/api/v1/files/:id/content`: the API uses bearer-token auth (no cookies), so
 * a browser-initiated navigation or media fetch would not include the
 * Authorization header and would 401. Fetching the bytes with the token and
 * creating an object URL is the documented pattern (see
 * architecture/file-module.md — "GetFileContent XSS hardening").
 */
export default function FileViewer({ file, open, onClose }: FileViewerProps) {
  const [objectUrl, setObjectUrl] = useState<string | null>(null);
  // Tracks the live object URL outside of React state so that the load effect's
  // cleanup can revoke it as a side effect — never via a setState updater.
  const objectUrlRef = useRef<string | null>(null);
  const [text, setText] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const kind = file ? classifyContentType(file.content_type) : "other";

  // Load content whenever the open file changes. This effect owns the object
  // URL lifecycle: its cleanup revokes the previous URL when the file changes,
  // the modal closes, or the component unmounts. Revocation happens via the
  // ref in the effect cleanup — NEVER inside a setState updater, which React
  // may double-invoke under StrictMode and which couples rendering to a side
  // effect.
  useEffect(() => {
    let cancelled = false;
    setText(null);
    setError(null);

    if (open && file) {
      setLoading(true);
      (async () => {
        try {
          if (kind === "text") {
            const body = await fetchContentText(file.id);
            if (!cancelled) setText(body);
          } else if (kind === "pdf" || kind === "image") {
            const blob = await fetchContentBlob(file.id);
            if (cancelled) return;
            const url = URL.createObjectURL(blob);
            objectUrlRef.current = url;
            setObjectUrl(url);
          }
          // "other" needs nothing up front — fetch on download click.
        } catch (e) {
          if (!cancelled) {
            setError(e instanceof Error ? e.message : "Failed to load file");
          }
        } finally {
          if (!cancelled) setLoading(false);
        }
      })();
    } else {
      setLoading(false);
    }

    return () => {
      cancelled = true;
      if (objectUrlRef.current) {
        URL.revokeObjectURL(objectUrlRef.current);
        objectUrlRef.current = null;
      }
      setObjectUrl(null);
    };
  }, [open, file, kind]);

  const handleDownload = async () => {
    if (!file) return;
    try {
      const blob = await fetchContentBlob(file.id);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = file.name || "file";
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      // Give the browser a tick to start the download before revoking.
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Download failed");
    }
  };

  const title = file?.name ?? "File preview";

  return (
    <Modal open={open} onClose={onClose} title={title} maxWidth="max-w-4xl">
      {error && (
        <div className="mb-4 rounded-md bg-red-50 p-3 text-sm text-red-700">
          {error}
        </div>
      )}

      {file && (
        <div className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-gray-500">
          <span className="font-medium text-gray-700">{file.content_type || "unknown type"}</span>
          <span>{formatBytes(file.size)}</span>
          {file.tags && file.tags.length > 0 && (
            <span>tags: {file.tags.join(", ")}</span>
          )}
        </div>
      )}

      {loading && (
        <div className="flex items-center justify-center py-16 text-sm text-gray-500">
          Loading preview…
        </div>
      )}

      {!loading && file && kind === "pdf" && objectUrl && (
        <iframe
          src={objectUrl}
          title={file.name}
          className="h-[75vh] w-full rounded border border-gray-200 bg-white"
        />
      )}

      {!loading && file && kind === "image" && objectUrl && (
        <div className="flex max-h-[75vh] items-center justify-center overflow-auto rounded border border-gray-200 bg-gray-50 p-2">
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src={objectUrl}
            alt={file.name}
            className="max-h-[72vh] max-w-full object-contain"
          />
        </div>
      )}

      {!loading && file && kind === "text" && text !== null && (
        <pre className="max-h-[75vh] overflow-auto rounded border border-gray-200 bg-gray-50 p-4 text-xs text-gray-800 whitespace-pre-wrap break-words">
          {text}
        </pre>
      )}

      {!loading && file && kind === "other" && (
        <div className="flex flex-col items-center gap-4 py-12 text-center">
          <p className="text-sm text-gray-600">
            Inline preview is not available for this file type.
          </p>
          <button
            type="button"
            onClick={handleDownload}
            className="inline-flex items-center rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 transition-colors"
          >
            Download {file.name}
          </button>
        </div>
      )}
    </Modal>
  );
}

/** Format a byte count as a human-readable string. */
function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.min(
    units.length - 1,
    Math.floor(Math.log(bytes) / Math.log(1024)),
  );
  const value = bytes / Math.pow(1024, i);
  return `${value.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}
