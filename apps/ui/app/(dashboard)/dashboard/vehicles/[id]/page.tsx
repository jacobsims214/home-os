"use client";

import EntityDetail, { type FieldConfig } from "@/components/entity/EntityDetail";
import LinkedFinances from "@/components/finance/LinkedFinances";

// The vehicle's loan, registration and insurance are a loan and bills with
// vehicle_id (#4704). The API still returns lender/loan_amount/... derived
// from them, but this form never sends those fields (#4706, #4707 drops the
// columns); they are shown and edited as the linked loan and bills.
const vehicleName = (v: Record<string, unknown>) =>
  [v.year, v.make, v.model].filter((x) => x != null && x !== "").join(" ") || "this vehicle";

export default function VehicleDetailPage({ params }: { params: { id: string } }) {
  const fields: FieldConfig[] = [
    { name: "year", label: "Year", type: "number" },
    { name: "make", label: "Make", type: "text" },
    { name: "model", label: "Model", type: "text" },
    { name: "vin", label: "VIN", type: "text" },
    { name: "license_plate", label: "License Plate", type: "text" },
    { name: "color", label: "Color", type: "text" },
    { name: "notes", label: "Notes", type: "textarea" },
    { name: "purchase_price", label: "Purchase Price", type: "text" },
    { name: "current_value", label: "Current Value", type: "text" },
    { name: "purchase_date", label: "Purchase Date", type: "date" },
    {
      name: "linked_finances",
      label: "Loans & bills",
      type: "custom",
      readOnly: true,
      renderView: (vehicle) => (
        <LinkedFinances subject={{ kind: "vehicle", id: params.id, name: vehicleName(vehicle) }} />
      ),
    },
  ];

  const sections = [
    { title: "Details", fields: ["year", "make", "model", "vin", "license_plate", "color", "notes"] },
    { title: "Purchase", fields: ["purchase_price", "purchase_date", "current_value"] },
    { title: "Loans & bills", fields: ["linked_finances"] },
  ];

  return (
    <EntityDetail
      entityType="vehicle"
      entityId={params.id}
      fields={fields}
      sections={sections}
    />
  );
}
