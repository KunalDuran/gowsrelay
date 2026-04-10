package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
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
	if cfg.PersistDir != "" {
		if err := os.MkdirAll(cfg.PersistDir, 0755); err != nil {
			log.Fatalf("failed to create persist directory %q: %v", cfg.PersistDir, err)
		}
	}
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

// HandleAdminPersist toggles hex-dump persistence for an active topic.
//
//	POST   /admin/persist?topic=name  — enable (appends to <PersistDir>/<topic>.hex)
//	DELETE /admin/persist?topic=name  — disable and flush
func HandleAdminPersist(w http.ResponseWriter, r *http.Request) {
	topicName := r.URL.Query().Get("topic")
	if !validTopicName.MatchString(topicName) {
		http.Error(w, "missing or invalid topic", http.StatusBadRequest)
		return
	}

	topic, ok := proxy.GetExistingTopic(topicName)
	if !ok {
		http.Error(w, "topic not found — it must have at least one active connection", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodPost:
		if err := topic.EnablePersist(proxy.cfg.PersistDir); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "persist enabled", "topic": topicName})

	case http.MethodDelete:
		topic.DisablePersist()
		json.NewEncoder(w).Encode(map[string]string{"status": "persist disabled", "topic": topicName})

	default:
		http.Error(w, "POST to enable, DELETE to disable", http.StatusMethodNotAllowed)
	}
}
