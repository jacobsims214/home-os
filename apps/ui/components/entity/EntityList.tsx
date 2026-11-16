"use client";

/**
 * EntityList (#4530): the list page for assets, vehicles, pets, vendors and
 * bills.
 *
 * - Phone (< 768 px, `max-md`): stacked cards. Each card is one link with a
 *   44 px+ tap target; nothing is clipped.
 * - ≥ 768 px: a table inside a horizontal scroller, so columns never clip;
 *   the first cell is a real link (keyboard and middle-click work) and the
 *   whole row is clickable with a mouse.
 * - `cardView` lists (vendors) use the card grid at every width.
 * The phone/desktop switch is CSS only (no useMediaQuery), so there is no
 * SSR mismatch. Colours are theme token classes only.
 *
 * "Add <type>" calls `onAdd` when a page passes one. Otherwise it opens the
 * built-in create modal (fields from `createFields`, or the defaults in
 * entity-list-utils.ts), POSTs /api/v1/<type>s, refreshes the list and opens
 * the new record. There is no /new route.
 */

import { useState, type FormEvent } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Modal, Skeleton, Stack, Table, Group } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconChevronRight, IconPlus } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { propertyKeys } from "@/lib/query-keys";
import { Select, TextInput } from "@/components/ui";
import type { Property } from "@/lib/types/api";
import {
  buildCreatePayload,
  cardParts,
  createFieldsFor,
  formatValue,
  type CreateField,
  type EntityColumn,
  type EntityRecord,
} from "./entity-list-utils";

export type { CreateField, EntityColumn, EntityRecord } from "./entity-list-utils";

interface EntityListProps {
  entityType: string;
  title: string;
  description: string;
  columns: EntityColumn[];
  showMonthlyTotal?: boolean;
  /** Card grid at every width (vendors). Otherwise cards on phones, a table from 768 px. */
  cardView?: boolean;
  propertyFilter?: boolean;
  /** Replace the built-in create modal with the page's own create flow. */
  onAdd?: () => void;
  /** Fields for the built-in create modal. Defaults per entity type. */
  createFields?: CreateField[];
}

const PHONE_QUERY = "(max-width: 47.99em)";
// On phones, Mantine inputs get a 44 px height and 16 px text (no iOS zoom on
// focus); Mantine's size prop can't be responsive. Same as EntityDetail.
const PHONE_INPUTS = "max-md:[&_.mantine-Input-input]:min-h-11 max-md:[&_.mantine-Input-input]:text-base";

