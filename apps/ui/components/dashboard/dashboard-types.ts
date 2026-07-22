// ── Dashboard API types ───────────────────────────────────────

export interface HouseholdRaw {
  ID: string; Name: string; Timezone: string;
  DefaultPropertyID: string | null; CreatedAt: string; UpdatedAt: string;
}

export interface CalendarEvent {
  id: string; title: string; start: string; end: string;
  event_type: string; color: string; description: string; location: string; all_day: boolean;
}

export interface Bill {
  id: string; property_id: string | null; name: string; amount: string | null;
  due_day: number | null; category: string | null; vendor_id: string | null;
  rrule: string | null; notes: string | null; created_at: string; updated_at: string;
}

// ── Helpers ────────────────────────────────────────────────────

export function cents$(c: string | null | undefined): number {
  if (!c) return 0;
  const n = parseInt(c, 10);
  return isNaN(n) ? 0 : n / 100;
}

export function weekStart(d: Date): Date {
  const date = new Date(d);
  const day = date.getDay();
  date.setDate(date.getDate() - day + (day === 0 ? -6 : 1));
  date.setHours(0, 0, 0, 0); return date;
}

export function weekEnd(d: Date): Date {
  const date = weekStart(d);
  date.setDate(date.getDate() + 6);
  date.setHours(23, 59, 59, 999); return date;
}

export function fmtTime(s: string) {
  return new Date(s).toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit", hour12: true });
}

export function fmtDate(s: string) {
  return new Date(s).toLocaleDateString("en-US", { weekday: "short", month: "short", day: "numeric" });
}

export function overdue(d: string | null) {
  return d ? new Date(d) < new Date() : false;
}

export const DARK_PURPLE = "text-[#4C1D95]";
export const CARD = "rounded-xl bg-white/80 backdrop-blur-sm border border-gray-200/50 shadow-sm";