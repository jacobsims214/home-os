/**
 * Typed API client for Home OS.
 *
 * All data fetching goes through apiFetch(), which:
 * 1. Resolves the base URL from NEXT_PUBLIC_API_URL (dev) or relative path (prod).
 * 2. Injects the Bearer token from the Zustand auth store, with cookie fallback.
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

/** Get the auth token from Zustand store, with cookie fallback for hydration races. */
function getAuthToken(): string | null {
  // Try Zustand store first
  const storeToken = useAuthStore.getState().token;
  if (storeToken) return storeToken;

  // Fallback: read from cookie (set by the BFF /api/auth route)
  if (typeof document !== "undefined") {
    const cookie = document.cookie
      .split("; ")
      .find((c) => c.startsWith("home-os-token="));
    if (cookie) {
      const token = cookie.split("=")[1];
      // Hydrate the store so subsequent calls don't need to read the cookie
      useAuthStore.setState({ token });
      return token;
    }
  }

  return null;
}

/**
 * Typed fetch wrapper for the Home OS API.
 * Automatically injects the Bearer token from the auth store or cookie.
 */
export async function apiFetch<T = unknown>(
  path: string,
  options: ApiFetchOptions = {},
): Promise<T> {
  const { body, params, headers: initHeaders, ...init } = options;

  // Build URL
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

  // Inject Bearer token (skip if caller already set Authorization)
  const token = getAuthToken();
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
