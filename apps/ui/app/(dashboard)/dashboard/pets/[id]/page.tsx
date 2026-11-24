"use client";

/**
 * One pet (#4556) — detail, edit and delete, mobile-first per the People &
 * House mockup (artifact #89 v1, the "Pet" screen: Biscuit) inside the
 * DetailPageLayout frame (#4531), the same frame the property (#4578), asset
 * (#4554) and vehicle (#4555) details use.
 *
 * WHAT THE MOCKUP ASKS FOR, AND WHERE EACH PART COMES FROM
 *  - The pet's head: a paw, the name, and "Golden retriever · 6" under it on a
 *    phone. Breed and age are from the record; the age is whole calendar years
 *    against the family's today (components/pet/pet-care.ts), never a Date
 *    built from a date-only string.
 *  - Care notes FIRST, because on a pet that is the line that matters: the
 *    mockup puts "Allergic to chicken… Salmon kibble only" above everything.
 *    A sentence that is a warning (allergic, don't, never…) is emphasised with
 *    the danger token, and always with its words — never colour alone.
 *  - Vet next, with the contact and a Call button. The vet is a family contact
 *    tagged "vet" (pets.vet_vendor_id, #4703): the API returns its name and
 *    phone read-only (vet_name / vet_phone) and the form only ever sends
 *    vet_vendor_id. Editing the contact's phone from here would change it for
 *    every pet that uses that vet, so it is done in Contacts.
 *  - Then the details: species, breed, born, and identification (microchip,
 *    insurance, registration).
 *  - Resources (files, notes, secrets) come with the frame, role-gated (#4564):
 *    a housesitter sees notes on a pet and can add them, which is the mockup's
 *    "Ate all his dinner, pill taken" note.
 *
 * Every field the PUT sends is one this page owns. PetWriteRequest has no
 * vet_name / vet_phone (#4700, #4705 drops the columns), so they can never
 * leak back from the read-only display.
 */

import { useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Skeleton, Stack, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconPaw, IconPhone, IconStethoscope } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { petKeys } from "@/lib/query-keys";
import { householdToday } from "@/lib/today/agenda";
import DetailPageLayout from "@/components/layout/DetailPageLayout";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import { TextInput } from "@/components/ui";
import VetPicker from "@/components/pet/VetPicker";
import { useCurrentHousehold } from "@/components/shared/use-nav-role";
import {
  ageLabel,
  formatBirthday,
  isCareWarning,
  noteSentences,
  petSubtitle,
  petEditBody,
  telHref,
  vetName,
  vetPhone,
} from "@/components/pet/pet-care";

const STROKE = 1.75;

/** A pet as GET /api/v1/pets/{id} returns it (apps/api/internal/pet/handler.go). */
interface Pet {
  id: string;
  name: string;
  species?: string | null;
  breed?: string | null;
  date_of_birth?: string | null;
  vet_vendor_id?: string | null;
  vet_name?: string | null;
  vet_phone?: string | null;
  notes?: string | null;
}

/** The editable fields, in form order. `date` is a date-only string. */
const EDIT_FIELDS = ["name", "species", "breed", "date_of_birth", "notes"] as const;

/**
 * Edit-mode groups, so the form is not one long wall on a phone. A group with
 * no title has nothing to add: the field's own label already says it (the
 * notes field is labelled "Care notes", so a "Care notes" heading above it
 * would print the same words twice).
 */
const FORM_GROUPS: { title?: string; fields: readonly string[] }[] = [
  { title: "The pet", fields: ["name", "species", "breed", "date_of_birth"] },
  { fields: ["notes"] },
];

const LABELS: Record<string, string> = {
  name: "Name",
  species: "Species",
  breed: "Breed",
  date_of_birth: "Birthday",
  notes: "Care notes",
};

/**
 * A read-only row, per the detail-page field contract (#4531): label above the
 * value on a phone, a label/value grid from 768 px. `data-field` is the
 * handle the other detail specs use (e.g. pet-vet-picker's vet row).
 */
