package server

import (
	"context"
	"fmt"
	"log"
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
