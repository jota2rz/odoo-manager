package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jota2rz/odoo-manager/internal/docker"
	"github.com/jota2rz/odoo-manager/internal/events"
	"github.com/jota2rz/odoo-manager/internal/store"
	"github.com/jota2rz/odoo-manager/templates"
)

// Handler manages HTTP requests
type Handler struct {
	store         *store.ProjectStore
	dockerManager *docker.Manager
	staticFS      http.Handler
	events        *events.Hub
}

// NewHandler creates a new HTTP handler
func NewHandler(projectStore *store.ProjectStore, staticFS http.Handler, eventHub *events.Hub) *Handler {
	dockerManager, err := docker.NewManager()
	if err != nil {
		log.Printf("Warning: Failed to create Docker manager: %v", err)
	}

	return &Handler{
		store:         projectStore,
		dockerManager: dockerManager,
		staticFS:      staticFS,
		events:        eventHub,
	}
}

// RegisterRoutes sets up all HTTP routes
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Static files
	if h.staticFS != nil {
		mux.Handle("/static/", h.staticFS)
	}

	// Pages
	mux.HandleFunc("/", h.handleIndex)
	mux.HandleFunc("/projects", h.handleProjects)

	// API endpoints
	mux.HandleFunc("/api/projects", h.handleAPIProjects)
	mux.HandleFunc("/api/projects/", h.handleAPIProject)
	mux.HandleFunc("/api/projects/{id}/start", h.handleStartProject)
	mux.HandleFunc("/api/projects/{id}/stop", h.handleStopProject)
	mux.HandleFunc("/api/projects/{id}/logs", h.handleProjectLogs)

	// SSE event stream
	mux.HandleFunc("/api/events", h.handleSSE)
}

// handleIndex serves the main dashboard page
func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	projects := h.store.List()

	// Reconcile project statuses with actual Docker state
	for _, project := range projects {
		if h.dockerManager != nil {
			reconciled := h.dockerManager.ReconcileStatus(r.Context(), project)
			if reconciled != project.Status {
				project.Status = reconciled
				if err := h.store.Update(project); err != nil {
					log.Printf("Warning: Failed to reconcile status for project %s: %v", project.ID, err)
				}
			}
		}
	}

	component := templates.Index(projects)
	component.Render(r.Context(), w)
}

// handleProjects serves the projects page
func (h *Handler) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects := h.store.List()
	component := templates.ProjectsList(projects)
	component.Render(r.Context(), w)
}

