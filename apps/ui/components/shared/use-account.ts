"use client";

/**
 * Who is signed in, for every surface that shows it: the account menu's
 * identity header (#5198), the desktop sidebar's user button, the drawer foot
 * and the More sheet's account row.
 *
 * WHY THIS READS THE API AND NOT JUST THE STORE. Nothing fills the zustand
 * profile any more: the token-accepting POST that used to was removed with the
 * BFF migration (#4622), so `user` is null on a fresh sign-in and every host
 * that read it rendered the placeholder "Account" / "U". The Settings index and
 * the Members page hit the same wall and read GET /auth/me instead
 * (`lib/today/api.ts` useMe, `lib/members/api.ts` useViewerId).
 *
 * This is that query with the one shape every host needs, and it is deliberately
 * READ-ONLY: it never writes the profile back into the store. Persisting it from
 * here would undo the logout purge in #4648 — the query cache still holds the
 * profile after `setUser(null)`, so an effect that re-hydrates would write the
 * previous person's name and email back into localStorage on a shared device.
 *
 * `["auth","me"]` is the key the Settings index and Members already use, with
 * the same 10 minute staleTime, so the shell adds no extra request.
 */

import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { useAuthStore, type User } from "@/stores/auth";

/** GET /auth/me's body: the user flat, as the Go handler sends it. */
interface Me {
  id: string;
  name?: string;
  email?: string;
  avatar_url?: string | null;
}

/**
 * The body, whichever way it is delivered: the API returns the user flat, and
 * the e2e stub answers inside the usual `{data: …}` envelope.
 */
function readMe(body: Me | { data?: Me } | undefined): Me | null {
  if (!body) return null;
  if (typeof body === "object" && "data" in body) return body.data ?? null;
  return body as Me;
}

export interface AccountIdentity {
  /** The profile, or null while it is still unknown. */
  user: User | null;
  /** What a heading says: the name, else "Account". */
  name: string;
  /** Up to two letters for the avatar. */
  initials: string;
  isLoading: boolean;
}

export function useAccountIdentity(): AccountIdentity {
  // A profile persisted by an older build is still good enough to draw, and
  // saves the request.
  const stored = useAuthStore((s) => s.user);
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => apiFetch<Me | { data?: Me }>("/api/v1/auth/me"),
    staleTime: 10 * 60_000,
    retry: false,
    enabled: !stored,
  });

  const fetched = readMe(me.data);
  const user: User | null =
    stored ??
    (fetched
      ? {
          id: fetched.id,
          name: fetched.name ?? "",
          email: fetched.email ?? "",
          avatar_url: fetched.avatar_url ?? null,
        }
      : null);

  const name = user?.name || "Account";
  const initials =
    (user?.name || user?.email || "U")
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((p) => p[0]!.toUpperCase())
      .join("") || "U";

  return { user, name, initials, isLoading: !stored && me.isLoading };
}
