"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";
import Modal from "@/components/ui/Modal";

interface Bill {
  id: string;
  name: string;
  amount: string;
  due_day: number | null;
  category: string | null;
  property_id: string | null;
  vendor_id: string | null;
  rrule: string | null;
  notes: string | null;
  created_at: string;
  updated_at: string;
}

interface BillsResponse {
  data: Bill[];
}

export default function BillsPage() {
  const queryClient = useQueryClient();
  const [showAdd, setShowAdd] = useState(false);

  // ── Form state ─────────────────────────────────────────────
  const [name, setName] = useState("");
  const [amount, setAmount] = useState("");
  const [dueDay, setDueDay] = useState("");
  const [category, setCategory] = useState("");
  const [rrule, setRrule] = useState("");
  const [notes, setNotes] = useState("");
  const [formError, setFormError] = useState("");

  // ── Query ──────────────────────────────────────────────────
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["bills"],
    queryFn: () => apiFetch<BillsResponse>("/api/v1/bills"),
    staleTime: 30_000,
  });

  const bills = data?.data ?? [];

  // ── Add mutation ───────────────────────────────────────────
  const addMutation = useMutation({
    mutationFn: () =>
      apiFetch<{ data: Bill }>("/api/v1/bills", {
        method: "POST",
        body: {
          name: name || null,
          amount: amount || null,
          due_day: dueDay ? parseInt(dueDay, 10) : null,
          category: category || null,
          rrule: rrule || null,
          notes: notes || null,
        },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["bills"] });
      setShowAdd(false);
      resetForm();
    },
    onError: (e: unknown) => {
      setFormError(e instanceof Error ? e.message : "Failed to add bill");
    },
  });

  function resetForm() {
    setName("");
    setAmount("");
    setDueDay("");
    setCategory("");
    setRrule("");
    setNotes("");
    setFormError("");
  }

  function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    setFormError("");
    if (!name.trim()) {
      setFormError("Name is required");
      return;
    }
    addMutation.mutate();
  }

  // ── Render ─────────────────────────────────────────────────
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <div className="sm:flex sm:items-center sm:justify-between">
        <h1 className="text-2xl font-semibold text-gray-900">Bills</h1>
        <div className="mt-3 sm:ml-4 sm:mt-0">
          <Button onClick={() => setShowAdd(true)}>Add Bill</Button>
        </div>
      </div>

      {/* Loading state */}
      {isLoading && (
        <div className="mt-6 animate-pulse space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="h-16 rounded-lg bg-gray-200" />
          ))}
        </div>
      )}

      {/* Error state */}
      {isError && (
        <div className="mt-6 rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">
            {error instanceof Error ? error.message : "Failed to load bills"}
          </p>
        </div>
      )}

      {/* Empty state */}
      {!isLoading && !isError && bills.length === 0 && (
        <div className="mt-12 text-center">
          <svg
            className="mx-auto h-12 w-12 text-gray-400"
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={1.5}
              d="M12 6v12m-3-2.818l.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
            />
          </svg>
          <h3 className="mt-2 text-sm font-semibold text-gray-900">No bills</h3>
          <p className="mt-1 text-sm text-gray-500">
            Add your first recurring bill to get started.
          </p>
          <div className="mt-6">
            <Button onClick={() => setShowAdd(true)}>Add Bill</Button>
          </div>
        </div>
      )}

      {/* Bill list */}
      {!isLoading && !isError && bills.length > 0 && (
        <div className="mt-6 overflow-hidden rounded-lg border border-gray-200">
          <table className="min-w-full divide-y divide-gray-300">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Name
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Amount
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Due Day
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Category
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200 bg-white">
              {bills.map((b) => (
                <tr key={b.id} className="hover:bg-gray-50">
                  <td className="whitespace-nowrap px-4 py-3 text-sm font-medium text-gray-900">
                    {b.name}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {b.amount ? `$${b.amount}` : "—"}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {b.due_day ?? "—"}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {b.category ?? "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Add Modal */}
      <Modal open={showAdd} onClose={() => { setShowAdd(false); resetForm(); }} title="Add Bill">
        <form onSubmit={handleAdd} className="space-y-4">
          <Input
            label="Name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Mortgage"
            required
          />
          <Input
            label="Amount"
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
            placeholder="e.g. 2500.00"
          />
          <Input
            label="Due Day"
            type="number"
            min="1"
            max="31"
            value={dueDay}
            onChange={(e) => setDueDay(e.target.value)}
            placeholder="e.g. 1"
          />
          <Input
            label="Category"
            value={category}
            onChange={(e) => setCategory(e.target.value)}
            placeholder="e.g. housing"
          />
          <Input
            label="Recurrence (RRULE)"
            value={rrule}
            onChange={(e) => setRrule(e.target.value)}
            placeholder="e.g. FREQ=MONTHLY;BYMONTHDAY=1"
          />
          <Input
            label="Notes"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            placeholder="Any notes about this bill"
          />
          {formError && (
            <p className="text-sm text-red-600">{formError}</p>
          )}
          <div className="flex justify-end space-x-3 pt-2">
            <button
              type="button"
              onClick={() => { setShowAdd(false); resetForm(); }}
              className="inline-flex items-center justify-center rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-semibold text-gray-700 hover:bg-gray-50"
            >
              Cancel
            </button>
            <Button type="submit" loading={addMutation.isPending}>
              Add Bill
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}
