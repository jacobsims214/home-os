/**
 * The active household, client side (#4574).
 *
 * Deliberately NOT a persisted zustand store: the persistence is the
 * `home-os-household` cookie (lib/household-cookie.ts), because the BFF — not
 * the browser — is what turns the choice into the API's X-Household-ID header.
 * Everything that is not fetch() (file downloads, any future server render)
 * carries the cookie and nothing else, so the cookie has to be the source of
 * truth and this store is just its readable mirror for React.
 *
 * `hydrate()` reads the cookie once, after mount: reading document.cookie during
 * render would disagree with the server's HTML.
 */

import { create } from "zustand";
import { readHouseholdCookie, writeHouseholdCookie } from "@/lib/household-cookie";

interface HouseholdState {
  /** The household the UI is working in, or null before the cookie is read / when unset. */
  activeHouseholdId: string | null;
  /** True once the cookie has been read on this page. */
  hydrated: boolean;
}

interface HouseholdActions {
  /** Read the cookie into state. Safe to call from every consumer; runs once. */
  hydrate: () => void;
  /** Make `id` active: writes the cookie AND updates state, in that order. */
  setActiveHousehold: (id: string | null) => void;
}

export const useHouseholdStore = create<HouseholdState & HouseholdActions>()((set, get) => ({
  activeHouseholdId: null,
  hydrated: false,

  hydrate: () => {
    if (get().hydrated) return;
    set({ activeHouseholdId: readHouseholdCookie(), hydrated: true });
  },

  setActiveHousehold: (id) => {
    // Cookie first: every request after this line is scoped to it.
    writeHouseholdCookie(id);
    set({ activeHouseholdId: id, hydrated: true });
  },
}));
