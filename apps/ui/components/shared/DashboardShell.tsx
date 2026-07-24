"use client";

import { AppShell } from "@mantine/core";
import Sidebar from "@/components/shared/Sidebar";


export default function DashboardShell({ children }: { children: React.ReactNode }) {
  return (
    <AppShell
      layout="alt"
      navbar={{ width: 260, breakpoint: "sm" }}

      styles={{
        main: {
          backgroundColor: "var(--mantine-color-gray-0)",
        },
      }}
    >
      <AppShell.Navbar p="md">
        <Sidebar />
      </AppShell.Navbar>

      <AppShell.Main>
        {children}
      </AppShell.Main>
    </AppShell>
  );
}
