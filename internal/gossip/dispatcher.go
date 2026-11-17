package gossip

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"sopholeth/internal/logging"
)

// Outbound gossip delivery bounds (#212, #167). Every PUT broadcast, epidemic
// forward and ACK goes through a sendDispatcher instead of being sent inline,
// so a client write or an inbound gossip request never waits on a peer.
const (
	// DefaultSendQueueDepth is the number of messages that may wait for one
	// peer. A full queue drops the new message and counts the drop.
	DefaultSendQueueDepth = 128
	// DefaultMaxSendsPerPeer is the number of workers draining one peer's
	// queue, and so its sends in flight. One worker would cap a WAN peer at
	// about one message per round trip (0.2-0.5s to the testnet roots),
	// shared between replicas and ACKs.
	DefaultMaxSendsPerPeer = 4
	// DefaultMaxConcurrentSends caps outbound gossip sends in flight across
	// all peers. It is sized so that 64 unanswering peers, each holding
	// DefaultMaxSendsPerPeer slots, are needed to fill it.
	DefaultMaxConcurrentSends = 256
	// DefaultSendTimeout is the deadline for one send, matching the HTTP
	// transport's client timeout.
	DefaultSendTimeout = 5 * time.Second
)

// sendMetrics tracks outbound gossip delivery for Prometheus.
type sendMetrics struct {
	queueDropped  prometheus.Counter
	sendDuration  *prometheus.HistogramVec
	sendsInFlight prometheus.Gauge
	sendsQueued   prometheus.Gauge
}

var (
	sharedSendMetrics     *sendMetrics
	sharedSendMetricsOnce sync.Once
)

func newSendMetrics() *sendMetrics {
	sharedSendMetricsOnce.Do(func() {
		sharedSendMetrics = &sendMetrics{
			queueDropped: prometheus.NewCounter(prometheus.CounterOpts{
				Name: "gossip_send_queue_dropped_total",
				Help: "Outbound gossip messages dropped because the peer's send queue was full",
			}),
			sendDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "gossip_send_duration_seconds",
				Help:    "Duration of outbound gossip sends, by outcome (ok or error)",
				Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			}, []string{"outcome"}),
			sendsInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
				Name: "gossip_sends_in_flight",
				Help: "Outbound gossip sends currently in flight",
			}),
			sendsQueued: prometheus.NewGauge(prometheus.GaugeOpts{
				Name: "gossip_sends_queued",
				Help: "Outbound gossip messages waiting in peer send queues",
			}),
		}
		prometheus.MustRegister(
			sharedSendMetrics.queueDropped,
			sharedSendMetrics.sendDuration,
			sharedSendMetrics.sendsInFlight,
			sharedSendMetrics.sendsQueued,
		)
	})
	return sharedSendMetrics
}

type sendJob struct {
	peer *Node
	msg  *Message
}

// peerSendQueue is one peer's FIFO and the context its worker runs under.
// Cancelling the context stops its workers and aborts their in-flight sends.
type peerSendQueue struct {
	jobs   chan sendJob
	ctx    context.Context
	cancel context.CancelFunc
}

// sendDispatcher delivers gossip messages to peers asynchronously with
// explicit bounds:
//
//   - one bounded FIFO per peer, drained by a fixed number of workers, so each
//     peer has at most that many sends in flight and a slow peer only delays
//     its own queue. Messages to one peer may complete out of order; the
//     primitive promises no ordering;
//   - a global semaphore capping sends in flight across all peers;
//   - a per-send deadline, derived from a dispatcher context that Stop
//     cancels, never from a client request;
//   - drop-and-count when a peer's queue is full, never unbounded queuing.
//
// Queues are created on first use for a peer in the peer table and removed
// with the peer (eviction or Stop), so goroutines are bounded by peer count.
type sendDispatcher struct {
	localID        NodeID
	transport      func() Transport
	queueDepth     int
	workersPerPeer int
	sendTimeout    time.Duration
	sendSlots      chan struct{}

	baseCtx    context.Context
	cancelBase context.CancelFunc

	mu      sync.Mutex
	queues  map[NodeID]*peerSendQueue
	stopped bool
	workers sync.WaitGroup

	pending atomic.Int64 // queued plus in flight
	dropped atomic.Int64
	metrics *sendMetrics // nil in tests
}

// sendLimits configures a sendDispatcher. Zero fields take the defaults.
type sendLimits struct {
	queueDepth    int
	sendsPerPeer  int
	maxConcurrent int
	sendTimeout   time.Duration
}

func defaultSendLimits() sendLimits {
	return sendLimits{
		queueDepth:    DefaultSendQueueDepth,
		sendsPerPeer:  DefaultMaxSendsPerPeer,
		maxConcurrent: DefaultMaxConcurrentSends,
		sendTimeout:   DefaultSendTimeout,
	}
}

