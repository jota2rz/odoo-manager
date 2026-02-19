package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/google/uuid"
	"github.com/jota2rz/odoo-manager/internal/audit"
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
	version       string
	audit         *audit.Logger

	backupMu       sync.Mutex
	backupsRunning map[string]bool // projectID -> true while a backup is in progress

	dockerMu sync.RWMutex
	dockerUp bool // last known Docker daemon reachability
}

// NewHandler creates a new HTTP handler
func NewHandler(projectStore *store.ProjectStore, staticFS http.Handler, eventHub *events.Hub, version string, auditLogger *audit.Logger) *Handler {
	dockerManager, err := docker.NewManager()
	if err != nil {
		log.Printf("Warning: Failed to create Docker manager: %v", err)
	}

	dockerUp := false
	if dockerManager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if dockerManager.Ping(ctx) == nil {
			dockerUp = true
		}
	}

	return &Handler{
		store:          projectStore,
		dockerManager:  dockerManager,
		staticFS:       staticFS,
		events:         eventHub,
		version:        version,
		audit:          auditLogger,
		backupsRunning: make(map[string]bool),
		dockerUp:       dockerUp,
	}
}

// IsDockerUp returns the last known Docker daemon reachability.
func (h *Handler) IsDockerUp() bool {
	h.dockerMu.RLock()
	defer h.dockerMu.RUnlock()
	return h.dockerUp
}

// StartDockerHealthCheck runs a background loop that periodically pings the
// Docker daemon. On status changes it broadcasts a docker_status SSE event.
// When Docker was unavailable at startup it will attempt to create a new
// Manager once the daemon becomes reachable.
func (h *Handler) StartDockerHealthCheck(ctx context.Context) {
	const (
		intervalUp   = 10 * time.Second // poll interval when Docker is up
		intervalDown = 5 * time.Second  // poll interval when Docker is down
		pingTimeout  = 3 * time.Second
	)

	go func() {
		for {
			// Determine sleep duration based on current state
			h.dockerMu.RLock()
			wasUp := h.dockerUp
			h.dockerMu.RUnlock()

			interval := intervalUp
			if !wasUp {
				interval = intervalDown
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}

			// Try to reach Docker
			isUp := false

			h.dockerMu.RLock()
			mgr := h.dockerManager
			h.dockerMu.RUnlock()

			if mgr == nil {
				// Docker client was never created — try now
				newMgr, err := docker.NewManager()
				if err == nil {
					pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
					err = newMgr.Ping(pingCtx)
					cancel()
					if err == nil {
						h.dockerMu.Lock()
						h.dockerManager = newMgr
						h.dockerMu.Unlock()
						isUp = true
					}
				}
			} else {
				pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
				err := mgr.Ping(pingCtx)
				cancel()
				isUp = err == nil
			}

			// Broadcast only on change
			h.dockerMu.Lock()
			changed := h.dockerUp != isUp
			h.dockerUp = isUp
			h.dockerMu.Unlock()

			if changed {
				status := "down"
				if isUp {
					status = "up"
					log.Println("Docker daemon is reachable again")
				} else {
					log.Println("Docker daemon is unreachable")
				}
				h.events.Publish(events.Event{
					Type: events.DockerStatus,
					Data: status,
				})
			}
		}
	}()
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
	mux.HandleFunc("/audit", h.handleAuditPage)

	// API endpoints
	mux.HandleFunc("/api/projects", h.withAudit(h.handleAPIProjects))
	mux.HandleFunc("/api/projects/", h.withAudit(h.handleAPIProject))
	mux.HandleFunc("/api/projects/{id}/start", h.withAudit(h.handleStartProject))
	mux.HandleFunc("/api/projects/{id}/stop", h.withAudit(h.handleStopProject))
	mux.HandleFunc("/api/projects/{id}/logs", h.withAudit(h.handleProjectLogs))
	mux.HandleFunc("/api/projects/{id}/databases", h.withAudit(h.handleListDatabases))
	mux.HandleFunc("/api/projects/{id}/backup", h.withAudit(h.handleBackupProject))
	mux.HandleFunc("/api/backup/download/", h.withAudit(h.handleBackupDownload))

	// Audit endpoints
	mux.HandleFunc("/api/audit/logs", h.handleAuditLogs)
	mux.HandleFunc("/api/audit/stream", h.handleAuditStream)

	// SSE event stream
	mux.HandleFunc("/api/events", h.handleSSE)
}

