"use client";

import { SegmentedControl, useMantineColorScheme, type MantineColorScheme } from "@mantine/core";
import { useEffect, useState } from "react";

/**
 * Light / Dark / System picker (#4529), for the account menu's "Theme" row in
 * KB design/app-shell.md. "System" follows prefers-color-scheme, and it's the
 * default. The choice persists via the colour scheme manager in providers.tsx.
 */
export default function ColorSchemeControl({ fullWidth = true }: { fullWidth?: boolean }) {
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  // The stored choice is only known on the client; render "System" on the
  // server and on first paint so hydration matches.
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  return (
    <SegmentedControl
      aria-label="Theme"
      size="xs"
      fullWidth={fullWidth}
      value={mounted ? colorScheme : "auto"}
      onChange={(value) => setColorScheme(value as MantineColorScheme)}
      data={[
        { label: "Light", value: "light" },
        { label: "Dark", value: "dark" },
        { label: "System", value: "auto" },
      ]}
    />
  );
}
