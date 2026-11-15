import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import DashboardShell from "@/components/shared/DashboardShell";
import { REFRESH_COOKIE, SESSION_COOKIE } from "@/middleware";

export const metadata = {
  title: "Dashboard — Home OS",
};

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  // Auth guard, same rule as middleware.ts: either session cookie lets the
  // page render. With only the refresh cookie left (the id token cookie
  // expires with the token), the first /bff call renews the session silently
  // (#4423). If that refresh fails, lib/api.ts sends the browser to /login on
  // the 401 (#4620), so there is nothing more to do here.
  const cookieStore = cookies();
  const hasSession =
    !!cookieStore.get(SESSION_COOKIE)?.value || !!cookieStore.get(REFRESH_COOKIE)?.value;
  if (!hasSession) {
    redirect("/login");
  }

  return <DashboardShell>{children}</DashboardShell>;
}
