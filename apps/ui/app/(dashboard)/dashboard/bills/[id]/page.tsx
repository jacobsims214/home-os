"use client";

import { useState } from "react";
import { useParams, useRouter } from "next/navigation";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import EntityResources from "@/components/EntityResources";
import type { Property } from "@/lib/types/api";

interface Bill {
  id: string;
  name: string;
  amount: string | null;
  due_day: number | null;
  category: string | null;
  property_id: string | null;
  vendor_id: string | null;
  is_autopay: boolean | null;
  account_number: string | null;
  payment_url: string | null;
  notes: string | null;
  created_at: string;
  updated_at: string;
}

const CATEGORIES = ["Mortgage", "Electric", "Water", "Gas", "Internet", "Trash", "Insurance", "HOA", "Subscription", "Other"];

function fmtCurrency(v: string | null): string {
  if (!v) return "—";
  const n = Number(v);
  if (Number.isNaN(n)) return "—";
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 2 });
}

function EditField({ label, value, onChange, type = "text", placeholder }: {
  label: string; value: string; onChange: (v: string) => void; type?: string; placeholder?: string;
}) {
  return (
    <label className="block">
      <span className="text-xs font-medium text-gray-600">{label}</span>
      <input type={type} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder}
        className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500" />
    </label>
  );
}

