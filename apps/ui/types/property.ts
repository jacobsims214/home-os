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
