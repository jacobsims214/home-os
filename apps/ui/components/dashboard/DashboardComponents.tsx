"use client";

export function StatCard({
  label,
  value,
  sublabel,
  icon,
  alert,
}: {
  label: string;
  value: string;
  sublabel: string;
  icon: React.ReactNode;
  alert?: boolean;
}) {
  return (
    <div className="rounded-xl bg-white/80 backdrop-blur-sm border border-gray-200/50 shadow-sm px-5 py-4">
      <div className="flex items-center justify-between">
        <div>
          <p className="text-sm font-medium text-gray-500">{label}</p>
          <p className={`mt-1 text-3xl font-bold tracking-tight ${alert ? "text-red-600" : "text-gray-900"}`}>
            {value}
          </p>
          <p className="mt-0.5 text-xs text-gray-400">{sublabel}</p>
        </div>
        <div className={`flex h-12 w-12 items-center justify-center rounded-lg ${alert ? "bg-red-100 text-red-600" : "bg-[#7C3AED]/10 text-[#7C3AED]"}`}>
          {icon}
        </div>
      </div>
    </div>
  );
}

type MaintenanceStatus = "pending" | "in_progress" | "done" | "skipped";

const statusStyles: Record<MaintenanceStatus, string> = {
  pending: "bg-yellow-100 text-yellow-800",
  in_progress: "bg-blue-100 text-blue-800",
  done: "bg-green-100 text-green-800",
  skipped: "bg-gray-100 text-gray-600",
};

const statusLabels: Record<MaintenanceStatus, string> = {
  pending: "Pending",
  in_progress: "In Progress",
  done: "Done",
  skipped: "Skipped",
};

export function StatusBadge({ status }: { status: MaintenanceStatus }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${statusStyles[status]}`}>
      {statusLabels[status]}
    </span>
  );
}