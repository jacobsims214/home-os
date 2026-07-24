"use client";

import EntityDetail from "@/components/entity/EntityDetail";

export default function PetDetailPage({ params }: { params: { id: string } }) {
  const fields: { name: string; label: string; type: "text" | "date" | "textarea"; required?: boolean }[] = [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "species", label: "Species", type: "text" },
    { name: "breed", label: "Breed", type: "text" },
    { name: "date_of_birth", label: "Date of Birth", type: "date" },
    { name: "vet_name", label: "Vet Name", type: "text" },
    { name: "vet_phone", label: "Vet Phone", type: "text" },
    { name: "notes", label: "Notes", type: "textarea" },
    { name: "microchip_id", label: "Microchip ID", type: "text" },
    { name: "insurance_provider", label: "Insurance Provider", type: "text" },
    { name: "insurance_policy", label: "Insurance Policy", type: "text" },
    { name: "registration_id", label: "Registration ID", type: "text" },
  ];

  const sections = [
    { title: "Details", fields: ["name", "species", "breed", "date_of_birth", "vet_name", "vet_phone", "notes"] },
    { title: "Identification", fields: ["microchip_id", "insurance_provider", "insurance_policy", "registration_id"] },
  ];

  return (
    <EntityDetail
      entityType="pet"
      entityId={params.id}
      fields={fields}
      sections={sections}
    />
  );
}
