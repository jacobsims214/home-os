"use client";

/**
 * PropertyDetailsSheet (#4578): the read-only Details fields of a property,
 * laid out per the detail-page contract (KB architecture/detail-page-layout.md):
 * a <dl> of rows, label above value below md and a label/value grid from md up,
 * value text in --ink and labels in --ink-3, so it reads in dark mode.
 *
 * Money is formatted by the shared helpers (property-finance.ts) and never
 * recomputed here: the mortgage rate is a percent, and the one place that
 * knows it is the shared module (#4553, audit #4509).
 */

import { formatUsd, monthlyCarryingCost, toNumber } from "./property-finance";
import { typeLabel } from "./property-tree";
import type { PropertyResponse } from "@/types/property";

function Row({ field, label, value }: { field: string; label: string; value: React.ReactNode }) {
  return (
    <div
      data-field={field}
      className="flex flex-col gap-1 py-2.5 md:grid md:grid-cols-[minmax(7rem,1fr)_minmax(0,2fr)] md:gap-x-6 md:items-baseline"
    >
      <dt className="text-xs text-ink-3 md:text-sm">{label}</dt>
      <dd className="m-0 text-sm text-ink [overflow-wrap:anywhere] whitespace-pre-line">{value}</dd>
    </div>
  );
}

function fmtDate(iso: string | null | undefined): string | null {
  if (!iso) return null;
  const d = new Date(`${iso.slice(0, 10)}T00:00:00`);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric" });
}

export default function PropertyDetailsSheet({
  property,
  roomCount,
  thingCount,
}: {
  property: PropertyResponse;
  roomCount: number;
  thingCount: number;
}) {
  const monthly = monthlyCarryingCost(property);
  const value = formatUsd(toNumber(property.current_value));
  const purchase = formatUsd(toNumber(property.purchase_price));
  const down = formatUsd(toNumber(property.down_payment));
  const bought = fmtDate(property.purchase_date);
  const type = typeLabel(property.property_type);

  return (
    <section aria-labelledby="property-details" className="min-w-0">
      <h2 id="property-details" className="mb-1 text-sm font-semibold text-ink-2">
        Details
      </h2>
      <dl className="m-0 divide-y divide-line">
        <Row field="address" label="Address" value={property.address || "Not set"} />
        <Row field="property_type" label="Type" value={type ?? "Not set"} />
        <Row
          field="rooms_and_things"
          label="Rooms and things"
          value={`${roomCount} ${roomCount === 1 ? "room" : "rooms"} · ${thingCount} ${thingCount === 1 ? "thing" : "things"}`}
        />
        <Row field="current_value" label="Current value" value={value ?? "Not set"} />
        <Row
          field="monthly_carrying_cost"
          label="Monthly cost"
          value={
            monthly === null ? (
              "Not set"
            ) : (
              <>
                {formatUsd(monthly)}
                <span className="text-ink-3">/mo</span>
                <span className="block text-xs text-ink-3">
                  Mortgage, property tax, insurance and HOA from this property&apos;s loans and bills.
                </span>
              </>
            )
          }
        />
        <Row field="purchase_price" label="Purchase price" value={purchase ?? "Not set"} />
        <Row field="purchase_date" label="Purchased" value={bought ?? "Not set"} />
        <Row field="down_payment" label="Down payment" value={down ?? "Not set"} />
        {property.notes && <Row field="notes" label="Notes" value={property.notes} />}
      </dl>
    </section>
  );
}
