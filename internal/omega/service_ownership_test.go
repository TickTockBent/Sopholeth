//go:build linux

package omega

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// CI runs this single test as root, then renewal in a child with a different
// real UID. Ordinary tests cannot expose privileged creation of root-owned files.
func TestRenewalWithServiceOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if base := os.Getenv("SOPH_SERVICE_OWNERSHIP_CHILD"); base != "" {
		if os.Geteuid() == 0 {
			t.Fatal("renewal child must be unprivileged")
		}
		_, err := os.Stat(filepath.Join(base, "custody", "rehearsal", "authority.json"))
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("offline custody must be inaccessible: %v", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(file(t, filepath.Join(base, "server-ca.pem"))) {
			t.Fatal("invalid fixture CA")
		}
		hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
		opts := RenewOptions{Home: filepath.Join(base, "online"), Network: "rehearsal", HTTPClient: hc}
		for range 2 {
			r, err := Renew(ctx, opts)
			must(t, err)
			if r.Publication != "verified" || r.Release.Version != 2 {
				t.Fatalf("unexpected renewal: %+v", r)
			}
		}
		_, err = VerifyPublication(ctx, opts.Home, opts.Network, hc)
		must(t, err)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("run the compiled test as root to exercise the service UID transition")
	}
	const uid, gid = 65534, 65534
	f := newCustodyPublishFixture(t, time.Now().UTC().Add(-25*time.Hour), true)
	base := filepath.Dir(f.opts.Home)
	// Only fixture ancestors/public certificate are traversable. Custody stays
	// root-owned 0700; renewal must use its separate account and key files.
	must(t, os.Chmod(filepath.Dir(base), 0755))
	must(t, os.Chmod(base, 0755))
	online := filepath.Join(base, "online")
	must(t, os.Mkdir(online, 0700))
	must(t, os.Chown(online, uid, gid))
	_, err := ProvisionRenewal(ctx, ProvisionRenewalOptions{Home: f.opts.Home, Network: "rehearsal", RenewalHome: online, Passphrase: testKeyPassphrase, codec: fastKeyCodec})
	must(t, err)
	// Existing operator-owned directories are rejected, not silently adopted.
	must(t, os.Mkdir(f.opts.Directory, 0755))
	_, err = publish(ctx, f.opts, time.Now().UTC().Add(-25*time.Hour), nil)
	if err == nil || !strings.Contains(err.Error(), "operational home's owner") {
		t.Fatalf("accepted an existing repository with the wrong owner: %v", err)
	}
	info, err := os.Stat(f.opts.Directory)
	must(t, err)
	if info.Sys().(*syscall.Stat_t).Uid != 0 {
		t.Fatal("changed existing repository ownership")
	}
	must(t, os.Remove(f.opts.Directory))
	_, err = publish(ctx, f.opts, time.Now().UTC().Add(-25*time.Hour), nil)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(base, "server-ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}), 0644))
	must(t, os.Chmod(filepath.Join(base, "server-ca.pem"), 0644))
	// The test runner's original build directory may be private to root.
	source, err := os.Open(os.Args[0])
	must(t, err)
	defer source.Close()
	binary := filepath.Join(base, "service-test")
	dest, err := os.OpenFile(binary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	must(t, err)
	_, err = io.Copy(dest, source)
	must(t, errors.Join(err, dest.Close()))
	must(t, os.Chmod(binary, 0755))
	temporary := filepath.Join(base, "service-tmp")
	must(t, os.Mkdir(temporary, 0700))
	must(t, os.Chown(temporary, uid, gid))
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestRenewalWithServiceOwnership$", "-test.v")
	cmd.Env = append(os.Environ(), "SOPH_SERVICE_OWNERSHIP_CHILD="+base, "TMPDIR="+temporary)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unprivileged renewal failed: %v\n%s", err, output)
	}
	for _, directory := range []string{online, f.opts.Directory} {
		must(t, filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			stat := info.Sys().(*syscall.Stat_t)
			if stat.Uid != uid || stat.Gid != gid {
				t.Errorf("%s is owned by %d:%d, want %d:%d", path, stat.Uid, stat.Gid, uid, gid)
			}
			return nil
		}))
	}
}
