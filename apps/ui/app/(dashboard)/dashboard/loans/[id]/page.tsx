"use client";

import { useParams } from "next/navigation";
import EntityDetail from "@/components/entity/EntityDetail";
import type { FieldConfig, SectionConfig } from "@/components/entity/EntityDetail";

const loanFields: FieldConfig[] = [
  { name: "name", label: "Name", type: "text", required: true },
  { name: "lender", label: "Lender", type: "text" },
  { name: "original_amount", label: "Original Amount", type: "number" },
  { name: "remaining_balance", label: "Remaining Balance", type: "number" },
  { name: "interest_rate", label: "Interest Rate (%)", type: "number" },
  { name: "term_months", label: "Term (months)", type: "number" },
  { name: "monthly_payment", label: "Monthly Payment", type: "number" },
  { name: "start_date", label: "Start Date", type: "date" },
  { name: "entity_type", label: "Entity Type", type: "select", options: [
    { value: "", label: "None" },
    { value: "property", label: "Property" },
    { value: "vehicle", label: "Vehicle" },
    { value: "asset", label: "Asset" },
  ] },
  { name: "entity_id", label: "Entity ID", type: "text" },
  { name: "notes", label: "Notes", type: "textarea" },
];

const loanSections: SectionConfig[] = [
  { title: "Loan Information", fields: ["name", "lender", "original_amount", "remaining_balance"] },
  { title: "Terms", fields: ["interest_rate", "term_months", "monthly_payment", "start_date"] },
  { title: "Association", fields: ["entity_type", "entity_id"] },
  { title: "Notes", fields: ["notes"] },
];

export default function LoanDetailPage() {
  const params = useParams<{ id: string }>();
  const id = params.id;

  return (
    <EntityDetail
      entityType="loan"
      entityId={id}
      fields={loanFields}
      sections={loanSections}
    />
  );
}