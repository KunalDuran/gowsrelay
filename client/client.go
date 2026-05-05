package client

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
)

type TunnelConfig struct {
	Scheme    string
	Host      string
	Port      string // port to open on the device
	LocalHost string // local host to forward to; defaults to "localhost"
	Path      string
	Topic     string
}

const (
	WS  = "ws"
	WSS = "wss"
)

func CreateWebSocketTunnel(cfg TunnelConfig) error {
	if strings.TrimSpace(cfg.LocalHost) == "" {
		cfg.LocalHost = "localhost"
	}

	port, err := strconv.Atoi(cfg.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid local port %q: must be a number between 1 and 65535", cfg.Port)
	}

	wsURL := url.URL{
		Scheme:   cfg.Scheme,
		Host:     cfg.Host,
		Path:     cfg.Path,
		RawQuery: fmt.Sprintf("role=producer&topic=%s", cfg.Topic),
	}

	log.Printf("connecting to %s", wsURL.String())

	dialer := websocket.DefaultDialer

	ws, _, err := dialer.Dial(wsURL.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect to websocket: %w", err)
	}
	defer ws.Close()

	log.Println("connected to websocket")

	tcpConn, err := net.Dial("tcp", net.JoinHostPort(cfg.LocalHost, cfg.Port))
	if err != nil {
		return fmt.Errorf("failed to connect to local tcp %s:%s: %w", cfg.LocalHost, cfg.Port, err)
	}
	defer tcpConn.Close()

	errCh := make(chan error, 2)
	ws.UnderlyingConn()
	// WS → TCP (reader preserves stream)
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
			if _, err := io.Copy(tcpConn, r); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// TCP → WS (writer preserves stream)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := tcpConn.Read(buf)
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

	log.Printf("proxying local tcp %s ↔ %s", cfg.Port, ws.RemoteAddr())

	err = <-errCh
	if err != nil && err != io.EOF {
		log.Println("tunnel error:", err)
		return err
	}

	return nil
}
