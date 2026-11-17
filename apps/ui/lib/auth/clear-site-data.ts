/**
 * The Clear-Site-Data value sent by POST /api/auth/logout (#4648).
 *
 * Lives here, not in the route file: a Next.js route module may only export
 * its HTTP methods plus `dynamic`/`runtime`/… (the build fails with
 * "CLEAR_SITE_DATA is not a valid Route export field"). app/api/auth/logout/
 * route.test.mjs imports this, so the value and the header it checks cannot
 * drift apart.
 *
 * "cache"   HTTP cache + Cache Storage + the back/forward entries. Cache
 *           Storage is where the pre-#4648 service worker kept authenticated
 *           `/bff/*` responses.
 * "storage" localStorage (the zustand `home-os-auth` profile), sessionStorage,
 *           IndexedDB, and the service worker registrations.
 *
 * Not "*": that also means "cookies", which the route's Set-Cookie expiries
 * already handle, and it pulls in directives (clientHints) that have nothing to
 * do with a logout.
 *
 * Every directive must be a quoted string — MDN: "A directive that does not
 * include the double quotes is invalid."
 *
 * The header only takes effect in a secure context, so the page purges too
 * (lib/session-teardown.ts). See KB architecture/pwa-caching-policy.md.
 */
export const CLEAR_SITE_DATA = '"cache", "storage"';
