"use client";

import { Switch as MantineSwitch } from "@mantine/core";
import type { ReactNode } from "react";

interface SwitchProps {
  checked?: boolean;
  onChange?: (checked: boolean) => void;
  label?: ReactNode;
  disabled?: boolean;
  className?: string;
}

export default function Switch({
  checked,
  onChange,
  label,
  disabled,
  className,
}: SwitchProps) {
  return (
    <div className={`flex items-center gap-3 ${className}`}>
      <MantineSwitch
        checked={checked}
        onChange={(e) => onChange?.(e.currentTarget.checked)}
        disabled={disabled}
      />
      {label && <span className="text-sm font-medium text-gray-900">{label}</span>}
    </div>
  );
}
