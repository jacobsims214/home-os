/**
 * Pure helpers for EntityList (#4530): value formatting, the card title, and
 * the built-in create form (fields and the POST body).
 *
 * Kept free of React and of "@/" imports so `node --test` can load it
 * directly (see entity-list-utils.test.mjs).
 */

export interface EntityColumn {
  name: string;
  label: string;
  format?: "currency" | "date" | "badge";
}

export interface EntityRecord {
  id: string;
  name?: string;
  title?: string;
  property_id?: string;
  amount?: string | number;
  [key: string]: unknown;
}

/**
 * A field in the built-in create form.
 * - "decimal" is sent as a string (the API keeps money as NUMERIC strings).
 * - "integer" is sent as a whole number (year, due_day).
 * - "property" is a select filled with the household's properties.
 */
export interface CreateField {
  name: string;
  label: string;
  type: "text" | "textarea" | "decimal" | "integer" | "date" | "select" | "property";
  required?: boolean;
  options?: { value: string; label: string }[];
  min?: number;
  max?: number;
}

const BILL_CATEGORIES = [
  "Mortgage", "Electric", "Water", "Gas", "Internet", "Trash", "Insurance", "HOA", "Subscription", "Other",
].map((c) => ({ value: c, label: c }));

const VENDOR_SPECIALTIES = [
  "HVAC", "Plumbing", "Electrical", "Roofing", "Landscaping", "Cleaning", "Pest Control", "General Contractor", "Other",
].map((c) => ({ value: c, label: c }));

/**
 * Create fields per entity type, matching the Go create handlers' request
 * types (apps/api/internal/<type>/handler.go). Types not listed here fall
 * back to the list's columns as text fields.
 */
export const DEFAULT_CREATE_FIELDS: Record<string, CreateField[]> = {
  bill: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "amount", label: "Amount", type: "decimal" },
    { name: "due_day", label: "Due day of month", type: "integer", min: 1, max: 31 },
    { name: "category", label: "Category", type: "select", options: BILL_CATEGORIES },
    { name: "property_id", label: "Property", type: "property" },
  ],
  asset: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "category", label: "Category", type: "text" },
    { name: "manufacturer", label: "Manufacturer", type: "text" },
    { name: "model", label: "Model", type: "text" },
    { name: "property_id", label: "Property", type: "property" },
  ],
  vehicle: [
    { name: "year", label: "Year", type: "integer", required: true, min: 1900, max: 2100 },
    { name: "make", label: "Make", type: "text", required: true },
    { name: "model", label: "Model", type: "text", required: true },
  ],
  pet: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "species", label: "Species", type: "text" },
    { name: "breed", label: "Breed", type: "text" },
  ],
  vendor: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "specialty", label: "Specialty", type: "select", options: VENDOR_SPECIALTIES },
    { name: "phone", label: "Phone", type: "text" },
    { name: "email", label: "Email", type: "text" },
  ],
};

/** The create form for an entity type: its defaults, or the columns as text. */
export function createFieldsFor(entityType: string, columns: EntityColumn[]): CreateField[] {
  const defaults = DEFAULT_CREATE_FIELDS[entityType];
  if (defaults) return defaults;
  return columns.map((col, i) => ({
    name: col.name,
    label: col.label,
    type: col.format === "currency" ? "decimal" : col.format === "date" ? "date" : "text",
    required: i === 0,
  }));
}

/**
 * Turn the form's string values into the POST body: trims, drops empty
 * optional fields, and converts integer fields to numbers. Returns the
 * errors per field instead when a required field is empty or a number is
 * invalid.
 */
export function buildCreatePayload(
  fields: CreateField[],
  values: Record<string, string>,
): { ok: true; body: Record<string, unknown> } | { ok: false; errors: Record<string, string> } {
  const body: Record<string, unknown> = {};
  const errors: Record<string, string> = {};
  for (const field of fields) {
    const raw = (values[field.name] ?? "").trim();
    if (raw === "") {
      if (field.required) errors[field.name] = `${field.label} is required`;
      continue;
    }
    if (field.type === "integer") {
      const n = Number(raw);
      if (!Number.isInteger(n)) {
        errors[field.name] = `${field.label} must be a whole number`;
      } else if ((field.min != null && n < field.min) || (field.max != null && n > field.max)) {
        errors[field.name] = `${field.label} must be between ${field.min} and ${field.max}`;
      } else {
        body[field.name] = n;
      }
      continue;
    }
    if (field.type === "decimal") {
      const cleaned = raw.replace(/[$,\s]/g, "");
      if (!/^-?\d+(\.\d+)?$/.test(cleaned)) {
        errors[field.name] = `${field.label} must be a number`;
      } else {
        body[field.name] = cleaned;
      }
      continue;
    }
    body[field.name] = raw;
  }
  return Object.keys(errors).length > 0 ? { ok: false, errors } : { ok: true, body };
}

/** Display a cell value. Unknown or empty values render as an em dash. */
export function formatValue(value: unknown, format?: EntityColumn["format"]): string {
  if (value == null || value === "") return "—";
  if (format === "currency") {
    const n = Number(value);
    if (Number.isNaN(n)) return "—";
    return n.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
  }
  if (format === "date") {
    const d = new Date(String(value));
    return Number.isNaN(d.getTime()) ? String(value) : d.toLocaleDateString();
  }
  return String(value);
}

/**
 * A card's heading and the columns shown under it. The heading is the
 * entity's name or title; an entity without one (a vehicle) is named by its
 * columns joined ("2019 Honda Odyssey"), and then has no detail rows.
 */
export function cardParts(
  entity: EntityRecord,
  columns: EntityColumn[],
): { heading: string; details: EntityColumn[] } {
  const own = typeof entity.name === "string" && entity.name ? "name" : typeof entity.title === "string" && entity.title ? "title" : null;
  if (own) {
    return { heading: String(entity[own]), details: columns.filter((c) => c.name !== own) };
  }
  const heading = columns
    .map((c) => entity[c.name])
    .filter((v) => v != null && v !== "")
    .join(" ");
  return { heading: heading || "Untitled", details: [] };
}
