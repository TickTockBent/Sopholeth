package ws

import (
	"net/http"

	"github.com/gorilla/websocket"

	"sopholeth/internal/logging"
)

// Handler returns the HTTP handler for the /v1/ws endpoint. On successful
// upgrade, onAccept is invoked with the wrapped Connection; the handler then
// returns, and the connection's read loop drives lifecycle from there.
//
// allowOrigin matches gorilla's Upgrader.CheckOrigin contract — pass nil to
// accept any origin (the node's CORS policy is intentionally permissive; see #38).
func Handler(clusterSecret string, allowOrigin func(*http.Request) bool, onAccept func(*Connection)) http.HandlerFunc {
	upgrader := &websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			if allowOrigin != nil {
				return allowOrigin(r)
			}
			return true
		},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrader.Upgrade has already written the HTTP error response.
			logging.Warn("WebSocket upgrade failed: %v", err)
			return
		}
		conn := NewConnection(ws, clusterSecret)
		if onAccept != nil {
			onAccept(conn)
		}
	}
}
