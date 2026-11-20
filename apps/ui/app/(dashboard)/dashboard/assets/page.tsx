"use client";

/**
 * Assets ("things") list (#4554), mobile-first per the People & House mockup
 * (artifact #89 v1, House > property > room > "thing") on the EntityList
 * pattern (KB architecture/entity-list-pattern.md), matching the properties
 * list (#4553) and the loans list (#4552).
 *
 * The mockup calls an asset a "thing" and nests it under property > room. This
 * page is the flat cross-house list at /dashboard/assets: the room and property
 * pages still own the tree view, and every row here links into it.
 *
 * - Phone (< 768 px): stacked cards, each one link with a 44 px+ tap target;
 *   nothing clips, and the facts sit in a two-up strip under the name.
 * - >= 768 px: a table in a horizontal scroller, first cell a real link.
 * - "Add thing" opens a ResponsiveModal (bottom sheet on phones) and POSTs
 *   /api/v1/assets. There is no /new route (the #4509 audit bug).
 *
 * Money is not redacted on top of the API. GET /assets is rolesEveryone - a
 * housesitter and a vendor may see the family's things - but the API leaves
 * purchase_price/current_value/purchase_date out (or null) for a role without
 * money read (#4912, auth.CanSeeMoney), so these pages render a money fact
 * only when the API actually sent one. A missing value is never shown as $0.
 */

import { useState, type FormEvent } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton, Stack, Table } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconBox, IconChevronRight, IconPlus } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { assetKeys, propertyKeys } from "@/lib/query-keys";
import { ResponsiveModal, ResponsiveModalActions, Select, TextInput } from "@/components/ui";
import { factsFor, makeAndModel, monthYear, usd, type Asset } from "@/lib/assets/model";

interface Named {
  id: string;
  name: string;
}

function AssetCard({ asset, propertyName }: { asset: Asset; propertyName: string | null }) {
  const facts = factsFor(asset, propertyName);
  const sub = makeAndModel(asset);

  return (
    <Link
      href={`/dashboard/assets/${asset.id}`}
      data-asset-card={asset.id}
      className="block min-h-11 rounded-token-md border border-line bg-surface shadow-token-sm transition-colors hover:bg-surface-2 active:bg-surface-2"
    >
      <div className="flex items-center gap-3 p-4">
        <span
          aria-hidden
          className="grid size-11 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent"
        >
          <IconBox size={22} stroke={1.75} />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">{asset.name}</h3>
          {sub && <p className="mt-0.5 text-sm text-ink-3 [overflow-wrap:anywhere]">{sub}</p>}
        </div>
        <IconChevronRight size={20} stroke={1.75} className="shrink-0 text-ink-3" aria-hidden />
      </div>
      {facts.length > 0 && (
        <dl className="grid grid-cols-2 border-t border-line">
          {facts.map((fact, i) => (
            <div
              key={fact.label}
              className={[
                "min-w-0 px-4 py-2.5",
                i % 2 === 1 ? "border-l border-line" : "",
                i > 1 ? "border-t border-line" : "",
                // An odd last fact spans the row: an empty half-cell beside a
                // lone fact reads as a missing value.
                facts.length % 2 === 1 && i === facts.length - 1 ? "col-span-2" : "",
              ].join(" ")}
            >
              <dt className="text-xs text-ink-3">{fact.label}</dt>
              <dd className="text-[15px] font-semibold tabular-nums text-ink [overflow-wrap:anywhere]">{fact.value}</dd>
            </div>
          ))}
        </dl>
      )}
    </Link>
  );
}

const EMPTY = {
  name: "",
  category: "",
  manufacturer: "",
  model: "",
  serial_number: "",
  property_id: "",
  room_id: "",
  purchase_price: "",
  current_value: "",
  purchase_date: "",
  warranty_expiry: "",
  notes: "",
};