// handleAPIProjects handles listing and creating projects
func (h *Handler) handleAPIProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		projects := h.store.List()

		// Reconcile statuses with actual Docker state
		for _, project := range projects {
			if h.dockerManager != nil {
				reconciled := h.dockerManager.ReconcileStatus(r.Context(), project)
				if reconciled != project.Status {
					project.Status = reconciled
					if err := h.store.Update(project); err != nil {
						log.Printf("Warning: Failed to reconcile status for project %s: %v", project.ID, err)
					}
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(projects)

	case http.MethodPost:
		var project store.Project
		if err := json.NewDecoder(r.Body).Decode(&project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Validate uniqueness
		if h.store.NameExists(project.Name, "") {
			http.Error(w, "A project with this name already exists", http.StatusConflict)
			return
		}
		if h.store.PortExists(project.Port, "") {
			http.Error(w, "A project with this port already exists", http.StatusConflict)
			return
		}

		project.ID = uuid.New().String()
		project.Status = "creating"

		if err := h.store.Create(&project); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		h.events.Publish(events.Event{
			Type:      events.ProjectCreated,
			ProjectID: project.ID,
			Data:      project,
		})

		// Return immediately so the UI can show the card
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(project)

		// Start containers asynchronously
		go h.createProjectContainers(project.ID)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAPIProject handles operations on a specific project
func (h *Handler) handleAPIProject(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}
	id := parts[2]

	switch r.Method {
	case http.MethodGet:
		project, ok := h.store.Get(id)
		if !ok {
			http.Error(w, "Project not found", http.StatusNotFound)
			return
		}

		// Reconcile status with actual Docker state
		if h.dockerManager != nil {
			reconciled := h.dockerManager.ReconcileStatus(r.Context(), project)
			if reconciled != project.Status {
				project.Status = reconciled
				if err := h.store.Update(project); err != nil {
					log.Printf("Warning: Failed to reconcile status for project %s: %v", project.ID, err)
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(project)

	case http.MethodPut:
		var project store.Project
		if err := json.NewDecoder(r.Body).Decode(&project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		project.ID = id
		if err := h.store.Update(&project); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(project)

	case http.MethodDelete:
		project, ok := h.store.Get(id)
		if !ok {
			http.Error(w, "Project not found", http.StatusNotFound)
			return
		}

		// Remove containers if they exist
		if h.dockerManager != nil {
			// Broadcast pending state so all browsers show a spinner
			h.events.Publish(events.Event{
				Type:      events.ProjectActionPending,
				ProjectID: id,
				Data:      "deleting",
			})

			if err := h.dockerManager.RemoveProject(r.Context(), project); err != nil {
				log.Printf("Warning: Failed to remove containers: %v", err)
			}
		}

		if err := h.store.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		h.events.Publish(events.Event{
			Type:      events.ProjectDeleted,
			ProjectID: id,
		})

		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// createProjectContainers pulls images and creates containers for a newly created project.
// Runs asynchronously after the create HTTP response has been sent.
func (h *Handler) createProjectContainers(projectID string) {
	project, ok := h.store.Get(projectID)
	if !ok {
		log.Printf("Warning: project %s not found for container creation", projectID)
		return
	}

	h.events.Publish(events.Event{
		Type:      events.ProjectActionPending,
		ProjectID: project.ID,
		Data:      "creating",
	})

	if h.dockerManager == nil {
		log.Printf("Warning: Docker manager not available, skipping container creation for %s", projectID)
		project.Status = "error"
		_ = h.store.Update(project)
		h.events.Publish(events.Event{
			Type:      events.ProjectStatusChanged,
			ProjectID: project.ID,
			Data:      project,
		})
		return
	}

	if err := h.dockerManager.CreateProject(context.Background(), project); err != nil {
		log.Printf("Warning: Failed to create containers for project %s: %v", projectID, err)
		project.Status = "error"
		_ = h.store.Update(project)
		h.events.Publish(events.Event{
			Type:      events.ProjectStatusChanged,
			ProjectID: project.ID,
			Data:      project,
		})
		return
	}

	project.Status = "stopped"
	if err := h.store.Update(project); err != nil {
		log.Printf("Warning: Failed to update project status: %v", err)
	}

	h.events.Publish(events.Event{
		Type:      events.ProjectStatusChanged,
		ProjectID: project.ID,
		Data:      project,
	})
}

// handleStartProject starts a project's containers
func (h *Handler) handleStartProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract ID from path
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}
	id := parts[2]

	project, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	if h.dockerManager == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}

	// Reconcile with actual Docker state before acting
	actual := h.dockerManager.ReconcileStatus(r.Context(), project)
	if actual != project.Status {
		project.Status = actual
		_ = h.store.Update(project)
	}
	if actual == "running" {
		// Already in desired state — broadcast to heal stale clients and return success
		log.Printf("Project %s is already running, treating as success", project.ID)
		h.events.Publish(events.Event{
			Type:      events.ProjectStatusChanged,
			ProjectID: project.ID,
			Data:      project,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(project)
		return
	}

	// Broadcast pending state so all browsers show a spinner
	h.events.Publish(events.Event{
		Type:      events.ProjectActionPending,
		ProjectID: project.ID,
		Data:      "starting",
	})

	if err := h.dockerManager.StartProject(r.Context(), project); err != nil {
		http.Error(w, fmt.Sprintf("Failed to start project: %v", err), http.StatusInternalServerError)
		return
	}

	project.Status = "running"
	if err := h.store.Update(project); err != nil {
		log.Printf("Warning: Failed to update project status: %v", err)
	}

	h.events.Publish(events.Event{
		Type:      events.ProjectStatusChanged,
		ProjectID: project.ID,
		Data:      project,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(project)
}

// handleStopProject stops a project's containers
func (h *Handler) handleStopProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract ID from path
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}
	id := parts[2]

	project, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	if h.dockerManager == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}

	// Reconcile with actual Docker state before acting
	actual := h.dockerManager.ReconcileStatus(r.Context(), project)
	if actual != project.Status {
		project.Status = actual
		_ = h.store.Update(project)
	}
	if actual == "stopped" {
		// Already in desired state — broadcast to heal stale clients and return success
		log.Printf("Project %s is already stopped, treating as success", project.ID)
		h.events.Publish(events.Event{
			Type:      events.ProjectStatusChanged,
			ProjectID: project.ID,
			Data:      project,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(project)
		return
	}

	// Broadcast pending state so all browsers show a spinner
	h.events.Publish(events.Event{
		Type:      events.ProjectActionPending,
		ProjectID: project.ID,
		Data:      "stopping",
	})

	if err := h.dockerManager.StopProject(r.Context(), project); err != nil {
		http.Error(w, fmt.Sprintf("Failed to stop project: %v", err), http.StatusInternalServerError)
		return
	}

	project.Status = "stopped"
	if err := h.store.Update(project); err != nil {
		log.Printf("Warning: Failed to update project status: %v", err)
	}

	h.events.Publish(events.Event{
		Type:      events.ProjectStatusChanged,
		ProjectID: project.ID,
		Data:      project,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(project)
}

// handleProjectLogs streams logs via SSE
func (h *Handler) handleProjectLogs(w http.ResponseWriter, r *http.Request) {
	// Extract ID and container type from query
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}
	id := parts[2]

	containerType := r.URL.Query().Get("container")
	if containerType == "" {
		containerType = "odoo"
	}

	if h.dockerManager == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Disable the server's WriteTimeout for this long-lived connection
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	// Get logs stream
	logs, err := h.dockerManager.GetLogs(r.Context(), id, containerType)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
		return
	}
	defer logs.Close()

	// Stream logs as SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	buf := make([]byte, 1024)
	for {
		select {
		case <-r.Context().Done():
			return
		default:
			n, err := logs.Read(buf)
			if err != nil {
				if err != io.EOF {
					fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
					flusher.Flush()
				}
				return
			}

			if n > 0 {
				// Clean up Docker log formatting (remove first 8 bytes header)
				data := buf[:n]
				if len(data) > 8 {
					data = data[8:]
				}

				fmt.Fprintf(w, "data: %s\n\n", strings.TrimSpace(string(data)))
				flusher.Flush()
			}

			time.Sleep(100 * time.Millisecond)
		}
	}
}

// handleSSE streams real-time project events to all connected clients
func (h *Handler) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Disable the server's WriteTimeout for this long-lived connection
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := h.events.Subscribe()
	defer h.events.Unsubscribe(ch)

	// Send initial keepalive so the client knows the connection is open
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Keepalive ticker prevents idle-timeout disconnections
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			// SSE comment line — keeps the connection alive without triggering client events
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
			flusher.Flush()
		}
	}
}
