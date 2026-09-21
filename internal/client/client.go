// Package client is a thin HTTP client for the Sopholeth node API.
//
// It talks to exactly one node. Every read, existence check, and listing
// reflects that node's local store; a missing value may exist elsewhere on
// the network. Writes report the contacted node's local outcome: confirmed
// when the node observed its dynamic quorum, pending when it stored the
// value but the quorum wait expired. See docs/api.md for the wire contract.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultHTTPPort is the port assumed when an endpoint omits one. It matches
// the node's NODE_HTTP_PORT default.
const DefaultHTTPPort = 8080

// UserAgent identifies the client on the wire.
const UserAgent = "soph"

var (
	// ErrNotFound is returned by Get and Exists when the contacted node has
	// no live value for the key. It does not establish network-wide absence.
	ErrNotFound = errors.New("key not found on contacted node")

	// ErrStoreFull is returned by Put when the node rejected the write
	// because its local payload capacity is exhausted (HTTP 507).
	ErrStoreFull = errors.New("node storage capacity exceeded")

	// ErrBadKey is returned when the node rejected the key itself (HTTP 400),
	// for example because it contains a path separator.
	ErrBadKey = errors.New("node rejected key")

	// ErrEmptyKey is returned before any request when the key is empty.
	ErrEmptyKey = errors.New("key must not be empty")
)

// UnreachableError wraps a transport-level failure: connection refused, DNS
// failure, timeout, or a request cancelled by the caller. The node produced
// no response, so nothing can be said about the outcome of a write.
type UnreachableError struct {
	Endpoint string
	Err      error
}

func (e *UnreachableError) Error() string {
	if e.Endpoint == "" {
		return fmt.Sprintf("unreachable: %v", e.Err)
	}
	return fmt.Sprintf("node unreachable at %s: %v", e.Endpoint, e.Err)
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// StatusError reports an unexpected HTTP status from the node. Body holds the
// response body, truncated, for diagnostics.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("node returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("node returned HTTP %d: %s", e.StatusCode, e.Body)
}

// WriteStatus is the contacted node's local outcome for a write.
type WriteStatus string

const (
	// WriteConfirmed means the node stored the value and observed its
	// dynamic quorum before responding (HTTP 201).
	WriteConfirmed WriteStatus = "confirmed"
	// WritePending means the node stored the value locally but the quorum
	// wait expired (HTTP 202). Replication continues in the background;
	// this is neither a failure nor a delivery guarantee.
	WritePending WriteStatus = "pending"
)

// PutResult describes a completed write.
type PutResult struct {
	Key    string
	Status WriteStatus
	// RequestedTTL is the TTL sent to the node, in seconds. The node may
	// clamp it to local bounds; use Exists to read the stored TTL back.
	RequestedTTL int
}

// Metadata is the contacted node's local TTL bookkeeping for a key.
type Metadata struct {
	Key          string
	CreatedAt    time.Time
	OriginalTTL  time.Duration
	RemainingTTL time.Duration
}

// Value is a retrieved payload with its local metadata.
type Value struct {
	Metadata
	Data []byte
}

// KeyPage is one page of a key listing.
type KeyPage struct {
	Keys       []string
	NextCursor string
}

// Health is the node's self-reported identity.
type Health struct {
	Status  string `json:"status"`
	NodeID  string `json:"node_id"`
	Network string `json:"network"`
	Enclave string `json:"enclave"`
}

// Client issues requests to one node.
type Client struct {
	endpoint string
	http     *http.Client
}

// New returns a client for endpoint, which must already be normalized (see
// NormalizeEndpoint). A nil httpClient uses a default with no overall
// timeout; callers bound requests through the context.
func New(endpoint string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), http: httpClient}
}

// Endpoint returns the base URL this client targets.
func (c *Client) Endpoint() string { return c.endpoint }

// NormalizeEndpoint turns what a person types into a base URL. Accepted
// forms: a bare host or IP, host:port, or a full http(s) URL. A missing
// scheme becomes http and a missing port becomes DefaultHTTPPort. Any path
// component is rejected because the client appends /v1/... itself.
func NormalizeEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("endpoint must not be empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid endpoint: scheme must be http or https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return "", errors.New("invalid endpoint: missing host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid endpoint: credentials, query, and fragment are not allowed")
	}
	if p := strings.TrimRight(u.Path, "/"); p != "" {
		return "", fmt.Errorf("invalid endpoint: path %q is not allowed", u.Path)
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(DefaultHTTPPort))
	} else if _, err := strconv.Atoi(u.Port()); err != nil {
		return "", fmt.Errorf("invalid endpoint: bad port %q", u.Port())
	}
	u.Path = ""
	u.RawPath = ""
	return u.String(), nil
}

func (c *Client) dataURL(key string) string {
	return c.endpoint + "/v1/data/" + url.PathEscape(key)
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &UnreachableError{Endpoint: c.endpoint, Err: err}
	}
	return resp, nil
}

// readBodyPreview drains and returns up to 512 bytes of a response body for
// error messages.
func readBodyPreview(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}

