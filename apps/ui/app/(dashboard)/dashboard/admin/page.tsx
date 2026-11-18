"use client";

/**
 * Instance admin: families and users (#4573), rebuilt on the instance-admin API
 * (KB api/instance-admin-endpoints.md) and laid out per the approved settings
 * mockup (artifact #103 v1, Settings > Instance > Families & users).
 *
 * Who sees it: only an instance admin. That flag is `users.is_instance_admin`
 * on GET /api/v1/auth/me (never the household role, a different axis: an admin
 * need not belong to any family). This page asks /me first and renders the
 * refusal for everyone else, so a non-admin's browser never fires an /admin
 * request — and the API refuses them anyway (RequireInstanceAdmin answers 403
 * for a non-admin on every request). Presentation only: nothing here decides
 * who may do anything.
 *
 * The old page posted the pre-#4426 contract (household_name optional, response
 * not wrapped in `data`) and could only create a new household. This one:
 *   - lists every family (GET /admin/families) and every user with their
 *     memberships and active flag (GET /admin/users);
 *   - creates a user into an EXISTING family (household_id + role) or a NEW one
 *     (household_name + timezone; its first member is always the owner), with a
 *     temporary password registered with Dex so they can sign in;
 *   - deactivates and reactivates (POST /admin/users/{id}/deactivate|reactivate,
 *     idempotent). Deactivating signs someone out everywhere at once — the app,
 *     their phone calendar, agent tokens — so it is behind a confirmation, and
 *     the button is absent on your own row (the API answers 400 for
 *     self-deactivation; the last active admin is a 409).
 *
 * Layout follows the app's list-page pattern (KB
 * architecture/entity-list-pattern.md): stacked cards on a phone, a table at
 * >= 768 px, one ResponsiveModal for the create form. It does not use
 * EntityList: families and users have no property_id, and EntityList's default
 * `propertyFilter` would silently empty the list (the #4556 Pets bug).
 */

import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Select, Skeleton, Table, Tabs } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconBan,
  IconCheck,
  IconRefresh,
  IconSearch,
  IconShieldOff,
  IconUserPlus,
} from "@tabler/icons-react";
import { apiFetch, ApiError } from "@/lib/api";
import { householdRoleLabel } from "@/lib/household-roles";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import { PasswordInput, ResponsiveModal, ResponsiveModalActions, TextInput } from "@/components/ui";

const STROKE = 1.75;

/** GET /api/v1/auth/me (auth.handler.userResponse; no `data` envelope). */
interface Me {
  id: string;
  email: string;
  name: string;
  is_instance_admin: boolean;
}

/** apps/api/internal/admin/repo.go Family. */
interface AdminFamily {
  id: string;
  name: string;
  timezone: string;
  member_count: number;
  created_at: string;
}

/** apps/api/internal/admin/repo.go UserMembership. */
interface AdminMembership {
  household_id: string;
  household_name: string;
  role: string;
}

/** apps/api/internal/admin/repo.go User — never any password or dex_sub. */
interface AdminUser {
  id: string;
  email: string;
  name: string;
  active: boolean;
  is_instance_admin: boolean;
  created_at: string;
  updated_at: string;
  memberships: AdminMembership[] | null;
}

/** POST /api/v1/admin/users 201 body. */
interface CreatedUser {
  user_id: string;
  email: string;
  name: string;
  household_id: string;
  household_name: string;
  role: string;
  household_created: boolean;
}

/**
 * Shares the ["auth","me"] key with useMe() (lib/today/api.ts), so the settings
 * index and this page read one cached answer. The type is wider here because
 * the API always sends is_instance_admin (auth.handler.userResponse says it is
 * always present, so "not an admin" can be told apart from "field missing").
 */
function useAdminMe() {
  return useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => apiFetch<Me>("/api/v1/auth/me"),
    staleTime: 10 * 60_000,
    retry: false,
  });
}

