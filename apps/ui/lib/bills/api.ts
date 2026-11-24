"use client";

/**
 * The bills pages' data layer (#4551): one query per source, so one failing
 * read shows one inline error card and leaves the rest of the page up — the
 * same rule as Today (#4549) and the calendar (#4550): a failed read must
 * never render as empty data.
 *
 * THE TWO READS, AND WHY BOTH ARE NEEDED
 *   GET /api/v1/bills             the records, with paid_date /
 *                                 paid_for_due_date as stored. No next due date:
 *                                 the API does not compute one (KB
 *                                 api/budget-categories-obligations.md).
 *   GET /api/v1/budget/due-soon   the API's own reading of when each bill falls
 *                                 due and whether it is paid: due_date,
 *                                 due_weekday, days_until_due, status, computed
 *                                 by the shared Go rule (billdue.BillDueFor) in
 *                                 the family's timezone (#4819).
 *
 * A PAID BILL NEVER READS AS DUE. Status comes from due-soon and nowhere else.
 * That is the R1 failure this page exists to not repeat (KB
 * discoveries/war-game-r1.md): "Electric $180 due, still unpaid" about a bill
 * paid that morning.
 *
 * MARKING A BILL PAID — THE ONE GAP THIS BUILD CANNOT CLOSE
 * The REST API has no route that records a payment. `paid_date` and
 * `paid_for_due_date` are response-only fields (apps/api/internal/bill/
 * handler.go: "written by the MCP bill_pay (the API does not record
 * payments)"), and the UI cannot call the MCP (it refuses home-os-ui tokens by
 * audience, #4692). So `markBillPaid` writes through the only write path that
 * exists — PUT /api/v1/bills/{id}, which merges only the fields it is sent —
 * and then BELIEVES THE RESPONSE: if the API's answer does not carry the
 * payment back, the payment was not stored, and the sheet says so instead of
 * showing a tick. One function changes the day a real pay route lands.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { billKeys } from "@/lib/query-keys";
import { useNavRole } from "@/components/shared/use-nav-role";
import { canSeeMoney, canWriteMoney, roleKnown, type Bill, type DueSoon } from "./model";

export const billsPageKeys = {
  all: ["bills-page"] as const,
  bills: () => [...billsPageKeys.all, "bills"] as const,
  bill: (id: string) => [...billsPageKeys.all, "bill", id] as const,
  dueSoon: () => [...billsPageKeys.all, "due-soon"] as const,
  subjects: () => [...billsPageKeys.all, "subjects"] as const,
};

/**
 * The due-soon window. 365 days is the API's maximum, and it is the honest
 * choice for a bills LIST: the page claims "sorted by next due date" for every
 * bill it shows, and a shorter window would leave most of a yearly bill's
 * neighbours unranked. `detail=items` lists them all (the API's summary depth
 * lists five).
 */
const DUE_SOON_DAYS = 365;

/** GET /api/v1/bills — the household's bills. */
export function useBills(enabled = true) {
  return useQuery({
    queryKey: billsPageKeys.bills(),
    queryFn: () => apiFetch<{ data: Bill[] }>("/api/v1/bills"),
    enabled,
    staleTime: 30_000,
    retry: false,
  });
}

/** GET /api/v1/bills/{id} — one bill, for the detail page. */
export function useBill(id: string | null | undefined) {
  return useQuery({
    queryKey: billsPageKeys.bill(id ?? ""),
    queryFn: () => apiFetch<{ data: Bill }>(`/api/v1/bills/${id}`),
    enabled: !!id,
    staleTime: 30_000,
    retry: false,
  });
}

/** GET /api/v1/budget/due-soon?days=365&detail=items — when each bill is due, and whether it is paid. */
export function useDueSoon(enabled = true) {
  return useQuery({
    queryKey: billsPageKeys.dueSoon(),
    queryFn: () =>
      apiFetch<{ data: DueSoon }>("/api/v1/budget/due-soon", {
        params: { days: DUE_SOON_DAYS, detail: "items" },
      }),
    enabled,
    staleTime: 30_000,
    retry: false,
  });
}

export interface SubjectOption {
  id: string;
  name: string;
}

/** The subjects a bill can be for: the option lists the form offers, and the name index the rows read. */
export interface Subjects {
  properties: SubjectOption[];
  assets: SubjectOption[];
  vehicles: SubjectOption[];
  names: { properties: Record<string, string>; assets: Record<string, string>; vehicles: Record<string, string> };
}

export const NO_SUBJECTS: Subjects = {
  properties: [],
  assets: [],
  vehicles: [],
  names: { properties: {}, assets: {}, vehicles: {} },
};

/**
 * GET /api/v1/properties, /assets and /vehicles — only for the names a bill's
 * "For" line shows and the choices its form offers. Each is read on its own so
 * a refused one (a role may read bills and not vehicles) costs that list, not
 * the page.
 */
