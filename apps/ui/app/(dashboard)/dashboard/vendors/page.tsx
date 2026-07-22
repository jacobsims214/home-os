"use client";

import { useState } from "react";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import Button from "@/components/ui/Button";
import Select from "@/components/ui/Select";
import type { Property } from "@/lib/types/api";

interface Vendor {
  id: string;
  name: string;
  specialty: string | null;
  phone: string | null;
  email: string | null;
  website: string | null;
  property_id: string | null;
  notes: string | null;
}

export default function VendorsPage() {
  const queryClient = useQueryClient();
  const [propertyFilter, setPropertyFilter] = useState("");

  const { data: propsData } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () => apiFetch<{ data: Property[] }>("/api/v1/properties"),
  });
  const properties = propsData?.data ?? [];

  const { data: vendorsData, isLoading } = useQuery({
    queryKey: ["vendors"],
    queryFn: () => apiFetch<{ data: Vendor[] }>("/api/v1/vendors"),
  });
  const allVendors = vendorsData?.data ?? [];
  const vendors = propertyFilter ? allVendors.filter((v) => v.property_id === propertyFilter) : allVendors;

  const propertyMap = new Map(properties.map((p) => [p.id, p.name]));

  return (
    <div className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Vendors</h1>
          <p className="mt-1 text-sm text-gray-500">Service providers and contractors</p>
        </div>
        <Link href="/dashboard/vendors/new">
          <Button>+ Add Vendor</Button>
        </Link>
      </div>

      <div className="mb-4">
        <Select label="" value={propertyFilter} onChange={(e) => setPropertyFilter(e.target.value)}
          options={[{ value: "", label: "All properties" }, ...properties.map((p) => ({ value: p.id, label: p.name }))]}
          placeholder="Filter" className="max-w-xs" />
      </div>

      {isLoading && <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">{[1,2,3].map((i) => <div key={i} className="h-32 animate-pulse rounded-xl bg-gray-100" />)}</div>}

      {!isLoading && vendors.length === 0 && (
        <div className="rounded-lg border-2 border-dashed border-gray-300 py-12 text-center">
          <p className="text-sm text-gray-500">No vendors yet.</p>
        </div>
      )}

      {!isLoading && vendors.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {vendors.map((vendor) => (
            <Link key={vendor.id} href={`/dashboard/vendors/${vendor.id}`}
              className="block cursor-pointer rounded-xl border border-gray-200 bg-white p-4 shadow-sm transition-all hover:shadow-lg hover:border-indigo-200">
              <h3 className="text-sm font-semibold text-gray-900">{vendor.name}</h3>
              {vendor.specialty && <span className="mt-1 inline-flex items-center rounded-full bg-indigo-50 px-2 py-0.5 text-xs text-indigo-700">{vendor.specialty}</span>}
              <dl className="mt-3 space-y-1 text-xs text-gray-500">
                {vendor.phone && <div className="flex justify-between"><dt>Phone</dt><dd className="font-medium text-gray-700">{vendor.phone}</dd></div>}
                {vendor.email && <div className="flex justify-between"><dt>Email</dt><dd className="font-medium text-gray-700 truncate ml-2">{vendor.email}</dd></div>}
                {vendor.property_id && <div className="flex justify-between"><dt>Property</dt><dd className="font-medium text-gray-700">{propertyMap.get(vendor.property_id) ?? "—"}</dd></div>}
              </dl>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
