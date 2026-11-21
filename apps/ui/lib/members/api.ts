"use client";

/**
 * The Members page's data layer (#4559): what exists, what does not, and why.
 *
 * ENDPOINTS USED (all through the BFF, all already in apps/api/cmd/api/routes.go
 * and mapped in middleware/roles.go — nothing here invents a route):
 *
 *   GET    /api/v1/me/households           the caller's role here AND the household
 *                                          record (name + timezone), via
 *                                          useCurrentHousehold() in
 *                                          components/shared/use-nav-role.ts
 *   GET    /api/v1/households/me/members   the roster: user_id, name, email, role,
 *                                          joined_at, vendor_id, vendor_name, color
 *                                          (ResMembers read: everyone but a vendor)
 *   GET    /api/v1/people                  relationship, allergies, user_id
 *                                          (ResPeople read_basic; a housesitter gets
 *                                          names/relationship/allergies only)
 *   GET    /api/v1/auth/me                 the caller's own user id ("you", self colour)
 *   PATCH  /api/v1/people/{id}             {color} — people.set_color (#4821)
 *   PATCH  /api/v1/households/me/members/{userId}  {role, vendor_id?} (ResMembers write)
 *   DELETE /api/v1/households/me/members/{userId}  (ResMembers write)
 *   GET    /api/v1/invites                 pending invitations (ResInvites read: managers)
 *   POST   /api/v1/invites                 {email, role, vendor_id?} (ResInvites write)
 *   DELETE /api/v1/invites/{id}            revoke a pending invite (#4688)
 *   GET    /api/v1/vendors                 the family's vendor contacts, to link a
 *                                          vendor login to a company (#4799)
 *
 * WHAT THE MOCKUP SHOWS THAT THE API DOES NOT HAVE (said in the UI, never faked):
 *  - A per-member "colour" on the members list is a JOIN of people.color (#4821),
 *    so it is real; changing it goes through PATCH /people/{id}, which needs the
 *    member's PERSON id. The members list does not return it, so this hook pairs
 *    each member with their person row by (user_id) — see `pairMembers`.
 *  - There is no "last active" column: memberships has no last-seen field and
 *    auth/me does not expose one. The roster shows the JOIN date instead, which
 *    the API does return.
 *  - There is no invite "resend": POST /invites mints a new link, so the row's
 *    action is Copy link (the accept_url GET /invites returns for pending ones).
 */

import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { personKeys } from "@/lib/query-keys";
// The pure join and the error wording live in roster.ts, so `npm test` can cover
// them without React or the path aliases. Re-exported here so callers import
// from one place.
import { pairMembers, type MemberRow, type PersonRow } from "./roster";
// The roster's shapes and the pure helpers live in roster.ts (no React, no path
// aliases), re-exported here so callers have one import for the page's data.
export { inviteExpiry, ownerCount, pairMembers, writeError } from "./roster";
export type { MemberRow, PersonRow, RosterMember, WriteError } from "./roster";

// ── Shapes, exactly as the API answers them ──────────────────────────

/** GET /api/v1/invites (apps/api/internal/invite/model.go inviteView). */
export interface InviteRow {
  id: string;
  email: string;
  role: string;
  invited_by: string;
  expires_at: string;
  accepted_at?: string | null;
  created_at: string;
  vendor_id: string | null;
  /** Present only for pending, unexpired invites (#4401). */
  accept_url?: string;
}

export interface VendorRow {
  id: string;
  name: string;
}

// ── Queries ──────────────────────────────────────────────────────────

export const memberKeys = {
  all: ["members-page"] as const,
  roster: () => [...memberKeys.all, "roster"] as const,
  invites: () => [...memberKeys.all, "invites"] as const,
  vendors: () => [...memberKeys.all, "vendors"] as const,
};

/**
 * The roster. One query for the members and one for the people, so a role that
 * cannot read people (none today: a vendor cannot read members either) still
 * gets a roster; the page renders what each returns.
 */
