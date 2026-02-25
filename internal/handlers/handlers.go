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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/google/uuid"
	"github.com/jota2rz/odoo-manager/internal/audit"
	"github.com/jota2rz/odoo-manager/internal/docker"
	"github.com/jota2rz/odoo-manager/internal/events"
	"github.com/jota2rz/odoo-manager/internal/gitops"
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

	gitAvailable bool // whether git CLI was found at startup
}

// NewHandler creates a new HTTP handler
func NewHandler(projectStore *store.ProjectStore, staticFS http.Handler, eventHub *events.Hub, version string, auditLogger *audit.Logger, gitAvailable bool) *Handler {
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
		gitAvailable:   gitAvailable,
	}
}

// knownProjectIDs returns a set of project IDs currently in the database.
// Used by maintenance cleanup functions to distinguish owned vs orphaned
// Docker resources.
func (h *Handler) knownProjectIDs() map[string]bool {
	projects := h.store.List()
	ids := make(map[string]bool, len(projects))
	for _, p := range projects {
		ids[p.ID] = true
	}
	return ids
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
	mux.HandleFunc("/maintenance", h.handleMaintenancePage)
	mux.HandleFunc("/configuration", h.handleConfigurationPage)

	// API endpoints
	mux.HandleFunc("/api/projects", h.withAudit(h.handleAPIProjects))
	mux.HandleFunc("/api/projects/", h.withAudit(h.handleAPIProject))
	mux.HandleFunc("/api/projects/{id}/start", h.withAudit(h.handleStartProject))
	mux.HandleFunc("/api/projects/{id}/stop", h.withAudit(h.handleStopProject))
	mux.HandleFunc("/api/projects/{id}/logs", h.withAudit(h.handleProjectLogs))
	mux.HandleFunc("/api/projects/{id}/databases", h.withAudit(h.handleListDatabases))
	mux.HandleFunc("/api/projects/{id}/backup", h.withAudit(h.handleBackupProject))
	mux.HandleFunc("/api/projects/{id}/config", h.withAudit(h.handleProjectConfig))
	mux.HandleFunc("/api/projects/{id}/repo", h.withAudit(h.handleProjectRepo))
	mux.HandleFunc("/api/repo/branches", h.handleRepoBranches)
	mux.HandleFunc("/api/enterprise/check-access", h.handleEnterpriseCheckAccess)
	mux.HandleFunc("/api/design-themes/check-access", h.handleDesignThemesCheckAccess)
	mux.HandleFunc("/api/backup/download/", h.withAudit(h.handleBackupDownload))
	mux.HandleFunc("/api/projects/{id}/update-odoo", h.withAudit(h.handleUpdateOdoo))
	mux.HandleFunc("/api/projects/{id}/update-repo", h.withAudit(h.handleUpdateRepos))
	mux.HandleFunc("/api/projects/{id}/restart-odoo", h.withAudit(h.handleRestartOdoo))

	// Settings endpoints
	mux.HandleFunc("/api/settings", h.withAudit(h.handleSettings))
	mux.HandleFunc("/api/settings/validate-token", h.withAudit(h.handleValidateToken))

	// Maintenance endpoints
	mux.HandleFunc("/api/maintenance/preview-containers", h.handlePreviewOrphaned("containers"))
	mux.HandleFunc("/api/maintenance/preview-volumes", h.handlePreviewOrphaned("volumes"))
	mux.HandleFunc("/api/maintenance/preview-images", h.handlePreviewOrphaned("images"))
	mux.HandleFunc("/api/maintenance/clean-containers", h.withAudit(h.handleCleanContainers))
	mux.HandleFunc("/api/maintenance/clean-volumes", h.withAudit(h.handleCleanVolumes))
	mux.HandleFunc("/api/maintenance/clean-images", h.withAudit(h.handleCleanImages))

	// Audit endpoints
	mux.HandleFunc("/api/audit/logs", h.handleAuditLogs)
	mux.HandleFunc("/api/audit/stream", h.handleAuditStream)

	// SSE event stream
	mux.HandleFunc("/api/events", h.handleSSE)
}

