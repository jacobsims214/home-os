"use client";

import { useAuthStore } from "@/stores/auth";
import { useMemo } from "react";

/**
 * Extracts the household_id from the JWT token payload.
 *
 * The JWT is stored in the Zustand auth store. This hook decodes
 * the payload (without verification — the Go API handles that) and
 * returns the household_id string. Returns null if no token or
 * if decoding fails.
 */
export function useHouseholdId(): string | null {
  const token = useAuthStore((s) => s.token);

  return useMemo(() => {
    if (!token) return null;

    try {
      const [, payloadB64] = token.split(".");
      if (!payloadB64) return null;

      const payload = JSON.parse(
        Buffer.from(payloadB64, "base64url").toString("utf-8"),
      );
      return (payload.household_id as string) ?? null;
    } catch {
      return null;
    }
  }, [token]);
}
