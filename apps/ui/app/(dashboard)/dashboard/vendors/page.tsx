"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";
import Modal from "@/components/ui/Modal";

interface Vendor {
  id: string;
  name: string;
  property_id: string | null;
  specialty: string | null;
  phone: string | null;
  email: string | null;
  website: string | null;
  notes: string | null;
  created_at: string;
  updated_at: string;
}

interface VendorsResponse {
  data: Vendor[];
}

export default function VendorsPage() {
  const queryClient = useQueryClient();
  const [showAdd, setShowAdd] = useState(false);

  // ── Form state ─────────────────────────────────────────────
  const [name, setName] = useState("");
  const [specialty, setSpecialty] = useState("");
  const [phone, setPhone] = useState("");
  const [email, setEmail] = useState("");
  const [website, setWebsite] = useState("");
  const [notes, setNotes] = useState("");
  const [formError, setFormError] = useState("");

  // ── Query ──────────────────────────────────────────────────
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["vendors"],
    queryFn: () => apiFetch<VendorsResponse>("/api/v1/vendors"),
    staleTime: 30_000,
  });

  const vendors = data?.data ?? [];

  // ── Add mutation ───────────────────────────────────────────
  const addMutation = useMutation({
    mutationFn: () =>
      apiFetch<{ data: Vendor }>("/api/v1/vendors", {
        method: "POST",
        body: {
          name: name || null,
          specialty: specialty || null,
          phone: phone || null,
          email: email || null,
          website: website || null,
          notes: notes || null,
        },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["vendors"] });
      setShowAdd(false);
      resetForm();
    },
    onError: (e: unknown) => {
      setFormError(e instanceof Error ? e.message : "Failed to add vendor");
    },
  });

  function resetForm() {
    setName("");
    setSpecialty("");
    setPhone("");
    setEmail("");
    setWebsite("");
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
        <h1 className="text-2xl font-semibold text-gray-900">Vendors</h1>
        <div className="mt-3 sm:ml-4 sm:mt-0">
          <Button onClick={() => setShowAdd(true)}>Add Vendor</Button>
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
            {error instanceof Error ? error.message : "Failed to load vendors"}
          </p>
        </div>
      )}

      {/* Empty state */}
      {!isLoading && !isError && vendors.length === 0 && (
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
              d="M15.75 6a3.75 3.75 0 11-7.5 0 3.75 3.75 0 017.5 0zM4.501 20.118a7.5 7.5 0 0114.998 0A17.933 17.933 0 0112 21.75c-2.676 0-5.216-.584-7.499-1.632z"
            />
          </svg>
          <h3 className="mt-2 text-sm font-semibold text-gray-900">No vendors</h3>
          <p className="mt-1 text-sm text-gray-500">
            Add your first vendor to get started.
          </p>
          <div className="mt-6">
            <Button onClick={() => setShowAdd(true)}>Add Vendor</Button>
          </div>
        </div>
      )}

      {/* Vendor list */}
      {!isLoading && !isError && vendors.length > 0 && (
        <div className="mt-6 overflow-hidden rounded-lg border border-gray-200">
          <table className="min-w-full divide-y divide-gray-300">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Name
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Specialty
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Phone
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Email
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200 bg-white">
              {vendors.map((v) => (
                <tr key={v.id} className="hover:bg-gray-50">
                  <td className="whitespace-nowrap px-4 py-3 text-sm font-medium text-gray-900">
                    {v.name}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {v.specialty ?? "—"}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {v.phone ?? "—"}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {v.email ?? "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Add Modal */}
      <Modal open={showAdd} onClose={() => { setShowAdd(false); resetForm(); }} title="Add Vendor">
        <form onSubmit={handleAdd} className="space-y-4">
          <Input
            label="Name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. ABC Plumbing"
            required
          />
          <Input
            label="Specialty"
            value={specialty}
            onChange={(e) => setSpecialty(e.target.value)}
            placeholder="e.g. Plumbing"
          />
          <Input
            label="Phone"
            type="tel"
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
            placeholder="e.g. (555) 123-4567"
          />
          <Input
            label="Email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="e.g. info@abcplumbing.com"
          />
          <Input
            label="Website"
            type="url"
            value={website}
            onChange={(e) => setWebsite(e.target.value)}
            placeholder="e.g. https://abcplumbing.com"
          />
          <Input
            label="Notes"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            placeholder="Any notes about this vendor"
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
              Add Vendor
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}
