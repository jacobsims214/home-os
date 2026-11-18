package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
)

// #4799: a vendor login linked to a vendors row (memberships.vendor_id)
// sees and updates only that vendor's maintenance jobs through the API; an
// unlinked vendor keeps seeing every job. Managers set and clear the link
// through PATCH /households/me/members/{userId}. Everything goes through the
// real RequireAuth + RequirePolicy stack on Postgres.

func (f *stage2Fixture) userIDOf(t *testing.T, m stage2Member) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT user_id::text FROM people WHERE id = $1`, m.personID).Scan(&id))
	return id
}

func (f *stage2Fixture) insertID(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&id))
	return id
}

func (f *stage2Fixture) membershipVendor(t *testing.T, hh, userID string) *string {
	t.Helper()
	var v *string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT vendor_id::text FROM memberships WHERE household_id = $1 AND user_id = $2`, hh, userID).Scan(&v))
	return v
}

// ids returns the sorted "id" fields of a list response.
func ids(t *testing.T, body []byte) []string {
	t.Helper()
	var rows []struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(dataOf(t, body), &rows), string(body))
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}

func sorted(s ...string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestVendorScope_MemberLinkAndMaintenance(t *testing.T) {
	f := newProtectedFixture(t)
	hh := f.household(t)
	other := f.household(t)

	owner := f.member(t, hh, auth.RoleOwner)
	manager := f.member(t, hh, auth.RoleFamilyManager)
	familyMember := f.member(t, hh, auth.RoleFamilyMember)
	linked := f.member(t, hh, auth.RoleVendor)
	unlinked := f.member(t, hh, auth.RoleVendor)
	linkedID := f.userIDOf(t, linked)
	familyMemberID := f.userIDOf(t, familyMember)

	plumber := f.insertID(t, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Plumber') RETURNING id::text`, hh)
	electrician := f.insertID(t, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Electrician') RETURNING id::text`, hh)
	foreign := f.insertID(t, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Other family plumber') RETURNING id::text`, other)

	plumberTask := f.insertID(t, `INSERT INTO maintenance_tasks (household_id, name, vendor_id) VALUES ($1, 'Fix sink', $2) RETURNING id::text`, hh, plumber)
	electricTask := f.insertID(t, `INSERT INTO maintenance_tasks (household_id, name, vendor_id) VALUES ($1, 'Replace outlet', $2) RETURNING id::text`, hh, electrician)
	openTask := f.insertID(t, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Clean gutters') RETURNING id::text`, hh)
	plumberSched := f.insertID(t, `INSERT INTO maintenance_schedules (household_id, name, rrule, vendor_id) VALUES ($1, 'Water heater flush', 'FREQ=YEARLY', $2) RETURNING id::text`, hh, plumber)
	openSched := f.insertID(t, `INSERT INTO maintenance_schedules (household_id, name, rrule) VALUES ($1, 'HVAC filter', 'FREQ=MONTHLY') RETURNING id::text`, hh)
	allTasks := sorted(plumberTask, electricTask, openTask)
	allScheds := sorted(plumberSched, openSched)

	patchMember := func(as stage2Member, userID string, body any) int {
		t.Helper()
		rec := f.do(t, http.MethodPatch, "/api/v1/households/me/members/"+userID, body, &as)
		return rec.Code
	}

	t.Run("only managers link, same family, vendor role only", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden, patchMember(familyMember, linkedID, map[string]any{"vendor_id": plumber}))
		assert.Equal(t, http.StatusForbidden, patchMember(linked, linkedID, map[string]any{"vendor_id": plumber}), "a vendor cannot link themselves")
		assert.Equal(t, http.StatusNotFound, patchMember(owner, linkedID, map[string]any{"vendor_id": foreign}), "another family's vendor")
		assert.Equal(t, http.StatusBadRequest, patchMember(owner, linkedID, map[string]any{"vendor_id": "not-a-uuid"}))
		assert.Equal(t, http.StatusBadRequest, patchMember(owner, familyMemberID, map[string]any{"vendor_id": plumber}), "non-vendor role")
		assert.Equal(t, http.StatusBadRequest, patchMember(owner, linkedID, map[string]any{"role": auth.RoleViewer, "vendor_id": plumber}), "resulting role is not vendor")
		assert.Equal(t, http.StatusBadRequest, patchMember(owner, linkedID, map[string]any{}), "empty body")
		assert.Nil(t, f.membershipVendor(t, hh, linkedID))
		assert.Equal(t, auth.RoleVendor, roleOf(t, f, hh, linkedID), "refused changes leave the role alone")

		// The manager links; the members list shows the vendor.
		rec := f.do(t, http.MethodPatch, "/api/v1/households/me/members/"+linkedID, map[string]any{"vendor_id": plumber}, &manager)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var members []struct {
			UserID     string  `json:"user_id"`
			Role       string  `json:"role"`
			VendorID   *string `json:"vendor_id"`
			VendorName *string `json:"vendor_name"`
		}
		require.NoError(t, json.Unmarshal(dataOf(t, rec.Body.Bytes()), &members))
		found := false
		for _, m := range members {
			if m.UserID == linkedID {
				found = true
				require.NotNil(t, m.VendorID)
				assert.Equal(t, plumber, *m.VendorID)
				require.NotNil(t, m.VendorName)
				assert.Equal(t, "Plumber", *m.VendorName)
				assert.Equal(t, auth.RoleVendor, m.Role)
			} else {
				assert.Nil(t, m.VendorID, m.UserID)
			}
		}
		assert.True(t, found)
	})

	t.Run("linked vendor sees and updates only their own jobs", func(t *testing.T) {
		rec := f.do(t, http.MethodGet, "/api/v1/maintenance/tasks", nil, &linked)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []string{plumberTask}, ids(t, rec.Body.Bytes()))

		rec = f.do(t, http.MethodGet, "/api/v1/maintenance/tasks?status=pending", nil, &linked)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, []string{plumberTask}, ids(t, rec.Body.Bytes()))

		rec = f.do(t, http.MethodGet, "/api/v1/maintenance/schedules", nil, &linked)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []string{plumberSched}, ids(t, rec.Body.Bytes()))

		rec = f.do(t, http.MethodPatch, "/api/v1/maintenance/tasks/"+plumberTask, map[string]any{"status": "in_progress", "notes": "parts ordered"}, &linked)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		for _, id := range []string{electricTask, openTask} {
			rec = f.do(t, http.MethodPatch, "/api/v1/maintenance/tasks/"+id, map[string]any{"status": "done"}, &linked)
			assert.Equal(t, http.StatusNotFound, rec.Code, "%s: %s", id, rec.Body.String())
		}
		var st string
		require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT status::text FROM maintenance_tasks WHERE id = $1`, electricTask).Scan(&st))
		assert.Equal(t, "pending", st, "a refused update changes nothing")

		// Still status-only on their own job.
		rec = f.do(t, http.MethodPatch, "/api/v1/maintenance/tasks/"+plumberTask, map[string]any{"name": "renamed"}, &linked)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("unlinked vendor and family roles see every job", func(t *testing.T) {
		for name, m := range map[string]stage2Member{"unlinked vendor": unlinked, "owner": owner, "family_member": familyMember} {
			rec := f.do(t, http.MethodGet, "/api/v1/maintenance/tasks", nil, &m)
			require.Equal(t, http.StatusOK, rec.Code, name)
			assert.Equal(t, allTasks, ids(t, rec.Body.Bytes()), name)
			rec = f.do(t, http.MethodGet, "/api/v1/maintenance/schedules", nil, &m)
			require.Equal(t, http.StatusOK, rec.Code, name)
			assert.Equal(t, allScheds, ids(t, rec.Body.Bytes()), name)
		}
		rec := f.do(t, http.MethodPatch, "/api/v1/maintenance/tasks/"+electricTask, map[string]any{"status": "in_progress"}, &unlinked)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("relinking, clearing and role changes apply on the next request", func(t *testing.T) {
		require.Equal(t, http.StatusOK, patchMember(owner, linkedID, map[string]any{"vendor_id": electrician}))
		rec := f.do(t, http.MethodGet, "/api/v1/maintenance/tasks", nil, &linked)
		assert.Equal(t, []string{electricTask}, ids(t, rec.Body.Bytes()))

		// null clears the link: back to every job.
		require.Equal(t, http.StatusOK, patchMember(owner, linkedID, map[string]any{"vendor_id": nil}))
		assert.Nil(t, f.membershipVendor(t, hh, linkedID))
		rec = f.do(t, http.MethodGet, "/api/v1/maintenance/tasks", nil, &linked)
		assert.Equal(t, allTasks, ids(t, rec.Body.Bytes()))

		// Changing the role away from vendor drops the link; a later
		// demotion back to vendor starts unlinked.
		require.Equal(t, http.StatusOK, patchMember(owner, linkedID, map[string]any{"vendor_id": plumber}))
		require.Equal(t, http.StatusOK, patchMember(owner, linkedID, map[string]any{"role": auth.RoleViewer}))
		assert.Nil(t, f.membershipVendor(t, hh, linkedID))
		require.Equal(t, http.StatusOK, patchMember(owner, linkedID, map[string]any{"role": auth.RoleVendor, "vendor_id": plumber}), "role and link in one request")
		v := f.membershipVendor(t, hh, linkedID)
		require.NotNil(t, v)
		assert.Equal(t, plumber, *v)

		// Deleting the vendors row clears the link (064 ON DELETE SET NULL).
		_, err := f.pool.Exec(context.Background(), `DELETE FROM vendors WHERE id = $1`, plumber)
		require.NoError(t, err)
		assert.Nil(t, f.membershipVendor(t, hh, linkedID))
	})

}

func roleOf(t *testing.T, f *stage2Fixture, hh, userID string) string {
	t.Helper()
	var r string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`, hh, userID).Scan(&r))
	return r
}
