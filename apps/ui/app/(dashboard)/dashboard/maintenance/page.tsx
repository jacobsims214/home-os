"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import {
  useDroppable,
  useDraggable,
} from "@dnd-kit/core";
import { CSS } from "@dnd-kit/utilities";
import { Card, Text, Group, Badge, Button, Loader, Stack } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import { IconPlus } from "@tabler/icons-react";
import type { Property } from "@/lib/types/api";
import AddMaintenanceTaskModal from "@/components/maintenance/AddMaintenanceTaskModal";

interface Task {
  id: string;
  name: string;
  description: string | null;
  status: string;
  due_date: string | null;
  property_id: string | null;
  asset_id: string | null;
  cost: string | null;
  notes: string | null;
}

const COLUMNS = [
  { id: "pending", title: "Pending", color: "yellow" },
  { id: "in_progress", title: "In Progress", color: "blue" },
  { id: "done", title: "Done", color: "green" },
  { id: "skipped", title: "Skipped", color: "gray" },
] as const;

function fmtDate(d: string | null): string {
  if (!d) return "No due date";
  try {
    return new Date(d).toLocaleDateString("en-US", { month: "short", day: "numeric" });
  } catch { return d; }
}

function isOverdue(d: string | null, status: string): boolean {
  if (!d || status === "done" || status === "skipped") return false;
  return new Date(d) < new Date();
}

// Draggable task card
function TaskCard({ task }: { task: Task }) {
  const { attributes, listeners, setNodeRef, transform, isDragging } = useDraggable({ id: task.id });
  const style = {
    transform: CSS.Translate.toString(transform),
    opacity: isDragging ? 0.5 : 1,
  };

  return (
    <Card
      ref={setNodeRef}
      style={style}
      {...attributes}
      {...listeners}
      padding="sm"
      radius="md"
      withBorder
      shadow="xs"
      mb="xs"
    >
      <Text size="sm" fw={600} mb={4}>{task.name}</Text>
      {task.description && (
        <Text size="xs" c="dimmed" lineClamp={2} mb={6}>{task.description}</Text>
      )}
      <Group gap="xs">
        <Badge size="xs" color={isOverdue(task.due_date, task.status) ? "red" : "gray"} variant="light">
          {fmtDate(task.due_date)}
        </Badge>
        {task.cost && (
          <Badge size="xs" variant="light">${task.cost}</Badge>
        )}
      </Group>
    </Card>
  );
}

// Droppable column
function Column({ column, tasks }: { column: typeof COLUMNS[number]; tasks: Task[] }) {
  const { setNodeRef, isOver } = useDroppable({ id: column.id });

  return (
    <div
      ref={setNodeRef}
      style={{
        flex: "1 1 0",
        minWidth: 250,
        maxWidth: 350,
        backgroundColor: isOver ? "var(--mantine-color-gray-1)" : "var(--mantine-color-gray-0)",
        borderRadius: "var(--mantine-radius-md)",
        padding: "12px",
        transition: "background-color 150ms ease",
      }}
    >
      <Group justify="space-between" mb="sm">
        <Text size="sm" fw={700} c={`${column.color}.6`}>{column.title}</Text>
        <Badge size="xs" variant="light" color={column.color}>{tasks.length}</Badge>
      </Group>
      <Stack gap={6}>
        {tasks.map((task) => (
          <TaskCard key={task.id} task={task} />
        ))}
        {tasks.length === 0 && (
          <Text size="xs" c="dimmed" ta="center" py="lg">Drop tasks here</Text>
        )}
      </Stack>
    </div>
  );
}

export default function MaintenancePage() {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [activeTask, setActiveTask] = useState<Task | null>(null);
  const [showAdd, setShowAdd] = useState(false);

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
  );

  const { data: propsData } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () => apiFetch<{ data: Property[] }>("/api/v1/properties"),
  });
  const properties = propsData?.data ?? [];
  const propertyMap = new Map(properties.map((p) => [p.id, p.name]));

  const { data: tasksData, isLoading } = useQuery({
    queryKey: ["maintenance", "tasks"],
    queryFn: () => apiFetch<{ data: Task[] }>("/api/v1/maintenance/tasks"),
  });
  const tasks = tasksData?.data ?? [];

  const updateStatus = useMutation({
    mutationFn: ({ id, status }: { id: string; status: string }) =>
      apiFetch(`/api/v1/maintenance/tasks/${id}`, { method: "PATCH", body: { status } }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["maintenance", "tasks"] });
      notifications.show({ message: "Task moved", color: "green" });
    },
    onError: () => {
      notifications.show({ message: "Failed to update task", color: "red" });
    },
  });

  function handleDragStart(e: DragStartEvent) {
    const task = tasks.find((t) => t.id === e.active.id);
    setActiveTask(task ?? null);
  }

  function handleDragEnd(e: DragEndEvent) {
    setActiveTask(null);
    if (!e.over) return;
    const taskId = e.active.id as string;
    const newStatus = e.over.id as string;
    const task = tasks.find((t) => t.id === taskId);
    if (!task || task.status === newStatus) return;
    updateStatus.mutate({ id: taskId, status: newStatus });
  }

  if (isLoading) {
    return (
      <div className="flex justify-center py-20">
        <Loader />
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-7xl px-4 py-6">
      {/* Header */}
      <Group justify="space-between" mb="lg">
        <Stack gap={0}>
          <Text size="xl" fw={700}>Maintenance</Text>
          <Text size="sm" c="dimmed">Drag tasks between columns to update status</Text>
        </Stack>
        <Button leftSection={<IconPlus size={16} />} onClick={() => setShowAdd(true)}>
          Add Task
        </Button>
      </Group>

      {/* Kanban board */}
      <DndContext
        sensors={sensors}
        onDragStart={handleDragStart}
        onDragEnd={handleDragEnd}
      >
        <div style={{ display: "flex", gap: 16, overflowX: "auto", paddingBottom: 16 }}>
          {COLUMNS.map((col) => (
            <Column
              key={col.id}
              column={col}
              tasks={tasks.filter((t) => t.status === col.id)}
            />
          ))}
        </div>
        <DragOverlay>
          {activeTask ? (
            <Card padding="sm" radius="md" withBorder shadow="md" style={{ maxWidth: 300, cursor: "grabbing" }}>
              <Text size="sm" fw={600}>{activeTask.name}</Text>
              <Badge size="xs" color="gray" variant="light" mt={4}>
                {fmtDate(activeTask.due_date)}
              </Badge>
            </Card>
          ) : null}
        </DragOverlay>
      </DndContext>

      {/* Add Task Modal */}
      <AddMaintenanceTaskModal
        open={showAdd}
        onClose={() => setShowAdd(false)}
      />
    </div>
  );
}
