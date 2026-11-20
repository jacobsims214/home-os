"use client";

/**
 * /welcome — onboarding for a signed-in person who is not in a family yet
 * (#4572), built to the approved settings/onboarding mockup (artifact #103 v1,
 * "ONBOARDING" block) at phone width.
 *
 * WHAT IT DOES
 *   start -> family (name + timezone) -> people -> property -> done.
 *   Every step after the family step is skippable; the family step is not,
 *   because a family is the one thing that has to exist before the app is
 *   usable at all (every /bff read resolves a membership). "Skip" on the
 *   optional steps simply leaves that slice of the draft empty.
 *
 * DATA IS HELD CLIENT-SIDE AND SUBMITTED ONCE, AT THE END. Nothing is written
 * while the person is still typing, so abandoning the wizard at step 3 leaves
 * no half-built family behind — the failure that would otherwise strand
 * someone in a family with the wrong name and no way back. On the last action
 * the draft is submitted in one sequence:
 *
 *   1. POST /api/v1/households {name, timezone}   (KB
 *      api/household-onboarding-create-family.md — the one route registered
 *      outside RequireAuth, so it works for a verified non-member)
 *   2. remember the new id as the active family (home-os-household cookie), so
 *      the writes below and every later page resolve to THIS family
 *   3. people, then the property, then the partner invite
 *
 * The API has no batch endpoint, so "one submit" means one user action, not one
 * transaction. If an optional add fails the family still exists and is
 * complete — a family with fewer extras, not a half-built record — so the
 * wizard names exactly what did not save and lets the person carry on to Today.
 * It never reports a failed add as saved.
 *
 * There is deliberately no localStorage draft: the draft holds children's
 * names and birthdates, and a reload losing it is a smaller harm than leaving
 * that on a shared device. A reload restarts at the welcome step.
 *
 * Landing: after a successful submit the browser hard-navigates to /dashboard
 * (Today). A full navigation, not router.push, is what the family switcher
 * does too (#4574): every cached query and the BFF's household header belong
 * to the family that no longer exists, and a reload is the cheapest way to be
 * certain none of them survive.
 */

import { useCallback, useMemo, useRef, useState } from "react";
import { Button } from "@mantine/core";
import {
  IconAlertTriangle,
  IconCheck,
  IconChevronLeft,
  IconLink,
  IconPlus,
  IconShare,
  IconX,
} from "@tabler/icons-react";
import HomeMark from "@/components/shared/HomeMark";
import { apiFetch, ApiError } from "@/lib/api";
import { writeHouseholdCookie } from "@/lib/household-cookie";
import { householdToday } from "@/lib/today/agenda";
import { personCreateBody } from "@/components/person/person-facts";

const STROKE = 1.75;

const STEPS = ["start", "family", "people", "property", "done"] as const;
type Step = (typeof STEPS)[number];

/** The steps that show the progress bar; the wizard proper starts at "family". */
const PROGRESS_STEPS: Step[] = ["family", "people", "property", "done"];

type DraftPerson = { key: string; name: string; relationship: string; birthdate: string };

type Draft = {
  name: string;
  timezone: string;
  people: DraftPerson[];
  propertyName: string;
  propertyAddress: string;
  propertyType: string;
  inviteEmail: string;
};

/** The browser's own zone, e.g. "America/Chicago"; UTC if it will not say. */
function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/**
 * The zones the picker offers. `Intl.supportedValuesOf` is the complete, honest
 * list and needs no bundle weight; where it is missing the picker falls back to
 * the browser's zone alone rather than a hand-written list that would be wrong
 * for someone.
 */
function zoneOptions(current: string): string[] {
  const supported = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] })
    .supportedValuesOf;
  let zones: string[] = [];
  try {
    zones = supported ? supported("timeZone") : [];
  } catch {
    zones = [];
  }
  // The device's zone is preselected and must always be selectable, even if the
  // runtime lists zones it does not include (or lists nothing).
  return Array.from(new Set([current, ...zones].filter(Boolean)));
}

