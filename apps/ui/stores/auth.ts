import { create } from "zustand";
import { persist } from "zustand/middleware";

export interface User {
  id: string;
  email: string;
  name: string;
  avatar_url: string | null;
}

interface AuthState {
  user: User | null;
  token: string | null;
}

interface AuthActions {
  setAuth: (user: User, token: string) => void;
  logout: () => Promise<void>;
}

export const useAuthStore = create<AuthState & AuthActions>()(
  persist(
    (set) => ({
      user: null,
      token: null,
      setAuth: (user: User, token: string) => set({ user, token }),
      logout: async () => {
        // Clear the auth cookie by calling the API route with a DELETE
        try {
          await fetch("/api/auth", { method: "DELETE" });
        } catch {
          // Ignore errors — cookie may already be gone
        }
        // Clear the Zustand store (and persisted localStorage)
        set({ user: null, token: null });
      },
    }),
    {
      name: "home-os-auth",
      partialize: (state) => ({ user: state.user, token: state.token }),
    },
  ),
);
