/**
 * Entity configuration definitions for Home OS.
 *
 * Each entity config defines:
 * - fields: form field definitions with type, label, options, and required flag
 * - sections: logical groupings of fields for form organization
 * - listColumns: columns to display in entity list views
 */

// ─── Field Types ──────────────────────────────────────────────────────────────

export type FieldType = "text" | "number" | "currency" | "date" | "select" | "textarea" | "checkbox";

export interface FieldConfig {
  name: string;
  label: string;
  type: FieldType;
  options?: { value: string; label: string }[];
  required?: boolean;
}

export interface SectionConfig {
  title: string;
  fields: string[];
}

export interface ListColumnConfig {
  name: string;
  label: string;
  format?: "currency" | "date" | "badge";
}

// ─── Entity Configs ───────────────────────────────────────────────────────────

export const vendorConfig = {
  fields: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "specialty", label: "Specialty", type: "text" },
    { name: "phone", label: "Phone", type: "text" },
    { name: "email", label: "Email", type: "text" },
    { name: "website", label: "Website", type: "text" },
    { name: "notes", label: "Notes", type: "textarea" },
  ] as FieldConfig[],
  sections: [
    { title: "Basic Information", fields: ["name", "specialty"] },
    { title: "Contact Details", fields: ["phone", "email", "website"] },
    { title: "Additional Info", fields: ["notes"] },
  ] as SectionConfig[],
  listColumns: [
    { name: "name", label: "Name" },
    { name: "specialty", label: "Specialty", format: "badge" },
    { name: "phone", label: "Phone" },
    { name: "email", label: "Email" },
  ] as ListColumnConfig[],
};

export const billConfig = {
  fields: [
    { name: "name", label: "Bill Name", type: "text", required: true },
    { name: "amount", label: "Amount", type: "currency", required: true },
    { name: "due_day", label: "Due Day", type: "number" },
    { name: "category", label: "Category", type: "select", options: [
      { value: "utilities", label: "Utilities" },
      { value: "insurance", label: "Insurance" },
      { value: "subscription", label: "Subscription" },
      { value: "loan", label: "Loan" },
      { value: "tax", label: "Tax" },
      { value: "other", label: "Other" },
    ] },
    { name: "vendor_id", label: "Vendor", type: "select", options: [] },
    { name: "rrule", label: "Recurrence", type: "text" },
    { name: "notes", label: "Notes", type: "textarea" },
    { name: "entity_type", label: "Entity Type", type: "select", options: [
      { value: "property", label: "Property" },
      { value: "vehicle", label: "Vehicle" },
      { value: "asset", label: "Asset" },
    ] },
    { name: "entity_id", label: "Entity", type: "select", options: [] },
    { name: "paid_date", label: "Paid Date", type: "date" },
    { name: "is_autopay", label: "Auto-pay", type: "checkbox" },
    { name: "account_number", label: "Account Number", type: "text" },
    { name: "payment_url", label: "Payment URL", type: "text" },
  ] as FieldConfig[],
  sections: [
    { title: "Bill Details", fields: ["name", "amount", "category", "due_day"] },
    { title: "Vendor", fields: ["vendor_id"] },
    { title: "Billing Info", fields: ["account_number", "payment_url", "is_autopay"] },
    { title: "Recurrence", fields: ["rrule"] },
    { title: "Payment", fields: ["paid_date"] },
    { title: "Additional Info", fields: ["notes"] },
  ] as SectionConfig[],
  listColumns: [
    { name: "name", label: "Name" },
    { name: "amount", label: "Amount", format: "currency" },
    { name: "category", label: "Category", format: "badge" },
    { name: "due_day", label: "Due Day" },
    { name: "paid_date", label: "Paid Date", format: "date" },
    { name: "is_autopay", label: "Auto-pay", format: "badge" },
  ] as ListColumnConfig[],
};

