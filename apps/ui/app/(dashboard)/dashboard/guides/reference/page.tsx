"use client";

/**
 * The family reference (HOME-OS-5503): `/dashboard/guides/reference`.
 *
 * This is the KB's list — every doc the household has, grouped by the first
 * part of its path, with a search that hits the KB by meaning.
 *
 * WHY IT IS UNDER /guides AND NOT ITS OWN NAV SECTION. See
 * components/kb/ReferenceList.tsx and the ticket's decision: guides and the
 * reference are the same JOB to a person and different RECORDS to the database,
 * so they share one nav section, one page family and one markdown renderer,
 * and the switch below says which is which in three words. Two tiles would
 * send a person with a router fix to a coin toss.
 *
 * The app header's h1 comes from nav-config's ROUTE_TITLES ("Reference"); the
 * h2 below is this page's own heading inside the Guides section.
 */

import { useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import ReferenceList from "@/components/kb/ReferenceList";
import DocEditor from "@/components/kb/DocEditor";
import LibrarySwitch from "@/components/kb/LibrarySwitch";
import { ResponsiveModal } from "@/components/ui";
import { kbKeys } from "@/lib/query-keys";

export default function ReferencePage() {
  const pathname = usePathname() ?? "/dashboard/guides/reference";
  const router = useRouter();
  const queryClient = useQueryClient();
  const [newOpen, setNewOpen] = useState(false);

  return (
    <div className="mx-auto w-full max-w-3xl min-w-0">
      <LibrarySwitch pathname={pathname} />
      <h2 className="mb-3 text-xl font-semibold text-ink md:text-2xl">Reference</h2>

      {/* The list owns its own "New doc" button and only offers it to a role
          that may write, so the modal can be reached by nobody else. */}
      <ReferenceList onNewDoc={() => setNewOpen(true)} />

      <ResponsiveModal
        opened={newOpen}
        onClose={() => setNewOpen(false)}
        title="New reference doc"
        size="lg"
      >
        <DocEditor
          doc={null}
          onCancel={() => setNewOpen(false)}
          onSaved={(saved) => {
            // Navigate after closing: ResponsiveModal pushes a history entry
            // for the back button, and its close pops it — a pop in the same
            // tick would cancel a push. Invalidating first makes the new doc
            // appear in the list behind the page we are leaving.
            queryClient.invalidateQueries({ queryKey: kbKeys.all });
            setNewOpen(false);
            router.push(`/dashboard/guides/reference/${saved.path}`);
          }}
        />
      </ResponsiveModal>
    </div>
  );
}
