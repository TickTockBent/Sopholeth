package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"sopholeth/internal/storage"
)

// putKey writes one key through the cluster node directly. Listing is an
// unauthenticated read-side feature; writing through the handler would couple
// these tests to the per-IP write rate limiter.
func putKey(t *testing.T, server *HTTPServer, key string) {
	t.Helper()
	if err := server.clusterNode.Put(context.Background(), key, []byte("v"), 10*time.Minute); err != nil {
		t.Fatalf("Put %s: %v", key, err)
	}
}

// listKeys calls GET /v1/keys with the given query and decodes the envelope.
func listKeys(t *testing.T, server *HTTPServer, query string) ([]string, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/keys"+query, nil)
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/keys%s: expected 200, got %d: %s", query, w.Code, w.Body.String())
	}
	var resp struct {
		Keys       []string `json:"keys"`
		NextCursor string   `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}
	return resp.Keys, resp.NextCursor
}

// TestKeysPaginationWalksWholeKeyspace pages through 250 keys with the
// default limit and expects to see every key exactly once, in order.
func TestKeysPaginationWalksWholeKeyspace(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	const total = 250
	for i := 0; i < total; i++ {
		putKey(t, server, fmt.Sprintf("k%04d", i))
	}

	seen := 0
	var lastKey string
	query := ""
	for {
		keys, next := listKeys(t, server, query)
		for _, k := range keys {
			if lastKey != "" && k <= lastKey {
				t.Fatalf("keys out of order: %q after %q", k, lastKey)
			}
			lastKey = k
			seen++
		}
		if next == "" {
			break
		}
		query = "?cursor=" + next
		if seen > total*2 {
			t.Fatal("pagination loop does not terminate")
		}
	}
	if seen != total {
		t.Fatalf("paged through %d keys, want %d", seen, total)
	}
}

// TestKeysEnforcesDefaultLimit checks that a request without limit returns
// the default page size and a cursor, not the whole keyspace.
func TestKeysEnforcesDefaultLimit(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	for i := 0; i < 150; i++ {
		putKey(t, server, fmt.Sprintf("d%04d", i))
	}

	keys, next := listKeys(t, server, "")
	if len(keys) != 100 {
		t.Fatalf("default page = %d keys, want 100", len(keys))
	}
	if next == "" {
		t.Fatal("expected next_cursor when more keys remain")
	}
}

// TestKeysCapsLimitAboveMaximum clamps oversized limit requests.
func TestKeysCapsLimitAboveMaximum(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	for i := 0; i < 20; i++ {
		putKey(t, server, fmt.Sprintf("c%04d", i))
	}

	keys, _ := listKeys(t, server, "?limit=1000000")
	if len(keys) != 20 {
		t.Fatalf("small keyspace should return all 20 keys, got %d", len(keys))
	}

	// Against a large keyspace the cap must hold; use storage.MaxScanPageLimit
	// as the contract.
	for i := 0; i < storage.MaxScanPageLimit+50; i++ {
		putKey(t, server, fmt.Sprintf("big%05d", i))
	}
	keys, _ = listKeys(t, server, "?limit=999999")
	if len(keys) > storage.MaxScanPageLimit {
		t.Fatalf("limit above maximum not enforced: got %d keys", len(keys))
	}
}

// TestKeysPrefixFilterIsOrdered checks prefix filtering through the index.
func TestKeysPrefixFilterIsOrdered(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	for i := 0; i < 10; i++ {
		putKey(t, server, fmt.Sprintf("alpha%02d", i))
		putKey(t, server, fmt.Sprintf("beta%02d", i))
	}

	keys, next := listKeys(t, server, "?prefix=alpha&limit=100")
	if len(keys) != 10 {
		t.Fatalf("prefix page = %d keys, want 10", len(keys))
	}
	for i, k := range keys {
		if want := fmt.Sprintf("alpha%02d", i); k != want {
			t.Fatalf("keys[%d] = %q, want %q", i, k, want)
		}
	}
	if next != "" {
		t.Fatalf("expected no next_cursor for a complete prefix page, got %q", next)
	}
}

// TestKeysCursorIsExclusive checks that the cursor key itself is skipped and
// the next page starts strictly after it.
func TestKeysCursorIsExclusive(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	for i := 0; i < 10; i++ {
		putKey(t, server, fmt.Sprintf("x%02d", i))
	}

	keys, next := listKeys(t, server, "?limit=4")
	if len(keys) != 4 || next == "" {
		t.Fatalf("first page = %v (next %q)", keys, next)
	}
	cursor := next
	second, _ := listKeys(t, server, "?limit=4&cursor="+cursor)
	if len(second) == 0 {
		t.Fatal("second page empty")
	}
	for _, k := range second {
		if k <= cursor {
			t.Fatalf("second page key %q sorts at or before cursor %q", k, cursor)
		}
	}
}

// TestKeysLimitValidation checks that invalid limit values fall back to the
// default instead of erroring or returning unbounded results.
func TestKeysLimitValidation(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	for i := 0; i < 150; i++ {
		putKey(t, server, fmt.Sprintf("v%04d", i))
	}

	for _, bad := range []string{"abc", "-5", "0"} {
		keys, _ := listKeys(t, server, "?limit="+bad)
		if len(keys) != 100 {
			t.Fatalf("limit=%q: page = %d keys, want default 100", bad, len(keys))
		}
	}
}

// TestKeysOversizedKeyspacePageCost guards the acceptance criterion that
// listing stays cheap as the keyspace grows: the default page must return
// the same bounded number of keys whether the store holds 100 or 5000 keys.
func TestKeysOversizedKeyspacePageCost(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	for i := 0; i < 5000; i++ {
		putKey(t, server, fmt.Sprintf("p%05d", i))
	}

	keys, next := listKeys(t, server, "")
	if len(keys) != 100 {
		t.Fatalf("default page = %d keys on a large keyspace, want 100", len(keys))
	}
	if next != keys[99] {
		t.Fatalf("next_cursor = %q, want last key %q", next, keys[99])
	}
}

// TestKeysPageFromNumericQuery isolates the raw-handler decoding used above.
func TestKeysPageFromNumericQuery(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		putKey(t, server, "n"+strconv.Itoa(i))
	}
	keys, _ := listKeys(t, server, "?limit=2")
	if len(keys) != 2 {
		t.Fatalf("limit=2 page = %d keys, want 2", len(keys))
	}
}
