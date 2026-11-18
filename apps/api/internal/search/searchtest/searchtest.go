// Package searchtest gives integration tests a search.Client on Typesense
// collections of their own, so tests in different packages (run in
// parallel with the default -p) never share or clobber household_search or
// files. Start a Typesense with
//
//	docker run -d --name ts-test -p 127.0.0.1::8108 --tmpfs /data \
//	  typesense/typesense:30.2 --api-key=k --data-dir=/data
//	export TYPESENSE_TEST_ADDR=127.0.0.1:$(docker port ts-test 8108 | cut -d: -f2) TYPESENSE_TEST_KEY=k
//
// The same variables are read by apps/mcp's Typesense tests.
package searchtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"home-os/api/internal/config"
	"home-os/api/internal/search"
)

// Env is a test Typesense and the collection names a test owns.
type Env struct {
	Base      string // http://host:port
	Key       string
	Household string // household_search_<8hex>
	Files     string // files_<8hex>
}

// Addr returns TYPESENSE_TEST_ADDR and TYPESENSE_TEST_KEY, skipping the test
// when either is unset.
func Addr(t testing.TB) (host, port, key string) {
	t.Helper()
	addr, key := os.Getenv("TYPESENSE_TEST_ADDR"), os.Getenv("TYPESENSE_TEST_KEY")
	if addr == "" || key == "" {
		t.Skip("TYPESENSE_TEST_ADDR/TYPESENSE_TEST_KEY not set; skipping Typesense integration test")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("TYPESENSE_TEST_ADDR %q: %v", addr, err)
	}
	return host, port, key
}

// Suffix returns 8 random hex characters for per-test collection names.
func Suffix(t testing.TB) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// New returns a client on fresh household_search_<8hex> / files_<8hex>
// collection names (not created yet) and drops both when the test ends. It
// skips when TYPESENSE_TEST_ADDR/TYPESENSE_TEST_KEY are unset.
func New(t testing.TB) (*search.Client, Env) {
	t.Helper()
	host, port, key := Addr(t)
	sfx := Suffix(t)
	env := Env{
		Base:      fmt.Sprintf("http://%s:%s", host, port),
		Key:       key,
		Household: "household_search_" + sfx,
		Files:     "files_" + sfx,
	}
	c := search.NewClient(&config.Config{TypesenseHost: host, TypesensePort: port, TypesenseAPIKey: key}).
		WithCollections(env.Household, env.Files)
	t.Cleanup(func() {
		for _, name := range []string{env.Household, env.Files} {
			env.drop(name)
		}
	})
	return c, env
}

// drop deletes a collection, ignoring errors (it may never have been created).
func (e Env) drop(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, e.Base+"/collections/"+name, nil)
	if err != nil {
		return
	}
	req.Header.Set("X-TYPESENSE-API-KEY", e.Key)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}
