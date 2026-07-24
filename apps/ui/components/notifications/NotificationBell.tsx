"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { IconBell } from "@tabler/icons-react";
import { Popover, Badge, Text, Stack, Button, Group, Avatar, Divider } from "@mantine/core";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";

dayjs.extend(relativeTime);

// ─── Types ────────────────────────────────────────────────────

interface Notification {
  id: string;
  title: string;
  body: string;
  entity_type: string;
  entity_id: string;
  is_read: boolean;
  created_at: string;
}

interface NotificationsResponse {
  data: Notification[];
}

// ─── Helper functions ────────────────────────────────────────

function getEntityRoute(entityType: string, entityId: string): string {
  const routeMap: Record<string, string> = {
    property: `/dashboard/properties/${entityId}`,
    asset: `/dashboard/assets/${entityId}`,
    maintenance: `/dashboard/maintenance/${entityId}`,
    calendar: `/dashboard/calendar`,
    vehicle: `/dashboard/vehicles/${entityId}`,
    pet: `/dashboard/pets/${entityId}`,
    vendor: `/dashboard/vendors/${entityId}`,
    bill: `/dashboard/bills/${entityId}`,
    member: `/dashboard/members/${entityId}`,
  };
  return routeMap[entityType] || `/dashboard`;
}

function getEntityLabel(entityType: string): string {
  const labelMap: Record<string, string> = {
    property: "Property",
    asset: "Asset",
    maintenance: "Maintenance",
    calendar: "Calendar",
    vehicle: "Vehicle",
    pet: "Pet",
    vendor: "Vendor",
    bill: "Bill",
    member: "Member",
  };
  return labelMap[entityType] || entityType.charAt(0).toUpperCase() + entityType.slice(1);
}

// ─── Component ────────────────────────────────────────────────

export default function NotificationBell() {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [opened, setOpened] = useState(false);

  const notificationsQ = useQuery({
    queryKey: ["notifications"],
    queryFn: () => apiFetch<NotificationsResponse>("/api/v1/notifications"),
  });

  const markAsReadMutation = useMutation({
    mutationFn: (id: string) => apiFetch(`/api/v1/notifications/${id}/read`, { method: "PATCH" }),
    onSuccess: (_, id) => {
      queryClient.setQueryData(["notifications"], (old: NotificationsResponse | undefined) => {
        if (!old) return old;
        return {
          ...old,
          data: old.data.map((n) =>
            n.id === id ? { ...n, is_read: true } : n
          ),
        };
      });
    },
  });

  const notifications = notificationsQ.data?.data ?? [];
  const unreadCount = notifications.filter((n) => !n.is_read).length;

  const handleNotificationClick = (notification: Notification) => {
    if (!notification.is_read) {
      markAsReadMutation.mutate(notification.id);
    }
    const route = getEntityRoute(notification.entity_type, notification.id);
    router.push(route);
    setOpened(false);
  };

  const fmtTime = (iso: string) => {
    try {
      return dayjs(iso).fromNow();
    } catch {
      return iso;
    }
  };

  if (notificationsQ.isLoading) {
    return (
      <Badge color="gray" size="md">
        <IconBell size={20} />
      </Badge>
    );
  }

  return (
    <Popover
      opened={opened}
      onClose={() => setOpened(false)}
      position="bottom"
      withArrow
      width={320}
    >
      <Popover.Target>
        <Badge
          color="red"
          size="md"
          variant={unreadCount > 0 ? "filled" : "light"}
          style={{ cursor: "pointer" }}
          onClick={() => setOpened(true)}
        >
          <IconBell size={20} />
          {unreadCount > 0 && (
            <span style={{ marginLeft: 4 }}>{unreadCount}</span>
          )}
        </Badge>
      </Popover.Target>

      <Popover.Dropdown>
        <Stack gap="xs">
          <Text fw={500} size="sm">
            Notifications
          </Text>
          <Divider />

          {notifications.length === 0 ? (
            <Text size="sm" c="dimmed" ta="center" py="md">
              No notifications
            </Text>
          ) : (
            <Stack gap="xs">
              {notifications.map((notification) => (
                <Group
                  key={notification.id}
                  onClick={() => handleNotificationClick(notification)}
                  style={{ cursor: "pointer" }}
                  p="xs"
                >
                  <Avatar
                    size="sm"
                    radius="xl"
                    color={notification.is_read ? "gray" : "cyan"}
                  >
                    {notification.title.charAt(0).toUpperCase()}
                  </Avatar>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <Text size="sm" fw={500} truncate>
                      {notification.title}
                    </Text>
                    <Text size="xs" c="dimmed" truncate>
                      {notification.body}
                    </Text>
                    <Text size="xs" c="gray" mt={2}>
                      {fmtTime(notification.created_at)} • {getEntityLabel(notification.entity_type)}
                    </Text>
                  </div>
                  {!notification.is_read && (
                    <div
                      style={{
                        width: 8,
                        height: 8,
                        borderRadius: "50%",
                        backgroundColor: "var(--mantine-color-cyan-6)",
                      }}
                    />
                  )}
                </Group>
              ))}
            </Stack>
          )}

          {notifications.length > 0 && (
            <>
              <Divider />
              <Button
                variant="subtle"
                size="xs"
                fullWidth
                onClick={() => setOpened(false)}
              >
                Close
              </Button>
            </>
          )}
        </Stack>
      </Popover.Dropdown>
    </Popover>
  );
}
