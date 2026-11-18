/**
 * Pure list helpers (#4566): what the quick-add box understood, how rows are
 * grouped on screen, and how a checked row describes who and when.
 *
 * Everything here is pure and unit-tested with `node --test`
 * (lib/lists/model.test.mjs), because these are the parts that are easy to get
 * subtly wrong and invisible in a screenshot:
 *
 * 1. PARSING. "2 lb ground beef" must become quantity 2, unit lb, text
 *    "Ground beef" — and "2 of the big yogurts" must NOT lose "of the big
 *    yogurts" to a unit it guessed. A wrong quantity is worse than no
 *    quantity: the person can see a blank and fill it, but they cannot see a
 *    2 they never typed.
 *
 * 2. GROUPING. A grocery list groups by aisle and a chores list by due date;
 *    "Other" and "No date" are real group names, and a list whose only group
 *    would be the fallback renders one unlabelled section instead of a
 *    heading that says nothing.
 *
 * 3. WHO AND WHEN. Never invent a name. The server sends `checked_by_name`
 *    once it has one; before that a row says when, not who.
 *
 * The aisle guess is a HINT, deliberately small. The server has the real one
 * (apps/mcp/internal/meals/grocery.go sectionOf) and re-derives it whenever it
 * parses an ingredient. This map only decides what the preview chip says
 * before the write, and what section the quick-add row sends; a wrong guess
 * puts an item in the wrong aisle, never in the wrong list.
 */

// The `.ts` extension matters: `node --test` loads this file directly (Node
// strips the types), and a bare "./types" does not resolve there. Type-only
// imports may stay extension-less; a VALUE import may not (see lib/today/agenda.ts).
import { LIST_KINDS, type ListItem, type ListKind, type ListRow } from "./types.ts";

// ── quick add ────────────────────────────────────────────────

const NUMBER_WORDS: Record<string, number> = {
  a: 1,
  an: 1,
  one: 1,
  two: 2,
  three: 3,
  four: 4,
  five: 5,
  six: 6,
  seven: 7,
  eight: 8,
  nine: 9,
  ten: 10,
};

/**
 * Units a quantity may be counted in. Kept to the ones a family actually
 * types; anything else stays part of the text ("2 heirloom tomatoes" keeps
 * "heirloom tomatoes" whole because "heirloom" is not a unit).
 */
export const UNITS = [
  "lb", "lbs", "oz", "g", "kg", "ml", "l", "gal", "gallon", "qt", "quart", "pt", "pint",
  "cup", "cups", "tbsp", "tsp", "dozen", "bag", "bags", "box", "boxes", "jar", "jars",
  "can", "cans", "pack", "packs", "bottle", "bottles", "bunch", "bunches", "loaf", "loaves",
  "roll", "rolls", "case", "cases", "head", "heads", "clove", "cloves", "stick", "sticks",
];

const UNIT_SET = new Set(UNITS);

export interface ParsedQuickAdd {
  /** "Ground beef" — capitalised, quantity and unit removed. */
  text: string;
  quantity: number | null;
  unit: string | null;
  /** A guessed aisle, or null when nothing matched. */
  section: string | null;
}

/** Capitalise the first letter, leave the rest of the word alone ("BBQ sauce"). */
function upperFirst(s: string): string {
  return s.length ? s[0].toUpperCase() + s.slice(1) : s;
}

/**
 * Parse one quick-add line into the API's structured item.
 *
 * The rule that keeps it honest: a number only counts as a QUANTITY when what
 * follows it is a known unit ("2 lb beef"), or when the number is a bare count
 * ("3 eggs"). "2 heirloom tomatoes" takes the 2 and keeps "heirloom tomatoes"
 * as the name, which is the shape the mockup draws. A line that is nothing but
 * an amount ("2 lb") keeps the words the person typed rather than saving an
 * item with no name.
 */
