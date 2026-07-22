"use client";

import { useState } from "react";
import { useParams, useRouter } from "next/navigation";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { assetKeys, propertyKeys, maintenanceKeys } from "@/lib/query-keys";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import EntityResources from "@/components/EntityResources";
import AddMaintenanceTaskModal from "@/components/maintenance/AddMaintenanceTaskModal";
import type { Asset, Property } from "@/lib/types/api";
import type { MaintenanceTask } from "@/components/maintenance/types";

// ── Helpers ──────────────────────────────────────────────────

function fmtCurrency(v?: string): string {
  if (!v) return "—";
  const n = Number(v);
  if (Number.isNaN(n)) return "—";
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
}

function fmtDate(iso?: string): string {
  if (!iso) return "—";
  try { return new Date(iso).toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" }); }
  catch { return iso; }
}

function warrantyStatus(expiry?: string): { label: string; cls: string; expired: boolean } {
  if (!expiry) return { label: "No warranty", cls: "bg-gray-100 text-gray-500", expired: false };
  const expired = new Date(expiry) < new Date();
  if (expired) return { label: "Warranty Expired", cls: "bg-red-50 text-red-700", expired: true };
  return { label: `Under Warranty until ${fmtDate(expiry)}`, cls: "bg-green-50 text-green-700", expired: false };
}

function statusBadge(status: string): { label: string; cls: string } {
  switch (status) {
    case "pending": return { label: "Pending", cls: "bg-amber-50 text-amber-700" };
    case "in_progress": return { label: "In Progress", cls: "bg-blue-50 text-blue-700" };
    case "done": return { label: "Done", cls: "bg-green-50 text-green-700" };
    default: return { label: status, cls: "bg-gray-100 text-gray-600" };
  }
}

const ASSET_CATEGORIES = ["HVAC", "Appliance", "Electronics", "Furniture", "Plumbing", "Lighting", "Landscaping", "Security", "Tools", "Other"];

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

// ── Page ─────────────────────────────────────────────────────

export default function AssetDetailPage() {
  const params = useParams<{ id: string }>();
  const router = useRouter();
  const queryClient = useQueryClient();
  const id = params.id;

  const [isEditing, setIsEditing] = useState(false);
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);
  const [showAddMaintenance, setShowAddMaintenance] = useState(false);
  const [editForm, setEditForm] = useState<Record<string, string>>({});

  // ── Data ───────────────────────────────────────────────────

  const { data: resp, isLoading, isError, error } = useQuery({
    queryKey: assetKeys.detail(id),
    queryFn: () => apiFetch<{ data: Asset }>(`/api/v1/assets/${id}`),
    enabled: !!id,
  });
  const asset = resp?.data;

  const { data: propertiesResp } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () => apiFetch<{ data: Property[] }>("/api/v1/properties"),
  });
  const properties = propertiesResp?.data ?? [];
  const propertyName = properties.find((p) => p.id === asset?.property_id)?.name;

  const { data: maintenanceResp } = useQuery({
    queryKey: [...assetKeys.detail(id), "maintenance"],
    queryFn: () => apiFetch<{ data: MaintenanceTask[] }>("/api/v1/maintenance/tasks", { params: { property_id: asset?.property_id } }).then((r) => r.data),
    enabled: !!asset?.property_id,
  });
  const maintenanceTasks = (maintenanceResp ?? []).filter((t) => t.name?.toLowerCase().includes(asset?.name?.toLowerCase() ?? "___"));

  // ── Mutations ──────────────────────────────────────────────

  const deleteMutation = useMutation({
    mutationFn: () => apiFetch<void>(`/api/v1/assets/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: assetKeys.all });
      router.push("/dashboard/assets");
    },
  });

  const updateMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) => apiFetch(`/api/v1/assets/${id}`, { method: "PUT", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: assetKeys.detail(id) });
      setIsEditing(false);
    },
  });

  // ── Edit handlers ──────────────────────────────────────────

  function startEditing() {
    if (!asset) return;
    const f: Record<string, string> = {};
    for (const k of ["name","property_id","room_id","category","manufacturer","model","serial_number","purchase_date","purchase_price","warranty_expiry","notes"]) {
      f[k] = ((asset as unknown as Record<string, string | null>)[k]) ?? "";
    }
    setEditForm(f);
    setIsEditing(true);
  }

  function handleSave() {
    const body: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(editForm)) {
      if (v.trim()) body[k] = v.trim();
    }
    if (!body.name) body.name = asset?.name;
    updateMutation.mutate(body);
  }

  // ── Loading / Error ────────────────────────────────────────

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-20">
        <svg className="h-6 w-6 animate-spin text-indigo-600" fill="none" viewBox="0 0 24 24" aria-hidden="true">
          <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
          <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
        </svg>
        <span className="ml-3 text-sm text-gray-500">Loading asset...</span>
      </div>
    );
  }

  if (isError || !asset) {
    return (
      <div className="mx-auto max-w-2xl px-4 py-12 text-center">
        <p className="text-sm text-red-600">{error instanceof ApiError && error.status === 404 ? "Asset not found" : "Failed to load asset"}</p>
        <Link href="/dashboard/assets" className="mt-4 inline-block text-sm font-medium text-indigo-600 hover:text-indigo-500">&larr; Back to Assets</Link>
      </div>
    );
  }

  const warranty = warrantyStatus(asset.warranty_expiry ?? undefined);

  // ── EDIT MODE ──────────────────────────────────────────────

  if (isEditing) {
    return (
      <div className="mx-auto max-w-4xl px-4 py-6 sm:px-6 lg:px-8">
        <button onClick={() => setIsEditing(false)} className="mb-4 text-sm font-medium text-gray-500 hover:text-gray-700">&larr; Cancel</button>

        <div className="rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
          <h1 className="text-xl font-bold text-gray-900 mb-6">Edit Asset</h1>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <EditField label="Name *" value={editForm.name ?? ""} onChange={(v) => setEditForm({ ...editForm, name: v })} />
            <label className="block">
              <span className="text-xs font-medium text-gray-600">Category</span>
              <select value={editForm.category ?? ""} onChange={(e) => setEditForm({ ...editForm, category: e.target.value })}
                className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500">
                <option value="">Select...</option>
                {ASSET_CATEGORIES.map((c) => <option key={c} value={c}>{c}</option>)}
              </select>
            </label>
            <label className="block">
              <span className="text-xs font-medium text-gray-600">Property</span>
              <select value={editForm.property_id ?? ""} onChange={(e) => setEditForm({ ...editForm, property_id: e.target.value })}
                className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500">
                <option value="">None</option>
                {properties.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
            </label>
            <EditField label="Room ID" value={editForm.room_id ?? ""} onChange={(v) => setEditForm({ ...editForm, room_id: v })} />
            <EditField label="Manufacturer" value={editForm.manufacturer ?? ""} onChange={(v) => setEditForm({ ...editForm, manufacturer: v })} placeholder="Carrier" />
            <EditField label="Model" value={editForm.model ?? ""} onChange={(v) => setEditForm({ ...editForm, model: v })} placeholder="24ACC6" />
            <EditField label="Serial Number" value={editForm.serial_number ?? ""} onChange={(v) => setEditForm({ ...editForm, serial_number: v })} />
            <EditField label="Purchase Price" value={editForm.purchase_price ?? ""} onChange={(v) => setEditForm({ ...editForm, purchase_price: v })} placeholder="$1,200" />
            <EditField label="Purchase Date" value={editForm.purchase_date ?? ""} onChange={(v) => setEditForm({ ...editForm, purchase_date: v })} type="date" />
            <EditField label="Warranty Expiry" value={editForm.warranty_expiry ?? ""} onChange={(v) => setEditForm({ ...editForm, warranty_expiry: v })} type="date" />
          </div>

          <label className="block mt-4">
            <span className="text-xs font-medium text-gray-600">Notes</span>
            <textarea value={editForm.notes ?? ""} onChange={(e) => setEditForm({ ...editForm, notes: e.target.value })} rows={2}
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500" />
          </label>

          <div className="mt-6 flex items-center justify-between border-t border-gray-100 pt-4">
            <button onClick={() => setShowDeleteConfirm(true)} className="text-sm font-medium text-red-600 hover:text-red-700">Delete Asset</button>
            <div className="flex gap-3">
              <button onClick={() => setIsEditing(false)} className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50">Cancel</button>
              <button onClick={handleSave} disabled={updateMutation.isPending} className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50">
                {updateMutation.isPending ? "Saving..." : "Save Changes"}
              </button>
            </div>
          </div>
        </div>

        <ConfirmDialog open={showDeleteConfirm} onClose={() => setShowDeleteConfirm(false)} onConfirm={() => deleteMutation.mutate()}
          title="Delete Asset" message={`Delete "${asset.name}"? This cannot be undone.`} confirmLabel="Delete" loading={deleteMutation.isPending} />
      </div>
    );
  }

  // ── VIEW MODE ──────────────────────────────────────────────

  return (
    <div className="mx-auto max-w-5xl px-4 py-6 sm:px-6 lg:px-8">
      {/* Back + Actions */}
      <div className="mb-4 flex items-center justify-between">
        <button onClick={() => router.back()} className="inline-flex items-center gap-1 text-sm font-medium text-gray-500 hover:text-gray-700">
          <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" d="M15.75 19.5L8.25 12l7.5-7.5" /></svg>
          Back
        </button>
        <div className="flex gap-2">
          <button onClick={startEditing} className="inline-flex items-center gap-1.5 rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 shadow-sm hover:bg-gray-50">
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" d="M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931z" /></svg>
            Edit
          </button>
          <button onClick={() => setShowDeleteConfirm(true)} className="inline-flex items-center gap-1.5 rounded-md border border-red-300 bg-white px-3 py-1.5 text-sm font-medium text-red-700 shadow-sm hover:bg-red-50">
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" d="M14.74 9l-.346 9m-4.788 0L9.26 9M18.16 19.673a2.25 2.25 0 01-2.244 2.077H8.084a2.25 2.25 0 01-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 00-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 013.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 00-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.533 48.533 0 00-7.5 0" /></svg>
            Delete
          </button>
        </div>
      </div>

      {/* ── Header Card ─────────────────────────────────────── */}
      <div className="rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0 flex-1">
            <h1 className="text-2xl font-bold tracking-tight text-gray-900">{asset.name}</h1>
            <div className="mt-2 flex flex-wrap gap-2">
              {asset.category && <span className="inline-flex items-center rounded-full bg-indigo-50 px-2.5 py-0.5 text-xs font-medium text-indigo-700">{asset.category}</span>}
              {propertyName && (
                <Link href={`/dashboard/properties/${asset.property_id}`} className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-600 hover:bg-gray-200">
                  {propertyName}
                </Link>
              )}
            </div>
          </div>
          <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${warranty.cls}`}>{warranty.label}</span>
        </div>

        {asset.notes && <div className="mt-4 rounded-md bg-gray-50 px-3 py-2 text-sm text-gray-600">{asset.notes}</div>}
      </div>

      {/* ── Details Grid ────────────────────────────────────── */}
      <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
          <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-500">Product Info</h2>
          <dl className="mt-3 space-y-2">
            <div className="flex justify-between text-sm"><dt className="text-gray-500">Manufacturer</dt><dd className="font-medium text-gray-900">{asset.manufacturer ?? "—"}</dd></div>
            <div className="flex justify-between text-sm"><dt className="text-gray-500">Model</dt><dd className="font-medium text-gray-900">{asset.model ?? "—"}</dd></div>
            <div className="flex justify-between text-sm"><dt className="text-gray-500">Serial Number</dt><dd className="font-medium text-gray-900">{asset.serial_number ?? "—"}</dd></div>
          </dl>
        </div>

        <div className="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
          <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-500">Purchase & Warranty</h2>
          <dl className="mt-3 space-y-2">
            <div className="flex justify-between text-sm"><dt className="text-gray-500">Purchase Price</dt><dd className="font-medium text-gray-900">{fmtCurrency(asset.purchase_price)}</dd></div>
            <div className="flex justify-between text-sm"><dt className="text-gray-500">Purchase Date</dt><dd className="font-medium text-gray-900">{fmtDate(asset.purchase_date)}</dd></div>
            <div className="flex justify-between text-sm"><dt className="text-gray-500">Warranty Expiry</dt><dd className="font-medium text-gray-900">{fmtDate(asset.warranty_expiry)}</dd></div>
          </dl>
        </div>
      </div>

      {/* ── Maintenance History ─────────────────────────────── */}
      <div className="mt-4 rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold text-gray-900">Maintenance History ({maintenanceTasks.length})</h2>
          <div className="flex items-center gap-3">
            <button onClick={() => setShowAddMaintenance(true)} className="text-xs font-medium text-indigo-600 hover:text-indigo-500">+ Add Task</button>
            <Link href="/dashboard/maintenance" className="text-xs font-medium text-indigo-600 hover:text-indigo-500">View all →</Link>
          </div>
        </div>
        <div className="mt-3">
          {maintenanceTasks.length === 0 ? (
            <p className="text-sm text-gray-400 py-4 text-center">No maintenance tasks for this asset</p>
          ) : (
            <ul className="divide-y divide-gray-50">
              {maintenanceTasks.slice(0, 5).map((task) => {
                const s = statusBadge(task.status);
                return (
                  <li key={task.id} className="flex items-center justify-between py-2">
                    <div>
                      <p className="text-sm font-medium text-gray-900">{task.name}</p>
                      <p className="text-xs text-gray-400">{task.due_date ? `Due ${fmtDate(task.due_date)}` : "No due date"}</p>
                    </div>
                    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${s.cls}`}>{s.label}</span>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      </div>

      {/* ── EntityResources ─────────────────────────────────── */}
      <EntityResources entityType="asset" entityId={id} />

      {/* ── Add Maintenance Task Modal ──────────────────────── */}
      <AddMaintenanceTaskModal
        open={showAddMaintenance}
        onClose={() => setShowAddMaintenance(false)}
        assetId={id}
        propertyId={asset.property_id}
        entityName={asset.name}
      />

      {/* ── Delete confirmation ─────────────────────────────── */}
      <ConfirmDialog open={showDeleteConfirm} onClose={() => setShowDeleteConfirm(false)} onConfirm={() => deleteMutation.mutate()}
        title="Delete Asset" message={`Delete "${asset.name}"? This cannot be undone.`} confirmLabel="Delete" loading={deleteMutation.isPending} />
    </div>
  );
}
