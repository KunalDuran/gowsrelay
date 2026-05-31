package client

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/gorilla/websocket"
)

type WSEndpoint struct {
	ws      *websocket.Conn
	msgType int // frame type used for Write (TextMessage or BinaryMessage)

	readMu   sync.Mutex
	leftover []byte // unread tail of the current upstream message

	writeMu sync.Mutex
}

// NewWSEndpoint dials rawURL (ws:// or wss://) and returns a ready endpoint.
// Outgoing messages are sent as text frames; pass headers for auth etc.
func NewWSEndpoint(rawURL string, header http.Header) (*WSEndpoint, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse ws url %q: %w", rawURL, err)
	}

	ws, _, err := websocket.DefaultDialer.Dial(u.String(), header)
	if err != nil {
		return nil, fmt.Errorf("ws dial %s: %w", rawURL, err)
	}

	return &WSEndpoint{
		ws:      ws,
		msgType: websocket.TextMessage,
	}, nil
}

// Read returns bytes from the next upstream message, buffering any tail that
// does not fit in p so the next Read continues where this one left off.
func (e *WSEndpoint) Read(p []byte) (int, error) {
	e.readMu.Lock()
	defer e.readMu.Unlock()

	for len(e.leftover) == 0 {
		_, data, err := e.ws.ReadMessage()
		if err != nil {
			return 0, err
		}
		e.leftover = data // may be empty (zero-length frame) — loop again
	}

	n := copy(p, e.leftover)
	e.leftover = e.leftover[n:]
	return n, nil
}

// Write sends p as a single upstream WebSocket message.
func (e *WSEndpoint) Write(p []byte) (int, error) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	if err := e.ws.WriteMessage(e.msgType, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close shuts down the upstream connection, unblocking a pending Read.
func (e *WSEndpoint) Close() error {
	return e.ws.Close()
}
