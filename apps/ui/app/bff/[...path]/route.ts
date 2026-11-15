import { NextRequest, NextResponse } from "next/server";

/**
 * BFF proxy: /bff/<path> → ${API_INTERNAL_URL}/api/v1/<path>
 *
 * The browser never sees the JWT. This handler reads it from the httpOnly
 * `home-os-token` cookie and attaches it as `Authorization: Bearer` on the
 * server-to-server call to the Go API. Request and response bodies are
 * streamed, so multipart uploads and file downloads pass through unbuffered.
 *
 * Env: API_INTERNAL_URL — base URL of the Go API reachable from the UI pod
 * (default http://home-os-api:8080). Read at request time, not build time.
 */

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const COOKIE_NAME = "home-os-token";
const DEFAULT_API_URL = "http://home-os-api:8080";

// Hop-by-hop headers (RFC 9110 §7.6.1) never cross a proxy.
const HOP_BY_HOP = [
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
];

// Request headers we never forward: the browser's cookies and any
// client-supplied Authorization stay here; host/length are recomputed.
const STRIP_REQUEST = new Set([
  ...HOP_BY_HOP,
  "host",
  "cookie",
  "authorization",
  "content-length",
  "accept-encoding",
  "expect", // undici rejects Expect: 100-continue (curl sends it for large uploads)
  "origin",
  "referer",
]);

// Response headers we never return: cookie management belongs to the UI.
const STRIP_RESPONSE = new Set([...HOP_BY_HOP, "set-cookie"]);

function apiBase(): string {
  return (process.env.API_INTERNAL_URL || DEFAULT_API_URL).replace(/\/+$/, "");
}

function errorResponse(status: number, code: string, message: string) {
  // Same envelope as the Go API's apierr package.
  return NextResponse.json(
    { error: { code, message } },
    { status, headers: { "Cache-Control": "no-store" } },
  );
}

async function proxy(
  request: NextRequest,
  { params }: { params: { path?: string[] } },
): Promise<Response> {
  const token = request.cookies.get(COOKIE_NAME)?.value;
  if (!token) {
    return errorResponse(401, "UNAUTHORIZED", "no session");
  }

  const segments = params.path ?? [];
  if (segments.some((s) => s === "" || s === "." || s === "..")) {
    return errorResponse(400, "BAD_REQUEST", "invalid path");
  }

  const base = apiBase();
  const target =
    `${base}/api/v1/${segments.map(encodeURIComponent).join("/")}` +
    request.nextUrl.search;

  const headers = new Headers();
  request.headers.forEach((value, key) => {
    if (!STRIP_REQUEST.has(key.toLowerCase())) headers.set(key, value);
  });
  headers.set("authorization", `Bearer ${token}`);
  // Ask for an uncompressed body: fetch would decode it but keep the
  // Content-Encoding header, which would corrupt the response.
  headers.set("accept-encoding", "identity");
  const fwdHost = request.headers.get("x-forwarded-host") ?? request.headers.get("host");
  if (fwdHost) headers.set("x-forwarded-host", fwdHost);

  const hasBody = request.method !== "GET" && request.method !== "HEAD";

  let upstream: Response;
  try {
    upstream = await fetch(target, {
      method: request.method,
      headers,
      body: hasBody ? request.body : undefined,
      redirect: "manual",
      cache: "no-store",
      signal: request.signal,
      // Required by undici to stream a ReadableStream request body.
      ...(hasBody ? { duplex: "half" } : {}),
    } as RequestInit);
  } catch (err) {
    if (request.signal.aborted) {
      return new Response(null, { status: 499 });
    }
    console.error("[bff] upstream request failed:", target, err);
    return errorResponse(502, "BAD_GATEWAY", "API unavailable");
  }

  const resHeaders = new Headers();
  upstream.headers.forEach((value, key) => {
    if (!STRIP_RESPONSE.has(key.toLowerCase())) resHeaders.set(key, value);
  });
  // If the API compressed anyway, fetch has already decoded the body.
  if (resHeaders.has("content-encoding")) {
    resHeaders.delete("content-encoding");
    resHeaders.delete("content-length");
  }
  // Keep redirects inside the BFF rather than leaking the internal host.
  const location = resHeaders.get("location");
  if (location) {
    const apiPrefix = `${base}/api/v1`;
    if (location.startsWith(apiPrefix)) {
      resHeaders.set("location", `/bff${location.slice(apiPrefix.length)}`);
    } else if (location.startsWith("/api/v1")) {
      resHeaders.set("location", `/bff${location.slice("/api/v1".length)}`);
    }
  }
  if (!resHeaders.has("cache-control")) {
    resHeaders.set("cache-control", "no-store");
  }

  const nullBody =
    request.method === "HEAD" || [101, 204, 205, 304].includes(upstream.status);

  return new Response(nullBody ? null : upstream.body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers: resHeaders,
  });
}

export const GET = proxy;
export const HEAD = proxy;
export const POST = proxy;
export const PUT = proxy;
export const PATCH = proxy;
export const DELETE = proxy;
