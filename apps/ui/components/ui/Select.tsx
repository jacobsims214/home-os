"use client";

import { Select as MantineSelect } from "@mantine/core";
import type { SelectProps as MantineSelectProps } from "@mantine/core";

// Re-export Mantine Select with size="sm" as default
// The MantineProvider already sets size="sm" as default
export default MantineSelect;
export { MantineSelect };
export type { MantineSelectProps as SelectProps };
