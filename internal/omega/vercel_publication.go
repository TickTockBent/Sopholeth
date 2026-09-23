package omega

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"sopholeth/internal/trust/bootstrap"
)

const vercelBindingName = "vercel-binding.json"
const vercelDeploymentName = "vercel-deployment.json"

var errVercelPromotionFailed = errors.New("omega: Vercel promotion failed; retry the same release")

type vercelReceipt struct {
	Schema        int    `json:"schema"`
	Version       int64  `json:"version"`
	ReleaseSHA256 string `json:"release_sha256"`
	ConfigSHA256  string `json:"config_sha256"`
	DeploymentID  string `json:"deployment_id"`
	Phase         string `json:"phase"` // staged, promoting, settled
}

func isVercelJournalFile(name string) bool {
	name = strings.TrimSuffix(name, ".pending")
	return name == vercelBindingName || name == vercelDeploymentName
}

func bindVercel(state *store, opts *VercelOptions) error {
	data, err := state.read(vercelBindingName)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if opts == nil {
		if err == nil {
			return errors.New("omega: this journal requires --vercel-config; retain the configured metadata project")
		}
		if _, pending := state.read(vercelBindingName + ".pending"); !errors.Is(pending, os.ErrNotExist) {
			return errors.New("omega: interrupted Vercel binding requires the same --vercel-config")
		}
		return nil
	}
	if e := opts.Config.validate(); e != nil {
		return e
	}
	expected := record(opts.Config)
	if err == nil {
		if !bytes.Equal(data, expected) {
			return errors.New("omega: Vercel project differs from the journal binding")
		}
		return nil
	}
	if data, e := state.read(vercelBindingName + ".pending"); e == nil && len(data) > 0 {
		var previous VercelConfig
		if decodeRecord(data, &previous) == nil && !bytes.Equal(data, expected) {
			return errors.New("omega: pending Vercel binding differs; retry the original configuration")
		}
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return state.install(vercelBindingName, expected)
}

func readVercelReceipt(state *store, opts *VercelOptions, history []release) (vercelReceipt, error) {
	var receipt vercelReceipt
	data, err := state.read(vercelDeploymentName)
	pendingOnly := false
	if errors.Is(err, os.ErrNotExist) {
		data, err = state.read(vercelDeploymentName + ".pending")
		if errors.Is(err, os.ErrNotExist) {
			return receipt, nil
		}
		if err == nil && !json.Valid(data) {
			// The first receipt is staged; no promotion is possible before it
			// commits. A torn first write may leave an unused remote deployment.
			if err := state.root.Remove(vercelDeploymentName + ".pending"); err != nil {
				return receipt, err
			}
			return receipt, state.syncDir()
		}
		pendingOnly = true
	}
	if err != nil {
		return receipt, err
	}
	if decodeRecord(data, &receipt) != nil || receipt.Schema != 1 || receipt.Version < 1 || receipt.Version > int64(len(history)) ||
		receipt.ReleaseSHA256 != digest(record(history[receipt.Version-1])) || receipt.ConfigSHA256 != digest(record(opts.Config)) ||
		!strings.HasPrefix(receipt.DeploymentID, "dpl_") || !vercelID.MatchString(receipt.DeploymentID) ||
		(receipt.Phase != "staged" && receipt.Phase != "promoting" && receipt.Phase != "settled") {
		return receipt, errors.New("omega: invalid deployment receipt; restore the publication journal")
	}
	if pendingOnly {
		if receipt.Phase != "staged" {
			return receipt, errors.New("omega: committed deployment receipt is missing; restore the publication journal")
		}
		if err := state.replaceRecord(vercelDeploymentName, data); err != nil {
			return receipt, err
		}
	}
	return receipt, nil
}

func (o *VercelOptions) settle(ctx context.Context, id string) error {
	for {
		p, err := o.project(ctx)
		if err != nil {
			return err
		}
		// Vercel can complete a promotion synchronously without recording a
		// lastAliasRequest. The production target then confirms its outcome.
		if p.LastAliasRequest.JobStatus == "" && p.Targets.Production.ID == id {
			return nil
		}
		if p.LastAliasRequest.ToDeploymentID != id {
			return errors.New("omega: promotion outcome is unknown; retry the same release and preserve the deployment receipt")
		}
		switch p.LastAliasRequest.JobStatus {
		case "succeeded":
			return nil
		case "failed", "skipped":
			return errVercelPromotionFailed
		case "pending", "in-progress":
		default:
			return errors.New("omega: unknown Vercel promotion state; preserve the deployment receipt")
		}
		if err := o.pause(ctx); err != nil {
			return err
		}
	}
}

func deployVercel(ctx context.Context, state *store, bundle bootstrap.Bundle, history []release, opts *VercelOptions, hc *http.Client) error {
	if opts == nil {
		return nil
	}
	r := history[len(history)-1]
	receipt, err := readVercelReceipt(state, opts, history)
	if err != nil {
		return err
	}
	p, err := opts.project(ctx)
	if err != nil {
		return err
	}
	// Serialize asynchronous promotions across process death, not merely while
	// the local OS lock is held. Staged deployments cannot change aliases.
	if receipt.Phase == "promoting" && receipt.Version != r.Version {
		if err := opts.settle(ctx, receipt.DeploymentID); err != nil && !errors.Is(err, errVercelPromotionFailed) {
			return err
		}
		// A terminal failure also settles the old operation. It must not
		// prevent renewal after that release has expired.
		receipt.Phase = "settled"
		if err := state.replaceRecord(vercelDeploymentName, record(receipt)); err != nil {
			return err
		}
	}
	if p.LastAliasRequest.JobStatus == "pending" || p.LastAliasRequest.JobStatus == "in-progress" {
		if p.LastAliasRequest.ToDeploymentID != receipt.DeploymentID {
			return errors.New("omega: another Vercel promotion is active; wait and retry")
		}
		if err := opts.settle(ctx, receipt.DeploymentID); err != nil {
			return err
		}
	}
	objects, err := retainedObjects(bundle, history)
	if err != nil {
		return err
	}
	// An hourly retry verifies the live release without creating a deployment.
	if receipt.Version == r.Version && receipt.Phase == "settled" {
		return verifyHostedObjects(ctx, hc, bundle.Repository, objects)
	}
	if err := checkHostedTimestamp(ctx, hc, bundle.Repository, history); err != nil {
		return err
	}
	var deployment vercelDeployment
	if receipt.Version == r.Version {
		deployment, err = opts.ready(ctx, receipt.DeploymentID)
		if err != nil && deployment.ReadyState != "ERROR" && deployment.ReadyState != "CANCELED" {
			return err
		}
	}
	if receipt.Version != r.Version || deployment.ReadyState == "ERROR" || deployment.ReadyState == "CANCELED" {
		deployment, err = opts.stage(ctx, objects, bundle.Repository)
		if err != nil {
			return err
		}
		receipt = vercelReceipt{Schema: 1, Version: r.Version, ReleaseSHA256: digest(record(r)), ConfigSHA256: digest(record(opts.Config)), DeploymentID: deployment.ID, Phase: "staged"}
		if err := state.replaceRecord(vercelDeploymentName, record(receipt)); err != nil {
			return err
		}
		if err := state.phase("vercel:staged"); err != nil {
			return err
		}
		deployment, err = opts.ready(ctx, receipt.DeploymentID)
		if err != nil {
			return err
		}
	}
	// Verify the isolated deployment before allowing it to become public.
	staged := "https://" + deployment.URL
	u, parseErr := url.Parse(staged)
	if parseErr != nil || u.User != nil || u.Port() != "" || !strings.HasSuffix(u.Hostname(), ".vercel.app") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("omega: invalid Vercel deployment hostname")
	}
	if opts.previewURL != nil {
		staged = opts.previewURL(staged)
	}
	repository, _ := url.Parse(bundle.Repository)
	if err := verifyHostedObjects(ctx, hc, staged+repository.Path, objects); err != nil {
		return fmt.Errorf("omega: staged deployment verification: %w", err)
	}
	_, expires, err := r.validate(bundle)
	if err != nil {
		return err
	}
	if !time.Now().Before(expires["timestamp"]) {
		return errors.New("omega: release expired while staging; renew before promotion")
	}
	completedSynchronously := p.LastAliasRequest.JobStatus == "" && p.Targets.Production.ID == receipt.DeploymentID
	if receipt.Phase != "promoting" || !completedSynchronously && (p.LastAliasRequest.ToDeploymentID != receipt.DeploymentID || p.LastAliasRequest.JobStatus == "failed" || p.LastAliasRequest.JobStatus == "skipped") {
		receipt.Phase = "promoting"
		if err := state.replaceRecord(vercelDeploymentName, record(receipt)); err != nil {
			return err
		}
		if err := state.phase("vercel:promoting"); err != nil {
			return err
		}
		if err := opts.request(ctx, http.MethodPost, "/v10/projects/"+opts.Config.ProjectID+"/promote/"+receipt.DeploymentID, map[string]any{}, nil); err != nil {
			return err
		}
		if err := state.phase("vercel:promotion-submitted"); err != nil {
			return err
		}
	}
	if err := opts.settle(ctx, receipt.DeploymentID); err != nil {
		return err
	}
	receipt.Phase = "settled"
	if err := state.replaceRecord(vercelDeploymentName, record(receipt)); err != nil {
		return err
	}
	return verifyHostedObjects(ctx, hc, bundle.Repository, objects)
}
