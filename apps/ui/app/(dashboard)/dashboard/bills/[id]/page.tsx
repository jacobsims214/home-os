import EntityDetail from "@/components/entity/EntityDetail";
import type { FieldConfig, SectionConfig } from "@/components/entity/EntityDetail";

const billFields: FieldConfig[] = [
  { name: "name", label: "Name", type: "text", required: true },
  { name: "amount", label: "Amount", type: "number" },
  { name: "due_day", label: "Due Day", type: "number" },
  { name: "category", label: "Category", type: "select", options: [
    { value: "Mortgage", label: "Mortgage" },
    { value: "Electric", label: "Electric" },
    { value: "Water", label: "Water" },
    { value: "Gas", label: "Gas" },
    { value: "Internet", label: "Internet" },
    { value: "Trash", label: "Trash" },
    { value: "Insurance", label: "Insurance" },
    { value: "HOA", label: "HOA" },
    { value: "Subscription", label: "Subscription" },
    { value: "Other", label: "Other" },
  ] },
  { name: "property_id", label: "Property", type: "select", options: [] },
  { name: "account_number", label: "Account Number", type: "text" },
  { name: "payment_url", label: "Payment URL", type: "text" },
  { name: "is_autopay", label: "Auto-pay", type: "boolean" },
  { name: "notes", label: "Notes", type: "textarea" },
];

const billSections: SectionConfig[] = [
  { title: "Basic Information", fields: ["name", "amount", "due_day", "category"] },
  { title: "Property & Payment", fields: ["property_id", "account_number", "payment_url", "is_autopay"] },
  { title: "Additional Details", fields: ["notes"] },
];

export default function BillDetailPage({ params }: { params: { id: string } }) {
  return <EntityDetail entityType="bill" entityId={params.id} fields={billFields} sections={billSections} />;
}
