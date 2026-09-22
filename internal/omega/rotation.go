package omega

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Bound the disposable journal's embedded root chains below both the private
// record size limit and the client's per-refresh root rotation limit.
const maxRootVersion = 64

type RotateOptions struct {
	Home, Network string
	RootVersion   int64
	Apply         string // Exact root SHA-256 from a prior preparation report.
	Disposable    bool
	HTTPClient    *http.Client
}

type RotationReport struct {
	State       string              `json:"state"`
	RootVersion int64               `json:"root_version"`
	RootSHA256  string              `json:"root_sha256"`
	Replaces    map[string][]string `json:"replaces"`
	Keys        map[string][]string `json:"keys"`
}

// Rotate prepares by default. Applying requires an already prepared root's
// exact digest and explicit version, so repeating either command cannot create
// another generation. An apply intent reserves the next release before signing.
func Rotate(ctx context.Context, opts RotateOptions) (Report, error) {
	return rotate(ctx, opts, time.Time{}, nil)
}

func rotate(ctx context.Context, opts RotateOptions, now time.Time, hook func(string) error) (report Report, resultErr error) {
	if !opts.Disposable || !networkID.MatchString(opts.Network) || opts.RootVersion < 2 || opts.RootVersion > maxRootVersion {
		return report, errors.New("omega: rotate requires --disposable, a network, and --root-version between 2 and 64")
	}
	report = Report{Schema: 1, State: "invalid", Network: opts.Network, Publication: "not_checked"}
	defer func() {
		if resultErr != nil {
			report.Problem = resultErr.Error()
			report.Action = "Preserve both custody homes and the journal. Resolve the failure and retry the same root version and apply digest; never roll back a transition."
			if opts.Apply != "" {
				report.Publication = "failed"
			}
		}
	}()
	home, err := openHome(ctx, opts.Home, opts.Network, false)
	if err != nil {
		return report, err
	}
	defer home.close()
	home.hook = hook
	current, err := home.subdir(opts.Network)
	if err != nil {
		return report, err
	}
	defer current.close()
	report, err = current.inspect(time.Now().UTC())
	if err != nil {
		return report, err
	}
	_, bundle, err := current.verify()
	if err != nil {
		return report, err
	}
	if bundle.Network != opts.Network {
		return report, errors.New("omega: authority network mismatch")
	}
	online, cleanup, err := operationalHome(ctx, home, bundle)
	if err != nil {
		return report, err
	}
	defer cleanup()
	if online == home {
		return report, errors.New("omega: provision a separate operational home before rotating online keys")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	report.OperationalHome = online.root.Name()
	state, binding, err := openPublication(online, bundle, nil, false)
	if err != nil {
		return report, err
	}
	defer state.close()
	history, err := loadReleases(state, bundle)
	if err != nil {
		return report, err
	}
	if len(history) == 0 {
		return report, errors.New("omega: publish membership before rotating online keys")
	}
	latest := history[len(history)-1]
	if err := applyReleaseRoot(&report, bundle, latest); err != nil {
		return report, err
	}
	if now.Before(latest.Created) {
		return report, errors.New("omega: rotation clock predates publication history")
	}
	if opts.RootVersion > latest.versions().Root+1 {
		return report, errors.New("omega: root versions must be consecutive")
	}
	previous := bundle.Root
	if opts.RootVersion > 2 {
		previous = latest.Roots[opts.RootVersion-3]
	}
	var rotation rotationRecord
	if opts.Apply == "" && opts.RootVersion == latest.versions().Root+1 {
		var a authority
		data, err := current.read("authority.json")
		if err != nil {
			return report, err
		}
		if err := decodeRecord(data, &a); err != nil {
			return report, err
		}
		rotation, err = prepareRotation(state, bundle, previous, opts.RootVersion, a, now)
		if err != nil {
			return report, err
		}
	} else {
		rotation, _, err = readRotation(state, bundle, previous, opts.RootVersion)
		if err != nil {
			return report, fmt.Errorf("omega: prepare this root version before applying it, or restore its journal: %w", err)
		}
	}
	report.Rotation = rotationReport(rotation, previous, "prepared")
	if opts.Apply != "" && opts.Apply != digest(rotation.Root) {
		return report, errors.New("omega: --apply must match the prepared root_sha256 exactly")
	}
	if opts.RootVersion <= latest.versions().Root {
		if !bytes.Equal(rotation.Root, latest.Roots[opts.RootVersion-2]) {
			return report, errors.New("omega: prepared rotation differs from committed history")
		}
		report.Rotation.State, err = appliedRotationState(state, history, opts.RootVersion)
		if err != nil {
			return report, err
		}
	}
	if opts.Apply == "" {
		report.Action = "Review rotation.root_sha256 and replacement key IDs. Apply with the same --root-version and --apply <root_sha256>; retain both custody homes."
		if report.Rotation.State == "applied" {
			report.Action = "This rotation was applied and verified. Check live publication with status --verify and confirm client adoption before retiring old key copies."
		}
		return report, nil
	}
	repo, err := openRepository(ctx, binding.Directory, online.root.Name())
	if err != nil {
		return report, err
	}
	defer repo.close()
	repo.hook = hook
	if err := checkRecordedDestination(repo, history); err != nil {
		return report, err
	}
	if opts.RootVersion == latest.versions().Root+1 {
		if err := reserveRotationApply(state, bundle, latest, rotation, now); err != nil {
			return report, err
		}
		var resumed bool
		history, resumed, err = resumeRotationApply(ctx, state, bundle, history, now)
		if err != nil {
			return report, err
		}
		if !resumed {
			return report, errors.New("omega: rotation apply reservation disappeared")
		}
		latest = history[len(history)-1]
	}
	report.Rotation.State = "applying"
	report, err = publishPrepared(ctx, state, repo, bundle, history, latest, report, now, opts.HTTPClient)
	if err != nil {
		return report, err
	}
	report.Rotation.State = "applied"
	report.Action = "The successor release is verified over HTTPS. Confirm adoption by roots and representative clients before retiring old key copies; retain every numbered root and the recovery journal."
	return report, nil
}
