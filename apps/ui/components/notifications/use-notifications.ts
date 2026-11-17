"use client";

/**
 * The one notifications query (#4563). Every surface that shows a count or a
 * row shares it, so the header badge, the More sheet's pill and the panel can
 * never disagree: the bell, the More row (BottomNav) and the panel all read
 * ["notifications"].
 *
 * Scoping is the API's, not the UI's: GET /api/v1/notifications is
 * `ListUnread` (apps/api/internal/notification/repo.go) and returns only the
 * caller's own personal rows plus family-wide ones, filtered by their current
 * role (visibility.go). Nothing here adds, merges or totals another member's
 * state — there is no endpoint that would return it.
 *
 * Freshness: real-time is out of scope (#4563), so this refetches on window
 * focus and whenever the panel is opened (NotificationsPanel), and not on a
 * timer.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import type { NotificationItem, NotificationsResponse } from "@/components/notifications/notification-model";

/** Shared cache key; keep it in sync with the places that read it. */
export const NOTIFICATIONS_QUERY_KEY = ["notifications"] as const;

export function useNotifications() {
  return useQuery({
    queryKey: NOTIFICATIONS_QUERY_KEY,
    queryFn: () => apiFetch<NotificationsResponse>("/api/v1/notifications"),
    // Short, so a focus refetch actually fetches.
    staleTime: 15_000,
    refetchOnWindowFocus: true,
    // A failed fetch must reach the panel as an error, not as "no
    // notifications" (audit #74: an error rendered as empty data).
    retry: 1,
  });
}

/**
 * The caller's unread count: the bell's badge and the More sheet's pill both
 * read this, so they always show the same number. It counts the rows this
 * member may see — their own plus family-wide ones, per the API's
 * `visibleSQL` — never anyone else's.
 */
export function useUnreadCount(): number {
  const q = useNotifications();
  return notificationsOf(q.data).filter((n) => !n.read_at).length;
}

/** The rows of a response, guarded against a non-array payload. */
export function notificationsOf(data: NotificationsResponse | undefined): NotificationItem[] {
  return Array.isArray(data?.data) ? data!.data : [];
}

/**
 * Marks ONE notification read: PATCH /api/v1/notifications/{id}/read.
 *
 * There is deliberately no bulk call. The MCP side lost `all` in #4947 after
 * a war-game agent marked the household's whole unread state read (including
 * the other adult's change notice), and the REST API never had one: MarkRead
 * takes the id in the path and its repo scopes the UPDATE to what the caller
 * may see. The panel's per-row button is the same shape, so it cannot
 * reintroduce that hole.
 *
 * On success the row keeps its place in the cache with read_at set, so the user
 * sees it move to "Earlier" and the count drop; the next refetch (focus, or
 * reopening the panel) drops it, because the server only lists unread rows.
 */
export function useMarkNotificationRead() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<void>(`/api/v1/notifications/${encodeURIComponent(id)}/read`, { method: "PATCH" }),
    onSuccess: (_result, id) => {
      queryClient.setQueryData<NotificationsResponse>(NOTIFICATIONS_QUERY_KEY, (old) => {
        if (!old || !Array.isArray(old.data)) return old;
        return {
          ...old,
          data: old.data.map((n) =>
            n.id === id ? { ...n, read_at: new Date().toISOString() } : n,
          ),
        };
      });
    },
    // A 404 means it is already read or no longer visible to this member (the
    // API answers the same for another family's, another member's, or a row
    // their role can't see). Refetching settles the list either way.
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: NOTIFICATIONS_QUERY_KEY });
    },
  });
}
