"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { apiFetch } from "@/lib/api";
import { Card, Text, SimpleGrid, Title, Skeleton, Badge } from "@mantine/core";
import { IconBuilding, IconTool, IconCar, IconCash, IconCoin } from "@tabler/icons-react";
import type { PropertyResponse } from "@/types/property";
import type { Asset } from "@/lib/types/api";
import type { MaintenanceTask } from "@/components/maintenance/types";

// ── Types ─────────────────────────────────────────────────────

interface Vehicle {
  id: string;
  year?: number | null;
  make?: string | null;
  model?: string | null;
  current_value?: string | null;
  purchase_price?: string | null;
}

interface Loan {
  id: string;
  name: string;
  remaining_balance: number;
  interest_rate?: number | null;
}

interface Bill {
  id: string;
  name: string;
  amount: string;
  due_day?: number | null;
  category?: string | null;
}

interface FinancialSummary {
  total_property_value: number;
  total_asset_value: number;
  total_vehicle_value: number;
  total_assets_value: number;
  total_liabilities: number;
  estimated_net_worth: number;
}

// ── Helpers ───────────────────────────────────────────────────

function fmtCurrency(n: number): string {
  if (n == null || isNaN(n)) return "—";
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
}

function fmtDate(iso: string | null | undefined): string {
  if (!iso) return "";
  try {
    return new Date(iso).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });
  } catch {
    return iso;
  }
}

function statusBadge(status: string): { label: string; color: string } {
  switch (status) {
    case "pending": return { label: "Pending", color: "yellow" };
    case "in_progress": return { label: "In Progress", color: "blue" };
    case "done": return { label: "Done", color: "green" };
    case "skipped": return { label: "Skipped", color: "gray" };
    default: return { label: status, color: "gray" };
  }
}

// ── Summary Card ──────────────────────────────────────────────

function SummaryCard({ title, value, icon: Icon, color }: { title: string; value: string; icon: React.ElementType; color: string }) {
  return (
    <Card shadow="sm" radius="md" withBorder padding="lg">
      <div className="flex items-center gap-3">
        <div className={`flex h-12 w-12 items-center justify-center rounded-full bg-${color}-50`}>
          <Icon className={`text-${color}-600`} size={24} />
        </div>
        <div>
          <Text size="xs" c="dimmed" tt="uppercase" fw={600}>{title}</Text>
          <Text size="xl" fw={700}>{value}</Text>
        </div>
      </div>
    </Card>
  );
}

// ── Page ──────────────────────────────────────────────────────

