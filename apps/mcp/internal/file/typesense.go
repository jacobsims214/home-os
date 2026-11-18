package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"home-os/mcp/internal/config"
)

// Typesense collections and query settings. They mirror the API
// (apps/api/internal/search/typesense.go, KB
// architecture/typesense-semantic-search.md and document-hub-api.md):
//
//   - household_search holds one metadata document per file, id
//     "document-<file id>" (or "secret-document-<file id>" for a file
//     attached to a credential), entity_type "document", entity_id the file
//     id. Typesense embeds title + body with its local ts/all-MiniLM-L12-v2
//     model, so a hybrid query matches the metadata by meaning.
//   - files (worker-owned) holds the extracted text in chunks; every chunk
//     carries file_id (chunk 0's id is the file id).
//
// Every query filters by the caller's household_id server-side, and every
// hit is re-checked against Postgres (document_find) before it is shown.
const (
	householdCollection = "household_search"
	filesCollection     = "files"
	embeddingField      = "embedding"
	documentEntityType  = "document"
	// semanticDistanceThreshold drops vector neighbours that are not
	// related (measured: related 0.50-0.69, unrelated 0.88+).
	semanticDistanceThreshold = 0.75
	// searchPerPage is how many hits each collection returns. Filters are
	// applied in Postgres afterwards, so this is the candidate pool.
	searchPerPage = 100
	// tsTimeout bounds every Typesense call; search falls back to keyword
	// matching in Postgres when it fails.
	tsTimeout = 5 * time.Second
)

// Typesense is the document hub's client for the household search index. A
// nil *Typesense (Typesense not configured) makes every method a no-op, so
// callers never need to check.
type Typesense struct {
	baseURL string
	apiKey  string
	hc      *http.Client
	// household and files are the collection names (tests use their own).
	household string
	files     string
}

// NewTypesense returns a client for cfg's Typesense, or nil when it is not
// configured (no TYPESENSE_API_KEY or host).
func NewTypesense(cfg *config.Config) *Typesense {
	if cfg == nil || cfg.TypesenseAPIKey == "" || cfg.TypesenseHost == "" {
		return nil
	}
	port := cfg.TypesensePort
	if port == "" {
		port = "8108"
	}
	return newTypesense("http://"+cfg.TypesenseHost+":"+port, cfg.TypesenseAPIKey)
}

func newTypesense(baseURL, apiKey string) *Typesense {
	return &Typesense{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, hc: &http.Client{Timeout: tsTimeout},
		household: householdCollection, files: filesCollection}
}

// tsStatusError is a Typesense reply with status >= 400.
type tsStatusError struct {
	Status int
	Body   string
}

func (e *tsStatusError) Error() string { return fmt.Sprintf("typesense %d: %s", e.Status, e.Body) }

func isTSStatus(err error, code int) bool {
	var se *tsStatusError
	return errors.As(err, &se) && se.Status == code
}

