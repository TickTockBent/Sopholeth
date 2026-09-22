package omega

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"sopholeth/internal/trust/bootstrap"
)

// These records are disposable custody, not a production authority schema.
// The publisher binding permanently routes offline approvals to one journal.
// The online record contains no root or membership private keys.
type publisherBinding struct {
	Schema      int    `json:"schema"`
	Fingerprint string `json:"fingerprint"`
	Home        string `json:"home"`
}
type renewalCustody struct {
	Schema int               `json:"schema"`
	Mode   string            `json:"mode"`
	Bundle bootstrap.Bundle  `json:"bundle"`
	Keys   map[string]string `json:"private_keys"`
}
type ProvisionRenewalOptions struct {
	Home, Network, RenewalHome string
	Disposable                 bool
}

func readRenewal(home *store, network string) (renewalCustody, error) {
	var c renewalCustody
	data, err := home.read(network + ".renewal.json")
	if err != nil {
		return c, err
	}
	if err := decodeRecord(data, &c); err != nil {
		return c, err
	}
	if c.Schema != 1 || c.Mode != "disposable" || c.Bundle.Network != network || len(c.Keys) != 2 {
		return c, errors.New("omega: invalid disposable renewal custody")
	}
	if _, err := bootstrap.ParseBundle(record(c.Bundle)); err != nil {
		return c, err
	}
	return c, verifyOnlineKeys(c.Keys, c.Bundle.Root)
}

func verifyOnlineKeys(keys map[string]string, raw []byte) error {
	root, err := metadata.Root().FromBytes(raw)
	if err != nil {
		return err
	}
	for _, role := range []string{"snapshot", "timestamp"} {
		private, err := decodeKey(keys[role])
		if err != nil {
			return fmt.Errorf("omega: invalid renewal %s key", role)
		}
		key, err := metadata.KeyFromPublicKey(private.Public())
		if err != nil {
			return err
		}
		id, err := key.ID()
		if err != nil {
			return err
		}
		assignment := root.Signed.Roles[role]
		if assignment == nil || assignment.Threshold != 1 || len(assignment.KeyIDs) != 1 || assignment.KeyIDs[0] != id {
			return errors.New("omega: renewal key does not match its authorized role")
		}
	}
	return nil
}

func readPublisherBinding(home *store, bundle bootstrap.Bundle) (publisherBinding, error) {
	var binding publisherBinding
	data, err := home.read(bundle.Network + ".publisher.json")
	if err != nil {
		return binding, err
	}
	if err := decodeRecord(data, &binding); err != nil {
		return binding, err
	}
	if binding.Schema != 1 || binding.Fingerprint != bundle.Fingerprint() || !filepath.IsAbs(binding.Home) || filepath.Clean(binding.Home) != binding.Home {
		return binding, errors.New("omega: invalid operational home binding")
	}
	return binding, nil
}

// A pending handoff also blocks use of the old journal. Only provisioning may
// complete it, including a process killed before the binding's final link.
func operationalHome(ctx context.Context, home *store, bundle bootstrap.Bundle) (*store, func(), error) {
	binding, err := readPublisherBinding(home, bundle)
	if errors.Is(err, os.ErrNotExist) {
		if _, e := home.root.Lstat(bundle.Network + ".publisher.json.pending"); !errors.Is(e, os.ErrNotExist) {
			return nil, func() {}, errors.New("omega: renewal provisioning is pending; rerun provision-renewal")
		}
		return home, func() {}, nil
	}
	if err != nil {
		return nil, func() {}, err
	}
	if containsPath(home.root.Name(), binding.Home) || containsPath(binding.Home, home.root.Name()) {
		return nil, func() {}, errors.New("omega: authority and operational homes must be disjoint")
	}
	online, err := openHome(ctx, binding.Home, bundle.Network, false)
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { online.close() }
	c, err := readRenewal(online, bundle.Network)
	if err == nil && !bytes.Equal(record(c.Bundle), record(bundle)) {
		err = errors.New("omega: operational home belongs to a different authority")
	}
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("omega: incomplete or damaged renewal custody; rerun provision-renewal or restore it: %w", err)
	}
	online.hook = home.hook
	return online, cleanup, nil
}

func ProvisionRenewal(ctx context.Context, opts ProvisionRenewalOptions) (Report, error) {
	return provisionRenewal(ctx, opts, nil)
}

