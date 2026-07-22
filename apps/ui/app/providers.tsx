"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ReactQueryDevtools } from "@tanstack/react-query-devtools";
import { MantineProvider, createTheme } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { ModalsProvider } from "@mantine/modals";
import { useState, type ReactNode } from "react";

const theme = createTheme({
  primaryColor: "cyan",
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
    <MantineProvider theme={theme}>
      <QueryClientProvider client={queryClient}>
        <ModalsProvider>
          <Notifications position="top-right" />
          {children}
        </ModalsProvider>
        <ReactQueryDevtools initialIsOpen={false} />
      </QueryClientProvider>
    </MantineProvider>
  );
}
