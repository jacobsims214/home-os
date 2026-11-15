package secret

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
)

// keyTestDB connects to TEST_DATABASE_URL (all migrations applied). Skips when unset.
func keyTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newHousehold(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ('Secret key HH') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, id)
	})
	return id
}

// call runs a secret-key handler as a member of hid and returns status + body.
func call(t *testing.T, fn http.HandlerFunc, hid uuid.UUID, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req = req.WithContext(middleware.WithClaims(req.Context(), &auth.Claims{HouseholdID: hid.String(), Role: "owner"}))
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec.Code, rec.Body.String()
}

type keyRow struct {
	keyHash  []byte
	verifier *string
}

func readKeyRow(t *testing.T, pool *pgxpool.Pool, hid uuid.UUID) keyRow {
	t.Helper()
	var k keyRow
	if err := pool.QueryRow(context.Background(),
		`SELECT key_hash, verifier FROM secret_keys WHERE household_id = $1 AND key_version = 1`, hid,
	).Scan(&k.keyHash, &k.verifier); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSecretKey_SetupStoresSaltedVerifier(t *testing.T) {
	pool := keyTestDB(t)
	h := NewHandler(NewRepo(pool))
	hid := newHousehold(t, pool)
	verifier := sha256.Sum256([]byte("derived key bytes"))
	salt := []byte("0123456789abcdef")

	// Legacy field name (current UI) is accepted.
	code, body := call(t, h.SetupKey, hid, map[string]any{"key_hash": verifier[:], "key_salt": salt, "key_version": 1})
	if code != http.StatusCreated {
		t.Fatalf("setup: %d %s", code, body)
	}
	if strings.Contains(body, "key_hash") || strings.Contains(body, "verifier") || strings.Contains(body, "argon2") {
		t.Fatalf("setup response leaks verifier material: %s", body)
	}

	row := readKeyRow(t, pool, hid)
	if row.keyHash != nil {
		t.Fatal("new setup stored a legacy key_hash")
	}
	if row.verifier == nil || !strings.HasPrefix(*row.verifier, "$argon2id$") {
		t.Fatalf("verifier not stored as argon2id: %v", row.verifier)
	}

	// Correct verifier, via the new field name.
	if code, body := call(t, h.VerifyKey, hid, map[string]any{"verifier": verifier[:], "key_version": 1}); code != http.StatusOK {
		t.Fatalf("verify(correct): %d %s", code, body)
	}
	// Wrong verifier.
	wrong := sha256.Sum256([]byte("wrong"))
	if code, _ := call(t, h.VerifyKey, hid, map[string]any{"key_hash": wrong[:], "key_version": 1}); code != http.StatusUnprocessableEntity {
		t.Fatalf("verify(wrong): %d", code)
	}
	// Second setup is a conflict.
	if code, _ := call(t, h.SetupKey, hid, map[string]any{"verifier": verifier[:], "key_salt": salt, "key_version": 1}); code != http.StatusConflict {
		t.Fatalf("second setup: %d", code)
	}
	// Missing verifier stays a 400 (the UI's setup probe relies on non-404).
	if code, _ := call(t, h.VerifyKey, hid, map[string]any{"key_version": 1}); code != http.StatusBadRequest {
		t.Fatalf("verify(no verifier): %d", code)
	}
	// Out-of-range verifier length never matches.
	if code, _ := call(t, h.SetupKey, newHousehold(t, pool), map[string]any{"verifier": []byte("short"), "key_salt": salt, "key_version": 1}); code != http.StatusBadRequest {
		t.Fatalf("setup(short verifier): %d", code)
	}
}

func TestSecretKey_LegacyRowUpgradesOnUnlock(t *testing.T) {
	pool := keyTestDB(t)
	ctx := context.Background()
	h := NewHandler(NewRepo(pool))
	hid := newHousehold(t, pool)
	legacy := sha256.Sum256([]byte("legacy derived key"))

	// A household set up before 051: key_hash only.
	if _, err := pool.Exec(ctx,
		`INSERT INTO secret_keys (household_id, key_hash, key_salt, key_version) VALUES ($1, $2, $3, 1)`,
		hid, legacy[:], []byte("legacy-salt-16by")); err != nil {
		t.Fatal(err)
	}

	// A wrong password does not upgrade the row.
	wrong := sha256.Sum256([]byte("nope"))
	if code, _ := call(t, h.VerifyKey, hid, map[string]any{"key_hash": wrong[:], "key_version": 1}); code != http.StatusUnprocessableEntity {
		t.Fatalf("verify(wrong legacy): %d", code)
	}
	if row := readKeyRow(t, pool, hid); row.verifier != nil || row.keyHash == nil {
		t.Fatal("wrong password changed the legacy row")
	}

	// The right one unlocks and upgrades: key_hash cleared, argon2id stored.
	if code, body := call(t, h.VerifyKey, hid, map[string]any{"key_hash": legacy[:], "key_version": 1}); code != http.StatusOK {
		t.Fatalf("verify(legacy): %d %s", code, body)
	}
	row := readKeyRow(t, pool, hid)
	if row.keyHash != nil {
		t.Fatal("legacy key_hash not cleared after upgrade")
	}
	if row.verifier == nil || !strings.HasPrefix(*row.verifier, "$argon2id$") {
		t.Fatal("legacy row not upgraded to argon2id")
	}

	// It keeps unlocking with the same client value, now via argon2id.
	if code, _ := call(t, h.VerifyKey, hid, map[string]any{"key_hash": legacy[:], "key_version": 1}); code != http.StatusOK {
		t.Fatalf("verify(after upgrade): %d", code)
	}
	if code, _ := call(t, h.VerifyKey, hid, map[string]any{"key_hash": wrong[:], "key_version": 1}); code != http.StatusUnprocessableEntity {
		t.Fatalf("verify(wrong after upgrade): %d", code)
	}

	// The schema refuses a row holding both (or neither).
	if _, err := pool.Exec(ctx, `UPDATE secret_keys SET key_hash = $2 WHERE household_id = $1`, hid, legacy[:]); err == nil {
		t.Fatal("CHECK allowed key_hash next to verifier")
	}

	// Nothing about secret_keys reaches the activity log.
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM activity_log WHERE household_id = $1 AND (entity_type ILIKE '%key%' OR summary ILIKE '%argon2%')`, hid,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("activity_log has %d secret_keys rows", n)
	}
}
