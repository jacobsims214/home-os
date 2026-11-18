/**
 * Dex settings for the UI's OIDC login (server-side only: reads process.env).
 *
 * One source for app/api/auth/login, app/api/auth/callback and the BFF refresh
 * (app/bff/[...path]/route.ts), so the authorize request, the code exchange
 * and the refresh always agree (#4617).
 *
 * Env, read at request time (the helm chart renders the first two, #4390):
 *   DEX_UI_REDIRECT_URI  the redirect_uri registered for the home-os-ui static
 *                        client in Dex. Sent verbatim: Dex compares it exactly.
 *   PUBLIC_URL           the origin the browser uses (global.publicUrl).
 *   DEX_TOKEN_URL        Dex token endpoint reachable from the UI server
 *                        (default http://home-os-dex:5556/dex/token).
 *
 * When PUBLIC_URL / DEX_UI_REDIRECT_URI are unset (local dev), the public
 * origin comes from the request: middleware.ts's publicOrigin(), which only
 * accepts a well-formed X-Forwarded-Host / Host (#4621).
 */
import type { NextRequest } from "next/server";
import { publicOrigin as requestPublicOrigin } from "@/middleware";

export const DEX_UI_CLIENT_ID = "home-os-ui"; // public client: no secret
export const DEFAULT_DEX_TOKEN_URL = "http://home-os-dex:5556/dex/token";
export const CALLBACK_PATH = "/api/auth/callback";

export type DexConfig = {
  /** Origin the browser uses, no trailing slash. Base for /dex/auth and every app redirect. */
  publicOrigin: string;
  /** redirect_uri for both the authorize request and the code exchange. */
  redirectUri: string;
  /** Dex token endpoint for the code exchange and refresh grants. */
  tokenUrl: string;
};

type Env = Record<string, string | undefined>;

/** The value as an absolute http(s) URL, or null if unset or malformed. */
function httpUrl(value: string | undefined): URL | null {
  const raw = value?.trim();
  if (!raw) return null;
  try {
    const url = new URL(raw);
    return url.protocol === "https:" || url.protocol === "http:" ? url : null;
  } catch {
    return null;
  }
}

export function dexTokenUrl(env: Env = process.env): string {
  return env.DEX_TOKEN_URL?.trim() || DEFAULT_DEX_TOKEN_URL;
}

/**
 * Resolves the Dex settings. `requestOrigin` is only called when neither
 * PUBLIC_URL nor DEX_UI_REDIRECT_URI gives a usable origin.
 *
 * Precedence for the public origin: PUBLIC_URL, then the origin of
 * DEX_UI_REDIRECT_URI (it is the public callback URL), then the request.
 * redirect_uri: DEX_UI_REDIRECT_URI as given, else <publicOrigin>/api/auth/callback.
 * A malformed or non-http(s) value is ignored, as if unset.
 */
export function resolveDexConfig(requestOrigin: () => string, env: Env = process.env): DexConfig {
  const configuredRedirect = httpUrl(env.DEX_UI_REDIRECT_URI);
  const origin = (httpUrl(env.PUBLIC_URL) ?? configuredRedirect)?.origin ?? requestOrigin().replace(/\/+$/, "");
  return {
    publicOrigin: origin,
    redirectUri: configuredRedirect ? env.DEX_UI_REDIRECT_URI!.trim() : `${origin}${CALLBACK_PATH}`,
    tokenUrl: dexTokenUrl(env),
  };
}

/** Dex settings for this request. */
export function dexConfig(request: Pick<NextRequest, "headers" | "nextUrl">): DexConfig {
  return resolveDexConfig(() => requestPublicOrigin(request));
}
