"use client";

import EntityDetail, { FieldConfig } from "@/components/entity/EntityDetail";

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
    { name: "lender", label: "Lender", type: "text" },
    { name: "loan_amount", label: "Loan Amount", type: "text" },
    { name: "loan_term_months", label: "Loan Term (months)", type: "number" },
    { name: "monthly_payment", label: "Monthly Payment", type: "text" },
    { name: "registration_renewal_month", label: "Registration Renewal Month", type: "number" },
    { name: "registration_cost", label: "Registration Cost", type: "text" },
    { name: "insurance_provider", label: "Insurance Provider", type: "text" },
    { name: "insurance_cost", label: "Insurance Cost", type: "text" },
  ];

  const sections = [
    { title: "Details", fields: ["year", "make", "model", "vin", "license_plate", "color", "notes"] },
    { title: "Financial", fields: ["purchase_price", "purchase_date", "current_value", "lender", "loan_amount", "loan_term_months", "monthly_payment", "registration_cost", "registration_renewal_month", "insurance_cost", "insurance_provider"] },
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
