// Lets `node --test` load the app's TypeScript modules the way Next resolves
// them: "@/x" -> <apps/ui>/x(.ts|.tsx), extensionless relative imports -> .ts,
// and "next/server" -> next/server.js (next has no exports map).
// Import this first, then load the modules under test with `await import()`:
// static imports are resolved before this file's hooks are registered.
import { registerHooks } from "node:module";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";

const root = new URL("../../", import.meta.url);

function withTsExtension(url) {
  if (/\.[cm]?[jt]sx?$/.test(url.pathname)) return null;
  for (const ext of [".ts", ".tsx", "/index.ts"]) {
    const candidate = new URL(url.href + ext);
    if (existsSync(fileURLToPath(candidate))) return candidate.href;
  }
  return null;
}

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "next/server") return nextResolve("next/server.js", context);
    if (specifier.startsWith("@/")) {
      const url = new URL(specifier.slice(2), root);
      const resolved = withTsExtension(url);
      return nextResolve(resolved ?? url.href, context);
    }
    if ((specifier.startsWith("./") || specifier.startsWith("../")) && context.parentURL?.startsWith("file:")) {
      const resolved = withTsExtension(new URL(specifier, context.parentURL));
      if (resolved) return nextResolve(resolved, context);
    }
    return nextResolve(specifier, context);
  },
});
