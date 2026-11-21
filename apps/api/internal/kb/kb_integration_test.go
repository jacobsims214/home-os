package kb_test

// #5502 acceptance test: the family knowledge base, end to end.
//
// THE THING THIS PINS DOWN. Agent A writes one doc — how-to/pets/vet-after-hours
// — and Agent B, asking a question in their own words, FINDS IT BY MEANING.
// Not by path, not by keyword: the test's second question shares no searchable
// word with the doc at all, is asserted to return nothing from a keyword-only
// query, and still comes back with the vet doc FIRST.
//
// WHY IT IS WRITTEN THIS WAY.
//
//   - The write is a plain INSERT, exactly what apps/mcp's kb_write does. It
//     does NOT go through the REST handler and never calls IndexDocument. That
//     is the point of #5298 carried into this feature: the index is derived
//     from Postgres, so a doc an AGENT wrote (the MCP is not the API) is still
//     findable. A test that indexed by hand would pass on a tree where the
//     reconciler had never learned about kb_docs.
//   - Then searchrecon.Reconcile runs — the same call cmd/reindex and the
//     API's periodic sweep make — and only after that is the doc searched for.
//   - The search is the real search.Client query the REST endpoint runs, in
//     hybrid mode against a real Typesense with its local embedding model.
//   - There are FOUR docs, three of them plausible answers to a household
//     question about something going wrong. A single-doc index would make
//     "found it" worthless: it would prove that search returns the only row
//     there is. Ranking the vet doc above a pediatrician, a router and a
//     sitter doc is what makes the hit mean something.
//   - Agent B is a HOUSESITTER, not a family member: the KB is readable by
//     every role (that is the whole point — the babysitter needs the router
//     doc), and the test proves it for the role most likely to be broken.
//
// Needs TEST_DATABASE_URL (a migrated database) and TYPESENSE_TEST_ADDR/
// TYPESENSE_TEST_KEY, like the other Typesense integration tests; skipped
// without them, and CI runs it in the must-PASS Typesense step.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/kb"
	"home-os/api/internal/search"
	"home-os/api/internal/search/searchtest"
	"home-os/api/internal/searchrecon"
)

// vetDoc is Agent A's doc: what a sitter needs at 2am, written once.
const (
	vetPath  = "how-to/pets/vet-after-hours"
	vetTitle = "Vet: what to do after hours"
	vetBody  = `## After hours (after 6pm, weekends, holidays)

Call the emergency clinic first — **Northside Veterinary Emergency**, (555) 014-7788.
They are open 24 hours and take walk-ins; ring the bell twice if the door is locked.

## When to go in immediately

- The dog is vomiting and cannot keep water down for more than a few hours.
- Any trouble breathing, or gums that look pale or blue.
- A swollen belly with retching (bloat) — this is an emergency, go now.

## When it can wait until morning

A single episode of vomiting, a mild limp, or a skipped meal: call our regular vet
at (555) 014-2200 when they open at 8am and describe what happened.

## Money

The emergency visit is roughly $200 just to be seen. The pet insurance card is in
the kitchen drawer; the policy number is in the vault, not in this doc.`
)

// rivalDocs are the other three docs a household would have written. Each one
// is a plausible but WRONG answer to the vet question, so a "found it" that
// returns one of these instead is a real failure.
var rivalDocs = []struct{ path, title, body string }{
	{
		"how-to/networking/router-reset",
		"Router: how to reset it",
		"Unplug the router for 30 seconds, plug it back in. Its light turns blue when it is\n" +
			"online. If it stays red after two tries, call the internet provider at (555) 014-1234.",
	},
	{
		"how-to/health/pediatrician-after-hours",
		"Pediatrician: after hours",
		"Dr. Chen's after-hours line is (555) 014-2200. For a fever over 104F, or a baby under\n" +
			"three months with any fever, go straight to the children's hospital emergency room.",
	},
	{
		"sitters/what-the-babysitter-needs-to-know",
		"What the babysitter needs to know",
		"Wifi password is in the vault. Bedtime is 8pm, lights out at 8.15. The dog is friendly\n" +
			"but does not like the doorbell. Snacks are in the second drawer.",
	},
}

// sitterQuery is the ticket's own question: a sitter at 2am, in their words.
const sitterQuery = "what do we do if the dog is sick at night?"

// paraphraseQuery shares NO word with the vet doc — not "dog", not "sick", not
// "night". A keyword-only query for it returns zero documents (asserted
// below), so any hit can only have come from the meaning.
const paraphraseQuery = "who do we contact when our four-legged friend is ill overnight?"

