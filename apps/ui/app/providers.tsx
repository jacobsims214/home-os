"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ReactQueryDevtools } from "@tanstack/react-query-devtools";
import {
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
// primaryShade applies to every colour, not just cyan. Light 9 keeps text
// such as red/green/orange light-variant labels above 4.5:1. Dark stays at 8,
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
  "--mantine-color-cyan-filled": FILLED,
  "--mantine-color-cyan-filled-hover": FILLED_HOVER,
  "--mantine-primary-color-filled": FILLED,
  "--mantine-primary-color-filled-hover": FILLED_HOVER,
  "--mantine-primary-color-contrast": "var(--mantine-color-white)",
  // Mantine's `dimmed` (gray.6) is 3.32:1 on white; the token passes AA.
  "--mantine-color-dimmed": "var(--ink-3)",
  "--mantine-color-text": "var(--ink)",
  "--mantine-color-anchor": "var(--accent)",
};

const cssVariablesResolver: CSSVariablesResolver = () => ({
  variables: {},
  light: sharedVariables,
  dark: sharedVariables,
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
