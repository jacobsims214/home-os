"use client";

/**
 * VetPicker (#4703): picks a pet's vet from the family's contacts tagged
 * "vet" (GET /api/v1/vendors?trade=vet), in a ResponsiveModal (bottom sheet on
 * phones). "Add a new vet contact" creates the contact inline
 * (POST /api/v1/vendors with trades ["vet"]) and selects it. The value is the
 * vendor id that goes into pets.vet_vendor_id; nothing here writes
 * vet_name / vet_phone. Choosing doesn't navigate, so closing the sheet after
 * a choice is safe (KB architecture/responsive-modal.md).
 */

import { useEffect, useId, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton, Stack } from "@mantine/core";
import { IconChevronRight, IconPlus, IconStethoscope } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { ResponsiveModal, ResponsiveModalActions, TextInput } from "@/components/ui";
import { newVetBody, vetContactsKey, VET_TRADE, type VetContact } from "./vet-utils";

export interface VetPickerProps {
  /** The chosen vendor id (pets.vet_vendor_id), or null for no vet. */
  value: string | null;
  onChange: (vendorId: string | null) => void;
  /** The vet the API returned with the pet, shown before the contact list loads. */
  current?: { name?: string | null; phone?: string | null };
  label?: string;
}

const NONE = "__none__";

export default function VetPicker({ value, onChange, current, label = "Vet" }: VetPickerProps) {
  const labelId = useId();
  const [open, setOpen] = useState(false);
  // Contacts created here this session, so the label is right before the list refetches.
  const [created, setCreated] = useState<VetContact | null>(null);

  const vets = useQuery({
    queryKey: vetContactsKey,
    queryFn: () => apiFetch<{ data: VetContact[] }>("/api/v1/vendors", { params: { trade: VET_TRADE } }),
    enabled: open || !!value,
  });
  const list = vets.data?.data ?? [];
  const chosen =
    list.find((v) => v.id === value) ?? (created?.id === value ? created : null) ??
    (value && current?.name ? { id: value, name: current.name, phone: current.phone } : null);

  return (
    <div>
      <p id={labelId} className="mb-1 text-sm font-medium text-ink">
        {label}
      </p>
      <button
        type="button"
        aria-haspopup="dialog"
        aria-labelledby={labelId}
        aria-describedby={`${labelId}-value`}
        onClick={() => setOpen(true)}
        data-vet-picker
        className="flex min-h-11 w-full items-center gap-3 rounded-token-md border border-line bg-surface px-3 py-2 text-left transition-colors hover:bg-surface-2"
      >
        <IconStethoscope size={18} stroke={1.75} className="shrink-0 text-ink-3" aria-hidden />
        <span id={`${labelId}-value`} className="min-w-0 flex-1">
          {chosen ? (
            <>
              <span className="block text-sm font-medium text-ink [overflow-wrap:anywhere]">{chosen.name}</span>
              {chosen.phone && <span className="block text-xs text-ink-3">{chosen.phone}</span>}
            </>
          ) : (
            <span className="block text-sm text-ink-3">No vet chosen</span>
          )}
        </span>
        <span className="shrink-0 text-sm font-medium text-accent">{chosen ? "Change" : "Choose"}</span>
        <IconChevronRight size={18} stroke={1.75} className="shrink-0 text-ink-3" aria-hidden />
      </button>
      {open && (
        <VetSheet
          value={value}
          vets={list}
          loading={vets.isLoading}
          error={vets.isError}
          retry={() => vets.refetch()}
          onClose={() => setOpen(false)}
          onPick={(vet) => {
            if (vet && !list.some((v) => v.id === vet.id)) setCreated(vet);
            onChange(vet ? vet.id : null);
            setOpen(false);
          }}
        />
      )}
    </div>
  );
}

