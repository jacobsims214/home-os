"use client";

import { useState, useRef, useEffect } from "react";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";
import Modal from "@/components/ui/Modal";
import { apiFetch, ApiError } from "@/lib/api";

interface AddTaskModalProps {
  open: boolean;
  onClose: () => void;
  /** Called after successful creation so the parent can refetch. */
  onCreated: () => void;
}

export default function AddTaskModal({
  open,
  onClose,
  onCreated,
}: AddTaskModalProps) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [dueDate, setDueDate] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const nameRef = useRef<HTMLInputElement>(null);

  // Focus the name input when the modal opens
  useEffect(() => {
    if (open) {
      // Small delay to allow the dialog transition
      const timer = setTimeout(() => nameRef.current?.focus(), 100);
      return () => clearTimeout(timer);
    }
  }, [open]);

  // Reset form when modal opens/closes
  useEffect(() => {
    if (!open) {
      setName("");
      setDescription("");
      setDueDate("");
      setError("");
      setLoading(false);
    }
  }, [open]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");

    if (!name.trim()) {
      setError("Task name is required.");
      return;
    }

    setLoading(true);
    try {
      await apiFetch("/api/v1/maintenance/tasks", {
        method: "POST",
        body: {
          name: name.trim(),
          description: description.trim() || undefined,
          due_date: dueDate || undefined,
        },
      });
      onCreated();
      onClose();
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError("Failed to create task. Please try again.");
      }
    } finally {
      setLoading(false);
    }
  };

  return (
    <Modal opened={open} onClose={onClose} title="Add Task">
      {error && (
        <div className="mb-4 rounded-md bg-red-50 border border-red-200 px-3 py-2 text-sm text-red-700">
          {error}
        </div>
      )}

      <form onSubmit={handleSubmit} className="space-y-4">
        <div>
          <label
            htmlFor="task-name"
            className="block text-sm font-medium text-gray-900"
          >
            Name *
          </label>
          <input
            ref={nameRef}
            id="task-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Change HVAC filter"
            className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:outline-none focus:ring-2 focus:ring-inset focus:ring-indigo-600"
          />
        </div>

        <div>
          <label
            htmlFor="task-description"
            className="block text-sm font-medium text-gray-900"
          >
            Description
          </label>
          <textarea
            id="task-description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            rows={2}
            placeholder="Optional notes about the task"
            className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:outline-none focus:ring-2 focus:ring-inset focus:ring-indigo-600 resize-none"
          />
        </div>

        <div>
          <label
            htmlFor="task-due-date"
            className="block text-sm font-medium text-gray-900"
          >
            Due Date
          </label>
          <input
            id="task-due-date"
            type="date"
            value={dueDate}
            onChange={(e) => setDueDate(e.target.value)}
            className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:outline-none focus:ring-2 focus:ring-inset focus:ring-indigo-600"
          />
        </div>

        <div className="flex justify-end gap-3 pt-2">
          <button
            type="button"
            onClick={onClose}
            disabled={loading}
            className="rounded-md px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-100 transition-colors"
          >
            Cancel
          </button>
          <Button type="submit" loading={loading}>
            Create Task
          </Button>
        </div>
      </form>
    </Modal>
  );
}