export default function DashboardPage() {
  const { data: propertiesData, isLoading: loadingProps } = useQuery({
    queryKey: ["properties", "list"],
    queryFn: () => apiFetch<{ data: PropertyResponse[] }>("/api/v1/properties"),
  });

  const { data: assetsData, isLoading: loadingAssets } = useQuery({
    queryKey: ["assets", "list"],
    queryFn: () => apiFetch<{ data: Asset[] }>("/api/v1/assets"),
  });

  const { data: vehiclesData, isLoading: loadingVehicles } = useQuery({
    queryKey: ["vehicles", "list"],
    queryFn: () => apiFetch<{ data: Vehicle[] }>("/api/v1/vehicles"),
  });

  const { data: loansData, isLoading: loadingLoans } = useQuery({
    queryKey: ["loans", "list"],
    queryFn: () => apiFetch<{ data: Loan[] }>("/api/v1/loans"),
  });

  const { data: billsData, isLoading: loadingBills } = useQuery({
    queryKey: ["bills", "list"],
    queryFn: () => apiFetch<{ data: Bill[] }>("/api/v1/bills"),
  });

  const { data: maintenanceData, isLoading: loadingMaintenance } = useQuery({
    queryKey: ["maintenance", "list"],
    queryFn: () => apiFetch<{ data: MaintenanceTask[] }>("/api/v1/maintenance/tasks"),
  });

  const properties = propertiesData?.data ?? [];
  const assets = assetsData?.data ?? [];
  const vehicles = vehiclesData?.data ?? [];
  const loans = loansData?.data ?? [];
  const bills = billsData?.data ?? [];
  const maintenance = maintenanceData?.data ?? [];

  // Financial summary calculations
  const totalPropertyValue = properties.reduce((sum, p) => sum + (Number(p.current_value) || Number(p.purchase_price) || 0), 0);
  const totalAssetValue = assets.reduce((sum, a) => sum + (Number(a.current_value) || Number(a.purchase_price) || 0), 0);
  const totalVehicleValue = vehicles.reduce((sum, v) => sum + (Number(v.current_value) || Number(v.purchase_price) || 0), 0);
  const totalAssets = totalPropertyValue + totalAssetValue + totalVehicleValue;
  const totalLiabilities = loans.reduce((sum, l) => sum + l.remaining_balance, 0);
  const estimatedNetWorth = totalAssets - totalLiabilities;

  const isLoading = loadingProps || loadingAssets || loadingVehicles || loadingLoans || loadingBills || loadingMaintenance;

  return (
    <div className="mx-auto max-w-7xl px-4 py-6 sm:px-6 lg:px-8">
      <div className="mb-8">
        <Title order={2}>Dashboard</Title>
        <Text c="dimmed" size="sm">Overview of your household finances</Text>
      </div>

      {isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3, 4, 5].map((i) => (
            <Skeleton key={i} height={100} radius="md" />
          ))}
        </div>
      ) : (
        <>
          {/* ── Financial Summary ───────────────────────────────── */}
          <Title order={4} mb="sm">Financial Summary</Title>
          <SimpleGrid cols={{ base: 1, sm: 2, lg: 5 }} mb="xl">
            <SummaryCard title="Property Value" value={fmtCurrency(totalPropertyValue)} icon={IconBuilding} color="indigo" />
            <SummaryCard title="Assets" value={fmtCurrency(totalAssetValue)} icon={IconTool} color="teal" />
            <SummaryCard title="Vehicles" value={fmtCurrency(totalVehicleValue)} icon={IconCar} color="cyan" />
            <SummaryCard title="Liabilities" value={fmtCurrency(totalLiabilities)} icon={IconCash} color="red" />
            <SummaryCard title="Net Worth" value={fmtCurrency(estimatedNetWorth)} icon={IconCoin} color={estimatedNetWorth >= 0 ? "green" : "red"} />
          </SimpleGrid>

          {/* ── Upcoming Bills + Recent Maintenance ─────────────── */}
          <SimpleGrid cols={{ base: 1, lg: 2 }} mb="xl">
            {/* Upcoming Bills */}
            <Card shadow="sm" radius="md" withBorder padding="lg">
              <div className="mb-3 flex items-center justify-between">
                <Title order={5}>Upcoming Bills</Title>
                <Link href="/dashboard/bills" className="text-xs text-indigo-600 hover:text-indigo-500">
                  View all
                </Link>
              </div>
              {bills.length === 0 ? (
                <Text size="sm" c="dimmed" ta="center" py="xl">No bills tracked</Text>
              ) : (
                <div className="divide-y divide-gray-50">
                  {bills.slice(0, 8).map((bill) => (
                    <div key={bill.id} className="flex items-center justify-between py-2">
                      <div>
                        <Text size="sm" fw={500}>{bill.name}</Text>
                        {bill.due_day && <Text size="xs" c="dimmed">Due day {bill.due_day}</Text>}
                      </div>
                      <Text size="sm" fw={600}>
                        {Number(bill.amount).toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 })}
                      </Text>
                    </div>
                  ))}
                </div>
              )}
            </Card>

            {/* Recent Maintenance */}
            <Card shadow="sm" radius="md" withBorder padding="lg">
              <div className="mb-3 flex items-center justify-between">
                <Title order={5}>Recent Maintenance</Title>
                <Link href="/dashboard/maintenance" className="text-xs text-indigo-600 hover:text-indigo-500">
                  View all
                </Link>
              </div>
              {maintenance.length === 0 ? (
                <Text size="sm" c="dimmed" ta="center" py="xl">No maintenance tasks</Text>
              ) : (
                <div className="divide-y divide-gray-50">
                  {maintenance.slice(0, 8).map((task) => {
                    const s = statusBadge(task.status);
                    return (
                      <div key={task.id} className="flex items-center justify-between py-2">
                        <div>
                          <Text size="sm" fw={500}>{task.name}</Text>
                          <Text size="xs" c="dimmed">{task.due_date ? `Due ${fmtDate(task.due_date)}` : "No due date"}</Text>
                        </div>
                        <Badge color={s.color} variant="light" size="sm">{s.label}</Badge>
                      </div>
                    );
                  })}
                </div>
              )}
            </Card>
          </SimpleGrid>
        </>
      )}
    </div>
  );
}