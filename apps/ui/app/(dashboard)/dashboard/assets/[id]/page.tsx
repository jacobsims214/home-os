"use client";

import { useParams } from "next/navigation";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { assetKeys } from "@/lib/query-keys";
import Button from "@/components/ui/Button";
import type { Asset } from "@/lib/types/api";

export default function AssetDetailPage() {
  const params = useParams();
  const id = params.id as string;

  const {
    data: asset,
    isLoading,
    isError,
    error,
  } = useQuery({
    queryKey: assetKeys.detail(id),
    queryFn: () => apiFetch<Asset>(`/api/v1/assets/${id}`),
    enabled: !!id,
  });

  // Loading state
  if (isLoading) {
    return (
      <div className="p-6">
        <div className="mb-6">
          <div className="h-5 w-16 animate-pulse rounded bg-gray-200" />
        </div>
        <div className="overflow-hidden rounded-lg border border-gray-200 bg-white">
          <div className="p-6 space-y-4">
            {[1, 2, 3, 4, 5].map((i) => (
              <div key={i}>
                <div className="h-3 w-20 animate-pulse rounded bg-gray-100" />
                <div className="mt-1 h-5 w-48 animate-pulse rounded bg-gray-200" />
              </div>
            ))}
          </div>
        </div>
      </div>
    );
  }

  // Error state
  if (isError) {
    const message =
      error instanceof ApiError
        ? error.status === 404
          ? "Asset not found"
          : error.message
        : "Failed to load asset";

    return (
      <div className="flex flex-col items-center justify-center p-12">
        <div className="rounded-lg bg-red-50 p-6 text-center">
          <p className="text-red-700 font-medium">{message}</p>
          <Link href="/dashboard/assets">
            <Button className="mt-4">Back to Assets</Button>
          </Link>
        </div>
      </div>
    );
  }

  // No data (shouldn't happen if not loading/error, but handle gracefully)
  if (!asset) {
    return (
      <div className="p-6">
        <Link
          href="/dashboard/assets"
          className="text-sm text-indigo-600 hover:text-indigo-500"
        >
          &larr; Back to Assets
        </Link>
        <p className="mt-6 text-gray-500">Asset data unavailable.</p>
      </div>
    );
  }

  return (
    <div className="p-6">
      {/* Back navigation */}
      <Link
        href="/dashboard/assets"
        className="mb-6 inline-flex items-center text-sm text-indigo-600 hover:text-indigo-500"
      >
        <svg className="mr-1 h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" d="M10.5 19.5L3 12m0 0l7.5-7.5M3 12h18" />
        </svg>
        Back to Assets
      </Link>

      {/* Asset detail card */}
      <div className="overflow-hidden rounded-lg border border-gray-200 bg-white">
        {/* Header */}
        <div className="border-b border-gray-200 px-6 py-4">
          <div className="flex items-start justify-between">
            <div>
              <h1 className="text-xl font-bold text-gray-900">{asset.name}</h1>
              {asset.category && (
                <span className="mt-1 inline-flex items-center rounded-full bg-indigo-50 px-2.5 py-0.5 text-xs font-medium text-indigo-700">
                  {asset.category}
                </span>
              )}
            </div>
            {asset.warranty_expiry && (
              <WarrantyBadge expiry={asset.warranty_expiry} />
            )}
          </div>
        </div>

        {/* Fields */}
        <div className="px-6 py-4">
          <dl className="grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-2">
            <Field label="Manufacturer" value={asset.manufacturer} />
            <Field label="Model" value={asset.model} />
            <Field label="Serial Number" value={asset.serial_number} />
            <Field label="Category" value={asset.category} />
            <Field label="Purchase Date" value={asset.purchase_date} />
            <Field label="Purchase Price" value={asset.purchase_price ? `$${asset.purchase_price}` : undefined} />
            <Field label="Warranty Expiry" value={asset.warranty_expiry} />
            <Field label="Created" value={formatDate(asset.created_at)} />
            <Field label="Last Updated" value={formatDate(asset.updated_at)} />
          </dl>

          {asset.notes && (
            <div className="mt-6 border-t border-gray-100 pt-4">
              <dt className="text-xs font-medium text-gray-500">Notes</dt>
              <dd className="mt-1 text-sm text-gray-900 whitespace-pre-wrap">
                {asset.notes}
              </dd>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

/** Renders a single detail field (label + value or placeholder). */
function Field({ label, value }: { label: string; value?: string }) {
  return (
    <div>
      <dt className="text-xs font-medium text-gray-500">{label}</dt>
      <dd className="mt-1 text-sm text-gray-900">
        {value ?? <span className="text-gray-400">—</span>}
      </dd>
    </div>
  );
}

/** Colour-coded warranty badge. */
function WarrantyBadge({ expiry }: { expiry: string }) {
  const expired = new Date(expiry) < new Date();
  return (
    <span
      className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${
        expired
          ? "bg-red-50 text-red-700"
          : "bg-green-50 text-green-700"
      }`}
    >
      {expired ? "Warranty Expired" : `Under Warranty until ${expiry}`}
    </span>
  );
}

/** Formats an ISO timestamp into a human-readable date. */
function formatDate(iso: string): string {
  try {
    return new Date(iso).toLocaleDateString("en-US", {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  } catch {
    return iso;
  }
}
