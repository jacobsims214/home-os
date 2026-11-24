"use client";

/**
 * People list (#4565), mobile-first per the People & House mockup (artifact
 * #89, the Family section) in the pattern the properties (#4553), assets
 * (#4554), vehicles (#4555) and pets (#4556) lists use.
 *
 * - Phone (< 768 px): stacked cards, each one link with a chevron and a 44 px+
 *   tap target; nothing clips at 375 px. A card carries the mockup's readings:
 *   the name in the person's own colour, "Daughter · 14", and "Allergic to
 *   peanuts" when they have an allergy — the line a sitter must not have to
 *   open the record to find.
 * - >= 768 px: a table in a horizontal scroller, first cell a real link.
 * - "Add person" opens the create modal (ResponsiveModal: a bottom sheet on
 *   phones) and POSTs /api/v1/people. There is no /new route (the #4509 audit
 *   bug: a /new path renders an empty detail page).
 *
 * WHY NOT EntityList
 * EntityList defaults `propertyFilter` ON, and people have no `property_id`,
 * so the filter emptied this list (the #4556 Pets bug, the manager note on
 * this ticket, and the pattern doc's "Known gaps"). Its generic card also
 * cannot show an allergy line or the person's colour. This page does the list
 * itself, exactly as the vehicles and pets lists do.
 *
 * ROLE. Every member may read people (api/people-preferences.md); only owner
 * and family_manager may create one, so "Add person" is drawn only for a
 * manager. The API refuses regardless — the UI only avoids drawing a button
 * that would 403.
 *
 * DATES. `birthdate` is a DATE. The age is worked out in
 * components/person/person-facts.ts from the string's own parts (never
 * `new Date("YYYY-MM-DD")`: #4550's bug class) against the household's today,
 * read after mount, so the server and the client cannot disagree about the day.
 */

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton, Table, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconChevronRight, IconPlus, IconUsers } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { personKeys } from "@/lib/query-keys";
import { householdToday } from "@/lib/today/agenda";
import { useCurrentHousehold, useNavRole } from "@/components/shared/use-nav-role";
import { canManageMembers, paletteVar } from "@/lib/members/roles";
import { ResponsiveModal, ResponsiveModalActions, TextInput } from "@/components/ui";
import {
  allergySummary,
  initials,
  personAge,
  personCreateBody,
  personSubtitle,
  splitAllergies,
  type PersonLike,
} from "@/components/person/person-facts";

const STROKE = 1.75;

/** The person's colour dot; the neutral fallback when the value is not a palette name. */
function Avatar({ person }: { person: PersonLike }) {
  const bg = paletteVar(person.color);
  return (
    <span
      aria-hidden
      className={
        "grid size-11 shrink-0 place-items-center rounded-full text-[13px] font-semibold " +
        (bg ? "text-white" : "bg-accent-soft text-accent")
      }
      style={bg ? { background: bg } : undefined}
    >
      {initials(person.display_name)}
    </span>
  );
}

/** One person, as the mockup's list row draws it: a colour dot, the name, and what matters. */
function PersonCard({ person, today }: { person: PersonLike; today: string | null }) {
  const subtitle = personSubtitle(person, today);
  const allergies = allergySummary(person);

  return (
    <Link
      href={`/dashboard/people/${person.id}`}
      data-person-card={person.id}
      className="block min-h-11 rounded-token-md border border-line bg-surface shadow-token-sm transition-colors hover:bg-surface-2 active:bg-surface-2"
    >
      <div className="flex items-center gap-3 p-4">
        <Avatar person={person} />
        <div className="min-w-0 flex-1">
          <h3 className="text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">
            {person.display_name}
          </h3>
          {subtitle && <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{subtitle}</p>}
        </div>
        <IconChevronRight size={20} stroke={STROKE} className="shrink-0 text-ink-3" aria-hidden />
      </div>
      {allergies && (
        <div className="border-t border-line px-4 py-2.5">
          {/* Words, then colour: the warning is never colour alone
              (KB design/accessibility-and-responsive.md). */}
          <p className="m-0 flex items-start gap-1.5 text-xs font-medium text-danger [overflow-wrap:anywhere]">
            <IconAlertTriangle size={14} stroke={2} className="mt-0.5 shrink-0" aria-hidden />
            {allergies}
          </p>
        </div>
      )}
    </Link>
  );
}

