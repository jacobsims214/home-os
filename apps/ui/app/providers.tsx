"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ReactQueryDevtools } from "@tanstack/react-query-devtools";
import {
  DEFAULT_THEME,
  MantineProvider,
  createTheme,
  type CSSVariablesResolver,
} from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { ModalsProvider } from "@mantine/modals";
import { useState, type ReactNode } from "react";
import { homeOsColorSchemeManager } from "@/components/ui/color-scheme";

// Theme tokens (#4529), per KB design/app-shell.md (artifact #75 v1). The
// semantic colours (--bg, --surface, --ink, --accent, ...) are defined for
// both schemes in app/globals.css. This theme only lines Mantine up with them.
//
// Accent text and icons are cyan.9 in light and cyan.3 in dark. Filled
// surfaces (buttons, checkboxes, switches) stay cyan.9 with white text in both
// schemes; see cssVariablesResolver below.
//
// primaryShade applies to every colour, not just cyan. Light 9 is Mantine's
// darkest shade, but it is still not dark enough for every colour's text. See
// the per-colour fixes (#4754) above cssVariablesResolver. Dark stays at 8,
// Mantine's default: Mantine derives dark text from it (light-color = shade
// 8 - 5 = cyan.3, the spec's dark accent), while a dark shade of 3 would put
// white text on pale fills for every colour (white on red.3 is 2.3:1).
const theme = createTheme({
  primaryColor: "cyan",
  primaryShade: { light: 9, dark: 8 },
  fontFamily: "Fira Sans, sans-serif",
  fontFamilyMonospace: "Fira Code, monospace",
  defaultRadius: "md",
  fontSizes: {
    xs: "0.75rem",
    sm: "0.875rem",
    md: "1rem",
    lg: "1.125rem",
    xl: "1.25rem",
  },
  headings: {
    fontFamily: "Fira Sans, sans-serif",
    fontWeight: "600",
    sizes: {
      h1: { fontSize: "2rem", lineHeight: "1.2" },
      h2: { fontSize: "1.5rem", lineHeight: "1.3" },
      h3: { fontSize: "1.25rem", lineHeight: "1.4" },
    },
  },
  components: {
    Button: {
      defaultProps: {
        size: "sm",
        fw: "600",
      },
    },
    Card: {
      defaultProps: {
        padding: "lg",
        shadow: "sm",
        radius: "md",
      },
    },
    TextInput: {
      defaultProps: {
        size: "sm",
      },
    },
    Select: {
      defaultProps: {
        size: "sm",
      },
    },
    Textarea: {
      defaultProps: {
        size: "sm",
      },
    },
    Badge: {
      defaultProps: {
        size: "sm",
        radius: "xl",
      },
    },
  },
});

// White on cyan.9 is 5.59:1. Mantine's default hover for shade 9 is cyan.8,
// which drops white text below 4.5:1, so hover darkens cyan.9 instead.
const FILLED = "var(--mantine-color-cyan-9)";
const FILLED_HOVER = "color-mix(in srgb, var(--mantine-color-cyan-9) 82%, #000)";

const sharedVariables = {
  "--mantine-primary-color-filled": FILLED,
  "--mantine-primary-color-filled-hover": FILLED_HOVER,
  "--mantine-primary-color-contrast": "var(--mantine-color-white)",
  // Mantine's `dimmed` (gray.6) is 3.32:1 on white; the token passes AA.
  "--mantine-color-dimmed": "var(--ink-3)",
  "--mantine-color-text": "var(--ink)",
  "--mantine-color-anchor": "var(--accent)",
};

