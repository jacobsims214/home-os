package notification

import (
	"context"
	"slices"
	"testing"
	"time"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// TestNotificationReadFollowsCurrentRole (#4794): notices written while a
// member had a broader role disappear from find, the counts, UnreadCount and
// mark read once they are demoted, and come back when they are promoted.
// The rule is role_can_see_entity (061) on the notice's kind and, for notes,
// files, lists and document links, on their parent's kind; files attached to
// or linked from a credential are hidden from roles without secrets read.
func TestNotificationReadFollowsCurrentRole(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewNotificationFindTool(pool)
	_, _, mark := NewNotificationMarkReadTool(pool)
	// Amber's claims keep saying family_member: the role must be re-read.
	amberCtx, amber := addMember(t, pool, hh, "Amber")
	h := hh.HouseholdID

	secret := insertID(t, pool, `INSERT INTO secrets (household_id, entity_type, entity_id, encrypted_data, iv, name, secret_type)
		VALUES ($1, 'household', $1, '\x00', '\x00', 'Bank login', 'login') RETURNING id`, h)
	secretNote := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'secret', $2, 'pin hint') RETURNING id`, h, secret)
	familyNote := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'household', $1, 'wifi') RETURNING id`, h)
	bill := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 142.50, 20) RETURNING id`, h)
	blob := func() string {
		return insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('\x00', 1) RETURNING id`)
	}
	file := func(kind, entity string) string {
		return insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id)
			VALUES ($1, $2, 'scan.pdf', 1, $3, $4) RETURNING id`, h, blob(), kind, entity)
	}
	plainFile := file("household", h.String())
	secretFile := file("secret", secret)
	linkedFile := file("household", h.String())
	if _, err := pool.Exec(context.Background(), `INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id)
		VALUES ($1, 'secret', $2, 'file', $3)`, h, secret, linkedFile); err != nil {
		t.Fatal(err)
	}
	vehicle := insertID(t, pool, `INSERT INTO vehicles (household_id) VALUES ($1) RETURNING id`, h)
	groceries := insertID(t, pool, `INSERT INTO lists (household_id, name, entity_type, entity_id) VALUES ($1, 'Groceries', 'household', $1) RETURNING id`, h)
	carList := insertID(t, pool, `INSERT INTO lists (household_id, name, entity_type, entity_id) VALUES ($1, 'Car kit', 'vehicle', $2) RETURNING id`, h, vehicle)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, h)
	event := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'dentist', '{}', 'e') RETURNING id`, cal)

	// Written while Amber was a family member (as 054/061 and the worker write them).
	n := map[string]string{
		"secret":     notify(t, pool, h, amber, "item_changed", `Dana updated secret "Bank login"`, "secret", secret, 9*time.Hour),
		"secretNote": notify(t, pool, h, amber, "item_changed", `Dana updated note "pin hint"`, "note", secretNote, 8*time.Hour),
		"secretFile": notify(t, pool, h, amber, "item_changed", `Dana updated file "scan.pdf"`, "file", secretFile, 7*time.Hour),
		"linkedFile": notify(t, pool, h, amber, "item_changed", `Dana updated file "scan.pdf"`, "file", linkedFile, 6*time.Hour),
		"deleted":    notify(t, pool, h, amber, "item_changed", `Dana deleted secret "Old login"`, "", nil, 5*time.Hour),
		"bill":       notify(t, pool, h, "", "bill_due", "Electric due soon", "bill", bill, 4*time.Hour),
		"plainFile":  notify(t, pool, h, amber, "item_changed", `Dana updated file "scan.pdf"`, "file", plainFile, 3*time.Hour),
		"familyNote": notify(t, pool, h, amber, "item_changed", `Dana updated note "wifi"`, "note", familyNote, 2*time.Hour),
		"event":      notify(t, pool, h, amber, "calendar_alarm", "Dentist in 30 minutes", "calendar_event", event, time.Hour),
		"groceries":  notify(t, pool, h, amber, "item_changed", `Dana deleted list item "milk"`, "list", groceries, 50*time.Minute),
		"carList":    notify(t, pool, h, amber, "item_changed", `Dana deleted list item "jack"`, "list", carList, 40*time.Minute),
	}
	// No row here has an unregistered kind: notifications.entity_type is
	// entity_kind since migration 070, so one cannot be stored. The fail-closed
	// branch for a kind the reader may not see is still covered by "deleted",
	// whose entity_type is NULL (the record is gone), which role_can_see_entity
	// refuses to viewer and housesitter.
	all := []string{"secret", "secretNote", "secretFile", "linkedFile", "deleted", "bill", "plainFile", "familyNote", "event", "groceries", "carList"}

	setRole := func(role string) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), `UPDATE memberships SET role = $3 WHERE household_id = $1 AND user_id = $2`, h, amber, role); err != nil {
			t.Fatal(err)
		}
	}
	// check asserts find, its counts and UnreadCount show exactly want.
	check := func(role string, want []string) {
		t.Helper()
		d := call[findResult](t, amberCtx, "notification_find", find, map[string]any{"limit": 100}).data(t)
		var got []string
		for _, x := range d.Notifications {
			for k, id := range n {
				if id == x.ID {
					got = append(got, k)
				}
			}
		}
		slices.Sort(got)
		w := slices.Clone(want)
		slices.Sort(w)
		if !slices.Equal(got, w) {
			t.Fatalf("%s sees %v, want %v", role, got, w)
		}
		unread := 0
		for _, x := range d.Notifications {
			if !x.Read {
				unread++
			}
		}
		if d.Total != len(want) || d.Returned != len(want) || d.UnreadCount != unread {
			t.Fatalf("%s counts: total %d returned %d unread %d, want %d/%d/%d", role, d.Total, d.Returned, d.UnreadCount, len(want), len(want), unread)
		}
		u := call[findResult](t, amberCtx, "notification_find", find, map[string]any{"unread_only": true, "limit": 100}).data(t)
		if u.Total != unread || u.Returned != unread {
			t.Fatalf("%s unread_only: total %d returned %d, want %d", role, u.Total, u.Returned, unread)
		}
		if c, err := UnreadCount(amberCtx, pool, h.String(), amber); err != nil || c != unread {
			t.Fatalf("%s UnreadCount = %d, %v; want %d", role, c, err, unread)
		}
	}

	setRole("owner")
	check("owner", all)

	// Owner -> viewer: credential notices go (the secret, a note on it, a file
	// on it or linked from it, a deleted record whose kind is unknown, an
	// unknown kind); bills, ordinary files, family notes and events stay.
	setRole("viewer")
	viewerSees := []string{"bill", "plainFile", "familyNote", "event", "groceries", "carList"}
	check("viewer", viewerSees)
	// A hidden id can't be marked read: not found, nothing marked.
	for _, k := range []string{"secret", "secretNote", "linkedFile"} {
		env := call[markResult](t, amberCtx, "notification_mark_read", mark, map[string]any{"notification_ids": []any{n["bill"], n[k]}})
		wantError(t, env, "notification_ids")
	}
	// all=true marks only the visible ones.
	d := call[markResult](t, amberCtx, "notification_mark_read", mark, map[string]any{"all": true}).data(t)
	if d.Marked != len(viewerSees) || d.UnreadCount != 0 {
		t.Fatalf("viewer mark all = %+v", d)
	}
	var stillUnread int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications WHERE id = ANY($1::uuid[]) AND read_at IS NULL`,
		[]string{n["secret"], n["secretNote"], n["secretFile"], n["linkedFile"], n["deleted"]}).Scan(&stillUnread); err != nil {
		t.Fatal(err)
	}
	if stillUnread != 5 {
		t.Fatalf("mark all touched %d hidden notices", 5-stillUnread)
	}
	check("viewer", viewerSees)

	// Housesitter: no bills, files, family-level notes or lists on a vehicle;
	// events and the family grocery list stay.
	setRole("housesitter")
	check("housesitter", []string{"event", "groceries"})

	// Promoted back: everything again, the six hidden ones still unread.
	setRole("owner")
	check("owner", all)

	// Removed from the family: nothing, even with old claims.
	if _, err := pool.Exec(context.Background(), `DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, h, amber); err != nil {
		t.Fatal(err)
	}
	check("removed", nil)

	// Local dev (no user UUID): the claims' role decides; no claims, nothing.
	dev := server.ContextWithClaims(context.Background(), &auth.Claims{UserID: "local-dev", HouseholdID: h.String(), Role: "viewer"})
	if c, err := UnreadCount(dev, pool, h.String(), "local-dev"); err != nil || c != 0 {
		t.Fatalf("local-dev viewer UnreadCount = %d, %v (bill was marked read)", c, err)
	}
	if c, err := UnreadCount(context.Background(), pool, h.String(), "local-dev"); err != nil || c != 0 {
		t.Fatalf("no-claims UnreadCount = %d, %v", c, err)
	}
}
