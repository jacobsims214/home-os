"use client";

import { useState } from "react";
import { closestCorners, DndContext, KeyboardSensor, PointerSensor, useSensor, useSensors, DragEndEvent, DragOverEvent, DragStartEvent } from "@dnd-kit/core";
import { sortableKeyboardCoordinates, SortableContext, verticalListSortingStrategy, useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import type { MaintenanceTask } from "./types";
import { maintenanceKeys } from "@/lib/query-keys";
import Badge from "@/components/ui/Badge";


// Status column definitions with colors
interface StatusColumn {
  id: "pending" | "in_progress" | "done";
  title: string;
  color: "orange" | "blue" | "green";
}

const statusColumns: StatusColumn[] = [
  { id: "pending", title: "Pending", color: "orange" },
  { id: "in_progress", title: "In Progress", color: "blue" },
  { id: "done", title: "Done", color: "green" },
];

// Fetch all maintenance tasks
async function fetchMaintenanceTasks(): Promise<MaintenanceTask[]> {
  const res = await apiFetch<{ data: MaintenanceTask[] }>("/api/v1/maintenance/tasks");
  return res.data;
}

// Update task status
async function updateTaskStatus(taskId: string, status: MaintenanceTask["status"]) {
  const res = await apiFetch<{ data: MaintenanceTask }>(`/api/v1/maintenance/tasks/${taskId}`, {
    method: "PATCH",
    body: { status },
  });
  return res.data;
}

// Format date for display
function formatDate(dateStr: string | null): string {
  if (!dateStr) return "";
  const d = new Date(dateStr);
  return d.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}

// Check if task is overdue
function isOverdue(task: MaintenanceTask): boolean {
  if (!task.due_date) return false;
  if (task.status === "done" || task.status === "skipped") return false;
  return new Date(task.due_date) < new Date();
}

// Task Card Component
function TaskCard({ task }: { task: MaintenanceTask }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: task.id });
  const style = { transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.5 : 1 };
  const overdue = isOverdue(task);

  return (
    <div ref={setNodeRef} style={style} {...attributes} {...listeners} className={`rounded-lg border bg-white p-4 shadow-sm transition-colors ${overdue ? "border-red-300 bg-red-50" : "border-gray-200"}`}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <h3 className={`text-sm font-semibold truncate ${overdue ? "text-red-700" : "text-gray-900"}`}>
            {task.name}
          </h3>
          {task.description && (
            <p className="mt-1 text-xs text-gray-500 line-clamp-2">
              {task.description}
            </p>
          )}
          <div className="mt-2 flex flex-wrap items-center gap-3 text-xs text-gray-500">
            {task.due_date && (
              <span className={overdue ? "font-medium text-red-600" : ""}>
                {overdue ? "Overdue: " : "Due: "}
                {formatDate(task.due_date)}
              </span>
            )}
            {task.cost && (
              <span className="font-mono">${task.cost}</span>
            )}
          </div>
        </div>
        <Badge
          color={
            task.status === "pending" ? "yellow" : 
            task.status === "in_progress" ? "blue" : 
            task.status === "done" ? "green" : "gray"
          }
          className="flex-shrink-0"
        />
      </div>
    </div>
  );
}

// Column Component
function Column({ status, tasks }: { status: StatusColumn; tasks: MaintenanceTask[] }) {
  return (
    <div className="flex w-80 flex-col rounded-xl bg-gray-100/50 p-3">
      <div className={`mb-3 rounded-lg px-3 py-2 text-sm font-semibold text-white ${status.color === "orange" ? "bg-orange-500" : status.color === "blue" ? "bg-blue-500" : "bg-green-500"}`}>
        {status.title} <span className="ml-1 text-xs opacity-75">({tasks.length})</span>
      </div>
      <div className="flex-1 space-y-3">
        <SortableContext items={tasks.map(t => t.id)} strategy={verticalListSortingStrategy}>
          {tasks.map((task) => (
            <TaskCard key={task.id} task={task} />
          ))}
        </SortableContext>
      </div>
    </div>
  );
}

