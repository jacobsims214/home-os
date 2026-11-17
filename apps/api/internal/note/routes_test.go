package note_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/internal/note"
)

const testIssuer = "https://dex.test/dex"

// testDB connects to TEST_DATABASE_URL (all migrations applied). Skips when unset.
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// router mirrors the note routes in cmd/api/main.go, behind the real
// RequireAuth middleware with the Postgres member resolver.
func router(pool *pgxpool.Pool, v *auth.Verifier) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		h := note.NewHandler(note.NewRepo(pool))
		r.Get("/api/v1/notes", h.List)
		r.Post("/api/v1/notes", h.Create)
		r.Get("/api/v1/notes/{id}", h.Get)
		r.Put("/api/v1/notes/{id}", h.Update)
		r.Patch("/api/v1/notes/{id}", h.Update)
		r.Delete("/api/v1/notes/{id}", h.Delete)
	})
	return r
}

type noteBody struct {
	Data struct {
		ID          string  `json:"id"`
		HouseholdID string  `json:"household_id"`
		EntityType  string  `json:"entity_type"`
		EntityID    string  `json:"entity_id"`
		Title       *string `json:"title"`
		Body        string  `json:"body"`
		AuthorID    *string `json:"author_id"`
		AuthorName  *string `json:"author_name"`
	} `json:"data"`
}

func call(t *testing.T, h http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, noteBody) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out noteBody
	if rec.Code < 300 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	}
	return rec, out
}