export default function BillDetailPage() {
  const params = useParams<{ id: string }>();
  const router = useRouter();
  const queryClient = useQueryClient();
  const id = params.id;

  const [isEditing, setIsEditing] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [form, setForm] = useState<Record<string, string>>({});

  const { data: resp, isLoading, isError } = useQuery({
    queryKey: ["bills", id],
    queryFn: () => apiFetch<{ data: Bill }>(`/api/v1/bills/${id}`),
    enabled: !!id,
  });
  const bill = resp?.data;

  const { data: propsData } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () => apiFetch<{ data: Property[] }>("/api/v1/properties"),
  });
  const properties = propsData?.data ?? [];
  const propertyName = properties.find((p) => p.id === bill?.property_id)?.name;

  const updateMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) => apiFetch(`/api/v1/bills/${id}`, { method: "PUT", body }),
    onSuccess: () => { queryClient.invalidateQueries({ queryKey: ["bills", id] }); setIsEditing(false); },
  });

  const deleteMutation = useMutation({
    mutationFn: () => apiFetch<void>(`/api/v1/bills/${id}`, { method: "DELETE" }),
    onSuccess: () => { queryClient.invalidateQueries({ queryKey: ["bills"] }); router.push("/dashboard/bills"); },
  });

  function startEditing() {
    if (!bill) return;
    setForm({
      name: bill.name, amount: bill.amount ?? "", due_day: bill.due_day?.toString() ?? "",
      category: bill.category ?? "", property_id: bill.property_id ?? "",
      account_number: bill.account_number ?? "", payment_url: bill.payment_url ?? "",
      is_autopay: bill.is_autopay ? "true" : "", notes: bill.notes ?? "",
    });
    setIsEditing(true);
  }

  function handleSave() {
    const body: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(form)) {
      if (k === "name") { body[k] = v.trim() || bill?.name; continue; }
      if (v.trim()) {
        if (k === "due_day") body[k] = parseInt(v);
        else if (k === "is_autopay") body[k] = v === "true";
        else body[k] = v.trim();
      }
    }
    updateMutation.mutate(body);
  }

  if (isLoading) return <div className="flex justify-center py-20"><svg className="h-6 w-6 animate-spin text-indigo-600" fill="none" viewBox="0 0 24 24"><circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" /><path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" /></svg></div>;
  if (isError || !bill) return <div className="text-center py-12"><p className="text-sm text-red-600">Bill not found</p><Link href="/dashboard/bills" className="mt-4 inline-block text-sm text-indigo-600">&larr; Back</Link></div>;

  if (isEditing) {
    return (
      <div className="mx-auto max-w-4xl px-4 py-6 sm:px-6 lg:px-8">
        <button onClick={() => setIsEditing(false)} className="mb-4 text-sm text-gray-500 hover:text-gray-700">&larr; Cancel</button>
        <div className="rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
          <h1 className="text-xl font-bold text-gray-900 mb-6">Edit Bill</h1>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <EditField label="Name *" value={form.name ?? ""} onChange={(v) => setForm({ ...form, name: v })} />
            <label className="block">
              <span className="text-xs font-medium text-gray-600">Category</span>
              <select value={form.category ?? ""} onChange={(e) => setForm({ ...form, category: e.target.value })} className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500">
                <option value="">Select...</option>
                {CATEGORIES.map((c) => <option key={c} value={c}>{c}</option>)}
              </select>
            </label>
            <EditField label="Amount" value={form.amount ?? ""} onChange={(v) => setForm({ ...form, amount: v })} placeholder="$145" />
            <EditField label="Due Day (1-31)" value={form.due_day ?? ""} onChange={(v) => setForm({ ...form, due_day: v })} type="number" placeholder="15" />
            <label className="block">
              <span className="text-xs font-medium text-gray-600">Property</span>
              <select value={form.property_id ?? ""} onChange={(e) => setForm({ ...form, property_id: e.target.value })} className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500">
                <option value="">None</option>
                {properties.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
            </label>
            <EditField label="Account Number" value={form.account_number ?? ""} onChange={(v) => setForm({ ...form, account_number: v })} />
            <EditField label="Payment URL" value={form.payment_url ?? ""} onChange={(v) => setForm({ ...form, payment_url: v })} placeholder="https://..." />
            <label className="flex items-center gap-2 pt-6">
              <input type="checkbox" checked={form.is_autopay === "true"} onChange={(e) => setForm({ ...form, is_autopay: e.target.checked ? "true" : "" })} className="h-4 w-4 rounded border-gray-300 text-indigo-600" />
              <span className="text-sm text-gray-600">Auto-pay enabled</span>
            </label>
          </div>
          <label className="block mt-4">
            <span className="text-xs font-medium text-gray-600">Notes</span>
            <textarea value={form.notes ?? ""} onChange={(e) => setForm({ ...form, notes: e.target.value })} rows={2} className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500" />
          </label>
          <div className="mt-6 flex items-center justify-between border-t border-gray-100 pt-4">
            <button onClick={() => setShowDelete(true)} className="text-sm font-medium text-red-600 hover:text-red-700">Delete Bill</button>
            <div className="flex gap-3">
              <button onClick={() => setIsEditing(false)} className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50">Cancel</button>
              <button onClick={handleSave} disabled={updateMutation.isPending} className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50">{updateMutation.isPending ? "Saving..." : "Save"}</button>
            </div>
          </div>
        </div>
        <ConfirmDialog open={showDelete} onClose={() => setShowDelete(false)} onConfirm={() => deleteMutation.mutate()} title="Delete Bill" message={`Delete "${bill.name}"? This cannot be undone.`} confirmLabel="Delete" loading={deleteMutation.isPending} />
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-5xl px-4 py-6 sm:px-6 lg:px-8">
      <div className="mb-4 flex items-center justify-between">
        <button onClick={() => router.back()} className="inline-flex items-center gap-1 text-sm text-gray-500 hover:text-gray-700">
          <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" d="M15.75 19.5L8.25 12l7.5-7.5" /></svg>Back
        </button>
        <div className="flex gap-2">
          <button onClick={startEditing} className="inline-flex items-center gap-1.5 rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 shadow-sm hover:bg-gray-50">
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" d="M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931z" /></svg>Edit
          </button>
          <button onClick={() => setShowDelete(true)} className="inline-flex items-center gap-1.5 rounded-md border border-red-300 bg-white px-3 py-1.5 text-sm font-medium text-red-700 shadow-sm hover:bg-red-50">
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" d="M14.74 9l-.346 9m-4.788 0L9.26 9M18.16 19.673a2.25 2.25 0 01-2.244 2.077H8.084a2.25 2.25 0 01-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 00-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 013.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 00-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.533 48.533 0 00-7.5 0" /></svg>Delete
          </button>
        </div>
      </div>

      <div className="rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
        <div className="flex items-start justify-between">
          <div>
            <h1 className="text-2xl font-bold text-gray-900">{bill.name}</h1>
            <div className="mt-2 flex flex-wrap gap-2">
              {bill.category && <span className="inline-flex items-center rounded-full bg-indigo-50 px-2.5 py-0.5 text-xs font-medium text-indigo-700">{bill.category}</span>}
              {bill.is_autopay && <span className="inline-flex items-center rounded-full bg-green-50 px-2.5 py-0.5 text-xs font-medium text-green-700">Auto-pay</span>}
              {propertyName && <Link href={`/dashboard/properties/${bill.property_id}`} className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-600 hover:bg-gray-200">{propertyName}</Link>}
            </div>
          </div>
          <div className="text-right">
            <p className="text-3xl font-bold text-gray-900">{fmtCurrency(bill.amount)}</p>
            {bill.due_day && <p className="text-xs text-gray-400">Due day {bill.due_day} each month</p>}
          </div>
        </div>

        <dl className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2 border-t border-gray-100 pt-4">
          {bill.account_number && <div><dt className="text-xs text-gray-400">Account Number</dt><dd className="text-sm font-medium text-gray-900">{bill.account_number}</dd></div>}
          {bill.payment_url && <div><dt className="text-xs text-gray-400">Payment URL</dt><dd className="text-sm font-medium text-gray-900"><a href={bill.payment_url} target="_blank" rel="noopener noreferrer" className="text-indigo-600 hover:text-indigo-500">{bill.payment_url}</a></dd></div>}
        </dl>
        {bill.notes && <div className="mt-4 rounded-md bg-gray-50 px-3 py-2 text-sm text-gray-600">{bill.notes}</div>}
      </div>

      <EntityResources entityType="bill" entityId={id} />

      <ConfirmDialog open={showDelete} onClose={() => setShowDelete(false)} onConfirm={() => deleteMutation.mutate()} title="Delete Bill" message={`Delete "${bill.name}"? This cannot be undone.`} confirmLabel="Delete" loading={deleteMutation.isPending} />
    </div>
  );
}
