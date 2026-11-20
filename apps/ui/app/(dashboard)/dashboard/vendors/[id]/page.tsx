"use client";

/**
 * Contact detail (#4557), mobile-first per the People & House mockup
 * (artifact #89) inside the DetailPageLayout frame (KB
 * architecture/detail-page-layout.md), the same frame the property and
 * vehicle details use.
 *
 * This page replaces `EntityDetail`, which is why it exists: the audit
 * (artifact #74) found vendors/[id] rendered an EMPTY card at every width,
 * because the page passed no `fields`. Every screen here reads the real
 * columns — trades, specialty, phone, email, website, address, notes.
 *
 * The things a phone needs most come first:
 * - **Call / Text / Email** as real `tel:` / `sms:` / `mailto:` links, so
 *   "who's the plumber?" ends in a phone call.
 * - **The address, tap-to-open in Maps** (#4766 added the column; the UI
 *   showed none until this page). Addresses are no longer kept in notes.
 * - **Trades as chips**, in the stored snake_case form behind a readable
 *   label, plus the free-text `specialty` when set.
 *
 * WHAT IT SHOWS BEYOND THE CONTACT: the pets this contact is the vet for
 * (pets.vet_vendor_id, #4703) and the bills linked to it (bills.vendor_id).
 * Those are the only two places a vendor id is referenced
 * (apps/api/internal), so those are its "linked items" — nothing is invented.
 */

import { useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconChevronRight,
  IconExternalLink,
  IconMail,
  IconMapPin,
  IconMessage,
  IconPhone,
} from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { billKeys, petKeys, vendorKeys } from "@/lib/query-keys";
import { TextInput } from "@/components/ui";
import DetailPageLayout from "@/components/layout/DetailPageLayout";
import {
  contactInitial,
  mailtoHref,
  mapsHref,
  smsHref,
  telHref,
  type Contact,
} from "@/components/contact/contact-facts";
import {
  buildContactUpdate,
  contactToFormValues,
  MAX_ADDRESS_LEN,
  websiteHref,
  type ContactFormValues,
} from "@/components/contact/contact-form";
import { tradeLabel, TRADE_SUGGESTIONS } from "@/components/contact/trade-vocabulary";

const STROKE = 1.75;

/** A pet this contact is the vet for (only the fields this page reads). */
interface Pet {
  id: string;
  name: string;
  species?: string | null;
  vet_vendor_id?: string | null;
}

/** A bill linked to this contact (bills.vendor_id). */
interface Bill {
  id: string;
  name: string;
  amount?: string | null;
  frequency?: string | null;
  vendor_id?: string | null;
}

function usd(v: string | null | undefined): string | null {
  const n = Number(v);
  if (v == null || v === "" || !Number.isFinite(n)) return null;
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", minimumFractionDigits: 0, maximumFractionDigits: 2 });
}

/** A read-only details row, per the detail-page field contract. */
function Row({ field, label, value }: { field: string; label: string; value: React.ReactNode }) {
  return (
    <div
      data-field={field}
      className="flex flex-col gap-1 py-2.5 md:grid md:grid-cols-[minmax(7rem,1fr)_minmax(0,2fr)] md:items-baseline md:gap-x-6"
    >
      <dt className="text-xs text-ink-3 md:text-sm">{label}</dt>
      <dd className="m-0 whitespace-pre-line text-sm text-ink [overflow-wrap:anywhere]">{value}</dd>
    </div>
  );
}

/** One of the three contact actions; a plain anchor, 44 px on a phone. */
function ActionLink({
  href,
  label,
  icon: Icon,
  tone,
  action,
}: {
  href: string;
  label: string;
  icon: typeof IconPhone;
  tone: "accent" | "plain";
  action: string;
}) {
  return (
    <a
      href={href}
      data-contact-action={action}
      className={`inline-flex min-h-11 flex-1 items-center justify-center gap-1.5 rounded-token-sm border border-line bg-surface px-3 text-sm font-semibold transition-colors hover:bg-surface-2 ${
        tone === "accent" ? "text-accent" : "text-ink-2"
      }`}
    >
      <Icon size={17} stroke={STROKE} aria-hidden />
      {label}
    </a>
  );
}

