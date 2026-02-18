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

	"github.com/jota2rz/odoo-manager/internal/handlers"
	"github.com/jota2rz/odoo-manager/internal/store"
)

const (
	defaultPort = "8080"
)

//go:embed static
var staticFiles embed.FS

func main() {
	// Initialize project store
	projectStore, err := store.NewProjectStore("data/projects.json")
	if err != nil {
		log.Fatalf("Failed to initialize project store: %v", err)
	}

	// Setup static file server
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("Failed to setup static files: %v", err)
	}
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))

	// Create handler with dependencies
	handler := handlers.NewHandler(projectStore, staticHandler)

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
