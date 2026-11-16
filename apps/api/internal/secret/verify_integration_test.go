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
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// verifyAs runs VerifyKey as member uid of hid and returns status + Retry-After.
func verifyAs(h *Handler, hid, uid uuid.UUID, verifier []byte) (int, string) {
	b, _ := json.Marshal(map[string]any{"verifier": verifier, "key_version": 1})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req = req.WithContext(middleware.WithClaims(req.Context(),
		&auth.Claims{HouseholdID: hid.String(), UserID: uid.String(), Role: "owner"}))
	rec := httptest.NewRecorder()
	h.VerifyKey(rec, req)
	return rec.Code, rec.Header().Get("Retry-After")
}

// countEvaluations replaces the verifierEvaluated hook with a counter. Each
// evaluation also sleeps, widening the window in which a check-then-act limiter
// would let parallel requests through.
func countEvaluations(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	prev := verifierEvaluated
	verifierEvaluated = func() {
		n.Add(1)
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { verifierEvaluated = prev })
	return &n
}

func TestVerifyKey_ConcurrentGuessesCannotBypassLimit(t *testing.T) {
	pool := keyTestDB(t)
	h := NewHandler(NewRepo(pool))
	hid, uid := newHousehold(t, pool), uuid.New()
	right := sha256.Sum256([]byte("right"))
	wrong := sha256.Sum256([]byte("wrong"))
	if code, body := call(t, h.SetupKey, hid, map[string]any{"verifier": right[:], "key_salt": []byte("0123456789abcdef"), "key_version": 1}); code != http.StatusCreated {
		t.Fatalf("setup: %d %s", code, body)
	}
	evals := countEvaluations(t)

	const n = 50
	var (
		wg              sync.WaitGroup
		start           = make(chan struct{})
		n422, n429, bad atomic.Int64
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			code, retry := verifyAs(h, hid, uid, wrong[:])
			switch {
			case code == http.StatusUnprocessableEntity:
				n422.Add(1)
			case code == http.StatusTooManyRequests && retry != "":
				n429.Add(1)
			default:
				bad.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if bad.Load() != 0 || n422.Load()+n429.Load() != n {
		t.Fatalf("unexpected responses: 422=%d 429=%d other=%d", n422.Load(), n429.Load(), bad.Load())
	}
	if n422.Load() > verifyMaxFailures {
		t.Fatalf("%d wrong guesses answered 422, limit %d", n422.Load(), verifyMaxFailures)
	}
	if evals.Load() > verifyMaxFailures {
		t.Fatalf("%d verifier evaluations, limit %d", evals.Load(), verifyMaxFailures)
	}
	if evals.Load() != n422.Load() {
		t.Fatalf("evaluations %d != 422s %d", evals.Load(), n422.Load())
	}
	t.Logf("50 concurrent wrong guesses: %d evaluated (422), %d refused (429)", n422.Load(), n429.Load())

	// Refused requests did not burn the member's slots: wrong guesses continue
	// until exactly verifyMaxFailures have been counted, then 429.
	for i := n422.Load(); i < verifyMaxFailures; i++ {
		if code, _ := verifyAs(h, hid, uid, wrong[:]); code != http.StatusUnprocessableEntity {
			t.Fatalf("sequential wrong %d: %d", i+1, code)
		}
	}
	if code, retry := verifyAs(h, hid, uid, right[:]); code != http.StatusTooManyRequests || retry == "" {
		t.Fatalf("locked member, right password: %d %q", code, retry)
	}
	if evals.Load() != verifyMaxFailures {
		t.Fatalf("total evaluations %d, want %d", evals.Load(), verifyMaxFailures)
	}
}

func TestVerifyKey_LimitIsPerMemberWithHouseholdCeiling(t *testing.T) {
	pool := keyTestDB(t)
	h := NewHandler(NewRepo(pool))
	hid := newHousehold(t, pool)
	right := sha256.Sum256([]byte("right"))
	wrong := sha256.Sum256([]byte("wrong"))
	if code, body := call(t, h.SetupKey, hid, map[string]any{"verifier": right[:], "key_salt": []byte("0123456789abcdef"), "key_version": 1}); code != http.StatusCreated {
		t.Fatalf("setup: %d %s", code, body)
	}

	// One member locks only themself.
	alice, bob := uuid.New(), uuid.New()
	for i := 0; i < verifyMaxFailures; i++ {
		if code, _ := verifyAs(h, hid, alice, wrong[:]); code != http.StatusUnprocessableEntity {
			t.Fatalf("alice wrong %d: %d", i+1, code)
		}
	}
	if code, _ := verifyAs(h, hid, alice, right[:]); code != http.StatusTooManyRequests {
		t.Fatalf("alice locked: %d", code)
	}
	if code, _ := verifyAs(h, hid, bob, right[:]); code != http.StatusOK {
		t.Fatalf("bob unlocks while alice is locked: %d", code)
	}

	// A correct password clears that member's count, but household failures
	// keep counting toward the ceiling.
	if code, _ := verifyAs(h, hid, bob, wrong[:]); code != http.StatusUnprocessableEntity {
		t.Fatal("bob wrong")
	}
	if code, _ := verifyAs(h, hid, bob, right[:]); code != http.StatusOK {
		t.Fatal("bob right")
	}
	failures := verifyMaxFailures + 1 // alice's 10 + bob's 1
	for failures < verifyHouseholdMaxFailures {
		member := uuid.New()
		for i := 0; i < verifyMaxFailures && failures < verifyHouseholdMaxFailures; i++ {
			if code, _ := verifyAs(h, hid, member, wrong[:]); code != http.StatusUnprocessableEntity {
				t.Fatalf("household failure %d: %d", failures+1, code)
			}
			failures++
		}
	}
	// Ceiling reached: even a fresh member with the right password waits.
	if code, retry := verifyAs(h, hid, uuid.New(), right[:]); code != http.StatusTooManyRequests || retry == "" {
		t.Fatalf("household ceiling: %d %q", code, retry)
	}
	// Another household is unaffected.
	other := newHousehold(t, pool)
	if code, _ := call(t, h.SetupKey, other, map[string]any{"verifier": right[:], "key_salt": []byte("0123456789abcdef"), "key_version": 1}); code != http.StatusCreated {
		t.Fatal("setup other")
	}
	if code, _ := verifyAs(h, other, uuid.New(), right[:]); code != http.StatusOK {
		t.Fatalf("other household: %d", code)
	}
}
