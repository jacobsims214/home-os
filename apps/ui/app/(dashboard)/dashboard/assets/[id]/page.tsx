"use client";

import { useParams } from "next/navigation";
import EntityDetail from "@/components/entity/EntityDetail";

export default function AssetDetailPage() {
  const params = useParams<{ id: string }>();
  const id = params.id;

  const fields = [
    { name: "name", label: "Name", type: "text" as const, required: true },
    { name: "property_id", label: "Property", type: "select" as const, options: [] },
    { name: "room_id", label: "Room ID", type: "text" as const },
    { name: "category", label: "Category", type: "select" as const, options: [] },
    { name: "manufacturer", label: "Manufacturer", type: "text" as const },
    { name: "model", label: "Model", type: "text" as const },
    { name: "serial_number", label: "Serial Number", type: "text" as const },
    { name: "purchase_price", label: "Purchase Price", type: "text" as const },
    { name: "purchase_date", label: "Purchase Date", type: "date" as const },
    { name: "warranty_expiry", label: "Warranty Expiry", type: "date" as const },
    { name: "notes", label: "Notes", type: "textarea" as const },
  ];

  const sections = [
    {
      title: "Product Info",
      fields: ["name", "category", "manufacturer", "model", "serial_number"],
    },
    {
      title: "Purchase & Warranty",
      fields: ["purchase_price", "purchase_date", "warranty_expiry"],
    },
    {
      title: "Location",
      fields: ["property_id", "room_id"],
    },
    {
      title: "Details",
      fields: ["notes"],
    },
  ];

  return <EntityDetail entityType="asset" entityId={id} fields={fields} sections={sections} />;
}
