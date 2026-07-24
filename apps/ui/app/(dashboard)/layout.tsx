import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import DashboardShell from "@/components/shared/DashboardShell";

export const metadata = {
  title: "Dashboard — Home OS",
};

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  // Auth guard — redirect to /login if home-os-token cookie is missing
  const cookieStore = cookies();
  const token = cookieStore.get("home-os-token")?.value;
  if (!token) {
    redirect("/login");
  }

  return <DashboardShell>{children}</DashboardShell>;
}
