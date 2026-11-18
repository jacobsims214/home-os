"use client";

/**
 * Upkeep / maintenance (#4558), built to the approved people & house mockup
 * (artifact #89 v1, the "Upkeep" screen) inside the approved shell (#75 v1).
 *
 * WHAT THIS REPLACED, AND WHY
 *
 * The old board was a drag-only kanban. On a phone one of three 320px columns
 * was visible and the only way to complete a task was to drag it (audit #74;
 * manager note on #4754). Its column headers were bg-orange-500 / blue-500 /
 * green-500 with white text — 2.80, 3.67 and 2.27:1, a failing axe
 * colour-contrast check.
 *
 * Now:
 *  - PHONES (< 768px) get the mockup's grouped list: Overdue / This week /
 *    Next 30 days / Later / No due date, then a collapsible "Done". A 44px
 *    checkbox completes a task (PATCH status=done, completed_at=now); the row
 *    menu has Start, Snooze a week and Skip.
 *  - 768px AND UP keep the kanban the ticket asks for, with a TouchSensor
 *    (200ms delay) and a PointerSensor (8px distance), so a tap scrolls and a
 *    held press drags. Every status a drag can produce is also a button.
 *  - Column headers use a token surface and Mantine light variants, which
 *    #4754 made AA-safe (architecture/theme-tokens.md). No white on a
 *    saturated 500 anywhere.
 *
 * MAINTENANCE IS NOT A MONEY SURFACE: nothing here formats, hides or redacts
 * money, and `cost` is not rendered. Costs are Money's job (see the note at
 * the foot of the page).
 */

import { useMemo, useState } from "react";
import {
  closestCorners,
  DndContext,
  KeyboardSensor,
  PointerSensor,
  TouchSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragOverEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Badge, Button, Card, Menu, Skeleton, Text, Title } from "@mantine/core";
import {
  IconAlertTriangle,
  IconBox,
  IconCar,
  IconCheck,
  IconChevronDown,
  IconClock,
  IconDots,
  IconHome,
  IconPlayerPlay,
  IconRefresh,
  IconX,
} from "@tabler/icons-react";
import { notifications } from "@mantine/notifications";
import { apiFetch } from "@/lib/api";
import { assetKeys, maintenanceKeys, propertyKeys, vehicleKeys } from "@/lib/query-keys";
import { parseCivil } from "@/lib/today/due";
import { useHouseholdToday } from "@/lib/today/api";
import type { MaintenanceTask } from "./types";

const ICON_STROKE = 1.75;

// ── data ────────────────────────────────────────────────────

type Status = MaintenanceTask["status"];

async function fetchMaintenanceTasks(): Promise<MaintenanceTask[]> {
  const res = await apiFetch<{ data: MaintenanceTask[] }>("/api/v1/maintenance/tasks");
  return res.data;
}

interface PatchBody {
  status?: Status;
  /** RFC3339 instant, sent with status=done (the API also sets it). */
  completed_at?: string | null;
  /** RFC3339 date; snooze moves it a week on. */
  due_date?: string;
}

async function patchTask(taskId: string, body: PatchBody): Promise<MaintenanceTask> {
  const res = await apiFetch<{ data: MaintenanceTask }>(`/api/v1/maintenance/tasks/${taskId}`, {
    method: "PATCH",
    body,
  });
  return res.data;
}

/**
 * The mockup's subject line names the thing, its room and the house. The list
 * API returns ids, so the names come from the same reads the rest of the app
 * makes; anything unresolved is left out rather than shown as a raw uuid.
 * These reads are decorative: their failure only shortens the second line.
 */
interface SubjectNames {
  properties: Map<string, string>;
  assets: Map<string, string>;
  vehicles: Map<string, string>;
}

