"use client";

import Link from "next/link";
import type { Asset } from "@/lib/types/api";

interface AssetCardProps {
  asset: Asset;
  propertyName?: string;
}

function fmtCurrency(v?: string): string {
  if (!v) return "";
  const n = Number(v);
  if (Number.isNaN(n)) return "";
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
}

export default function AssetCard({ asset, propertyName }: AssetCardProps) {
  const hasWarranty = !!asset.warranty_expiry;
  const warrantyExpired = hasWarranty && new Date(asset.warranty_expiry!) < new Date();
  const price = fmtCurrency(asset.purchase_price);

  return (
    <Link
      href={`/dashboard/assets/${asset.id}`}
      className="block cursor-pointer rounded-xl border border-gray-200 bg-white p-4 shadow-sm transition-all hover:shadow-lg hover:border-indigo-200"
    >
      <div className="flex items-start justify-between">
        <div className="min-w-0 flex-1">
          <h3 className="truncate text-sm font-semibold text-gray-900">{asset.name}</h3>
          <div className="mt-1 flex flex-wrap gap-1.5">
            {asset.category && (
              <span className="inline-flex items-center rounded-full bg-indigo-50 px-2 py-0.5 text-xs font-medium text-indigo-700">{asset.category}</span>
            )}
            {propertyName && (
              <span className="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-600">{propertyName}</span>
            )}
          </div>
        </div>
        {hasWarranty && (
          <span className={`ml-2 flex-shrink-0 inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${warrantyExpired ? "bg-red-50 text-red-700" : "bg-green-50 text-green-700"}`}>
            {warrantyExpired ? "Expired" : "Warranty"}
          </span>
        )}
      </div>

      {(asset.manufacturer || asset.model || price) && (
        <p className="mt-2 text-xs text-gray-400">
          {[asset.manufacturer, asset.model].filter(Boolean).join(" — ")}
          {price && <span className="ml-2 font-medium text-gray-500">{price}</span>}
        </p>
      )}
    </Link>
  );
}