function AddAssetModal({
  opened,
  onClose,
  properties,
}: {
  opened: boolean;
  onClose: () => void;
  properties: Named[];
}) {
  const queryClient = useQueryClient();
  const [values, setValues] = useState<Record<string, string>>({ ...EMPTY });
  const [errors, setErrors] = useState<Record<string, string>>({});

  const roomsQuery = useQuery({
    queryKey: [...propertyKeys.detail(values.property_id || "_"), "rooms"],
    queryFn: () => apiFetch<{ data: { id: string; name: string }[] }>(`/api/v1/properties/${values.property_id}/rooms`),
    enabled: opened && !!values.property_id,
  });

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Asset }>("/api/v1/assets", { method: "POST", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: assetKeys.all });
      notifications.show({ message: "Asset added", color: "teal" });
      close();
    },
  });

  const set = (name: string, value: string) => {
    setValues((prev) => ({ ...prev, [name]: value }));
    setErrors((prev) => ({ ...prev, [name]: "" }));
  };

  const close = () => {
    setValues({ ...EMPTY });
    setErrors({});
    create.reset();
    onClose();
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const name = values.name.trim();
    if (!name) {
      setErrors({ name: "A name is required" });
      return;
    }
    // Optional fields are sent only when filled: the API stores NULL otherwise,
    // and an empty string is not a date or a number.
    const body: Record<string, unknown> = { name };
    for (const key of [
      "category",
      "manufacturer",
      "model",
      "serial_number",
      "purchase_price",
      "current_value",
      "purchase_date",
      "warranty_expiry",
      "notes",
    ] as const) {
      const v = values[key]?.trim();
      if (v) body[key] = v;
    }
    // With a room the API derives property_id from it; sending both would be a
    // second source of truth for the same fact.
    if (values.room_id) body.room_id = values.room_id;
    else if (values.property_id) body.property_id = values.property_id;
    create.mutate(body);
  };

  const field = (name: string, label: string, extra: Record<string, unknown> = {}) => (
    <TextInput
      key={name}
      label={label}
      value={values[name] ?? ""}
      error={errors[name]}
      onChange={(e) => set(name, e.currentTarget.value)}
      data-autofocus={name === "name" ? true : undefined}
      {...extra}
    />
  );

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add asset">
      <form onSubmit={submit} noValidate>
        <Stack gap="md">
          {field("name", "Name", { required: true, placeholder: "e.g. Furnace" })}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
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
              data={(roomsQuery.data?.data ?? []).map((r) => ({ value: r.id, label: r.name }))}
              value={values.room_id || null}
              onChange={(v) => set("room_id", v ?? "")}
              placeholder={values.property_id ? "Not in a room" : "Pick a property first"}
              disabled={!values.property_id}
              clearable
            />
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {field("category", "Category", { placeholder: "e.g. HVAC" })}
            {field("manufacturer", "Make", { placeholder: "e.g. Carrier" })}
            {field("model", "Model", { placeholder: "e.g. Infinity 59SC5" })}
            {field("serial_number", "Serial number")}
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {field("purchase_price", "Purchase price", { inputMode: "decimal", placeholder: "$" })}
            {field("current_value", "Current value", { inputMode: "decimal", placeholder: "$" })}
            {field("purchase_date", "Purchase date", { type: "date" })}
            {field("warranty_expiry", "Warranty until", { type: "date" })}
          </div>
          {field("notes", "Notes", { placeholder: "Filter size, where the manual is, anything useful" })}
          {create.isError && (
            <p role="alert" className="text-sm text-danger">
              Couldn&apos;t add the asset: {create.error instanceof Error ? create.error.message : "try again"}
            </p>
          )}
        </Stack>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Add asset
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

