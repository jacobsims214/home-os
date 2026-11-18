"use client";

/**
 * The sidebar / drawer search box (#4543), wired to the real search (#5303).
 *
 * WHAT WAS WRONG. Until #5303 this was a submit-only form: it kept the typed
 * text and, on `onSubmit`, did `router.push('/dashboard/search?q=…')`. Nothing
 * listened to the text itself, so typing produced no request, no drop-down and
 * no feedback at all — war game R2 (G4, Amber) is exactly that: "open nav
 * drawer, tap Search, type mortgage, wait 8 s: nothing happens". On a phone
 * there is no Enter key on the on-screen keyboard, so the box was a dead
 * control; Jacob's note about synthetic Enter not submitting is a second,
 * smaller defect on the same form (`requestSubmit()` now covers it).
 *
 * NOT the #5298 bug: that one was an empty search index, which shows as a
 * search that RUNS and answers "nothing matched". Here no request was sent at
 * all. Both are fixed; neither fix substitutes for the other.
 *
 * WHAT IT DOES NOW, and why each piece is here:
 *
 *  1. ONE REQUEST PER PAUSE. Typing goes through `useSearchQuery` — the same
 *     hook the Search page uses — so the debounce (250 ms), the endpoint
 *     (`GET /api/v1/search` through the BFF), the role gate and the state
 *     machine are the page's, not a second copy of them.
 *
 *  2. A DROP-DOWN THAT ANSWERS. Results appear under the box while it has
 *     focus, at most five rows per group, drawn by the page's own `ResultGroup`
 *     and `ResultRow` (components/search/SearchResults.tsx): same grouping,
 *     same facts (a bill's amount and due state, a document's date, a note's
 *     author), same 48 px tap target. Counting and copy come from the page's
 *     pure rules (`areasLine`, `noResultsCopy`, `searchState`).
 *
 *  3. NO SILENCE. Idle says what search covers; loading says it is searching;
 *     no-results names the query back; a failure is its own panel with Try
 *     again. That is the ticket's rule — the current bug is a box that does
 *     nothing, so doing nothing can't be the new behaviour in any state.
 *
 *  4. A PHONE CAN SUBMIT. There is an explicit Search button (44 px) beside the
 *     box, so a person never has to find Enter: it runs the current query and
 *     opens the full page. Enter in the box does the same through the form's
 *     submit, and the form's submit handler is the only navigation path.
 *
 *  5. THE DRAWER STAYS OUT OF THE WAY. The panel is capped (min(45dvh, 380px))
 *     and scrolls inside, so the nav rows under it stay reachable; the drawer's
 *     own scroll area is the shell's. Below md the panel is not a dialog — it is
 *     part of the drawer, so it does not fight the drawer's focus trap, and Esc
 *     closes the panel first (back to the box), then the drawer.
 *
 * The desktop sidebar gets the same box and the same panel: it has room, the
 * behaviour is identical, and a second variant of the control is exactly how
 * the two surfaces drifted apart in the first place.
 */

import { useCallback, useEffect, useId, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Button, Loader, Text, TextInput } from "@mantine/core";
import { IconAlertTriangle, IconSearch } from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import { Panel, ResultGroup } from "@/components/search/SearchResults";
import { useSearchQuery } from "@/lib/search/query";
import { useGroupFacts } from "@/lib/search/records";
import { areasLine, idleCopy, noResultsCopy, searchHref, type Group } from "@/lib/search/results";
import FileViewer from "@/components/files/FileViewer";
import type { FileRecord } from "@/lib/types/api";
import classes from "@/components/shared/Sidebar.module.css";

/** No groups: what a closed panel hands the record reads, so nothing is read. */
const NO_GROUPS: Group[] = [];

/**
 * A file record for the preview modal, from the hit alone. The drawer reads no
 * per-row records for documents it must open in a viewer: the modal's own
 * metadata read fills in what it can, and the row already carries the title.
 */
function viewerFileFromHit(hit: { file_id?: string; entity_id: string; title?: string }): FileRecord {
  return {
    id: hit.file_id || hit.entity_id,
    household_id: "",
    blob_id: "",
    name: hit.title || "Document",
    content_type: "",
    size: 0,
    ocr_status: "done",
    tags: [],
    created_at: "",
    updated_at: "",
  };
}

