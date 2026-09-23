package omega

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"sopholeth/internal/trust/bootstrap"
)

// VercelConfig identifies a metadata-only project, never the docs project.
// Credentials are deliberately absent from the durable configuration.
type VercelConfig struct {
	Schema      int    `json:"schema"`
	ProjectID   string `json:"project_id"`
	TeamID      string `json:"team_id"`
	ProjectName string `json:"project_name"`
}

type VercelOptions struct {
	Config VercelConfig
	Token  string
	// Test seams are not exposed through config, environment, or command flags.
	apiURL       string
	apiClient    *http.Client
	pollInterval time.Duration
	previewURL   func(string) string
}

var vercelID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var vercelName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

func ParseVercelConfig(data []byte) (VercelConfig, error) {
	var c VercelConfig
	invalid := errors.New("omega: invalid Vercel config (expected schema, project_id, team_id, project_name)")
	if len(data) > 4096 {
		return c, invalid
	}
	// Operator input allows whitespace and field ordering. Journal records
	// remain canonical; do not apply their byte-for-byte check to this file.
	d := json.NewDecoder(bytes.NewReader(data))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return c, invalid
	}
	fields := map[string]any{"schema": &c.Schema, "project_id": &c.ProjectID, "team_id": &c.TeamID, "project_name": &c.ProjectName}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || fields[name] == nil {
			return c, invalid
		}
		if err := d.Decode(fields[name]); err != nil {
			return c, invalid
		}
		delete(fields, name) // Reject duplicate fields as well as unknown ones.
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') || len(fields) != 0 {
		return c, invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return c, invalid
	}
	return c, c.validate()
}

func (c VercelConfig) validate() error {
	if c.Schema != 1 || !strings.HasPrefix(c.ProjectID, "prj_") || !vercelID.MatchString(c.ProjectID) ||
		!strings.HasPrefix(c.TeamID, "team_") || !vercelID.MatchString(c.TeamID) || !vercelName.MatchString(c.ProjectName) {
		return errors.New("omega: Vercel config requires schema 1, prj_ and team_ IDs, and a project name")
	}
	return nil
}

type vercelDeployment struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	ProjectID  string `json:"projectId"`
	ReadyState string `json:"readyState"`
}

type vercelProject struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	AccountID      string          `json:"accountId"`
	Link           json.RawMessage `json:"link"`
	RollingRelease json.RawMessage `json:"rollingRelease"`
	Targets        struct {
		Production struct {
			ID string `json:"id"`
		} `json:"production"`
	} `json:"targets"`
	LastAliasRequest struct {
		JobStatus      string `json:"jobStatus"`
		ToDeploymentID string `json:"toDeploymentId"`
	} `json:"lastAliasRequest"`
}

func (o *VercelOptions) request(ctx context.Context, method, path string, body any, out any) error {
	if o.Token == "" || strings.ContainsAny(o.Token, "\r\n") {
		return errors.New("omega: set VERCEL_TOKEN in the publisher environment")
	}
	base := "https://api.vercel.com"
	if o.apiURL != "" {
		base = o.apiURL
	}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path+"?teamId="+url.QueryEscape(o.Config.TeamID), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+o.Token)
	req.Header.Set("Content-Type", "application/json")
	hc := &http.Client{Timeout: 30 * time.Second}
	if o.apiClient != nil {
		*hc = *o.apiClient
	}
	hc.Jar = nil
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("omega: Vercel API request: %w", err)
	}
	defer response.Body.Close()
	// Never include an upstream body: it may echo credentials or request data.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("omega: Vercel %s %s returned HTTP %d", method, path, response.StatusCode)
	}
	if out == nil {
		return nil
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 || json.Unmarshal(data, out) != nil {
		return errors.New("omega: invalid or oversized Vercel API response")
	}
	return nil
}

func (o *VercelOptions) project(ctx context.Context) (vercelProject, error) {
	var p vercelProject
	err := o.request(ctx, http.MethodGet, "/v9/projects/"+o.Config.ProjectID, nil, &p)
	if err != nil {
		return p, err
	}
	if p.ID != o.Config.ProjectID || p.Name != o.Config.ProjectName || p.AccountID != o.Config.TeamID {
		return p, errors.New("omega: Vercel project identity differs from configuration")
	}
	if presentJSON(p.Link) || presentJSON(p.RollingRelease) {
		return p, errors.New("omega: metadata project must have no Git integration or rolling releases")
	}
	return p, nil
}

func presentJSON(raw json.RawMessage) bool {
	return len(raw) != 0 && string(raw) != "null" && string(raw) != "false"
}

