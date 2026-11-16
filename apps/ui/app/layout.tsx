import type { Metadata } from "next";
import { ColorSchemeScript, mantineHtmlProps } from "@mantine/core";
import "@mantine/core/styles.css";
import "@mantine/notifications/styles.css";
import "@mantine/dates/styles.css";
import "./globals.css";
import Providers from "./providers";
import { COLOR_SCHEME_STORAGE_KEY } from "@/components/ui/color-scheme";

export const metadata: Metadata = {
  title: "Home OS",
  description: "Your household operating system — manage properties, assets, maintenance, and more.",
  manifest: "/manifest.json",
  appleWebApp: {
    capable: true,
    statusBarStyle: "default",
    title: "Home OS",
  },
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" {...mantineHtmlProps}>
      <head>
        {/* Sets data-mantine-color-scheme before first paint (system scheme
            unless the user picked one), so dark mode doesn't flash light. */}
        <ColorSchemeScript defaultColorScheme="auto" localStorageKey={COLOR_SCHEME_STORAGE_KEY} />
        <link rel="preconnect" href="https://fonts.googleapis.com" />
        <link rel="preconnect" href="https://fonts.gstatic.com" crossOrigin="anonymous" />
        <link
          href="https://fonts.googleapis.com/css2?family=Fira+Sans:wght@300;400;500;600;700&family=Fira+Code:wght@400;500;600;700&display=swap"
          rel="stylesheet"
        />
      </head>
      <body>
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