// withAudit wraps an HTTP handler to log each request to the audit log.
// When the path targets a specific project it includes the project name for
// human readability, e.g. "POST /api/projects/abc/start (My Project)".
func (h *Handler) withAudit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.audit != nil {
			msg := fmt.Sprintf("%s %s", r.Method, r.URL.Path)

			// Try to resolve project name from URL path
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) >= 3 && parts[0] == "api" && parts[1] == "projects" {
				if project, ok := h.store.Get(parts[2]); ok {
					msg += fmt.Sprintf(" (%s)", project.Name)
				}
			}

			h.audit.Log(r, msg)
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
		templates.DashboardContent(projects, h.gitAvailable).Render(r.Context(), w)
		return
	}

	component := templates.Index(projects, h.gitAvailable)
	component.Render(r.Context(), w)
}

// handleProjects serves the projects page
func (h *Handler) handleProjects(w http.ResponseWriter, r *http.Request) {
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

		// Validate git repo URL if provided
		if project.GitRepoURL != "" {
			if err := gitops.ValidateRepoURL(project.GitRepoURL); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			token := h.store.GetSetting("github_pat")
			if err := gitops.CheckRepoAccessible(r.Context(), project.GitRepoURL, token); err != nil {
				http.Error(w, "Repository not accessible: "+err.Error(), http.StatusBadRequest)
				return
			}
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

	// Clean up cloned git repo if any
	if project.GitRepoURL != "" {
		if err := gitops.RemoveRepo(project.ID); err != nil {
			log.Printf("Warning: Failed to remove git repo for project %s: %v", project.ID, err)
		}
	}
	// Clean up enterprise repo if any
	if project.EnterpriseEnabled {
		if err := gitops.RemoveEnterpriseRepo(project.ID); err != nil {
			log.Printf("Warning: Failed to remove enterprise repo for project %s: %v", project.ID, err)
		}
	}
	// Clean up design-themes repo if any
	if project.DesignThemesEnabled {
		if err := gitops.RemoveDesignThemesRepo(project.ID); err != nil {
			log.Printf("Warning: Failed to remove design-themes repo for project %s: %v", project.ID, err)
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

// addonsHostDir resolves the absolute host path for a project's addons
// bind mount. If the project has a git repo configured, it clones/pulls it
// and returns the local directory. Otherwise returns empty string.
func (h *Handler) addonsHostDir(ctx context.Context, projectID, repoURL, branch string) string {
	if repoURL == "" {
		return ""
	}
	token := h.store.GetSetting("github_pat")
	dir, err := gitops.CloneOrPull(ctx, projectID, repoURL, token, branch)
	if err != nil {
		log.Printf("Warning: git clone/pull failed for project %s: %v", projectID, err)
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// enterpriseHostDir resolves the absolute host path for the Odoo Enterprise
// addons bind mount. If enterprise is enabled, it clones/pulls the enterprise
// repo using the project's Odoo version as the branch. Returns empty string
// if enterprise is not enabled.
func (h *Handler) enterpriseHostDir(ctx context.Context, projectID, odooVersion string, enabled bool) string {
	if !enabled {
		return ""
	}
	token := h.store.GetSetting("github_pat")
	dir, err := gitops.CloneOrPullEnterprise(ctx, projectID, token, odooVersion)
	if err != nil {
		log.Printf("Warning: enterprise clone/pull failed for project %s: %v", projectID, err)
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// designThemesHostDir resolves the absolute host path for the Odoo Design
// Themes bind mount. If design themes is enabled, it clones/pulls the
// design-themes repo using the project's Odoo version as the branch.
// Returns empty string if design themes is not enabled.
func (h *Handler) designThemesHostDir(ctx context.Context, projectID, odooVersion string, enabled bool) string {
	if !enabled {
		return ""
	}
	token := h.store.GetSetting("github_pat")
	dir, err := gitops.CloneOrPullDesignThemes(ctx, projectID, token, odooVersion)
	if err != nil {
		log.Printf("Warning: design-themes clone/pull failed for project %s: %v", projectID, err)
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// createProjectContainers pulls images and creates containers for a newly created project.
// Runs asynchronously after the create HTTP response has been sent.
func (h *Handler) createProjectContainers(projectID string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in createProjectContainers for %s: %v", projectID, r)
			if project, ok := h.store.Get(projectID); ok {
				project.Status = "error"
				_ = h.store.Update(project)
				h.events.Publish(events.Event{
					Type:      events.ProjectStatusChanged,
					ProjectID: project.ID,
					Data:      project,
				})
			}
		}
	}()

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

	// Clone git repos with a timeout so we don't hang forever
	gitCtx, gitCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer gitCancel()

	log.Printf("Project %s: resolving addons host directories...", projectID)

	addonsDir := h.addonsHostDir(gitCtx, project.ID, project.GitRepoURL, project.GitRepoBranch)
	if project.GitRepoURL != "" {
		log.Printf("Project %s: addons repo cloned (dir=%q)", projectID, addonsDir)
	}

	entDir := h.enterpriseHostDir(gitCtx, project.ID, project.OdooVersion, project.EnterpriseEnabled)
	if project.EnterpriseEnabled {
		log.Printf("Project %s: enterprise repo cloned (dir=%q)", projectID, entDir)
	}

	dtDir := h.designThemesHostDir(gitCtx, project.ID, project.OdooVersion, project.DesignThemesEnabled)
	if project.DesignThemesEnabled {
		log.Printf("Project %s: design-themes repo cloned (dir=%q)", projectID, dtDir)
	}

	log.Printf("Project %s: creating Docker containers...", projectID)

	if err := h.dockerManager.CreateProject(context.Background(), project, addonsDir, entDir, dtDir); err != nil {
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

	log.Printf("Project %s: containers created successfully", projectID)
	h.events.Publish(events.Event{
		Type:      events.ProjectStatusChanged,
		ProjectID: project.ID,
		Data:      project,
	})
}

// startProjectContainers starts Docker containers for a project asynchronously.
func (h *Handler) startProjectContainers(projectID string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in startProjectContainers for %s: %v", projectID, r)
			if project, ok := h.store.Get(projectID); ok {
				project.Status = "error"
				_ = h.store.Update(project)
				h.events.Publish(events.Event{
					Type:      events.ProjectStatusChanged,
					ProjectID: project.ID,
					Data:      project,
				})
			}
		}
	}()

	project, ok := h.store.Get(projectID)
	if !ok {
		log.Printf("Warning: project %s not found for start", projectID)
		return
	}

	// Clone/pull git repos with a timeout
	gitCtx, gitCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer gitCancel()

	addonsDir := h.addonsHostDir(gitCtx, project.ID, project.GitRepoURL, project.GitRepoBranch)
	entDir := h.enterpriseHostDir(gitCtx, project.ID, project.OdooVersion, project.EnterpriseEnabled)
	dtDir := h.designThemesHostDir(gitCtx, project.ID, project.OdooVersion, project.DesignThemesEnabled)

	if err := h.dockerManager.StartProject(context.Background(), project, addonsDir, entDir, dtDir); err != nil {
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

// handleProjectConfig reads or writes odoo.conf for a project.
// GET  → returns { "content": "<odoo.conf text>" }
// PUT  → accepts { "content": "<new odoo.conf text>" } and writes it
func (h *Handler) handleProjectConfig(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}
	id := parts[2]

	if _, ok := h.store.Get(id); !ok {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	if h.dockerManager == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		content, err := h.dockerManager.ReadOdooConfig(r.Context(), id)
		if err != nil {
			http.Error(w, "Failed to read odoo.conf: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"content": content})

	case http.MethodPut:
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if err := h.dockerManager.WriteOdooConfig(r.Context(), id, body.Content); err != nil {
			http.Error(w, "Failed to write odoo.conf: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
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

// handleMaintenancePage serves the maintenance page.
func (h *Handler) handleMaintenancePage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Spa") == "1" {
		templates.MaintenanceContent().Render(r.Context(), w)
		return
	}
	component := templates.Maintenance()
	component.Render(r.Context(), w)
}

// handlePreviewOrphaned returns a JSON list of orphaned resource names without deleting.
func (h *Handler) handlePreviewOrphaned(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if h.dockerManager == nil {
			http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
			return
		}
		knownIDs := h.knownProjectIDs()
		var names []string
		var err error
		switch kind {
		case "containers":
			names, err = h.dockerManager.ListOrphanedContainers(r.Context(), knownIDs)
		case "volumes":
			names, err = h.dockerManager.ListOrphanedVolumes(r.Context(), knownIDs)
		case "images":
			names, err = h.dockerManager.ListOrphanedImages(r.Context(), knownIDs)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if names == nil {
			names = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"items": names})
	}
}

// handleCleanContainers removes all orphaned Docker containers.
func (h *Handler) handleCleanContainers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.dockerManager == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}
	result, err := h.dockerManager.CleanOrphanedContainers(r.Context(), h.knownProjectIDs())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleCleanVolumes removes all orphaned Docker volumes.
func (h *Handler) handleCleanVolumes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.dockerManager == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}
	result, err := h.dockerManager.CleanOrphanedVolumes(r.Context(), h.knownProjectIDs())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleCleanImages removes all orphaned Docker images.
func (h *Handler) handleCleanImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.dockerManager == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}
	result, err := h.dockerManager.CleanOrphanedImages(r.Context(), h.knownProjectIDs())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
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

// handleConfigurationPage renders the Configuration page.
func (h *Handler) handleConfigurationPage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Spa") == "1" {
		templates.ConfigurationContent().Render(r.Context(), w)
		return
	}
	templates.Configuration().Render(r.Context(), w)
}

// handleSettings handles GET/PUT for global settings (e.g. GitHub PAT).
// GET  → returns { "github_pat": "<masked or empty>" }
// PUT  → accepts { "github_pat": "<token>" } and stores it
func (h *Handler) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pat := h.store.GetSetting("github_pat")
		masked := ""
		if pat != "" {
			if len(pat) > 8 {
				masked = pat[:4] + "..." + pat[len(pat)-4:]
			} else {
				masked = "****"
			}
		}
		patValid := h.store.GetSetting("github_pat_valid")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"github_pat": masked, "github_pat_valid": patValid})

	case http.MethodPut:
		var body struct {
			GitHubPAT string `json:"github_pat"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if err := h.store.SetSetting("github_pat", body.GitHubPAT); err != nil {
			http.Error(w, "Failed to save setting: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Re-validate the new token and store result
		if body.GitHubPAT != "" {
			if err := gitops.ValidateToken(r.Context(), body.GitHubPAT); err != nil {
				_ = h.store.SetSetting("github_pat_valid", "false")
			} else {
				_ = h.store.SetSetting("github_pat_valid", "true")
			}
		} else {
			_ = h.store.SetSetting("github_pat_valid", "")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleValidateToken validates the current stored GitHub PAT token.
// POST → tests the token against GitHub API and returns result.
func (h *Handler) handleValidateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Accept optional token in body; if empty, use stored token
	var body struct {
		Token string `json:"token"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	token := body.Token
	if token == "" {
		token = h.store.GetSetting("github_pat")
	}
	if token == "" {
		http.Error(w, "No token provided", http.StatusBadRequest)
		return
	}

	if err := gitops.ValidateToken(r.Context(), token); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "valid"})
}

// handleProjectRepo handles PUT for a project's git repo URL and enterprise setting.
// PUT → validates URL format, checks accessibility, and saves.
func (h *Handler) handleProjectRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.PathValue("id")
	project, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	var body struct {
		GitRepoURL          string `json:"git_repo_url"`
		GitRepoBranch       string `json:"git_repo_branch"`
		EnterpriseEnabled   *bool  `json:"enterprise_enabled"`    // pointer to detect if field was sent
		DesignThemesEnabled *bool  `json:"design_themes_enabled"` // pointer to detect if field was sent
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	previousURL := project.GitRepoURL
	previousBranch := project.GitRepoBranch
	previousEnterprise := project.EnterpriseEnabled
	previousDesignThemes := project.DesignThemesEnabled

	// Update enterprise flag if provided
	if body.EnterpriseEnabled != nil {
		project.EnterpriseEnabled = *body.EnterpriseEnabled
	}
	// Update design themes flag if provided
	if body.DesignThemesEnabled != nil {
		project.DesignThemesEnabled = *body.DesignThemesEnabled
	}

	// Allow clearing the repo URL
	if body.GitRepoURL == "" {
		// Remove existing clone if any
		if previousURL != "" {
			gitops.RemoveRepo(project.ID)
		}
		project.GitRepoURL = ""
		project.GitRepoBranch = ""
		if err := h.store.Update(project); err != nil {
			http.Error(w, "Failed to update project: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Recreate container without the addons bind mount (enterprise/design-themes may still change)
		if (previousURL != "" || previousEnterprise != project.EnterpriseEnabled || previousDesignThemes != project.DesignThemesEnabled) && h.dockerManager != nil {
			entDir := h.enterpriseHostDir(r.Context(), project.ID, project.OdooVersion, project.EnterpriseEnabled)
			dtDir := h.designThemesHostDir(r.Context(), project.ID, project.OdooVersion, project.DesignThemesEnabled)
			if !project.EnterpriseEnabled && previousEnterprise {
				gitops.RemoveEnterpriseRepo(project.ID)
			}
			if !project.DesignThemesEnabled && previousDesignThemes {
				gitops.RemoveDesignThemesRepo(project.ID)
			}
			if err := h.dockerManager.RecreateOdooContainer(r.Context(), project, "", entDir, dtDir); err != nil {
				log.Printf("Warning: failed to recreate container for project %s after clearing repo: %v", project.ID, err)
			}
			// Update odoo.conf addons_path: remove /mnt/extra-addons since repo was cleared
			if previousURL != "" {
				if err := h.dockerManager.UpdateOdooConfigExtraAddons(r.Context(), project.ID, false); err != nil {
					log.Printf("Warning: failed to update odoo.conf extra-addons for project %s: %v", project.ID, err)
				}
			}
			// Update odoo.conf addons_path when enterprise flag changes
			if previousEnterprise != project.EnterpriseEnabled {
				if err := h.dockerManager.UpdateOdooConfigEnterprise(r.Context(), project.ID, project.EnterpriseEnabled); err != nil {
					log.Printf("Warning: failed to update odoo.conf addons_path for project %s: %v", project.ID, err)
				}
			}
			// Update odoo.conf addons_path when design themes flag changes
			if previousDesignThemes != project.DesignThemesEnabled {
				if err := h.dockerManager.UpdateOdooConfigDesignThemes(r.Context(), project.ID, project.DesignThemesEnabled); err != nil {
					log.Printf("Warning: failed to update odoo.conf design-themes for project %s: %v", project.ID, err)
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	// Validate URL format
	if err := gitops.ValidateRepoURL(body.GitRepoURL); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Check repo accessibility
	token := h.store.GetSetting("github_pat")
	if err := gitops.CheckRepoAccessible(r.Context(), body.GitRepoURL, token); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"error": "Repository not accessible: " + err.Error()})
		return
	}

	project.GitRepoURL = body.GitRepoURL
	project.GitRepoBranch = body.GitRepoBranch
	if err := h.store.Update(project); err != nil {
		http.Error(w, "Failed to update project: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// If the repo URL, branch, enterprise, or design themes flag changed, recreate the Odoo container
	needsRecreate := previousURL != body.GitRepoURL || previousBranch != body.GitRepoBranch || previousEnterprise != project.EnterpriseEnabled || previousDesignThemes != project.DesignThemesEnabled
	if needsRecreate && h.dockerManager != nil {
		// Remove old clone if URL changed so we get a fresh checkout
		if previousURL != body.GitRepoURL {
			gitops.RemoveRepo(project.ID)
		}
		// Handle enterprise repo changes
		if !project.EnterpriseEnabled && previousEnterprise {
			gitops.RemoveEnterpriseRepo(project.ID)
		}
		// Handle design-themes repo changes
		if !project.DesignThemesEnabled && previousDesignThemes {
			gitops.RemoveDesignThemesRepo(project.ID)
		}
		addonsDir := h.addonsHostDir(r.Context(), project.ID, project.GitRepoURL, project.GitRepoBranch)
		entDir := h.enterpriseHostDir(r.Context(), project.ID, project.OdooVersion, project.EnterpriseEnabled)
		dtDir := h.designThemesHostDir(r.Context(), project.ID, project.OdooVersion, project.DesignThemesEnabled)
		if err := h.dockerManager.RecreateOdooContainer(r.Context(), project, addonsDir, entDir, dtDir); err != nil {
			log.Printf("Warning: failed to recreate container for project %s: %v", project.ID, err)
		}
		// Update odoo.conf addons_path when repo URL is added or removed
		hasRepo := body.GitRepoURL != ""
		hadRepo := previousURL != ""
		if hasRepo != hadRepo {
			if err := h.dockerManager.UpdateOdooConfigExtraAddons(r.Context(), project.ID, hasRepo); err != nil {
				log.Printf("Warning: failed to update odoo.conf extra-addons for project %s: %v", project.ID, err)
			}
		}
		// Update odoo.conf addons_path when enterprise flag changes
		if previousEnterprise != project.EnterpriseEnabled {
			if err := h.dockerManager.UpdateOdooConfigEnterprise(r.Context(), project.ID, project.EnterpriseEnabled); err != nil {
				log.Printf("Warning: failed to update odoo.conf addons_path for project %s: %v", project.ID, err)
			}
		}
		// Update odoo.conf addons_path when design themes flag changes
		if previousDesignThemes != project.DesignThemesEnabled {
			if err := h.dockerManager.UpdateOdooConfigDesignThemes(r.Context(), project.ID, project.DesignThemesEnabled); err != nil {
				log.Printf("Warning: failed to update odoo.conf design-themes for project %s: %v", project.ID, err)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleRepoBranches returns the list of branches for a given repo URL.
// GET /api/repo/branches?url=https://...git
func (h *Handler) handleRepoBranches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	repoURL := r.URL.Query().Get("url")
	if repoURL == "" {
		http.Error(w, "Missing url parameter", http.StatusBadRequest)
		return
	}

	if err := gitops.ValidateRepoURL(repoURL); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	token := h.store.GetSetting("github_pat")
	branches, err := gitops.ListBranches(r.Context(), repoURL, token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to list branches: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(branches)
}

// handleEnterpriseCheckAccess checks whether the stored PAT token has access
// to the Odoo Enterprise repository.
// GET /api/enterprise/check-access → { "accessible": true/false, "error": "..." }
func (h *Handler) handleEnterpriseCheckAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := h.store.GetSetting("github_pat")
	if token == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"accessible": false,
			"error":      "No GitHub PAT token configured. Set one in Configuration.",
		})
		return
	}

	err := gitops.CheckEnterpriseAccess(r.Context(), token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"accessible": false,
			"error":      "Your PAT token does not have access to the Odoo Enterprise repository.",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"accessible": true,
	})
}

// handleDesignThemesCheckAccess checks whether the stored PAT token has access
// to the Odoo Design Themes repository.
// GET /api/design-themes/check-access → { "accessible": true/false, "error": "..." }
func (h *Handler) handleDesignThemesCheckAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := h.store.GetSetting("github_pat")
	if token == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"accessible": false,
			"error":      "No GitHub PAT token configured. Set one in Configuration.",
		})
		return
	}

	err := gitops.CheckDesignThemesAccess(r.Context(), token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"accessible": false,
			"error":      "Your PAT token does not have access to the Odoo Design Themes repository.",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"accessible": true,
	})
}

// handleUpdateOdoo pulls the latest Odoo image, recreates the Odoo container
// (preserving data volumes), and git-pulls all configured repos.
func (h *Handler) handleUpdateOdoo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.PathValue("id")
	project, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	if h.dockerManager == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}

	h.events.Publish(events.Event{Type: events.ProjectActionPending, ProjectID: id, Data: "updating"})

	go func() {
		defer func() {
			if rv := recover(); rv != nil {
				log.Printf("PANIC in handleUpdateOdoo for %s: %v", id, rv)
				project.Status = "error"
				_ = h.store.Update(project)
				h.events.Publish(events.Event{Type: events.ProjectStatusChanged, ProjectID: id, Data: project})
			}
		}()

		gitCtx, gitCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer gitCancel()

		addonsDir := h.addonsHostDir(gitCtx, project.ID, project.GitRepoURL, project.GitRepoBranch)
		entDir := h.enterpriseHostDir(gitCtx, project.ID, project.OdooVersion, project.EnterpriseEnabled)
		dtDir := h.designThemesHostDir(gitCtx, project.ID, project.OdooVersion, project.DesignThemesEnabled)

		log.Printf("Project %s: pulling latest Odoo image and recreating container...", id)
		if err := h.dockerManager.UpdateOdooContainer(context.Background(), project, addonsDir, entDir, dtDir); err != nil {
			log.Printf("Project %s: update failed: %v", id, err)
			project.Status = "error"
			_ = h.store.Update(project)
			h.events.Publish(events.Event{Type: events.ProjectStatusChanged, ProjectID: id, Data: project})
			return
		}

		status, _ := h.dockerManager.GetProjectStatus(context.Background(), project.ID)
		project.Status = status
		_ = h.store.Update(project)
		log.Printf("Project %s: Odoo update complete (status=%s)", id, status)
		h.events.Publish(events.Event{Type: events.ProjectStatusChanged, ProjectID: id, Data: project})
	}()

	w.WriteHeader(http.StatusAccepted)
}

