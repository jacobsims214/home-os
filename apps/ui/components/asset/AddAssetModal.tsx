"use client";

import { useState, type FormEvent } from "react";
import Modal from "@/components/ui/Modal";
import Input from "@/components/ui/Input";
import Select from "@/components/ui/Select";
import Button from "@/components/ui/Button";
import type { Property, CreateAssetRequest } from "@/lib/types/api";

interface AddAssetModalProps {
  open: boolean;
  onClose: () => void;
  onSubmit: (data: CreateAssetRequest) => Promise<void>;
  properties: Property[];
}

/** Common asset categories for the dropdown */
const ASSET_CATEGORIES = [
  { value: "", label: "Select category" },
  { value: "HVAC", label: "HVAC" },
  { value: "Appliance", label: "Appliance" },
  { value: "Electronics", label: "Electronics" },
  { value: "Furniture", label: "Furniture" },
  { value: "Plumbing", label: "Plumbing" },
  { value: "Lighting", label: "Lighting" },
  { value: "Landscaping", label: "Landscaping" },
  { value: "Security", label: "Security" },
  { value: "Other", label: "Other" },
];

export default function AddAssetModal({
  open,
  onClose,
  onSubmit,
  properties,
}: AddAssetModalProps) {
  const [name, setName] = useState("");
  const [category, setCategory] = useState("");
  const [propertyId, setPropertyId] = useState("");
  const [roomId, setRoomId] = useState("");
  const [serialNumber, setSerialNumber] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const resetForm = () => {
    setName("");
    setCategory("");
    setPropertyId("");
    setRoomId("");
    setSerialNumber("");
    setError("");
  };

  const handleClose = () => {
    if (!submitting) {
      resetForm();
      onClose();
    }
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");

    if (!name.trim()) {
      setError("Name is required");
      return;
    }

    setSubmitting(true);
    try {
      const data: CreateAssetRequest = { name: name.trim() };
      if (category) data.category = category;
      if (propertyId) data.property_id = propertyId;
      if (roomId) data.room_id = roomId;
      if (serialNumber.trim()) data.serial_number = serialNumber.trim();

      await onSubmit(data);
      resetForm();
      onClose();
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : "Failed to create asset";
      setError(msg);
    } finally {
      setSubmitting(false);
    }
  };

  const propertyOptions = properties.map((p) => ({
    value: p.id,
    label: p.name,
  }));

  return (
    <Modal open={open} onClose={handleClose} title="Add Asset" maxWidth="max-w-lg">
      <form onSubmit={handleSubmit} className="space-y-4">
        {error && (
          <div className="rounded-md bg-red-50 p-3 text-sm text-red-700">
            {error}
          </div>
        )}

        <Input
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="e.g. Hot Water Heater"
          required
        />

        <Select
          label="Category"
          value={category}
          onChange={(e) => setCategory(e.target.value)}
          options={ASSET_CATEGORIES}
          placeholder="Select category"
        />

        <Select
          label="Property"
          value={propertyId}
          onChange={(e) => setPropertyId(e.target.value)}
          options={propertyOptions}
          placeholder="Select property (optional)"
        />

        <Input
          label="Room"
          value={roomId}
          onChange={(e) => setRoomId(e.target.value)}
          placeholder="e.g. Basement"
        />

        <Input
          label="Serial Number"
          value={serialNumber}
          onChange={(e) => setSerialNumber(e.target.value)}
          placeholder="e.g. ABC-12345"
        />

        <div className="flex justify-end gap-3 pt-2">
          <Button
            type="button"
            variant="primary"
            className="bg-white !text-gray-700 border border-gray-300 hover:bg-gray-50 focus-visible:outline-gray-400 disabled:bg-gray-100"
            onClick={handleClose}
            disabled={submitting}
          >
            Cancel
          </Button>
          <Button type="submit" loading={submitting}>
            Add Asset
          </Button>
        </div>
      </form>
    </Modal>
  );
}
