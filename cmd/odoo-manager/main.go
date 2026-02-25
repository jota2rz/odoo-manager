package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jota2rz/odoo-manager/internal/audit"
	"github.com/jota2rz/odoo-manager/internal/events"
	"github.com/jota2rz/odoo-manager/internal/gitops"
	"github.com/jota2rz/odoo-manager/internal/handlers"
	"github.com/jota2rz/odoo-manager/internal/store"
)

// Version is set at build time via -ldflags; falls back to "dev".
var Version = "dev"

const (
	defaultPort = "8080"
)

//go:embed static
var staticFiles embed.FS

func main() {
	// Initialize project store
	projectStore, err := store.NewProjectStore("data/odoo-manager.db")
	if err != nil {
		log.Fatalf("Failed to initialize project store: %v", err)
	}
	defer projectStore.Close()

	// Ensure git CLI is available (download portable MinGit if needed)
	gitAvailable := true
	if err := gitops.EnsureGit(); err != nil {
		log.Printf("WARNING: %v — git repo features (custom addons, Enterprise, Design Themes) will not work", err)
		gitAvailable = false
	}

	// Reset any projects stuck in transient statuses from a previous session
	if n, err := projectStore.ReconcileStaleStatuses(); err != nil {
		log.Printf("Warning: failed to reconcile stale statuses: %v", err)
	} else if n > 0 {
		log.Printf("Reconciled %d project(s) stuck in transient status → error", n)
	}

	// Validate stored GitHub PAT token at startup
	if pat := projectStore.GetSetting("github_pat"); pat != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := gitops.ValidateToken(ctx, pat); err != nil {
			log.Printf("WARNING: Stored GitHub PAT is invalid: %v", err)
			_ = projectStore.SetSetting("github_pat_valid", "false")
		} else {
			log.Printf("GitHub PAT token validated successfully")
			_ = projectStore.SetSetting("github_pat_valid", "true")
		}
		cancel()
	} else {
		_ = projectStore.SetSetting("github_pat_valid", "")
	}

	// Setup static file server
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("Failed to setup static files: %v", err)
	}
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))

	// Create event hub for real-time SSE broadcasts
	eventHub := events.NewHub()

	// Initialize audit logger
	auditLogger, err := audit.NewLogger("data/audit.log")
	if err != nil {
		log.Fatalf("Failed to initialize audit logger: %v", err)
	}
	defer auditLogger.Close()

	// Create handler with dependencies
	handler := handlers.NewHandler(projectStore, staticHandler, eventHub, Version, auditLogger, gitAvailable)

	// Start background Docker health check
	healthCtx, healthCancel := context.WithCancel(context.Background())
	defer healthCancel()
	handler.StartDockerHealthCheck(healthCtx)

	// Setup HTTP routes
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Get port from environment or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	// Create server
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Printf("Starting Odoo Manager on http://localhost:%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
