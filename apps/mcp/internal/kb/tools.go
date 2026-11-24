package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// Register adds the knowledge-base tools to srv: kb_search, kb_get, kb_write
// and kb_list. cmd/mcp calls it once at startup, so changes to these tools stay
// inside this package.
func Register(srv *server.Server, deps server.Deps) {
	var ts *typesense
	if deps.Config != nil {
		ts = newTypesense(deps.Config.TypesenseHost, deps.Config.TypesensePort, deps.Config.TypesenseAPIKey)
	}
	srv.RegisterTool(NewKBSearchTool(deps.Pool, ts))
	srv.RegisterTool(NewKBGetTool(deps.Pool))
	srv.RegisterTool(NewKBWriteTool(deps.Pool))
	srv.RegisterTool(NewKBListTool(deps.Pool))
}

// --- response shapes --------------------------------------------------------

// docData is one full doc, as kb_get and kb_write return it.
type docData struct {
	Path      string    `json:"path" jsonschema_description:"The doc's address in the family's namespace, e.g. how-to/pets/vet-after-hours."`
	Title     string    `json:"title"`
	Body      string    `json:"body" jsonschema_description:"The doc's markdown body."`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at" jsonschema_description:"When the doc last changed."`
	CreatedBy *string   `json:"created_by,omitempty" jsonschema_description:"Who first wrote it."`
	UpdatedBy *string   `json:"updated_by,omitempty" jsonschema_description:"Who last changed it."`
}

// hitData is one search or list row: enough to decide whether to read it.
type hitData struct {
	Path      string    `json:"path" jsonschema_description:"Pass this to kb_get to read the whole doc."`
	Title     string    `json:"title"`
	Excerpt   string    `json:"excerpt,omitempty" jsonschema_description:"The opening of the doc (or the list row's preview)."`
	Tags      []string  `json:"tags,omitempty"`
	UpdatedAt time.Time `json:"updated_at" jsonschema_description:"When the doc last changed."`
	UpdatedBy *string   `json:"updated_by,omitempty" jsonschema_description:"Who last changed it, so you know whose knowledge this is."`
	Score     *float64  `json:"score,omitempty" jsonschema_description:"How well the doc matched the query (search only, higher is better)."`
}

// writeData is kb_write's payload.
type writeData struct {
	Doc     docData `json:"doc"`
	Created bool    `json:"created" jsonschema_description:"true when this call created the doc, false when it replaced an existing one."`
}

func toDocData(d *Doc) docData {
	return docData{
		Path: d.Path, Title: d.Title, Body: d.Body, Tags: d.Tags,
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(),
		CreatedBy: d.CreatedName, UpdatedBy: d.UpdatedName,
	}
}

func toHitData(s Summary) hitData {
	out := hitData{
		Path: s.Path, Title: s.Title, Excerpt: s.Excerpt, Tags: s.Tags,
		UpdatedAt: s.UpdatedAt.UTC(), UpdatedBy: s.UpdatedBy,
	}
	if s.Score != 0 {
		score := s.Score
		out.Score = &score
	}
	return out
}

// --- kb_search --------------------------------------------------------------

