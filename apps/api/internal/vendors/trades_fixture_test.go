package vendors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tradeFixture is packages/db/testdata/trade_normalization.json. The MCP's
// contact tools (apps/mcp/internal/vendors, trades_test.go) load the same
// file, so a synonym or limit changed on only one side fails one of the two
// suites and REST and MCP trade matching cannot drift (#4466).
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

func TestTradeNormalizationMatchesMCP(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "packages", "db", "testdata", "trade_normalization.json")
	b, err := os.ReadFile(path)
	require.NoError(t, err, "read shared trade fixture")
	var f tradeFixture
	require.NoError(t, json.Unmarshal(b, &f))
	require.NotEmpty(t, f.Synonyms)
	require.NotEmpty(t, f.Cases)
	require.NotEmpty(t, f.ListCases)

	assert.Equal(t, f.Synonyms, tradeSynonyms, "tradeSynonyms must equal the shared fixture")
	assert.Equal(t, f.MaxTrades, maxTrades)
	assert.Equal(t, f.MaxTradeLen, maxTradeLen)
	for _, tc := range f.Cases {
		assert.Equal(t, tc.Want, NormalizeTrade(tc.In), "NormalizeTrade(%q)", tc.In)
	}
	for _, tc := range f.ListCases {
		t.Run(tc.Name, func(t *testing.T) {
			got, msg := normalizeTrades(tc.In)
			if tc.Error {
				assert.NotEmpty(t, msg)
				return
			}
			assert.Empty(t, msg)
			assert.Equal(t, tc.Want, got)
		})
	}
}
