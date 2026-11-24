"use client";

/**
 * Properties list (#4553), mobile-first per the People & House mockup
 * (artifact #89, House > Properties) and the EntityList pattern
 * (KB architecture/entity-list-pattern.md):
 *
 * - Phone (< 768 px): stacked cards, each one link with a chevron and a
 *   44 px+ tap target; nothing clips.
 * - ≥ 768 px: a table in a horizontal scroller, first cell a real link.
 * - "Add property" opens the create modal. There is no /new route.
 * - The monthly total uses the shared money math in
 *   components/property/property-finance.ts, which divides the mortgage rate
 *   (a percent) by 100*12. Dividing by 12 alone is the #4509 bug that showed
 *   ~$86,606/mo instead of ~$1,412.
 *
 * The list API exposes no room/asset/overdue counts, so the cards show the
 * facts it does have (value, monthly cost) rather than inventing numbers.
 */

import { useState } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { Button, Skeleton, Table } from "@mantine/core";
import { IconAlertTriangle, IconChevronRight, IconHome, IconPlus } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import type { PropertyListResponse, PropertyResponse } from "@/types/property";
import AddPropertyModal from "@/components/property/AddPropertyModal";
import {
  formatUsd,
  hasFinancialData,
  monthlyCarryingCost,
  totalMonthlyCarryingCost,
} from "@/components/property/property-finance";

const TYPE_LABEL: Record<string, string> = {
  single_family: "Single family",
  condo: "Condo",
  townhouse: "Townhouse",
  apartment: "Apartment",
  land: "Land",
  multi_family: "Multi-family",
  commercial: "Commercial",
};

function typeLabel(type: string | null): string | null {
  if (!type) return null;
  return TYPE_LABEL[type] ?? type.replace(/_/g, " ");
}

/** The two facts a card shows under the name, from what the API returns. */
function factsFor(p: PropertyResponse): { label: string; value: string }[] {
  const facts: { label: string; value: string }[] = [];
  const value = formatUsd(Number(p.current_value));
  if (p.current_value && value) facts.push({ label: "Value", value });
  const monthly = formatUsd(monthlyCarryingCost(p));
  if (monthly) facts.push({ label: "Monthly", value: `${monthly}/mo` });
  return facts;
}

function PropertyCard({ property }: { property: PropertyResponse }) {
  const facts = factsFor(property);
  const type = typeLabel(property.property_type);
  const sub = [property.address, type].filter(Boolean).join(" · ");

  return (
    <Link
      href={`/dashboard/properties/${property.id}`}
      data-property-card={property.id}
      className="block min-h-11 rounded-token-md border border-line bg-surface shadow-token-sm transition-colors hover:bg-surface-2 active:bg-surface-2"
    >
      <div className="flex items-center gap-3 p-4">
        <span
          aria-hidden
          className="grid size-11 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent"
        >
          <IconHome size={22} stroke={1.75} />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">
            {property.name}
          </h3>
          {sub && <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{sub}</p>}
        </div>
        <IconChevronRight size={20} stroke={1.75} className="shrink-0 text-ink-3" aria-hidden />
      </div>
      {facts.length > 0 && (
        <dl className="flex border-t border-line">
          {facts.map((fact) => (
            <div key={fact.label} className="flex-1 border-l border-line px-4 py-2.5 first:border-l-0">
              <dt className="text-xs text-ink-3">{fact.label}</dt>
              <dd className="text-[15px] font-semibold tabular-nums text-ink">{fact.value}</dd>
            </div>
          ))}
        </dl>
      )}
    </Link>
  );
}

