"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";
import Modal from "@/components/ui/Modal";

interface Pet {
  id: string;
  name: string;
  species: string | null;
  breed: string | null;
  date_of_birth: string | null;
  vet_name: string | null;
  vet_phone: string | null;
  notes: string | null;
  created_at: string;
  updated_at: string;
}

interface PetsResponse {
  data: Pet[];
}

export default function PetsPage() {
  const queryClient = useQueryClient();
  const [showAdd, setShowAdd] = useState(false);

  // ── Form state ─────────────────────────────────────────────
  const [name, setName] = useState("");
  const [species, setSpecies] = useState("");
  const [breed, setBreed] = useState("");
  const [dateOfBirth, setDateOfBirth] = useState("");
  const [vetName, setVetName] = useState("");
  const [vetPhone, setVetPhone] = useState("");
  const [notes, setNotes] = useState("");
  const [formError, setFormError] = useState("");

  // ── Query ──────────────────────────────────────────────────
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["pets"],
    queryFn: () => apiFetch<PetsResponse>("/api/v1/pets"),
    staleTime: 30_000,
  });

  const pets = data?.data ?? [];

  // ── Add mutation ───────────────────────────────────────────
  const addMutation = useMutation({
    mutationFn: () =>
      apiFetch<{ data: Pet }>("/api/v1/pets", {
        method: "POST",
        body: {
          name: name || null,
          species: species || null,
          breed: breed || null,
          date_of_birth: dateOfBirth || null,
          vet_name: vetName || null,
          vet_phone: vetPhone || null,
          notes: notes || null,
        },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pets"] });
      setShowAdd(false);
      resetForm();
    },
    onError: (e: unknown) => {
      setFormError(e instanceof Error ? e.message : "Failed to add pet");
    },
  });

  function resetForm() {
    setName("");
    setSpecies("");
    setBreed("");
    setDateOfBirth("");
    setVetName("");
    setVetPhone("");
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
        <h1 className="text-2xl font-semibold text-gray-900">Pets</h1>
        <div className="mt-3 sm:ml-4 sm:mt-0">
          <Button onClick={() => setShowAdd(true)}>Add Pet</Button>
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
            {error instanceof Error ? error.message : "Failed to load pets"}
          </p>
        </div>
      )}

      {/* Empty state */}
      {!isLoading && !isError && pets.length === 0 && (
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
              d="M15.182 15.182a4.5 4.5 0 01-6.364 0M21 12a9 9 0 11-18 0 9 9 0 0118 0zM9.75 9.75c0 .414-.168.75-.375.75S9 10.164 9 9.75 9.168 9 9.375 9s.375.336.375.75zm-.375 0h.008v.015h-.008V9.75zm5.625 0c0 .414-.168.75-.375.75s-.375-.336-.375-.75.168-.75.375-.75.375.336.375.75zm-.375 0h.008v.015h-.008V9.75z"
            />
          </svg>
          <h3 className="mt-2 text-sm font-semibold text-gray-900">No pets</h3>
          <p className="mt-1 text-sm text-gray-500">
            Add your first pet to get started.
          </p>
          <div className="mt-6">
            <Button onClick={() => setShowAdd(true)}>Add Pet</Button>
          </div>
        </div>
      )}

      {/* Pet list */}
      {!isLoading && !isError && pets.length > 0 && (
        <div className="mt-6 overflow-hidden rounded-lg border border-gray-200">
          <table className="min-w-full divide-y divide-gray-300">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Name
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Species
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Breed
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Vet
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200 bg-white">
              {pets.map((p) => (
                <tr key={p.id} className="hover:bg-gray-50">
                  <td className="whitespace-nowrap px-4 py-3 text-sm font-medium text-gray-900">
                    {p.name}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {p.species ?? "—"}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {p.breed ?? "—"}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {p.vet_name ?? "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Add Modal */}
      <Modal open={showAdd} onClose={() => { setShowAdd(false); resetForm(); }} title="Add Pet">
        <form onSubmit={handleAdd} className="space-y-4">
          <Input
            label="Name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Max"
            required
          />
          <Input
            label="Species"
            value={species}
            onChange={(e) => setSpecies(e.target.value)}
            placeholder="e.g. Dog"
          />
          <Input
            label="Breed"
            value={breed}
            onChange={(e) => setBreed(e.target.value)}
            placeholder="e.g. Golden Retriever"
          />
          <Input
            label="Date of Birth"
            type="date"
            value={dateOfBirth}
            onChange={(e) => setDateOfBirth(e.target.value)}
          />
          <Input
            label="Vet Name"
            value={vetName}
            onChange={(e) => setVetName(e.target.value)}
            placeholder="e.g. Dr. Smith"
          />
          <Input
            label="Vet Phone"
            type="tel"
            value={vetPhone}
            onChange={(e) => setVetPhone(e.target.value)}
            placeholder="e.g. (555) 123-4567"
          />
          <Input
            label="Notes"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            placeholder="Any notes about this pet"
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
              Add Pet
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}