const usd = (n: number) => n.toLocaleString("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });

function EntityCard({ entity, columns, href }: { entity: EntityRecord; columns: EntityColumn[]; href: string }) {
  const { heading, details } = cardParts(entity, columns);
  return (
    <Link
      href={href}
      data-entity-card={entity.id}
      className="flex min-h-11 items-center gap-3 rounded-token-md border border-line bg-surface p-4 shadow-token-sm transition-colors hover:bg-surface-2 active:bg-surface-2"
    >
      <div className="min-w-0 flex-1">
        <h3 className="text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">{heading}</h3>
        {details.length > 0 && (
          <dl className="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
            {details.map((col) => (
              <div key={col.name} className="contents">
                <dt className="text-ink-3">{col.label}</dt>
                <dd className={`text-ink-2 [overflow-wrap:anywhere] ${col.format === "currency" ? "tabular-nums" : ""}`}>
                  {formatValue(entity[col.name], col.format)}
                </dd>
              </div>
            ))}
          </dl>
        )}
      </div>
      <IconChevronRight size={20} stroke={1.75} className="shrink-0 text-ink-3" aria-hidden />
    </Link>
  );
}

function CreateEntityModal({
  opened,
  onClose,
  entityType,
  fields,
  properties,
  defaultPropertyId,
}: {
  opened: boolean;
  onClose: () => void;
  entityType: string;
  fields: CreateField[];
  properties: Property[];
  defaultPropertyId: string;
}) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const isPhone = useMediaQuery(PHONE_QUERY);
  const [values, setValues] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});

  const reset = () => {
    setValues({});
    setErrors({});
  };
  const close = () => {
    reset();
    onClose();
  };

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: EntityRecord }>(`/api/v1/${entityType}s`, { method: "POST", body }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: [entityType] });
      notifications.show({ message: `${entityType[0].toUpperCase()}${entityType.slice(1)} added`, color: "teal" });
      close();
      const id = res?.data?.id;
      if (id) router.push(`/dashboard/${entityType}s/${id}`);
    },
  });

  const valueOf = (field: CreateField) =>
    values[field.name] ?? (field.type === "property" ? defaultPropertyId : "");

  const set = (name: string, value: string) => {
    setValues((prev) => ({ ...prev, [name]: value }));
    if (errors[name]) {
      setErrors((prev) => {
        const next = { ...prev };
        delete next[name];
        return next;
      });
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const filled = Object.fromEntries(fields.map((f) => [f.name, valueOf(f)]));
    const result = buildCreatePayload(fields, filled);
    if (!result.ok) {
      setErrors(result.errors);
      return;
    }
    create.mutate(result.body);
  };

  return (
    <Modal opened={opened} onClose={close} title={`Add ${entityType}`} fullScreen={!!isPhone} centered>
      <form onSubmit={submit} noValidate>
        <Stack gap="sm" className={PHONE_INPUTS}>
          {fields.map((field) => {
            const common = {
              label: field.label,
              required: field.required,
              error: errors[field.name],
              value: valueOf(field),
              "data-autofocus": field === fields[0] ? true : undefined,
            };
            if (field.type === "select" || field.type === "property") {
              const data =
                field.type === "property" ? properties.map((p) => ({ value: p.id, label: p.name })) : field.options ?? [];
              return (
                <Select
                  key={field.name}
                  {...common}
                  value={common.value || null}
                  data={data}
                  onChange={(v) => set(field.name, v ?? "")}
                  clearable={!field.required}
                  searchable={data.length > 8}
                  comboboxProps={{ withinPortal: true }}
                />
              );
            }
            return (
              <TextInput
                key={field.name}
                {...common}
                type={field.type === "date" ? "date" : "text"}
                inputMode={field.type === "integer" ? "numeric" : field.type === "decimal" ? "decimal" : undefined}
                onChange={(e) => set(field.name, e.currentTarget.value)}
              />
            );
          })}
          {create.isError && (
            <p role="alert" className="text-sm text-danger">
              Couldn&apos;t add the {entityType}: {create.error instanceof Error ? create.error.message : "try again"}
            </p>
          )}
          <Group justify="flex-end" gap="sm" mt="xs" className="max-md:[&_button]:min-h-11 max-md:[&_button]:flex-1">
            <Button variant="default" onClick={close} type="button">
              Cancel
            </Button>
            <Button type="submit" loading={create.isPending}>
              Add {entityType}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

export default function EntityList({
  entityType,
  title,
  description,
  columns,
  showMonthlyTotal = false,
  cardView = false,
  propertyFilter = true,
  onAdd,
  createFields,
}: EntityListProps) {
  const router = useRouter();
  const [filterValue, setFilterValue] = useState("");
  const [createOpen, setCreateOpen] = useState(false);

  const { data: propertiesData } = useQuery({
    queryKey: propertyKeys.all,
    queryFn: () => apiFetch<{ data: Property[] }>("/api/v1/properties"),
  });
  const properties = propertiesData?.data ?? [];

  const {
    data: entitiesData,
    isLoading,
    isError,
    refetch,
    isRefetching,
  } = useQuery({
    queryKey: [entityType],
    queryFn: () => apiFetch<{ data: EntityRecord[] }>(`/api/v1/${entityType}s`),
  });
  const allEntities = entitiesData?.data ?? [];
  const entities = filterValue ? allEntities.filter((e) => e.property_id === filterValue) : allEntities;

  const monthlyTotal = showMonthlyTotal
    ? entities.reduce((sum: number, e: EntityRecord) => sum + (Number(e.amount) || 0), 0)
    : null;

  const hrefFor = (entity: EntityRecord) => `/dashboard/${entityType}s/${entity.id}`;
  const add = () => (onAdd ? onAdd() : setCreateOpen(true));
  const addLabel = `Add ${entityType}`;
  // When the first column isn't the name (vehicles: Year), its link is labelled with the card heading.
  const firstColumnIsName = columns[0]?.name === "name" || columns[0]?.name === "title";

  return (
    <div className="mx-auto w-full max-w-6xl">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3 md:mb-6">
        <div className="min-w-0">
          <h2 className="text-xl font-semibold text-ink md:text-2xl">{title}</h2>
          <p className="mt-1 text-sm text-ink-3">{description}</p>
        </div>
        <Button
          onClick={add}
          leftSection={<IconPlus size={16} />}
          aria-haspopup={onAdd ? undefined : "dialog"}
          className="max-md:min-h-11"
        >
          {addLabel}
        </Button>
      </div>

      {propertyFilter && (
        <div className={`mb-4 md:max-w-xs ${PHONE_INPUTS}`}>
          <Select
            label="Filter by property"
            value={filterValue || null}
            onChange={(val) => setFilterValue(val || "")}
            data={properties.map((p) => ({ value: p.id, label: p.name }))}
            placeholder="All properties"
            clearable
            searchable
          />
        </div>
      )}

      {showMonthlyTotal && monthlyTotal !== null && !isError && (
        <div className="mb-4 flex items-center gap-4 rounded-token-md border border-line bg-surface p-4 shadow-token-sm md:mb-6 md:w-fit md:pr-8">
          <div>
            <p className="text-xs text-ink-3">Monthly total</p>
            <p className="text-2xl font-semibold tabular-nums text-ink">
              {usd(monthlyTotal)}
              <span className="text-sm font-normal text-ink-3">/mo</span>
            </p>
          </div>
          <div className="h-10 w-px bg-line" aria-hidden />
          <div>
            <p className="text-xs text-ink-3">Count</p>
            <p className="text-lg font-semibold tabular-nums text-ink-2">{entities.length}</p>
          </div>
        </div>
      )}

      {isLoading && (
        <div
          aria-busy="true"
          aria-label={`Loading ${title.toLowerCase()}`}
          className={cardView ? "grid gap-3 sm:grid-cols-2 lg:grid-cols-3" : "space-y-2"}
        >
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
          <IconAlertTriangle size={20} stroke={1.75} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">Couldn&apos;t load {title.toLowerCase()}. Check your connection and try again.</p>
          <Button variant="default" onClick={() => refetch()} loading={isRefetching} className="max-md:min-h-11">
            Try again
          </Button>
        </div>
      )}

      {!isLoading && !isError && entities.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <p className="text-sm text-ink-3">
            {filterValue ? `No ${title.toLowerCase()} for this property.` : `No ${title.toLowerCase()} yet.`}
          </p>
          <Button variant="light" onClick={add} leftSection={<IconPlus size={16} />} className="max-md:min-h-11">
            {addLabel}
          </Button>
        </div>
      )}

      {!isLoading && !isError && entities.length > 0 && (
        <>
          <ul
            aria-label={title}
            className={cardView ? "grid gap-3 sm:grid-cols-2 lg:grid-cols-3" : "space-y-2 md:hidden"}
          >
            {entities.map((entity) => (
              <li key={entity.id} className="min-w-0">
                <EntityCard entity={entity} columns={columns} href={hrefFor(entity)} />
              </li>
            ))}
          </ul>

          {!cardView && (
            <div
              data-entity-table
              className="overflow-x-auto rounded-token-md border border-line bg-surface shadow-token-sm max-md:hidden"
            >
              <Table highlightOnHover verticalSpacing="sm" horizontalSpacing="md" className="min-w-[36rem]">
                <Table.Thead>
                  <Table.Tr>
                    {columns.map((col) => (
                      <Table.Th
                        key={col.name}
                        scope="col"
                        className={`whitespace-nowrap text-xs font-semibold uppercase tracking-wide text-ink-3 ${col.format === "currency" ? "text-right" : "text-left"}`}
                      >
                        {col.label}
                      </Table.Th>
                    ))}
                    <Table.Th className="w-10">
                      <span className="sr-only">Open</span>
                    </Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {entities.map((entity) => {
                    const { heading } = cardParts(entity, columns);
                    return (
                      <Table.Tr
                        key={entity.id}
                        onClick={(e) => {
                          // The first cell's link handles its own clicks (and modifier keys).
                          if ((e.target as HTMLElement).closest("a")) return;
                          router.push(hrefFor(entity));
                        }}
                        className="cursor-pointer"
                      >
                        {columns.map((col, i) => {
                          const text = formatValue(entity[col.name], col.format);
                          const isCurrency = col.format === "currency";
                          return (
                            <Table.Td
                              key={col.name}
                              className={
                                i === 0
                                  ? "min-w-[12rem] max-w-[24rem] font-medium text-ink [overflow-wrap:anywhere]"
                                  : `whitespace-nowrap text-ink-2 ${isCurrency ? "text-right tabular-nums" : ""}`
                              }
                            >
                              {i === 0 ? (
                                <Link
                                  href={hrefFor(entity)}
                                  className="text-ink hover:underline"
                                  aria-label={firstColumnIsName ? undefined : heading}
                                >
                                  {text}
                                </Link>
                              ) : (
                                text
                              )}
                            </Table.Td>
                          );
                        })}
                        <Table.Td className="text-ink-3">
                          <IconChevronRight size={18} stroke={1.75} aria-hidden />
                        </Table.Td>
                      </Table.Tr>
                    );
                  })}
                </Table.Tbody>
              </Table>
            </div>
          )}
        </>
      )}

      {!onAdd && (
        <CreateEntityModal
          opened={createOpen}
          onClose={() => setCreateOpen(false)}
          entityType={entityType}
          fields={createFields ?? createFieldsFor(entityType, columns)}
          properties={properties}
          defaultPropertyId={filterValue}
        />
      )}
    </div>
  );
}