// withAudit wraps an HTTP handler to log each request to the audit log.
func (h *Handler) withAudit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.audit != nil {
			h.audit.Log(r, fmt.Sprintf("%s %s", r.Method, r.URL.Path))
		}
		next(w, r)
	}
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

	// SPA navigation: return only the inner content
	if r.Header.Get("X-Spa") == "1" {
		templates.DashboardContent(projects).Render(r.Context(), w)
		return
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

		// Broadcast pending state so all browsers show a spinner
		h.events.Publish(events.Event{
			Type:      events.ProjectActionPending,
			ProjectID: id,
			Data:      "deleting",
		})

		// Return immediately — Docker stop+remove may exceed the server WriteTimeout.
		// The actual work happens in a goroutine; SSE events update all clients.
		w.WriteHeader(http.StatusAccepted)

		go h.deleteProject(project)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// deleteProject removes Docker containers and deletes a project from the store.
// Runs asynchronously after the delete HTTP response has been sent.
func (h *Handler) deleteProject(project *store.Project) {
	if h.dockerManager != nil {
		if err := h.dockerManager.RemoveProject(context.Background(), project); err != nil {
			log.Printf("Warning: Failed to remove containers for project %s: %v", project.ID, err)
		}
	}

	if err := h.store.Delete(project.ID); err != nil {
		log.Printf("Warning: Failed to delete project %s from store: %v", project.ID, err)
		return
	}

	h.events.Publish(events.Event{
		Type:      events.ProjectDeleted,
		ProjectID: project.ID,
	})
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

// startProjectContainers starts Docker containers for a project asynchronously.
func (h *Handler) startProjectContainers(projectID string) {
	project, ok := h.store.Get(projectID)
	if !ok {
		log.Printf("Warning: project %s not found for start", projectID)
		return
	}

	if err := h.dockerManager.StartProject(context.Background(), project); err != nil {
		log.Printf("Warning: Failed to start containers for project %s: %v", projectID, err)
		project.Status = "error"
		_ = h.store.Update(project)
		h.events.Publish(events.Event{
			Type:      events.ProjectStatusChanged,
			ProjectID: project.ID,
			Data:      project,
		})
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
}

// stopProjectContainers stops Docker containers for a project asynchronously.
func (h *Handler) stopProjectContainers(projectID string) {
	project, ok := h.store.Get(projectID)
	if !ok {
		log.Printf("Warning: project %s not found for stop", projectID)
		return
	}

	if err := h.dockerManager.StopProject(context.Background(), project); err != nil {
		log.Printf("Warning: Failed to stop containers for project %s: %v", projectID, err)
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

	// Return immediately — Docker start may exceed the server WriteTimeout.
	// The actual work happens in a goroutine; SSE events update all clients.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(project)

	go h.startProjectContainers(project.ID)
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

	// Return immediately — Docker stop may exceed the server WriteTimeout.
	// The actual work happens in a goroutine; SSE events update all clients.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(project)

	go h.stopProjectContainers(project.ID)
}

// handleListDatabases returns JSON array of database names for a project.
func (h *Handler) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	project, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	h.dockerMu.RLock()
	dm := h.dockerManager
	h.dockerMu.RUnlock()
	if dm == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}

	actual := dm.ReconcileStatus(r.Context(), project)
	if actual != "running" {
		http.Error(w, "Project must be running to list databases", http.StatusConflict)
		return
	}

	databases, err := dm.ListDatabases(r.Context(), id)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list databases: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(databases)
}

// handleBackupProject streams backup progress via SSE.
// The exec command runs "odoo db dump" inside the container, redirecting the
// zip to a file while streaming console output (stderr) back to the browser.
// When the command finishes the backup file is copied out of the container
// and a download URL is sent as the final SSE event.
func (h *Handler) handleBackupProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	project, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	h.dockerMu.RLock()
	dm := h.dockerManager
	h.dockerMu.RUnlock()
	if dm == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}

	// Project must be running
	actual := dm.ReconcileStatus(r.Context(), project)
	if actual != "running" {
		http.Error(w, "Project must be running to create a backup", http.StatusConflict)
		return
	}

	// Only one backup at a time per project
	h.backupMu.Lock()
	if h.backupsRunning[id] {
		h.backupMu.Unlock()
		http.Error(w, "A backup is already in progress for this project", http.StatusConflict)
		return
	}
	h.backupsRunning[id] = true
	h.backupMu.Unlock()

	// Broadcast backup-pending so all browsers disable the button
	h.events.Publish(events.Event{
		Type:      events.ProjectBackupPending,
		ProjectID: id,
	})

	// Ensure we always clean up and broadcast backup-done
	defer func() {
		h.backupMu.Lock()
		delete(h.backupsRunning, id)
		h.backupMu.Unlock()
		h.events.Publish(events.Event{
			Type:      events.ProjectBackupDone,
			ProjectID: id,
		})
	}()

	// ── SSE setup ─────────────────────────────────────────────────────
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	sendLog := func(line string) {
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()
	}
	sendEvent := func(event, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}

	dbName := r.URL.Query().Get("db")
	if dbName == "" {
		dbName = "postgres"
	}

	sendLog(fmt.Sprintf("Starting backup of database %q for project %s…", dbName, project.Name))

	logReader, execID, cleanup, err := dm.BackupDatabase(r.Context(), id, dbName)
	if err != nil {
		sendEvent("error", fmt.Sprintf("Failed to start backup: %v", err))
		return
	}
	defer cleanup()

	// Stream exec output line-by-line
	buf := make([]byte, 4096)
	for {
		n, readErr := logReader.Read(buf)
		if n > 0 {
			lines := strings.Split(strings.TrimRight(string(buf[:n]), "\r\n"), "\n")
			for _, line := range lines {
				line = strings.TrimRight(line, "\r")
				if line != "" {
					sendLog(line)
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	// Wait for the exec to finish and check exit code
	exitCode, err := dm.WaitExec(r.Context(), execID)
	if err != nil {
		sendEvent("error", fmt.Sprintf("Failed waiting for backup process: %v", err))
		return
	}
	if exitCode != 0 {
		sendEvent("error", fmt.Sprintf("Backup command exited with code %d", exitCode))
		return
	}

	// Copy the backup file out of the container
	sendLog("Backup command completed, extracting file…")
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("%s-%s-%s.zip", project.Name, dbName, timestamp)
	destPath := fmt.Sprintf("data/backups/%s", filename)

	if err := dm.CopyBackupFromContainer(r.Context(), id, destPath); err != nil {
		sendEvent("error", fmt.Sprintf("Failed to extract backup: %v", err))
		return
	}

	sendLog("Backup ready for download.")
	sendEvent("complete", fmt.Sprintf("/api/backup/download/%s", filename))
}

// handleBackupDownload serves a previously-created backup file and removes it
// from disk once fully sent.
func (h *Handler) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	// Path: /api/backup/download/{filename}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	filename := parts[3]

	filePath := fmt.Sprintf("data/backups/%s", filename)
	info, err := os.Stat(filePath)
	if err != nil {
		http.Error(w, "Backup file not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))

	f, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "Failed to open backup file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	io.Copy(w, f)

	// Clean up the file after serving
	go os.Remove(filePath)
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
	logs, hasTTY, err := h.dockerManager.GetLogs(r.Context(), id, containerType)
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

	// Build a reader: if the container has a TTY the stream is raw (ANSI
	// colours are preserved as-is). Otherwise Docker multiplexes
	// stdout/stderr with 8-byte headers; use stdcopy to demux.
	var reader io.Reader
	if hasTTY {
		reader = logs
	} else {
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			_, _ = stdcopy.StdCopy(pw, pw, logs)
		}()
		reader = pr
	}

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		line := scanner.Text()
		if line != "" {
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
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

	// Send the application version so the client can detect updates on reconnect
	fmt.Fprintf(w, "event: version\ndata: %s\n\n", h.version)
	flusher.Flush()

	// Send the current Docker daemon status so the client can show/hide the overlay
	dockerStatus := "down"
	if h.IsDockerUp() {
		dockerStatus = "up"
	}
	fmt.Fprintf(w, "event: docker_status\ndata: %s\n\n", dockerStatus)
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

// ── Audit endpoints ───────────────────────────────────────────────────

// handleAuditPage serves the Audit log viewer page.
func (h *Handler) handleAuditPage(w http.ResponseWriter, r *http.Request) {
	// SPA navigation: return only the inner content
	if r.Header.Get("X-Spa") == "1" {
		templates.AuditContent().Render(r.Context(), w)
		return
	}

	component := templates.Audit()
	component.Render(r.Context(), w)
}

// handleAuditLogs returns the last N lines of the audit log as a JSON array
// of strings. Supports pagination via ?before=<offset>&limit=<n>.
func (h *Handler) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	if h.audit == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"lines": []string{}, "offset": 0})
		return
	}

	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	beforeStr := r.URL.Query().Get("before")

	var lines []string
	var offset int
	var err error

	if beforeStr == "" {
		// Initial load — last N lines
		lines, err = h.audit.Tail(limit)
		offset = len(lines)
	} else {
		before, _ := strconv.Atoi(beforeStr)
		if before <= 0 {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"lines": []string{}, "offset": 0})
			return
		}
		lines, offset, err = h.audit.TailBefore(limit, before)
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read audit log: %v", err), http.StatusInternalServerError)
		return
	}
	if lines == nil {
		lines = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"lines":  lines,
		"offset": offset,
	})
}

// handleAuditStream is an SSE endpoint that streams new audit entries in
// real time to the Audit page.
func (h *Handler) handleAuditStream(w http.ResponseWriter, r *http.Request) {
	if h.audit == nil {
		http.Error(w, "Audit logger not available", http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := h.audit.Subscribe()
	defer h.audit.Unsubscribe(ch)

	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case entry, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
