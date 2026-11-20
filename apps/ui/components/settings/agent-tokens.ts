/**
 * Agent token facts (#4580): what the Agent tokens section shows, and what a
 * role may ask for.
 *
 * Plain data and pure helpers only (no JSX, no React), so `npm test` covers the
 * rules in agent-tokens.test.mjs — the same split as settings-sections.ts and
 * timezone.ts. The API is the authority; this file mirrors it and never widens
 * it:
 *
 * - A token's plaintext is `hos_` + base64url(32 bytes) and is returned ONCE,
 *   from POST /api/v1/agent-tokens (KB architecture/agent-tokens.md). Every
 *   other read carries `token_prefix` — the first 12 characters (`hos_` + 8) —
 *   and nothing else. So a list can only ever draw the prefix, and nothing in
 *   this module takes or returns a full token: the secret lives in
 *   AgentTokens.tsx's component state, nowhere else (never in the query cache,
 *   never in localStorage).
 * - Scopes: `read`, `write` (apps/api/internal/agenttoken ScopeRead/ScopeWrite).
 *   ActWriteScope is rolesFamily, so a viewer may only create read-only tokens;
 *   ActCreate is rolesReaders, so housesitters and vendors create none at all.
 */

/** One row of GET /api/v1/agent-tokens (apps/api/internal/agenttoken tokenResponse). */
export interface AgentToken {
  id: string;
  name: string;
  /** "hos_" + 8 characters. Never the secret. */
  token_prefix: string;
  scopes: string[];
  last_used_at: string | null;
  expires_at: string | null;
  revoked_at: string | null;
  created_at: string;
}

/** The create response: the metadata plus the one-time plaintext. */
export interface CreatedAgentToken extends AgentToken {
  token: string;
}

export const SCOPE_READ = "read";
export const SCOPE_WRITE = "write";

/**
 * Household roles that may create a token. API: ResAgentTokens ActCreate is
 * rolesReaders = owner, family_manager, family_member AND viewer (a viewer's
 * token can only be read-scoped, see below). Only housesitter and vendor have
 * no create right at all.
 */
const CREATORS: readonly string[] = ["owner", "family_manager", "family_member", "viewer"];
/** Household roles that may ask for the write scope (API: ActWriteScope). */
const WRITERS: readonly string[] = ["owner", "family_manager", "family_member"];

/**
 * Whether a role can create a token at all. An unknown role fails closed, like
 * canManageHousehold: a control that answers 403 teaches someone to tap a dead
 * button. (settings-sections.ts also gates the section itself.)
 */
export function canCreateAgentTokens(role: string | null | undefined): boolean {
  if (role == null || role === "") return false;
  return CREATORS.includes(role);
}

/**
 * Whether a role may create a token with the write scope. A viewer may hold a
 * read-only token; the API refuses `write` with its own sentence, so the form
 * disables that choice and says why rather than letting someone find out by
 * submitting.
 */
export function canWriteAgentTokens(role: string | null | undefined): boolean {
  if (role == null || role === "") return false;
  return WRITERS.includes(role);
}

/**
 * The masked form of a token for every list and every later render: the
 * server's prefix plus an ellipsis ("hos_k3Fq9aZx…"). A prefix is not secret,
 * which is why it is all the API ever returns after creation.
 */
export function maskedToken(tokenPrefix: string): string {
  const prefix = (tokenPrefix ?? "").trim();
  if (prefix === "") return "hos_…";
  return `${prefix}…`;
}

/** "Read & write" for a token that can change data, "Read only" otherwise. */
export function scopeLabel(scopes: readonly string[] | null | undefined): string {
  return (scopes ?? []).includes(SCOPE_WRITE) ? "Read & write" : "Read only";
}

/** The scopes array to POST: `read` always, plus `write` when asked. */
export function scopesFor(write: boolean): string[] {
  return write ? [SCOPE_READ, SCOPE_WRITE] : [SCOPE_READ];
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * "Used 4 min ago" / "Used Nov 12" / "Not used yet". `now` is injectable so the
 * boundaries are testable. An unparseable or missing timestamp reads "Not used
 * yet": the token still exists, nobody has called with it.
 */
export function lastUsedLabel(iso: string | null | undefined, now: number = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(t)) return "Not used yet";
  const delta = now - t;
  if (delta < MINUTE) return "Used just now";
  if (delta < HOUR) return `Used ${Math.round(delta / MINUTE)} min ago`;
  if (delta < DAY) {
    const hours = Math.round(delta / HOUR);
    return `Used ${hours} hour${hours === 1 ? "" : "s"} ago`;
  }
  if (delta < 7 * DAY) {
    const days = Math.round(delta / DAY);
    return `Used ${days} day${days === 1 ? "" : "s"} ago`;
  }
  return `Used ${shortDate(iso as string, now)}`;
}

/** "Never expires" / "Expires Nov 30" / "Expired Nov 3". */
export function expiryLabel(iso: string | null | undefined, now: number = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(t)) return "Never expires";
  return t <= now ? `Expired ${shortDate(iso as string, now)}` : `Expires ${shortDate(iso as string, now)}`;
}

/** Whether an expiry is close enough to flag (30 days), so a token dies on time. */
export function expiresSoon(iso: string | null | undefined, now: number = Date.now()): boolean {
  const t = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(t)) return false;
  return t > now && t - now <= 30 * DAY;
}

/** "Revoked Nov 2" — the collapsed group's line for a revoked row. */
export function revokedLabel(iso: string | null | undefined, now: number = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(t)) return "Revoked";
  return `Revoked ${shortDate(iso as string, now)}`;
}

/**
 * "Nov 12", with the year once it is not this one.
 *
 * `timeZone: "UTC"` because this renders a stored CIVIL date, not a moment in
 * the reader's day: without it a token used at 23:30 UTC would read as the next
 * day for a browser west of UTC (the defect class #5316 guards). Same rule as
 * notification-model.ts's date label.
 */
function shortDate(iso: string, now: number): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const date = new Date(t);
  const sameYear = new Date(now).getUTCFullYear() === date.getUTCFullYear();
  return date.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    ...(sameYear ? {} : { year: "numeric" }),
    timeZone: "UTC",
  });
}

/** The RFC3339 instant `days` from `now`, for the create form's expiry choice. */
export function expiryFromDays(days: number | null, now: Date = new Date()): string | undefined {
  if (days == null) return undefined;
  return new Date(now.getTime() + days * DAY).toISOString();
}

/** Active tokens and revoked ones, split for the two lists (order kept). */
export function splitTokens(tokens: readonly AgentToken[]): {
  active: AgentToken[];
  revoked: AgentToken[];
} {
  const active: AgentToken[] = [];
  const revoked: AgentToken[] = [];
  for (const token of tokens) (token.revoked_at ? revoked : active).push(token);
  return { active, revoked };
}
