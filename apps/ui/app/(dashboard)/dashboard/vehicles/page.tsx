"use client";

import EntityList from "@/components/entity/EntityList";

export default function VehiclesPage() {
  return (
    <EntityList
      entityType="vehicle"
      title="Vehicles"
      description="Manage your vehicles"
      columns={[
        { name: "year", label: "Year" },
        { name: "make", label: "Make" },
        { name: "model", label: "Model" },
      ]}
    />
  );
}
