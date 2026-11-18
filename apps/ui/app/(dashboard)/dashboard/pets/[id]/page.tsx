"use client";

import EntityDetail, { type FieldConfig } from "@/components/entity/EntityDetail";
import VetPicker from "@/components/pet/VetPicker";

// The vet is a family contact tagged "vet" (pets.vet_vendor_id, #4703). The
// API returns vet_name / vet_phone read-only (the contact's name and phone);
// the form only ever sends vet_vendor_id.
const str = (v: unknown) => (typeof v === "string" && v ? v : null);

const fields: FieldConfig[] = [
  { name: "name", label: "Name", type: "text", required: true },
  { name: "species", label: "Species", type: "text" },
  { name: "breed", label: "Breed", type: "text" },
  { name: "date_of_birth", label: "Date of Birth", type: "date" },
  {
    name: "vet_vendor_id",
    label: "Vet",
    type: "custom",
    renderView: (pet) => {
      const name = str(pet.vet_name);
      const phone = str(pet.vet_phone);
      if (!pet.vet_vendor_id || !name) return "—";
      return (
        <>
          <span className="block">{name}</span>
          {phone && (
            <a href={`tel:${phone.replace(/[^\d+]/g, "")}`} className="text-accent hover:underline">
              {phone}
            </a>
          )}
        </>
      );
    },
    renderEdit: ({ value, onChange, entity }) => (
      <VetPicker
        value={str(value)}
        onChange={onChange}
        current={{ name: str(entity.vet_name), phone: str(entity.vet_phone) }}
      />
    ),
  },
  { name: "notes", label: "Notes", type: "textarea" },
  { name: "microchip_id", label: "Microchip ID", type: "text" },
  { name: "insurance_provider", label: "Insurance Provider", type: "text" },
  { name: "insurance_policy", label: "Insurance Policy", type: "text" },
  { name: "registration_id", label: "Registration ID", type: "text" },
];

const sections = [
  { title: "Details", fields: ["name", "species", "breed", "date_of_birth", "vet_vendor_id", "notes"] },
  { title: "Identification", fields: ["microchip_id", "insurance_provider", "insurance_policy", "registration_id"] },
];

export default function PetDetailPage({ params }: { params: { id: string } }) {
  return <EntityDetail entityType="pet" entityId={params.id} fields={fields} sections={sections} />;
}
