"use client";

/**
 * Property detail (#4578), rebuilt per the People & House mockup (artifact #89,
 * House > property) and domain model v2: property → rooms → things, with the
 * upkeep that needs attention and the details sheet.
 *
 * Mobile-first:
 * - Phone (< 768 px): DetailPageLayout's sticky title bar (back 44 px, h2
 *   title, actions menu), then Details / Resources as collapsible sections.
 *   Rooms are a two-column grid of 44 px+ cards; a card opens the room's
 *   things in a bottom sheet. Adding a room or a thing is one bottom sheet
 *   with 44 px fields (no iOS zoom, no sideways scroll at 375 px).
 * - ≥ 1024 px: the layout's two columns — the sheet on the left, Resources on
 *   the right — and the room grid grows to three columns.
 *
 * Money: every figure comes from components/property/property-finance.ts
 * (#4553), the ONE implementation of the mortgage math. The page used to carry
 * its own copy of it (a plain /12 here, of a percent), which is the #4509 bug
 * class; there is no second copy now.
 *
 * The edit form is the one the #4706/#4728 work landed: only the property's
 * own fields are edited here — the mortgage, tax, insurance and HOA are its
 * loans and bills (LinkedFinances), never the property's own inputs.
 */

import { useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Table } from "@mantine/core";
import {
  IconAlertTriangle,
  IconChevronRight,
  IconHome,
  IconPlus,
  IconRefresh,
} from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { assetKeys, maintenanceKeys, propertyKeys } from "@/lib/query-keys";
import type { PropertyDetailResponse, RoomListResponse } from "@/types/property";
import type { Asset } from "@/lib/types/api";
import type { MaintenanceTask } from "@/components/maintenance/types";
import { Skeleton } from "@mantine/core";
import { ResponsiveModal, ResponsiveModalActions, Select, TextInput } from "@/components/ui";
import DetailPageLayout from "@/components/layout/DetailPageLayout";
import LinkedFinances from "@/components/finance/LinkedFinances";
import { PROPERTY_EDIT_FIELDS, propertyEditBody } from "@/components/finance/linked-finance-utils";
import { typeLabel } from "@/components/property/property-tree";
import { formatUsd, toNumber } from "@/components/property/property-finance";
import PropertyRooms from "@/components/property/PropertyRooms";
import PropertyAttention from "@/components/property/PropertyAttention";
import PropertyDetailsSheet from "@/components/property/PropertyDetailsSheet";
import AddRoomModal, { roomsKey } from "@/components/property/AddRoomModal";
import AddThingModal from "@/components/property/AddThingModal";
import { useNavRole } from "@/components/shared/use-nav-role";
import { canWriteProperty } from "@/components/property/property-access";

const STROKE = 1.75;

/** The property types the API stores, for the edit select. */
const PROPERTY_TYPES = [
  { value: "single_family", label: "Single family" },
  { value: "condo", label: "Condo" },
  { value: "townhouse", label: "Townhouse" },
  { value: "apartment", label: "Apartment" },
  { value: "land", label: "Land" },
  { value: "multi_family", label: "Multi-family" },
  { value: "commercial", label: "Commercial" },
];

