package gossip

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestBootstrapHTTPSVerificationAndRedirects(t *testing.T) {
	var hits, redirected atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer plain.Close()
	var redirect atomic.Bool
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if redirect.Load() {
			http.Redirect(w, r, plain.URL, http.StatusTemporaryRedirect)
			return
		}
		json.NewEncoder(w).Encode(BootstrapResponse{Success: true, Peers: []*Node{{ID: "root", HTTPOrigin: "https://root.invalid"}}})
	}))
	defer tls.Close()
	p, _ := newTestProtocol()
	req := &BootstrapRequest{NodeID: "joiner"}
	if _, err := p.sendBootstrapRequest(context.Background(), tls.URL, req); err == nil || hits.Load() != 0 {
		t.Fatal("accepted untrusted TLS certificate")
	}
	if err := p.ConfigureBootstrap(tls.Client(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.sendBootstrapRequest(context.Background(), tls.URL, req); err != nil {
		t.Fatal(err)
	}
	wrong := *tls.Client()
	transport := wrong.Transport.(*http.Transport).Clone()
	u, _ := url.Parse(tls.URL)
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, u.Host)
	}
	wrong.Transport = transport
	if err := p.ConfigureBootstrap(&wrong, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.sendBootstrapRequest(context.Background(), "https://wrong.invalid", req); err == nil {
		t.Fatal("accepted wrong TLS hostname")
	}
	if err := p.ConfigureBootstrap(tls.Client(), nil); err != nil {
		t.Fatal(err)
	}
	redirect.Store(true)
	if _, err := p.sendBootstrapRequest(context.Background(), tls.URL, req); err == nil || redirected.Load() != 0 {
		t.Fatal("followed bootstrap redirect/downgrade")
	}
}
