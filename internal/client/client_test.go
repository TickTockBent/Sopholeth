package client

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/client/clienttest"
)

func newTestClient(t *testing.T, node *clienttest.FakeNode) *Client {
	t.Helper()
	srv := httptest.NewServer(node.Handler())
	t.Cleanup(srv.Close)
	return New(srv.URL, srv.Client())
}

func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "192.168.0.1", want: "http://192.168.0.1:8080"},
		{in: "node.example", want: "http://node.example:8080"},
		{in: "node.example:9000", want: "http://node.example:9000"},
		{in: "http://node.example", want: "http://node.example:8080"},
		{in: "https://node.example", want: "https://node.example:8080"},
		{in: "http://node.example:8080/", want: "http://node.example:8080"},
		{in: "  10.0.0.5  ", want: "http://10.0.0.5:8080"},
		{in: "[::1]:8080", want: "http://[::1]:8080"},
		{in: "", wantErr: true},
		{in: "ftp://node.example", wantErr: true},
		{in: "http://node.example/v1", wantErr: true},
		{in: "http://user:pw@node.example", wantErr: true},
		{in: "http://node.example?x=1", wantErr: true},
		{in: "node.example:notaport", wantErr: true},
	}
	for _, tc := range cases {
		got, err := NormalizeEndpoint(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeEndpoint(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeEndpoint(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPutGetRoundTripPreservesBytes(t *testing.T) {
	node := clienttest.New()
	c := newTestClient(t, node)
	ctx := context.Background()

	payloads := map[string][]byte{
		"text":   []byte("hello"),
		"binary": {0x00, 0xff, 0x0a, 0x0d, 0x80, 0x00},
		"empty":  {},
		"ns:key": []byte("namespaced"),
	}
	for key, data := range payloads {
		res, err := c.Put(ctx, key, data, 300)
		if err != nil {
			t.Fatalf("Put(%q): %v", key, err)
		}
		if res.Status != WriteConfirmed || res.RequestedTTL != 300 || res.Key != key {
			t.Fatalf("Put(%q) result = %+v", key, res)
		}
		got, err := c.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(%q): %v", key, err)
		}
		if !bytes.Equal(got.Data, data) {
			t.Fatalf("Get(%q) = %v, want %v", key, got.Data, data)
		}
		if got.OriginalTTL != 300*time.Second || got.RemainingTTL != 290*time.Second {
			t.Fatalf("Get(%q) metadata = %+v", key, got.Metadata)
		}
		if got.CreatedAt.IsZero() {
			t.Fatalf("Get(%q) CreatedAt not parsed", key)
		}
	}
}

func TestPutOmitsTTLHeaderWhenZero(t *testing.T) {
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header["X-Ttl"]
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := New(srv.URL, nil)
	if _, err := c.Put(context.Background(), "k", nil, 0); err != nil {
		t.Fatal(err)
	}
	if sawHeader {
		t.Fatal("X-TTL header sent with zero TTL")
	}
}

func TestPutPendingIsNotAnError(t *testing.T) {
	node := clienttest.New()
	node.Pending = true
	c := newTestClient(t, node)
	res, err := c.Put(context.Background(), "k", []byte("v"), 300)
	if err != nil {
		t.Fatalf("pending write returned error: %v", err)
	}
	if res.Status != WritePending {
		t.Fatalf("status = %q, want pending", res.Status)
	}
}

func TestPutStoreFull(t *testing.T) {
	node := clienttest.New()
	node.Full = true
	c := newTestClient(t, node)
	_, err := c.Put(context.Background(), "k", []byte("v"), 300)
	if !errors.Is(err, ErrStoreFull) {
		t.Fatalf("err = %v, want ErrStoreFull", err)
	}
}

func TestKeyWithSlashIsRejectedByNode(t *testing.T) {
	c := newTestClient(t, clienttest.New())
	_, err := c.Put(context.Background(), "a/b", []byte("v"), 300)
	if !errors.Is(err, ErrBadKey) {
		t.Fatalf("err = %v, want ErrBadKey", err)
	}
}

func TestEmptyKeyRejectedLocally(t *testing.T) {
	node := clienttest.New()
	c := newTestClient(t, node)
	ctx := context.Background()
	if _, err := c.Put(ctx, "", nil, 0); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("Put err = %v", err)
	}
	if _, err := c.Get(ctx, ""); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("Get err = %v", err)
	}
	if _, err := c.Exists(ctx, ""); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("Exists err = %v", err)
	}
	if len(node.Requests) != 0 {
		t.Fatalf("requests were sent for empty keys: %v", node.Requests)
	}
}