func unexpectedStatus(resp *http.Response) error {
	return &StatusError{StatusCode: resp.StatusCode, Body: readBodyPreview(resp.Body)}
}

// Put stores data under key with the given TTL in seconds. A ttlSeconds of
// zero omits the header and lets the node apply its default.
func (c *Client) Put(ctx context.Context, key string, data []byte, ttlSeconds int) (*PutResult, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.dataURL(key), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	if ttlSeconds > 0 {
		req.Header.Set("X-TTL", strconv.Itoa(ttlSeconds))
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	result := &PutResult{Key: key, RequestedTTL: ttlSeconds}
	switch resp.StatusCode {
	case http.StatusCreated:
		result.Status = WriteConfirmed
	case http.StatusAccepted:
		result.Status = WritePending
	case http.StatusInsufficientStorage:
		return nil, ErrStoreFull
	case http.StatusBadRequest:
		return nil, fmt.Errorf("%w: %s", ErrBadKey, readBodyPreview(resp.Body))
	default:
		return nil, unexpectedStatus(resp)
	}
	io.Copy(io.Discard, resp.Body)
	return result, nil
}

func parseMetadata(key string, h http.Header) Metadata {
	m := Metadata{Key: key}
	if t, err := time.Parse(time.RFC3339, h.Get("X-Created-At")); err == nil {
		m.CreatedAt = t
	}
	if n, err := strconv.Atoi(h.Get("X-Original-TTL")); err == nil {
		m.OriginalTTL = time.Duration(n) * time.Second
	}
	if n, err := strconv.Atoi(h.Get("X-Remaining-TTL")); err == nil {
		m.RemainingTTL = time.Duration(n) * time.Second
	}
	return m
}

// Get retrieves the value for key. The returned bytes are exactly what the
// node holds; an empty payload is a valid value distinct from ErrNotFound.
func (c *Client) Get(ctx context.Context, key string) (*Value, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.dataURL(key), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusBadRequest:
		return nil, fmt.Errorf("%w: %s", ErrBadKey, readBodyPreview(resp.Body))
	default:
		return nil, unexpectedStatus(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &UnreachableError{Endpoint: c.endpoint, Err: fmt.Errorf("reading response body: %w", err)}
	}
	return &Value{Metadata: parseMetadata(key, resp.Header), Data: data}, nil
}

// Exists checks for key with a HEAD request and returns its local metadata
// without transferring the payload.
func (c *Client) Exists(ctx context.Context, key string) (*Metadata, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.dataURL(key), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		m := parseMetadata(key, resp.Header)
		return &m, nil
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusBadRequest:
		return nil, ErrBadKey
	default:
		return nil, &StatusError{StatusCode: resp.StatusCode}
	}
}

// ListKeys returns one page of live keys on the contacted node. A limit of
// zero requests an unbounded page. Pass the previous page's NextCursor to
// continue; an empty NextCursor means the listing is complete as of that
// request. The node may serialize an empty page as null; Keys is never nil.
func (c *Client) ListKeys(ctx context.Context, prefix string, limit int, cursor string) (*KeyPage, error) {
	q := url.Values{}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	u := c.endpoint + "/v1/keys"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body struct {
		Keys       []string `json:"keys"`
		NextCursor string   `json:"next_cursor"`
	}
	if err := c.getJSON(ctx, u, &body); err != nil {
		return nil, err
	}
	page := &KeyPage{Keys: body.Keys, NextCursor: body.NextCursor}
	if page.Keys == nil {
		page.Keys = []string{}
	}
	return page, nil
}

// ListAllKeys walks every page of a listing. The pages are not a snapshot:
// keys can appear, expire, or be overwritten between requests.
func (c *Client) ListAllKeys(ctx context.Context, prefix string, pageSize int) ([]string, error) {
	if pageSize <= 0 {
		pageSize = 1000
	}
	all := []string{}
	cursor := ""
	for {
		page, err := c.ListKeys(ctx, prefix, pageSize, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Keys...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// Health returns the node's self-reported identity.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	var h Health
	if err := c.getJSON(ctx, c.endpoint+"/v1/health", &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// Status returns the raw JSON document from /v1/status.
func (c *Client) Status(ctx context.Context) (json.RawMessage, error) {
	return c.getRaw(ctx, c.endpoint+"/v1/status")
}

// Topology returns the raw JSON document from /v1/topology.
func (c *Client) Topology(ctx context.Context) (json.RawMessage, error) {
	return c.getRaw(ctx, c.endpoint+"/v1/topology")
}

// Metrics returns the Prometheus exposition text from /v1/metrics.
func (c *Client) Metrics(ctx context.Context) ([]byte, error) {
	return c.getRaw(ctx, c.endpoint+"/v1/metrics")
}

func (c *Client) getRaw(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, unexpectedStatus(resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &UnreachableError{Endpoint: c.endpoint, Err: fmt.Errorf("reading response body: %w", err)}
	}
	return body, nil
}

func (c *Client) getJSON(ctx context.Context, u string, into any) error {
	body, err := c.getRaw(ctx, u)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, into); err != nil {
		return &StatusError{StatusCode: http.StatusOK, Body: "response is not valid JSON: " + err.Error()}
	}
	return nil
}
