package omega

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"sopholeth/internal/trust/bootstrap"
)

const allocationName = "init-keys.json"

// Only staging contains this aggregate of individually encrypted allocations.
// It fixes all keys before public output and is removed before promotion.
type encryptedAllocation struct {
	Schema      int               `json:"schema"`
	Type        string            `json:"type"`
	Mode        string            `json:"mode"`
	Transaction string            `json:"transaction"`
	Network     string            `json:"network"`
	Repository  string            `json:"repository"`
	Created     time.Time         `json:"created"`
	Expires     time.Time         `json:"root_expires"`
	Keys        map[string][]byte `json:"encrypted_keys"`
}

type publicAuthority struct {
	Schema      int               `json:"schema"`
	Mode        string            `json:"mode"`
	Custody     string            `json:"custody"`
	Transaction string            `json:"transaction"`
	Network     string            `json:"network"`
	Repository  string            `json:"repository"`
	Created     time.Time         `json:"created"`
	Expires     time.Time         `json:"root_expires"`
	Fingerprint string            `json:"fingerprint"`
	Keys        map[string]string `json:"public_keys"`
	KeyDigests  map[string]string `json:"key_file_sha256"`
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func validatePublicIdentity(network, repository, transaction string, created, expires time.Time) error {
	canonical, err := bootstrap.ValidateLocation(network, repository)
	if err != nil || canonical != repository || !networkID.MatchString(network) || !validDigest(transaction) || created.IsZero() || !expires.Equal(created.Add(rootLifetime)) {
		return errors.New("omega: invalid encrypted authority identity")
	}
	return nil
}

func (p publicAuthority) binding(name string) keyBinding {
	return keyBinding{Transaction: p.Transaction, Network: p.Network, Repository: p.Repository, Name: name, Generation: 1}
}

func (p publicAuthority) validate() error {
	if err := validatePublicIdentity(p.Network, p.Repository, p.Transaction, p.Created, p.Expires); err != nil {
		return err
	}
	if p.Schema != 2 || p.Mode != "disposable" || p.Custody != encryptedCustody || !validDigest(p.Fingerprint) || len(p.Keys) != len(keyNames) || len(p.KeyDigests) != len(keyNames) {
		return errors.New("omega: invalid public authority record")
	}
	seen := map[string]bool{}
	for _, name := range keyNames {
		key, err := decodePublicKey(p.Keys[name])
		if err != nil || seen[string(key)] || !validDigest(p.KeyDigests[name]) {
			return errors.New("omega: public authority needs six distinct keys and matching encrypted-file digests")
		}
		seen[string(key)] = true
	}
	return nil
}

func decodePublicKey(encoded string) (ed25519.PublicKey, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(key) != encoded {
		return nil, errors.New("omega: invalid public key encoding")
	}
	return ed25519.PublicKey(key), nil
}

func (p encryptedAllocation) validate() error {
	if err := validatePublicIdentity(p.Network, p.Repository, p.Transaction, p.Created, p.Expires); err != nil {
		return err
	}
	if p.Schema != 1 || p.Type != "encrypted-authority-allocation" || p.Mode != "disposable" || len(p.Keys) != len(keyNames) {
		return errors.New("omega: invalid encrypted initialization allocation")
	}
	for _, name := range keyNames {
		if len(p.Keys[name]) == 0 || len(p.Keys[name]) > maxEncryptedKey {
			return errors.New("omega: invalid encrypted allocation size")
		}
	}
	return nil
}

func authoritySchema(data []byte) int {
	var header struct {
		Schema int `json:"schema"`
	}
	_ = json.Unmarshal(data, &header)
	return header.Schema
}

func (s *store) prepareEncrypted(ctx context.Context, opts InitOptions, now time.Time, codec keyCodec) error {
	data, err := s.read(allocationName)
	committed := err == nil
	if errors.Is(err, os.ErrNotExist) {
		data, err = s.read(allocationName + ".pending")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		// The allocation is retired only after complete public material and all
		// separate encrypted files are durable. Resume that completed stage
		// without asking for passwords or re-creating private material.
		if _, doneErr := s.read("complete.json"); doneErr == nil {
			report, inspectErr := s.inspect(now)
			if inspectErr != nil {
				return inspectErr
			}
			if report.Custody != encryptedCustody || report.Network != opts.Network || report.Repository != opts.Repository {
				return errors.New("omega: completed staging has different custody or configuration")
			}
			return requireKeyFiles(report)
		}
	}
	var allocation encryptedAllocation
	existing := err == nil && json.Valid(data)
	if existing {
		if err := decodeRecord(data, &allocation); err != nil {
			return errors.New("omega: invalid encrypted allocation; preserve staging and restore its backup")
		}
		if err := allocation.validate(); err != nil {
			return err
		}
		if allocation.Network != opts.Network || allocation.Repository != opts.Repository {
			return errors.New("omega: encrypted initialization already began with different configuration")
		}
	} else {
		if committed {
			return errors.New("omega: committed encrypted allocation is corrupt; restore it, never regenerate keys")
		}
		// A torn first write is replaceable only before any derived key or
		// public output exists. Complete candidates, including wrong-password
		// ones, are never discarded as an interrupted first write.
		for _, name := range encryptedAuthorityFiles() {
			if name == allocationName {
				continue
			}
			for _, candidate := range []string{name, name + ".pending"} {
				if _, statErr := s.root.Lstat(candidate); !errors.Is(statErr, os.ErrNotExist) {
					return errors.New("omega: encrypted allocation is missing or torn after output; restore staging")
				}
			}
		}
	}
	if existing && !now.Before(allocation.Expires) {
		return errors.New("omega: staged encrypted authority expired; preserve its keys for explicit recovery")
	}
	if opts.Passphrase == nil {
		return errors.New("omega: encrypted initialization needs an interactive passphrase")
	}
	password, err := opts.Passphrase(ctx, !existing)
	if err != nil {
		return err
	}
	defer clear(password)
	if len(password) == 0 {
		return errors.New("omega: key passphrase cannot be empty")
	}
	if !existing {
		var transaction [32]byte
		if _, err := rand.Read(transaction[:]); err != nil {
			return err
		}
		allocation = encryptedAllocation{Schema: 1, Type: "encrypted-authority-allocation", Mode: "disposable", Transaction: hex.EncodeToString(transaction[:]),
			Network: opts.Network, Repository: opts.Repository, Created: now, Expires: now.Add(rootLifetime), Keys: map[string][]byte{}}
		for _, name := range keyNames {
			binding := keyBinding{Transaction: allocation.Transaction, Network: allocation.Network, Repository: allocation.Repository, Name: name, Generation: 1}
			allocation.Keys[name], err = codec.create(ctx, binding, password)
			if err != nil {
				return err
			}
		}
		data = record(allocation)
	}
	if err := s.install(allocationName, data); err != nil {
		return err
	}
	if err := s.syncDir(); err != nil {
		return err
	}
	p := publicAuthority{Schema: 2, Mode: "disposable", Custody: encryptedCustody, Transaction: allocation.Transaction, Network: allocation.Network,
		Repository: allocation.Repository, Created: allocation.Created, Expires: allocation.Expires, Keys: map[string]string{}, KeyDigests: map[string]string{}}
	keys := map[string]ed25519.PrivateKey{}
	defer func() {
		for _, key := range keys {
			clear(key)
		}
	}()
	for _, name := range keyNames {
		key, err := codec.unlock(ctx, allocation.Keys[name], p.binding(name), password)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		keys[name] = key
		p.Keys[name] = base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
		p.KeyDigests[name] = digest(allocation.Keys[name])
		if err := s.install(encryptedKeyName(name), allocation.Keys[name]); err != nil {
			return err
		}
	}
	outputs, err := buildPublicOutputs(p.Network, p.Repository, p.Expires, p.Keys, func(name string) (signature.Signer, error) { return signature.LoadSigner(keys[name], crypto.Hash(0)) })
	if err != nil {
		return err
	}
	bundle, err := bootstrap.ParseBundle(outputs["bundle.json"])
	if err != nil {
		return err
	}
	p.Fingerprint = bundle.Fingerprint()
	if err := p.validate(); err != nil {
		return err
	}
	outputs["authority.json"] = record(p)
	for _, name := range []string{"authority.json", "1.root.json", "bundle.json"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.install(name, outputs[name]); err != nil {
			return err
		}
	}
	return nil
}

func encryptedAuthorityFiles() []string {
	names := append([]string(nil), authorityFiles...)
	names = append(names, allocationName)
	for _, name := range keyNames {
		names = append(names, encryptedKeyName(name))
	}
	return names
}

func (s *store) readPublicAuthority() (publicAuthority, error) {
	var p publicAuthority
	data, err := s.read("authority.json")
	if err != nil {
		return p, err
	}
	if err := decodeRecord(data, &p); err != nil {
		return p, errors.New("omega: invalid public authority record")
	}
	return p, p.validate()
}

func (s *store) verifyPublicAuthority(data []byte) (completion, bootstrap.Bundle, error) {
	var p publicAuthority
	if err := decodeRecord(data, &p); err != nil {
		return completion{}, bootstrap.Bundle{}, err
	}
	if err := p.validate(); err != nil {
		return completion{}, bootstrap.Bundle{}, err
	}
	raw, err := s.read("1.root.json")
	if err != nil {
		return completion{}, bootstrap.Bundle{}, err
	}
	encoded, err := s.read("bundle.json")
	if err != nil {
		return completion{}, bootstrap.Bundle{}, err
	}
	bundle, err := bootstrap.ParseBundle(encoded)
	if err != nil {
		return completion{}, bundle, err
	}
	if bundle.Fingerprint() != p.Fingerprint {
		return completion{}, bundle, errors.New("omega: initial fingerprint does not match public authority")
	}
	if err := verifyPublicRoot(p.Network, p.Repository, p.Expires, p.Keys, bundle, raw); err != nil {
		return completion{}, bundle, err
	}
	return completion{Schema: 2, Fingerprint: bundle.Fingerprint(), Files: map[string]string{
		"authority.json": digest(data), "1.root.json": digest(raw), "bundle.json": digest(encoded),
	}}, bundle, nil
}

func (s *store) encryptedKeyStatus(p publicAuthority) map[string]string {
	status := map[string]string{}
	for _, name := range keyNames {
		data, err := s.read(encryptedKeyName(name))
		switch {
		case errors.Is(err, os.ErrNotExist):
			status[name] = "missing"
		case err != nil || len(data) > maxEncryptedKey || digest(data) != p.KeyDigests[name]:
			status[name] = "damaged"
		default:
			status[name] = "locked"
		}
	}
	return status
}

func requireKeyFiles(report Report) error {
	for _, name := range keyNames {
		if state := report.KeyFiles[name]; state != "locked" && state != "verified" {
			return fmt.Errorf("omega: %s encrypted key is %s; restore its matching backup", name, state)
		}
	}
	return nil
}

func encryptedLifecyclePending(report Report) (Report, error) {
	err := errors.New("omega: encrypted custody currently supports init and status only; publication and rotation are the next slice")
	report.Problem = err.Error()
	report.Action = "Preserve this disposable encrypted authority for inspection/recovery tests; use a separate plaintext disposable authority for lifecycle rehearsal."
	return report, err
}

// CheckKeys verifies a restored encrypted home without signing or changing its
// authority. Ordinary Status never asks for passwords or requires key files.
func CheckKeys(ctx context.Context, homePath, network string, passphrase func(context.Context, bool) ([]byte, error)) (Report, error) {
	return checkKeys(ctx, homePath, network, passphrase, defaultKeyCodec())
}

func checkKeys(ctx context.Context, homePath, network string, passphrase func(context.Context, bool) ([]byte, error), codec keyCodec) (report Report, resultErr error) {
	if !networkID.MatchString(network) {
		return failedReport(errors.New("omega: invalid network identity")), errors.New("omega: invalid network identity")
	}
	home, err := openHome(ctx, homePath, network, false)
	if err != nil {
		return failedReport(err), err
	}
	defer home.close()
	current, err := home.subdir(network)
	if err != nil {
		return failedReport(err), err
	}
	defer current.close()
	report, err = current.inspect(time.Now().UTC())
	if err != nil && !errors.Is(err, errRootExpired) {
		return report, err
	}
	defer func() {
		if resultErr != nil && !errors.Is(resultErr, errRootExpired) {
			report.Problem = resultErr.Error()
			report.Action = "Check the passphrase and restore matching encrypted key files; preserve the existing public authority."
		}
	}()
	if report.Network != network || report.Custody != encryptedCustody {
		return report, errors.New("omega: key verification requires this network's encrypted authority")
	}
	if err := requireKeyFiles(report); err != nil {
		return report, err
	}
	if passphrase == nil {
		return report, errors.New("omega: encrypted key verification needs an interactive passphrase")
	}
	password, err := passphrase(ctx, false)
	if err != nil {
		return report, err
	}
	defer clear(password)
	p, err := current.readPublicAuthority()
	if err != nil {
		return report, err
	}
	for _, name := range keyNames {
		data, err := current.read(encryptedKeyName(name))
		if err != nil {
			return report, err
		}
		key, err := codec.unlock(ctx, data, p.binding(name), password)
		if err != nil {
			return report, fmt.Errorf("%s: %w", name, err)
		}
		matches := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)) == p.Keys[name]
		clear(key)
		if !matches {
			return report, errors.New("omega: restored private key does not match public authority")
		}
		report.KeyFiles[name] = "verified"
	}
	report.Action = "All six encrypted keys match the public authority. Keep this verified backup separate from the working copy and retain its unlock information."
	if report.State == "expired" {
		report.Action = "Recovered keys match, but the public root is expired. Preserve this authority for explicit recovery; never reinitialize it."
		return report, errRootExpired
	}
	return report, nil
}
