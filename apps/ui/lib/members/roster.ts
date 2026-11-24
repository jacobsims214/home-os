/**
 * The pure half of the Members page's data layer (#4559): the members × people
 * join and the error messages a person reads.
 *
 * Split out of lib/members/api.ts because api.ts imports TanStack Query and the
 * app's fetch client, which `node --test` cannot load (no path aliases, no
 * React). Everything here is data in, data out, so lib/members/roster.test.mjs
 * covers it with plain imports.
 */

// ── Shapes, exactly as the API answers them ──────────────────────────

/** GET /api/v1/households/me/members (apps/api/internal/household/repo.go). */
export interface MemberRow {
  user_id: string;
  email: string;
  name: string;
  role: string;
  /** memberships.created_at, RFC 3339. The roster's "joined" line. */
  joined_at: string;
  /** role vendor only: the family vendors row the login works for (#4799). */
  vendor_id: string | null;
  vendor_name: string | null;
  /** people.color (#4821): one of the eight palette names, null if no person row. */
  color: string | null;
}

/** GET /api/v1/people (apps/api/internal/people/handler.go personResponse). */
export interface PersonRow {
  id: string;
  display_name: string;
  relationship: string | null;
  allergies: string[] | null;
  /**
   * Null for a housesitter: `view()` redacts it along with birthdate, school,
   * grade and notes (read_basic). The page never renders what it does not get.
   */
  user_id: string | null;
  color: string;
}

// ── The roster: members joined with their person row ─────────────────

/**
 * One household member as the page needs them: the membership plus the person
 * row that carries relationship, allergies and the colour.
 *
 * Members and people are two tables, and the API exposes them as two lists. The
 * join key is `people.user_id = memberships.user_id` (migration 041 creates one
 * person per membership, and memberships.plainly has a 1:1 person). Doing the
 * join here rather than in SQL keeps the page on two real, policy-checked
 * endpoints: /people already applies read_basic for a housesitter, so a sitter
 * simply gets people rows with the fields they may see.
 */
export interface RosterMember extends MemberRow {
  /** The person row's id, for PATCH /people/{id}; null when there is none. */
  person_id: string | null;
  relationship: string | null;
  allergies: string[];
  /** The palette colour from people.color; `member.color` is the same value. */
  color: string | null;
  is_self: boolean;
}

export function pairMembers(
  members: readonly MemberRow[],
  people: readonly PersonRow[],
  viewerUserId: string | null,
): RosterMember[] {
  const byUser = new Map<string, PersonRow>();
  const byName = new Map<string, PersonRow | null>();
  for (const p of people) {
    if (p.user_id) byUser.set(p.user_id, p);
    // Second index for the housesitter case below: only UNIQUE names get an
    // entry, so two people called "Amber Sims" pair with nobody rather than
    // with each other's allergies.
    const key = p.display_name.trim().toLowerCase();
    byName.set(key, byName.has(key) ? null : p);
  }
  return members.map((m) => {
    // A housesitter gets read_basic from /people, and `view()` nulls user_id
    // along with birthdate/school/grade/notes, so the person cannot be joined
    // by login there. The name is the only key left, and the fields this page
    // renders for a sitter (relationship, allergies) are exactly the ones
    // read_basic keeps — so match on a unique display_name instead of showing a
    // sitter a roster with no relationships at all. An ambiguous name pairs
    // with nobody: a wrong allergy is worse than a missing one.
    const p = byUser.get(m.user_id) ?? byName.get(m.name.trim().toLowerCase()) ?? null;
    return {
      ...m,
      person_id: p?.id ?? null,
      relationship: p?.relationship ?? null,
      allergies: Array.isArray(p?.allergies) ? p!.allergies : [],
      color: m.color ?? p?.color ?? null,
      is_self: !!viewerUserId && m.user_id === viewerUserId,
    };
  });
}

/** How many owners the roster has, for the last-owner rule (ErrLastOwner → 409). */
export function ownerCount(members: readonly { role: string }[]): number {
  return members.filter((m) => m.role === "owner").length;
}

/** "in 6 days" / "tomorrow" / "expired Nov 9" for an invitation row. */
export function inviteExpiry(expiresAt: string, now: number = Date.now()): { text: string; soon: boolean } {
  const at = new Date(expiresAt).getTime();
  if (Number.isNaN(at)) return { text: "", soon: false };
  const days = Math.ceil((at - now) / 86_400_000);
  if (days < 0) {
    // UTC: the invite's instant, shown as a fixed-zone date so it does not
    // change shape with the reader's device (#5316).
    const when = new Date(at).toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
    return { text: `expired ${when}`, soon: false };
  }
  if (days === 0) return { text: "expires today", soon: true };
  if (days === 1) return { text: "expires tomorrow", soon: true };
  return { text: `expires in ${days} days`, soon: days <= 2 };
}

// ── Errors, as a person reads them ───────────────────────────────────

export interface WriteError {
  title: string;
  detail: string;
}

/**
 * Turns an ApiError into the message the dialog shows. The API's own words are
 * used where they exist (apierr puts them in `error.message`), and the 403/409
 * cases the member routes actually answer (#4683, #4688) get a sentence that
 * says what to do next:
 *
 *   403  a manager-only act was attempted (a race: someone was demoted while
 *        the page was open, or a stale tab).
 *   404  the person is no longer a member (another manager removed them).
 *   409  the household would lose its last owner, or the invitation was
 *        already accepted so there is nothing to revoke.
 */
export function writeError(err: unknown, fallback: string): WriteError {
  const e = err as { status?: number; body?: { error?: { message?: string } }; message?: string; offline?: boolean };
  // The API's own words, when this really is an API response. A plain thrown
  // Error (a bug inside a mutationFn) has no status and gets OUR sentence: its
  // message is a developer's note, not something to show a parent.
  const apiMessage = typeof e?.status === "number" ? e.body?.error?.message?.trim() || "" : "";
  if (e?.offline) {
    return { title: "Not saved", detail: "You're offline. Nothing was changed; try again when you're back." };
  }
  switch (e?.status) {
    case 403:
      return {
        title: "You can't do that",
        detail:
          apiMessage ||
          "Only an owner or family manager can change the family, and only an owner can change an owner. Ask one of them.",
      };
    case 404:
      return { title: "They're not in the family any more", detail: apiMessage || "Another manager may have just removed or changed this person. Reload to see the current list." };
    case 409:
      return { title: "Not possible right now", detail: apiMessage || fallback };
    case 400:
      return { title: "That wasn't accepted", detail: apiMessage || fallback };
    default:
      return { title: "Something went wrong", detail: apiMessage || fallback };
  }
}
