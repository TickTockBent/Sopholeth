package ws

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"sopholeth/internal/gossip"
	"sopholeth/internal/logging"
)

const (
	// HeartbeatInterval is the default interval between WS pings while the
	// heartbeat loop is running. The TS reference uses 30s; matching it keeps
	// cross-validation simple.
	HeartbeatInterval = 30 * time.Second

	// MaxMissedPongs is how many consecutive intervals without a pong trigger
	// termination. With 30s intervals this gives the same ~90s dead-peer
	// timeout as the rest of the cluster's ping-based eviction.
	MaxMissedPongs = 3

	// writeTimeout caps individual frame writes. Concurrent writes are
	// serialized through writeMu.
	writeTimeout = 10 * time.Second
)

// Connection wraps a *websocket.Conn with the AttachmentMessage envelope,
// optional HMAC signing/verification, and a ping/pong heartbeat. A single
// internal goroutine reads frames and dispatches handlers; writes are
// serialized through writeMu. All public methods are safe for concurrent use.
type Connection struct {
	ws            *websocket.Conn
	clusterSecret string

	writeMu   sync.Mutex
	closeOnce sync.Once
	closed    atomic.Bool

	missedPongs      atomic.Int32
	heartbeatStop    chan struct{}
	heartbeatStarted atomic.Bool
	heartbeatPeriod  time.Duration

	handlersMu         sync.RWMutex
	onMessage          func(*gossip.Message)
	onError            func(error)
	attachmentHandlers []handlerEntry[func(*AttachmentMessage)]
	closeHandlers      []handlerEntry[func(int, string)]
	handlerSeq         atomic.Uint64

	remoteMu      sync.RWMutex
	remoteNodeID  string
	remoteEnclave string
}

// NewConnection wraps an open *websocket.Conn. The Connection takes ownership
// of ws — call Close or Terminate to release it. The clusterSecret enables
// HMAC signing of every outgoing payload and verification of every incoming
// payload; passing "" disables signing (open-cluster mode).
func NewConnection(ws *websocket.Conn, clusterSecret string) *Connection {
	c := &Connection{
		ws:              ws,
		clusterSecret:   clusterSecret,
		heartbeatStop:   make(chan struct{}),
		heartbeatPeriod: HeartbeatInterval,
	}
	ws.SetPongHandler(func(string) error {
		c.missedPongs.Store(0)
		return nil
	})
	go c.readLoop()
	return c
}

// handlerEntry wraps a multi-subscriber lifecycle callback with a stable ID
// so a registration can be removed later (matches the EventEmitter
// addListener/removeListener pattern in the TS reference).
type handlerEntry[T any] struct {
	id uint64
	fn T
}

// OnMessage installs the single application-level gossip-message handler.
// Subsequent calls replace the previous handler. Lifecycle events use the
// multi-subscriber AddOn* methods instead.
func (c *Connection) OnMessage(fn func(*gossip.Message)) {
	c.handlersMu.Lock()
	c.onMessage = fn
	c.handlersMu.Unlock()
}

// OnError registers the read-loop error handler. Subsequent calls replace.
// Fatal errors surface through close handlers instead.
func (c *Connection) OnError(fn func(error)) {
	c.handlersMu.Lock()
	c.onError = fn
	c.handlersMu.Unlock()
}

// AddAttachmentHandler subscribes fn to every parsed AttachmentMessage
// (gossip + hello/welcome/goodbye). The returned function removes this
// subscription; multiple subscribers fire in registration order.
//
// The tree-manager attach handshake uses this to register a temporary
// welcome/goodbye-waiting handler that it removes once welcome arrives, then
// registers a long-lived goodbye-handling subscription.
func (c *Connection) AddAttachmentHandler(fn func(*AttachmentMessage)) (remove func()) {
	id := c.handlerSeq.Add(1)
	c.handlersMu.Lock()
	c.attachmentHandlers = append(c.attachmentHandlers, handlerEntry[func(*AttachmentMessage)]{id: id, fn: fn})
	c.handlersMu.Unlock()
	return func() {
		c.handlersMu.Lock()
		defer c.handlersMu.Unlock()
		for i, h := range c.attachmentHandlers {
			if h.id == id {
				c.attachmentHandlers = append(c.attachmentHandlers[:i], c.attachmentHandlers[i+1:]...)
				return
			}
		}
	}
}

