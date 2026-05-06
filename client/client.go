package client

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
)

// TunnelConfig holds the WebSocket connection parameters.
type TunnelConfig struct {
	Scheme string
	Host   string
	Path   string
	Topic  string
}

// LocalEndpoint is anything that can act as the local side of the tunnel.
// TCP conn, subprocess stdio, serial port, whatever — just give us
// a reader, a writer, and a way to shut it down.
type LocalEndpoint interface {
	io.ReadWriteCloser
}

// TCPEndpoint dials a local TCP port and returns an endpoint.
func TCPEndpoint(host, port string) (LocalEndpoint, error) {
	if strings.TrimSpace(host) == "" {
		host = "localhost"
	}
	conn, err := net.Dial("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("tcp dial %s:%s: %w", host, port, err)
	}
	return conn, nil // net.Conn already satisfies io.ReadWriteCloser
}

func CreateWebSocketTunnel(cfg TunnelConfig, endpoint LocalEndpoint) error {
	defer endpoint.Close()

	wsURL := url.URL{
		Scheme:   cfg.Scheme,
		Host:     cfg.Host,
		Path:     cfg.Path,
		RawQuery: fmt.Sprintf("role=producer&topic=%s", cfg.Topic),
	}

	log.Printf("connecting to %s", wsURL.String())

	ws, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	defer ws.Close()

	log.Println("connected to websocket")

	errCh := make(chan error, 2)

	// WS → local endpoint
	go func() {
		for {
			msgType, r, err := ws.NextReader()
			if err != nil {
				errCh <- err
				return
			}
			if msgType != websocket.BinaryMessage {
				continue
			}
			if _, err := io.Copy(endpoint, r); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// local endpoint → WS
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := endpoint.Read(buf)
			if err != nil {
				errCh <- err
				return
			}
			w, err := ws.NextWriter(websocket.BinaryMessage)
			if err != nil {
				errCh <- err
				return
			}
			if _, err := w.Write(buf[:n]); err != nil {
				w.Close()
				errCh <- err
				return
			}
			if err := w.Close(); err != nil {
				errCh <- err
				return
			}
		}
	}()

	log.Printf("proxying endpoint ↔ %s", wsURL.String())

	err = <-errCh
	if err != nil && err != io.EOF {
		log.Println("tunnel error:", err)
		return err
	}
	return nil
}