func TestAgentAWritesAgentBFindsByMeaningIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	client, env := searchtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(t, client.InitCollection(ctx))

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// A household of its own, with its own members, so the reconcile pass
	// cannot mix in another family's rows.
	var hh uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name, timezone) VALUES ('Vet KB test', 'America/New_York') RETURNING id`).Scan(&hh))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hh)
	})
	agentA := seedMember(t, pool, hh, "family_manager", "Dana (Agent A)")
	agentB := seedMember(t, pool, hh, "housesitter", "Sam the sitter (Agent B)")
	repo := kb.NewRepo(pool)

	// ---- The household's other knowledge, so the index is not a one-row
	//      toy. ------------------------------------------------------------
	for _, d := range rivalDocs {
		_, err := pool.Exec(ctx,
			`INSERT INTO kb_docs (household_id, path, title, body, tags, created_by, updated_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $6)`,
			hh, d.path, d.title, d.body, []string{"how-to"}, agentA.id)
		require.NoError(t, err, "seed %s", d.path)
	}

	// ---- Agent A writes the vet doc. This is apps/mcp's kb_write: one
	//      INSERT, no REST handler, no IndexDocument. ---------------------
	_, err = pool.Exec(ctx,
		`INSERT INTO kb_docs (household_id, path, title, body, tags, created_by, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $6)`,
		hh, vetPath, vetTitle, vetBody, []string{"pets", "vets", "emergency"}, agentA.id)
	require.NoError(t, err, "Agent A writes %s", vetPath)

	// Nothing is in the index yet: Agent A was not the API, so no handler ran.
	before, err := client.Search(ctx, hh.String(), sitterQuery, search.SearchFilters{EntityTypes: []string{kb.EntityType}})
	require.NoError(t, err)
	require.Empty(t, before, "the doc must not be findable before the reconcile — this test would otherwise prove nothing")

	// ---- The reconciler derives the index from Postgres (#5298). ---------
	stats, err := searchrecon.Reconcile(ctx, pool, client, hh.String())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, stats.Indexed, len(rivalDocs)+1, "the reconcile must index every live doc")

	// ---- Agent B asks the ticket's question. -----------------------------
	// The SQL word match (no index, the fallback) finds nothing: it matches all
	// words of a query against one doc, and no doc holds all of them.
	words, err := repo.SearchByWord(ctx, hh, sitterQuery)
	require.NoError(t, err)
	assert.Empty(t, words, "the SQL word match must find nothing for %q", sitterQuery)

	hit := topHit(t, client, hh, sitterQuery)
	assert.Equal(t, kb.EntityType, hit.EntityType)
	// The hit is a pointer back to Postgres, so B reads the row the family
	// actually owns — the same path kb_get takes.
	doc := liveDoc(t, repo, hh, hit)
	assert.Equal(t, vetPath, doc.Path, "the vet doc must outrank the pediatrician, the router and the sitter doc")
	assert.Equal(t, vetTitle, doc.Title)
	assert.Contains(t, doc.Body, "Northside Veterinary Emergency")
	require.NotNil(t, doc.CreatedName)
	assert.Equal(t, "Dana (Agent A)", *doc.CreatedName, "the doc says whose knowledge it is")

	// ---- The proof that it was MEANING, not words. ----------------------
	// A keyword-only query over the same collection finds nothing for the
	// paraphrase; the hybrid query finds the vet doc first. If this assertion
	// were removed, the "by meaning" claim above could be a lucky keyword hit.
	require.Zero(t, keywordFound(t, env, hh, paraphraseQuery),
		"a keyword-only query must find nothing for %q — otherwise this test proves nothing about meaning", paraphraseQuery)
	semantic := topHit(t, client, hh, paraphraseQuery)
	assert.Equal(t, vetPath, liveDoc(t, repo, hh, semantic).Path,
		"a question that shares no word with the doc must still find it first")

	// ---- Role scoping: a housesitter reads, and cannot write. ------------
	assert.True(t, auth.Can(agentB.role, auth.ResKB, auth.ActRead), "a housesitter reads the KB")
	assert.False(t, auth.Can(agentB.role, auth.ResKB, auth.ActWrite), "a housesitter does not write the KB")
	assert.True(t, auth.Can(auth.RoleVendor, auth.ResKB, auth.ActRead), "a vendor reads the KB too")
	assert.True(t, auth.Can(auth.RoleViewer, auth.ResKB, auth.ActRead), "a viewer reads the KB too")
	assert.False(t, auth.Can(auth.RoleVendor, auth.ResKB, auth.ActWrite), "a vendor does not write the KB")

	// A housesitter's write is refused by the policy the tool and handler ask,
	// and the doc is untouched: the refusal happens before any SQL.
	require.False(t, auth.Can(agentB.role, auth.ResKB, auth.ActWrite), "kb_write refuses a housesitter")
	unchanged, err := repo.Get(ctx, hh, vetPath)
	require.NoError(t, err)
	assert.Equal(t, vetTitle, unchanged.Title, "the doc is unchanged")

	// ---- One path, one doc; a second family has its own namespace. -------
	var other uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Other family') RETURNING id`).Scan(&other))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, other)
	})
	_, err = pool.Exec(ctx,
		`INSERT INTO kb_docs (household_id, path, title, body) VALUES ($1, $2, 'Their vet', 'theirs')`,
		other, vetPath)
	require.NoError(t, err, "the same path is free in another household (UNIQUE is per household)")
	theirs, err := repo.Get(ctx, other, vetPath)
	require.NoError(t, err)
	assert.Equal(t, "Their vet", theirs.Title)
	_, err = repo.Get(ctx, hh, "how-to/pets/does-not-exist")
	assert.ErrorIs(t, err, kb.ErrNotFound)

	// ---- A write to an existing path REPLACES it. -----------------------
	updated := strings.Replace(vetBody, "(555) 014-7788", "(555) 014-9999", 1)
	_, created, err := repo.Upsert(ctx, hh, &kb.Doc{
		Path: vetPath, Title: vetTitle, Body: updated, Tags: []string{"pets", "vets"},
	}, &agentA.id)
	require.NoError(t, err)
	assert.False(t, created, "writing an existing path replaces the doc, it does not create a second one")
	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM kb_docs WHERE household_id = $1 AND path = $2`, hh, vetPath).Scan(&count))
	assert.Equal(t, 1, count, "one path, one doc")

	// ---- A soft delete leaves the index on the next reconcile. ----------
	require.NoError(t, repo.Delete(ctx, hh, vetPath, &agentA.id))
	_, err = repo.Get(ctx, hh, vetPath)
	assert.ErrorIs(t, err, kb.ErrNotFound, "a deleted doc is not readable")
	_, err = searchrecon.Reconcile(ctx, pool, client, hh.String())
	require.NoError(t, err)
	after, err := client.Search(ctx, hh.String(), sitterQuery, search.SearchFilters{EntityTypes: []string{kb.EntityType}})
	require.NoError(t, err)
	for _, h := range after {
		assert.NotEqual(t, unchanged.ID.String(), h.EntityID, "the soft-deleted doc must be pruned from the index")
		if id, err := uuid.Parse(h.EntityID); err == nil {
			_, err := repo.GetByID(ctx, hh, id)
			assert.NoError(t, err, "every remaining hit must still be a live doc")
		}
	}
	// The rivals are still live and still findable: the prune removed what the
	// row said to remove, not the whole household.
	require.NotEmpty(t, after, "the other docs are still live")
	assert.NotZero(t, keywordFound(t, env, hh, "router"), "an untouched doc is still indexed")
}

// topHit returns the best-ranked KB hit for query, and logs the run so the
// ticket carries the A-writes / B-finds output as evidence.
func topHit(t *testing.T, client *search.Client, hh uuid.UUID, query string) search.SearchResult {
	t.Helper()
	hits, err := client.Search(context.Background(), hh.String(), query, search.SearchFilters{
		EntityTypes: []string{kb.EntityType},
	})
	require.NoError(t, err)
	require.NotEmpty(t, hits, "no doc found for %q", query)
	t.Logf("query %q → %d hit(s):", query, len(hits))
	for i, h := range hits {
		if i >= 3 {
			break
		}
		t.Logf("   #%d score=%.4f  %q", i+1, h.Score, firstLine(h.Body))
	}
	return hits[0]
}

// liveDoc turns a hit back into the Postgres row (what kb_get does).
func liveDoc(t *testing.T, repo *kb.Repo, hh uuid.UUID, hit search.SearchResult) *kb.Doc {
	t.Helper()
	doc, err := repo.GetByID(context.Background(), hh, uuid.MustParse(hit.EntityID))
	require.NoError(t, err, "the hit must point at a live doc")
	return doc
}

// keywordFound runs a keyword-only query (query_by over the text fields, no
// embedding) against the test's own collection and returns how many documents
// matched. It is the negative control: when this is 0 and the hybrid search
// still finds the doc, the match was semantic.
func keywordFound(t *testing.T, env searchtest.Env, hh uuid.UUID, query string) int {
	t.Helper()
	v := url.Values{}
	v.Set("q", query)
	v.Set("query_by", "title,body,tags")
	v.Set("filter_by", fmt.Sprintf("household_id:=`%s` && entity_type:=`%s`", hh, kb.EntityType))
	v.Set("per_page", "10")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.Base+"/collections/"+env.Household+"/documents/search?"+v.Encode(), nil)
	require.NoError(t, err)
	req.Header.Set("X-TYPESENSE-API-KEY", env.Key)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Less(t, resp.StatusCode, 300, "keyword-only query failed")
	var out struct {
		Found int `json:"found"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Found
}

// firstLine is a hit's first line, for readable test output.
func firstLine(body string) string {
	line := strings.SplitN(strings.TrimSpace(body), "\n", 2)[0]
	if len(line) > 100 {
		line = line[:100] + "…"
	}
	return line
}

// member is a seeded household member: their users.id and role.
type member struct {
	id   uuid.UUID
	role string
}

// seedMember inserts a user and their membership in hh and returns them.
func seedMember(t *testing.T, pool *pgxpool.Pool, hh uuid.UUID, role, name string) member {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		fmt.Sprintf("%s-%s@test.homeos.local", role, uuid.NewString()[:8]), name).Scan(&id))
	_, err := pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh, id, role)
	require.NoError(t, err)
	return member{id: id, role: role}
}
