"use client";

/**
 * The notifications list itself (#4563), rendered inside the phone bottom sheet
 * and the desktop popover (NotificationsSurface). One component, so both
 * layouts show the same rows, states and actions.
 *
 * What each row shows, and why:
 * - the actor, when there is one (`item_changed`: "Amber", "Amber via moltis"),
 *   taken out of the title by notification-model.parseNotificationTitle;
 * - the action and what changed, in full — no line clamping. The war-game
 *   record is that this trail ("Amber deleted event …") is what prevents a
 *   double payment or an argument, so "Something changed" is not enough
 *   (KB architecture/notification-mark-read.md, #4947);
 * - the body when the writer set one (a bill's amount and due date, an alarm's
 *   start time, "Changed: amount, notes.");
 * - a relative time, with the exact time in its `title` attribute;
 * - the scope in words: "Family-wide" or "Only you". A family-wide row's single
 *   read_at belongs to the whole family, so its mark-read says so.
 *
 * Mark-read is per row on purpose: PATCH /api/v1/notifications/{id}/read names
 * the row, exactly like the MCP side after #4947. There is no "mark all read".
 */

import Link from "next/link";
import {
  IconAlertTriangle,
  IconArrowUpRight,
  IconBell,
  IconCalendarEvent,
  IconCheck,
  IconPencil,
  IconReceipt2,
  IconRefresh,
  IconTool,
} from "@tabler/icons-react";
import { VisuallyHidden } from "@mantine/core";
import {
  absoluteTime,
  aboutLabel,
  entityHref,
  groupNotifications,
  isSharedReadState,
  notificationKindLabel,
  notificationScope,
  parseNotificationTitle,
  relativeTime,
  scopeLabel,
  unreadCount,
  type NotificationItem,
} from "@/components/notifications/notification-model";
import {
  notificationsOf,
  useMarkNotificationRead,
  useNotifications,
} from "@/components/notifications/use-notifications";
import classes from "./Notifications.module.css";

export interface NotificationsPanelProps {
  /** Epoch ms to measure relative times against (tests, screenshots). */
  now?: number;
  /** Called after a row's link is followed, so the sheet can close. */
  onNavigate?: () => void;
  /**
   * Render the panel's own h2. The phone sheet doesn't: its Drawer header is
   * already the h2 that names the dialog, and two identical headings stack.
   */
  heading?: boolean;
}

export default function NotificationsPanel({
  now,
  onNavigate,
  heading = true,
}: NotificationsPanelProps) {
  const notificationsQ = useNotifications();
  const items = notificationsOf(notificationsQ.data);
  const unread = unreadCount(items);

  return (
    <div className={classes.panel}>
      {/* In the phone sheet the Drawer header already says "Notifications", so
          the panel shows only the count there. */}
      <div className={classes.head} data-compact={heading ? undefined : true}>
        {heading && (
          <h2 className={classes.title} id="notifications-panel-title">
            Notifications
          </h2>
        )}
        <span className={classes.summary} aria-live="polite">
          {notificationsQ.isPending
            ? null
            : notificationsQ.isError
              ? "Couldn't load"
              : unread > 0
                ? `${unread} unread`
                : "All caught up"}
        </span>
      </div>

      {notificationsQ.isPending ? (
        <LoadingRows />
      ) : notificationsQ.isError ? (
        <ErrorState
          onRetry={() => void notificationsQ.refetch()}
          retrying={notificationsQ.isFetching}
        />
      ) : items.length === 0 ? (
        <EmptyState />
      ) : (
        <NotificationList items={items} now={now} onNavigate={onNavigate} />
      )}
    </div>
  );
}

