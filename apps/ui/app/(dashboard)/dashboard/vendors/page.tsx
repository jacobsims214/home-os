"use client";

/**
 * Contacts list (#4557), mobile-first per the People & House mockup
 * (artifact #89, People & House > Contacts): a row of trade filter chips over
 * cards that carry the phone actions, because on a phone this page answers
 * "who's the plumber, and call them".
 *
 * It does NOT use EntityList, for the reasons #4555 documented and this page
 * repeats:
 * - EntityList defaults `propertyFilter` on, and a contact has no
 *   `property_id`, so the filter only empties the list (the manager note on
 *   this ticket).
 * - Its "Add" went to `/dashboard/vendors/new`, a route that does not exist.
 *   Here Add opens a sheet and POSTs to /api/v1/vendors.
 * - Its card cannot carry the mockup's Call / Text row, the trade chips, or
 *   the avatar tile.
 *
 * The chips are built from the trades the family's own contacts actually
 * have (`tradeFacets`), not from a hand-written list, so a free-text trade
 * like "handyman" gets a chip and no chip is ever empty. Trade values are the
 * API's stored snake_case forms; `tradeLabel` makes them read as words.
 */

import { useMemo, useState, type FormEvent } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconChevronRight,
  IconMail,
  IconMessage,
  IconPhone,
  IconPlus,
  IconUser,
} from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { vendorKeys } from "@/lib/query-keys";
import { ResponsiveModal, ResponsiveModalActions, TextInput } from "@/components/ui";
import {
  contactInitial,
  contactTradesLine,
  hasTrade,
  mailtoHref,
  smsHref,
  telHref,
  tradeFacets,
  type Contact,
} from "@/components/contact/contact-facts";
import { tradeLabel, TRADE_SUGGESTIONS } from "@/components/contact/trade-vocabulary";
import { createContactBody } from "@/components/contact/contact-form";

const STROKE = 1.75;

/** The chip row: All plus one chip per trade the contacts use. */
function TradeChips({
  facets,
  total,
  active,
  onSelect,
}: {
  facets: Array<{ trade: string; count: number }>;
  total: number;
  active: string | null;
  onSelect: (trade: string | null) => void;
}) {
  return (
    <div
      role="group"
      aria-label="Filter by trade"
      data-trade-chips
      className="-mx-4 mb-3 flex gap-2 overflow-x-auto px-4 pb-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden md:mx-0 md:flex-wrap md:px-0"
    >
      <Chip label="All" count={total} pressed={active === null} onClick={() => onSelect(null)} />
      {facets.map((f) => (
        <Chip
          key={f.trade}
          label={tradeLabel(f.trade)}
          count={f.count}
          pressed={active === f.trade}
          onClick={() => onSelect(f.trade)}
        />
      ))}
    </div>
  );
}

function Chip({
  label,
  count,
  pressed,
  onClick,
}: {
  label: string;
  count: number;
  pressed: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      data-trade-chip
      className={`inline-flex min-h-11 shrink-0 items-center gap-1.5 rounded-full border px-3.5 text-sm font-medium transition-colors ${
        pressed
          ? "border-ink bg-ink text-bg"
          : "border-line bg-surface text-ink hover:bg-surface-2"
      }`}
    >
      {label}
      <span className={pressed ? "text-xs text-bg/75" : "text-xs text-ink-3"}>{count}</span>
    </button>
  );
}

