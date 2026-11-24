"use client";

/**
 * One person (#4565) — household details, allergies and preferences, links to
 * what they are part of, plus edit and delete. Mobile-first per the People &
 * House mockup (artifact #89, the person's screen: Nora) inside the
 * DetailPageLayout frame (#4531) that the property (#4578), asset (#4554),
 * vehicle (#4555) and pet (#4556) details use.
 *
 * WHAT THE MOCKUP ASKS FOR, AND WHERE EACH PART COMES FROM
 *  - The head: a colour dot with their initials and "Daughter · 14" under the
 *    name. Both are from the record (people.color, #4821); the age is whole
 *    calendar years against the family's today, read after mount.
 *  - ALLERGIES FIRST, because on a child that is the line that matters: the
 *    mockup puts "Peanuts — carries an EpiPen" above everything. Every entry is
 *    printed with its words and, when it carries a note, that note too.
 *  - Preferences next, grouped by kind (likes, dislikes, dietary, other) from
 *    GET /people/{id}'s embedded `preferences`.
 *  - Then the household facts: relationship, birthday, school and grade, notes.
 *  - Resources (files, notes, secrets) come with the frame, role-gated (#4564:
 *    notes on a person, a housesitter reads them and may add one).
 *  - "Their documents" and "Their events" are links out, not lists: the
 *    Documents hub filters by person_id and the calendar by member, and both
 *    already do it better than a fourth list here would (see the notes at the
 *    links). Nothing is invented for a surface that does not exist.
 *
 * SAVING. PUT /people/{id} replaces every editable field, so the body is built
 * by personEditBody(), which ALWAYS sends the current allergies. Leaving them
 * out clears them — the manager note on this ticket, and the reason
 * person-facts.test.mjs proves a rename keeps them. The allergies have their
 * own field in the form, so they can also be edited deliberately; what the
 * field holds when Save is pressed is exactly what is sent.
 *
 * DELETING. DELETE answers 409 CONFIRM_REQUIRED with counts of what points at
 * the person (notes, documents, list items, meal ratings, events) when
 * anything does (#4743). The dialog shows those counts, says plainly that
 * notes are DELETED with the person, and repeats the call with ?confirm=true
 * only after the person agrees. A 409 LINKED_MEMBER is a different answer and
 * is shown as itself: remove the member, not the person.
 */

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Skeleton, Stack, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconCalendarEvent,
  IconFolder,
  IconUser,
} from "@tabler/icons-react";
import { ApiError, apiFetch } from "@/lib/api";
import { personKeys } from "@/lib/query-keys";
import { householdToday } from "@/lib/today/agenda";
import { useCurrentHousehold, useNavRole } from "@/components/shared/use-nav-role";
import { canManageMembers, paletteVar } from "@/lib/members/roles";
import DetailPageLayout from "@/components/layout/DetailPageLayout";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import { TextInput } from "@/components/ui";
import {
  allergyEntries,
  allergyNote,
  bareAllergen,
  bornValue,
  initials,
  personEditBody,
  personSubtitle,
  preferenceCategory,
  preferenceGroups,
  relationLabel,
  schoolValue,
  splitAllergies,
  type PersonLike,
  type PreferenceLike,
} from "@/components/person/person-facts";

const STROKE = 1.75;

/** A person as GET /api/v1/people/{id} returns it: the list row plus preferences. */
interface Person extends PersonLike {
  preferences?: PreferenceLike[];
}

/** The 409 CONFIRM_REQUIRED body: the counts of what points at the person. */
interface ReferenceCounts {
  notes: number;
  documents: number;
  list_items: number;
  meal_ratings: number;
  events: number;
}

/** The editable fields, in form order. */
const EDIT_FIELDS = ["display_name", "relationship", "birthdate", "school", "grade", "notes"] as const;

const LABELS: Record<string, string> = {
  display_name: "Name",
  relationship: "Relationship",
  birthdate: "Birthday",
  school: "School",
  grade: "Grade",
  notes: "Notes",
};

/**
 * Edit-mode groups, so the form is not one long wall on a phone. Allergies are
 * edited in their own textarea and appended by the form, not listed here.
 */
