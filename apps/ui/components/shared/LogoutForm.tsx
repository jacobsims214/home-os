"use client";

/**
 * The Log out form (#4424), shared by every place that offers it: the account
 * menu (header avatar on phone/tablet, sidebar user button on desktop) and the
 * drawer's foot.
 *
 * A native `<form method="post" action="/api/auth/logout">`: the route expires
 * the session cookies and 303s to /login, and the full page load drops the React
 * Query cache.
 *
 * Before that (#4648) the page purges what the session left in the browser —
 * Cache Storage (the pre-#4648 worker cached authenticated `/bff/*` responses
 * there), localStorage (`home-os-auth` profile, `home-os-recent` titles,
 * `home-os-property`), sessionStorage, IndexedDB — and clears the in-memory
 * user. A shared family device must not serve one person's household data to
 * the next one after this button is pressed.
 *
 * The submit is held back for that purge, but never for long: teardownSession()
 * bounds each step (2 s) and the form is submitted from `finally`, so a wedged
 * browser cannot trap the person on the page still holding a session. The
 * route's own `Clear-Site-Data` header then clears the rest anyway.
 *
 * It lives in its own module rather than inside Sidebar because the account
 * menu (#5198) renders the same form and Sidebar renders the account menu:
 * importing it from Sidebar would be a cycle.
 */

import { useRef } from "react";
import { teardownSession } from "@/lib/session-teardown";
import { useAuthStore } from "@/stores/auth";

export default function LogoutForm({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  const submitting = useRef(false);
  return (
    <form
      method="post"
      action="/api/auth/logout"
      className={className}
      onSubmit={(event) => {
        // A double submit while the purge is running must not queue a second one.
        if (submitting.current) {
          event.preventDefault();
          return;
        }
        const form = event.currentTarget;
        // No JS? The form posts and the route's Clear-Site-Data does the work.
        if (typeof window === "undefined") return;
        event.preventDefault();
        submitting.current = true;
        void purgeThenSubmit(form);
      }}
    >
      {children}
    </form>
  );
}

/**
 * Runs the teardown, then submits the native form (so the cookies still go).
 *
 * Order matters: dropping the in-memory user FIRST makes zustand's persist
 * middleware write `home-os-auth` one last time (with `user: null`), and the
 * purge that follows removes the key outright. The other way round leaves an
 * empty-looking key behind.
 */
async function purgeThenSubmit(form: HTMLFormElement): Promise<void> {
  try {
    useAuthStore.getState().setUser(null);
    await teardownSession({ unregister: true });
  } finally {
    form.submit();
  }
}