function VetSheet({
  value,
  vets,
  loading,
  error,
  retry,
  onClose,
  onPick,
}: {
  value: string | null;
  vets: VetContact[];
  loading: boolean;
  error: boolean;
  retry: () => void;
  onClose: () => void;
  onPick: (vet: VetContact | null) => void;
}) {
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<string>(value ?? NONE);
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState({ name: "", phone: "" });
  const [nameError, setNameError] = useState<string | undefined>();
  const showAdd = adding || (!loading && !error && vets.length === 0);
  const nameId = useId();

  // "Add a new vet contact" replaces the button that had focus: move focus to
  // the name field (data-autofocus only applies when the dialog opens).
  useEffect(() => {
    if (adding) document.getElementById(nameId)?.focus();
  }, [adding, nameId]);

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: VetContact }>("/api/v1/vendors", { method: "POST", body }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: ["vendor"] });
      onPick(res.data);
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (showAdd) {
      const r = newVetBody(draft);
      if (!r.ok) return setNameError(r.errors.name);
      return create.mutate(r.body);
    }
    if (selected === NONE) return onPick(null);
    const vet = vets.find((v) => v.id === selected);
    if (vet) onPick(vet);
  };

  return (
    <ResponsiveModal opened onClose={onClose} title="Choose a vet">
      <form onSubmit={submit} noValidate>
        <Stack gap="sm">
          {loading && (
            <div role="status" aria-busy="true" aria-label="Loading vet contacts" className="space-y-2">
              {[1, 2].map((i) => (
                <Skeleton key={i} height={52} radius="md" />
              ))}
            </div>
          )}
          {error && (
            <div role="alert" className="flex flex-wrap items-center gap-3 rounded-token-md border border-warn-line bg-warn-bg p-3 text-sm text-warn-ink">
              <span className="flex-1">Couldn&apos;t load your vet contacts.</span>
              <Button variant="default" size="xs" onClick={retry}>
                Try again
              </Button>
            </div>
          )}
          {!loading && !error && vets.length === 0 && (
            <p className="text-sm text-ink-3">No contacts are tagged Vet yet. Add your vet below.</p>
          )}
          {!loading && !error && vets.length > 0 && !adding && (
            <fieldset className="m-0 border-0 p-0">
              <legend className="sr-only">Vet contacts</legend>
              <ul className="m-0 list-none space-y-2 p-0">
                {[...vets.map((v) => ({ id: v.id, name: v.name, phone: v.phone })), ...(value ? [{ id: NONE, name: "No vet", phone: null }] : [])].map(
                  (v, i) => (
                    <li key={v.id}>
                      <label
                        data-vet-option={v.id}
                        className={`flex min-h-11 cursor-pointer items-center gap-3 rounded-token-md border px-3 py-2 transition-colors ${
                          selected === v.id ? "border-accent bg-accent-soft" : "border-line bg-surface hover:bg-surface-2"
                        }`}
                      >
                        <input
                          type="radio"
                          name="vet"
                          value={v.id}
                          checked={selected === v.id}
                          onChange={() => setSelected(v.id)}
                          data-autofocus={i === 0 ? true : undefined}
                          className="size-4 shrink-0 accent-accent"
                        />
                        <span className="min-w-0 flex-1">
                          <span className="block text-sm font-medium text-ink [overflow-wrap:anywhere]">{v.name}</span>
                          {v.phone && <span className="block text-xs text-ink-3">{v.phone}</span>}
                        </span>
                      </label>
                    </li>
                  ),
                )}
              </ul>
            </fieldset>
          )}
          {!loading && !error && !showAdd && (
            <Button
              variant="subtle"
              leftSection={<IconPlus size={16} />}
              onClick={() => setAdding(true)}
              className="min-h-11 self-start"
            >
              Add a new vet contact
            </Button>
          )}
          {showAdd && !loading && (
            <div className="space-y-3 rounded-token-md border border-line bg-surface-2 p-3" data-new-vet>
              <p className="text-sm font-semibold text-ink-2">New vet contact</p>
              <TextInput
                id={nameId}
                label="Vet or clinic name"
                required
                value={draft.name}
                error={nameError}
                data-autofocus
                onChange={(e) => {
                  const name = e.currentTarget.value;
                  setDraft((d) => ({ ...d, name }));
                  setNameError(undefined);
                }}
              />
              <TextInput
                label="Phone"
                type="tel"
                inputMode="tel"
                value={draft.phone}
                onChange={(e) => {
                  const phone = e.currentTarget.value;
                  setDraft((d) => ({ ...d, phone }));
                }}
              />
              <p className="text-xs text-ink-3">Saved to Contacts, tagged Vet.</p>
              {create.isError && (
                <p role="alert" className="text-sm text-danger">
                  Couldn&apos;t add the contact: {create.error instanceof Error ? create.error.message : "try again"}
                </p>
              )}
            </div>
          )}
        </Stack>
        <ResponsiveModalActions>
          <Button
            variant="default"
            type="button"
            onClick={adding && vets.length > 0 ? () => setAdding(false) : onClose}
          >
            {adding && vets.length > 0 ? "Back" : "Cancel"}
          </Button>
          <Button type="submit" loading={create.isPending} disabled={loading || (error && !showAdd)}>
            {showAdd ? "Add vet" : "Use this vet"}
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}
