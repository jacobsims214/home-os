"use client";

/**
 * Pets list (#4556), mobile-first per the People & House mockup (artifact #89
 * v1, the "Pets" section on Family and the pet screen) inside the approved
 * shell (#75 v1), in the pattern the properties (#4553), assets (#4554) and
 * vehicles (#4555) lists now use.
 *
 * - Phone (< 768 px): stacked cards, each a 44 px+ tap target; nothing clips
 *   at 375 px. A card carries the mockup's readings: the name, "Golden
 *   retriever · 6" (breed and age), the vet's name, and a call button when the
 *   vet has a phone.
 * - >= 768 px: a table in a horizontal scroller, first cell a real link.
 * - "Add pet" opens the create modal (ResponsiveModal: a bottom sheet on
 *   phones) and POSTs /api/v1/pets. There is no /new route (the #4509 audit
 *   bug). The create form has no vet field: a vet is a shared contact, chosen
 *   on the pet's page (#4703), so the modal says so.
 *
 * WHY NOT EntityList
 * EntityList defaults `propertyFilter` on and pets have no `property_id`, so
 * the filter emptied this list (the manager note on this ticket and the
 * pattern doc's "Known gaps"). Its cards also cannot show the vet, which is
 * the pet's most useful fact after its name. Pets have no due date, so
 * #4558's due-date grouping does not apply and there is no second grouping.
 *
 * DATES: `date_of_birth` is a DATE. The age is worked out in
 * components/pet/pet-care.ts from the string's own parts, never with
 * `new Date("YYYY-MM-DD")` (UTC midnight renders as the day before in New
 * York: #4550's bug class). It uses the household's today, after mount, so
 * the server and the client cannot disagree about the day.
 */

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton, Stack, Table } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconChevronRight, IconPaw, IconPlus } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { petKeys } from "@/lib/query-keys";
import { householdToday } from "@/lib/today/agenda";
import { useCurrentHousehold } from "@/components/shared/use-nav-role";
import { ResponsiveModal, ResponsiveModalActions, TextInput } from "@/components/ui";
import { ageShort, petLine, vetName } from "@/components/pet/pet-care";

const STROKE = 1.75;

/** A pet as GET /api/v1/pets returns it (apps/api/internal/pet/handler.go). */
interface Pet {
  id: string;
  name: string;
  species?: string | null;
  breed?: string | null;
  date_of_birth?: string | null;
  vet_vendor_id?: string | null;
  /** Read-only: the vet contact's name and phone, resolved by the API (#4700). */
  vet_name?: string | null;
  vet_phone?: string | null;
  notes?: string | null;
}

/**
 * One pet, as the mockup's list row draws it: a paw, the name, and "Golden
 * retriever · 6 · Springfield Animal Hospital" under it. The whole card is the
 * link (one tap target, like the assets and vehicles lists); calling the vet
 * is one tap further, on the pet's page.
 */
/** The Age cell: a number, a dash when unknown, and never a zone-shifted date. */
function ageColumn(pet: Pet, today: string | null): string {
  const age = today ? ageShort(pet.date_of_birth, today) : "";
  return age || "\u2014";
}

function PetCard({ pet, today }: { pet: Pet; today: string | null }) {
  const line = petLine(pet, today);
  return (
    <Link
      href={`/dashboard/pets/${pet.id}`}
      data-pet-card={pet.id}
      className="flex min-h-11 items-center gap-3 rounded-token-md border border-line bg-surface p-4 shadow-token-sm transition-colors hover:bg-surface-2 active:bg-surface-2"
    >
      <span aria-hidden className="grid size-11 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent">
        <IconPaw size={22} stroke={STROKE} />
      </span>
      <div className="min-w-0 flex-1">
        <h3 className="text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">{pet.name}</h3>
        {line && <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{line}</p>}
      </div>
      <IconChevronRight size={20} stroke={STROKE} className="shrink-0 text-ink-3" aria-hidden />
    </Link>
  );
}

/** "Add pet": a name is all the API requires; the rest is edited on its page. */
function AddPetModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [species, setSpecies] = useState("");
  const [breed, setBreed] = useState("");
  const [formError, setFormError] = useState<string | null>(null);

  const close = () => {
    setName("");
    setSpecies("");
    setBreed("");
    setFormError(null);
    create.reset();
    onClose();
  };

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Pet }>("/api/v1/pets", { method: "POST", body }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: petKeys.all });
      notifications.show({ message: "Pet added", color: "teal" });
      const id = res?.data?.id;
      if (!id) return close();
      // Navigate without closing first: replace swaps out the sheet's history
      // entry, and closing would pop it and cancel this (architecture/responsive-modal.md).
      router.replace(`/dashboard/pets/${id}`);
    },
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const petName = name.trim();
    if (!petName) {
      setFormError("A name is required");
      return;
    }
    // Optional fields are sent only when filled: the API stores NULL otherwise
    // and an empty string is not a species.
    const body: Record<string, unknown> = { name: petName };
    if (species.trim()) body.species = species.trim();
    if (breed.trim()) body.breed = breed.trim();
    create.mutate(body);
  };

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add pet">
      <form onSubmit={submit} noValidate>
        <Stack gap="md">
          <TextInput
            label="Name"
            required
            value={name}
            error={formError ?? undefined}
            placeholder="e.g. Biscuit"
            data-autofocus
            onChange={(e) => {
              setName(e.currentTarget.value);
            }}
          />
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <TextInput
              label="Species"
              value={species}
              placeholder="Dog, cat, bird…"
              onChange={(e) => setSpecies(e.currentTarget.value)}
            />
            <TextInput
              label="Breed"
              value={breed}
              placeholder="e.g. Golden retriever"
              onChange={(e) => setBreed(e.currentTarget.value)}
            />
          </div>
          <p className="text-[13px] text-ink-3">
            The vet, birthday, microchip and insurance are on the pet&apos;s page once it exists.
          </p>
          {create.isError && (
            <p role="alert" className="text-sm text-danger">
              Couldn&apos;t add the pet: {create.error instanceof Error ? create.error.message : "try again"}
            </p>
          )}
        </Stack>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Add pet
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

