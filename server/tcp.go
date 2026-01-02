package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/gorilla/websocket"
)

func HandleTCPProxy(w http.ResponseWriter, r *http.Request) {

	topic := r.URL.Query().Get("topic")
	if topic == "" {
		http.Error(w, "missing topic query param", http.StatusBadRequest)
		return
	}

	wsURL := url.URL{
		Scheme:   "ws",
		Host:     "localhost:8090",
		Path:     "/ws",
		RawQuery: "role=subscriber&topic=" + url.QueryEscape(topic),
	}

	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	if err != nil {
		log.Println("websocket dial error:", err)
		http.Error(w, "failed to connect to websocket backend", http.StatusBadGateway)
		return
	}

	// Create TCP listener on random free port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Println("tcp listen error:", err)
		wsConn.Close()
		http.Error(w, "failed to create tcp listener", http.StatusInternalServerError)
		return
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	port := tcpAddr.Port

	// Start goroutine that accepts exactly ONE connection
	go func() {
		defer func() {
			ln.Close()
			wsConn.Close() // Close the websocket when listener closes
			log.Println("tcp listener closed:", ln.Addr())
		}()

		conn, err := ln.Accept() // Accept only ONCE, not in a loop
		if err != nil {
			log.Println("tcp accept error:", err)
			return
		}

		log.Printf("Client connected on TCP %d for topic %s\n", port, topic)
		handleTCPClient(conn, wsConn)
	}()

	// Return JSON with newly opened TCP port
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]int{"port": port}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Println("json encode error:", err)
		return
	}

	fmt.Printf("Created proxy for topic=%s on TCP port %d\n", topic, port)
}

func handleTCPClient(conn net.Conn, wsConn *websocket.Conn) {
	defer conn.Close()

	// Ensure WS + TCP close exactly once
	var closeOnce sync.Once
	closeAll := func() {
		closeOnce.Do(func() {
			conn.Close()
			wsConn.Close()
			log.Println("tcp + websocket connection closed")
		})
	}

	// TCP -> WS
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				log.Println("read from tcp error:", err)
				closeAll()
				return
			}

			if err := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				log.Println("write to ws error:", err)
				closeAll()
				return
			}
		}
	}()

	// WS -> TCP (blocking)
	for {
		msgType, data, err := wsConn.ReadMessage()
		if err != nil {
			log.Println("read from ws error:", err)
			closeAll()
			return
		}

		if msgType != websocket.BinaryMessage {
			log.Println("unexpected message type:", msgType)
			continue
		}

		if _, err := conn.Write(data); err != nil {
			log.Println("write to tcp error:", err)
			closeAll()
			return
		}
	}
}
