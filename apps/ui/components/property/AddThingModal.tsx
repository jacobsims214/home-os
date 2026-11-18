"use client";

/**
 * AddThingModal (#4578): add a "thing" (asset) to a property, optionally into
 * one of its rooms. The property detail page opens it from the Rooms section
 * header and from a room card, so `defaultRoomId` is preselected.
 *
 * POST /api/v1/assets with property_id (and room_id when chosen). The UI word
 * is "thing" (People & House mockup, artifact #89); the API and the routes
 * still say "asset" (/dashboard/assets/[id]).
 */

import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Stack } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { apiFetch } from "@/lib/api";
import { assetKeys, propertyKeys } from "@/lib/query-keys";
import { ResponsiveModal, ResponsiveModalActions, Button, Select, TextInput } from "@/components/ui";
import type { Asset } from "@/lib/types/api";
import type { RoomResponse } from "@/types/property";
import { roomsKey } from "./AddRoomModal";

/** The categories the API stores today (apps/api/internal/asset). */
const CATEGORIES = [
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

/** "" is the "Not in a room" choice; the API wants room_id absent then. */
const NO_ROOM = "";

interface AddThingModalProps {
  opened: boolean;
  onClose: () => void;
  propertyId: string;
  propertyName: string;
  rooms: RoomResponse[];
  /** Preselected room (a room card's "Add thing"). */
  defaultRoomId?: string;
}

export default function AddThingModal({
  opened,
  onClose,
  propertyId,
  propertyName,
  rooms,
  defaultRoomId,
}: AddThingModalProps) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [roomId, setRoomId] = useState(defaultRoomId ?? NO_ROOM);
  const [category, setCategory] = useState<string | null>(null);
  const [manufacturer, setManufacturer] = useState("");
  const [model, setModel] = useState("");
  const [error, setError] = useState<string | null>(null);

  // Re-opening from another room card must preselect that room.
  useEffect(() => {
    if (opened) {
      setRoomId(defaultRoomId ?? NO_ROOM);
      setError(null);
    }
  }, [opened, defaultRoomId]);

  const close = () => {
    setName("");
    setCategory(null);
    setManufacturer("");
    setModel("");
    setError(null);
    create.reset();
    onClose();
  };

  const create = useMutation({
    mutationFn: (body: Record<string, string>) =>
      apiFetch<{ data: Asset }>("/api/v1/assets", { method: "POST", body }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: assetKeys.byProperty(propertyId) });
      queryClient.invalidateQueries({ queryKey: roomsKey(propertyId) });
      notifications.show({ message: `${res.data?.name ?? "Thing"} added`, color: "teal" });
      close();
    },
    onError: (err: Error) => setError(err.message || "Couldn't add the thing. Try again."),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setError("Give it a name, like Furnace or Water heater.");
      return;
    }
    setError(null);
    const body: Record<string, string> = { name: trimmed, property_id: propertyId };
    if (roomId !== NO_ROOM) body.room_id = roomId;
    if (category) body.category = category;
    if (manufacturer.trim()) body.manufacturer = manufacturer.trim();
    if (model.trim()) body.model = model.trim();
    create.mutate(body);
  };

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add a thing">
      <form onSubmit={submit} noValidate>
        <Stack gap="sm">
          <p className="text-sm text-ink-2">
            For <span className="font-medium text-ink">{propertyName}</span>
          </p>
          <TextInput
            label="Name"
            required
            data-autofocus
            placeholder="Furnace"
            value={name}
            error={error ?? undefined}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <Select
            label="Room"
            description="A roof or a sump pump can stay out of a room."
            data={[
              { value: NO_ROOM, label: "Not in a room" },
              ...rooms.map((r) => ({ value: r.id, label: r.name })),
            ]}
            value={roomId}
            onChange={(value) => setRoomId(value ?? NO_ROOM)}
            allowDeselect={false}
          />
          <Select
            label="Category"
            data={CATEGORIES}
            value={category}
            onChange={setCategory}
            placeholder="Optional"
            clearable
          />
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <TextInput
              label="Make"
              placeholder="Carrier"
              value={manufacturer}
              onChange={(e) => setManufacturer(e.currentTarget.value)}
            />
            <TextInput
              label="Model"
              placeholder="Infinity 59SC5"
              value={model}
              onChange={(e) => setModel(e.currentTarget.value)}
            />
          </div>
        </Stack>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Add thing
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}
