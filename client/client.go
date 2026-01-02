package client

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/url"

	"github.com/gorilla/websocket"
)

func CreateWebSocketTunnel(host, port, path, topic string) error {

	wsURL := url.URL{
		Scheme:   "ws",
		Host:     host,
		Path:     "/ws",
		RawQuery: fmt.Sprintf("role=producer&topic=%s", topic),
	}

	log.Printf("connecting to %s", wsURL.String())

	conn, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}
	defer conn.Close()

	log.Println("Connected to WebSocket server")

	target, err := net.Dial("tcp", fmt.Sprintf("localhost:%s", port))
	if err != nil {
		return fmt.Errorf("failed to connect to target localhost:%s: %w", port, err)
	}
	defer target.Close()

	errChan := make(chan error, 2)
	done := make(chan struct{})

	// WS -> TCP
	go func() {
		defer func() { close(done) }() // signal that at least one direction finished

		for {
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				nonBlockingSend(errChan, err)
				return
			}

			if messageType != websocket.BinaryMessage {
				log.Println("ignoring non-binary message", messageType)
				continue
			}

			if _, err = target.Write(message); err != nil {
				nonBlockingSend(errChan, err)
				return
			}
		}
	}()

	// TCP -> WS
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := target.Read(buf)
			if err != nil {
				nonBlockingSend(errChan, err)
				return
			}
			if err = conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				nonBlockingSend(errChan, err)
				return
			}
		}
	}()

	log.Println("proxying", port, "to", host, "remote", conn.RemoteAddr())

	// Wait for one direction to finish
	select {
	case err = <-errChan:
		if err != nil && err != io.EOF {
			log.Println("proxy error:", err)
		}
	case <-done:
		// One direction ended without sending an error; still fine.
	}

	return nil
}

// nonBlockingSend avoids goroutine leaks if nobody is listening anymore
func nonBlockingSend(ch chan<- error, err error) {
	select {
	case ch <- err:
	default:
	}
}