/** "Add person": a name is all the API requires; the rest is edited on their page. */
function AddPersonModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [relationship, setRelationship] = useState("");
  const [allergies, setAllergies] = useState("");
  const [error, setError] = useState<string | null>(null);

  const close = () => {
    setName("");
    setRelationship("");
    setAllergies("");
    setError(null);
    create.reset();
    onClose();
  };

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: PersonLike }>("/api/v1/people", { method: "POST", body }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: personKeys.all });
      notifications.show({ message: "Person added", color: "teal" });
      const id = res?.data?.id;
      if (!id) return close();
      // Navigate without closing first: closing pops the sheet's history entry,
      // and the router RESTORE that triggers cancels the push (#4532).
      router.replace(`/dashboard/people/${id}`);
    },
    onError: (err: Error) => setError(err.message || "Couldn't add the person. Try again."),
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const who = name.trim();
    if (!who) {
      setError("A name is required");
      return;
    }
    setError(null);
    create.mutate(
      personCreateBody(
        { display_name: who, relationship, birthdate: "", school: "", grade: "", notes: "" },
        splitAllergies(allergies),
      ),
    );
  };

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add person">
      <form onSubmit={submit} noValidate>
        <div className="flex flex-col gap-3">
          <TextInput
            label="Name"
            required
            data-autofocus
            placeholder="e.g. Nora Sims"
            value={name}
            error={error ?? undefined}
            onChange={(e) => {
              setName(e.currentTarget.value);
              setError(null);
            }}
          />
          <TextInput
            label="Relationship"
            placeholder="e.g. daughter, grandma, sitter"
            description="Optional. How this person fits in the family."
            value={relationship}
            onChange={(e) => setRelationship(e.currentTarget.value)}
          />
          <Textarea
            label="Allergies"
            minRows={2}
            autosize
            placeholder={"peanuts\nkiwi"}
            description="Optional. One per line — it goes on their card and their page."
            value={allergies}
            onChange={(e) => setAllergies(e.currentTarget.value)}
          />
          <p className="text-[13px] text-ink-3">
            Birthday, school, grade, notes and preferences are on their page once they exist.
          </p>
        </div>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Add person
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

