"use client";

import { useEffect, useState, useCallback, type FormEvent } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { useRecentStore } from "@/stores/recent";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";
import Modal from "@/components/ui/Modal";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import EntityResources from "@/components/EntityResources";
import AddMaintenanceTaskModal from "@/components/maintenance/AddMaintenanceTaskModal";

// ─── Vehicle type from Go model ─────────────────────────────────

interface Vehicle {
  id: string;
  household_id: string;
  year: number | null;
  make: string | null;
  model: string | null;
  vin: string | null;
  license_plate: string | null;
  color: string | null;
  notes: string | null;
  purchase_price: string | null;
  purchase_date: string | null;
  lender: string | null;
  loan_amount: string | null;
  loan_term_months: number | null;
  monthly_payment: string | null;
  registration_renewal_mon: number | null;
  registration_cost: string | null;
  insurance_provider: string | null;
  insurance_cost: string | null;
  created_at: string;
  updated_at: string;
}

interface VehicleResponse {
  data: Vehicle;
}

// ─── Page component ──────────────────────────────────────────

export default function VehicleDetailPage() {
  const params = useParams();
  const id = params.id as string;
  const queryClient = useQueryClient();

  // ── Edit mode state ──────────────────────────────────────
  const [isEditing, setIsEditing] = useState(false);
  const [showAddMaintenance, setShowAddMaintenance] = useState(false);

  // ── Edit form fields ─────────────────────────────────────
  const [editYear, setEditYear] = useState("");
  const [editMake, setEditMake] = useState("");
  const [editModel, setEditModel] = useState("");
  const [editVin, setEditVin] = useState("");
  const [editLicensePlate, setEditLicensePlate] = useState("");
  const [editColor, setEditColor] = useState("");
  const [editNotes, setEditNotes] = useState("");
  const [editPurchasePrice, setEditPurchasePrice] = useState("");
  const [editPurchaseDate, setEditPurchaseDate] = useState("");
  const [editLender, setEditLender] = useState("");
  const [editLoanAmount, setEditLoanAmount] = useState("");
  const [editLoanTerm, setEditLoanTerm] = useState("");
  const [editMonthlyPayment, setEditMonthlyPayment] = useState("");
  const [editRegRenewalMon, setEditRegRenewalMon] = useState("");
  const [editRegCost, setEditRegCost] = useState("");
  const [editInsProvider, setEditInsProvider] = useState("");
  const [editInsCost, setEditInsCost] = useState("");
  const [editError, setEditError] = useState("");

  // ── Delete state ─────────────────────────────────────────
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);

  // ── Fetch vehicle ────────────────────────────────────────
  const {
    data: vehicleResp,
    isLoading,
    isError,
    error,
  } = useQuery({
    queryKey: ["vehicle", id],
    queryFn: () => apiFetch<VehicleResponse>(`/api/v1/vehicles/${id}`),
    enabled: !!id,
  });

  const vehicle = vehicleResp?.data;

  // ── Record visit in recent store ──────────────────────────
  useEffect(() => {
    if (vehicle) {
      useRecentStore.getState().addItem({
        entity_type: "vehicle",
        entity_id: vehicle.id,
        title: [vehicle.year, vehicle.make, vehicle.model].filter(Boolean).join(" ") || "Vehicle",
      });
    }
  }, [vehicle]);

  // ── Edit mutation ────────────────────────────────────────
  const editMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<VehicleResponse>(`/api/v1/vehicles/${id}`, { method: "PUT", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["vehicle", id] });
      queryClient.invalidateQueries({ queryKey: ["vehicles"] });
      setIsEditing(false);
      setEditError("");
    },
    onError: (err: Error) => {
      setEditError(err.message);
    },
  });

  // ── Delete mutation ──────────────────────────────────────
  const deleteMutation = useMutation({
    mutationFn: () => apiFetch(`/api/v1/vehicles/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["vehicles"] });
      setShowDeleteConfirm(false);
      window.location.href = "/dashboard/vehicles";
    },
    onError: (e: unknown) => {
      console.error("Failed to delete vehicle", e);
    },
  });

  // ── Enter edit mode ──────────────────────────────────────
  const startEditing = useCallback(() => {
    if (!vehicle) return;
    setEditYear(vehicle.year?.toString() ?? "");
    setEditMake(vehicle.make ?? "");
    setEditModel(vehicle.model ?? "");
    setEditVin(vehicle.vin ?? "");
    setEditLicensePlate(vehicle.license_plate ?? "");
    setEditColor(vehicle.color ?? "");
    setEditNotes(vehicle.notes ?? "");
    setEditPurchasePrice(vehicle.purchase_price ?? "");
    setEditPurchaseDate(vehicle.purchase_date ?? "");
    setEditLender(vehicle.lender ?? "");
    setEditLoanAmount(vehicle.loan_amount ?? "");
    setEditLoanTerm(vehicle.loan_term_months?.toString() ?? "");
    setEditMonthlyPayment(vehicle.monthly_payment ?? "");
    setEditRegRenewalMon(vehicle.registration_renewal_mon?.toString() ?? "");
    setEditRegCost(vehicle.registration_cost ?? "");
    setEditInsProvider(vehicle.insurance_provider ?? "");
    setEditInsCost(vehicle.insurance_cost ?? "");
    setEditError("");
    setIsEditing(true);
  }, [vehicle]);

  // ── Save edit ────────────────────────────────────────────
  const handleSave = (e: FormEvent) => {
    e.preventDefault();
    setEditError("");

    const body: Record<string, unknown> = {};
    if (editYear) body.year = parseInt(editYear, 10);
    if (editMake.trim()) body.make = editMake.trim();
    if (editModel.trim()) body.model = editModel.trim();
    if (editVin.trim()) body.vin = editVin.trim();
    if (editLicensePlate.trim()) body.license_plate = editLicensePlate.trim();
    if (editColor.trim()) body.color = editColor.trim();
    if (editNotes.trim()) body.notes = editNotes.trim();
    if (editPurchasePrice) body.purchase_price = editPurchasePrice;
    if (editPurchaseDate) body.purchase_date = editPurchaseDate;
    if (editLender.trim()) body.lender = editLender.trim();
    if (editLoanAmount) body.loan_amount = editLoanAmount;
    if (editLoanTerm) body.loan_term_months = parseInt(editLoanTerm, 10);
    if (editMonthlyPayment) body.monthly_payment = editMonthlyPayment;
    if (editRegRenewalMon) body.registration_renewal_mon = parseInt(editRegRenewalMon, 10);
    if (editRegCost) body.registration_cost = editRegCost;
    if (editInsProvider.trim()) body.insurance_provider = editInsProvider.trim();
    if (editInsCost) body.insurance_cost = editInsCost;

    editMutation.mutate(body);
  };

  // ── Cancel edit ──────────────────────────────────────────
  const cancelEditing = () => {
    setIsEditing(false);
    setEditError("");
  };

  // ── Loading state ────────────────────────────────────────
  if (isLoading) {
    return (
      <div className="p-6">
        <div className="mb-6">
          <div className="h-5 w-16 animate-pulse rounded bg-gray-200" />
        </div>
        <div className="overflow-hidden rounded-lg border border-gray-200 bg-white">
          <div className="p-6 space-y-4">
            {[1, 2, 3, 4, 5].map((i) => (
              <div key={i}>
                <div className="h-3 w-20 animate-pulse rounded bg-gray-100" />
                <div className="mt-1 h-5 w-48 animate-pulse rounded bg-gray-200" />
              </div>
            ))}
          </div>
        </div>
      </div>
    );
  }

  // ── Error state ──────────────────────────────────────────
  if (isError) {
    const message =
      error instanceof ApiError
        ? error.status === 404
          ? "Vehicle not found"
          : error.message
        : "Failed to load vehicle";

    return (
      <div className="flex flex-col items-center justify-center p-12">
        <div className="rounded-lg bg-red-50 p-6 text-center">
          <p className="text-red-700 font-medium">{message}</p>
          <Link href="/dashboard/vehicles">
            <Button className="mt-4">Back to Vehicles</Button>
          </Link>
        </div>
      </div>
    );
  }

  if (!vehicle) {
    return (
      <div className="p-6">
        <Link
          href="/dashboard/vehicles"
          className="text-sm text-indigo-600 hover:text-indigo-500"
        >
          &larr; Back to Vehicles
        </Link>
        <p className="mt-6 text-gray-500">Vehicle data unavailable.</p>
      </div>
    );
  }

  return (
    <div className="p-6">
      {/* Back navigation */}
      <div className="mb-6 flex items-center justify-between">
        <Link
          href="/dashboard/vehicles"
          className="inline-flex items-center text-sm text-indigo-600 hover:text-indigo-500"
        >
          <svg className="mr-1 h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" d="M10.5 19.5L3 12m0 0l7.5-7.5M3 12h18" />
          </svg>
          Back to Vehicles
        </Link>

        {/* Action buttons */}
        <div className="flex gap-2">
          {!isEditing ? (
            <>
              <button
                onClick={startEditing}
                className="inline-flex items-center rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 transition-colors"
              >
                <svg className="mr-1.5 h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" d="M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931zm0 0L19.5 7.125M18 14v4.75A2.25 2.25 0 0115.75 21H5.25A2.25 2.25 0 013 18.75V8.25A2.25 2.25 0 015.25 6H10" />
                </svg>
                Edit
              </button>
              <button
                onClick={() => setShowDeleteConfirm(true)}
                className="inline-flex items-center rounded-md border border-red-300 bg-white px-3 py-1.5 text-sm font-medium text-red-700 hover:bg-red-50 transition-colors"
              >
                <svg className="mr-1.5 h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" d="M14.74 9l-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 01-2.244 2.077H8.084a2.25 2.25 0 01-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 00-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 013.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 00-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 00-7.5 0" />
                </svg>
                Delete
              </button>
            </>
          ) : null}
        </div>
      </div>

      {/* Vehicle detail card — view mode */}
      {!isEditing && (
        <div className="overflow-hidden rounded-lg border border-gray-200 bg-white">
          {/* Header */}
          <div className="border-b border-gray-200 px-6 py-4">
            <h1 className="text-xl font-bold text-gray-900">
              {[vehicle.year, vehicle.make, vehicle.model].filter(Boolean).join(" ")}
            </h1>
            {vehicle.color && (
              <p className="mt-1 text-sm text-gray-500">{vehicle.color}</p>
            )}
          </div>

          {/* Fields */}
          <div className="px-6 py-4">
            <h2 className="text-sm font-semibold text-gray-700 mb-3 uppercase tracking-wider">Details</h2>
            <dl className="grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-2">
              <Field label="Year" value={vehicle.year?.toString()} />
              <Field label="Make" value={vehicle.make ?? undefined} />
              <Field label="Model" value={vehicle.model ?? undefined} />
              <Field label="VIN" value={vehicle.vin ?? undefined} />
              <Field label="License Plate" value={vehicle.license_plate ?? undefined} />
              <Field label="Color" value={vehicle.color ?? undefined} />
            </dl>

            {/* Financial Section */}
            {(vehicle.purchase_price || vehicle.loan_amount || vehicle.insurance_cost) && (
              <>
                <h2 className="text-sm font-semibold text-gray-700 mb-3 mt-6 uppercase tracking-wider">Financial</h2>
                <dl className="grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-2">
                  <Field label="Purchase Price" value={vehicle.purchase_price ? `$${fmtNumeric(vehicle.purchase_price)}` : undefined} />
                  <Field label="Purchase Date" value={vehicle.purchase_date ?? undefined} />
                  <Field label="Lender" value={vehicle.lender ?? undefined} />
                  <Field label="Loan Amount" value={vehicle.loan_amount ? `$${fmtNumeric(vehicle.loan_amount)}` : undefined} />
                  <Field label="Loan Term" value={vehicle.loan_term_months ? `${vehicle.loan_term_months} months` : undefined} />
                  <Field label="Monthly Payment" value={vehicle.monthly_payment ? `$${fmtNumeric(vehicle.monthly_payment)}` : undefined} />
                  <Field label="Registration Renewal" value={vehicle.registration_renewal_mon ? `Month ${vehicle.registration_renewal_mon}` : undefined} />
                  <Field label="Registration Cost" value={vehicle.registration_cost ? `$${fmtNumeric(vehicle.registration_cost)}` : undefined} />
                  <Field label="Insurance Provider" value={vehicle.insurance_provider ?? undefined} />
                  <Field label="Insurance Cost" value={vehicle.insurance_cost ? `$${fmtNumeric(vehicle.insurance_cost)}` : undefined} />
                </dl>
              </>
            )}

            {vehicle.notes && (
              <div className="mt-6 border-t border-gray-100 pt-4">
                <dt className="text-xs font-medium text-gray-500">Notes</dt>
                <dd className="mt-1 text-sm text-gray-900 whitespace-pre-wrap">
                  {vehicle.notes}
                </dd>
              </div>
            )}

            <dl className="grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-2 mt-6 border-t border-gray-100 pt-4">
              <Field label="Created" value={formatDate(vehicle.created_at)} />
              <Field label="Last Updated" value={formatDate(vehicle.updated_at)} />
            </dl>
          </div>
        </div>
      )}

      {/* Inline edit form — edit mode */}
      {isEditing && (
        <form
          onSubmit={handleSave}
          className="overflow-hidden rounded-lg border border-indigo-200 bg-white"
        >
          <div className="border-b border-indigo-100 bg-indigo-50 px-6 py-3">
            <h2 className="text-base font-semibold text-indigo-900">
              Editing: {[vehicle.year, vehicle.make, vehicle.model].filter(Boolean).join(" ")}
            </h2>
          </div>
          <div className="px-6 py-4 space-y-4">
            {editError && (
              <div className="rounded-md bg-red-50 p-3 text-sm text-red-700">{editError}</div>
            )}

            <h3 className="text-sm font-semibold text-gray-700 uppercase tracking-wider">Details</h3>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Year"
                type="number"
                value={editYear}
                onChange={(e) => setEditYear(e.target.value)}
                placeholder="e.g. 2023"
              />
              <Input
                label="Make"
                value={editMake}
                onChange={(e) => setEditMake(e.target.value)}
                placeholder="e.g. Toyota"
              />
            </div>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Model"
                value={editModel}
                onChange={(e) => setEditModel(e.target.value)}
                placeholder="e.g. Camry"
              />
              <Input
                label="Color"
                value={editColor}
                onChange={(e) => setEditColor(e.target.value)}
                placeholder="e.g. Silver"
              />
            </div>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="VIN"
                value={editVin}
                onChange={(e) => setEditVin(e.target.value)}
                placeholder="Vehicle identification number"
              />
              <Input
                label="License Plate"
                value={editLicensePlate}
                onChange={(e) => setEditLicensePlate(e.target.value)}
                placeholder="e.g. ABC-1234"
              />
            </div>

            <h3 className="text-sm font-semibold text-gray-700 uppercase tracking-wider pt-2">Financial</h3>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Purchase Price"
                value={editPurchasePrice}
                onChange={(e) => setEditPurchasePrice(e.target.value)}
                placeholder="e.g. 35000.00"
              />
              <Input
                label="Purchase Date"
                type="date"
                value={editPurchaseDate}
                onChange={(e) => setEditPurchaseDate(e.target.value)}
              />
            </div>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Lender"
                value={editLender}
                onChange={(e) => setEditLender(e.target.value)}
                placeholder="e.g. Bank of America"
              />
              <Input
                label="Loan Amount"
                value={editLoanAmount}
                onChange={(e) => setEditLoanAmount(e.target.value)}
                placeholder="e.g. 25000.00"
              />
            </div>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Loan Term (months)"
                type="number"
                value={editLoanTerm}
                onChange={(e) => setEditLoanTerm(e.target.value)}
                placeholder="e.g. 60"
              />
              <Input
                label="Monthly Payment"
                value={editMonthlyPayment}
                onChange={(e) => setEditMonthlyPayment(e.target.value)}
                placeholder="e.g. 450.00"
              />
            </div>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Registration Renewal Month"
                type="number"
                min="1"
                max="12"
                value={editRegRenewalMon}
                onChange={(e) => setEditRegRenewalMon(e.target.value)}
                placeholder="e.g. 6"
              />
              <Input
                label="Registration Cost"
                value={editRegCost}
                onChange={(e) => setEditRegCost(e.target.value)}
                placeholder="e.g. 150.00"
              />
            </div>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Insurance Provider"
                value={editInsProvider}
                onChange={(e) => setEditInsProvider(e.target.value)}
                placeholder="e.g. GEICO"
              />
              <Input
                label="Insurance Cost"
                value={editInsCost}
                onChange={(e) => setEditInsCost(e.target.value)}
                placeholder="e.g. 1200.00"
              />
            </div>

            <Input
              label="Notes"
              value={editNotes}
              onChange={(e) => setEditNotes(e.target.value)}
              placeholder="Any notes about this vehicle"
            />

            <div className="flex justify-end gap-3 pt-2">
              <button
                type="button"
                onClick={cancelEditing}
                disabled={editMutation.isPending}
                className="inline-flex items-center justify-center rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-semibold text-gray-700 hover:bg-gray-50 disabled:bg-gray-100"
              >
                Cancel
              </button>
              <Button type="submit" loading={editMutation.isPending}>
                Save Changes
              </Button>
            </div>
          </div>
        </form>
      )}

      {/* Files, Notes, and Passwords sections */}
      <EntityResources entityType="vehicle" entityId={id} />

      <AddMaintenanceTaskModal
        open={showAddMaintenance}
        onClose={() => setShowAddMaintenance(false)}
        vehicleId={id}
        entityName={vehicle?.year && vehicle?.make && vehicle?.model ? `${vehicle.year} ${vehicle.make} ${vehicle.model}` : "Vehicle"}
      />

      {/* Delete confirmation */}
      <ConfirmDialog
        open={showDeleteConfirm}
        onClose={() => setShowDeleteConfirm(false)}
        onConfirm={() => deleteMutation.mutate()}
        title="Delete Vehicle"
        message={`Are you sure you want to delete ${[vehicle.year, vehicle.make, vehicle.model].filter(Boolean).join(" ") || "this vehicle"}? This action cannot be undone.`}
        loading={deleteMutation.isPending}
      />
    </div>
  );
}

// ─── Helper components ────────────────────────────────────────

/** Renders a single detail field (label + value or placeholder). */
function Field({ label, value }: { label: string; value?: string }) {
  return (
    <div>
      <dt className="text-xs font-medium text-gray-500">{label}</dt>
      <dd className="mt-1 text-sm text-gray-900">
        {value ?? <span className="text-gray-400">—</span>}
      </dd>
    </div>
  );
}

/** Formats a string numeric value into a readable number string. */
function fmtNumeric(val: string): string {
  const n = parseFloat(val);
  if (isNaN(n)) return val;
  return n.toLocaleString("en-US", { maximumFractionDigits: 0 });
}

/** Formats an ISO timestamp into a human-readable date. */
function formatDate(iso: string): string {
  try {
    return new Date(iso).toLocaleDateString("en-US", {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  } catch {
    return iso;
  }
}
