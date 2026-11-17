package endpoint

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientRejectsVerificationBypass(t *testing.T) {
	unsafeTransports := map[string]http.RoundTripper{
		"insecure skip verify": &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		"server name override": &http.Transport{TLSClientConfig: &tls.Config{ServerName: "other.invalid"}},
		"custom TLS dialer": &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, nil
		}},
		"legacy TLS dialer":    &http.Transport{DialTLS: func(string, string) (net.Conn, error) { return nil, nil }},
		"opaque round tripper": roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil }),
	}
	for name, unsafeTransport := range unsafeTransports {
		t.Run(name, func(t *testing.T) {
			if _, err := Client(&http.Client{Transport: unsafeTransport}); err == nil {
				t.Fatal("accepted a transport that bypasses verification")
			}
		})
	}
}

func TestClientWithoutTransportUsesPrivateClone(t *testing.T) {
	for name, callerClient := range map[string]*http.Client{"nil client": nil, "no transport": {}} {
		t.Run(name, func(t *testing.T) {
			verifiedClient, err := Client(callerClient)
			if err != nil {
				t.Fatal(err)
			}
			privateTransport, ok := verifiedClient.Transport.(*http.Transport)
			if !ok || privateTransport == nil {
				t.Fatalf("transport = %T, want a private *http.Transport", verifiedClient.Transport)
			}
			if verifiedClient.Transport == http.DefaultTransport {
				t.Fatal("handed out the shared http.DefaultTransport")
			}
		})
	}
	first, _ := Client(nil)
	second, _ := Client(nil)
	if first.Transport == second.Transport {
		t.Fatal("two verified clients share one default transport")
	}
}

func TestClientKeepsTimeoutDropsJarAndRefusesRedirects(t *testing.T) {
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("followed a redirect")
	}))
	defer redirectTarget.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	callerClient := &http.Client{Timeout: 7 * time.Second, Jar: jar}
	verifiedClient, err := Client(callerClient)
	if err != nil {
		t.Fatal(err)
	}
	if verifiedClient == callerClient {
		t.Fatal("returned the caller's client instead of a copy")
	}
	if verifiedClient.Timeout != 7*time.Second {
		t.Fatalf("timeout = %v, want the caller's 7s", verifiedClient.Timeout)
	}
	if verifiedClient.Jar != nil {
		t.Fatal("kept the caller's cookie jar")
	}
	response, err := verifiedClient.Get(redirecting.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want the unfollowed redirect", response.StatusCode)
	}
}

func TestClientVerifiesCertificates(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tlsServer.Close()

	systemRootsClient, err := Client(nil)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := systemRootsClient.Get(tlsServer.URL); err == nil {
		response.Body.Close()
		t.Fatal("accepted an untrusted certificate")
	}

	trustingClient, err := Client(tlsServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, err := trustingClient.Get(tlsServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
