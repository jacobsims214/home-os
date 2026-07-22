"use client";

import { useState, useCallback } from "react";
import { useRouter, usePathname } from "next/navigation";
import Link from "next/link";
import { useAuthStore } from "@/stores/auth";
import { TextInput } from "@mantine/core";
import { IconSearch, IconHome, IconBuilding, IconTool, IconCalendar, IconCar, IconPaw, IconBuildingStore, IconCash, IconUsers, IconSettings } from "@tabler/icons-react";

interface NavItem {
  name: string;
  href: string;
  icon: React.ReactNode;
}

const navItems: NavItem[] = [
  { name: "Home", href: "/dashboard", icon: <IconHome size={20} /> },
  { name: "Properties", href: "/dashboard/properties", icon: <IconBuilding size={20} /> },
  { name: "Assets", href: "/dashboard/assets", icon: <IconTool size={20} /> },
  { name: "Maintenance", href: "/dashboard/maintenance", icon: <IconTool size={20} /> },
  { name: "Calendar", href: "/dashboard/calendar", icon: <IconCalendar size={20} /> },
  { name: "Vehicles", href: "/dashboard/vehicles", icon: <IconCar size={20} /> },
  { name: "Pets", href: "/dashboard/pets", icon: <IconPaw size={20} /> },
  { name: "Vendors", href: "/dashboard/vendors", icon: <IconBuildingStore size={20} /> },
  { name: "Bills", href: "/dashboard/bills", icon: <IconCash size={20} /> },
  { name: "Members", href: "/dashboard/members", icon: <IconUsers size={20} /> },
  { name: "Settings", href: "/dashboard/settings", icon: <IconSettings size={20} /> },
];

export default function Sidebar() {
  const pathname = usePathname();
  const router = useRouter();
  const user = useAuthStore((s) => s.user);
  const [searchQuery, setSearchQuery] = useState("");

  const handleSearch = useCallback(
    (e?: React.FormEvent) => {
      e?.preventDefault();
      const trimmed = searchQuery.trim();
      if (!trimmed) return;
      router.push("/dashboard/search?q=" + encodeURIComponent(trimmed));
    },
    [searchQuery, router],
  );

  return (
    <aside className="flex h-full w-64 flex-col border-r border-gray-200 bg-white">
      {/* Logo */}
      <div className="flex h-16 items-center border-b border-gray-100 px-6">
        <span className="text-lg font-bold text-cyan-600">Home OS</span>
      </div>

      {/* Search */}
      <div className="px-4 py-3">
        <form onSubmit={handleSearch}>
          <TextInput
            placeholder="Search..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            leftSection={<IconSearch size={16} />}
            size="xs"
            radius="md"
          />
        </form>
      </div>

      {/* Nav items */}
      <nav className="flex-1 overflow-y-auto px-3 py-2">
        <ul className="space-y-1">
          {navItems.map((item) => {
            const isActive = pathname === item.href ||
              (item.href !== "/dashboard" && pathname.startsWith(item.href));
            return (
              <li key={item.name}>
                <Link
                  href={item.href}
                  className={`flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors ${
                    isActive
                      ? "bg-cyan-50 text-cyan-700"
                      : "text-gray-600 hover:bg-gray-50 hover:text-gray-900"
                  }`}
                >
                  <span className={isActive ? "text-cyan-600" : "text-gray-400"}>{item.icon}</span>
                  {item.name}
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>

      {/* User info */}
      <div className="border-t border-gray-100 px-4 py-3">
        <div className="flex items-center gap-3">
          <div className="flex h-8 w-8 items-center justify-center rounded-full bg-cyan-100 text-sm font-semibold text-cyan-700">
            {user?.name?.[0]?.toUpperCase() || "U"}
          </div>
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium text-gray-900">{user?.name || "User"}</p>
            <p className="truncate text-xs text-gray-400">{user?.email}</p>
          </div>
          <button
            onClick={async () => {
              await useAuthStore.getState().logout();
              router.push("/login");
            }}
            className="text-xs text-gray-400 hover:text-red-500"
          >
            Logout
          </button>
        </div>
      </div>
    </aside>
  );
}
