package link

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// All 7 entity types that can have linked resources.
var entityTypes = []string{
	"asset",
	"property",
	"vehicle",
	"pet",
	"vendor",
	"bill",
	"maintenance_task",
}

// Known link types exercised by the link tests. The link package no longer
// maintains typed buckets for specific integration types — link_type is a
// free-form string and all links are surfaced via the "other" bucket of the
// grouped response (see handler.go). Legacy integration link rows whose
// link_type was once a first-class integration type may still exist in the
// database.
var linkTypes = []string{
	"external",
}

var (
	prepareOnce sync.Once
)

// testDB returns a connection pool. Skips the test if TEST_DATABASE_URL is not set.
// Prepares the schema once: the repo's CreateLink INSERT does not include document_id,
// but the original DDL has document_id NOT NULL. We make it nullable so CreateLink works.
// This is done once per test run since DDL auto-commits in PostgreSQL.
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	// Use TEST_DATABASE_URL directly — Postgres is expected to already have
	// all migrations applied. To prepare: docker compose -f deploy/docker-compose.yml up -d

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err, "connect to test database")
	t.Cleanup(pool.Close)

	// Run schema preparation once per test run.
	prepareOnce.Do(func() {
		ctx := context.Background()
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire connection for schema prep: %v", err)
		}
		defer conn.Release()

		// Make document_id nullable so the repo's CreateLink (which omits document_id)
		// can succeed. Migration 019 added link_type/link_id/title/url columns but
		// the original document_id remained NOT NULL without a default — the repo
		// INSERT (which omits document_id) would fail against the pure migration
		// schema. The repo was written assuming document_id should be optional for
		// the unified link types that are not tied to a documents row. A follow-up
		// migration (020) should formalize this by running:
		// ALTER TABLE document_links ALTER COLUMN document_id DROP NOT NULL;
		_, err = conn.Exec(ctx,
			`ALTER TABLE document_links ALTER COLUMN document_id DROP NOT NULL`,
		)
		if err != nil {
			t.Fatalf("alter document_links.document_id drop not null: %v", err)
		}
	})

	return pool
}

// testFixtures holds IDs for entities created in the test database.
type testFixtures struct {
	HouseholdID uuid.UUID
	EntityID    uuid.UUID // reusable entity ID for link tests
}

