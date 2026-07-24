"use client";

import EntityList from "@/components/entity/EntityList";

export default function AssetsPage() {
  return (
    <EntityList
      entityType="asset"
      title="Assets"
      description="Track appliances, HVAC systems, electronics, and more"
      columns={[
        { name: "name", label: "Name" },
        { name: "category", label: "Category" },
        { name: "manufacturer", label: "Manufacturer" },
      ]}
      propertyFilter={true}
    />
  );
}
