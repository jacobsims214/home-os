"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Modal from "@/components/ui/Modal";
import Input from "@/components/ui/Input";
import Button from "@/components/ui/Button";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import type { PropertyDetailResponse } from "@/types/property";

interface AddPropertyModalProps {
  open: boolean;
  onClose: () => void;
}

export default function AddPropertyModal({
  open,
  onClose,
}: AddPropertyModalProps) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [error, setError] = useState("");

  const createMutation = useMutation({
    mutationFn: (body: { name: string; address: string }) =>
      apiFetch<PropertyDetailResponse>("/api/v1/properties", {
        method: "POST",
        body,
      }),
    onSuccess: () => {
      // Invalidate all property queries to refetch the list
      queryClient.invalidateQueries({ queryKey: propertyKeys.all });
      setName("");
      setAddress("");
      setError("");
      onClose();
    },
    onError: (err: Error) => {
      setError(err.message);
    },
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setError("");

    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("Property name is required.");
      return;
    }

    createMutation.mutate({ name: trimmedName, address: address.trim() });
  };

  const handleClose = () => {
    if (createMutation.isPending) return; // prevent closing while submitting
    setName("");
    setAddress("");
    setError("");
    onClose();
  };

  return (
    <Modal open={open} onClose={handleClose} title="Add Property">
      <form onSubmit={handleSubmit} className="space-y-4">
        <Input
          label="Property Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="e.g. Main House, Beach Cottage"
          required
        />

        <Input
          label="Address"
          value={address}
          onChange={(e) => setAddress(e.target.value)}
          placeholder="123 Main St, Springfield"
        />

        {error && (
          <p className="text-sm text-red-600" role="alert">
            {error}
          </p>
        )}

        <div className="flex justify-end gap-3 pt-2">
          <Button
            type="button"
            variant="primary"
            className="bg-gray-100 !text-gray-700 hover:bg-gray-200 focus-visible:outline-gray-400"
            onClick={handleClose}
            disabled={createMutation.isPending}
          >
            Cancel
          </Button>
          <Button type="submit" loading={createMutation.isPending}>
            Add Property
          </Button>
        </div>
      </form>
    </Modal>
  );
}
