/**
 * What the signed-in role may see and do in an entity's Resources section
 * (#4564): files, notes and secrets on a property, vehicle, pet, person,
 * maintenance task, bill or any other entity detail page.
 *
 * This is a UI mirror of the API's household role policy
 * (apps/api/internal/auth/roles.go, KB decisions/2026-11-15-role-policy.md).
 * The API enforces; this module only decides what to render, because a section
 * a role cannot use must not be drawn at all — an empty "Files" heading with a
 * 403 behind it teaches a housesitter to tap a dead control, and a "Secrets"
 * tab that answers 403 is worse.
 *
 * Keep the tables in step with roles.go and with the MCP's copy. The one
 * deliberate difference is the unknown-role case: the UI fails CLOSED for a
 * role it does not know (nothing but what every role sees), exactly as the API
 * does for an unknown entity kind.
 *
 * Plain data and pure functions only (no JSX), so `npm test` (node --test)
 * covers it.
 */

export type Role =
  | "owner"
  | "family_manager"
  | "family_member"
  | "viewer"
  | "housesitter"
  | "vendor";

export const ROLES: readonly Role[] = [
  "owner",
  "family_manager",
  "family_member",
  "viewer",
  "housesitter",
  "vendor",
];

/** The three household roles that may write almost everything. */
const FAMILY: readonly Role[] = ["owner", "family_manager", "family_member"];
/** Family roles plus viewer: everyone who may read a document. */
const READERS: readonly Role[] = [...FAMILY, "viewer"];
/** Readers plus housesitter. */
const SITTERS: readonly Role[] = [...READERS, "housesitter"];
/** Readers plus vendor (properties, assets, maintenance). */
const FABRIC: readonly Role[] = [...READERS, "vendor"];

/**
 * Which resource guards each entity kind (roles.go entityResources). An
 * unknown kind is visible to the family roles only, so a new kind fails
 * closed for outsiders.
 */
const ENTITY_RESOURCES: Readonly<Record<string, string>> = {
  household: "family_notes",
  property: "properties",
  room: "properties",
  asset: "assets",
  vehicle: "vehicles",
  pet: "pets",
  vendor: "vendors",
  bill: "bills",
  loan: "loans",
  maintenance: "maintenance",
  maintenance_task: "maintenance",
  maintenance_schedule: "maintenance",
  calendar: "calendar",
  calendar_event: "calendar",
  note: "notes",
  file: "files",
  document: "files",
  document_link: "files",
  secret: "secrets",
  person: "people",
  person_preference: "preferences",
  budget: "budget",
  list: "lists",
  list_item: "lists",
  recipe: "meals",
  meal_plan: "meals",
  meal_feedback: "meals",
  guide: "guides",
};

/** Read grants per resource (roles.go policy[x].read). Missing: nobody. */
const READ_GRANTS: Readonly<Record<string, readonly Role[]>> = {
  household: READERS,
  properties: ROLES as readonly Role[],
  assets: ROLES as readonly Role[],
  vehicles: READERS,
  pets: SITTERS,
  vendors: SITTERS,
  maintenance: FABRIC,
  bills: READERS,
  loans: READERS,
  budget: READERS,
  notes: SITTERS,
  family_notes: READERS,
  files: READERS,
  secrets: FAMILY,
  people: READERS,
  preferences: READERS,
  lists: SITTERS,
  meals: SITTERS,
  calendar: SITTERS,
  guides: SITTERS,
};

function isRole(role: string | null | undefined): role is Role {
  return !!role && (ROLES as readonly string[]).includes(role);
}

/** The resource guarding an entity kind, or null for an unknown kind. */
export function entityResource(kind: string | null | undefined): string | null {
  if (!kind) return null;
  return ENTITY_RESOURCES[kind] ?? null;
}

/**
 * Whether a role may read records of this entity kind at all (and so notes
 * attached to them). `null` role (still loading, or the request failed) shows
 * everything: the common owner case must not flicker, and the API refuses
 * what the role cannot use. A role this build does not know sees nothing
 * outside the family — fails closed, like the API's unknown kinds.
 */
export function canSeeEntity(role: string | null | undefined, kind: string | null | undefined): boolean {
  if (!kind) return false;
  if (role == null || role === "") return true;
  const res = entityResource(kind);
  if (!res) return isRole(role) && FAMILY.includes(role);
  const grants = READ_GRANTS[res];
  if (!grants) return false;
  return isRole(role) ? grants.includes(role) : false;
}

/**
 * Whether a role may add a note on this entity kind: family roles on anything
 * they can see, a housesitter on pets only (roles.go CanAnnotateEntity).
 */
export function canAnnotateEntity(role: string | null | undefined, kind: string | null | undefined): boolean {
  if (!canSeeEntity(role, kind)) return false;
  if (role == null || role === "") return true;
  if (!isRole(role)) return false;
  if (FAMILY.includes(role)) return true;
  return role === "housesitter" && kind === "pet";
}

