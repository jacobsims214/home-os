/**
 * Property money math (#4553).
 *
 * Kept free of React and of "@/" imports so `node --test` can load it
 * directly (see property-finance.test.mjs).
 *
 * The API stores money as NUMERIC strings and `mortgage_rate` as a PERCENT
 * ("4.250" means 4.25%), matching the Go DTO in
 * apps/api/internal/property/handler.go. The monthly rate is therefore
 * `rate / 100 / 12` — dividing by 12 alone inflates the payment ~100x, which
 * is what made the properties list show ~$86,606/mo instead of ~$1,412
 * (mobile audit #4509).
 */

/** The subset of a property the money math needs. */
export interface PropertyMoneyFields {
  mortgage_amount?: string | null;
  mortgage_rate?: string | null;
  mortgage_term_months?: string | null;
  property_tax_annual?: string | null;
  insurance_annual?: string | null;
  hoa_fee_monthly?: string | null;
  current_value?: string | null;
  purchase_price?: string | null;
  down_payment?: string | null;
}

/** Parses a nullable numeric string to a finite number, or null. */
export function toNumber(value: string | null | undefined): number | null {
  if (value === null || value === undefined || value === "") return null;
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}

/**
 * Monthly mortgage payment (principal + interest) using the standard
 * fixed-rate amortization formula: M = P * r / (1 - (1+r)^-n), where r is the
 * MONTHLY rate. `annualRatePercent` is a percent (4.25), not a fraction.
 * Returns null when the inputs can't produce a payment.
 */
export function monthlyMortgagePayment(
  principal: number | null,
  annualRatePercent: number | null,
  termMonths: number | null,
): number | null {
  if (principal === null || termMonths === null) return null;
  if (principal <= 0 || termMonths <= 0) return null;
  const rate = annualRatePercent ?? 0;
  if (rate <= 0) return principal / termMonths;
  const r = rate / 100 / 12;
  return (principal * r) / (1 - Math.pow(1 + r, -termMonths));
}

/**
 * Monthly carrying cost = mortgage payment + property tax / 12 +
 * insurance / 12 + HOA. Returns null when no component can be computed, so
 * the UI can say "not set" instead of showing $0.
 */
export function monthlyCarryingCost(p: PropertyMoneyFields): number | null {
  let total = 0;
  let hasAny = false;

  const payment = monthlyMortgagePayment(
    toNumber(p.mortgage_amount),
    toNumber(p.mortgage_rate),
    toNumber(p.mortgage_term_months),
  );
  if (payment !== null) {
    total += payment;
    hasAny = true;
  }

  const taxAnnual = toNumber(p.property_tax_annual);
  if (taxAnnual !== null) {
    total += taxAnnual / 12;
    hasAny = true;
  }

  const insuranceAnnual = toNumber(p.insurance_annual);
  if (insuranceAnnual !== null) {
    total += insuranceAnnual / 12;
    hasAny = true;
  }

  const hoaMonthly = toNumber(p.hoa_fee_monthly);
  if (hoaMonthly !== null) {
    total += hoaMonthly;
    hasAny = true;
  }

  return hasAny ? total : null;
}

/** True when the property has any financial field worth showing. */
export function hasFinancialData(p: PropertyMoneyFields): boolean {
  return Boolean(
    p.current_value ||
      p.mortgage_amount ||
      p.property_tax_annual ||
      p.insurance_annual ||
      p.hoa_fee_monthly ||
      p.purchase_price ||
      p.down_payment,
  );
}

const usdWhole = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  maximumFractionDigits: 0,
});

/** "$1,412" — whole dollars, for list summaries. */
export function formatUsd(value: number | null | undefined): string | null {
  if (value === null || value === undefined || !Number.isFinite(value)) return null;
  return usdWhole.format(value);
}

/**
 * The list's monthly total: the sum of every property's carrying cost.
 * Properties with no computable cost contribute nothing (they are not $0).
 */
export function totalMonthlyCarryingCost(properties: PropertyMoneyFields[]): number {
  return properties.reduce((sum, p) => sum + (monthlyCarryingCost(p) ?? 0), 0);
}
