/**
 * TypeScript types matching the Go API response DTOs for the property module.
 *
 * All responses from the Go API are wrapped in { data: T }.
 * Nullable pointer fields from Go map to T | null in the JSON response.
 */

export interface PropertyResponse {
  id: string;
  household_id: string;
  name: string;
  address: string | null;
  property_type: string | null;
  notes: string | null;
  // Financial fields — all optional nullable strings matching Go *string serialization.
  purchase_price: string | null;
  purchase_date: string | null;
  current_value: string | null;
  down_payment: string | null;
  // Read-only, derived by the API from the property's linked loan and bills
  // (#4704, KB apis/property-vehicle-finance.md). The UI shows them and never
  // sends them (#4706); change the loan or bill instead.
  readonly mortgage_amount: string | null;
  readonly mortgage_rate: string | null;
  readonly mortgage_term_months: string | null;
  readonly mortgage_start_date: string | null;
  readonly mortgage_lender: string | null;
  readonly mortgage_account_number: string | null;
  readonly property_tax_annual: string | null;
  readonly property_tax_due_months: string | null;
  readonly insurance_annual: string | null;
  readonly insurance_provider: string | null;
  readonly hoa_fee_monthly: string | null;
  created_at: string;
  updated_at: string;
}

export interface RoomResponse {
  id: string;
  property_id: string;
  name: string;
  floor: number | null;
  notes: string | null;
  created_at: string;
}

/** API list wrapper — GET /api/v1/properties returns { data: PropertyResponse[] } */
export interface PropertyListResponse {
  data: PropertyResponse[];
}

/** API single wrapper — GET /api/v1/properties/:id returns { data: PropertyResponse } */
export interface PropertyDetailResponse {
  data: PropertyResponse;
}

/** API rooms list wrapper — GET /api/v1/properties/:id/rooms returns { data: RoomResponse[] } */
export interface RoomListResponse {
  data: RoomResponse[];
}