/** A human label for a zone: "Central Standard Time (America/Chicago)". */
function zoneLabel(zone: string): string {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      timeZoneName: "long",
    }).formatToParts(new Date());
    const name = parts.find((p) => p.type === "timeZoneName")?.value;
    return name ? `${name} (${zone})` : zone;
  } catch {
    return zone;
  }
}

const H1 = "m-0 text-[24px] font-semibold leading-tight tracking-[-0.015em] text-ink";
const SUB = "mb-6 mt-1.5 max-w-[54ch] text-[15px] leading-normal text-ink-2";
const FIELD =
  "block min-h-11 w-full rounded-token-sm border border-line bg-surface px-3 text-base text-ink";

export default function WelcomePage() {
  const [step, setStep] = useState<Step>("start");
  const [draft, setDraft] = useState<Draft>(() => ({
    name: "",
    timezone: browserTimezone(),
    people: [],
    propertyName: "",
    propertyAddress: "",
    propertyType: "Single Family",
    inviteEmail: "",
  }));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Set after the family exists but an optional add did not save.
  const [unsaved, setUnsaved] = useState<string[]>([]);
  // The partner's one-time accept link, once the invite is minted.
  const [acceptUrl, setAcceptUrl] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const patch = useCallback((changes: Partial<Draft>) => {
    setDraft((d) => ({ ...d, ...changes }));
    setError(null);
  }, []);

  const zones = useMemo(() => zoneOptions(draft.timezone), [draft.timezone]);
  const stepIndex = STEPS.indexOf(step);

  const goTo = (next: Step) => {
    setError(null);
    setStep(next);
    // One screen per step, so start each at the top.
    if (typeof window !== "undefined") window.scrollTo(0, 0);
  };

  /**
   * The one submit: create the family, then the optional slices, then Today.
   * Each optional add is its own try/catch so one failure cannot lose the rest.
   */
  const finish = async () => {
    if (submitting) return;
    const name = draft.name.trim();
    if (!name) {
      setStep("family");
      setError("Give the family a name first. It is what everyone sees when they sign in.");
      return;
    }
    setSubmitting(true);
    setError(null);
    setUnsaved([]);

    let householdId: string;
    try {
      const res = await apiFetch<{ data: { id: string } }>("/api/v1/households", {
        method: "POST",
        body: { name, timezone: draft.timezone },
      });
      householdId = res?.data?.id ?? "";
      if (!householdId) throw new Error("The server didn't say which family it made. Try again.");
    } catch (err) {
      setSubmitting(false);
      // 409: they are already in a family (another tab, or an invite accepted
      // meanwhile). Sending them on is right; making a second family is not.
      if (err instanceof ApiError && err.status === 409) {
        setError("You're already part of a family. Taking you to Today…");
        window.location.assign("/dashboard");
        return;
      }
      setError(err instanceof Error ? err.message : "Couldn't create the family. Try again.");
      return;
    }

    // Every write below is scoped by this cookie (the BFF turns it into
    // X-Household-ID), so it is set before the first one.
    writeHouseholdCookie(householdId);

    const failed: string[] = [];

    for (const person of draft.people) {
      try {
        await apiFetch("/api/v1/people", {
          method: "POST",
          body: personCreateBody(
            {
              display_name: person.name,
              relationship: person.relationship,
              birthdate: person.birthdate,
              school: "",
              grade: "",
              notes: "",
            },
            [],
          ),
        });
      } catch {
        failed.push(person.name);
      }
    }

    if (draft.propertyName.trim()) {
      try {
        const body: Record<string, unknown> = { name: draft.propertyName.trim() };
        if (draft.propertyAddress.trim()) body.address = draft.propertyAddress.trim();
        if (draft.propertyType) body.property_type = draft.propertyType;
        await apiFetch("/api/v1/properties", { method: "POST", body });
      } catch {
        failed.push(draft.propertyName.trim());
      }
    }

    let inviteLink: string | null = null;
    const email = draft.inviteEmail.trim();
    if (email) {
      try {
        const res = await apiFetch<{ data: { accept_url?: string } }>("/api/v1/invites", {
          method: "POST",
          // family_manager, as the mockup's step 4 says: everything the owner
          // can do except the owner-only changes.
          body: { email, role: "family_manager" },
        });
        inviteLink = res?.data?.accept_url ?? null;
        if (!inviteLink) failed.push(`an invite link for ${email}`);
      } catch {
        failed.push(`an invite link for ${email}`);
      }
    }

    setSubmitting(false);

    if (failed.length > 0) {
      // The family exists. Say what did not save, plainly, and let them choose.
      setUnsaved(failed);
      if (inviteLink) setAcceptUrl(inviteLink);
      setStep("done");
      return;
    }
    if (inviteLink) {
      // Show the link before leaving: it is the only time it is handed over.
      setAcceptUrl(inviteLink);
      return;
    }
    window.location.assign("/dashboard");
  };

  const copyInvite = async () => {
    if (!acceptUrl) return;
    try {
      await navigator.clipboard.writeText(acceptUrl);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };

  const shareInvite = async () => {
    if (!acceptUrl) return;
    const data = { title: "Join our family on Home OS", url: acceptUrl };
    if (navigator.share) {
      try {
        await navigator.share(data);
        return;
      } catch {
        /* dismissed */
      }
    }
    await copyInvite();
  };

  const doneAction = () => {
    if (step !== "done") return goTo(STEPS[stepIndex + 1]);
    // Once the family exists (a link to show, or extras that failed), the only
    // remaining action is to leave for Today.
    if (acceptUrl || unsaved.length > 0) return window.location.assign("/dashboard");
    void finish();
  };

  const primaryLabel = () => {
    if (step === "family") return "Continue";
    if (step === "done") return acceptUrl || unsaved.length > 0 ? "Go to Today" : "Create family hub";
    return "Continue";
  };

  return (
    <div className="flex min-h-[100dvh] flex-col bg-bg">
      {/* The artifact's `.onb-top`: brand on the left, Log out on the right,
          then the progress bar. Sticky so the way out is always reachable. */}
      <header className="sticky top-0 z-10 border-b border-line bg-surface pt-[env(safe-area-inset-top,0px)]">
        <div className="mx-auto flex h-14 max-w-[1120px] items-center gap-3 px-4">
          <div className="flex flex-1 items-center gap-2.5">
            <HomeMark size={28} />
            <span className="text-base font-semibold leading-none tracking-[-0.01em] text-ink">
              Home OS
            </span>
          </div>
          <form method="post" action="/api/auth/logout">
            <Button
              variant="subtle"
              type="submit"
              className="max-md:min-h-11"
              styles={{ label: { color: "var(--ink-2)" } }}
            >
              Log out
            </Button>
          </form>
        </div>
        {/* Decorative: each step's own "Step N of 4" carries the position for a
            screen reader, so the bar is never the only signal. */}
        {PROGRESS_STEPS.includes(step) && (
          <div aria-hidden className="mx-auto flex max-w-[640px] gap-1.5 px-4 pb-3">
            {PROGRESS_STEPS.map((s, i) => (
              <i
                key={s}
                className={`h-1 flex-1 rounded-full ${i <= stepIndex - 1 ? "bg-accent-fill" : "bg-line"}`}
              />
            ))}
          </div>
        )}
      </header>

      <main className="mx-auto w-full max-w-[640px] flex-1 px-4 py-6">
        {/* ── Welcome ─────────────────────────────────────────────── */}
        {step === "start" && (
          <section aria-labelledby="ob-h">
            <h1 id="ob-h" className={H1}>
              Welcome to Home OS
            </h1>
            <p className={SUB}>
              You&apos;re signed in, but you&apos;re not part of a family yet. Start a hub for your
              household, or open the invite link someone sent you — it signs you straight into their
              family.
            </p>
            <div className="grid gap-3 md:grid-cols-2">
              <button
                type="button"
                onClick={() => goTo("family")}
                className="flex min-h-11 w-full items-start gap-3.5 rounded-token-md border border-line bg-surface p-4 text-left hover:border-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
              >
                <span
                  aria-hidden
                  className="grid size-11 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent"
                >
                  <HomeMark size={22} />
                </span>
                <span className="min-w-0">
                  <b className="block text-base font-semibold text-ink">Start a family hub</b>
                  <span className="mt-0.5 block text-sm text-ink-2">
                    For your household. You&apos;ll be its owner and can invite everyone else.
                  </span>
                </span>
              </button>
              <div className="flex items-start gap-3.5 rounded-token-md border border-dashed border-line p-4">
                <span
                  aria-hidden
                  className="grid size-11 shrink-0 place-items-center rounded-token-md bg-surface-2 text-ink-2"
                >
                  <IconLink size={22} stroke={STROKE} />
                </span>
                <span className="min-w-0">
                  <b className="block text-base font-semibold text-ink">I have an invite link</b>
                  <span className="mt-0.5 block text-sm text-ink-2">
                    Open the link in this browser and you&apos;ll join that family as the role they
                    chose.
                  </span>
                </span>
              </div>
            </div>
          </section>
        )}

        {/* ── 1. Family ───────────────────────────────────────────── */}
        {step === "family" && (
          <section aria-labelledby="ob-h">
            <p className="mb-1.5 text-[13px] font-semibold text-accent">Step 1 of 4</p>
            <h1 id="ob-h" className={H1}>
              Name your family hub
            </h1>
            <p className={SUB}>
              This is what everyone sees when they sign in. You can change it later.
            </p>
            <div className="mb-4">
              <label htmlFor="ob-name" className="mb-1.5 block text-[13px] font-semibold text-ink-2">
                Family name
              </label>
              <input
                id="ob-name"
                className={FIELD}
                value={draft.name}
                onChange={(e) => patch({ name: e.currentTarget.value })}
                placeholder="The Sims family"
                autoComplete="off"
                // A phone opens with the keyboard up, on the field that matters.
                autoFocus
              />
            </div>
            <div className="mb-4">
              <label htmlFor="ob-tz" className="mb-1.5 block text-[13px] font-semibold text-ink-2">
                Time zone
              </label>
              <select
                id="ob-tz"
                className={FIELD}
                value={draft.timezone}
                onChange={(e) => patch({ timezone: e.currentTarget.value })}
              >
                {zones.map((zone) => (
                  <option key={zone} value={zone}>
                    {zoneLabel(zone)}
                  </option>
                ))}
              </select>
              <p className="mt-1.5 text-xs leading-normal text-ink-3">
                Sets when &quot;today&quot;, reminders and bill due dates roll over for the whole
                family.
              </p>
            </div>
            <div className="flex gap-2.5 rounded-token-md bg-surface-2 p-3.5 text-[13px] leading-normal text-ink-2">
              <IconCheck size={18} stroke={STROKE} aria-hidden className="mt-px shrink-0 text-accent" />
              <div>
                We&apos;ll set up <b className="font-semibold text-ink">Family</b>,{" "}
                <b className="font-semibold text-ink">Bills</b> and{" "}
                <b className="font-semibold text-ink">Maintenance</b> calendars for you.
              </div>
            </div>
          </section>
        )}

        {/* ── 2. People ───────────────────────────────────────────── */}
        {step === "people" && (
          <section aria-labelledby="ob-h">
            <p className="mb-1.5 text-[13px] font-semibold text-accent">Step 2 of 4</p>
            <h1 id="ob-h" className={H1}>
              Who&apos;s in the family?
            </h1>
            <p className={SUB}>
              Add everyone, kids included, so events, allergies and school things have someone to
              belong to. This doesn&apos;t give anyone a login.
            </p>
            <PeopleEditor
              people={draft.people}
              onChange={(people) => patch({ people })}
              timezone={draft.timezone}
            />
          </section>
        )}

        {/* ── 3. Property ─────────────────────────────────────────── */}
        {step === "property" && (
          <section aria-labelledby="ob-h">
            <p className="mb-1.5 text-[13px] font-semibold text-accent">Step 3 of 4</p>
            <h1 id="ob-h" className={H1}>
              Add your home
            </h1>
            <p className={SUB}>
              Rooms, appliances and upkeep hang off it later. Add a cabin or rental the same way from
              People &amp; House.
            </p>
            <div className="mb-4">
              <label htmlFor="ob-hn" className="mb-1.5 block text-[13px] font-semibold text-ink-2">
                Name
              </label>
              <input
                id="ob-hn"
                className={FIELD}
                value={draft.propertyName}
                onChange={(e) => patch({ propertyName: e.currentTarget.value })}
                placeholder="Our house"
                autoComplete="off"
              />
            </div>
            <div className="mb-4">
              <label htmlFor="ob-ha" className="mb-1.5 block text-[13px] font-semibold text-ink-2">
                Address <span className="font-normal text-ink-3">(optional)</span>
              </label>
              <input
                id="ob-ha"
                className={FIELD}
                value={draft.propertyAddress}
                onChange={(e) => patch({ propertyAddress: e.currentTarget.value })}
                placeholder="2214 Willow Creek Dr, Naperville, IL"
                autoComplete="street-address"
              />
            </div>
            <fieldset className="m-0 border-0 p-0">
              <legend className="mb-1.5 text-[13px] font-semibold text-ink-2">Type</legend>
              <div className="grid grid-cols-2 gap-2" role="radiogroup" aria-label="Type">
                {[
                  { value: "Single Family", label: "House" },
                  { value: "Condo", label: "Apartment or condo" },
                ].map((option) => {
                  const checked = draft.propertyType === option.value;
                  return (
                    <button
                      key={option.value}
                      type="button"
                      role="radio"
                      aria-checked={checked}
                      onClick={() => patch({ propertyType: option.value })}
                      className={`flex min-h-11 items-center gap-2.5 rounded-token-md border p-3 text-left text-sm font-semibold ${
                        checked
                          ? "border-accent bg-accent-soft text-ink"
                          : "border-line bg-surface text-ink-2 hover:border-accent"
                      }`}
                    >
                      <span
                        aria-hidden
                        className={`grid size-5 shrink-0 place-items-center rounded-full border-2 ${
                          checked ? "border-accent" : "border-ink-3"
                        }`}
                      >
                        {checked && <span className="size-2.5 rounded-full bg-accent" />}
                      </span>
                      {option.label}
                    </button>
                  );
                })}
              </div>
            </fieldset>
          </section>
        )}

        {/* ── 4. Review + partner invite ──────────────────────────── */}
        {step === "done" && (
          <section aria-labelledby="ob-h">
            <p className="mb-1.5 text-[13px] font-semibold text-accent">Step 4 of 4</p>
            <h1 id="ob-h" className={H1}>
              {unsaved.length > 0 || acceptUrl
                ? `${draft.name.trim() || "Your family"} is ready`
                : `Ready to start ${draft.name.trim() || "your family"}`}
            </h1>
            <p className={SUB}>
              {acceptUrl
                ? "Send your partner this link and they can sign in too."
                : "Nothing is saved until you tap the button below. Check it over, then we'll build it."}
            </p>

            {unsaved.length > 0 && (
              <div
                role="alert"
                className="mb-4 flex gap-3 rounded-token-md border border-warn-line bg-warn-bg p-3.5 text-warn-ink"
              >
                <IconAlertTriangle size={20} stroke={STROKE} aria-hidden className="mt-px shrink-0" />
                <div className="min-w-0 text-[13px] leading-normal">
                  <b className="block font-semibold">Your family is ready — but not everything saved.</b>
                  <ul className="mb-2 mt-1 list-inside list-disc">
                    {unsaved.map((item) => (
                      <li key={item}>{item}</li>
                    ))}
                  </ul>
                  You can add these from their own pages. Nothing else was lost.
                </div>
              </div>
            )}

            {acceptUrl ? (
              <div className="mb-4 rounded-token-md border border-line bg-surface p-3.5">
                <div className="mb-2 flex items-center gap-2 text-sm font-semibold text-ink">
                  <IconCheck size={18} stroke={STROKE} aria-hidden className="text-accent" />
                  Invite link ready for {draft.inviteEmail.trim()}
                </div>
                <p className="mb-2.5 break-all rounded-token-sm bg-surface-2 px-3 py-2.5 font-mono text-[13px] text-ink">
                  {acceptUrl}
                </p>
                <div className="flex gap-2">
                  <Button
                    variant="default"
                    onClick={copyInvite}
                    leftSection={<IconLink size={16} />}
                    className="min-h-11 flex-1"
                  >
                    {copied ? "Copied" : "Copy link"}
                  </Button>
                  <Button
                    variant="default"
                    onClick={shareInvite}
                    leftSection={<IconShare size={16} />}
                    className="min-h-11 flex-1"
                  >
                    Share
                  </Button>
                </div>
                <p className="mt-2.5 text-xs leading-normal text-ink-3">
                  No email was sent. Anyone with this link can join, so send it only to them.
                </p>
              </div>
            ) : (
              <div className="mb-4 rounded-token-md border border-line bg-surface p-3.5">
                <label htmlFor="ob-ie" className="mb-1.5 block text-[13px] font-semibold text-ink-2">
                  Invite your partner <span className="font-normal text-ink-3">(optional)</span>
                </label>
                <input
                  id="ob-ie"
                  type="email"
                  inputMode="email"
                  className={FIELD}
                  value={draft.inviteEmail}
                  onChange={(e) => patch({ inviteEmail: e.currentTarget.value })}
                  placeholder="them@example.com"
                  autoComplete="email"
                />
                <p className="mt-1.5 text-xs leading-normal text-ink-3">
                  They&apos;ll get a Family manager role: everything you can do except owner-only
                  changes. You get a link to share — no email is sent for you.
                </p>
              </div>
            )}

            {/* What will exist. Each line is a fact about the draft, not a claim
                that it is already saved — the heading says "Ready". */}
            <ul className="mb-4 list-none overflow-hidden rounded-token-md border border-line bg-surface p-0">
              <SummaryRow label={draft.name.trim() || "Your family"} meta={zoneLabel(draft.timezone)} />
              {draft.people.length > 0 && (
                <SummaryRow
                  label={`${draft.people.length} ${draft.people.length === 1 ? "person" : "people"}`}
                  meta={draft.people.map((p) => p.name).join(", ")}
                />
              )}
              {draft.propertyName.trim() && (
                <SummaryRow
                  label={draft.propertyName.trim()}
                  meta={draft.propertyAddress.trim() || "Home"}
                />
              )}
              <SummaryRow label="3 calendars" meta="Family, Bills, Maintenance" />
            </ul>
          </section>
        )}

        {error && (
          <p
            id="ob-err"
            role="alert"
            className="mt-4 flex items-start gap-2 text-sm font-medium text-danger"
          >
            <IconAlertTriangle size={18} stroke={STROKE} aria-hidden className="mt-px shrink-0" />
            {error}
          </p>
        )}
      </main>

      {/* The artifact's `.onb-act`: on a phone it is pinned to the bottom with
          the safe-area inset, so the primary action never scrolls away. */}
      <div className="sticky bottom-0 border-t border-line bg-surface px-4 pb-[calc(12px+env(safe-area-inset-bottom,0px))] pt-3 md:static md:border-0 md:bg-transparent md:pb-8 md:pt-0">
        <div className="mx-auto flex max-w-[608px] items-center gap-2">
          {step === "start" ? (
            <Button fullWidth size="md" onClick={() => goTo("family")} className="min-h-12">
              Start a family hub
            </Button>
          ) : (
            <>
              <Button
                variant="default"
                onClick={() => goTo(STEPS[stepIndex - 1])}
                disabled={submitting}
                leftSection={<IconChevronLeft size={18} />}
                aria-label="Back"
                className="min-h-12"
              >
                <span className="max-md:hidden">Back</span>
              </Button>
              <span className="flex-1" />
              {/* Optional steps can be left empty; the family step cannot. */}
              {step !== "family" && step !== "done" && (
                <Button
                  variant="subtle"
                  onClick={() => goTo(STEPS[stepIndex + 1])}
                  disabled={submitting}
                  className="min-h-12"
                >
                  Skip
                </Button>
              )}
              <Button
                onClick={doneAction}
                loading={submitting}
                disabled={submitting || acceptUrl !== null}
                className="min-h-12 md:min-w-40"
              >
                {primaryLabel()}
              </Button>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

/** One line of the review list: a check, the thing, and its detail. */
function SummaryRow({ label, meta }: { label: string; meta: string }) {
  return (
    <li className="flex items-center gap-3 border-b border-line px-3.5 py-3 text-[15px] last:border-b-0">
      <span
        aria-hidden
        className="grid size-6 shrink-0 place-items-center rounded-full bg-accent-soft text-accent"
      >
        <IconCheck size={14} stroke={2} />
      </span>
      <span className="min-w-0 flex-1 font-medium text-ink [overflow-wrap:anywhere]">{label}</span>
      <span className="max-w-[50%] shrink-0 text-right text-[13px] text-ink-3 [overflow-wrap:anywhere]">
        {meta}
      </span>
    </li>
  );
}

/**
 * The people step's list and add form. A person is added to the draft only, so
 * Remove before submitting leaves nothing behind on the server.
 */
function PeopleEditor({
  people,
  onChange,
  timezone,
}: {
  people: DraftPerson[];
  onChange: (people: DraftPerson[]) => void;
  /** The family's zone from step 1: an age is a civil-day fact (#5316). */
  timezone: string;
}) {
  const [name, setName] = useState("");
  const [relationship, setRelationship] = useState("");
  const [birthdate, setBirthdate] = useState("");
  const [error, setError] = useState<string | null>(null);
  // A row key only has to be unique for this list's lifetime, so a counter is
  // enough — and unlike `Date.now()` it cannot collide when two people are
  // added inside the same millisecond, which is what a fast typist doing
  // "add, add" actually hits. (#5316: no page reads the clock.)
  const nextKey = useRef(0);

  const add = () => {
    const who = name.trim();
    if (!who) {
      setError("A name is all we need.");
      return;
    }
    setError(null);
    onChange([
      ...people,
      {
        key: `person-${nextKey.current++}`,
        name: who,
        relationship: relationship.trim(),
        birthdate,
      },
    ]);
    setName("");
    setRelationship("");
    setBirthdate("");
  };

  return (
    <div>
      {people.length > 0 && (
        <ul
          aria-label="People added so far"
          className="mb-4 list-none overflow-hidden rounded-token-md border border-line bg-surface p-0"
        >
          {people.map((person) => (
            <li
              key={person.key}
              className="flex min-h-14 items-center gap-3 border-b border-line px-3.5 py-2.5 last:border-b-0"
            >
              <span
                aria-hidden
                className="grid size-10 shrink-0 place-items-center rounded-full bg-surface-2 text-[13px] font-semibold text-ink-2"
              >
                {initials(person.name)}
              </span>
              <span className="min-w-0 flex-1">
                <b className="block text-[15px] font-semibold text-ink [overflow-wrap:anywhere]">
                  {person.name}
                </b>
                <span className="block text-[13px] text-ink-3">
                  {relationshipLine(person.relationship, person.birthdate, timezone)}
                </span>
              </span>
              <button
                type="button"
                onClick={() => onChange(people.filter((p) => p.key !== person.key))}
                aria-label={`Remove ${person.name}`}
                className="grid size-11 shrink-0 place-items-center rounded-token-sm text-ink-3 hover:bg-surface-2 hover:text-ink"
              >
                <IconX size={18} stroke={STROKE} />
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="rounded-token-md border border-line bg-surface-2 p-3.5">
        <div className="mb-3 grid gap-3 md:grid-cols-2">
          <div>
            <label htmlFor="ob-pn" className="mb-1.5 block text-[13px] font-semibold text-ink-2">
              Name
            </label>
            <input
              id="ob-pn"
              className={`${FIELD} bg-surface`}
              value={name}
              onChange={(e) => {
                setName(e.currentTarget.value);
                setError(null);
              }}
              placeholder="Nora Sims"
              autoComplete="off"
            />
          </div>
          <div>
            <label htmlFor="ob-pr" className="mb-1.5 block text-[13px] font-semibold text-ink-2">
              Relationship
            </label>
            <input
              id="ob-pr"
              className={`${FIELD} bg-surface`}
              value={relationship}
              onChange={(e) => setRelationship(e.currentTarget.value)}
              placeholder="Daughter, spouse, grandma"
              autoComplete="off"
            />
          </div>
        </div>
        <div className="mb-3">
          <label htmlFor="ob-pb" className="mb-1.5 block text-[13px] font-semibold text-ink-2">
            Birthday <span className="font-normal text-ink-3">(optional)</span>
          </label>
          <input
            id="ob-pb"
            type="date"
            className={`${FIELD} bg-surface`}
            value={birthdate}
            onChange={(e) => setBirthdate(e.currentTarget.value)}
          />
        </div>
        {error && (
          <p role="alert" className="mb-2 text-sm font-medium text-danger">
            {error}
          </p>
        )}
        <div className="flex justify-end">
          <Button
            variant="default"
            onClick={add}
            leftSection={<IconPlus size={16} />}
            className="min-h-11"
          >
            Add person
          </Button>
        </div>
      </div>
    </div>
  );
}

function initials(name: string): string {
  return name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase() ?? "")
    .join("");
}

/** "Daughter · 8" when there is a birthday, else just the relationship. */
function relationshipLine(relationship: string, birthdate: string, timezone: string): string {
  const years = birthdate ? ageFrom(birthdate, timezone) : null;
  const who = relationship || "Family";
  return years === null ? who : `${who} · ${years}`;
}

/**
 * Whole years since `birthdate`, as of the FAMILY's day (#5316).
 *
 * A birthday is a civil date, so "has their birthday happened yet this year?"
 * must be asked in the family's zone — `householdToday(timezone)` is the one
 * helper that answers what day it is for them. Reading the clock here would
 * make a child's age depend on the device's zone (a browser on UTC at 22:30 in
 * New York is already on tomorrow, so a birthday can arrive a day early).
 */
function ageFrom(birthdate: string, timezone: string): number | null {
  const born = new Date(`${birthdate}T00:00:00Z`);
  if (Number.isNaN(born.getTime())) return null;
  const today = householdToday(timezone);
  let age = Number(today.slice(0, 4)) - born.getUTCFullYear();
  const monthDay = today.slice(5);
  const bornMonthDay = `${String(born.getUTCMonth() + 1).padStart(2, "0")}-${String(born.getUTCDate()).padStart(2, "0")}`;
  if (monthDay < bornMonthDay) age -= 1;
  return age >= 0 && age < 130 ? age : null;
}
