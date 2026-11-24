"use client";

import Link from "next/link";
import { Button } from "@mantine/core";

/**
 * /register, rebuilt to the approved settings/onboarding mockup (artifact #103
 * v1, "standalone screens: auth") at phone width (#4562).
 *
 * THERE IS NOTHING TO SIGN UP FOR, and the page now says so in the mockup's
 * own voice instead of as a Mantine `Title` inside a card. Home OS accounts
 * are created two ways and neither is a public form: a household admin makes
 * one (Settings > Family & members, or an instance admin's Add user), or the
 * person opens an invite link. A registration form here would be a third way
 * in that the API does not have -- so this stays an explanation with one way
 * out, exactly as the artifact's dead-link screen is.
 *
 * The frame (brand row, full-bleed phone page, 48 px primary action) comes
 * from the (auth) layout, so this page draws only its own content.
 */
export default function RegisterPage() {
  return (
    <div data-auth-page="register">
      <span
        aria-hidden
        className="mb-4 grid size-14 place-items-center rounded-token-lg bg-accent-soft text-accent"
      >
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
          <circle cx="9" cy="7" r="4" />
          <path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2M16 11h6M19 8v6" />
        </svg>
      </span>
      <h1 className="m-0 text-[24px] font-semibold leading-tight tracking-[-0.015em] text-ink">
        Registration
      </h1>
      <p className="mb-5 mt-1.5 text-[15px] leading-normal text-ink-2">
        New accounts can only be created by a household administrator. Please contact your
        household admin to set up your account.
      </p>
      <Button component={Link} href="/login" size="md" variant="default" fullWidth className="min-h-12 text-[15px]">
        Back to sign in
      </Button>
      <p className="mb-0 mt-5 text-center text-sm text-ink-2">
        Got an invite link? Open it, or{" "}
        <Link href="/login" className="font-semibold text-accent underline-offset-2 hover:underline">
          sign in
        </Link>{" "}
        and it will be waiting.
      </p>
    </div>
  );
}
