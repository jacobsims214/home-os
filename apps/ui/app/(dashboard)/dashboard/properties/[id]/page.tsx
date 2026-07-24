"use client";

import { useState } from "react";
import { useParams, useRouter } from "next/navigation";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { propertyKeys, assetKeys, maintenanceKeys, calendarKeys } from "@/lib/query-keys";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import AddMaintenanceTaskModal from "@/components/maintenance/AddMaintenanceTaskModal";
import AssetCard from "@/components/asset/AssetCard";
import { Card } from "@/components/ui/Card";
import DetailPageLayout from "@/components/layout/DetailPageLayout";
import { TextInput } from "@/components/ui/TextInput";
import { MantineSelect as Select } from "@/components/ui/Select";
import { Button } from "@/components/ui/Button";
import type { PropertyDetailResponse, RoomListResponse } from "@/types/property";
import type { Asset } from "@/lib/types/api";
import type { MaintenanceTask } from "@/components/maintenance/types";

// ── Local type definitions (replacing deleted @/components/dashboard/dashboard-types) ──

interface Bill {
  id: string;
  name: string;
  amount: string;
  property_id?: string;
  category?: string;
  due_day?: number;
}

interface CalendarEvent {
  id: string;
  title: string;
  date?: string;
  start?: string;
  property_id?: string;
  event_type?: string;
}

// ── Helpers ──────────────────────────────────────────────────

function fmtCurrency(value: string | null | undefined): string {
  if (!value) return "—";
  const n = Number(value);
  if (Number.isNaN(n)) return "—";
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
}

