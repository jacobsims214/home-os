/**
 * Yes/no fields in EntityDetail edit mode (#4728).
 *
 * The record holds a real boolean (or nothing), the Select works on the
 * strings "true" / "false". These map one to the other so the saved value is
 * shown, and a choice goes back to the API as a boolean, never a string.
 * Unset stays unset: null for the Select (placeholder), and the form value is
 * left untouched, so it isn't sent.
 *
 * Kept free of React and "@/" imports so `node --test` can load it directly.
 */

export type BooleanOption = "true" | "false";

export const BOOLEAN_OPTIONS: { value: BooleanOption; label: string }[] = [
  { value: "true", label: "Yes" },
  { value: "false", label: "No" },
];

/** The Select value for a stored value: "true", "false", or null when unset. */
export function booleanToOption(value: unknown): BooleanOption | null {
  if (value === true || value === "true") return "true";
  if (value === false || value === "false") return "false";
  return null;
}

/** The form value for a Select choice: true, false, or undefined (not sent). */
export function optionToBoolean(option: string | null): boolean | undefined {
  if (option === "true") return true;
  if (option === "false") return false;
  return undefined;
}
