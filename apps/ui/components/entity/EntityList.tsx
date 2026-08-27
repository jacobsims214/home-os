"use client";

import { useState } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import {
  Table,
  Card,
  TextInput,
  Select,
  Button,
  Skeleton,
  Text,
  Group,
  Paper,
  Badge,
} from "@mantine/core";
import { IconPlus, IconX } from "@tabler/icons-react";
import type { Property } from "@/lib/types/api";

/** Generic entity shape for the entity list — all entities have at least id, name/title, and property_id. */
interface EntityRecord {
  id: string;
  name?: string;
  title?: string;
  property_id?: string;
  amount?: string | number;
  [key: string]: unknown;
}

interface EntityColumn {
  name: string;
  label: string;
  format?: "currency" | "date" | "badge";
}

interface EntityListProps {
  entityType: string;
  title: string;
  description: string;
  columns: EntityColumn[];
  showMonthlyTotal?: boolean;
  cardView?: boolean;
  propertyFilter?: boolean;
}

function formatValue(value: string | number | null, format?: string): string {
  if (value == null) return "—";
  if (format === "currency") {
    const n = Number(value);
    if (Number.isNaN(n)) return "—";
    return n.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
  }
  if (format === "date") {
    try {
      return new Date(String(value)).toLocaleDateString();
    } catch {
      return String(value);
    }
  }
  if (format === "badge") {
    return String(value);
  }
  return String(value);
}

export default function EntityList({
  entityType,
  title,
  description,
  columns,
  showMonthlyTotal = false,
  cardView = false,
  propertyFilter = true,
}: EntityListProps) {
  const [filterValue, setFilterValue] = useState("");

  const { data: propertiesData } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () => apiFetch<{ data: Property[] }>("/api/v1/properties"),
  });
  const properties = propertiesData?.data ?? [];

  const { data: entitiesData, isLoading } = useQuery({
    queryKey: [entityType],
    queryFn: () => apiFetch<{ data: EntityRecord[] }>(`/api/v1/${entityType}s`),
  });
  const allEntities = entitiesData?.data ?? [];
  const entities = filterValue ? allEntities.filter((e) => e.property_id === filterValue) : allEntities;

  const propertyMap = new Map(properties.map((p) => [p.id, p.name]));

  const monthlyTotal = showMonthlyTotal
    ? entities.reduce((sum: number, e: EntityRecord) => sum + (Number(e.amount) || 0), 0)
    : null;

  return (
    <div className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">{title}</h1>
          <p className="mt-1 text-sm text-gray-500">{description}</p>
        </div>
        <Link href={`/dashboard/${entityType}s/new`}>
          <Button leftSection={<IconPlus size={16} />}>Add {title}</Button>
        </Link>
      </div>

      {propertyFilter && (
        <div className="mb-4">
          <Select
            label="Filter by property"
            value={filterValue}
            onChange={(val) => setFilterValue(val || "")}
            data={[{ value: "", label: "All properties" }, ...properties.map((p) => ({ value: p.id, label: p.name }))]}
            placeholder="Filter by property"
            className="max-w-xs"
            clearable
            searchable
          />
        </div>
      )}

      {showMonthlyTotal && monthlyTotal !== null && (
        <div className="mb-6 flex items-center gap-4 rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
          <div>
            <p className="text-xs text-gray-400">Monthly Total</p>
            <p className="text-2xl font-bold text-gray-900">
              {monthlyTotal.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 })}
              <span className="text-sm font-normal text-gray-400">/mo</span>
            </p>
          </div>
          <div className="h-10 w-px bg-gray-200" />
          <div>
            <p className="text-xs text-gray-400">Count</p>
            <p className="text-lg font-semibold text-gray-700">{entities.length}</p>
          </div>
        </div>
      )}

      {isLoading && (
        <div className={cardView ? "grid gap-4 sm:grid-cols-2 lg:grid-cols-3" : "space-y-2"}>
          {[1, 2, 3].map((i) => (
            <div key={i} className={cardView ? "rounded-xl border border-gray-200 bg-white p-4 shadow-sm" : "flex items-center gap-4 rounded-xl border border-gray-200 bg-white p-4 shadow-sm"}>
              <Skeleton key={i} height={cardView ? 180 : 56} radius="sm" />
              {!cardView && (
                <div className="flex-1 space-y-2">
                  <Skeleton height={12} width="80%" radius="sm" />
                  <Skeleton height={12} width="60%" radius="sm" />
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {!isLoading && entities.length === 0 && (
        <div className="flex flex-col items-center justify-center rounded-lg border-2 border-dashed border-gray-300 py-12 text-center">
          <p className="text-sm text-gray-500">No {title.toLowerCase()} yet.</p>
        </div>
      )}

      {!isLoading && entities.length > 0 && (
        <>
          {cardView ? (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {entities.map((entity) => (
                <Link
                  key={entity.id}
                  href={`/dashboard/${entityType}s/${entity.id}`}
                  className="block cursor-pointer rounded-xl border border-gray-200 bg-white p-4 shadow-sm transition-all hover:shadow-lg hover:border-indigo-200"
                >
                  <h3 className="text-sm font-semibold text-gray-900">{entity.name}</h3>
                  <div className="mt-3 space-y-1 text-xs text-gray-500">
                    {columns.slice(0, 3).map((col) => (
                      <div key={col.name} className="flex justify-between">
                        <dt>{col.label}</dt>
                        <dd className="font-medium text-gray-700">{formatValue(entity[col.name] as string | number | null, col.format)}</dd>
                      </div>
                    ))}
                  </div>
                </Link>
              ))}
            </div>
          ) : (
            <div className="overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm">
              <Table highlightOnHover>
                <thead>
                  <tr>
                    {columns.map((col) => (
                      <th key={col.name} className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase">
                        {col.label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {entities.map((entity) => (
                    <tr
                      key={entity.id}
                      onClick={() => (window.location.href = `/dashboard/${entityType}s/${entity.id}`)}
                      className="hover:bg-gray-50"
                      style={{ cursor: "pointer" }}
                    >
                      {columns.map((col) => (
                        <td key={col.name} className="px-4 py-3">
                          <span className={col.format === "badge" ? "text-gray-900" : "text-gray-600"}>
                            {formatValue(entity[col.name] as string | number | null, col.format)}
                          </span>
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
                {showMonthlyTotal && monthlyTotal !== null && (
                  <tfoot>
                    <tr>
                      <td colSpan={columns.length - 1} className="px-4 py-3 text-right text-sm font-medium text-gray-500">
                        Monthly Total
                      </td>
                      <td className="px-4 py-3 text-right text-sm font-bold text-gray-900">
                        {monthlyTotal.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 })}
                      </td>
                    </tr>
                  </tfoot>
                )}
              </Table>
            </div>
          )}
        </>
      )}
    </div>
  );
}
