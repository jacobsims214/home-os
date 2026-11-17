"use client";

/**
 * EntityResources — the single entry point for an entity's Resources (#4564).
 *
 * Every entity detail page renders `<EntityResources entityType entityId />`
 * and gets the same Files / Notes / Secrets sections, role-gated, mobile-first.
 * No page wires FileUpload, FileList, NotesSection or SecretsSection itself:
 * that is how the sections drifted apart before, and how a page ends up
 * showing a control its role cannot use.
 *
 * ## Role policy (#4689)
 * The signed-in role comes from `useNavRole()` (GET /me/households). The
 * access decision is `resourceAccess()` in lib/resource-access.ts, a UI mirror
 * of apps/api/internal/auth/roles.go. A tab whose access is "none" is not
 * rendered, and when no tab is usable the whole section renders nothing:
 *
 *   - housesitter: no files, no secrets; notes only on a pet (may add them).
 *     On a bill or a vehicle the section is absent entirely.
 *   - vendor: no files, no notes, no secrets — absent on every entity.
 *   - viewer: files and notes read-only, no secrets.
 *   - family roles: everything.
 *
 * The API enforces all of this; the UI only avoids drawing dead controls.
 *
 * ## Layout
 * A tab bar (Files / Notes / Secrets) under one "Resources" heading, so three
 * heavy lists never stack. Only the active tab mounts, so a page does not
 * fetch three lists to show one; the TanStack Query cache keeps a revisited
 * tab instant. On a phone the tab bar scrolls horizontally if it must, and
 * each tab is a 44 px target.
 */

import { useMemo, useState } from "react";
import FilesSection from "@/components/files/FilesSection";
import NotesSection from "@/components/notes/NotesSection";
import SecretsSection from "@/components/secrets/SecretsSection";
import { useNavRole } from "@/components/shared/use-nav-role";
import { resourceAccess, type Access } from "@/lib/resource-access";

// ─── Types ────────────────────────────────────────────────────

interface EntityResourcesProps {
  entityType: string;
  entityId: string;
  /**
   * Override the role decision (tests, and a page that already knows the
   * role). Left out, the role comes from the session.
   */
  role?: string | null;
  /** Hide the "Resources" heading when a parent already renders one. */
  hideHeading?: boolean;
}

type ResourceTab = "files" | "notes" | "secrets";

interface TabDef {
  id: ResourceTab;
  label: string;
}

const ALL_TABS: readonly TabDef[] = [
  { id: "files", label: "Files" },
  { id: "notes", label: "Notes" },
  { id: "secrets", label: "Secrets" },
];

/** The tabs a role may use, in order. */
export function tabsForAccess(access: { files: Access; notes: Access; secrets: Access }): TabDef[] {
  return ALL_TABS.filter((t) => access[t.id] !== "none");
}

// ─── Component ────────────────────────────────────────────────

export default function EntityResources({
  entityType,
  entityId,
  role,
  hideHeading = false,
}: EntityResourcesProps) {
  const sessionRole = useNavRole();
  const effectiveRole = role !== undefined ? role : sessionRole;

  const access = useMemo(
    () => resourceAccess(effectiveRole, entityType),
    [effectiveRole, entityType],
  );
  const tabs = useMemo(() => tabsForAccess(access), [access]);

  const [activeTab, setActiveTab] = useState<ResourceTab>("files");
  // The active tab may not exist for this role (a viewer has no Secrets): fall
  // back to the first usable one rather than rendering an empty panel.
  const current = tabs.find((t) => t.id === activeTab) ?? tabs[0];

  // Nothing this role can use: no heading, no empty tab bar, no 403 behind a
  // control. A housesitter on a bill sees no Resources section at all.
  if (!access.any || !current) return null;

  return (
    <section className="mt-8" data-testid="entity-resources">
      {!hideHeading && (
        <h2 className="mb-3 text-lg font-semibold text-ink">Resources</h2>
      )}

      {tabs.length > 1 && (
        <div
          role="tablist"
          aria-label="Resource types"
          className="mb-3 flex gap-1 overflow-x-auto border-b border-line"
        >
          {tabs.map((tab) => {
            const active = tab.id === current.id;
            return (
              <button
                key={tab.id}
                role="tab"
                aria-selected={active}
                aria-controls={`tabpanel-${tab.id}`}
                id={`tab-${tab.id}`}
                onClick={() => setActiveTab(tab.id)}
                className={
                  "inline-flex min-h-11 shrink-0 items-center border-b-2 px-4 text-sm font-medium transition-colors " +
                  (active
                    ? "border-accent text-accent"
                    : "border-transparent text-ink-3 hover:border-line hover:text-ink")
                }
              >
                {tab.label}
              </button>
            );
          })}
        </div>
      )}

      <div id={`tabpanel-${current.id}`} role="tabpanel" aria-labelledby={`tab-${current.id}`}>
        {current.id === "files" && (
          <FilesSection entityType={entityType} entityId={entityId} access={access.files} hideHeading />
        )}
        {current.id === "notes" && (
          <NotesSection entityType={entityType} entityId={entityId} access={access.notes} hideHeading />
        )}
        {current.id === "secrets" && (
          <SecretsSection entityType={entityType} entityId={entityId} />
        )}
      </div>
    </section>
  );
}
