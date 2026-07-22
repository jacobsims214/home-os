"use client";

import { useState, useEffect } from "react";
import { Modal, Button, TextInput, Textarea, Select, Stack, Group, Text } from "@mantine/core";
import { apiFetch } from "@/lib/api";
import { useQueryClient, useQuery } from "@tanstack/react-query";
import { notifications } from "@mantine/notifications";
import { propertyKeys, assetKeys } from "@/lib/query-keys";
import type { Property, Asset } from "@/lib/types/api";

interface Vehicle {
  id: string;
  year: number | null;
  make: string | null;
  model: string | null;
}

interface AddMaintenanceTaskModalProps {
  open: boolean;
  onClose: () => void;
  /** Pre-fill the entity association */
  propertyId?: string;
  assetId?: string;
  vehicleId?: string;
  /** Entity name for the title */
  entityName?: string;
}

export default function AddMaintenanceTaskModal({
  open,
  onClose,
  propertyId: defaultPropertyId,
  assetId: defaultAssetId,
  vehicleId: defaultVehicleId,
  entityName,
}: AddMaintenanceTaskModalProps) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [dueDate, setDueDate] = useState("");
  const [cost, setCost] = useState("");
  const [notes, setNotes] = useState("");
  const [selectedPropertyId, setSelectedPropertyId] = useState<string | undefined>(defaultPropertyId);
  const [selectedAssetId, setSelectedAssetId] = useState<string | undefined>(defaultAssetId);
  const [selectedVehicleId, setSelectedVehicleId] = useState<string | undefined>(defaultVehicleId);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  // Fetch properties for dropdown
  const { data: propsData } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () => apiFetch<{ data: Property[] }>("/api/v1/properties"),
  });
  const properties = propsData?.data ?? [];

  // Fetch assets for the selected property
  const { data: assetsData } = useQuery({
    queryKey: selectedPropertyId ? assetKeys.byProperty(selectedPropertyId) : ["assets", "none"],
    queryFn: () => {
      if (!selectedPropertyId) return Promise.resolve({ data: [] as Asset[] });
      return apiFetch<{ data: Asset[] }>("/api/v1/assets", { params: { property_id: selectedPropertyId } });
    },
    enabled: !!selectedPropertyId,
  });
  const assets = assetsData?.data ?? [];

  // Fetch vehicles for the dropdown
  const { data: vehiclesData } = useQuery({
    queryKey: ["vehicles"],
    queryFn: () => apiFetch<{ data: Vehicle[] }>("/api/v1/vehicles"),
  });
  const vehicles = vehiclesData?.data ?? [];

  // Reset form when modal opens
  useEffect(() => {
    if (open) {
      setName("");
      setDescription("");
      setDueDate("");
      setCost("");
      setNotes("");
      setError("");
      setSelectedPropertyId(defaultPropertyId);
      setSelectedAssetId(defaultAssetId);
      setSelectedVehicleId(defaultVehicleId);
    }
  }, [open, defaultPropertyId, defaultAssetId, defaultVehicleId]);

  const handleClose = () => {
    if (!submitting) onClose();
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    if (!name.trim()) { setError("Task name is required"); return; }

    setSubmitting(true);
    try {
      const body: Record<string, unknown> = { name: name.trim() };
      if (description.trim()) body.description = description.trim();
      if (dueDate) body.due_date = dueDate;
      if (cost.trim()) body.cost = cost.trim();
      if (notes.trim()) body.notes = notes.trim();
      if (selectedPropertyId) body.property_id = selectedPropertyId;
      if (selectedAssetId) body.asset_id = selectedAssetId;
      if (selectedVehicleId) body.vehicle_id = selectedVehicleId;

      await apiFetch("/api/v1/maintenance/tasks", { method: "POST", body });
      queryClient.invalidateQueries({ queryKey: ["maintenance"] });
      notifications.show({ message: "Task created", color: "green" });
      onClose();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to create task");
    } finally {
      setSubmitting(false);
    }
  };

  const title = entityName ? `Add Maintenance — ${entityName}` : "Add Maintenance Task";

  return (
    <Modal opened={open} onClose={handleClose} title={title} size="md" centered>
      <form onSubmit={handleSubmit}>
        <Stack gap="sm">
          {error && <Text size="sm" c="red">{error}</Text>}

          <TextInput
            label="Task Name *"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Replace HVAC filter"
            required
          />

          <Textarea
            label="Description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="What needs to be done?"
            rows={2}
          />

          <Group grow>
            <TextInput
              label="Due Date"
              type="date"
              value={dueDate}
              onChange={(e) => setDueDate(e.target.value)}
            />
            <TextInput
              label="Estimated Cost"
              value={cost}
              onChange={(e) => setCost(e.target.value)}
              placeholder="$150"
            />
          </Group>

          {/* Association — show what this task is for */}
          <Stack gap="xs">
            <Text size="xs" fw={600} c="dimmed">Associate with</Text>
            <Select
              label="Property"
              value={selectedPropertyId ?? null}
              onChange={(val) => {
                setSelectedPropertyId(val ?? undefined);
                setSelectedAssetId(undefined); // reset asset when property changes
              }}
              data={properties.map((p) => ({ value: p.id, label: p.name }))}
              placeholder="Select property"
              clearable
              searchable
            />
            {selectedPropertyId && assets.length > 0 && (
              <Select
                label="Asset (optional)"
                value={selectedAssetId ?? null}
                onChange={(val) => setSelectedAssetId(val ?? undefined)}
                data={assets.map((a) => ({ value: a.id, label: a.name }))}
                placeholder="Select asset"
                clearable
                searchable
              />
            )}
            {selectedPropertyId && assets.length === 0 && (
              <Text size="xs" c="dimmed">No assets for this property</Text>
            )}
            {vehicles.length > 0 && (
              <Select
                label="Vehicle (optional)"
                value={selectedVehicleId ?? null}
                onChange={(val) => setSelectedVehicleId(val ?? undefined)}
                data={vehicles.map((v) => ({
                  value: v.id,
                  label: [v.year, v.make, v.model].filter(Boolean).join(" ") || "Unknown",
                }))}
                placeholder="Select vehicle"
                clearable
                searchable
              />
            )}
          </Stack>

          <Textarea
            label="Notes"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            placeholder="Any additional notes"
            rows={2}
          />

          <Group justify="flex-end" mt="sm">
            <Button variant="subtle" onClick={handleClose} disabled={submitting}>Cancel</Button>
            <Button type="submit" loading={submitting}>Add Task</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
