package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/testutil"
)

// seedUser inserts a user (dex_sub may be empty for NULL) and returns its id.
func seedUser(t *testing.T, pool *pgxpool.Pool, email, dexSub string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	var sub any
	if dexSub != "" {
		sub = dexSub
	}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, 'U', 'x', $2) RETURNING id`,
		email, sub).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

// seedMembership adds userID to a new household with the given role and
// membership created_at, and returns the household id.
func seedMembership(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, role string, createdAt time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var hid uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO households (name) VALUES ('H') RETURNING id`).Scan(&hid); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)`,
		hid, userID, role, createdAt); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return hid
}

func dexSubOf(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) *string {
	t.Helper()
	var s *string
	if err := pool.QueryRow(context.Background(), `SELECT dex_sub FROM users WHERE id = $1`, userID).Scan(&s); err != nil {
		t.Fatalf("read dex_sub: %v", err)
	}
	return s
}

func TestResolveMember(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	t0 := time.Now().Add(-48 * time.Hour)

	t.Run("by dex_sub", func(t *testing.T) {
		uid := seedUser(t, pool, "bysub@test.local", "sub-bysub")
		hid := seedMembership(t, pool, uid, "owner", t0)

		// Email deliberately differs: sub wins.
		m, err := auth.ResolveMember(ctx, pool, "sub-bysub", "someone-else@test.local")
		if err != nil {
			t.Fatalf("ResolveMember: %v", err)
		}
		if m.UserID != uid.String() || m.HouseholdID != hid.String() || m.Role != "owner" {
			t.Fatalf("got %+v, want user %s household %s owner", m, uid, hid)
		}
	})

	t.Run("by email case-insensitive backfills dex_sub", func(t *testing.T) {
		uid := seedUser(t, pool, "Legacy@Test.local", "")
		hid := seedMembership(t, pool, uid, "family_member", t0)

		m, err := auth.ResolveMember(ctx, pool, "sub-legacy", "  legacy@TEST.local ")
		if err != nil {
			t.Fatalf("ResolveMember: %v", err)
		}
		if m.UserID != uid.String() || m.HouseholdID != hid.String() || m.Role != "family_member" {
			t.Fatalf("got %+v", m)
		}
		if got := dexSubOf(t, pool, uid); got == nil || *got != "sub-legacy" {
			t.Fatalf("dex_sub = %v, want backfilled sub-legacy", got)
		}
		// Next lookup resolves by sub even with a changed email.
		if m2, err := auth.ResolveMember(ctx, pool, "sub-legacy", "renamed@test.local"); err != nil || m2.UserID != uid.String() {
			t.Fatalf("second lookup by sub: %+v, %v", m2, err)
		}
	})

	t.Run("email match with different dex_sub is not overwritten", func(t *testing.T) {
		uid := seedUser(t, pool, "keep@test.local", "sub-original")
		seedMembership(t, pool, uid, "owner", t0)

		m, err := auth.ResolveMember(ctx, pool, "sub-other", "keep@test.local")
		if err != nil {
			t.Fatalf("ResolveMember: %v", err)
		}
		if m.UserID != uid.String() {
			t.Fatalf("user = %s, want %s", m.UserID, uid)
		}
		if got := dexSubOf(t, pool, uid); got == nil || *got != "sub-original" {
			t.Fatalf("dex_sub = %v, want sub-original kept", got)
		}
	})

	t.Run("several households resolve to the oldest membership", func(t *testing.T) {
		uid := seedUser(t, pool, "multi@test.local", "sub-multi")
		// Insert the newer membership first so insertion order can't decide.
		seedMembership(t, pool, uid, "family_member", t0.Add(time.Hour))
		oldest := seedMembership(t, pool, uid, "family_manager", t0)
		seedMembership(t, pool, uid, "owner", t0.Add(2*time.Hour))

		for i := 0; i < 3; i++ {
			m, err := auth.ResolveMember(ctx, pool, "sub-multi", "multi@test.local")
			if err != nil {
				t.Fatalf("ResolveMember: %v", err)
			}
			if m.HouseholdID != oldest.String() || m.Role != "family_manager" {
				t.Fatalf("got household %s role %s, want oldest %s family_manager", m.HouseholdID, m.Role, oldest)
			}
		}
	})

	noMember := []struct {
		name, sub, email string
		setup            func(t *testing.T)
	}{
		{name: "user without membership", sub: "sub-lonely", email: "lonely@test.local",
			setup: func(t *testing.T) { seedUser(t, pool, "lonely@test.local", "sub-lonely") }},
		{name: "unknown sub and email", sub: "sub-nobody", email: "nobody@test.local"},
		{name: "unknown sub, empty email", sub: "sub-nobody", email: ""},
		{name: "empty sub", sub: "", email: "bysub@test.local"},
	}
	for _, tc := range noMember {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup(t)
			}
			_, err := auth.ResolveMember(ctx, pool, tc.sub, tc.email)
			if !errors.Is(err, auth.ErrNoMembership) {
				t.Fatalf("err = %v, want ErrNoMembership", err)
			}
		})
	}
}

func TestResolveMember_DBError(t *testing.T) {
	pool := testutil.NewPool(t)
	pool.Close()
	_, err := auth.ResolveMember(context.Background(), pool, "sub", "x@test.local")
	if !errors.Is(err, auth.ErrMemberLookup) {
		t.Fatalf("err = %v, want ErrMemberLookup", err)
	}
}

func TestVerifyTokenAndEnrich(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	iss := testutil.NewTokenIssuer(t)
	v := iss.Verifier(pool)

	uid := seedUser(t, pool, "member@test.local", "")
	hid := seedMembership(t, pool, uid, "owner", time.Now())

	claims, err := v.VerifyTokenAndEnrich(ctx, pool, iss.Token("sub-member", "member@test.local"))
	if err != nil {
		t.Fatalf("VerifyTokenAndEnrich: %v", err)
	}
	if claims.Subject != "sub-member" || claims.UserID != uid.String() ||
		claims.HouseholdID != hid.String() || claims.Role != "owner" {
		t.Fatalf("claims = %+v", claims)
	}

	if _, err := v.VerifyTokenAndEnrich(ctx, pool, iss.Token("sub-stranger", "stranger@test.local")); !errors.Is(err, auth.ErrNoMembership) {
		t.Fatalf("stranger: err = %v, want ErrNoMembership", err)
	}

	// A token signed by another key is a token error, not a membership one.
	other := testutil.NewTokenIssuer(t)
	_, err = v.VerifyTokenAndEnrich(ctx, pool, other.Token("sub-member", "member@test.local"))
	if err == nil || errors.Is(err, auth.ErrNoMembership) || errors.Is(err, auth.ErrMemberLookup) {
		t.Fatalf("bad signature: err = %v, want plain verification error", err)
	}
	if !strings.Contains(err.Error(), "verify token") {
		t.Fatalf("bad signature: err = %v", err)
	}
}