export function parseQuickAdd(input: string): ParsedQuickAdd {
  const raw = input.trim().replace(/\s+/g, " ");
  if (!raw) return { text: "", quantity: null, unit: null, section: null };

  const tokens = raw.split(" ");
  let quantity: number | null = null;
  let unit: string | null = null;
  let rest = tokens;

  const first = tokens[0].toLowerCase();
  const asNumber = /^\d+(?:\.\d+)?$/.test(first) ? Number(first) : NUMBER_WORDS[first];
  if (asNumber !== undefined && asNumber !== null) {
    quantity = asNumber;
    rest = tokens.slice(1);
    // "2 dozen eggs": both words are the amount, and the count is the pair.
    if (rest[0]?.toLowerCase() === "dozen") {
      unit = "dozen";
      rest = rest.slice(1);
    } else {
      // A following unit is part of the amount, not the name.
      const candidate = rest[0]?.toLowerCase().replace(/[.,]$/, "");
      if (candidate && UNIT_SET.has(candidate)) {
        unit = candidate;
        rest = rest.slice(1);
      }
    }
  }

  const text = rest.join(" ").trim();
  if (!text) {
    // The whole line was an amount ("2 lb"). Keep the words the person typed
    // rather than saving an item with no name.
    return { text: upperFirst(raw), quantity: null, unit: null, section: guessSection(raw) };
  }
  return { text: upperFirst(text), quantity, unit, section: guessSection(text) };
}

// ── the aisle hint ───────────────────────────────────────────

/** The store's aisles, in the order the mockup walks them. */
export const AISLES = [
  "Produce",
  "Dairy & eggs",
  "Bakery",
  "Meat & seafood",
  "Pantry",
  "Frozen",
  "Household",
] as const;

/**
 * A deliberately small word→aisle map: enough for the supermarket case
 * ("milk", "bananas"), not a second grocery engine. Unknown words are "Other"
 * and the person can move the item.
 */
const SECTION_WORDS: Record<string, string> = {};
function learn(section: string, words: string): void {
  for (const w of words.split(/\s+/).filter(Boolean)) SECTION_WORDS[singular(w)] = section;
}
learn("Produce", "apple apples banana bananas onion onions potato potatoes celery lettuce spinach kale garlic tomato tomatoes avocado lemons lemon lime orange oranges berries strawberry blueberries grapes carrot carrots cucumber corn broccoli herbs cilantro parsley basil cranberries pumpkin");
learn("Dairy & eggs", "milk butter cheese yogurt yoghurt cream eggs egg cheddar mozzarella parmesan");
learn("Bakery", "bread rolls roll bagels bagel baguette tortillas bun buns loaf muffins");
learn("Meat & seafood", "beef chicken turkey pork bacon salmon fish shrimp sausage ham lamb steak mince thighs");
learn("Pantry", "rice pasta spaghetti flour sugar cereal coffee broth stock oil vinegar sauce peanut jam honey oats noodles beans tuna stuffing spice spices");
learn("Frozen", "frozen ice pizza peas waffles");
learn("Household", "soap foil towels paper diapers detergent trash candles batteries");

/**
 * Singularise one trailing "s" so "apples" and "apple" agree. The map is
 * stored singular too (learn() singularises on the way in), so a lookup and
 * its key cannot disagree — "Diapers" must find "diaper".
 */
function singular(word: string): string {
  return word.length > 3 && word.endsWith("s") && !word.endsWith("ss") ? word.slice(0, -1) : word;
}

/** The guessed aisle for a name, or null when nothing matched. */
export function guessSection(text: string): string | null {
  const words = text
    .toLowerCase()
    .split(/[^a-z]+/)
    .filter(Boolean)
    .map(singular);
  // Last word first: "chicken broth" is Pantry, "chicken thighs" is Meat.
  for (let i = words.length - 1; i >= 0; i--) {
    const hit = SECTION_WORDS[words[i]];
    if (hit) return hit;
  }
  return null;
}

// ── dates ────────────────────────────────────────────────────

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/**
 * A date's UTC midnight, from a DATE string ("2026-11-26") or an ISO one.
 *
 * Deliberately UTC: `due_date` is a DATE column with no time, and
 * `new Date("2026-11-26")` is UTC midnight anyway — reading it with local
 * getters is the #4550 bug (Nov 26 shows as Nov 25 in New York). Comparing two
 * UTC midnights keeps the day the day.
 */
function dateKey(value: string): number | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(value);
  if (!m) {
    const t = Date.parse(value);
    return Number.isNaN(t) ? null : t;
  }
  return Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
}

/** Whole days from `today` (YYYY-MM-DD) to `date`, or null if either won't parse. */
export function daysUntil(date: string, today: string): number | null {
  const a = dateKey(date);
  const b = dateKey(today);
  if (a === null || b === null) return null;
  return Math.round((a - b) / 86_400_000);
}

