package omega

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"sopholeth/internal/trust/bootstrap"
)

type PublishOptions struct {
	Home       string
	Network    string
	Directory  string
	Manifest   []byte
	Version    int64
	Disposable bool
	HTTPClient *http.Client
}

type PublicationReport struct {
	Version    int64                `json:"version"`
	Versions   bootstrap.Versions   `json:"versions"`
	Roots      []bootstrap.Root     `json:"roots"`
	Expires    map[string]time.Time `json:"expires"`
	CheckedAt  time.Time            `json:"checked_at,omitzero"`
	VerifiedAt time.Time            `json:"last_verified_at,omitzero"`
	LastError  string               `json:"last_error,omitempty"`
}

type verificationRecord struct {
	Schema        int       `json:"schema"`
	Version       int64     `json:"version"`
	ReleaseSHA256 string    `json:"release_sha256"`
	CheckedAt     time.Time `json:"checked_at"`
	VerifiedAt    time.Time `json:"last_verified_at,omitzero"`
	Error         string    `json:"error,omitempty"`
}

func Publish(ctx context.Context, opts PublishOptions) (Report, error) {
	return publish(ctx, opts, time.Now().UTC().Truncate(time.Second), nil)
}

func publish(ctx context.Context, opts PublishOptions, now time.Time, hook func(string) error) (report Report, resultErr error) {
	if !opts.Disposable || !networkID.MatchString(opts.Network) || opts.Version < 1 || opts.Version > maxReleases {
		return report, errors.New("omega: publish requires --disposable, a valid network, and a version between 1 and 10000")
	}
	manifest, err := approvedManifest(opts.Manifest, opts.Network)
	if err != nil {
		return report, err
	}
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
	report, err = current.inspect(now)
	if err != nil {
		return report, err
	}
	if report.Network != opts.Network {
		return failedReport(errors.New("omega: authority network mismatch")), errors.New("omega: authority network mismatch")
	}
	defer func() {
		if resultErr != nil {
			report.Publication = "failed"
			report.Problem = resultErr.Error()
			report.Action = "Preserve publication history. Resolve the failure and retry the same version; use the next version if the recorded release expired."
		}
	}()
	_, bundle, err := current.verify()
	if err != nil {
		return report, err
	}
	repo, err := openRepository(ctx, opts.Directory, home.root.Name())
	if err != nil {
		return report, err
	}
	defer repo.close()
	repo.hook = hook
	state, _, err := openPublication(home, bundle, repo, true)
	if err != nil {
		return report, err
	}
	defer state.close()
	history, err := loadReleases(state, bundle)
	if err != nil {
		return report, err
	}
	if err := checkRecordedDestination(repo, history); err != nil {
		return report, err
	}
	latest := int64(len(history))
	if opts.Version < latest || opts.Version > latest+1 {
		return report, fmt.Errorf("omega: version must be the latest (%d) or the next (%d); published versions cannot be rolled back or skipped", latest, latest+1)
	}
	var r release
	if opts.Version == latest {
		r = history[len(history)-1]
		if !bytes.Equal(r.Manifest, manifest) {
			return report, errors.New("omega: version already has a different approved manifest; choose the next version")
		}
	} else {
		name := releaseName(opts.Version)
		pending, pendingErr := state.read(name + ".pending")
		if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
			return report, pendingErr
		}
		previous := ""
		if len(history) > 0 {
			previous = digest(record(history[len(history)-1]))
		}
		if pendingErr == nil && json.Valid(pending) {
			if err := decodeRecord(pending, &r); err != nil {
				return report, errors.New("omega: invalid pending release; preserve the journal for recovery")
			}
			if r.Version != opts.Version || r.Previous != previous || !bytes.Equal(r.Manifest, manifest) {
				return report, errors.New("omega: pending version has different approval; retry its original manifest")
			}
			if _, _, err := r.validate(bundle); err != nil {
				return report, err
			}
		} else {
			data, err := current.read("authority.json")
			if err != nil {
				return report, err
			}
			var a authority
			if err := decodeRecord(data, &a); err != nil {
				return report, err
			}
			r, err = prepareRelease(a, bundle, opts.Version, previous, manifest, now)
			if err != nil {
				return report, err
			}
		}
		// Reserve the exact signed bytes before any write to the public directory.
		if err := state.install(name, record(r)); err != nil {
			return report, err
		}
		history = append(history, r)
	}
	if err := state.syncDir(); err != nil {
		return report, err
	}
	m, expires, err := r.validate(bundle)
	if err != nil {
		return report, err
	}
	report.Release = &PublicationReport{Version: r.Version, Versions: r.versions(), Roots: m.Roots, Expires: expires}
	if err := state.phase("release:durable"); err != nil {
		return report, err
	}
	// Save failures as well as successes, while retaining historical successful
	// verification time. A receipt cannot authorize a write or reset ordering.
	receiptAttempted := false
	defer func() {
		if resultErr != nil && !receiptAttempted {
			if saveErr := saveVerification(state, r, resultErr, time.Now().UTC()); saveErr != nil {
				resultErr = errors.Join(resultErr, saveErr)
			}
		}
	}()
	if !now.Before(expires["timestamp"]) {
		return report, errors.New("omega: recorded release has expired; publish the next version with renewed approval")
	}
	if err := checkTimestamp(repo, r, history); err != nil {
		return report, err
	}
	// Validate TLS configuration and initialize a fresh real client before
	// touching public objects. The private journal supplies expected ordering.
	verifier, cleanup, err := newPublicationVerifier(ctx, state, bundle, opts.HTTPClient)
	if err != nil {
		return report, err
	}
	defer cleanup()
	if err := repo.targetsDir(); err != nil {
		return report, err
	}
	for _, obj := range r.objects(bundle) {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := repo.writeObject(obj, false); err != nil {
			return report, err
		}
	}
	if err := repo.phase("public:objects-ready"); err != nil {
		return report, err
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if !time.Now().Before(expires["timestamp"]) {
		return report, errors.New("omega: release expired before timestamp publication; approve the next version")
	}
	// Recheck under the destination lock before replacing the sole mutable file.
	if err := checkTimestamp(repo, r, history); err != nil {
		return report, err
	}
	if err := repo.writeObject(publicObject{"timestamp.json", r.Timestamp}, true); err != nil {
		return report, err
	}
	report.Publication = "unverified"
	if err := verifier.verify(ctx, bundle, r); err != nil {
		return report, err
	}
	if err := state.phase("publication:verified"); err != nil {
		return report, err
	}
	checked := time.Now().UTC()
	receiptAttempted = true
	if err := saveVerification(state, r, nil, checked); err != nil {
		return report, err
	}
	report.Publication = "verified"
	report.Release.CheckedAt = checked
	report.Release.VerifiedAt = checked
	report.Action = "The expected release was verified over HTTPS. Retain publication history; unattended renewal and rotation remain pending."
	return report, nil
}

