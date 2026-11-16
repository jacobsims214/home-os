"use client";

/**
 * The signed-in user's role in the current household, for role-aware
 * navigation (#4543). Read from GET /api/v1/me/households: the entry with
 * `current: true` (the household the API resolves for this session), else
 * the default one. The UI only hides what a role can't use; the API's role
 * policy enforces it (KB decisions/2026-11-15-role-policy.md).
 */

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { NAV_SECTIONS, sectionsForRole, type NavSection } from "@/components/shared/nav-config";

interface MyHousehold {
  id: string;
  name: string;
  role: string;
  is_default?: boolean;
  current?: boolean;
}

/** Shared with the family switcher (#4574), which lists the same data. */
export const MY_HOUSEHOLDS_QUERY_KEY = ["me", "households"] as const;

export function useMyHouseholds() {
  return useQuery({
    queryKey: MY_HOUSEHOLDS_QUERY_KEY,
    queryFn: () => apiFetch<{ data: MyHousehold[] }>("/api/v1/me/households"),
    staleTime: 5 * 60_000,
    retry: false,
  });
}

/** The current household's role, or null while it isn't known. */
export function useNavRole(): string | null {
  const q = useMyHouseholds();
  const list = Array.isArray(q.data?.data) ? q.data!.data : [];
  const current = list.find((h) => h.current) ?? list.find((h) => h.is_default) ?? list[0];
  return current?.role || null;
}

/** The nav sections (or a subset of them) the current role sees. */
export function useNavSections(sections: readonly NavSection[] = NAV_SECTIONS): NavSection[] {
  const role = useNavRole();
  return useMemo(() => sectionsForRole(role, sections), [role, sections]);
}
