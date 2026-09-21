package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/cluster"
	"sopholeth/internal/node"
)

// newTestServer creates an HTTPServer backed by a single-node cluster
// suitable for handler-level tests. No gossip, no network.
func newTestServer(t *testing.T) (*HTTPServer, func()) {
	t.Helper()
	return newTestServerWithSecret(t, "")
}

func newTestServerWithSecret(t *testing.T, secret string) (*HTTPServer, func()) {
	t.Helper()

	cn := cluster.NewClusterNode(
		"test-node", "localhost", 0, 0,
		1, // replicationFactor=1 → quorum=1 (local write sufficient)
		0, // unlimited storage
		5*time.Second,
		secret,
		"default",
	)

	ctx, cancel := context.WithCancel(context.Background())
	if err := cn.Start(ctx, nil); err != nil {
		t.Fatalf("failed to start cluster node: %v", err)
	}

	securityMW := node.NewSecurityMiddleware(1000, 2000, 10*1024*1024, false)

	server := &HTTPServer{
		clusterNode: cn,
		nodeID:      "test-node",
		network:     "private",
		minTTL:      300,
		maxTTL:      86400,
		startTime:   time.Now(),
		securityMW:  securityMW,
	}

	cleanup := func() {
		securityMW.Close()
		cn.Stop()
		cancel()
	}

	return server, cleanup
}

// --- PUT handler tests ---

func TestPutReturns201(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("PUT", "/v1/data/mykey", strings.NewReader("hello"))
	req.Header.Set("X-TTL", "600")
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPutTTLFromQueryParam(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("PUT", "/v1/data/qkey?ttl=600", strings.NewReader("data"))
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	// Verify it was stored
	getReq := httptest.NewRequest("GET", "/v1/data/qkey", nil)
	getW := httptest.NewRecorder()
	server.Router().ServeHTTP(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("GET after PUT: expected 200, got %d", getW.Code)
	}
	if getW.Body.String() != "data" {
		t.Fatalf("GET body = %q, want %q", getW.Body.String(), "data")
	}
}

func TestPutOmittedTTLDefaultsToThirtyMinutes(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("PUT", "/v1/data/defaultttl", strings.NewReader("data"))
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	getReq := httptest.NewRequest("GET", "/v1/data/defaultttl", nil)
	getW := httptest.NewRecorder()
	server.Router().ServeHTTP(getW, getReq)
	if got := getW.Header().Get("X-Original-TTL"); got != "1800" {
		t.Fatalf("X-Original-TTL = %q, want %q", got, "1800")
	}
}

func TestPutTTLClampedToMin(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	// Request TTL=1 (below minTTL=300) — should be clamped, not rejected
	req := httptest.NewRequest("PUT", "/v1/data/clampkey", strings.NewReader("data"))
	req.Header.Set("X-TTL", "1")
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	// Verify the data is stored and TTL was clamped to minTTL (300)
	getReq := httptest.NewRequest("GET", "/v1/data/clampkey", nil)
	getW := httptest.NewRecorder()
	server.Router().ServeHTTP(getW, getReq)

	originalTTL := getW.Header().Get("X-Original-TTL")
	if originalTTL != "300" {
		t.Fatalf("X-Original-TTL = %q, want %q (clamped to minTTL)", originalTTL, "300")
	}
}

func TestPutMalformedTTLUsesDefault(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("PUT", "/v1/data/badttl", strings.NewReader("data"))
	req.Header.Set("X-TTL", "not-a-number")
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	// Malformed TTL falls through to the 30-minute default.
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPutNegativeTTLUsesDefault(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("PUT", "/v1/data/negttl", strings.NewReader("data"))
	req.Header.Set("X-TTL", "-100")
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	// Negative TTL fails the `parsed > 0` check, so the 30-minute default is used.
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
}

func TestPutEmptyBody(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("PUT", "/v1/data/emptykey", strings.NewReader(""))
	req.Header.Set("X-TTL", "600")
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	// Empty body is valid — store empty bytes
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	getReq := httptest.NewRequest("GET", "/v1/data/emptykey", nil)
	getW := httptest.NewRecorder()
	server.Router().ServeHTTP(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("GET after PUT empty: expected 200, got %d", getW.Code)
	}
	if getW.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %d bytes", getW.Body.Len())
	}
}

// --- GET handler tests ---

func TestGetNonexistentKey(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/v1/data/noexist", nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGetReturnsMetadataHeaders(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	// Store a key
	putReq := httptest.NewRequest("PUT", "/v1/data/metakey", strings.NewReader("payload"))
	putReq.Header.Set("X-TTL", "600")
	putW := httptest.NewRecorder()
	server.Router().ServeHTTP(putW, putReq)

	// Retrieve it
	getReq := httptest.NewRequest("GET", "/v1/data/metakey", nil)
	getW := httptest.NewRecorder()
	server.Router().ServeHTTP(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getW.Code)
	}

	// Check metadata headers
	if getW.Header().Get("X-Created-At") == "" {
		t.Error("missing X-Created-At header")
	}
	if getW.Header().Get("X-Original-TTL") != "600" {
		t.Errorf("X-Original-TTL = %q, want %q", getW.Header().Get("X-Original-TTL"), "600")
	}
	if getW.Header().Get("X-Remaining-TTL") == "" {
		t.Error("missing X-Remaining-TTL header")
	}
	if getW.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want %q", getW.Header().Get("Content-Type"), "application/octet-stream")
	}
	if getW.Header().Get("Content-Length") != "7" {
		t.Errorf("Content-Length = %q, want %q", getW.Header().Get("Content-Length"), "7")
	}
}

