"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";
import Modal from "@/components/ui/Modal";

interface Vehicle {
  id: string;
  year: number | null;
  make: string | null;
  model: string | null;
  vin: string | null;
  license_plate: string | null;
  color: string | null;
  notes: string | null;
  created_at: string;
  updated_at: string;
}

interface VehiclesResponse {
  data: Vehicle[];
}

export default function VehiclesPage() {
  const queryClient = useQueryClient();
  const [showAdd, setShowAdd] = useState(false);

  // ── Form state ─────────────────────────────────────────────
  const [year, setYear] = useState("");
  const [make, setMake] = useState("");
  const [model, setModel] = useState("");
  const [vin, setVin] = useState("");
  const [licensePlate, setLicensePlate] = useState("");
  const [color, setColor] = useState("");
  const [notes, setNotes] = useState("");
  const [formError, setFormError] = useState("");

  // ── Query ──────────────────────────────────────────────────
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["vehicles"],
    queryFn: () => apiFetch<VehiclesResponse>("/api/v1/vehicles"),
    staleTime: 30_000,
  });

  const vehicles = data?.data ?? [];

  // ── Add mutation ───────────────────────────────────────────
  const addMutation = useMutation({
    mutationFn: () =>
      apiFetch<{ data: Vehicle }>("/api/v1/vehicles", {
        method: "POST",
        body: {
          year: year ? parseInt(year, 10) : null,
          make: make || null,
          model: model || null,
          vin: vin || null,
          license_plate: licensePlate || null,
          color: color || null,
          notes: notes || null,
        },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["vehicles"] });
      setShowAdd(false);
      resetForm();
    },
    onError: (e: unknown) => {
      setFormError(e instanceof Error ? e.message : "Failed to add vehicle");
    },
  });

  function resetForm() {
    setYear("");
    setMake("");
    setModel("");
    setVin("");
    setLicensePlate("");
    setColor("");
    setNotes("");
    setFormError("");
  }

  function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    addMutation.mutate();
  }

  // ── Render ─────────────────────────────────────────────────
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <div className="sm:flex sm:items-center sm:justify-between">
        <h1 className="text-2xl font-semibold text-gray-900">Vehicles</h1>
        <div className="mt-3 sm:ml-4 sm:mt-0">
          <Button onClick={() => setShowAdd(true)}>Add Vehicle</Button>
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
            {error instanceof Error ? error.message : "Failed to load vehicles"}
          </p>
        </div>
      )}

      {/* Empty state */}
      {!isLoading && !isError && vehicles.length === 0 && (
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
              d="M8.25 18.75a1.5 1.5 0 01-3 0m3 0a1.5 1.5 0 00-3 0m3 0h6m-9 0H3.375a1.125 1.125 0 01-1.125-1.125V14.25m17.25 4.5a1.5 1.5 0 01-3 0m3 0a1.5 1.5 0 00-3 0m3 0h1.125c.621 0 1.129-.504 1.09-1.124a17.902 17.902 0 00-3.213-9.193 2.056 2.056 0 00-1.58-.86H14.25M16.5 18.75h-2.25m0-11.177v-.958c0-.568-.422-1.048-.987-1.106a48.554 48.554 0 00-10.026 0 1.106 1.106 0 00-.987 1.106v7.635m12-6.677v6.677m0 4.5v-4.5m0 0h-12"
            />
          </svg>
          <h3 className="mt-2 text-sm font-semibold text-gray-900">No vehicles</h3>
          <p className="mt-1 text-sm text-gray-500">
            Add your first vehicle to get started.
          </p>
          <div className="mt-6">
            <Button onClick={() => setShowAdd(true)}>Add Vehicle</Button>
          </div>
        </div>
      )}

      {/* Vehicle list */}
      {!isLoading && !isError && vehicles.length > 0 && (
        <div className="mt-6 overflow-hidden rounded-lg border border-gray-200">
          <table className="min-w-full divide-y divide-gray-300">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Year
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Make
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  Model
                </th>
                <th className="px-4 py-3 text-left text-sm font-semibold text-gray-900">
                  License Plate
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200 bg-white">
              {vehicles.map((v) => (
                <tr key={v.id} className="hover:bg-gray-50">
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {v.year ?? "—"}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-900">
                    {v.make ?? "—"}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-900">
                    {v.model ?? "—"}
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-sm text-gray-700">
                    {v.license_plate ?? "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Add Modal */}
      <Modal open={showAdd} onClose={() => { setShowAdd(false); resetForm(); }} title="Add Vehicle">
        <form onSubmit={handleAdd} className="space-y-4">
          <Input
            label="Year"
            type="number"
            value={year}
            onChange={(e) => setYear(e.target.value)}
            placeholder="e.g. 2023"
          />
          <Input
            label="Make"
            value={make}
            onChange={(e) => setMake(e.target.value)}
            placeholder="e.g. Toyota"
          />
          <Input
            label="Model"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder="e.g. Camry"
          />
          <Input
            label="VIN"
            value={vin}
            onChange={(e) => setVin(e.target.value)}
            placeholder="Vehicle identification number"
          />
          <Input
            label="License Plate"
            value={licensePlate}
            onChange={(e) => setLicensePlate(e.target.value)}
            placeholder="e.g. ABC-1234"
          />
          <Input
            label="Color"
            value={color}
            onChange={(e) => setColor(e.target.value)}
            placeholder="e.g. Silver"
          />
          <Input
            label="Notes"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            placeholder="Any notes about this vehicle"
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
            <Button
              type="submit"
              loading={addMutation.isPending}
            >
              Add Vehicle
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}
