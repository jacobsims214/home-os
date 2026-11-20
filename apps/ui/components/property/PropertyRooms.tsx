"use client";

/**
 * PropertyRooms (#4578): the rooms → things tree of a property, mobile-first.
 *
 * Phone (< 768 px): a two-column grid of room cards, one 44 px+ link each,
 * exactly like the People & House mockup (artifact #89, House > Maple Street).
 * Each card names the room, its count and any overdue upkeep in its things.
 * A dashed "Not in a room" card follows when things belong to the house
 * itself. Tapping a card opens the room's things inline in a sheet (there is
 * no separate room page yet), where things can be added and opened.
 * ≥ 768 px: three columns.
 *
 * The data is grouped by property-tree.ts (pure, node --test covered), so
 * "no room" and "a room of another property" are the same honest answer here.
 */

import { useId, useState } from "react";
import { Button } from "@mantine/core";
import {
  IconBox,
  IconBulb,
  IconBed,
  IconChefHat,
  IconDoor,
  IconDroplet,
  IconPlus,
  IconSofa,
  IconStairs,
  IconTool,
  IconTrees,
  IconCar,
} from "@tabler/icons-react";
import type { Asset } from "@/lib/types/api";
import type { RoomResponse } from "@/types/property";
import {
  groupByRoom,
  roomIconKey,
  roomOverdueCount,
  thingsLabel,
  type TreeTask,
} from "./property-tree";
import RoomThingsModal from "./RoomThingsModal";

const STROKE = 1.75;
const LOOSE_KEY = "__loose__";

/** roomIconKey's keys → the icon. Literal components so the bundler keeps them. */
const ROOM_ICON: Record<string, typeof IconDoor> = {
  cook: IconChefHat,
  bed: IconBed,
  drop: IconDroplet,
  car: IconCar,
  sofa: IconSofa,
  tool: IconTool,
  stairs: IconStairs,
  tree: IconTrees,
  bulb: IconBulb,
  box: IconBox,
  door: IconDoor,
};

export function RoomIcon({ name, size = 20 }: { name: string; size?: number }) {
  const Icon = ROOM_ICON[roomIconKey(name)] ?? IconDoor;
  return <Icon size={size} stroke={STROKE} aria-hidden />;
}

interface PropertyRoomsProps {
  propertyId: string;
  rooms: RoomResponse[];
  things: Asset[];
  tasks: TreeTask[];
  /** False for a read-only role (a vendor): no Add, no sheets to add from. */
  canWrite: boolean;
  /** The page's Add-room handler; the header button and the empty state use it. */
  onAddRoom: () => void;
  /** The page's Add-thing handler, optionally into a room. */
  onAddThing: (roomId?: string) => void;
  /** The FAMILY's civil date (#5316); null while the household loads. */
  today: string | null;
}

