/**
 * Pet vet helpers (#4703). A pet's vet is a family contact (vendor) with the
 * trade "vet", referenced by pets.vet_vendor_id (KB apis/pets.md). The UI
 * never writes the legacy vet_name / vet_phone: the API returns them read-only
 * (the vendor's name and phone) and #4705 drops the columns.
 */

/** A contact as GET /api/v1/vendors returns it (only the fields the picker uses). */
export interface VetContact {
  id: string;
  name: string;
  phone?: string | null;
  trades?: string[];
}

/** GET /api/v1/vendors?trade=vet: the family's vet contacts. */
export const VET_TRADE = "vet";

/** Query key for the vet list; under ["vendor"] so vendor invalidations refresh it too. */
export const vetContactsKey = ["vendor", { trade: VET_TRADE }] as const;

export type NewVetResult =
  | { ok: true; body: { name: string; phone?: string; specialty: string; trades: string[] } }
  | { ok: false; errors: { name?: string } };

/** POST /api/v1/vendors body for "add a new vet contact". */
export function newVetBody(input: { name: string; phone: string }): NewVetResult {
  const name = input.name.trim();
  if (!name) return { ok: false, errors: { name: "Enter the vet's name" } };
  const phone = input.phone.trim();
  return {
    ok: true,
    body: { name, ...(phone ? { phone } : {}), specialty: "Veterinarian", trades: [VET_TRADE] },
  };
}
