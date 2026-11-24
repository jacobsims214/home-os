"use client";

/**
 * The Add / Edit bill form (#4551), built to the money mockup (artifact #102).
 *
 * Three rules from the mockup and the API, and they are the whole point:
 *
 *  1. THE SCHEDULE FOLLOWS THE FREQUENCY (migration 044). A monthly bill takes
 *     a DAY of the month; every other frequency takes a DATE. The form swaps
 *     the one field for the other, so it can never send both (the API answers
 *     400 for the pair) and never sends the one that no longer applies.
 *  2. A BILL IS FOR ONE THING. Nothing, one home or one thing in a home, or
 *     one vehicle. Picking a vehicle clears the home and the reverse, because
 *     the database's bills_subject_check refuses both (the manager note on
 *     #4551, KB architecture/linked-finances-ui.md).
 *  3. NO AMOUNT IS NOT $0. An amount left empty sends no amount at all (the
 *     column is nullable and the mockup shows "No amount"), and it is never
 *     filled in with a zero.
 */

import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Button, Stack, Text, Textarea } from "@mantine/core";
import { ResponsiveModal, ResponsiveModalActions, Select, TextInput } from "@/components/ui";
import {
  FREQUENCY_OPTIONS,
  normaliseFrequency,
  scheduleBody,
  subjectBody,
  subjectKindOf,
  type Bill,
  type SubjectKindChoice,
} from "@/lib/bills/model";

export interface BillFormValues {
  name: string;
  amount: string;
  frequency: string;
  due_day: string;
  due_date: string;
  category: string;
  notes: string;
  is_autopay: string;
  account_number: string;
  payment_url: string;
  subject_kind: SubjectKindChoice;
  subject_id: string;
}

const CATEGORIES = [
  "Electric",
  "Water",
  "Gas",
  "Internet",
  "Trash",
  "Insurance",
  "Property Tax",
  "HOA",
  "Vehicle",
  "Subscription",
  "Other",
];

const SUBJECT_KINDS: { value: SubjectKindChoice; label: string }[] = [
  { value: "none", label: "Nothing" },
  { value: "property", label: "Home" },
  { value: "asset", label: "Thing in a home" },
  { value: "vehicle", label: "Vehicle" },
];

export interface SubjectOption {
  id: string;
  name: string;
}

export interface BillFormSubjects {
  properties: SubjectOption[];
  assets: SubjectOption[];
  vehicles: SubjectOption[];
}

export function emptyBillForm(): BillFormValues {
  return {
    name: "",
    amount: "",
    frequency: "monthly",
    due_day: "",
    due_date: "",
    category: "",
    notes: "",
    is_autopay: "",
    account_number: "",
    payment_url: "",
    subject_kind: "none",
    subject_id: "",
  };
}

/** A stored bill as form values (edit mode). */
export function billToForm(bill: Bill): BillFormValues {
  const kind = subjectKindOf(bill);
  const id =
    kind === "vehicle" ? bill.vehicle_id : kind === "asset" ? bill.asset_id : kind === "property" ? bill.property_id : null;
  return {
    name: bill.name ?? "",
    amount: bill.amount === null || bill.amount === undefined ? "" : String(bill.amount),
    frequency: normaliseFrequency(bill.frequency),
    due_day: bill.due_day ? String(bill.due_day) : "",
    due_date: (bill.due_date ?? "").slice(0, 10),
    category: bill.category ?? "",
    notes: bill.notes ?? "",
    is_autopay: bill.is_autopay === true ? "yes" : bill.is_autopay === false ? "no" : "",
    account_number: bill.account_number ?? "",
    payment_url: bill.payment_url ?? "",
    subject_kind: kind,
    subject_id: id ?? "",
  };
}

/**
 * The PUT/POST body from form values. Only the fields the form owns, and the
 * schedule and subject are built by the model's rules, not here.
 */
export function billFormBody(v: BillFormValues): Record<string, unknown> {
  const amount = v.amount.replace(/[$,\s]/g, "");
  const body: Record<string, unknown> = {
    name: v.name.trim(),
    ...scheduleBody(v.frequency, v.due_day, v.due_date),
    ...subjectBody(v.subject_kind, v.subject_id),
  };
  if (amount !== "" && /^\d+(\.\d{1,2})?$/.test(amount)) body.amount = amount;
  if (v.category.trim()) body.category = v.category.trim();
  if (v.notes.trim()) body.notes = v.notes.trim();
  if (v.account_number.trim()) body.account_number = v.account_number.trim();
  if (v.payment_url.trim()) body.payment_url = v.payment_url.trim();
  if (v.is_autopay === "yes" || v.is_autopay === "no") body.is_autopay = v.is_autopay === "yes";
  return body;
}

/** Field-level validation, in the form's own words. */
export function validateBillForm(v: BillFormValues): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!v.name.trim()) errors.name = "Enter a name";
  const amount = v.amount.replace(/[$,\s]/g, "");
  if (amount !== "" && !/^\d+(\.\d{1,2})?$/.test(amount)) errors.amount = "Amount must be a number, like 164.00";
  const f = normaliseFrequency(v.frequency);
  if (f === "monthly") {
    if (v.due_day.trim() === "") {
      // Allowed: a monthly bill with no day is legal (the rule reports no due
      // date and the list says so), so this is not an error — just no date.
    } else {
      const day = Number(v.due_day);
      if (!Number.isInteger(day) || day < 1 || day > 31) errors.due_day = "Day must be 1 to 31";
    }
  } else if (v.due_date.trim() !== "" && !/^\d{4}-\d{2}-\d{2}$/.test(v.due_date.slice(0, 10))) {
    errors.due_date = "Date must be YYYY-MM-DD";
  }
  if (v.subject_kind !== "none" && !v.subject_id) errors.subject_id = "Choose which one";
  return errors;
}

