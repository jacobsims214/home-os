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
  purchase_price?: string;
  purchase_date?: string;
  current_value?: string;
  mortgage_amount?: string;
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
  current_value?: string;
  warranty_expiry?: string;
  notes?: string;
  created_at: string;
  updated_at: string;
}

/** POST /api/v1/assets request body */
/** GET /api/v1/vendors response item (a family contact). `trades` is always an array. */
export interface Vendor {
  id: string;
  household_id: string;
  name: string;
  trades: string[];
  specialty?: string;
  phone?: string;
  email?: string;
  website?: string;
  notes?: string;
  created_at: string;
  updated_at: string;
}

/**
 * GET /api/v1/pets response item (#4700). The vet is a family contact:
 * `vet_vendor_id`. `vet_name` / `vet_phone` are read-only, the contact's name
 * and phone, omitted when there is no vet.
 */
export interface Pet {
  id: string;
  household_id: string;
  name: string;
  species?: string;
  breed?: string;
  date_of_birth?: string;
  vet_vendor_id?: string;
  readonly vet_name?: string;
  readonly vet_phone?: string;
  notes?: string;
  created_at: string;
  updated_at: string;
}

/** POST / PUT /api/v1/pets body. Never vet_name / vet_phone (#4703, dropped in #4705). */
export interface PetWriteRequest {
  name: string;
  species?: string;
  breed?: string;
  date_of_birth?: string;
  vet_vendor_id?: string | null;
  notes?: string;
}

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

// ─── File types ───────────────────────────────────────────────

export type FileOCRStatus = "pending" | "processing" | "done" | "failed" | "skipped";

export interface FileRecord {
  id: string;
  household_id: string;
  blob_id: string;
  name: string;
  content_type: string;
  size: number;
  entity_type?: string;
  entity_id?: string;
  extracted_text?: string;
  ocr_status: FileOCRStatus;
  ocr_error?: string;
  tags?: string[] | null;
  created_at: string;
  updated_at: string;
}

// ─── Secret types ─────────────────────────────────────────────

export type SecretType = "login" | "note" | "api_key" | "card";

export interface SecretRecord {
  id: string;
  household_id: string;
  entity_type?: string;
  entity_id?: string;
  encrypted_data?: string; // base64 — only present on GET /secrets/:id
  iv?: string; // base64 — only present on GET /secrets/:id
  key_version: number;
  name: string;
  secret_type: SecretType;
  created_at: string;
  updated_at: string;
}

/** Alias used by UI components */
export type Secret = SecretRecord;

/** Metadata-only secret (from GET /api/v1/secrets list — no encrypted_data) */
export type SecretListItem = Omit<SecretRecord, "encrypted_data" | "iv">;

/** Response envelope for GET /api/v1/secrets */
export interface SecretListResponse {
  data: SecretListItem[];
}

/** POST /api/v1/secrets request body */
export interface CreateSecretRequest {
  encrypted_data: string;
  iv: string;
  key_version: number;
  name: string;
  secret_type: SecretType;
  entity_type?: string;
  entity_id?: string;
}

/** PATCH /api/v1/secrets/:id request body */
export interface UpdateSecretRequest {
  encrypted_data: string;
  iv: string;
  key_version: number;
  name: string;
  secret_type: SecretType;
}