export function useSubjectNames(enabled = true) {
  return useQuery({
    queryKey: billsPageKeys.subjects(),
    queryFn: async (): Promise<Subjects> => {
      const [properties, assets, vehicles] = await Promise.all([
        apiFetch<{ data: SubjectOption[] }>("/api/v1/properties").catch(() => ({ data: [] as SubjectOption[] })),
        apiFetch<{ data: SubjectOption[] }>("/api/v1/assets").catch(() => ({ data: [] as SubjectOption[] })),
        apiFetch<{ data: SubjectOption[] }>("/api/v1/vehicles").catch(() => ({ data: [] as SubjectOption[] })),
      ]);
      const index = (rows: SubjectOption[]) => Object.fromEntries(rows.map((r) => [r.id, r.name]));
      return {
        properties: properties.data ?? [],
        assets: assets.data ?? [],
        vehicles: vehicles.data ?? [],
        names: {
          properties: index(properties.data ?? []),
          assets: index(assets.data ?? []),
          vehicles: index(vehicles.data ?? []),
        },
      };
    },
    enabled,
    staleTime: 5 * 60_000,
    retry: false,
  });
}

// ── role ────────────────────────────────────────────────────

/**
 * What the signed-in role may do on these pages, from the API's policy
 * (auth.CanSeeMoney / ResBills write, KB decisions/2026-11-15-role-policy.md):
 *
 *  - `canSee` false: housesitter and vendor. No money, no list, no page.
 *  - `canWrite` false with `canSee` true: viewer. The page renders and every
 *    write affordance is disabled, with a line saying why.
 *  - `known` false: the role is still loading; render a skeleton rather than
 *    flash an owner's controls at a housesitter.
 */
export function useBillsAccess() {
  const role = useNavRole();
  return {
    role,
    known: roleKnown(role),
    canSee: canSeeMoney(role),
    canWrite: canWriteMoney(role),
  };
}

// ── writes ──────────────────────────────────────────────────

/** The body a bill form PUTs: only the fields the form owns. */
export interface BillBody {
  name?: string;
  amount?: string;
  due_day?: number;
  due_date?: string;
  frequency?: string;
  category?: string;
  budget_category_id?: string;
  property_id?: string;
  asset_id?: string;
  vehicle_id?: string;
  is_autopay?: boolean;
  account_number?: string;
  payment_url?: string;
  notes?: string;
}

/** POST /api/v1/bills — a new bill. */
export function useCreateBill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: BillBody) => apiFetch<{ data: Bill }>("/api/v1/bills", { method: "POST", body }),
    onSuccess: () => invalidateBills(qc),
  });
}

/** PUT /api/v1/bills/{id} — an edit (partial: only the fields sent change). */
export function useUpdateBill(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: BillBody) => apiFetch<{ data: Bill }>(`/api/v1/bills/${id}`, { method: "PUT", body }),
    onSuccess: () => invalidateBills(qc),
  });
}

/** DELETE /api/v1/bills/{id}. */
export function useDeleteBill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/v1/bills/${id}`, { method: "DELETE" }),
    onSuccess: () => invalidateBills(qc),
  });
}

/**
 * What a mark-paid attempt did, as the API answered it.
 *  - `recorded`: the API came back with the payment on the bill, so the next
 *    due date in `bill`/due-soon is real.
 *  - `not-recorded`: the API answered 2xx but did not store the payment (this
 *    build's API has no field for it). The UI must say so.
 */
export interface MarkPaidResult {
  recorded: boolean;
  bill: Bill | null;
}

/**
 * Record a bill as paid for a due date.
 *
 * The API has no payment route (see the file header), so this writes the two
 * payment columns through PUT /api/v1/bills/{id}, which merges only the fields
 * it is sent, and then checks the response. Sending `paid_date` and
 * `paid_for_due_date` is exactly what the MCP bill_pay stores (migration 052),
 * so the day the API accepts them — or gains POST /bills/{id}/pay — the call
 * below is the only thing that changes.
 */
export async function markBillPaid(
  id: string,
  paidDate: string,
  forDueDate: string | null,
): Promise<MarkPaidResult> {
  const body: Record<string, string> = { paid_date: paidDate };
  if (forDueDate) body.paid_for_due_date = forDueDate;
  const res = await apiFetch<{ data: Bill }>(`/api/v1/bills/${id}`, { method: "PUT", body });
  const bill = res?.data ?? null;
  const recorded = !!bill && (bill.paid_date ?? "").slice(0, 10) === paidDate;
  return { recorded, bill };
}

/** The mark-paid mutation: one place, so the page does not own the rule. */
export function useMarkBillPaid() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, paidDate, forDueDate }: { id: string; paidDate: string; forDueDate: string | null }) =>
      markBillPaid(id, paidDate, forDueDate),
    onSuccess: () => invalidateBills(qc),
  });
}

/**
 * Invalidate every read a bill write can change: the bills list and detail,
 * the due-soon reading (a payment moves the next due date), and Today's and
 * the calendar's copies of the same data, so no screen keeps a paid bill on
 * its "due" list.
 */
function invalidateBills(qc: ReturnType<typeof useQueryClient>): void {
  qc.invalidateQueries({ queryKey: billsPageKeys.all });
  qc.invalidateQueries({ queryKey: billKeys.all });
  qc.invalidateQueries({ queryKey: ["bills"] });
  qc.invalidateQueries({ queryKey: ["calendar-page", "bills"] });
}
