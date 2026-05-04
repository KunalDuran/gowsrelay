package server

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

var proxy *Proxy

// Configure initialises the proxy with the given config.
// Must be called before the HTTP server starts accepting connections.
func Configure(cfg Config) {
	proxy = NewProxy(cfg)
}

// validTopicName restricts topic names to safe characters, preventing path
// traversal even before filepath.Base strips directory components in run().
var validTopicName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	roleParam := r.URL.Query().Get("role")

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
	if !validTopicName.MatchString(topicName) {
		http.Error(w, "Invalid topic name. Use alphanumeric characters, hyphens, or underscores (max 64 chars)", http.StatusBadRequest)
		return
	}

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
	if err := topic.AddClient(client); err != nil {
		log.Printf("Failed to add client: %v", err)
		client.Close()
		return
	}
	defer topic.RemoveClient(client)

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

// SetTopicWorker sets a worker on an active topic. Returns false if the topic
// does not exist (no active connections yet).
func SetTopicWorker(topicName string, fn WorkerFunc) bool {
	topic, ok := proxy.GetExistingTopic(topicName)
	if !ok {
		return false
	}
	topic.SetWorker(fn)
	return true
}

// ClearTopicWorker removes the worker from an active topic. Returns false if
// the topic does not exist.
func ClearTopicWorker(topicName string) bool {
	topic, ok := proxy.GetExistingTopic(topicName)
	if !ok {
		return false
	}
	topic.ClearWorker()
	return true
}

