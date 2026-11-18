"use client";

/**
 * The one way the UI switches households (#4574).
 *
 * Every opener (header family chip, sidebar/drawer family card, More sheet row,
 * the switcher's own rows) goes through this hook, so the security-relevant
 * steps can't be implemented differently in one place and forgotten in another.
 * The steps themselves live in lib/household-switch.ts (documented and unit
 * tested there); this file only supplies the real side effects.
 */

import { useCallback } from "react";
import { usePathname, useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { applyHouseholdSwitch, householdSwitchTarget } from "@/lib/household-switch";
import { useHouseholdStore } from "@/stores/household";
import { usePropertyStore } from "@/stores/property";
import { useRecentStore } from "@/stores/recent";
import { useSecretsStore } from "@/stores/secrets";

export interface SwitchHouseholdResult {
  /** False when the household was already the active one (nothing happened). */
  changed: boolean;
}

export function useHouseholdSwitch(): (householdId: string) => Promise<SwitchHouseholdResult> {
  const queryClient = useQueryClient();
  const router = useRouter();
  const pathname = usePathname();
  const setActiveHousehold = useHouseholdStore((s) => s.setActiveHousehold);

  return useCallback(
    async (householdId: string) => {
      const { activeHouseholdId } = useHouseholdStore.getState();
      if (activeHouseholdId === householdId) return { changed: false };

      await applyHouseholdSwitch(
        {
          // The cookie is written here, FIRST, before any refetch runs: from this
          // point every BFF request carries the new household.
          remember: setActiveHousehold,
          forgetHouseholdState: () => {
            // The vault key was derived for the previous household's master
            // password; keeping it unlocked would show its secrets here.
            useSecretsStore.getState().lock();
            useRecentStore.getState().clearItems();
            usePropertyStore.getState().clearActiveProperty();
          },
          client: queryClient,
        },
        householdId,
      );

      // A record page names another family's row by id: leave it for the
      // section's list rather than render a 404 for the bill next door.
      const target = householdSwitchTarget(pathname);
      if (target) router.replace(target);
      return { changed: true };
    },
    [pathname, queryClient, router, setActiveHousehold],
  );
}
