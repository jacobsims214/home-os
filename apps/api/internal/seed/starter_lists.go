package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// starterItem is one item of a starter list template. section is the aisle
// (grocery) or the group (packing, chores) the item is shown under.
type starterItem struct {
	section string
	text    string
}

// starterList is a list template every family starts with. key is stable
// forever: it derives the list's id and is recorded in households.settings,
// so renaming a template here never makes a family get a second copy.
type starterList struct {
	key   string
	name  string
	kind  string
	items []starterItem
}

// starterListsSettingsKey is the households.settings key that records which
// starter templates a family has already been given (a JSON array of keys).
// A key in it is never seeded again, so a template the family deleted or
// renamed stays that way.
const starterListsSettingsKey = "seeded_list_templates"

// starterListNamespace is the UUIDv5 namespace for starter list ids:
// id = uuid5(namespace, household_id + "/" + key).
var starterListNamespace = uuid.MustParse("6f1d7a52-3c4b-4b8e-9a51-2f0c8d3e7b14")

func items(section string, texts ...string) []starterItem {
	out := make([]starterItem, len(texts))
	for i, t := range texts {
		out[i] = starterItem{section: section, text: t}
	}
	return out
}

func concat(groups ...[]starterItem) []starterItem {
	return slices.Concat(groups...)
}

// starterLists are seeded, as templates (is_template = true), into every
// family. Families copy one to start a real list (POST /api/v1/lists with
// template_id); the copy's items start unchecked.
var starterLists = []starterList{
	{
		key: "weekend-trip", name: "Weekend trip packing", kind: "packing",
		items: concat(
			items("Clothes", "Outfits for each day", "Pajamas", "Underwear and socks", "Jacket or sweater", "Comfortable shoes"),
			items("Toiletries", "Toothbrushes and toothpaste", "Shampoo and soap", "Deodorant", "Hairbrush", "Medications"),
			items("Electronics", "Phone chargers", "Headphones", "Portable battery"),
			items("Documents", "IDs", "Wallet", "Reservation details", "House keys"),
			items("Before leaving", "Take out the trash", "Lock doors and windows", "Adjust the thermostat"),
		),
	},
	{
		key: "beach-trip", name: "Beach trip", kind: "packing",
		items: concat(
			items("Clothes", "Swimsuits", "Cover-ups", "Sandals or water shoes", "Sun hats", "Light evening layers"),
			items("Sun care", "Sunscreen", "After-sun lotion", "Sunglasses", "Lip balm with SPF"),
			items("Beach gear", "Beach towels", "Beach umbrella or shade tent", "Beach chairs", "Sand toys", "Snorkel gear", "Waterproof phone pouch"),
			items("Food and drinks", "Cooler", "Water bottles", "Snacks"),
			items("Extras", "Dry bag for wet clothes", "Small first aid kit", "Bug spray"),
		),
	},
	{
		key: "ski-trip", name: "Ski trip", kind: "packing",
		items: concat(
			items("Ski clothes", "Ski jackets", "Ski pants", "Thermal base layers", "Fleece mid layers", "Ski socks", "Gloves or mittens", "Neck gaiters"),
			items("Gear", "Skis or snowboards", "Boots", "Poles", "Helmets", "Goggles", "Lift tickets or passes"),
			items("Après ski", "Warm hats", "Snow boots", "Casual warm clothes", "Swimsuits for the hot tub"),
			items("Care", "Sunscreen", "Lip balm", "Hand warmers", "Moisturizer"),
		),
	},
	{
		key: "baby-toddler-travel", name: "Baby & toddler travel", kind: "packing",
		items: concat(
			items("Diapering", "Diapers", "Wipes", "Diaper cream", "Changing pad", "Disposable bags"),
			items("Feeding", "Bottles", "Formula or milk", "Snacks", "Sippy cups", "Bibs", "Spoons", "Burp cloths"),
			items("Sleep", "Travel crib", "Crib sheets", "Sleep sack", "Favorite blanket or lovey", "White noise machine", "Night light"),
			items("Clothes", "Outfits (one extra per day)", "Pajamas", "Socks", "Hat"),
			items("Gear", "Car seat", "Stroller", "Baby carrier", "Toys and books"),
			items("Health", "Thermometer", "Infant pain reliever", "Baby sunscreen", "Nail clippers"),
		),
	},
	{
		key: "weekly-chores", name: "Weekly chores", kind: "chores",
		items: concat(
			items("Kitchen", "Wipe counters and stovetop", "Clean the microwave", "Mop the floor", "Clean out the fridge"),
			items("Bathrooms", "Clean toilets", "Scrub sinks and showers", "Replace towels"),
			items("Bedrooms", "Change bed sheets", "Tidy up toys and clothes"),
			items("Whole house", "Vacuum", "Dust surfaces", "Take out trash and recycling", "Water plants"),
			items("Laundry", "Wash, dry and fold laundry", "Put clothes away"),
			items("Outside", "Sweep the porch", "Bring in the mail and packages"),
		),
	},
	{
		key: "grocery-staples", name: "Grocery staples", kind: "grocery",
		items: concat(
			items("Produce", "Bananas", "Apples", "Lettuce or salad greens", "Tomatoes", "Onions", "Potatoes", "Carrots"),
			items("Dairy & eggs", "Milk", "Eggs", "Butter", "Cheese", "Yogurt"),
			items("Bakery", "Bread", "Tortillas"),
			items("Meat & seafood", "Chicken", "Ground beef"),
			items("Pantry", "Rice", "Pasta", "Pasta sauce", "Canned beans", "Peanut butter", "Cereal", "Olive oil", "Coffee or tea"),
			items("Frozen", "Frozen vegetables", "Frozen fruit"),
			items("Household", "Paper towels", "Toilet paper", "Dish soap", "Trash bags"),
		),
	},
}

