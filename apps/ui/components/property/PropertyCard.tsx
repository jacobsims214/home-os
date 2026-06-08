"use client";

import Link from "next/link";
import type { PropertyResponse } from "@/types/property";

interface PropertyCardProps {
  property: PropertyResponse;
  /** Optional room count to display. Not fetched by the card itself. */
  roomCount?: number;
}

export default function PropertyCard({
  property,
  roomCount,
}: PropertyCardProps) {
  return (
    <Link
      href={`/dashboard/properties/${property.id}`}
      className="block rounded-lg border border-gray-200 bg-white p-5 shadow-sm transition-shadow hover:shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-indigo-600"
    >
      <h3 className="text-base font-semibold text-gray-900">{property.name}</h3>

      {property.address && (
        <p className="mt-1 text-sm text-gray-500">{property.address}</p>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-3 text-xs text-gray-400">
        {property.property_type && (
          <span className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 font-medium text-gray-600">
            {property.property_type}
          </span>
        )}
        {roomCount !== undefined && (
          <span>
            {roomCount} {roomCount === 1 ? "room" : "rooms"}
          </span>
        )}
      </div>
    </Link>
  );
}
