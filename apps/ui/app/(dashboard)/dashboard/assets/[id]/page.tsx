"use client";

/**
 * One thing (#4554) - detail and edit, mobile-first, in the DetailPageLayout
 * frame (#4531) per the People & House mockup (artifact #89 v1, House >
 * property > room > "thing", the `data-show="asset"` screen).
 *
 * WHAT THE MOCKUP ASKS FOR, AND WHERE EACH PART COMES FROM
 *  - A summary strip straight after the title: the numbers worth seeing before
 *    scrolling (value, paid, warranty, bought), 2-up on a phone and 4-up from
 *    768 px. Each stat is rendered only when the API returned a value, so a
 *    role without money read (a housesitter, a vendor: #4912 redacts
 *    purchase_price / current_value / purchase_date) gets no strip rather than
 *    a row of dashes about money it may not see.
 *  - "Where": the room and the property as links, so the tree the mockup draws
 *    (property > room > thing) is walkable from the thing back up. An asset
 *    with no house is a family-owned thing, and says so.
 *  - Make, model and serial in the mono face, because they are transcribed
 *    numbers, not prose.
 *
 * Editing goes through the layout's own Edit -> Save (Cancel/Save in the phone
 * title bar, 44 px each). The PUT sends only the fields the API accepts and
 * clears a room/property with an empty string, which updateAssetRequest
 * documents as the clear.
 */

import { useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Skeleton, Stack } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconExternalLink } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { assetKeys, propertyKeys } from "@/lib/query-keys";
import DetailPageLayout from "@/components/layout/DetailPageLayout";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import { Select, TextInput } from "@/components/ui";
import { monthYear, summaryFacts, usd, type Asset } from "@/lib/assets/model";

interface Named {
  id: string;
  name: string;
}

interface Room {
  id: string;
  property_id: string;
  name: string;
}

interface EditableField {
  name: string;
  label: string;
  required?: boolean;
  /** A date input (YYYY-MM-DD); never sent empty. */
  date?: boolean;
  /** Money (a NUMERIC string in the API); never sent empty. */
  money?: boolean;
}

/** The editable fields, in the order the form and the view both use. */
const TEXT_FIELDS: EditableField[] = [
  { name: "name", label: "Name", required: true },
  { name: "category", label: "Category" },
  { name: "manufacturer", label: "Make" },
  { name: "model", label: "Model" },
  { name: "serial_number", label: "Serial number" },
  { name: "purchase_price", label: "Purchase price", money: true },
  { name: "current_value", label: "Current value", money: true },
  { name: "purchase_date", label: "Purchase date", date: true },
  { name: "warranty_expiry", label: "Warranty until", date: true },
  { name: "notes", label: "Notes" },
];

/** Edit-mode cards group the form so it is not one 10-field wall on a phone. */
const FORM_GROUPS: { title: string; fields: string[] }[] = [
  { title: "What it is", fields: ["name", "category", "manufacturer", "model", "serial_number"] },
  { title: "Money and dates", fields: ["purchase_price", "current_value", "purchase_date", "warranty_expiry"] },
  { title: "Notes", fields: ["notes"] },
];

const FIELD =
  "flex flex-col gap-3 rounded-token-md border border-line bg-surface p-4 shadow-token-sm md:block md:border-0 md:bg-transparent md:p-0 md:shadow-none";

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 px-4 py-3">
      <dt className="text-xs text-ink-3">{label}</dt>
      <dd className="text-[17px] font-semibold tabular-nums text-ink [overflow-wrap:anywhere]">{value}</dd>
    </div>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div
      data-asset-field={label.toLowerCase().replace(/\s+/g, "-")}
      className="flex flex-col gap-1 py-2.5 md:grid md:grid-cols-[minmax(7rem,1fr)_minmax(0,2fr)] md:gap-x-6"
    >
      <dt className="text-sm font-medium text-ink-3">{label}</dt>
      <dd className="m-0 whitespace-pre-line text-sm text-ink [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

