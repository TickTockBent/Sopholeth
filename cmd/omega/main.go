// Command omega signs public root lists with the operator's trust anchor.
// It runs offline, separate from the node. See
// docs/discovery.md and docs/omega-operations.md.
//
// Subcommands:
//
//	keygen  generate a new Ed25519 omega keypair
//	sign    sign a root list for publication as a DNS TXT record
//
// The omega private key must be stored on an air-gapped signing machine.
// Never transmit it over any network.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sopholeth/internal/trust"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "keygen":
		if err := runKeygen(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "keygen: %v\n", err)
			os.Exit(1)
		}
	case "sign":
		if err := runSign(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "sign: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, "omega — operator tool for signed root lists")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  omega keygen --out-private <path> --out-public <path>")
	fmt.Fprintln(w, "  omega sign --key <path> --version <id> --expires-in <duration> --nodes <host:http-port,...>")
}

func runKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	outPriv := fs.String("out-private", "", "path to write the Ed25519 private key (required)")
	outPub := fs.String("out-public", "", "path to write the Ed25519 public key (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outPriv == "" || *outPub == "" {
		fs.Usage()
		return fmt.Errorf("both --out-private and --out-public are required")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate keypair: %w", err)
	}

	pubB64 := base64.StdEncoding.EncodeToString(pub)
	privB64 := base64.StdEncoding.EncodeToString(priv)

	if err := atomicWriteNoClobber(*outPriv, []byte(privB64+"\n"), 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	if err := atomicWriteNoClobber(*outPub, []byte(pubB64+"\n"), 0o644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	fmt.Println("Generated Ed25519 keypair.")
	fmt.Printf("Public key: %s\n", pubB64)
	fmt.Printf("Private key written to %s (mode 0600)\n", *outPriv)
	fmt.Printf("Public key written to %s\n", *outPub)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Bake the public key into internal/trust/omega.go (OmegaPubkey).")
	fmt.Printf("  2. Store %s on your offline signing machine.\n", *outPriv)
	fmt.Println("  3. Never transmit the private key over any network.")
	return nil
}

func runSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	keyPath := fs.String("key", "", "path to Ed25519 private key file (required)")
	version := fs.String("version", trust.OmegaVersion, "omega version identifier")
	expiresIn := fs.Duration("expires-in", 0, "lifetime of the signed list (e.g. 24h)")
	nodes := fs.String("nodes", "", "comma-separated root node addresses host:http-port (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyPath == "" {
		return fmt.Errorf("--key is required")
	}
	if *expiresIn <= 0 {
		return fmt.Errorf("--expires-in must be positive (e.g. 24h)")
	}
	if strings.TrimSpace(*nodes) == "" {
		return fmt.Errorf("--nodes is required")
	}

	privKeyB64, err := os.ReadFile(*keyPath)
	if err != nil {
		return fmt.Errorf("read key: %w", err)
	}
	priv, err := decodePrivateKey(string(privKeyB64))
	if err != nil {
		return err
	}

	list := &trust.SignedList{
		Version: *version,
		Expires: time.Now().Add(*expiresIn).Unix(),
		Nodes:   splitCSV(*nodes),
	}
	list.Sign(priv)

	fmt.Println(list.Encode())
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "To publish:")
	fmt.Fprintf(os.Stderr, "  1. Paste the line above as the value of the %s TXT record.\n", trust.DefaultSignedListName)
	fmt.Fprintf(os.Stderr, "  2. Verify propagation with: dig TXT %s\n", trust.DefaultSignedListName)
	fmt.Fprintf(os.Stderr, "  3. This record expires at %s UTC.\n",
		time.Unix(list.Expires, 0).UTC().Format(time.RFC3339))
	return nil
}

func decodePrivateKey(raw string) (ed25519.PrivateKey, error) {
	raw = strings.TrimSpace(raw)
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key has wrong length: got %d want %d", len(decoded), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(decoded), nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// atomicWriteNoClobber writes data to path via tempfile+rename so a crash
// mid-write cannot leave a truncated key file on disk. Refuses to overwrite
// an existing file — rerunning keygen against the same path is almost
// certainly an operator mistake, and the consequence (silently clobbering
// a working key) is unrecoverable.
func atomicWriteNoClobber(path string, data []byte, mode os.FileMode) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite existing file %s", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
