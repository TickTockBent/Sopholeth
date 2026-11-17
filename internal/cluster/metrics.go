package cluster

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// relayMetrics tracks the receive side of gossip replication for Prometheus.
type relayMetrics struct {
	storeFailed prometheus.Counter
}

var (
	sharedRelayMetrics     *relayMetrics
	sharedRelayMetricsOnce sync.Once
)

func newRelayMetrics() *relayMetrics {
	sharedRelayMetricsOnce.Do(func() {
		sharedRelayMetrics = &relayMetrics{
			storeFailed: prometheus.NewCounter(prometheus.CounterOpts{
				Name: "gossip_relay_store_failed_total",
				Help: "Inbound gossip PUTs relayed onward but not stored locally (for example, store full); no ACK is sent for them",
			}),
		}
		prometheus.MustRegister(sharedRelayMetrics.storeFailed)
	})
	return sharedRelayMetrics
}

// countStoreFailure records a relayed PUT the local store refused. Safe on a
// nil receiver, which is how nodes run before Start enables metrics.
func (m *relayMetrics) countStoreFailure() {
	if m == nil {
		return
	}
	m.storeFailed.Inc()
}
