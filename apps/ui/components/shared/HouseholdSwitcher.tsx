"use client";

/**
 * Family switcher (#4574), in the shell's switcher slot. Built to the approved
 * shell spec (KB design/app-shell.md, artifact #75 v1, "Family switcher, user
 * menu, notifications"): the current family's name in the header chip, the
 * sidebar/drawer card and the More sheet row, and a picker of the families this
 * person belongs to — a bottom sheet on phones, a 340 px popover from 768 px up,
 * `role="radiogroup"`, one 64 px row per family with its mark, name, the role
 * the person holds there and a check on the selected one.
 *
 * One household in the account means no switcher at all: the chip and the card
 * still name the family (that is the "where am I" the spec asks for) but there is
 * nothing to pick, so they are not buttons, and the More sheet's row becomes the
 * Members link instead of a "Switch family" row that switches nothing.
 *
 * The switch itself is useHouseholdSwitch: cookie first, then cancel and reset
 * every household-scoped query so the previous family's rows can never stay on
 * screen; the new role applies in the same render, because use-nav-role reads
 * the same list this component lists.
 *
 * WHO OWNS THE PICKER. One instance per shell slot (`variant`), but the picker
 * itself is rendered by exactly one of them at any width (`surfaceAt`), because
 * two open surfaces would be two focus traps and a portal escapes the CSS that
 * hides a slot:
 *
 *   chip         (header, below 992 px)   renders the picker below 992 px
 *   card/wide    (desktop sidebar, >=992) renders the picker from 992 px
 *   card/drawer  (phone + tablet drawer)  only a trigger: it closes the drawer
 *                                         and opens the chip's picker
 *   row          (More sheet, < 768)      only a trigger: same, via the chip
 *
 * The OPEN state lives in the shell (useHouseholdSwitcherControl), so a trigger
 * can open a picker it does not own.
 */

import { forwardRef, useRef, useState } from "react";
import Link from "next/link";
import { Drawer, Popover, UnstyledButton, VisuallyHidden } from "@mantine/core";
import { useMediaQuery, useReducedMotion } from "@mantine/hooks";
import { IconCheck, IconSelector, IconUsers } from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import { householdRoleLabel, householdRoleSummary } from "@/lib/household-roles";
import {
  useCurrentHousehold,
  useMyHouseholds,
  type MyHousehold,
} from "@/components/shared/use-nav-role";
import { useHouseholdSwitch } from "@/components/shared/use-household-switch";
import shellClasses from "./DashboardShell.module.css";
import sidebarClasses from "./Sidebar.module.css";
import classes from "./HouseholdSwitcher.module.css";

/** From Mantine `sm` (48em) up the picker is a popover, below it a bottom sheet. */
const WIDE_QUERY = "(min-width: 48em)";
/** Mantine `md` (62em): where the desktop sidebar replaces the header chip. */
const DESKTOP_QUERY = "(min-width: 62em)";

/** The picker's heading, which also labels its radiogroup. */
const PANEL_TITLE = "Your families";
const PANEL_TITLE_ID = "household-switcher-title";

/** Which shell slot the switcher renders in. */
export type HouseholdSwitcherVariant = "chip" | "card" | "row";

/** The open state the shell owns; every variant reads it. */
export interface HouseholdSwitcherControl {
  opened: boolean;
  open: () => void;
  close: () => void;
}

export function useHouseholdSwitcherControl(): HouseholdSwitcherControl {
  const [opened, setOpened] = useState(false);
  return {
    opened,
    open: () => setOpened(true),
    close: () => setOpened(false),
  };
}

/** The households this person belongs to, and which one is active. */
function useHouseholds(): { list: MyHousehold[]; current: MyHousehold | null } {
  const q = useMyHouseholds();
  const current = useCurrentHousehold();
  return { list: Array.isArray(q.data?.data) ? q.data!.data : [], current };
}

/**
 * Where a slot's picker may appear. Exactly one mounted slot matches at any
 * width, so only one picker can ever be open.
 *   "compact" — below 992 px (the header chip is on screen there)
 *   "wide"    — from 992 px (the desktop sidebar card)
 *   "none"    — never: this slot only opens another slot's picker
 */