// NewKBSearchTool builds kb_search: find docs by meaning and keyword.
func NewKBSearchTool(pool *pgxpool.Pool, ts *typesense) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("kb_search",
		append([]mcp.ToolOption{
			mcp.WithDescription(
				"Search the family's knowledge base by MEANING, not by path: \"what do we do if the dog is sick at night?\" finds how-to/pets/vet-after-hours. " +
					"Read this BEFORE saying you don't know how this family does something — another agent or family member may already have written it up for exactly this. " +
					"The family's durable how-tos, phone numbers and routines live here (distinct from notes, which are transient, and files, which are uploads). " +
					"Returns each doc's path, title, excerpt, when it last changed and who changed it. Use kb_get with a path to read the whole doc."),
			mcp.WithString("query",
				mcp.Description("What you want to know, in the caller's own words — a question works better than keywords, e.g. \"the dog is sick at night, who do we call\"."),
				mcp.Required()),
			mcp.WithNumber("limit",
				mcp.Description("How many docs to return, 1-50. Default 20.")),
			respond.ListSchema[hitData](),
		}, annotations("Search the family knowledge base", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		query := strings.TrimSpace(argString(req, "query", "q", "search"))
		if query == "" {
			return respond.Invalid("query", "query is required.",
				"Pass what you want to know, e.g. query=\"what do we do if the dog is sick at night\".",
				map[string]any{"query": "what do we do if the dog is sick at night"}), nil
		}
		if len(query) > MaxQuery {
			return respond.Invalid("query", "query must be at most 500 characters.",
				"Shorten the query to the question you are asking.", nil), nil
		}
		limit := argLimit(req)

		// Search by MEANING. A failed semantic search must never pass for
		// "nothing matched" (#5621): the word fallback below is a lesser
		// search, so when the index is unreachable the result says so in the
		// summary AND in searched_range, and when there is nothing usable to
		// return at all the call FAILS. The alternative is what cost the family
		// this bug — the MCP pod shipped without TYPESENSE_* env vars, every
		// meaning search errored here, the error was dropped, and a paraphrase
		// of a doc that existed came back as a normal empty list. An agent read
		// that as "there is no such document" and would have said so.
		searchedBy := "meaning"
		var searched *respond.Range
		hits, err := searchByMeaning(ctx, pool, ts, c.householdID, query, limit)
		if err != nil || len(hits) == 0 {
			// No Typesense, a collection without an embedding field, or an
			// index that has not caught up yet: fall back to words so a doc
			// written a moment ago is still findable by its title and path.
			wordHits, werr := searchByWords(ctx, pool, c.householdID, query, limit)
			if werr != nil {
				return respond.Failed("search the knowledge base", werr), nil
			}
			if err != nil {
				// Degraded: words answered instead of the index.
				if len(wordHits) == 0 {
					// ...and they found nothing either. An empty list here is
					// the exact output that made an agent tell a family a
					// document did not exist, so refuse instead of returning it.
					slog.Error("kb_search: meaning search unavailable and no word matches; failing rather than reporting an empty result",
						"household_id", c.householdID, "error", err)
					return respond.Fail(respond.Error{
						Message: fmt.Sprintf("Could not search the knowledge base by meaning: %v. Matching by words found no doc either, so this result says nothing about whether one exists.", err),
						Fix:     "Tell the user that knowledge base search is impaired right now, and call kb_list to read the doc paths and titles directly. Never tell them the family has no such document.",
					}), nil
				}
				slog.Warn("kb_search: meaning search unavailable; answering with word matches",
					"household_id", c.householdID, "error", err)
				searchedBy = "words"
				searched = respond.FallbackRange("literal words", err.Error())
			}
			if len(wordHits) > 0 {
				hits = wordHits
				if searchedBy == "meaning" {
					// The index was fine and simply had no match; the words
					// only add a doc it has not indexed yet. Still say which
					// search answered, as kb_search always has.
					searchedBy = "words"
				}
			}
		}

		var next []string
		if len(hits) == 0 {
			next = append(next,
				"Call kb_list to see every doc path and title; the doc may be under a different wording.",
				"If this is knowledge the family will want again, write it with kb_write (suggest an intent-style path such as how-to/pets/vet-after-hours).")
		} else {
			next = append(next, fmt.Sprintf("Read the whole doc with kb_get path=%q.", hits[0].Path))
		}
		summary := fmt.Sprintf("Found %s by %s for %q.", plural(len(hits), "knowledge base doc"), searchedBy, query)
		if searched != nil {
			summary = fmt.Sprintf("Semantic search is unavailable, so this is a WORD match only: found %s by %s for %q. A doc worded differently was never looked for.",
				plural(len(hits), "knowledge base doc"), searchedBy, query)
		}
		return respond.ListIn(summary, toHits(hits), respond.Page{Total: len(hits), Narrow: "Add more words from the doc you expect, or call kb_list to browse."}, searched, next...), nil
	}
	return "kb_search", tool, handler
}

func toHits(in []Summary) []hitData {
	out := make([]hitData, 0, len(in))
	for _, s := range in {
		out = append(out, toHitData(s))
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// --- kb_get -----------------------------------------------------------------

// NewKBGetTool builds kb_get: one full doc by path.
func NewKBGetTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("kb_get",
		append([]mcp.ToolOption{
			mcp.WithDescription(
				"Read one knowledge base doc in full, by its path (e.g. how-to/pets/vet-after-hours). " +
					"Use kb_search first when you don't know the path — it finds docs by meaning. " +
					"A doc is the family's durable how-to, written by one agent or family member for the next one to read."),
			mcp.WithString("path",
				mcp.Description("The doc's path from kb_search or kb_list, e.g. how-to/pets/vet-after-hours. Case and spaces are normalized."),
				mcp.Required()),
			respond.Schema[docData](),
		}, annotations("Read a knowledge base doc", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		raw := argString(req, "path", "doc", "name")
		if raw == "" {
			return respond.Invalid("path", "path is required.",
				"Pass the doc's path as kb_search or kb_list returned it.",
				map[string]any{"path": "how-to/pets/vet-after-hours"}), nil
		}
		path, err := cleanPath(raw)
		if err != nil {
			return respond.Invalid("path", err.Error(),
				"Pass a path of lower-case segments separated by '/', e.g. how-to/pets/vet-after-hours.",
				map[string]any{"path": "how-to/pets/vet-after-hours"}), nil
		}
		d, err := getDoc(ctx, pool, c.householdID, path)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("No knowledge base doc at %q.", path),
				Fix:     "Call kb_search with the question in your own words (it matches by meaning), or kb_list to browse the paths.",
				Field:   "path",
				Example: map[string]any{"path": "how-to/pets/vet-after-hours"},
			}), nil
		}
		if err != nil {
			return respond.Failed("read the doc", err), nil
		}
		summary := fmt.Sprintf("%s (last changed %s).", d.Title, whenBy(d))
		return respond.OK(summary, toDocData(d), "Change it with kb_write path="+path+" (an update REPLACES the doc)."), nil
	}
	return "kb_get", tool, handler
}

// whenBy describes a doc's last change and who made it, for a summary line.
func whenBy(d *Doc) string {
	s := d.UpdatedAt.UTC().Format("2006-01-02")
	if d.UpdatedName != nil && *d.UpdatedName != "" {
		s += " by " + *d.UpdatedName
	}
	return s
}

// --- kb_write ---------------------------------------------------------------

// NewKBWriteTool builds kb_write: create or REPLACE the doc at a path.
func NewKBWriteTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("kb_write",
		append([]mcp.ToolOption{
			mcp.WithDescription(
				"Write durable knowledge into the family's knowledge base for another agent or family member to find later. " +
					"The doc is addressed by PATH, so writing to a path that already exists REPLACES that doc entirely: its previous title, body and tags are gone. " +
					"Read it with kb_get first if you only mean to add to it. " +
					"Use this for how-this-family-does-it knowledge (routines, who to call, what the sitter needs), not for one-off notes. " +
					"Never put a password, key or account number in a doc: store it in the secrets feature and say where it is. " +
					"The doc becomes searchable by meaning (kb_search) as soon as the index catches up."),
			mcp.WithString("path",
				mcp.Description("Where the doc lives in the family's namespace, e.g. how-to/networking/router-reset, pets/vet-after-hours, sitters/what-the-babysitter-needs-to-know. Lower-case segments separated by '/'."),
				mcp.Required()),
			mcp.WithString("title",
				mcp.Description("A short title, e.g. \"Vet: what to do after hours\"."),
				mcp.Required()),
			mcp.WithString("body",
				mcp.Description("The doc's markdown body. Write it so the next reader can act on it: the phone number, the steps, when to use them."),
				mcp.Required()),
			mcp.WithArray("tags",
				mcp.Description("Optional tags, e.g. [\"pets\", \"vets\", \"emergency\"]."),
				mcp.WithStringItems()),
			respond.Schema[writeData](),
		}, annotations("Write a knowledge base doc", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "kb_write"), nil
		}
		rawPath := argString(req, "path", "doc_path", "name")
		path, err := cleanPath(rawPath)
		if err != nil {
			return respond.Invalid("path", err.Error(),
				"Pass a path of lower-case segments separated by '/', e.g. how-to/pets/vet-after-hours.",
				map[string]any{"path": "how-to/pets/vet-after-hours", "title": "Vet: what to do after hours", "body": "..."}), nil
		}
		title := strings.TrimSpace(argString(req, "title"))
		if title == "" {
			return respond.Invalid("title", "title is required.", "Give the doc a short title.", nil), nil
		}
		if len([]rune(title)) > MaxTitle {
			return respond.Invalid("title", "title must be at most 255 characters.", "Shorten the title.", nil), nil
		}
		body := argString(req, "body", "content", "text")
		if body == "" {
			return respond.Invalid("body", "body is required.", "Write the doc's content.", nil), nil
		}
		if len(body) > MaxBody {
			return respond.Invalid("body", "body must be at most 256 KiB.", "Split it into two docs under different paths.", nil), nil
		}
		tags, err := cleanTags(argStringList(req, "tags"))
		if err != nil {
			return respond.Invalid("tags", err.Error(), "Pass at most 30 short tags.", nil), nil
		}

		// Read what is there BEFORE writing, so a replacement can say what it
		// replaced (the caller asked for an upsert, but a silent clobber is
		// how one agent deletes another's work).
		existing, getErr := getDoc(ctx, pool, c.householdID, path)
		hadDoc := getErr == nil

		saved, created, err := upsert(ctx, pool, c.householdID, path, title, body, tags, c.userID)
		if err != nil {
			return respond.Failed("write the doc", err), nil
		}

		var summary string
		var next []string
		switch {
		case created:
			summary = fmt.Sprintf("Wrote a new knowledge base doc at %s.", path)
			next = append(next, "Another agent or family member will find this with kb_search by asking about it in their own words.")
		case hadDoc:
			summary = fmt.Sprintf("REPLACED the knowledge base doc at %s (it had been changed %s by %s).",
				path, existing.UpdatedAt.UTC().Format("2006-01-02 15:04"), nameOrDash(existing.UpdatedName))
			next = append(next, "The previous title, body and tags are gone; the doc is now exactly what you wrote.")
		default:
			summary = fmt.Sprintf("Restored a knowledge base doc at %s.", path)
			next = append(next, "A deleted doc at this path was brought back with your text.")
		}
		next = append(next, "Check it reads the way you meant with kb_get path="+path+".")
		return respond.OK(summary, writeData{Doc: toDocData(saved), Created: created}, next...), nil
	}
	return "kb_write", tool, handler
}