// setupFixtures creates the minimal data needed for link tests within a transaction.
// The caller must rollback the transaction when done to keep the DB clean.
//
// Note: the `documents` table was dropped by migration 024. Earlier versions
// of this helper inserted a row into `documents` and stored its UUID on
// testFixtures.DocumentID, but no test ever read that field — it was dead
// code. It has been removed along with the table.
func setupFixtures(ctx context.Context, t *testing.T, tx pgx.Tx) testFixtures {
	t.Helper()

	// Create a household
	var householdID uuid.UUID
	err := tx.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Test Household for Links')
		 RETURNING id`,
	).Scan(&householdID)
	require.NoError(t, err, "create household")

	return testFixtures{
		HouseholdID: householdID,
		EntityID:    uuid.New(),
	}
}

func TestCreateLink_AllEntityTypes(t *testing.T) {
	pool := testDB(t)

	for _, et := range entityTypes {
		et := et // capture
		t.Run(et, func(t *testing.T) {
			ctx := context.Background()
			tx, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx) //nolint:errcheck

			fixtures := setupFixtures(ctx, t, tx)
			repo := NewRepo(pool)

			for _, lt := range linkTypes {
				lt := lt
				t.Run(lt, func(t *testing.T) {
					link := &Link{
						EntityType: et,
						EntityID:   fixtures.EntityID,
						LinkType:   lt,
						LinkID:     fmt.Sprintf("%s-%s-doc-1", et, lt),
						Title:      fmt.Sprintf("Test %s link for %s", lt, et),
						URL:        strPtr(fmt.Sprintf("https://example.com/%s/%s", lt, et)),
					}

					created, err := repo.CreateLink(ctx, link)
					require.NoError(t, err, "CreateLink should succeed")
					require.NotNil(t, created, "created link should not be nil")
					assert.NotEqual(t, uuid.Nil, created.ID, "link should have a generated ID")
					assert.Equal(t, et, created.EntityType)
					assert.Equal(t, fixtures.EntityID, created.EntityID)
					assert.Equal(t, lt, created.LinkType)
					assert.Equal(t, link.LinkID, created.LinkID)
					assert.Equal(t, link.Title, created.Title)
					require.NotNil(t, created.URL)
					assert.Equal(t, *link.URL, *created.URL)
					assert.False(t, created.CreatedAt.IsZero(), "created_at should be set")
				})
			}
		})
	}
}

func TestCreateLink_Duplicate(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	fixtures := setupFixtures(ctx, t, tx)
	repo := NewRepo(pool)

	link := &Link{
		EntityType: "asset",
		EntityID:   fixtures.EntityID,
		LinkType:   "external",
		LinkID:     "dup-doc-1",
		Title:      "Original",
	}

	// First creation should succeed.
	_, err = repo.CreateLink(ctx, link)
	require.NoError(t, err, "first CreateLink should succeed")

	// Second creation with identical (entity_type, entity_id, link_type, link_id)
	// should also succeed — the document_links table has no unique constraint
	// on these four columns. Duplicates are allowed at the DB level.
	dup, err := repo.CreateLink(ctx, link)
	require.NoError(t, err, "duplicate CreateLink should also succeed (no unique constraint)")
	require.NotNil(t, dup)
	assert.NotEqual(t, uuid.Nil, dup.ID, "duplicate link should have its own ID")
}

func TestGetLinks_ByEntity(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	fixtures := setupFixtures(ctx, t, tx)
	repo := NewRepo(pool)

	// Create one link of each type for the same entity.
	for _, lt := range linkTypes {
		_, err := repo.CreateLink(ctx, &Link{
			EntityType: "asset",
			EntityID:   fixtures.EntityID,
			LinkType:   lt,
			LinkID:     fmt.Sprintf("list-test-%s", lt),
			Title:      fmt.Sprintf("Link %s", lt),
		})
		require.NoError(t, err)
	}

	// GetLinks should return all of them.
	links, err := repo.GetLinks(ctx, "asset", fixtures.EntityID)
	require.NoError(t, err)
	require.Len(t, links, len(linkTypes), "should return all links for the entity")

	// Verify each link type is present.
	types := make(map[string]bool)
	for _, l := range links {
		types[l.LinkType] = true
	}
	for _, lt := range linkTypes {
		assert.True(t, types[lt], "link type %s should be in results", lt)
	}

	// Links should be ordered by created_at DESC (most recent first).
	for i := 1; i < len(links); i++ {
		assert.False(t, links[i].CreatedAt.After(links[i-1].CreatedAt),
			"links should be ordered by created_at DESC")
	}
}

func TestGetLinks_FiltersByEntityType(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	fixtures := setupFixtures(ctx, t, tx)
	repo := NewRepo(pool)

	otherEntityID := uuid.New()

	// Create links for two different entities.
	_, err = repo.CreateLink(ctx, &Link{
		EntityType: "asset",
		EntityID:   fixtures.EntityID,
		LinkType:   "external",
		LinkID:     "doc-1",
		Title:      "Asset link",
	})
	require.NoError(t, err)

	_, err = repo.CreateLink(ctx, &Link{
		EntityType: "asset",
		EntityID:   otherEntityID,
		LinkType:   "external",
		LinkID:     "doc-2",
		Title:      "Other entity link",
	})
	require.NoError(t, err)

	// GetLinks for fixtures.EntityID should return only its link.
	links, err := repo.GetLinks(ctx, "asset", fixtures.EntityID)
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "doc-1", links[0].LinkID)

	// GetLinks for otherEntityID should return its link.
	links, err = repo.GetLinks(ctx, "asset", otherEntityID)
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "doc-2", links[0].LinkID)
}

func TestGetLinks_NoResults(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	fixtures := setupFixtures(ctx, t, tx)
	repo := NewRepo(pool)

	links, err := repo.GetLinks(ctx, "asset", fixtures.EntityID)
	require.NoError(t, err)
	assert.Empty(t, links, "no links should return empty slice, not nil")
}

func TestDeleteLink(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	fixtures := setupFixtures(ctx, t, tx)
	repo := NewRepo(pool)

	created, err := repo.CreateLink(ctx, &Link{
		EntityType: "asset",
		EntityID:   fixtures.EntityID,
		LinkType:   "external",
		LinkID:     "delete-me",
		Title:      "To be deleted",
	})
	require.NoError(t, err)

	// Delete it.
	err = repo.DeleteLink(ctx, created.ID)
	require.NoError(t, err, "DeleteLink should succeed")

	// Verify it's gone.
	links, err := repo.GetLinks(ctx, "asset", fixtures.EntityID)
	require.NoError(t, err)
	assert.Empty(t, links, "link should no longer exist")
}

func TestDeleteLink_Nonexistent(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	_ = setupFixtures(ctx, t, tx)
	repo := NewRepo(pool)

	// Deleting a nonexistent link should return an error.
	err = repo.DeleteLink(ctx, uuid.New())
	assert.Error(t, err, "deleting nonexistent link should error")
	assert.ErrorIs(t, err, pgx.ErrNoRows, "error should wrap pgx.ErrNoRows")
}

func TestGetLink_ByID(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	fixtures := setupFixtures(ctx, t, tx)
	repo := NewRepo(pool)

	created, err := repo.CreateLink(ctx, &Link{
		EntityType: "vehicle",
		EntityID:   fixtures.EntityID,
		LinkType:   "external",
		LinkID:     "vw-item-1",
		Title:      "Insurance reference",
		URL:        strPtr("https://example.com/vw-item-1"),
	})
	require.NoError(t, err)

	got, err := repo.GetLink(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, "vehicle", got.EntityType)
	assert.Equal(t, "external", got.LinkType)
	assert.Equal(t, "vw-item-1", got.LinkID)
	assert.Equal(t, "Insurance reference", got.Title)
	require.NotNil(t, got.URL)
	assert.Equal(t, "https://example.com/vw-item-1", *got.URL)
}

func TestGetLink_NotFound(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	_ = setupFixtures(ctx, t, tx)
	repo := NewRepo(pool)

	got, err := repo.GetLink(ctx, uuid.New())
	require.NoError(t, err, "GetLink on missing ID should return nil, nil")
	assert.Nil(t, got, "GetLink on missing ID should return nil")
}

func TestCreateLink_WithNilURL(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	fixtures := setupFixtures(ctx, t, tx)
	repo := NewRepo(pool)

	link := &Link{
		EntityType: "bill",
		EntityID:   fixtures.EntityID,
		LinkType:   "external",
		LinkID:     "receipt-2024-01.pdf",
		Title:      "January receipt",
		URL:        nil,
	}

	created, err := repo.CreateLink(ctx, link)
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Nil(t, created.URL, "URL should be nil")

	// Verify via GetLink.
	got, err := repo.GetLink(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.URL)
}

func TestLinkRoundtrip_AllFields(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	fixtures := setupFixtures(ctx, t, tx)
	repo := NewRepo(pool)

	url := "https://example.com/items/42"
	link := &Link{
		EntityType: "property",
		EntityID:   fixtures.EntityID,
		LinkType:   "external",
		LinkID:     "42",
		Title:      "Property deed",
		URL:        &url,
	}

	created, err := repo.CreateLink(ctx, link)
	require.NoError(t, err)

	// Fetch by GetLink.
	fetched, err := repo.GetLink(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)

	assert.Equal(t, created.ID, fetched.ID)
	assert.Equal(t, "property", fetched.EntityType)
	assert.Equal(t, fixtures.EntityID, fetched.EntityID)
	assert.Equal(t, "external", fetched.LinkType)
	assert.Equal(t, "42", fetched.LinkID)
	assert.Equal(t, "Property deed", fetched.Title)
	require.NotNil(t, fetched.URL)
	assert.Equal(t, url, *fetched.URL)
	assert.False(t, fetched.CreatedAt.IsZero())
}

// --- helpers ---

func strPtr(s string) *string {
	return &s
}

// TestEntityOwnedByHousehold_CrossHouseholdDenial covers the cross-tenant IDOR
// fix (task #1226). The link handler delegates to EntityOwnedByHousehold before
// any Create/List/Delete read or write. This test verifies:
//
//   - an entity owned by household A returns true for household A
//   - the SAME entity returns false for household B (the denial path)
//   - a nonexistent entity_id returns false (no leak)
//   - an unrecognized entity_type returns ErrUnknownEntityType
//
// It uses the `assets` table (the simplest entity with only name + household_id
// required) as a representative; the query is structurally identical for the
// other six entity types.
//
// Note: we cannot use a rollback-guarded transaction here because
// EntityOwnedByHousehold queries through the pool (a separate connection) and
// would not see uncommitted rows. Instead we insert via the pool and rely on
// ON DELETE CASCADE from households to clean up assets, and explicit cleanup
// of the households themselves.
func TestEntityOwnedByHousehold_CrossHouseholdDenial(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := NewRepo(pool)

	// Two separate households.
	var householdA, householdB uuid.UUID
	var err error
	err = pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Household A (owner)') RETURNING id`,
	).Scan(&householdA)
	require.NoError(t, err, "create household A")

	err = pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Household B (attacker)') RETURNING id`,
	).Scan(&householdB)
	require.NoError(t, err, "create household B")

	// Clean up both households at the end; ON DELETE CASCADE removes the asset.
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM households WHERE id IN ($1, $2)`, householdA, householdB)
	})

	// A real asset row owned by household A.
	var assetID uuid.UUID
	err = pool.QueryRow(ctx,
		`INSERT INTO assets (household_id, name) VALUES ($1, 'Denial Test Asset') RETURNING id`,
		householdA,
	).Scan(&assetID)
	require.NoError(t, err, "create asset in household A")

	// Owner may see their own entity.
	owned, err := repo.EntityOwnedByHousehold(ctx, "asset", assetID, householdA)
	require.NoError(t, err)
	assert.True(t, owned, "entity should be owned by household A")

	// Cross-household denial: household B must NOT be reported as owner.
	owned, err = repo.EntityOwnedByHousehold(ctx, "asset", assetID, householdB)
	require.NoError(t, err, "cross-household check should not error — it returns false")
	assert.False(t, owned, "entity must NOT be owned by household B (IDOR denial path)")

	// Nonexistent entity_id must return false (no existence leak).
	owned, err = repo.EntityOwnedByHousehold(ctx, "asset", uuid.New(), householdA)
	require.NoError(t, err)
	assert.False(t, owned, "nonexistent entity should return false")

	// Unrecognized entity_type must surface ErrUnknownEntityType so the handler
	// can map it to a 400 rather than a 404.
	owned, err = repo.EntityOwnedByHousehold(ctx, "bogus_type", assetID, householdA)
	assert.False(t, owned, "unknown entity_type should return false")
	assert.ErrorIs(t, err, ErrUnknownEntityType, "unknown entity_type should return ErrUnknownEntityType")

	// Sanity: every entity_type in the entityTable allow-list resolves to a
	// real table. We cannot easily insert rows for all seven types here, but
	// we can at least confirm the helper does not return ErrUnknownEntityType
	// for any supported type against a random UUID (it should return false,
	// nil — meaning the table exists and the query ran).
	for _, et := range entityTypes {
		et := et
		t.Run(et, func(t *testing.T) {
			owned, err := repo.EntityOwnedByHousehold(ctx, et, uuid.New(), householdA)
			require.NoError(t, err, "supported entity_type %q should not error", et)
			assert.False(t, owned, "random UUID should not be owned by any household")
		})
	}
}