export type DueBucket = "Overdue" | "Today" | "This week" | "Later" | "No date";
const DUE_ORDER: readonly DueBucket[] = ["Overdue", "Today", "This week", "Later", "No date"];

/** Which due bucket a row belongs in, for a chores/todo list. */
export function dueBucket(item: Pick<ListItem, "due_date">, today: string): DueBucket {
  if (!item.due_date) return "No date";
  const n = daysUntil(item.due_date, today);
  if (n === null) return "No date";
  if (n < 0) return "Overdue";
  if (n === 0) return "Today";
  if (n < 7) return "This week";
  return "Later";
}

function utcParts(value: string): { y: number; m: number; d: number } | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(value);
  if (!m) return null;
  return { y: Number(m[1]), m: Number(m[2]), d: Number(m[3]) };
}

function weekday(value: string): string {
  const p = utcParts(value);
  if (!p) return "";
  return WEEKDAYS[new Date(Date.UTC(p.y, p.m - 1, p.d)).getUTCDay()];
}

function monthDay(value: string): string {
  const p = utcParts(value);
  if (!p) return "";
  return `${MONTHS[p.m - 1]} ${p.d}`;
}

/**
 * A short label for a due date: "Overdue", "Today", "Tomorrow", "Thu", or
 * "Thu, Nov 26". Never a raw ISO string.
 */
export function dueLabel(due: string, today: string): string {
  const n = daysUntil(due, today);
  if (n === null) return "";
  if (n < 0) return n === -1 ? "Overdue, yesterday" : `Overdue, ${weekday(due)}`;
  if (n === 0) return "Today";
  if (n === 1) return "Tomorrow";
  if (n < 7) return weekday(due);
  return `${weekday(due)}, ${monthDay(due)}`;
}

// ── rows ─────────────────────────────────────────────────────

/** "2 lb", "6", "1 gal" — the quantity column as the mockup draws it. */
export function amountLabel(item: Pick<ListItem, "quantity" | "unit">): string {
  const q = item.quantity;
  if (q === null || q === undefined) return "";
  // 1.5 lb, not 1.5000000000000002 lb: JSON floats arrive as they were stored.
  const n = Number.isInteger(q) ? String(q) : String(Math.round(q * 100) / 100);
  return item.unit ? `${n} ${item.unit}` : n;
}

export interface ItemGroup {
  key: string;
  /** Null when the group has no meaningful name (a list with no dates/aisles). */
  label: string | null;
  items: ListItem[];
}

/** Chores and to-dos group by when they are due; a grocery list by aisle. */
export function groupMode(kind: string): "due" | "section" {
  return kind === "chores" || kind === "todo" ? "due" : "section";
}

/**
 * Group open items for display. Open only: the caller renders the checked rows
 * in their own "In the cart" / "Checked off" block, which is what keeps the top
 * of the page the things still to do.
 */
export function groupItems(items: readonly ListItem[], kind: string, today: string): ItemGroup[] {
  const byKey = new Map<string, ListItem[]>();
  for (const item of items) {
    const key =
      groupMode(kind) === "due"
        ? dueBucket(item, today)
        : (item.section || "").trim() || "Other";
    const bucket = byKey.get(key);
    if (bucket) bucket.push(item);
    else byKey.set(key, [item]);
  }

  const keys = Array.from(byKey.keys());
  if (groupMode(kind) === "due") {
    keys.sort((a, b) => DUE_ORDER.indexOf(a as DueBucket) - DUE_ORDER.indexOf(b as DueBucket));
  } else {
    keys.sort((a, b) => {
      const ia = AISLES.indexOf(a as (typeof AISLES)[number]);
      const ib = AISLES.indexOf(b as (typeof AISLES)[number]);
      return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib) || a.localeCompare(b);
    });
  }

  const groups = keys.map((key) => ({ key, label: key as string | null, items: byKey.get(key)! }));
  // One fallback group only ("Other" / "No date"): a heading that says nothing
  // is worse than no heading, so it renders unlabelled.
  if (groups.length === 1 && (groups[0].key === "Other" || groups[0].key === "No date")) {
    return [{ ...groups[0], label: null }];
  }
  return groups;
}

/** Checked rows, newest check first; open rows keep the server's order. */
export function splitChecked(items: readonly ListItem[]): { open: ListItem[]; checked: ListItem[] } {
  const open: ListItem[] = [];
  const checked: ListItem[] = [];
  for (const item of items) (item.checked ? checked : open).push(item);
  checked.sort((a, b) => (b.checked_at ?? "").localeCompare(a.checked_at ?? ""));
  return { open, checked };
}

