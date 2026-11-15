package testutil

import (
	"context"
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
