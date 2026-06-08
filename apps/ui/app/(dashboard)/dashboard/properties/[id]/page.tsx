"use client";

import { useParams, useRouter } from "next/navigation";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import type {
  PropertyDetailResponse,
  RoomListResponse,
} from "@/types/property";

export default function PropertyDetailPage() {
  const params = useParams<{ id: string }>();
  const router = useRouter();
  const propertyId = params.id;

  // Fetch the property detail
  const {
    data: propData,
    isLoading: propLoading,
    isError: propError,
    error: propErr,
  } = useQuery({
    queryKey: propertyKeys.detail(propertyId),
    queryFn: () =>
      apiFetch<PropertyDetailResponse>(`/api/v1/properties/${propertyId}`),
    enabled: !!propertyId,
  });

  // Fetch rooms for this property
  const {
    data: roomsData,
    isLoading: roomsLoading,
    isError: roomsError,
  } = useQuery({
    queryKey: [...propertyKeys.detail(propertyId), "rooms"],
    queryFn: () =>
      apiFetch<RoomListResponse>(`/api/v1/properties/${propertyId}/rooms`),
    enabled: !!propertyId,
  });

  const property = propData?.data;
  const rooms = roomsData?.data ?? [];

  if (propLoading) {
    return (
      <div className="flex items-center justify-center py-20">
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
        <span className="ml-3 text-sm text-gray-500">Loading property...</span>
      </div>
    );
  }

  if (propError) {
    return (
      <div className="mx-auto max-w-2xl px-4 py-12 text-center">
        <p className="text-sm text-red-600">
          Failed to load property. {(propErr as Error)?.message}
        </p>
        <Link
          href="/dashboard/properties"
          className="mt-4 inline-block text-sm font-medium text-indigo-600 hover:text-indigo-500"
        >
          &larr; Back to properties
        </Link>
      </div>
    );
  }

  if (!property) {
    return (
      <div className="mx-auto max-w-2xl px-4 py-12 text-center">
        <p className="text-sm text-gray-500">Property not found.</p>
        <Link
          href="/dashboard/properties"
          className="mt-4 inline-block text-sm font-medium text-indigo-600 hover:text-indigo-500"
        >
          &larr; Back to properties
        </Link>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-4xl px-4 py-6 sm:px-6 lg:px-8">
      {/* Back link */}
      <button
        onClick={() => router.back()}
        className="mb-4 inline-flex items-center gap-1 text-sm font-medium text-gray-500 hover:text-gray-700"
      >
        <svg
          className="h-4 w-4"
          fill="none"
          viewBox="0 0 24 24"
          strokeWidth={1.5}
          stroke="currentColor"
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            d="M15.75 19.5L8.25 12l7.5-7.5"
          />
        </svg>
        Back
      </button>

      {/* Property details header */}
      <div className="rounded-lg border border-gray-200 bg-white p-6 shadow-sm">
        <h1 className="text-2xl font-bold tracking-tight text-gray-900">
          {property.name}
        </h1>

        {property.address && (
          <p className="mt-2 text-sm text-gray-500">{property.address}</p>
        )}

        <div className="mt-4 flex flex-wrap gap-3">
          {property.property_type && (
            <span className="inline-flex items-center rounded-full bg-indigo-50 px-2.5 py-0.5 text-xs font-medium text-indigo-700">
              {property.property_type}
            </span>
          )}
        </div>

        {property.notes && (
          <p className="mt-4 text-sm text-gray-600 border-t border-gray-100 pt-4">
            {property.notes}
          </p>
        )}
      </div>

      {/* Rooms section */}
      <div className="mt-8">
        <h2 className="text-lg font-semibold text-gray-900">
          Rooms{" "}
          {rooms.length > 0 && (
            <span className="font-normal text-gray-400">({rooms.length})</span>
          )}
        </h2>

        {roomsLoading && (
          <p className="mt-2 text-sm text-gray-500">Loading rooms...</p>
        )}

        {roomsError && (
          <p className="mt-2 text-sm text-red-600">Failed to load rooms.</p>
        )}

        {!roomsLoading && !roomsError && rooms.length === 0 && (
          <div className="mt-4 rounded-lg border-2 border-dashed border-gray-200 py-8 text-center">
            <p className="text-sm text-gray-500">
              No rooms added yet. Use the API to add rooms to this property.
            </p>
          </div>
        )}

        {!roomsLoading && rooms.length > 0 && (
          <div className="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {rooms.map((room) => (
              <div
                key={room.id}
                className="rounded-lg border border-gray-200 bg-white px-4 py-3 shadow-sm"
              >
                <p className="text-sm font-medium text-gray-900">{room.name}</p>
                {room.floor !== null && (
                  <p className="mt-1 text-xs text-gray-500">
                    Floor {room.floor}
                  </p>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