func checkTimestamp(repo *repositoryStore, r release, history []release) error {
	data, err := repo.read("timestamp.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if bytes.Equal(data, r.Timestamp) {
		return nil
	}
	// Only a known older release may be replaced. A newer, unknown, or changed
	// timestamp means the journal/destination disagree; never guess a repair.
	for _, old := range history {
		if old.Version < r.Version && bytes.Equal(data, old.Timestamp) {
			return nil
		}
	}
	return errors.New("omega: repository timestamp differs from recorded history; refusing rollback or replacement of unknown publication")
}

// Check before allocating or signing a new version. A restored/truncated
// journal must not reuse a number already visible in the destination.
func checkRecordedDestination(repo *repositoryStore, history []release) error {
	data, err := repo.read("timestamp.json")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		known := false
		for _, r := range history {
			if bytes.Equal(data, r.Timestamp) {
				known = true
				break
			}
		}
		if !known {
			return errors.New("omega: destination timestamp is absent from publication history; restore the journal")
		}
	}
	names, err := directoryNames(repo.store)
	if err != nil {
		return err
	}
	for _, name := range names {
		for _, suffix := range []string{".targets.json", ".snapshot.json"} {
			if strings.HasSuffix(name, suffix) {
				version, err := strconv.ParseInt(strings.TrimSuffix(name, suffix), 10, 64)
				if err != nil || version < 1 || version > int64(len(history)) {
					return errors.New("omega: destination contains metadata absent from publication history; restore the journal")
				}
			}
		}
	}
	return nil
}

