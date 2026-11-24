"use client";

/**
 * AddRoomModal (#4578): a room in one tap on a phone.
 *
 * POSTs /api/v1/properties/{id}/rooms (name required, floor optional), then
 * refreshes the property's room list so the new card appears on the page.
 * ResponsiveModal gives the bottom sheet < 768 px and the centred dialog above
 * it (architecture/responsive-modal.md).
 */

import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Stack } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import { ResponsiveModal, ResponsiveModalActions, Button, TextInput } from "@/components/ui";
import type { RoomResponse } from "@/types/property";

export const roomsKey = (propertyId: string) =>
  [...propertyKeys.detail(propertyId), "rooms"] as const;

interface AddRoomModalProps {
  opened: boolean;
  onClose: () => void;
  propertyId: string;
  /** Name of the property, so the sheet says where the room is going. */
  propertyName: string;
}

export default function AddRoomModal({ opened, onClose, propertyId, propertyName }: AddRoomModalProps) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [floor, setFloor] = useState("");
  const [error, setError] = useState<string | null>(null);

  const close = () => {
    setName("");
    setFloor("");
    setError(null);
    create.reset();
    onClose();
  };

  const create = useMutation({
    mutationFn: (body: { name: string; floor?: number }) =>
      apiFetch<{ data: RoomResponse }>(`/api/v1/properties/${propertyId}/rooms`, { method: "POST", body }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: roomsKey(propertyId) });
      queryClient.invalidateQueries({ queryKey: propertyKeys.detail(propertyId) });
      notifications.show({ message: `${res.data?.name ?? "Room"} added`, color: "teal" });
      close();
    },
    onError: (err: Error) => setError(err.message || "Couldn't add the room. Try again."),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setError("Give the room a name, like Kitchen or Utility room.");
      return;
    }
    setError(null);
    const body: { name: string; floor?: number } = { name: trimmed };
    const f = Number(floor);
    if (floor.trim() !== "" && Number.isFinite(f)) body.floor = f;
    create.mutate(body);
  };

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add a room">
      <form onSubmit={submit} noValidate>
        <Stack gap="sm">
          <p className="text-sm text-ink-2">
            For <span className="font-medium text-ink">{propertyName}</span>
          </p>
          <TextInput
            label="Room name"
            required
            data-autofocus
            placeholder="Kitchen"
            value={name}
            error={error ?? undefined}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <TextInput
            label="Floor"
            inputMode="numeric"
            placeholder="1"
            description="Optional. 0 is the ground floor, -1 the basement."
            value={floor}
            onChange={(e) => setFloor(e.currentTarget.value)}
          />
        </Stack>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Add room
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}