func (t *Typesense) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-TYPESENSE-API-KEY", t.apiKey)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &tsStatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// searchDoc is a household_search document (the API's search.SearchDocument).
type searchDoc struct {
	ID          string   `json:"id"`
	HouseholdID string   `json:"household_id"`
	EntityType  string   `json:"entity_type"`
	EntityID    string   `json:"entity_id"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// searchDocIDs returns a file's metadata document ids: the current one and
// both possible ones (a delete removes both). Same rule as the API's
// file.SearchDocIDs: a file attached to a credential is indexed as
// "secret-document-<id>", which the API's VisibleResults hides from roles
// that cannot read credentials.
func searchDocIDs(fileID string, entityType *string) (current string, all []string) {
	plain := documentEntityType + "-" + fileID
	secret := "secret-" + plain
	if entityType != nil && *entityType == "secret" {
		return secret, []string{plain, secret}
	}
	return plain, []string{plain, secret}
}

// searchDocument builds r's metadata document exactly like the API's
// file.SearchDocument, so documents filed through the MCP and the API are
// indexed the same way. The household scope is the row's own household_id.
func (r docRow) searchDocument() searchDoc {
	id, _ := searchDocIDs(r.ID.String(), r.EntityType)
	var body []string
	if r.Description != nil && *r.Description != "" {
		body = append(body, *r.Description)
	}
	body = append(body, "Category: "+r.Category)
	if r.PersonName != nil && *r.PersonName != "" {
		body = append(body, "For: "+*r.PersonName)
	}
	if r.DocumentDate != nil {
		body = append(body, "Dated "+*r.DocumentDate)
	}
	if r.ExpiresOn != nil {
		body = append(body, "Expires "+*r.ExpiresOn)
	}
	if r.displayTitle() != r.Name {
		body = append(body, "File: "+r.Name)
	}
	tags := append([]string{}, r.Tags...)
	if r.Category != "" && r.Category != "other" {
		tags = append(tags, r.Category)
	}
	return searchDoc{
		ID: id, HouseholdID: r.HouseholdID.String(), EntityType: documentEntityType, EntityID: r.ID.String(),
		Title: r.displayTitle(), Body: strings.Join(body, ". "), Tags: tags,
		CreatedAt: r.CreatedAt.Unix(), UpdatedAt: r.UpdatedAt.Unix(),
	}
}

// indexDocument upserts r's metadata document, and removes the one under
// the other id when the file moved to or from a credential. It runs detached
// from the request and bounded; a failure is logged, never returned: the row
// is saved, and document_find still finds it by words. reindex repairs the
// index.
func (t *Typesense) indexDocument(ctx context.Context, r docRow) {
	if t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tsTimeout)
	defer cancel()
	doc := r.searchDocument()
	if err := t.do(ctx, http.MethodPost,
		"/collections/"+t.household+"/documents?action=upsert", doc, nil); err != nil {
		slog.Warn("document saved but its metadata is not yet searchable; run reindex",
			"file_id", r.ID, "error", err)
		return
	}
	_, all := searchDocIDs(r.ID.String(), r.EntityType)
	for _, id := range all {
		if id != doc.ID {
			t.deleteDoc(ctx, r.ID.String(), id)
		}
	}
}

// removeDocument deletes every search document of a deleted file: both
// metadata ids and the worker's chunks in files. The delete is also queued
// in file_search_deletions (053's trigger) for the worker, so a failure here
// is only logged.
func (t *Typesense) removeDocument(ctx context.Context, fileID string) {
	if t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tsTimeout)
	defer cancel()
	_, all := searchDocIDs(fileID, nil)
	for _, id := range all {
		t.deleteDoc(ctx, fileID, id)
	}
	q := url.Values{"filter_by": {fmt.Sprintf("file_id:=`%s`", fileID)}}
	for _, path := range []string{
		"/collections/" + t.files + "/documents?" + q.Encode(),
		"/collections/" + t.files + "/documents/" + url.PathEscape(fileID),
	} {
		if err := t.do(ctx, http.MethodDelete, path, nil, nil); err != nil && !isTSStatus(err, http.StatusNotFound) {
			slog.Warn("document deleted but its text is still in search; the worker will retry",
				"file_id", fileID, "error", err)
			return
		}
	}
}

func (t *Typesense) deleteDoc(ctx context.Context, fileID, docID string) {
	err := t.do(ctx, http.MethodDelete,
		"/collections/"+t.household+"/documents/"+url.PathEscape(docID), nil, nil)
	if err != nil && !isTSStatus(err, http.StatusNotFound) {
		slog.Warn("stale document metadata left in search; run reindex",
			"file_id", fileID, "doc_id", docID, "error", err)
	}
}

type tsHit struct {
	Document      map[string]any `json:"document"`
	TextMatchInfo *struct {
		Score string `json:"score"`
	} `json:"text_match_info"`
	HybridSearchInfo *struct {
		RankFusionScore float64 `json:"rank_fusion_score"`
	} `json:"hybrid_search_info"`
}

// searchFileIDs runs a hybrid (keyword + local-embedding) query over the
// household's document metadata and extracted text and returns the matching
// file ids, best first. householdID must come from the caller's claims: it
// is the server-side scope of both queries. A nil client returns nothing.
func (t *Typesense) searchFileIDs(ctx context.Context, householdID, query string) ([]string, error) {
	if t == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, tsTimeout)
	defer cancel()
	scope := fmt.Sprintf("household_id:=`%s`", householdID)
	meta, metaErr := t.searchCollection(ctx, t.household,
		scope+" && entity_type:=`"+documentEntityType+"`", "title,body,tags", query)
	text, textErr := t.searchCollection(ctx, t.files, scope, "name,extracted_text", query)
	if metaErr != nil && textErr != nil {
		return nil, metaErr
	}

	best := map[string]float64{}
	add := func(hits []tsHit, idOf func(map[string]any) string) {
		for _, h := range hits {
			if h.Document == nil {
				continue
			}
			id := idOf(h.Document)
			if id == "" {
				continue
			}
			var score float64
			switch {
			case h.HybridSearchInfo != nil:
				score = h.HybridSearchInfo.RankFusionScore
			case h.TextMatchInfo != nil:
				// keyword-only fallback: text-match scores are large ints;
				// squash them below any fusion score's scale
				s, _ := strconv.ParseFloat(h.TextMatchInfo.Score, 64)
				score = s / (s + 1e18)
			}
			if cur, ok := best[id]; !ok || score > cur {
				best[id] = score
			}
		}
	}
	add(meta, func(d map[string]any) string {
		if fmt.Sprint(d["household_id"]) != householdID {
			return "" // defence in depth: never trust a hit outside the scope
		}
		s, _ := d["entity_id"].(string)
		return s
	})
	add(text, func(d map[string]any) string {
		if fmt.Sprint(d["household_id"]) != householdID {
			return ""
		}
		if s, _ := d["file_id"].(string); s != "" {
			return s
		}
		s, _ := d["id"].(string)
		return s
	})
	ids := make([]string, 0, len(best))
	for id := range best {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if best[ids[i]] != best[ids[j]] {
			return best[ids[i]] > best[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids, nil
}

// searchCollection queries one collection in hybrid mode, retrying
// keyword-only when the collection has no embedding field (a 4xx other than
// "Collection not found"). A missing collection is no hits.
func (t *Typesense) searchCollection(ctx context.Context, coll, filterBy, queryBy, query string) ([]tsHit, error) {
	params := func(hybrid bool) string {
		v := url.Values{}
		v.Set("q", query)
		v.Set("filter_by", filterBy)
		v.Set("exclude_fields", embeddingField)
		v.Set("include_fields", "id,household_id,entity_id,file_id")
		v.Set("per_page", strconv.Itoa(searchPerPage))
		if hybrid {
			v.Set("query_by", queryBy+","+embeddingField)
			v.Set("vector_query", fmt.Sprintf("%s:([], distance_threshold: %s)",
				embeddingField, strconv.FormatFloat(semanticDistanceThreshold, 'f', -1, 64)))
		} else {
			v.Set("query_by", queryBy)
		}
		return v.Encode()
	}
	path := "/collections/" + url.PathEscape(coll) + "/documents/search?"
	var resp struct {
		Hits []tsHit `json:"hits"`
	}
	err := t.do(ctx, http.MethodGet, path+params(true), nil, &resp)
	var se *tsStatusError
	if errors.As(err, &se) && se.Status < 500 && !strings.Contains(se.Body, "Collection not found") {
		resp.Hits = nil
		err = t.do(ctx, http.MethodGet, path+params(false), nil, &resp)
	}
	if isTSStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", coll, err)
	}
	return resp.Hits, nil
}
