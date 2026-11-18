import { localStorageColorSchemeManager, type MantineColorSchemeManager } from "@mantine/core";

/**
 * Colour scheme storage (#4529). No "use client": app/layout.tsx (a server
 * component) reads the key for ColorSchemeScript, so it must be a plain value.
 *
 * The app follows the OS scheme ("auto") until the user picks Light or Dark in
 * ColorSchemeControl. The choice is stored under this key as a bare string.
 */
export const COLOR_SCHEME_STORAGE_KEY = "home-os-color-scheme";

/** Before #4529 the Sidebar toggle stored a JSON string ('"dark"') here and
 *  the app forced light whenever it was missing. */
const LEGACY_KEY = "mantine-color-scheme";

/**
 * Mantine's localStorage manager, plus a one-time carry-over of an explicit
 * Dark choice made with the old toggle. A legacy "light" is dropped: it was
 * also the forced default, so it doesn't show the user chose it.
 */
export function homeOsColorSchemeManager(): MantineColorSchemeManager {
  const base = localStorageColorSchemeManager({ key: COLOR_SCHEME_STORAGE_KEY });
  return {
    ...base,
    get: (defaultValue) => {
      try {
        if (window.localStorage.getItem(COLOR_SCHEME_STORAGE_KEY) === null) {
          const legacy = window.localStorage.getItem(LEGACY_KEY);
          if (legacy === '"dark"') base.set("dark");
          if (legacy !== null) window.localStorage.removeItem(LEGACY_KEY);
        }
      } catch {
        // Storage blocked (private mode): fall through to Mantine's default.
      }
      return base.get(defaultValue);
    },
  };
}
