/**
 * TanStack Query hooks for the Documents hub (#4569).
 *
 * Every call goes through `apiFetch` (lib/api.ts), which routes to the BFF and
 * sends a 401 to /login. The endpoints are the ones #4478 shipped — there is no
 * documents-specific route and this file invents none
 * (KB architecture/document-hub-api.md):
 *
 *   GET    /files?category=&person_id=   household files, newest first, minus
 *                                        the credential-linked ones a viewer may
 *                                        not see (the API removes them; the UI
 *                                        never learns they existed)
 *   GET    /files/{id}                   one file's metadata
 *   GET    /files/{id}/content           the bytes (preview / download)
 *   POST   /files/upload                 multipart: file + entity_type/entity_id
 *                                        + optional metadata form fields
 *   PATCH  /files/{id}                   jsonopt: absent = unchanged, null = clear
 *   DELETE /files/{id}                   file + blob
 *   GET    /links?entity_type=&entity_id=  links FROM this file
 *   GET    /people                        names for the "For" field and chips
 *
 * What it deliberately does NOT do: search. Semantic search is the one
 * endpoint under /search (GET /api/v1/search, #4561), and the hub links into
 * its page rather than re-implementing it (the hub's own box is a plain text
 * filter over the list it already has).
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError, bffPath, redirectToLogin } from "@/lib/api";
import { fileKeys } from "@/lib/query-keys";
import type { FileRecord } from "@/lib/types/api";
import type { Category } from "@/lib/documents/model";

// ─── Wire shapes ─────────────────────────────────────────────

interface DataEnvelope<T> {
  data: T;
}

/** GET /api/v1/people row: the fields a document's "For" field needs. */
export interface PersonRow {
  id: string;
  display_name: string;
  relationship?: string | null;
  color?: string;
}

/** GET /api/v1/links row (the API groups every link under "other"). */
export interface LinkRow {
  id: string;
  entity_type: string;
  entity_id: string;
  link_type: "file" | "secret" | "url" | string;
  link_id: string;
  title: string;
  url?: string | null;
  created_at: string;
}

interface LinksResponse {
  data: { other?: LinkRow[] };
}

export interface DocumentListFilters {
  category?: Category | null;
  personId?: string | null;
}

// ─── Query keys ──────────────────────────────────────────────

/**
 * The hub's keys hang off `fileKeys.lists()` (lib/query-keys.ts), so the entity
 * pages' invalidation (`invalidateQueries({ queryKey: fileKeys.lists() })`)
 * refreshes this list too, and vice versa: a document uploaded here appears on
 * its bill's Files section without a manual reload.
 */
export const documentKeys = {
  list: (filters: DocumentListFilters = {}) =>
    [...fileKeys.lists(), "documents", filters.category ?? "", filters.personId ?? ""] as const,
  links: (fileId: string) => ["documents", "links", fileId] as const,
  person: (id: string) => ["people", "detail", id] as const,
};

// ─── Reads ───────────────────────────────────────────────────

/**
 * The household's documents. `category` and `person_id` are the API's own
 * filters (GET /files), so a narrowed view stays correct as the family's pile
 * grows; nothing is filtered here.
 */
export function useDocuments(filters: DocumentListFilters = {}, enabled = true) {
  const params: Record<string, string | undefined> = {};
  if (filters.category) params.category = filters.category;
  if (filters.personId) params.person_id = filters.personId;
  return useQuery({
    queryKey: documentKeys.list(filters),
    queryFn: () => apiFetch<DataEnvelope<FileRecord[]>>("/api/v1/files", { params }),
    enabled,
    retry: false,
  });
}

/** One document's metadata. */
export function useDocument(id: string | undefined, enabled = true) {
  return useQuery({
    queryKey: fileKeys.detail(id ?? ""),
    queryFn: () => apiFetch<DataEnvelope<FileRecord>>(`/api/v1/files/${encodeURIComponent(id!)}`),
    enabled: !!id && enabled,
    retry: false,
  });
}

/** The household's people, for the "For" field and the person filter chips. */
export function usePeople(enabled = true) {
  return useQuery({
    queryKey: ["people", "list"],
    queryFn: () => apiFetch<DataEnvelope<PersonRow[]>>("/api/v1/people"),
    staleTime: 60_000,
    enabled,
    retry: false,
  });
}

/**
 * Links FROM this file: a link row names another record ("Also linked"). The
 * API has no reverse lookup (links pointing AT a file live under the other
 * record's entity), so the detail page shows the file's own links plus the
 * record it is attached to (files.entity_type / entity_id) — never an invented
 * "related documents" list.
 */
export function useDocumentLinks(fileId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: documentKeys.links(fileId ?? ""),
    queryFn: () =>
      apiFetch<LinksResponse>("/api/v1/links", {
        params: { entity_type: "file", entity_id: fileId! },
      }),
    enabled: !!fileId && enabled,
    retry: false,
  });
}

// ─── Upload ──────────────────────────────────────────────────

