"use client";

/**
 * RoomThingsModal (#4578): the things in one room, opened from a room card.
 *
 * There is no room route yet, so the room opens as a sheet
 * (ResponsiveModal: bottom sheet on a phone) instead of a dead link: the
 * person sees what is in the room, opens any thing's own page, and adds the
 * next thing with the room already chosen.
 *
 * `room === null` means the "Not in a room" group: things that belong to the
 * house itself. Adding from there leaves room_id off, which is what keeps a
 * roof a roof.
 */

import Link from "next/link";
import { Button, Stack } from "@mantine/core";
import { IconBox, IconChevronRight, IconPlus } from "@tabler/icons-react";
import { ResponsiveModal } from "@/components/ui";
import { thingSubtitle, type TreeAsset, type TreeRoom } from "./property-tree";

interface RoomThingsModalProps {
  opened: boolean;
  onClose: () => void;
  propertyId: string;
  room: TreeRoom | null;
  things: TreeAsset[];
  canWrite: boolean;
  onAddThing: (roomId?: string) => void;
}

export default function RoomThingsModal({
  opened,
  onClose,
  propertyId,
  room,
  things,
  canWrite,
  onAddThing,
}: RoomThingsModalProps) {
  const title = room?.name ?? "Not in a room";
  const count = things.length;

  return (
    <ResponsiveModal opened={opened} onClose={onClose} title={title}>
      <Stack gap="sm">
        <p className="text-sm text-ink-3">
          {count === 0
            ? "Nothing in here yet."
            : `${count} ${count === 1 ? "thing" : "things"}`}
          {room?.floor !== null && room?.floor !== undefined && ` · Floor ${room.floor}`}
        </p>

        {count > 0 && (
          <ul aria-label={`Things in ${title}`} className="divide-y divide-line rounded-token-md border border-line">
            {things.map((thing) => {
              const sub = thingSubtitle(thing);
              return (
                <li key={thing.id}>
                  <Link
                    href={`/dashboard/assets/${thing.id}`}
                    onClick={onClose}
                    className="flex min-h-11 items-center gap-3 px-3 py-2.5 transition-colors hover:bg-surface-2 active:bg-surface-2"
                  >
                    <span className="grid size-9 shrink-0 place-items-center rounded-[9px] bg-surface-2 text-ink-2">
                      <IconBox size={18} stroke={1.75} aria-hidden />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm font-medium text-ink [overflow-wrap:anywhere]">{thing.name}</span>
                      {sub && <span className="block text-xs text-ink-3 [overflow-wrap:anywhere]">{sub}</span>}
                    </span>
                    <IconChevronRight size={18} stroke={1.75} className="shrink-0 text-ink-3" aria-hidden />
                  </Link>
                </li>
              );
            })}
          </ul>
        )}

        {canWrite && (
          <Button
            onClick={() => {
              onAddThing(room?.id);
              onClose();
            }}
            leftSection={<IconPlus size={16} stroke={1.75} />}
            aria-haspopup="dialog"
            className="max-md:min-h-11"
            fullWidth
          >
            {room ? `Add a thing to ${room.name}` : "Add a thing"}
          </Button>
        )}
        {/* The property id is what the sheet's things belong to; kept in the
            DOM so the sheet never renders without its subject. */}
        <span data-room-property={propertyId} className="sr-only" />
      </Stack>
    </ResponsiveModal>
  );
}