export const assetConfig = {
  fields: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "category", label: "Category", type: "select", options: [
      { value: "appliance", label: "Appliance" },
      { value: "electronics", label: "Electronics" },
      { value: "furniture", label: "Furniture" },
      { value: "tool", label: "Tool" },
      { value: "other", label: "Other" },
    ] },
    { name: "manufacturer", label: "Manufacturer", type: "text" },
    { name: "model", label: "Model", type: "text" },
    { name: "serial_number", label: "Serial Number", type: "text" },
    { name: "purchase_date", label: "Purchase Date", type: "date" },
    { name: "purchase_price", label: "Purchase Price", type: "currency" },
    { name: "warranty_expiry", label: "Warranty Expiry", type: "date" },
    { name: "property_id", label: "Property", type: "select", options: [] },
    { name: "room_id", label: "Room", type: "select", options: [] },
    { name: "notes", label: "Notes", type: "textarea" },
  ] as FieldConfig[],
  sections: [
    { title: "Asset Details", fields: ["name", "category", "manufacturer", "model"] },
    { title: "Identification", fields: ["serial_number"] },
    { title: "Location", fields: ["property_id", "room_id"] },
    { title: "Purchase Info", fields: ["purchase_date", "purchase_price", "warranty_expiry"] },
    { title: "Additional Info", fields: ["notes"] },
  ] as SectionConfig[],
  listColumns: [
    { name: "name", label: "Name" },
    { name: "category", label: "Category", format: "badge" },
    { name: "manufacturer", label: "Manufacturer" },
    { name: "model", label: "Model" },
    { name: "purchase_price", label: "Purchase Price", format: "currency" },
  ] as ListColumnConfig[],
};

export const vehicleConfig = {
  fields: [
    { name: "year", label: "Year", type: "number" },
    { name: "make", label: "Make", type: "text" },
    { name: "model", label: "Model", type: "text" },
    { name: "vin", label: "VIN", type: "text" },
    { name: "license_plate", label: "License Plate", type: "text" },
    { name: "color", label: "Color", type: "text" },
    { name: "purchase_price", label: "Purchase Price", type: "currency" },
    { name: "purchase_date", label: "Purchase Date", type: "date" },
    { name: "lender", label: "Lender", type: "text" },
    { name: "loan_amount", label: "Loan Amount", type: "currency" },
    { name: "loan_term_months", label: "Loan Term (months)", type: "number" },
    { name: "monthly_payment", label: "Monthly Payment", type: "currency" },
    { name: "registration_renewal_month", label: "Registration Renewal Month", type: "number" },
    { name: "registration_cost", label: "Registration Cost", type: "currency" },
    { name: "insurance_provider", label: "Insurance Provider", type: "text" },
    { name: "insurance_cost", label: "Insurance Cost", type: "currency" },
    { name: "notes", label: "Notes", type: "textarea" },
  ] as FieldConfig[],
  sections: [
    { title: "Vehicle Details", fields: ["year", "make", "model", "vin", "license_plate", "color"] },
    { title: "Purchase Info", fields: ["purchase_price", "purchase_date"] },
    { title: "Financing", fields: ["lender", "loan_amount", "loan_term_months", "monthly_payment"] },
    { title: "Registration", fields: ["registration_renewal_month", "registration_cost"] },
    { title: "Insurance", fields: ["insurance_provider", "insurance_cost"] },
    { title: "Additional Info", fields: ["notes"] },
  ] as SectionConfig[],
  listColumns: [
    { name: "year", label: "Year" },
    { name: "make", label: "Make" },
    { name: "model", label: "Model" },
    { name: "license_plate", label: "License Plate" },
    { name: "purchase_price", label: "Purchase Price", format: "currency" },
  ] as ListColumnConfig[],
};

export const petConfig = {
  fields: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "species", label: "Species", type: "select", options: [
      { value: "dog", label: "Dog" },
      { value: "cat", label: "Cat" },
      { value: "bird", label: "Bird" },
      { value: "fish", label: "Fish" },
      { value: "reptile", label: "Reptile" },
      { value: "other", label: "Other" },
    ] },
    { name: "breed", label: "Breed", type: "text" },
    { name: "date_of_birth", label: "Date of Birth", type: "date" },
    { name: "vet_name", label: "Veterinarian", type: "text" },
    { name: "vet_phone", label: "Vet Phone", type: "text" },
    { name: "microchip_id", label: "Microchip ID", type: "text" },
    { name: "insurance_provider", label: "Insurance Provider", type: "text" },
    { name: "insurance_policy", label: "Insurance Policy", type: "text" },
    { name: "registration_id", label: "Registration ID", type: "text" },
    { name: "notes", label: "Notes", type: "textarea" },
  ] as FieldConfig[],
  sections: [
    { title: "Pet Details", fields: ["name", "species", "breed", "date_of_birth"] },
    { title: "Veterinary Info", fields: ["vet_name", "vet_phone"] },
    { title: "Identification", fields: ["microchip_id", "registration_id"] },
    { title: "Insurance", fields: ["insurance_provider", "insurance_policy"] },
    { title: "Additional Info", fields: ["notes"] },
  ] as SectionConfig[],
  listColumns: [
    { name: "name", label: "Name" },
    { name: "species", label: "Species", format: "badge" },
    { name: "breed", label: "Breed" },
    { name: "date_of_birth", label: "DOB", format: "date" },
  ] as ListColumnConfig[],
};