export function useRoster(enabled: boolean, viewerUserId: string | null) {
  const members = useQuery({
    queryKey: memberKeys.roster(),
    queryFn: () => apiFetch<{ data: MemberRow[] }>("/api/v1/households/me/members"),
    enabled,
    staleTime: 30_000,
    retry: false,
  });
  const people = useQuery({
    queryKey: personKeys.list(),
    queryFn: () => apiFetch<{ data: PersonRow[] }>("/api/v1/people"),
    enabled,
    staleTime: 30_000,
    retry: false,
  });

  const roster = useMemo(
    () => pairMembers(members.data?.data ?? [], people.data?.data ?? [], viewerUserId),
    [members.data, people.data, viewerUserId],
  );
  // The roster is only as ready as its slowest half: a row without its person
  // would show no relationship and no colour, which reads as "we don't know"
  // rather than "still loading".
  const isLoading = members.isLoading || people.isLoading;
  return { roster, members, people, isLoading, error: members.error ?? people.error };
}

/**
 * The caller's own user id (GET /auth/me returns the user flat, not under
 * `data`). It is what makes a row "you", and the only thing that decides the
 * self half of #4821's colour rule on this page — so a failure here fails
 * towards "no row is you", which means a family_member sees no colour menu at
 * all rather than someone else's.
 */
export function useViewerId(enabled = true) {
  return useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => apiFetch<{ id: string; name?: string }>("/api/v1/auth/me"),
    enabled,
    staleTime: 10 * 60_000,
    retry: false,
  });
}

/** Pending invitations. Managers only: the API answers 403 to everyone else. */
export function useInvites(enabled: boolean) {
  return useQuery({
    queryKey: memberKeys.invites(),
    queryFn: () => apiFetch<{ data: InviteRow[] }>("/api/v1/invites"),
    enabled,
    staleTime: 30_000,
    retry: false,
  });
}

/**
 * The family's vendor contacts, for linking a vendor login to its company.
 *
 * kind=service_provider is explicit (#5529): a vendor login belongs to a
 * company that does work for the family, never to a utility that is only a
 * bill payee.
 */
export function useVendors(enabled: boolean) {
  return useQuery({
    queryKey: memberKeys.vendors(),
    queryFn: () =>
      apiFetch<{ data: VendorRow[] }>("/api/v1/vendors", {
        params: { kind: "service_provider" },
      }),
    enabled,
    staleTime: 5 * 60_000,
    retry: false,
  });
}

// ── Writes ───────────────────────────────────────────────────────────

export function useSetColor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ personId, color }: { personId: string; color: string }) =>
      apiFetch(`/api/v1/people/${personId}`, { method: "PATCH", body: { color } }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: personKeys.all });
      qc.invalidateQueries({ queryKey: memberKeys.roster() });
    },
  });
}

export function useChangeRole() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ userId, role, vendorId }: { userId: string; role: string; vendorId?: string | null }) =>
      apiFetch(`/api/v1/households/me/members/${userId}`, {
        method: "PATCH",
        // vendor_id goes only with the vendor role; the API clears the link by
        // itself when the role moves away from vendor (repo.UpdateMember).
        body: role === "vendor" ? { role, vendor_id: vendorId ?? null } : { role },
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: memberKeys.roster() });
      qc.invalidateQueries({ queryKey: ["me", "households"] });
      qc.invalidateQueries({ queryKey: memberKeys.invites() });
    },
  });
}

export function useRemoveMember() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) =>
      apiFetch(`/api/v1/households/me/members/${userId}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: memberKeys.roster() });
      qc.invalidateQueries({ queryKey: memberKeys.invites() });
    },
  });
}

export function useCreateInvite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { email: string; role: string; vendor_id?: string | null }) =>
      apiFetch<{ data: { accept_url?: string } }>("/api/v1/invites", { method: "POST", body }),
    onSuccess: () => qc.invalidateQueries({ queryKey: memberKeys.invites() }),
  });
}

export function useRevokeInvite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch(`/api/v1/invites/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: memberKeys.invites() }),
  });
}

