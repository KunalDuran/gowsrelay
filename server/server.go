package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	WriteTimeout = 5 * time.Second
	ReadTimeout  = 60 * time.Second
	PingInterval = 54 * time.Second

	MaxMessageSize = 64 * 1024 // 64KB max message
	QueueSize      = 256       // per-client send queue

	MaxProducersPerTopic   = 1    // Only 1 producer per topic
	MaxSubscribersPerTopic = 1000 // Max subscribers per topic
	MaxTopics              = 1000 // Max total topics
)

type Role string

const (
	Producer   Role = "producer"
	Subscriber Role = "subscriber"
)

type Message struct {
	Data []byte
	From Role
}

type Client struct {
	id     string
	conn   *websocket.Conn
	role   Role
	send   chan []byte
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	mu     sync.Mutex
}

func NewClient(conn *websocket.Conn, role Role) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		id:     fmt.Sprintf("%s_%d", role, time.Now().UnixNano()),
		conn:   conn,
		role:   role,
		send:   make(chan []byte, QueueSize),
		ctx:    ctx,
		cancel: cancel,
	}
}

func (c *Client) ReadMessages(messages chan<- Message) {
	defer c.Close()

	c.conn.SetReadLimit(MaxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(ReadTimeout))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(ReadTimeout))
		return nil
	})

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		msgType, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Client %s read error: %v", c.id, err)
			}
			return
		}

		if msgType == websocket.BinaryMessage || msgType == websocket.TextMessage {
			select {
			case messages <- Message{Data: data, From: c.role}:
			case <-c.ctx.Done():
				return
			}
		}
	}
}

func (c *Client) WriteMessages() {
	defer c.Close()

	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()

	for {
		select {
		case data, ok := <-c.send:
			if !ok {
				c.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			c.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
			if err := c.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				log.Printf("Client %s write error: %v", c.id, err)
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Client) Send(data []byte) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()

	select {
	case c.send <- data:
		return true
	case <-c.ctx.Done():
		return false
	default:
		// Channel full - drop message instead of blocking
		log.Printf("Client %s send queue full, dropping message", c.id)
		return false
	}
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	c.closed = true

	c.cancel()
	close(c.send)
	c.conn.Close()
}

func (c *Client) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

type Topic struct {
	name        string
	producer    *Client
	subscribers map[string]*Client
	messages    chan Message
	register    chan *Client
	unregister  chan *Client
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	mu          sync.RWMutex
}

func NewTopic(name string) *Topic {
	ctx, cancel := context.WithCancel(context.Background())
	t := &Topic{
		name:        name,
		subscribers: make(map[string]*Client),
		messages:    make(chan Message, QueueSize*4), // Larger buffer for hub
		register:    make(chan *Client, 10),
		unregister:  make(chan *Client, 10),
		ctx:         ctx,
		cancel:      cancel,
	}

	t.wg.Add(1)
	go t.run()
	return t
}

func (t *Topic) run() {
	defer t.wg.Done()

	for {
		select {
		case client := <-t.register:
			if err := t.addClient(client); err != nil {
				log.Printf("Topic %s: Failed to add client: %v", t.name, err)
				if !client.IsClosed() {
					client.Close()
				}
				continue
			}

			// Start client goroutines
			go client.ReadMessages(t.messages)
			go client.WriteMessages()

		case client := <-t.unregister:
			t.removeClient(client)

		case msg := <-t.messages:
			t.routeMessage(msg)

		case <-t.ctx.Done():
			return
		}
	}
}

func (t *Topic) addClient(client *Client) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if client.role == Producer {
		if t.producer != nil {
			return fmt.Errorf("producer already exists")
		}
		t.producer = client
		log.Printf("Topic %s: Producer %s connected", t.name, client.id)
	} else {
		if len(t.subscribers) >= MaxSubscribersPerTopic {
			return fmt.Errorf("max subscribers reached")
		}
		t.subscribers[client.id] = client
		log.Printf("Topic %s: Subscriber %s connected (total: %d)",
			t.name, client.id, len(t.subscribers))
	}
	return nil
}

func (t *Topic) removeClient(client *Client) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if client.role == Producer && t.producer == client {
		t.producer = nil
		log.Printf("Topic %s: Producer %s disconnected", t.name, client.id)
	} else if client.role == Subscriber {
		delete(t.subscribers, client.id)
		log.Printf("Topic %s: Subscriber %s disconnected (remaining: %d)",
			t.name, client.id, len(t.subscribers))
	}

	// Only close if not already closed
	if !client.IsClosed() {
		client.Close()
	}
}

