package caldav

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"home-os/calendar/internal/db"
	"home-os/calendar/internal/ical"
)

// propfindEventResponses splits a PROPFIND multistatus body into its
// D:response elements keyed by href. It works on the raw text rather than an
// XML decoder because encoding/xml normalises CRLF to LF inside CDATA, which
// would hide exactly the byte differences these tests look for.
func propfindEventResponses(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, chunk := range strings.Split(body, "<D:response>")[1:] {
		m := regexp.MustCompile(`<D:href>([^<]*)</D:href>`).FindStringSubmatch(chunk)
		if m == nil {
			t.Fatalf("response without href:\n%s", chunk)
		}
		out[m[1]] = chunk
	}
	return out
}

// calendarDataOf returns the calendar-data text of one D:response, undoing
// the "]]>" CDATA split.
func calendarDataOf(t *testing.T, resp string) string {
	t.Helper()
	const open, close = "<C:calendar-data><![CDATA[", "]]></C:calendar-data>"
	i := strings.Index(resp, open)
	j := strings.LastIndex(resp, close)
	if i < 0 || j < i {
		t.Fatalf("no calendar-data in response:\n%s", resp)
	}
	return strings.ReplaceAll(resp[i+len(open):j], "]]]]><![CDATA[>", "]]>")
}

func contentLengthOf(t *testing.T, resp string) int {
	t.Helper()
	m := regexp.MustCompile(`<D:getcontentlength>(\d+)</D:getcontentlength>`).FindStringSubmatch(resp)
	if m == nil {
		t.Fatalf("no getcontentlength in response:\n%s", resp)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

const propfindEventPropsBody = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/><D:getcontentlength/><C:calendar-data/></D:prop>
</D:propfind>`

// TestPROPFINDCalendarDataMatchesGETIntegration checks that PROPFIND Depth:1
// on a calendar serves, for every event, the exact bytes GET serves, and
// reports their length, for the three ways renderObjectICal picks its text:
// a fresh client PUT (raw), a JSON-only row (generated), and a raw row made
// stale by a JSON-only edit (generated).
func TestPROPFINDCalendarDataMatchesGETIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	ctx := context.Background()
	hid, calUID, calID := seedCalendar(t, pool)

	// 1. Raw: a client PUT.
	rawPath := "/dav/" + calUID + "/evt-raw.ics"
	w := httptest.NewRecorder()
	HandlePUT(w, davRequest(http.MethodPut, rawPath, hid, strings.ReplaceAll(rawFixture, "evt-roundtrip", "evt-raw"), nil), repo, rawPath)
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT raw status = %d: %s", w.Code, w.Body)
	}

	// 2. Generated: a JSON-only row, as the API writes it.
	legacy := ical.CalEvent{
		Title:       "Bin day <recycling> & glass",
		Description: "Ünïcode so bytes != runes",
		Start:       time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC),
		End:         time.Date(2026, 11, 20, 7, 30, 0, 0, time.UTC),
	}
	b, _ := json.Marshal(&legacy)
	if _, err := pool.Exec(ctx,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'evt-json', $2, 'json-etag')`,
		calID, string(b)); err != nil {
		t.Fatal(err)
	}

	// 3. Stale raw: PUT, then a JSON-only edit.
	stalePath := "/dav/" + calUID + "/evt-stale.ics"
	w = httptest.NewRecorder()
	HandlePUT(w, davRequest(http.MethodPut, stalePath, hid, strings.ReplaceAll(rawFixture, "evt-roundtrip", "evt-stale"), nil), repo, stalePath)
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT stale status = %d: %s", w.Code, w.Body)
	}
	var jsonData string
	if err := pool.QueryRow(ctx, `SELECT ical_data FROM calendar_objects WHERE calendar_id = $1 AND uid = 'evt-stale'`, calID).Scan(&jsonData); err != nil {
		t.Fatal(err)
	}
	var ev ical.CalEvent
	if err := json.Unmarshal([]byte(jsonData), &ev); err != nil {
		t.Fatal(err)
	}
	ev.Title = "Edited by the API"
	b, _ = json.Marshal(&ev)
	if _, err := pool.Exec(ctx, `UPDATE calendar_objects SET ical_data = $1, etag = 'api-etag', updated_at = NOW() WHERE calendar_id = $2 AND uid = 'evt-stale'`, string(b), calID); err != nil {
		t.Fatal(err)
	}

	colPath := "/dav/" + calUID + "/"
	w = httptest.NewRecorder()
	HandlePROPFIND(w, davRequest("PROPFIND", colPath, hid, propfindEventPropsBody, map[string]string{"Depth": "1"}), repo, colPath)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status = %d: %s", w.Code, w.Body)
	}
	propfindBody := w.Body.String()
	if err := xml.Unmarshal([]byte(propfindBody), new(struct{})); err != nil {
		t.Fatalf("PROPFIND body is not well-formed XML: %v\n%s", err, propfindBody)
	}
	responses := propfindEventResponses(t, propfindBody)

	for _, uid := range []string{"evt-raw", "evt-json", "evt-stale"} {
		t.Run(uid, func(t *testing.T) {
			path := "/dav/" + calUID + "/" + uid + ".ics"
			gw := httptest.NewRecorder()
			HandleGET(gw, davRequest(http.MethodGet, path, hid, "", nil), repo, path)
			if gw.Code != http.StatusOK {
				t.Fatalf("GET status = %d", gw.Code)
			}
			getBody := gw.Body.String()

			resp, ok := responses[path]
			if !ok {
				t.Fatalf("PROPFIND has no response for %s:\n%s", path, propfindBody)
			}
			if got := calendarDataOf(t, resp); got != getBody {
				t.Errorf("calendar-data differs from GET body:\n got: %q\nwant: %q", got, getBody)
			}
			if got := contentLengthOf(t, resp); got != len(getBody) {
				t.Errorf("getcontentlength = %d, want %d (GET body length)", got, len(getBody))
			}
			if strings.Contains(resp, `"title"`) {
				t.Errorf("PROPFIND still serves JSON:\n%s", resp)
			}
		})
	}

	// The raw case really is the client's text, and the stale case really
	// is regenerated with the edit.
	if got := calendarDataOf(t, responses[rawPath]); got != strings.ReplaceAll(rawFixture, "evt-roundtrip", "evt-raw") {
		t.Errorf("raw event not served verbatim")
	}
	if got := calendarDataOf(t, responses[stalePath]); !strings.Contains(got, "SUMMARY:Edited by the API") {
		t.Errorf("stale raw served instead of the JSON edit:\n%s", got)
	}
}

