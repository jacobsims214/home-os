/**
 * Switching the active household (#4574), in one place.
 *
 * What a switch has to do, and why each step is there:
 *
 * 1. REMEMBER the choice before anything else happens, in the
 *    `home-os-household` cookie (lib/household-cookie.ts). The BFF turns that
 *    cookie into the API's `X-Household-ID` header, so from that instant every
 *    later request — including `<a href="/bff/...">` downloads that carry no
 *    headers of their own — is scoped to the new family.
 *
 * 2. CANCEL what is in flight. A fetch started a moment before the switch was
 *    asked for the OLD household; letting it land would put the previous
 *    family's rows into the cache under the new household's identity. That is
 *    the data leak this ticket exists to prevent, and cancelling is the only
 *    way to stop a response that is already on the wire.
 *
 * 3. DROP the cached data, do not just mark it stale. `invalidateQueries` keeps
 *    the old rows on screen while the refetch runs — a switcher that shows the
 *    other family's bills for 300 ms is exactly the bug. `resetQueries` puts
 *    every observer back to its initial (pending, data-less) state and refetches
 *    the ones on screen, so the page goes skeleton → new family, never
 *    old family → new family.
 *
 * 4. KEEP the family list (`["me","households"]`). It is the signed-in user's
 *    own families with their roles, not one household's data, and it is what the
 *    switcher and the role-aware navigation read. Keeping it is also what lets
 *    the new household's role apply in the same render as the switch instead of
 *    a flash of the old one (components/shared/use-nav-role.ts), and its
 *    `current` flag is refreshed right after.
 *
 * 5. FORGET household-scoped client state: the secrets vault's in-memory key,
 *    the persisted recent-items list and the selected property, all of which
 *    would otherwise survive into the other family.
 *
 * The caller passes the side effects in (deps), so this file is pure enough to
 * unit test with a fake cache and a fake cookie writer
 * (lib/household-switch.test.mjs) — the order of the steps above is asserted
 * there.
 *
 * No Next imports, so `node --test` can load this file.
 */

import type { QueryClient } from "@tanstack/react-query";
import { isHouseholdId } from "./household-cookie";

/** The signed-in user's families with their role in each (GET /me/households). */
export const HOUSEHOLD_LIST_QUERY_KEY = ["me", "households"] as const;

/**
 * True for a query key inside the family list, e.g.
 * ["me","households"]. A switch keeps these: they belong to the user, not the
 * household, and both the switcher and the role-aware nav read them.
 */
export function isHouseholdListQueryKey(queryKey: readonly unknown[]): boolean {
  if (queryKey.length < HOUSEHOLD_LIST_QUERY_KEY.length) return false;
  return HOUSEHOLD_LIST_QUERY_KEY.every((part, i) => queryKey[i] === part);
}

/** The part of TanStack Query's client a switch touches. */
export type HouseholdSwitchClient = Pick<
  QueryClient,
  "cancelQueries" | "resetQueries" | "invalidateQueries"
>;

export interface HouseholdSwitchDeps {
  /** Persist the choice where later requests read it. Must run before any refetch. */
  remember: (householdId: string) => void;
  /** Drop household-scoped client state that must not survive the switch. */
  forgetHouseholdState: () => void;
  client: HouseholdSwitchClient;
}

/**
 * Make `householdId` the active household: remember it, kill the old family's
 * in-flight and cached data, then refetch what is on screen.
 *
 * Rejects on an id that is not a UUID, because the API would answer 400 for
 * every request afterwards and the UI would look broken rather than refuse.
 */
export async function applyHouseholdSwitch(
  deps: HouseholdSwitchDeps,
  householdId: string,
): Promise<void> {
  if (!isHouseholdId(householdId)) {
    throw new Error(`applyHouseholdSwitch: not a household id: ${String(householdId)}`);
  }
  const id = householdId.trim();

  // 1. Remember first: everything after this must already be scoped to it.
  deps.remember(id);
  // 5. Client state that belongs to the household we are leaving.
  deps.forgetHouseholdState();

  const scoped = { predicate: (query: { queryKey: readonly unknown[] }) => !isHouseholdListQueryKey(query.queryKey) };

  // 2. In-flight fetches were asked for the old household: cancel them so the
  //    response cannot be written.
  await deps.client.cancelQueries(scoped);
  // 3. Drop every household-scoped cache entry and refetch the mounted ones.
  await deps.client.resetQueries(scoped);
  // 4. The kept list now has a stale `current` flag; refresh it without
  //    dropping the roles the nav is reading.
  await deps.client.invalidateQueries({ queryKey: HOUSEHOLD_LIST_QUERY_KEY });
}

// ---------------------------------------------------------------------------
// Where to go after a switch
// ---------------------------------------------------------------------------

/**
 * Sub-paths of a section that are pages, not records. `/dashboard/meals/grocery`
 * is a page of the Meals section; anything else in that position is a record id
 * (`/dashboard/bills/<uuid>`), which belongs to the household we just left.
 */
const SECTION_PAGES = new Set(["grocery", "search"]);

/**
 * Sections whose sub-paths are all PAGES, never records: Settings (#4560) is an
 * index plus /dashboard/settings/<section>, and every section reads the
 * household the request is scoped to, so a switch can stay right where it is
 * instead of bouncing the person out to the section list.
 */
const PAGED_SECTIONS = new Set(["settings"]);

/**
 * The URL a switch should move to, or null when the current page is safe.
 *
 * A record page names one household's row by id. After a switch that id is not
 * this family's, so the honest thing is to leave the page rather than render a
 * "not found" for a bill that exists in the family next door. Section pages
 * (lists, the week plan, settings) are fine: their data refetches.
 */
export function householdSwitchTarget(pathname: string | null | undefined): string | null {
  const path = (pathname || "/").replace(/\/+$/, "") || "/";
  const parts = path.split("/").filter(Boolean);
  // ["dashboard", "<section>", "<maybe record id>"]
  if (parts[0] !== "dashboard" || parts.length < 3) return null;
  if (PAGED_SECTIONS.has(parts[1] ?? "")) return null;
  if (SECTION_PAGES.has(parts[2])) return null;
  const section = parts[1];
  if (!section) return "/dashboard";
  return `/dashboard/${section}`;
}
