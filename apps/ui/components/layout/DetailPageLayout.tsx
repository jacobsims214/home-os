"use client";

/**
 * DetailPageLayout (#4531) — the frame of every entity detail page (vendor,
 * bill, loan, pet via EntityDetail; the property edit view).
 *
 * Phone (< 768 px, the shell's phone breakpoint, KB design/app-shell.md):
 *   - A title bar that sticks under the shell header: a 44 px back button,
 *     the entity title (h2, ellipsis) and an actions menu (Edit / Delete).
 *     While editing, Cancel and Save replace the menu so saving is one tap.
 *   - The sheet and Resources stack as collapsible sections. Details starts
 *     open, Resources starts closed (it still mounts, so the toggle is instant).
 * Tablet (768–1023 px): one column, full button row, nothing collapses.
 * Desktop (≥ 1024 px): two columns, the sheet on the left and Resources on
 *   the right.
 *
 * Collapsing is CSS-only (`max-md:hidden`), so server and client render the
 * same markup and wider screens never see a closed section.
 * Colours come from the semantic tokens only (architecture/theme-tokens.md).
 */

import { useId, useState } from "react";
import Link from "next/link";
import { Button, Menu } from "@mantine/core";
import {
  IconArrowLeft,
  IconChevronDown,
  IconDotsVertical,
  IconPencil,
  IconTrash,
} from "@tabler/icons-react";
import EntityResources from "@/components/EntityResources";

interface DetailPageLayoutProps {
  entityType: string;
  entityId: string;
  title: string;
  isEditing?: boolean;
  onEdit?: () => void;
  onDelete?: () => void;
  onCancel?: () => void;
  onSave?: () => void;
  isSaving?: boolean;
  children: React.ReactNode;
}

const ICON_STROKE = 1.75;

/** "vendor" → "Vendors", "property" → "Properties". */
function pluralLabel(entityType: string): string {
  const word = entityType.charAt(0).toUpperCase() + entityType.slice(1);
  return /[^aeiou]y$/i.test(word) ? `${word.slice(0, -1)}ies` : `${word}s`;
}

/** URL segment for the list page: "property" → "properties". */
function pluralPath(entityType: string): string {
  return /[^aeiou]y$/i.test(entityType) ? `${entityType.slice(0, -1)}ies` : `${entityType}s`;
}

const iconButton =
  "inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-token-sm text-ink-2 " +
  "transition-colors hover:bg-surface-2 hover:text-ink";

