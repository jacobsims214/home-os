"use client";

/**
 * The household a page is working in, and the signed-in user's role there
 * (#4543, multi-household from #4574).
 *
 * The list comes from GET /api/v1/me/households: every household the caller
 * belongs to, each with the role they hold in it, `is_default` on the oldest
 * membership and `current` on the one the request resolved to (the
 * X-Household-ID the BFF sent from the `home-os-household` cookie).
 *
 * Which entry counts as "the household I am in" is decided HERE, once, for the
 * whole UI — the switcher, every nav surface and every role-gated page read it
 * through this hook, so they cannot disagree:
 *
 *   1. the cookie's household, when the list has it (the person switched, and
 *      this page may not have refetched the list yet);
 *   2. else the entry the API marked `current`;
 *   3. else the default / oldest membership.
 *
 * Reading the cookie first is what makes a switch instant for the role: the new
 * household's role is applied in the same render as the switch, never the role
 * carried over from the family the person just left (#4574).
 *
 * The UI only hides what a role can't use; the API's role policy enforces it
 * (KB decisions/2026-11-15-role-policy.md).
 */

import { useEffect, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { HOUSEHOLD_COOKIE } from "@/lib/household-cookie";
import { HOUSEHOLD_LIST_QUERY_KEY } from "@/lib/household-switch";
import { useHouseholdStore } from "@/stores/household";
import { NAV_SECTIONS, sectionsForRole, type NavSection } from "@/components/shared/nav-config";

export interface MyHousehold {
  id: string;
  name: string;
  timezone: string;
  role: string;
  joined_at: string;
  is_default?: boolean;
  current?: boolean;
}

/** Shared with the family switcher (#4574), which lists the same data. */
export const MY_HOUSEHOLDS_QUERY_KEY = HOUSEHOLD_LIST_QUERY_KEY;

export function useMyHouseholds() {
  return useQuery({
    queryKey: MY_HOUSEHOLDS_QUERY_KEY,
    queryFn: () => apiFetch<{ data: MyHousehold[] }>("/api/v1/me/households"),
    // The list is the user's own families and their roles, so it is not
    // household-scoped data and a switch keeps it (lib/household-switch.ts).
    // Still short-lived: a role change made in another tab shows up quickly.
    staleTime: 60_000,
    retry: false,
  });
}

/**
 * The household the person switched to, as React state.
 *
 * `document.cookie` is read once, after mount (the store's hydrate), so server
 * HTML and the first client render agree. Before that the value is null and the
 * selection falls back to what the API marked `current` — which is the same
 * household, because the BFF sends the cookie upstream. The store survives
 * client-side navigation, so after a switch this is already the new id.
 */
export function useActiveHouseholdId(): string | null {
  const active = useHouseholdStore((s) => s.activeHouseholdId);
  const hydrate = useHouseholdStore((s) => s.hydrate);

  useEffect(() => {
    hydrate();
  }, [hydrate]);

  return active;
}

/** Which entry is the household this UI is working in. */
export function pickCurrentHousehold(
  list: readonly MyHousehold[],
  activeId: string | null,
): MyHousehold | null {
  if (list.length === 0) return null;
  if (activeId) {
    const chosen = list.find((h) => h.id === activeId);
    if (chosen) return chosen;
    // The cookie names a household this user isn't in any more (membership
    // removed elsewhere). Fall through to what the API resolved rather than
    // keeping a stale selection.
  }
  return list.find((h) => h.current) ?? list.find((h) => h.is_default) ?? list[0];
}

/** The household this UI is working in, with this user's role in it. */
export function useCurrentHousehold(): MyHousehold | null {
  const q = useMyHouseholds();
  const activeId = useActiveHouseholdId();
  // The memo's dependency: a new [] every render would defeat it.
  const list = useMemo(() => (Array.isArray(q.data?.data) ? q.data!.data : []), [q.data]);
  return useMemo(() => pickCurrentHousehold(list, activeId), [list, activeId]);
}

/** The current household's role, or null while it isn't known. */
export function useNavRole(): string | null {
  return useCurrentHousehold()?.role || null;
}

/** The nav sections (or a subset of them) the current role sees. */
export function useNavSections(sections: readonly NavSection[] = NAV_SECTIONS): NavSection[] {
  const role = useNavRole();
  return useMemo(() => sectionsForRole(role, sections), [role, sections]);
}

/** The cookie name, re-exported for tests and for the logout route. */
export { HOUSEHOLD_COOKIE };
