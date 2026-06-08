/**
 * Typed API client for Home OS.
 *
 * All data fetching goes through apiFetch(), which:
 * 1. Resolves the base URL from NEXT_PUBLIC_API_URL (dev) or relative path (prod).
 * 2. Injects the Bearer token from the Zustand auth store automatically.
 * 3. Automatically parses JSON responses and throws ApiError on non-2xx status.
 */

import { useAuthStore } from "@/stores/auth";

const BASE_URL = process.env.NEXT_PUBLIC_API_URL ?? "";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public body?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export interface ApiFetchOptions extends Omit<RequestInit, "body"> {
  /** JSON-serializable request body. Automatically sets Content-Type: application/json. */
  body?: unknown;
  /** Search params to append to the URL. */
  params?: Record<string, string | number | boolean | undefined>;
}

/**
 * Typed fetch wrapper for the Home OS API.
 * Automatically injects the Bearer token from the Zustand auth store.
 *
 * @example
 * const properties = await apiFetch<Property[]>("/api/v1/properties");
 * const asset = await apiFetch<Asset>(`/api/v1/assets/${id}`);
 */
export async function apiFetch<T = unknown>(
  path: string,
  options: ApiFetchOptions = {},
): Promise<T> {
  const { body, params, headers: initHeaders, ...init } = options;

  // Build URL — BASE_URL is the Go API origin in dev, empty string in prod (same origin via BFF)
  const origin =
    typeof window !== "undefined" ? window.location.origin : "http://localhost:3000";
  const url = new URL(`${BASE_URL}${path}`, origin);

  if (params) {
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined) {
        url.searchParams.set(key, String(value));
      }
    }
  }

  // Build headers
  const headers = new Headers(initHeaders);

  // Inject Bearer token from auth store (skip if caller already set Authorization)
  const token = useAuthStore.getState().token;
  if (token && !headers.has("Authorization")) {
    headers.set("Authorization", `Bearer ${token}`);
  }

  if (body !== undefined) {
    headers.set("Content-Type", "application/json");
  }

  const serializedBody =
    body !== undefined
      ? typeof body === "string"
        ? body
        : JSON.stringify(body)
      : undefined;

  const response = await fetch(url.toString(), {
    ...init,
    headers,
    credentials: "include",
    body: serializedBody,
  });

  if (!response.ok) {
    let errorBody: unknown;
    try {
      errorBody = await response.json();
    } catch {
      // response may not be JSON
    }
    throw new ApiError(
      response.status,
      `API ${response.status}: ${response.statusText}`,
      errorBody,
    );
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return response.json() as Promise<T>;
}