export default function PeoplePage() {
  const [showAdd, setShowAdd] = useState(false);
  const household = useCurrentHousehold();
  const role = useNavRole();
  const mayManage = canManageMembers(role);

  // Today in the FAMILY's zone, after mount (the server's clock is not the
  // family's): an age is a birthday comparison, so a wrong day is a wrong age.
  const [today, setToday] = useState<string | null>(null);
  useEffect(() => {
    setToday(householdToday(household?.timezone ?? null));
  }, [household?.timezone]);

  const peopleQuery = useQuery({
    queryKey: personKeys.list(),
    queryFn: () => apiFetch<{ data: PersonLike[] }>("/api/v1/people"),
  });

  const people = peopleQuery.data?.data ?? [];

  return (
    <div className="mx-auto w-full max-w-6xl">
      <div className="mb-4 md:mb-6">
        <div className="flex items-center justify-between gap-3">
          <h2 className="min-w-0 text-xl font-semibold text-ink md:text-2xl">
            People
            {!peopleQuery.isLoading && !peopleQuery.isError && people.length > 0 && (
              <span className="ml-2 align-middle text-base font-medium text-ink-3">{people.length}</span>
            )}
          </h2>
          {mayManage && (
            <Button
              onClick={() => setShowAdd(true)}
              leftSection={<IconPlus size={16} />}
              aria-haspopup="dialog"
              className="shrink-0 max-md:min-h-11"
            >
              Add person
            </Button>
          )}
        </div>
        <p className="mt-1 text-sm text-ink-3">
          Everyone in the family, including the kids — their birthdays, schools and allergies.
        </p>
      </div>

      {peopleQuery.isLoading && (
        <div role="status" aria-busy="true" aria-label="Loading people" className="space-y-2">
          {[1, 2, 3].map((i) => (
            <div key={i} className="space-y-2 rounded-token-md border border-line bg-surface p-4 shadow-token-sm">
              <Skeleton height={14} width="50%" radius="sm" />
              <Skeleton height={12} width="70%" radius="sm" />
            </div>
          ))}
        </div>
      )}

      {peopleQuery.isError && (
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">Couldn&apos;t load your people. Check your connection and try again.</p>
          <Button
            variant="default"
            onClick={() => peopleQuery.refetch()}
            loading={peopleQuery.isRefetching}
            className="max-md:min-h-11"
          >
            Try again
          </Button>
        </div>
      )}

      {!peopleQuery.isLoading && !peopleQuery.isError && people.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <IconUsers size={28} stroke={STROKE} className="text-ink-3" aria-hidden />
          <p className="max-w-prose text-sm text-ink-3">
            Nobody here yet. Add the people in your household — their birthday, school and allergies live on
            their own page.
          </p>
          {mayManage && (
            <Button
              variant="light"
              onClick={() => setShowAdd(true)}
              leftSection={<IconPlus size={16} />}
              className="max-md:min-h-11"
            >
              Add person
            </Button>
          )}
        </div>
      )}

      {!peopleQuery.isLoading && !peopleQuery.isError && people.length > 0 && (
        <>
          <ul aria-label="People" className="space-y-2 md:hidden">
            {people.map((person) => (
              <li key={person.id} className="min-w-0">
                <PersonCard person={person} today={today} />
              </li>
            ))}
          </ul>

          <div
            data-person-table
            className="overflow-x-auto rounded-token-md border border-line bg-surface shadow-token-sm max-md:hidden"
          >
            <Table highlightOnHover verticalSpacing="sm" horizontalSpacing="md" className="min-w-[36rem]">
              <Table.Thead>
                <Table.Tr>
                  {["Name", "Relationship", "Age", "Allergies"].map((label) => (
                    <Table.Th
                      key={label}
                      scope="col"
                      className="whitespace-nowrap text-left text-xs font-semibold uppercase tracking-wide text-ink-3"
                    >
                      {label}
                    </Table.Th>
                  ))}
                  <Table.Th className="w-10">
                    <span className="sr-only">Open</span>
                  </Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {people.map((person) => (
                  <Table.Tr key={person.id}>
                    <Table.Td className="min-w-[10rem] max-w-[22rem] font-medium text-ink [overflow-wrap:anywhere]">
                      <span className="flex items-center gap-2">
                        <Avatar person={person} />
                        <Link href={`/dashboard/people/${person.id}`} className="text-ink hover:underline">
                          {person.display_name}
                        </Link>
                      </span>
                    </Table.Td>
                    <Table.Td className="whitespace-nowrap text-ink-2">{person.relationship || "\u2014"}</Table.Td>
                    {/* The age, from the shared helper: never a Date on a date-only string. */}
                    <Table.Td className="whitespace-nowrap text-ink-2">{ageColumn(person, today)}</Table.Td>
                    <Table.Td className="text-ink-2 [overflow-wrap:anywhere]">
                      {allergySummary(person).replace(/^Allergic to /, "") || "\u2014"}
                    </Table.Td>
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

      <AddPersonModal opened={showAdd} onClose={() => setShowAdd(false)} />
    </div>
  );
}

/** The Age cell: a number, a dash when unknown, and never a zone-shifted date. */
function ageColumn(person: PersonLike, today: string | null): string {
  return personAge(person, today) || "\u2014";
}
