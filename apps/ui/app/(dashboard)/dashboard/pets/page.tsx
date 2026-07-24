"use client";

import EntityList from "@/components/entity/EntityList";

export default function PetsPage() {
  return (
    <EntityList
      entityType="pet"
      title="Pets"
      description="Manage your pets"
      columns={[
        { name: "name", label: "Name" },
        { name: "species", label: "Species" },
        { name: "breed", label: "Breed" },
      ]}
    />
  );
}