export default function PropertyRooms({
  propertyId,
  rooms,
  things,
  tasks,
  canWrite,
  onAddRoom,
  onAddThing,
  today,
}: PropertyRoomsProps) {
  const headingId = useId();
  const { rooms: groups, notInARoom } = groupByRoom(rooms, things);
  // "Not in a room" is its own small group; null means nothing is open.
  const [openKey, setOpenKey] = useState<string | null>(null);
  const openLoose = openKey === LOOSE_KEY;
  const openGroup = groups.find((g) => g.room.id === openKey) ?? null;
  const isEmpty = rooms.length === 0 && things.length === 0;

  return (
    <section aria-labelledby={headingId} className="min-w-0">
      <div className="mb-2 flex items-center justify-between gap-3">
        <h2 id={headingId} className="min-w-0 text-sm font-semibold text-ink-2">
          Rooms
          {rooms.length > 0 && <span className="ml-1.5 font-medium text-ink-3">{rooms.length}</span>}
        </h2>
        {canWrite && (
          <Button
            variant="subtle"
            size="compact-sm"
            onClick={onAddRoom}
            leftSection={<IconPlus size={16} stroke={STROKE} />}
            aria-haspopup="dialog"
            className="max-md:min-h-11 shrink-0"
          >
            Add room
          </Button>
        )}
      </div>

      {isEmpty ? (
        <div
          data-rooms-empty
          className="flex flex-col items-start gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-8 text-center"
        >
          <p className="w-full text-sm text-ink-3">
            No rooms yet. Add the first one, then the things in it.
          </p>
          {canWrite && (
            <Button
              variant="light"
              onClick={onAddRoom}
              leftSection={<IconPlus size={16} stroke={STROKE} />}
              aria-haspopup="dialog"
              className="mx-auto max-md:min-h-11"
            >
              Add room
            </Button>
          )}
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-2.5 md:grid-cols-3 md:gap-3">
          {groups.map(({ room, things: roomThings }) => {
            const overdue = today ? roomOverdueCount(roomThings, tasks, today) : 0;
            const body = (
              <>
                <span className="grid size-8 shrink-0 place-items-center rounded-[9px] bg-surface-2 text-ink-2">
                  <RoomIcon name={room.name} size={18} />
                </span>
                <span className="min-w-0">
                  <span className="block text-[15px] font-semibold leading-tight text-ink [overflow-wrap:anywhere]">
                    {room.name}
                  </span>
                  <span className="mt-0.5 block text-xs text-ink-3">
                    {thingsLabel(roomThings.length)}
                    {room.floor !== null && room.floor !== undefined && ` · Floor ${room.floor}`}
                  </span>
                  {overdue > 0 && (
                    <span className="mt-1 flex items-center gap-1 text-xs font-semibold text-warn-ink">
                      <IconTool size={13} stroke={2} aria-hidden />
                      {overdue} overdue
                    </span>
                  )}
                </span>
              </>
            );
            const className =
              "flex min-h-24 w-full flex-col items-start gap-2 rounded-token-md border border-line bg-surface p-3.5 text-left shadow-token-sm transition-colors hover:bg-surface-2 active:bg-surface-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent";

            // The whole card is one control: a button opens the room's things
            // (a link would need a room page; the sheet keeps Back honest).
            return (
              <button
                key={room.id}
                type="button"
                data-room-card={room.id}
                aria-label={`${room.name}, ${thingsLabel(roomThings.length)}`}
                onClick={() => setOpenKey(room.id)}
                className={className}
              >
                {body}
              </button>
            );
          })}

          {notInARoom.length > 0 && (
            <button
              type="button"
              data-room-loose
              aria-label={`Not in a room, ${thingsLabel(notInARoom.length)}`}
              onClick={() => setOpenKey(LOOSE_KEY)}
              className="flex min-h-24 w-full flex-col items-start gap-2 rounded-token-md border border-dashed border-line p-3.5 text-left transition-colors hover:bg-surface-2 active:bg-surface-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
            >
              <span className="grid size-8 shrink-0 place-items-center rounded-[9px] bg-surface-2 text-ink-2">
                <IconBox size={18} stroke={STROKE} aria-hidden />
              </span>
              <span className="min-w-0">
                <span className="block text-[15px] font-semibold leading-tight text-ink">Not in a room</span>
                <span className="mt-0.5 block text-xs text-ink-3">{thingsLabel(notInARoom.length)}</span>
                <span className="mt-1 block text-xs text-ink-3 [overflow-wrap:anywhere]">
                  {notInARoom.map((t) => t.name).join(", ")}
                </span>
              </span>
            </button>
          )}
        </div>
      )}

      <RoomThingsModal
        opened={openGroup !== null || openLoose}
        onClose={() => setOpenKey(null)}
        propertyId={propertyId}
        room={openGroup?.room ?? null}
        things={openGroup?.things ?? notInARoom}
        canWrite={canWrite}
        onAddThing={onAddThing}
      />
    </section>
  );
}
