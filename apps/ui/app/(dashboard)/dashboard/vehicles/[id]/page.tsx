"use client";

/**
 * Vehicle detail (#4555), mobile-first per the People & House mockup
 * (artifact #89, House > Vehicles) inside the DetailPageLayout frame
 * (KB architecture/detail-page-layout.md), the same frame the property detail
 * uses (#4578):
 *
 * - Phone (< 768 px): DetailPageLayout's sticky title bar (44 px back, h2
 *   title, actions menu), then the car's head, a 2x2 summary of the facts a
 *   phone can read at a glance, and the sections.
 * - >= 768 px: the summary goes 4-up; >= 1024 px the layout's two columns put
 *   the sheet on the left and Resources on the right.
 *
 * WHAT THIS PAGE MAY AND MAY NOT EDIT
 * The vehicle's loan, registration and insurance are a loan and bills with
 * vehicle_id (#4704). The API still returns lender / loan_amount /
 * registration_cost / ... derived from those rows, and the page never sends
 * them (#4706; #4707 drops the columns) — they are shown and edited as the
 * linked loan and bills, through LinkedFinances. e2e/property-vehicle-finance
 * asserts exactly that, so no legacy field is a form input or a data-field row.
 *
 * There is no odometer column in the API (apps/api/internal/vehicle/model.go),
 * so the summary shows value, plate, colour and the registration month rather
 * than a mileage the page cannot read.
 */

import { useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconCar } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { maintenanceKeys, vehicleKeys } from "@/lib/query-keys";
import { TextInput } from "@/components/ui";
import DetailPageLayout from "@/components/layout/DetailPageLayout";
import LinkedFinances from "@/components/finance/LinkedFinances";
import { formatUsd, toNumber } from "@/components/property/property-finance";
import { registrationLabel, vehicleSubtitle, vehicleTitle } from "@/components/vehicle/vehicle-facts";

const STROKE = 1.75;

/** The vehicle response (apps/api/internal/vehicle/handler.go vehicleResponse). */
interface Vehicle {
  id: string;
  year?: number | null;
  make?: string | null;
  model?: string | null;
  vin?: string | null;
  license_plate?: string | null;
  color?: string | null;
  notes?: string | null;
  purchase_price?: string | null;
  purchase_date?: string | null;
  current_value?: string | null;
  registration_renewal_month?: number | null;
  created_at?: string;
  updated_at?: string;
}

/**
 * A maintenance task, with the vehicle_id the shared MaintenanceTask type
 * omits (it predates the column). Only what this page reads.
 */
interface VehicleTask {
  id: string;
  name: string;
  status: "pending" | "in_progress" | "done" | "skipped";
  due_date?: string | null;
  vehicle_id?: string | null;
}

/**
 * The fields this page edits and sends. A vehicle's own columns only: the
 * finance fields are the linked loan and bills (#4706).
 */
const VEHICLE_EDIT_FIELDS = [
  "year",
  "make",
  "model",
  "vin",
  "license_plate",
  "color",
  "notes",
  "purchase_price",
  "purchase_date",
  "current_value",
] as const;

/** The PUT body: every editable field as a string, or null when blank. */
function vehicleEditBody(values: Record<string, string>): Record<string, unknown> {
  const body: Record<string, unknown> = {};
  for (const k of VEHICLE_EDIT_FIELDS) {
    const v = values[k]?.trim() ?? "";
    if (k === "year") {
      // year is *int in the API; a string is a 400.
      body.year = v === "" ? null : Number(v);
      continue;
    }
    body[k] = v === "" ? null : v;
  }
  return body;
}

function fmtDate(iso: string | null | undefined): string | null {
  if (!iso) return null;
  const d = new Date(`${iso.slice(0, 10)}T00:00:00`);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric" });
}

/** One fact in the summary strip: a label and a value, or a dash when unset. */
function Fact({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="min-w-0 px-4 py-2.5">
      <dt className="text-xs text-ink-3">{label}</dt>
      <dd className="m-0 text-[15px] font-semibold text-ink [overflow-wrap:anywhere]">{value}</dd>
    </div>
  );
}