const adminKeys = {
  all: ["admin"] as const,
  families: () => [...adminKeys.all, "families"] as const,
  users: () => [...adminKeys.all, "users"] as const,
};

/** The API's own message for a 4xx: {"error":{"code","message"}}. */
function errorBodyMessage(error: ApiError): string | null {
  const body = error.body as { error?: { message?: unknown } } | undefined;
  const message = body?.error?.message;
  return typeof message === "string" && message.trim() !== "" ? message : null;
}

/** "Priya Patel" -> "PP"; a single word keeps its first two letters. */
function initials(name: string, email: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length >= 2) return (words[0][0] + words[words.length - 1][0]).toUpperCase();
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (email[0] ?? "?").toUpperCase();
}

/** "Mar 2, 2026" from an RFC3339 timestamp; the API always sends one. */
function day(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
}

/**
 * "Central Time (America/Chicago)" for a family row, the way the mockup names
 * it. The IANA name stays in the text: an admin comparing two families needs the
 * exact zone, and the long name alone ("Central Standard Time") is ambiguous
 * across the year. Falls back to the raw value for a zone Intl doesn't know.
 */
function zoneLabel(timezone: string): string {
  try {
    const long = new Intl.DateTimeFormat("en-US", { timeZone: timezone, timeZoneName: "long" })
      .formatToParts(new Date())
      .find((part) => part.type === "timeZoneName")?.value;
    if (!long) return timezone;
    const short = long.replace(/ (Standard|Daylight) Time$/, " Time");
    return `${short} (${timezone})`;
  } catch {
    return timezone;
  }
}

/** A phone row's families line: the first membership, plus how many more. */
function membershipSummary(user: AdminUser): string {
  const list = user.memberships ?? [];
  if (list.length === 0) return "No family yet";
  const first = `${list[0].household_name} · ${householdRoleLabel(list[0].role)}`;
  return list.length === 1 ? first : `${first} +${list.length - 1} more`;
}

// ── temporary passwords -----------------------------------------------------

/**
 * Readable words with a couple of digits: an admin reads it to the person, and
 * it clears bcrypt's 8-byte floor easily (the API takes 8–72 bytes).
 */
const PASSWORD_WORDS = [
  "sunny", "otter", "maple", "river", "amber", "cedar", "pilot", "lemon",
  "falcon", "tulip", "harbor", "copper", "velvet", "meadow", "pepper", "walnut",
];

/** A fresh temporary password, e.g. "sunny-otter-42-maple". */
function generateTempPassword(): string {
  const buf = new Uint32Array(3);
  crypto.getRandomValues(buf);
  const word = (n: number) => PASSWORD_WORDS[n % PASSWORD_WORDS.length];
  return `${word(buf[0])}-${word(buf[1])}-${(buf[2] % 90) + 10}-${word(buf[0] + buf[2])}`;
}

// ── add user ----------------------------------------------------------------

const ROLE_OPTIONS = [
  "owner",
  "family_manager",
  "family_member",
  "viewer",
  "housesitter",
  "vendor",
].map((role) => ({ value: role, label: householdRoleLabel(role) }));

interface AddUserModalProps {
  opened: boolean;
  onClose: () => void;
  families: AdminFamily[];
}

/**
 * Add user: name, email, a temporary password, then an existing family (with a
 * role, default Family member) or a new one (whose first member is always its
 * owner, so the role is fixed and a time zone is asked for instead).
 */
