"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import type { PropertyListResponse } from "@/types/property";
import PropertyCard from "@/components/property/PropertyCard";
import AddPropertyModal from "@/components/property/AddPropertyModal";
import Button from "@/components/ui/Button";

export default function PropertiesPage() {
  const [showAddModal, setShowAddModal] = useState(false);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: propertyKeys.lists(),
    queryFn: () => apiFetch<PropertyListResponse>("/api/v1/properties"),
  });

  const properties = data?.data ?? [];

  return (
    <div className="mx-auto max-w-4xl px-4 py-6 sm:px-6 lg:px-8">
      {/* Page header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-gray-900">
            Properties
          </h1>
          <p className="mt-1 text-sm text-gray-500">
            Manage your homes and properties.
          </p>
        </div>
        <Button onClick={() => setShowAddModal(true)}>Add Property</Button>
      </div>

      {/* Loading state */}
      {isLoading && (
        <div className="mt-8 flex items-center justify-center py-12">
          <svg
            className="h-6 w-6 animate-spin text-indigo-600"
            fill="none"
            viewBox="0 0 24 24"
            aria-hidden="true"
          >
            <circle
              className="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              strokeWidth="4"
            />
            <path
              className="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
            />
          </svg>
          <span className="ml-3 text-sm text-gray-500">Loading properties...</span>
        </div>
      )}

      {/* Error state */}
      {isError && (
        <div className="mt-8 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
          Failed to load properties. {(error as Error)?.message}
        </div>
      )}

      {/* Empty state */}
      {!isLoading && !isError && properties.length === 0 && (
        <div className="mt-8 rounded-lg border-2 border-dashed border-gray-300 py-12 text-center">
          <svg
            className="mx-auto h-12 w-12 text-gray-400"
            fill="none"
            viewBox="0 0 24 24"
            strokeWidth={1}
            stroke="currentColor"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M8.25 21v-4.875c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125V21m0 0h4.5V3.545M12.75 21h7.5V10.75M2.25 21h1.5m18 0h-18M2.25 9l4.5-1.636M18.75 3l-1.5.545m0 6.205l3 1m1.5.5l-1.5-.5M6.75 7.364V3h-3v18m3-13.636l10.5-3.819"
            />
          </svg>
          <h3 className="mt-4 text-sm font-semibold text-gray-900">
            No properties yet
          </h3>
          <p className="mt-1 text-sm text-gray-500">
            Add your first property to get started.
          </p>
          <div className="mt-6">
            <Button onClick={() => setShowAddModal(true)}>
              Add Property
            </Button>
          </div>
        </div>
      )}

      {/* Property cards grid */}
      {!isLoading && !isError && properties.length > 0 && (
        <div className="mt-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {properties.map((property) => (
            <PropertyCard key={property.id} property={property} />
          ))}
        </div>
      )}

      {/* Add Property Modal */}
      <AddPropertyModal
        open={showAddModal}
        onClose={() => setShowAddModal(false)}
      />
    </div>
  );
}
