"use client";

/**
 * The frame every Settings section page sits in (#4560).
 *
 * Two layouts, one markup, and the same one the rest of the app uses:
 *
 * - Phone (< 768 px, the shell's breakpoint): a sticky "Back to Settings" bar
 *   (the back button + the section title) exactly like
 *   components/layout/DetailPageLayout.tsx, then the section in one column.
 *   The section list lives on the index page, so a phone never scrolls a
 *   700 px settings page to find a control.
 * - >= 768 px: a sticky section list beside the pane (artifact #103's desktop
 *   layout), the current row marked with aria-current.
 *
 * The list is filtered by role (settings-sections.ts), so a section a role
 * cannot use is not drawn at all. The API enforces the same table.
 */

import Link from "next/link";
import { IconArrowLeft, IconChevronRight } from "@tabler/icons-react";
import { useNavRole } from "@/components/shared/use-nav-role";
import settingsClasses from "./settings.module.css";
import {
  SETTINGS_BASE,
  settingsSectionsForRole,
  type SettingsSection,
  type SettingsSectionId,
} from "./settings-sections";

const ICON_STROKE = 1.75;

export function SettingsSectionNav({
  active,
  className = "",
}: {
  active: SettingsSectionId | null;
  className?: string;
}) {
  const role = useNavRole();
  const sections = settingsSectionsForRole(role);

  return (
    <nav aria-label="Settings sections" className={className}>
      <ul className={settingsClasses.sectionList}>
        {sections.map((section) => {
          const Icon = section.icon;
          const current = section.id === active;
          return (
            <li key={section.id}>
              <Link
                href={section.path}
                aria-current={current ? "page" : undefined}
                className={settingsClasses.sectionLink}
              >
                <Icon size={18} stroke={ICON_STROKE} aria-hidden />
                {section.label}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

export default function SettingsFrame({
  section,
  children,
}: {
  /** The section this page shows; null on the index page. */
  section: SettingsSection | null;
  children: React.ReactNode;
}) {
  return (
    <div className={settingsClasses.frame}>
      {/* Phone: the section's own title bar. Back goes to the index. */}
      {section && (
        <div className={settingsClasses.titleBar}>
          <Link
            href={SETTINGS_BASE}
            aria-label="Back to Settings"
            className={settingsClasses.backButton}
          >
            <IconArrowLeft size={22} stroke={ICON_STROKE} aria-hidden />
          </Link>
          <h2 className={settingsClasses.titleBarHeading}>{section.label}</h2>
          <span className={settingsClasses.backSpacer} aria-hidden />
        </div>
      )}

      <div className={settingsClasses.columns}>
        <SettingsSectionNav active={section?.id ?? null} className={settingsClasses.nav} />
        <div className={settingsClasses.pane}>{children}</div>
      </div>
    </div>
  );
}

/** The section's heading in the pane: the title, and one line on what it is. */
export function SectionHeader({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children?: React.ReactNode;
}) {
  return (
    <header className={settingsClasses.paneHeader}>
      <div className={settingsClasses.paneHeadingRow}>
        <h2 className={settingsClasses.paneTitle}>{title}</h2>
        {children}
      </div>
      <p className={settingsClasses.paneDescription}>{description}</p>
    </header>
  );
}

/** A titled block inside a section, built from the app's card tokens. */
export function SettingsCard({
  title,
  description,
  children,
  footer,
}: {
  title: string;
  description?: string;
  children?: React.ReactNode;
  footer?: React.ReactNode;
}) {
  return (
    <section className={settingsClasses.card}>
      <div className={settingsClasses.cardHead}>
        <h3 className={settingsClasses.cardTitle}>{title}</h3>
        {description && <p className={settingsClasses.cardDescription}>{description}</p>}
      </div>
      {children && <div className={settingsClasses.cardBody}>{children}</div>}
      {footer && <div className={settingsClasses.cardFooter}>{footer}</div>}
    </section>
  );
}

/** One row of the index list and of "go to that page" links. */
export function SectionRow({
  href,
  icon,
  title,
  hint,
  label,
}: {
  href: string;
  icon: SettingsSection["icon"];
  title: string;
  hint: string;
  /** Accessible label, when the visible text alone is ambiguous. */
  label?: string;
}) {
  const Icon = icon;
  return (
    <Link href={href} aria-label={label} className={settingsClasses.indexRow}>
      <span className={settingsClasses.indexIcon} aria-hidden>
        <Icon size={20} stroke={ICON_STROKE} />
      </span>
      <span className={settingsClasses.indexText}>
        <span className={settingsClasses.indexTitle}>{title}</span>
        <span className={settingsClasses.indexHint}>{hint}</span>
      </span>
      <IconChevronRight size={18} stroke={ICON_STROKE} aria-hidden className={settingsClasses.indexChevron} />
    </Link>
  );
}
