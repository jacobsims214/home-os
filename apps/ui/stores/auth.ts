import { create } from "zustand";
import { persist } from "zustand/middleware";

export interface User {
  id: string;
  email: string;
  name: string;
  avatar_url: string | null;
}

/**
 * Client-side auth state: the signed-in user's profile only.
 *
 * The session token lives in the httpOnly `home-os-token` cookie and is
 * attached server-side by the /bff proxy. It is never held in JS or
 * persisted to localStorage.
 */
interface AuthState {
  user: User | null;
}

interface AuthActions {
  setUser: (user: User | null) => void;
  logout: () => Promise<void>;
}

export const useAuthStore = create<AuthState & AuthActions>()(
  persist(
    (set) => ({
      user: null,
      setUser: (user: User | null) => set({ user }),
      logout: async () => {
        // Clear the session cookie via the auth route handler.
        try {
          await fetch("/api/auth", { method: "DELETE" });
        } catch {
          // Ignore errors — cookie may already be gone
        }
        set({ user: null });
      },
    }),
    {
      name: "home-os-auth",
      // Version 1 drops the `token` field persisted by earlier builds.
      version: 1,
      migrate: (persisted) => {
        const state = (persisted ?? {}) as Partial<AuthState>;
        return { user: state.user ?? null } as AuthState & AuthActions;
      },
      partialize: (state) => ({ user: state.user }),
    },
  ),
);