function AddUserModal({ opened, onClose, families }: AddUserModalProps) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [mode, setMode] = useState<"existing" | "new">("existing");
  const [householdId, setHouseholdId] = useState<string | null>(null);
  const [role, setRole] = useState<string>("family_member");
  const [householdName, setHouseholdName] = useState("");
  const [timezone, setTimezone] = useState("");
  const [error, setError] = useState<string | null>(null);

  // Open with a usable password (generated after mount: crypto is browser-only)
  // and the family list's first entry already selected.
  useEffect(() => {
    if (!opened) return;
    setPassword((current) => current || generateTempPassword());
    setHouseholdId((current) => current ?? families[0]?.id ?? null);
  }, [opened, families]);

  const reset = () => {
    setName("");
    setEmail("");
    setPassword("");
    setMode("existing");
    setHouseholdId(null);
    setRole("family_member");
    setHouseholdName("");
    setTimezone("");
    setError(null);
    create.reset();
  };

  const close = () => {
    reset();
    onClose();
  };

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: CreatedUser }>("/api/v1/admin/users", { method: "POST", body }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: adminKeys.users() });
      queryClient.invalidateQueries({ queryKey: adminKeys.families() });
      const created = res?.data;
      notifications.show({
        message: created
          ? `${created.name} can now sign in with the temporary password you set${
              created.household_created
                ? ` — ${created.household_name} was created with them as owner`
                : ""
            }.`
          : "User added.",
        color: "teal",
      });
      close();
    },
    onError: (err: unknown) => {
      const api = err instanceof ApiError ? err : null;
      if (api?.status === 409) {
        setError(
          "An account with this email already exists. Find them in the list and add them to a family from there.",
        );
      } else if (api?.status === 404) {
        setError("That family doesn't exist any more. Reload the page and pick another one.");
      } else if (api?.status === 502 || api?.status === 503) {
        setError(
          "The identity provider couldn't register the password, so nothing was created. Ask whoever runs this server to check Dex; until then the person cannot sign in.",
        );
      } else if (api?.status === 400) {
        setError(errorBodyMessage(api) ?? "The API refused those details. Check the fields and try again.");
      } else {
        setError("Couldn't add the user. Nothing was created — check your connection and try again.");
      }
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const trimmedName = name.trim();
    const trimmedEmail = email.trim();
    if (!trimmedName) return setError("Enter their name.");
    if (!trimmedEmail.includes("@") || trimmedEmail.startsWith("@") || trimmedEmail.endsWith("@")) {
      return setError("Enter a valid email address; they will sign in with it.");
    }
    if (password.length < 8) return setError("The temporary password needs at least 8 characters.");
    if (password.length > 72) return setError("The temporary password must be at most 72 characters.");
    if (mode === "existing" && !householdId) {
      return setError("Pick the family they should join, or create a new one.");
    }
    if (mode === "new" && !householdName.trim()) {
      return setError("Name the new family; its first user is its owner.");
    }
    setError(null);
    create.mutate(
      mode === "existing"
        ? { name: trimmedName, email: trimmedEmail, password, role, household_id: householdId }
        : {
            name: trimmedName,
            email: trimmedEmail,
            password,
            role: "owner",
            household_name: householdName.trim(),
            ...(timezone.trim() ? { timezone: timezone.trim() } : {}),
          },
    );
  };

  const familyOptions = families.map((family) => ({ value: family.id, label: family.name }));

  return (
    <ResponsiveModal opened={opened} onClose={close} title="Add user">
      <form onSubmit={submit} noValidate>
        <div className="flex flex-col gap-3">
          <TextInput
            label="Name"
            required
            data-autofocus
            placeholder="Priya Patel"
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <TextInput
            label="Email"
            required
            type="email"
            autoComplete="off"
            placeholder="priya@example.com"
            description="They sign in with this address, so it has to be theirs."
            value={email}
            onChange={(e) => setEmail(e.currentTarget.value)}
          />
          <div>
            <div className="flex items-end gap-2">
              <div className="min-w-0 flex-1">
                <PasswordInput
                  label="Temporary password"
                  required
                  autoComplete="new-password"
                  value={password}
                  onChange={(e) => setPassword(e.currentTarget.value)}
                />
              </div>
              <Button
                variant="default"
                type="button"
                onClick={() => setPassword(generateTempPassword())}
                leftSection={<IconRefresh size={16} />}
                className="shrink-0 max-md:min-h-11"
              >
                New
              </Button>
            </div>
            <p className="mt-1 text-xs text-ink-3">
              Give it to them yourself. They can change it after signing in. At least 8 characters.
            </p>
          </div>

          <fieldset className="m-0 border-0 p-0">
            <legend className="mb-1.5 text-[13px] font-semibold text-ink-2">Family</legend>
            <div className="grid grid-cols-2 gap-2" role="radiogroup" aria-label="Family">
              {(
                [
                  ["existing", "Existing family"],
                  ["new", "New family"],
                ] as const
              ).map(([value, label]) => {
                const selected = mode === value;
                return (
                  <button
                    key={value}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    data-add-user-mode={value}
                    onClick={() => setMode(value)}
                    className={`min-h-11 rounded-token-md border px-3 py-2 text-left text-sm font-medium ${
                      selected
                        ? "border-accent bg-accent-soft text-ink"
                        : "border-line bg-surface text-ink-2 hover:bg-surface-2"
                    }`}
                  >
                    {label}
                  </button>
                );
              })}
            </div>
          </fieldset>

          {mode === "existing" ? (
            familyOptions.length > 0 ? (
              <>
                <Select
                  label="Which family"
                  required
                  data={familyOptions}
                  value={householdId}
                  onChange={setHouseholdId}
                  placeholder="Pick a family"
                  checkIconPosition="right"
                  comboboxProps={{ withinPortal: true }}
                />
                <Select
                  label="Role"
                  data={ROLE_OPTIONS}
                  value={role}
                  onChange={(value) => setRole(value ?? "family_member")}
                  allowDeselect={false}
                  checkIconPosition="right"
                  comboboxProps={{ withinPortal: true }}
                />
              </>
            ) : (
              <p className="m-0 text-sm text-ink-3">
                This server has no families yet. Choose <b>New family</b> to create the first one.
              </p>
            )
          ) : (
            <>
              <TextInput
                label="New family name"
                required
                placeholder="The Harts"
                description="Its first user becomes its owner, and Family, Bills and Maintenance calendars are created."
                value={householdName}
                onChange={(e) => setHouseholdName(e.currentTarget.value)}
              />
              <TextInput
                label="Time zone"
                placeholder="America/Chicago"
                description="Optional. An IANA zone; the server uses UTC when left empty."
                value={timezone}
                onChange={(e) => setTimezone(e.currentTarget.value)}
              />
            </>
          )}

          {error && (
            <p role="alert" className="m-0 flex items-start gap-1.5 text-sm text-danger">
              <IconAlertTriangle size={16} stroke={STROKE} className="mt-0.5 shrink-0" aria-hidden />
              {error}
            </p>
          )}
        </div>
        <ResponsiveModalActions>
          <Button variant="default" type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Add user
          </Button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

// ── the page ----------------------------------------------------------------

/**
 * What a non-admin sees. It never says "403": the person signed in normally and
 * simply isn't an instance admin, and the API enforces the same thing.
 */
function NotAnAdmin() {
  return (
    <div className="mx-auto w-full max-w-3xl" data-testid="admin-forbidden">
      <div className="flex flex-col items-start gap-3 rounded-token-md border border-line bg-surface p-6 shadow-token-sm">
        <span aria-hidden className="grid size-11 place-items-center rounded-token-md bg-surface-2 text-ink-2">
          <IconShieldOff size={22} stroke={STROKE} />
        </span>
        <h2 className="m-0 text-xl font-semibold text-ink">Families &amp; users</h2>
        <p className="m-0 text-sm text-ink-2">
          This page manages every family on this Home OS server, so only an instance admin can open
          it. Your account isn&apos;t one. Nothing is wrong with your session — ask whoever set up
          this server if you need access.
        </p>
      </div>
    </div>
  );
}

/** Admin / Active / Deactivated. Text as well as colour, never colour alone. */
function StatusTag({ user }: { user: AdminUser }) {
  if (!user.active) {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-danger/10 px-2.5 py-1 text-xs font-semibold text-danger">
        <IconBan size={13} stroke={STROKE} aria-hidden />
        Deactivated
      </span>
    );
  }
  if (user.is_instance_admin) {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-accent-soft px-2.5 py-1 text-xs font-semibold text-accent">
        <IconCheck size={13} stroke={STROKE} aria-hidden />
        Admin
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-surface-2 px-2.5 py-1 text-xs font-medium text-ink-2">
      Active
    </span>
  );
}

