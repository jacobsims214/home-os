// Package seed provides demo data seeding for development and demo environments.
// The SeedDemo function creates a rich, realistic household with assets, vehicles,
// pets, vendors, bills, and maintenance tasks so the app appears fully populated
// on first load when DEMO_MODE=true.
package seed

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"home-os/api/internal/dex"
)

// SeedDemo populates the database with a rich demo household and all associated
// data: properties, rooms, assets, vehicles, pets, vendors, bills, maintenance
// tasks, calendars, and calendar events. It is idempotent — if the demo user
// already exists, it skips the main data seed and only seeds calendars if they
// are missing.
func SeedDemo(ctx context.Context, pool *pgxpool.Pool, dexClient *dex.Client) error {
	const demoEmail = "admin@homeos.demo"

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)`, demoEmail,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check demo user exists: %w", err)
	}

	// householdID is needed for calendar seeding regardless of whether main seed
	// runs or was already run.
	var householdID string

	if !exists {
		slog.Info("demo seed: creating demo data...")

		// 1. Create demo user: admin@homeos.demo / demo1234
		hash, err := bcrypt.GenerateFromPassword([]byte("demo1234"), 12)
		if err != nil {
			return fmt.Errorf("hash demo password: %w", err)
		}
		var userID string
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id`,
			demoEmail, "Alex Johnson", string(hash),
		).Scan(&userID); err != nil {
			return fmt.Errorf("insert demo user: %w", err)
		}
		slog.Info("demo seed: created demo user", "email", demoEmail, "id", userID)

		// Create password in Dex's local password database so the demo user
		// can authenticate via OIDC/device code flow.
		if dexClient != nil {
			if err := dexClient.CreatePassword(ctx, demoEmail, string(hash), userID); err != nil {
				slog.Warn("demo seed: failed to create dex password, continuing", "email", demoEmail, "error", err)
			} else {
				slog.Info("demo seed: created dex password", "email", demoEmail)
			}
		} else {
			slog.Info("demo seed: dex client not available, skipping dex password creation")
		}

		// 2. Create household: "The Johnson Family"
		if err := pool.QueryRow(ctx,
			`INSERT INTO households (name) VALUES ($1) RETURNING id`,
			"The Johnson Family",
		).Scan(&householdID); err != nil {
			return fmt.Errorf("insert demo household: %w", err)
		}
		slog.Info("demo seed: created household", "id", householdID)

		// 3. Create membership (user as owner of household)
		if _, err := pool.Exec(ctx,
			`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
			householdID, userID, "owner",
		); err != nil {
			return fmt.Errorf("insert demo membership: %w", err)
		}

		// 4. Create properties and rooms
		// Property 1: Main Residence — 123 Oak Street, Springfield, IL
		var mainResidenceID string
		if err := pool.QueryRow(ctx,
			`INSERT INTO properties (household_id, name, address, property_type)
			 VALUES ($1, $2, $3, $4) RETURNING id`,
			householdID, "Main Residence", "123 Oak Street, Springfield, IL", "single_family",
		).Scan(&mainResidenceID); err != nil {
			return fmt.Errorf("insert main residence: %w", err)
		}

		// Rooms for Main Residence
		mainResRooms := map[string]int{
			"Living Room":     1,
			"Master Bedroom":  1,
			"Kitchen":         1,
			"Garage":          1,
			"Basement":        1,
		}
		var roomIDs = make(map[string]string) // keyed by "propertyName/roomName"
		for roomName, floor := range mainResRooms {
			var roomID string
			f := floor
			if err := pool.QueryRow(ctx,
				`INSERT INTO rooms (property_id, name, floor) VALUES ($1, $2, $3) RETURNING id`,
				mainResidenceID, roomName, f,
			).Scan(&roomID); err != nil {
				return fmt.Errorf("insert room %s: %w", roomName, err)
			}
			roomIDs[roomName] = roomID
		}

		// Property 2: Lake Cabin — 45 Lakeview Drive, Lake Geneva, WI
		var lakeCabinID string
		if err := pool.QueryRow(ctx,
			`INSERT INTO properties (household_id, name, address, property_type)
			 VALUES ($1, $2, $3, $4) RETURNING id`,
			householdID, "Lake Cabin", "45 Lakeview Drive, Lake Geneva, WI", "cabin",
		).Scan(&lakeCabinID); err != nil {
			return fmt.Errorf("insert lake cabin: %w", err)
		}

		// Rooms for Lake Cabin
		cabinRooms := []string{"Main Room", "Bedroom", "Deck"}
		for _, roomName := range cabinRooms {
			var roomID string
			if err := pool.QueryRow(ctx,
				`INSERT INTO rooms (property_id, name) VALUES ($1, $2) RETURNING id`,
				lakeCabinID, roomName,
			).Scan(&roomID); err != nil {
				return fmt.Errorf("insert room %s: %w", roomName, err)
			}
			roomIDs["Cabin/"+roomName] = roomID
		}

		// Set financial fields on Main Residence
		if _, err := pool.Exec(ctx,
			`UPDATE properties SET purchase_price = 320000, purchase_date = '2018-06-15', current_value = 425000, mortgage_amount = 295000 WHERE id = $1`,
			mainResidenceID,
		); err != nil {
			return fmt.Errorf("update main residence financials: %w", err)
		}

		// Set financial fields on Lake Cabin
		if _, err := pool.Exec(ctx,
			`UPDATE properties SET purchase_price = 155000, purchase_date = '2020-03-01', current_value = 185000 WHERE id = $1`,
			lakeCabinID,
		); err != nil {
			return fmt.Errorf("update lake cabin financials: %w", err)
		}

		slog.Info("demo seed: created 2 properties with rooms")

		// 5. Create assets (15+)

		// Helper to get room ID from the map
		getRoom := func(name string) string { return roomIDs[name] }

		type assetDef struct {
			name           string
			category       string
			manufacturer   string
			model          string
			serialNumber   string
			purchaseDate   string
			purchasePrice  float64
			currentValue   float64
			warrantyExpiry string
			propertyID     string
			roomID         string
		}

		assets := []assetDef{
			{"HVAC System", "HVAC", "Carrier", "24ACC636A003", "HVA-2019-001", "2019-03-15", 4200, 3400, "2029-03-15", mainResidenceID, getRoom("Basement")},
			{"Water Heater", "Plumbing", "Rheem", "XE50T12CS55U1", "", "2021-06-01", 800, 600, "", mainResidenceID, getRoom("Basement")},
			{"Refrigerator", "Appliance", "Samsung", "RF28R7351SR", "REF-2020-445", "2020-01-10", 1800, 1260, "", mainResidenceID, getRoom("Kitchen")},
			{"Dishwasher", "Appliance", "Bosch", "SHPM88Z75N", "", "2020-01-10", 900, 630, "", mainResidenceID, getRoom("Kitchen")},
			{"Washer", "Appliance", "LG", "WM4000HWA", "", "2021-03-20", 900, 675, "", mainResidenceID, ""},
			{"Dryer", "Appliance", "LG", "DLEX4000W", "", "2021-03-20", 800, 600, "", mainResidenceID, ""},
			{"Lawn Mower", "Outdoor Equipment", "John Deere", "E130", "LM-2022-789", "2022-05-01", 1500, 1050, "", mainResidenceID, getRoom("Garage")},
			{"Generator", "Outdoor Equipment", "Honda", "EU7000iS", "GEN-2020-112", "2020-08-15", 4000, 3000, "", mainResidenceID, getRoom("Garage")},
			{"Smart TV 65\"", "Electronics", "Samsung", "QN65Q80C", "", "2022-11-25", 1200, 900, "", mainResidenceID, getRoom("Living Room")},
			{"Home Security System", "Electronics", "Ring", "Alarm Pro", "", "2021-09-01", 500, 425, "", mainResidenceID, ""},
			{"Cabin HVAC", "HVAC", "Lennox", "XC21-036", "", "2018-05-01", 3500, 2800, "", lakeCabinID, getRoom("Cabin/Main Room")},
			{"Cabin Generator", "Outdoor Equipment", "Generac", "7043", "", "2020-06-01", 2000, 1500, "", lakeCabinID, ""},
			{"Snowblower", "Outdoor Equipment", "Ariens", "921046", "", "2020-11-01", 700, 490, "", mainResidenceID, getRoom("Garage")},
			{"Pressure Washer", "Outdoor Equipment", "Karcher", "K5 Premium", "", "2021-04-15", 350, 260, "", mainResidenceID, getRoom("Garage")},
			{"Water Softener", "Plumbing", "Culligan", "HE 1.25", "", "2019-03-15", 1200, 900, "", mainResidenceID, getRoom("Basement")},
		}

		var assetIDs = make(map[string]string) // keyed by asset name
		for _, a := range assets {
			// Build nullable columns
			var serialNumber, roomID interface{}
			if a.serialNumber != "" {
				serialNumber = a.serialNumber
			}
			if a.roomID != "" {
				roomID = a.roomID
			}

			var warrantyExpiry interface{}
			if a.warrantyExpiry != "" {
				t, err := time.Parse("2006-01-02", a.warrantyExpiry)
				if err != nil {
					return fmt.Errorf("parse warranty expiry for %s: %w", a.name, err)
				}
				warrantyExpiry = t
			}

			purchaseDate, err := time.Parse("2006-01-02", a.purchaseDate)
			if err != nil {
				return fmt.Errorf("parse purchase date for %s: %w", a.name, err)
			}

			var assetID string
			if err := pool.QueryRow(ctx,
				`INSERT INTO assets (household_id, property_id, room_id,
				                    name, category, manufacturer, model, serial_number,
				                    purchase_date, purchase_price, current_value, warranty_expiry)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				 RETURNING id`,
				householdID, a.propertyID, roomID,
				a.name, a.category, a.manufacturer, a.model, serialNumber,
				purchaseDate, a.purchasePrice, a.currentValue, warrantyExpiry,
			).Scan(&assetID); err != nil {
				return fmt.Errorf("insert asset %s: %w", a.name, err)
			}
			assetIDs[a.name] = assetID
		}

		slog.Info("demo seed: created 15 assets")

		// 6. Create maintenance tasks
		type maintTaskDef struct {
			name        string
			description string
			status      string
			dueDate     string
			propertyID  string
			assetName   string // maps to assetIDs lookup
			completedAt string
		}

		maintTasks := []maintTaskDef{
			{"Replace HVAC Filter", "Replace disposable filter in main HVAC unit", "pending", "2026-07-01", mainResidenceID, "HVAC System", ""},
			{"Annual Furnace Inspection", "Professional inspection by HVAC contractor", "pending", "2026-10-01", mainResidenceID, "HVAC System", ""},
			{"Cabin Winterization", "Drain pipes, winterize plumbing, cover outdoor fixtures", "pending", "2026-11-01", lakeCabinID, "", ""},
			{"Gutter Cleaning", "Clean gutters and downspouts at main residence", "done", "", mainResidenceID, "", "2026-04-15"},
			{"Lawn Mower Oil Change", "Change oil, replace air filter and spark plug", "done", "", mainResidenceID, "Lawn Mower", "2026-05-01"},
		}

		for _, mt := range maintTasks {
			var dueDate, completedAt, assetID interface{}
			if mt.dueDate != "" {
				t, err := time.Parse("2006-01-02", mt.dueDate)
				if err != nil {
					return fmt.Errorf("parse due date for task %s: %w", mt.name, err)
				}
				dueDate = t
			}
			if mt.completedAt != "" {
				t, err := time.Parse("2006-01-02", mt.completedAt)
				if err != nil {
					return fmt.Errorf("parse completed date for task %s: %w", mt.name, err)
				}
				completedAt = t
			}
			if mt.assetName != "" {
				assetID = assetIDs[mt.assetName]
			}

			var propertyID interface{}
			if mt.propertyID != "" {
				propertyID = mt.propertyID
			}

			if _, err := pool.Exec(ctx,
				`INSERT INTO maintenance_tasks (household_id, property_id, asset_id,
				                               name, description, status, due_date, completed_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				householdID, propertyID, assetID,
				mt.name, mt.description, mt.status, dueDate, completedAt,
			); err != nil {
				return fmt.Errorf("insert maintenance task %s: %w", mt.name, err)
			}
		}

		slog.Info("demo seed: created 5 maintenance tasks")

		// 7. Create vehicles
		type vehicleDef struct {
			year             int
			make             string
			model            string
			vin              string
			licensePlate     string
			color            string
			purchasePrice    float64
			purchaseDate     string
			currentValue     float64
			loanAmount       float64
			insuranceCost    float64
			registrationCost float64
		}

		vehicles := []vehicleDef{
			{2022, "Ford", "F-150", "1FTFW1E83NFA12345", "IL-ABC123", "Agate Black", 55000, "2022-01-15", 42000, 12000, 1200, 150},
			{2021, "Toyota", "RAV4", "2T3P1RFV5MC123456", "IL-XYZ789", "Magnetic Gray", 32000, "2021-06-01", 26000, 0, 1100, 120},
			{2019, "Harley-Davidson", "Road Glide", "1HD1KHC19KB123456", "IL-MOTO1", "Vivid Black", 28000, "2019-08-15", 18000, 0, 800, 90},
		}

		for _, v := range vehicles {
			purchaseDate, err := time.Parse("2006-01-02", v.purchaseDate)
			if err != nil {
				return fmt.Errorf("parse purchase date for vehicle %s %s: %w", v.make, v.model, err)
			}
			if _, err := pool.Exec(ctx,
				`INSERT INTO vehicles (household_id, year, make, model, vin, license_plate, color,
				                      purchase_price, purchase_date, current_value, loan_amount, insurance_cost, registration_cost)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
				householdID, v.year, v.make, v.model, v.vin, v.licensePlate, v.color,
				v.purchasePrice, purchaseDate, v.currentValue, v.loanAmount, v.insuranceCost, v.registrationCost,
			); err != nil {
				return fmt.Errorf("insert vehicle %s %s: %w", v.make, v.model, err)
			}
		}

		slog.Info("demo seed: created 3 vehicles")

		// 8. Create pets
		type petDef struct {
			name        string
			species     string
			breed       string
			dateOfBirth string
			vetName     string
			vetPhone    string
		}

		pets := []petDef{
			{"Max", "Dog", "Golden Retriever", "2020-03-15", "Springfield Animal Hospital", "555-0100"},
			{"Luna", "Cat", "Domestic Shorthair", "2021-07-04", "Springfield Animal Hospital", "555-0100"},
		}

		for _, p := range pets {
			dob, err := time.Parse("2006-01-02", p.dateOfBirth)
			if err != nil {
				return fmt.Errorf("parse pet DOB for %s: %w", p.name, err)
			}
			if _, err := pool.Exec(ctx,
				`INSERT INTO pets (household_id, name, species, breed, date_of_birth, vet_name, vet_phone)
				 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				householdID, p.name, p.species, p.breed, dob, p.vetName, p.vetPhone,
			); err != nil {
				return fmt.Errorf("insert pet %s: %w", p.name, err)
			}
		}

		slog.Info("demo seed: created 2 pets")

		// 9. Create vendors
		type vendorDef struct {
			name      string
			specialty string
			phone     string
			email     string
		}

		vendors := []vendorDef{
			{"Springfield HVAC Services", "HVAC", "555-0201", "hvac@springfield.example"},
			{"Oak Street Plumbing", "Plumbing", "555-0202", ""},
			{"Green Thumb Landscaping", "Landscaping", "555-0203", ""},
			{"Johnson Electric", "Electrical", "555-0204", ""},
		}

		for _, v := range vendors {
			var email interface{}
			if v.email != "" {
				email = v.email
			}
			if _, err := pool.Exec(ctx,
				`INSERT INTO vendors (household_id, name, specialty, phone, email)
				 VALUES ($1, $2, $3, $4, $5)`,
				householdID, v.name, v.specialty, v.phone, email,
			); err != nil {
				return fmt.Errorf("insert vendor %s: %w", v.name, err)
			}
		}

		slog.Info("demo seed: created 4 vendors")

		// 10. Create bills
		type billDef struct {
			name     string
			amount   string
			dueDay   int
			category string
		}

		bills := []billDef{
			{"Mortgage", "2450.00", 1, "Housing"},
			{"Electric", "180.00", 15, "Utilities"},
			{"Internet", "89.00", 20, "Utilities"},
			{"Home Insurance", "210.00", 1, "Insurance"},
			{"Cabin Property Tax", "1800.00", 15, "Taxes"},
		}

		for _, b := range bills {
			var amount pgtype.Numeric
			if err := amount.Scan(b.amount); err != nil {
				return fmt.Errorf("parse bill amount for %s: %w", b.name, err)
			}
			if _, err := pool.Exec(ctx,
				`INSERT INTO bills (household_id, name, amount, due_day, category)
				 VALUES ($1, $2, $3, $4, $5)`,
				householdID, b.name, amount, b.dueDay, b.category,
			); err != nil {
				return fmt.Errorf("insert bill %s: %w", b.name, err)
			}
		}

		slog.Info("demo seed: created 5 bills")

		// 11. Create loans
		// Mortgage — linked to Main Residence
		if _, err := pool.Exec(ctx,
			`INSERT INTO loans (household_id, name, entity_type, entity_id, lender, original_amount, remaining_balance, interest_rate, term_months, monthly_payment, start_date)
			 VALUES ($1, 'Main Residence Mortgage', 'property', $2, 'Wells Fargo', 295000, 275000, 4.25, 360, 1450, '2018-07-01')`,
			householdID, mainResidenceID,
		); err != nil {
			return fmt.Errorf("insert mortgage loan: %w", err)
		}

		// Vehicle loan — look up the Ford F-150 ID
		var fordID string
		if err := pool.QueryRow(ctx, `SELECT id FROM vehicles WHERE household_id = $1 AND make = 'Ford' LIMIT 1`, householdID).Scan(&fordID); err != nil {
			return fmt.Errorf("get ford ID: %w", err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO loans (household_id, name, entity_type, entity_id, lender, original_amount, remaining_balance, interest_rate, term_months, monthly_payment, start_date)
			 VALUES ($1, 'F-150 Financing', 'vehicle', $2, 'Ford Credit', 12000, 8500, 6.99, 60, 350, '2022-01-15')`,
			householdID, fordID,
		); err != nil {
			return fmt.Errorf("insert ford loan: %w", err)
		}

		slog.Info("demo seed: created 2 loans")
	} else {
		slog.Info("demo seed: demo user already exists, checking calendars")

		// Get the household ID since we skipped the main seed block.
		if err := pool.QueryRow(ctx,
			`SELECT id FROM households ORDER BY created_at ASC LIMIT 1`,
		).Scan(&householdID); err != nil {
			return fmt.Errorf("get demo household: %w", err)
		}
	}

	// ---------------------------------------------------------------------------
	// 11. Calendars and calendar events — runs every time, but only creates if
	//     no calendars exist for the household (idempotent).
	// ---------------------------------------------------------------------------
	var calendarsExist bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM calendars WHERE household_id = $1)`, householdID,
	).Scan(&calendarsExist); err != nil {
		return fmt.Errorf("check calendars exist: %w", err)
	}

	if calendarsExist {
		slog.Info("demo seed: calendars already exist, skipping")
	} else {
		slog.Info("demo seed: creating calendars and calendar events...")

		// Resolve property IDs from the database (they were inserted by the main
		// seed above or already exist from a prior run).
		var mainResidenceID, lakeCabinID string
		if err := pool.QueryRow(ctx,
			`SELECT id FROM properties WHERE household_id = $1 AND name = $2`,
			householdID, "Main Residence",
		).Scan(&mainResidenceID); err != nil {
			return fmt.Errorf("get Main Residence property ID: %w", err)
		}
		if err := pool.QueryRow(ctx,
			`SELECT id FROM properties WHERE household_id = $1 AND name = $2`,
			householdID, "Lake Cabin",
		).Scan(&lakeCabinID); err != nil {
			return fmt.Errorf("get Lake Cabin property ID: %w", err)
		}

		// Build ical JSON helper
		makeICal := func(title, description, start, end string, allDay bool, location, eventType, color string) string {
			desc := ""
			if description != "" {
				desc = description
			}
			loc := ""
			if location != "" {
				loc = location
			}
			return fmt.Sprintf(`{"title":"%s","description":"%s","start":"%s","end":"%s","all_day":%t,"location":"%s","color":"%s","event_type":"%s"}`,
				title, desc, start, end, allDay, loc, color, eventType)
		}

		// Helper to format a date in RFC3339 for all-day events
		allDayStart := func(date string) string { return date + "T00:00:00Z" }
		allDayEnd := func(date string) string { return date + "T23:59:00Z" }
		makeTime := func(date, timeOfDay string) string { return date + "T" + timeOfDay + ":00Z" }

		// -----------------------------------------------------------------------
		// Create 3 calendars
		// -----------------------------------------------------------------------
		type calDef struct {
			name       string
			calType    string
			color      string
			propertyID string
		}

		calendars := []calDef{
			{"Johnson Family", "family", "#3B82F6", ""},
			{"Main Residence", "properties", "#10B981", mainResidenceID},
			{"Lake Cabin", "properties", "#F59E0B", lakeCabinID},
		}

		type createdCalendar struct {
			id   string
			name string
			uid  string
		}
		var createdCalendars []createdCalendar

		for _, c := range calendars {
			caldavUID := uuid.New().String()
			var calID string

			var propID interface{}
			if c.propertyID != "" {
				propID = c.propertyID
			}

			if err := pool.QueryRow(ctx,
				`INSERT INTO calendars (household_id, property_id, name, type, color, caldav_uid)
				 VALUES ($1, $2, $3, $4, $5, $6)
				 RETURNING id`,
				householdID, propID, c.name, c.calType, c.color, caldavUID,
			).Scan(&calID); err != nil {
				return fmt.Errorf("insert calendar %s: %w", c.name, err)
			}
			createdCalendars = append(createdCalendars, createdCalendar{id: calID, name: c.name, uid: caldavUID})
		}
		slog.Info("demo seed: created 3 calendars")

		// Helper to find calendar ID by name
		calID := func(name string) string {
			for _, cc := range createdCalendars {
				if cc.name == name {
					return cc.id
				}
			}
			return ""
		}

		// -------------------------------------------------------------------
		// Johnson Family Calendar events (~30)
		// -------------------------------------------------------------------
		type eventDef struct {
			calendarName string
			title        string
			description  string
			start        string // RFC3339
			end          string // RFC3339
			allDay       bool
			location     string
			eventType    string
			color        string
		}

		familyCal := "Johnson Family"
		mainResCal := "Main Residence"
		lakeCal := "Lake Cabin"
		famColor := "#3B82F6"
		mainColor := "#10B981"
		lakeColor := "#F59E0B"

		events := []eventDef{
			// --- Johnson Family: Holidays ---
			{familyCal, "New Year's Day", "Happy New Year!", allDayStart("2026-01-01"), allDayEnd("2026-01-01"), true, "", "holiday", famColor},
			{familyCal, "Valentine's Day Dinner", "Dinner reservation at The Grill", makeTime("2026-02-14", "19:00"), makeTime("2026-02-14", "21:00"), false, "The Grill Downtown", "social", famColor},
			{familyCal, "Easter Sunday", "Easter brunch at home", allDayStart("2026-04-05"), allDayEnd("2026-04-05"), true, "", "holiday", famColor},
			{familyCal, "Memorial Day BBQ", "Annual family BBQ", allDayStart("2026-05-25"), allDayEnd("2026-05-25"), true, "123 Oak Street", "social", famColor},
			{familyCal, "Independence Day", "Town fireworks at 8pm", allDayStart("2026-07-04"), allDayEnd("2026-07-04"), true, "Springfield Park", "holiday", famColor},
			{familyCal, "Labor Day Weekend", "Family trip to the lake", allDayStart("2026-09-05"), allDayEnd("2026-09-07"), true, "Lake Cabin", "trip", famColor},
			{familyCal, "Thanksgiving Dinner", "Family gathering at home", allDayStart("2026-11-26"), allDayEnd("2026-11-26"), true, "123 Oak Street", "holiday", famColor},
			{familyCal, "Christmas Day", "Presents and family dinner", allDayStart("2026-12-25"), allDayEnd("2026-12-25"), true, "123 Oak Street", "holiday", famColor},
			{familyCal, "New Year's Eve", "Countdown party at the Johnsons", makeTime("2026-12-31", "20:00"), allDayEnd("2026-12-31"), false, "123 Oak Street", "social", famColor},

			// --- Johnson Family: Birthdays ---
			{familyCal, "Alex's Birthday", "Happy Birthday Alex!", allDayStart("2026-02-14"), allDayEnd("2026-02-14"), true, "", "birthday", famColor},
			{familyCal, "Jamie's Birthday", "Happy Birthday Jamie!", allDayStart("2026-06-08"), allDayEnd("2026-06-08"), true, "", "birthday", famColor},
			{familyCal, "Max's Birthday", "Happy Birthday Max! 🐾", allDayStart("2026-03-15"), allDayEnd("2026-03-15"), true, "", "birthday", famColor},
			{familyCal, "Luna's Birthday", "Happy Birthday Luna! 🐾", allDayStart("2026-07-04"), allDayEnd("2026-07-04"), true, "", "birthday", famColor},

			// --- Johnson Family: Anniversary ---
			{familyCal, "Wedding Anniversary", "15th Wedding Anniversary", allDayStart("2026-09-20"), allDayEnd("2026-09-20"), true, "", "custom", famColor},

			// --- Johnson Family: School events ---
			{familyCal, "Parent-Teacher Conference", "Spring semester conference", makeTime("2026-03-10", "14:00"), makeTime("2026-03-10", "16:00"), false, "Springfield Elementary", "school", famColor},
			{familyCal, "Spring Break Starts", "Kids are off school", allDayStart("2026-03-16"), allDayEnd("2026-03-20"), false, "", "school", famColor},
			{familyCal, "Last Day of School", "Summer vacation begins!", allDayStart("2026-06-05"), allDayEnd("2026-06-05"), true, "Springfield Elementary", "school", famColor},
			{familyCal, "First Day of School", "Back to school!", allDayStart("2026-08-24"), allDayEnd("2026-08-24"), true, "Springfield Elementary", "school", famColor},
			{familyCal, "School Science Fair", "Jamie's science project presentation", makeTime("2026-05-15", "18:00"), makeTime("2026-05-15", "20:00"), false, "Springfield Elementary Gym", "school", famColor},

			// --- Johnson Family: Appointments ---
			{familyCal, "Family Dentist Appointment", "Annual cleaning for Alex and Jamie", makeTime("2026-01-12", "09:00"), makeTime("2026-01-12", "10:30"), false, "Springfield Dental", "appointment", famColor},
			{familyCal, "Alex Annual Physical", "Yearly checkup", makeTime("2026-03-03", "10:00"), makeTime("2026-03-03", "11:00"), false, "Springfield Medical Group", "appointment", famColor},
			{familyCal, "Jamie Eye Exam", "Yearly vision check", makeTime("2026-05-10", "11:00"), makeTime("2026-05-10", "12:00"), false, "Springfield Optometry", "appointment", famColor},
			{familyCal, "Flu Shots", "Family flu vaccinations", makeTime("2026-10-15", "09:00"), makeTime("2026-10-15", "10:00"), false, "Springfield Pharmacy", "appointment", famColor},
			{familyCal, "Vet Appointment - Max", "Annual checkup for Max", makeTime("2026-04-20", "10:00"), makeTime("2026-04-20", "11:00"), false, "Springfield Animal Hospital", "appointment", famColor},

			// --- Johnson Family: Social / Family events ---
			{familyCal, "Family Movie Night", "Movie and popcorn at home", makeTime("2026-01-31", "18:00"), makeTime("2026-01-31", "21:00"), false, "Home - Living Room", "social", famColor},
			{familyCal, "Family Game Night", "Board games and pizza", makeTime("2026-02-28", "17:00"), makeTime("2026-02-28", "20:00"), false, "Home", "social", famColor},
			{familyCal, "Camping Trip", "Weekend camping at Lake Geneva", allDayStart("2026-07-10"), allDayEnd("2026-07-12"), false, "Lake Geneva Campground", "trip", famColor},
			{familyCal, "Neighborhood Block Party", "Annual block party on Oak Street", makeTime("2026-08-08", "14:00"), makeTime("2026-08-08", "20:00"), false, "123 Oak Street", "social", famColor},
			{familyCal, "Pumpkin Picking", "Trip to Johnson's Farm for pumpkins", makeTime("2026-10-17", "10:00"), makeTime("2026-10-17", "13:00"), false, "Johnson's Farm", "social", famColor},
			{familyCal, "Holiday Party", "Annual holiday gathering with friends", makeTime("2026-12-19", "18:00"), makeTime("2026-12-19", "22:00"), false, "123 Oak Street", "social", famColor},
		}

		// --- Main Residence Calendar events (~20) ---
		mainResEvents := []eventDef{
			{mainResCal, "HVAC Spring Tune-Up", "Annual HVAC maintenance - spring", makeTime("2026-04-20", "08:00"), makeTime("2026-04-20", "12:00"), false, "", "maintenance", mainColor},
			{mainResCal, "HVAC Fall Tune-Up", "Annual HVAC maintenance - fall", makeTime("2026-10-05", "08:00"), makeTime("2026-10-05", "12:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Spring Gutter Cleaning", "Clean gutters and downspouts", makeTime("2026-03-22", "09:00"), makeTime("2026-03-22", "12:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Fall Gutter Cleaning", "Clean gutters and downspouts", makeTime("2026-11-07", "09:00"), makeTime("2026-11-07", "12:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Furniture Delivery", "New living room sofa arrives", makeTime("2026-01-18", "10:00"), makeTime("2026-01-18", "12:00"), false, "123 Oak Street", "delivery", mainColor},
			{mainResCal, "Appliance Delivery", "New refrigerator delivery", makeTime("2026-03-12", "08:00"), makeTime("2026-03-12", "10:00"), false, "123 Oak Street", "delivery", mainColor},
			{mainResCal, "Garage Door Repair", "Garage door opener replacement", makeTime("2026-02-10", "09:00"), makeTime("2026-02-10", "12:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Driveway Sealcoating", "Sealcoat the asphalt driveway", makeTime("2026-06-01", "07:00"), makeTime("2026-06-01", "16:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Sprinkler System Startup", "Spring sprinkler activation", makeTime("2026-05-01", "09:00"), makeTime("2026-05-01", "11:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Sprinkler Winterization", "Blow out sprinkler lines for winter", makeTime("2026-10-20", "09:00"), makeTime("2026-10-20", "11:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Window Cleaning", "Professional window cleaning - all windows", makeTime("2026-05-05", "08:00"), makeTime("2026-05-05", "15:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Pest Control - Spring", "Quarterly pest control treatment", makeTime("2026-03-15", "09:00"), makeTime("2026-03-15", "10:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Pest Control - Summer", "Quarterly pest control treatment", makeTime("2026-06-15", "09:00"), makeTime("2026-06-15", "10:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Pest Control - Fall", "Quarterly pest control treatment", makeTime("2026-09-15", "09:00"), makeTime("2026-09-15", "10:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Pest Control - Winter", "Quarterly pest control treatment", makeTime("2026-12-15", "09:00"), makeTime("2026-12-15", "10:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Carpet Cleaning", "Professional carpet cleaning - upstairs", makeTime("2026-02-01", "09:00"), makeTime("2026-02-01", "13:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Chimney Sweep", "Annual chimney cleaning and inspection", makeTime("2026-09-01", "08:00"), makeTime("2026-09-01", "10:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Tree Trimming", "Trim overgrown branches near house", makeTime("2026-03-05", "08:00"), makeTime("2026-03-05", "14:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Leaf Cleanup", "Fall leaf removal from yard", makeTime("2026-10-25", "08:00"), makeTime("2026-10-25", "15:00"), false, "", "maintenance", mainColor},
			{mainResCal, "Annual Home Inspection", "Yearly home inspection for insurance", makeTime("2026-08-15", "09:00"), makeTime("2026-08-15", "12:00"), false, "", "maintenance", mainColor},
		}

		// --- Lake Cabin Calendar events (~19) ---
		lakeEvents := []eventDef{
			{lakeCal, "Cabin Spring Opening", "Open cabin for the season - turn on water, clean", makeTime("2026-04-01", "08:00"), makeTime("2026-04-01", "16:00"), false, "45 Lakeview Drive", "maintenance", lakeColor},
			{lakeCal, "Cabin Winter Closing", "Close cabin for winter - drain pipes, winterize", makeTime("2026-11-01", "08:00"), makeTime("2026-11-01", "16:00"), false, "45 Lakeview Drive", "maintenance", lakeColor},
			{lakeCal, "Spring Getaway", "Family spring trip to the cabin", allDayStart("2026-04-10"), allDayEnd("2026-04-12"), false, "Lake Cabin", "trip", lakeColor},
			{lakeCal, "Memorial Day at the Cabin", "Long weekend at the lake", allDayStart("2026-05-23"), allDayEnd("2026-05-25"), false, "Lake Cabin", "trip", lakeColor},
			{lakeCal, "Summer Week 1", "First summer week at the cabin", allDayStart("2026-06-22"), allDayEnd("2026-06-28"), false, "Lake Cabin", "trip", lakeColor},
			{lakeCal, "Summer Week 2", "Second summer week at the cabin", allDayStart("2026-07-20"), allDayEnd("2026-07-26"), false, "Lake Cabin", "trip", lakeColor},
			{lakeCal, "Labor Day at the Cabin", "End of summer long weekend", allDayStart("2026-09-04"), allDayEnd("2026-09-07"), false, "Lake Cabin", "trip", lakeColor},
			{lakeCal, "Fall Foliage Trip", "Weekend trip to see fall colors", allDayStart("2026-10-09"), allDayEnd("2026-10-12"), false, "Lake Cabin", "trip", lakeColor},
			{lakeCal, "Winter Holiday at the Cabin", "Christmas week at the cabin", allDayStart("2026-12-26"), allDayEnd("2026-12-30"), false, "Lake Cabin", "trip", lakeColor},
			{lakeCal, "Cabin HVAC Service", "Annual HVAC check for cabin", makeTime("2026-05-15", "09:00"), makeTime("2026-05-15", "11:00"), false, "45 Lakeview Drive", "maintenance", lakeColor},
			{lakeCal, "Dock Inspection", "Annual dock safety and repair check", makeTime("2026-05-20", "09:00"), makeTime("2026-05-20", "12:00"), false, "Lake Cabin Dock", "maintenance", lakeColor},
			{lakeCal, "Septic Tank Pumping", "Pump septic tank - cabin", makeTime("2026-08-15", "08:00"), makeTime("2026-08-15", "10:00"), false, "", "maintenance", lakeColor},
			{lakeCal, "Cabin Roof Inspection", "Check roof for winter readiness", makeTime("2026-09-15", "09:00"), makeTime("2026-09-15", "11:00"), false, "45 Lakeview Drive", "maintenance", lakeColor},
			{lakeCal, "Cabin Generator Service", "Annual generator maintenance", makeTime("2026-10-01", "09:00"), makeTime("2026-10-01", "11:00"), false, "", "maintenance", lakeColor},
			{lakeCal, "Cabin Pest Control - Spring", "Cabin pest treatment before season", makeTime("2026-04-05", "09:00"), makeTime("2026-04-05", "10:00"), false, "", "maintenance", lakeColor},
			{lakeCal, "Cabin Pest Control - Fall", "Cabin pest treatment before closing", makeTime("2026-10-05", "09:00"), makeTime("2026-10-05", "10:00"), false, "", "maintenance", lakeColor},
			{lakeCal, "Firewood Delivery", "Season's firewood delivered", makeTime("2026-09-20", "10:00"), makeTime("2026-09-20", "12:00"), false, "45 Lakeview Drive", "delivery", lakeColor},
			{lakeCal, "Winter Supplies Delivery", "Salt, propane, and winter supplies", makeTime("2026-11-15", "10:00"), makeTime("2026-11-15", "12:00"), false, "45 Lakeview Drive", "delivery", lakeColor},
			{lakeCal, "Well Water Test", "Annual water quality test", makeTime("2026-06-10", "09:00"), makeTime("2026-06-10", "10:00"), false, "", "maintenance", lakeColor},
		}

		// Combine all events
		allEvents := append(events, mainResEvents...)
		allEvents = append(allEvents, lakeEvents...)

		// Count for verification
		eventCount := 0

		for _, e := range allEvents {
			cal := calID(e.calendarName)
			if cal == "" {
				return fmt.Errorf("calendar %q not found", e.calendarName)
			}

			eventUID := uuid.New().String()
			etag := uuid.New().String()
			icalData := makeICal(e.title, e.description, e.start, e.end, e.allDay, e.location, e.eventType, e.color)

			if _, err := pool.Exec(ctx,
				`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
				 VALUES ($1, $2, $3, $4, $5, NULL, NULL)`,
				cal, eventUID, icalData, etag, e.eventType,
			); err != nil {
				return fmt.Errorf("insert calendar event %q: %w", e.title, err)
			}

			// Record change for sync-collection
			if _, err := pool.Exec(ctx,
				`INSERT INTO calendar_changes (calendar_id, event_uid, action) VALUES ($1, $2, 'create')`,
				cal, eventUID,
			); err != nil {
				return fmt.Errorf("insert calendar change for %q: %w", e.title, err)
			}

			eventCount++
		}

		// Bump ctag on each calendar after all events are created
		for _, cc := range createdCalendars {
			if _, err := pool.Exec(ctx,
				`UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW() WHERE id = $1`,
				cc.id,
			); err != nil {
				return fmt.Errorf("bump ctag for calendar %q: %w", cc.name, err)
			}
		}

		slog.Info("demo seed: created calendars and events", "events", eventCount)
	}

	slog.Info("demo seed: complete — demo data ready")

	return nil
}
