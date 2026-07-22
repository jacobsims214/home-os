"use client";

import { useRef } from "react";
import { useAuthStore } from "@/stores/auth";

/**
 * Synchronously hydrates the Zustand auth store with the token from the
 * server-side cookie before any child components render.
 *
 * This solves the race condition where useQuery fires before Zustand's
 * localStorage persist middleware has rehydrated the store.
 */
export default function AuthHydrator({
  token,
  children,
}: {
  token: string;
  children: React.ReactNode;
}) {
  // Use a ref so we only hydrate once per mount, not on every render
  const hydrated = useRef(false);

  if (!hydrated.current) {
    hydrated.current = true;
    // Only set if the store doesn't already have a token
    // (avoids overwriting a fresher in-memory token)
    if (!useAuthStore.getState().token && token) {
      // Set the token immediately so apiFetch has it before any useQuery fires.
      // We don't have the full user object here — the /me call will fill it in,
      // but the token is enough for authenticated API calls to work.
      useAuthStore.setState((s) => ({
        ...s,
        token,
        // Preserve existing user if already set
        user: s.user,
      }));
    }
  }

  return <>{children}</>;
}
