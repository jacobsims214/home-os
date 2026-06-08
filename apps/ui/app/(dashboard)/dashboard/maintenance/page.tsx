"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { maintenanceKeys } from "@/lib/query-keys";
import Button from "@/components/ui/Button";
import TaskCard from "@/components/maintenance/TaskCard";
import AddTaskModal from "@/components/maintenance/AddTaskModal";
import type { MaintenanceTask } from "@/components/maintenance/types";

/** All status groups we display, in display order. */
const STATUS_GROUPS = [
  { status: "pending" as const, label: "Pending", emptyMsg: "No pending tasks. 🎉" },
  { status: "in_progress" as const, label: "In Progress", emptyMsg: "Nothing in progress." },
  { status: "done" as const, label: "Done", emptyMsg: "No completed tasks yet." },
] as const;

export default function MaintenancePage() {
  const queryClient = useQueryClient();
  const [showAddModal, setShowAddModal] = useState(false);

  // Fetch all maintenance tasks for the household
  const {
    data: tasks,
    isLoading,
    isError,
    error,
  } = useQuery({
    queryKey: maintenanceKeys.all,
    queryFn: () => apiFetch<{data: MaintenanceTask[]}>("/api/v1/maintenance/tasks").then(r => r.data),
  });

  // Mutation to update a task's status (optimistic)
  const statusMutation = useMutation({
    mutationFn: ({
      taskId,
      status,
    }: {
      taskId: string;
      status: MaintenanceTask["status"];
    }) =>
      apiFetch<MaintenanceTask>(`/api/v1/maintenance/tasks/${taskId}`, {
        method: "PATCH",
        body: { status },
      }),
    onMutate: async ({ taskId, status }) => {
      // Cancel any outgoing refetches so they don't overwrite the optimistic update
      await queryClient.cancelQueries({ queryKey: maintenanceKeys.all });

      // Snapshot previous tasks for rollback
      const previous = queryClient.getQueryData<MaintenanceTask[]>(
        maintenanceKeys.all,
      );

      // Optimistically update the cache
      queryClient.setQueryData<MaintenanceTask[]>(
        maintenanceKeys.all,
        (old) =>
          old?.map((t) =>
            t.id === taskId ? { ...t, status, updated_at: new Date().toISOString() } : t,
          ) ?? [],
      );

      return { previous };
    },
    onError: (_err, _vars, context) => {
      // Rollback to previous state on error
      if (context?.previous) {
        queryClient.setQueryData(maintenanceKeys.all, context.previous);
      }
    },
    onSettled: () => {
      // Always refetch to ensure consistency with server state
      queryClient.invalidateQueries({ queryKey: maintenanceKeys.all });
    },
  });

  const handleStatusChange = (taskId: string, newStatus: MaintenanceTask["status"]) => {
    statusMutation.mutate({ taskId, status: newStatus });
  };

  // Group tasks by status
  const tasksByStatus = (status: MaintenanceTask["status"]) =>
    tasks?.filter((t) => t.status === status) ?? [];

  return (
    <div className="mx-auto max-w-4xl px-4 py-6 sm:px-6 lg:px-8">
      {/* Page header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Maintenance</h1>
          <p className="mt-1 text-sm text-gray-500">
            Track and manage home maintenance tasks.
          </p>
        </div>
        <Button onClick={() => setShowAddModal(true)}>
          <svg
            className="-ml-1 mr-1.5 h-4 w-4"
            fill="none"
            viewBox="0 0 24 24"
            strokeWidth={2}
            stroke="currentColor"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M12 4.5v15m7.5-7.5h-15"
            />
          </svg>
          Add Task
        </Button>
      </div>

      {/* Loading state */}
      {isLoading && (
        <div className="space-y-4">
          {[...Array(3)].map((_, i) => (
            <div
              key={i}
              className="rounded-lg border border-gray-200 bg-white p-4 animate-pulse"
            >
              <div className="h-4 w-3/4 rounded bg-gray-200 mb-2" />
              <div className="h-3 w-1/2 rounded bg-gray-100" />
            </div>
          ))}
        </div>
      )}

      {/* Error state */}
      {isError && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">
            Failed to load tasks.{" "}
            {error instanceof Error ? error.message : "Please try again."}
          </p>
        </div>
      )}

      {/* Task lists grouped by status */}
      {tasks && (
        <div className="space-y-8">
          {STATUS_GROUPS.map(({ status, label, emptyMsg }) => {
            const groupTasks = tasksByStatus(status);
            return (
              <section key={status}>
                <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-gray-500">
                  {label} ({groupTasks.length})
                </h2>
                {groupTasks.length === 0 ? (
                  <p className="rounded-lg border border-dashed border-gray-300 bg-gray-50 px-4 py-6 text-center text-sm text-gray-500">
                    {emptyMsg}
                  </p>
                ) : (
                  <div className="grid gap-3 sm:grid-cols-2">
                    {groupTasks.map((task) => (
                      <TaskCard
                        key={task.id}
                        task={task}
                        onStatusChange={handleStatusChange}
                      />
                    ))}
                  </div>
                )}
              </section>
            );
          })}
        </div>
      )}

      {/* Add task modal */}
      <AddTaskModal
        open={showAddModal}
        onClose={() => setShowAddModal(false)}
        onCreated={() => {
          queryClient.invalidateQueries({ queryKey: maintenanceKeys.all });
        }}
      />
    </div>
  );
}