function useSubjectNames(enabled: boolean): SubjectNames {
  const properties = useQuery({
    queryKey: propertyKeys.lists(),
    queryFn: () => apiFetch<{ data: Array<{ id: string; name: string }> }>("/api/v1/properties"),
    enabled,
    retry: false,
    staleTime: 5 * 60_000,
  });
  const assets = useQuery({
    queryKey: assetKeys.lists(),
    queryFn: () => apiFetch<{ data: Array<{ id: string; name: string }> }>("/api/v1/assets"),
    enabled,
    retry: false,
    staleTime: 5 * 60_000,
  });
  const vehicles = useQuery({
    queryKey: vehicleKeys.lists(),
    queryFn: () =>
      apiFetch<{ data: Array<{ id: string; year: number | null; make: string | null; model: string | null }> }>(
        "/api/v1/vehicles",
      ),
    enabled,
    retry: false,
    staleTime: 5 * 60_000,
  });

  return useMemo(() => {
    const toMap = (rows?: Array<{ id: string; name: string }>) => new Map((rows ?? []).map((r) => [r.id, r.name]));
    const vehiclesMap = new Map<string, string>();
    for (const v of vehicles.data?.data ?? []) {
      const label = [v.year, v.make, v.model].filter(Boolean).join(" ");
      if (label) vehiclesMap.set(v.id, label);
    }
    return {
      properties: toMap(properties.data?.data),
      assets: toMap(assets.data?.data),
      vehicles: vehiclesMap,
    };
  }, [properties.data, assets.data, vehicles.data]);
}

type Subject = { parts: string[]; kind: "asset" | "vehicle" | "property" | "none" };

function subjectOf(task: MaintenanceTask, names: SubjectNames): Subject {
  const vehicle = task.vehicle_id ? names.vehicles.get(task.vehicle_id) : undefined;
  if (vehicle) return { parts: [vehicle], kind: "vehicle" };
  const asset = task.asset_id ? names.assets.get(task.asset_id) : undefined;
  if (asset) {
    const house = task.property_id ? names.properties.get(task.property_id) : undefined;
    return { parts: house ? [asset, house] : [asset], kind: "asset" };
  }
  const house = task.property_id ? names.properties.get(task.property_id) : undefined;
  if (house) return { parts: [house], kind: "property" };
  return { parts: [], kind: "none" };
}

// ── due grouping (the mockup's Upkeep groups) ───────────────

type GroupKey = "overdue" | "week" | "month30" | "later" | "nodate";

const GROUP_TITLES: Record<GroupKey, string> = {
  overdue: "Overdue",
  week: "This week",
  month30: "Next 30 days",
  later: "Later",
  nodate: "No due date",
};

/** The kanban column a task belongs in. A skipped task sits in To do. */
function columnOf(task: MaintenanceTask): "pending" | "in_progress" | "done" {
  if (task.status === "in_progress") return "in_progress";
  if (task.status === "done") return "done";
  return "pending";
}

const OPEN_GROUPS: GroupKey[] = ["overdue", "week", "month30", "later", "nodate"];

/**
 * Whole calendar days from `today` to `date` (negative = overdue).
 *
 * Both are CIVIL dates (YYYY-MM-DD), in the FAMILY's day (#5316). This used to
 * take a `Date` defaulting to `new Date()`, which compared the browser's clock
 * against the household's due date: a task due today read as "1 day overdue"
 * for anyone west of UTC after 19:00. `parseCivil` turns each into a UTC day
 * number, so no zone or DST change can move the difference by a day.
 */
export function daysUntil(date: string, today: string): number {
  const a = parseCivil(date);
  const b = parseCivil(today);
  if (a === null || b === null) return Number.NaN;
  return Math.round(a - b);
}

export function dueGroup(task: MaintenanceTask, today: string): GroupKey {
  // A skipped task was deliberately not done, so a past due date is not
  // overdue: it keeps the bucket its date falls in and never counts as overdue.
  if (task.status === "skipped") return "nodate";
  if (!task.due_date) return "nodate";
  const days = daysUntil(task.due_date, today);
  if (Number.isNaN(days)) return "nodate";
  if (days < 0) return "overdue";
  if (days <= 7) return "week";
  if (days <= 30) return "month30";
  return "later";
}

/** "<weekday>, <Mon D>" — the shape the rest of Home OS shows dates in. */
export function formatDue(dateStr: string | null): string {
  if (!dateStr) return "";
  const d = new Date(dateStr);
  if (Number.isNaN(d.getTime())) return "";
  // UTC: due dates are civil dates (no zone of their own), so reading them
  // in UTC is the reading that cannot move one by a day (#5316).
  return d.toLocaleDateString("en-US", {
    weekday: "short",
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  });
}

/**
 * "5 days overdue" / "Due Fri, Nov 20" — a word, never colour alone.
 *
 * A DONE TASK IS NEVER OVERDUE. Its due_date is in the past by definition once
 * it is finished, so "9 days overdue" on a completed card reads as a warning
 * about work that is already done (the same mistake as a paid bill reading as
 * due). A done task says when it was finished instead.
 */
