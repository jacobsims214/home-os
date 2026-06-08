"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { apiFetch } from "@/lib/api";
import type { PropertyListResponse } from "@/types/property";
import type { Asset } from "@/lib/types/api";
import type { MaintenanceTask } from "@/components/maintenance/types";

// ── API response wrappers ─────────────────────────────────────

interface Bill {
  id: string;
  name: string;
  amount: string;
  due_day: number | null;
  category: string | null;
  created_at: string;
  updated_at: string;
}

interface BillsResponse {
  data: Bill[];
}

interface AssetsResponse {
  data: Asset[];
}

interface MaintenanceResponse {
  data: MaintenanceTask[];
}

// ── Quick nav item definition ─────────────────────────────────

interface QuickNavItem {
  label: string;
  href: string;
  description: string;
  icon: React.ReactNode;
  color: string; // Tailwind bg color class for the icon container
}

const quickNavItems: QuickNavItem[] = [
  {
    label: "Properties",
    href: "/dashboard/properties",
    description: "Manage your homes and properties",
    color: "bg-blue-500",
    icon: (
      <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M8.25 21v-4.875c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125V21m0 0h4.5V3.545M12.75 21h7.5V10.75M2.25 21h1.5m18 0h-18M2.25 9l4.5-1.636M18.75 3l-1.5.545m0 6.205l3 1m1.5.5l-1.5-.5M6.75 7.364V3h-3v18m3-13.636l10.5-3.819" />
      </svg>
    ),
  },
  {
    label: "Assets",
    href: "/dashboard/assets",
    description: "Track appliances, furniture, and equipment",
    color: "bg-emerald-500",
    icon: (
      <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 10.5V6a3.75 3.75 0 10-7.5 0v4.5m11.356-1.993l1.263 12c.07.665-.45 1.243-1.119 1.243H4.25a1.125 1.125 0 01-1.12-1.243l1.264-12A1.125 1.125 0 015.513 7.5h12.974c.576 0 1.059.435 1.119 1.007zM8.625 10.5a.375.375 0 11-.75 0 .375.375 0 01.75 0zm7.5 0a.375.375 0 11-.75 0 .375.375 0 01.75 0z" />
      </svg>
    ),
  },
  {
    label: "Maintenance",
    href: "/dashboard/maintenance",
    description: "Track and schedule home repairs",
    color: "bg-amber-500",
    icon: (
      <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M11.42 15.17l7.07-7.07a4.95 4.95 0 00-7-7l-7.07 7.07a4.95 4.95 0 007 7zm-2.83-2.83l-1.41-1.41" />
      </svg>
    ),
  },
  {
    label: "Vehicles",
    href: "/dashboard/vehicles",
    description: "Manage cars, maintenance schedules",
    color: "bg-violet-500",
    icon: (
      <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M8.25 18.75a1.5 1.5 0 01-3 0m3 0a1.5 1.5 0 00-3 0m3 0h6m-9 0H3.375a1.125 1.125 0 01-1.125-1.125V14.25m17.25 4.5a1.5 1.5 0 01-3 0m3 0a1.5 1.5 0 00-3 0m3 0h1.125c.621 0 1.129-.504 1.09-1.124a17.902 17.902 0 00-3.213-9.193 2.056 2.056 0 00-1.58-.86H14.25M16.5 18.75h-2.25m0-11.177v-.958c0-.568-.422-1.048-.987-1.106a48.554 48.554 0 00-10.026 0 1.106 1.106 0 00-.987 1.106v7.635m12-6.677v6.677m0 4.5v-4.5m0 0h-12" />
      </svg>
    ),
  },
  {
    label: "Pets",
    href: "/dashboard/pets",
    description: "Track pets, vet visits, and records",
    color: "bg-rose-500",
    icon: (
      <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M15.182 15.182a4.5 4.5 0 01-6.364 0M21 12a9 9 0 11-18 0 9 9 0 0118 0zM9.75 9.75c0 .414-.168.75-.375.75S9 10.164 9 9.75 9.168 9 9.375 9s.375.336.375.75zm-.375 0h.008v.015h-.008V9.75zm5.625 0c0 .414-.168.75-.375.75s-.375-.336-.375-.75.168-.75.375-.75.375.336.375.75zm-.375 0h.008v.015h-.008V9.75z" />
      </svg>
    ),
  },
  {
    label: "Vendors",
    href: "/dashboard/vendors",
    description: "Manage service providers and contacts",
    color: "bg-cyan-500",
    icon: (
      <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 6a3.75 3.75 0 11-7.5 0 3.75 3.75 0 017.5 0zM4.501 20.118a7.5 7.5 0 0114.998 0A17.933 17.933 0 0112 21.75c-2.676 0-5.216-.584-7.499-1.632z" />
      </svg>
    ),
  },
  {
    label: "Bills",
    href: "/dashboard/bills",
    description: "Track recurring bills and payments",
    color: "bg-orange-500",
    icon: (
      <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M12 6v12m-3-2.818l.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
      </svg>
    ),
  },
  {
    label: "Search",
    href: "/dashboard/search",
    description: "Find anything across your household",
    color: "bg-gray-500",
    icon: (
      <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-5.197-5.197m0 0A7.5 7.5 0 105.196 5.196a7.5 7.5 0 0010.607 10.607z" />
      </svg>
    ),
  },
];

