import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import Sidebar from "@/components/shared/Sidebar";
import BottomNav from "@/components/shared/BottomNav";

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

  return (
    <div className="flex h-screen overflow-hidden bg-gray-50">
      {/* Desktop sidebar — hidden below lg breakpoint */}
      <div className="hidden lg:flex lg:w-64 lg:flex-shrink-0">
        <Sidebar />
      </div>

      {/* Main content area */}
      <div className="flex flex-1 flex-col overflow-hidden">
        <main className="flex-1 overflow-y-auto pb-20 lg:pb-0">
          {children}
        </main>
      </div>

      {/* Mobile bottom nav — visible below lg breakpoint */}
      <div className="lg:hidden">
        <BottomNav />
      </div>
    </div>
  );
}