export const propertyConfig = {
  fields: [
    { name: "name", label: "Name", type: "text", required: true },
    { name: "address", label: "Address", type: "text" },
    { name: "property_type", label: "Property Type", type: "select", options: [
      { value: "house", label: "House" },
      { value: "apartment", label: "Apartment" },
      { value: "condo", label: "Condo" },
      { value: "townhouse", label: "Townhouse" },
      { value: "land", label: "Land" },
      { value: "commercial", label: "Commercial" },
      { value: "other", label: "Other" },
    ] },
    { name: "purchase_price", label: "Purchase Price", type: "currency" },
    { name: "purchase_date", label: "Purchase Date", type: "date" },
    { name: "current_value", label: "Current Value", type: "currency" },
    { name: "mortgage_amount", label: "Mortgage Amount", type: "currency" },
    { name: "notes", label: "Notes", type: "textarea" },
  ] as FieldConfig[],
  sections: [
    { title: "Basic Information", fields: ["name", "address", "property_type"] },
    { title: "Financial Details", fields: ["purchase_price", "purchase_date", "current_value", "mortgage_amount"] },
    { title: "Additional Info", fields: ["notes"] },
  ] as SectionConfig[],
  listColumns: [
    { name: "name", label: "Name" },
    { name: "property_type", label: "Type", format: "badge" },
    { name: "address", label: "Address" },
    { name: "current_value", label: "Current Value", format: "currency" },
  ] as ListColumnConfig[],
};

export const loanConfig = {
  fields: [
    { name: "name", label: "Loan Name", type: "text", required: true },
    { name: "lender", label: "Lender", type: "text" },
    { name: "original_amount", label: "Original Amount", type: "currency", required: true },
    { name: "interest_rate", label: "Interest Rate (%)", type: "number" },
    { name: "term_months", label: "Term (months)", type: "number" },
    { name: "monthly_payment", label: "Monthly Payment", type: "currency" },
    { name: "remaining_balance", label: "Remaining Balance", type: "currency", required: true },
    { name: "start_date", label: "Start Date", type: "date" },
    { name: "entity_type", label: "Entity Type", type: "select", options: [
      { value: "property", label: "Property" },
      { value: "vehicle", label: "Vehicle" },
      { value: "asset", label: "Asset" },
    ] },
    { name: "entity_id", label: "Entity", type: "select", options: [] },
    { name: "notes", label: "Notes", type: "textarea" },
  ] as FieldConfig[],
  sections: [
    { title: "Loan Details", fields: ["name", "lender", "original_amount", "remaining_balance"] },
    { title: "Terms", fields: ["interest_rate", "term_months", "monthly_payment", "start_date"] },
    { title: "Association", fields: ["entity_type", "entity_id"] },
    { title: "Additional Info", fields: ["notes"] },
  ] as SectionConfig[],
  listColumns: [
    { name: "name", label: "Name" },
    { name: "lender", label: "Lender" },
    { name: "original_amount", label: "Original Amount", format: "currency" },
    { name: "remaining_balance", label: "Remaining Balance", format: "currency" },
    { name: "monthly_payment", label: "Monthly Payment", format: "currency" },
  ] as ListColumnConfig[],
};

// ─── Entity Config Registry ───────────────────────────────────────────────────

export const entityConfigs = {
  vendor: vendorConfig,
  bill: billConfig,
  asset: assetConfig,
  vehicle: vehicleConfig,
  pet: petConfig,
  property: propertyConfig,
  loan: loanConfig,
};

export type EntityName = keyof typeof entityConfigs;