func newSendDispatcher(localID NodeID, transport func() Transport, limits sendLimits) *sendDispatcher {
	defaults := defaultSendLimits()
	if limits.queueDepth < 1 {
		limits.queueDepth = defaults.queueDepth
	}
	if limits.sendsPerPeer < 1 {
		limits.sendsPerPeer = defaults.sendsPerPeer
	}
	if limits.maxConcurrent < 1 {
		limits.maxConcurrent = defaults.maxConcurrent
	}
	if limits.sendTimeout <= 0 {
		limits.sendTimeout = defaults.sendTimeout
	}
	baseCtx, cancelBase := context.WithCancel(context.Background())
	return &sendDispatcher{
		localID:        localID,
		transport:      transport,
		queueDepth:     limits.queueDepth,
		workersPerPeer: limits.sendsPerPeer,
		sendTimeout:    limits.sendTimeout,
		sendSlots:      make(chan struct{}, limits.maxConcurrent),
		baseCtx:        baseCtx,
		cancelBase:     cancelBase,
		queues:         make(map[NodeID]*peerSendQueue),
	}
}

// enqueue queues msg for peer without blocking. It returns false when the
// message was dropped: the peer's queue is full or the dispatcher stopped.
// Callers must only pass peers that are currently in the peer table; the
// Protocol guarantees this by holding peersMutex across the call.
func (d *sendDispatcher) enqueue(peer *Node, msg *Message) bool {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return false
	}
	queue := d.queues[peer.ID]
	if queue == nil {
		queueCtx, cancelQueue := context.WithCancel(d.baseCtx)
		queue = &peerSendQueue{
			jobs:   make(chan sendJob, d.queueDepth),
			ctx:    queueCtx,
			cancel: cancelQueue,
		}
		d.queues[peer.ID] = queue
		d.workers.Add(d.workersPerPeer)
		for workerIndex := 0; workerIndex < d.workersPerPeer; workerIndex++ {
			go d.runWorker(queue)
		}
	}
	// Count before the channel send so the worker never decrements first.
	d.pending.Add(1)
	if d.metrics != nil {
		d.metrics.sendsQueued.Inc()
	}
	select {
	case queue.jobs <- sendJob{peer: peer, msg: msg}:
		d.mu.Unlock()
		return true
	default:
		d.mu.Unlock()
		d.pending.Add(-1)
		d.dropped.Add(1)
		if d.metrics != nil {
			d.metrics.sendsQueued.Dec()
			d.metrics.queueDropped.Inc()
		}
		logging.Debug("[%s] Send queue full for peer %s; dropped %s (msg %s)",
			d.localID, peer.ID, msg.Type, msg.MessageID)
		return false
	}
}

// removePeer stops the peer's workers, aborts its in-flight sends and discards
// whatever is still queued. It does not wait for the worker to exit.
func (d *sendDispatcher) removePeer(id NodeID) {
	d.mu.Lock()
	queue := d.queues[id]
	delete(d.queues, id)
	d.mu.Unlock()
	if queue != nil {
		queue.cancel()
	}
}

// stop cancels every queue and in-flight send, then waits for the workers to
// exit. Messages still queued are discarded. Safe to call more than once.
func (d *sendDispatcher) stop() {
	d.mu.Lock()
	d.stopped = true
	for id, queue := range d.queues {
		queue.cancel()
		delete(d.queues, id)
	}
	d.mu.Unlock()
	d.cancelBase()
	d.workers.Wait()
}

func (d *sendDispatcher) runWorker(queue *peerSendQueue) {
	defer d.workers.Done()
	for {
		select {
		case <-queue.ctx.Done():
			d.discard(queue)
			return
		case job := <-queue.jobs:
			d.markDequeued()
			// select picks randomly among ready cases; never start a send
			// after the queue was cancelled.
			if queue.ctx.Err() != nil {
				d.pending.Add(-1)
				d.discard(queue)
				return
			}
			d.deliver(queue.ctx, job)
		}
	}
}

// discard empties a cancelled queue. No new jobs can arrive: the queue was
// removed from the map under d.mu before its context was cancelled.
func (d *sendDispatcher) discard(queue *peerSendQueue) {
	for {
		select {
		case <-queue.jobs:
			d.markDequeued()
			d.pending.Add(-1)
		default:
			return
		}
	}
}

func (d *sendDispatcher) markDequeued() {
	if d.metrics != nil {
		d.metrics.sendsQueued.Dec()
	}
}

func (d *sendDispatcher) deliver(queueCtx context.Context, job sendJob) {
	defer d.pending.Add(-1)

	select {
	case d.sendSlots <- struct{}{}:
	case <-queueCtx.Done():
		return
	}
	defer func() { <-d.sendSlots }()
	if queueCtx.Err() != nil {
		return
	}

	if d.metrics != nil {
		d.metrics.sendsInFlight.Inc()
		defer d.metrics.sendsInFlight.Dec()
	}

	sendCtx, cancelSend := context.WithTimeout(queueCtx, d.sendTimeout)
	defer cancelSend()

	startedAt := time.Now()
	err := d.send(sendCtx, job)
	if d.metrics != nil {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		d.metrics.sendDuration.WithLabelValues(outcome).Observe(time.Since(startedAt).Seconds())
	}
	if err != nil && queueCtx.Err() == nil {
		logging.Warn("[%s] Failed to send %s to peer %s: %v", d.localID, job.msg.Type, job.peer.ID, err)
	}
}

func (d *sendDispatcher) send(ctx context.Context, job sendJob) error {
	transport := d.transport()
	if transport == nil {
		return fmt.Errorf("transport not set")
	}
	return transport.Send(ctx, job.peer, job.msg)
}