export type HouseholdSwitcherSurface = "compact" | "wide" | "none";

export interface HouseholdSwitcherProps {
  variant: HouseholdSwitcherVariant;
  control: HouseholdSwitcherControl;
  surfaceAt: HouseholdSwitcherSurface;
  /**
   * Runs before the picker opens, for a trigger inside another overlay: the More
   * sheet / drawer closes first so its focus trap and focus return don't fight
   * the sheet that opens next (BottomNav does the same for notifications).
   */
  onBeforeOpen?: () => void;
}

export default function HouseholdSwitcher({
  variant,
  control,
  surfaceAt,
  onBeforeOpen,
}: HouseholdSwitcherProps) {
  const { list, current } = useHouseholds();
  const isWide = useMediaQuery(WIDE_QUERY);
  const isDesktop = useMediaQuery(DESKTOP_QUERY);
  const reduceMotion = useReducedMotion();
  const switchHousehold = useHouseholdSwitch();
  const ownsPicker = surfaceAt === "compact" ? !isDesktop : surfaceAt === "wide" ? isDesktop : false;
  const name = current?.name || "Your family";
  // One family: there is nothing to pick (the list is empty only while it loads,
  // and then the trigger is the plain label, so no flash of a dead button).
  const switchable = list.length > 1;

  const [switching, setSwitching] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  const close = () => {
    setError(null);
    setSwitching(null);
    control.close();
  };

  const choose = async (household: MyHousehold) => {
    if (switching) return;
    if (household.id === current?.id) {
      close();
      return;
    }
    setSwitching(household.id);
    setError(null);
    try {
      await switchHousehold(household.id);
      close();
    } catch {
      // A failed switch changed nothing (remembering the id is its first step),
      // so say so rather than closing as if it had worked.
      setError(`Could not switch to ${household.name}. Try again.`);
      setSwitching(null);
    }
  };

  if (!switchable) {
    return <PlainTrigger variant={variant} name={name} />;
  }

  const openPicker = () => {
    if (!onBeforeOpen) {
      control.open();
      return;
    }
    // A delegating trigger sits inside a drawer/sheet that has to close first
    // (two focus traps, and its focus return would steal the new sheet's).
    onBeforeOpen();
    window.setTimeout(() => control.open(), 0);
  };

  const trigger = (
    <TriggerButton
      variant={variant}
      name={name}
      role={current?.role}
      expanded={control.opened}
      aria-label={triggerLabel(name, list.length)}
      onClick={openPicker}
    />
  );

  // A delegating slot: the trigger only, no surface of its own.
  if (!ownsPicker) return <>{trigger}</>;

  const panel = (
    <div className={classes.panel} ref={panelRef} data-testid="household-switcher-panel">
      <div className={classes.head}>
        <h2 className={classes.title} id={PANEL_TITLE_ID}>
          {PANEL_TITLE}
        </h2>
        <p className={classes.hint}>Switching changes what this app shows and what you can do here.</p>
      </div>
      <HouseholdList
        list={list}
        currentId={current?.id ?? null}
        switching={switching}
        onChoose={choose}
      />
      {error && (
        <p className={classes.error} role="alert">
          {error}
        </p>
      )}
    </div>
  );

  if (isWide) {
    return (
      <Popover
        opened={control.opened}
        onChange={(next) => {
          if (!next) close();
        }}
        position="bottom-start"
        offset={8}
        width={340}
        withinPortal
        trapFocus
        returnFocus
        closeOnEscape
        closeOnClickOutside
        transitionProps={{ duration: 0, timingFunction: "ease-out" }}
        classNames={{ dropdown: classes.popover }}
      >
        <Popover.Target>{trigger}</Popover.Target>
        <Popover.Dropdown role="dialog" aria-label="Switch family" data-testid="household-switcher-popover">
          <div className={classes.popoverBody}>{panel}</div>
        </Popover.Dropdown>
      </Popover>
    );
  }

  return (
    <>
      {trigger}
      <Drawer
        opened={control.opened}
        onClose={close}
        position="bottom"
        size="auto"
        // Above the header (100) and the More sheet (101), below the tab bar
        // (102, BottomNav.module.css), like the notifications sheet: the bar
        // stays visible and usable.
        zIndex={101}
        title="Switch family"
        closeButtonProps={{ "aria-label": "Close family switcher", size: "lg" }}
        transitionProps={{ duration: reduceMotion ? 0 : 240, timingFunction: "ease-out" }}
        overlayProps={{ backgroundOpacity: 0.45, blur: 0 }}
        classNames={{
          inner: classes.sheetInner,
          content: classes.sheet,
          header: classes.sheetHeader,
          title: classes.sheetTitle,
          body: classes.sheetBody,
        }}
        data-testid="household-switcher-sheet"
      >
        <span className={classes.handle} aria-hidden />
        {panel}
      </Drawer>
    </>
  );
}

