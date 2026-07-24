"use client";
import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { Loader } from "@mantine/core";
import { apiFetch } from "@/lib/api";

export default function DashboardPage() {
  const router = useRouter();
  useEffect(() => {
    apiFetch("/api/v1/households/me").then((res: any) => {
      const defaultPropertyId = res.data?.default_property_id;
      if (defaultPropertyId) {
        router.push(`/dashboard/properties/${defaultPropertyId}`);
      } else {
        router.push("/dashboard/properties");
      }
    });
  }, [router]);
  return <div className="flex items-center justify-center h-64"><Loader size="lg" /></div>;
}
