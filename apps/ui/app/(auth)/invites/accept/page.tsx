"use client";

import { Suspense, useEffect, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Button, PasswordInput, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { apiFetch, ApiError } from "@/lib/api";
import {
  MISSING_TOKEN,
  acceptedCopy,
  describeAcceptError,
  inviteLoginHref,
  readAcceptSuccess,
  readInviteToken,
  readSessionEmail,
  redactToken,
  type AcceptSuccess,
  type InviteErrorCopy,
} from "@/lib/invites/accept";

/**
 * /invites/accept?token=… — where an invitation link lands (#4579), built to
 * the approved settings mockup (artifact #103 v1, "standalone screens: auth"
 * → invite: new / signed in / dead link) inside the (auth) frame.
 *
 * WHY THIS PATH. The API mints `accept_url` as PUBLIC_URL + "/invites/accept"
 * (invite/handler.go acceptURL, architecture/api-invite-accept.md), so this is
 * the page that link must open. middleware.ts lists "/invites" as public; a
 * signed-out visitor must reach it without being bounced to /login, or the
 * token is lost.
 *
 * THE TWO MODES ARE THE API's, NOT THIS PAGE'S (invite/handler.go AcceptInvite):
 *
 *  - Signed out, the person is new: POST /api/v1/invites/accept with
 *    {token,name,password} creates the Home OS user, the membership and the Dex
 *    password in one transaction (mode B, 201). Sending it to the API's own
 *    /api/v1 route — not /bff — matters: the BFF answers 401 without a session,
 *    and this route is registered outside RequireAuth for exactly this person.
 *    apiFetch's PUBLIC_API_PATHS carries that exception (lib/api.ts).
 *
 *  - Signed in, the person already has an account: POST /bff/invites/accept
 *    with {token} (mode A, 200). The BFF attaches the session as a Bearer, and
 *    the API requires the token's email to match the invitation's. Going
 *    through the BFF is deliberate: it is the only place that can turn the
 *    httpOnly cookie into an Authorization header.
 *
 * THIS PAGE PRESENTS AND WIRES; IT DOES NOT DECIDE. Expiry, single use, the
 * strict email match, role and scopes all stay in the API. Nothing here
 * pre-validates a token (there is no endpoint to read one), so an expired, used
 * or cancelled link is discovered by the POST and shown as the mockup's dead
 * link — never as a broken page.
 *
 * THE TOKEN IS A SECRET. It stays in the query string (that is how the API
 * mints the link), is posted once, is never logged, never shortened for
 * display and never put in a URL fragment or storage. When it has to appear
 * inside a message, `redactToken` takes it out first.
 *
 * DIFFERENCES FROM THE MOCKUP, all because the API has no "read this invite"
 * call: the family name, the invited email and the role are not known until
 * the invite is accepted, so the "Amber invited you to the Sims family / as a
 * housesitter" header is not drawn — the page says what it does know and the
 * role appears in the success card, from the API's own answer. The signup form
 * asks for the password twice (the mockup shows one field): a typo there would
 * spend a single-use invite on an account whose password nobody knows.
 *
 * THE EMAIL FIELD IS NOT SHOWN READ-ONLY either, for the same reason: the page
 * cannot know the invited address, so it tells the person to use the one the
 * invite came to — which is the rule the API enforces (403 on a mismatch).
 */

// The mockup's auth type scale (`.acard h1`, `.acard .sub`), as in
// app/(auth)/reset-password/page.tsx, so the standalone screens agree.
const H1 = "m-0 text-[24px] font-semibold leading-tight tracking-[-0.015em] text-ink";
const SUB = "mb-5 mt-1.5 text-[15px] leading-normal text-ink-2";
const ACTION = "min-h-12 text-[15px]";
const FIELD = { input: { height: 48, fontSize: 16 } } as const;

function AlertIcon() {
  return (
    <span aria-hidden className="mb-4 grid size-14 place-items-center rounded-token-lg bg-warn-bg text-warn-ink">
      <svg
        width={28}
        height={28}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={1.75}
        strokeLinecap="round"
        strokeLinejoin="round"
        focusable="false"
      >
        <path d="M12 9v4M12 17v.01" />
        <path d="M10.3 4.2 2.6 17.5A2 2 0 0 0 4.3 20.5h15.4a2 2 0 0 0 1.7-3L13.7 4.2a2 2 0 0 0-3.4 0z" />
      </svg>
    </span>
  );
}

function DoneIcon() {
  return (
    <span aria-hidden className="mb-4 grid size-14 place-items-center rounded-token-lg bg-accent-soft text-accent">
      <svg
        width={28}
        height={28}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={1.75}
        strokeLinecap="round"
        strokeLinejoin="round"
        focusable="false"
      >
        <path d="m5 12 5 5L20 7" />
      </svg>
    </span>
  );
}

/** The invite-link mark for the dead-link card (the mockup's #i-link art). */
function LinkIcon() {
  return (
    <span aria-hidden className="mb-4 grid size-14 place-items-center rounded-token-lg bg-warn-bg text-warn-ink">
      <svg
        width={28}
        height={28}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={1.75}
        strokeLinecap="round"
        strokeLinejoin="round"
        focusable="false"
      >
        <path d="M9 15l6-6M11 6l.5-.5a5 5 0 0 1 7 7l-.5.5M13 18l-.4.5a5 5 0 0 1-7-7l.4-.5" />
      </svg>
    </span>
  );
}

/**
 * The page's whole state. "checking" is the moment between mounting and knowing
 * whether there is a session: rendering the signup form then would flash a
 * "create an account" screen at someone who already has one.
 */
type Phase =
  | { kind: "checking" }
  | { kind: "signed-out" }
  | { kind: "signed-in"; email: string }
  | { kind: "accepted"; success: AcceptSuccess }
  | { kind: "problem"; copy: InviteErrorCopy };

type Session = { state: "signed-in"; email: string } | { state: "signed-out" };

/**
 * Who is asking? GET /bff/auth/me is the only honest answer: the session lives
 * in an httpOnly cookie, so script cannot read it. A 401 means signed out (and
 * `redirectOn401: false` stops apiFetch from sending them to /login — this page
 * is public and a visitor with no session belongs on the form, not the login
 * page). Any other failure is treated as signed out too: the signup form is
 * still useful for a new person, and an existing one gets the API's 409, which
 * turns into "sign in to accept".
 */
async function whoIsAsking(): Promise<Session> {
  try {
    const me = await apiFetch<unknown>("/api/v1/auth/me", { redirectOn401: false });
    return { state: "signed-in", email: readSessionEmail(me) };
  } catch {
    return { state: "signed-out" };
  }
}

function InviteAccept() {
  const searchParams = useSearchParams();
  // Read once from the URL, exactly as the API wrote it. Never re-encoded by
  // hand: tokenInUrl/inviteLoginHref (lib/invites/accept.ts) own that.
  const token = readInviteToken(searchParams?.toString() ? `?${searchParams.toString()}` : "");
  const [phase, setPhase] = useState<Phase>({ kind: "checking" });
  const [busy, setBusy] = useState(false);
  const [signOutFailed, setSignOutFailed] = useState(false);

  const form = useForm({
    initialValues: { name: "", password: "", confirm: "" },
    validate: {
      name: (value) => (value.trim() ? null : "Tell us your name so the family knows who joined."),
      password: (value) =>
        value.length < 8 ? "At least 8 characters. A short phrase is easier to remember." : null,
      confirm: (value, values) => (value !== values.password ? "These don't match yet. Check the second one." : null),
    },
  });

  // Ask once, on mount. A missing token is settled without asking: it is a dead
  // link whatever the session is.
  useEffect(() => {
    if (!token) {
      setPhase({ kind: "problem", copy: MISSING_TOKEN });
      return;
    }
    let cancelled = false;
    void whoIsAsking().then((session) => {
      if (cancelled) return;
      setPhase(
        session.state === "signed-in"
          ? { kind: "signed-in", email: session.email }
          : { kind: "signed-out" },
      );
    });
    return () => {
      cancelled = true;
    };
  }, [token]);

  /** Shows a failure without ever putting the token on screen. */
  function fail(err: unknown) {
    if (err instanceof ApiError) {
      // A network failure has no body: say nothing was sent, in the API's terms.
      const copy =
        err.status === 0
          ? {
              kind: "unavailable" as const,
              title: "You're offline",
              detail: "Home OS couldn't be reached, so nothing was sent and nothing was created. Check your connection and try again.",
              action: "signup" as const,
              dead: false,
            }
          : describeAcceptError(err.status, err.body);
      setPhase({ kind: "problem", copy });
      return;
    }
    const message = err instanceof Error ? redactToken(err.message, token) : "";
    // Not an ApiError: describeAcceptError leaves an unknown status on its
    // "that didn't work" copy, which is the right thing to show for anything
    // this page did not expect.
    setPhase({ kind: "problem", copy: describeAcceptError(0, null) });
    // The message is already redacted; it is only logged when it says something
    // the generic detail does not, never the token.
    if (message && typeof console !== "undefined") console.warn("invite accept failed:", message);
  }

  /** Mode B: a person with no account. Straight to the API (no session). */
  async function signUp(name: string, password: string) {
    setBusy(true);
    try {
      const body = await apiFetch<unknown>("/api/v1/invites/accept", {
        method: "POST",
        body: { token, name: name.trim(), password },
        redirectOn401: false,
      });
      setPhase({ kind: "accepted", success: readAcceptSuccess(body) });
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  }

  /** Mode A: a signed-in person. Through the BFF, which adds the Bearer. */
  async function joinAsSignedIn() {
    setBusy(true);
    try {
      const body = await apiFetch<unknown>("/bff/invites/accept", { method: "POST", body: { token } });
      setPhase({ kind: "accepted", success: readAcceptSuccess(body) });
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  }

  /**
   * Sign out so the link can be opened as the right person. The token survives:
   * the next page is /login?redirect=<this invite>, so the Dex round trip comes
   * back here. Done from script rather than a plain form POST because the logout
   * route lands on /login with no return path, and this invite would be lost.
   */
  async function signOut() {
    setBusy(true);
    try {
      const res = await fetch("/api/auth/logout", { method: "POST", credentials: "same-origin" });
      if (!res.ok) {
        setSignOutFailed(true);
        return;
      }
      window.location.assign(inviteLoginHref(token));
    } catch {
      setSignOutFailed(true);
    } finally {
      setBusy(false);
    }
  }

  // ── Checking the session ─────────────────────────────────────────────
  if (phase.kind === "checking") {
    return (
      <div data-auth-page="invite-accept" data-state="checking" role="status" aria-live="polite">
        <h1 className={H1}>Open your invite</h1>
        <p className={SUB}>Checking whether you&apos;re already signed in…</p>
      </div>
    );
  }

  // ── Joined ───────────────────────────────────────────────────────────
  if (phase.kind === "accepted") {
    const copy = acceptedCopy(phase.success);
    return (
      <div data-auth-page="invite-accept" data-state="accepted" role="status">
        <DoneIcon />
        <h1 className={H1}>{copy.title}</h1>
        <p className={SUB}>{copy.detail}</p>
        {/* Signup (201) has no session yet: the person signs in with the password
            they just chose. Accept (200) already had one, so it goes into the app
            rather than asking them to sign in again. */}
        {phase.success.userCreated ? (
          <Button component={Link} href="/login" size="md" fullWidth className={ACTION} data-testid="invite-signin">
            Sign in
          </Button>
        ) : (
          <Button component={Link} href="/dashboard" size="md" fullWidth className={ACTION} data-testid="invite-home">
            Go to Home OS
          </Button>
        )}
      </div>
    );
  }

  // ── A dead link, a refusal, or a transient failure ────────────────────
  if (phase.kind === "problem") {
    const { copy } = phase;
    return (
      <div data-auth-page="invite-accept" data-state={`error-${copy.kind}`} data-dead={copy.dead ? "true" : "false"}>
        {copy.dead ? <LinkIcon /> : <AlertIcon />}
        <h1 className={H1}>{copy.title}</h1>
        <p className={SUB}>{copy.detail}</p>

        {copy.action === "signin" && (
          <Button
            component={Link}
            href={inviteLoginHref(token)}
            size="md"
            fullWidth
            className={ACTION}
            data-testid="invite-signin-to-accept"
          >
            Sign in to accept
          </Button>
        )}
        {copy.action === "signup" && (
          <Button
            size="md"
            fullWidth
            className={ACTION}
            onClick={() => setPhase({ kind: "signed-out" })}
            data-testid="invite-retry"
          >
            Try again
          </Button>
        )}
        {copy.action === "signout" && (
          <>
            <Button
              size="md"
              fullWidth
              className={ACTION}
              loading={busy}
              onClick={() => void signOut()}
              data-testid="invite-sign-out"
            >
              Sign out and open the link again
            </Button>
            {signOutFailed && (
              <p role="alert" className="mb-0 mt-3 text-[13px] leading-snug text-danger">
                Home OS couldn&apos;t sign you out. Check your connection and try again.
              </p>
            )}
          </>
        )}
        {copy.action === "home" && (
          <Button component={Link} href="/dashboard" size="md" fullWidth className={ACTION} data-testid="invite-home">
            Go to Home OS
          </Button>
        )}
        {copy.action === "none" && (
          <Button
            component={Link}
            href="/login"
            variant="default"
            size="md"
            fullWidth
            className={ACTION}
            data-testid="invite-go-signin"
          >
            {token ? "Go to sign in" : "Back to sign in"}
          </Button>
        )}
        <p className="mb-0 mt-5 text-center text-sm text-ink-2">
          Wrong person? Ask whoever invited you for a new link.
        </p>
      </div>
    );
  }

  // ── Signed in: one press to join ─────────────────────────────────────
  if (phase.kind === "signed-in") {
    return (
      <div data-auth-page="invite-accept" data-state="signed-in">
        <h1 className={H1}>Join this family?</h1>
        <p className={SUB}>
          {phase.email ? (
            <>
              You&apos;re signed in as <b className="text-ink">{phase.email}</b>. Accepting adds this
              account to the family that sent the invite.
            </>
          ) : (
            <>You&apos;re signed in. Accepting adds this account to the family that sent the invite.</>
          )}
        </p>
        <Button size="md" fullWidth className={ACTION} loading={busy} onClick={() => void joinAsSignedIn()} data-testid="invite-accept">
          Accept invite
        </Button>
        <p className="mb-0 mt-5 text-center text-sm text-ink-2">
          Not you?{" "}
          <button
            type="button"
            className="font-semibold text-accent underline-offset-2 hover:underline"
            onClick={() => void signOut()}
            disabled={busy}
            data-testid="invite-switch-account"
          >
            Sign in with a different account
          </button>
        </p>
        {signOutFailed && (
          <p role="alert" className="mb-0 mt-3 text-center text-[13px] leading-snug text-danger">
            Home OS couldn&apos;t sign you out. Check your connection and try again.
          </p>
        )}
      </div>
    );
  }

  // ── Signed out: create an account (mode B) ───────────────────────────
  return (
    <div data-auth-page="invite-accept" data-state="signup">
      <h1 className={H1}>Create your account</h1>
      <p className={SUB}>
        You&apos;ve been invited to a family on Home OS. Use the email the invite was sent to: the
        link only works for that address.
      </p>

      <form
        className="flex flex-col gap-4"
        onSubmit={form.onSubmit((values) => void signUp(values.name, values.password))}
      >
        <TextInput
          label="Your name"
          description="So the family knows who joined."
          autoComplete="name"
          required
          size="md"
          inputWrapperOrder={["label", "input", "description", "error"]}
          styles={FIELD}
          data-testid="invite-name"
          {...form.getInputProps("name")}
        />
        <PasswordInput
          label="Password"
          description="At least 8 characters. A short phrase is easier to remember."
          autoComplete="new-password"
          required
          size="md"
          inputWrapperOrder={["label", "input", "description", "error"]}
          styles={FIELD}
          data-testid="invite-password"
          {...form.getInputProps("password")}
        />
        <PasswordInput
          label="Type it again"
          autoComplete="new-password"
          required
          size="md"
          inputWrapperOrder={["label", "input", "description", "error"]}
          styles={FIELD}
          data-testid="invite-confirm"
          {...form.getInputProps("confirm")}
        />
        <Button type="submit" size="md" fullWidth className={ACTION} loading={busy} data-testid="invite-create">
          Create account and join
        </Button>
      </form>

      <p className="mb-0 mt-5 text-center text-sm text-ink-2">
        Already have a Home OS account?{" "}
        <Link
          href={inviteLoginHref(token)}
          className="font-semibold text-accent underline-offset-2 hover:underline"
          data-testid="invite-have-account"
        >
          Sign in to accept
        </Link>
      </p>
    </div>
  );
}

export default function InviteAcceptPage() {
  // useSearchParams needs a Suspense boundary to prerender (Next 14), as
  // reset-password does. The token is read inside, from the URL, and nowhere
  // else.
  return (
    <Suspense
      fallback={
        <p role="status" className="m-0 text-sm text-ink-2">
          Opening your invite…
        </p>
      }
    >
      <InviteAccept />
    </Suspense>
  );
}
