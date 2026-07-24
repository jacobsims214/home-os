"use client";

import EntityDetail from "@/components/entity/EntityDetail";
export default function VendorDetailPage({ params }: { params: { id: string } }) {
  return <EntityDetail entityType="vendor" entityId={params.id} />;
}