/** One contact card: who they are, their trades, and what you do next. */
function ContactCard({ contact }: { contact: Contact }) {
  const tel = telHref(contact.phone);
  const sms = smsHref(contact.phone);
  const mail = mailtoHref(contact.email);
  const trades = contactTradesLine(contact);

  return (
    <li className="min-w-0">
      <div
        data-contact-card={contact.id}
        className="flex flex-col rounded-token-md border border-line bg-surface shadow-token-sm"
      >
        <Link
          href={`/dashboard/vendors/${contact.id}`}
          className="flex min-h-11 items-center gap-3 p-3.5 transition-colors hover:bg-surface-2 active:bg-surface-2"
        >
          <span
            aria-hidden
            className="grid size-10 shrink-0 place-items-center rounded-token-md bg-surface-2 text-[15px] font-bold text-ink-2"
          >
            {contactInitial(contact.name)}
          </span>
          <span className="min-w-0 flex-1">
            <span className="block text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">
              {contact.name}
            </span>
            {trades && (
              <span className="mt-0.5 block text-sm text-ink-3 [overflow-wrap:anywhere]">{trades}</span>
            )}
          </span>
          <IconChevronRight size={20} stroke={STROKE} className="shrink-0 text-ink-3" aria-hidden />
        </Link>

        {/* The actions a phone actually needs. Real <a>s, so tel:/sms:/mailto:
            hand off to the dialler and mail client; each is a 44 px target. */}
        {(tel || mail) && (
          <div className="flex gap-2 px-3.5 pb-3.5">
            {tel && (
              <a
                href={tel}
                aria-label={`Call ${contact.name}`}
                data-contact-action="call"
                className="inline-flex min-h-11 flex-1 items-center justify-center gap-1.5 rounded-token-sm border border-line bg-surface px-3 text-sm font-semibold text-accent transition-colors hover:bg-surface-2"
              >
                <IconPhone size={17} stroke={STROKE} aria-hidden />
                Call
              </a>
            )}
            {sms && (
              <a
                href={sms}
                aria-label={`Text ${contact.name}`}
                data-contact-action="text"
                className="inline-flex min-h-11 flex-1 items-center justify-center gap-1.5 rounded-token-sm border border-line bg-surface px-3 text-sm font-semibold text-ink-2 transition-colors hover:bg-surface-2"
              >
                <IconMessage size={17} stroke={STROKE} aria-hidden />
                Text
              </a>
            )}
            {mail && (
              <a
                href={mail}
                aria-label={`Email ${contact.name}`}
                data-contact-action="email"
                className="inline-flex min-h-11 flex-1 items-center justify-center gap-1.5 rounded-token-sm border border-line bg-surface px-3 text-sm font-semibold text-ink-2 transition-colors hover:bg-surface-2"
              >
                <IconMail size={17} stroke={STROKE} aria-hidden />
                Email
              </a>
            )}
          </div>
        )}
      </div>
    </li>
  );
}

/** "Add contact": a name and a trade is enough; the rest is on the detail page. */
function AddContactModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [trades, setTrades] = useState("");
  const [phone, setPhone] = useState("");
  const [error, setError] = useState<string | null>(null);

  const close = () => {
    setName("");
    setTrades("");
    setPhone("");
    setError(null);
    create.reset();
    onClose();
  };

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Contact }>("/api/v1/vendors", { method: "POST", body }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: vendorKeys.all });
      notifications.show({ message: "Contact added", color: "teal" });
      const id = res?.data?.id;
      if (!id) return close();
      // Navigate without closing first: closing pops the modal's history
      // entry, and the router RESTORE that triggers cancels the push (#4532).
      router.replace(`/dashboard/vendors/${id}`);
    },
    onError: (err: Error) => setError(err.message || "Couldn't add the contact. Try again."),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const result = createContactBody({ name, trades, phone });
    if (!result.ok) {
      setError(result.error);
      return;
    }
    setError(null);
    create.mutate(result.body);
  };

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add contact">
      <form onSubmit={submit} noValidate>
        <div className="flex flex-col gap-3">
          <TextInput
            label="Name"
            required
            data-autofocus
            placeholder="Joe's Plumbing"
            description="A business or a person."
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <TextInput
            label="Trades"
            placeholder="plumber, handyman"
            description="Comma separated. Plumbing is saved as plumber — the same words the app already uses."
            list="trade-suggestions"
            value={trades}
            onChange={(e) => setTrades(e.currentTarget.value)}
          />
          {/* The canonical stored trades, from the shared vocabulary (one list,
              mirrored from the API's fixture — never a second hand-written one). */}
          <datalist id="trade-suggestions">
            {TRADE_SUGGESTIONS.map((t) => (
              <option key={t} value={t}>
                {tradeLabel(t)}
              </option>
            ))}
          </datalist>
          <TextInput
            label="Phone"
            type="tel"
            inputMode="tel"
            placeholder="555-0100"
            value={phone}
            onChange={(e) => setPhone(e.currentTarget.value)}
          />
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
        </div>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Add contact
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

