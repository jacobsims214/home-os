import { create } from "zustand";
import { persist } from "zustand/middleware";

export interface RecentItem {
  entity_type: string;
  entity_id: string;
  title: string;
  timestamp: number;
}

const MAX_ITEMS = 20;

interface RecentState {
  items: RecentItem[];
}

interface RecentActions {
  addItem: (item: Omit<RecentItem, "timestamp">) => void;
  getItems: () => RecentItem[];
  getRecent: (count?: number) => RecentItem[];
  clearItems: () => void;
}

export const useRecentStore = create<RecentState & RecentActions>()(
  persist(
    (set, get) => ({
      items: [],

      addItem: (item) =>
        set((state) => {
          // Deduplicate: remove any existing entry for the same entity
          const filtered = state.items.filter(
            (i) => !(i.entity_type === item.entity_type && i.entity_id === item.entity_id),
          );
          // Add the new item at the front
          const updated = [
            { ...item, timestamp: Date.now() },
            ...filtered,
          ].slice(0, MAX_ITEMS);
          return { items: updated };
        }),

      getItems: () => get().items,

      getRecent: (count = 6) => get().items.slice(0, count),

      clearItems: () => set({ items: [] }),
    }),
    {
      name: "home-os-recent",
      partialize: (state) => ({ items: state.items }),
    },
  ),
);