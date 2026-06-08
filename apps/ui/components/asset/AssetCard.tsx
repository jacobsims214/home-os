"use client";

import Link from "next/link";
import { cn } from "@/lib/cn";
import type { Asset } from "@/lib/types/api";

interface AssetCardProps {
  asset: Asset;
  propertyName?: string;
}

/**
 * A card showing asset summary: name, category, property, and warranty status.
 * Clicking navigates to the asset detail page.
 */
export default function AssetCard({ asset, propertyName }: AssetCardProps) {
  const hasWarranty = !!asset.warranty_expiry;
  const warrantyExpired = hasWarranty && new Date(asset.warranty_expiry!) < new Date();

  return (
    <Link
      href={`/dashboard/assets/${asset.id}`}
      className="block rounded-lg border border-gray-200 bg-white p-4 shadow-sm transition-shadow hover:shadow-md"
    >
      <div className="flex items-start justify-between">
        <div className="min-w-0 flex-1">
          <h3 className="truncate text-sm font-semibold text-gray-900">
            {asset.name}
          </h3>
          <div className="mt-1 flex flex-wrap gap-2 text-xs text-gray-500">
            {asset.category && (
              <span className="inline-flex items-center rounded-full bg-indigo-50 px-2 py-0.5 text-indigo-700">
                {asset.category}
              </span>
            )}
            {propertyName && (
              <span className="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-gray-600">
                {propertyName}
              </span>
            )}
          </div>
        </div>
        <div className="ml-3 flex-shrink-0">
          {hasWarranty ? (
            <span
              className={cn(
                "inline-flex items-center rounded-full px-2 py-1 text-xs font-medium",
                warrantyExpired
                  ? "bg-red-50 text-red-700"
                  : "bg-green-50 text-green-700",
              )}
            >
              {warrantyExpired ? "Warranty Expired" : "Under Warranty"}
            </span>
          ) : null}
        </div>
      </div>

      {asset.manufacturer && (
        <p className="mt-2 text-xs text-gray-400">
          {[asset.manufacturer, asset.model].filter(Boolean).join(" — ")}
        </p>
      )}
    </Link>
  );
}
