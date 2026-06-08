/**
 * TypeScript types for Home OS API responses.
 *
 * These match the JSON shapes returned by the Go core API.
 * All UUIDs and timestamps are strings in the JSON layer.
 */

/** GET /api/v1/properties response item */
export interface Property {
  id: string;
  household_id: string;
  name: string;
  address?: string;
  property_type?: string;
  notes?: string;
  created_at: string;
  updated_at: string;
}

/** GET/POST /api/v1/assets response item */
export interface Asset {
  id: string;
  household_id: string;
  property_id?: string;
  room_id?: string;
  name: string;
  category?: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  purchase_date?: string;
  purchase_price?: string;
  warranty_expiry?: string;
  notes?: string;
  created_at: string;
  updated_at: string;
}

/** POST /api/v1/assets request body */
export interface CreateAssetRequest {
  name: string;
  property_id?: string;
  room_id?: string;
  category?: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  purchase_date?: string;
  purchase_price?: string;
  warranty_expiry?: string;
  notes?: string;
}

/** GET /api/v1/search response item */
export interface SearchResult {
  entity_type: string;
  entity_id: string;
  title: string;
  body: string;
}

/** GET /api/v1/search response */
export interface SearchResponse {
  results: SearchResult[];
}
