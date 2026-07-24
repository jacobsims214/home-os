"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import Modal from "@/components/ui/Modal";
import Input from "@/components/ui/Input";
import Select from "@/components/ui/Select";
import Button from "@/components/ui/Button";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import type { PropertyDetailResponse } from "@/types/property";

interface AddPropertyModalProps {
  opened: boolean;
  onClose: () => void;
}

const PROPERTY_TYPES = [
  { value: "Single Family", label: "Single Family" },
  { value: "Condo", label: "Condo" },
  { value: "Townhouse", label: "Townhouse" },
  { value: "Multi-Family", label: "Multi-Family" },
  { value: "Land", label: "Land" },
  { value: "Commercial", label: "Commercial" },
  { value: "Other", label: "Other" },
];

export default function AddPropertyModal({
  opened,
  onClose,
}: AddPropertyModalProps) {
  const queryClient = useQueryClient();

  // ── Basic info ─────────────────────────────────────────────
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [propertyType, setPropertyType] = useState("");
  const [notes, setNotes] = useState("");

  // ── Financial details ──────────────────────────────────────
  const [purchasePrice, setPurchasePrice] = useState("");
  const [purchaseDate, setPurchaseDate] = useState("");
  const [currentValue, setCurrentValue] = useState("");
  const [downPayment, setDownPayment] = useState("");
  const [mortgageAmount, setMortgageAmount] = useState("");
  const [mortgageRate, setMortgageRate] = useState("");
  const [mortgageTermMonths, setMortgageTermMonths] = useState("");
  const [mortgageStartDate, setMortgageStartDate] = useState("");
  const [mortgageLender, setMortgageLender] = useState("");
  const [mortgageAccountNumber, setMortgageAccountNumber] = useState("");
  const [propertyTaxAnnual, setPropertyTaxAnnual] = useState("");
  const [propertyTaxDueMonths, setPropertyTaxDueMonths] = useState("");
  const [insuranceAnnual, setInsuranceAnnual] = useState("");
  const [insuranceProvider, setInsuranceProvider] = useState("");
  const [hoaFeeMonthly, setHoaFeeMonthly] = useState("");

  const [error, setError] = useState("");

  const createMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<PropertyDetailResponse>("/api/v1/properties", {
        method: "POST",
        body,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: propertyKeys.all });
      resetForm();
      onClose();
    },
    onError: (err: Error) => {
      setError(err.message);
    },
  });

  function resetForm() {
    setName("");
    setAddress("");
    setPropertyType("");
    setNotes("");
    setPurchasePrice("");
    setPurchaseDate("");
    setCurrentValue("");
    setDownPayment("");
    setMortgageAmount("");
    setMortgageRate("");
    setMortgageTermMonths("");
    setMortgageStartDate("");
    setMortgageLender("");
    setMortgageAccountNumber("");
    setPropertyTaxAnnual("");
    setPropertyTaxDueMonths("");
    setInsuranceAnnual("");
    setInsuranceProvider("");
    setHoaFeeMonthly("");
    setError("");
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setError("");

    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("Property name is required.");
      return;
    }

    // Build body — only include fields with truthy values so we don't
    // overwrite existing data with empty strings on partial edits.
    const body: Record<string, unknown> = { name: trimmedName };

    if (address.trim()) body.address = address.trim();
    if (propertyType) body.property_type = propertyType;
    if (notes.trim()) body.notes = notes.trim();

    if (purchasePrice) body.purchase_price = purchasePrice;
    if (purchaseDate) body.purchase_date = purchaseDate;
    if (currentValue) body.current_value = currentValue;
    if (downPayment) body.down_payment = downPayment;
    if (mortgageAmount) body.mortgage_amount = mortgageAmount;
    if (mortgageRate) body.mortgage_rate = mortgageRate;
    if (mortgageTermMonths) body.mortgage_term_months = mortgageTermMonths;
    if (mortgageStartDate) body.mortgage_start_date = mortgageStartDate;
    if (mortgageLender.trim()) body.mortgage_lender = mortgageLender.trim();
    if (mortgageAccountNumber.trim()) body.mortgage_account_number = mortgageAccountNumber.trim();
    if (propertyTaxAnnual) body.property_tax_annual = propertyTaxAnnual;
    if (propertyTaxDueMonths.trim()) body.property_tax_due_months = propertyTaxDueMonths.trim();
    if (insuranceAnnual) body.insurance_annual = insuranceAnnual;
    if (insuranceProvider.trim()) body.insurance_provider = insuranceProvider.trim();
    if (hoaFeeMonthly) body.hoa_fee_monthly = hoaFeeMonthly;

    createMutation.mutate(body);
  };

  const handleClose = () => {
    if (createMutation.isPending) return; // prevent closing while submitting
    resetForm();
    onClose();
  };

  return (
    <Modal opened={opened} onClose={handleClose} title="Add Property" size="lg">
      <form onSubmit={handleSubmit} className="space-y-4">
        {/* ── Section 1: Basic Info ─────────────────────────── */}
        <div className="space-y-4">
          <Input
            label="Property Name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Main House, Beach Cottage"
            required
          />

          <Input
            label="Address"
            value={address}
            onChange={(e) => setAddress(e.target.value)}
            placeholder="123 Main St, Springfield"
          />

          <Select
            label="Property Type"
            value={propertyType}
            onChange={(value) => setPropertyType(value ?? "")}
            data={PROPERTY_TYPES}
            placeholder="Select type (optional)"
          />

          <div>
            <label
              htmlFor="notes"
              className="block text-sm font-medium text-gray-900"
            >
              Notes
            </label>
            <textarea
              id="notes"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="Any additional notes about this property"
              rows={3}
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm text-gray-900 shadow-sm placeholder:text-gray-400 focus:outline-none focus:ring-2 focus:ring-inset focus:ring-indigo-600"
            />
          </div>
        </div>

        {/* ── Section 2: Financial Details (collapsible) ────── */}
        <details className="group rounded-md border border-gray-200 bg-gray-50">
          <summary className="flex cursor-pointer items-center justify-between px-4 py-3 text-sm font-medium text-gray-700 select-none">
            Financial Details
            <svg
              className="h-4 w-4 text-gray-400 transition-transform group-open:rotate-180"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth={2}
              stroke="currentColor"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d="M19.5 8.25l-7.5 7.5-7.5-7.5"
              />
            </svg>
          </summary>

          <div className="space-y-4 px-4 pb-4 pt-2">
            {/* Purchase */}
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Purchase Price"
                type="text"
                inputMode="decimal"
                value={purchasePrice}
                onChange={(e) => setPurchasePrice(e.target.value)}
                placeholder="$"
              />
              <Input
                label="Purchase Date"
                type="date"
                value={purchaseDate}
                onChange={(e) => setPurchaseDate(e.target.value)}
              />
            </div>

            {/* Current value & down payment */}
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Current Value"
                type="text"
                inputMode="decimal"
                value={currentValue}
                onChange={(e) => setCurrentValue(e.target.value)}
                placeholder="$"
              />
              <Input
                label="Down Payment"
                type="text"
                inputMode="decimal"
                value={downPayment}
                onChange={(e) => setDownPayment(e.target.value)}
                placeholder="$"
              />
            </div>

            {/* Mortgage */}
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Mortgage Amount"
                type="text"
                inputMode="decimal"
                value={mortgageAmount}
                onChange={(e) => setMortgageAmount(e.target.value)}
                placeholder="$"
              />
              <Input
                label="Mortgage Rate (%)"
                type="text"
                inputMode="decimal"
                value={mortgageRate}
                onChange={(e) => setMortgageRate(e.target.value)}
                placeholder="e.g. 6.5"
              />
            </div>

            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Mortgage Term (months)"
                type="text"
                inputMode="numeric"
                value={mortgageTermMonths}
                onChange={(e) => setMortgageTermMonths(e.target.value)}
                placeholder="e.g. 360"
              />
              <Input
                label="Mortgage Start Date"
                type="date"
                value={mortgageStartDate}
                onChange={(e) => setMortgageStartDate(e.target.value)}
              />
            </div>

            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Mortgage Lender"
                value={mortgageLender}
                onChange={(e) => setMortgageLender(e.target.value)}
                placeholder="e.g. Wells Fargo"
              />
              <Input
                label="Mortgage Account #"
                value={mortgageAccountNumber}
                onChange={(e) => setMortgageAccountNumber(e.target.value)}
              />
            </div>

            {/* Property tax */}
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Property Tax (annual)"
                type="text"
                inputMode="decimal"
                value={propertyTaxAnnual}
                onChange={(e) => setPropertyTaxAnnual(e.target.value)}
                placeholder="$"
              />
              <Input
                label="Property Tax Due Months"
                value={propertyTaxDueMonths}
                onChange={(e) => setPropertyTaxDueMonths(e.target.value)}
                placeholder="e.g. Jan, Jul"
              />
            </div>

            {/* Insurance */}
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Insurance (annual)"
                type="text"
                inputMode="decimal"
                value={insuranceAnnual}
                onChange={(e) => setInsuranceAnnual(e.target.value)}
                placeholder="$"
              />
              <Input
                label="Insurance Provider"
                value={insuranceProvider}
                onChange={(e) => setInsuranceProvider(e.target.value)}
                placeholder="e.g. State Farm"
              />
            </div>

            {/* HOA */}
            <Input
              label="HOA Fee (monthly)"
              type="text"
              inputMode="decimal"
              value={hoaFeeMonthly}
              onChange={(e) => setHoaFeeMonthly(e.target.value)}
              placeholder="$"
            />
          </div>
        </details>

        {error && (
          <p className="text-sm text-red-600" role="alert">
            {error}
          </p>
        )}

        <div className="flex justify-end gap-3 pt-2">
          <Button
            type="button"
            variant="primary"
            className="bg-gray-100 !text-gray-700 hover:bg-gray-200 focus-visible:outline-gray-400"
            onClick={handleClose}
            disabled={createMutation.isPending}
          >
            Cancel
          </Button>
          <Button type="submit" loading={createMutation.isPending}>
            Add Property
          </Button>
        </div>
      </form>
    </Modal>
  );
}
