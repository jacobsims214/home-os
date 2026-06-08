"use client";

import { useState, useMemo } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { searchKeys, propertyKeys } from "@/lib/query-keys";
import Select from "@/components/ui/Select";
import type { SearchResult, SearchResponse, Property } from "@/lib/types/api";

interface EntityTypeConfig {
  label: string;
  icon: string;
  /** Route resolver — return null if no detail page exists */
  route: (id: string) => string | null;
}

const ENTITY_TYPES: Record<string, EntityTypeConfig> = {
  property: {
    label: "Properties",
    icon: "🏠",
    route: (id) => `/dashboard/properties/${id}`,
  },
  asset: {
    label: "Assets",
    icon: "📦",
    route: (id) => `/dashboard/assets/${id}`,
  },
  maintenance: {
    label: "Maintenance",
    icon: "🔧",
    route: () => `/dashboard/maintenance`,
  },
  vehicle: {
    label: "Vehicles",
    icon: "🚗",
    route: () => `/dashboard/vehicles`,
  },
  pet: {
    label: "Pets",
    icon: "🐾",
    route: () => `/dashboard/pets`,
  },
  vendor: {
    label: "Vendors",
    icon: "🏢",
    route: () => `/dashboard/vendors`,
  },
  bill: {
    label: "Bills",
    icon: "💰",
    route: () => `/dashboard/bills`,
  },
};

const FILTER_TYPES = [
  { value: "", label: "All" },
  { value: "property", label: "Properties" },
  { value: "asset", label: "Assets" },
  { value: "maintenance", label: "Maintenance" },
  { value: "vehicle", label: "Vehicles" },
  { value: "pet", label: "Pets" },
  { value: "vendor", label: "Vendors" },
  { value: "bill", label: "Bills" },
];

/** Truncate body text to a reasonable snippet length */
function snippet(text: string, maxLen = 120): string {
  if (text.length <= maxLen) return text;
  return text.slice(0, maxLen).replace(/\s+\S*$/, "") + "…";
}