/** A read-only details row, per the detail-page field contract. */
function Row({ field, label, value }: { field: string; label: string; value: React.ReactNode }) {
  return (
    <div
      data-field={field}
      className="flex flex-col gap-1 py-2.5 md:grid md:grid-cols-[minmax(7rem,1fr)_minmax(0,2fr)] md:items-baseline md:gap-x-6"
    >
      <dt className="text-xs text-ink-3 md:text-sm">{label}</dt>
      <dd className="m-0 whitespace-pre-line text-sm text-ink [overflow-wrap:anywhere]">{value}</dd>
    </div>
  );
}

export default function VehicleDetailPage() {
  const params = useParams<{ id: string }>();
  const vehicleId = params.id;
  const queryClient = useQueryClient();

  const [isEditing, setIsEditing] = useState(false);
  const [editValues, setEditValues] = useState<Record<string, string>>({});

  const vehicleQuery = useQuery({
    queryKey: vehicleKeys.detail(vehicleId),
    queryFn: () => apiFetch<{ data: Vehicle }>(`/api/v1/vehicles/${vehicleId}`),
    enabled: !!vehicleId,
  });

  // The vehicle's upkeep. maintenance_tasks carry vehicle_id, but
  // GET /maintenance/tasks has no vehicle_id filter (only status and
  // property_id — apps/api/internal/maintenance/handler.go ListTasks), so the
  // filter is applied here. Sending ?vehicle_id= would be ignored by the API
  // and list every task in the household. Same shape as LinkedFinances' bills.
  const tasksQuery = useQuery({
    queryKey: [...maintenanceKeys.all, "byVehicle", vehicleId],
    queryFn: () =>
      apiFetch<{ data: VehicleTask[] }>("/api/v1/maintenance/tasks").then((r) =>
        (r.data ?? []).filter((t) => t.vehicle_id === vehicleId),
      ),
    enabled: !!vehicleId,
  });

  const update = useMutation({
    mutationFn: (values: Record<string, string>) =>
      apiFetch<{ data: Vehicle }>(`/api/v1/vehicles/${vehicleId}`, {
        method: "PUT",
        body: vehicleEditBody(values),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: vehicleKeys.detail(vehicleId) });
      queryClient.invalidateQueries({ queryKey: vehicleKeys.lists() });
      setIsEditing(false);
    },
    onError: (err: Error) =>
      notifications.show({ title: "Save failed", message: err.message, color: "red" }),
  });

  const vehicle = vehicleQuery.data?.data;

  const startEditing = () => {
    if (!vehicle) return;
    const values: Record<string, string> = {};
    for (const k of VEHICLE_EDIT_FIELDS) {
      const raw = (vehicle as unknown as Record<string, string | number | null>)[k];
      values[k] = raw == null ? "" : String(raw);
    }
    setEditValues(values);
    setIsEditing(true);
  };

  // ── Loading / error ────────────────────────────────────────

  if (vehicleQuery.isLoading) {
    return (
      <div aria-busy="true" aria-label="Loading vehicle" className="mx-auto w-full max-w-2xl space-y-3">
        <Skeleton height={20} width="55%" radius="sm" />
        <Skeleton height={14} width="35%" radius="sm" />
        <Skeleton height={96} radius="md" />
        <Skeleton height={96} radius="md" />
      </div>
    );
  }

  if (vehicleQuery.isError || !vehicle) {
    return (
      <div className="mx-auto w-full max-w-2xl">
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">Couldn&apos;t load this vehicle. Check your connection and try again.</p>
          <Button
            variant="default"
            onClick={() => vehicleQuery.refetch()}
            loading={vehicleQuery.isFetching}
            className="max-md:min-h-11"
          >
            Try again
          </Button>
        </div>
        <Link
          href="/dashboard/vehicles"
          className="mt-4 inline-flex min-h-11 items-center gap-1 text-sm font-medium text-accent hover:underline"
        >
          Back to vehicles
        </Link>
      </div>
    );
  }

  const title = vehicleTitle(vehicle);
  const subtitle = vehicleSubtitle(vehicle);
  const registration = registrationLabel(vehicle.registration_renewal_month);
  const value = formatUsd(toNumber(vehicle.current_value));
  const tasks = tasksQuery.data ?? [];
  const openTasks = tasks.filter((t) => t.status === "pending" || t.status === "in_progress");

  // ── Edit mode ──────────────────────────────────────────────

  if (isEditing) {
    // Read the value BEFORE entering the updater: React clears the event by
    // the time a state updater runs, and `currentTarget` is null there.
    const field = (name: string, label: string, props: Record<string, unknown> = {}) => (
      <TextInput
        label={label}
        value={editValues[name] ?? ""}
        onChange={(e) => {
          const v = e.currentTarget.value;
          setEditValues((prev) => ({ ...prev, [name]: v }));
        }}
        {...props}
      />
    );
    return (
      <DetailPageLayout
        entityType="vehicle"
        entityId={vehicleId}
        title={`Edit ${title}`}
        isEditing
        onCancel={() => setIsEditing(false)}
        onSave={() => update.mutate(editValues)}
        isSaving={update.isPending}
      >
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 max-md:[&_.mantine-Input-input]:min-h-11 max-md:[&_.mantine-Input-input]:text-base">
          {field("year", "Year", { inputMode: "numeric", placeholder: "2019", "data-autofocus": true })}
          {field("make", "Make", { placeholder: "Honda" })}
          {field("model", "Model", { placeholder: "Odyssey" })}
          {field("license_plate", "License plate", { placeholder: "8KJD214" })}
          {field("color", "Color", { placeholder: "Blue" })}
          {field("vin", "VIN")}
          {field("current_value", "Current value", { inputMode: "decimal", placeholder: "$" })}
          {field("purchase_price", "Purchase price", { inputMode: "decimal", placeholder: "$" })}
          {field("purchase_date", "Purchase date", { type: "date" })}
        </div>
        <div className="mt-4">
          <TextInput
            label="Notes"
            value={editValues.notes ?? ""}
            onChange={(e) => {
              const v = e.currentTarget.value;
              setEditValues((prev) => ({ ...prev, notes: v }));
            }}
          />
        </div>
        <p className="mt-3 text-xs text-ink-3">
          The loan, registration and insurance are the loans and bills linked to this vehicle. Change
          them under Loans &amp; bills.
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
    <DetailPageLayout entityType="vehicle" entityId={vehicleId} title={title} onEdit={startEditing}>
      <div data-vehicle-detail className="min-w-0">
        {/* The car's head: icon, its name, and the plate/colour line. */}
        <div className="mb-4 flex items-center gap-3.5">
          <span
            aria-hidden
            className="grid size-14 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent"
          >
            <IconCar size={28} stroke={STROKE} />
          </span>
          <div className="min-w-0 flex-1">
            <h1 className="text-xl font-semibold leading-tight text-ink [overflow-wrap:anywhere] md:text-2xl">
              {title}
            </h1>
            {subtitle && <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{subtitle}</p>}
          </div>
          <span className="shrink-0 text-right">
            <span className="block text-xs text-ink-3">Value</span>
            <span className="block text-[15px] font-semibold tabular-nums text-ink">{value ?? "—"}</span>
          </span>
        </div>

        <div className="flex flex-col gap-6">
          {/* 2x2 on a phone, 4-up from 768 px (the detail-page frame). */}
          <dl
            data-vehicle-summary
            className="m-0 grid grid-cols-2 divide-line rounded-token-md border border-line bg-surface shadow-token-sm sm:divide-x md:grid-cols-4"
          >
            <Fact label="Plate" value={vehicle.license_plate || "—"} />
            <Fact label="Colour" value={vehicle.color || "—"} />
            <Fact label="Registration" value={registration ?? "—"} />
            <Fact
              label="Upkeep"
              value={openTasks.length > 0 ? `${openTasks.length} open` : "None open"}
            />
          </dl>

          <section aria-labelledby="vehicle-details" className="min-w-0">
            <h2 id="vehicle-details" className="mb-1 text-sm font-semibold text-ink-2">
              Details
            </h2>
            <dl className="m-0 divide-y divide-line">
              <Row field="year" label="Year" value={vehicle.year ?? "—"} />
              <Row field="make" label="Make" value={vehicle.make || "—"} />
              <Row field="model" label="Model" value={vehicle.model || "—"} />
              <Row field="license_plate" label="License plate" value={vehicle.license_plate || "—"} />
              <Row field="color" label="Color" value={vehicle.color || "—"} />
              <Row
                field="vin"
                label="VIN"
                value={<span className="font-mono text-[13.5px]">{vehicle.vin || "—"}</span>}
              />
              <Row field="notes" label="Notes" value={vehicle.notes || "—"} />
            </dl>
          </section>

          <section aria-labelledby="vehicle-purchase" className="min-w-0">
            <h2 id="vehicle-purchase" className="mb-1 text-sm font-semibold text-ink-2">
              Purchase
            </h2>
            <dl className="m-0 divide-y divide-line">
              <Row field="purchase_price" label="Purchase price" value={formatUsd(toNumber(vehicle.purchase_price)) ?? "—"} />
              <Row field="purchase_date" label="Purchase date" value={fmtDate(vehicle.purchase_date) ?? "—"} />
              <Row field="current_value" label="Current value" value={value ?? "—"} />
            </dl>
          </section>

          {/* The vehicle's upkeep, from maintenance_tasks filtered by vehicle_id. */}
          <section aria-labelledby="vehicle-upkeep" className="min-w-0">
            <div className="mb-1 flex items-baseline justify-between gap-3">
              <h2 id="vehicle-upkeep" className="min-w-0 text-sm font-semibold text-ink-2">
                Upkeep
                {openTasks.length > 0 && (
                  <span className="ml-1.5 font-medium text-ink-3">{openTasks.length}</span>
                )}
              </h2>
              <Link
                href="/dashboard/maintenance"
                className="shrink-0 py-2 text-sm font-semibold text-accent hover:underline"
              >
                All upkeep
              </Link>
            </div>
            {tasksQuery.isLoading && <Skeleton height={56} radius="sm" />}
            {tasksQuery.isError && (
              <p role="alert" className="flex items-center gap-2 text-sm text-warn-ink">
                <IconAlertTriangle size={16} stroke={STROKE} aria-hidden />
                Couldn&apos;t load upkeep for this vehicle.
              </p>
            )}
            {!tasksQuery.isLoading && !tasksQuery.isError && openTasks.length === 0 && (
              <p className="rounded-token-md border border-dashed border-line px-4 py-6 text-center text-sm text-ink-3">
                No upkeep due for this vehicle.
              </p>
            )}
            {!tasksQuery.isLoading && !tasksQuery.isError && openTasks.length > 0 && (
              <ul
                data-vehicle-upkeep
                className="divide-y divide-line rounded-token-md border border-line bg-surface shadow-token-sm"
              >
                {openTasks.map((task) => (
                  <li key={task.id} className="flex min-h-11 items-center gap-3 px-4 py-2.5">
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm font-medium text-ink [overflow-wrap:anywhere]">
                        {task.name}
                      </span>
                      <span className="block text-xs text-ink-3">
                        {task.status === "in_progress" ? "In progress" : "Not started"}
                      </span>
                    </span>
                    {task.due_date && (
                      <span className="shrink-0 text-xs font-medium tabular-nums text-ink-2">
                        {fmtDate(task.due_date)}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>

          {/* The loan, registration and insurance: a loan and bills with this
              vehicle_id (#4704/#4706). Bare here: the section title is above. */}
          <section aria-labelledby="vehicle-finances" className="min-w-0">
            <h2 id="vehicle-finances" className="mb-1 text-sm font-semibold text-ink-2">
              Loans &amp; bills
            </h2>
            <LinkedFinances subject={{ kind: "vehicle", id: vehicleId, name: title }} />
          </section>
        </div>
      </div>
    </DetailPageLayout>
  );
}
