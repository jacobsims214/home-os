"use client";

import FileUpload from "@/components/files/FileUpload";
import FileList from "@/components/files/FileList";

// ─── Types ────────────────────────────────────────────────────

interface FilesSectionProps {
  entityType: string;
  entityId: string;
}

/**
 * FilesSection — single-section wrapper around FileUpload and FileList.
 *
 * Renders one "Files" heading and the two file widgets (upload + list) for a
 * given household entity. This replaces the previous pattern where FileUpload
 * and FileList each rendered their own `<h2>Files</h2>`, producing duplicate
 * headings on detail pages. The section wrapper and the heading are owned
 * here; FileUpload and FileList now render only their own inner content (drop
 * zone, button row, uploaded slots, file rows, dialogs).
 *
 * Mirrors the structure of NotesSection
 * (apps/ui/components/notes/NotesSection.tsx): one `<section>`, one heading,
 * inner content below. `entityType` + `entityId` are forwarded to both
 * children so they scope their queries and uploads to the same entity, exactly
 * as before. The two children stay in sync via the shared
 * `fileKeys.listByEntity(entityType, entityId)` TanStack Query key —
 * FileUpload invalidates that key when an upload settles and FileList
 * refetches automatically (see architecture/file-upload-ui.md and
 * architecture/file-module-ui.md).
 *
 * Why a wrapper instead of merging the two components: FileUpload owns the
 * upload flow (drag-drop, file picker, mobile camera, XHR progress, OCR
 * polling) and FileList owns the listing, content-type icons, OCR badges, and
 * delete confirmation. They are deliberately separate so a page can render one
 * without the other and so each can be tested independently. FilesSection
 * composes them for the common case where a detail page wants both under a
 * single heading.
 */
export default function FilesSection({
  entityType,
  entityId,
}: FilesSectionProps) {
  return (
    <section className="mt-8">
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-lg font-semibold text-gray-900">Files</h2>
      </div>

      <FileUpload entityType={entityType} entityId={entityId} />

      <div className="mt-4">
        <FileList entityType={entityType} entityId={entityId} />
      </div>
    </section>
  );
}
