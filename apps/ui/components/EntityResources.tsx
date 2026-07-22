"use client";

/**
 * EntityResources — the single entry point for the per-entity resource UI.
 *
 * Renders three polymorphic resource sections (Files, Notes, Secrets) for any
 * entity detail page. Instead of every detail page wiring FileUpload, FileList,
 * NotesSection, and SecretsSection individually (and duplicating
 * ~200 lines of fetch/mutation/search boilerplate per page), detail pages
 * render a single `<EntityResources entityType="..." entityId={...} />`.
 *
 * Layout: a tabbed interface (Files / Notes / Secrets) under a single
 * "Resources" heading. Tabs keep the page compact when an entity has many
 * resources of one kind, and avoid stacking three heavy lists on top of each
 * other. Each child section owns its own data fetching and mutations via
 * TanStack Query, so switching tabs is instant — inactive tab content is
 * unmounted but its query cache is preserved, so returning to a tab restores
 * immediately from cache and revalidates in the background.
 *
 * Each child section accepts the same prop shape:
 *   { entityType: string; entityId: string }
 * — camelCase on the client, matching FileUpload / FileList / NotesSection /
 * SecretsSection. The API layer translates to snake_case
 * (entity_type / entity_id) at the fetch boundary.
 */

import { useState } from "react";
import FilesSection from "@/components/files/FilesSection";
import NotesSection from "@/components/notes/NotesSection";
import SecretsSection from "@/components/secrets/SecretsSection";

// ─── Types ────────────────────────────────────────────────────

interface EntityResourcesProps {
  entityType: string;
  entityId: string;
}

type ResourceTab = "files" | "notes" | "secrets";

interface TabDef {
  id: ResourceTab;
  label: string;
}

const TABS: TabDef[] = [
  { id: "files", label: "Files" },
  { id: "notes", label: "Notes" },
  { id: "secrets", label: "Secrets" },
];

// ─── Component ────────────────────────────────────────────────

export default function EntityResources({
  entityType,
  entityId,
}: EntityResourcesProps) {
  const [activeTab, setActiveTab] = useState<ResourceTab>("files");

  return (
    <section className="mt-8">
      <h2 className="text-lg font-semibold text-gray-900 mb-4">Resources</h2>

      {/* Tab bar */}
      <div
        role="tablist"
        aria-label="Resource types"
        className="flex gap-1 border-b border-gray-200 mb-4"
      >
        {TABS.map((tab) => {
          const active = tab.id === activeTab;
          return (
            <button
              key={tab.id}
              role="tab"
              aria-selected={active}
              aria-controls={`tabpanel-${tab.id}`}
              id={`tab-${tab.id}`}
              onClick={() => setActiveTab(tab.id)}
              className={
                "inline-flex items-center px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors " +
                (active
                  ? "border-indigo-600 text-indigo-700"
                  : "border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300")
              }
            >
              {tab.label}
            </button>
          );
        })}
      </div>

      {/* Active tab panel.
          We render only the active section so inactive sections don't fetch
          until the user opens them — keeps the initial page load light and
          avoids hammering the links/files/notes endpoints for every entity
          view. The TanStack Query cache persists across tab switches, so
          returning to a previously-opened tab restores from cache instantly. */}
      <div
        id={`tabpanel-${activeTab}`}
        role="tabpanel"
        aria-labelledby={`tab-${activeTab}`}
      >
        {activeTab === "files" && (
          <FilesSection entityType={entityType} entityId={entityId} />
        )}
        {activeTab === "notes" && (
          <NotesSection entityType={entityType} entityId={entityId} />
        )}
        {activeTab === "secrets" && (
          <SecretsSection entityType={entityType} entityId={entityId} />
        )}
      </div>
    </section>
  );
}