// AddCloseHandler subscribes fn to the one-shot close event. Multiple
// subscribers fire in registration order. The returned function removes
// the subscription (e.g., when a temporary handler should not run if
// close happens later).
func (c *Connection) AddCloseHandler(fn func(code int, reason string)) (remove func()) {
	id := c.handlerSeq.Add(1)
	c.handlersMu.Lock()
	c.closeHandlers = append(c.closeHandlers, handlerEntry[func(int, string)]{id: id, fn: fn})
	c.handlersMu.Unlock()
	return func() {
		c.handlersMu.Lock()
		defer c.handlersMu.Unlock()
		for i, h := range c.closeHandlers {
			if h.id == id {
				c.closeHandlers = append(c.closeHandlers[:i], c.closeHandlers[i+1:]...)
				return
			}
		}
	}
}

// RemoteNodeID returns the peer's node ID once the hello/welcome handshake
// has populated it. Empty before the handshake completes.
func (c *Connection) RemoteNodeID() string {
	c.remoteMu.RLock()
	defer c.remoteMu.RUnlock()
	return c.remoteNodeID
}

// RemoteEnclave returns the peer's enclave, populated alongside RemoteNodeID.
func (c *Connection) RemoteEnclave() string {
	c.remoteMu.RLock()
	defer c.remoteMu.RUnlock()
	return c.remoteEnclave
}

// SetRemote records the peer's identity after a successful hello/welcome.
// Intended to be called by the tree manager once it has processed the
// handshake payload.
func (c *Connection) SetRemote(nodeID, enclave string) {
	c.remoteMu.Lock()
	c.remoteNodeID = nodeID
	c.remoteEnclave = enclave
	c.remoteMu.Unlock()
}

// IsClosed reports whether the connection has been torn down.
func (c *Connection) IsClosed() bool { return c.closed.Load() }

// SendGossip serializes msg into a SimpleMessage payload, wraps it in an
// AttachmentMessage of the matching gossip type, and writes it.
func (c *Connection) SendGossip(msg *gossip.Message) error {
	if c.closed.Load() {
		return nil
	}
	return c.SendAttachment(gossipTypeFor(msg.Type), messageToWire(msg))
}

// SendAttachment marshals payload, signs it with the cluster secret when set,
// and writes the framed envelope.
func (c *Connection) SendAttachment(t AttachmentType, payload any) error {
	if c.closed.Load() {
		return nil
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	env := AttachmentMessage{Type: t, Payload: payloadBytes}
	if c.clusterSecret != "" {
		env.Signature = gossip.SignBody(c.clusterSecret, payloadBytes)
	}
	frame, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	return c.writeFrame(frame)
}

func (c *Connection) writeFrame(frame []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return nil
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	return c.ws.WriteMessage(websocket.TextMessage, frame)
}

// StartHeartbeat begins sending WS pings at the configured period. After
// MaxMissedPongs consecutive intervals without a pong the connection is
// terminated. Idempotent — repeated calls are no-ops.
func (c *Connection) StartHeartbeat() {
	if !c.heartbeatStarted.CompareAndSwap(false, true) {
		return
	}
	go c.heartbeatLoop()
}

// StopHeartbeat halts the heartbeat goroutine. Idempotent.
func (c *Connection) StopHeartbeat() {
	if c.heartbeatStarted.Load() {
		c.closeHeartbeat()
	}
}

func (c *Connection) closeHeartbeat() {
	defer func() { _ = recover() }()
	select {
	case <-c.heartbeatStop:
	default:
		close(c.heartbeatStop)
	}
}

func (c *Connection) heartbeatLoop() {
	c.missedPongs.Store(0)
	ticker := time.NewTicker(c.heartbeatPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-c.heartbeatStop:
			return
		case <-ticker.C:
			if c.closed.Load() {
				return
			}
			// Increment first; an arriving pong resets to zero. Matches TS.
			missed := c.missedPongs.Add(1)
			if missed > MaxMissedPongs {
				c.remoteMu.RLock()
				id := c.remoteNodeID
				c.remoteMu.RUnlock()
				logging.Warn("WebSocket to %q: %d missed pongs, terminating", id, missed)
				c.Terminate()
				return
			}
			// WriteControl is documented safe alongside WriteMessage, so we
			// do not hold writeMu here — that would block heartbeat on slow
			// large-frame writes.
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				logging.Debug("WebSocket ping failed: %v", err)
			}
		}
	}
}