// handleUpdateRepos git-pulls the project's addons, enterprise, and
// design-themes repos. If odoo.conf contains dev=all or dev=reload it
// skips the restart (Odoo auto-reloads), otherwise it restarts the Odoo
// container.
func (h *Handler) handleUpdateRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.PathValue("id")
	project, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	if project.GitRepoURL == "" {
		http.Error(w, "No repository configured", http.StatusBadRequest)
		return
	}

	if h.dockerManager == nil {
		http.Error(w, "Docker manager not available", http.StatusServiceUnavailable)
		return
	}

	h.events.Publish(events.Event{Type: events.ProjectActionPending, ProjectID: id, Data: "updating-repo"})

	go func() {
		defer func() {
			if rv := recover(); rv != nil {
				log.Printf("PANIC in handleUpdateRepos for %s: %v", id, rv)
				project.Status = "error"
				_ = h.store.Update(project)
				h.events.Publish(events.Event{Type: events.ProjectStatusChanged, ProjectID: id, Data: project})
			}
		}()

		gitCtx, gitCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer gitCancel()

		addonsDir := h.addonsHostDir(gitCtx, project.ID, project.GitRepoURL, project.GitRepoBranch)
		if addonsDir == "" {
			log.Printf("Project %s: repo pull failed (addonsHostDir returned empty)", id)
			status, _ := h.dockerManager.GetProjectStatus(context.Background(), project.ID)
			project.Status = status
			_ = h.store.Update(project)
			h.events.Publish(events.Event{Type: events.ProjectStatusChanged, ProjectID: id, Data: project})
			return
		}

		// Also pull enterprise and design-themes repos if enabled
		h.enterpriseHostDir(gitCtx, project.ID, project.OdooVersion, project.EnterpriseEnabled)
		h.designThemesHostDir(gitCtx, project.ID, project.OdooVersion, project.DesignThemesEnabled)

		// Check odoo.conf for dev mode (all or reload)
		needsRestart := true
		confContent, err := h.dockerManager.ReadOdooConfig(context.Background(), project.ID)
		if err == nil {
			for _, line := range strings.Split(string(confContent), "\n") {
				trimmed := strings.TrimSpace(line)
				lower := strings.ToLower(trimmed)
				if strings.HasPrefix(lower, "dev") && !strings.HasPrefix(lower, "dev_") {
					parts := strings.SplitN(lower, "=", 2)
					if len(parts) == 2 {
						val := strings.TrimSpace(parts[1])
						if strings.Contains(val, "all") || strings.Contains(val, "reload") {
							needsRestart = false
							log.Printf("Project %s: dev mode detected (%s), skipping restart", id, val)
							break
						}
					}
				}
			}
		}

		if needsRestart {
			log.Printf("Project %s: restarting Odoo container after code update...", id)
			if err := h.dockerManager.RestartOdooContainer(context.Background(), project.ID); err != nil {
				log.Printf("Project %s: restart failed: %v", id, err)
			}
		}

		status, _ := h.dockerManager.GetProjectStatus(context.Background(), project.ID)
		project.Status = status
		_ = h.store.Update(project)
		log.Printf("Project %s: repos update complete (status=%s, restarted=%v)", id, status, needsRestart)
		h.events.Publish(events.Event{Type: events.ProjectStatusChanged, ProjectID: id, Data: project})
	}()

	w.WriteHeader(http.StatusAccepted)
}

// handleRestartOdoo restarts only the Odoo container for a project.
// POST /api/projects/{id}/restart-odoo
func (h *Handler) handleRestartOdoo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.PathValue("id")
	project, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	if h.dockerManager == nil {
		http.Error(w, "Docker not available", http.StatusServiceUnavailable)
		return
	}

	go func() {
		defer func() {
			if rv := recover(); rv != nil {
				log.Printf("PANIC in handleRestartOdoo for %s: %v", id, rv)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		if err := h.dockerManager.RestartOdooContainer(ctx, project.ID); err != nil {
			log.Printf("Project %s: restart failed: %v", id, err)
		}

		status, _ := h.dockerManager.GetProjectStatus(context.Background(), id)
		project.Status = status
		_ = h.store.Update(project)
		h.events.Publish(events.Event{Type: events.ProjectStatusChanged, ProjectID: id, Data: project})
	}()

	w.WriteHeader(http.StatusAccepted)
}
