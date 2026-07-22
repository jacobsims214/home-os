"use client";

import { useState } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import Button from "@/components/ui/Button";
import Select from "@/components/ui/Select";
import type { Property } from "@/lib/types/api";

interface Bill {
  id: string;
  name: string;
  amount: string | null;
  due_day: number | null;
  category: string | null;
  property_id: string | null;
  is_autopay: boolean | null;
}

function fmtCurrency(v: string | null): string {
  if (!v) return "—";
  const n = Number(v);
  if (Number.isNaN(n)) return "—";
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
}

export default function BillsPage() {
  const [propertyFilter, setPropertyFilter] = useState("");

  const { data: propsData } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () => apiFetch<{ data: Property[] }>("/api/v1/properties"),
  });
  const properties = propsData?.data ?? [];

  const { data: billsData, isLoading } = useQuery({
    queryKey: ["bills"],
    queryFn: () => apiFetch<{ data: Bill[] }>("/api/v1/bills"),
  });
  const allBills = billsData?.data ?? [];
  const bills = propertyFilter ? allBills.filter((b) => b.property_id === propertyFilter) : allBills;
  const monthlyTotal = bills.reduce((sum, b) => sum + Number(b.amount || 0), 0);
  const propertyMap = new Map(properties.map((p) => [p.id, p.name]));

  return (
    <div className="mx-auto max-w-5xl px-4 py-6 sm:px-6 lg:px-8">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Bills</h1>
          <p className="mt-1 text-sm text-gray-500">Track recurring expenses and build your monthly budget</p>
        </div>
        <Link href="/dashboard/bills/new"><Button>+ Add Bill</Button></Link>
      </div>

      <div className="mb-6 flex items-center gap-4 rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
        <div>
          <p className="text-xs text-gray-400">Monthly Total</p>
          <p className="text-2xl font-bold text-gray-900">{fmtCurrency(monthlyTotal.toString())}<span className="text-sm font-normal text-gray-400">/mo</span></p>
        </div>
        <div className="h-10 w-px bg-gray-200" />
        <div>
          <p className="text-xs text-gray-400">Bills</p>
          <p className="text-lg font-semibold text-gray-700">{bills.length}</p>
        </div>
        <div className="ml-auto">
          <Select label="" value={propertyFilter} onChange={(e) => setPropertyFilter(e.target.value)}
            options={[{ value: "", label: "All properties" }, ...properties.map((p) => ({ value: p.id, label: p.name }))]}
            placeholder="Filter" className="max-w-xs" />
        </div>
      </div>

      {isLoading && <div className="space-y-2">{[1,2,3].map((i) => <div key={i} className="h-14 animate-pulse rounded-lg bg-gray-100" />)}</div>}

      {!isLoading && bills.length === 0 && (
        <div className="rounded-lg border-2 border-dashed border-gray-300 py-12 text-center">
          <p className="text-sm text-gray-500">No bills yet.</p>
        </div>
      )}

      {!isLoading && bills.length > 0 && (
        <div className="overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm">
          <table className="min-w-full divide-y divide-gray-200">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase">Name</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase">Category</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase">Property</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase">Due</th>
                <th className="px-4 py-3 text-right text-xs font-semibold text-gray-500 uppercase">Amount</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {bills.map((bill) => (
                <tr key={bill.id} className="hover:bg-gray-50 cursor-pointer" onClick={() => window.location.href = `/dashboard/bills/${bill.id}`}>
                  <td className="px-4 py-3">
                    <p className="text-sm font-medium text-gray-900">{bill.name}</p>
                    {bill.is_autopay && <span className="text-xs text-green-600">Auto-pay</span>}
                  </td>
                  <td className="px-4 py-3 text-sm text-gray-500">{bill.category ?? "—"}</td>
                  <td className="px-4 py-3 text-sm text-gray-500">{bill.property_id ? propertyMap.get(bill.property_id) ?? "—" : "—"}</td>
                  <td className="px-4 py-3 text-sm text-gray-500">{bill.due_day ? `Day ${bill.due_day}` : "—"}</td>
                  <td className="px-4 py-3 text-right text-sm font-medium text-gray-900">{fmtCurrency(bill.amount)}</td>
                </tr>
              ))}
            </tbody>
            <tfoot className="bg-gray-50">
              <tr>
                <td colSpan={4} className="px-4 py-3 text-right text-sm font-medium text-gray-500">Monthly Total</td>
                <td className="px-4 py-3 text-right text-sm font-bold text-gray-900">{fmtCurrency(monthlyTotal.toString())}</td>
              </tr>
            </tfoot>
          </table>
        </div>
      )}
    </div>
  );
}
