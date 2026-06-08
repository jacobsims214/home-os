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

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// SeedDemo populates the database with a rich demo household and all associated
// data: properties, rooms, assets, vehicles, pets, vendors, bills, and maintenance
// tasks. It is idempotent — if the demo user already exists, it logs a message and
// returns nil without making any changes.
func SeedDemo(ctx context.Context, pool *pgxpool.Pool) error {
	const demoEmail = "admin@homeos.demo"

	// Idempotency check: if the demo user already exists, skip seeding.
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)`, demoEmail,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check demo user exists: %w", err)
	}
	if exists {
		slog.Info("demo seed: demo user already exists, skipping")
		return nil
	}

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

	// 2. Create household: "The Johnson Family"
	var householdID string
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
		warrantyExpiry string
		propertyID     string
		roomID         string
	}

	assets := []assetDef{
		{"HVAC System", "HVAC", "Carrier", "24ACC636A003", "HVA-2019-001", "2019-03-15", 4200, "2029-03-15", mainResidenceID, getRoom("Basement")},
		{"Water Heater", "Plumbing", "Rheem", "XE50T12CS55U1", "", "2021-06-01", 800, "", mainResidenceID, getRoom("Basement")},
		{"Refrigerator", "Appliance", "Samsung", "RF28R7351SR", "REF-2020-445", "2020-01-10", 1800, "", mainResidenceID, getRoom("Kitchen")},
		{"Dishwasher", "Appliance", "Bosch", "SHPM88Z75N", "", "2020-01-10", 900, "", mainResidenceID, getRoom("Kitchen")},
		{"Washer", "Appliance", "LG", "WM4000HWA", "", "2021-03-20", 900, "", mainResidenceID, ""},
		{"Dryer", "Appliance", "LG", "DLEX4000W", "", "2021-03-20", 800, "", mainResidenceID, ""},
		{"Lawn Mower", "Outdoor Equipment", "John Deere", "E130", "LM-2022-789", "2022-05-01", 1500, "", mainResidenceID, getRoom("Garage")},
		{"Generator", "Outdoor Equipment", "Honda", "EU7000iS", "GEN-2020-112", "2020-08-15", 4000, "", mainResidenceID, getRoom("Garage")},
		{"Smart TV 65\"", "Electronics", "Samsung", "QN65Q80C", "", "2022-11-25", 1200, "", mainResidenceID, getRoom("Living Room")},
		{"Home Security System", "Electronics", "Ring", "Alarm Pro", "", "2021-09-01", 500, "", mainResidenceID, ""},
		{"Cabin HVAC", "HVAC", "Lennox", "XC21-036", "", "2018-05-01", 3500, "", lakeCabinID, getRoom("Cabin/Main Room")},
		{"Cabin Generator", "Outdoor Equipment", "Generac", "7043", "", "2020-06-01", 2000, "", lakeCabinID, ""},
		{"Snowblower", "Outdoor Equipment", "Ariens", "921046", "", "2020-11-01", 700, "", mainResidenceID, getRoom("Garage")},
		{"Pressure Washer", "Outdoor Equipment", "Karcher", "K5 Premium", "", "2021-04-15", 350, "", mainResidenceID, getRoom("Garage")},
		{"Water Softener", "Plumbing", "Culligan", "HE 1.25", "", "2019-03-15", 1200, "", mainResidenceID, getRoom("Basement")},
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
			                    purchase_date, purchase_price, warranty_expiry)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 RETURNING id`,
			householdID, a.propertyID, roomID,
			a.name, a.category, a.manufacturer, a.model, serialNumber,
			purchaseDate, a.purchasePrice, warrantyExpiry,
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
		year         int
		make         string
		model        string
		vin          string
		licensePlate string
		color        string
	}

	vehicles := []vehicleDef{
		{2022, "Ford", "F-150", "1FTFW1E83NFA12345", "IL-ABC123", "Agate Black"},
		{2021, "Toyota", "RAV4", "2T3P1RFV5MC123456", "IL-XYZ789", "Magnetic Gray"},
		{2019, "Harley-Davidson", "Road Glide", "1HD1KHC19KB123456", "IL-MOTO1", "Vivid Black"},
	}

	for _, v := range vehicles {
		if _, err := pool.Exec(ctx,
			`INSERT INTO vehicles (household_id, year, make, model, vin, license_plate, color)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			householdID, v.year, v.make, v.model, v.vin, v.licensePlate, v.color,
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
	slog.Info("demo seed: complete — demo data ready")

	return nil
}