// ─── The trigger, per slot ────────────────────────────────────

function FamilyMark({ name, className }: { name: string; className: string }) {
  return (
    <span className={className} aria-hidden>
      {name.trim().charAt(0).toUpperCase() || "F"}
    </span>
  );
}

/**
 * What the trigger reads out: the family, that it opens a switcher, and how many
 * families there are. Kept as one string so the accessible name matches what is
 * on screen (WCAG 2.5.3).
 */
function triggerLabel(name: string, count: number): string {
  return `Family: ${name}. Switch family (${count} families)`;
}

/**
 * The trigger itself.
 *
 * A forwardRef that spreads Mantine's cloned props onto the real button: as a
 * Popover.Target, Mantine clones its aria-haspopup / aria-expanded /
 * aria-controls and its ref onto this component, and the ref is what anchors the
 * popover. Without it the dropdown renders at the viewport's top-left corner
 * (the notifications bell carries the same forwardRef for the same reason).
 */
const TriggerButton = forwardRef<
  HTMLButtonElement,
  {
    variant: HouseholdSwitcherVariant;
    name: string;
    role: string | null | undefined;
    /** Whether the picker this trigger opens is showing (omitted when it delegates). */
    expanded?: boolean;
    onClick: () => void;
  } & Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "onClick" | "children">
>(function TriggerButton({ variant, name, role, expanded, onClick, ...rest }, ref) {
  const common = {
    ...rest,
    ref,
    onClick,
    "aria-haspopup": "dialog" as const,
    "aria-expanded": expanded,
    // Mantine's clone passes className="" when the target has none; the variant's
    // own class is applied after it so the shell's geometry always wins.
    "data-testid": "household-trigger",
  };

  if (variant === "chip") {
    return (
      <UnstyledButton
        {...common}
        className={[shellClasses.familyChip, rest.className].filter(Boolean).join(" ")}
        data-variant="chip"
      >
        <span>{name}</span>
        <IconSelector size={14} stroke={NAV_ICON_STROKE} aria-hidden />
      </UnstyledButton>
    );
  }

  if (variant === "card") {
    return (
      <UnstyledButton
        {...common}
        className={[sidebarClasses.familyCard, rest.className].filter(Boolean).join(" ")}
        data-variant="card"
      >
        <FamilyMark name={name} className={sidebarClasses.familyMark} />
        <span className={sidebarClasses.familyText}>
          <span className={sidebarClasses.familyName}>{name}</span>
          <span className={sidebarClasses.familyMeta}>{householdRoleLabel(role)}</span>
        </span>
        <IconSelector size={18} stroke={NAV_ICON_STROKE} className={sidebarClasses.familyIcon} aria-hidden />
      </UnstyledButton>
    );
  }

  // The More sheet's row: same geometry as its Notifications and Settings rows.
  return (
    <UnstyledButton
      {...common}
      className={[classes.sheetRow, rest.className].filter(Boolean).join(" ")}
      data-variant="row"
    >
      <IconUsers size={22} stroke={NAV_ICON_STROKE} aria-hidden />
      <span>Switch family</span>
      <span>{name}</span>
    </UnstyledButton>
  );
});

