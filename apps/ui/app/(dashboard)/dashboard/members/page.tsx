"use client";

/**
 * Members — the family roster and per-member settings (#4559), mobile first.
 *
 * Built to the approved settings mockup (artifact #103 v1, its "Family &
 * members" pane) inside the approved shell (artifact #75 v1). It replaces the
 * raw `min-w-full` table whose actions were cut off at phone widths (mobile
 * audit #4509 / artifact 74, manager note #11636): rows wrap, and the row's one
 * control is a 44 px menu, so nothing lives past the right edge of a 390 px
 * screen.
 *
 * WHAT THIS PAGE DECIDES, AND WHY IT DECIDES IT IN ONE PLACE
 * (lib/members/roles.ts mirrors apps/api/internal/auth/roles.go):
 *
 *  1. NO CONTROL THAT ANSWERS 403. Only owner and family_manager invite, change
 *     a role or remove a member (roles.go CanManageMembers). A family_member,
 *     viewer or housesitter sees the roster and nothing to press on it; the
 *     hint under the heading says who can manage, so the missing controls read
 *     as someone else's job rather than as a broken page. A vendor sees no
 *     roster at all (ResMembers read is rolesSitter) and gets an explanation
 *     instead of a request the API would refuse.
 *  2. A FAMILY MANAGER NEVER SEES AN OWNER's menu (CanModifyMember), and the
 *     role picker offers only what that actor may grant (CanGrantRole), so the
 *     "Owner" option simply is not there for a manager.
 *  3. COLOUR IS SELF-OR-MANAGER (#4821). Your own row offers it to a
 *     family_member; everyone else's only to a manager; a viewer/housesitter/
 *     vendor gets none, because they may not edit a people record at all.
 *  4. A ROLE CHANGE SAYS WHAT IT CHANGES (#4559 req. 4). A demotion applies on
 *     the next request and housesitter/vendor change which data is visible, so
 *     the dialog names the capabilities gained and lost, from the same table
 *     that draws the legend below the roster — not hand-written prose per pair.
 *  5. EVERY STATE EXPLAINS ITSELF: skeleton rows while loading (not a blank
 *     page), an error card with Try again (never an empty roster), and an empty
 *     state that says what a roster is for.
 *
 * DATA GAPS, rendered rather than faked (see lib/members/api.ts):
 *  - The members list returns the colour but not the person id, so relationship,
 *    allergies and the id the colour PATCH needs come from GET /people, joined
 *    on user_id. /people applies read_basic for a housesitter, so this page can
 *    never render a birthdate, school, grade or note to one: it asks for none.
 *  - There is no "last active": memberships has no last-seen field. The row
 *    shows the JOIN date, which the API does return.
 *  - Invitations have no resend; a pending row offers Copy link (the accept_url
 *    GET /invites returns) and Revoke, which are the two things the API has.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import {
  Box,
  Button,
  Menu,
  Skeleton,
  Tooltip,
} from "@mantine/core";
import {
  IconAlertTriangle,
  IconCheck,
  IconCopy,
  IconDots,
  IconLink,
  IconPalette,
  IconRefresh,
  IconTrash,
  IconUserPlus,
  IconUsers,
} from "@tabler/icons-react";
import ResponsiveModal, { ResponsiveModalActions } from "@/components/ui/ResponsiveModal";
import { useMyHouseholds, useNavRole } from "@/components/shared/use-nav-role";
import { householdRoleLabel } from "@/lib/household-roles";
import {
  canManageMembers,
  canSeeMembers,
  canSeePeopleFull,
  canSetColor,
  grantableRoles,
  hasMemberActions,
  isMemberRole,
  isPaletteColor,
  memberActions,
  paletteVar,
  PALETTE,
  PALETTE_LABELS,
  ROLE_SUMMARIES,
  roleChangeSummary,
  type MemberRole,
} from "@/lib/members/roles";
import {
  ownerCount,
  useChangeRole,
  useCreateInvite,
  useHousehold,
  useInvites,
  useRemoveMember,
  useRoster,
  useRevokeInvite,
  useSetColor,
  useVendors,
  useViewerId,
  writeError,
  inviteExpiry,
  type RosterMember,
  type WriteError,
} from "@/lib/members/api";
import classes from "./Members.module.css";

const ICON_STROKE = 1.75;

/** True once the role for this household is something this build knows. */
function roleIsKnown(role: string | null | undefined): role is MemberRole {
  return isMemberRole(role);
}