// Close performs a graceful close, sending a close frame with the given code
// and reason. Idempotent.
func (c *Connection) Close(code int, reason string) {
	c.closeWith(code, reason, false)
}

// Terminate tears the connection down immediately without a close frame.
func (c *Connection) Terminate() {
	c.closeWith(websocket.CloseAbnormalClosure, "", true)
}

func (c *Connection) closeWith(code int, reason string, force bool) {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.closeHeartbeat()
		if !force {
			msg := websocket.FormatCloseMessage(code, reason)
			_ = c.ws.WriteControl(websocket.CloseMessage, msg, time.Now().Add(writeTimeout))
		}
		_ = c.ws.Close()
	})
}

func (c *Connection) readLoop() {
	closeCode := websocket.CloseAbnormalClosure
	closeReason := ""

	c.ws.SetCloseHandler(func(code int, text string) error {
		closeCode = code
		closeReason = text
		msg := websocket.FormatCloseMessage(code, "")
		_ = c.ws.WriteControl(websocket.CloseMessage, msg, time.Now().Add(writeTimeout))
		return nil
	})

	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				closeCode = closeErr.Code
				closeReason = closeErr.Text
			} else if errors.Is(err, net.ErrClosed) {
				// local Close() already recorded the intent
			}
			c.closed.Store(true)
			c.closeHeartbeat()
			_ = c.ws.Close()
			c.fireClose(closeCode, closeReason)
			return
		}
		c.handleFrame(data)
	}
}

func (c *Connection) handleFrame(data []byte) {
	var env AttachmentMessage
	if err := json.Unmarshal(data, &env); err != nil {
		logging.Warn("WebSocket received invalid JSON, ignoring")
		return
	}
	if env.Type == "" || len(env.Payload) == 0 {
		logging.Warn("WebSocket received malformed AttachmentMessage, ignoring")
		return
	}

	if c.clusterSecret != "" {
		if env.Signature == "" {
			logging.Warn("WebSocket message missing signature, rejecting")
			return
		}
		if !gossip.VerifyBody(c.clusterSecret, env.Payload, env.Signature) {
			logging.Warn("WebSocket message signature invalid, rejecting")
			return
		}
	}

	c.fireAttachment(&env)

	if isGossipType(env.Type) {
		var wire gossip.SimpleMessage
		if err := json.Unmarshal(env.Payload, &wire); err != nil {
			c.fireError(fmt.Errorf("decode gossip payload: %w", err))
			return
		}
		c.fireMessage(wireToMessage(&wire))
	}
}

func (c *Connection) fireMessage(msg *gossip.Message) {
	c.handlersMu.RLock()
	fn := c.onMessage
	c.handlersMu.RUnlock()
	if fn != nil {
		fn(msg)
	}
}

func (c *Connection) fireAttachment(msg *AttachmentMessage) {
	c.handlersMu.RLock()
	handlers := append([]handlerEntry[func(*AttachmentMessage)](nil), c.attachmentHandlers...)
	c.handlersMu.RUnlock()
	for _, h := range handlers {
		h.fn(msg)
	}
}

func (c *Connection) fireClose(code int, reason string) {
	c.handlersMu.RLock()
	handlers := append([]handlerEntry[func(int, string)](nil), c.closeHandlers...)
	c.handlersMu.RUnlock()
	for _, h := range handlers {
		h.fn(code, reason)
	}
}

func (c *Connection) fireError(err error) {
	c.handlersMu.RLock()
	fn := c.onError
	c.handlersMu.RUnlock()
	if fn != nil {
		fn(err)
	}
}