export default function AdminPage() {
  const me = useAdminMe();
  const queryClient = useQueryClient();
  const [tab, setTab] = useState<string | null>("users");
  const [search, setSearch] = useState("");
  const [showAddUser, setShowAddUser] = useState(false);
  const [confirmDeactivate, setConfirmDeactivate] = useState<AdminUser | null>(null);

  const isAdmin = me.data?.is_instance_admin === true;

  const familiesQuery = useQuery({
    queryKey: adminKeys.families(),
    queryFn: () => apiFetch<{ data: AdminFamily[] }>("/api/v1/admin/families"),
    enabled: isAdmin,
    retry: false,
  });
  const usersQuery = useQuery({
    queryKey: adminKeys.users(),
    queryFn: () => apiFetch<{ data: AdminUser[] }>("/api/v1/admin/users"),
    enabled: isAdmin,
    retry: false,
  });

  const setActive = useMutation({
    mutationFn: ({ user, active }: { user: AdminUser; active: boolean }) =>
      apiFetch<{ data: AdminUser }>(
        `/api/v1/admin/users/${user.id}/${active ? "reactivate" : "deactivate"}`,
        { method: "POST" },
      ),
    onSuccess: (_res, { user, active }) => {
      queryClient.invalidateQueries({ queryKey: adminKeys.users() });
      notifications.show({
        message: active
          ? `${user.name} can sign in again.`
          : `${user.name} is deactivated and signed out everywhere.`,
        color: active ? "teal" : "orange",
      });
    },
    onError: (err: unknown, { user }) => {
      const api = err instanceof ApiError ? err : null;
      const message =
        api?.status === 409
          ? "That would leave the server with no active instance admin, so it was refused."
          : api?.status === 400
            ? (errorBodyMessage(api) ?? "You can't deactivate your own account.")
            : `Couldn't ${user.active ? "deactivate" : "reactivate"} ${user.name}. Nothing was changed.`;
      notifications.show({ message, color: "red" });
    },
  });

  const families = useMemo(() => familiesQuery.data?.data ?? [], [familiesQuery.data]);
  const users = useMemo(() => usersQuery.data?.data ?? [], [usersQuery.data]);
  const activeUsers = users.filter((user) => user.active).length;
  const deactivatedUsers = users.length - activeUsers;

  const shownUsers = useMemo(() => {
    const needle = search.trim().toLowerCase();
    if (!needle) return users;
    return users.filter(
      (user) => user.name.toLowerCase().includes(needle) || user.email.toLowerCase().includes(needle),
    );
  }, [users, search]);

  // The flag is still loading (or /me failed): say so instead of drawing the
  // admin console for an account that may not have it.
  if (me.isLoading) {
    return (
      <div className="mx-auto w-full max-w-6xl" aria-busy="true" aria-label="Loading">
        <Skeleton height={28} width={220} radius="sm" mb="md" />
        <Skeleton height={120} radius="md" />
      </div>
    );
  }
  if (me.isError && !me.data) {
    return (
      <div className="mx-auto w-full max-w-3xl" data-testid="admin-me-error">
        <div
          role="alert"
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="m-0 flex-1 text-sm">
            Couldn&apos;t check whether this account is an instance admin. Check your connection and
            try again.
          </p>
          <Button variant="default" onClick={() => me.refetch()} className="max-md:min-h-11">
            Try again
          </Button>
        </div>
      </div>
    );
  }
  if (!isAdmin) return <NotAnAdmin />;

  const loading = familiesQuery.isLoading || usersQuery.isLoading;
  const failed = familiesQuery.isError || usersQuery.isError;

  return (
    <div className="mx-auto w-full max-w-6xl" data-testid="admin-page">
      <div className="mb-4 md:mb-6">
        <h2 className="m-0 min-w-0 text-xl font-semibold text-ink md:text-2xl">Families &amp; users</h2>
        <p className="mt-1 text-sm text-ink-3">
          Everyone on this Home OS server, across every family. Only instance admins see this.
        </p>
      </div>

      {/* Counts first: the answer to "how big is this server" before any list. */}
      <div className="mb-5 grid grid-cols-3 gap-2 md:gap-3" data-testid="admin-stats">
        <div className="rounded-token-md border border-line bg-surface px-3 py-2.5 shadow-token-sm md:px-4 md:py-3">
          <b className="block text-xl font-semibold text-ink md:text-2xl">
            {loading ? "–" : families.length}
          </b>
          <span className="text-xs font-medium text-ink-3">Families</span>
        </div>
        <div className="rounded-token-md border border-line bg-surface px-3 py-2.5 shadow-token-sm md:px-4 md:py-3">
          <b className="block text-xl font-semibold text-ink md:text-2xl">
            {loading ? "–" : activeUsers}
          </b>
          <span className="text-xs font-medium text-ink-3">Active users</span>
        </div>
        <div className="rounded-token-md border border-line bg-surface px-3 py-2.5 shadow-token-sm md:px-4 md:py-3">
          <b className="block text-xl font-semibold text-ink md:text-2xl">
            {loading ? "–" : deactivatedUsers}
          </b>
          <span className="text-xs font-medium text-ink-3">Deactivated</span>
        </div>
      </div>

      {failed && (
        <div
          role="alert"
          className="mb-4 flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="m-0 flex-1 text-sm">
            Couldn&apos;t load the families and users. Nothing was changed. Check your connection and
            try again.
          </p>
          <Button
            variant="default"
            onClick={() => {
              familiesQuery.refetch();
              usersQuery.refetch();
            }}
            loading={familiesQuery.isRefetching || usersQuery.isRefetching}
            className="max-md:min-h-11"
          >
            Try again
          </Button>
        </div>
      )}

      <Tabs value={tab} onChange={setTab} variant="default">
        <Tabs.List>
          <Tabs.Tab value="users" data-testid="admin-users-tab">
            Users {!loading && <span className="ml-1.5 text-xs text-ink-3">{users.length}</span>}
          </Tabs.Tab>
          <Tabs.Tab value="families" data-testid="admin-families-tab">
            Families {!loading && <span className="ml-1.5 text-xs text-ink-3">{families.length}</span>}
          </Tabs.Tab>
        </Tabs.List>

        <Tabs.Panel value="users" pt="md">
          <div className="mb-3 flex items-center gap-2">
            <div className="min-w-0 flex-1 md:max-w-sm">
              <TextInput
                aria-label="Search users"
                placeholder="Search by name or email"
                leftSection={<IconSearch size={16} stroke={STROKE} />}
                value={search}
                onChange={(e) => setSearch(e.currentTarget.value)}
              />
            </div>
            <Button
              onClick={() => setShowAddUser(true)}
              leftSection={<IconUserPlus size={16} />}
              aria-haspopup="dialog"
              className="shrink-0 max-md:min-h-11"
            >
              Add user
            </Button>
          </div>

          {loading && (
            <div aria-busy="true" aria-label="Loading users" className="space-y-2">
              {[1, 2, 3].map((i) => (
                <div key={i} className="rounded-token-md border border-line bg-surface p-4 shadow-token-sm">
                  <Skeleton height={14} width="45%" radius="sm" />
                  <Skeleton height={12} width="70%" radius="sm" mt={8} />
                </div>
              ))}
            </div>
          )}

          {!loading && !failed && shownUsers.length === 0 && (
            <div className="rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
              <p className="m-0 text-sm text-ink-3">
                {users.length === 0
                  ? "No users yet. Add the first one, and a family for them."
                  : `No user matches “${search.trim()}”.`}
              </p>
            </div>
          )}

          {!loading && shownUsers.length > 0 && (
            <>
              <ul aria-label="Users" className="space-y-2 md:hidden">
                {shownUsers.map((user) => (
                  <li
                    key={user.id}
                    data-testid={`admin-user-card-${user.id}`}
                    className="rounded-token-md border border-line bg-surface shadow-token-sm"
                  >
                    <div className="flex items-start gap-3 p-4">
                      <span
                        aria-hidden
                        className="grid size-10 shrink-0 place-items-center rounded-full bg-accent-soft text-sm font-semibold text-accent"
                      >
                        {initials(user.name, user.email)}
                      </span>
                      <div className="min-w-0 flex-1">
                        <p className="m-0 text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">
                          {user.name}
                          {user.id === me.data?.id && (
                            <span className="ml-1 font-medium text-ink-3">(you)</span>
                          )}
                        </p>
                        <p className="m-0 text-sm text-ink-3 [overflow-wrap:anywhere]">{user.email}</p>
                        <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                          <StatusTag user={user} />
                          <span className="text-xs text-ink-3 [overflow-wrap:anywhere]">
                            {membershipSummary(user)}
                          </span>
                        </div>
                      </div>
                    </div>
                    {user.active ? (
                      user.id !== me.data?.id && (
                        <div className="border-t border-line px-4 py-2.5 text-right">
                          <Button
                            variant="subtle"
                            color="red"
                            size="compact-md"
                            onClick={() => setConfirmDeactivate(user)}
                          >
                            Deactivate
                          </Button>
                        </div>
                      )
                    ) : (
                      <div className="border-t border-line px-4 py-2.5 text-right">
                        <Button
                          variant="default"
                          size="compact-md"
                          loading={setActive.isPending && setActive.variables?.user.id === user.id}
                          onClick={() => setActive.mutate({ user, active: true })}
                        >
                          Reactivate
                        </Button>
                      </div>
                    )}
                  </li>
                ))}
              </ul>

              <div className="overflow-x-auto rounded-token-md border border-line bg-surface shadow-token-sm max-md:hidden">
                <Table highlightOnHover verticalSpacing="sm" horizontalSpacing="md" className="min-w-[40rem]">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                        User
                      </Table.Th>
                      <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                        Families
                      </Table.Th>
                      <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                        Status
                      </Table.Th>
                      <Table.Th scope="col" className="text-xs font-semibold uppercase tracking-wide text-ink-3">
                        Joined
                      </Table.Th>
                      <Table.Th className="w-28 text-right">
                        <span className="sr-only">Actions</span>
                      </Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {shownUsers.map((user) => (
                      <Table.Tr key={user.id} data-testid={`admin-user-row-${user.id}`}>
                        <Table.Td className="min-w-[14rem]">
                          <span className="block font-medium text-ink [overflow-wrap:anywhere]">
                            {user.name}
                            {user.id === me.data?.id && (
                              <span className="ml-1 font-normal text-ink-3">(you)</span>
                            )}
                          </span>
                          <span className="block text-sm text-ink-3 [overflow-wrap:anywhere]">
                            {user.email}
                          </span>
                        </Table.Td>
                        <Table.Td className="text-sm text-ink-2">
                          {user.memberships && user.memberships.length > 0 ? (
                            <ul className="m-0 list-none p-0">
                              {user.memberships.map((membership) => (
                                <li key={membership.household_id} className="whitespace-nowrap">
                                  {membership.household_name}{" "}
                                  <span className="text-ink-3">
                                    {householdRoleLabel(membership.role)}
                                  </span>
                                </li>
                              ))}
                            </ul>
                          ) : (
                            <span className="text-ink-3">No family yet</span>
                          )}
                        </Table.Td>
                        <Table.Td>
                          <StatusTag user={user} />
                        </Table.Td>
                        <Table.Td className="whitespace-nowrap text-sm text-ink-2">
                          {day(user.created_at)}
                        </Table.Td>
                        <Table.Td className="text-right">
                          {user.active ? (
                            user.id !== me.data?.id && (
                              <Button
                                variant="subtle"
                                color="red"
                                size="compact-sm"
                                onClick={() => setConfirmDeactivate(user)}
                              >
                                Deactivate
                              </Button>
                            )
                          ) : (
                            <Button
                              variant="default"
                              size="compact-sm"
                              loading={setActive.isPending && setActive.variables?.user.id === user.id}
                              onClick={() => setActive.mutate({ user, active: true })}
                            >
                              Reactivate
                            </Button>
                          )}
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
                <p className="m-0 border-t border-line px-4 py-2.5 text-xs text-ink-3">
                  Deactivating signs someone out everywhere at once, including their phone calendar
                  and agent tokens.
                </p>
              </div>
            </>
          )}
        </Tabs.Panel>

        <Tabs.Panel value="families" pt="md">
          {loading && <Skeleton height={80} radius="md" />}
          {!loading && !failed && families.length === 0 && (
            <div className="rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
              <p className="m-0 text-sm text-ink-3">
                No families yet. Add a user into a new family and they become its owner.
              </p>
            </div>
          )}
          {!loading && families.length > 0 && (
            <>
              <ul aria-label="Families" className="space-y-2">
                {families.map((family) => (
                  <li
                    key={family.id}
                    data-testid={`admin-family-${family.id}`}
                    className="flex items-center gap-3 rounded-token-md border border-line bg-surface p-4 shadow-token-sm"
                  >
                    <span
                      aria-hidden
                      className="grid size-10 shrink-0 place-items-center rounded-token-md bg-accent-soft text-sm font-bold text-accent"
                    >
                      {family.name.trim().charAt(0).toUpperCase() || "?"}
                    </span>
                    <div className="min-w-0 flex-1">
                      <p className="m-0 text-[15px] font-semibold text-ink [overflow-wrap:anywhere]">
                        {family.name}
                      </p>
                      <p className="m-0 text-sm text-ink-3 [overflow-wrap:anywhere]">
                        {zoneLabel(family.timezone)} · {family.member_count}{" "}
                        {family.member_count === 1 ? "member" : "members"} · since {day(family.created_at)}
                      </p>
                    </div>
                  </li>
                ))}
              </ul>
              <p className="mt-3 text-xs text-ink-3">
                A new family starts with its first user as owner, plus Family, Bills and Maintenance
                calendars.
              </p>
            </>
          )}
        </Tabs.Panel>
      </Tabs>

      <AddUserModal opened={showAddUser} onClose={() => setShowAddUser(false)} families={families} />

      <ConfirmDialog
        open={confirmDeactivate !== null}
        onClose={() => setConfirmDeactivate(null)}
        onConfirm={() => {
          if (!confirmDeactivate) return;
          setActive.mutate(
            { user: confirmDeactivate, active: false },
            { onSuccess: () => setConfirmDeactivate(null) },
          );
        }}
        title={confirmDeactivate ? `Deactivate ${confirmDeactivate.name}?` : "Deactivate user?"}
        message={
          confirmDeactivate
            ? `${confirmDeactivate.name} is signed out everywhere straight away: the app, their phone calendar and any agent tokens. You can reactivate them at any time.`
            : ""
        }
        confirmLabel="Deactivate"
        loading={setActive.isPending}
      />
    </div>
  );
}
