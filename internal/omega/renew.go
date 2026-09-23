package omega

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

type RenewOptions struct {
	Home, Network string
	Disposable    bool
	HTTPClient    *http.Client
}

const renewalProvisionAction = "Check --home. For first-time setup, run soph omega provision-renewal from the existing offline authority home; restore the operational home and journal if it was already provisioned."

// Renew is a scheduler-friendly single invocation. It completes an interrupted
// release first, renews after six hours, or verifies the existing release when
// not due. No code path opens an offline authority or signs targets/root.
func Renew(ctx context.Context, opts RenewOptions) (Report, error) {
	return renew(ctx, opts, time.Time{}, nil)
}

func renew(ctx context.Context, opts RenewOptions, now time.Time, hook func(string) error) (report Report, resultErr error) {
	if !opts.Disposable || !networkID.MatchString(opts.Network) {
		return report, errors.New("omega: renewal requires a network and --disposable")
	}
	report = Report{Schema: 1, State: "invalid", Network: opts.Network, OperationalHome: opts.Home, Publication: "failed",
		Action: "Check --home, ownership, and private-directory permissions; retry when the operational home is accessible."}
	home, err := openHome(ctx, opts.Home, opts.Network, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			report.State = "absent"
			report.Action = renewalProvisionAction
		}
		err = fmt.Errorf("omega: cannot open operational home: %w", err)
		report.Problem = err.Error()
		return report, err
	}
	defer home.close()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	home.hook = hook
	custody, err := readRenewal(home, opts.Network)
	if err != nil {
		report.Action = "Preserve the operational home and restore verified renewal custody and its publication journal; never initialize a replacement authority."
		if errors.Is(err, os.ErrNotExist) && rejectOrphanedOperationalState(home, opts.Network) == nil {
			report.State = "unprovisioned"
			report.Action = renewalProvisionAction
		}
		err = fmt.Errorf("omega: renewal custody is unavailable: %w", err)
		report.Problem = err.Error()
		return report, err
	}
	bundle := custody.Bundle
	report, err = inspectBundle(bundle, now)
	if err != nil && !errors.Is(err, errRootExpired) {
		return report, err
	}
	report.State = "renewal_ready"
	report.OperationalHome = home.root.Name()
	defer func() {
		if resultErr != nil {
			report.Publication = "failed"
			report.Problem = resultErr.Error()
			report.Action = "Preserve the operational journal and retry publish --renew. Expired root or membership approval requires the offline authority; never reset versions."
			if errors.Is(resultErr, errOfflineHandoff) {
				report.Action = "Mount the offline home and rerun the original soph omega rotate with its original --role, --root-version and --apply digest (and --renew-approval for root). Preserve the journal; restore corrupt committed handoff records instead of replacing them."
			}
		}
	}()
	state, binding, err := openPublication(home, bundle, nil, false)
	if err != nil {
		return report, err
	}
	defer state.close()
	history, err := loadReleases(state, bundle)
	if err != nil {
		return report, err
	}
	if len(history) == 0 {
		return report, errors.New("omega: no approved membership; publish using the offline authority first")
	}
	latest := history[len(history)-1]
	if err := applyReleaseRoot(&report, bundle, latest); err != nil {
		return report, err
	}
	m, expires, err := latest.validate(bundle)
	if err != nil {
		return report, err
	}
	report.Release = publicationReport(latest, m, expires, now)
	if now.Before(latest.Created) {
		return report, errors.New("omega: clock predates latest release")
	}
	receipt, receiptErr := readVerification(state, latest)
	if receiptErr != nil && !errors.Is(receiptErr, os.ErrNotExist) {
		return report, receiptErr
	}
	if now.Before(receipt.CheckedAt) {
		return report, errors.New("omega: clock moved backward since the last publication check")
	}
	report.Release.CheckedAt, report.Release.VerifiedAt, report.Release.LastError = receipt.CheckedAt, receipt.VerifiedAt, receipt.Error
	repo, err := openRepository(ctx, binding.Directory, home.root.Name())
	if err != nil {
		return report, err
	}
	defer repo.close()
	repo.hook = hook
	if err := checkRecordedDestination(repo, history); err != nil {
		return report, err
	}
	history, resumed, err := resumeRotationApply(ctx, state, bundle, history, now, custody.Keys)
	if err != nil {
		return report, err
	}
	if resumed {
		latest = history[len(history)-1]
		return publishPrepared(ctx, state, repo, bundle, history, latest, report, now, opts.HTTPClient)
	}
	for _, name := range []string{rotationTargetsName(latest.Version + 1), rotationTargetsName(latest.Version+1) + ".pending"} {
		if _, err := state.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return report, errors.New("omega: targets handoff has no apply reservation; restore its journal before renewal")
		}
	}
	keys, err := activeOnlineKeys(state, bundle, latest, custody.Keys)
	if err != nil {
		return report, err
	}

	name := releaseName(latest.Version + 1)
	pending, pendingErr := state.read(name + ".pending")
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return report, pendingErr
	}
	intentName := renewalIntentName(latest.Version + 1)
	intentData, intentErr := state.read(intentName)
	intentCommitted := intentErr == nil
	if errors.Is(intentErr, os.ErrNotExist) {
		intentData, intentErr = state.read(intentName + ".pending")
	}
	if intentErr != nil && !errors.Is(intentErr, os.ErrNotExist) {
		return report, intentErr
	}
	// No renewal may claim a pending offline approval, including a torn write.
	if pendingErr == nil && intentErr != nil {
		return report, errors.New("omega: pending release needs its original offline publish or recovery; renewal will not replace it")
	}
	var intent renewalIntent
	if intentErr == nil && json.Valid(intentData) {
		if err := decodeRecord(intentData, &intent); err != nil {
			return report, err
		}
		if intent.Schema != 1 || intent.Version != latest.Version+1 || intent.Previous != digest(record(latest)) ||
			intent.Created.IsZero() || intent.Created.Before(latest.Created) || now.Before(intent.Created) {
			return report, errors.New("omega: invalid renewal intent or clock predates it")
		}
	} else {
		if pendingErr == nil {
			return report, errors.New("omega: renewal intent is missing or torn after signing; restore the journal")
		}
		if intentCommitted {
			return report, errors.New("omega: committed renewal intent is corrupt; restore the journal")
		}
		if !now.Before(expires["targets"]) {
			// A torn, uncommitted intent cannot have signed anything. Release
			// that empty reservation so offline reapproval can use the next
			// version. Complete valid intents instead, even when expired: their
			// recorded version must be preserved before a higher repair.
			if intentErr == nil {
				if err := state.root.Remove(intentName + ".pending"); err != nil {
					return report, err
				}
				if err := state.syncDir(); err != nil {
					return report, err
				}
			}
			return report, errors.New("omega: membership approval expired; offline reapproval is required")
		}
		expired := !now.Before(expires["timestamp"])
		verified := receiptErr == nil && receipt.Error == "" && !receipt.VerifiedAt.IsZero()
		if intentErr != nil && !expired && (!verified || !report.Release.RenewalDue) {
			return publishPrepared(ctx, state, repo, bundle, history, latest, report, now, opts.HTTPClient)
		}
		intent = renewalIntent{Schema: 1, Version: latest.Version + 1, Previous: digest(record(latest)), Created: now.Truncate(time.Second)}
	}
	if intent.Version > maxReleases {
		return report, errors.New("omega: publication journal version limit reached")
	}
	if err := state.install(intentName, record(intent)); err != nil {
		return report, err
	}
	if err := state.syncDir(); err != nil {
		return report, err
	}
	if err := state.phase("renewal:intent-durable"); err != nil {
		return report, err
	}
	r, err := prepareRenewal(keys, bundle, latest, intent.Created)
	if err != nil {
		return report, err
	}
	if pendingErr == nil && json.Valid(pending) && !bytes.Equal(pending, record(r)) {
		return report, errors.New("omega: pending renewal differs from its durable intent; preserve the journal")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := state.install(name, record(r)); err != nil {
		return report, err
	}
	history = append(history, r)
	return publishPrepared(ctx, state, repo, bundle, history, r, report, now, opts.HTTPClient)
}

type renewalIntent struct {
	Schema   int       `json:"schema"`
	Version  int64     `json:"version"`
	Previous string    `json:"previous_sha256"`
	Created  time.Time `json:"created"`
}

func renewalIntentName(version int64) string { return fmt.Sprintf("%d.renewal-intent.json", version) }