// ── Status badge component ────────────────────────────────────

const statusStyles: Record<MaintenanceTask["status"], string> = {
  pending: "bg-yellow-100 text-yellow-800",
  in_progress: "bg-blue-100 text-blue-800",
  done: "bg-green-100 text-green-800",
  skipped: "bg-gray-100 text-gray-600",
};

const statusLabels: Record<MaintenanceTask["status"], string> = {
  pending: "Pending",
  in_progress: "In Progress",
  done: "Done",
  skipped: "Skipped",
};

function StatusBadge({ status }: { status: MaintenanceTask["status"] }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${statusStyles[status]}`}
    >
      {statusLabels[status]}
    </span>
  );
}

// ── Stat card component ───────────────────────────────────────

function StatCard({
  label,
  value,
  icon,
  loading,
  highlight,
  href,
}: {
  label: string;
  value: number | string;
  icon: React.ReactNode;
  loading: boolean;
  highlight?: boolean;
  href: string;
}) {
  const content = (
    <div
      className={`rounded-xl border bg-white px-5 py-4 shadow-sm transition-shadow hover:shadow-md ${
        highlight ? "border-red-300 ring-1 ring-red-200" : "border-gray-200"
      }`}
    >
      <div className="flex items-center justify-between">
        <div>
          <p className="text-sm font-medium text-gray-500">{label}</p>
          {loading ? (
            <div className="mt-1 h-8 w-12 animate-pulse rounded bg-gray-200" />
          ) : (
            <p
              className={`mt-1 text-3xl font-bold tracking-tight ${
                highlight ? "text-red-600" : "text-gray-900"
              }`}
            >
              {value}
            </p>
          )}
        </div>
        <div
          className={`flex h-12 w-12 items-center justify-center rounded-lg ${
            highlight ? "bg-red-100 text-red-600" : "bg-gray-100 text-gray-500"
          }`}
        >
          {icon}
        </div>
      </div>
    </div>
  );

  if (loading) return content;

  return (
    <Link href={href} className="block">
      {content}
    </Link>
  );
}

// ── Skeleton for the stats row ─────────────────────────────────

function StatCardSkeleton() {
  return (
    <div className="rounded-xl border border-gray-200 bg-white px-5 py-4 shadow-sm">
      <div className="flex items-center justify-between">
        <div className="space-y-2">
          <div className="h-4 w-20 animate-pulse rounded bg-gray-200" />
          <div className="h-8 w-12 animate-pulse rounded bg-gray-200" />
        </div>
        <div className="h-12 w-12 animate-pulse rounded-lg bg-gray-200" />
      </div>
    </div>
  );
}

// ── Page component ────────────────────────────────────────────

export default function DashboardPage() {
  // Fetch all four data sources in parallel
  const properties = useQuery({
    queryKey: ["properties", "dashboard-count"],
    queryFn: () =>
      apiFetch<PropertyListResponse>("/api/v1/properties").then((r) => r.data),
    staleTime: 60_000,
  });

  const assets = useQuery({
    queryKey: ["assets", "dashboard-count"],
    queryFn: () =>
      apiFetch<AssetsResponse>("/api/v1/assets").then((r) => r.data),
    staleTime: 60_000,
  });

  const maintenance = useQuery({
    queryKey: ["maintenance", "dashboard-pending"],
    queryFn: () =>
      apiFetch<MaintenanceResponse>("/api/v1/maintenance/tasks").then(
        (r) => r.data,
      ),
    staleTime: 60_000,
  });

  const bills = useQuery({
    queryKey: ["bills", "dashboard-count"],
    queryFn: () =>
      apiFetch<BillsResponse>("/api/v1/bills").then((r) => r.data),
    staleTime: 60_000,
  });

  const anyLoading =
    properties.isLoading || assets.isLoading || maintenance.isLoading || bills.isLoading;

  const anyError =
    properties.isError || assets.isError || maintenance.isError || bills.isError;

  // Derived counts
  const propertyCount = properties.data?.length ?? 0;
  const assetCount = assets.data?.length ?? 0;
  const pendingTasks = (maintenance.data ?? []).filter(
    (t) => t.status === "pending" || t.status === "in_progress",
  );
  const pendingCount = pendingTasks.length;
  const billCount = bills.data?.length ?? 0;

  // Up to 5 most recent pending tasks, sorted by due date (nulls last)
  const recentPendingTasks = [...pendingTasks]
    .sort((a, b) => {
      if (!a.due_date && !b.due_date) return 0;
      if (!a.due_date) return 1;
      if (!b.due_date) return -1;
      return new Date(a.due_date).getTime() - new Date(b.due_date).getTime();
    })
    .slice(0, 5);

  return (
    <div className="mx-auto max-w-5xl px-4 py-6 sm:px-6 lg:px-8">
      {/* Page header */}
      <div className="mb-8">
        <h1 className="text-2xl font-bold text-gray-900">Dashboard</h1>
        <p className="mt-1 text-sm text-gray-500">
          Overview of your household — everything in one place.
        </p>
      </div>

      {/* ── Stats row ──────────────────────────────────────────── */}
      <div className="mb-10 grid grid-cols-2 gap-4 lg:grid-cols-4">
        {anyLoading && properties.isLoading ? (
          <>
            <StatCardSkeleton />
            <StatCardSkeleton />
            <StatCardSkeleton />
            <StatCardSkeleton />
          </>
        ) : (
          <>
            <StatCard
              label="Properties"
              value={propertyCount}
              loading={properties.isLoading}
              href="/dashboard/properties"
              icon={
                <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" d="M8.25 21v-4.875c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125V21m0 0h4.5V3.545M12.75 21h7.5V10.75M2.25 21h1.5m18 0h-18M2.25 9l4.5-1.636M18.75 3l-1.5.545m0 6.205l3 1m1.5.5l-1.5-.5M6.75 7.364V3h-3v18m3-13.636l10.5-3.819" />
                </svg>
              }
            />
            <StatCard
              label="Assets"
              value={assetCount}
              loading={assets.isLoading}
              href="/dashboard/assets"
              icon={
                <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 10.5V6a3.75 3.75 0 10-7.5 0v4.5m11.356-1.993l1.263 12c.07.665-.45 1.243-1.119 1.243H4.25a1.125 1.125 0 01-1.12-1.243l1.264-12A1.125 1.125 0 015.513 7.5h12.974c.576 0 1.059.435 1.119 1.007zM8.625 10.5a.375.375 0 11-.75 0 .375.375 0 01.75 0zm7.5 0a.375.375 0 11-.75 0 .375.375 0 01.75 0z" />
                </svg>
              }
            />
            <StatCard
              label="Pending Maintenance"
              value={pendingCount}
              loading={maintenance.isLoading}
              highlight={pendingCount > 0}
              href="/dashboard/maintenance"
              icon={
                <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" d="M11.42 15.17l7.07-7.07a4.95 4.95 0 00-7-7l-7.07 7.07a4.95 4.95 0 007 7zm-2.83-2.83l-1.41-1.41" />
                </svg>
              }
            />
            <StatCard
              label="Bills"
              value={billCount}
              loading={bills.isLoading}
              href="/dashboard/bills"
              icon={
                <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" d="M12 6v12m-3-2.818l.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
                </svg>
              }
            />
          </>
        )}
      </div>

      {/* ── Error state ────────────────────────────────────────── */}
      {anyError && (
        <div className="mb-8 rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">
            Some data failed to load. Try refreshing the page.
          </p>
        </div>
      )}

      {/* ── Pending maintenance section ────────────────────────── */}
      <section className="mb-10">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold text-gray-900">
            Pending Maintenance
          </h2>
          <Link
            href="/dashboard/maintenance"
            className="text-sm font-medium text-indigo-600 hover:text-indigo-500"
          >
            View all &rarr;
          </Link>
        </div>

        {maintenance.isLoading ? (
          <div className="space-y-3">
            {[...Array(3)].map((_, i) => (
              <div
                key={i}
                className="rounded-lg border border-gray-200 bg-white p-4 animate-pulse"
              >
                <div className="h-4 w-3/4 rounded bg-gray-200 mb-2" />
                <div className="h-3 w-1/2 rounded bg-gray-100" />
              </div>
            ))}
          </div>
        ) : recentPendingTasks.length === 0 ? (
          <div className="rounded-lg border border-dashed border-gray-300 bg-gray-50 px-4 py-8 text-center">
            <svg
              className="mx-auto h-10 w-10 text-gray-400"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth={1}
              stroke="currentColor"
            >
              <path strokeLinecap="round" strokeLinejoin="round" d="M9 12.75L11.25 15 15 9.75M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
            </svg>
            <p className="mt-2 text-sm font-medium text-gray-900">
              All caught up!
            </p>
            <p className="mt-1 text-sm text-gray-500">
              No pending maintenance tasks.
            </p>
          </div>
        ) : (
          <div className="space-y-3">
            {recentPendingTasks.map((task) => (
              <Link
                key={task.id}
                href={`/dashboard/maintenance`}
                className="flex items-center justify-between rounded-lg border border-gray-200 bg-white px-4 py-3 shadow-sm transition-colors hover:bg-gray-50"
              >
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium text-gray-900">
                    {task.name}
                  </p>
                  <p className="mt-0.5 text-xs text-gray-500">
                    {task.due_date
                      ? `Due ${new Date(task.due_date).toLocaleDateString("en-US", {
                          month: "short",
                          day: "numeric",
                          year: "numeric",
                        })}`
                      : "No due date"}
                  </p>
                </div>
                <StatusBadge status={task.status} />
              </Link>
            ))}
          </div>
        )}
      </section>

      {/* ── Quick navigation grid ──────────────────────────────── */}
      <section>
        <h2 className="mb-4 text-lg font-semibold text-gray-900">
          Quick Navigation
        </h2>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {quickNavItems.map((item) => (
            <Link
              key={item.href}
              href={item.href}
              className="group flex flex-col rounded-xl border border-gray-200 bg-white p-4 shadow-sm transition-all hover:border-gray-300 hover:shadow-md"
            >
              <div
                className={`mb-3 flex h-10 w-10 items-center justify-center rounded-lg ${item.color} text-white`}
              >
                {item.icon}
              </div>
              <p className="text-sm font-semibold text-gray-900 group-hover:text-indigo-600">
                {item.label}
              </p>
              <p className="mt-0.5 text-xs text-gray-500">{item.description}</p>
            </Link>
          ))}
        </div>
      </section>
    </div>
  );
}