// Main Kanban Component
export default function MaintenanceKanban() {
  const queryClient = useQueryClient();
  const [activeId, setActiveId] = useState<string | null>(null);

  // Dnd-kit sensors
  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
  );

  // Fetch all maintenance tasks
  const { data: tasks = [], isLoading, error } = useQuery({
    queryKey: maintenanceKeys.list("all"),
    queryFn: fetchMaintenanceTasks,
  });

  // Mutation to update task status
  const mutation = useMutation({
    mutationFn: ({ taskId, status }: { taskId: string; status: MaintenanceTask["status"] }) =>
      updateTaskStatus(taskId, status),
    onMutate: async ({ taskId, status }) => {
      // Cancel any outgoing refetches
      await queryClient.cancelQueries({ queryKey: maintenanceKeys.all });

      // Snapshot the previous value
      const previousTasks = queryClient.getQueryData<MaintenanceTask[]>(maintenanceKeys.list("all"));

      // Optimistically update the cache
      if (previousTasks) {
        queryClient.setQueryData<MaintenanceTask[]>(maintenanceKeys.list("all"), 
          previousTasks.map((t) => 
            t.id === taskId ? { ...t, status, updated_at: new Date().toISOString() } : t
          )
        );
      }

      return { previousTasks };
    },
    onError: (_err, _variables, context) => {
      // Rollback to previous state
      if (context?.previousTasks) {
        queryClient.setQueryData<MaintenanceTask[]>(maintenanceKeys.list("all"), context.previousTasks);
      }
    },
    onSettled: () => {
      // Refetch to ensure consistency
      queryClient.invalidateQueries({ queryKey: maintenanceKeys.all });
    },
  });

  // Group tasks by status
  const tasksByStatus = {
    pending: tasks.filter((t) => t.status === "pending" || t.status === "skipped"),
    in_progress: tasks.filter((t) => t.status === "in_progress"),
    done: tasks.filter((t) => t.status === "done"),
  };

  // Handle drag start
  const handleDragStart = (event: DragStartEvent) => {
    setActiveId(event.active.id as string);
  };

  // Handle drag over
  const handleDragOver = (event: DragOverEvent) => {
    const { active, over } = event;
    if (!over) return;

    const activeId = active.id as string;
    const overId = over.id as string;

    // Don't allow dropping on self
    if (activeId === overId) return;

    // Get the current status of the active task
    const activeTask = tasks.find((t) => t.id === activeId);
    if (!activeTask) return;

    // Determine the new status based on the column ID
    let newStatus: MaintenanceTask["status"] | null = null;
    if (overId.startsWith("pending")) newStatus = "pending";
    else if (overId.startsWith("in_progress")) newStatus = "in_progress";
    else if (overId.startsWith("done")) newStatus = "done";

    if (newStatus && newStatus !== activeTask.status) {
      mutation.mutate({ taskId: activeId, status: newStatus });
    }
  };

  // Handle drag end
  const handleDragEnd = (event: DragEndEvent) => {
    setActiveId(null);
  };

  if (isLoading) {
    return (
      <div className="flex h-64 items-center justify-center">
        <div className="text-gray-500">Loading maintenance tasks...</div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex h-64 items-center justify-center">
        <div className="text-red-500">Error loading maintenance tasks</div>
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-screen bg-gray-50 p-6">
      <div className="flex w-full gap-6 overflow-x-auto">
        <DndContext sensors={sensors} collisionDetection={closestCorners} onDragStart={handleDragStart} onDragOver={handleDragOver} onDragEnd={handleDragEnd}>
          {statusColumns.map((status) => (
            <div key={status.id} id={status.id} className="flex flex-1 flex-col">
              <Column status={status} tasks={tasksByStatus[status.id]} />
            </div>
          ))}
        </DndContext>
      </div>
    </div>
  );
}