/** Whether a role may read files at all (ResFiles read: family + viewer). */
export function canSeeFiles(role: string | null | undefined): boolean {
  if (role == null || role === "") return true;
  return isRole(role) && READERS.includes(role);
}

/** Whether a role may upload or delete a file (ResFiles write: family). */
export function canWriteFiles(role: string | null | undefined): boolean {
  if (role == null || role === "") return true;
  return isRole(role) && FAMILY.includes(role);
}

/** Whether a role may read credentials (ResSecrets read: family only). */
export function canSeeSecrets(role: string | null | undefined): boolean {
  if (role == null || role === "") return true;
  return isRole(role) && FAMILY.includes(role);
}

/**
 * Whether a role may read the family activity feed (#4577).
 *
 * ResActivity's read grant is rolesReaders — the family roles and a viewer
 * (roles.go). A housesitter and a vendor get 403 from GET /activity, so Today
 * must not draw the section for them: a "Family activity" heading with a 403
 * behind it is a dead panel, and the activity trail names records a sitter or
 * a vendor may not see (KB decisions/2026-11-15-role-policy.md).
 *
 * Unlike the helpers above, an UNKNOWN role (null/"" while GET /me/households
 * is in flight) is also false here. Those answer "show it and let the API
 * refuse", which is fine for a read that returns empty data; this one is a
 * route that answers 403 by role, so drawing it for a role that may not read it
 * means a refused request on every visit and a panel that appears and is then
 * taken away from a housesitter. Today asks for the role alongside the
 * household it already needs, so the section arrives with the page's first real
 * paint either way — it is never late, and it is never a promise it withdraws.
 */
export function canReadActivity(role: string | null | undefined): boolean {
  return isRole(role) && READERS.includes(role);
}

/** True once the signed-in role is known, so a panel can wait instead of guess. */
export function roleKnown(role: string | null | undefined): role is Role {
  return isRole(role);
}

/** Whether a role may read notes at all (ResNotes read: family + viewer + sitter). */
export function canReadNotes(role: string | null | undefined): boolean {
  if (role == null || role === "") return true;
  return isRole(role) && SITTERS.includes(role);
}

/** Whether a role may edit or delete a note (ResNotes write: family). */
export function canWriteNotes(role: string | null | undefined): boolean {
  if (role == null || role === "") return true;
  return isRole(role) && FAMILY.includes(role);
}

/**
 * Access level to one resource.
 *
 * "annotate" exists because notes have TWO write grants on the API, not one
 * (roles.go: ResNotes ActWrite is the family, ActAnnotate is family plus a
 * housesitter on pets). A housesitter may ADD a pet note but may not rename or
 * delete one — Update and Delete both require ActWrite and answer 403. Folding
 * both into "write" drew Edit and Delete buttons that a sitter cannot use,
 * which is exactly what the role policy says the UI must not do (#4564).
 */
export type Access = "none" | "read" | "annotate" | "write";

/**
 * The Resources section for one entity and role: which tabs exist, and what
 * each allows. A tab whose access is "none" is not rendered.
 *
 * The section itself is rendered only when at least one tab exists, so a
 * housesitter on a bill (no files, no notes on a bill, no secrets) sees no
 * Resources heading at all rather than an empty one.
 *
 * "annotate" is the narrow case: a housesitter on a pet may add a note but not
 * edit or delete one, so the notes tab exists with only the add affordance.
 */
export interface ResourceAccess {
  files: Access;
  notes: Access;
  secrets: Access;
  /** True when at least one tab is usable; false: render nothing. */
  any: boolean;
}

export function resourceAccess(
  role: string | null | undefined,
  entityType: string | null | undefined,
): ResourceAccess {
  const files: Access = !canSeeFiles(role)
    ? "none"
    : canWriteFiles(role)
      ? "write"
      : "read";

  // Notes need BOTH grants: the role must see the entity the notes hang on
  // AND hold ResNotes read (a vendor sees maintenance tasks but no notes at
  // all). A role that may annotate gets the add affordance; everyone else
  // reads. A housesitter may annotate a pet without the general notes write
  // grant, which is why the annotate grant is asked separately.
  let notes: Access = "none";
  if (canSeeEntity(role, entityType) && canReadNotes(role)) {
    if (canWriteNotes(role)) notes = "write";
    else if (canAnnotateEntity(role, entityType)) notes = "annotate";
    else notes = "read";
  }

  const secrets: Access = !canSeeSecrets(role)
    ? "none"
    : canWriteFiles(role)
      ? "write"
      : "read";

  return {
    files,
    notes,
    secrets,
    any: files !== "none" || notes !== "none" || secrets !== "none",
  };
}
