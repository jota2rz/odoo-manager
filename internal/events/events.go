package events

import (
	"encoding/json"
	"log"
	"sync"
)

// EventType identifies the kind of project event
type EventType string

const (
	ProjectCreated       EventType = "project_created"
	ProjectDeleted       EventType = "project_deleted"
	ProjectStatusChanged EventType = "project_status_changed"
	ProjectActionPending EventType = "project_action_pending"
	ProjectBackupPending EventType = "project_backup_pending"
	ProjectBackupDone    EventType = "project_backup_done"
	DockerStatus         EventType = "docker_status"
)

// Event represents a project lifecycle event broadcast to all SSE clients
type Event struct {
	Type      EventType   `json:"type"`
	ProjectID string      `json:"project_id"`
	Data      interface{} `json:"data,omitempty"`
}

// Hub is a thread-safe pub/sub hub for server-sent events.
// Clients subscribe via Subscribe() and receive events on a channel.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
}

// NewHub creates a new event hub
func NewHub() *Hub {
	return &Hub{
		clients: make(map[chan Event]struct{}),
	}
}

// Subscribe registers a new client and returns its event channel.
// The caller must call Unsubscribe when done.
func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, 16) // buffered to avoid blocking broadcasts
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	log.Printf("SSE client connected (total: %d)", h.ClientCount())
	return ch
}

// Unsubscribe removes a client and closes its channel
func (h *Hub) Unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.clients, ch)
	close(ch)
	h.mu.Unlock()
	log.Printf("SSE client disconnected (total: %d)", h.ClientCount())
}

// Publish sends an event to all connected clients.
// Slow clients that can't keep up will have events dropped.
func (h *Hub) Publish(evt Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	data, _ := json.Marshal(evt)
	log.Printf("Broadcasting event: %s", string(data))

	for ch := range h.clients {
		select {
		case ch <- evt:
		default:
			// Client buffer full — drop event rather than block
			log.Printf("Warning: dropped event for slow SSE client")
		}
	}
}

// ClientCount returns the number of connected clients
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
