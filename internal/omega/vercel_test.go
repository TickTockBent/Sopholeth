//go:build linux

package omega

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVercelPublicationRecoveryAndRenewal(t *testing.T) {
	ctx := context.Background()
	config := VercelConfig{Schema: 1, ProjectID: "prj_test", TeamID: "team_test", ProjectName: "metadata-test"}
	var mu sync.Mutex
	staged, live := map[string][]byte{}, map[string][]byte{}
	creates, promotes := 0, 0
	lastPromotion := ""
	badCache := false
	laggedResponses := 0
	public := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			t.Error("deployment credential leaked to public host")
		}
		files, name := live, strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasPrefix(name, "stage/") {
			files, name = staged, strings.TrimPrefix(name, "stage/")
		}
		data, ok := files[name]
		if promotes == 2 && laggedResponses == 0 && r.URL.Path == "/omega/2.snapshot.json" {
			// The promotion API acknowledged before this edge saw the alias.
			laggedResponses++
			ok = false
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"Metadata not found"}`))
			return
		}
		if !strings.HasSuffix(name, "/timestamp.json") {
			w.Header().Set("Cache-Control", immutableCache)
		}
		if badCache && files != nil && strings.HasPrefix(r.URL.Path, "/stage/") {
			w.Header().Set("Cache-Control", "public, max-age=9999")
		}
		_, _ = w.Write(data)
	}))
	defer public.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.URL.Query().Get("teamId") != config.TeamID {
			t.Error("missing API credential or team")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/v9/projects/"+config.ProjectID:
			// The live API can omit its job record for a synchronous promotion.
			var job any
			if promotes > 1 {
				job = map[string]string{"toDeploymentId": lastPromotion, "jobStatus": "succeeded"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": config.ProjectID, "name": config.ProjectName, "accountId": config.TeamID,
				"targets": map[string]any{"production": map[string]string{"id": lastPromotion}}, "lastAliasRequest": job})
		case r.Method == "POST" && r.URL.Path == "/v13/deployments":
			var body struct {
				Project    string                                  `json:"project"`
				Target     string                                  `json:"target"`
				AutoAssign *bool                                   `json:"autoAssignCustomDomains"`
				Files      []struct{ File, Data, Encoding string } `json:"files"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if body.Project != config.ProjectID || body.Target != "production" || body.AutoAssign == nil || *body.AutoAssign {
				t.Error("unsafe deployment target or automatic promotion")
			}
			staged = map[string][]byte{}
			for _, f := range body.Files {
				data := []byte(f.Data)
				if f.Encoding == "base64" {
					var err error
					data, err = base64.StdEncoding.DecodeString(f.Data)
					if err != nil {
						t.Error(err)
					}
				}
				if strings.Contains(f.File, "authority") || strings.Contains(f.File, "pending") || strings.Contains(string(data), "fixture-token") {
					t.Error("private data in upload")
				}
				staged[f.File] = data
			}
			creates++
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("dpl_%d", creates)})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v13/deployments/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"id": strings.TrimPrefix(r.URL.Path, "/v13/deployments/"), "projectId": config.ProjectID, "url": "fixture.vercel.app", "readyState": "READY"})
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v10/projects/"+config.ProjectID+"/promote/"):
			lastPromotion = strings.TrimPrefix(r.URL.Path, "/v10/projects/"+config.ProjectID+"/promote/")
			live = staged
			promotes++
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected API request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	base := t.TempDir()
	opts := PublishOptions{Home: filepath.Join(base, "custody"), Network: "rehearsal", Directory: filepath.Join(base, "repository"), Manifest: publicationManifest, Version: 1, Disposable: true, HTTPClient: public.Client(),
		Vercel: &VercelOptions{Config: config, Token: "fixture-token", apiURL: api.URL, apiClient: api.Client(), pollInterval: time.Millisecond, previewURL: func(string) string { return public.URL + "/stage" }}}
	created := time.Now().UTC().Truncate(time.Second).Add(-25 * time.Hour)
	_, err := initializeWithCodec(ctx, InitOptions{Home: opts.Home, Network: opts.Network, Repository: public.URL + "/omega", Disposable: true}, created, nil, fastKeyCodec)
	must(t, err)
	// The promotion may finish after the process dies. Retry must observe that
	// outcome, not allocate another release or submit another deployment.
	for _, phase := range []string{"vercel:staged", "vercel:promotion-submitted"} {
		_, err = publish(ctx, opts, created, func(at string) error {
			if at == phase {
				if phase == "vercel:staged" {
					path := filepath.Join(opts.Home, opts.Network+".publication", vercelDeploymentName)
					must(t, os.Rename(path, path+".pending")) // Complete initial write before its rename.
				}
				return errors.New("interrupted deployment")
			}
			return nil
		})
		if err == nil {
			t.Fatalf("missed %s interruption", phase)
		}
	}
	report, err := Publish(ctx, opts)
	must(t, err)
	if report.Publication != "verified" || creates != 1 || promotes != 1 {
		t.Fatalf("retry repeated deployment: %+v creates=%d promotes=%d", report, creates, promotes)
	}
	// Custody transfer must preserve hosting binding and in-flight receipts.
	online := RenewOptions{Home: filepath.Join(base, "online"), Network: opts.Network, Disposable: true, HTTPClient: public.Client(), Vercel: opts.Vercel}
	_, err = ProvisionRenewal(ctx, ProvisionRenewalOptions{Home: opts.Home, Network: opts.Network, RenewalHome: online.Home, Disposable: true})
	must(t, err)
	mu.Lock()
	firstTimestamp := bytes.Clone(live["omega/timestamp.json"])
	badCache = true
	mu.Unlock()
	report, err = Renew(ctx, online)
	if err == nil || report.Publication != "failed" {
		t.Fatal("bad staged cache policy was published")
	}
	mu.Lock()
	if creates != 2 || promotes != 1 || !bytes.Equal(firstTimestamp, live["omega/timestamp.json"]) {
		t.Error("failed staging changed live publication")
	}
	badCache = false
	mu.Unlock()
	report, err = Renew(ctx, online)
	must(t, err)
	if report.Release.Version != 2 || creates != 2 || promotes != 2 || laggedResponses != 1 {
		t.Fatalf("renewal retry did not reuse staged release: %+v", report)
	}
	_, err = Renew(ctx, online)
	must(t, err)
	if creates != 2 || promotes != 2 {
		t.Fatal("hourly verification deployed unchanged metadata")
	}
	_, err = VerifyPublication(ctx, online.Home, online.Network, online.HTTPClient)
	must(t, err)
	// A restored local journal must not replace unknown newer remote metadata.
	mu.Lock()
	currentTimestamp := bytes.Clone(live["omega/timestamp.json"])
	live["omega/timestamp.json"] = []byte(`{"unknown":"newer remote history"}`)
	mu.Unlock()
	opts.Version = 3
	_, err = Publish(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "absent from retained history") {
		t.Fatalf("unknown remote publication was replaced: %v", err)
	}
	mu.Lock()
	if creates != 2 || promotes != 2 {
		t.Error("remote conflict created or promoted a deployment")
	}
	live["omega/timestamp.json"] = currentTimestamp
	mu.Unlock()
	mu.Lock()
	for _, name := range []string{"omega/1.root.json", "omega/1.snapshot.json", "omega/2.snapshot.json", "omega/1.targets.json"} {
		if len(live[name]) == 0 {
			t.Errorf("lost history: %s", name)
		}
	}
	mu.Unlock()
	// Dropping or switching config cannot silently change an established backend.
	changed := *opts.Vercel
	changed.Config.ProjectID = "prj_other"
	for _, hosting := range []*VercelOptions{nil, &changed} {
		bad := online
		bad.Vercel = hosting
		if _, err := Renew(ctx, bad); err == nil || !strings.Contains(err.Error(), "Vercel") && !strings.Contains(err.Error(), "vercel") {
			t.Fatalf("accepted changed hosting: %v", err)
		}
	}
	for _, name := range []string{vercelBindingName, vercelDeploymentName} {
		data := file(t, filepath.Join(onlineState(online), name))
		if bytes.Contains(data, []byte("fixture-token")) {
			t.Fatal("credential persisted in journal")
		}
	}
}

