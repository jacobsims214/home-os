package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func guardBody(rule string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\n" +
		"UID:evt-guard\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:19700101T000000Z\r\n" +
		"DTEND:19700101T000001Z\r\nSUMMARY:Guard\r\nRRULE:" + rule + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func TestRejectUnsupportedRecurrence(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		reject bool
	}{
		{"secondly", guardBody("FREQ=SECONDLY"), true},
		{"hourly off lattice", guardBody("FREQ=HOURLY;INTERVAL=6;BYHOUR=1"), true},
		{"never matches", guardBody("FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30"), true},
		{"weekly", guardBody("FREQ=WEEKLY;BYDAY=MO"), false},
		{"minutely", guardBody("FREQ=MINUTELY;INTERVAL=15"), false},
		{"not recurring", strings.Replace(guardBody("X"), "RRULE:X\r\n", "", 1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			got := rejectUnsupportedRecurrence(w, []byte(tt.body), "evt-guard")
			if got != tt.reject {
				t.Fatalf("rejected = %v, want %v", got, tt.reject)
			}
			if !got {
				return
			}
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", w.Code)
			}
			if !strings.Contains(w.Body.String(), "<C:valid-calendar-data/>") {
				t.Errorf("body lacks CALDAV:valid-calendar-data: %s", w.Body)
			}
		})
	}
}

// TestRecurringInTimeRangeUnsupportedRule: an unsupported rule already in the
// database (stored before #4656) answers quickly and is kept in time-range
// results rather than dropped.
func TestRecurringInTimeRangeUnsupportedRule(t *testing.T) {
	start := time.Now()
	in, ok := recurringInTimeRange(guardBody("FREQ=SECONDLY"),
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC))
	if !in || !ok {
		t.Fatalf("in, ok = %v, %v; want true, true", in, ok)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("took %v", d)
	}
}

func TestCalDAVPUTRejectsUnsupportedRuleIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	hid, calUID, calID := seedCalendar(t, pool)
	path := "/dav/" + calUID + "/evt-guard.ics"

	w := httptest.NewRecorder()
	HandlePUT(w, davRequest(http.MethodPut, path, hid, guardBody("FREQ=SECONDLY"), nil), repo, path)
	if w.Code != http.StatusForbidden {
		t.Fatalf("PUT status = %d, want 403; body %s", w.Code, w.Body)
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM calendar_objects WHERE calendar_id = $1`, calID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d objects stored, want 0", n)
	}

	w = httptest.NewRecorder()
	HandlePUT(w, davRequest(http.MethodPut, path, hid, guardBody("FREQ=WEEKLY;BYDAY=TH"), nil), repo, path)
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT weekly status = %d, want 201; body %s", w.Code, w.Body)
	}
}
