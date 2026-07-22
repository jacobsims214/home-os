"use client";

import { Badge as MantineBadge } from "@mantine/core";
import type { ReactNode } from "react";

interface BadgeProps {
  children?: ReactNode;
  status?: string;
  onClick?: () => void;
  className?: string;
  color?: string;
  variant?: string;
  size?: string;
}

const statusColors: Record<string, string> = {
  pending: "yellow",
  in_progress: "blue",
  done: "green",
  skipped: "gray",
};

export default function Badge({
  children,
  status,
  onClick,
  className,
  color,
  variant,
  size,
}: BadgeProps) {
  const badgeColor = color || (status ? statusColors[status] || "gray" : "gray");

  return (
    <MantineBadge
      color={badgeColor}
      variant={variant === "outline" ? "light" : "filled"}
      size={size as any}
      className={className}
      onClick={onClick}
      style={onClick ? { cursor: "pointer" } : undefined}
    >
      {children || status}
    </MantineBadge>
  );
}