export default function DetailPageLayout({
  entityType,
  entityId,
  title,
  isEditing,
  onEdit,
  onDelete,
  onCancel,
  onSave,
  isSaving,
  children,
}: DetailPageLayoutProps) {
  const [detailsOpen, setDetailsOpen] = useState(true);
  const [resourcesOpen, setResourcesOpen] = useState(false);
  const backHref = `/dashboard/${pluralPath(entityType)}`;
  const backLabel = `Back to ${pluralLabel(entityType)}`;
  const heading = title || "Untitled";

  const startEdit = () => {
    setDetailsOpen(true); // the form lives in Details; never edit into a closed section
    onEdit?.();
  };

  const editButtons = (
    <>
      {/* Size sm from md up; 44 px touch targets on phones. */}
      <Button variant="default" size="sm" onClick={onCancel} className="max-md:min-h-11">
        Cancel
      </Button>
      <Button size="sm" onClick={onSave} loading={isSaving} className="max-md:min-h-11">
        Save
      </Button>
    </>
  );

  return (
    <div className="min-w-0">
      {/* Title bar. Sticky on phones only; spans the shell's 16 px gutter. */}
      <div className="sticky top-[var(--app-shell-header-offset,0px)] z-10 -mx-4 -mt-4 mb-4 flex min-h-14 items-center gap-1 border-b border-line bg-bg px-2 md:static md:mx-0 md:mt-0 md:mb-6 md:items-end md:gap-4 md:border-0 md:bg-transparent md:px-0">
        <Link href={backHref} aria-label={backLabel} className={`${iconButton} md:hidden`}>
          <IconArrowLeft size={22} stroke={ICON_STROKE} aria-hidden />
        </Link>

        <div className="min-w-0 flex-1">
          <Link
            href={backHref}
            className="hidden items-center gap-1 text-sm text-ink-3 hover:text-accent md:inline-flex"
          >
            <IconArrowLeft size={16} stroke={ICON_STROKE} aria-hidden />
            {backLabel}
          </Link>
          <h2 className="truncate text-lg font-semibold leading-tight text-ink md:mt-1 md:text-2xl md:font-bold">
            {heading}
          </h2>
        </div>

        {isEditing ? (
          <div className="flex shrink-0 items-center gap-2 pr-1 md:pr-0">{editButtons}</div>
        ) : (
          <>
            {/* Phone: one actions menu. */}
            <div className="md:hidden">
              <Menu position="bottom-end" width={200} shadow="lg" radius="md" withinPortal>
                <Menu.Target>
                  <button type="button" className={iconButton} aria-label={`Actions for ${heading}`}>
                    <IconDotsVertical size={22} stroke={ICON_STROKE} aria-hidden />
                  </button>
                </Menu.Target>
                <Menu.Dropdown>
                  <Menu.Item
                    onClick={startEdit}
                    leftSection={<IconPencil size={18} stroke={ICON_STROKE} aria-hidden />}
                    className="min-h-11"
                  >
                    Edit
                  </Menu.Item>
                  {onDelete && (
                    <Menu.Item
                      onClick={onDelete}
                      leftSection={<IconTrash size={18} stroke={ICON_STROKE} aria-hidden />}
                      className="min-h-11"
                      style={{ color: "var(--danger)" }}
                    >
                      Delete
                    </Menu.Item>
                  )}
                </Menu.Dropdown>
              </Menu>
            </div>
            {/* Tablet and desktop: the buttons themselves. */}
            <div className="hidden shrink-0 items-center gap-2 md:flex">
              <Button variant="default" onClick={startEdit} leftSection={<IconPencil size={16} stroke={ICON_STROKE} />}>
                Edit
              </Button>
              {onDelete && (
                <Button color="red" variant="outline" onClick={onDelete} leftSection={<IconTrash size={16} stroke={ICON_STROKE} />}>
                  Delete
                </Button>
              )}
            </div>
          </>
        )}
      </div>

      <div className="flex flex-col gap-3 md:gap-6 lg:grid lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)] lg:items-start">
        <CollapsibleSection
          title="Details"
          open={detailsOpen}
          onToggle={() => setDetailsOpen((o) => !o)}
          className="rounded-token-md border border-line bg-surface px-4 shadow-token-sm md:p-6"
          panelClassName="max-md:pb-4"
        >
          {children}
        </CollapsibleSection>

        {/* EntityResources renders its own "Resources" h2 and top margin. On a
            phone the section toggle is the heading, so that h2 is hidden there;
            in the desktop column the margin would misalign the two tops. */}
        <CollapsibleSection
          title="Resources"
          hint="Files, notes and secrets"
          open={resourcesOpen}
          onToggle={() => setResourcesOpen((o) => !o)}
          className="max-md:rounded-token-md max-md:border max-md:border-line max-md:bg-surface max-md:px-4 max-md:shadow-token-sm max-md:[&_section>h2]:hidden md:[&_section]:mt-0"
          panelClassName="max-md:pb-2"
        >
          <EntityResources entityType={entityType} entityId={entityId} />
        </CollapsibleSection>
      </div>
    </div>
  );
}

/**
 * A section that collapses below 768 px. From 768 px up the toggle is gone
 * and the content always shows.
 */
function CollapsibleSection({
  title,
  hint,
  open,
  onToggle,
  className = "",
  panelClassName = "",
  children,
}: {
  title: string;
  hint?: string;
  open: boolean;
  onToggle: () => void;
  className?: string;
  panelClassName?: string;
  children: React.ReactNode;
}) {
  const id = useId();
  const panelId = `${id}-panel`;

  return (
    <section aria-label={title} className={`min-w-0 ${className}`}>
      <h3 className="md:hidden">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={panelId}
          onClick={onToggle}
          className="flex min-h-12 w-full items-center gap-2 text-left"
        >
          <span className="min-w-0 flex-1">
            <span className="block text-sm font-semibold text-ink">{title}</span>
            {hint && !open && <span className="block truncate text-xs text-ink-3">{hint}</span>}
          </span>
          <IconChevronDown
            size={20}
            stroke={ICON_STROKE}
            aria-hidden
            className={`shrink-0 text-ink-2 transition-transform motion-reduce:transition-none ${open ? "rotate-180" : ""}`}
          />
        </button>
      </h3>
      <div id={panelId} className={`min-w-0 ${open ? panelClassName : "max-md:hidden"}`}>
        {children}
      </div>
    </section>
  );
}