const FORM_GROUPS: { title?: string; fields: readonly string[] }[] = [
  { title: "Who they are", fields: ["display_name", "relationship", "birthdate"] },
  { title: "School", fields: ["school", "grade"] },
  { fields: ["notes"] },
];

/** A read-only row, per the detail-page field contract (#4531). */
function Row({
  field,
  label,
  value,
  hideLabel = false,
}: {
  field: string;
  label: string;
  value: React.ReactNode;
  hideLabel?: boolean;
}) {
  return (
    <div
      data-field={field}
      className="flex flex-col gap-1 py-2.5 md:grid md:grid-cols-[minmax(7rem,1fr)_minmax(0,2fr)] md:items-baseline md:gap-x-6"
    >
      <dt className={hideLabel ? "sr-only" : "text-sm font-medium text-ink-3"}>{label}</dt>
      <dd className="m-0 whitespace-pre-line text-sm text-ink [overflow-wrap:anywhere]">{value}</dd>
    </div>
  );
}

/** The 409 body's counts as a sentence, and the same counts as a confirm message. */
function referenceLines(refs: ReferenceCounts): { lines: string[]; warn: string | null } {
  const lines: string[] = [];
  if (refs.notes > 0) lines.push(`${refs.notes} ${refs.notes === 1 ? "note" : "notes"} about them`);
  if (refs.documents > 0) lines.push(`${refs.documents} ${refs.documents === 1 ? "document" : "documents"}`);
  if (refs.list_items > 0) lines.push(`${refs.list_items} list ${refs.list_items === 1 ? "item" : "items"}`);
  if (refs.meal_ratings > 0)
    lines.push(`${refs.meal_ratings} meal ${refs.meal_ratings === 1 ? "rating" : "ratings"}`);
  if (refs.events > 0) lines.push(`${refs.events} ${refs.events === 1 ? "event" : "events"}`);
  // Notes are the ones that are LOST: the API deletes them with the person and
  // only unlinks the rest (api/people-preferences.md). Saying so is the whole
  // point of asking again, so it is never buried.
  const warn =
    refs.notes > 0
      ? `Their ${refs.notes === 1 ? "note is" : "notes are"} deleted with them. Everything else stays, unlinked.`
      : "Nothing is deleted except the person and their preferences.";
  return { lines, warn };
}

/** "documents" / "events" links: the existing hubs, filtered by this person. */
function RelatedLinks({ person }: { person: Person }) {
  // The Documents hub already narrows by person (GET /files?person_id=), and
  // the calendar already narrows by member; linking beats a fourth list that
  // would disagree with them. A person who is not a member has no calendar
  // filter (the API's `member` takes a user id or a person id), so both links
  // are shown and each surface decides.
  return (
    <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap">
      <Link
        href={`/dashboard/documents?person=${person.id}`}
        className="inline-flex min-h-11 items-center gap-2 rounded-token-md border border-line bg-surface px-4 text-sm font-medium text-ink shadow-token-sm transition-colors hover:bg-surface-2"
      >
        <IconFolder size={18} stroke={STROKE} className="text-ink-3" aria-hidden />
        Their documents
      </Link>
      <Link
        href={`/dashboard/activity?member=${person.id}`}
        className="inline-flex min-h-11 items-center gap-2 rounded-token-md border border-line bg-surface px-4 text-sm font-medium text-ink shadow-token-sm transition-colors hover:bg-surface-2"
      >
        <IconCalendarEvent size={18} stroke={STROKE} className="text-ink-3" aria-hidden />
        Their activity
      </Link>
    </div>
  );
}

