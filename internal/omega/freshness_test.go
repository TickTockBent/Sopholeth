//go:build linux

package omega

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"sopholeth/internal/trust/bootstrap"
)

func TestFreshnessLifetimeAndRenewalBoundary(t *testing.T) {
	f := newPublishFixture(t)
	var a authority
	must(t, decodeRecord(file(t, filepath.Join(f.opts.Home, f.opts.Network, "authority.json")), &a))
	bundle, err := bootstrap.ParseBundle(file(t, filepath.Join(f.opts.Home, f.opts.Network, "bundle.json")))
	must(t, err)
	manifest, err := approvedManifest(f.opts.Manifest, f.opts.Network)
	must(t, err)
	created := time.Now().UTC().Truncate(time.Second)
	current, err := prepareRelease(a, bundle, 1, "", manifest, created)
	must(t, err)

	for _, tc := range []struct {
		name     string
		days     int
		interval time.Duration
		lifetime time.Duration
	}{
		{"daily", 7, 24 * time.Hour, 7 * 24 * time.Hour},
		{"legacy", 0, 6 * time.Hour, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := current
			if tc.days == 0 {
				// Reproduce an old signed journal record without adding the new
				// policy field or changing the snapshot/targets objects.
				r.TimestampDays = 0
				timestamp, err := metadata.Timestamp().FromBytes(r.Timestamp)
				must(t, err)
				timestamp.Signed.Expires = created.Add(tc.lifetime)
				timestamp.Signatures = nil
				r.Timestamp, err = signMetadata(timestamp, a.Keys["timestamp"])
				must(t, err)
				if bytes.Contains(record(r), []byte("timestamp_days")) {
					t.Fatal("legacy journal serialization changed")
				}
			}
			m, expires, err := r.validate(bundle)
			must(t, err)
			if !expires["timestamp"].Equal(created.Add(tc.lifetime)) || !expires["snapshot"].Equal(created.Add(7*24*time.Hour)) {
				t.Fatalf("unexpected freshness deadlines: %v", expires)
			}
			due := created.Add(tc.interval)
			before := publicationReport(r, m, expires, due.Add(-time.Second))
			at := publicationReport(r, m, expires, due)
			if before.RenewalDue || !at.RenewalDue || !at.RenewAfter.Equal(due) {
				t.Fatalf("incorrect renewal boundary: before=%+v at=%+v", before, at)
			}
			bad := r
			bad.TimestampDays = 2
			if _, _, err := bad.validate(bundle); err == nil {
				t.Fatal("accepted unknown timestamp policy")
			}
			if tc.days == 0 {
				bad.TimestampDays = 7
				if _, _, err := bad.validate(bundle); err == nil {
					t.Fatal("accepted lifetime different from reserved policy")
				}
			}
		})
	}
}

func TestLegacyPendingRenewalKeepsSignedBytes(t *testing.T) {
	_, _, opts := renewalFixture(t, 25*time.Hour)
	var custody renewalCustody
	must(t, decodeRecord(file(t, filepath.Join(opts.Home, opts.Network+".renewal.json")), &custody))
	var previous release
	must(t, decodeRecord(file(t, filepath.Join(onlineState(opts), releaseName(1))), &previous))
	created := time.Now().UTC().Truncate(time.Second)
	intent := renewalIntent{Schema: 1, Version: 2, Previous: digest(record(previous)), Created: created}
	legacy, err := prepareRenewal(custody.Keys, custody.Bundle, previous, created, 0)
	must(t, err)
	expected := record(legacy)
	must(t, os.WriteFile(filepath.Join(onlineState(opts), renewalIntentName(2)), record(intent), 0600))
	must(t, os.WriteFile(filepath.Join(onlineState(opts), releaseName(2)+".pending"), expected, 0600))

	_, err = Renew(context.Background(), opts)
	must(t, err)
	if !bytes.Equal(expected, file(t, filepath.Join(onlineState(opts), releaseName(2)))) {
		t.Fatal("upgrade changed an interrupted legacy release")
	}
	// Its next unreserved renewal adopts the new policy without replacing keys
	// or rewriting the old publication history.
	report, err := renew(context.Background(), opts, created.Add(7*time.Hour), nil)
	must(t, err)
	if report.Release.Version != 3 || !report.Release.Expires["timestamp"].Equal(created.Add(7*time.Hour+7*24*time.Hour)) {
		t.Fatalf("legacy publication did not adopt seven-day freshness: %+v", report)
	}
}