// Per-colour contrast fixes for Badge, Alert, Button, ThemeIcon and Text c=
// (#4754). With primaryShade { light: 9, dark: 8 }, Mantine's stock values miss
// 4.5:1 for these WCAG 2 pairs. The worst case is each variant's tint composited
// over --bg, --surface and --surface-2 (light) or --bg, --surface, --surface-2
// and --raised (dark):
//   light variant, light scheme: shade 9 on its own 10% tint
//     yellow 2.45, lime 2.98, orange 3.39, green 3.49, teal 3.95, red 4.22,
//     cyan 4.39
//   filled, white text on shade 9 (light) / shade 8 (dark)
//     yellow 3.00 / 2.48, lime 3.69 / 2.93, orange 4.30 / 3.58,
//     green 4.37 / 3.45, teal 5.00 / 3.95
//   outline and c= text, dark scheme: violet.4 4.04
// The replacements are shade 9 darkened only as far as needed. Yellow text
// uses --warn-ink, the warn-chip ink from the KB spec. After the fix, worst case:
//   light variant, light scheme: yellow 6.28, red 4.66, cyan 4.70, teal 4.69,
//     green 4.63, lime 4.62, orange 4.62 (blue 4.76, indigo 5.27 unchanged)
//   light variant, dark scheme (stock shade 3 on a 15% shade-6 tint): yellow
//     7.63, orange 6.68, green 7.11, red 6.20, teal 6.95, blue 5.77
//   filled, white text, light / dark: yellow 5.70, orange 5.85, green 5.80,
//     teal 5.94, lime 5.71, cyan 5.59; red 5.46 / 4.51 and blue 6.09 / 5.02 stock
//   outline and text, dark scheme: violet.3 5.63
// Dark-scheme light variants for every palette colour reach at least 4.96:1.
// app/theme-tokens.test.mjs parses these three objects and re-measures every
// pair for every palette colour, so they must stay JSON.
const LIGHT_INK: Record<string, string> = {
  "yellow": "var(--warn-ink)",
  "orange": "#b43c0c",
  "lime": "#47720a",
  "green": "#247434",
  "teal": "#077252",
  "cyan": "#0b6d80",
  "red": "#bd2727"
};
const FILLED_BG: Record<string, string> = {
  "cyan": "var(--mantine-color-cyan-9)",
  "yellow": "#9f5200",
  "orange": "#b43c0c",
  "lime": "#47720a",
  "green": "#247434",
  "teal": "#077252"
};
const DARK_INK: Record<string, string> = {
  "violet": "var(--mantine-color-violet-3)"
};

function colourFixes(scheme: "light" | "dark"): Record<string, string> {
  const vars: Record<string, string> = {};
  for (const color of Object.keys(DEFAULT_THEME.colors)) {
    // Mantine's light-scheme hover is shade 8, which is lighter than the shade 9
    // fill and loses contrast with white. Darken the fill instead.
    vars[`--mantine-color-${color}-filled-hover`] =
      `color-mix(in srgb, var(--mantine-color-${color}-filled) 82%, #000)`;
  }
  for (const [color, value] of Object.entries(FILLED_BG)) {
    vars[`--mantine-color-${color}-filled`] = value;
  }
  // In the light scheme, -text (Text c="green") is var(-filled), so it follows FILLED_BG.
  const ink = scheme === "light" ? LIGHT_INK : DARK_INK;
  for (const [color, value] of Object.entries(ink)) {
    vars[`--mantine-color-${color}-light-color`] = value;
    vars[`--mantine-color-${color}-outline`] = value;
    if (scheme === "dark") vars[`--mantine-color-${color}-text`] = value;
  }
  return vars;
}

const cssVariablesResolver: CSSVariablesResolver = () => ({
  variables: {},
  light: { ...colourFixes("light"), ...sharedVariables },
  dark: { ...colourFixes("dark"), ...sharedVariables },
});

// Follows the OS scheme ("auto") until the user picks Light or Dark in the
// account menu (ColorSchemeControl). See components/ui/color-scheme.ts.
const colorSchemeManager = homeOsColorSchemeManager();

export default function Providers({ children }: { children: ReactNode }) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 30_000,
            refetchOnWindowFocus: false,
          },
        },
      }),
  );

  return (
    <MantineProvider
      theme={theme}
      defaultColorScheme="auto"
      colorSchemeManager={colorSchemeManager}
      cssVariablesResolver={cssVariablesResolver}
    >
      <QueryClientProvider client={queryClient}>
        <ModalsProvider>
          <Notifications position="top-right" />
          {children}
        </ModalsProvider>
        {/* Dev only (renders nothing in production builds). The E2E run turns
            it off: its floating button sits over the phone tab bar's More tab. */}
        {process.env.NEXT_PUBLIC_QUERY_DEVTOOLS !== "0" && <ReactQueryDevtools initialIsOpen={false} />}
      </QueryClientProvider>
    </MantineProvider>
  );
}