/** Initials for the colour dot: "Amber Sims" → "AS". */
function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  const first = parts[0]!.charAt(0);
  const last = parts.length > 1 ? parts[parts.length - 1]!.charAt(0) : "";
  return (first + last).toUpperCase();
}

/** The dot's background: the validated palette variable, or the neutral ring. */
function dotStyle(color: string | null): React.CSSProperties | undefined {
  const v = paletteVar(color);
  return v ? { background: v } : undefined;
}

/** "Mar 2, 2026" from the API's RFC 3339 joined_at. */
function joinedLabel(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });
}

// ─── Page ─────────────────────────────────────────────────────────────

export default function MembersPage() {
  const role = useNavRole();
  // Four states, not two. The role is unknown while /me/households is in
  // flight; then it is either known-and-allowed, known-and-refused (a vendor:
  // ResMembers read is rolesSitter) or the request failed and the role will
  // never arrive. Only "refused" is "not your access", and only "allowed"
  // fetches anything: asking for a roster before the role is known would put a
  // 403 on the wire for every vendor who opens this URL (and the audit rule is
  // that the UI does not ask for what it may not read).
  const householdsQ = useMyHouseholds();
  const roleSettled = !householdsQ.isLoading;
  const roleKnown = roleIsKnown(role);
  // `allowed` is a plain boolean, so the page starts in the "waiting for the
  // role" state and never fetches on a guess.
  const allowed = roleSettled && canSeeMembers(role);
  const manager = allowed && canManageMembers(role);

  const { data: hh } = useHousehold(allowed);
  const { data: me } = useViewerId(allowed);
  const viewer = me?.id ?? null;
  // The roster shows skeleton rows only AFTER the role says yes: before that
  // there is nothing to load, and a skeleton for a request that was never sent
  // would be a lie.
  const { roster, isLoading: rosterLoading, error } = useRoster(allowed, viewer);
  const isLoading = allowed && rosterLoading;
  const owners = useMemo(() => ownerCount(roster), [roster]);

  const invites = useInvites(allowed && manager);
  // Only the two dialogs that can link a vendor need the contacts list, and
  // only a manager can open either. Asking earlier would be a request the
  // person never asked for.
  const vendors = useVendors(allowed && manager);

  const setColor = useSetColor();
  const changeRole = useChangeRole();
  const removeMember = useRemoveMember();
  const revokeInvite = useRevokeInvite();
  const createInvite = useCreateInvite();

  const [colorFor, setColorFor] = useState<RosterMember | null>(null);
  const [roleFor, setRoleFor] = useState<RosterMember | null>(null);
  const [removeFor, setRemoveFor] = useState<RosterMember | null>(null);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [errorBanner, setErrorBanner] = useState<WriteError | null>(null);

  const pendingInvites = (invites.data?.data ?? []).filter((i) => !i.accepted_at);

  // The role request failed: say so rather than guessing either way.
  if (roleSettled && !isMemberRole(role)) {
    return (
      <div className={classes.page}>
        <div className={classes.errorCard} role="alert" data-testid="members-role-error">
          <IconAlertTriangle size={20} stroke={ICON_STROKE} />
          <div>
            <b>We couldn&apos;t work out what you can do in this family</b>
            <p>
              Home OS didn&apos;t answer the family request, so this page won&apos;t guess whether to
              show you the member list. Nothing was changed.
            </p>
            <Button
              size="xs"
              variant="default"
              leftSection={<IconRefresh size={16} stroke={ICON_STROKE} />}
              onClick={() => window.location.reload()}
            >
              Try again
            </Button>
          </div>
        </div>
      </div>
    );
  }

  if (roleSettled && !allowed) {
    return (
      <div className={classes.page}>
        <div className={classes.stateCard}>
          <div className={classes.stateArt}>
            <IconUsers size={28} stroke={ICON_STROKE} />
          </div>
          <h2 className={classes.stateTitle}>The member list isn&apos;t part of your access</h2>
          <p className={classes.stateText}>
            The <b>{householdRoleLabel(role)}</b> role works on the house and its jobs, not on the
            family itself, so Home OS doesn&apos;t show you who is in it. An owner or family manager
            can change your role if that&apos;s wrong.
          </p>
          <Button component={Link} href="/dashboard" variant="default">
            Back to Today
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className={classes.page} data-testid="members-page">
      <p className={classes.lede}>
        Who can sign in to {hh?.data?.name ?? "this family"} and what each person can do. Kids and
        anyone without a login live in People &amp; House.
      </p>

      {/* ── The household itself ─────────────────────────────── */}
      <section className={classes.houseCard} aria-label="Household">
        <span className={classes.houseMark} aria-hidden>
          {(hh?.data?.name ?? "H").charAt(0).toUpperCase()}
        </span>
        <div className={classes.houseBody}>
          <h2 className={classes.houseName}>{hh?.data?.name ?? "This family"}</h2>
          <p className={classes.houseMeta}>
            {hh?.data?.timezone ?? "Time zone not set"}
            {role ? ` · you're a ${householdRoleLabel(role).toLowerCase()}` : ""}
          </p>
          <p className={classes.houseNote}>
            The time zone sets when reminders, bills and &ldquo;today&rdquo; roll over for everyone.
            {!manager && " Only an owner or family manager can change it."}
          </p>
        </div>
      </section>

      {/* ── Members ──────────────────────────────────────────── */}
      <div className={classes.sectionHead}>
        <h2 className={classes.sectionTitle}>
          Members {!isLoading && !error && <span className={classes.count}>{roster.length}</span>}
        </h2>
        {manager && (
          <Button
            leftSection={<IconUserPlus size={18} stroke={ICON_STROKE} />}
            onClick={() => setInviteOpen(true)}
            data-testid="invite-open"
          >
            Invite someone
          </Button>
        )}
      </div>

      {roleKnown && !manager && (
        <p className={classes.hint} data-testid="members-manager-hint">
          Only an owner or family manager can invite people, change roles or remove someone.
          {canSetColor(role) ? " You can still change your own colour." : ""}
        </p>
      )}

      {roleKnown && !canSeePeopleFull(role) && (
        // read_basic (people/handler.go `view`): a housesitter sees the name,
        // the relationship and the allergies, and nothing else about a person.
        // The page never asks for the rest, and says so rather than letting a
        // short row look like an empty record.
        <p className={classes.hint} data-testid="members-basic-people">
          You see each person&apos;s name, relationship and allergies. Birth dates, schools and
          notes stay private to the family.
        </p>
      )}

      {errorBanner && (
        <div className={classes.errorCard} role="alert" style={{ marginBottom: 12 }}>
          <IconAlertTriangle size={20} stroke={ICON_STROKE} />
          <div>
            <b>{errorBanner.title}</b>
            <p style={{ marginBottom: 0 }}>{errorBanner.detail}</p>
          </div>
        </div>
      )}

      {isLoading && (
        <div className={classes.roster} aria-hidden data-testid="members-loading">
          {[0, 1, 2].map((i) => (
            <div key={i} className={classes.skeletonRow}>
              <Skeleton height={32} width={32} radius="xl" />
              <Box style={{ flex: 1 }}>
                <Skeleton height={14} width={`${40 + i * 6}%`} />
                <Skeleton height={11} width="60%" mt={8} />
              </Box>
            </div>
          ))}
        </div>
      )}

      {!isLoading && error && (
        <div className={classes.errorCard} role="alert" data-testid="members-error">
          <IconAlertTriangle size={20} stroke={ICON_STROKE} />
          <div>
            <b>Couldn&apos;t load the family</b>
            <p>
              Home OS didn&apos;t answer. Nothing was changed. Check your connection and try again.
            </p>
            <Button
              size="xs"
              variant="default"
              leftSection={<IconRefresh size={16} stroke={ICON_STROKE} />}
              onClick={() => window.location.reload()}
            >
              Try again
            </Button>
          </div>
        </div>
      )}

      {!isLoading && !error && roster.length === 0 && (
        <div className={classes.stateCard} data-testid="members-empty">
          <div className={classes.stateArt}>
            <IconUsers size={28} stroke={ICON_STROKE} />
          </div>
          <h3 className={classes.stateTitle}>Nobody&apos;s in this family yet</h3>
          <p className={classes.stateText}>
            That can&apos;t usually happen: the household&apos;s first owner is its first member. If
            this stays empty, reload the page.
          </p>
        </div>
      )}

      {!isLoading && !error && roster.length > 0 && (
        <div className={classes.roster} role="list" data-testid="members-roster">
          {roster.map((m) => {
            const actions = memberActions(role, { role: m.role, isSelf: m.is_self }, owners);
            return (
              <div key={m.user_id} className={classes.row} role="listitem" data-testid="member-row">
                <span
                  className={`${classes.dot} ${dotStyle(m.color) ? "" : classes.dotFallback}`}
                  style={dotStyle(m.color)}
                  aria-hidden
                  data-color={isPaletteColor(m.color) ? m.color : "none"}
                >
                  {initials(m.name)}
                </span>

                <div className={classes.rowBody}>
                  <div className={classes.nameLine}>
                    <span className={classes.name}>{m.name}</span>
                    {m.is_self && <span className={classes.you}>(you)</span>}
                  </div>
                  <p className={classes.sub}>{m.email}</p>
                  <div className={classes.meta}>
                    <span
                      className={`${classes.roleTag} ${
                        isMemberRole(m.role) && canManageMembers(m.role) ? classes.roleManager : ""
                      }`}
                      data-testid="member-role"
                      data-role={m.role}
                    >
                      {householdRoleLabel(m.role)}
                    </span>
                    {m.relationship && <span>{m.relationship}</span>}
                    {m.allergies.length > 0 && (
                      <span className={classes.allergy} data-testid="member-allergy">
                        <IconAlertTriangle size={12} stroke={ICON_STROKE} />
                        {m.allergies.join(", ")}
                      </span>
                    )}
                    {m.role === "vendor" && (
                      <span className={classes.vendorLink}>
                        <IconLink size={13} stroke={ICON_STROKE} />
                        {m.vendor_name ? `${m.vendor_name} jobs only` : "every maintenance job"}
                      </span>
                    )}
                    <span>joined {joinedLabel(m.joined_at)}</span>
                  </div>
                </div>

                {hasMemberActions(actions) && (
                  <MemberMenu
                    member={m}
                    actions={actions}
                    onChangeRole={() => setRoleFor(m)}
                    onRemove={() => setRemoveFor(m)}
                    onColor={() => setColorFor(m)}
                  />
                )}
              </div>
            );
          })}
        </div>
      )}

      {/* ── Invitations (managers only) ──────────────────────── */}
      {manager && pendingInvites.length > 0 && (
        <section aria-label="Invites waiting">
          <div className={classes.sectionHead}>
            <h2 className={classes.sectionTitle}>
              Invites waiting <span className={classes.count}>{pendingInvites.length}</span>
            </h2>
          </div>
          <div className={classes.roster} data-testid="invites-list">
            {pendingInvites.map((inv) => {
              const exp = inviteExpiry(inv.expires_at);
              return (
                <div key={inv.id} className={classes.inviteRow} data-testid="invite-row">
                  <div className={classes.inviteBody}>
                    <div className={classes.name}>{inv.email}</div>
                    <div className={classes.meta}>
                      <span className={classes.roleTag}>{householdRoleLabel(inv.role)}</span>
                      <span className={exp.soon ? classes.expiring : undefined}>{exp.text}</span>
                    </div>
                  </div>
                  <div className={classes.inviteActions}>
                    {inv.accept_url && <CopyLinkButton url={inv.accept_url} email={inv.email} />}
                    <Button
                      size="xs"
                      variant="subtle"
                      color="red"
                      leftSection={<IconTrash size={15} stroke={ICON_STROKE} />}
                      onClick={() =>
                        revokeInvite.mutate(inv.id, {
                          onError: (err) => setErrorBanner(writeError(err, "The invite could not be revoked.")),
                        })
                      }
                      loading={revokeInvite.isPending}
                      data-testid="invite-revoke"
                    >
                      Revoke
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        </section>
      )}

      {/* ── The legend: what each role can do ────────────────── */}
      <section aria-labelledby="roles-legend">
        <div className={classes.sectionHead}>
          <h2 className={classes.sectionTitle} id="roles-legend">
            What each role can do
          </h2>
        </div>
        <div className={classes.legend} data-testid="roles-legend">
          {(Object.keys(ROLE_SUMMARIES) as MemberRole[]).map((r) => (
            <div key={r} className={classes.legendRow}>
              <b>{householdRoleLabel(r)}</b>
              <span>{ROLE_SUMMARIES[r]}</span>
            </div>
          ))}
        </div>
      </section>

      {/* ── Dialogs ──────────────────────────────────────────── */}
      {colorFor && (
        <ColorDialog
          member={colorFor}
          canEdit={canSetColor(role)}
          saving={setColor.isPending}
          onClose={() => setColorFor(null)}
          onSave={(color) => {
            if (!colorFor.person_id) return;
            setColor.mutate(
              { personId: colorFor.person_id, color },
              {
                onSuccess: () => setColorFor(null),
                onError: (err) => setErrorBanner(writeError(err, "The colour could not be saved.")),
              },
            );
          }}
        />
      )}

      {roleFor && (
        <RoleDialog
          member={roleFor}
          actorRole={role}
          saving={changeRole.isPending}
          vendors={vendors.data?.data ?? []}
          onClose={() => setRoleFor(null)}
          onSave={(nextRole, vendorId) => {
            changeRole.mutate(
              { userId: roleFor.user_id, role: nextRole, vendorId },
              {
                onSuccess: () => setRoleFor(null),
                onError: (err) => setErrorBanner(writeError(err, "The role could not be changed.")),
              },
            );
          }}
        />
      )}

      {removeFor && (
        <RemoveDialog
          member={removeFor}
          saving={removeMember.isPending}
          onClose={() => setRemoveFor(null)}
          onConfirm={() => {
            removeMember.mutate(removeFor.user_id, {
              onSuccess: () => setRemoveFor(null),
              onError: (err) => setErrorBanner(writeError(err, "The member could not be removed.")),
            });
          }}
        />
      )}

      {inviteOpen && (
        <InviteDialog
          actorRole={role}
          vendors={vendors.data?.data ?? []}
          saving={createInvite.isPending}
          result={createInvite.data?.data?.accept_url ?? null}
          onClose={() => {
            createInvite.reset();
            setInviteOpen(false);
          }}
          onCreate={(email, inviteRole, vendorId) =>
            createInvite.mutate(
              { email, role: inviteRole, vendor_id: vendorId },
              { onError: (err) => setErrorBanner(writeError(err, "The invitation could not be created.")) },
            )
          }
        />
      )}
    </div>
  );
}

// ─── Row menu ─────────────────────────────────────────────────────────

function MemberMenu({
  member,
  actions,
  onChangeRole,
  onRemove,
  onColor,
}: {
  member: RosterMember;
  actions: { changeRole: boolean; remove: boolean; color: boolean };
  onChangeRole: () => void;
  onRemove: () => void;
  onColor: () => void;
}) {
  return (
    <Menu position="bottom-end" withinPortal shadow="lg" radius="md" width={220}>
      <Menu.Target>
        <button
          type="button"
          className={classes.rowMenu}
          aria-label={`Options for ${member.name}`}
          data-testid="member-menu"
        >
          <IconDots size={20} stroke={ICON_STROKE} />
        </button>
      </Menu.Target>
      <Menu.Dropdown>
        {actions.changeRole && (
          <Menu.Item leftSection={<IconUsers size={16} stroke={ICON_STROKE} />} onClick={onChangeRole} data-testid="member-change-role">
            Change role
          </Menu.Item>
        )}
        {actions.color && (
          <Menu.Item leftSection={<IconPalette size={16} stroke={ICON_STROKE} />} onClick={onColor} data-testid="member-change-color">
            Change colour
          </Menu.Item>
        )}
        {actions.remove && (
          <>
            <Menu.Divider />
            <Menu.Item
              color="red"
              leftSection={<IconTrash size={16} stroke={ICON_STROKE} />}
              onClick={onRemove}
              data-testid="member-remove"
            >
              Remove from family
            </Menu.Item>
          </>
        )}
      </Menu.Dropdown>
    </Menu>
  );
}

// ─── Colour ───────────────────────────────────────────────────────────

function ColorDialog({
  member,
  canEdit,
  saving,
  onClose,
  onSave,
}: {
  member: RosterMember;
  canEdit: boolean;
  saving: boolean;
  onClose: () => void;
  onSave: (color: string) => void;
}) {
  const [picked, setPicked] = useState<string>(isPaletteColor(member.color) ? member.color : "cyan");
  const unchanged = picked === member.color;

  return (
    <ResponsiveModal opened onClose={onClose} title={`Colour for ${member.name}`} size="sm">
      <p className={classes.hint} style={{ marginTop: 0 }}>
        {member.is_self
          ? "Your colour marks your events on the calendar and on lists."
          : `${member.name.split(" ")[0]}'s colour marks their events on the calendar and on lists.`}
      </p>
      <div className={classes.swatches} role="radiogroup" aria-label="Colour" data-testid="color-swatches">
        {PALETTE.map((name) => (
          <button
            key={name}
            type="button"
            role="radio"
            aria-checked={picked === name}
            aria-label={PALETTE_LABELS[name]}
            className={classes.swatch}
            onClick={() => setPicked(name)}
            disabled={!canEdit || saving}
            data-testid={`color-swatch-${name}`}
          >
            <span className={classes.swatchDisc} style={{ background: paletteVar(name) ?? undefined }} aria-hidden />
            <span className={classes.swatchLabel}>{PALETTE_LABELS[name]}</span>
          </button>
        ))}
      </div>
      {!member.person_id && (
        <p className={classes.hint}>This member has no people record yet, so there is nothing to recolour.</p>
      )}
      <ResponsiveModalActions>
        <Button variant="default" onClick={onClose} disabled={saving}>
          Cancel
        </Button>
        <Button
          onClick={() => onSave(picked)}
          loading={saving}
          disabled={!canEdit || unchanged || !member.person_id}
          data-testid="color-save"
        >
          Save colour
        </Button>
      </ResponsiveModalActions>
    </ResponsiveModal>
  );
}

// ─── Role change ──────────────────────────────────────────────────────

function RoleDialog({
  member,
  actorRole,
  saving,
  vendors,
  onClose,
  onSave,
}: {
  member: RosterMember;
  actorRole: string | null;
  saving: boolean;
  vendors: { id: string; name: string }[];
  onClose: () => void;
  onSave: (role: string, vendorId: string | null) => void;
}) {
  const current = isMemberRole(member.role) ? member.role : null;
  const choices = grantableRoles(actorRole);
  const [picked, setPicked] = useState<MemberRole | null>(current);
  const [vendorId, setVendorId] = useState<string | null>(member.vendor_id);

  const summary = current && picked ? roleChangeSummary(current, picked) : null;
  const changes = !!picked && picked !== current;

  // The six role cards fill a phone sheet, so a pick at the top leaves the
  // consequence below the fold. Bring it into view the first time it appears:
  // the whole point of the block is that nobody commits to a demotion unseen.
  const consequenceRef = useRef<HTMLDivElement | null>(null);
  const shown = useRef(false);
  useEffect(() => {
    if (!changes) {
      shown.current = false;
      return;
    }
    if (shown.current) return;
    shown.current = true;
    consequenceRef.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [changes]);

  return (
    <ResponsiveModal opened onClose={onClose} title={`Role for ${member.name}`} size="md">
      <p className={classes.hint} style={{ marginTop: 0 }}>
        A role decides what {member.name.split(" ")[0]} can see and change. It applies the moment you
        save.
      </p>
      <div className={classes.roleCards} role="radiogroup" aria-label="Role" data-testid="role-choices">
        {choices.map((r) => (
          <button
            key={r}
            type="button"
            role="radio"
            aria-checked={picked === r}
            className={classes.roleCard}
            onClick={() => setPicked(r)}
            disabled={saving}
            data-testid={`role-choice-${r}`}
          >
            <span className={classes.roleRadio} aria-hidden />
            <span className={classes.roleCardBody}>
              <span className={classes.roleCardTitle}>
                {householdRoleLabel(r)}
                {r === current ? " · current" : ""}
              </span>
              <span className={classes.roleCardText}>{ROLE_SUMMARIES[r]}</span>
            </span>
          </button>
        ))}
      </div>

      {picked === "vendor" && (
        <div style={{ marginTop: 12 }}>
          <label className={classes.hint} htmlFor="member-vendor" style={{ display: "block", fontWeight: 600, color: "var(--ink-2)" }}>
            Which company do they work for? (optional)
          </label>
          <select
            id="member-vendor"
            className="w-full"
            style={{
              height: 40,
              padding: "0 10px",
              borderRadius: "var(--r-sm)",
              border: "1px solid var(--line)",
              background: "var(--surface)",
              color: "var(--ink)",
              fontSize: 14,
            }}
            value={vendorId ?? ""}
            onChange={(e) => setVendorId(e.target.value || null)}
            disabled={saving}
          >
            <option value="">Not linked: every maintenance job</option>
            {vendors.map((v) => (
              <option key={v.id} value={v.id}>
                {v.name}
              </option>
            ))}
          </select>
          <p className={classes.hint} style={{ marginTop: 6 }}>
            A linked vendor sees only that company&apos;s jobs.
          </p>
        </div>
      )}

      {changes && summary && (
        <div
          ref={consequenceRef}
          className={`${classes.consequence} ${summary.narrower ? classes.consequenceWarn : ""}`}
          data-testid="role-consequence"
        >
          <span className={classes.consequenceTitle}>
            {summary.narrower ? "This takes access away" : "This gives more access"}
          </span>
          {summary.lost.length > 0 && (
            <>
              <span>{member.name.split(" ")[0]} can no longer:</span>
              <ul className={classes.changeList}>
                {summary.lost.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
            </>
          )}
          {summary.gained.length > 0 && (
            <>
              <span>{summary.lost.length > 0 ? "And they can now:" : "They can now:"}</span>
              <ul className={classes.changeList}>
                {summary.gained.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
            </>
          )}
          {summary.lost.length === 0 && summary.gained.length === 0 && (
            <span>The two roles see and do the same things; only the label changes.</span>
          )}
        </div>
      )}

      <ResponsiveModalActions>
        <div className={classes.confirmActions}>
          <Button variant="default" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button
            onClick={() => picked && onSave(picked, picked === "vendor" ? vendorId : null)}
            loading={saving}
            disabled={!picked || !changes}
            data-testid="role-save"
          >
            {picked && picked !== current ? `Change to ${householdRoleLabel(picked)}` : "Change role"}
          </Button>
        </div>
      </ResponsiveModalActions>
    </ResponsiveModal>
  );
}

// ─── Remove ───────────────────────────────────────────────────────────

function RemoveDialog({
  member,
  saving,
  onClose,
  onConfirm,
}: {
  member: RosterMember;
  saving: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <ResponsiveModal opened onClose={onClose} title={`Remove ${member.name}?`} size="sm">
      <div className={`${classes.consequence} ${classes.consequenceWarn}`}>
        <span className={classes.consequenceTitle}>They lose access right away</span>
        <span>
          {member.name.split(" ")[0]} can&apos;t sign in to this family any more, and every agent
          token they made for it stops working. Their people record and the things they wrote stay.
        </span>
      </div>
      <ResponsiveModalActions>
        <Button variant="default" onClick={onClose} disabled={saving}>
          Keep {member.name.split(" ")[0]}
        </Button>
        <Button color="red" onClick={onConfirm} loading={saving} data-testid="member-remove-confirm">
          Remove
        </Button>
      </ResponsiveModalActions>
    </ResponsiveModal>
  );
}

// ─── Invite ───────────────────────────────────────────────────────────

function InviteDialog({
  actorRole,
  vendors,
  saving,
  result,
  onClose,
  onCreate,
}: {
  actorRole: string | null;
  vendors: { id: string; name: string }[];
  saving: boolean;
  result: string | null;
  onClose: () => void;
  onCreate: (email: string, role: string, vendorId: string | null) => void;
}) {
  const choices = grantableRoles(actorRole);
  const [email, setEmail] = useState("");
  const [picked, setPicked] = useState<MemberRole | null>("family_member");
  const [vendorId, setVendorId] = useState<string | null>(null);

  return (
    <ResponsiveModal opened onClose={onClose} title="Invite someone" size="md">
      {result ? (
        <>
          <p className={classes.hint} style={{ marginTop: 0 }}>
            Send them this link: text, email or in person. It works once and expires in 7 days.
          </p>
          <CopyLinkButton url={result} email={email} block />
        </>
      ) : (
        <>
          <p className={classes.hint} style={{ marginTop: 0 }}>
            You&apos;ll get a link to send them however you like. They sign in with this address, so
            it has to be theirs.
          </p>
          <label htmlFor="invite-email" className={classes.hint} style={{ display: "block", fontWeight: 600, color: "var(--ink-2)" }}>
            Their email
          </label>
          <input
            id="invite-email"
            type="email"
            autoComplete="off"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="name@example.com"
            style={{
              width: "100%",
              height: 44,
              padding: "0 12px",
              borderRadius: "var(--r-sm)",
              border: "1px solid var(--line)",
              background: "var(--surface)",
              color: "var(--ink)",
              fontSize: 16,
            }}
          />
          <div className={classes.roleCards} role="radiogroup" aria-label="What they can do" style={{ marginTop: 16 }} data-testid="invite-choices">
            {choices.map((r) => (
              <button
                key={r}
                type="button"
                role="radio"
                aria-checked={picked === r}
                className={classes.roleCard}
                onClick={() => setPicked(r)}
                disabled={saving}
                data-testid={`invite-role-${r}`}
              >
                <span className={classes.roleRadio} aria-hidden />
                <span className={classes.roleCardBody}>
                  <span className={classes.roleCardTitle}>{householdRoleLabel(r)}</span>
                  <span className={classes.roleCardText}>{ROLE_SUMMARIES[r]}</span>
                </span>
              </button>
            ))}
          </div>
          {picked === "vendor" && (
            <div style={{ marginTop: 12 }}>
              <label htmlFor="invite-vendor" className={classes.hint} style={{ display: "block", fontWeight: 600, color: "var(--ink-2)" }}>
                Which company are they from? (optional)
              </label>
              <select
                id="invite-vendor"
                value={vendorId ?? ""}
                onChange={(e) => setVendorId(e.target.value || null)}
                disabled={saving}
                style={{
                  width: "100%",
                  height: 44,
                  padding: "0 10px",
                  borderRadius: "var(--r-sm)",
                  border: "1px solid var(--line)",
                  background: "var(--surface)",
                  color: "var(--ink)",
                  fontSize: 16,
                }}
              >
                <option value="">Not linked: every maintenance job</option>
                {vendors.map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.name}
                  </option>
                ))}
              </select>
            </div>
          )}
          <p className={classes.hint}>
            {(grantableRoles(actorRole).length === 0)
              ? "You can't invite anyone with your role."
              : "Owner isn't offered here. To make someone an owner, invite them, then change their role."}
          </p>
        </>
      )}
      <ResponsiveModalActions>
        <Button variant="default" onClick={onClose} disabled={saving}>
          {result ? "Done" : "Cancel"}
        </Button>
        {!result && (
          <Button
            onClick={() => picked && onCreate(email.trim(), picked, picked === "vendor" ? vendorId : null)}
            loading={saving}
            disabled={!email.includes("@") || !picked}
            data-testid="invite-create"
          >
            Create invite link
          </Button>
        )}
      </ResponsiveModalActions>
    </ResponsiveModal>
  );
}

// ─── Copy link ────────────────────────────────────────────────────────

function CopyLinkButton({ url, email, block }: { url: string; email: string; block?: boolean }) {
  const [copied, setCopied] = useState(false);
  return (
    <Tooltip label={copied ? "Copied" : "Copy the invite link"} withArrow>
      <Button
        size="xs"
        variant="default"
        fullWidth={block}
        leftSection={copied ? <IconCheck size={15} stroke={ICON_STROKE} /> : <IconCopy size={15} stroke={ICON_STROKE} />}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(url);
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
          } catch {
            // Clipboard blocked (insecure context): show the link instead of
            // pretending it was copied.
            window.prompt(`Copy this link for ${email}`, url);
          }
        }}
        data-testid="invite-copy"
      >
        {copied ? "Copied" : "Copy link"}
      </Button>
    </Tooltip>
  );
}
