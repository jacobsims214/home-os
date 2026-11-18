package search

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// HouseholdDocumentIDs returns the ids of every household_search document
// belonging to one household.
//
// It exists for reconciliation (#5298): after upserting the documents built
// from Postgres, the reconciler deletes the index documents whose record is
// gone, and it needs the household's current ids to know which those are. The
// scan is paged because a large family can hold more documents than one
// Typesense page returns (per_page is capped at 250).
//
// The id is the only field requested (exclude_fields=*), so this cannot leak
// record text and does not pay for the embedding vectors.
func (c *Client) HouseholdDocumentIDs(ctx context.Context, householdID string) ([]string, error) {
	const perPage = 250
	var out []string
	for page := 1; ; page++ {
		v := url.Values{}
		v.Set("q", "*")
		v.Set("query_by", "title")
		v.Set("filter_by", fmt.Sprintf("household_id:=`%s`", householdID))
		v.Set("include_fields", "id")
		v.Set("per_page", strconv.Itoa(perPage))
		v.Set("page", strconv.Itoa(page))

		var resp struct {
			Found int `json:"found"`
			Hits  []struct {
				Document struct {
					ID string `json:"id"`
				} `json:"document"`
			} `json:"hits"`
		}
		path := "/collections/" + url.PathEscape(c.household) + "/documents/search?" + v.Encode()
		err := c.do(ctx, c.httpClient, http.MethodGet, path, nil, &resp)
		if isStatus(err, http.StatusNotFound) {
			return nil, nil // no collection yet: nothing indexed
		}
		if err != nil {
			return nil, fmt.Errorf("search: list household documents %s: %w", householdID, err)
		}
		if len(resp.Hits) == 0 {
			return out, nil
		}
		for _, hit := range resp.Hits {
			if hit.Document.ID != "" {
				out = append(out, hit.Document.ID)
			}
		}
		if len(out) >= resp.Found || page*perPage >= resp.Found {
			return out, nil
		}
	}
}
