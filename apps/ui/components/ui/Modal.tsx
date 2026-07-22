"use client";

import { Modal as MantineModal } from "@mantine/core";
import type { ReactNode } from "react";

interface ModalProps {
  open: boolean;
  onClose: () => void;
  title?: string;
  children: ReactNode;
  maxWidth?: string;
  size?: string | number;
}

export default function Modal({
  open,
  onClose,
  title,
  children,
  maxWidth,
  size,
}: ModalProps) {
  const modalSize = size || (maxWidth === "max-w-2xl" ? "lg" : maxWidth === "max-w-lg" ? "md" : "md");

  return (
    <MantineModal
      opened={open}
      onClose={onClose}
      title={title}
      size={modalSize as any}
      centered
    >
      {children}
    </MantineModal>
  );
}
