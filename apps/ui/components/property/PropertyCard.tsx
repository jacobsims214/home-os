"use client";

import Link from "next/link";
import type { PropertyResponse } from "@/types/property";

interface PropertyCardProps {
  property: PropertyResponse;
  /** Optional room count to display. Not fetched by the card itself. */
  roomCount?: number;
  /** Optional asset count to display as a badge. */
  assetCount?: number;
  /** Optional maintenance task count to display as a badge. */
  maintenanceCount?: number;
}

// Full Tailwind class strings keyed by property_type. Strings must be literal
// for Tailwind's content scanner to include them in the build.
const ACCENT_BY_TYPE: Record<string, string> = {
  single_family: "bg-indigo-500",
  condo: "bg-teal-500",
  townhouse: "bg-blue-500",
  apartment: "bg-purple-500",
  land: "bg-amber-500",
  multi_family: "bg-emerald-500",
  commercial: "bg-rose-500",
};

const DEFAULT_ACCENT = "bg-gray-400";

function accentClass(propertyType: string | null | undefined): string {
  if (!propertyType) return DEFAULT_ACCENT;
  return ACCENT_BY_TYPE[propertyType] ?? DEFAULT_ACCENT;
}

const currencyFormatter = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  maximumFractionDigits: 0,
});

function formatCurrency(value: string | null | undefined): string | null {
  if (!value) return null;
  const n = Number(value);
  return Number.isFinite(n) ? currencyFormatter.format(n) : null;
}

/** Parses a nullable numeric string to a finite number, or null. */
function toNumber(value: string | null | undefined): number | null {
  if (!value) return null;
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}

/**
 * Monthly carrying cost = amortized mortgage payment + property tax / 12 +
 * insurance / 12 + HOA. Mortgage payment uses the standard fixed-rate
 * amortization formula: M = P * (r/12) / (1 - (1+r/12)^-n). Returns null if no
 * monthly component can be computed.
 */
function monthlyCost(p: PropertyResponse): number | null {
  let total = 0;
  let hasAny = false;

  const amount = toNumber(p.mortgage_amount);
  const rate = toNumber(p.mortgage_rate);
  const termMonths = toNumber(p.mortgage_term_months);
  if (amount !== null && rate !== null && termMonths !== null && termMonths > 0) {
    const r = rate / 12;
    if (r === 0) {
      total += amount / termMonths;
    } else {
      total += (amount * r) / (1 - Math.pow(1 + r, -termMonths));
    }
    hasAny = true;
  }

  const taxAnnual = toNumber(p.property_tax_annual);
  if (taxAnnual !== null) {
    total += taxAnnual / 12;
    hasAny = true;
  }

  const insuranceAnnual = toNumber(p.insurance_annual);
  if (insuranceAnnual !== null) {
    total += insuranceAnnual / 12;
    hasAny = true;
  }

  const hoaMonthly = toNumber(p.hoa_fee_monthly);
  if (hoaMonthly !== null) {
    total += hoaMonthly;
    hasAny = true;
  }

  return hasAny ? total : null;
}

function hasFinancialData(p: PropertyResponse): boolean {
  return Boolean(
    p.current_value ||
      p.mortgage_amount ||
      p.property_tax_annual ||
      p.insurance_annual ||
      p.hoa_fee_monthly ||
      p.purchase_price ||
      p.down_payment,
  );
}

function CountBadge({ label, count }: { label: string; count: number }) {
  return (
    <span className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-600">
      {count} {count === 1 ? label : `${label}s`}
    </span>
  );
}

export default function PropertyCard({
  property,
  roomCount,
  assetCount,
  maintenanceCount,
}: PropertyCardProps) {
  const currentValue = formatCurrency(property.current_value);
  const monthly = monthlyCost(property);
  const showFinancials = hasFinancialData(property);
  const showCounts =
    roomCount !== undefined || assetCount !== undefined || maintenanceCount !== undefined;

  return (
    <Link
      href={`/dashboard/properties/${property.id}`}
      className="relative block cursor-pointer overflow-hidden rounded-xl border border-gray-200 bg-white p-5 shadow-md transition-all duration-200 hover:border-indigo-200 hover:shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-indigo-600"
    >
      {/* Left accent bar — color varies by property_type */}
      <span
        aria-hidden="true"
        className={`absolute left-0 top-0 bottom-0 w-1 ${accentClass(property.property_type)}`}
      />

      {/* Header: name + type badge */}
      <div className="flex items-start justify-between gap-2">
        <h3 className="text-base font-semibold text-gray-900">{property.name}</h3>
        {property.property_type && (
          <span className="inline-flex shrink-0 items-center rounded-full bg-indigo-100 px-2.5 py-0.5 text-xs font-medium capitalize text-indigo-700">
            {property.property_type.replace(/_/g, " ")}
          </span>
        )}
      </div>

      {/* Address */}
      {property.address && (
        <p className="mt-1 text-sm text-gray-500">{property.address}</p>
      )}

      {/* Financial summary */}
      {showFinancials ? (
        <div className="mt-4 border-t border-gray-100 pt-3">
          {currentValue !== null && (
            <p className="text-lg font-semibold text-gray-900">{currentValue}</p>
          )}
          {monthly !== null && (
            <p className="text-sm text-gray-500">
              {currencyFormatter.format(monthly)}
              <span className="text-gray-400">/mo</span>
            </p>
          )}
          {currentValue === null && monthly === null && (
            <p className="text-sm text-gray-400">Financial details on file</p>
          )}
        </div>
      ) : (
        <p className="mt-4 border-t border-gray-100 pt-3 text-sm text-gray-400">
          Financials not set
        </p>
      )}

      {/* Count badges */}
      {showCounts && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {roomCount !== undefined && <CountBadge label="room" count={roomCount} />}
          {assetCount !== undefined && <CountBadge label="asset" count={assetCount} />}
          {maintenanceCount !== undefined && (
            <CountBadge label="task" count={maintenanceCount} />
          )}
        </div>
      )}
    </Link>
  );
}
