"use client";

/**
 * FilesSection — the files half of an entity's Resources (#4564).
 *
 * One heading, the upload affordance (with camera capture on a phone) and the
 * list, for any entity: property, vehicle, pet, person, maintenance task,
 * bill. A detail page renders `<FilesSection entityType entityId access />`
 * and nothing else; it never wires FileUpload and FileList itself.
 *
 * `access` is the role decision from resourceAccess() (lib/resource-access.ts):
 * - "write": upload, delete, list (family roles).
 * - "read":  list and download only (a viewer). No upload control at all,
 *            because a housesitter or vendor never reaches this section and a
 *            viewer's upload would be a 403 behind a button.
 * - "none":  renders nothing.
 *
 * The two children stay in sync through the shared
 * `fileKeys.listByEntity(entityType, entityId)` query key: FileUpload
 * invalidates it when an upload settles and FileList refetches.
 */

import FileUpload from "@/components/files/FileUpload";
import FileList from "@/components/files/FileList";
import type { Access } from "@/lib/resource-access";

interface FilesSectionProps {
  entityType: string;
  entityId: string;
  /** "write" (default), "read" or "none". */
  access?: Access;
  /** Hide the section's own heading when a parent already renders one. */
  hideHeading?: boolean;
}

export default function FilesSection({
  entityType,
  entityId,
  access = "write",
  hideHeading = false,
}: FilesSectionProps) {
  if (access === "none") return null;

  return (
    <section className="mt-6" aria-labelledby={hideHeading ? undefined : "files-heading"} data-section="files">
      {!hideHeading && (
        <h3 id="files-heading" className="mb-3 text-base font-semibold text-ink">
          Files
        </h3>
      )}

      {access === "write" && <FileUpload entityType={entityType} entityId={entityId} />}

      <div className={access === "write" ? "mt-3" : ""}>
        <FileList entityType={entityType} entityId={entityId} access={access} />
      </div>
    </section>
  );
}
