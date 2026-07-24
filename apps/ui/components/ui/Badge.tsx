"use client";

import { Badge as MantineBadge } from "@mantine/core";
import type { BadgeProps as MantineBadgeProps } from "@mantine/core";
import type { ReactNode } from "react";

// Re-export Mantine Badge
// The MantineProvider already sets size="sm" and radius="xl" as defaults
export default MantineBadge;
export { MantineBadge };
export type { MantineBadgeProps as BadgeProps };
