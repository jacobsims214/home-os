"use client";

import EntityList from "@/components/entity/EntityList";
export default function VendorsPage() {
  return <EntityList entityType="vendor" title="Vendors" description="Service providers" columns={[{name:"name",label:"Name"},{name:"specialty",label:"Specialty"},{name:"phone",label:"Phone"}]} cardView />;
}