func verificationName(r release) string { return fmt.Sprintf("%d.verification.json", r.Version) }
func readVerification(state *store, r release) (verificationRecord, error) {
	data, err := state.read(verificationName(r))
	if err != nil {
		return verificationRecord{}, err
	}
	var v verificationRecord
	if err := decodeRecord(data, &v); err != nil || v.Schema != 1 || v.Version != r.Version || v.ReleaseSHA256 != digest(record(r)) || v.CheckedAt.IsZero() || v.VerifiedAt.After(v.CheckedAt) || (v.Error == "" && v.VerifiedAt.IsZero()) {
		return v, errors.New("omega: invalid publication verification receipt")
	}
	return v, nil
}
func saveVerification(state *store, r release, result error, now time.Time) error {
	previous, err := readVerification(state, r)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if now.Before(previous.CheckedAt) {
		return errors.New("omega: clock moved backward since the last publication check")
	}
	v := verificationRecord{Schema: 1, Version: r.Version, ReleaseSHA256: digest(record(r)), CheckedAt: now, VerifiedAt: previous.VerifiedAt}
	if result == nil {
		v.VerifiedAt = now
	} else {
		v.Error = result.Error()
	}
	return state.replaceRecord(verificationName(r), record(v))
}

func inspectPublication(ctx context.Context, home, current *store, report Report, verify bool, hc *http.Client) (Report, error) {
	_, bundle, err := current.verify()
	if err != nil {
		return report, err
	}
	state, _, err := openPublication(home, bundle, nil, false)
	if errors.Is(err, os.ErrNotExist) {
		if !verify {
			return report, nil
		}
		err = errors.New("omega: no publication journal; publish an approved manifest first")
		report.Publication = "unpublished"
		report.Problem = err.Error()
		report.Action = "Use soph omega publish to approve and publish the first release."
		return report, err
	}
	if err != nil {
		return publicationFailure(report, err)
	}
	defer state.close()
	history, err := loadReleases(state, bundle)
	if err != nil {
		return publicationFailure(report, err)
	}
	if len(history) == 0 {
		report.Publication = "unpublished"
		report.Action = "Use soph omega publish to approve and publish the first release."
		if verify {
			return publicationFailure(report, errors.New("omega: no prepared release to verify"))
		}
		return report, nil
	}
	r := history[len(history)-1]
	m, expires, err := r.validate(bundle)
	if err != nil {
		return publicationFailure(report, err)
	}
	report.Release = &PublicationReport{Version: r.Version, Versions: r.versions(), Roots: m.Roots, Expires: expires}
	receipt, err := readVerification(state, r)
	if err == nil {
		report.Release.CheckedAt = receipt.CheckedAt
		report.Release.VerifiedAt = receipt.VerifiedAt
		report.Release.LastError = receipt.Error
	} else if !errors.Is(err, os.ErrNotExist) {
		return publicationFailure(report, err)
	}
	if !time.Now().Before(expires["timestamp"]) {
		return publicationFailure(report, errors.New("omega: latest recorded release has expired; publish the next version"))
	}
	if !verify {
		report.Publication = "not_checked"
		report.Action = "Use soph omega status --verify to check the latest prepared release over HTTPS."
		if receipt.Error != "" {
			return publicationFailure(report, errors.New("omega: last publication attempt failed: "+receipt.Error))
		}
		return report, nil
	}
	verifier, cleanup, err := newPublicationVerifier(ctx, state, bundle, hc)
	if err == nil {
		defer cleanup()
		err = verifier.verify(ctx, bundle, r)
	}
	checked := time.Now().UTC()
	saveErr := saveVerification(state, r, err, checked)
	if err = errors.Join(err, saveErr); err != nil {
		return publicationFailure(report, err)
	}
	report.Publication = "verified"
	report.Release.CheckedAt = checked
	report.Release.VerifiedAt = checked
	report.Release.LastError = ""
	report.Action = "The expected release was verified over HTTPS. Retain publication history and monitor expiration."
	return report, nil
}

func publicationFailure(report Report, err error) (Report, error) {
	report.Publication = "failed"
	report.Problem = err.Error()
	report.Action = "Preserve the journal and resolve the reported publication failure; retry the same version, or the next version for expired approval."
	return report, err
}
