package client

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/url"

	"github.com/gorilla/websocket"
)

func CreateWebSocketTunnel(host, portToOpen, path, topic string) error {
	wsURL := url.URL{
		Scheme:   "ws",
		Host:     host,
		Path:     path,
		RawQuery: fmt.Sprintf("role=producer&topic=%s", topic),
	}

	log.Printf("connecting to %s", wsURL.String())

	ws, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect to websocket: %w", err)
	}
	defer ws.Close()

	log.Println("connected to websocket")

	tcpConn, err := net.Dial("tcp", fmt.Sprintf("localhost:%s", portToOpen))
	if err != nil {
		return fmt.Errorf("failed to connect to local tcp %s: %w", portToOpen, err)
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

	log.Printf("proxying local tcp %s ↔ %s", portToOpen, ws.RemoteAddr())

	err = <-errCh
	if err != nil && err != io.EOF {
		log.Println("tunnel error:", err)
		return err
	}

	return nil
}
