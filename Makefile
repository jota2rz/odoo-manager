.PHONY: help install build run dev clean test templ deps

# Default target
help:
	@echo "Odoo Manager - Available commands:"
	@echo "  make install    - Install dependencies and tools"
	@echo "  make deps       - Download Go dependencies"
	@echo "  make templ      - Generate Templ templates"
	@echo "  make build      - Build the application"
	@echo "  make run        - Run the application"
	@echo "  make dev        - Run in development mode with auto-reload"
	@echo "  make clean      - Clean build artifacts"
	@echo "  make test       - Run tests"

# Install required tools
install:
	@echo "Installing required tools..."
	go install github.com/a-h/templ/cmd/templ@latest
	@echo "Tools installed successfully!"

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	go mod download
	go mod tidy
	@echo "Dependencies downloaded!"

# Generate Templ templates
templ:
	@echo "Generating Templ templates..."
	@which templ > /dev/null || (echo "Templ not found. Installing..." && go install github.com/a-h/templ/cmd/templ@latest)
	@$(HOME)/go/bin/templ generate || templ generate
	@echo "Templates generated!"

# Build the application
build: templ
	@echo "Building application..."
	go build -o odoo-manager ./cmd/odoo-manager
	@echo "Build complete! Binary: ./odoo-manager"

# Run the application
run: build
	@echo "Starting Odoo Manager..."
	./odoo-manager

# Development mode with file watching (requires air)
dev:
	@echo "Starting development server..."
	@which air > /dev/null || (echo "Installing air..." && go install github.com/cosmtrek/air@latest)
	air

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -f odoo-manager
	rm -f templates/*_templ.go
	rm -rf data/
	@echo "Clean complete!"

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...
	templ fmt .

# Lint code
lint:
	@echo "Linting code..."
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run

# Initialize project (first time setup)
init: install deps templ
	@echo "Project initialized! Run 'make build' to build the application."
