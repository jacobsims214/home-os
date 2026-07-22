"use client";

import { Modal, Button, Group, Text } from "@mantine/core";

interface ConfirmDialogProps {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  message: string;
  confirmLabel?: string;
  loading?: boolean;
}

export default function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  message,
  confirmLabel = "Delete",
  loading = false,
}: ConfirmDialogProps) {
  return (
    <Modal opened={open} onClose={onClose} title={title} size="sm" centered>
      <Text size="sm" c="dimmed">{message}</Text>
      <Group justify="flex-end" mt="xl">
        <Button variant="subtle" onClick={onClose} disabled={loading}>Cancel</Button>
        <Button color="red" onClick={onConfirm} loading={loading}>{confirmLabel}</Button>
      </Group>
    </Modal>
  );
}
