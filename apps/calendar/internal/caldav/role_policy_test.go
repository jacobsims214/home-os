package caldav

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"home-os/calendar/internal/auth"
	"home-os/calendar/internal/db"
)

// Role policy tests (#4718, KB decisions/2026-11-15-role-policy.md). Every
// request goes through AuthMiddleware and Handler, as in production, so the
// role really comes from memberships.

// davMultistatus is just enough of a 207 body to tell which hrefs came back
// with content (a 200 propstat) and which as 404s.
type davMultistatus struct {
	Responses []struct {
		Href     string `xml:"href"`
		Status   string `xml:"status"`
		Propstat []struct {
			Status string `xml:"status"`
		} `xml:"propstat"`
	} `xml:"response"`
}

// okHrefs returns the sorted hrefs of the responses that have a 200 propstat.
func okHrefs(t *testing.T, body string) []string {
	t.Helper()
	var ms davMultistatus
	if err := xml.Unmarshal([]byte(body), &ms); err != nil {
		t.Fatalf("parse multistatus: %v\n%s", err, body)
	}
	var out []string
	for _, r := range ms.Responses {
		for _, ps := range r.Propstat {
			if strings.Contains(ps.Status, " 200 ") {
				out = append(out, r.Href)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// roleFixture is one family with every calendar type, mirror events, two
// vendors and one login per role.
type roleFixture struct {
	hh      string
	cals    map[string]string // short name -> caldav_uid
	vendorA string
	members map[string]string // login name -> email
	userIDs map[string]string // login name -> user id
	bodies  map[string]string // "cal/uid" -> PUT body (served byte for byte)
	srv     http.Handler
}

const rolePassword = "role-app-pw"

// calNames are the fixture's calendars and their types.
var calNames = []struct{ name, typ string }{
	{"family", "family"},
	{"bills", "bills"},
	{"maint", "maintenance"},
	{"house", "properties"},
	{"cars", "vehicles"},
}

// logins: login name -> membership role. vendor-a is a vendor linked to
// vendor A (memberships.vendor_id); vendor is a vendor with no link.
var logins = []struct{ name, role string }{
	{"owner", "owner"},
	{"manager", "family_manager"},
	{"member", "family_member"},
	{"viewer", "viewer"},
	{"sitter", "housesitter"},
	{"vendor", "vendor"},
	{"vendor-a", "vendor"},
}

func seedRoleFixture(t *testing.T, repo *db.Repo, pool *pgxpool.Pool) *roleFixture {
	t.Helper()
	ctx := context.Background()
	// The table sends hundreds of requests from one IP: lift the request
	// limit (the limiter itself is covered in the auth package).
	limits := auth.DefaultRateLimitConfig()
	limits.RequestsPerMinute, limits.RequestBurst = 1_000_000, 1_000_000
	f := &roleFixture{
		srv:     auth.AuthMiddlewareWithConfig(repo, limits)(NewHandler(repo)),
		cals:    map[string]string{},
		members: map[string]string{},
		userIDs: map[string]string{},
		bodies:  map[string]string{},
	}
	if err := pool.QueryRow(ctx, `INSERT INTO households (name) VALUES ('Roles') RETURNING id::text`).Scan(&f.hh); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	for _, c := range calNames {
		uid := "cal-" + c.name + "-" + f.hh[:8]
		if _, err := pool.Exec(ctx,
			`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, $2, $3::calendar_type, $4)`,
			f.hh, c.name, c.typ, uid); err != nil {
			t.Fatalf("insert calendar %s: %v", c.name, err)
		}
		f.cals[c.name] = uid
	}
	var vendorB string
	for _, v := range []struct {
		name string
		dst  *string
	}{{"Ace Plumbing", &f.vendorA}, {"Bolt Electric", &vendorB}} {
		if err := pool.QueryRow(ctx,
			`INSERT INTO vendors (household_id, name) VALUES ($1, $2) RETURNING id::text`, f.hh, v.name).Scan(v.dst); err != nil {
			t.Fatalf("insert vendor: %v", err)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(rolePassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logins {
		email := l.name + "-" + f.hh[:8] + "@roles.example.com"
		var uid string
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (email, name, password_hash, caldav_password_hash) VALUES ($1, $2, 'x', $3) RETURNING id::text`,
			email, l.name, string(hash)).Scan(&uid); err != nil {
			t.Fatalf("insert user %s: %v", l.name, err)
		}
		var vendor any
		if l.name == "vendor-a" {
			vendor = f.vendorA
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO memberships (household_id, user_id, role, vendor_id) VALUES ($1, $2, $3::membership_role, $4)`,
			f.hh, uid, l.role, vendor); err != nil {
			t.Fatalf("insert membership %s: %v", l.name, err)
		}
		f.members[l.name] = email
		f.userIDs[l.name] = uid
	}

	// Maintenance tasks for vendor A and vendor B, and a bill.
	var taskA, taskB, bill string
	for _, tk := range []struct {
		vendor string
		dst    *string
	}{{f.vendorA, &taskA}, {vendorB, &taskB}} {
		if err := pool.QueryRow(ctx,
			`INSERT INTO maintenance_tasks (household_id, name, vendor_id) VALUES ($1, 'job', $2) RETURNING id::text`,
			f.hh, tk.vendor).Scan(tk.dst); err != nil {
			t.Fatalf("insert task: %v", err)
		}
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO bills (household_id, name) VALUES ($1, 'Power') RETURNING id::text`, f.hh).Scan(&bill); err != nil {
		t.Fatalf("insert bill: %v", err)
	}

	// Events, written by the owner through CalDAV (so they have ical_raw),
	// then tagged the way the API/MCP mirrors tag theirs.
	events := []struct {
		cal, uid, eventType, entityType, entityID string
	}{
		{"family", "soccer", "", "", ""},                                  // ordinary family event
		{"family", "fence-job", "maintenance", "maintenance_task", taskA}, // maintenance event on Family (no Maintenance calendar fallback)
		{"bills", "power-bill", "bill", "bill", bill},                     // bill mirror
		{"maint", "job-a", "maintenance", "maintenance_task", taskA},
		{"maint", "job-b", "maintenance", "maintenance_task", taskB},
		{"maint", "quote-call", "", "", ""}, // ad-hoc event on Maintenance
		{"house", "lake-weekend", "", "", ""},
		{"cars", "oil-change", "", "", ""},
	}
	for _, e := range events {
		body := simpleEvent(e.uid, "Event "+e.uid)
		path := "/dav/" + f.cals[e.cal] + "/" + e.uid + ".ics"
		if w := f.do(t, "owner", http.MethodPut, path, body, nil); w.Code != http.StatusCreated {
			t.Fatalf("owner PUT %s = %d: %s", path, w.Code, w.Body)
		}
		f.bodies[e.cal+"/"+e.uid] = body
		if e.eventType != "" {
			if _, err := pool.Exec(ctx,
				`UPDATE calendar_objects SET event_type = $1, entity_type = $2, entity_id = $3::uuid
				  WHERE uid = $4 AND calendar_id = (SELECT id FROM calendars WHERE caldav_uid = $5)`,
				e.eventType, e.entityType, e.entityID, e.uid, f.cals[e.cal]); err != nil {
				t.Fatalf("tag %s: %v", e.uid, err)
			}
		}
	}
	return f
}

// do sends one request as the given login through the full stack.
func (f *roleFixture) do(t *testing.T, login, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "198.51.100.47:5555"
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(f.members[login]+":"+rolePassword)))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	return w
}

// visibility is the policy, written out independently of the code: the
// calendars and events each login must see.
var visibility = map[string][]string{
	"family": {"family/soccer", "family/fence-job", "bills/power-bill", "maint/job-a", "maint/job-b", "maint/quote-call", "house/lake-weekend", "cars/oil-change"},
	// Housesitter: calendar entries yes; money (bills), maintenance and
	// vehicles no — including a maintenance event that sits on Family.
	"sitter": {"family/soccer", "house/lake-weekend"},
	// Vendor without a link: maintenance task events only (not the ad-hoc
	// event on the Maintenance calendar), and only on Maintenance.
	"vendor": {"maint/job-a", "maint/job-b"},
	// Vendor linked to vendor A: only vendor A's jobs.
	"vendor-a": {"maint/job-a"},
}

var visibleCalendars = map[string][]string{
	"family":   {"family", "bills", "maint", "house", "cars"},
	"sitter":   {"family", "house"},
	"vendor":   {"maint"},
	"vendor-a": {"maint"},
}

// policyKey maps a login to its visibility row: every family role and the
// viewer see everything.
func policyKey(login string) string {
	switch login {
	case "owner", "manager", "member", "viewer":
		return "family"
	}
	return login
}

func isWriter(login string) bool {
	switch login {
	case "owner", "manager", "member":
		return true
	}
	return false
}

// TestCalDAVRolePolicyIntegration is the method x role table: PROPFIND,
// REPORT (calendar-query, multiget, sync-collection), GET, PUT, DELETE,
// PROPPATCH and MKCALENDAR for each of the six roles (vendor both with and
// without a vendor link).
func TestCalDAVRolePolicyIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	f := seedRoleFixture(t, repo, pool)

	for _, l := range logins {
		login := l.name
		t.Run(login, func(t *testing.T) {
			key := policyKey(login)
			wantEvents := map[string]bool{}
			for _, e := range visibility[key] {
				wantEvents[e] = true
			}
			wantCals := map[string]bool{}
			for _, c := range visibleCalendars[key] {
				wantCals[c] = true
			}
			calHref := func(c string) string { return "/dav/" + f.cals[c] + "/" }
			evHref := func(ce string) string {
				c, uid, _ := strings.Cut(ce, "/")
				return calHref(c) + uid + ".ics"
			}
			wantEventHrefs := func(c string) []string {
				var out []string
				for e := range wantEvents {
					if strings.HasPrefix(e, c+"/") {
						out = append(out, evHref(e))
					}
				}
				return sortedCopy(out)
			}

			// PROPFIND discovery: hidden calendars are absent, not listed
			// with 403.
			for _, p := range []string{"/dav/", "/dav/calendars/"} {
				w := f.do(t, login, "PROPFIND", p, "", map[string]string{"Depth": "1"})
				if w.Code != http.StatusMultiStatus {
					t.Fatalf("PROPFIND %s = %d: %s", p, w.Code, w.Body)
				}
				for _, c := range calNames {
					listed := strings.Contains(w.Body.String(), "<D:href>"+calHref(c.name)+"</D:href>")
					if listed != wantCals[c.name] {
						t.Errorf("PROPFIND %s lists %s = %v, want %v", p, c.name, listed, wantCals[c.name])
					}
				}
				// A status, not the three digits anywhere: a household UUID in
				// the body contains them about one run in a few hundred, which
				// made this flaky. No 403 element and no status line 403.
				if body := w.Body.String(); strings.Contains(body, "403 Forbidden") ||
					strings.Contains(body, "<D:status>HTTP/1.1 403") {
					t.Errorf("PROPFIND %s answers 403 for something it lists:\n%s", p, w.Body)
				}
			}

			for _, c := range calNames {
				cal := c.name
				path := calHref(cal)

				// PROPFIND Depth 1 on the collection.
				w := f.do(t, login, "PROPFIND", path, "", map[string]string{"Depth": "1"})
				if !wantCals[cal] {
					if w.Code != http.StatusNotFound {
						t.Errorf("PROPFIND hidden %s = %d, want 404", cal, w.Code)
					}
				} else {
					if w.Code != http.StatusMultiStatus {
						t.Fatalf("PROPFIND %s = %d: %s", cal, w.Code, w.Body)
					}
					var events []string
					for _, h := range okHrefs(t, w.Body.String()) {
						if strings.HasSuffix(h, ".ics") {
							events = append(events, h)
						}
					}
					if want := wantEventHrefs(cal); !equalStrings(events, want) {
						t.Errorf("PROPFIND %s events = %v, want %v", cal, events, want)
					}
					hasWrite := strings.Contains(w.Body.String(), "<D:write/>")
					if hasWrite != isWriter(login) {
						t.Errorf("PROPFIND %s privilege set has write = %v, want %v", cal, hasWrite, isWriter(login))
					}
				}

				// REPORT calendar-query, sync-collection, multiget.
				reports := map[string]string{
					"calendar-query": `<?xml version="1.0" encoding="utf-8"?>` +
						`<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
						`<D:prop><D:getetag/><C:calendar-data/></D:prop>` +
						`<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">` +
						`<C:time-range start="20260101T000000Z" end="20280101T000000Z"/>` +
						`</C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`,
					"sync-collection": `<?xml version="1.0" encoding="utf-8"?>` +
						`<D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
						`<D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/><C:calendar-data/></D:prop></D:sync-collection>`,
				}
				var all strings.Builder
				for _, e := range visibility["family"] {
					if strings.HasPrefix(e, cal+"/") {
						all.WriteString("<D:href>" + evHref(e) + "</D:href>")
					}
				}
				reports["calendar-multiget"] = `<?xml version="1.0" encoding="utf-8"?>` +
					`<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
					`<D:prop><D:getetag/><C:calendar-data/></D:prop>` + all.String() + `</C:calendar-multiget>`
				for name, body := range reports {
					w := f.do(t, login, "REPORT", path, body, map[string]string{"Depth": "1"})
					if !wantCals[cal] {
						if w.Code != http.StatusNotFound {
							t.Errorf("REPORT %s on hidden %s = %d, want 404", name, cal, w.Code)
						}
						continue
					}
					if w.Code != http.StatusMultiStatus {
						t.Fatalf("REPORT %s on %s = %d: %s", name, cal, w.Code, w.Body)
					}
					if got, want := okHrefs(t, w.Body.String()), wantEventHrefs(cal); !equalStrings(got, want) {
						t.Errorf("REPORT %s on %s = %v, want %v", name, cal, got, want)
					}
				}
			}

			// GET every event: visible ones byte for byte (phones keep
			// syncing), hidden ones 404.
			for _, e := range visibility["family"] {
				w := f.do(t, login, http.MethodGet, evHref(e), "", nil)
				if !wantEvents[e] {
					if w.Code != http.StatusNotFound {
						t.Errorf("GET hidden %s = %d, want 404", e, w.Code)
					}
					continue
				}
				if w.Code != http.StatusOK {
					t.Errorf("GET %s = %d: %s", e, w.Code, w.Body)
					continue
				}
				if w.Body.String() != f.bodies[e] {
					t.Errorf("GET %s not byte-identical to the stored PUT body:\n%q\nwant\n%q", e, w.Body.String(), f.bodies[e])
				}
			}

			// Writes. Writers can PUT a new event, PROPPATCH and DELETE it;
			// read-only roles get 403 everywhere, visible calendar or not,
			// and nothing changes.
			newUID := "new-" + login
			newPath := calHref("family") + newUID + ".ics"
			hiddenPath := calHref("bills") + newUID + ".ics"
			proppatch := `<?xml version="1.0" encoding="utf-8"?>` +
				`<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>family</D:displayname></D:prop></D:set></D:propertyupdate>`
			if isWriter(login) {
				if w := f.do(t, login, http.MethodPut, newPath, simpleEvent(newUID, "new"), nil); w.Code != http.StatusCreated {
					t.Errorf("PUT = %d, want 201: %s", w.Code, w.Body)
				}
				if w := f.do(t, login, "PROPPATCH", calHref("family"), proppatch, nil); w.Code != http.StatusMultiStatus {
					t.Errorf("PROPPATCH = %d, want 207: %s", w.Code, w.Body)
				}
				if w := f.do(t, login, http.MethodDelete, newPath, "", nil); w.Code != http.StatusNoContent {
					t.Errorf("DELETE = %d, want 204: %s", w.Code, w.Body)
				}
				if w := f.do(t, login, "MKCALENDAR", "/dav/"+newUID+"/", "", nil); w.Code != http.StatusNotImplemented {
					t.Errorf("MKCALENDAR = %d, want 501", w.Code)
				}
				return
			}
			for _, req := range []struct{ method, path, body string }{
				{http.MethodPut, newPath, simpleEvent(newUID, "new")},
				{http.MethodPut, hiddenPath, simpleEvent(newUID, "new")},
				{http.MethodPut, evHref("maint/job-a"), simpleEvent("job-a", "vendor edit")},
				{http.MethodDelete, evHref("family/soccer"), ""},
				{http.MethodDelete, evHref("maint/job-a"), ""},
				{http.MethodDelete, evHref("bills/power-bill"), ""},
				{"PROPPATCH", calHref("family"), proppatch},
				{"PROPPATCH", calHref("maint"), proppatch},
				{"MKCALENDAR", "/dav/" + newUID + "/", ""},
			} {
				w := f.do(t, login, req.method, req.path, req.body, nil)
				if w.Code != http.StatusForbidden {
					t.Errorf("%s %s = %d, want 403: %s", req.method, req.path, w.Code, w.Body)
					continue
				}
				if !strings.Contains(w.Body.String(), "<D:need-privileges/>") {
					t.Errorf("%s %s 403 body has no need-privileges: %s", req.method, req.path, w.Body)
				}
			}
			// Nothing was written.
			if w := f.do(t, "owner", http.MethodGet, newPath, "", nil); w.Code != http.StatusNotFound {
				t.Errorf("read-only PUT created an event: GET = %d", w.Code)
			}
			for _, e := range []string{"family/soccer", "maint/job-a", "bills/power-bill"} {
				w := f.do(t, "owner", http.MethodGet, evHref(e), "", nil)
				if w.Code != http.StatusOK || w.Body.String() != f.bodies[e] {
					t.Errorf("read-only write changed %s: GET = %d", e, w.Code)
				}
			}
		})
	}
}

// TestCalDAVRoleChangesApplyNextRequest checks the role and the vendor link
// are read per request: a demotion, a promotion and a vendor deletion take
// effect on the next request with the same app password.
func TestCalDAVRoleChangesApplyNextRequest(t *testing.T) {
	repo, pool := testRepo(t)
	f := seedRoleFixture(t, repo, pool)
	ctx := context.Background()
	famCal := "/dav/" + f.cals["family"] + "/"
	maintCal := "/dav/" + f.cals["maint"] + "/"
	setRole := func(login, role string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE memberships SET role = $1::membership_role WHERE user_id = $2`, role, f.userIDs[login]); err != nil {
			t.Fatal(err)
		}
	}
	events := func(login, cal string) []string {
		t.Helper()
		w := f.do(t, login, "PROPFIND", cal, "", map[string]string{"Depth": "1"})
		if w.Code != http.StatusMultiStatus {
			return []string{w.Result().Status}
		}
		var out []string
		for _, h := range okHrefs(t, w.Body.String()) {
			if strings.HasSuffix(h, ".ics") {
				out = append(out, strings.TrimSuffix(h[strings.LastIndex(h, "/")+1:], ".ics"))
			}
		}
		return out
	}

	// Demote a family member to housesitter: writes stop, Bills disappears.
	if w := f.do(t, "member", http.MethodPut, famCal+"before.ics", simpleEvent("before", "x"), nil); w.Code != http.StatusCreated {
		t.Fatalf("member PUT = %d", w.Code)
	}
	setRole("member", "housesitter")
	if w := f.do(t, "member", http.MethodPut, famCal+"after.ics", simpleEvent("after", "x"), nil); w.Code != http.StatusForbidden {
		t.Errorf("demoted member PUT = %d, want 403", w.Code)
	}
	if w := f.do(t, "member", "PROPFIND", "/dav/"+f.cals["bills"]+"/", "", map[string]string{"Depth": "0"}); w.Code != http.StatusNotFound {
		t.Errorf("demoted member PROPFIND Bills = %d, want 404", w.Code)
	}

	// Promote the linked vendor: everything is visible and writable.
	setRole("vendor-a", "family_member")
	if got := events("vendor-a", maintCal); !equalStrings(sortedCopy(got), []string{"job-a", "job-b", "quote-call"}) {
		t.Errorf("promoted vendor sees %v on Maintenance", got)
	}
	setRole("vendor-a", "vendor")
	if got := events("vendor-a", maintCal); !equalStrings(got, []string{"job-a"}) {
		t.Errorf("linked vendor sees %v, want [job-a]", got)
	}

	// Deleting vendor A clears the link (ON DELETE SET NULL (vendor_id)):
	// the vendor falls back to every maintenance job.
	if _, err := pool.Exec(ctx, `UPDATE maintenance_tasks SET vendor_id = NULL WHERE vendor_id = $1`, f.vendorA); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, f.vendorA); err != nil {
		t.Fatalf("delete vendor: %v", err)
	}
	var link *string
	if err := pool.QueryRow(ctx, `SELECT vendor_id::text FROM memberships WHERE user_id = $1`, f.userIDs["vendor-a"]).Scan(&link); err != nil || link != nil {
		t.Fatalf("vendor link after vendor delete = %v, %v; want NULL", link, err)
	}
	if got := events("vendor-a", maintCal); !equalStrings(sortedCopy(got), []string{"job-a", "job-b"}) {
		t.Errorf("unlinked vendor sees %v, want [job-a job-b]", got)
	}
}

// TestMembershipVendorLinkSameFamily checks the 064 FK: a membership can only
// name a vendor of its own family.
func TestMembershipVendorLinkSameFamily(t *testing.T) {
	repo, pool := testRepo(t)
	a := seedRoleFixture(t, repo, pool)
	b := seedRoleFixture(t, repo, pool)
	_, err := pool.Exec(context.Background(),
		`UPDATE memberships SET vendor_id = $1 WHERE user_id = $2`, a.vendorA, b.userIDs["vendor"])
	if err == nil || !strings.Contains(err.Error(), "memberships_vendor_household_fkey") {
		t.Fatalf("cross-family vendor link: err = %v, want FK violation", err)
	}
}
