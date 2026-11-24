"use client";

import { useState } from "react";
import Badge from "@/components/ui/Badge";
import type { MaintenanceTask } from "./types";

interface TaskCardProps {
  task: MaintenanceTask;
  onStatusChange: (taskId: string, newStatus: MaintenanceTask["status"]) => void;
}

const statusCycle: Record<
  MaintenanceTask["status"],
  MaintenanceTask["status"]
> = {
  pending: "in_progress",
  in_progress: "done",
  done: "pending",
  skipped: "pending",
};

function isOverdue(task: MaintenanceTask, today: string): boolean {
  if (!task.due_date) return false;
  if (task.status === "done" || task.status === "skipped") return false;
  // Civil dates compare as strings (#5316): no browser zone can move the day.
  return task.due_date.slice(0, 10) < today.slice(0, 10);
}

function formatDate(dateStr: string | null): string {
  if (!dateStr) return "";
  const d = new Date(dateStr);
  // UTC: a civil due date has no zone, so UTC cannot move it (#5316).
  return d.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone: "UTC",
  });
}

export default function TaskCard({
  task,
  today,
  onStatusChange,
}: TaskCardProps & { today: string }) {
  const [pending, setPending] = useState(false);

  const handleStatusClick = () => {
    if (pending) return;
    const nextStatus = statusCycle[task.status];
    setPending(true);
    onStatusChange(task.id, nextStatus);
    // Briefly show pending state before the parent clears it on next render
    setTimeout(() => setPending(false), 500);
  };

  const overdue = isOverdue(task, today);

  return (
    <div
      className={`rounded-lg border bg-white p-4 shadow-sm transition-colors ${
        overdue ? "border-red-300 bg-red-50" : "border-gray-200"
      }`}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <h3
            className={`text-sm font-semibold truncate ${
              overdue ? "text-red-700" : "text-gray-900"
            }`}
          >
            {task.name}
          </h3>
          {task.description && (
            <p className="mt-1 text-xs text-gray-500 line-clamp-2">
              {task.description}
            </p>
          )}
          <div className="mt-2 flex items-center gap-3 text-xs text-gray-500">
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
          color={task.status === "pending" ? "yellow" : task.status === "in_progress" ? "blue" : task.status === "done" ? "green" : "gray"}
          onClick={handleStatusClick}
          className="flex-shrink-0"
        />
      </div>
    </div>
  );
}
