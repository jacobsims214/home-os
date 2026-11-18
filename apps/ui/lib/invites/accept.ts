/**
 * The pure half of the invite accept page (#4579): what a link means, what the
 * API's answers mean, and what a person reads for each.
 *
 * WHY THIS IS A SEPARATE FILE. The page (app/(auth)/invites/accept/page.tsx)
 * imports React and the app's fetch client, which `node --test` cannot load.
 * Everything here is data in, data out, so lib/invites/accept.test.mjs covers
 * the whole decision table — including the messages that must never be wrong,
 * because they are the only thing a person sees when their link is dead.
 *
 * THE TOKEN IS A SECRET (architecture/api-invite-accept.md):
 *  - `invitations.token` is a random UUIDv4 and it is the credential that joins
 *    a family. The inviter sees it only inside `accept_url`.
 *  - Nothing here logs it, formats it for display, or shortens it. The page
 *    keeps it in the query string (that is how the API mints the link) and
 *    posts it once. `tokenInUrl` is the ONLY place a token becomes a string,
 *    so the page never hand-builds a URL around one.
 *  - `redactToken` is for messages only: if a token ever ends up inside a
 *    string that is shown or logged, this is what takes it out.
 *
 * WHAT THE API ANSWERS FOR POST /api/v1/invites/accept (invite/handler.go,
 * writeAcceptError). The page cannot pre-check an invitation: there is no
 * "read this invite" endpoint, and the read-only check in the API is a
 * server-side guard before bcrypt, not a route. So every state except a missing
 * token is discovered by posting.
 *
 *   200 accepted · 201 signed up
 *   400 expired · 401 no signup fields and no bearer
 *   403 email differs · 403 account deactivated
 *   404 unknown/revoked/deleted · 409 already accepted · 409 already a member
 *   409 an account with this email exists (signup) → sign in instead
 *   413 body too large · 429 rate limited · 502/503 Dex trouble
 */

// ── The link ─────────────────────────────────────────────────────────

/** The query parameter the API's accept_url uses. */
export const TOKEN_PARAM = "token";

/**
 * The token from a location search string, trimmed, or "" when the link has
 * none. Decoded exactly once (URLSearchParams already did) and never validated
 * against a shape: the API decides what a valid token is, and a malformed one
 * must reach it so it gets the same 404 as an unknown one.
 */
export function readInviteToken(search: string | null | undefined): string {
  if (!search) return "";
  const token = new URLSearchParams(search).get(TOKEN_PARAM) ?? "";
  return token.trim();
}

/** `/invites/accept?token=<token>` — the path the API's accept_url points at. */
export function tokenInUrl(token: string): string {
  return `/invites/accept?${TOKEN_PARAM}=${encodeURIComponent(token)}`;
}

/**
 * Where "Sign in to accept" sends someone who already has an account: the login
 * page, told to come back to this invite. `/login` hands the value to
 * /api/auth/login and the callback re-checks it (lib/auth/return-path.ts), so
 * signing in costs no token: the query survives the Dex round trip and the
 * person lands back on this page, signed in, with the same link.
 */
export function inviteLoginHref(token: string): string {
  return `/login?redirect=${encodeURIComponent(tokenInUrl(token))}`;
}

/**
 * A string with any invite token in it removed: for messages that could carry
 * one (a fetch error that echoes the URL, an upstream body). Everything else is
 * left alone so the message still says something true.
 */
export function redactToken(text: string, token: string): string {
  if (!token) return text;
  return text.split(token).join("[invite token]");
}

// ── What the API's answer means ──────────────────────────────────────

/**
 * The one action a state offers:
 * - "signin": this person already has an account; sign in and come back.
 * - "signup": retry the form (a transient failure; nothing was written).
 * - "signout": the session is the wrong person; sign out and reopen the link.
 * - "home": nothing left to do here (already a member).
 * - "none": no action (a dead link an invitation can only fix).
 */
export type InviteAction = "signin" | "signup" | "signout" | "home" | "none";

export type InviteErrorKind =
  | "expired"
  | "used"
  | "unknown"
  | "already-member"
  | "account-exists"
  | "email-mismatch"
  | "deactivated"
  | "not-authenticated"
  | "rate-limited"
  | "unavailable"
  | "other";

export interface InviteErrorCopy {
  kind: InviteErrorKind;
  /** Short heading: the page's h1 beside the alert icon. */
  title: string;
  /** One or two sentences: what happened and what to do about it. */
  detail: string;
  action: InviteAction;
  /** True for the states where the link itself is finished (expired/used/unknown). */
  dead: boolean;
}

/** The API's error body, as much of it as this page reads. */
export interface ApiErrorBody {
  error?: { code?: string; message?: string };
}

function messageOf(body: unknown): string {
  if (!body || typeof body !== "object") return "";
  const error = (body as ApiErrorBody).error;
  return typeof error?.message === "string" ? error.message : "";
}

// The exact strings in invite/handler.go writeAcceptError. Matched by substring
// so a change in capitalisation or a wrapped error still lands on the right
// copy, and so the page never parses a status code into a story it invented.
const ACCOUNT_EXISTS = "an account with this email already exists";
const ALREADY_MEMBER = "already a member";
const ALREADY_ACCEPTED = "invitation already accepted";
const EXPIRED = "invitation has expired";
const EMAIL_MISMATCH = "different email address";
const DEACTIVATED = "deactivated";

/**
 * What to show for a failed accept. Ordered by what the person can DO about it:
 * a wrong-account answer is a session problem, a dead link is the inviter's
 * problem, and a transient one is worth a retry.
 */
