/**
 * Minimal className merge utility.
 * Filters out falsy values and joins with spaces.
 * Use for conditional Tailwind classes: cn("base", isActive && "active")
 */
export function cn(...classes: (string | false | undefined | null)[]): string {
  return classes.filter(Boolean).join(" ");
}