export default function AssetDetailPage() {
  const params = useParams<{ id: string }>();
  const id = typeof params?.id === "string" ? params.id : "";
  const router = useRouter();
  const queryClient = useQueryClient();

  const [isEditing, setIsEditing] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});

  const assetQuery = useQuery({
    queryKey: assetKeys.detail(id),
    queryFn: () => apiFetch<{ data: Asset }>(`/api/v1/assets/${id}`),
    enabled: !!id,
  });
  const propertiesQuery = useQuery({
    queryKey: propertyKeys.lists(),
    queryFn: () => apiFetch<{ data: Named[] }>("/api/v1/properties"),
  });

  const asset = assetQuery.data?.data ?? null;
  const properties = propertiesQuery.data?.data ?? [];

  // The asset's own room, from the rooms of the property it is in. The asset
  // response carries room_id but not the room's name, and the room list is
  // already the property page's read.
  const roomsQuery = useQuery({
    queryKey: [...propertyKeys.detail(asset?.property_id || "_"), "rooms"],
    queryFn: () => apiFetch<{ data: Room[] }>(`/api/v1/properties/${asset?.property_id}/rooms`),
    enabled: !!asset?.property_id,
  });

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Asset }>(`/api/v1/assets/${id}`, { method: "PUT", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: assetKeys.all });
      setIsEditing(false);
      notifications.show({ message: "Saved", color: "teal" });
    },
  });

  const remove = useMutation({
    mutationFn: () => apiFetch<void>(`/api/v1/assets/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: assetKeys.all });
      notifications.show({ message: "Deleted", color: "teal" });
      router.push("/dashboard/assets");
    },
  });

  const set = (name: string, value: string) => {
    setValues((prev) => ({ ...prev, [name]: value }));
    setErrors((prev) => ({ ...prev, [name]: "" }));
  };

  const startEdit = () => {
    if (!asset) return;
    const next: Record<string, string> = {};
    for (const f of TEXT_FIELDS) {
      const v = asset[f.name as keyof Asset];
      next[f.name] = v == null ? "" : String(v);
    }
    // The HTML date input wants YYYY-MM-DD; the API may return a timestamp.
    for (const key of ["purchase_date", "warranty_expiry"]) {
      if (next[key]) next[key] = next[key].slice(0, 10);
    }
    next.property_id = asset.property_id ?? "";
    next.room_id = asset.room_id ?? "";
    setValues(next);
    setErrors({});
    setIsEditing(true);
  };

  const submit = () => {
    const name = (values.name ?? "").trim();
    if (!name) {
      setErrors({ name: "A name is required" });
      return;
    }
    const body: Record<string, unknown> = { name };
    for (const f of TEXT_FIELDS) {
      if (f.name === "name") continue;
      const v = (values[f.name] ?? "").trim();
      // An empty string clears a text field on update; an empty date or money
      // field is simply not sent, because "" is not a date or a number.
      if (v) body[f.name] = v;
      else if (!f.date && !f.money) body[f.name] = "";
    }
    // updateAssetRequest: an empty room_id/property_id clears the column.
    body.property_id = values.property_id || "";
    body.room_id = values.room_id || "";
    save.mutate(body);
  };

  const title = asset?.name ?? "Thing";
  const room = asset?.room_id ? roomsQuery.data?.data.find((r) => r.id === asset.room_id) ?? null : null;
  const property = asset?.property_id ? properties.find((p) => p.id === asset.property_id) ?? null : null;
  const stats = asset ? summaryFacts(asset) : [];

  const view = asset && (
    <div className="flex flex-col gap-5">
      {stats.length > 0 && (
        <dl
          data-asset-summary
          className="grid grid-cols-2 divide-x divide-y divide-line overflow-hidden rounded-token-md border border-line bg-surface-2/40 md:grid-cols-4 md:divide-y-0"
        >
          {stats.map((s) => (
            <Stat key={s.label} label={s.label} value={s.value} />
          ))}
        </dl>
      )}

      <div>
        <h3 className="text-sm font-semibold text-ink-2">Where</h3>
        <dl className="m-0 divide-y divide-line">
          <Row label="Room">
            {room ? (
              <Link
                href={`/dashboard/properties/${asset.property_id}/rooms/${room.id}`}
                className="inline-flex min-h-6 items-center gap-1 text-accent hover:underline"
              >
                {room.name}
              </Link>
            ) : asset.room_id ? (
              "Not in a room"
            ) : (
              "Not in a room"
            )}
          </Row>
          <Row label="Property">
            {property ? (
              <Link
                href={`/dashboard/properties/${property.id}`}
                className="inline-flex min-h-6 items-center gap-1 text-accent hover:underline"
              >
                {property.name}
                <IconExternalLink size={14} stroke={1.75} aria-hidden />
              </Link>
            ) : (
              "The whole family's, not a house"
            )}
          </Row>
        </dl>
      </div>

      <div>
        <h3 className="text-sm font-semibold text-ink-2">What it is</h3>
        <dl className="m-0 divide-y divide-line">
          <Row label="Category">{asset.category || "\u2014"}</Row>
          <Row label="Make">{asset.manufacturer || "\u2014"}</Row>
          <Row label="Model">
            {asset.model ? <span className="font-mono text-[13.5px]">{asset.model}</span> : "\u2014"}
          </Row>
          <Row label="Serial number">
            {asset.serial_number ? <span className="font-mono text-[13.5px]">{asset.serial_number}</span> : "\u2014"}
          </Row>
        </dl>
      </div>

      {/* Money is absent, not zero: a role without money read gets no rows at
          all, not a row of dashes about values it may not see (#4912). */}
      {(usd(asset.current_value) || usd(asset.purchase_price) || asset.purchase_date || asset.warranty_expiry) && (
        <div>
          <h3 className="text-sm font-semibold text-ink-2">Money and dates</h3>
          <dl className="m-0 divide-y divide-line">
            <Row label="Paid">{usd(asset.purchase_price) ?? "\u2014"}</Row>
            <Row label="Current value">{usd(asset.current_value) ?? "\u2014"}</Row>
            <Row label="Bought">{asset.purchase_date ? monthYear(asset.purchase_date) : "\u2014"}</Row>
            <Row label="Warranty until">{asset.warranty_expiry ? monthYear(asset.warranty_expiry) : "\u2014"}</Row>
          </dl>
        </div>
      )}

      {asset.notes && (
        <div>
          <h3 className="text-sm font-semibold text-ink-2">Notes</h3>
          <p className="mt-2 whitespace-pre-line text-sm text-ink [overflow-wrap:anywhere]">{asset.notes}</p>
        </div>
      )}
    </div>
  );

  const edit = (
    <form
      id="asset-edit"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      noValidate
    >
      <Stack gap="md">
        <div className={FIELD}>
          <Select
            label="Property"
            data={properties.map((p) => ({ value: p.id, label: p.name }))}
            value={values.property_id || null}
            onChange={(v) => {
              set("property_id", v ?? "");
              set("room_id", "");
            }}
            placeholder="The family's, not a house"
            clearable
          />
          <Select
            label="Room"
            data={(asset?.property_id === values.property_id ? roomsQuery.data?.data ?? [] : []).map((r) => ({
              value: r.id,
              label: r.name,
            }))}
            value={values.room_id || null}
            onChange={(v) => set("room_id", v ?? "")}
            placeholder={values.property_id ? "Not in a room" : "Pick a property first"}
            disabled={!values.property_id}
            clearable
          />
        </div>
        {FORM_GROUPS.map((group) => (
          <div key={group.title} className={FIELD}>
            {group.fields.map((name) => {
              const f = TEXT_FIELDS.find((x) => x.name === name);
              if (!f) return null;
              return (
                <TextInput
                  key={f.name}
                  label={f.label}
                  required={f.required}
                  error={errors[f.name]}
                  type={f.date ? "date" : "text"}
                  inputMode={f.money ? "decimal" : undefined}
                  placeholder={f.money ? "$" : undefined}
                  value={values[f.name] ?? ""}
                  onChange={(e) => set(f.name, e.currentTarget.value)}
                />
              );
            })}
          </div>
        ))}
        {save.isError && (
          <p role="alert" className="text-sm text-danger">
            Couldn&apos;t save: {save.error instanceof Error ? save.error.message : "try again"}
          </p>
        )}
        {/* The layout's Save in the title bar submits this form. */}
        <button type="submit" className="sr-only" tabIndex={-1} aria-hidden />
      </Stack>
    </form>
  );

  return (
    <DetailPageLayout
      entityType="asset"
      entityId={id}
      title={isEditing ? `Edit ${title}` : title}
      isEditing={isEditing}
      onEdit={startEdit}
      onDelete={() => setShowDelete(true)}
      onCancel={() => setIsEditing(false)}
      onSave={() => {
        const form = document.getElementById("asset-edit") as HTMLFormElement | null;
        form?.requestSubmit();
      }}
      isSaving={save.isPending}
    >
      {assetQuery.isLoading && (
        <Stack aria-busy="true" aria-label="Loading thing">
          <Skeleton height={72} radius="sm" />
          <Skeleton height={140} radius="sm" />
        </Stack>
      )}
      {assetQuery.isError && (
        <Alert color="red" title="Couldn't load this thing">
          {assetQuery.error instanceof Error ? assetQuery.error.message : "Try again in a moment."}
          <Button variant="default" size="sm" mt="sm" onClick={() => assetQuery.refetch()} loading={assetQuery.isRefetching}>
            Try again
          </Button>
        </Alert>
      )}
      {!assetQuery.isLoading && !assetQuery.isError && !asset && (
        <p className="text-sm text-ink-3">No thing found with that id.</p>
      )}
      {asset && (isEditing ? edit : view)}

      <ConfirmDialog
        open={showDelete}
        onClose={() => setShowDelete(false)}
        onConfirm={() => remove.mutate()}
        title="Delete thing"
        message={`Delete ${title}? This cannot be undone.`}
        loading={remove.isPending}
      />
    </DetailPageLayout>
  );
}