function NotificationList({
  items,
  now,
  onNavigate,
}: {
  items: NotificationItem[];
  now?: number;
  onNavigate?: () => void;
}) {
  const groups = groupNotifications(items);
  return (
    <div className={classes.list} role="region" aria-label="Notifications">
      {groups.map((group) => (
        <section key={group.key} aria-label={`${group.label} notifications`}>
          <h3 className={classes.groupHead}>{group.label}</h3>
          <ul className={classes.rows}>
            {group.items.map((item) => (
              <NotificationRow key={item.id} item={item} now={now} onNavigate={onNavigate} />
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

function NotificationRow({
  item,
  now,
  onNavigate,
}: {
  item: NotificationItem;
  now?: number;
  onNavigate?: () => void;
}) {
  const markRead = useMarkNotificationRead();
  const action = parseNotificationTitle(item);
  const scope = notificationScope(item);
  const scopeWords = scopeLabel(scope);
  const href = entityHref(item.entity_type, item.entity_id);
  const noun = aboutLabel(item.entity_type);
  const kind = notificationKindLabel(item);
  const unread = !item.read_at;
  const rel = relativeTime(item.created_at, now);
  const exact = absoluteTime(item.created_at);
  const shared = isSharedReadState(item);
  const pending = markRead.isPending && markRead.variables === item.id;

  return (
    <li className={classes.item} data-unread={unread || undefined}>
      <span className={classes.icon}>
        <KindIcon item={item} />
      </span>
      <div className={classes.grow}>
        {action ? (
          <>
            <p className={classes.subject}>
              {action.actor}
              {action.via && <span className={classes.via}> via {action.via}</span>}
            </p>
            <p className={classes.what}>
              <span className={classes.verb}>{action.verb}</span> {action.rest}
            </p>
          </>
        ) : (
          <p className={classes.subject}>{item.title}</p>
        )}

        {item.body && <p className={classes.detail}>{item.body}</p>}

        <p className={classes.meta}>
          {unread && <span className={classes.dot} aria-hidden />}
          {unread && <VisuallyHidden>Unread. </VisuallyHidden>}
          <time dateTime={item.created_at} title={exact}>
            {rel || exact || "Time unknown"}
          </time>
          <span className={classes.scope} data-scope={scope}>
            {scopeWords ?? kind}
          </span>
        </p>

        <div className={classes.actions}>
          {href && (
            <Link className={classes.action} href={href} onClick={onNavigate}>
              Open {noun ?? "what this is about"}
              <IconArrowUpRight size={16} stroke={1.75} aria-hidden />
            </Link>
          )}
          {unread && (
            <button
              type="button"
              className={classes.action}
              aria-busy={pending || undefined}
              disabled={pending}
              onClick={() => markRead.mutate(item.id)}
            >
              <IconCheck size={16} stroke={2} aria-hidden />
              {pending ? "Marking…" : "Mark read"}
              {shared && <VisuallyHidden>, for the whole family too</VisuallyHidden>}
            </button>
          )}
        </div>

        {shared && unread && (
          <p className={classes.hint}>
            Everyone in the family sees this one; marking it read marks it read for all of
            them.
          </p>
        )}

        {markRead.isError && markRead.variables === item.id && (
          <p className={classes.rowError} role="status">
            Couldn&apos;t mark that read. Check your connection and try again.
          </p>
        )}
      </div>
    </li>
  );
}

/** The icon for a row's type, matching the Today mockup's bill/upkeep icons. */
function KindIcon({ item }: { item: NotificationItem }) {
  const p = { size: 18, stroke: 1.75, "aria-hidden": true } as const;
  switch (item.type) {
    case "bill_due":
      return <IconReceipt2 {...p} />;
    case "maintenance_due":
      return <IconTool {...p} />;
    case "calendar_alarm":
      return <IconCalendarEvent {...p} />;
    case "item_changed":
      return <IconPencil {...p} />;
    default:
      return <IconBell {...p} />;
  }
}

function LoadingRows() {
  return (
    <div aria-busy="true" aria-label="Loading notifications" data-testid="notifications-loading">
      {[0, 1, 2].map((i) => (
        <div key={i} className={classes.item}>
          <span className={classes.skeleton} style={{ width: 36, height: 36, flex: "none" }} />
          <div className={classes.grow}>
            <div className={classes.skeleton} style={{ width: "55%", height: 14 }} />
            <div className={classes.skeleton} style={{ width: "85%", height: 12, marginTop: 8 }} />
            <div className={classes.skeleton} style={{ width: "35%", height: 11, marginTop: 8 }} />
          </div>
        </div>
      ))}
    </div>
  );
}

/**
 * Says what will appear here instead of leaving a blank panel (#4563): a new
 * family has nothing yet, and a member with nothing unread needs to know the
 * panel is working.
 */
function EmptyState() {
  return (
    <div className={classes.empty} data-testid="notifications-empty">
      <span className={classes.emptyIcon}>
        <IconBell size={22} stroke={1.75} aria-hidden />
      </span>
      <p className={classes.emptyTitle}>Nothing needs you right now</p>
      <p className={classes.emptyText}>
        This is where the family&apos;s notices land. They appear here when:
      </p>
      <ul className={classes.emptyList}>
        <li>a bill is due soon — everyone in the family sees that one,</li>
        <li>home upkeep comes due,</li>
        <li>a calendar reminder fires for you,</li>
        <li>
          someone changes or deletes something you added — the notice names who did it.
        </li>
      </ul>
      <p className={classes.emptyText}>
        A notice stays until you mark it read, and marking one read only ever affects that
        one.
      </p>
    </div>
  );
}

function ErrorState({ onRetry, retrying }: { onRetry: () => void; retrying: boolean }) {
  return (
    <div className={classes.error} role="alert" data-testid="notifications-error">
      <span className={classes.errorIcon}>
        <IconAlertTriangle size={22} stroke={1.75} aria-hidden />
      </span>
      <div>
        <p className={classes.errorTitle}>Notifications didn&apos;t load</p>
        <p className={classes.errorText}>
          We couldn&apos;t reach them just now. Anything unread is still there — this panel
          just couldn&apos;t read it.
        </p>
        <button type="button" className={classes.action} onClick={onRetry} disabled={retrying}>
          <IconRefresh size={16} stroke={1.75} aria-hidden />
          {retrying ? "Trying…" : "Try again"}
        </button>
      </div>
    </div>
  );
}
