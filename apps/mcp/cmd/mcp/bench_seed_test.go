package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// benchDisposablePrefix names the extra rows the seed adds for the
// *_delete tools to consume, one per measured call.
const benchDisposablePrefix = "Disposable"

var placeholderRE = regexp.MustCompile(`\$(\d+)`)

// seedBusyFamily fills household hh (owned by user owner) with a year of a
// busy family's life, centred on today: ~2 one-off events a day, six weekly
// activities materialised for 52 weeks plus a few RRULE series, 2 properties
// with rooms, 150 assets, 3 vehicles, 3 pets, 60 contacts, 300 bills, 400
// maintenance tasks, 500 notes, 400 documents with extracted text, 300
// notifications, loans, budget categories, lists, recipes, guides and 1000
// activity-log entries. disposable extra rows of each deletable kind are
// named "Disposable <n>" for the *_delete tools.
//
// Everything is generated with generate_series, so seeding a household takes
// well under a second.
func seedBusyFamily(t testing.TB, pool *pgxpool.Pool, hh, owner uuid.UUID, disposable int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// $1 household, $2 owner user; %[1]d is the disposable count.
	stmts := []string{
		`UPDATE households SET timezone = 'America/Chicago' WHERE id = $1`,

		// Three more members with logins, plus two kids without.
		`WITH u AS (
			INSERT INTO users (email, name, password_hash)
			SELECT 'member-' || g || '-' || $1::uuid::text || '@bench.homeos.local', 'Member ' || g, 'x'
			  FROM generate_series(1, 3) g RETURNING id
		) INSERT INTO memberships (household_id, user_id, role) SELECT $1, id, 'family_member' FROM u`,
		// (A trigger adds a people row for every membership.)
		`INSERT INTO people (household_id, display_name, relationship, birthdate, school, grade, allergies)
		 VALUES ($1, 'Nora', 'child', '2015-04-02', 'Maple Elementary', '5', '{peanuts}'),
		        ($1, 'Eli',  'child', '2018-09-14', 'Maple Elementary', '2', '{}')`,

		// Calendars.
		`INSERT INTO calendars (household_id, name, type, color, caldav_uid)
		 SELECT $1, n, t::calendar_type, '#336699', gen_random_uuid()::text
		   FROM (VALUES ('Family','family'), ('Kids','family'), ('Bills','bills'), ('Maintenance','maintenance')) v(n, t)`,

		// ~2 one-off events a day for a year (half a year back, half ahead).
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		 SELECT (SELECT id FROM calendars WHERE household_id = $1 AND name = CASE WHEN g % 3 = 0 THEN 'Kids' ELSE 'Family' END),
		        gen_random_uuid()::text,
		        json_build_object(
		            'title', (ARRAY['Dentist','Soccer game','Birthday party','Parent-teacher conference','Dinner with the Hendersons','Vet visit','Piano recital','School play','Haircut','Date night'])[1 + g % 10] || ' ' || g,
		            'start', to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		            'end',   to_char((ts + interval '90 minutes') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		            'all_day', false,
		            'location', 'Location ' || (g % 40),
		            'description', 'Bring the forms. Reminder ' || g)::text,
		        'etag-0', 'event'
		   FROM generate_series(1, 730) g,
		        LATERAL (SELECT date_trunc('hour', now()) - interval '182 days' + g * interval '12 hours' + (g % 5) * interval '1 hour' AS ts) x`,

		// Six weekly activities materialised for 52 weeks.
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		 SELECT (SELECT id FROM calendars WHERE household_id = $1 AND name = 'Kids'),
		        gen_random_uuid()::text,
		        json_build_object(
		            'title', a.title,
		            'start', to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		            'end',   to_char((ts + interval '1 hour') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		            'all_day', false, 'location', a.loc)::text,
		        'etag-0', 'event'
		   FROM (VALUES ('Swim practice', 'YMCA', 1, 17), ('Piano lesson', 'Music school', 2, 16),
		                ('Soccer practice', 'Field 3', 3, 18), ('Scouts', 'Church hall', 4, 19),
		                ('Art class', 'Rec center', 5, 16), ('Family movie night', 'Home', 6, 19)) a(title, loc, dow, hr),
		        generate_series(0, 51) w,
		        LATERAL (SELECT date_trunc('week', now()) - interval '26 weeks' + w * interval '1 week'
		                        + a.dow * interval '1 day' + a.hr * interval '1 hour' AS ts) x`,

		// A few true RRULE series, weekly and daily, some started months
		// ago: range reads expand them into occurrences (#4483).
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, rrule)
		 SELECT (SELECT id FROM calendars WHERE household_id = $1 AND name = 'Family'), gen_random_uuid()::text,
		        json_build_object('title', t, 'start', to_char((date_trunc('week', now()) - ago + interval '8 hours') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		            'end', to_char((date_trunc('week', now()) - ago + interval '9 hours') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), 'all_day', false)::text,
		        'etag-0', 'event', r
		   FROM (VALUES ('Trash day', 'FREQ=WEEKLY;BYDAY=TU', interval '0'), ('Church', 'FREQ=WEEKLY;BYDAY=SU', interval '0'),
		                ('Grocery run', 'FREQ=WEEKLY;BYDAY=SA', interval '0'), ('Allowance', 'FREQ=WEEKLY;BYDAY=FR', interval '0'),
		                ('Medication', 'FREQ=DAILY', interval '26 weeks'), ('Dog walk', 'FREQ=DAILY', interval '0'),
		                ('School run', 'FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR', interval '10 weeks')) v(t, r, ago)`,

		// Properties and rooms.
		`INSERT INTO properties (household_id, name, address, property_type, purchase_price, current_value)
		 VALUES ($1, 'Home', '12 Oak St', 'house', 350000, 420000), ($1, 'Lake cabin', '3 Shore Rd', 'cabin', 150000, 180000)`,
		`INSERT INTO rooms (property_id, household_id, name, floor)
		 SELECT p.id, $1, r || CASE WHEN p.name = 'Lake cabin' THEN ' (cabin)' ELSE '' END, 1
		   FROM properties p, unnest(ARRAY['Kitchen','Living room','Garage','Primary bedroom','Kids room','Basement']) r
		  WHERE p.household_id = $1`,

		// Contacts (vendors).
		`INSERT INTO vendors (household_id, name, specialty, phone, email, trades)
		 SELECT $1, (ARRAY['Ace','Best','City','Delta','Eagle','Fox'])[1 + g % 6] || ' ' || t || ' ' || g, t, '555-01' || lpad(g::text, 2, '0'),
		        'contact' || g || '@example.com', ARRAY[lower(t)]
		   FROM generate_series(1, 60) g,
		        LATERAL (SELECT (ARRAY['Plumbing','Electrical','HVAC','Roofing','Landscaping','Pest control'])[1 + g % 6] AS t) x`,

		// Assets spread over both properties' rooms.
		`INSERT INTO assets (household_id, property_id, room_id, name, category, manufacturer, model, serial_number,
		                     purchase_date, purchase_price, warranty_expiry, current_value)
		 SELECT $1, r.property_id, r.id,
		        (ARRAY['Refrigerator','Dishwasher','Furnace','Water heater','TV','Washer','Dryer','Lawn mower','Laptop','Sofa'])[1 + g % 10] || ' ' || g,
		        (ARRAY['appliance','appliance','hvac','plumbing','electronics','appliance','appliance','outdoor','electronics','furniture'])[1 + g % 10],
		        'Maker ' || (g % 12), 'M-' || g, 'SN' || g, current_date - g * 7, 100 + g * 13,
		        current_date + (g % 50 - 10) * 30, 50 + g * 7
		   FROM generate_series(1, 150) g,
		        LATERAL (SELECT id, property_id FROM rooms WHERE household_id = $1 ORDER BY name OFFSET g % 12 LIMIT 1) r`,

		// Vehicles and pets.
		`INSERT INTO vehicles (household_id, year, make, model, vin, license_plate, current_value)
		 VALUES ($1, 2019, 'Honda', 'Odyssey', 'VIN1' || $1::uuid::text, 'ABC123', 18000),
		        ($1, 2022, 'Toyota', 'RAV4', 'VIN2' || $1::uuid::text, 'XYZ789', 26000),
		        ($1, 2008, 'Ford', 'F-150', 'VIN3' || $1::uuid::text, 'OLD111', 4000)`,
		`INSERT INTO pets (household_id, name, species, breed)
		 VALUES ($1, 'Rex', 'dog', 'Labrador'), ($1, 'Milo', 'cat', 'Tabby'), ($1, 'Bubbles', 'fish', 'Goldfish')`,

		// Budget categories.
		`INSERT INTO budget_categories (household_id, name, monthly_amount)
		 SELECT $1, c, 100 + 50 * row_number() OVER ()
		   FROM unnest(ARRAY['Housing','Utilities','Groceries','Transport','Insurance','Kids','Entertainment','Health','Pets','Savings']) c`,

		// Bills: monthly with due days, some paid, some autopay, plus non-monthly.
		`INSERT INTO bills (household_id, name, amount, due_day, category, is_autopay, paid_date, paid_for_due_date, vendor_id, budget_category_id)
		 SELECT $1, (ARRAY['Electric','Water','Internet','Phone','Streaming','Insurance','Gym','Trash','Gas','Daycare'])[1 + g % 10] || ' ' || g,
		        20 + g, 1 + g % 28,
		        (ARRAY['utilities','utilities','utilities','utilities','entertainment','insurance','health','utilities','utilities','kids'])[1 + g % 10],
		        g % 4 = 0,
		        CASE WHEN g % 3 = 0 THEN current_date - (g % 20) END,
		        CASE WHEN g % 3 = 0 THEN current_date - (g % 20) END,
		        (SELECT id FROM vendors WHERE household_id = $1 ORDER BY name OFFSET g % 60 LIMIT 1),
		        (SELECT id FROM budget_categories WHERE household_id = $1 ORDER BY name OFFSET g % 10 LIMIT 1)
		   FROM generate_series(1, 270) g`,
		`INSERT INTO bills (household_id, name, amount, frequency, due_date, category)
		 SELECT $1, 'Annual ' || g, 500 + g, (ARRAY['quarterly','semiannual','annual','once'])[1 + g % 4], current_date + (g * 11 % 365) - 100, 'insurance'
		   FROM generate_series(1, 30) g`,

		// Loans.
		`INSERT INTO loans (household_id, name, lender, original_amount, remaining_balance, interest_rate, term_months, monthly_payment, start_date, due_day, property_id)
		 SELECT $1, 'Mortgage', 'Big Bank', 280000, 240000, 6.1, 360, 1700, '2020-06-01', 1, id FROM properties WHERE household_id = $1 AND name = 'Home'`,
		`INSERT INTO loans (household_id, name, lender, original_amount, remaining_balance, interest_rate, term_months, monthly_payment, start_date, due_day, vehicle_id)
		 SELECT $1, 'Car loan ' || model, 'Credit Union', 30000, 12000, 4.2, 60, 550, '2022-03-01', 15, id FROM vehicles WHERE household_id = $1 AND year > 2010`,
		`INSERT INTO loans (household_id, name, lender, original_amount, remaining_balance, due_day)
		 VALUES ($1, 'Student loan', 'Servicer', 40000, 9000, 20), ($1, 'HELOC', 'Big Bank', 50000, 20000, 5)`,

		// Maintenance: overdue, upcoming, done; on properties, assets and vehicles.
		`INSERT INTO maintenance_tasks (household_id, name, description, status, due_date, completed_at, cost, property_id, asset_id, vehicle_id, vendor_id)
		 SELECT $1, (ARRAY['Replace filter','Clean gutters','Oil change','Service furnace','Test smoke alarms','Flush water heater','Rotate tires','Seal deck'])[1 + g % 8] || ' ' || g,
		        'Routine upkeep ' || g,
		        (CASE WHEN g % 3 = 0 THEN 'done' ELSE 'pending' END)::task_status,
		        current_date + (g % 240 - 120),
		        CASE WHEN g % 3 = 0 THEN now() - (g % 120) * interval '1 day' END,
		        CASE WHEN g % 3 = 0 THEN 40 + g END,
		        CASE WHEN g % 4 = 0 THEN (SELECT id FROM properties WHERE household_id = $1 ORDER BY name OFFSET g % 2 LIMIT 1) END,
		        CASE WHEN g % 4 = 1 THEN (SELECT id FROM assets WHERE household_id = $1 ORDER BY name OFFSET g % 150 LIMIT 1) END,
		        CASE WHEN g % 4 = 2 THEN (SELECT id FROM vehicles WHERE household_id = $1 ORDER BY year OFFSET g % 3 LIMIT 1) END,
		        (SELECT id FROM vendors WHERE household_id = $1 ORDER BY name OFFSET g % 60 LIMIT 1)
		   FROM generate_series(1, 400) g`,

		// Notes: family-wide, and about assets, vehicles, pets and people.
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body, author_id)
		 SELECT $1, 'household', $1, 'Family note ' || g,
		        'Remember to ' || (ARRAY['call grandma','renew passports','order school supplies','book the cabin','check the smoke alarms'])[1 + g % 5]
		        || '. Details: ' || repeat('lorem ipsum dolor sit amet ', 1 + g % 12), $2
		   FROM generate_series(1, 300) g`,
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body, author_id)
		 SELECT $1, 'asset', a.id, 'Asset note ' || g, 'Serviced, works fine. ' || repeat('notes ', g % 20), $2
		   FROM generate_series(1, 120) g, LATERAL (SELECT id FROM assets WHERE household_id = $1 ORDER BY name OFFSET g % 150 LIMIT 1) a`,
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body, author_id)
		 SELECT $1, 'vehicle', v.id, 'Vehicle note ' || g, 'Odometer and service notes ' || g, $2
		   FROM generate_series(1, 40) g, LATERAL (SELECT id FROM vehicles WHERE household_id = $1 ORDER BY year OFFSET g % 3 LIMIT 1) v`,
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body, author_id)
		 SELECT $1, 'pet', p.id, 'Pet note ' || g, 'Vet said all good ' || g, $2
		   FROM generate_series(1, 20) g, LATERAL (SELECT id FROM pets WHERE household_id = $1 ORDER BY name OFFSET g % 3 LIMIT 1) p`,
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body, author_id)
		 SELECT $1, 'person', p.id, 'Person note ' || g, 'Shoe size, teacher, allergies ' || g, $2
		   FROM generate_series(1, 20) g, LATERAL (SELECT id FROM people WHERE household_id = $1 ORDER BY display_name OFFSET g % 6 LIMIT 1) p`,

		// Documents: metadata plus extracted text, 1 small blob each.
		`WITH b AS (
			INSERT INTO file_blobs (data, size) SELECT convert_to('doc ' || g, 'UTF8'), 8 FROM generate_series(1, 400) g RETURNING id
		), nb AS (SELECT id, (row_number() OVER ())::int AS g FROM b)
		INSERT INTO files (household_id, blob_id, name, content_type, size, entity_type, entity_id, extracted_text, ocr_status,
		                   tags, title, category, document_date, expires_on, description, person_id, created_by)
		SELECT $1, nb.id, 'scan-' || g || '.pdf', 'application/pdf', 8 + g * 1000, 'household', $1,
		       'Policy number ' || g || ' ' || repeat('This agreement covers the insured property and its contents. ', 1 + g % 30),
		       'done', ARRAY[(ARRAY['tax','insurance','school','medical','warranty','receipt'])[1 + g % 6], 'y' || (2020 + g % 6)],
		       (ARRAY['Tax return','Insurance policy','Report card','Lab results','Warranty card','Receipt'])[1 + g % 6] || ' ' || g,
		       (ARRAY['tax','insurance','school','medical','warranty','receipt'])[1 + g % 6],
		       current_date - g * 3, CASE WHEN g % 5 = 0 THEN current_date + g END, 'Scanned document ' || g,
		       CASE WHEN g % 4 = 0 THEN (SELECT id FROM people WHERE household_id = $1 ORDER BY display_name OFFSET g % 6 LIMIT 1) END, $2
		  FROM nb`,

		// Notifications: a third unread.
		`INSERT INTO notifications (household_id, user_id, type, title, body, read_at, created_at)
		 SELECT $1, CASE WHEN g % 2 = 0 THEN $2::uuid END, 'reminder', 'Reminder ' || g, 'Something needs attention ' || g,
		        CASE WHEN g % 3 <> 0 THEN now() - g * interval '1 hour' END, now() - g * interval '3 hours'
		   FROM generate_series(1, 300) g`,

		// Lists, recipes, guides for search_all.
		`INSERT INTO lists (household_id, name, kind, entity_type, entity_id, is_template, archived)
		 SELECT $1, 'List ' || g, 'todo', 'household', $1, false, false FROM generate_series(1, 15) g`,
		`INSERT INTO list_items (list_id, household_id, text, checked, position)
		 SELECT l.id, $1, 'Item ' || g, g % 2 = 0, g FROM lists l, generate_series(1, 12) g WHERE l.household_id = $1`,
		// The grocery list the list_* tools work on (#4473): 40 items, a third checked.
		`INSERT INTO lists (household_id, name, kind, entity_type, entity_id) VALUES ($1, 'Grocery', 'grocery', 'household', $1)`,
		`INSERT INTO list_items (list_id, household_id, text, section, checked, position)
		 SELECT l.id, $1, 'Grocery item ' || g, 'Aisle ' || (g % 8), g % 3 = 0, g
		   FROM lists l, generate_series(1, 40) g WHERE l.household_id = $1 AND l.name = 'Grocery'`,
		`INSERT INTO recipes (household_id, title, description, ingredients, tags)
		 SELECT $1, 'Recipe ' || g, 'Weeknight dinner ' || g, '[]'::jsonb, ARRAY['dinner'] FROM generate_series(1, 40) g`,
		`INSERT INTO guides (household_id, title, summary, body, tags)
		 SELECT $1, 'How to ' || g, 'Guide ' || g, repeat('Step. ', 20), ARRAY['howto'] FROM generate_series(1, 20) g`,

		// Activity log.
		`INSERT INTO activity_log (household_id, entity_type, entity_id, action, summary, created_at)
		 SELECT $1, 'bill', $1, 'update', 'Changed bill ' || g, now() - g * interval '2 hours' FROM generate_series(1, 1000) g`,

		// Disposable rows for the *_delete tools.
		`INSERT INTO assets (household_id, name, category) SELECT $1, '` + benchDisposablePrefix + ` asset ' || g, 'other' FROM generate_series(1, %[1]d) g`,
		`INSERT INTO bills (household_id, name, amount, due_day) SELECT $1, '` + benchDisposablePrefix + ` bill ' || g, 1, 1 FROM generate_series(1, %[1]d) g`,
		`INSERT INTO loans (household_id, name, original_amount, remaining_balance) SELECT $1, '` + benchDisposablePrefix + ` loan ' || g, 1, 1 FROM generate_series(1, %[1]d) g`,
		`INSERT INTO maintenance_tasks (household_id, name, due_date) SELECT $1, '` + benchDisposablePrefix + ` task ' || g, current_date + 400 FROM generate_series(1, %[1]d) g`,
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body) SELECT $1, 'household', $1, '` + benchDisposablePrefix + ` note ' || g, 'x' FROM generate_series(1, %[1]d) g`,
		// Pets with nothing attached, for pet_delete.
		`INSERT INTO pets (household_id, name, species) SELECT $1, '` + benchDisposablePrefix + ` pet ' || g, 'fish' FROM generate_series(1, %[1]d) g`,
		// Vehicles with nothing attached, for vehicle_delete.
		`INSERT INTO vehicles (household_id, make, model) SELECT $1, 'Junker', '` + benchDisposablePrefix + ` car ' || g FROM generate_series(1, %[1]d) g`,
		// People without a login and with nothing attached, for person_delete.
		`INSERT INTO people (household_id, display_name, relationship) SELECT $1, '` + benchDisposablePrefix + ` person ' || g, 'other' FROM generate_series(1, %[1]d) g`,
		// Properties with nothing attached, for property_delete, and empty
		// rooms in a spare property, for room_delete.
		`INSERT INTO properties (household_id, name) SELECT $1, '` + benchDisposablePrefix + ` property ' || g FROM generate_series(1, %[1]d) g`,
		`INSERT INTO properties (household_id, name) VALUES ($1, 'Spare rooms')`,
		`INSERT INTO rooms (property_id, household_id, name)
		 SELECT p.id, $1, '` + benchDisposablePrefix + ` room ' || g
		   FROM properties p, generate_series(1, %[1]d) g WHERE p.household_id = $1 AND p.name = 'Spare rooms'`,
		`INSERT INTO list_items (list_id, household_id, text, position)
		 SELECT l.id, $1, '` + benchDisposablePrefix + ` list item ' || g, 1000 + g
		   FROM lists l, generate_series(1, %[1]d) g WHERE l.household_id = $1 AND l.name = 'Grocery'`,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		 SELECT (SELECT id FROM calendars WHERE household_id = $1 AND name = 'Family'), gen_random_uuid()::text,
		        json_build_object('title', '` + benchDisposablePrefix + ` event ' || g,
		            'start', to_char((now() + interval '400 days') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		            'end', to_char((now() + interval '400 days 1 hour') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), 'all_day', false)::text,
		        'etag-0', 'event'
		   FROM generate_series(1, %[1]d) g`,
	}
	args := []any{hh, owner}
	for i, sql := range stmts {
		if strings.Contains(sql, "%[1]d") {
			sql = fmt.Sprintf(sql, disposable)
		}
		// Pass only the parameters the statement references ($1..$n).
		n := 0
		for _, m := range placeholderRE.FindAllStringSubmatch(sql, -1) {
			if k, _ := strconv.Atoi(m[1]); k > n {
				n = k
			}
		}
		if _, err := pool.Exec(ctx, sql, args[:n]...); err != nil {
			t.Fatalf("seed statement %d: %v\n%s", i, err, sql)
		}
	}
}

