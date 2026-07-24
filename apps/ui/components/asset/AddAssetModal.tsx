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
  /** Pre-select a property (e.g. when adding from property detail page) */
  defaultPropertyId?: string;
}

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
  { value: "Tools", label: "Tools" },
  { value: "Other", label: "Other" },
];

export default function AddAssetModal({ open, onClose, onSubmit, properties, defaultPropertyId }: AddAssetModalProps) {
  const [form, setForm] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const set = (k: string, v: string) => setForm((f) => ({ ...f, [k]: v }));

  const resetForm = () => {
    setForm(defaultPropertyId ? { property_id: defaultPropertyId } : {});
    setError("");
  };

  const handleClose = () => {
    if (!submitting) { resetForm(); onClose(); }
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");
    if (!form.name?.trim()) { setError("Name is required"); return; }

    setSubmitting(true);
    try {
      const data: Record<string, unknown> = { name: form.name.trim() };
      for (const k of ["category","property_id","room_id","manufacturer","model","serial_number","purchase_date","purchase_price","warranty_expiry","notes"]) {
        if (form[k]?.trim()) data[k] = form[k].trim();
      }
      await onSubmit(data as unknown as CreateAssetRequest);
      resetForm();
      onClose();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to create asset");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal opened={open} onClose={handleClose} title="Add Asset" size="lg">
      <form onSubmit={handleSubmit} className="space-y-4">
        {error && <div className="rounded-md bg-red-50 p-3 text-sm text-red-700">{error}</div>}

        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Input label="Name *" value={form.name ?? ""} onChange={(e) => set("name", e.target.value)} placeholder="e.g. Hot Water Heater" required />
          <Select label="Category" value={(form.category ?? "") as string} onChange={(value) => set("category", value ?? "")} data={ASSET_CATEGORIES} placeholder="Select category" />
          <Select label="Property" value={(form.property_id ?? defaultPropertyId ?? "") as string} onChange={(value) => set("property_id", value ?? "")} data={properties.map((p) => ({ value: p.id, label: p.name }))} placeholder="Select property" />
          <Input label="Room ID" value={form.room_id ?? ""} onChange={(e) => set("room_id", e.target.value)} placeholder="Optional" />
          <Input label="Manufacturer" value={form.manufacturer ?? ""} onChange={(e) => set("manufacturer", e.target.value)} placeholder="e.g. Carrier" />
          <Input label="Model" value={form.model ?? ""} onChange={(e) => set("model", e.target.value)} placeholder="e.g. 24ACC6" />
          <Input label="Serial Number" value={form.serial_number ?? ""} onChange={(e) => set("serial_number", e.target.value)} placeholder="e.g. ABC-12345" />
          <Input label="Purchase Price" value={form.purchase_price ?? ""} onChange={(e) => set("purchase_price", e.target.value)} placeholder="$1,200" />
          <Input label="Purchase Date" value={form.purchase_date ?? ""} onChange={(e) => set("purchase_date", e.target.value)} type="date" />
          <Input label="Warranty Expiry" value={form.warranty_expiry ?? ""} onChange={(e) => set("warranty_expiry", e.target.value)} type="date" />
        </div>

        <label className="block">
          <span className="text-xs font-medium text-gray-600">Notes</span>
          <textarea
            value={form.notes ?? ""}
            onChange={(e) => set("notes", e.target.value)}
            rows={2}
            className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500"
            placeholder="Any additional notes about this asset"
          />
        </label>

        <div className="flex justify-end gap-3 pt-2">
          <Button type="button" variant="primary" className="bg-white !text-gray-700 border border-gray-300 hover:bg-gray-50" onClick={handleClose} disabled={submitting}>Cancel</Button>
          <Button type="submit" loading={submitting}>Add Asset</Button>
        </div>
      </form>
    </Modal>
  );
}
