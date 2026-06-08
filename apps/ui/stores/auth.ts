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
  logout: () => void;
}

export const useAuthStore = create<AuthState & AuthActions>()(
  persist(
    (set) => ({
      user: null,
      token: null,
      setAuth: (user: User, token: string) => set({ user, token }),
      logout: () => set({ user: null, token: null }),
    }),
    {
      name: "home-os-auth",
      // Only persist user and token — not actions
      partialize: (state) => ({ user: state.user, token: state.token }),
    },
  ),
);