// benchFixtures are ids from the measured household that tool arguments use.
type benchFixtures struct {
	Asset, Bill, Event, Note, Document, Loan, Task, Property, Vehicle, Calendar, Person, Pet string
	List, ListItem, Credential                                                               string

	// Rows each measured call of a write tool consumes, in order.
	DisposableAssets, DisposableBills, DisposableLoans, DisposableTasks, DisposableNotes, DisposableEvents []string
	DisposableListItems, DisposablePeople, DisposablePets, DisposableProperties, DisposableRooms           []string
	DisposableVehicles                                                                                     []string
	PayableBills, PendingTasks, UnreadNotifications                                                        []string
}

func loadFixtures(t testing.TB, pool *pgxpool.Pool, hh uuid.UUID) benchFixtures {
	t.Helper()
	ctx := context.Background()
	one := func(sql string) string {
		var id string
		if err := pool.QueryRow(ctx, sql, hh).Scan(&id); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
		return id
	}
	many := func(sql string) []string {
		rows, err := pool.Query(ctx, sql, hh)
		if err != nil {
			t.Fatalf("fixtures %q: %v", sql, err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("fixtures scan: %v", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("fixtures %q: %v", sql, err)
		}
		return ids
	}
	disp := func(table, col string) []string {
		return many(fmt.Sprintf(`SELECT id::text FROM %s WHERE household_id = $1 AND %s LIKE '%s %%' ORDER BY created_at, id`, table, col, benchDisposablePrefix))
	}
	f := benchFixtures{
		Asset:    one(`SELECT id::text FROM assets WHERE household_id = $1 AND name LIKE 'Furnace%' ORDER BY name LIMIT 1`),
		Bill:     one(`SELECT id::text FROM bills WHERE household_id = $1 AND name LIKE 'Electric%' ORDER BY name LIMIT 1`),
		Note:     one(`SELECT id::text FROM notes WHERE household_id = $1 AND title LIKE 'Family note%' ORDER BY title LIMIT 1`),
		Document: one(`SELECT id::text FROM files WHERE household_id = $1 ORDER BY name LIMIT 1`),
		Loan:     one(`SELECT id::text FROM loans WHERE household_id = $1 AND name = 'Mortgage'`),
		Task:     one(`SELECT id::text FROM maintenance_tasks WHERE household_id = $1 AND status = 'pending' AND name NOT LIKE 'Disposable%' ORDER BY name LIMIT 1`),
		Property: one(`SELECT id::text FROM properties WHERE household_id = $1 AND name = 'Home'`),
		Vehicle:  one(`SELECT id::text FROM vehicles WHERE household_id = $1 AND year IS NOT NULL ORDER BY year DESC LIMIT 1`),
		Calendar: one(`SELECT id::text FROM calendars WHERE household_id = $1 AND name = 'Family'`),
		Person:   one(`SELECT id::text FROM people WHERE household_id = $1 AND display_name = 'Nora'`),
		Pet:      one(`SELECT id::text FROM pets WHERE household_id = $1 AND name = 'Rex'`),
		Event: one(`SELECT co.id::text FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
		             WHERE c.household_id = $1 AND co.ical_data LIKE '%Dentist%' ORDER BY co.id LIMIT 1`),

		List:     one(`SELECT id::text FROM lists WHERE household_id = $1 AND name = 'Grocery'`),
		ListItem: one(`SELECT i.id::text FROM list_items i JOIN lists l ON l.id = i.list_id WHERE l.household_id = $1 AND l.name = 'Grocery' AND i.text = 'Grocery item 1'`),

		DisposableAssets:     disp("assets", "name"),
		DisposableListItems:  disp("list_items", "text"),
		DisposableBills:      disp("bills", "name"),
		DisposableLoans:      disp("loans", "name"),
		DisposableTasks:      disp("maintenance_tasks", "name"),
		DisposableNotes:      disp("notes", "title"),
		DisposablePeople:     disp("people", "display_name"),
		DisposablePets:       disp("pets", "name"),
		DisposableProperties: disp("properties", "name"),
		DisposableRooms:      disp("rooms", "name"),
		DisposableVehicles:   disp("vehicles", "model"),
		DisposableEvents: many(`SELECT co.id::text FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
		                         WHERE c.household_id = $1 AND co.ical_data LIKE '%` + benchDisposablePrefix + ` event%' ORDER BY co.id`),
		PayableBills: many(`SELECT id::text FROM bills WHERE household_id = $1 AND paid_date IS NULL AND frequency = 'monthly'
		                     AND name NOT LIKE 'Disposable%' ORDER BY name`),
		PendingTasks: many(`SELECT id::text FROM maintenance_tasks WHERE household_id = $1 AND status = 'pending'
		                     AND name NOT LIKE 'Disposable%' ORDER BY name OFFSET 1`),
		UnreadNotifications: many(`SELECT id::text FROM notifications WHERE household_id = $1 AND read_at IS NULL ORDER BY created_at`),
	}
	return f
}
