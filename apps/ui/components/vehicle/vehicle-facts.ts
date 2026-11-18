/**
 * Vehicle display helpers (#4555), mobile-first vehicles pages.
 *
 * A vehicle has no `name` column (apps/api/internal/vehicle/model.go): its
 * identity is year + make + model, which is what the People & House mockup
 * (artifact #89) prints under the owner's own label ("2019 Honda Odyssey").
 * There is no owner-of-a-vehicle field either, so the card cannot say
 * "Amber's minivan" the way the mockup's sample data does; it names the car.
 *
 * Free of React and of "@/" imports so `node --test` loads it directly
 * (vehicle-facts.test.mjs).
 */

/** The fields a vehicle heading needs. All nullable in the API. */
export interface VehicleIdentityFields {
  year?: number | string | null;
  make?: string | null;
  model?: string | null;
  license_plate?: string | null;
  color?: string | null;
}

/**
 * "2019 Honda Odyssey", from whatever of year/make/model is filled in.
 * Falls back to the plate, then to "Untitled vehicle" — never an empty card.
 */
export function vehicleTitle(v: VehicleIdentityFields): string {
  const parts = [v.year, v.make, v.model]
    .filter((x) => x != null && String(x).trim() !== "")
    .map((x) => String(x).trim());
  if (parts.length > 0) return parts.join(" ");
  const plate = v.license_plate?.trim();
  return plate ? plate : "Untitled vehicle";
}

/**
 * The one line under the heading: "8KJD214 · Blue". Either part may be
 * missing, and an empty result means the card shows no subline at all.
 */
export function vehicleSubtitle(v: VehicleIdentityFields): string {
  return [v.license_plate?.trim(), v.color?.trim()].filter(Boolean).join(" · ");
}

const MONTHS = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
];

/**
 * "Renews in March" from the vehicle's registration_renewal_month (1-12).
 * The API derives that month from the linked registration bill's due date
 * (#4704); it is a month, never a day, so the label says only the month.
 * Returns null when unset or out of range, and the card omits the tag.
 */
export function registrationLabel(month: number | string | null | undefined): string | null {
  if (month == null || month === "") return null;
  const n = Number(month);
  if (!Number.isInteger(n) || n < 1 || n > 12) return null;
  return `Renews in ${MONTHS[n - 1]}`;
}