// TestWriteResponseForEvent covers the render-dependent props without a
// database: CDATA splitting, byte (not rune) length, lazy rendering, and the
// 500 propstat when the stored JSON can't be rendered.
func TestWriteResponseForEvent(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e1\r\n" +
		"DTSTART:20261120T070000Z\r\nDTEND:20261120T073000Z\r\nSUMMARY:Café\r\n" +
		"DESCRIPTION:has ]]> inside\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	good := storedFromPUT(t, "e1", raw)
	want, err := renderObjectICal(good)
	if err != nil {
		t.Fatal(err)
	}
	if want != raw {
		t.Fatalf("fixture not served raw; test premise broken")
	}

	props := func(names ...xml.Name) []xmlElement {
		out := make([]xmlElement, len(names))
		for i, n := range names {
			out[i] = xmlElement{XMLName: n}
		}
		return out
	}
	etag := xml.Name{Space: nsDAV, Local: "getetag"}
	length := xml.Name{Space: nsDAV, Local: "getcontentlength"}
	data := xml.Name{Space: nsC, Local: "calendar-data"}
	unknown := xml.Name{Space: nsDAV, Local: "quota-used-bytes"}

	t.Run("calendar-data and length match render", func(t *testing.T) {
		var sb strings.Builder
		writeResponseForEvent(&sb, "cal", good, props(etag, length, data, unknown))
		out := sb.String()
		if got := calendarDataOf(t, out); got != want {
			t.Errorf("calendar-data = %q, want %q", got, want)
		}
		if got := contentLengthOf(t, out); got != len(want) {
			t.Errorf("getcontentlength = %d, want %d", got, len(want))
		}
		if !strings.Contains(out, "]]]]><![CDATA[>") {
			t.Errorf("]]> not split across CDATA sections:\n%s", out)
		}
		if !strings.Contains(out, "404 Not Found") || strings.Contains(out, "500") {
			t.Errorf("unexpected propstats:\n%s", out)
		}
	})

	t.Run("render failure is a 500 propstat for render props only", func(t *testing.T) {
		bad := &db.CalendarObject{UID: "bad", ETag: "x", ICALData: "not json"}
		var sb strings.Builder
		writeResponseForEvent(&sb, "cal", bad, props(etag, length, data))
		out := sb.String()
		if !strings.Contains(out, `<D:getetag>"x"</D:getetag>`) {
			t.Errorf("getetag missing:\n%s", out)
		}
		if !strings.Contains(out, "HTTP/1.1 500 Internal Server Error") {
			t.Errorf("no 500 propstat:\n%s", out)
		}
		if strings.Contains(out, "CDATA") || strings.Contains(out, "not json") {
			t.Errorf("unrenderable JSON leaked into the response:\n%s", out)
		}
	})

	t.Run("no render needed when not requested", func(t *testing.T) {
		bad := &db.CalendarObject{UID: "bad", ETag: "x", ICALData: "not json"}
		var sb strings.Builder
		writeResponseForEvent(&sb, "cal", bad, props(etag))
		if strings.Contains(sb.String(), "500") {
			t.Errorf("rendered although only getetag was requested:\n%s", sb.String())
		}
	})
}