func (o *VercelOptions) pause(ctx context.Context) error {
	d := time.Second
	if o.pollInterval > 0 {
		d = o.pollInterval
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (o *VercelOptions) ready(ctx context.Context, id string) (vercelDeployment, error) {
	for {
		var d vercelDeployment
		if err := o.request(ctx, http.MethodGet, "/v13/deployments/"+id, nil, &d); err != nil {
			return d, err
		}
		if d.ID != id || d.ProjectID != o.Config.ProjectID {
			return d, errors.New("omega: Vercel deployment identity mismatch")
		}
		switch d.ReadyState {
		case "READY":
			return d, nil
		case "ERROR", "CANCELED":
			return d, errors.New("omega: Vercel deployment failed; retry to stage the same release again")
		case "QUEUED", "INITIALIZING", "BUILDING":
		default:
			return d, errors.New("omega: unknown Vercel deployment state")
		}
		if err := o.pause(ctx); err != nil {
			return d, err
		}
	}
}

// Stage without assigning the project's production domains. A lost response can leave an
// unused deployment, but that deployment can never publish itself later.
func (o *VercelOptions) stage(ctx context.Context, objects []publicObject, repository string) (vercelDeployment, error) {
	u, _ := url.Parse(repository)
	files := []map[string]string{}
	total := 0
	for _, obj := range objects {
		total += len(obj.data)
		if total > 32<<20 {
			return vercelDeployment{}, errors.New("omega: retained Vercel upload exceeds 32 MiB; preserve history and review hosting capacity")
		}
		files = append(files, map[string]string{"file": strings.TrimPrefix(u.Path, "/") + "/" + obj.name, "data": base64.StdEncoding.EncodeToString(obj.data), "encoding": "base64"})
		files[len(files)-1]["file"] = strings.TrimPrefix(files[len(files)-1]["file"], "/")
	}
	files = append(files, map[string]string{"file": "vercel.json", "data": string(vercelRoutes(u.Path))},
		map[string]string{"file": "omega-not-found.json", "data": "{\"error\":\"Metadata not found\"}\n"})
	var d vercelDeployment
	err := o.request(ctx, http.MethodPost, "/v13/deployments", map[string]any{
		"name": o.Config.ProjectName, "project": o.Config.ProjectID, "target": "production",
		"autoAssignCustomDomains": false, "files": files,
		"projectSettings": map[string]any{"framework": nil, "buildCommand": "", "installCommand": "", "outputDirectory": ".", "rootDirectory": nil},
	}, &d)
	if err != nil {
		return d, err
	}
	if !strings.HasPrefix(d.ID, "dpl_") || !vercelID.MatchString(d.ID) {
		return d, errors.New("omega: invalid Vercel deployment ID")
	}
	return d, nil
}

const immutableCache = "public, max-age=31536000, immutable"

func vercelRoutes(basePath string) []byte {
	prefix := regexp.QuoteMeta(basePath)
	immutable := prefix + `/(?:[1-9][0-9]*\.(?:root|snapshot|targets)\.json|targets/[a-f0-9]{64}\.bootstrap\.json)`
	allowed := strings.TrimPrefix(prefix+`/timestamp\.json`, "/") + "|" + strings.TrimPrefix(immutable, "/")
	headers := func(value string) map[string]string {
		return map[string]string{"Cache-Control": value, "Vercel-CDN-Cache-Control": value}
	}
	missing := map[string]any{"src": "/.*", "status": 404, "dest": "/omega-not-found.json", "headers": headers("no-store")}
	return record(map[string]any{"version": 2, "framework": nil, "installCommand": "", "buildCommand": "", "outputDirectory": ".", "routes": []any{
		map[string]any{"src": "^" + prefix + `/timestamp\.json$`, "headers": headers("no-store"), "continue": true},
		map[string]any{"src": "^/(?!(?:" + allowed + ")$).*", "status": 404, "dest": "/omega-not-found.json", "headers": headers("no-store")},
		map[string]string{"handle": "filesystem"}, missing,
		map[string]string{"handle": "hit"},
		map[string]any{"src": "^" + immutable + "$", "headers": headers(immutableCache), "continue": true},
	}})
}

// Reconstruct only authenticated public objects. Never walk the repository or
// custody directory to decide what to upload, and retain every historical object.
func retainedObjects(bundle bootstrap.Bundle, history []release) ([]publicObject, error) {
	all := map[string][]byte{}
	for _, r := range history {
		for _, obj := range r.objects(bundle) {
			if previous, ok := all[obj.name]; ok && !bytes.Equal(previous, obj.data) {
				return nil, errors.New("omega: immutable history conflict")
			}
			all[obj.name] = obj.data
		}
	}
	all["timestamp.json"] = history[len(history)-1].Timestamp
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	objects := make([]publicObject, 0, len(names))
	for _, name := range names {
		objects = append(objects, publicObject{name, all[name]})
	}
	return objects, nil
}

func verifyHostedObjects(ctx context.Context, hc *http.Client, base string, objects []publicObject) error {
	rootVersion := 1
	for _, obj := range objects {
		if strings.HasSuffix(obj.name, ".root.json") {
			n, _ := strconv.Atoi(strings.TrimSuffix(obj.name, ".root.json"))
			rootVersion = max(rootVersion, n)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+obj.name, nil)
		if err != nil {
			return err
		}
		response, err := hc.Do(req)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, int64(len(obj.data))+1))
		response.Body.Close()
		cache := "no-store"
		if obj.name != "timestamp.json" {
			cache = immutableCache
		}
		if readErr != nil {
			return readErr
		}
		if response.StatusCode != http.StatusOK || !bytes.Equal(data, obj.data) || response.Header.Get("Cache-Control") != cache {
			return fmt.Errorf("omega: hosted %s differs in bytes, HTTP status, or cache policy", obj.name)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%d.root.json", base, rootVersion+1), nil)
	if err != nil {
		return err
	}
	response, err := hc.Do(req)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound || response.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return errors.New("omega: missing next root must return an uncached JSON 404")
	}
	return nil
}

func checkHostedTimestamp(ctx context.Context, hc *http.Client, repository string, history []release) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repository+"/timestamp.json", nil)
	if err != nil {
		return err
	}
	response, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		if response.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
			return errors.New("omega: empty repository must return an uncached JSON 404; configure hosting before deployment")
		}
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("omega: live timestamp preflight returned HTTP %d; check hosting before deployment", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil {
		return err
	}
	for _, r := range history {
		if bytes.Equal(data, r.Timestamp) {
			return nil
		}
	}
	return errors.New("omega: live timestamp is absent from retained history; refusing to deploy a restored or conflicting journal")
}
