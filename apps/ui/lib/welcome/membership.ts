/**
 * What /welcome should say to the person who opened it (#5409).
 *
 * The page used to assume everyone who reached it had no family, so an owner
 * who came back (Back button, a bookmark, a stale notification link) was told
 * "you're not part of a family yet" and offered a second hub. The state is a
 * fact about the caller, and it comes from the app's one household source —
 * GET /api/v1/me/households via useMyHouseholds() — never from the
 * `home-os-household` cookie: the auth callback CLEARS that cookie on every
 * sign-in (#4574), so a cached value is exactly what is unknown right after
 * signing in (the #5307 race).
 *
 * Three answers, and only one of them may show the "no family" copy:
 *
 *   checking  the list has not answered yet. Say nothing about the family.
 *   unknown   the list could not be read. That is NOT "no family": the page
 *             says so instead of guessing (KB gotchas/household-timezone-one-source.md,
 *             "an unknown household is NOT a household with no zone").
 *   member    at least one membership: send them to Today.
 *   newcomer  the list answered, empty: the wizard is the right screen.
 */
export type WelcomeState = "checking" | "unknown" | "member" | "newcomer";

export function welcomeState(input: {
  /** The query is still fetching its first answer. */
  isLoading: boolean;
  /** The query failed (after its session-read retry). */
  isError: boolean;
  /** How many families the caller belongs to, or null when the payload is not a list. */
  families: number | null;
}): WelcomeState {
  if (input.isLoading) return "checking";
  // A failed read, or a payload that is not a list, is "we don't know".
  if (input.isError || input.families === null) return "unknown";
  return input.families > 0 ? "member" : "newcomer";
}
