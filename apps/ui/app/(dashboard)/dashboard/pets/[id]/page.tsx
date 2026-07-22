"use client";

import { useEffect, useState, useCallback, type FormEvent } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { useRecentStore } from "@/stores/recent";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";
import Modal from "@/components/ui/Modal";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import EntityResources from "@/components/EntityResources";

// ─── Pet type from Go model ───────────────────────────────────

interface Pet {
  id: string;
  household_id: string;
  name: string;
  species: string | null;
  breed: string | null;
  date_of_birth: string | null;
  vet_name: string | null;
  vet_phone: string | null;
  notes: string | null;
  microchip_id: string | null;
  insurance_provider: string | null;
  insurance_policy: string | null;
  registration_id: string | null;
  created_at: string;
  updated_at: string;
}

interface PetResponse {
  data: Pet;
}

// ─── Page component ──────────────────────────────────────────

export default function PetDetailPage() {
  const params = useParams();
  const id = params.id as string;
  const queryClient = useQueryClient();

  // ── Edit mode state ──────────────────────────────────────
  const [isEditing, setIsEditing] = useState(false);

  // ── Edit form fields ─────────────────────────────────────
  const [editName, setEditName] = useState("");
  const [editSpecies, setEditSpecies] = useState("");
  const [editBreed, setEditBreed] = useState("");
  const [editDateOfBirth, setEditDateOfBirth] = useState("");
  const [editVetName, setEditVetName] = useState("");
  const [editVetPhone, setEditVetPhone] = useState("");
  const [editNotes, setEditNotes] = useState("");
  const [editMicrochipId, setEditMicrochipId] = useState("");
  const [editInsProvider, setEditInsProvider] = useState("");
  const [editInsPolicy, setEditInsPolicy] = useState("");
  const [editRegId, setEditRegId] = useState("");
  const [editError, setEditError] = useState("");

  // ── Delete state ─────────────────────────────────────────
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);

  // ── Fetch pet ────────────────────────────────────────────
  const {
    data: petResp,
    isLoading,
    isError,
    error,
  } = useQuery({
    queryKey: ["pet", id],
    queryFn: () => apiFetch<PetResponse>(`/api/v1/pets/${id}`),
    enabled: !!id,
  });

  const pet = petResp?.data;

  // ── Record visit in recent store ──────────────────────────
  useEffect(() => {
    if (pet) {
      useRecentStore.getState().addItem({
        entity_type: "pet",
        entity_id: pet.id,
        title: pet.name,
      });
    }
  }, [pet]);

  // ── Edit mutation ────────────────────────────────────────
  const editMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<PetResponse>(`/api/v1/pets/${id}`, { method: "PUT", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pet", id] });
      queryClient.invalidateQueries({ queryKey: ["pets"] });
      setIsEditing(false);
      setEditError("");
    },
    onError: (err: Error) => {
      setEditError(err.message);
    },
  });

  // ── Delete mutation ──────────────────────────────────────
  const deleteMutation = useMutation({
    mutationFn: () => apiFetch(`/api/v1/pets/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pets"] });
      setShowDeleteConfirm(false);
      window.location.href = "/dashboard/pets";
    },
    onError: (e: unknown) => {
      console.error("Failed to delete pet", e);
    },
  });

  // ── Enter edit mode ──────────────────────────────────────
  const startEditing = useCallback(() => {
    if (!pet) return;
    setEditName(pet.name);
    setEditSpecies(pet.species ?? "");
    setEditBreed(pet.breed ?? "");
    setEditDateOfBirth(pet.date_of_birth ?? "");
    setEditVetName(pet.vet_name ?? "");
    setEditVetPhone(pet.vet_phone ?? "");
    setEditNotes(pet.notes ?? "");
    setEditMicrochipId(pet.microchip_id ?? "");
    setEditInsProvider(pet.insurance_provider ?? "");
    setEditInsPolicy(pet.insurance_policy ?? "");
    setEditRegId(pet.registration_id ?? "");
    setEditError("");
    setIsEditing(true);
  }, [pet]);

  // ── Save edit ────────────────────────────────────────────
  const handleSave = (e: FormEvent) => {
    e.preventDefault();
    setEditError("");

    const trimmedName = editName.trim();
    if (!trimmedName) {
      setEditError("Name is required");
      return;
    }

    const body: Record<string, unknown> = { name: trimmedName };
    if (editSpecies.trim()) body.species = editSpecies.trim();
    if (editBreed.trim()) body.breed = editBreed.trim();
    if (editDateOfBirth) body.date_of_birth = editDateOfBirth;
    if (editVetName.trim()) body.vet_name = editVetName.trim();
    if (editVetPhone.trim()) body.vet_phone = editVetPhone.trim();
    if (editNotes.trim()) body.notes = editNotes.trim();
    if (editMicrochipId.trim()) body.microchip_id = editMicrochipId.trim();
    if (editInsProvider.trim()) body.insurance_provider = editInsProvider.trim();
    if (editInsPolicy.trim()) body.insurance_policy = editInsPolicy.trim();
    if (editRegId.trim()) body.registration_id = editRegId.trim();

    editMutation.mutate(body);
  };

  // ── Cancel edit ──────────────────────────────────────────
  const cancelEditing = () => {
    setIsEditing(false);
    setEditError("");
  };

  // ── Loading state ────────────────────────────────────────
  if (isLoading) {
    return (
      <div className="p-6">
        <div className="mb-6">
          <div className="h-5 w-16 animate-pulse rounded bg-gray-200" />
        </div>
        <div className="overflow-hidden rounded-lg border border-gray-200 bg-white">
          <div className="p-6 space-y-4">
            {[1, 2, 3, 4, 5].map((i) => (
              <div key={i}>
                <div className="h-3 w-20 animate-pulse rounded bg-gray-100" />
                <div className="mt-1 h-5 w-48 animate-pulse rounded bg-gray-200" />
              </div>
            ))}
          </div>
        </div>
      </div>
    );
  }

  // ── Error state ──────────────────────────────────────────
  if (isError) {
    const message =
      error instanceof ApiError
        ? error.status === 404
          ? "Pet not found"
          : error.message
        : "Failed to load pet";

    return (
      <div className="flex flex-col items-center justify-center p-12">
        <div className="rounded-lg bg-red-50 p-6 text-center">
          <p className="text-red-700 font-medium">{message}</p>
          <Link href="/dashboard/pets">
            <Button className="mt-4">Back to Pets</Button>
          </Link>
        </div>
      </div>
    );
  }

  if (!pet) {
    return (
      <div className="p-6">
        <Link
          href="/dashboard/pets"
          className="text-sm text-indigo-600 hover:text-indigo-500"
        >
          &larr; Back to Pets
        </Link>
        <p className="mt-6 text-gray-500">Pet data unavailable.</p>
      </div>
    );
  }

  return (
    <div className="p-6">
      {/* Back navigation */}
      <div className="mb-6 flex items-center justify-between">
        <Link
          href="/dashboard/pets"
          className="inline-flex items-center text-sm text-indigo-600 hover:text-indigo-500"
        >
          <svg className="mr-1 h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" d="M10.5 19.5L3 12m0 0l7.5-7.5M3 12h18" />
          </svg>
          Back to Pets
        </Link>

        {/* Action buttons */}
        <div className="flex gap-2">
          {!isEditing ? (
            <>
              <button
                onClick={startEditing}
                className="inline-flex items-center rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
              >
                <svg className="mr-1.5 h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" d="M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931zm0 0L19.5 7.125M18 14v4.75A2.25 2.25 0 0115.75 21H5.25A2.25 2.25 0 013 18.75V8.25A2.25 2.25 0 015.25 6H10" />
                </svg>
                Edit
              </button>
              <button
                onClick={() => setShowDeleteConfirm(true)}
                className="inline-flex items-center rounded-md border border-red-300 bg-white px-3 py-1.5 text-sm font-medium text-red-700 hover:bg-red-50 transition-colors"
              >
                <svg className="mr-1.5 h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" d="M14.74 9l-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 01-2.244 2.077H8.084a2.25 2.25 0 01-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 00-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 013.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 00-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 00-7.5 0" />
                </svg>
                Delete
              </button>
            </>
          ) : null}
        </div>
      </div>

      {/* Pet detail card — view mode */}
      {!isEditing && (
        <div className="overflow-hidden rounded-lg border border-gray-200 bg-white">
          {/* Header */}
          <div className="border-b border-gray-200 px-6 py-4">
            <h1 className="text-xl font-bold text-gray-900">{pet.name}</h1>
            {pet.species && (
              <span className="mt-1 inline-flex items-center rounded-full bg-indigo-50 px-2.5 py-0.5 text-xs font-medium text-indigo-700">
                {pet.species}
                {pet.breed && <> — {pet.breed}</>}
              </span>
            )}
          </div>

          {/* Fields */}
          <div className="px-6 py-4">
            <dl className="grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-2">
              <Field label="Name" value={pet.name} />
              <Field label="Species" value={pet.species ?? undefined} />
              <Field label="Breed" value={pet.breed ?? undefined} />
              <Field label="Date of Birth" value={pet.date_of_birth ?? undefined} />
              <Field label="Vet Name" value={pet.vet_name ?? undefined} />
              <Field label="Vet Phone" value={pet.vet_phone ?? undefined} />
              <Field label="Microchip ID" value={pet.microchip_id ?? undefined} />
              <Field label="Insurance Provider" value={pet.insurance_provider ?? undefined} />
              <Field label="Insurance Policy #" value={pet.insurance_policy ?? undefined} />
              <Field label="Registration ID" value={pet.registration_id ?? undefined} />
            </dl>

            {pet.notes && (
              <div className="mt-6 border-t border-gray-100 pt-4">
                <dt className="text-xs font-medium text-gray-500">Notes</dt>
                <dd className="mt-1 text-sm text-gray-900 whitespace-pre-wrap">
                  {pet.notes}
                </dd>
              </div>
            )}

            <dl className="grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-2 mt-6 border-t border-gray-100 pt-4">
              <Field label="Created" value={formatDate(pet.created_at)} />
              <Field label="Last Updated" value={formatDate(pet.updated_at)} />
            </dl>
          </div>
        </div>
      )}

      {/* Inline edit form — edit mode */}
      {isEditing && (
        <form
          onSubmit={handleSave}
          className="overflow-hidden rounded-lg border border-indigo-200 bg-white"
        >
          <div className="border-b border-indigo-100 bg-indigo-50 px-6 py-3">
            <h2 className="text-base font-semibold text-indigo-900">Editing: {pet.name}</h2>
          </div>
          <div className="px-6 py-4 space-y-4">
            {editError && (
              <div className="rounded-md bg-red-50 p-3 text-sm text-red-700">{editError}</div>
            )}

            <Input
              label="Name"
              value={editName}
              onChange={(e) => setEditName(e.target.value)}
              placeholder="e.g. Max"
              required
            />

            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Species"
                value={editSpecies}
                onChange={(e) => setEditSpecies(e.target.value)}
                placeholder="e.g. Dog"
              />
              <Input
                label="Breed"
                value={editBreed}
                onChange={(e) => setEditBreed(e.target.value)}
                placeholder="e.g. Golden Retriever"
              />
            </div>

            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Date of Birth"
                type="date"
                value={editDateOfBirth}
                onChange={(e) => setEditDateOfBirth(e.target.value)}
              />
              <Input
                label="Microchip ID"
                value={editMicrochipId}
                onChange={(e) => setEditMicrochipId(e.target.value)}
                placeholder="e.g. 985112001234567"
              />
            </div>

            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Vet Name"
                value={editVetName}
                onChange={(e) => setEditVetName(e.target.value)}
                placeholder="e.g. Dr. Smith"
              />
              <Input
                label="Vet Phone"
                type="tel"
                value={editVetPhone}
                onChange={(e) => setEditVetPhone(e.target.value)}
                placeholder="e.g. (555) 123-4567"
              />
            </div>

            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Insurance Provider"
                value={editInsProvider}
                onChange={(e) => setEditInsProvider(e.target.value)}
                placeholder="e.g. Healthy Paws"
              />
              <Input
                label="Insurance Policy #"
                value={editInsPolicy}
                onChange={(e) => setEditInsPolicy(e.target.value)}
                placeholder="e.g. POL-12345"
              />
            </div>

            <Input
              label="Registration ID"
              value={editRegId}
              onChange={(e) => setEditRegId(e.target.value)}
              placeholder="e.g. City license number"
            />

            <Input
              label="Notes"
              value={editNotes}
              onChange={(e) => setEditNotes(e.target.value)}
              placeholder="Any notes about this pet"
            />

            <div className="flex justify-end gap-3 pt-2">
              <button
                type="button"
                onClick={cancelEditing}
                disabled={editMutation.isPending}
                className="inline-flex items-center justify-center rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-semibold text-gray-700 hover:bg-gray-50 disabled:bg-gray-100"
              >
                Cancel
              </button>
              <Button type="submit" loading={editMutation.isPending}>
                Save Changes
              </Button>
            </div>
          </div>
        </form>
      )}

      {/* Resource sections: Files / Notes / Passwords */}
      <EntityResources entityType="pet" entityId={id} />

      {/* Delete confirmation */}
      <ConfirmDialog
        open={showDeleteConfirm}
        onClose={() => setShowDeleteConfirm(false)}
        onConfirm={() => deleteMutation.mutate()}
        title="Delete Pet"
        message={`Are you sure you want to delete ${pet.name}? This action cannot be undone.`}
        loading={deleteMutation.isPending}
      />
    </div>
  );
}

// ─── Helper components ────────────────────────────────────────

/** Renders a single detail field (label + value or placeholder). */
function Field({ label, value }: { label: string; value?: string }) {
  return (
    <div>
      <dt className="text-xs font-medium text-gray-500">{label}</dt>
      <dd className="mt-1 text-sm text-gray-900">
        {value ?? <span className="text-gray-400">—</span>}
      </dd>
    </div>
  );
}

/** Formats an ISO timestamp into a human-readable date. */
function formatDate(iso: string): string {
  try {
    return new Date(iso).toLocaleDateString("en-US", {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  } catch {
    return iso;
  }
}