/** "6:12 PM" for today, "Thu" for this week, "Nov 12" after that, "" if unknown. */
export function clockTime(iso: string, now: Date = new Date()): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "";
  const mins = Math.floor((now.getTime() - t.getTime()) / 60_000);
  if (mins >= 0 && mins < 1) return "just now";
  const sameDay =
    t.getFullYear() === now.getFullYear() && t.getMonth() === now.getMonth() && t.getDate() === now.getDate();
  if (sameDay) {
    const h = t.getHours();
    const mm = String(t.getMinutes()).padStart(2, "0");
    const ampm = h >= 12 ? "PM" : "AM";
    const h12 = h % 12 === 0 ? 12 : h % 12;
    return `${h12}:${mm} ${ampm}`;
  }
  const days = Math.floor((now.getTime() - t.getTime()) / 86_400_000);
  if (days >= 0 && days < 7) return WEEKDAYS[t.getDay()];
  return `${MONTHS[t.getMonth()]} ${t.getDate()}`;
}

/**
 * "Checked by Jacob, 6:12 PM", or just when we know only the time. The name is
 * never guessed from the signed-in user: another phone may have done it.
 */
export function checkedLabel(item: ListItem, now: Date = new Date()): string {
  const when = item.checked_at ? clockTime(item.checked_at, now) : "";
  if (item.checked_by_name) {
    return when ? `Checked by ${item.checked_by_name}, ${when}` : `Checked by ${item.checked_by_name}`;
  }
  return when ? `Checked ${when}` : "Checked";
}

/** "Updated 2 min ago", "Updated yesterday" — never a raw timestamp. */
export function updatedLabel(updatedAt: string, now: Date = new Date()): string {
  const t = Date.parse(updatedAt);
  if (Number.isNaN(t)) return "";
  const mins = Math.floor((now.getTime() - t) / 60_000);
  if (mins < 1) return "Updated just now";
  if (mins < 60) return `Updated ${mins} min ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `Updated ${hours} hr ago`;
  const days = Math.floor(hours / 24);
  if (days === 1) return "Updated yesterday";
  if (days < 7) return `Updated ${days} days ago`;
  const d = new Date(t);
  return `Updated ${MONTHS[d.getMonth()]} ${d.getDate()}`;
}

// ── the index page ───────────────────────────────────────────

/** The mockup's "13 left" / "4 to pack" / "6 to do" caption. */
export function countCaption(kind: string): string {
  if (kind === "packing") return "to pack";
  if (kind === "chores" || kind === "todo") return "to do";
  return "left";
}

/** Split the family of lists into active and archived, templates kept apart. */
export function partitionLists(lists: readonly ListRow[]): {
  active: ListRow[];
  archived: ListRow[];
  templates: ListRow[];
} {
  const active: ListRow[] = [];
  const archived: ListRow[] = [];
  const templates: ListRow[] = [];
  for (const list of lists) {
    if (list.is_template) templates.push(list);
    else if (list.archived) archived.push(list);
    else active.push(list);
  }
  return { active, archived, templates };
}

/** The kind chips, in a stable order, with the counts the mockup shows. */
export function kindCounts(lists: readonly ListRow[]): Array<{ kind: ListKind; count: number }> {
  const counts = new Map<string, number>();
  for (const list of lists) counts.set(list.kind, (counts.get(list.kind) ?? 0) + 1);
  return LIST_KINDS.filter((k) => (counts.get(k) ?? 0) > 0).map((kind) => ({ kind, count: counts.get(kind)! }));
}

/** The kind's display label. */
export const KIND_LABELS: Record<ListKind, string> = {
  grocery: "Grocery",
  packing: "Packing",
  chores: "Chores",
  todo: "To-do",
  shopping: "Shopping",
  other: "Other",
};

export function kindLabel(kind: string): string {
  return (LIST_KINDS as readonly string[]).includes(kind) ? KIND_LABELS[kind as ListKind] : "Other";
}

/**
 * Whether a list's kind draws a Shop button (store mode). Only the kinds a
 * person walks a store with; a chore list is not shopped.
 */
export function hasStoreMode(kind: string): boolean {
  return kind === "grocery" || kind === "shopping";
}
