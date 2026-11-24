"use client";

/**
 * The one search request (#4561, extracted for #5303).
 *
 * WHY THIS FILE EXISTS: the Search page and the sidebar/drawer box used to be
 * two different things — the page had the debounce, the `/api/v1/search`
 * query, the grouping and the five states; the box had only a form submit. So
 * the box produced no request at all while typing (war game R2 G4: "type
 * mortgage, wait 8 s: nothing happens"), which on a phone is a dead control.
 * #5303 wires the box, and the way not to grow a second search implementation
 * is for BOTH surfaces to read this hook: one debounce, one endpoint, one
 * grouping rule, one idea of what "empty" and "error" mean.
 *
 * The rules this hook carries, unchanged from the page:
 *
 *  1. ONE REQUEST PER PAUSE, NOT PER KEYSTROKE. `input` is what the person is
 *     typing; `submitted` is what has been searched. A 250 ms pause promotes
 *     one to the other. Clearing the box promotes immediately — an empty box is
 *     idle, and waiting 250 ms to say so reads as lag.
 *
 *  2. THE ROLE IS PART OF THE ANSWER. `groupHits` drops groups the household
 *     role may not search (#4696), so both surfaces show the same rows for the
 *     same person. The API already filters per hit; this is the UI half.
 *
 *  3. EMPTY ≠ NO RESULTS ≠ ERROR. `searchState` is still the single arbiter
 *     (lib/search/results.ts); the hook only tells it what it knows.
 *
 * The caller owns its own `input` (the page keeps it in the URL, the box keeps
 * it in local state) and passes it in; `runNow` skips the debounce for the
 * actions that are already deliberate, such as "Try fewer words".
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiFetch, ApiError } from "@/lib/api";
import { searchKeys } from "@/lib/query-keys";
import { useNavRole } from "@/components/shared/use-nav-role";
import {
  groupHits,
  searchState,
  totalRows,
  type Group,
  type SearchHit,
  type SearchRole,
  type SearchState,
} from "./results";

/** GET /api/v1/search envelope (search.SearchResult list). */
export interface SearchResponse {
  results: SearchHit[];
  total: number;
  query: string;
}

/** What a failed search says when the API's own message is not available. */
export const SEARCH_NO_ANSWER = "Search didn’t answer.";

export interface UseSearchQueryOptions {
  /** How long a pause promotes typed text to a search. Default 250 ms. */
  delayMs?: number;
  /** How long an answer stays fresh. Default 30 s (the page's value). */
  staleTime?: number;
}

export interface SearchQuery {
  /** The household role the rows were gated for (null while it loads). */
  role: SearchRole;
  /** The query that was actually searched (trimmed). */
  submitted: string;
  /** The role's groups for `submitted`, in display order. Empty while idle. */
  groups: Group[];
  /** Rows across those groups. */
  count: number;
  /** idle | loading | error | empty | results — the one state machine. */
  state: SearchState;
  loading: boolean;
  error: boolean;
  /** The API's message, or a plain "didn't answer". Never empty. */
  errorMessage: string;
  /** Re-read the same query (the error panel's Try again). */
  refetch: () => void;
  /** Search `q` now, skipping the debounce (the "fewer words" action). */
  runNow: (q: string) => void;
}

export function useSearchQuery(input: string, opts: UseSearchQueryOptions = {}): SearchQuery {
  const { delayMs = 250, staleTime = 30_000 } = opts;
  const role = useNavRole();

  // Seeded from the first input so a page loaded with ?q= does not flash idle
  // before its first effect runs.
  const [submitted, setSubmitted] = useState(() => input.trim());
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (timer.current) clearTimeout(timer.current);
    const next = input.trim();
    // Clearing the box is a real action: go idle at once, not 250 ms later.
    if (!next) {
      setSubmitted("");
      return;
    }
    timer.current = setTimeout(() => setSubmitted(next), delayMs);
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, [input, delayMs]);

  const q = useQuery({
    queryKey: searchKeys.results(submitted),
    queryFn: () => apiFetch<{ data: SearchResponse }>("/api/v1/search", { params: { q: submitted } }),
    enabled: submitted.length > 0,
    // No placeholder rows from the previous query: the summary line names the
    // query, so showing the previous query's rows under the new query's name
    // would be a false answer. A new query gets its own loading state.
    staleTime,
    // NO AUTOMATIC RETRIES. TanStack's default is 3 attempts with exponential
    // backoff, which keeps a failed search in `loading` for several seconds —
    // and a search box that sits on "Searching…" after the API already said 500
    // is the same silence this ticket exists to remove (#5303). A search is
    // cheap and side-effect free, so the failure is shown at once and the panel
    // offers Try again, which re-runs it on purpose.
    retry: false,
  });

  const groups = useMemo(() => groupHits(q.data?.data?.results ?? [], role), [q.data, role]);
  const count = totalRows(groups);
  const state = searchState({ query: submitted, loading: q.isLoading, error: q.isError, count });

  const runNow = useCallback((next: string) => setSubmitted(next.trim()), []);

  return {
    role,
    submitted,
    groups,
    count,
    state,
    loading: q.isLoading,
    error: q.isError,
    errorMessage: q.error instanceof ApiError ? q.error.message : SEARCH_NO_ANSWER,
    refetch: () => void q.refetch(),
    runNow,
  };
}
