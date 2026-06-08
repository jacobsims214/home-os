"use client";

import { cn } from "@/lib/cn";

interface SelectProps extends React.SelectHTMLAttributes<HTMLSelectElement> {
  label: string;
  error?: string;
  options: { value: string; label: string }[];
  placeholder?: string;
}

export default function Select({
  label,
  error,
  id,
  options,
  placeholder,
  className = "",
  ...props
}: SelectProps) {
  const selectId = id ?? label.toLowerCase().replace(/\s+/g, "-");

  return (
    <div className={className}>
      <label
        htmlFor={selectId}
        className="block text-sm font-medium text-gray-900"
      >
        {label}
      </label>
      <select
        id={selectId}
        className={cn(
          "mt-1 block w-full rounded-md border px-3 py-2 text-sm text-gray-900 shadow-sm",
          "focus:outline-none focus:ring-2 focus:ring-inset focus:ring-indigo-600",
          error ? "border-red-500" : "border-gray-300",
        )}
        aria-invalid={!!error}
        aria-describedby={error ? `${selectId}-error` : undefined}
        {...props}
      >
        {placeholder && (
          <option value="">{placeholder}</option>
        )}
        {options.map((opt) => (
          <option key={opt.value} value={opt.value}>
            {opt.label}
          </option>
        ))}
      </select>
      {error && (
        <p id={`${selectId}-error`} className="mt-1 text-sm text-red-600">
          {error}
        </p>
      )}
    </div>
  );
}
