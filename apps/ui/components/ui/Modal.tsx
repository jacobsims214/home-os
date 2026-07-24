"use client";

import { Modal as MantineModal } from "@mantine/core";
import type { ModalProps as MantineModalProps } from "@mantine/core";
import type { ReactNode } from "react";

// Re-export Mantine Modal with default size and padding
// The MantineProvider sets defaultRadius="md" for components
// We keep the wrapper to provide centered prop and consistent defaults
export default MantineModal;
export { MantineModal };
export type { MantineModalProps as ModalProps };
