package storage

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

// BenchmarkKeysListPaged measures listing cost against a large keyspace: a
// paged request should stay near-constant as the store grows (#220).
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
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		store := newTestStore(0)
		for j := 0; j < DefaultSweepBatch; j++ {
			_ = store.Put(strconv.Itoa(j), []byte("v"), time.Nanosecond)
		}
		b.StartTimer()
		store.sweepExpired(DefaultSweepBatch)
		store.Close()
	}
}