func TestHeadReturnsHeadersNoBody(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	// Store
	putReq := httptest.NewRequest("PUT", "/v1/data/headkey", strings.NewReader("payload"))
	putReq.Header.Set("X-TTL", "600")
	putW := httptest.NewRecorder()
	server.Router().ServeHTTP(putW, putReq)

	// HEAD request
	headReq := httptest.NewRequest("HEAD", "/v1/data/headkey", nil)
	headW := httptest.NewRecorder()
	server.Router().ServeHTTP(headW, headReq)

	if headW.Code != http.StatusOK {
		t.Fatalf("HEAD: expected 200, got %d", headW.Code)
	}
	if headW.Header().Get("X-Remaining-TTL") == "" {
		t.Error("HEAD: missing X-Remaining-TTL header")
	}
	// Note: httptest.NewRecorder doesn't strip body for HEAD (real http.Server does).
	// Verify Content-Length is set correctly — the real server handles body suppression.
	if headW.Header().Get("Content-Length") != "7" {
		t.Errorf("HEAD: Content-Length = %q, want %q", headW.Header().Get("Content-Length"), "7")
	}
}

func TestHeadNonexistentKey(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("HEAD", "/v1/data/missing", nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("HEAD missing key: expected 404, got %d", w.Code)
	}
}

// --- Keys handler tests ---

func TestKeysEmpty(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/v1/keys", nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string][]string
	json.NewDecoder(w.Body).Decode(&resp)

	if len(resp["keys"]) != 0 {
		t.Fatalf("expected empty keys list, got %v", resp["keys"])
	}
}

func TestKeysPrefixFilter(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	// Store keys with different prefixes (no slashes — mux {key} is one path segment)
	for _, key := range []string{"app-foo", "app-bar", "other-baz"} {
		req := httptest.NewRequest("PUT", "/v1/data/"+key, strings.NewReader("data"))
		req.Header.Set("X-TTL", "600")
		w := httptest.NewRecorder()
		server.Router().ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("PUT %s: expected 201, got %d", key, w.Code)
		}
	}

	// Filter by prefix
	req := httptest.NewRequest("GET", "/v1/keys?prefix=app-", nil)
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	var resp map[string][]string
	json.NewDecoder(w.Body).Decode(&resp)

	if len(resp["keys"]) != 2 {
		t.Fatalf("expected 2 keys with prefix app-, got %d: %v", len(resp["keys"]), resp["keys"])
	}

	for _, k := range resp["keys"] {
		if !strings.HasPrefix(k, "app-") {
			t.Errorf("key %q does not have prefix app-", k)
		}
	}
}

// --- Health / status handler tests ---

func TestHealthEndpoint(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/v1/health", nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["status"] != "healthy" {
		t.Errorf("status = %v, want healthy", resp["status"])
	}
	if resp["node_id"] != "test-node" {
		t.Errorf("node_id = %v, want test-node", resp["node_id"])
	}
}

func TestStatusEndpoint(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/v1/status", nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["uptime"] == nil {
		t.Error("missing uptime field")
	}
	if resp["memory"] == nil {
		t.Error("missing memory field")
	}
}

// --- Overwrite behavior ---