/** The metadata a person can set at upload time (all optional in the API). */
export interface UploadMetadata {
  title?: string;
  category?: Category;
  document_date?: string;
  expires_on?: string;
  description?: string;
  person_id?: string;
}

/**
 * Upload one file with its metadata, reporting byte progress.
 *
 * XMLHttpRequest rather than fetch because fetch has no upload-progress event,
 * and progress is what makes a phone upload at the kitchen counter trustworthy
 * (#4564's FileUpload uses the same approach for the same reason). Posts
 * same-origin to the BFF, which authenticates from the session cookie.
 *
 * Metadata goes as ordinary multipart form fields, exactly as the API's
 * UploadFile reads them (#4478): blank means "not given".
 */
export function uploadDocument(
  file: File,
  metadata: UploadMetadata,
  onProgress: (pct: number) => void,
  signal?: AbortSignal,
): Promise<FileRecord> {
  return new Promise((resolve, reject) => {
    const form = new FormData();
    form.append("file", file, file.name);
    // No entity means a family-level document; the API defaults it to
    // ('household', <this household>) and rejects the pair otherwise. Sending
    // it explicitly is what the API documents, and it keeps the row honest if
    // that default ever changes.
    form.append("entity_type", "household");
    for (const [key, value] of Object.entries(metadata)) {
      const v = (value ?? "").toString().trim();
      if (v) form.append(key, v);
    }

    const xhr = new XMLHttpRequest();
    xhr.open("POST", bffPath("/api/v1/files/upload"), true);
    // Do NOT set Content-Type: the browser must add the multipart boundary.

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) onProgress(Math.round((event.loaded / event.total) * 100));
    };

    const onAbort = () => xhr.abort();
    let removeAbort: () => void = () => {};
    if (signal) {
      if (signal.aborted) {
        reject(new ApiError(0, "Upload cancelled"));
        return;
      }
      signal.addEventListener("abort", onAbort);
      removeAbort = () => signal.removeEventListener("abort", onAbort);
    }

    xhr.onload = () => {
      removeAbort();
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          const body = JSON.parse(xhr.responseText) as DataEnvelope<FileRecord>;
          resolve(body.data);
        } catch {
          reject(new ApiError(xhr.status, "the upload answered with something that wasn't a document"));
        }
        return;
      }
      if (xhr.status === 401) redirectToLogin();
      let details: unknown;
      try {
        details = JSON.parse(xhr.responseText);
      } catch {
        details = xhr.responseText;
      }
      reject(new ApiError(xhr.status, uploadFailureMessage(xhr.status, details), details));
    };
    xhr.onerror = () => {
      removeAbort();
      reject(new ApiError(0, "The upload didn't reach Home OS. Check your connection and try again — nothing was saved.", undefined, true));
    };
    xhr.onabort = () => {
      removeAbort();
      reject(new ApiError(0, "Upload cancelled. Nothing was saved."));
    };

    xhr.send(form);
  });
}

/**
 * A failure a person can act on. The API answers 400 with a specific message
 * ("upload too large or invalid multipart form (max 25MB)", the metadata
 * validation text) and 403 for a role that may not write — each says something
 * different, so they must not collapse into "upload failed".
 */
export function uploadFailureMessage(status: number, body: unknown): string {
  const apiMessage =
    body && typeof body === "object" && "error" in body
      ? (body as { error?: { message?: unknown } }).error?.message
      : undefined;
  const message = typeof apiMessage === "string" ? apiMessage : "";
  if (status === 413 || /too large|25MB/i.test(message)) {
    return "That file is larger than the 25 MB limit. Nothing was saved — try a smaller photo or a PDF.";
  }
  if (status === 403) return "Your role can't upload documents in this family.";
  if (status === 404) return "Home OS couldn't find the family to file this under. Nothing was saved.";
  if (status >= 500) return "Home OS had a problem saving this. Nothing was saved — try again in a moment.";
  if (status >= 400 && message) return `${message} Nothing was saved.`;
  if (status >= 400) return "Home OS refused this upload. Nothing was saved.";
  return "Upload failed. Nothing was saved.";
}

// ─── Writes on an existing document ─────────────────────────

/** A PATCH /files/{id} body. `null` clears a field; omitting it leaves it. */
export interface DocumentPatch {
  title?: string | null;
  category?: Category;
  document_date?: string | null;
  expires_on?: string | null;
  description?: string | null;
  person_id?: string | null;
  tags?: string[];
  name?: string;
}

export function useUpdateDocument(id: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (patch: DocumentPatch) =>
      apiFetch<DataEnvelope<FileRecord>>(`/api/v1/files/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body: patch,
      }),
    onSuccess: (res) => {
      queryClient.setQueryData(fileKeys.detail(id), res);
      void queryClient.invalidateQueries({ queryKey: fileKeys.lists() });
    },
  });
}

export function useDeleteDocument(id: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch<void>(`/api/v1/files/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: fileKeys.detail(id) });
      void queryClient.invalidateQueries({ queryKey: fileKeys.lists() });
    },
  });
}