function Row({
  field,
  label,
  value,
  hideLabel = false,
}: {
  field: string;
  label: string;
  value: React.ReactNode;
  /** The section heading already says it, so it is not printed twice. */
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

/** A "born" line: the date and the age in years, both from the string's parts. */
function bornValue(pet: Pet, today: string | null): string {
  const birthday = formatBirthday(pet.date_of_birth);
  if (!birthday) return "\u2014";
  const age = today ? ageLabel(pet.date_of_birth, today) : "";
  return age ? `${birthday} (${age} old)` : birthday;
}

export default function PetDetailPage() {
  const params = useParams<{ id: string }>();
  const id = typeof params?.id === "string" ? params.id : "";
  const router = useRouter();
  const queryClient = useQueryClient();
  const household = useCurrentHousehold();

  const [isEditing, setIsEditing] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const [vetId, setVetId] = useState<string | null>(null);
  const [nameError, setNameError] = useState<string | undefined>();

  // Today in the FAMILY's zone, after mount: an age is only right on the
  // family's day. The server's clock is not it, so it is never read in render.
  const [today, setToday] = useState<string | null>(null);
  useEffect(() => {
    setToday(householdToday(household?.timezone ?? null));
  }, [household?.timezone]);

  const petQuery = useQuery({
    queryKey: petKeys.detail(id),
    queryFn: () => apiFetch<{ data: Pet }>(`/api/v1/pets/${id}`),
    enabled: !!id,
  });

  const pet = petQuery.data?.data ?? null;

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Pet }>(`/api/v1/pets/${id}`, { method: "PUT", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: petKeys.all });
      setIsEditing(false);
      notifications.show({ message: "Saved", color: "teal" });
    },
  });

  const remove = useMutation({
    mutationFn: () => apiFetch<void>(`/api/v1/pets/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: petKeys.all });
      notifications.show({ message: "Deleted", color: "teal" });
      router.push("/dashboard/pets");
    },
    onError: (err: Error) =>
      notifications.show({ title: "Couldn't delete it", message: err.message, color: "red" }),
  });

  const startEdit = () => {
    if (!pet) return;
    const next: Record<string, string> = {};
    for (const f of EDIT_FIELDS) {
      const v = pet[f as keyof Pet];
      next[f] = v == null ? "" : String(v);
    }
    // The HTML date input wants YYYY-MM-DD; the API returns a timestamp.
    if (next.date_of_birth) next.date_of_birth = next.date_of_birth.slice(0, 10);
    setValues(next);
    setVetId(pet.vet_vendor_id ?? null);
    setNameError(undefined);
    setIsEditing(true);
  };

  const submit = () => {
    const name = (values.name ?? "").trim();
    if (!name) {
      setNameError("A name is required");
      return;
    }
    save.mutate(petEditBody(values, vetId));
  };

  const title = pet?.name ?? "Pet";
  const subtitle = pet ? petSubtitle(pet, today) : "";
  const vet = pet ? vetName(pet) : "";
  const phone = pet ? vetPhone(pet) : "";
  const notes = pet ? noteSentences(pet.notes) : [];

  /** The head the mockup draws between the title bar and the sections. */
  const head = pet && !isEditing ? (
    <div className="flex items-center gap-3.5 pb-4" data-pet-head>
      <span aria-hidden className="grid size-14 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent">
        <IconPaw size={28} stroke={STROKE} />
      </span>
      <div className="min-w-0">
        <h3 className="text-[22px] font-semibold leading-tight tracking-tight text-ink [overflow-wrap:anywhere]">{pet.name}</h3>
        {/* Breed and age only: the vet is the Vet section's job one line below,
            and the mockup's head line carries the next vet visit, which the API
            does not return. This says what the record does have. */}
        <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{subtitle || "No details yet"}</p>
      </div>
    </div>
  ) : null;

  const view = pet && (
    <div className="flex flex-col gap-5">
      {/* Care notes first: the one thing whoever feeds this pet must read. */}
      <div>
        <h3 className="text-sm font-semibold text-ink-2">Care notes</h3>
        {notes.length > 0 ? (
          <div className="mt-2 flex flex-col gap-1.5 text-sm leading-relaxed" data-pet-notes>
            {notes.map((sentence) => (
              <p
                key={sentence}
                className={
                  isCareWarning(sentence)
                    ? "m-0 font-medium text-danger [overflow-wrap:anywhere]"
                    : "m-0 text-ink [overflow-wrap:anywhere]"
                }
              >
                {sentence}
              </p>
            ))}
          </div>
        ) : (
          <p className="mt-2 text-sm text-ink-3">
            No care notes yet. Add what a sitter needs to know: food, medicine, what not to give them.
          </p>
        )}
      </div>

      {/* Vet: the contact, and one tap to call. */}
      <div>
        <h3 className="text-sm font-semibold text-ink-2">Vet</h3>
        <dl className="m-0 divide-y divide-line">
          <Row
            field="vet_vendor_id"
            label="Vet"
            hideLabel
            value={
              vet ? (
                <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="inline-flex items-center gap-1.5">
                    <IconStethoscope size={16} stroke={STROKE} className="shrink-0 text-ink-3" aria-hidden />
                    <span className="font-medium text-ink">{vet}</span>
                  </span>
                  {phone && (
                    <a
                      href={telHref(phone)}
                      className="inline-flex min-h-11 items-center gap-1.5 rounded-token-sm text-accent hover:underline"
                      aria-label={`Call ${vet}, ${phone}`}
                    >
                      <IconPhone size={16} stroke={STROKE} aria-hidden />
                      {phone}
                    </a>
                  )}
                </span>
              ) : (
                <span className="text-ink-3">
                  No vet chosen. Edit the pet to pick a contact tagged Vet, or add one there.
                </span>
              )
            }
          />
        </dl>
      </div>

      <div>
        <h3 className="text-sm font-semibold text-ink-2">Details</h3>
        <dl className="m-0 divide-y divide-line">
          <Row field="species" label="Species" value={pet.species || "\u2014"} />
          <Row field="breed" label="Breed" value={pet.breed || "\u2014"} />
          <Row field="date_of_birth" label="Born" value={bornValue(pet, today)} />
        </dl>
      </div>
    </div>
  );

  // NOT a <form>: the vet picker's sheet lives in here and its "Use this vet"
  // button is a submit, so a form here would save the pet when a vet is chosen.
  // The frame's Save is the only way to save (as on the vehicle page, #4555).
  // Phone inputs still get 44 px and 16 px text (no iOS zoom on focus).
  const edit = (
    <Stack gap="md" className="max-md:[&_input]:min-h-11 max-md:[&_input]:text-base max-md:[&_textarea]:min-h-11 max-md:[&_textarea]:text-base">
        {FORM_GROUPS.map((group) => (
          <div key={group.title ?? "notes"} className="flex flex-col gap-3 max-md:rounded-token-md max-md:border max-md:border-line max-md:bg-surface max-md:p-4 max-md:shadow-token-sm">
            {group.title && <h3 className="text-sm font-semibold text-ink-2">{group.title}</h3>}
            {group.fields.map((name) => {
              if (name === "notes") {
                return (
                  <Textarea
                    key={name}
                    label={LABELS[name]}
                    minRows={4}
                    autosize
                    value={values.notes ?? ""}
                    placeholder="Food, medicine, what not to give them, where the lead hangs"
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
                  required={name === "name"}
                  error={name === "name" ? nameError : undefined}
                  type={name === "date_of_birth" ? "date" : "text"}
                  value={values[name] ?? ""}
                  data-autofocus={name === "name" ? true : undefined}
                  onChange={(e) => {
                    const v = e.currentTarget.value;
                    setValues((prev) => ({ ...prev, [name]: v }));
                    if (name === "name") setNameError(undefined);
                  }}
                />
              );
            })}
          </div>
        ))}

        {/* The vet picker (a ResponsiveModal sheet): a family contact tagged
            vet, stored as vet_vendor_id. Never the contact's phone. */}
        <div className="rounded-token-md border border-line bg-surface p-4 shadow-token-sm md:border-0 md:bg-transparent md:p-0 md:shadow-none">
          <VetPicker
            value={vetId}
            onChange={setVetId}
            current={{ name: vet || null, phone: phone || null }}
          />

        </div>

        {save.isError && (
          <p role="alert" className="text-sm text-danger">
            Couldn&apos;t save: {save.error instanceof Error ? save.error.message : "try again"}
          </p>
        )}
      </Stack>
  );

  return (
    <DetailPageLayout
      entityType="pet"
      entityId={id}
      title={isEditing ? `Edit ${title}` : title}
      isEditing={isEditing}
      onEdit={startEdit}
      onDelete={() => setShowDelete(true)}
      onCancel={() => setIsEditing(false)}
      onSave={submit}
      isSaving={save.isPending}
    >
      {!isEditing && head}

      {petQuery.isLoading && (
        <Stack role="status" aria-busy="true" aria-label="Loading pet">
          <Skeleton height={64} radius="sm" />
          <Skeleton height={140} radius="sm" />
        </Stack>
      )}
      {petQuery.isError && (
        <Alert color="red" title="Couldn't load this pet">
          {petQuery.error instanceof Error ? petQuery.error.message : "Try again in a moment."}
          <Button variant="default" size="sm" mt="sm" onClick={() => petQuery.refetch()} loading={petQuery.isRefetching}>
            Try again
          </Button>
        </Alert>
      )}
      {!petQuery.isLoading && !petQuery.isError && !pet && (
        <p className="text-sm text-ink-3">No pet found with that id.</p>
      )}
      {pet && (isEditing ? edit : view)}

      <ConfirmDialog
        open={showDelete}
        onClose={() => setShowDelete(false)}
        onConfirm={() => remove.mutate()}
        title="Delete pet"
        message={`Delete ${title}? This cannot be undone.`}
        loading={remove.isPending}
      />
    </DetailPageLayout>
  );
}
