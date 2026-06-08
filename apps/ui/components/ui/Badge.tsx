"use client";

interface BadgeProps {
  status: "pending" | "in_progress" | "done" | "skipped";
  onClick?: () => void;
  className?: string;
}

const statusStyles: Record<BadgeProps["status"], string> = {
  pending:
    "bg-yellow-100 text-yellow-800 border-yellow-300",
  in_progress:
    "bg-blue-100 text-blue-800 border-blue-300",
  done:
    "bg-green-100 text-green-800 border-green-300",
  skipped:
    "bg-gray-100 text-gray-500 border-gray-300 line-through",
};

const statusLabels: Record<BadgeProps["status"], string> = {
  pending: "Pending",
  in_progress: "In Progress",
  done: "Done",
  skipped: "Skipped",
};

export default function Badge({ status, onClick, className = "" }: BadgeProps) {
  return (
    <span
      role={onClick ? "button" : undefined}
      tabIndex={onClick ? 0 : undefined}
      onClick={onClick}
      onKeyDown={
        onClick
          ? (e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                onClick();
              }
            }
          : undefined
      }
      className={`inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-medium ${
        onClick ? "cursor-pointer select-none hover:opacity-80" : ""
      } ${statusStyles[status]} ${className}`}
    >
      {statusLabels[status]}
    </span>
  );
}