func TestPutOverwriteReplacesData(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	// Write v1
	req := httptest.NewRequest("PUT", "/v1/data/overkey", strings.NewReader("version1"))
	req.Header.Set("X-TTL", "600")
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	// Write v2 (overwrite)
	req2 := httptest.NewRequest("PUT", "/v1/data/overkey", strings.NewReader("version2"))
	req2.Header.Set("X-TTL", "600")
	w2 := httptest.NewRecorder()
	server.Router().ServeHTTP(w2, req2)

	if w2.Code != http.StatusCreated {
		t.Fatalf("overwrite: expected 201, got %d", w2.Code)
	}

	// Read — should get v2
	getReq := httptest.NewRequest("GET", "/v1/data/overkey", nil)
	getW := httptest.NewRecorder()
	server.Router().ServeHTTP(getW, getReq)

	if getW.Body.String() != "version2" {
		t.Fatalf("after overwrite: got %q, want %q", getW.Body.String(), "version2")
	}
}

// --- Pagination tests ---

// storeKeys is a helper that creates n keys named key-000, key-001, etc.
func storeKeys(t *testing.T, server *HTTPServer, router http.Handler, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("key-%03d", i)
		req := httptest.NewRequest("PUT", "/v1/data/"+key, strings.NewReader("data"))
		req.Header.Set("X-TTL", "600")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("PUT %s: expected 201, got %d", key, w.Code)
		}
	}
}

func TestKeysLimit(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	router := server.Router()

	storeKeys(t, server, router, 10)

	req := httptest.NewRequest("GET", "/v1/keys?limit=3", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	keys := resp["keys"].([]interface{})
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}

	// Should have a next_cursor
	nextCursor, ok := resp["next_cursor"].(string)
	if !ok || nextCursor == "" {
		t.Fatal("expected next_cursor in response")
	}
}

