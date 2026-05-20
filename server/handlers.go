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

// validTopicName restricts topic names to safe characters, preventing path
// traversal even before filepath.Base strips directory components in run().
var validTopicName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (p *Proxy) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
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

	var parser Parser
	if name := r.URL.Query().Get("parser"); name != "" {
		if role != Subscriber {
			http.Error(w, "parser param is only valid for subscribers", http.StatusBadRequest)
			return
		}
		p.mu.RLock()
		parser = p.parsers[name]
		p.mu.RUnlock()
		if parser == nil {
			http.Error(w, "unknown parser: "+name, http.StatusBadRequest)
			return
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Upgrade failed: %v", err)
		return
	}

	topic, err := p.GetTopic(topicName)
	if err != nil {
		log.Printf("Failed to get topic %s: %v", topicName, err)
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseUnsupportedData, err.Error()))
		conn.Close()
		return
	}

	client := NewClient(conn, role)
	client.parser = parser

	if err := topic.AddClient(client); err != nil {
		log.Printf("Failed to add client: %v", err)
		client.Close()
		return
	}
	defer topic.RemoveClient(client)

	<-client.ctx.Done()
}

func (p *Proxy) HandleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p.Stats())
}

func (p *Proxy) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}