export default function PetsPage() {
  const [showAdd, setShowAdd] = useState(false);
  const household = useCurrentHousehold();

  // Today in the FAMILY's zone, after mount (the server's clock is not the
  // family's): an age is a whole number of days' difference, so a wrong day
  // can age a pet a day early. Never derived during render.
  const [today, setToday] = useState<string | null>(null);
  useEffect(() => {
    setToday(householdToday(household?.timezone ?? null));
  }, [household?.timezone]);

  const petsQuery = useQuery({
    queryKey: petKeys.lists(),
    queryFn: () => apiFetch<{ data: Pet[] }>("/api/v1/pets"),
  });

  const pets = petsQuery.data?.data ?? [];

  return (
    <div className="mx-auto w-full max-w-6xl">
      <div className="mb-4 md:mb-6">
        <div className="flex items-center justify-between gap-3">
          <h2 className="min-w-0 text-xl font-semibold text-ink md:text-2xl">
            Pets
            {!petsQuery.isLoading && !petsQuery.isError && pets.length > 0 && (
              <span className="ml-2 align-middle text-base font-medium text-ink-3">{pets.length}</span>
            )}
          </h2>
          <Button
            onClick={() => setShowAdd(true)}
            leftSection={<IconPlus size={16} />}
            aria-haspopup="dialog"
            className="max-md:min-h-11 shrink-0"
          >
            Add pet
          </Button>
        </div>
        <p className="mt-1 text-sm text-ink-3">The family&apos;s animals, their vet and their care notes.</p>
      </div>

      {petsQuery.isLoading && (
        <div aria-busy="true" aria-label="Loading pets" className="space-y-2">
          {[1, 2, 3].map((i) => (
            <div key={i} className="space-y-2 rounded-token-md border border-line bg-surface p-4 shadow-token-sm">
              <Skeleton height={14} width="45%" radius="sm" />
              <Skeleton height={12} width="75%" radius="sm" />
            </div>
          ))}
        </div>
      )}

      {petsQuery.isError && (
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">Couldn&apos;t load your pets. Check your connection and try again.</p>
          <Button variant="default" onClick={() => petsQuery.refetch()} loading={petsQuery.isRefetching} className="max-md:min-h-11">
            Try again
          </Button>
        </div>
      )}

      {!petsQuery.isLoading && !petsQuery.isError && pets.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <p className="max-w-prose text-sm text-ink-3">
            No pets yet. Add one with its vet and care notes, so whoever is looking after the house knows what they need.
          </p>
          <Button variant="light" onClick={() => setShowAdd(true)} leftSection={<IconPlus size={16} />} className="max-md:min-h-11">
            Add pet
          </Button>
        </div>
      )}

      {!petsQuery.isLoading && !petsQuery.isError && pets.length > 0 && (
        <>
          <ul aria-label="Pets" className="space-y-2 md:hidden">
            {pets.map((pet) => (
              <li key={pet.id} className="min-w-0">
                <PetCard pet={pet} today={today} />
              </li>
            ))}
          </ul>

          <div data-pet-table className="overflow-x-auto rounded-token-md border border-line bg-surface shadow-token-sm max-md:hidden">
            <Table highlightOnHover verticalSpacing="sm" horizontalSpacing="md" className="min-w-[36rem]">
              <Table.Thead>
                <Table.Tr>
                  {["Name", "Species", "Breed", "Age", "Vet"].map((label) => (
                    <Table.Th key={label} scope="col" className="whitespace-nowrap text-left text-xs font-semibold uppercase tracking-wide text-ink-3">
                      {label}
                    </Table.Th>
                  ))}
                  <Table.Th className="w-10">
                    <span className="sr-only">Open</span>
                  </Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {pets.map((pet) => (
                  <Table.Tr key={pet.id}>
                    <Table.Td className="min-w-[10rem] max-w-[22rem] font-medium text-ink [overflow-wrap:anywhere]">
                      <Link href={`/dashboard/pets/${pet.id}`} className="text-ink hover:underline">
                        {pet.name}
                      </Link>
                    </Table.Td>
                    <Table.Td className="whitespace-nowrap text-ink-2">{pet.species || "\u2014"}</Table.Td>
                    <Table.Td className="text-ink-2 [overflow-wrap:anywhere]">{pet.breed || "\u2014"}</Table.Td>
                    {/* The age, from the shared helper: never a Date on a date-only string. */}
                    <Table.Td className="whitespace-nowrap text-ink-2">{ageColumn(pet, today)}</Table.Td>
                    <Table.Td className="text-ink-2 [overflow-wrap:anywhere]">{vetName(pet) || "\u2014"}</Table.Td>
                    <Table.Td className="text-ink-3">
                      <IconChevronRight size={18} stroke={STROKE} aria-hidden />
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </div>
        </>
      )}

      <AddPetModal opened={showAdd} onClose={() => setShowAdd(false)} />
    </div>
  );
}
