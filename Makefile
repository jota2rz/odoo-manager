.PHONY: help install build run dev clean test templ deps tailwind ensure-tailwind

# Default target
help:
	@echo "Odoo Manager - Available commands:"
	@echo "  make install    - Install dependencies and tools"
	@echo "  make deps       - Download Go dependencies"
	@echo "  make templ      - Generate Templ templates"
	@echo "  make tailwind   - Build Tailwind CSS"
	@echo "  make build      - Build the application"
	@echo "  make run        - Run the application"
	@echo "  make dev        - Run in development mode with auto-reload"
	@echo "  make clean      - Clean build artifacts"
	@echo "  make test       - Run tests"

# Install required tools
install:
	@echo "Installing required tools..."
	go install github.com/a-h/templ/cmd/templ@latest
	@$(MAKE) ensure-tailwind
	@echo "Tools installed successfully!"

# Download Tailwind CSS standalone CLI if not system-installed
ensure-tailwind:
	@if command -v tailwindcss > /dev/null 2>&1; then \
		echo "Tailwind CSS found (system-installed)"; \
	elif [ -f ./bin/tailwindcss ]; then \
		echo "Tailwind CSS found (standalone in bin/)"; \
	else \
		echo "Downloading Tailwind CSS standalone CLI..." && \
		mkdir -p bin && \
		OS=$$(uname -s | tr '[:upper:]' '[:lower:]') && \
		ARCH=$$(uname -m) && \
		if [ "$$ARCH" = "x86_64" ]; then ARCH="x64"; elif [ "$$ARCH" = "aarch64" ]; then ARCH="arm64"; fi && \
		curl -sLo bin/tailwindcss https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-$$OS-$$ARCH && \
		chmod +x bin/tailwindcss && \
		echo "Tailwind CSS standalone CLI downloaded to bin/"; \
	fi

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

# Resolve Tailwind CLI: system-installed or standalone in bin/
TAILWINDCSS := $(shell command -v tailwindcss 2>/dev/null || echo ./bin/tailwindcss)

# Build Tailwind CSS
tailwind:
	@echo "Building Tailwind CSS..."
	@$(MAKE) ensure-tailwind
	$(TAILWINDCSS) -i src/css/input.css -o cmd/odoo-manager/static/css/style.css --minify
	@echo "Tailwind CSS built!"

# Build the application
build: templ tailwind
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
	rm -rf bin/
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
init: install deps templ tailwind
	@echo "Project initialized! Run 'make build' to build the application."
