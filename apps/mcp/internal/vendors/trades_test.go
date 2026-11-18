package vendors

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// tradeFixture is packages/db/testdata/trade_normalization.json. The REST
// API's vendors tests (apps/api/internal/vendors) load the same file, so a
// synonym added to only one side fails one of the two suites.
type tradeFixture struct {
	MaxTrades   int               `json:"max_trades"`
	MaxTradeLen int               `json:"max_trade_len"`
	Synonyms    map[string]string `json:"synonyms"`
	Cases       []struct {
		In   string `json:"in"`
		Want string `json:"want"`
	} `json:"cases"`
	ListCases []struct {
		Name  string   `json:"name"`
		In    []string `json:"in"`
		Want  []string `json:"want"`
		Error bool     `json:"error"`
	} `json:"list_cases"`
}

func loadTradeFixture(t *testing.T) tradeFixture {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "packages", "db", "testdata", "trade_normalization.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared trade fixture: %v", err)
	}
	var f tradeFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(f.Synonyms) == 0 || len(f.Cases) == 0 || len(f.ListCases) == 0 {
		t.Fatalf("%s is incomplete", path)
	}
	return f
}

// TestTradeNormalizationMatchesAPI pins the MCP's trade normalization to the
// shared fixture: the same synonym table, the same limits and the same
// answers as the API's vendors.NormalizeTrade / normalizeTrades.
func TestTradeNormalizationMatchesAPI(t *testing.T) {
	f := loadTradeFixture(t)
	if !reflect.DeepEqual(tradeSynonyms, f.Synonyms) {
		t.Errorf("tradeSynonyms differs from the shared fixture:\n mcp     %v\n fixture %v", tradeSynonyms, f.Synonyms)
	}
	if maxTrades != f.MaxTrades || maxTradeLen != f.MaxTradeLen {
		t.Errorf("limits = %d/%d, fixture %d/%d", maxTrades, maxTradeLen, f.MaxTrades, f.MaxTradeLen)
	}
	for _, tc := range f.Cases {
		if got := normalizeTrade(tc.In); got != tc.Want {
			t.Errorf("normalizeTrade(%q) = %q, want %q", tc.In, got, tc.Want)
		}
	}
	for _, tc := range f.ListCases {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := normalizeTrades(tc.In)
			if tc.Error {
				var ae *argError
				if !errors.As(err, &ae) || ae.field != "trades" || ae.fix == "" {
					t.Fatalf("want a trades argError, got %v (%v)", err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil || !reflect.DeepEqual(got, tc.Want) {
				t.Errorf("got %#v, want %#v", got, tc.Want)
			}
		})
	}
}