func TestKeysCursorPagination(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	router := server.Router()

	storeKeys(t, server, router, 10) // key-000 through key-009

	// Page 1: first 3
	req := httptest.NewRequest("GET", "/v1/keys?limit=3", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var page1 map[string]interface{}
	json.NewDecoder(w.Body).Decode(&page1)
	keys1 := page1["keys"].([]interface{})
	cursor := page1["next_cursor"].(string)

	if len(keys1) != 3 {
		t.Fatalf("page 1: expected 3 keys, got %d", len(keys1))
	}
	if keys1[0].(string) != "key-000" {
		t.Fatalf("page 1 first key = %q, want key-000", keys1[0])
	}

	// Page 2: next 3 using cursor
	req2 := httptest.NewRequest("GET", "/v1/keys?limit=3&cursor="+cursor, nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	var page2 map[string]interface{}
	json.NewDecoder(w2.Body).Decode(&page2)
	keys2 := page2["keys"].([]interface{})

	if len(keys2) != 3 {
		t.Fatalf("page 2: expected 3 keys, got %d", len(keys2))
	}

	// Page 2 first key should come after page 1 last key
	if keys2[0].(string) <= keys1[2].(string) {
		t.Fatalf("page 2 first key %q should be after page 1 last key %q", keys2[0], keys1[2])
	}

	// Collect all pages to verify we get all 10 keys
	allKeys := make(map[string]bool)
	for _, k := range keys1 {
		allKeys[k.(string)] = true
	}
	for _, k := range keys2 {
		allKeys[k.(string)] = true
	}

	// Page 3
	cursor2 := page2["next_cursor"].(string)
	req3 := httptest.NewRequest("GET", "/v1/keys?limit=3&cursor="+cursor2, nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)

	var page3 map[string]interface{}
	json.NewDecoder(w3.Body).Decode(&page3)
	keys3 := page3["keys"].([]interface{})
	for _, k := range keys3 {
		allKeys[k.(string)] = true
	}

	// Page 4: last key (1 remaining)
	cursor3 := page3["next_cursor"].(string)
	req4 := httptest.NewRequest("GET", "/v1/keys?limit=3&cursor="+cursor3, nil)
	w4 := httptest.NewRecorder()
	router.ServeHTTP(w4, req4)

	var page4 map[string]interface{}
	json.NewDecoder(w4.Body).Decode(&page4)
	keys4 := page4["keys"].([]interface{})
	for _, k := range keys4 {
		allKeys[k.(string)] = true
	}

	if len(keys4) != 1 {
		t.Fatalf("page 4: expected 1 key, got %d", len(keys4))
	}

	// No more pages — next_cursor should be absent
	if _, hasCursor := page4["next_cursor"]; hasCursor {
		t.Fatal("page 4 should not have next_cursor (last page)")
	}

	// Verify all 10 keys collected
	if len(allKeys) != 10 {
		t.Fatalf("expected 10 unique keys across all pages, got %d", len(allKeys))
	}
}

func TestKeysLimitWithoutCursor(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	router := server.Router()

	storeKeys(t, server, router, 5)

	// Limit larger than total keys — should return all, no cursor
	req := httptest.NewRequest("GET", "/v1/keys?limit=100", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	keys := resp["keys"].([]interface{})
	if len(keys) != 5 {
		t.Fatalf("expected 5 keys, got %d", len(keys))
	}
	if _, hasCursor := resp["next_cursor"]; hasCursor {
		t.Fatal("should not have next_cursor when all keys fit in limit")
	}
}

func TestKeysNoLimitReturnsAll(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	router := server.Router()

	storeKeys(t, server, router, 10)

	// No limit param — backwards compatible, returns everything
	req := httptest.NewRequest("GET", "/v1/keys", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	keys := resp["keys"].([]interface{})
	if len(keys) != 10 {
		t.Fatalf("expected 10 keys (no limit), got %d", len(keys))
	}
	if _, hasCursor := resp["next_cursor"]; hasCursor {
		t.Fatal("should not have next_cursor when no limit is set")
	}
}

func TestKeysSortedOrder(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()
	router := server.Router()

	// Store in non-alphabetical order
	for _, key := range []string{"cherry", "apple", "banana"} {
		req := httptest.NewRequest("PUT", "/v1/data/"+key, strings.NewReader("data"))
		req.Header.Set("X-TTL", "600")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	req := httptest.NewRequest("GET", "/v1/keys", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp map[string][]string
	json.NewDecoder(w.Body).Decode(&resp)

	keys := resp["keys"]
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
	if keys[0] != "apple" || keys[1] != "banana" || keys[2] != "cherry" {
		t.Fatalf("keys not sorted: %v", keys)
	}
}

// --- #91: keys must not contain '/' ---

// TestSlashKeyReturns400_PUT locks in #91: a request to PUT /v1/data/foo%2Fbar
// must return 400, not gorilla/mux's silent 404 from the route mismatch.
func TestSlashKeyReturns400_PUT(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	// Client sends an encoded slash; gorilla/mux decodes it before routing,
	// so the path becomes /v1/data/foo/bar and falls to NotFoundHandler.
	req := httptest.NewRequest("PUT", "/v1/data/foo%2Fbar", strings.NewReader("data"))
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "key must not contain") {
		t.Fatalf("expected error message about keys with '/', got: %s", w.Body.String())
	}
}

// TestSlashKeyReturns400_GET mirrors the PUT test for read paths.
func TestSlashKeyReturns400_GET(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/v1/data/foo%2Fbar", nil)
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUnknownPathStill404 verifies the NotFoundHandler only converts
// /v1/data/... 404s into 400s — anything else keeps the standard 404.
func TestUnknownPathStill404(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/v1/nonexistent/route", nil)
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown route, got %d", w.Code)
	}
}

// TestSlashOnlyKeyReturns400_NotRedirect locks in the parity-gap fix
// surfaced by PR #104 review: /v1/data/%2F (slash-only key) decodes
// to /v1/data//. Without SkipClean(true), gorilla/mux normalizes the
// double slash to /v1/data/ and returns 301 before NotFoundHandler
// can fire, so a PUT client sees a redirect that converts to GET per
// RFC 9110 and never gets a coherent 400 for the original PUT. With
// SkipClean, the original path reaches the router and 400 is returned
// directly. TS already returned 400 here; this brings Go to parity.
func TestSlashOnlyKeyReturns400_NotRedirect(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	for _, method := range []string{"PUT", "GET"} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/v1/data/%2F", strings.NewReader("data"))
			w := httptest.NewRecorder()
			server.Router().ServeHTTP(w, req)

			if w.Code == http.StatusMovedPermanently || w.Code == http.StatusPermanentRedirect {
				t.Fatalf("got %d redirect — SkipClean should prevent this; PUT clients can't follow without losing the body", w.Code)
			}
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// --- #97: pprof on separate listener ---

func TestPprofDefaultServeMuxHasHandlers(t *testing.T) {
	// The blank import of net/http/pprof registers handlers on
	// http.DefaultServeMux. Verify they respond.
	req := httptest.NewRequest("GET", "/debug/pprof/", nil)
	w := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from /debug/pprof/, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "goroutine") {
		t.Fatal("pprof index should list goroutine profile")
	}
}

func TestPprofHeapProfile(t *testing.T) {
	req := httptest.NewRequest("GET", "/debug/pprof/heap", nil)
	w := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from /debug/pprof/heap, got %d", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Fatal("heap profile should not be empty")
	}
}

func TestPprofNotOnMainRouter(t *testing.T) {
	server, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/debug/pprof/", nil)
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("pprof should NOT be on the main router; expected 404, got %d", w.Code)
	}
}