/**
 * The trigger when there is one family: a plain label in the chip slot (the name
 * of the family you are in), and the Members link in the side card and the sheet
 * row (where the family is managed today).
 */
function PlainTrigger({ variant, name }: { variant: HouseholdSwitcherVariant; name: string }) {
  if (variant === "chip") {
    return (
      <span className={shellClasses.familyChip} data-testid="household-chip" data-variant="chip">
        {name}
      </span>
    );
  }
  if (variant === "card") {
    return (
      <UnstyledButton
        component={Link}
        href="/dashboard/members"
        className={sidebarClasses.familyCard}
        data-testid="household-card"
        data-variant="card"
      >
        <FamilyMark name={name} className={sidebarClasses.familyMark} />
        <span className={sidebarClasses.familyText}>
          <span className={sidebarClasses.familyName}>{name}</span>
          <span className={sidebarClasses.familyMeta}>Manage family</span>
        </span>
      </UnstyledButton>
    );
  }
  // One family: the row goes where the family is managed, named for what it is
  // — a "Switch family" row that switches nothing would be a dead end.
  return (
    <UnstyledButton
      component={Link}
      href="/dashboard/members"
      className={classes.sheetRow}
      data-testid="household-row"
      data-variant="row"
    >
      <IconUsers size={22} stroke={NAV_ICON_STROKE} aria-hidden />
      <span>Family members</span>
      <span>{name}</span>
    </UnstyledButton>
  );
}

// ─── The radiogroup of families ──────────────────────────────

function HouseholdList({
  list,
  currentId,
  switching,
  onChoose,
}: {
  list: MyHousehold[];
  currentId: string | null;
  switching: string | null;
  onChoose: (household: MyHousehold) => void;
}) {
  const rows = useRef<Array<HTMLButtonElement | null>>([]);

  if (list.length === 0) {
    return (
      <div aria-hidden>
        <div className={classes.skeleton} />
        <div className={classes.skeleton} />
      </div>
    );
  }

  // Roving tabindex, like a native radio group: arrows move between families.
  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const keys = ["ArrowDown", "ArrowRight", "ArrowUp", "ArrowLeft", "Home", "End"];
    if (!keys.includes(event.key)) return;
    event.preventDefault();
    const focused = rows.current.findIndex((el) => el === document.activeElement);
    const from = focused >= 0 ? focused : Math.max(0, list.findIndex((h) => h.id === currentId));
    let next = from;
    if (event.key === "ArrowDown" || event.key === "ArrowRight") next = (from + 1) % list.length;
    else if (event.key === "ArrowUp" || event.key === "ArrowLeft") next = (from - 1 + list.length) % list.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = list.length - 1;
    rows.current[next]?.focus();
  };

  return (
    <div
      role="radiogroup"
      aria-labelledby={PANEL_TITLE_ID}
      className={classes.list}
      onKeyDown={onKeyDown}
      data-testid="household-list"
    >
      {list.map((household, i) => {
        const selected = household.id === currentId;
        const busy = switching === household.id;
        return (
          <button
            key={household.id}
            ref={(el) => {
              rows.current[i] = el;
            }}
            type="button"
            role="radio"
            aria-checked={selected}
            aria-busy={busy || undefined}
            tabIndex={selected || (currentId === null && i === 0) ? 0 : -1}
            className={classes.row}
            data-household-id={household.id}
            data-household-role={household.role}
            onClick={() => onChoose(household)}
          >
            <FamilyMark name={household.name} className={classes.mark} />
            <span className={classes.text}>
              <span className={classes.name}>{household.name}</span>
              <span className={classes.meta}>{householdRoleSummary(household.role, selected)}</span>
            </span>
            {selected && <IconCheck size={20} stroke={2} className={classes.check} aria-hidden />}
            {selected && <VisuallyHidden> current household</VisuallyHidden>}
          </button>
        );
      })}
    </div>
  );
}
