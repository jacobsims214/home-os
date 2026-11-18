"use client";

/**
 * Vehicles list (#4555), mobile-first per the People & House mockup
 * (artifact #89, House > Vehicles) and the list-page pattern
 * (KB architecture/entity-list-pattern.md):
 *
 * - Phone (< 768 px): stacked cards, each one link with a chevron and a
 *   44 px+ tap target; nothing clips at 375 px.
 * - >= 768 px: a table in a horizontal scroller, first cell a real link.
 * - "Add vehicle" opens the create modal (ResponsiveModal: a bottom sheet on
 *   phones). There is no /new route — see the pattern doc for why.
 *
 * This page used to hand the work to EntityList with its default columns. It
 * does the list itself now because the mockup's vehicle row carries things
 * EntityList's generic card cannot: the car's own heading (year + make +
 * model, since a vehicle has no `name`), the plate and colour under it, and
 * the registration month. EntityList also defaults `propertyFilter` on, which
 * emptied this list (it filters on a column vehicles do not have) — the
 * manager note on #4555.
 *
 * The API exposes no odometer, and a vehicle's maintenance is maintenance_tasks
 * with vehicle_id; the list returns no counts, so the cards show the facts the
 * list does have rather than inventing numbers.
 */

import { useMemo, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton, Table } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconCar, IconChevronRight, IconPlus } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { vehicleKeys } from "@/lib/query-keys";
import { ResponsiveModal, ResponsiveModalActions, TextInput } from "@/components/ui";
import { registrationLabel, vehicleSubtitle, vehicleTitle } from "@/components/vehicle/vehicle-facts";

const STROKE = 1.75;

/** The vehicle list row (apps/api/internal/vehicle/handler.go vehicleResponse). */
interface Vehicle {
  id: string;
  year?: number | null;
  make?: string | null;
  model?: string | null;
  vin?: string | null;
  license_plate?: string | null;
  color?: string | null;
  notes?: string | null;
  current_value?: string | null;
  registration_renewal_month?: number | null;
}

function VehicleCard({ vehicle }: { vehicle: Vehicle }) {
  const subtitle = vehicleSubtitle(vehicle);
  // The registration month is the only upkeep fact the list carries; it is a
  // month, so the tag says "Renews in March" and never invents a day.
  const registration = registrationLabel(vehicle.registration_renewal_month);

  return (
    <Link
      href={`/dashboard/vehicles/${vehicle.id}`}
      data-vehicle-card={vehicle.id}
      className="block min-h-11 rounded-token-md border border-line bg-surface shadow-token-sm transition-colors hover:bg-surface-2 active:bg-surface-2"
    >
      <div className="flex items-center gap-3 p-4">
        <span
          aria-hidden
          className="grid size-11 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent"
        >
          <IconCar size={22} stroke={STROKE} />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">
            {vehicleTitle(vehicle)}
          </h3>
          {subtitle && <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{subtitle}</p>}
        </div>
        <IconChevronRight size={20} stroke={STROKE} className="shrink-0 text-ink-3" aria-hidden />
      </div>
      {registration && (
        <div className="border-t border-line px-4 py-2.5">
          <span className="inline-flex items-center gap-1.5 rounded-full bg-surface-2 px-2.5 py-1 text-xs font-medium text-ink-2">
            {registration}
          </span>
        </div>
      )}
    </Link>
  );
}

/** The built-in "Add vehicle" modal: a year, a make and a model is a car. */
function AddVehicleModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [year, setYear] = useState("");
  const [make, setMake] = useState("");
  const [model, setModel] = useState("");
  const [plate, setPlate] = useState("");
  const [error, setError] = useState<string | null>(null);

  const close = () => {
    setYear("");
    setMake("");
    setModel("");
    setPlate("");
    setError(null);
    create.reset();
    onClose();
  };

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Vehicle }>("/api/v1/vehicles", { method: "POST", body }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: vehicleKeys.all });
      notifications.show({ message: "Vehicle added", color: "teal" });
      const id = res?.data?.id;
      if (!id) return close();
      // Navigate without closing first: closing pops the modal's history entry,
      // and the router RESTORE that triggers cancels the push (#4532).
      router.replace(`/dashboard/vehicles/${id}`);
    },
    onError: (err: Error) => setError(err.message || "Couldn't add the vehicle. Try again."),
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const y = Number(year);
    if (year.trim() === "" || !Number.isInteger(y) || y < 1900 || y > 2100) {
      setError("Give the year as a whole number, like 2019.");
      return;
    }
    if (!make.trim() || !model.trim()) {
      setError("A make and a model name the car, like Honda and Odyssey.");
      return;
    }
    setError(null);
    // year is *int in the API: send a number, never a string (a string is a 400).
    create.mutate({
      year: y,
      make: make.trim(),
      model: model.trim(),
      license_plate: plate.trim() || undefined,
    });
  };

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add vehicle">
      <form onSubmit={submit} noValidate>
        <div className="flex flex-col gap-3">
          <TextInput
            label="Year"
            required
            data-autofocus
            inputMode="numeric"
            placeholder="2019"
            value={year}
            onChange={(e) => setYear(e.currentTarget.value)}
          />
          <TextInput
            label="Make"
            required
            placeholder="Honda"
            value={make}
            onChange={(e) => setMake(e.currentTarget.value)}
          />
          <TextInput
            label="Model"
            required
            placeholder="Odyssey"
            value={model}
            onChange={(e) => setModel(e.currentTarget.value)}
          />
          <TextInput
            label="License plate"
            placeholder="8KJD214"
            description="Optional. Shown under the car's name."
            value={plate}
            onChange={(e) => setPlate(e.currentTarget.value)}
          />
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
        </div>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Add vehicle
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