func provisionRenewal(ctx context.Context, opts ProvisionRenewalOptions, hook func(string) error) (report Report, resultErr error) {
	if !opts.Disposable || !networkID.MatchString(opts.Network) || opts.RenewalHome == "" {
		return report, errors.New("omega: provision-renewal requires a network, renewal home, and --disposable")
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
	report, err = current.inspect(time.Now().UTC())
	if err != nil {
		return report, err
	}
	defer func() {
		if resultErr != nil {
			report.Problem = resultErr.Error()
			report.Action = "Preserve both homes. Resolve the failure and rerun the same provision-renewal command."
		}
	}()
	_, bundle, err := current.verify()
	if err != nil {
		return report, err
	}
	if bundle.Network != opts.Network {
		return report, errors.New("omega: authority network mismatch")
	}
	path, err := canonicalPath(opts.RenewalHome)
	if err != nil {
		return report, err
	}
	if containsPath(home.root.Name(), path) || containsPath(path, home.root.Name()) {
		return report, errors.New("omega: authority and renewal homes must be disjoint directory trees")
	}
	binding := publisherBinding{Schema: 1, Fingerprint: bundle.Fingerprint(), Home: path}
	bindingName := opts.Network + ".publisher.json"
	// Never redirect a completed or fully written handoff to another home.
	for _, name := range []string{bindingName, bindingName + ".pending"} {
		data, err := home.read(name)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return report, err
		}
		if err == nil && (name == bindingName || json.Valid(data)) && !bytes.Equal(data, record(binding)) {
			return report, errors.New("omega: authority already has a different operational home binding")
		}
	}
	online, err := openHome(ctx, path, opts.Network, true)
	if err != nil {
		return report, err
	}
	defer online.close()
	online.hook = hook
	if _, err := online.root.Lstat(opts.Network); !errors.Is(err, os.ErrNotExist) {
		return report, errors.New("omega: renewal home must not contain the offline authority")
	}
	var a authority
	data, err := current.read("authority.json")
	if err != nil {
		return report, err
	}
	if err := decodeRecord(data, &a); err != nil {
		return report, err
	}
	planned := renewalCustody{Schema: 1, Mode: "disposable", Bundle: bundle, Keys: map[string]string{"snapshot": a.Keys["snapshot"], "timestamp": a.Keys["timestamp"]}}
	if data, err := online.read(opts.Network + ".renewal.json.pending"); err == nil {
		if json.Valid(data) && !bytes.Equal(data, record(planned)) {
			return report, errors.New("omega: fully written pending renewal custody differs; preserve it for recovery")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	if existing, err := readRenewal(online, opts.Network); err == nil {
		if !bytes.Equal(record(existing), record(planned)) {
			return report, errors.New("omega: renewal home already has different custody")
		}
		// The commit record is written only after the source binding and journal
		// are durable. Never copy an old source journal over a running publisher.
		actual, err := readPublisherBinding(home, bundle)
		if err != nil || actual != binding {
			return report, errors.New("omega: missing or mismatched handoff binding; restore it")
		}
		if err := online.syncDir(); err != nil {
			return report, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return report, err
	} else {
		// Fence the source before copying anything. A crash can delay publishing,
		// but cannot leave two usable journals for this authority home.
		if err := home.install(bindingName, record(binding)); err != nil {
			return report, err
		}
		if err := home.syncDir(); err != nil {
			return report, err
		}
		if err := home.phase("renewal:bound"); err != nil {
			return report, err
		}
		if err := copyPublication(ctx, home, online, bundle); err != nil {
			return report, err
		}
		if err := online.phase("renewal:journal-durable"); err != nil {
			return report, err
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := online.install(opts.Network+".renewal.json", record(planned)); err != nil {
			return report, err
		}
		if err := online.syncDir(); err != nil {
			return report, err
		}
	}
	if _, err := readRenewal(online, opts.Network); err != nil {
		return report, err
	}
	if err := cleanPendingTwins(home, ".", home.read); err != nil {
		return report, err
	}
	if err := cleanPendingTwins(online, ".", online.read); err != nil {
		return report, err
	}
	report.OperationalHome = path
	report.Action = "Renewal custody is ready. Keep the authority home offline between membership approvals; schedule soph omega publish --renew using the operational home."
	return report, nil
}

var journalEntry = regexp.MustCompile(`^[1-9][0-9]*\.(release|verification|renewal-intent)\.json(\.pending)?$`)

func copyPublication(ctx context.Context, source, dest *store, bundle bootstrap.Bundle) error {
	state, binding, err := openPublication(source, bundle, nil, false)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := dest.root.Lstat(bundle.Network + ".publication"); !errors.Is(err, os.ErrNotExist) {
			return errors.New("omega: unexpected destination journal during initial provisioning")
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer state.close()
	history, err := loadReleases(state, bundle)
	if err != nil {
		return err
	}
	for _, r := range history {
		if _, err := readVerification(state, r); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	repo, err := openRepository(ctx, binding.Directory, dest.root.Name())
	if err != nil {
		return err
	}
	defer repo.close()
	if err := checkRecordedDestination(repo, history); err != nil {
		return err
	}
	name := bundle.Network + ".publication"
	if err := dest.root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := dest.syncDir(); err != nil {
		return err
	}
	out, err := dest.subdir(name)
	if err != nil {
		return err
	}
	defer out.close()
	names, err := directoryNames(state)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name != "binding.json" && name != "binding.json.pending" && !journalEntry.MatchString(name) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := state.read(name)
		if err != nil {
			return err
		}
		if err := out.install(name, data); err != nil {
			return err
		}
	}
	copied, err := loadReleases(out, bundle)
	if err != nil {
		return err
	}
	if !bytes.Equal(record(history), record(copied)) {
		return errors.New("omega: handoff journal differs from source history")
	}
	if err := cleanPendingTwins(out, ".", out.read); err != nil {
		return err
	}
	return out.syncDir()
}

// An operational home is not an unused authority slot, even if its key record
// was lost. Status must ask for recovery, and init must not create replacement
// authority keys alongside existing publication state.
func rejectOrphanedOperationalState(home *store, network string) error {
	for _, suffix := range []string{".renewal.json", ".renewal.json.pending", ".publisher.json", ".publisher.json.pending", ".publication"} {
		if _, err := home.root.Lstat(network + suffix); err == nil {
			return errors.New("omega: existing operational state requires recovery, not authority initialization; restore the missing custody material")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
