/**
 * Maintenance task response from the API.
 * Matches the taskResponse JSON shape from the Go handler.
 */
export interface MaintenanceTask {
  id: string;
  household_id: string;
  schedule_id: string | null;
  property_id: string | null;
  asset_id: string | null;
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