export default function VehiclesPage() {
  const [showAddModal, setShowAddModal] = useState(false);

  const { data, isLoading, isError, refetch, isRefetching } = useQuery({
    queryKey: vehicleKeys.lists(),
    queryFn: () => apiFetch<{ data: Vehicle[] }>("/api/v1/vehicles"),
  });

  const vehicles = useMemo(() => data?.data ?? [], [data]);

  return (
    <div className="mx-auto w-full max-w-6xl">
      {/* Title and Add share a row (Add top-right, as in the mockup); the
          description sits under both so it never pushes Add down. */}
      <div className="mb-4 md:mb-6">
        <div className="flex items-center justify-between gap-3">
          <h2 className="min-w-0 text-xl font-semibold text-ink md:text-2xl">
            Vehicles
            {!isLoading && !isError && vehicles.length > 0 && (
              <span className="ml-2 align-middle text-base font-medium text-ink-3">{vehicles.length}</span>
            )}
          </h2>
          <Button
            onClick={() => setShowAddModal(true)}
            leftSection={<IconPlus size={16} />}
            aria-haspopup="dialog"
            className="shrink-0 max-md:min-h-11"
          >
            Add vehicle
          </Button>
        </div>
        <p className="mt-1 text-sm text-ink-3">The cars the family drives, and what they cost to keep on the road.</p>
      </div>

      {isLoading && (
        <div aria-busy="true" aria-label="Loading vehicles" className="space-y-2">
          {[1, 2, 3].map((i) => (
            <div key={i} className="space-y-2 rounded-token-md border border-line bg-surface p-4 shadow-token-sm">
              <Skeleton height={14} width="55%" radius="sm" />
              <Skeleton height={12} width="80%" radius="sm" />
            </div>
          ))}
        </div>
      )}

      {isError && (
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={1.75} className="shrink-0" />
          <p className="flex-1 text-sm">Couldn&apos;t load vehicles. Check your connection and try again.</p>
          <Button variant="default" onClick={() => refetch()} loading={isRefetching} className="max-md:min-h-11">
            Try again
          </Button>
        </div>
      )}

      {!isLoading && !isError && vehicles.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <p className="text-sm text-ink-3">No vehicles yet. Add your first car to track registration and upkeep.</p>
          <Button
            variant="light"
            onClick={() => setShowAddModal(true)}
            leftSection={<IconPlus size={16} />}
            className="max-md:min-h-11"
          >
            Add vehicle
          </Button>
        </div>
      )}

      {!isLoading && !isError && vehicles.length > 0 && (
        <>
          <ul aria-label="Vehicles" className="space-y-2 md:hidden">
            {vehicles.map((vehicle) => (
              <li key={vehicle.id} className="min-w-0">
                <VehicleCard vehicle={vehicle} />
              </li>
            ))}
          </ul>

          <div
            data-vehicle-table
            className="overflow-x-auto rounded-token-md border border-line bg-surface shadow-token-sm max-md:hidden"
          >
            <Table highlightOnHover verticalSpacing="sm" horizontalSpacing="md" className="min-w-[36rem]">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                    Vehicle
                  </Table.Th>
                  <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                    Plate
                  </Table.Th>
                  <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                    Colour
                  </Table.Th>
                  <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                    Registration
                  </Table.Th>
                  <Table.Th className="w-10">
                    <span className="sr-only">Open</span>
                  </Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {vehicles.map((vehicle) => (
                  <Table.Tr key={vehicle.id}>
                    <Table.Td className="min-w-[12rem] max-w-[24rem] font-medium text-ink [overflow-wrap:anywhere]">
                      <Link href={`/dashboard/vehicles/${vehicle.id}`} className="text-ink hover:underline">
                        {vehicleTitle(vehicle)}
                      </Link>
                    </Table.Td>
                    <Table.Td className="whitespace-nowrap text-ink-2">{vehicle.license_plate ?? "—"}</Table.Td>
                    <Table.Td className="text-ink-2">{vehicle.color ?? "—"}</Table.Td>
                    <Table.Td className="whitespace-nowrap text-ink-2">
                      {registrationLabel(vehicle.registration_renewal_month) ?? "—"}
                    </Table.Td>
                    <Table.Td className="text-ink-3">
                      <IconChevronRight size={18} stroke={STROKE} aria-hidden />
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </div>
        </>
      )}

      <AddVehicleModal opened={showAddModal} onClose={() => setShowAddModal(false)} />
    </div>
  );
}
