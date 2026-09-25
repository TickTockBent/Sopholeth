package storage

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

// BenchmarkKeysListPaged measures listing cost against a large keyspace: a
// paged request should stay near-constant as the store grows (#220). Run
// with: go test ./internal/storage -bench BenchmarkKeysListPaged
func BenchmarkKeysListPaged(b *testing.B) {
	for _, size := range []int{100000, 1000000} {
		b.Run(fmt.Sprintf("keys=%d", size), func(b *testing.B) {
			store := newTestStore(0)
			defer store.Close()

			for i := 0; i < size; i++ {
				if err := store.Put(strconv.Itoa(i), []byte("v"), time.Hour); err != nil {
					b.Fatal(err)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				keys := store.ScanPage("", "", 100)
				if len(keys) != 100 {
					b.Fatalf("page = %d keys, want 100", len(keys))
				}
			}
		})
	}
}

// BenchmarkPutUnderLargeKeyspace checks that writes stay O(log n) as the
// ordered index grows.
func BenchmarkPutUnderLargeKeyspace(b *testing.B) {
	store := newTestStore(0)
	defer store.Close()

	const base = 500000
	for i := 0; i < base; i++ {
		if err := store.Put(strconv.Itoa(i), []byte("v"), time.Hour); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Put("bench-"+strconv.Itoa(i), []byte("v"), time.Hour); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSweepExpired verifies the incremental sweeper removes expired
// entries in batches without a full-map pass.
func BenchmarkSweepExpired(b *testing.B) {
	store := newTestStore(0)
	defer store.Close()

	const n = 100000
	for i := 0; i < n; i++ {
		if err := store.Put(strconv.Itoa(i), []byte("v"), time.Nanosecond); err != nil {
			b.Fatal(err)
		}
	}

	removed := 0
	b.ResetTimer()
	for removed < n {
		removed += store.sweepExpired(DefaultSweepBatch)
	}
}