// StarterListKeys returns the stable keys of the starter templates, in seed
// order.
func StarterListKeys() []string {
	keys := make([]string, len(starterLists))
	for i, l := range starterLists {
		keys[i] = l.key
	}
	return keys
}

// StarterListID returns the deterministic id of a household's starter
// template.
func StarterListID(householdID uuid.UUID, key string) uuid.UUID {
	return uuid.NewSHA1(starterListNamespace, []byte(householdID.String()+"/"+key))
}

// Querier is satisfied by pgx.Tx and *pgxpool.Pool.
type Querier interface {
	Execer
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// EnsureStarterLists gives a household the starter list templates it has not
// been given yet. Call it inside the transaction that creates the household
// (as with EnsureDefaultCalendars) or inside a transaction of its own: it
// locks the household row so concurrent callers do not race.
//
// It is idempotent, by two rules:
//   - households.settings.seeded_list_templates records the keys already
//     seeded; those are skipped, so a template the family deleted or renamed
//     is not recreated, while a template added to starterLists later still
//     reaches existing families;
//   - each template's id is StarterListID(household, key), inserted with
//     ON CONFLICT (id) DO NOTHING, so even with the settings key lost a
//     family never gets two copies.
func EnsureStarterLists(ctx context.Context, db Querier, householdID string) error {
	hhID, err := uuid.Parse(householdID)
	if err != nil {
		return fmt.Errorf("starter lists: household id: %w", err)
	}

	var raw []byte
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(settings -> $2::text, '[]'::jsonb) FROM households WHERE id = $1 FOR UPDATE`,
		hhID, starterListsSettingsKey,
	).Scan(&raw); err != nil {
		return fmt.Errorf("starter lists: lock household: %w", err)
	}
	var seeded []string
	if err := json.Unmarshal(raw, &seeded); err != nil {
		// A malformed value (not an array of strings) is replaced; the
		// deterministic ids still prevent duplicates.
		seeded = nil
	}

	created := 0
	changed := false
	for _, l := range starterLists {
		if slices.Contains(seeded, l.key) {
			continue
		}
		listID := StarterListID(hhID, l.key)
		tag, err := db.Exec(ctx,
			`INSERT INTO lists (id, household_id, name, kind, is_template)
			 VALUES ($1, $2, $3, $4, true)
			 ON CONFLICT (id) DO NOTHING`,
			listID, hhID, l.name, l.kind,
		)
		if err != nil {
			return fmt.Errorf("starter lists: insert %s: %w", l.key, err)
		}
		if tag.RowsAffected() == 1 && len(l.items) > 0 {
			sections := make([]string, len(l.items))
			texts := make([]string, len(l.items))
			for i, it := range l.items {
				sections[i], texts[i] = it.section, it.text
			}
			if _, err := db.Exec(ctx,
				`INSERT INTO list_items (list_id, household_id, section, text, position)
				 SELECT $1, $2, s.section, s.text, s.ord - 1
				   FROM unnest($3::text[], $4::text[]) WITH ORDINALITY AS s(section, text, ord)`,
				listID, hhID, sections, texts,
			); err != nil {
				return fmt.Errorf("starter lists: insert %s items: %w", l.key, err)
			}
			created++
		}
		seeded = append(seeded, l.key)
		changed = true
	}

	if changed {
		keys, err := json.Marshal(seeded)
		if err != nil {
			return fmt.Errorf("starter lists: encode keys: %w", err)
		}
		if _, err := db.Exec(ctx,
			`UPDATE households SET settings = jsonb_set(settings, ARRAY[$2::text], $3::jsonb)
			 WHERE id = $1`,
			hhID, starterListsSettingsKey, string(keys),
		); err != nil {
			return fmt.Errorf("starter lists: record keys: %w", err)
		}
	}
	if created > 0 {
		slog.Info("created starter list templates", "household_id", householdID, "count", created)
	}
	return nil
}

// txBeginner is satisfied by *pgxpool.Pool.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

var _ txBeginner = (*pgxpool.Pool)(nil)

// BackfillStarterLists runs EnsureStarterLists, one transaction each, for
// every household that has not been given every current starter template.
// It runs at api startup; once every family is seeded it does one query and
// writes nothing.
func BackfillStarterLists(ctx context.Context, db txBeginner) error {
	keys, err := json.Marshal(StarterListKeys())
	if err != nil {
		return fmt.Errorf("starter lists backfill: encode keys: %w", err)
	}
	rows, err := db.Query(ctx,
		`SELECT id::text FROM households
		  WHERE NOT (COALESCE(settings -> $1::text, '[]'::jsonb) @> $2::jsonb)
		  ORDER BY created_at, id`,
		starterListsSettingsKey, string(keys),
	)
	if err != nil {
		return fmt.Errorf("starter lists backfill: list households: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("starter lists backfill: list households: %w", err)
	}
	for _, id := range ids {
		if err := ensureStarterListsTx(ctx, db, id); err != nil {
			return err
		}
	}
	if len(ids) > 0 {
		slog.Info("starter lists backfill done", "households", len(ids))
	}
	return nil
}

func ensureStarterListsTx(ctx context.Context, db txBeginner, householdID string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starter lists: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := EnsureStarterLists(ctx, tx, householdID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("starter lists: commit: %w", err)
	}
	return nil
}
