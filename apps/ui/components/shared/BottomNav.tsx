"use client";

import { usePathname, useRouter } from "next/navigation";
import { Tabs } from "@mantine/core";
import { IconHome, IconBuilding, IconTool, IconCalendar, IconPaw, IconCar, IconCash, IconUsers, IconSettings } from "@tabler/icons-react";

interface MobileNavItem {
  name: string;
  href: string;
  icon: React.ReactNode;
}

const mobileNavItems: MobileNavItem[] = [
  {
    name: "Home",
    href: "/dashboard",
    icon: <IconHome size={24} />,
  },
  {
    name: "Properties",
    href: "/dashboard/properties",
    icon: <IconBuilding size={24} />,
  },
  {
    name: "Maintenance",
    href: "/dashboard/maintenance",
    icon: <IconTool size={24} />,
  },
  {
    name: "Calendar",
    href: "/dashboard/calendar",
    icon: <IconCalendar size={24} />,
  },
  {
    name: "Assets",
    href: "/dashboard/assets",
    icon: <IconTool size={24} />,
  },
];

const otherNavItems: MobileNavItem[] = [
  {
    name: "Vehicles",
    href: "/dashboard/vehicles",
    icon: <IconCar size={24} />,
  },
  {
    name: "Pets",
    href: "/dashboard/pets",
    icon: <IconPaw size={24} />,
  },
  {
    name: "Bills",
    href: "/dashboard/bills",
    icon: <IconCash size={24} />,
  },
  {
    name: "Members",
    href: "/dashboard/members",
    icon: <IconUsers size={24} />,
  },
  {
    name: "Settings",
    href: "/dashboard/settings",
    icon: <IconSettings size={24} />,
  },
];

export default function BottomNav() {
  const pathname = usePathname();
  const router = useRouter();

  const activeTab = mobileNavItems.find((item) => {
    if (item.href === "/dashboard") return pathname === "/dashboard";
    return pathname.startsWith(item.href);
  })?.name;

  return (
    <div>
      <div className="mx-auto max-w-lg">
        <Tabs defaultValue={activeTab || "Home"}>
          <Tabs.List grow>
            {mobileNavItems.map((item) => {
              const isActive = () => {
                if (item.href === "/dashboard") return pathname === "/dashboard";
                return pathname.startsWith(item.href);
              };
              return (
                <Tabs.Tab
                  key={item.name}
                  value={item.name}
                  leftSection={isActive() ? <span className="text-indigo-600">{item.icon}</span> : item.icon}
                   styles={{
                     tab: {
                       backgroundColor: isActive() ? "#eef2ff" : "transparent",
                     },
                     tabLabel: {
                       color: isActive() ? "#4f46e5" : "#6b7280",
                       fontSize: "0.75rem",
                       fontWeight: 500,
                     },
                   }}
                  onClick={() => router.push(item.href)}
                >
                  {item.name}
                </Tabs.Tab>
              );
            })}
          </Tabs.List>
        </Tabs>
      </div>
    </div>
  );
}