func nameOrDash(s *string) string {
	if s == nil || *s == "" {
		return "someone"
	}
	return *s
}

// --- kb_list ----------------------------------------------------------------

// NewKBListTool builds kb_list: every doc's path and title, for discovery.
func NewKBListTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("kb_list",
		append([]mcp.ToolOption{
			mcp.WithDescription(
				"List the family's knowledge base: every doc's path and title, with a short excerpt. " +
					"Use this to see WHAT the family has written down before asking a question or writing a doc — it tells you which paths already exist, so you extend a doc instead of creating a near-duplicate of it. " +
					"kb_search finds docs by meaning; kb_list shows the whole shelf."),
			mcp.WithString("prefix",
				mcp.Description("Optional namespace to browse, e.g. \"how-to\" or \"pets\". Omit for everything.")),
			respond.ListSchema[hitData](),
		}, annotations("List the family knowledge base", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		prefix := strings.TrimSpace(argString(req, "prefix", "namespace", "under"))
		if prefix != "" {
			clean, err := cleanPath(prefix)
			if err != nil {
				return respond.Invalid("prefix", err.Error(),
					"Pass a namespace such as \"how-to\" or \"pets\", or omit it to list everything.", map[string]any{"prefix": "how-to"}), nil
			}
			prefix = clean
		}
		rows, err := pool.Query(ctx,
			`SELECT d.path, d.title, d.tags, d.updated_at, uu.name, left(d.body, 400)
			   FROM kb_docs d LEFT JOIN users uu ON uu.id = d.updated_by
			  WHERE d.household_id = $1 AND d.deleted_at IS NULL
			    AND ($2 = '' OR d.path = $2 OR d.path LIKE $2 || '/%')
			  ORDER BY d.path`, c.householdID, prefix)
		if err != nil {
			return respond.Failed("list the knowledge base", err), nil
		}
		defer rows.Close()
		var out []hitData
		for rows.Next() {
			var s Summary
			var body string
			if err := rows.Scan(&s.Path, &s.Title, &s.Tags, &s.UpdatedAt, &s.UpdatedBy, &body); err != nil {
				return respond.Failed("list the knowledge base", err), nil
			}
			s.Excerpt = excerpt(body)
			out = append(out, toHitData(s))
		}
		if err := rows.Err(); err != nil {
			return respond.Failed("list the knowledge base", err), nil
		}
		summary := fmt.Sprintf("The family knowledge base has %s.", plural(len(out), "doc"))
		if prefix != "" {
			summary = fmt.Sprintf("Under %s the family knowledge base has %s.", prefix, plural(len(out), "doc"))
		}
		var next []string
		if len(out) > 0 {
			next = append(next, "Read one with kb_get path=…; find one by meaning with kb_search query=….")
		} else if canWrite(c.role) {
			next = append(next, "It is empty. Write the first doc with kb_write path=… title=… body=….")
		}
		return respond.List(summary, out, respond.Page{Total: len(out)}, next...), nil
	}
	return "kb_list", tool, handler
}

// --- Typesense --------------------------------------------------------------

// typesense is the small client kb_search needs: a hybrid (keyword + vector)
// query against the household_search collection the API owns. It is the same
// index every other record lives in — there is no second integration — and
// only the query lives here, never the indexing (see the package doc).
type typesense struct {
	baseURL string
	apiKey  string
	client  *http.Client
	// collection is the household_search collection to query. Empty means the
	// default; a test sets its own so it never shares an index with another
	// run (see WithCollection).
	collection string
}

// collectionName returns the collection to query: the default, or the one a
// test pinned.
func (t *typesense) collectionName() string {
	if t.collection != "" {
		return t.collection
	}
	return collectionName
}

// newTypesense returns nil when Typesense is not configured.
func newTypesense(host, port, apiKey string) *typesense {
	if apiKey == "" || host == "" {
		return nil
	}
	base := "http://" + host
	if port != "" {
		base += ":" + port
	}
	return &typesense{baseURL: base, apiKey: apiKey, client: &http.Client{Timeout: 10 * time.Second}}
}

// searchByMeaning asks household_search for the docs matching query by
// meaning, restricted to this household's kb_doc documents, and returns the
// live docs among the hits in ranked order.
//
// Every hit is re-read from Postgres: a doc soft-deleted while the index was
// stale must not come back, and the author and excerpt come from the source
// of truth (#5298).
func searchByMeaning(ctx context.Context, pool *pgxpool.Pool, ts *typesense, householdID uuid.UUID, query string, limit int) ([]Summary, error) {
	if ts == nil {
		return nil, errors.New("no search index configured")
	}
	v := url.Values{}
	v.Set("q", query)
	// query_by includes the embedding field so Typesense embeds the query
	// with the same local model the documents were embedded with and fuses
	// keyword and vector ranks by rank.
	v.Set("query_by", "title,body,tags,"+embeddingField)
	v.Set("vector_query", fmt.Sprintf("%s:([], distance_threshold: %s)",
		embeddingField, strconv.FormatFloat(semanticDistanceThreshold, 'f', -1, 64)))
	v.Set("filter_by", fmt.Sprintf("household_id:=`%s` && entity_type:=`%s`", householdID, entityType))
	v.Set("include_fields", "id,entity_id,title,body")
	v.Set("exclude_fields", embeddingField)
	v.Set("per_page", strconv.Itoa(max(limit, 10)))

	var resp struct {
		Hits []struct {
			Document struct {
				EntityID string `json:"entity_id"`
			} `json:"document"`
			HybridSearchInfo *struct {
				RankFusionScore float64 `json:"rank_fusion_score"`
			} `json:"hybrid_search_info"`
			TextMatchInfo *struct {
				Score string `json:"score"`
			} `json:"text_match_info"`
		} `json:"hits"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.baseURL+"/collections/"+ts.collectionName()+"/documents/search?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-TYPESENSE-API-KEY", ts.apiKey)
	httpResp, err := ts.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("typesense returned %d", httpResp.StatusCode)
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, err
	}

	out := make([]Summary, 0, len(resp.Hits))
	seen := map[string]bool{}
	for _, hit := range resp.Hits {
		id, err := uuid.Parse(hit.Document.EntityID)
		if err != nil {
			continue
		}
		d, err := getDocByID(ctx, pool, householdID, id)
		if err != nil {
			continue // gone or soft-deleted: the reconciler prunes it
		}
		if seen[d.Path] {
			continue
		}
		seen[d.Path] = true
		var score float64
		switch {
		case hit.HybridSearchInfo != nil:
			score = hit.HybridSearchInfo.RankFusionScore
		case hit.TextMatchInfo != nil:
			score, _ = strconv.ParseFloat(hit.TextMatchInfo.Score, 64)
		}
		out = append(out, Summary{
			Path: d.Path, Title: d.Title, Tags: d.Tags,
			Excerpt: excerpt(d.Body), UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedName,
			Score: score,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// searchByWords is the fallback when there is no index or it has not caught
// up: every word in the query must appear in the path, title, tags or body.
// It finds less than meaning does, which is why it is the fallback and not the
// first choice.
func searchByWords(ctx context.Context, pool *pgxpool.Pool, householdID uuid.UUID, query string, limit int) ([]Summary, error) {
	words := queryWords(query)
	if len(words) == 0 {
		return nil, nil
	}
	conds := make([]string, 0, len(words))
	args := []any{householdID}
	for _, w := range words {
		like := "%" + escapeLike(w) + "%"
		args = append(args, like)
		conds = append(conds, fmt.Sprintf(
			"(d.path ILIKE $%d OR d.title ILIKE $%d OR d.body ILIKE $%d OR array_to_string(d.tags, ' ') ILIKE $%d)",
			len(args), len(args), len(args), len(args)))
	}
	args = append(args, limit)
	q := `SELECT d.path, d.title, d.tags, d.updated_at, uu.name, left(d.body, 400)
	        FROM kb_docs d LEFT JOIN users uu ON uu.id = d.updated_by
	       WHERE d.household_id = $1 AND d.deleted_at IS NULL AND ` + strings.Join(conds, " AND ") + `
	       ORDER BY d.updated_at DESC
	       LIMIT $` + strconv.Itoa(len(args))
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s Summary
		var body string
		if err := rows.Scan(&s.Path, &s.Title, &s.Tags, &s.UpdatedAt, &s.UpdatedBy, &body); err != nil {
			return nil, err
		}
		s.Excerpt = excerpt(body)
		out = append(out, s)
	}
	return out, rows.Err()
}

// queryWords tokenizes a question into the words worth matching: lower-case,
// letters and digits only, at least three characters, stop words dropped and
// trailing 's' folded so "pets" finds "pet".
func queryWords(q string) []string {
	stop := map[string]bool{
		"the": true, "and": true, "for": true, "what": true, "when": true, "who": true,
		"how": true, "does": true, "did": true, "our": true, "you": true, "your": true,
		"are": true, "was": true, "with": true, "this": true, "that": true, "have": true,
		"from": true, "at": true, "in": true, "on": true, "do": true, "we": true, "is": true,
		"it": true, "if": true, "of": true, "to": true, "a": true, "an": true, "be": true,
		"can": true, "should": true, "there": true, "their": true, "them": true, "they": true,
	}
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(raw) < 3 || stop[raw] || seen[raw] {
			continue
		}
		seen[raw] = true
		out = append(out, raw)
		if strings.HasSuffix(raw, "s") && len(raw) > 4 {
			sing := strings.TrimSuffix(raw, "s")
			if !seen[sing] {
				seen[sing] = true
				out = append(out, sing)
			}
		}
	}
	return out
}

// escapeLike escapes LIKE wildcards in a user-supplied word.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