export default function PropertiesPage() {
  const [showAddModal, setShowAddModal] = useState(false);

  const { data, isLoading, isError, refetch, isRefetching } = useQuery({
    queryKey: propertyKeys.lists(),
    queryFn: () => apiFetch<PropertyListResponse>("/api/v1/properties"),
  });

  const properties = data?.data ?? [];
  const monthlyTotal = totalMonthlyCarryingCost(properties);
  const showTotal = !isLoading && !isError && properties.some(hasFinancialData);

  return (
    <div className="mx-auto w-full max-w-6xl">
      {/* Title and Add share a row (Add top-right, as in the mockup's tabrow);
          the description sits under both so it never pushes Add down. */}
      <div className="mb-4 md:mb-6">
        <div className="flex items-center justify-between gap-3">
          <h2 className="min-w-0 text-xl font-semibold text-ink md:text-2xl">
            Properties
            {!isLoading && !isError && properties.length > 0 && (
              <span className="ml-2 align-middle text-base font-medium text-ink-3">{properties.length}</span>
            )}
          </h2>
          <Button
            onClick={() => setShowAddModal(true)}
            leftSection={<IconPlus size={16} />}
            aria-haspopup="dialog"
            className="shrink-0 max-md:min-h-11"
          >
            Add property
          </Button>
        </div>
        <p className="mt-1 text-sm text-ink-3">Your homes and the places the family looks after.</p>
      </div>

      {showTotal && (
        <div className="mb-4 flex items-center gap-4 rounded-token-md border border-line bg-surface p-4 shadow-token-sm md:mb-6 md:w-fit md:pr-8">
          <div>
            <p className="text-xs text-ink-3">Monthly total</p>
            <p className="text-2xl font-semibold tabular-nums text-ink">
              {formatUsd(monthlyTotal)}
              <span className="text-sm font-normal text-ink-3">/mo</span>
            </p>
          </div>
          <div className="h-10 w-px bg-line" aria-hidden />
          <div>
            <p className="text-xs text-ink-3">Properties</p>
            <p className="text-lg font-semibold tabular-nums text-ink-2">{properties.length}</p>
          </div>
        </div>
      )}

      {isLoading && (
        <div role="status" aria-busy="true" aria-label="Loading properties" className="space-y-2">
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
          <IconAlertTriangle size={20} stroke={1.75} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">Couldn&apos;t load properties. Check your connection and try again.</p>
          <Button variant="default" onClick={() => refetch()} loading={isRefetching} className="max-md:min-h-11">
            Try again
          </Button>
        </div>
      )}

      {!isLoading && !isError && properties.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <p className="text-sm text-ink-3">No properties yet. Add your first home to get started.</p>
          <Button
            variant="light"
            onClick={() => setShowAddModal(true)}
            leftSection={<IconPlus size={16} />}
            className="max-md:min-h-11"
          >
            Add property
          </Button>
        </div>
      )}

      {!isLoading && !isError && properties.length > 0 && (
        <>
          <ul aria-label="Properties" className="space-y-2 md:hidden">
            {properties.map((property) => (
              <li key={property.id} className="min-w-0">
                <PropertyCard property={property} />
              </li>
            ))}
          </ul>

          <div
            data-property-table
            className="overflow-x-auto rounded-token-md border border-line bg-surface shadow-token-sm max-md:hidden"
          >
            <Table highlightOnHover verticalSpacing="sm" horizontalSpacing="md" className="min-w-[36rem]">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                    Property
                  </Table.Th>
                  <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                    Address
                  </Table.Th>
                  <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                    Type
                  </Table.Th>
                  <Table.Th scope="col" className="text-right text-xs font-semibold uppercase tracking-wide text-ink-3">
                    Value
                  </Table.Th>
                  <Table.Th scope="col" className="text-right text-xs font-semibold uppercase tracking-wide text-ink-3">
                    Monthly
                  </Table.Th>
                  <Table.Th className="w-10">
                    <span className="sr-only">Open</span>
                  </Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {properties.map((property) => (
                  <Table.Tr key={property.id}>
                    <Table.Td className="min-w-[12rem] max-w-[24rem] font-medium text-ink [overflow-wrap:anywhere]">
                      <Link href={`/dashboard/properties/${property.id}`} className="text-ink hover:underline">
                        {property.name}
                      </Link>
                    </Table.Td>
                    <Table.Td className="text-ink-2 [overflow-wrap:anywhere]">{property.address ?? "—"}</Table.Td>
                    <Table.Td className="whitespace-nowrap text-ink-2">{typeLabel(property.property_type) ?? "—"}</Table.Td>
                    <Table.Td className="whitespace-nowrap text-right tabular-nums text-ink-2">
                      {formatUsd(Number(property.current_value)) ?? "—"}
                    </Table.Td>
                    <Table.Td className="whitespace-nowrap text-right tabular-nums text-ink-2">
                      {formatUsd(monthlyCarryingCost(property)) ?? "—"}
                    </Table.Td>
                    <Table.Td className="text-ink-3">
                      <IconChevronRight size={18} stroke={1.75} aria-hidden />
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </div>
        </>
      )}

      <AddPropertyModal opened={showAddModal} onClose={() => setShowAddModal(false)} />
    </div>
  );
}