export default function PersonDetailPage() {
  const params = useParams<{ id: string }>();
  const id = typeof params?.id === "string" ? params.id : "";
  const router = useRouter();
  const queryClient = useQueryClient();
  const household = useCurrentHousehold();
  const role = useNavRole();
  const mayManage = canManageMembers(role);

  const [isEditing, setIsEditing] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const [allergyDraft, setAllergyDraft] = useState("");
  const [nameError, setNameError] = useState<string | undefined>();
  const [refs, setRefs] = useState<ReferenceCounts | null>(null);
  const [linkedMember, setLinkedMember] = useState(false);

  // Today in the FAMILY's zone, after mount: an age is only right on the
  // family's day. The server's clock is not it, so it is never read in render.
  const [today, setToday] = useState<string | null>(null);
  useEffect(() => {
    setToday(householdToday(household?.timezone ?? null));
  }, [household?.timezone]);

  const personQuery = useQuery({
    queryKey: personKeys.detail(id),
    queryFn: () => apiFetch<{ data: Person }>(`/api/v1/people/${id}`),
    enabled: !!id,
  });

  const person = personQuery.data?.data ?? null;
  const preferences = useMemo(() => person?.preferences ?? [], [person]);
  const groups = useMemo(() => preferenceGroups(preferences), [preferences]);
  const allergies = useMemo(() => (person ? allergyEntries(person) : []), [person]);
  const born = person ? bornValue(person, today) : "";
  const schoolAndGrade = person ? schoolValue(person) : "";
  const subtitle = person ? personSubtitle(person, today) : "";

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Person }>(`/api/v1/people/${id}`, { method: "PUT", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: personKeys.all });
      setIsEditing(false);
      notifications.show({ message: "Saved", color: "teal" });
    },
  });

  const remove = useMutation({
    mutationFn: (confirm: boolean) =>
      apiFetch<void>(`/api/v1/people/${id}${confirm ? "?confirm=true" : ""}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: personKeys.all });
      notifications.show({ message: "Deleted", color: "teal" });
      router.push("/dashboard/people");
    },
    onError: (err: Error) => {
      // 409 is the API asking a question, not a failure: read which one.
      if (err instanceof ApiError && err.status === 409) {
        const body = err.body as
          | { error?: { code?: string; message?: string }; references?: ReferenceCounts }
          | undefined;
        if (body?.error?.code === "CONFIRM_REQUIRED" && body.references) {
          setRefs(body.references);
          setLinkedMember(false);
          setShowDelete(true);
          return;
        }
        if (body?.error?.code === "LINKED_MEMBER") {
          setLinkedMember(true);
          setRefs(null);
          setShowDelete(true);
          return;
        }
      }
      notifications.show({
        title: "Couldn't delete them",
        message: err instanceof Error ? err.message : "Try again",
        color: "red",
      });
    },
  });

  const startEdit = () => {
    if (!person) return;
    const next: Record<string, string> = {};
    for (const f of EDIT_FIELDS) {
      const v = person[f as keyof Person];
      next[f] = v == null ? "" : String(v);
    }
    // The HTML date input wants YYYY-MM-DD; the API may return a timestamp.
    if (next.birthdate) next.birthdate = next.birthdate.slice(0, 10);
    setValues(next);
    // The allergies open as one per line, exactly as they are stored, so an
    // untouched Save sends them straight back.
    setAllergyDraft(allergies.join("\n"));
    setNameError(undefined);
    save.reset();
    setIsEditing(true);
  };

  const submit = () => {
    const name = (values.display_name ?? "").trim();
    if (!name) {
      setNameError("A name is required");
      return;
    }
    // The allergies come from the form when it was touched, otherwise from the
    // record — and either way they are IN the body (see personEditBody).
    const edited = splitAllergies(allergyDraft);
    save.mutate(personEditBody(values, edited));
  };

  /** Ask first; the API answers with the counts (or "remove the member"). */
  const askDelete = () => {
    setRefs(null);
    setLinkedMember(false);
    remove.mutate(false);
  };

  const closeDelete = () => {
    if (remove.isPending) return;
    setShowDelete(false);
    setRefs(null);
    setLinkedMember(false);
  };

  const title = person?.display_name ?? "Person";
  const avatarBg = person ? paletteVar(person.color) : null;

  /** The head the mockup draws between the title bar and the sections. */
  const head =
    person && !isEditing ? (
      <div className="flex items-center gap-3.5 pb-4" data-person-head>
        <span
          aria-hidden
          className={
            "grid size-14 shrink-0 place-items-center rounded-full text-base font-semibold " +
            (avatarBg ? "text-white" : "bg-accent-soft text-accent")
          }
          style={avatarBg ? { background: avatarBg } : undefined}
        >
          {initials(person.display_name)}
        </span>
        <div className="min-w-0">
          <h3 className="text-[22px] font-semibold leading-tight tracking-tight text-ink [overflow-wrap:anywhere]">
            {person.display_name}
          </h3>
          <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{subtitle || "No details yet"}</p>
        </div>
      </div>
    ) : null;

  const view = person && (
    <div className="flex flex-col gap-5">
      {/* Allergies first: the one thing whoever feeds this child must read. */}
      <div>
        <h3 className="text-sm font-semibold text-ink-2">Allergies</h3>
        {allergies.length > 0 ? (
          <ul className="m-0 mt-2 flex list-none flex-col gap-1.5 p-0 text-sm leading-relaxed" data-person-allergies>
            {allergies.map((entry) => {
              // The stored entry may be "peanuts (carries an EpiPen)": the
              // allergen leads in the emphasis, its note follows in words, so
              // the note is printed once and never twice.
              const note = allergyNote(entry);
              return (
                <li key={entry} className="flex items-start gap-1.5 [overflow-wrap:anywhere]">
                  <IconAlertTriangle size={16} stroke={2} className="mt-0.5 shrink-0 text-danger" aria-hidden />
                  <span className="font-medium text-danger">
                    {bareAllergen(entry)}
                    {note && <span className="font-normal text-ink-2"> — {note}</span>}
                  </span>
                </li>
              );
            })}
          </ul>
        ) : (
          <p className="mt-2 text-sm text-ink-3">
            No allergies recorded. Add one if it matters — it shows on their card too.
          </p>
        )}
      </div>

      {groups.length > 0 && (
        <div>
          <h3 className="text-sm font-semibold text-ink-2">Preferences</h3>
          <dl className="m-0 mt-1 divide-y divide-line" data-person-preferences>
            {groups.map((group) => (
              <Row
                key={group.kind}
                field={`preferences_${group.kind}`}
                label={group.label}
                value={
                  <ul className="m-0 list-none p-0">
                    {group.items.map((pref) => {
                      const category = preferenceCategory(pref.category);
                      return (
                        <li key={pref.id} className="[overflow-wrap:anywhere]">
                          {pref.value}
                          {category && <span className="text-ink-3"> · {category}</span>}
                          {pref.notes && <span className="block text-sm text-ink-3">{pref.notes}</span>}
                        </li>
                      );
                    })}
                  </ul>
                }
              />
            ))}
          </dl>
        </div>
      )}

      <div>
        <h3 className="text-sm font-semibold text-ink-2">Details</h3>
        <dl className="m-0 divide-y divide-line">
          <Row field="relationship" label="Relationship" value={relationLabel(person.relationship) || "\u2014"} />
          <Row field="birthdate" label="Birthday" value={born} />
          <Row field="school" label="School" value={schoolAndGrade || "\u2014"} />
          <Row field="notes" label="Notes" value={person.notes || "\u2014"} />
        </dl>
      </div>

      <div>
        <h3 className="text-sm font-semibold text-ink-2">Their things</h3>
        <div className="mt-2">
          <RelatedLinks person={person} />
        </div>
      </div>
    </div>
  );

  const allergyCount = splitAllergies(allergyDraft).length;
  const edit = (
    <Stack
      gap="md"
      className="max-md:[&_input]:min-h-11 max-md:[&_input]:text-base max-md:[&_textarea]:min-h-11 max-md:[&_textarea]:text-base"
    >
      {FORM_GROUPS.map((group) => (
        <div
          key={group.title ?? "notes"}
          className="flex flex-col gap-3 max-md:rounded-token-md max-md:border max-md:border-line max-md:bg-surface max-md:p-4 max-md:shadow-token-sm"
        >
          {group.title && <h3 className="text-sm font-semibold text-ink-2">{group.title}</h3>}
          {group.fields.map((name) => {
            if (name === "notes") {
              return (
                <Textarea
                  key={name}
                  label={LABELS[name]}
                  minRows={3}
                  autosize
                  value={values.notes ?? ""}
                  placeholder="Anything a sitter should know that is not a medical fact"
                  onChange={(e) => {
                    const v = e.currentTarget.value;
                    setValues((prev) => ({ ...prev, notes: v }));
                  }}
                />
              );
            }
            return (
              <TextInput
                key={name}
                label={LABELS[name]}
                required={name === "display_name"}
                error={name === "display_name" ? nameError : undefined}
                type={name === "birthdate" ? "date" : "text"}
                value={values[name] ?? ""}
                data-autofocus={name === "display_name" ? true : undefined}
                onChange={(e) => {
                  const v = e.currentTarget.value;
                  setValues((prev) => ({ ...prev, [name]: v }));
                  if (name === "display_name") setNameError(undefined);
                }}
              />
            );
          })}
        </div>
      ))}

      {/* Allergies are edited IN THE SAME SAVE, and the body always carries
          whatever this holds: a PUT that left them out would clear them. */}
      <div className="flex flex-col gap-2 max-md:rounded-token-md max-md:border max-md:border-line max-md:bg-surface max-md:p-4 max-md:shadow-token-sm">
        <Textarea
          label="Allergies"
          minRows={2}
          autosize
          value={allergyDraft}
          placeholder={"peanuts\nkiwi (raw only)"}
          description="One per line. Notes go in brackets: peanuts (carries an EpiPen)."
          onChange={(e) => setAllergyDraft(e.currentTarget.value)}
        />
        <p className="text-[13px] text-ink-3">
          {allergyCount === 1 ? "1 allergy" : `${allergyCount} allergies`} saved with this person.
        </p>
      </div>

      {save.isError && (
        <p role="alert" className="text-sm text-danger">
          Couldn&apos;t save: {save.error instanceof Error ? save.error.message : "try again"}
        </p>
      )}
    </Stack>
  );

  // The confirm message: the API's counts when it gave them (CONFIRM_REQUIRED),
  // or an honest sentence when it did not (LINKED_MEMBER).
  const refLines = refs ? referenceLines(refs) : null;
  const deleteMessage = linkedMember
    ? "This person is a household member, so they cannot be deleted. Remove the member in Members instead — that unlinks the person, and then this page can delete them."
    : refs && refLines
      ? [
          `${title} still has ${refLines.lines.join(", ")}.`,
          refLines.warn ?? "",
          `Delete ${title} anyway? This cannot be undone.`,
        ]
          .filter(Boolean)
          .join(" ")
      : `Delete ${title}? This cannot be undone.`;

  return (
    <DetailPageLayout
      entityType="person"
      entityId={id}
      backHref="/dashboard/people"
      backLabel="Back to People"
      title={isEditing ? `Edit ${title}` : title}
      isEditing={isEditing}
      onEdit={startEdit}
      onDelete={mayManage ? askDelete : undefined}
      onCancel={() => setIsEditing(false)}
      onSave={submit}
      isSaving={save.isPending}
    >
      {!isEditing && head}

      {personQuery.isLoading && (
        <Stack role="status" aria-busy="true" aria-label="Loading person">
          <Skeleton height={64} radius="sm" />
          <Skeleton height={140} radius="sm" />
        </Stack>
      )}
      {personQuery.isError && (
        <Alert color="red" title="Couldn't load this person">
          {personQuery.error instanceof Error ? personQuery.error.message : "Try again in a moment."}
          <Button
            variant="default"
            size="sm"
            mt="sm"
            onClick={() => personQuery.refetch()}
            loading={personQuery.isRefetching}
          >
            Try again
          </Button>
        </Alert>
      )}
      {!personQuery.isLoading && !personQuery.isError && !person && (
        <p className="flex items-center gap-2 text-sm text-ink-3">
          <IconUser size={18} stroke={STROKE} aria-hidden />
          No person found with that id.
        </p>
      )}
      {person && (isEditing ? edit : view)}

      <ConfirmDialog
        open={showDelete}
        onClose={closeDelete}
        onConfirm={() => {
          if (linkedMember) return;
          // A counted 409 is the API's "yes, if you mean it": repeat with
          // confirm=true. Nothing was deleted by the first call.
          remove.mutate(refs != null);
        }}
        title={linkedMember ? "Can't delete a member" : "Delete person"}
        message={deleteMessage}
        confirmLabel={linkedMember ? "Close" : "Delete"}
        loading={remove.isPending}
      />
    </DetailPageLayout>
  );
}