func (t *Topic) routeMessage(msg Message) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if msg.From == Producer {
		// Producer → All Subscribers
		dropped := 0
		for _, sub := range t.subscribers {
			if !sub.Send(msg.Data) {
				dropped++
			}
		}
		if dropped > 0 {
			log.Printf("Topic %s: Dropped message to %d slow subscribers", t.name, dropped)
		}
	} else if msg.From == Subscriber && t.producer != nil {
		// Subscriber → Producer only
		if !t.producer.Send(msg.Data) {
			log.Printf("Topic %s: Failed to send to producer", t.name)
		}
	}
}

func (t *Topic) RegisterClient(client *Client) {
	select {
	case t.register <- client:
	case <-t.ctx.Done():
		if !client.IsClosed() {
			client.Close()
		}
	}
}

func (t *Topic) UnregisterClient(client *Client) {
	select {
	case t.unregister <- client:
	case <-t.ctx.Done():
	}
}

func (t *Topic) Stats() map[string]interface{} {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return map[string]interface{}{
		"name":               t.name,
		"has_producer":       t.producer != nil,
		"subscriber_count":   len(t.subscribers),
		"message_queue_size": len(t.messages),
	}
}

func (t *Topic) Stop() {
	t.cancel()

	t.mu.Lock()
	if t.producer != nil && !t.producer.IsClosed() {
		t.producer.Close()
	}
	for _, sub := range t.subscribers {
		if !sub.IsClosed() {
			sub.Close()
		}
	}
	t.mu.Unlock()

	t.wg.Wait()
}

type Proxy struct {
	topics map[string]*Topic
	mu     sync.RWMutex
}

func NewProxy() *Proxy {
	return &Proxy{
		topics: make(map[string]*Topic),
	}
}

func (p *Proxy) GetTopic(name string) (*Topic, error) {
	p.mu.RLock()
	topic := p.topics[name]
	p.mu.RUnlock()

	if topic != nil {
		return topic, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after write lock
	if topic = p.topics[name]; topic != nil {
		return topic, nil
	}

	if len(p.topics) >= MaxTopics {
		return nil, fmt.Errorf("max topics reached")
	}

	topic = NewTopic(name)
	p.topics[name] = topic
	log.Printf("Created topic: %s", name)
	return topic, nil
}

func (p *Proxy) Stats() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()

	topics := make(map[string]interface{})
	totalSubs := 0
	activeProducers := 0

	for name, topic := range p.topics {
		stats := topic.Stats()
		topics[name] = stats
		totalSubs += stats["subscriber_count"].(int)
		if stats["has_producer"].(bool) {
			activeProducers++
		}
	}

	return map[string]interface{}{
		"total_topics":      len(p.topics),
		"active_producers":  activeProducers,
		"total_subscribers": totalSubs,
		"topics":            topics,
		"timestamp":         time.Now().Format(time.RFC3339),
	}
}

func (p *Proxy) Shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for name, topic := range p.topics {
		log.Printf("Stopping topic: %s", name)
		topic.Stop()
	}
}

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

type proxyResponse struct {
	Port int `json:"port"`
}

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

	// Start goroutine that will accept exactly one TCP connection and proxy it
	go func() {
		defer func() {
			ln.Close()
			log.Println("tcp listener closed:", ln.Addr())
		}()

		for {
			conn, err := ln.Accept()
			if err != nil {
				// If listener is intentionally closed, exit
				if ne, ok := err.(net.Error); ok && ne.Temporary() {
					log.Println("temporary accept error:", err)
					continue
				}
				log.Println("tcp accept error:", err)
				return
			}

			log.Printf("Client connected on TCP %d for topic %s\n", port, topic)

			// Handle each TCP client independently
			go handleTCPClient(conn, wsConn)
		}
	}()

	// Return JSON with newly opened TCP port
	w.Header().Set("Content-Type", "application/json")
	resp := proxyResponse{Port: port}
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