func TestVercelConfigAndAPIConfinement(t *testing.T) {
	config := VercelConfig{Schema: 1, ProjectID: "prj_test", TeamID: "team_test", ProjectName: "metadata-test"}
	_, err := ParseVercelConfig(record(config))
	must(t, err)
	_, err = ParseVercelConfig(file(t, "../../docs/examples/omega/vercel.json"))
	must(t, err)
	parsed, err := ParseVercelConfig([]byte(`{ "project_name": "metadata-test", "team_id": "team_test", "project_id": "prj_test", "schema": 1 }`))
	must(t, err)
	if parsed != config {
		t.Fatal("formatting changed the project binding")
	}
	for _, data := range [][]byte{[]byte(`{}`), []byte(`{"schema":1,"project_id":"prj_test","team_id":"team_test","project_name":"test","token":"secret"}`),
		[]byte(`{"schema":1,"schema":1,"project_id":"prj_test","team_id":"team_test","project_name":"test"}`), append(record(config), []byte(`{}`)...), bytes.Repeat([]byte(" "), 4097)} {
		if _, err := ParseVercelConfig(data); err == nil {
			t.Fatal("accepted unsafe config")
		}
	}
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer other.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", other.URL)
		w.WriteHeader(307)
		_, _ = w.Write([]byte("secret"))
	}))
	defer api.Close()
	opts := VercelOptions{Config: config, Token: "secret", apiURL: api.URL, apiClient: api.Client()}
	err = opts.request(context.Background(), "GET", "/v9/projects/prj_test", nil, nil)
	if err == nil || leaked || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect or credential leakage: %v", err)
	}
	// Optional artifact for the real Vercel router smoke check; no authority is created.
	if path := os.Getenv("OMEGA_ROUTING_FIXTURE"); path != "" {
		must(t, os.WriteFile(path, vercelRoutes("/omega"), 0600))
	}
}
