"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { usePropertyStore } from "@/stores/property";
import type { PropertyResponse } from "@/types/property";

interface DashboardHeaderProps {
  currentProperty: PropertyResponse | undefined;
  propertiesData: PropertyResponse[] | undefined;
  propertyId: string;
}

export default function DashboardHeader({
  currentProperty,
  propertiesData,
  propertyId,
}: DashboardHeaderProps) {
  const setActiveProperty = usePropertyStore((s) => s.setActiveProperty);
  const [showSwitcher, setShowSwitcher] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setShowSwitcher(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  const handleSwitch = useCallback(
    (id: string) => {
      setActiveProperty(id);
      setShowSwitcher(false);
    },
    [setActiveProperty],
  );

  return (
    <div className="mb-8">
      {/* Property name + switcher */}
      <div className="mb-4 flex items-center gap-3" ref={dropdownRef}>
        <div className="relative flex-1">
          <button
            onClick={() => setShowSwitcher(!showSwitcher)}
            className="flex items-center gap-2 text-2xl font-bold text-[#4C1D95] hover:text-[#7C3AED] transition-colors"
          >
            <svg className="h-7 w-7" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" d="M2.25 12l8.954-8.955a1.126 1.126 0 011.591 0L21.75 12M4.5 9.75v10.125c0 .621.504 1.125 1.125 1.125H9.75v-4.875c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125V21h4.125c.621 0 1.125-.504 1.125-1.125V9.75M8.25 21h8.25" />
            </svg>
            {currentProperty?.name ?? "Select Property"}
            {currentProperty?.address && (
              <span className="ml-2 text-base font-normal text-gray-500">
                {currentProperty.address}
              </span>
            )}
            <svg
              className={`h-5 w-5 text-[#7C3AED] transition-transform ${showSwitcher ? "rotate-180" : ""}`}
              fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor"
            >
              <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 8.25l-7.5 7.5-7.5-7.5" />
            </svg>
          </button>

          {/* Dropdown */}
          {showSwitcher && (
            <div className="absolute left-0 top-full z-20 mt-1 w-72 rounded-xl border border-gray-200 bg-white shadow-lg">
              <div className="p-2">
                {(propertiesData ?? []).map((p) => (
                  <button
                    key={p.id}
                    onClick={() => handleSwitch(p.id)}
                    className={`flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left text-sm transition-colors ${
                      p.id === propertyId
                        ? "bg-[#7C3AED]/10 text-[#7C3AED]"
                        : "text-gray-700 hover:bg-gray-50"
                    }`}
                  >
                    <svg className="h-5 w-5 shrink-0 text-gray-400" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                      <path strokeLinecap="round" strokeLinejoin="round" d="M2.25 12l8.954-8.955a1.126 1.126 0 011.591 0L21.75 12M4.5 9.75v10.125c0 .621.504 1.125 1.125 1.125H9.75v-4.875c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125V21h4.125c.621 0 1.125-.504 1.125-1.125V9.75M8.25 21h8.25" />
                    </svg>
                    <div className="min-w-0 flex-1">
                      <p className="font-medium truncate">{p.name}</p>
                      {p.address && <p className="text-xs text-gray-500 truncate">{p.address}</p>}
                    </div>
                    {p.id === propertyId && (
                      <svg className="h-4 w-4 shrink-0 text-[#7C3AED]" fill="none" viewBox="0 0 24 24" strokeWidth={2.5} stroke="currentColor">
                        <path strokeLinecap="round" strokeLinejoin="round" d="M4.5 12.75l6 6 9-13.5" />
                      </svg>
                    )}
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>

      {/* Search bar */}
      <div className="relative">
        <div className="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-3">
          <svg className="h-5 w-5 text-gray-400" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-5.197-5.197m0 0A7.5 7.5 0 105.196 5.196a7.5 7.5 0 0010.607 10.607z" />
          </svg>
        </div>
        <form action={`/dashboard/search?property_id=${propertyId}`} method="GET">
          <input
            name="q"
            type="text"
            placeholder={`Search ${currentProperty?.name ?? "Main Residence"}...`}
            className="w-full rounded-xl border border-gray-200/50 bg-white/80 py-3 pl-10 pr-4 text-sm text-gray-900 placeholder:text-gray-400 focus:border-[#7C3AED] focus:outline-none focus:ring-1 focus:ring-[#7C3AED] shadow-sm backdrop-blur-sm"
          />
        </form>
      </div>
    </div>
  );
}