export default function BillForm({
  opened,
  onClose,
  title,
  initial,
  subjects,
  saving,
  error,
  onSubmit,
  onDelete,
}: {
  opened: boolean;
  onClose: () => void;
  title: string;
  initial: BillFormValues;
  subjects: BillFormSubjects;
  saving?: boolean;
  error?: string | null;
  onSubmit: (body: Record<string, unknown>) => void;
  onDelete?: () => void;
}) {
  const [values, setValues] = useState<BillFormValues>(initial);
  const [errors, setErrors] = useState<Record<string, string>>({});

  // Reopening on a different bill starts from that bill, not the last one.
  useEffect(() => {
    if (opened) {
      setValues(initial);
      setErrors({});
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [opened, initial.name, initial.due_day, initial.due_date, initial.subject_id]);

  const monthly = normaliseFrequency(values.frequency) === "monthly";
  const subjectOptions = useMemo(() => {
    if (values.subject_kind === "property") return subjects.properties;
    if (values.subject_kind === "asset") return subjects.assets;
    if (values.subject_kind === "vehicle") return subjects.vehicles;
    return [];
  }, [values.subject_kind, subjects]);

  const set = (name: keyof BillFormValues, value: string) => setValues((prev) => ({ ...prev, [name]: value }));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const found = validateBillForm(values);
    setErrors(found);
    if (Object.keys(found).length) return;
    onSubmit(billFormBody(values));
  };

  return (
    <ResponsiveModal opened={opened} onClose={onClose} title={title} size="md">
      <form onSubmit={submit}>
        <Stack gap="md">
          <TextInput
            label="Name"
            value={values.name}
            onChange={(e) => set("name", e.currentTarget.value)}
            error={errors.name}
            data-autofocus
            placeholder="Electric"
          />
          <div className="grid grid-cols-2 gap-3">
            <TextInput
              label="Amount"
              value={values.amount}
              onChange={(e) => set("amount", e.currentTarget.value)}
              error={errors.amount}
              inputMode="decimal"
              placeholder="164.00"
            />
            <Select
              label="How often"
              value={values.frequency}
              onChange={(v) => set("frequency", v ?? "monthly")}
              data={FREQUENCY_OPTIONS}
              allowDeselect={false}
            />
          </div>

          {monthly ? (
            <TextInput
              label="Due day"
              description="Which day of the month, 1 to 31. Leave it empty if the bill has no fixed day."
              value={values.due_day}
              onChange={(e) => set("due_day", e.currentTarget.value)}
              error={errors.due_day}
              inputMode="numeric"
              placeholder="22"
            />
          ) : (
            <TextInput
              label="Next due date"
              description="The date it is due next; it repeats from this date at the frequency above."
              value={values.due_date}
              onChange={(e) => set("due_date", e.currentTarget.value)}
              error={errors.due_date}
              type="date"
            />
          )}

          <div>
            <Select
              label="For"
              description="A bill is for one home, one thing in a home, or one vehicle."
              value={values.subject_kind}
              onChange={(v) => {
                set("subject_kind", (v ?? "none") as SubjectKindChoice);
                // Switching the kind clears the id: a property id is not an asset id.
                set("subject_id", "");
              }}
              data={SUBJECT_KINDS}
              allowDeselect={false}
            />
            {values.subject_kind !== "none" && (
              <div className="mt-3">
                <Select
                  label={values.subject_kind === "vehicle" ? "Vehicle" : values.subject_kind === "asset" ? "Thing" : "Home"}
                  value={values.subject_id}
                  onChange={(v) => set("subject_id", v ?? "")}
                  data={subjectOptions.map((s) => ({ value: s.id, label: s.name }))}
                  error={errors.subject_id}
                  placeholder={subjectOptions.length ? "Choose one" : "Nothing to choose from yet"}
                  searchable
                />
              </div>
            )}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <Select
              label="Category"
              value={values.category}
              onChange={(v) => set("category", v ?? "")}
              data={CATEGORIES}
              searchable
              clearable
              placeholder="No category"
            />
            <Select
              label="Auto-pay"
              value={values.is_autopay}
              onChange={(v) => set("is_autopay", v ?? "")}
              data={[
                { value: "yes", label: "Yes" },
                { value: "no", label: "No" },
              ]}
              placeholder="Not set"
              clearable
            />
          </div>

          <TextInput
            label="Account number"
            value={values.account_number}
            onChange={(e) => set("account_number", e.currentTarget.value)}
            placeholder="Optional"
          />
          <TextInput
            label="Payment URL"
            value={values.payment_url}
            onChange={(e) => set("payment_url", e.currentTarget.value)}
            placeholder="Optional"
            inputMode="url"
          />
          <Textarea
            label="Notes"
            value={values.notes}
            onChange={(e) => set("notes", e.currentTarget.value)}
            placeholder="Optional"
            minRows={2}
          />

          {error && (
            <Text size="sm" c="red" role="alert">
              {error}
            </Text>
          )}

          {onDelete && (
            <Button variant="subtle" color="red" onClick={onDelete} className="max-md:min-h-11">
              Delete bill
            </Button>
          )}
        </Stack>

        <ResponsiveModalActions>
          <Button variant="default" onClick={onClose} className="max-md:min-h-11">
            Cancel
          </Button>
          <Button type="submit" loading={saving} className="max-md:min-h-11">
            Save
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}
