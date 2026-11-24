/**
 * When household-scoped data may read, and which failures may be retried (#5307).
 *
 * The bug: a fresh sign-in → straight to /dashboard/calendar intermittently
 * showed "Couldn't load the calendar". Two things made one transient blip into
 * a permanent error card:
 *
 *   1. It read before it knew WHICH household it was in. `today` was set on
 *      mount from the BROWSER's clock with `timezone` still null, and that
 *      alone enabled all five calendar reads — while GET /me/households was
 *      still in flight. The auth callback clears the `home-os-household`
 *      cookie on every fresh sign-in (#4574), so those first requests carried
 *      no X-Household-ID and the API had to resolve a membership for them
 *      itself, on the one request that had no household to resolve against.
 *   2. It was the only surface in the app with `retry: false`: the global
 *      QueryClient (app/providers.tsx) leaves React Query's default three
 *      retries on, so every other page rode out the same first-load blip. The
 *      calendar turned it into an error card.
 *
 * The fix is not "wait a bit and try again". It is: do not ask until the
 * precondition exists (the household list has answered, so the read is scoped
 * to the family the person is actually in), and when a read does fail, retry
 * ONLY the failures that mean "the session wasn't ready yet" — never a role
 * refusal, a missing record, a server fault, or an offline device, which are
 * all answers, not races.
 */

import { ApiError } from "@/lib/api";

/** The household the page reads in, or nothing while it isn't known. */
export interface HouseholdReadiness {
  /** True once GET /me/households has ANSWERED — the household and its zone are known. */
  resolved: boolean;
  /** True if that read itself failed, so waiting longer would never end. */
  failed: boolean;
}

/**
 * Whether the calendar's reads may fire.
 *
 * `resolved` is the real precondition: the household list has answered, so the
 * family (and its timezone) is known and the read runs once, scoped to the
 * right family — not once against a guessed scope and again against the real
 * one.
 *
 * `failed` is the escape hatch: a broken /me/households must not trap the page
 * on a skeleton forever. The page loads anyway and says so, which is the same
 * rule the calendar already follows for a failed read (never an empty week).
 */
export function calendarReadsReady({ resolved, failed }: HouseholdReadiness): boolean {
  return resolved || failed;
}

/**
 * Whether to retry a household-scoped read after `failureCount` failures.
 *
 * Used by the calendar's five reads AND by the household-list query they wait
 * on: a precondition query with `retry: false` would answer "failed" on the
 * same transient and open the gate onto the same broken session, which is the
 * bug wearing a hat.
 *
 * Only the transient session race is retried, and only once:
 *
 *   - 401 — the BFF saw no usable session yet ("no session"). Right after a
 *     sign-in this is the cookie not being in place yet, not a signed-out
 *     person; lib/api.ts's redirect to /login is the backstop.
 *   - 503 — the BFF's AUTH_UNAVAILABLE: the id token was gone and Dex was
 *     unreachable for the refresh. Transient by definition.
 *
 * Everything else is an answer to keep: 403 is the role/household policy
 * (retrying a housesitter's read gets the same 403), 404 and 500 are what they
 * say, and an offline device (ApiError.offline, status 0) must never be
 * retried — the offline copy is the honest answer and the request would fail
 * the same way (lib/pwa/offline.js).
 */
export function shouldRetrySessionRead(failureCount: number, error: unknown): boolean {
  if (failureCount >= 1) return false;
  // A code that names the session, not a status: apiFetch only sets `status`
  // on ApiError, so anything else (a TypeError from a bug) is not a race.
  if (!(error instanceof ApiError)) return false;
  if (error.offline) return false;
  return error.status === 401 || error.status === 503;
}