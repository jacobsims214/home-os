"use client";

import { useState, useCallback } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { assetKeys, propertyKeys } from "@/lib/query-keys";
import AssetCard from "@/components/asset/AssetCard";
import AddAssetModal from "@/components/asset/AddAssetModal";
import Select from "@/components/ui/Select";
import Button from "@/components/ui/Button";
import type { Asset, Property, CreateAssetRequest } from "@/lib/types/api";

export default function AssetsPage() {
  const queryClient = useQueryClient();
  const [propertyFilter, setPropertyFilter] = useState("");
  const [showAddModal, setShowAddModal] = useState(false);

  // Fetch properties for the filter dropdown
  const { data: properties = [], isLoading: propsLoading } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () => apiFetch<{data: Property[]}>("/api/v1/properties").then(r => r.data),
  });

  // Fetch assets, optionally filtered by property
  const {
    data: assets = [],
    isLoading: assetsLoading,
    isError: assetsError,
    error: assetsFetchError,
  } = useQuery({
    queryKey: propertyFilter
      ? assetKeys.byProperty(propertyFilter)
      : assetKeys.all,
    queryFn: () => {
      const params: Record<string, string | undefined> = {};
      if (propertyFilter) params.property_id = propertyFilter;
      return apiFetch<{data: Asset[]}>("/api/v1/assets", { params }).then(r => r.data);
    },
  });

  // Create asset mutation
  const createMutation = useMutation({
    mutationFn: (data: CreateAssetRequest) =>
      apiFetch<Asset>("/api/v1/assets", { method: "POST", body: data }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: assetKeys.all });
    },
  });

  const handleAddAsset = useCallback(
    async (data: CreateAssetRequest) => {
      await createMutation.mutateAsync(data);
    },
    [createMutation],
  );

  // Build a property name lookup for displaying on cards
  const propertyMap = new Map(properties.map((p) => [p.id, p.name]));

  const isLoading = propsLoading || assetsLoading;
  const isError = assetsError;
  const errorMessage =
    assetsFetchError instanceof ApiError
      ? assetsFetchError.message
      : "Failed to load assets";

  // Loading state
  if (isLoading) {
    return (
      <div className="p-6">
        <div className="mb-6">
          <div className="h-7 w-24 animate-pulse rounded bg-gray-200" />
          <div className="mt-2 h-4 w-48 animate-pulse rounded bg-gray-100" />
        </div>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <div
              key={i}
              className="h-28 animate-pulse rounded-lg border border-gray-200 bg-white p-4"
            >
              <div className="h-4 w-3/4 rounded bg-gray-200" />
              <div className="mt-2 h-3 w-1/2 rounded bg-gray-100" />
            </div>
          ))}
        </div>
      </div>
    );
  }

  // Error state
  if (isError) {
    return (
      <div className="flex flex-col items-center justify-center p-12">
        <div className="rounded-lg bg-red-50 p-6 text-center">
          <p className="text-red-700 font-medium">Failed to load assets</p>
          <p className="mt-1 text-sm text-red-600">{errorMessage}</p>
          <Button
            className="mt-4"
            onClick={() =>
              queryClient.invalidateQueries({ queryKey: assetKeys.all })
            }
          >
            Retry
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="p-6">
      {/* Header */}
      <div className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Assets</h1>
          <p className="mt-1 text-sm text-gray-500">
            Track appliances, HVAC systems, electronics, and more
          </p>
        </div>
        <Button onClick={() => setShowAddModal(true)}>
          <svg className="-ml-1 mr-2 h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
          </svg>
          Add Asset
        </Button>
      </div>

      {/* Property filter */}
      <div className="mb-6">
        <Select
          label="Filter by Property"
          value={propertyFilter}
          onChange={(e) => setPropertyFilter(e.target.value)}
          options={properties.map((p) => ({ value: p.id, label: p.name }))}
          placeholder="All properties"
          className="max-w-xs"
        />
      </div>

      {/* Asset list */}
      {assets.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-lg border-2 border-dashed border-gray-300 bg-white p-12 text-center">
          <svg className="h-12 w-12 text-gray-400" fill="none" viewBox="0 0 24 24" strokeWidth={1} stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 10.5V6a3.75 3.75 0 10-7.5 0v4.5m11.356-1.993l1.263 12c.07.665-.45 1.243-1.119 1.243H4.25a1.125 1.125 0 01-1.12-1.243l1.264-12A1.125 1.125 0 015.513 7.5h12.974c.576 0 1.059.435 1.119 1.007zM8.625 10.5a.375.375 0 11-.75 0 .375.375 0 01.75 0zm7.5 0a.375.375 0 11-.75 0 .375.375 0 01.75 0z" />
          </svg>
          <h3 className="mt-4 text-sm font-semibold text-gray-900">
            No assets found
          </h3>
          <p className="mt-1 text-sm text-gray-500">
            {propertyFilter
              ? "No assets match this property filter. Try selecting a different property or clearing the filter."
              : "Get started by adding your first asset."}
          </p>
          {!propertyFilter && (
            <Button className="mt-4" onClick={() => setShowAddModal(true)}>
              <svg className="-ml-1 mr-2 h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
              </svg>
              Add Asset
            </Button>
          )}
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {assets.map((asset) => (
            <AssetCard
              key={asset.id}
              asset={asset}
              propertyName={asset.property_id ? propertyMap.get(asset.property_id) : undefined}
            />
          ))}
        </div>
      )}

      {/* Add Asset Modal */}
      <AddAssetModal
        open={showAddModal}
        onClose={() => setShowAddModal(false)}
        onSubmit={handleAddAsset}
        properties={properties}
      />
    </div>
  );
}