func TestGetMissingKey(t *testing.T) {
	c := newTestClient(t, clienttest.New())
	_, err := c.Get(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestExistsUsesHeadAndReturnsMetadata(t *testing.T) {
	node := clienttest.New()
	c := newTestClient(t, node)
	ctx := context.Background()
	if _, err := c.Put(ctx, "k", []byte("payload"), 600); err != nil {
		t.Fatal(err)
	}
	m, err := c.Exists(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if m.OriginalTTL != 600*time.Second || m.RemainingTTL != 590*time.Second {
		t.Fatalf("metadata = %+v", m)
	}
	if _, err := c.Exists(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	last := node.Requests[len(node.Requests)-1]
	if !strings.HasPrefix(last, "HEAD ") {
		t.Fatalf("last request = %q, want HEAD", last)
	}
}

func TestKeysAreURLEscaped(t *testing.T) {
	node := clienttest.New()
	c := newTestClient(t, node)
	ctx := context.Background()
	key := "weird key:with spaces&and%percent"
	if _, err := c.Put(ctx, key, []byte("x"), 300); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "x" {
		t.Fatalf("round trip through escaped key failed: %q", got.Data)
	}
	if !strings.Contains(node.Requests[0], "weird%20key:with%20spaces&and%25percent") {
		t.Fatalf("request path not escaped as expected: %q", node.Requests[0])
	}
}

func TestListKeysPaginationAndNull(t *testing.T) {
	node := clienttest.New()
	c := newTestClient(t, node)
	ctx := context.Background()
	for _, k := range []string{"demo:a", "demo:b", "demo:c", "other:z"} {
		if _, err := c.Put(ctx, k, []byte("v"), 300); err != nil {
			t.Fatal(err)
		}
	}

	page, err := c.ListKeys(ctx, "demo:", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(page.Keys, ",") != "demo:a,demo:b" || page.NextCursor != "demo:b" {
		t.Fatalf("page 1 = %+v", page)
	}
	page, err = c.ListKeys(ctx, "demo:", 2, page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(page.Keys, ",") != "demo:c" || page.NextCursor != "" {
		t.Fatalf("page 2 = %+v", page)
	}

	all, err := c.ListAllKeys(ctx, "demo:", 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(all, ",") != "demo:a,demo:b,demo:c" {
		t.Fatalf("ListAllKeys = %v", all)
	}

	node.NullKeys = true
	page, err = c.ListKeys(ctx, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Keys == nil || len(page.Keys) != 0 {
		t.Fatalf("null keys should become empty slice, got %#v", page.Keys)
	}
	all, err = c.ListAllKeys(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if all == nil || len(all) != 0 {
		t.Fatalf("ListAllKeys on null = %#v", all)
	}
}

func TestDiagnosticEndpoints(t *testing.T) {
	c := newTestClient(t, clienttest.New())
	ctx := context.Background()
	h, err := c.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.NodeID != "fake-1" || h.Network != "private" || h.Enclave != "default" {
		t.Fatalf("health = %+v", h)
	}
	if s, err := c.Status(ctx); err != nil || !strings.Contains(string(s), "uptime") {
		t.Fatalf("status = %s, %v", s, err)
	}
	if tp, err := c.Topology(ctx); err != nil || !strings.Contains(string(tp), "peers") {
		t.Fatalf("topology = %s, %v", tp, err)
	}
	if m, err := c.Metrics(ctx); err != nil || !strings.Contains(string(m), "http_requests_total") {
		t.Fatalf("metrics = %s, %v", m, err)
	}
}

func TestUnreachableNode(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	c := New(addr, nil)
	_, err := c.Health(context.Background())
	var unreachable *UnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("err = %T %v, want UnreachableError", err, err)
	}
	if unreachable.Endpoint != addr {
		t.Fatalf("endpoint = %q, want %q", unreachable.Endpoint, addr)
	}
}

func TestServerErrorSurfacesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := New(srv.URL, nil)
	_, err := c.Put(context.Background(), "k", []byte("v"), 300)
	var status *StatusError
	if !errors.As(err, &status) || status.StatusCode != 500 || status.Body != "boom" {
		t.Fatalf("err = %v", err)
	}
}

func hangingServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv
}

func TestCancellationAbortsRequest(t *testing.T) {
	srv := hangingServer(t)
	c := New(srv.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := c.Get(ctx, "k")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var unreachable *UnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("cancellation should classify as unreachable, got %T", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("cancellation took too long: %v", time.Since(start))
	}
}

func TestTimeoutAbortsRequest(t *testing.T) {
	srv := hangingServer(t)
	c := New(srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Put(ctx, "k", []byte("v"), 300)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}
