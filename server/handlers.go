package server

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

var proxy = NewProxy()

func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	roleParam := r.URL.Query().Get("role")
	if roleParam == "" {
		http.Error(w, "Missing 'role' parameter (producer/subscriber)", http.StatusBadRequest)
		return
	}

	var role Role
	switch roleParam {
	case "producer":
		role = Producer
	case "subscriber":
		role = Subscriber
	default:
		http.Error(w, "Invalid role. Use 'producer' or 'subscriber'", http.StatusBadRequest)
		return
	}

	topicName := r.URL.Query().Get("topic")
	if topicName == "" {
		topicName = "default"
	}

	// Upgrade connection
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Upgrade failed: %v", err)
		return
	}

	// Get topic and register client
	topic, err := proxy.GetTopic(topicName)
	if err != nil {
		log.Printf("Failed to get topic %s: %v", topicName, err)
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseUnsupportedData, err.Error()))
		conn.Close()
		return
	}

	client := NewClient(conn, role)
	topic.RegisterClient(client)
	defer topic.UnregisterClient(client)

	// Wait for client to finish
	<-client.ctx.Done()
}

func HandleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(proxy.Stats())
}

func HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}
