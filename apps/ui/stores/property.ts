import { create } from "zustand";
import { persist } from "zustand/middleware";

interface PropertyState {
  activePropertyId: string | null;
}

interface PropertyActions {
  setActiveProperty: (id: string) => void;
  clearActiveProperty: () => void;
}

export const usePropertyStore = create<PropertyState & PropertyActions>()(
  persist(
    (set) => ({
      activePropertyId: null,
      setActiveProperty: (id: string) => set({ activePropertyId: id }),
      clearActiveProperty: () => set({ activePropertyId: null }),
    }),
    {
      name: "home-os-property",
      partialize: (state) => ({ activePropertyId: state.activePropertyId }),
    },
  ),
);