export default function PropertyDetailPage() {
  const params = useParams<{ id: string }>();
  const propertyId = params.id;
  const queryClient = useQueryClient();
  const role = useNavRole();
  // The family roles add rooms and things; a housesitter or vendor reads the
  // house only (roles.go ResProperties/ResAssets ActWrite — property-access.ts).
  const canWrite = canWriteProperty(role);

  const [isEditing, setIsEditing] = useState(false);
  const [editValues, setEditValues] = useState<Record<string, string>>({});
  const [addRoomOpen, setAddRoomOpen] = useState(false);
  const [addThingRoomId, setAddThingRoomId] = useState<string | undefined>(undefined);
  const [addThingOpen, setAddThingOpen] = useState(false);

  const propertyQuery = useQuery({
    queryKey: propertyKeys.detail(propertyId),
    queryFn: () => apiFetch<PropertyDetailResponse>(`/api/v1/properties/${propertyId}`),
    enabled: !!propertyId,
  });

  const roomsQuery = useQuery({
    queryKey: roomsKey(propertyId),
    queryFn: () => apiFetch<RoomListResponse>(`/api/v1/properties/${propertyId}/rooms`),
    enabled: !!propertyId,
  });

  const thingsQuery = useQuery({
    queryKey: assetKeys.byProperty(propertyId),
    queryFn: () =>
      apiFetch<{ data: Asset[] }>("/api/v1/assets", { params: { property_id: propertyId } }).then(
        (r) => r.data ?? [],
      ),
    enabled: !!propertyId,
  });

  const tasksQuery = useQuery({
    queryKey: maintenanceKeys.byProperty(propertyId),
    queryFn: () =>
      apiFetch<{ data: MaintenanceTask[] }>("/api/v1/maintenance/tasks", {
        params: { property_id: propertyId },
      }).then((r) => r.data ?? []),
    enabled: !!propertyId,
  });

  const property = propertyQuery.data?.data;
  const rooms = roomsQuery.data?.data ?? [];
  const things = thingsQuery.data ?? [];
  const tasks = tasksQuery.data ?? [];

  const update = useMutation({
    mutationFn: (values: Record<string, string>) =>
      apiFetch<PropertyDetailResponse>(`/api/v1/properties/${propertyId}`, {
        method: "PUT",
        body: propertyEditBody(values),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: propertyKeys.detail(propertyId) });
      queryClient.invalidateQueries({ queryKey: propertyKeys.lists() });
      setIsEditing(false);
    },
  });

  const startEditing = () => {
    if (!property) return;
    const values: Record<string, string> = {};
    for (const k of PROPERTY_EDIT_FIELDS) {
      values[k] = (property as unknown as Record<string, string | null>)[k] ?? "";
    }
    setEditValues(values);
    setIsEditing(true);
  };

  const openAddThing = (roomId?: string) => {
    setAddThingRoomId(roomId);
    setAddThingOpen(true);
  };

  // ── Loading / error ────────────────────────────────────────

  if (propertyQuery.isLoading) {
    return (
      <div aria-busy="true" aria-label="Loading property" className="mx-auto w-full max-w-2xl space-y-3">
        <Skeleton height={20} width="55%" radius="sm" />
        <Skeleton height={14} width="35%" radius="sm" />
        <Skeleton height={96} radius="md" />
        <Skeleton height={96} radius="md" />
      </div>
    );
  }

  if (propertyQuery.isError || !property) {
    return (
      <div className="mx-auto w-full max-w-2xl">
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">
            Couldn&apos;t load this property. Check your connection and try again.
          </p>
          <Button
            variant="default"
            onClick={() => propertyQuery.refetch()}
            loading={propertyQuery.isFetching}
            className="max-md:min-h-11"
          >
            Try again
          </Button>
        </div>
        <Link
          href="/dashboard/properties"
          className="mt-4 inline-flex min-h-11 items-center gap-1 text-sm font-medium text-accent hover:underline"
        >
          Back to properties
        </Link>
      </div>
    );
  }

  const type = typeLabel(property.property_type);
  const summary = [property.address, type, `${rooms.length} ${rooms.length === 1 ? "room" : "rooms"}`]
    .filter(Boolean)
    .join(" · ");

  // ── Edit mode ──────────────────────────────────────────────

  if (isEditing) {
    const field = (name: string, label: string, props: Record<string, unknown> = {}) => (
      <TextInput
        label={label}
        value={editValues[name] ?? ""}
        onChange={(e) => setEditValues((v) => ({ ...v, [name]: e.currentTarget.value }))}
        {...props}
      />
    );
    return (
      <DetailPageLayout
        entityType="property"
        entityId={propertyId}
        title={`Edit ${property.name}`}
        isEditing
        onCancel={() => setIsEditing(false)}
        onSave={() => update.mutate(editValues)}
        isSaving={update.isPending}
      >
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 max-md:[&_.mantine-Input-input]:min-h-11 max-md:[&_.mantine-Input-input]:text-base">
          {field("name", "Name", { required: true, "data-autofocus": true })}
          {field("address", "Address")}
          <Select
            label="Type"
            data={PROPERTY_TYPES}
            value={editValues.property_type || null}
            onChange={(value) => setEditValues((v) => ({ ...v, property_type: value ?? "" }))}
            placeholder="Not set"
            clearable
          />
          {field("current_value", "Current value", { inputMode: "decimal", placeholder: "$" })}
          {field("purchase_price", "Purchase price", { inputMode: "decimal", placeholder: "$" })}
          {field("purchase_date", "Purchase date", { type: "date" })}
          {field("down_payment", "Down payment", { inputMode: "decimal", placeholder: "$" })}
        </div>
        <div className="mt-4">
          <TextInput
            label="Notes"
            value={editValues.notes ?? ""}
            onChange={(e) => setEditValues((v) => ({ ...v, notes: e.currentTarget.value }))}
          />
        </div>
        <p className="mt-3 text-xs text-ink-3">
          The mortgage, property tax, insurance and HOA are the loans and bills linked to this
          property. Change them under Loans &amp; bills.
        </p>
        {update.isError && (
          <p role="alert" className="mt-3 text-sm text-danger">
            Couldn&apos;t save: {update.error instanceof Error ? update.error.message : "try again"}
          </p>
        )}
      </DetailPageLayout>
    );
  }

  // ── Read mode ──────────────────────────────────────────────

  return (
    <DetailPageLayout
      entityType="property"
      entityId={propertyId}
      title={property.name}
      onEdit={startEditing}
    >
      <div data-property-detail className="min-w-0">
        {/* The property head: icon, name, the one line that says where it is. */}
        <div className="mb-4 flex items-center gap-3.5">
          <span
            aria-hidden
            className="grid size-14 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent"
          >
            <IconHome size={28} stroke={STROKE} />
          </span>
          <div className="min-w-0 flex-1">
            <h1 className="text-xl font-semibold leading-tight text-ink [overflow-wrap:anywhere] md:text-2xl">
              {property.name}
            </h1>
            {summary && <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{summary}</p>}
          </div>
          <span className="shrink-0 text-right">
            <span className="block text-xs text-ink-3">Value</span>
            <span className="block text-[15px] font-semibold tabular-nums text-ink">
              {formatUsd(toNumber(property.current_value)) ?? "—"}
            </span>
          </span>
        </div>

        <div className="flex flex-col gap-6">
          {/* Add-room / Add-thing live at the top of the sheet on a phone: the
              title bar's menu is Edit only, and the room grid follows. */}
          {canWrite && (
            <div className="flex flex-wrap gap-2 md:hidden">
              <Button
                variant="default"
                onClick={() => setAddRoomOpen(true)}
                leftSection={<IconPlus size={16} stroke={STROKE} />}
                aria-haspopup="dialog"
                className="min-h-11 flex-1"
              >
                Add room
              </Button>
              <Button
                variant="default"
                onClick={() => openAddThing()}
                leftSection={<IconPlus size={16} stroke={STROKE} />}
                aria-haspopup="dialog"
                className="min-h-11 flex-1"
              >
                Add thing
              </Button>
            </div>
          )}

          <PropertyRooms
            propertyId={propertyId}
            rooms={rooms}
            things={things}
            tasks={tasks}
            canWrite={canWrite}
            onAddRoom={() => setAddRoomOpen(true)}
            onAddThing={openAddThing}
          />

          <PropertyAttention tasks={tasks} things={things} propertyName={property.name} />

          <PropertyDetailsSheet property={property} roomCount={rooms.length} thingCount={things.length} />

          <LinkedFinances subject={{ kind: "property", id: propertyId, name: property.name }} framed />

          {/* The things list, for the tablet/desktop reader who wants the flat
              view (the rooms grid above is the phone's way in). */}
          {things.length > 0 && (
            <section aria-labelledby="property-things" className="hidden min-w-0 md:block">
              <h2 id="property-things" className="mb-1 text-sm font-semibold text-ink-2">
                Things <span className="font-medium text-ink-3">{things.length}</span>
              </h2>
              <div className="overflow-x-auto rounded-token-md border border-line bg-surface shadow-token-sm">
                <Table highlightOnHover verticalSpacing="sm" horizontalSpacing="md">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                        Thing
                      </Table.Th>
                      <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                        Room
                      </Table.Th>
                      <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                        Category
                      </Table.Th>
                      <Table.Th className="w-10">
                        <span className="sr-only">Open</span>
                      </Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {things.map((thing) => (
                      <Table.Tr key={thing.id}>
                        <Table.Td className="font-medium text-ink">
                          <Link href={`/dashboard/assets/${thing.id}`} className="hover:underline">
                            {thing.name}
                          </Link>
                        </Table.Td>
                        <Table.Td className="text-ink-2">
                          {rooms.find((r) => r.id === thing.room_id)?.name ?? "Not in a room"}
                        </Table.Td>
                        <Table.Td className="text-ink-2">{thing.category ?? "—"}</Table.Td>
                        <Table.Td className="text-ink-3">
                          <IconChevronRight size={18} stroke={STROKE} aria-hidden />
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </div>
            </section>
          )}

          {tasksQuery.isError && (
            <p role="alert" className="flex items-center gap-2 text-sm text-warn-ink">
              <IconRefresh size={16} stroke={STROKE} aria-hidden />
              Couldn&apos;t load upkeep for this property.
            </p>
          )}
        </div>
      </div>

      <AddRoomModal
        opened={addRoomOpen}
        onClose={() => setAddRoomOpen(false)}
        propertyId={propertyId}
        propertyName={property.name}
      />
      <AddThingModal
        opened={addThingOpen}
        onClose={() => setAddThingOpen(false)}
        propertyId={propertyId}
        propertyName={property.name}
        rooms={rooms}
        defaultRoomId={addThingRoomId}
      />
    </DetailPageLayout>
  );
}