export default function SearchPage() {
  const searchParams = useSearchParams();
  const router = useRouter();

  const q = searchParams.get("q") ?? "";
  const [typeFilter, setTypeFilter] = useState("");
  const [propertyFilter, setPropertyFilter] = useState("");

  // Fetch properties for the filter dropdown
  const { data: properties = [] } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () =>
      apiFetch<{ data: Property[] }>("/api/v1/properties").then((r) => r.data),
    staleTime: 30_000,
  });

  // Fetch search results
  const {
    data: results = [],
    isLoading,
    isError,
    error,
  } = useQuery({
    queryKey: searchKeys.results(q, typeFilter, propertyFilter),
    queryFn: () =>
      apiFetch<SearchResponse>("/api/v1/search", {
        params: {
          q,
          type: typeFilter || undefined,
          property_id: propertyFilter || undefined,
        },
      }).then((r) => r.results),
    enabled: q.length > 0,
  });

  const errorMessage =
    error instanceof ApiError ? error.message : "Search failed";

  // Group results by entity_type, preserving insertion order
  const grouped = useMemo(() => {
    const map = new Map<string, SearchResult[]>();
    for (const r of results) {
      const group = map.get(r.entity_type);
      if (group) {
        group.push(r);
      } else {
        map.set(r.entity_type, [r]);
      }
    }
    return map;
  }, [results]);

  // --- Empty query state (no ?q= in URL) ---
  if (!q) {
    return (
      <div className="p-6">
        <h1 className="text-2xl font-bold text-gray-900">Search</h1>
        <p className="mt-1 text-sm text-gray-500">
          Find anything across your Home OS — properties, assets, maintenance
          tasks, vehicles, and more.
        </p>
        <div className="mt-12 flex flex-col items-center justify-center text-center">
          <svg
            className="h-16 w-16 text-gray-300"
            fill="none"
            viewBox="0 0 24 24"
            strokeWidth={1}
            stroke="currentColor"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M21 21l-5.197-5.197m0 0A7.5 7.5 0 105.196 5.196a7.5 7.5 0 0010.607 10.607z"
            />
          </svg>
          <h3 className="mt-4 text-sm font-semibold text-gray-900">
            Start typing to search
          </h3>
          <p className="mt-1 text-sm text-gray-500">
            Use the search box in the sidebar or press{" "}
            <kbd className="rounded border border-gray-300 bg-gray-100 px-1.5 py-0.5 text-xs font-mono">
              Cmd+K
            </kbd>
          </p>
        </div>
      </div>
    );
  }

  // --- Loading state ---
  if (isLoading) {
    return (
      <div className="p-6">
        <div className="mb-6">
          <h1 className="text-2xl font-bold text-gray-900">Search</h1>
        </div>
        {/* Skeleton rows */}
        <div className="space-y-4">
          {[1, 2, 3, 4].map((i) => (
            <div
              key={i}
              className="animate-pulse rounded-lg border border-gray-200 bg-white p-4"
            >
              <div className="h-4 w-3/4 rounded bg-gray-200" />
              <div className="mt-2 h-3 w-full rounded bg-gray-100" />
            </div>
          ))}
        </div>
      </div>
    );
  }

  // --- Error state ---
  if (isError) {
    return (
      <div className="p-6">
        <h1 className="text-2xl font-bold text-gray-900">Search</h1>
        <div className="mt-12 flex flex-col items-center justify-center">
          <div className="rounded-lg bg-red-50 p-6 text-center">
            <p className="font-medium text-red-700">Search failed</p>
            <p className="mt-1 text-sm text-red-600">{errorMessage}</p>
            <button
              onClick={() => window.location.reload()}
              className="mt-4 inline-flex items-center rounded-md bg-red-600 px-4 py-2 text-sm font-semibold text-white hover:bg-red-500 transition-colors"
            >
              Retry
            </button>
          </div>
        </div>
      </div>
    );
  }

  // --- Results ---
  return (
    <div className="p-6">
      {/* Header */}
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-gray-900">Search</h1>
        <p className="mt-1 text-sm text-gray-500">
          {results.length > 0
            ? `${results.length} result${results.length === 1 ? "" : "s"} for "${q}"`
            : `No results for "${q}"`}
        </p>
      </div>

      {/* Filters row */}
      <div className="mb-6 flex flex-wrap items-center gap-3">
        {/* Type filter chips */}
        <div className="flex flex-wrap gap-1.5">
          {FILTER_TYPES.map((ft) => (
            <button
              key={ft.value}
              onClick={() => setTypeFilter(ft.value)}
              className={`rounded-full px-3 py-1 text-xs font-medium transition-colors ${
                typeFilter === ft.value
                  ? "bg-indigo-600 text-white"
                  : "bg-gray-100 text-gray-600 hover:bg-gray-200"
              }`}
            >
              {ft.label}
            </button>
          ))}
        </div>

        {/* Property filter dropdown */}
        {properties.length > 0 && (
          <div className="ml-auto">
            <Select
              label=""
              value={propertyFilter}
              onChange={(e) => setPropertyFilter(e.target.value)}
              options={properties.map((p) => ({
                value: p.id,
                label: p.name,
              }))}
              placeholder="All Properties"
              className="w-48"
            />
          </div>
        )}
      </div>

      {/* Empty results state */}
      {results.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-lg border-2 border-dashed border-gray-300 bg-white p-12 text-center">
          <svg
            className="h-12 w-12 text-gray-400"
            fill="none"
            viewBox="0 0 24 24"
            strokeWidth={1}
            stroke="currentColor"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M21 21l-5.197-5.197m0 0A7.5 7.5 0 105.196 5.196a7.5 7.5 0 0010.607 10.607z"
            />
          </svg>
          <h3 className="mt-4 text-sm font-semibold text-gray-900">
            No results found
          </h3>
          <p className="mt-1 text-sm text-gray-500">
            Try a different search term or clear your filters.
          </p>
        </div>
      ) : (
        /* Results grouped by entity type */
        <div className="space-y-6">
          {Array.from(grouped.entries()).map(([entityType, items]) => {
            const config = ENTITY_TYPES[entityType];
            return (
              <section key={entityType}>
                <h2 className="mb-3 flex items-center gap-2 text-sm font-semibold uppercase tracking-wider text-gray-500">
                  <span>{config?.icon ?? "📄"}</span>
                  {config?.label ?? entityType}
                  <span className="ml-1 font-normal normal-case text-gray-400">
                    ({items.length})
                  </span>
                </h2>
                <ul className="space-y-2">
                  {items.map((result) => {
                    const href = config?.route(result.entity_id);
                    const content = (
                      <div className="rounded-lg border border-gray-200 bg-white p-4 transition-colors hover:border-indigo-300 hover:shadow-sm">
                        <h3 className="text-sm font-semibold text-gray-900">
                          {result.title}
                        </h3>
                        {result.body && (
                          <p className="mt-1 text-sm text-gray-500 line-clamp-2">
                            {snippet(result.body)}
                          </p>
                        )}
                        {config && (
                          <span className="mt-2 inline-block rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-500">
                            {config.label}
                          </span>
                        )}
                      </div>
                    );

                    if (href) {
                      return (
                        <li key={result.entity_id}>
                          <Link href={href}>{content}</Link>
                        </li>
                      );
                    }
                    return <li key={result.entity_id}>{content}</li>;
                  })}
                </ul>
              </section>
            );
          })}
        </div>
      )}
    </div>
  );
}
