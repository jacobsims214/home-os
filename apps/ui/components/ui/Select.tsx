"use client";

import { Select as MantineSelect } from "@mantine/core";

interface SelectOption {
  value: string;
  label: string;
}

interface SelectProps {
  label?: string;
  value: string;
  onChange: (e: { target: { value: string } }) => void;
  options?: SelectOption[];
  placeholder?: string;
  className?: string;
  disabled?: boolean;
}

export default function Select({
  label,
  value,
  onChange,
  options = [],
  placeholder,
  className,
  disabled,
}: SelectProps) {
  return (
    <MantineSelect
      label={label || undefined}
      value={value || null}
      onChange={(val) => onChange({ target: { value: val || "" } })}
      data={options}
      placeholder={placeholder}
      className={className}
      disabled={disabled}
      clearable
      searchable
    />
  );
}
