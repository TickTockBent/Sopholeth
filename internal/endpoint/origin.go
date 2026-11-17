// Package endpoint handles node HTTP origins shared by clients and gossip.
package endpoint

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Normalize accepts an HTTP(S) origin or a bare address. Only bare addresses
// default to the node's 8080 port; explicit URLs retain HTTP/HTTPS defaults.
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("endpoint must not be empty")
	}
	bare := !strings.Contains(raw, "://")
	if bare {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || strings.ContainsAny(raw, "?#\\%") {
		return "", errors.New("invalid endpoint: expected an HTTP(S) origin without credentials, path, query, or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("invalid endpoint: empty port")
	}
	if port == "" && bare {
		port = "8080"
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid endpoint: bad port")
		}
		port = strconv.Itoa(n)
		if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
			port = ""
		}
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}

// Client returns a copy of hc that preserves certificate and hostname
// validation, refuses redirects, and drops any cookie jar. Public callers can
// supply additional CA roots, never a verification bypass.
//
// Client inspects the caller's transport, so call it once at configuration
// time, before any request uses that transport, and reuse the result:
// net/http initializes a Transport's TLS settings on its first RoundTrip, and
// inspecting a transport while another goroutine sends on it is a data race
// (#285). When hc has no Transport, the result gets a private clone of
// http.DefaultTransport; the shared global is never inspected or handed out.
func Client(hc *http.Client) (*http.Client, error) {
	verifiedClient := &http.Client{}
	if hc != nil {
		*verifiedClient = *hc
	}
	if verifiedClient.Transport == nil {
		verifiedClient.Transport = newPrivateTransport()
	}
	transport, ok := verifiedClient.Transport.(*http.Transport)
	if !ok || transport.DialTLS != nil || transport.DialTLSContext != nil ||
		(transport.TLSClientConfig != nil && (transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.ServerName != "")) {
		return nil, errors.New("node transport requires certificate and hostname verification")
	}
	verifiedClient.Jar = nil
	verifiedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return verifiedClient, nil
}

// newPrivateTransport clones the standard library defaults. Clone first runs
// the transport's own one-time protocol setup under its sync.Once, so it does
// not race with requests other code sends on http.DefaultTransport.
func newPrivateTransport() *http.Transport {
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		return defaultTransport.Clone()
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}