export default function ContactDetailPage() {
  const params = useParams<{ id: string }>();
  const vendorId = params.id;
  const queryClient = useQueryClient();

  const [isEditing, setIsEditing] = useState(false);
  const [values, setValues] = useState<ContactFormValues>(contactToFormValues({}));
  const [formError, setFormError] = useState<string | null>(null);

  const contactQuery = useQuery({
    queryKey: vendorKeys.detail(vendorId),
    queryFn: () => apiFetch<{ data: Contact }>(`/api/v1/vendors/${vendorId}`),
    enabled: !!vendorId,
  });

  // The pets whose vet this is (#4703). GET /pets has no vet_vendor_id filter,
  // so it is filtered here — the same shape the vehicle page uses for upkeep.
  const isVet = (contactQuery.data?.data?.trades ?? []).includes("vet");
  const petsQuery = useQuery({
    queryKey: [...petKeys.all, "byVet", vendorId],
    queryFn: () =>
      apiFetch<{ data: Pet[] }>("/api/v1/pets").then((r) =>
        (r.data ?? []).filter((p) => p.vet_vendor_id === vendorId),
      ),
    enabled: !!vendorId && isVet,
  });

  // The bills paid to this contact (bills.vendor_id). Also filtered here:
  // GET /bills lists the household.
  const billsQuery = useQuery({
    queryKey: [...billKeys.all, "byVendor", vendorId],
    queryFn: () =>
      apiFetch<{ data: Bill[] }>("/api/v1/bills").then((r) =>
        (r.data ?? []).filter((b) => b.vendor_id === vendorId),
      ),
    enabled: !!vendorId,
  });

  const update = useMutation({
    mutationFn: (v: ContactFormValues) => {
      const result = buildContactUpdate(v);
      if (!result.ok) throw new Error(result.error);
      return apiFetch<{ data: Contact }>(`/api/v1/vendors/${vendorId}`, { method: "PUT", body: result.body });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: vendorKeys.detail(vendorId) });
      queryClient.invalidateQueries({ queryKey: vendorKeys.lists() });
      setIsEditing(false);
      setFormError(null);
      notifications.show({ message: "Contact saved", color: "teal" });
    },
    onError: (err: Error) => setFormError(err.message || "Couldn't save. Try again."),
  });

  const contact = contactQuery.data?.data;

  const startEditing = () => {
    if (!contact) return;
    setValues(contactToFormValues(contact));
    setFormError(null);
    setIsEditing(true);
  };

  const set = (field: keyof ContactFormValues) => (e: React.ChangeEvent<HTMLInputElement>) => {
    const v = e.currentTarget.value;
    setValues((prev) => ({ ...prev, [field]: v }));
  };

  // ── Loading / error ────────────────────────────────────────

  if (contactQuery.isLoading) {
    return (
      <div role="status" aria-busy="true" aria-label="Loading contact" className="mx-auto w-full max-w-2xl space-y-3">
        <Skeleton height={20} width="55%" radius="sm" />
        <Skeleton height={14} width="35%" radius="sm" />
        <Skeleton height={96} radius="md" />
        <Skeleton height={96} radius="md" />
      </div>
    );
  }

  if (contactQuery.isError || !contact) {
    return (
      <div className="mx-auto w-full max-w-2xl">
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">Couldn&apos;t load this contact. Check your connection and try again.</p>
          <Button
            variant="default"
            onClick={() => contactQuery.refetch()}
            loading={contactQuery.isFetching}
            className="max-md:min-h-11"
          >
            Try again
          </Button>
        </div>
        <Link
          href="/dashboard/vendors"
          className="mt-4 inline-flex min-h-11 items-center gap-1 text-sm font-medium text-accent hover:underline"
        >
          Back to contacts
        </Link>
      </div>
    );
  }

  const tel = telHref(contact.phone);
  const sms = smsHref(contact.phone);
  const mail = mailtoHref(contact.email);
  const maps = mapsHref(contact.address);
  const site = websiteHref(contact.website);
  const trades = contact.trades ?? [];
  const pets = petsQuery.data ?? [];
  const bills = billsQuery.data ?? [];

  // ── Edit mode ──────────────────────────────────────────────

  if (isEditing) {
    // Read the value BEFORE entering the updater: React clears the event by the
    // time a state updater runs, so `currentTarget` is null there (the vehicle
    // page documents the same trap).
    return (
      <DetailPageLayout
        entityType="vendor"
        entityId={vendorId}
        title={`Edit ${contact.name}`}
        isEditing
        onCancel={() => setIsEditing(false)}
        onSave={() => {
          // Validate here too, so a bad body never leaves as a request.
          const result = buildContactUpdate(values);
          if (!result.ok) {
            setFormError(result.error);
            return;
          }
          setFormError(null);
          update.mutate(values);
        }}
        isSaving={update.isPending}
      >
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 max-md:[&_.mantine-Input-input]:min-h-11 max-md:[&_.mantine-Input-input]:text-base">
          <TextInput
            label="Name"
            required
            data-autofocus
            value={values.name}
            onChange={set("name")}
          />
          <TextInput
            label="Trades"
            description="Comma separated, saved in the app's own words."
            list="contact-trade-suggestions"
            value={values.trades}
            onChange={set("trades")}
          />
          <datalist id="contact-trade-suggestions">
            {TRADE_SUGGESTIONS.map((t) => (
              <option key={t} value={t}>
                {tradeLabel(t)}
              </option>
            ))}
          </datalist>
          <TextInput label="Specialty" value={values.specialty} onChange={set("specialty")} />
          <TextInput label="Phone" type="tel" inputMode="tel" value={values.phone} onChange={set("phone")} />
          <TextInput label="Email" type="email" inputMode="email" value={values.email} onChange={set("email")} />
          <TextInput label="Website" inputMode="url" value={values.website} onChange={set("website")} />
        </div>
        <div className="mt-4">
          {/* One line, 500 chars: the API normalises it to a single line and
              rejects anything longer (KB apis/vendors.md). */}
          <TextInput
            label="Address"
            description={`One line, up to ${MAX_ADDRESS_LEN} characters. Opens in Maps from the contact.`}
            value={values.address}
            onChange={set("address")}
            maxLength={MAX_ADDRESS_LEN}
          />
        </div>
        <div className="mt-4">
          <TextInput label="Notes" value={values.notes} onChange={set("notes")} />
        </div>
        {formError && (
          <p role="alert" className="mt-3 text-sm text-danger">
            {formError}
          </p>
        )}
      </DetailPageLayout>
    );
  }

  // ── Read mode ──────────────────────────────────────────────

  return (
    <DetailPageLayout entityType="vendor" entityId={vendorId} title={contact.name} onEdit={startEditing}>
      <div data-contact-detail className="min-w-0">
        {/* The contact's head: avatar, name, and the trades line. */}
        <div className="mb-4 flex items-center gap-3.5">
          <span
            aria-hidden
            className="grid size-14 shrink-0 place-items-center rounded-token-md bg-accent-soft text-xl font-bold text-accent"
          >
            {contactInitial(contact.name)}
          </span>
          <div className="min-w-0 flex-1">
            <h1 className="text-xl font-semibold leading-tight text-ink [overflow-wrap:anywhere] md:text-2xl">
              {contact.name}
            </h1>
            {trades.length > 0 ? (
              <p className="mt-0.5 flex flex-wrap gap-1.5" data-contact-trades>
                {trades.map((t) => (
                  <span
                    key={t}
                    className="inline-flex items-center rounded-full bg-surface-2 px-2.5 py-1 text-xs font-medium text-ink-2"
                  >
                    {tradeLabel(t)}
                  </span>
                ))}
              </p>
            ) : (
              contact.specialty && (
                <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{contact.specialty}</p>
              )
            )}
          </div>
        </div>

        <div className="flex flex-col gap-6">
          {/* The actions, first: on a phone this is the whole point. */}
          {(tel || mail) && (
            <div data-contact-actions className="flex flex-wrap gap-2">
              {tel && <ActionLink href={tel} label="Call" icon={IconPhone} tone="accent" action="call" />}
              {sms && <ActionLink href={sms} label="Text" icon={IconMessage} tone="plain" action="text" />}
              {mail && <ActionLink href={mail} label="Email" icon={IconMail} tone="plain" action="email" />}
            </div>
          )}

          {/* The address, tap-to-open in Maps (#4766). */}
          {contact.address && (
            <section aria-labelledby="contact-address" className="min-w-0">
              <h2 id="contact-address" className="mb-1 text-sm font-semibold text-ink-2">
                Address
              </h2>
              {maps ? (
                <a
                  href={maps}
                  target="_blank"
                  rel="noreferrer noopener"
                  data-contact-address
                  className="flex min-h-11 items-center gap-3 rounded-token-md border border-line bg-surface p-3.5 text-ink shadow-token-sm transition-colors hover:bg-surface-2"
                >
                  <IconMapPin size={20} stroke={STROKE} className="shrink-0 text-accent" aria-hidden />
                  <span className="min-w-0 flex-1 text-sm [overflow-wrap:anywhere]">{contact.address}</span>
                  <span className="shrink-0 text-sm font-semibold text-accent">Maps</span>
                </a>
              ) : (
                <p className="text-sm text-ink [overflow-wrap:anywhere]">{contact.address}</p>
              )}
            </section>
          )}

          {/* The contact's own fields. No heading of its own: the frame's
              collapsible section is already titled "Details", and a second one
              inside it reads as a duplicate (the vehicle page's trap). */}
          <section aria-label="Contact details" className="min-w-0">
            <dl className="m-0 divide-y divide-line">
              <Row field="trades" label="Trades" value={trades.length > 0 ? trades.map(tradeLabel).join(", ") : "—"} />
              <Row field="specialty" label="Specialty" value={contact.specialty || "—"} />
              <Row
                field="phone"
                label="Phone"
                value={
                  tel ? (
                    <a href={tel} className="font-medium text-accent hover:underline">
                      {contact.phone}
                    </a>
                  ) : (
                    "—"
                  )
                }
              />
              <Row
                field="email"
                label="Email"
                value={
                  mail ? (
                    <a href={mail} className="font-medium text-accent hover:underline">
                      {contact.email}
                    </a>
                  ) : (
                    contact.email || "—"
                  )
                }
              />
              <Row
                field="website"
                label="Website"
                value={
                  site ? (
                    <a
                      href={site}
                      target="_blank"
                      rel="noreferrer noopener"
                      className="inline-flex items-center gap-1 font-medium text-accent hover:underline [overflow-wrap:anywhere]"
                    >
                      {contact.website}
                      <IconExternalLink size={14} stroke={STROKE} aria-hidden />
                    </a>
                  ) : (
                    contact.website || "—"
                  )
                }
              />
              <Row field="address" label="Address" value={contact.address || "—"} />
              <Row field="notes" label="Notes" value={contact.notes || "—"} />
            </dl>
          </section>

          {/* The pets whose vet this contact is (#4703). Only for a vet, and
              only when at least one pet names them, so no empty box. */}
          {isVet && pets.length > 0 && (
            <section aria-labelledby="contact-pets" className="min-w-0">
              <h2 id="contact-pets" className="mb-1 text-sm font-semibold text-ink-2">
                Vet for
              </h2>
              <ul
                data-contact-pets
                className="divide-y divide-line rounded-token-md border border-line bg-surface shadow-token-sm"
              >
                {pets.map((pet) => (
                  <li key={pet.id}>
                    <Link
                      href={`/dashboard/pets/${pet.id}`}
                      className="flex min-h-11 items-center gap-3 px-4 py-2.5 transition-colors hover:bg-surface-2"
                    >
                      <span className="min-w-0 flex-1">
                        <span className="block text-sm font-medium text-ink [overflow-wrap:anywhere]">{pet.name}</span>
                        {pet.species && <span className="block text-xs text-ink-3">{pet.species}</span>}
                      </span>
                      <IconChevronRight size={18} stroke={STROKE} className="shrink-0 text-ink-3" aria-hidden />
                    </Link>
                  </li>
                ))}
              </ul>
            </section>
          )}

          {/* The bills paid to this contact (bills.vendor_id). */}
          {bills.length > 0 && (
            <section aria-labelledby="contact-bills" className="min-w-0">
              <h2 id="contact-bills" className="mb-1 text-sm font-semibold text-ink-2">
                Bills
              </h2>
              <ul
                data-contact-bills
                className="divide-y divide-line rounded-token-md border border-line bg-surface shadow-token-sm"
              >
                {bills.map((bill) => (
                  <li key={bill.id}>
                    <Link
                      href={`/dashboard/bills/${bill.id}`}
                      className="flex min-h-11 items-center gap-3 px-4 py-2.5 transition-colors hover:bg-surface-2"
                    >
                      <span className="min-w-0 flex-1">
                        <span className="block text-sm font-medium text-ink [overflow-wrap:anywhere]">{bill.name}</span>
                        {bill.frequency && <span className="block text-xs text-ink-3">{bill.frequency}</span>}
                      </span>
                      {usd(bill.amount) && (
                        <span className="shrink-0 text-sm font-semibold tabular-nums text-ink">{usd(bill.amount)}</span>
                      )}
                      <IconChevronRight size={18} stroke={STROKE} className="shrink-0 text-ink-3" aria-hidden />
                    </Link>
                  </li>
                ))}
              </ul>
            </section>
          )}

          {billsQuery.isError && (
            <p role="alert" className="flex items-center gap-2 text-sm text-warn-ink">
              <IconAlertTriangle size={16} stroke={STROKE} aria-hidden />
              Couldn&apos;t load the bills for this contact.
            </p>
          )}
        </div>
      </div>
    </DetailPageLayout>
  );
}