export function dueLine(task: MaintenanceTask, today: string): string {
  if (task.status === "done") {
    return task.completed_at ? `Done ${formatDue(task.completed_at)}` : "Done";
  }
  if (task.status === "skipped") {
    // Deliberately not done: neither overdue nor due. The row's Skipped badge
    // carries the status; this line only says when it had been due.
    return task.due_date ? `Was due ${formatDue(task.due_date)}` : "No due date";
  }
  if (!task.due_date) return "No due date";
  const days = daysUntil(task.due_date, today);
  if (Number.isNaN(days)) return "No due date";
  if (days < 0) return days === -1 ? "1 day overdue" : `${Math.abs(days)} days overdue`;
  if (days === 0) return "Due today";
  if (days === 1) return "Due tomorrow";
  return `Due ${formatDue(task.due_date)}`;
}

// ── the row (phones) ────────────────────────────────────────

function TaskRow({
  task,
  names,
  busy,
  canWrite,
  canEdit,
  today,
  onStatus,
  onSnooze,
  first,
}: {
  task: MaintenanceTask;
  names: SubjectNames;
  busy: boolean;
  canWrite: boolean;
  canEdit: boolean;
  today: string;
  onStatus: (status: Status) => void;
  onSnooze: () => void;
  first: boolean;
}) {
  const group = dueGroup(task, today);
  const subject = subjectOf(task, names);
  const done = task.status === "done";

  return (
    <div
      data-task-row={task.id}
      data-task-status={task.status}
      className={["flex min-h-[68px] items-center gap-3 py-2.5 pl-3.5 pr-1", first ? "" : "border-t border-line"].join(
        " ",
      )}
    >
      <button
        type="button"
        role="checkbox"
        aria-checked={done}
        aria-label={done ? `Undo completion: ${task.name}` : `Mark done: ${task.name}`}
        disabled={!canWrite || busy}
        onClick={() => onStatus(done ? "pending" : "done")}
        className="grid h-11 w-11 flex-none place-items-center rounded-token-sm disabled:opacity-50"
      >
        <span
          aria-hidden
          className={[
            "grid h-[22px] w-[22px] place-items-center rounded-md border-2",
            done ? "border-accent-fill bg-accent-fill text-white" : "border-ink-3 bg-surface",
          ].join(" ")}
        >
          {done && <IconCheck size={14} stroke={3} />}
        </span>
      </button>

      <div className="min-w-0 flex-1">
        <b
          className={[
            "block text-[15px] font-semibold leading-snug",
            done ? "font-medium text-ink-3 line-through" : "text-ink",
          ].join(" ")}
        >
          {task.name}
        </b>
        {subject.parts.length > 0 && (
          <span className="flex items-center gap-1 text-[13px] leading-snug text-ink-3">
            {subject.kind === "vehicle" ? (
              <IconCar size={14} stroke={ICON_STROKE} aria-hidden />
            ) : subject.kind === "asset" ? (
              <IconBox size={14} stroke={ICON_STROKE} aria-hidden />
            ) : (
              <IconHome size={14} stroke={ICON_STROKE} aria-hidden />
            )}
            <span className="truncate">{subject.parts.join(" · ")}</span>
          </span>
        )}
        {done ? (
          task.completed_at && (
            <span className="block text-[13px] leading-snug text-ink-3">Done {formatDue(task.completed_at)}</span>
          )
        ) : (
          <span className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1">
            <span className={["text-[13px] font-medium", group === "overdue" ? "text-danger" : "text-ink-2"].join(" ")}>
              {dueLine(task, today)}
            </span>
            {/* Status is a word, never just a dot or a colour. */}
            {task.status === "in_progress" && (
              <span className="inline-flex items-center gap-1 rounded-full bg-accent-soft px-2 py-0.5 text-[12px] font-semibold text-accent">
                <IconPlayerPlay size={12} stroke={ICON_STROKE} aria-hidden />
                In progress
              </span>
            )}
            {task.status === "skipped" && (
              <span className="inline-flex items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-[12px] font-semibold text-ink-2">
                <IconX size={12} stroke={ICON_STROKE} aria-hidden />
                Skipped
              </span>
            )}
          </span>
        )}
      </div>

      {canWrite && (
        <Menu position="bottom-end" withinPortal>
          <Menu.Target>
            <button
              type="button"
              aria-label={`More for ${task.name}`}
              className="grid h-11 w-11 flex-none place-items-center rounded-token-sm text-ink-2 hover:bg-surface-2"
            >
              <IconDots size={18} stroke={ICON_STROKE} aria-hidden />
            </button>
          </Menu.Target>
          <Menu.Dropdown>
            {!done && task.status !== "in_progress" && (
              <Menu.Item leftSection={<IconPlayerPlay size={16} stroke={ICON_STROKE} />} onClick={() => onStatus("in_progress")}>
                Start
              </Menu.Item>
            )}
            {!done && canEdit && (
              <Menu.Item leftSection={<IconClock size={16} stroke={ICON_STROKE} />} onClick={onSnooze}>
                Snooze a week
              </Menu.Item>
            )}
            {!done && task.status !== "skipped" && (
              <Menu.Item leftSection={<IconX size={16} stroke={ICON_STROKE} />} onClick={() => onStatus("skipped")}>
                Skip
              </Menu.Item>
            )}
            {done && (
              <Menu.Item leftSection={<IconRefresh size={16} stroke={ICON_STROKE} />} onClick={() => onStatus("pending")}>
                Move back to to-do
              </Menu.Item>
            )}
          </Menu.Dropdown>
        </Menu>
      )}
    </div>
  );
}

