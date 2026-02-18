package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jota2rz/odoo-manager/internal/docker"
	"github.com/jota2rz/odoo-manager/internal/store"
	"github.com/jota2rz/odoo-manager/templates"
)

// Handler manages HTTP requests
type Handler struct {
	store         *store.ProjectStore
	dockerManager *docker.Manager
	staticFS      http.Handler
}

// NewHandler creates a new HTTP handler
func NewHandler(projectStore *store.ProjectStore, staticFS http.Handler) *Handler {
	dockerManager, err := docker.NewManager()
	if err != nil {
		log.Printf("Warning: Failed to create Docker manager: %v", err)
	}

	return &Handler{
		store:         projectStore,
		dockerManager: dockerManager,
		staticFS:      staticFS,
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
	mux.HandleFunc("/api/projects/{id}/docker-compose", h.handleDockerCompose)
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

		project.ID = uuid.New().String()
		project.Status = "stopped"

		if err := h.store.Create(&project); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(project)

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
			if err := h.dockerManager.RemoveProject(r.Context(), project); err != nil {
				log.Printf("Warning: Failed to remove containers: %v", err)
			}
		}

		if err := h.store.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
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

	if err := h.dockerManager.StartProject(r.Context(), project); err != nil {
		http.Error(w, fmt.Sprintf("Failed to start project: %v", err), http.StatusInternalServerError)
		return
	}

	project.Status = "running"
	if err := h.store.Update(project); err != nil {
		log.Printf("Warning: Failed to update project status: %v", err)
	}

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

	if err := h.dockerManager.StopProject(r.Context(), project); err != nil {
		http.Error(w, fmt.Sprintf("Failed to stop project: %v", err), http.StatusInternalServerError)
		return
	}

	project.Status = "stopped"
	if err := h.store.Update(project); err != nil {
		log.Printf("Warning: Failed to update project status: %v", err)
	}

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

// handleDockerCompose generates docker-compose.yml for a project
func (h *Handler) handleDockerCompose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
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

	compose := docker.GenerateDockerCompose(project)

	w.Header().Set("Content-Type", "text/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=docker-compose-%s.yml", project.Name))
	w.Write([]byte(compose))
}