export function describeAcceptError(status: number, body: unknown): InviteErrorCopy {
  const message = messageOf(body).toLowerCase();

  // Signup mode's "the account already exists" is a 409 like an accepted
  // invite. The difference matters: one means "sign in", the other means the
  // link is spent.
  if (status === 409 && message.includes(ACCOUNT_EXISTS)) {
    return {
      kind: "account-exists",
      title: "You already have an account",
      detail:
        "This invite is for an address that already signs in to Home OS. Sign in with it, then accept: the link still works.",
      action: "signin",
      dead: false,
    };
  }
  if (status === 409 && message.includes(ALREADY_ACCEPTED)) {
    return {
      kind: "used",
      title: "This invite has already been used",
      detail:
        "Someone accepted it, so the link is spent. If that wasn't you, ask the person who invited you for a new one.",
      action: "signin",
      dead: true,
    };
  }
  if (status === 409 && message.includes(ALREADY_MEMBER)) {
    return {
      kind: "already-member",
      title: "You're already in this family",
      detail: "Nothing to accept: this account is already a member of the family the invite was for.",
      action: "home",
      dead: false,
    };
  }
  if (status === 400 && message.includes(EXPIRED)) {
    return {
      kind: "expired",
      title: "This invite has expired",
      detail:
        "Invite links work for a few days and this one is past that. Ask the person who invited you for a new link.",
      action: "none",
      dead: true,
    };
  }
  // 404 is deliberately the same body for unknown, revoked and deleted tokens,
  // so the page cannot tell them apart — and must not guess which it was.
  if (status === 404) {
    return {
      kind: "unknown",
      title: "This invite link doesn't work anymore",
      detail:
        "It may have been cancelled by the person who sent it, or it was never a real link. Ask them for a new one.",
      action: "none",
      dead: true,
    };
  }
  if (status === 403 && message.includes(EMAIL_MISMATCH)) {
    return {
      kind: "email-mismatch",
      title: "This invite is for a different address",
      detail:
        "The link was sent to another email, and Home OS won't join an account it wasn't meant for. Sign out, then open the link again signed in as the right person — it keeps working until it expires.",
      action: "signout",
      dead: false,
    };
  }
  if (status === 403 && message.includes(DEACTIVATED)) {
    return {
      kind: "deactivated",
      title: "This account has been deactivated",
      detail:
        "An instance admin turned this account off, so it can't join a family. Ask them to reactivate it.",
      action: "none",
      dead: false,
    };
  }
  if (status === 401) {
    return {
      kind: "not-authenticated",
      title: "Sign in again to accept",
      detail: "The session ended before the invite could be accepted. Sign in and the link will join you to the family.",
      action: "signin",
      dead: false,
    };
  }
  if (status === 429) {
    return {
      kind: "rate-limited",
      title: "Too many tries",
      detail: "Home OS is refusing new attempts for a minute to slow down guessing. Wait a minute, then try again.",
      action: "signup",
      dead: false,
    };
  }
  if (status === 502 || status === 503) {
    return {
      kind: "unavailable",
      title: "Sign-in isn't available right now",
      detail:
        "The place that stores Home OS passwords didn't answer, so nothing was created. Try again in a moment.",
      action: "signup",
      dead: false,
    };
  }
  return {
    kind: "other",
    title: "That didn't work",
    detail: "Home OS couldn't accept the invite. Nothing was changed. Try again, or ask the person who invited you.",
    action: "signup",
    dead: false,
  };
}

/** The card for a URL with no token at all. */
export const MISSING_TOKEN: InviteErrorCopy = {
  kind: "unknown",
  title: "This invite link is incomplete",
  detail:
    "The link is missing the part that identifies the invite, which usually means it was cut short when it was copied. Ask the person who invited you to send it again.",
  action: "none",
  dead: true,
};

/**
 * The signed-in person's email from GET /auth/me. The Go handler answers the
 * user flat (`{"id","email",…}`, auth/handler.go userResponse); the e2e stub
 * wraps it in `data`. Both are read here so the page never renders an email it
 * had in hand and dropped.
 */
export function readSessionEmail(body: unknown): string {
  if (!body || typeof body !== "object") return "";
  const flat = body as { email?: unknown; data?: { email?: unknown } };
  if (typeof flat.email === "string") return flat.email;
  if (flat.data && typeof flat.data.email === "string") return flat.data.email;
  return "";
}

// ── What success means ───────────────────────────────────────────────

export interface AcceptSuccess {
  /** The role the invitation granted, as the API named it. */
  role: string;
  /** True for 201: a Home OS account was created and needs a first sign-in. */
  userCreated: boolean;
  email: string;
}

/** Reads POST /invites/accept's body: {data:{role,user_created,email,…}}. */
export function readAcceptSuccess(body: unknown): AcceptSuccess {
  const data =
    body && typeof body === "object" && "data" in body
      ? ((body as { data?: Record<string, unknown> }).data ?? {})
      : {};
  return {
    role: typeof data.role === "string" ? data.role : "",
    userCreated: data.user_created === true,
    email: typeof data.email === "string" ? data.email : "",
  };
}

/**
 * What to say after a successful accept. Signup mode is 201: the account and
 * the Dex password exist, so the next step is a first sign-in. Accept mode is
 * 200: the session already exists, so it is straight into the app.
 */
export function acceptedCopy(success: AcceptSuccess): { title: string; detail: string } {
  if (success.userCreated) {
    return {
      title: "You're in",
      detail: success.email
        ? `Your account is ready and you've joined the family. Sign in with ${success.email} and the password you just chose.`
        : "Your account is ready and you've joined the family. Sign in with the email the invite was sent to.",
    };
  }
  return {
    title: "You're in",
    detail: "You've joined the family. Everything your role can see is in the app now.",
  };
}
