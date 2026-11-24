/**
 * Maintenance task response from the API.
 * Matches the taskResponse JSON shape from the Go handler
 * (apps/api/internal/maintenance/handler.go). `vehicle_id` was missing here
 * until #4558: the handler has always returned it, and the upkeep list needs it
 * to name a vehicle the way the mockup's subject line does ("2019 Honda
 * Odyssey EX-L").
 */
export interface MaintenanceTask {
  id: string;
  household_id: string;
  schedule_id: string | null;
  property_id: string | null;
  asset_id: string | null;
  vehicle_id: string | null;
  name: string;
  description: string | null;
  status: "pending" | "in_progress" | "done" | "skipped";
  due_date: string | null;
  completed_at: string | null;
  cost: string | null;
  vendor_id: string | null;
  notes: string | null;
  created_at: string;
  updated_at: string;
}