export default function AssetsPage() {
  const [showAdd, setShowAdd] = useState(false);

  const assetsQuery = useQuery({
    queryKey: assetKeys.lists(),
    queryFn: () => apiFetch<{ data: Asset[] }>("/api/v1/assets"),
  });
  // Property names for the "Place" fact and the create form's picker.
  const propertiesQuery = useQuery({
    queryKey: propertyKeys.lists(),
    queryFn: () => apiFetch<{ data: Named[] }>("/api/v1/properties"),
  });

  const assets = assetsQuery.data?.data ?? [];
  const properties = propertiesQuery.data?.data ?? [];
  const propertyName = (id: string | null | undefined) =>
    (id && properties.find((p) => p.id === id)?.name) || null;

  return (
    <div className="mx-auto w-full max-w-6xl">
      <div className="mb-4 md:mb-6">
        <div className="flex items-center justify-between gap-3">
          <h2 className="min-w-0 text-xl font-semibold text-ink md:text-2xl">
            Assets
            {!assetsQuery.isLoading && !assetsQuery.isError && assets.length > 0 && (
              <span className="ml-2 align-middle text-base font-medium text-ink-3">{assets.length}</span>
            )}
          </h2>
          <Button
            onClick={() => setShowAdd(true)}
            leftSection={<IconPlus size={16} />}
            aria-haspopup="dialog"
            className="max-md:min-h-11 shrink-0"
          >
            Add asset
          </Button>
        </div>
        <p className="mt-1 text-sm text-ink-3">
          The things the house is made of: appliances, HVAC, electronics and the rest.
        </p>
      </div>

      {assetsQuery.isLoading && (
        <div role="status" aria-busy="true" aria-label="Loading assets" className="space-y-2">
          {[1, 2, 3].map((i) => (
            <div key={i} className="space-y-2 rounded-token-md border border-line bg-surface p-4 shadow-token-sm">
              <Skeleton height={14} width="55%" radius="sm" />
              <Skeleton height={12} width="80%" radius="sm" />
            </div>
          ))}
        </div>
      )}

      {assetsQuery.isError && (
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={1.75} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">Couldn&apos;t load assets. Check your connection and try again.</p>
          <Button variant="default" onClick={() => assetsQuery.refetch()} loading={assetsQuery.isRefetching} className="max-md:min-h-11">
            Try again
          </Button>
        </div>
      )}

      {!assetsQuery.isLoading && !assetsQuery.isError && assets.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <p className="text-sm text-ink-3">
            No assets yet. Add the furnace, the washer or anything else worth keeping the details of.
          </p>
          <Button variant="light" onClick={() => setShowAdd(true)} leftSection={<IconPlus size={16} />} className="max-md:min-h-11">
            Add asset
          </Button>
        </div>
      )}

      {!assetsQuery.isLoading && !assetsQuery.isError && assets.length > 0 && (
        <>
          <ul aria-label="Assets" className="space-y-2 md:hidden">
            {assets.map((asset) => (
              <li key={asset.id} className="min-w-0">
                <AssetCard asset={asset} propertyName={propertyName(asset.property_id)} />
              </li>
            ))}
          </ul>

          <div
            data-asset-table
            className="overflow-x-auto rounded-token-md border border-line bg-surface shadow-token-sm max-md:hidden"
          >
            <Table highlightOnHover verticalSpacing="sm" horizontalSpacing="md" className="min-w-[44rem]">
              <Table.Thead>
                <Table.Tr>
                  {["Name", "Make and model", "Place", "Value", "Warranty"].map((label, i) => (
                    <Table.Th
                      key={label}
                      scope="col"
                      className={`whitespace-nowrap text-xs font-semibold uppercase tracking-wide text-ink-3 ${i >= 3 ? "text-right" : "text-left"}`}
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
                {assets.map((asset) => (
                  <Table.Tr key={asset.id}>
                    <Table.Td className="min-w-[12rem] max-w-[24rem] font-medium text-ink [overflow-wrap:anywhere]">
                      <Link href={`/dashboard/assets/${asset.id}`} className="text-ink hover:underline">
                        {asset.name}
                      </Link>
                    </Table.Td>
                    <Table.Td className="text-ink-2 [overflow-wrap:anywhere]">{makeAndModel(asset) ?? "\u2014"}</Table.Td>
                    <Table.Td className="whitespace-nowrap text-ink-2">{propertyName(asset.property_id) ?? "\u2014"}</Table.Td>
                    <Table.Td className="whitespace-nowrap text-right tabular-nums text-ink-2">
                      {usd(asset.current_value) ?? "\u2014"}
                    </Table.Td>
                    <Table.Td className="whitespace-nowrap text-right text-ink-2">
                      {asset.warranty_expiry ? monthYear(asset.warranty_expiry) : "\u2014"}
                    </Table.Td>
                    <Table.Td className="text-ink-3">
                      <IconChevronRight size={18} stroke={1.75} aria-hidden />
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </div>
        </>
      )}

      <AddAssetModal opened={showAdd} onClose={() => setShowAdd(false)} properties={properties} />
    </div>
  );
}