function fmtCurrencyPrecise(value: string | null | undefined): string {
  if (!value) return "—";
  const n = Number(value);
  if (Number.isNaN(n)) return "—";
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function fmtDate(iso: string | null | undefined): string {
  if (!iso) return "—";
  try {
    return new Date(iso).toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" });
  } catch { return iso; }
}

function num(v: string | null | undefined): number {
  if (!v) return 0;
  const n = Number(v);
  return Number.isNaN(n) ? 0 : n;
}

/** Calculate monthly mortgage payment using standard amortization formula. */
function monthlyMortgagePayment(principal: number, annualRate: number, termMonths: number): number {
  if (principal <= 0 || termMonths <= 0) return 0;
  if (annualRate === 0) return principal / termMonths;
  const r = annualRate / 100 / 12;
  return (principal * r) / (1 - Math.pow(1 + r, -termMonths));
}

/** Calculate remaining mortgage balance after N months. */
function remainingBalance(principal: number, annualRate: number, termMonths: number, monthsElapsed: number): number {
  if (principal <= 0) return 0;
  if (monthsElapsed >= termMonths) return 0;
  if (annualRate === 0) return principal * (1 - monthsElapsed / termMonths);
  const r = annualRate / 100 / 12;
  const pmt = monthlyMortgagePayment(principal, annualRate, termMonths);
  return principal * Math.pow(1 + r, monthsElapsed) - pmt * (Math.pow(1 + r, monthsElapsed) - 1) / r;
}

/** Calculate months elapsed since a date string. */
function monthsSince(dateStr: string | null | undefined): number {
  if (!dateStr) return 0;
  const start = new Date(dateStr);
  if (isNaN(start.getTime())) return 0;
  const now = new Date();
  return Math.max(0, (now.getFullYear() - start.getFullYear()) * 12 + (now.getMonth() - start.getMonth()));
}

function statusBadge(status: string): { label: string; cls: string } {
  switch (status) {
    case "pending": return { label: "Pending", cls: "bg-amber-50 text-amber-700" };
    case "in_progress": return { label: "In Progress", cls: "bg-blue-50 text-blue-700" };
    case "done": return { label: "Done", cls: "bg-green-50 text-green-700" };
    case "skipped": return { label: "Skipped", cls: "bg-gray-100 text-gray-600" };
    default: return { label: status, cls: "bg-gray-100 text-gray-600" };
  }
}

// ── Edit form fields ─────────────────────────────────────────

const PROPERTY_TYPES = ["Single Family", "Condo", "Townhouse", "Multi-Family", "Land", "Commercial", "Other"];

function EditField({ label, value, onChange, type = "text", placeholder, required = false }: {
  label: string; value: string; onChange: (v: string) => void; type?: string; placeholder?: string; required?: boolean;
}) {
  return (
    <div>
      <label className="mb-1 block text-xs font-medium text-gray-600">
        {label} {required && <span className="text-red-500">*</span>}
      </label>
      <TextInput
        type={type}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        size="sm"
      />
    </div>
  );
}

// ── Page ─────────────────────────────────────────────────────

export default function PropertyDetailPage() {
  const params = useParams<{ id: string }>();
  const router = useRouter();
  const queryClient = useQueryClient();
  const propertyId = params.id;

  const [isEditing, setIsEditing] = useState(false);
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);
  const [showAddMaintenance, setShowAddMaintenance] = useState(false);
  const [showFinancials, setShowFinancials] = useState(false);
  const [editValues, setEditValues] = useState<Record<string, string>>({});

  // ── Data fetching ──────────────────────────────────────────

  const { data: propData, isLoading, isError, error } = useQuery({
    queryKey: propertyKeys.detail(propertyId),
    queryFn: () => apiFetch<PropertyDetailResponse>(`/api/v1/properties/${propertyId}`),
    enabled: !!propertyId,
  });

  const { data: roomsData } = useQuery({
    queryKey: [...propertyKeys.detail(propertyId), "rooms"],
    queryFn: () => apiFetch<RoomListResponse>(`/api/v1/properties/${propertyId}/rooms`),
    enabled: !!propertyId,
  });

  const { data: assets = [] } = useQuery({
    queryKey: assetKeys.byProperty(propertyId),
    queryFn: () => apiFetch<{ data: Asset[] }>("/api/v1/assets", { params: { property_id: propertyId } }).then((r) => r.data),
    enabled: !!propertyId,
  });

  const { data: maintenanceTasks = [] } = useQuery({
    queryKey: maintenanceKeys.byProperty(propertyId),
    queryFn: () => apiFetch<{ data: MaintenanceTask[] }>("/api/v1/maintenance/tasks", { params: { property_id: propertyId } }).then((r) => r.data),
    enabled: !!propertyId,
  });

  const { data: billsData } = useQuery({
    queryKey: ["bills", "property", propertyId],
    queryFn: () => apiFetch<{ data: Bill[] }>("/api/v1/bills"),
    enabled: !!propertyId,
  });

  const { data: calendarEvents = [] } = useQuery({
    queryKey: calendarKeys.eventsByProperty(propertyId),
    queryFn: () => apiFetch<{ data: CalendarEvent[] }>("/api/v1/calendars/events", { params: { property_id: propertyId } }).then((r) => r.data),
    enabled: !!propertyId,
  });

  const property = propData?.data;
  const rooms = roomsData?.data ?? [];
  const bills = (billsData?.data ?? []).filter((b) => b.property_id === propertyId);

  // Calendar events (all, not filtered by month)
  const monthEvents = calendarEvents;

  // ── Delete mutation ────────────────────────────────────────

  const deleteMutation = useMutation({
    mutationFn: () => apiFetch<void>(`/api/v1/properties/${propertyId}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: propertyKeys.lists() });
      router.push("/dashboard/properties");
    },
  });

  // ── Update mutation ────────────────────────────────────────

  const updateMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch(`/api/v1/properties/${propertyId}`, { method: "PUT", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: propertyKeys.detail(propertyId) });
      setIsEditing(false);
    },
  });

  // ── Edit mutation ──────────────────────────────────────────

  const editMutation = useMutation({
    mutationFn: (data: any) => apiFetch(`/api/v1/properties/${propertyId}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: propertyKeys.detail(propertyId) });
    },
  });

  // ── Edit handlers ──────────────────────────────────────────

  function startEditing() {
    if (!property) return;
    const f: Record<string, string> = {};
    for (const k of ["name","address","property_type","notes","purchase_price","purchase_date","current_value","down_payment","mortgage_amount","mortgage_rate","mortgage_term_months","mortgage_start_date","mortgage_lender","mortgage_account_number","property_tax_annual","property_tax_due_months","insurance_annual","insurance_provider","hoa_fee_monthly"]) {
      f[k] = ((property as unknown as Record<string, string | null>)[k]) ?? "";
    }
    setEditValues(f);
    setIsEditing(true);
  }

  function cancelEditing() {
    setIsEditing(false);
  }

  // ── Financial calculations ─────────────────────────────────

  const mortgagePrincipal = num(property?.mortgage_amount);
  const mortgageRate = num(property?.mortgage_rate);
  const mortgageTerm = num(property?.mortgage_term_months);
  const mortgageStart = property?.mortgage_start_date;
  const monthsElapsed = monthsSince(mortgageStart);
  const monthlyPayment = monthlyMortgagePayment(mortgagePrincipal, mortgageRate, mortgageTerm);
  const remaining = remainingBalance(mortgagePrincipal, mortgageRate, mortgageTerm, monthsElapsed);
  const totalPaid = monthlyPayment * monthsElapsed;
  const interestPaid = totalPaid - (mortgagePrincipal - remaining);
  const principalPaid = mortgagePrincipal - remaining;
  const payoffProgress = mortgagePrincipal > 0 ? Math.min(100, (principalPaid / mortgagePrincipal) * 100) : 0;

  const currentValue = num(property?.current_value);
  const equity = currentValue - remaining;
  const equityPct = currentValue > 0 ? (equity / currentValue) * 100 : 0;

  const monthlyTax = num(property?.property_tax_annual) / 12;
  const monthlyInsurance = num(property?.insurance_annual) / 12;
  const monthlyHOA = num(property?.hoa_fee_monthly);
  const monthlyBills = bills.reduce((sum, b) => sum + num(b.amount), 0);
  const totalMonthlyCost = monthlyPayment + monthlyTax + monthlyInsurance + monthlyHOA + monthlyBills;
  const totalAssetValue = Array.isArray(assets) ? assets.reduce((sum, a) => sum + num(a.purchase_price), 0) : 0;

  const hasFinancialData = mortgagePrincipal > 0 || currentValue > 0 || num(property?.purchase_price) > 0;

  // ── Loading / Error states ─────────────────────────────────

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-20">
        <svg className="h-6 w-6 animate-spin text-indigo-600" fill="none" viewBox="0 0 24 24" aria-hidden="true">
          <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
          <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
        </svg>
        <span className="ml-3 text-sm text-gray-500">Loading property...</span>
      </div>
    );
  }

  if (isError || !property) {
    return (
      <div className="mx-auto max-w-2xl px-4 py-12 text-center">
        <p className="text-sm text-red-600">Failed to load property. {(error as Error)?.message}</p>
        <Link href="/dashboard/properties" className="mt-4 inline-block text-sm font-medium text-indigo-600 hover:text-indigo-500">
          &larr; Back to properties
        </Link>
      </div>
    );
  }

  // ── EDIT MODE ──────────────────────────────────────────────

  if (isEditing) {
    return (
      <DetailPageLayout
        entityType="property"
        entityId={propertyId}
        title="Edit Property"
        isEditing={isEditing}
        onEdit={startEditing}
        onDelete={() => setShowDeleteConfirm(true)}
        onCancel={cancelEditing}
        onSave={() => editMutation.mutate(editValues)}
        isSaving={editMutation.isPending}
      >
        <div className="mb-6">
          <h2 className="mb-4 text-sm font-semibold uppercase tracking-wide text-gray-500">Basic Info</h2>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <EditField label="Property Name *" value={editValues.name ?? ""} onChange={(v) => setEditValues({ ...editValues, name: v })} placeholder="Main Residence" required />
            <EditField label="Address" value={editValues.address ?? ""} onChange={(v) => setEditValues({ ...editValues, address: v })} placeholder="123 Main St" />
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-600">
                Property Type <span className="text-red-500">*</span>
              </label>
              <Select
                value={editValues.property_type ?? ""}
                onChange={(value) => setEditValues({ ...editValues, property_type: value ?? "" })}
                placeholder="Select type..."
                size="sm"
                data={[{ value: "", label: "Select type..." }, ...PROPERTY_TYPES.map(t => ({ value: t, label: t }))]}
                clearable
              />
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-600">Notes</label>
              <TextInput
                value={editValues.notes ?? ""}
                onChange={(e) => setEditValues({ ...editValues, notes: e.target.value })}
                placeholder="Add notes..."
                size="sm"
              />
            </div>
          </div>
        </div>

        {/* Financial Details (collapsible using details element) */}
        <details className="mb-6 rounded-lg border border-gray-200">
          <summary className="cursor-pointer list-none px-4 py-3">
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                setShowFinancials(!showFinancials);
              }}
              className="flex w-full items-center justify-between text-sm font-semibold text-gray-700 hover:text-gray-900"
            >
              <span className="flex items-center gap-2">
                <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" d={showFinancials ? "M19.5 8.25l-7.5 7.5-7.5-7.5" : "M8.25 4.5l7.5 7.5-7.5 7.5"} />
                </svg>
                Financial Details
              </span>
              <svg className="h-4 w-4 transition-transform" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" d={showFinancials ? "M19.5 8.25l-7.5 7.5-7.5-7.5" : "M8.25 4.5l7.5 7.5-7.5 7.5"} />
              </svg>
            </button>
          </summary>
          <div className="border-t border-gray-200 p-4">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
              <EditField label="Purchase Price" value={editValues.purchase_price ?? ""} onChange={(v) => setEditValues({ ...editValues, purchase_price: v })} placeholder="$350,000" />
              <EditField label="Purchase Date" value={editValues.purchase_date ?? ""} onChange={(v) => setEditValues({ ...editValues, purchase_date: v })} type="date" />
              <EditField label="Current Value" value={editValues.current_value ?? ""} onChange={(v) => setEditValues({ ...editValues, current_value: v })} placeholder="$420,000" />
              <EditField label="Down Payment" value={editValues.down_payment ?? ""} onChange={(v) => setEditValues({ ...editValues, down_payment: v })} placeholder="$50,000" />
              <EditField label="Mortgage Amount" value={editValues.mortgage_amount ?? ""} onChange={(v) => setEditValues({ ...editValues, mortgage_amount: v })} placeholder="$300,000" />
              <EditField label="Mortgage Rate (%)" value={editValues.mortgage_rate ?? ""} onChange={(v) => setEditValues({ ...editValues, mortgage_rate: v })} placeholder="6.5" />
              <EditField label="Mortgage Term (months)" value={editValues.mortgage_term_months ?? ""} onChange={(v) => setEditValues({ ...editValues, mortgage_term_months: v })} placeholder="360" />
              <EditField label="Mortgage Start Date" value={editValues.mortgage_start_date ?? ""} onChange={(v) => setEditValues({ ...editValues, mortgage_start_date: v })} type="date" />
              <EditField label="Mortgage Lender" value={editValues.mortgage_lender ?? ""} onChange={(v) => setEditValues({ ...editValues, mortgage_lender: v })} placeholder="Wells Fargo" />
              <EditField label="Mortgage Account #" value={editValues.mortgage_account_number ?? ""} onChange={(v) => setEditValues({ ...editValues, mortgage_account_number: v })} />
              <EditField label="Property Tax (annual)" value={editValues.property_tax_annual ?? ""} onChange={(v) => setEditValues({ ...editValues, property_tax_annual: v })} placeholder="$3,000" />
              <EditField label="Tax Due Months" value={editValues.property_tax_due_months ?? ""} onChange={(v) => setEditValues({ ...editValues, property_tax_due_months: v })} placeholder="Jan, Jul" />
              <EditField label="Insurance (annual)" value={editValues.insurance_annual ?? ""} onChange={(v) => setEditValues({ ...editValues, insurance_annual: v })} placeholder="$1,800" />
              <EditField label="Insurance Provider" value={editValues.insurance_provider ?? ""} onChange={(v) => setEditValues({ ...editValues, insurance_provider: v })} placeholder="State Farm" />
              <EditField label="HOA Fee (monthly)" value={editValues.hoa_fee_monthly ?? ""} onChange={(v) => setEditValues({ ...editValues, hoa_fee_monthly: v })} placeholder="$0" />
            </div>
          </div>
        </details>

        {/* Actions */}
        <div className="mt-8 flex items-center justify-between border-t border-gray-200 pt-4">
          <button
            onClick={() => setShowDeleteConfirm(true)}
            className="text-sm font-medium text-red-600 hover:text-red-700"
          >
            Delete Property
          </button>
          <div className="flex gap-3">
            <Button variant="outline" onClick={cancelEditing}>
              Cancel
            </Button>
            <Button onClick={() => editMutation.mutate(editValues)} loading={editMutation.isPending}>
              {editMutation.isPending ? "Saving..." : "Save Changes"}
            </Button>
          </div>
        </div>

        <ConfirmDialog
          open={showDeleteConfirm}
          onClose={() => setShowDeleteConfirm(false)}
          onConfirm={() => deleteMutation.mutate()}
          title="Delete Property"
          message={`Are you sure you want to delete "${property.name}"? This cannot be undone.`}
          confirmLabel="Delete"
          loading={deleteMutation.isPending}
        />
      </DetailPageLayout>
    );
  }

  // ── VIEW MODE ──────────────────────────────────────────────

  return (
    <DetailPageLayout
      entityType="property"
      entityId={propertyId}
      title={property.name}
      isEditing={isEditing}
      onEdit={startEditing}
      onDelete={() => setShowDeleteConfirm(true)}
      onCancel={cancelEditing}
      onSave={() => editMutation.mutate(editValues)}
      isSaving={editMutation.isPending}
    >
      {/* ── Header Card ─────────────────────────────────────── */}
      <div className="rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0 flex-1">
            <h1 className="text-2xl font-bold tracking-tight text-gray-900">{property.name}</h1>
            {property.address && <p className="mt-1 text-sm text-gray-500">{property.address}</p>}
            <div className="mt-3 flex flex-wrap gap-2">
              {property.property_type && (
                <span className="inline-flex items-center rounded-full bg-indigo-50 px-2.5 py-0.5 text-xs font-medium text-indigo-700">
                  {property.property_type}
                </span>
              )}
            </div>
          </div>
        </div>
        {property.notes && (
          <div className="mt-4 rounded-md bg-gray-50 px-3 py-2 text-sm text-gray-600">{property.notes}</div>
        )}
      </div>

      {/* ── Financial Hub ───────────────────────────────────── */}
      {hasFinancialData && (
        <div className="mt-8 grid grid-cols-1 gap-4 lg:grid-cols-2">
          {/* Equity & Value Card */}
          <div className="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-sm font-semibold text-gray-900">Value & Equity</h2>
            </div>
            <div className="mt-3">
              <p className="text-3xl font-bold text-gray-900">{fmtCurrency(property.current_value)}</p>
              <p className="text-xs text-gray-400">Current value (user estimate)</p>
            </div>

            {currentValue > 0 && remaining > 0 && (
              <div className="mt-4">
                {/* Equity bar */}
                <div className="flex h-6 overflow-hidden rounded-full bg-gray-100">
                  <div className="flex items-center justify-center bg-green-500 text-xs font-medium text-white" style={{ width: `${equityPct}%` }}>
                    {equityPct > 15 && `${Math.round(equityPct)}%`}
                  </div>
                  <div className="flex items-center justify-center bg-orange-400 text-xs font-medium text-white" style={{ width: `${100 - equityPct}%` }}>
                    {(100 - equityPct) > 15 && `${Math.round(100 - equityPct)}%`}
                  </div>
                </div>
                <div className="mt-2 flex justify-between text-xs">
                  <span className="text-green-600">Equity: {fmtCurrency(equity.toFixed(2))}</span>
                  <span className="text-orange-600">Owed: {fmtCurrency(remaining.toFixed(2))}</span>
                </div>
              </div>
            )}

            <dl className="mt-4 space-y-1.5 border-t border-gray-100 pt-3">
              <div className="flex justify-between text-sm"><dt className="text-gray-500">Purchase Price</dt><dd className="font-medium text-gray-900">{fmtCurrency(property.purchase_price)}</dd></div>
              <div className="flex justify-between text-sm"><dt className="text-gray-500">Purchase Date</dt><dd className="font-medium text-gray-900">{fmtDate(property.purchase_date)}</dd></div>
              <div className="flex justify-between text-sm"><dt className="text-gray-500">Down Payment</dt><dd className="font-medium text-gray-900">{fmtCurrency(property.down_payment)}</dd></div>
            </dl>
          </div>

          {/* Monthly Cost of Ownership Card */}
          <div className="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-sm font-semibold text-gray-900">Monthly Cost</h2>
            </div>
            <div className="mt-3">
              <p className="text-3xl font-bold text-gray-900">{fmtCurrencyPrecise(totalMonthlyCost.toFixed(2))}<span className="text-base font-normal text-gray-400">/mo</span></p>
            </div>

            <dl className="mt-4 space-y-1.5">
              {monthlyPayment > 0 && (
                <div className="flex justify-between text-sm"><dt className="text-gray-500">Mortgage Payment</dt><dd className="font-medium text-gray-900">{fmtCurrencyPrecise(monthlyPayment.toFixed(2))}</dd></div>
              )}
              {monthlyTax > 0 && (
                <div className="flex justify-between text-sm"><dt className="text-gray-500">Property Tax</dt><dd className="font-medium text-gray-900">{fmtCurrencyPrecise(monthlyTax.toFixed(2))}</dd></div>
              )}
              {monthlyInsurance > 0 && (
                <div className="flex justify-between text-sm"><dt className="text-gray-500">Insurance</dt><dd className="font-medium text-gray-900">{fmtCurrencyPrecise(monthlyInsurance.toFixed(2))}</dd></div>
              )}
              {monthlyHOA > 0 && (
                <div className="flex justify-between text-sm"><dt className="text-gray-500">HOA</dt><dd className="font-medium text-gray-900">{fmtCurrencyPrecise(monthlyHOA.toFixed(2))}</dd></div>
              )}
              {monthlyBills > 0 && (
                <div className="flex justify-between text-sm"><dt className="text-gray-500">Bills (utilities etc.)</dt><dd className="font-medium text-gray-900">{fmtCurrencyPrecise(monthlyBills.toFixed(2))}</dd></div>
              )}
            </dl>

            {monthlyPayment === 0 && monthlyTax === 0 && monthlyInsurance === 0 && monthlyHOA === 0 && monthlyBills === 0 && (
              <p className="mt-3 text-xs text-gray-400">No cost data set. Click Edit to add financial details.</p>
            )}
          </div>
        </div>
      )}

      {/* ── Mortgage Amortization ──────────────────────────── */}
      {mortgagePrincipal > 0 && mortgageTerm > 0 && (
        <div className="mt-8 rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-sm font-semibold text-gray-900">Mortgage Amortization</h2>
          </div>
          <div className="mt-3 flex flex-wrap items-center gap-x-6 gap-y-1 text-sm text-gray-600">
            {property.mortgage_lender && <span><span className="text-gray-400">Lender:</span> {property.mortgage_lender}</span>}
            <span><span className="text-gray-400">Rate:</span> {property.mortgage_rate}%</span>
            <span><span className="text-gray-400">Term:</span> {mortgageTerm} months</span>
            {mortgageStart && <span><span className="text-gray-400">Started:</span> {fmtDate(mortgageStart)}</span>}
            <span><span className="text-gray-400">Months in:</span> {monthsElapsed} / {mortgageTerm}</span>
          </div>

          {/* Progress bar */}
          <div className="mt-4">
            <div className="flex justify-between text-xs text-gray-500 mb-1">
              <span>Principal paid: {fmtCurrency(principalPaid.toFixed(2))}</span>
              <span>{Math.round(payoffProgress)}% paid off</span>
            </div>
            <div className="h-3 overflow-hidden rounded-full bg-gray-100">
              <div className="h-full rounded-full bg-indigo-500 transition-all" style={{ width: `${payoffProgress}%` }} />
            </div>
          </div>

          <div className="mt-4 grid grid-cols-2 gap-4 sm:grid-cols-4">
            <div>
              <p className="text-xs text-gray-400">Monthly Payment</p>
              <p className="text-lg font-semibold text-gray-900">{fmtCurrencyPrecise(monthlyPayment.toFixed(2))}</p>
            </div>
            <div>
              <p className="text-xs text-gray-400">Principal Paid</p>
              <p className="text-lg font-semibold text-green-600">{fmtCurrency(principalPaid.toFixed(2))}</p>
            </div>
            <div>
              <p className="text-xs text-gray-400">Interest Paid</p>
              <p className="text-lg font-semibold text-orange-500">{fmtCurrency(interestPaid.toFixed(2))}</p>
            </div>
            <div>
              <p className="text-xs text-gray-400">Remaining Balance</p>
              <p className="text-lg font-semibold text-gray-900">{fmtCurrency(remaining.toFixed(2))}</p>
            </div>
          </div>
        </div>
      )}

      {/* ── Bills + Maintenance (side by side) ─────────────── */}
      <div className="mt-8 grid grid-cols-1 gap-4 lg:grid-cols-2">
        {/* Bills */}
        <div className="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-sm font-semibold text-gray-900">Bills</h2>
            <span className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-700">{bills.length}</span>
          </div>
          <div className="mt-3">
            {bills.length === 0 ? (
              <p className="text-sm text-gray-400 py-4 text-center">No bills tracked</p>
            ) : (
              <>
                <ul className="divide-y divide-gray-50">
                  {bills.slice(0, 5).map((bill) => (
                    <li key={bill.id} className="flex items-center justify-between py-2">
                      <div>
                        <p className="text-sm font-medium text-gray-900">{bill.name}</p>
                        <p className="text-xs text-gray-400">{bill.category ?? "—"}{bill.due_day ? ` · Due day ${bill.due_day}` : ""}</p>
                      </div>
                      <p className="text-sm font-medium text-gray-900">{fmtCurrencyPrecise(bill.amount)}</p>
                    </li>
                  ))}
                </ul>
                {bills.length > 0 && (
                  <div className="mt-2 flex justify-between border-t border-gray-100 pt-2 text-sm">
                    <span className="font-medium text-gray-500">Monthly Total</span>
                    <span className="font-bold text-gray-900">{fmtCurrencyPrecise(monthlyBills.toFixed(2))}</span>
                  </div>
                )}
              </>
            )}
          </div>
        </div>

        {/* Maintenance */}
        <div className="rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-sm font-semibold text-gray-900">Maintenance</h2>
            <span className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-700">{maintenanceTasks.length}</span>
          </div>
          <div className="mt-3">
            {maintenanceTasks.length === 0 ? (
              <p className="text-sm text-gray-400 py-4 text-center">No maintenance tasks</p>
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
      </div>

      {/* ── Assets ──────────────────────────────────────────── */}
      <div className="mt-8 rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-sm font-semibold text-gray-900">Assets</h2>
          <span className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-700">{assets.length}</span>
        </div>
        {totalAssetValue > 0 && (
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-xs text-gray-400">Total asset value:</span>
            <span className="text-sm font-bold text-gray-900">{fmtCurrency(totalAssetValue.toFixed(2))}</span>
          </div>
        )}
        <div className="mt-3">
          {assets.length === 0 ? (
            <p className="text-sm text-gray-400 py-4 text-center">No assets tracked</p>
          ) : (
            <ul className="divide-y divide-gray-50">
              {assets.slice(0, 5).map((asset) => (
                <li key={asset.id}>
                  <Link href={`/dashboard/assets/${asset.id}`} className="flex items-center justify-between py-2 hover:bg-gray-50 -mx-2 px-2 rounded cursor-pointer">
                    <div>
                      <p className="text-sm font-medium text-gray-900">{asset.name}</p>
                      <p className="text-xs text-gray-400">{[asset.category, asset.manufacturer, asset.model].filter(Boolean).join(" · ") || "—"}</p>
                    </div>
                    <div className="flex items-center gap-2">
                      {asset.purchase_price && <span className="text-xs text-gray-400">{fmtCurrency(asset.purchase_price)}</span>}
                      {asset.warranty_expiry && (
                        <span className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-xs ${new Date(asset.warranty_expiry) < new Date() ? "bg-red-50 text-red-600" : "bg-green-50 text-green-600"}`}>
                          {new Date(asset.warranty_expiry) < new Date() ? "Exp" : "Warr"}
                        </span>
                      )}
                    </div>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>

      {/* ── Calendar Events (this month) ───────────────────── */}
      <div className="mt-8 rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-sm font-semibold text-gray-900">Events</h2>
          <span className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-700">{monthEvents.length}</span>
        </div>
        <div className="mt-3">
          {monthEvents.length === 0 ? (
            <p className="text-sm text-gray-400 py-4 text-center">No events this month</p>
          ) : (
            <ul className="divide-y divide-gray-50">
              {monthEvents.slice(0, 6).map((event) => (
                <li key={event.id} className="flex items-center justify-between py-2">
                  <div>
                    <p className="text-sm font-medium text-gray-900">{event.title}</p>
                    <p className="text-xs text-gray-400">{fmtDate(event.start)}{event.event_type ? ` · ${event.event_type}` : ""}</p>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>

      {/* ── Rooms ───────────────────────────────────────────── */}
      <div className="mt-8 rounded-xl border border-gray-200 bg-white p-5 shadow-sm">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-sm font-semibold text-gray-900">Rooms</h2>
          <span className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-700">{rooms.length}</span>
        </div>
        <div className="mt-3">
          {rooms.length === 0 ? (
            <p className="text-sm text-gray-400 py-4 text-center">No rooms added</p>
          ) : (
            <div className="flex flex-wrap gap-2">
              {rooms.map((room) => (
                <span key={room.id} className="inline-flex items-center rounded-lg border border-gray-200 bg-gray-50 px-3 py-1.5 text-sm text-gray-700">
                  {room.name}{room.floor !== null && <span className="ml-1.5 text-xs text-gray-400">· Floor {room.floor}</span>}
                </span>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* ── Add Maintenance Task Modal ──────────────────────── */}
      <AddMaintenanceTaskModal
        open={showAddMaintenance}
        onClose={() => setShowAddMaintenance(false)}
        propertyId={propertyId}
        entityName={property.name}
      />

      {/* ── Delete confirmation ─────────────────────────────── */}
      <ConfirmDialog
        open={showDeleteConfirm}
        onClose={() => setShowDeleteConfirm(false)}
        onConfirm={() => deleteMutation.mutate()}
        title="Delete Property"
        message={`Are you sure you want to delete "${property.name}"? This will also remove associated data. This cannot be undone.`}
        confirmLabel="Delete Property"
        loading={deleteMutation.isPending}
      />
    </DetailPageLayout>
  );
}
