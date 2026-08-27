"use client";

import { useState, useCallback } from "react";
import { useRouter, usePathname } from "next/navigation";
import { useAuthStore } from "@/stores/auth";
import { AppShell, NavLink, ScrollArea, TextInput, Group, Avatar, Text, Button, ActionIcon } from "@mantine/core";
import { IconSearch, IconHome, IconBuilding, IconTool, IconCalendar, IconCar, IconPaw, IconBuildingStore, IconCash, IconUsers, IconSettings, IconMoon, IconSun, IconBell, IconCreditCard, IconShield } from "@tabler/icons-react";
import { useLocalStorage } from "@mantine/hooks";
import { useMantineColorScheme } from "@mantine/core";
import NotificationBell from "@/components/notifications/NotificationBell";

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
  { name: "Loans", href: "/dashboard/loans", icon: <IconCreditCard size={20} /> },
  { name: "Members", href: "/dashboard/members", icon: <IconUsers size={20} /> },
  { name: "Settings", href: "/dashboard/settings", icon: <IconSettings size={20} /> },
  { name: "Admin", href: "/dashboard/admin", icon: <IconShield size={20} /> },
];

export default function Sidebar() {
  const pathname = usePathname();
  const router = useRouter();
  const user = useAuthStore((s) => s.user);
  const [searchQuery, setSearchQuery] = useState("");
  const { colorScheme, toggleColorScheme } = useMantineColorScheme();
  const [darkMode, setDarkMode] = useLocalStorage({
    key: "mantine-color-scheme",
    defaultValue: "light",
  });

  const handleSearch = useCallback(
    (e?: React.FormEvent) => {
      e?.preventDefault();
      const trimmed = searchQuery.trim();
      if (!trimmed) return;
      router.push("/dashboard/search?q=" + encodeURIComponent(trimmed));
    },
    [searchQuery, router],
  );

  const handleLogout = async () => {
    await useAuthStore.getState().logout();
    router.push("/login");
  };

  const isActive = (href: string) => {
    if (href === "/dashboard") return pathname === "/dashboard";
    return pathname.startsWith(href);
  };

  const getNavLinkProps = (href: string) => {
    const active = isActive(href);
    return {
      active: active,
      styles: {
        label: { color: active ? "#0891B2" : "#333" },
        icon: { color: active ? "#0891B2" : "#999" },
        body: {
          backgroundColor: active ? "#e0f7fa" : "transparent",
        },
      },
    };
  };

  return (
    <div>
      <AppShell.Section grow component={ScrollArea}>
        {/* Logo */}
        <div className="flex h-16 items-center border-b border-gray-200 px-0">
          <span className="text-lg font-bold text-cyan-600">Home OS</span>
        </div>

        {/* Search */}
        <div className="py-3">
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

        {/* Notification Bell */}
        <div className="py-2">
          <NotificationBell />
        </div>

        {/* Nav items */}
        <div className="space-y-1">
          {navItems.map((item) => (
            <NavLink
              key={item.name}
              href={item.href}
              label={item.name}
              leftSection={item.icon}
              onClick={() => router.push(item.href)}
              {...getNavLinkProps(item.href)}
            />
          ))}
        </div>
      </AppShell.Section>

      {/* User info and dark mode toggle */}
      <AppShell.Section>
        <div className="flex items-center justify-between px-4 py-3">
          <div className="flex items-center gap-3">
            <Avatar size="sm" radius="xl">
              {user?.name?.[0]?.toUpperCase() || "U"}
            </Avatar>
            <div className="min-w-0 flex-1">
              <Text size="sm" fw={500} truncate="end">
                {user?.name || "User"}
              </Text>
              <Text size="xs" c="dimmed" truncate="end">
                {user?.email}
              </Text>
            </div>
          </div>
          <Button
            variant="subtle"
            size="xs"
            onClick={handleLogout}
            color="red"
          >
            Logout
          </Button>
        </div>
        <div className="flex items-center justify-center px-4 py-2 border-t border-gray-200">
          <ActionIcon
            variant="outline"
            size="sm"
            onClick={() => {
              const newScheme = colorScheme === "dark" ? "light" : "dark";
              setDarkMode(newScheme);
              toggleColorScheme();
            }}
          >
            {colorScheme === "dark" ? <IconSun size={18} /> : <IconMoon size={18} />}
          </ActionIcon>
        </div>
      </AppShell.Section>
    </div>
  );
}