// member inserts a user with a household membership and returns a Dex-style
// token for them plus their user and household IDs.
func member(t *testing.T, pool *pgxpool.Pool, key *rsa.PrivateKey) (token, userID, hhID string) {
	t.Helper()
	ctx := context.Background()
	email := "note-" + uuid.NewString()[:8] + "@example.com"
	sub := "CiQ" + uuid.NewString() + "EgVsb2NhbA"
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, 'Note Member', 'x', $2) RETURNING id::text`,
		email, sub).Scan(&userID))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Note HH') RETURNING id::text`).Scan(&hhID))
	// Households cascade to notes, so this also removes the notes created below.
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hhID) })
	_, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'family_member')`, hhID, userID)
	require.NoError(t, err)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": testIssuer, "aud": "home-os-ui", "sub": sub, "email": email,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	token, err = tok.SignedString(key)
	require.NoError(t, err)
	return token, userID, hhID
}

func TestNoteRoutes_GetAndUpdateByID(t *testing.T) {
	pool := testDB(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{key.Public()}}
	v := auth.NewVerifierWithKeySet(testIssuer, ks, []string{"home-os-ui"}).
		WithResolver(auth.NewMemberResolver(pool))
	h := router(pool, v)

	token, userID, hhID := member(t, pool, key)
	otherToken, _, _ := member(t, pool, key)

	// Notes must be about a row of the caller's own family (migration 038).
	var entityID string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO properties (household_id, name) VALUES ($1, 'Note test property') RETURNING id::text`, hhID).Scan(&entityID))
	title := "Water heater"
	rec, created := call(t, h, http.MethodPost, "/api/v1/notes", token, map[string]any{
		"entity_type": "property", "entity_id": entityID, "title": title, "body": "Flushed in March.",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	id := created.Data.ID
	path := "/api/v1/notes/" + id

	t.Run("GET returns the full note", func(t *testing.T) {
		rec, got := call(t, h, http.MethodGet, path, token, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, id, got.Data.ID)
		assert.Equal(t, hhID, got.Data.HouseholdID)
		assert.Equal(t, "property", got.Data.EntityType)
		assert.Equal(t, entityID, got.Data.EntityID)
		require.NotNil(t, got.Data.Title)
		assert.Equal(t, title, *got.Data.Title)
		assert.Equal(t, "Flushed in March.", got.Data.Body)
		require.NotNil(t, got.Data.AuthorID)
		assert.Equal(t, userID, *got.Data.AuthorID)
		require.NotNil(t, got.Data.AuthorName)
		assert.Equal(t, "Note Member", *got.Data.AuthorName)
	})

	t.Run("PUT persists the change", func(t *testing.T) {
		rec, upd := call(t, h, http.MethodPut, path, token, map[string]any{"body": "Flushed in March and October."})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "Flushed in March and October.", upd.Data.Body)

		rec, got := call(t, h, http.MethodGet, path, token, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "Flushed in March and October.", got.Data.Body)
		require.NotNil(t, got.Data.Title)
		assert.Equal(t, title, *got.Data.Title, "fields not sent are kept")

		var dbBody string
		require.NoError(t, pool.QueryRow(context.Background(), `SELECT body FROM notes WHERE id = $1`, id).Scan(&dbBody))
		assert.Equal(t, "Flushed in March and October.", dbBody)
	})

	t.Run("PATCH persists the change", func(t *testing.T) {
		rec, _ := call(t, h, http.MethodPatch, path, token, map[string]any{"title": "Tankless heater"})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		_, got := call(t, h, http.MethodGet, path, token, nil)
		require.NotNil(t, got.Data.Title)
		assert.Equal(t, "Tankless heater", *got.Data.Title)
		assert.Equal(t, "Flushed in March and October.", got.Data.Body)
	})

	t.Run("another household gets 404", func(t *testing.T) {
		rec, _ := call(t, h, http.MethodGet, path, otherToken, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		rec, _ = call(t, h, http.MethodPut, path, otherToken, map[string]any{"body": "hijack"})
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})

	t.Run("no token is 401", func(t *testing.T) {
		rec, _ := call(t, h, http.MethodGet, path, "", nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

// #4564: a note whose title is close to an existing note comes back with
// possible_duplicates, and the note is still written. The pair is the war
// game's contradictory twins (#4945); the prompt is what stops the second one
// being created silently.
func TestNoteRoutes_NearDuplicatePrompt(t *testing.T) {
	pool := testDB(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{key.Public()}}
	v := auth.NewVerifierWithKeySet(testIssuer, ks, []string{"home-os-ui"}).
		WithResolver(auth.NewMemberResolver(pool))
	h := router(pool, v)

	token, _, hhID := member(t, pool, key)

	var entityID string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO properties (household_id, name) VALUES ($1, 'Pickup test property') RETURNING id::text`, hhID).Scan(&entityID))

	first := "Ava's school — teacher & pickup rules"
	rec, _ := call(t, h, http.MethodPost, "/api/v1/notes", token, map[string]any{
		"entity_type": "property", "entity_id": entityID, "title": first,
		"body": "Mrs. Patel. 2:50 car line. Green tag, front office.",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var firstResp struct {
		PossibleDuplicates []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"possible_duplicates"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &firstResp))
	assert.Empty(t, firstResp.PossibleDuplicates, "the first note has no twin yet")

	// The second note: the contradictory twin from the war game.
	second := "Ava's school: teacher & pickup"
	rec, _ = call(t, h, http.MethodPost, "/api/v1/notes", token, map[string]any{
		"entity_type": "property", "entity_id": entityID, "title": second,
		"body": "Ms. Delgado. 3:15 side door. Nut allergy, EpiPen in the office.",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var resp struct {
		Data struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"data"`
		PossibleDuplicates []struct {
			ID               string  `json:"id"`
			Title            string  `json:"title"`
			UpdatedLocalDate string  `json:"updated_local_date"`
			Excerpt          string  `json:"excerpt"`
			TitleSimilarity  float64 `json:"title_similarity"`
		} `json:"possible_duplicates"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// The note was saved (the prompt is never a gate) ...
	assert.NotEmpty(t, resp.Data.ID)
	assert.Equal(t, second, resp.Data.Title)
	// ... and the twin is named, with enough to decide.
	require.Len(t, resp.PossibleDuplicates, 1, "the twin must be reported")
	assert.Equal(t, first, resp.PossibleDuplicates[0].Title)
	assert.NotEmpty(t, resp.PossibleDuplicates[0].ID)
	assert.NotEmpty(t, resp.PossibleDuplicates[0].UpdatedLocalDate)
	assert.Contains(t, resp.PossibleDuplicates[0].Excerpt, "Mrs. Patel")
	assert.GreaterOrEqual(t, resp.PossibleDuplicates[0].TitleSimilarity, 0.6)

	// A distinct title stays quiet.
	rec, _ = call(t, h, http.MethodPost, "/api/v1/notes", token, map[string]any{
		"entity_type": "property", "entity_id": entityID, "title": "Water heater",
		"body": "Flushed in March.",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var quiet struct {
		PossibleDuplicates []struct{} `json:"possible_duplicates"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &quiet))
	assert.Empty(t, quiet.PossibleDuplicates)

	// A rename into a collision reports it and still renames.
	rec, _ = call(t, h, http.MethodPatch, "/api/v1/notes/"+resp.Data.ID, token, map[string]any{"title": first + " (old)"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var renamed struct {
		PossibleDuplicates []struct {
			Title string `json:"title"`
		} `json:"possible_duplicates"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &renamed))
	assert.NotEmpty(t, renamed.PossibleDuplicates)
	// A note is never its own candidate.
	for _, c := range renamed.PossibleDuplicates {
		assert.NotEqual(t, first+" (old)", c.Title)
	}
}
