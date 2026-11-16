package testutil

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Migration 051 (#4440): secret_keys.verifier (argon2id PHC string) next to
// the legacy key_hash, exactly one set per row, and a lossless down/up cycle.
func TestSecretKeyVerifier051_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	legacyHH := seedV2Family(t, pool).household
	upgradedHH := seedV2Family(t, pool).household

	const phc = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"
	legacyHash := []byte("0123456789abcdef0123456789abcdef")
	mustExec(t, pool, `INSERT INTO secret_keys (household_id, key_hash, key_salt) VALUES ($1, $2, 'salt-a')`, legacyHH, legacyHash)
	mustExec(t, pool, `INSERT INTO secret_keys (household_id, verifier, key_salt) VALUES ($1, $2, 'salt-b')`, upgradedHH, phc)

	// The CHECKs: exactly one of key_hash / verifier, and verifier is argon2id.
	for name, sql := range map[string]string{
		"both":    `INSERT INTO secret_keys (household_id, key_hash, verifier, key_salt, key_version) VALUES ($1, 'x', '$argon2id$x', 's', 2)`,
		"neither": `INSERT INTO secret_keys (household_id, key_salt, key_version) VALUES ($1, 's', 3)`,
		"format":  `INSERT INTO secret_keys (household_id, verifier, key_salt, key_version) VALUES ($1, 'sha256:abc', 's', 4)`,
	} {
		if _, err := pool.Exec(ctx, sql, legacyHH); err == nil {
			t.Errorf("CHECK allowed %s", name)
		}
	}

	// secret_keys has no activity/authorship triggers (050 excludes it).
	if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgrelid = 'secret_keys'::regclass AND NOT tgisinternal`); n != 0 {
		t.Fatalf("secret_keys has %d triggers", n)
	}

	for cycle := 0; cycle < 2; cycle++ {
		applyMigration(t, pool, "051_secret_key_verifier.down.sql")
		if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'secret_keys' AND column_name = 'verifier'`); n != 0 {
			t.Fatalf("cycle %d: verifier column left after down", cycle)
		}
		var got []byte
		mustScan(t, pool.QueryRow(ctx, `SELECT key_hash FROM secret_keys WHERE household_id = $1`, legacyHH), &got)
		if string(got) != string(legacyHash) {
			t.Fatalf("cycle %d: down changed a legacy key_hash", cycle)
		}
		mustScan(t, pool.QueryRow(ctx, `SELECT key_hash FROM secret_keys WHERE household_id = $1`, upgradedHH), &got)
		if len(got) != 0 {
			t.Fatalf("cycle %d: upgraded row got a non-empty key_hash on down", cycle)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM secret_key_verifier_051_backup`); n != 1 {
			t.Fatalf("cycle %d: want 1 parked verifier, got %d", cycle, n)
		}

		applyMigration(t, pool, "051_secret_key_verifier.up.sql")
		if n := countRows(t, pool, `SELECT count(*) FROM pg_tables WHERE tablename = 'secret_key_verifier_051_backup'`); n != 0 {
			t.Fatalf("cycle %d: backup table left after up", cycle)
		}
		var v *string
		mustScan(t, pool.QueryRow(ctx, `SELECT key_hash, verifier FROM secret_keys WHERE household_id = $1`, upgradedHH), &got, &v)
		if got != nil || v == nil || *v != phc {
			t.Fatalf("cycle %d: up did not restore the parked verifier", cycle)
		}
		mustScan(t, pool.QueryRow(ctx, `SELECT key_hash, verifier FROM secret_keys WHERE household_id = $1`, legacyHH), &got, &v)
		if string(got) != string(legacyHash) || v != nil {
			t.Fatalf("cycle %d: legacy row changed across down/up", cycle)
		}
	}
}

// The whole chain rolls back to 025 (just below 026_create_secrets) and up
// again (#4699). 051 down used to create secret_key_verifier_051_backup with
// an FK to secret_keys, so 026 down could not drop secret_keys and nothing
// below 051 could be rolled back. The backup now has no FK: it survives down
// to 025 holding the parked verifier, and 051 up drops it on the way back.
func TestMigrationChain_DownTo025AndUp(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()

	// Data in tables on both sides of 026, so the downs and the backfills in
	// the ups (039, 043, 056, ...) run over real rows.
	f := seedV2Family(t, pool)
	SeedHousehold(t, pool)
	const phc = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"
	mustExec(t, pool, `INSERT INTO secret_keys (household_id, verifier, key_salt) VALUES ($1, $2, 'salt')`, f.household, phc)
	mustExec(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Plumber')`, f.household)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, vehicle_id) VALUES ($1, 'Insurance', $2)`, f.household, f.vehicle)

	// Table and column shape of public, compared before and after.
	schema := func() string {
		t.Helper()
		var s string
		mustScan(t, pool.QueryRow(ctx, `SELECT coalesce(string_agg(table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable, E'\n'
			ORDER BY table_name, column_name), '') FROM information_schema.columns WHERE table_schema = 'public'`), &s)
		return s
	}
	before := schema()

	dir, err := migrationsDir()
	if err != nil {
		t.Fatal(err)
	}
	var downs, ups []string
	for _, kind := range []string{"down", "up"} {
		files, err := filepath.Glob(filepath.Join(dir, "*."+kind+".sql"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			name := filepath.Base(f)
			if name < "026_" { // keep 001-025
				continue
			}
			if kind == "down" {
				downs = append(downs, name)
			} else {
				ups = append(ups, name)
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(downs)))
	sort.Strings(ups)
	if len(downs) != len(ups) || len(downs) == 0 {
		t.Fatalf("%d down files vs %d up files from 026", len(downs), len(ups))
	}

	for _, name := range downs {
		applyMigration(t, pool, name)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename = 'secret_keys'`); n != 0 {
		t.Fatal("secret_keys left after 026 down")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM secret_key_verifier_051_backup`); n != 1 {
		t.Fatalf("want the parked verifier kept below 026, got %d rows", n)
	}

	for _, name := range ups {
		applyMigration(t, pool, name)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_tables WHERE tablename = 'secret_key_verifier_051_backup'`); n != 0 {
		t.Fatal("051 up left the backup table")
	}
	if after := schema(); after != before {
		t.Fatalf("schema differs after down to 025 and up:\n%s", diffLines(before, after))
	}
	if n := countRows(t, pool, `SELECT count(*) FROM households WHERE id = $1`, f.household); n != 1 {
		t.Fatal("pre-026 household lost across the chain")
	}
}

// diffLines lists lines only in a ("-") or only in b ("+").
func diffLines(a, b string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var out []string
	for l := range ma {
		if !mb[l] {
			out = append(out, "- "+l)
		}
	}
	for l := range mb {
		if !ma[l] {
			out = append(out, "+ "+l)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}