// ── the card (kanban, 768px and up) ─────────────────────────

function KanbanCard({
  task,
  names,
  canWrite,
  today,
  onStatus,
}: {
  task: MaintenanceTask;
  names: SubjectNames;
  canWrite: boolean;
  today: string;
  onStatus: (status: Status) => void;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: task.id });
  const subject = subjectOf(task, names);
  const overdue = task.status !== "done" && task.status !== "skipped" && dueGroup(task, today) === "overdue";

  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.5 : 1 }}
      className={["rounded-token-md border bg-surface p-3", overdue ? "border-warn-line" : "border-line"].join(" ")}
      data-kanban-card={task.id}
    >
      {/* Only this block drags: a tap on the buttons must never start a drag. */}
      <div {...attributes} {...listeners} className="cursor-grab touch-none active:cursor-grabbing">
        <b className="block text-sm font-semibold leading-snug text-ink">{task.name}</b>
        {subject.parts.length > 0 && (
          <span className="mt-0.5 block truncate text-[12px] text-ink-3">{subject.parts.join(" · ")}</span>
        )}
        <span className={["mt-1 block text-[12px] font-medium", overdue ? "text-danger" : "text-ink-2"].join(" ")}>
          {dueLine(task, today)}
        </span>
      </div>
      {canWrite && task.status !== "done" && (
        <div className="mt-2 flex gap-1.5">
          <Button
            size="compact-sm"
            variant="default"
            onClick={() => onStatus("done")}
            leftSection={<IconCheck size={14} stroke={2} aria-hidden />}
          >
            Done
          </Button>
          {task.status !== "in_progress" && (
            <Button
              size="compact-sm"
              variant="subtle"
              onClick={() => onStatus("in_progress")}
              leftSection={<IconPlayerPlay size={14} stroke={ICON_STROKE} aria-hidden />}
            >
              Start
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * Column headers: a Badge in a light Mantine variant on a --surface-2 column.
 * #4754 made the light variants AA-safe; the old white-on-orange/blue/green
 * headers (2.80 / 3.67 / 2.27:1) are gone.
 */
const COLUMNS: ReadonlyArray<{ id: "pending" | "in_progress" | "done"; title: string; color: string }> = [
  { id: "pending", title: "To do", color: "orange" },
  { id: "in_progress", title: "In progress", color: "blue" },
  { id: "done", title: "Done", color: "green" },
];

// ── the page ────────────────────────────────────────────────

export interface MaintenanceBoardProps {
  /**
   * The role may change a task's status (roles.go ResMaintenance
   * ActUpdateStatus: the family roles and a vendor).
   */
  canChangeStatus?: boolean;
  /**
   * The role may change a task's dates and details (ActWrite: family only), so
   * a vendor gets no Snooze — that PATCHes due_date and the API answers 403.
   */
  canEdit?: boolean;
}

export default function MaintenanceKanban({ canChangeStatus = true, canEdit = true }: MaintenanceBoardProps) {
  const queryClient = useQueryClient();
  const [showDone, setShowDone] = useState(false);

  // One flag for "this role may change status at all", which is what the
  // checkbox, the row menu and the card buttons key off. Snooze needs canEdit.
  const write = canChangeStatus;

  const { data: tasks = [], isLoading, error, refetch } = useQuery({
    queryKey: maintenanceKeys.list("all"),
    queryFn: fetchMaintenanceTasks,
  });
  const names = useSubjectNames(!isLoading);
  // The FAMILY's day (#5316). Null until the household is known, and until
  // then not one row claims a date is overdue: `today` falls back to "" which
  // makes every day comparison unreadable, so `dueGroup`/`dueLine` say nothing
  // rather than something wrong. The load is one round trip the shell makes.
  const today = useHouseholdToday() ?? "";

  const mutation = useMutation({
    mutationFn: ({ taskId, body }: { taskId: string; body: PatchBody }) => patchTask(taskId, body),
    onMutate: async ({ taskId, body }) => {
      await queryClient.cancelQueries({ queryKey: maintenanceKeys.all });
      const previous = queryClient.getQueryData<MaintenanceTask[]>(maintenanceKeys.list("all"));
      if (previous) {
        queryClient.setQueryData<MaintenanceTask[]>(
          maintenanceKeys.list("all"),
          previous.map((t) => (t.id === taskId ? { ...t, ...body } : t)),
        );
      }
      return { previous };
    },
    onError: (err, _vars, context) => {
      if (context?.previous) queryClient.setQueryData(maintenanceKeys.list("all"), context.previous);
      // The API refused: say so rather than leaving an optimistic lie on screen.
      notifications.show({ message: err instanceof Error ? err.message : "That didn't save", color: "red" });
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: maintenanceKeys.all }),
  });

  const setStatus = (taskId: string, status: Status) =>
    mutation.mutate({
      taskId,
      body: { status, completed_at: status === "done" ? new Date().toISOString() : null },
    });

  const snooze = (task: MaintenanceTask) => {
    if (!canEdit) return;
    // Snooze is +7 CIVIL days from the task's due date, or from the family's
    // today (#5316). A Date is used only to do the 7-day addition, and it is
    // anchored at T00:00:00 of the civil date so no browser zone can shift it.
    const base = new Date(`${(task.due_date ?? today).slice(0, 10)}T00:00:00`);
    if (Number.isNaN(base.getTime())) return;
    base.setDate(base.getDate() + 7);
    mutation.mutate({ taskId: task.id, body: { due_date: base.toISOString() } });
  };

  const sensors = useSensors(
    // A tap must scroll a phone's page; only a press held ~200ms starts a drag.
    useSensor(TouchSensor, { activationConstraint: { delay: 200, tolerance: 8 } }),
    useSensor(PointerSensor, { activationConstraint: { distance: 8 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const byStatus = useMemo(
    () => ({
      pending: tasks.filter((t) => t.status === "pending" || t.status === "skipped"),
      in_progress: tasks.filter((t) => t.status === "in_progress"),
      done: tasks.filter((t) => t.status === "done"),
    }),
    [tasks],
  );

  const openGroups = useMemo(() => {
    const groups: Record<GroupKey, MaintenanceTask[]> = { overdue: [], week: [], month30: [], later: [], nodate: [] };
    for (const t of tasks) {
      if (t.status === "done" || t.status === "skipped") continue;
      groups[dueGroup(t, today)].push(t);
    }
    for (const g of OPEN_GROUPS) {
      groups[g].sort((a, b) => {
        // Undated tasks sort after dated ones by name; the rest by due date.
        if (!a.due_date && !b.due_date) return a.name.localeCompare(b.name);
        if (!a.due_date) return -1;
        if (!b.due_date) return 1;
        return new Date(a.due_date).getTime() - new Date(b.due_date).getTime();
      });
    }
    return groups;
  }, [tasks]);

  const doneTasks = byStatus.done;
  const skippedTasks = useMemo(() => tasks.filter((t) => t.status === "skipped"), [tasks]);

  /**
   * A card hovered over another column changes its status as it passes, which
   * is what makes a 200ms-delayed touch drag feel alive. `over.id` is a column
   * id when the pointer is over empty column space and a task id when it is
   * over a card, so both are resolved here.
   */
  const handleDragOver = (event: DragOverEvent) => {
    const { active, over } = event;
    if (!over) return;
    const overId = String(over.id);
    const dragged = tasks.find((t) => t.id === String(active.id));
    if (!dragged) return;
    const overTask = tasks.find((t) => t.id === overId);
    const nextStatus = COLUMNS.find((c) => c.id === overId)?.id ?? (overTask ? columnOf(overTask) : undefined);
    if (!nextStatus) return;
    if (nextStatus !== columnOf(dragged)) setStatus(dragged.id, nextStatus);
  };

  const handleDragEnd = (_event: DragEndEvent) => {
    // The status was written on drag-over; refetch settles the cache.
    if (write) queryClient.invalidateQueries({ queryKey: maintenanceKeys.all });
  };

  if (isLoading) {
    return (
      <div className="mx-auto w-full max-w-3xl" aria-busy="true" aria-label="Loading maintenance tasks">
        <Skeleton height={28} width={140} radius="sm" mb="md" />
        <Skeleton height={220} radius="md" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="mx-auto w-full max-w-3xl">
        <div
          className="flex items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-3.5 text-warn-ink"
          role="alert"
        >
          <IconAlertTriangle size={20} stroke={ICON_STROKE} className="mt-0.5 shrink-0" aria-hidden />
          <div>
            <b className="block font-semibold">Maintenance didn&apos;t load</b>
            <p className="mt-0.5 text-[13px] leading-snug">
              Home OS couldn&apos;t reach the server. Nothing was lost: your tasks are saved. Check your connection and
              try again.
            </p>
            <Button
              variant="default"
              size="sm"
              mt="xs"
              className="max-md:min-h-11"
              leftSection={<IconRefresh size={16} stroke={ICON_STROKE} aria-hidden />}
              onClick={() => refetch()}
            >
              Try again
            </Button>
          </div>
        </div>
      </div>
    );
  }

  const openCount = tasks.filter((t) => t.status !== "done" && t.status !== "skipped").length;
  const overdueCount = openGroups.overdue.length;

  return (
    <div className="mx-auto w-full" data-testid="maintenance-page">
      <div className="mb-4 flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <Title order={2} className="text-xl font-semibold leading-tight md:text-2xl">
            Maintenance
          </Title>
          <Text size="sm" c="dimmed" className="mt-0.5">
            {tasks.length === 0
              ? "No tasks yet"
              : openCount === 0
                ? doneTasks.length === tasks.length
                  ? "Everything is done"
                  : "Nothing left to do"
                : `${openCount} to do${overdueCount > 0 ? ` · ${overdueCount} overdue` : ""}`}
          </Text>
        </div>
        {!write && (
          <Badge variant="light" color="gray" className="shrink-0">
            View only
          </Badge>
        )}
      </div>

      {tasks.length === 0 ? (
        <div className="rounded-token-md border border-line bg-surface p-5" data-testid="upkeep-empty">
          <Title order={3} className="text-lg font-semibold">
            No maintenance tasks yet
          </Title>
          <p className="mt-1 max-w-prose text-sm text-ink-2">
            Maintenance is the house&apos;s to-do list: replace the furnace filter, test the smoke detectors, winterize the
            cabin. Add a task on a house, a room or a thing and it collects here with its next due date.
          </p>
        </div>
      ) : (
        <>
          {/* ── Phones: the mockup's grouped list ───────────────────────── */}
          <div className="md:hidden" data-testid="upkeep-list">
            {OPEN_GROUPS.map((group) => {
              const rows = openGroups[group];
              if (rows.length === 0) return null;
              const headingId = `upkeep-${group}`;
              return (
                <section key={group} className="mb-4" aria-labelledby={headingId}>
                  <h3
                    id={headingId}
                    className={[
                      "mb-1.5 flex items-center gap-1.5 px-1 text-[12px] font-semibold uppercase tracking-[.06em]",
                      group === "overdue" ? "text-danger" : "text-ink-3",
                    ].join(" ")}
                  >
                    {group === "overdue" && <IconAlertTriangle size={14} stroke={2} aria-hidden />}
                    {GROUP_TITLES[group]} · {rows.length}
                  </h3>
                  <Card withBorder radius="md" padding={0} className="overflow-hidden">
                    {rows.map((task, i) => (
                      <TaskRow
                        key={task.id}
                        task={task}
                        names={names}
                        busy={mutation.isPending}
                        canWrite={write}
                        canEdit={canEdit}
                        today={today}
                        onStatus={(s) => setStatus(task.id, s)}
                        onSnooze={() => snooze(task)}
                        first={i === 0}
                      />
                    ))}
                  </Card>
                </section>
              );
            })}

            {skippedTasks.length > 0 && (
              <section className="mb-4" aria-labelledby="upkeep-skipped">
                <h3
                  id="upkeep-skipped"
                  className="mb-1.5 flex items-center gap-1.5 px-1 text-[12px] font-semibold uppercase tracking-[.06em] text-ink-3"
                >
                  <IconX size={14} stroke={2} aria-hidden />
                  Skipped · {skippedTasks.length}
                </h3>
                <Card withBorder radius="md" padding={0} className="overflow-hidden">
                  {skippedTasks.map((task, i) => (
                    <TaskRow
                      key={task.id}
                      task={task}
                      names={names}
                      busy={mutation.isPending}
                      canWrite={write}
                      canEdit={canEdit}
                      today={today}
                      onStatus={(s) => setStatus(task.id, s)}
                      onSnooze={() => snooze(task)}
                      first={i === 0}
                    />
                  ))}
                </Card>
              </section>
            )}

            {doneTasks.length > 0 && (
              <section className="mb-4" aria-labelledby="upkeep-done">
                <h3
                  id="upkeep-done"
                  className="mb-1.5 flex items-center gap-1.5 px-1 text-[12px] font-semibold uppercase tracking-[.06em] text-ink-3"
                >
                  <IconCheck size={14} stroke={2} aria-hidden />
                  Done · {doneTasks.length}
                </h3>
                <Card withBorder radius="md" padding={0} className="overflow-hidden">
                  {(showDone ? doneTasks : doneTasks.slice(0, 3)).map((task, i) => (
                    <TaskRow
                      key={task.id}
                      task={task}
                      names={names}
                      busy={mutation.isPending}
                      canWrite={write}
                      canEdit={canEdit}
                      today={today}
                      onStatus={(s) => setStatus(task.id, s)}
                      onSnooze={() => snooze(task)}
                      first={i === 0}
                    />
                  ))}
                  {doneTasks.length > 3 && (
                    <button
                      type="button"
                      onClick={() => setShowDone((v) => !v)}
                      className="flex h-11 w-full items-center justify-center gap-1 border-t border-line text-[13px] font-semibold text-accent"
                    >
                      {showDone ? "Show fewer" : `Show ${doneTasks.length - 3} more`}
                      <IconChevronDown
                        size={16}
                        stroke={ICON_STROKE}
                        aria-hidden
                        className={showDone ? "rotate-180" : ""}
                      />
                    </button>
                  )}
                </Card>
              </section>
            )}
          </div>

          {/* ── 768px and up: the kanban, with a touch sensor and a delay ─ */}
          <div className="hidden md:block" data-testid="upkeep-kanban">
            <p className="mb-2 text-[13px] text-ink-3">
              Drag a card to change its status, or use its buttons. Every change saves straight away.
            </p>
            <DndContext
              sensors={sensors}
              collisionDetection={closestCorners}
              onDragOver={handleDragOver}
              onDragEnd={handleDragEnd}
            >
              <div className="flex gap-4">
                {COLUMNS.map((col) => {
                  const cards = byStatus[col.id];
                  return (
                    <section
                      key={col.id}
                      className="flex min-w-0 flex-1 flex-col rounded-token-md bg-surface-2 p-3"
                      aria-labelledby={`col-${col.id}`}
                    >
                      <div className="mb-2 flex items-center justify-between gap-2">
                        <h3 id={`col-${col.id}`} className="text-sm font-semibold text-ink">
                          {col.title}
                        </h3>
                        <Badge variant="light" color={col.color} size="sm">
                          {cards.length}
                        </Badge>
                      </div>
                      <div className="flex-1 space-y-2" id={col.id}>
                        <SortableContext items={cards.map((t) => t.id)} strategy={verticalListSortingStrategy}>
                          {cards.map((task) => (
                            <KanbanCard
                              key={task.id}
                              task={task}
                              names={names}
                              canWrite={write}
                              today={today}
                              onStatus={(s) => setStatus(task.id, s)}
                            />
                          ))}
                        </SortableContext>
                        {cards.length === 0 && (
                          <p className="rounded-token-sm border border-dashed border-line p-3 text-center text-[13px] text-ink-3">
                            Nothing here
                          </p>
                        )}
                      </div>
                    </section>
                  );
                })}
              </div>
            </DndContext>
          </div>

          <p className="mt-4 text-[13px] text-ink-3">
            Costs live in Money, not here. Completing a task marks it done today; the calendar event moves with it.
          </p>
        </>
      )}
    </div>
  );
}
