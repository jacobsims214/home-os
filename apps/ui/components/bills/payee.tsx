/**
 * Who we pay, and how to reach them (#5499).
 *
 * The payee is NOT money: a vendor is readable by every role that can read the
 * bill, so this block renders for a housesitter too (the account beside it does
 * not — see the payment-account line in the list). The contact links are the
 * established ones from `components/contact/contact-facts.ts`: `tel:`,
 * `mailto:` and the website, so a phone taps straight through to the dialler
 * rather than copying a number by hand.
 *
 * The website is not a `websiteHref` from contact-form.ts here because that
 * helper is the contact form's; both do the same normalisation and the payee
 * card links out with `target="_blank"`, so the address is used as given when
 * the API already stores a full URL.
 */

import { IconExternalLink, IconMail, IconPhone, IconWorld } from "@tabler/icons-react";
import { mailtoHref, telHref } from "@/components/contact/contact-facts";
import type { Payee } from "@/lib/bills/model";

const STROKE = 1.75;

/** A website as an href: as given when it already has a scheme, else https. */
export function payeeWebsiteHref(website: string | null | undefined): string | null {
  const w = (website ?? "").trim();
  if (!w) return null;
  if (/^https?:\/\//i.test(w)) return w;
  // Anything that looks like a host gets https; a bare word does not become a link.
  return /^[a-z0-9-]+(\.[a-z0-9-]+)+(\/|$)/i.test(w) ? `https://${w}` : null;
}

/** The payee's phone numbers, as dialable hrefs, in the order the API gave them. */
export function payeePhones(payee: Payee | null | undefined): { display: string; href: string }[] {
  const seen = new Set<string>();
  const out: { display: string; href: string }[] = [];
  for (const phone of payee?.phones ?? []) {
    const display = (phone ?? "").trim();
    const href = telHref(display);
    if (!display || !href || seen.has(href)) continue;
    seen.add(href);
    out.push({ display, href });
  }
  return out;
}

/**
 * The payee as one compact line — "Riverside Water Authority" — for a list row
 * where there is no room for the contact buttons. Null when the API sent no
 * payee (older bills, or a bill with no vendor linked).
 */
export function payeeName(payee: Payee | null | undefined): string | null {
  const name = (payee?.name ?? "").trim();
  return name || null;
}

/**
 * The payee card's contact row: Call / Email / Website, each a 44 px target,
 * each a real link so the phone hands off to the dialler, the mail app or the
 * browser. Nothing renders when the vendor has no contact details — the card
 * then just names who is paid.
 */
export function PayeeActions({ payee, className = "" }: { payee: Payee | null | undefined; className?: string }) {
  const phones = payeePhones(payee);
  const mail = mailtoHref(payee?.email);
  const site = payeeWebsiteHref(payee?.website);
  if (phones.length === 0 && !mail && !site) return null;

  return (
    <div className={`flex flex-wrap gap-2 ${className}`} data-payee-actions>
      {phones.map(({ display, href }) => (
        <a
          key={href}
          href={href}
          data-payee-action="call"
          aria-label={`Call ${payee?.name ?? "the payee"} at ${display}`}
          className="inline-flex min-h-11 flex-1 items-center justify-center gap-1.5 rounded-token-sm border border-line bg-surface px-3 text-sm font-semibold text-accent transition-colors hover:bg-surface-2"
        >
          <IconPhone size={17} stroke={STROKE} aria-hidden />
          Call
        </a>
      ))}
      {mail && (
        <a
          href={mail}
          data-payee-action="email"
          aria-label={`Email ${payee?.name ?? "the payee"}`}
          className="inline-flex min-h-11 flex-1 items-center justify-center gap-1.5 rounded-token-sm border border-line bg-surface px-3 text-sm font-semibold text-ink-2 transition-colors hover:bg-surface-2"
        >
          <IconMail size={17} stroke={STROKE} aria-hidden />
          Email
        </a>
      )}
      {site && (
        <a
          href={site}
          target="_blank"
          rel="noreferrer noopener"
          data-payee-action="website"
          aria-label={`Open ${payee?.name ?? "the payee"}'s website`}
          className="inline-flex min-h-11 flex-1 items-center justify-center gap-1.5 rounded-token-sm border border-line bg-surface px-3 text-sm font-semibold text-ink-2 transition-colors hover:bg-surface-2"
        >
          <IconWorld size={17} stroke={STROKE} aria-hidden />
          Website
        </a>
      )}
    </div>
  );
}

/**
 * The payee card on a bill's detail page: who is paid, how often, how much, and
 * the way to reach them. The cadence and amount are the same strings the list
 * shows, so the two surfaces cannot disagree.
 */
export function PayeeCard({
  payee,
  cadence,
  amount,
  frequency,
}: {
  payee: Payee | null | undefined;
  cadence: string | null;
  amount: string | null;
  frequency: string | null;
}) {
  const name = payeeName(payee);
  if (!name) return null;
  const phones = payeePhones(payee);
  const mail = mailtoHref(payee?.email);
  const site = payeeWebsiteHref(payee?.website);

  return (
    <section className="rounded-token-md border border-line bg-surface" aria-labelledby="payee-heading" data-testid="payee-card">
      <div className="px-4 pt-3.5">
        <h2 id="payee-heading" className="m-0 text-[13px] font-semibold uppercase tracking-[.04em] text-ink-3">
          Who we pay
        </h2>
        <p className="mt-1 text-[17px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">{name}</p>
        <p className="mt-0.5 text-[13px] text-ink-3">
          {[amount, cadence ?? frequency].filter(Boolean).join(" · ") || "No amount recorded"}
        </p>
        {/* The contact details in words as well as buttons: a person reading on
            a desktop wants to see the number, not only a Call button. */}
        <dl className="mt-2 space-y-1 text-[13px] text-ink-2">
          {phones.map(({ display, href }) => (
            <div key={href} className="flex items-center gap-1.5">
              <dt className="sr-only">Phone</dt>
              <dd className="m-0">
                <a href={href} className="font-medium text-accent underline [overflow-wrap:anywhere]">
                  {display}
                </a>
              </dd>
            </div>
          ))}
          {mail && (
            <div className="flex items-center gap-1.5">
              <dt className="sr-only">Email</dt>
              <dd className="m-0">
                <a href={mail} className="font-medium text-accent underline [overflow-wrap:anywhere]">
                  {payee?.email}
                </a>
              </dd>
            </div>
          )}
          {site && (
            <div className="flex items-center gap-1.5">
              <dt className="sr-only">Website</dt>
              <dd className="m-0">
                <a
                  href={site}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="inline-flex items-center gap-1 font-medium text-accent underline [overflow-wrap:anywhere]"
                >
                  {payee?.website}
                  <IconExternalLink size={13} stroke={STROKE} aria-hidden />
                </a>
              </dd>
            </div>
          )}
        </dl>
      </div>
      <PayeeActions payee={payee} className="px-4 pb-3.5 pt-3" />
    </section>
  );
}