export default function ContactsPage() {
  const [showAddModal, setShowAddModal] = useState(false);
  // The active chip is a stored trade (or null for All). The list is filtered
  // client-side from one fetch: the API's ?trade= filter is there, but the
  // household's contact list is small and chips must not flash a skeleton on
  // every tap.
  const [activeTrade, setActiveTrade] = useState<string | null>(null);

  const { data, isLoading, isError, refetch, isRefetching } = useQuery({
    queryKey: vendorKeys.lists(),
    queryFn: () => apiFetch<{ data: Contact[] }>("/api/v1/vendors"),
  });

  const contacts = useMemo(() => data?.data ?? [], [data]);
  const facets = useMemo(() => tradeFacets(contacts), [contacts]);
  const shown = useMemo(() => contacts.filter((c) => hasTrade(c, activeTrade)), [contacts, activeTrade]);

  // A trade can disappear (its last contact deleted): fall back to All rather
  // than an empty list under a chip that no longer exists.
  const activeExists = activeTrade === null || facets.some((f) => f.trade === activeTrade);
  const shownSafe = activeExists ? shown : contacts;

  return (
    <div className="mx-auto w-full max-w-6xl">
      <div className="mb-4 md:mb-6">
        <div className="flex items-center justify-between gap-3">
          <h2 className="min-w-0 text-xl font-semibold text-ink md:text-2xl">
            Contacts
            {!isLoading && !isError && contacts.length > 0 && (
              <span className="ml-2 align-middle text-base font-medium text-ink-3">{contacts.length}</span>
            )}
          </h2>
          <Button
            onClick={() => setShowAddModal(true)}
            leftSection={<IconPlus size={16} />}
            aria-haspopup="dialog"
            className="shrink-0 max-md:min-h-11"
          >
            Add contact
          </Button>
        </div>
        <p className="mt-1 text-sm text-ink-3">
          The plumber, the vet, the babysitter — everyone the family calls.
        </p>
      </div>

      {isLoading && (
        <div aria-busy="true" aria-label="Loading contacts" className="space-y-2">
          {[1, 2, 3].map((i) => (
            <div key={i} className="space-y-2 rounded-token-md border border-line bg-surface p-4 shadow-token-sm">
              <Skeleton height={14} width="55%" radius="sm" />
              <Skeleton height={12} width="80%" radius="sm" />
            </div>
          ))}
        </div>
      )}

      {isError && (
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" />
          <p className="flex-1 text-sm">Couldn&apos;t load contacts. Check your connection and try again.</p>
          <Button variant="default" onClick={() => refetch()} loading={isRefetching} className="max-md:min-h-11">
            Try again
          </Button>
        </div>
      )}

      {!isLoading && !isError && contacts.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <IconUser size={28} stroke={STROKE} className="text-ink-3" aria-hidden />
          <p className="text-sm text-ink-3">
            No contacts yet. Add your plumber, your vet, or anyone else the family calls.
          </p>
          <Button
            variant="light"
            onClick={() => setShowAddModal(true)}
            leftSection={<IconPlus size={16} />}
            className="max-md:min-h-11"
          >
            Add contact
          </Button>
        </div>
      )}

      {!isLoading && !isError && contacts.length > 0 && (
        <>
          <TradeChips
            facets={facets}
            total={contacts.length}
            active={activeExists ? activeTrade : null}
            onSelect={setActiveTrade}
          />

          {shownSafe.length === 0 ? (
            <p className="rounded-token-md border border-dashed border-line px-4 py-8 text-center text-sm text-ink-3">
              No contacts with that trade.
            </p>
          ) : (
            <ul aria-label="Contacts" className="grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-3">
              {shownSafe.map((contact) => (
                <ContactCard key={contact.id} contact={contact} />
              ))}
            </ul>
          )}

          {activeTrade !== null && <p className="sr-only" aria-live="polite">{`Showing ${shownSafe.length} contact${shownSafe.length === 1 ? "" : "s"} with the ${tradeLabel(activeTrade)} trade`}</p>}
        </>
      )}

      <AddContactModal opened={showAddModal} onClose={() => setShowAddModal(false)} />
    </div>
  );
}