export default function SidebarSearch({ variant }: { variant: "sidebar" | "drawer" }) {
  const router = useRouter();
  const drawer = variant === "drawer";
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const panelId = useId();

  // The page's query, verbatim: debounce, endpoint, role gate, states.
  const search = useSearchQuery(query);
  const { groups, count, state, submitted, role } = search;
  // Rows are only completed for a panel that is on screen: with the box closed
  // the answer is still cached (TanStack, 30 s) but no record is re-read.
  const { records, ctx } = useGroupFacts(open ? groups : NO_GROUPS, role);

  // A document found from the drawer opens the same preview the page opens.
  const [viewer, setViewer] = useState<{ file: FileRecord } | null>(null);

  // "/" focuses the search, unless the user is already typing. The desktop
  // sidebar's shortcut, unchanged (#4543).
  useEffect(() => {
    if (drawer) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
      const input = inputRef.current;
      // Only when the sidebar is on screen (it's display:none below 62em).
      if (!input || input.offsetParent === null) return;
      e.preventDefault();
      input.focus();
      setOpen(true);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [drawer]);

  // A tap anywhere outside the box closes the panel; the typed text stays, so
  // the next tap puts the answer back without retyping.
  //
  // ON `click`, NOT `pointerdown`: the panel sits in the flow, so closing it
  // shortens the column and everything below moves UP. Closing on pointerdown
  // therefore moved the nav row out from under the finger between the press and
  // the release, and the tap landed on nothing (#5303 — caught by the drawer
  // spec's "a nav row still navigates" check). On click, the layout changes
  // only after the tap has been dispatched to the row it was aimed at.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const root = rootRef.current;
      if (!root || root.contains(e.target as Node)) return;
      // The preview modal is rendered in a portal: a tap inside it is not a tap
      // outside the search.
      const target = e.target as HTMLElement | null;
      if (target?.closest?.(".mantine-Modal-content")) return;
      setOpen(false);
    };
    document.addEventListener("click", onDown);
    return () => document.removeEventListener("click", onDown);
  }, [open]);

  /** Go to the full page for `q` — the deliberate "I want all of it" action. */
  const submit = useCallback(
    (e?: React.FormEvent) => {
      e?.preventDefault();
      const q = query.trim();
      if (!q) return;
      setOpen(false);
      router.push(searchHref(q));
    },
    [query, router],
  );

  const idle = idleCopy(role);
  const none = noResultsCopy(submitted, role);
  const areas = areasLine(role);
  const errorMessage = search.errorMessage;

  return (
    <div ref={rootRef} className={classes.searchRoot} data-search-root>
      <form role="search" onSubmit={submit} className={classes.search}>
        <TextInput
          ref={inputRef}
          type="search"
          value={query}
          onChange={(e) => {
            setQuery(e.currentTarget.value);
            setOpen(true);
          }}
          onFocus={() => setOpen(true)}
          placeholder="Search"
          aria-label="Search"
          /* Announcements come from the panel's live status line (below), not
             from aria-describedby on the input: Mantine's Input writes its own
             `aria-describedby` over any prop passed here (Input.cjs spreads
             ariaAttributes AFTER the rest props), so a description attached to
             the box would silently disappear. A live region is the normal way
             to announce "3 results" / "Nothing matched" as a person types, and
             it does not need the input to point at it. No aria-expanded /
             aria-controls either: those belong to the combobox pattern, and an
             input claiming them without the rest of it is an axe
             aria-allowed-attr violation (#4752's lesson). */
          leftSection={<IconSearch size={drawer ? 18 : 16} stroke={NAV_ICON_STROKE} aria-hidden />}
          rightSection={
            search.loading ? (
              <Loader size={drawer ? 16 : 14} aria-hidden />
            ) : drawer ? undefined : (
              <kbd className={classes.kbd} aria-hidden>
                /
              </kbd>
            )
          }
          rightSectionPointerEvents="none"
          classNames={{ input: classes.searchInput }}
          data-search-input
        />
        {/* A phone has no Enter key: the button IS how the full page is reached. */}
        <Button
          type="submit"
          variant="filled"
          aria-label="Search everything"
          className={classes.searchSubmit}
          data-search-submit
        >
          <IconSearch size={drawer ? 20 : 18} stroke={NAV_ICON_STROKE} aria-hidden />
        </Button>
      </form>

      {open && (
        <div
          id={panelId}
          className={classes.searchPanel}
          data-search-panel
          data-search-state={state}
          // A list of results, not a dialog: the drawer already owns the modal
          // semantics, and a second dialog inside it traps focus twice.
          role="region"
          aria-label={`Search for ${query.trim() || "…"}`}
          aria-busy={state === "loading" || undefined}
        >
          {/* The one spoken summary, at every state — the panel is drawn, and
              saying so is not optional (a box that answers silently is the bug
              this replaced). */}
          <span className={classes.visuallyHidden} role="status" data-search-status>
            {state === "idle" && idle.title}
            {state === "loading" && `Searching for ${submitted}`}
            {state === "empty" && none.title}
            {state === "error" && `Search didn’t answer. ${errorMessage}`}
            {state === "results" && `${count} ${count === 1 ? "result" : "results"} for ${submitted}`}
          </span>

          {/* Idle and loading are ONE line each, not a panel: the panel is in
              the flow, so every extra pixel here is a nav row that moves. The
              information still has to be there (an untouched box that says
              nothing is how the drawer read as broken), it just does not need
              a 40 px icon tile to say it. */}
          {state === "idle" && (
            <p className={classes.searchHint} data-search-state="idle">
              {areas ? `Searches ${areas}.` : idle.body}
            </p>
          )}

          {state === "loading" && (
            <p className={classes.searchHint} data-search-state="loading">
              Searching for “{submitted}”…
            </p>
          )}

          {state === "error" && (
            <Panel
              icon={IconAlertTriangle}
              tone="alert"
              title="Search didn’t answer"
              body={`${errorMessage} Nothing was changed.`}
              state="error"
              size="sm"
            >
              <Button variant="default" size="compact-md" mt={8} onClick={search.refetch} data-search-retry>
                Try again
              </Button>
            </Panel>
          )}

          {state === "empty" && (
            <Panel icon={IconSearch} title={none.title} body={none.body} state="no-results" size="sm">
              <Button
                variant="default"
                size="compact-md"
                mt={8}
                onClick={() => {
                  const words = submitted.split(/\s+/).filter(Boolean);
                  // The page's own cure, applied in place: drop the last word.
                  if (words.length > 1) {
                    const fewer = words.slice(0, -1).join(" ");
                    setQuery(fewer);
                    search.runNow(fewer);
                  } else {
                    setQuery("");
                    search.runNow("");
                  }
                }}
                data-search-fewer
              >
                {submitted.split(/\s+/).filter(Boolean).length > 1 ? "Try fewer words" : "Clear"}
              </Button>
            </Panel>
          )}

          {state === "results" && (
            <>
              <div className={classes.searchSummary}>
                <Text size="xs" style={{ color: "var(--ink-2)" }} data-search-summary>
                  <Text span fw={600} size="xs" style={{ color: "var(--ink)" }}>
                    {count}
                  </Text>{" "}
                  {count === 1 ? "result" : "results"} for “{submitted}”
                </Text>
                <Button variant="subtle" size="compact-xs" onClick={() => submit()} data-search-all>
                  See all
                </Button>
              </div>

              <div className={classes.searchGroups}>
                {groups.map((g) => (
                  <ResultGroup
                    key={g.def.id}
                    group={g}
                    records={records}
                    ctx={ctx}
                    // The drawer shows the first rows of each group and hands
                    // "all of it" to the page: expanding inside a 380 px panel
                    // would push the nav out of reach.
                    expanded={false}
                    // A record with no page of its own (a note, a guide) opens
                    // in the page's own modal, which the drawer does not host:
                    // the row goes to the page, which opens it from there by
                    // the query that found it. Never a dead tap.
                    onOpenRecord={() => router.push(searchHref(submitted))}
                    onOpenDocument={(hit) => {
                      setOpen(false);
                      setViewer({ file: viewerFileFromHit(hit) });
                    }}
                    idPrefix="nav-search"
                    // The panel is ~230 px wide: one line per row part, a
                    // 40 px row, facts that ellipsize. Same component and data.
                    dense
                  />
                ))}
              </div>
            </>
          )}
        </div>
      )}

      <FileViewer file={viewer?.file ?? null} open={viewer !== null} onClose={() => setViewer(null)} />
    </div>
  );
}
